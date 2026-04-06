package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	ententity "github.com/cloudreve/Cloudreve/v4/ent/entity"
	entnode "github.com/cloudreve/Cloudreve/v4/ent/node"
	taskmodel "github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	tikaextractor "github.com/cloudreve/Cloudreve/v4/pkg/searcher/extractor"
	appsetting "github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	"github.com/gofrs/uuid"
)

const (
	smokeUserID = 1
	esEndpoint  = "http://127.0.0.1:9200"
)

var esIndex = "cloudreve_files"
var smokePreferredContentNodeID int

type esAttachment struct {
	ID                 string `json:"id"`
	ParentID           string `json:"parent_id"`
	ParentAttachmentID string `json:"parent_attachment_id"`
	Depth              int    `json:"depth"`
	Type               string `json:"type"`
	Name               string `json:"name"`
	Path               string `json:"path"`
	Content            string `json:"content"`
}

type esDoc struct {
	Found  bool `json:"found"`
	Source struct {
		FileID      int            `json:"file_id"`
		PathText    string         `json:"path_text"`
		Content     string         `json:"content"`
		FileName    string         `json:"file_name"`
		OwnerID     int            `json:"owner_id"`
		EntityID    int            `json:"entity_id"`
		ParentID    int            `json:"parent_id"`
		UpdatedAt   string         `json:"updated_at"`
		Attachments []esAttachment `json:"attachments"`
	} `json:"_source"`
}

type smokeFullTextIndexTaskItem struct {
	Uri      *fs.URI `json:"uri,omitempty"`
	EntityID int     `json:"entity_id,omitempty"`
	FileID   int     `json:"file_id"`
	OwnerID  int     `json:"owner_id,omitempty"`
}

type smokeFullTextIndexTaskState struct {
	Uri      *fs.URI                      `json:"uri,omitempty"`
	EntityID int                          `json:"entity_id,omitempty"`
	FileID   int                          `json:"file_id,omitempty"`
	OwnerID  int                          `json:"owner_id,omitempty"`
	FileIDs  []int                        `json:"file_ids,omitempty"`
	Files    []smokeFullTextIndexTaskItem `json:"files,omitempty"`
	Phase    string                       `json:"phase,omitempty"`
	NodeID   int                          `json:"node_id,omitempty"`
	SlaveID  int                          `json:"slave_id,omitempty"`
	Active   *smokeFullTextIndexTaskItem  `json:"active,omitempty"`
}

func main() {
	util.UseWorkingDir = true
	logger := logging.NewConsoleLogger(logging.LevelDebug)
	dep := dependency.NewDependency(
		dependency.WithConfigPath(".tmp/fts_real_smoke.ini"),
		dependency.WithLogger(logger),
		dependency.WithRequiredDbVersion(constants.BackendVersion),
	)
	defer dep.Shutdown(context.Background())

	baseCtx := context.WithValue(context.Background(), dependency.DepCtx{}, dep)
	baseCtx = context.WithValue(baseCtx, inventory.LoadTaskUser{}, true)

	user, err := dep.UserClient().GetLoginUserByID(baseCtx, smokeUserID)
	must(err, "load login user")
	baseCtx = context.WithValue(baseCtx, inventory.UserCtx{}, user)
	baseCtx = context.WithValue(baseCtx, inventory.UserIDCtx{}, user.ID)
	must(ensureSmokeFTSSettings(baseCtx, dep), "prepare smoke fts settings")
	if node, err := ensureSmokeContentProcessingSlave(baseCtx, dep); err != nil {
		panic(fmt.Sprintf("prepare slave content processing node: %v", err))
	} else if node != nil {
		smokePreferredContentNodeID = node.ID
		fmt.Printf("content processing slave enabled node_id=%d server=%s\n", node.ID, node.Server)
	}
	reloadCtx := context.WithValue(baseCtx, dependency.ReloadCtx{}, true)
	if strings.TrimSpace(os.Getenv("FTS_SMOKE_EXTERNAL_MASTER")) != "" {
		contentQueue := dep.ContentProcessingQueue(reloadCtx)
		contentQueue.Start()
		defer contentQueue.Shutdown()
	}
	fmt.Printf("fts_enabled=%t index_type=%s extractor_type=%s es_endpoint=%s es_index=%s\n",
		dep.SettingProvider().FTSEnabled(reloadCtx),
		dep.SettingProvider().FTSIndexType(reloadCtx),
		dep.SettingProvider().FTSExtractorType(reloadCtx),
		dep.SettingProvider().FTSIndexElasticsearch(reloadCtx).Endpoint,
		dep.SettingProvider().FTSIndexElasticsearch(reloadCtx).Index,
	)
	esIndex = dep.SettingProvider().FTSIndexElasticsearch(reloadCtx).Index
	fmt.Printf("search_indexer=%T text_extractor=%T text_max=%d text_exts=%d\n",
		dep.SearchIndexer(reloadCtx),
		dep.TextExtractor(reloadCtx),
		dep.TextExtractor(reloadCtx).MaxFileSize(),
		len(dep.TextExtractor(reloadCtx).Exts()),
	)
	must(dep.SearchIndexer(reloadCtx).EnsureIndex(reloadCtx), "ensure es index")

	fm := manager.NewFileManager(dep, user)
	defer fm.Recycle()

	suffix := time.Now().Format("20060102_150405")
	root := mustURI(fmt.Sprintf("cloudreve://my/__fts_real_smoke_%s", suffix))
	aDir := root.Join("a")
	bDir := root.Join("b")
	nestedDir := aDir.Join("nested")
	docsDir := root.Join("docs")
	archivesDir := root.Join("archives")
	batchSrcDir := root.Join("batch-src")
	batchDstDir := root.Join("batch-dst")

	fmt.Printf("base=%s\n", root.String())
	must(createFolder(baseCtx, fm, root), "create root folder")
	must(createFolder(baseCtx, fm, aDir), "create a folder")
	must(createFolder(baseCtx, fm, bDir), "create b folder")
	must(createFolder(baseCtx, fm, nestedDir), "create nested folder")
	must(createFolder(baseCtx, fm, docsDir), "create docs folder")
	must(createFolder(baseCtx, fm, archivesDir), "create archives folder")
	must(createFolder(baseCtx, fm, batchSrcDir), "create batch src folder")
	must(createFolder(baseCtx, fm, batchDstDir), "create batch dst folder")

	rootFile, rootCID, err := uploadText(baseCtx, fm, aDir.Join("root.txt"), "root smoke content "+suffix)
	must(err, "upload root file")
	rootFileID := rootFile.ID()
	rootRaw := logSource(baseCtx, fm, "root source", rootFile)
	logTikaProbe(baseCtx, dep, fm, rootFile, rootRaw, "root tika")
	_ = rootCID
	must(drainFTSTasksForFiles(baseCtx, dep, user, rootFileID), "drain upload(root) tasks")
	must(refreshES(), "refresh ES after root upload")
	logBuiltDoc(baseCtx, dep, user, rootFileID, "upload root build")
	rootDoc := ensureDocSeeded(baseCtx, dep, user, rootFileID, "upload root")
	fmt.Printf("upload root file_id=%d path=%s content_len=%d\n", rootFileID, rootDoc.Source.PathText, len(strings.TrimSpace(rootDoc.Source.Content)))
	assertPGSidecarStored(baseCtx, dep, rootFileID, "root.txt")

	childFile, childCID, err := uploadText(baseCtx, fm, nestedDir.Join("child.txt"), "child smoke content "+suffix)
	must(err, "upload child file")
	childFileID := childFile.ID()
	childRaw := logSource(baseCtx, fm, "child source", childFile)
	logTikaProbe(baseCtx, dep, fm, childFile, childRaw, "child tika")
	_ = childCID
	must(drainFTSTasksForFiles(baseCtx, dep, user, childFileID), "drain upload(child) tasks")
	must(refreshES(), "refresh ES after child upload")
	logBuiltDoc(baseCtx, dep, user, childFileID, "upload child build")
	childDoc := ensureDocSeeded(baseCtx, dep, user, childFileID, "upload child")
	fmt.Printf("upload child file_id=%d path=%s content_len=%d\n", childFileID, childDoc.Source.PathText, len(strings.TrimSpace(childDoc.Source.Content)))
	assertPGSidecarStored(baseCtx, dep, childFileID, "child.txt")

	docxBytes := buildMinimalDocx("docx smoke content " + suffix)
	docxFile, _, err := uploadBytes(baseCtx, fm, docsDir.Join("sample.docx"), docxBytes)
	must(err, "upload docx file")
	docxFileID := docxFile.ID()
	must(drainFTSTasksForFiles(baseCtx, dep, user, docxFileID), "drain docx upload tasks")
	must(refreshES(), "refresh ES after docx upload")
	assertDocContentContains(docxFileID, "docx smoke content "+suffix, "docx upload")
	assertPGSidecarStored(baseCtx, dep, docxFileID, "sample.docx")
	assertDocHasAttachment(docxFileID, "image1.png", "docx upload attachment")
	logBuiltDoc(baseCtx, dep, user, docxFileID, "upload docx build")

	pdfBytes := buildMinimalPDFWithEmbeddedFile(
		"pdf smoke content "+suffix,
		"attached.txt",
		"pdf embedded attachment "+suffix,
	)
	pdfFile, _, err := uploadBytes(baseCtx, fm, docsDir.Join("sample.pdf"), pdfBytes)
	must(err, "upload pdf file")
	pdfFileID := pdfFile.ID()
	pdfRaw := logSource(baseCtx, fm, "pdf source", pdfFile)
	logTikaProbe(baseCtx, dep, fm, pdfFile, pdfRaw, "pdf tika")
	must(drainFTSTasksForFiles(baseCtx, dep, user, pdfFileID), "drain pdf upload tasks")
	must(refreshES(), "refresh ES after pdf upload")
	assertDocContentContains(pdfFileID, "pdf smoke content "+suffix, "pdf upload")
	assertPGSidecarStored(baseCtx, dep, pdfFileID, "sample.pdf")
	assertDocHasImageAttachment(pdfFileID, "pdf upload image attachment")
	assertDocHasAttachment(pdfFileID, "attached.txt", "pdf upload embedded attachment")
	logBuiltDoc(baseCtx, dep, user, pdfFileID, "upload pdf build")

	smokeWinmailUpload(baseCtx, dep, fm, user, docsDir, filepath.Join(".tmp", "testdata", "testWINMAIL.dat"))
	emlSubject := "EML smoke subject " + suffix
	emlBody := "EML smoke body " + suffix
	emlAttachment := "EML attachment payload " + suffix
	emlBytes := buildSmokeEML(emlSubject, emlBody, "eml-attachment.txt", emlAttachment)
	smokeEMLUpload(baseCtx, dep, fm, user, docsDir, "sample.eml", emlBytes, emlBody, emlAttachment)
	smokeMboxUpload(baseCtx, dep, fm, user, docsDir, "sample.mbox", buildSmokeMbox(emlBytes), emlBody, emlAttachment)
	boundaryText := "中文边界测试 你好，世界\n第二行：压缩包中文名 " + suffix
	smokeBoundaryTextUpload(baseCtx, dep, fm, user, docsDir, "utf8-中文.txt", []byte(boundaryText), boundaryText, true)
	smokeBoundaryTextUpload(baseCtx, dep, fm, user, docsDir, "gbk-中文.txt", encodeTextWithIconv("GBK", boundaryText), boundaryText, true)
	smokeBoundaryTextUpload(baseCtx, dep, fm, user, docsDir, "gb2312-中文.txt", encodeTextWithIconv("GB2312", boundaryText), boundaryText, true)
	smokeBoundaryArchiveUpload(baseCtx, dep, fm, user, archivesDir, "utf8-中文.zip", buildUTF8ChineseNestedZip(boundaryText), "utf8 chinese zip", boundaryText, []string{"外层说明.txt", "内层.zip", "中文内容.txt"}, true)
	smokeBoundaryArchiveUpload(baseCtx, dep, fm, user, archivesDir, "gbk-中文.zip", buildEncodedChineseNestedZip("GBK", boundaryText), "gbk chinese zip", boundaryText, []string{"外层说明.txt", "内层.zip", "中文内容.txt"}, false)
	smokeBoundaryArchiveUpload(baseCtx, dep, fm, user, archivesDir, "gb2312-中文.zip", buildEncodedChineseNestedZip("GB2312", boundaryText), "gb2312 chinese zip", boundaryText, []string{"外层说明.txt", "内层.zip", "中文内容.txt"}, false)
	if raw, ok := buildChineseNestedCommandArchive("7z", "utf8-中文.7z", []string{"a", "-bd", "-y"}, boundaryText); ok {
		smokeBoundaryArchiveUpload(baseCtx, dep, fm, user, archivesDir, "utf8-中文.7z", raw, "utf8 chinese 7z", boundaryText, []string{"外层说明.txt", "内层.zip", "中文内容.txt"}, true)
	}
	if raw, ok := buildChineseNestedCommandArchive("rar", "utf8-中文.rar", []string{"a", "-ma4", "-idq"}, boundaryText); ok {
		smokeBoundaryArchiveUpload(baseCtx, dep, fm, user, archivesDir, "utf8-中文.rar", raw, "utf8 chinese rar", boundaryText, []string{"外层说明.txt", "内层.zip", "中文内容.txt"}, false)
	}

	rootRenameCtx, rootRenameCID := withCorrelation(baseCtx)
	_, err = fm.Rename(rootRenameCtx, aDir.Join("root.txt"), "root-renamed.txt")
	must(err, "rename root file")
	_ = rootRenameCID
	must(drainFTSTasksForFiles(baseCtx, dep, user, rootFileID), "drain rename tasks")
	must(refreshES(), "refresh ES after rename")
	rootRenamedURI := aDir.Join("root-renamed.txt")
	assertDocPath(rootFileID, rootRenamedURI.String(), "rename")

	rootMoveCtx, rootMoveCID := withCorrelation(baseCtx)
	must(fm.MoveOrCopy(rootMoveCtx, []*fs.URI{rootRenamedURI}, bDir, false), "move root file")
	_ = rootMoveCID
	must(drainFTSTasksForFiles(baseCtx, dep, user, rootFileID), "drain move(file) tasks")
	must(refreshES(), "refresh ES after file move")
	rootMovedURI := bDir.Join("root-renamed.txt")
	assertDocPath(rootFileID, rootMovedURI.String(), "move file")

	rootCopyCtx, rootCopyCID := withCorrelation(baseCtx)
	must(fm.MoveOrCopy(rootCopyCtx, []*fs.URI{rootMovedURI}, aDir, true), "copy root file")
	_ = rootCopyCID
	copiedFile, err := fm.Get(baseCtx, aDir.Join("root-renamed.txt"))
	must(err, "load copied file")
	copiedFileID := copiedFile.ID()
	must(drainFTSTasksForFiles(baseCtx, dep, user, copiedFileID), "drain copy tasks")
	must(refreshES(), "refresh ES after copy")
	assertDocPath(copiedFileID, aDir.Join("root-renamed.txt").String(), "copy file")
	assertPGSidecarStored(baseCtx, dep, copiedFileID, "copied root-renamed.txt")
	fmt.Printf("copy file original=%d copied=%d\n", rootFileID, copiedFileID)

	nestedMoveCtx, nestedMoveCID := withCorrelation(baseCtx)
	must(fm.MoveOrCopy(nestedMoveCtx, []*fs.URI{nestedDir}, bDir, false), "move nested folder")
	_ = nestedMoveCID
	must(drainFTSTasksForFiles(baseCtx, dep, user, childFileID), "drain move(folder) tasks")
	must(refreshES(), "refresh ES after folder move")
	movedChildURI := bDir.Join("nested").Join("child.txt")
	assertDocPath(childFileID, movedChildURI.String(), "move folder descendant")

	nestedRenameCtx, nestedRenameCID := withCorrelation(baseCtx)
	_, err = fm.Rename(nestedRenameCtx, bDir.Join("nested"), "nested-renamed")
	must(err, "rename nested folder")
	_ = nestedRenameCID
	must(drainFTSTasksForFiles(baseCtx, dep, user, childFileID), "drain rename(folder) tasks")
	must(refreshES(), "refresh ES after folder rename")
	renamedChildURI := bDir.Join("nested-renamed").Join("child.txt")
	assertDocPath(childFileID, renamedChildURI.String(), "rename folder descendant")

	folderCopyCtx, folderCopyCID := withCorrelation(baseCtx)
	must(fm.MoveOrCopy(folderCopyCtx, []*fs.URI{bDir.Join("nested-renamed")}, docsDir, true), "copy nested folder")
	_ = folderCopyCID
	copiedChild, err := fm.Get(baseCtx, docsDir.Join("nested-renamed").Join("child.txt"))
	must(err, "load copied folder child")
	copiedChildID := copiedChild.ID()
	must(drainFTSTasksForFiles(baseCtx, dep, user, copiedChildID), "drain copy(folder) tasks")
	must(refreshES(), "refresh ES after folder copy")
	assertDocPath(copiedChildID, docsDir.Join("nested-renamed").Join("child.txt").String(), "copy folder descendant")
	assertDocContentContains(copiedChildID, "child smoke content "+suffix, "copy folder descendant")
	assertPGSidecarStored(baseCtx, dep, copiedChildID, "copied child.txt")

		archiveFileIDs := []int{
			smokeArchiveUpload(baseCtx, dep, fm, user, archivesDir, "nested.zip", buildNestedZip(
				"archive outer content "+suffix,
				"archive inner content "+suffix,
			), "zip"),
			smokeArchiveUpload(baseCtx, dep, fm, user, archivesDir, "nested.tar", buildNestedTar(
				"tar outer content "+suffix,
				"tar inner content "+suffix,
			), "tar"),
			smokeArchiveUpload(baseCtx, dep, fm, user, archivesDir, "nested.jar", buildNestedJar(
				"jar outer content "+suffix,
				"jar inner content "+suffix,
			), "jar"),
			smokeArchiveUpload(baseCtx, dep, fm, user, archivesDir, "nested.war", buildNestedJar(
				"war outer content "+suffix,
				"war inner content "+suffix,
			), "war"),
			smokeArchiveUpload(baseCtx, dep, fm, user, archivesDir, "nested.ear", buildNestedJar(
				"ear outer content "+suffix,
				"ear inner content "+suffix,
			), "ear"),
		}
	archiveFileIDs = append(archiveFileIDs, smokeCompressedTarArchiveUpload(baseCtx, dep, fm, user, archivesDir, "nested.tgz", buildNestedTGZ(
		"tgz outer content "+suffix,
		"tgz inner content "+suffix,
	), "tgz", "nested.tar"))
	if raw, ok := buildNestedTBZ2(
		"tbz2 outer content "+suffix,
		"tbz2 inner content "+suffix,
	); ok {
		archiveFileIDs = append(archiveFileIDs, smokeCompressedTarArchiveUpload(baseCtx, dep, fm, user, archivesDir, "nested.tbz2", raw, "tbz2", "nested.tar"))
	} else {
		fmt.Printf("skip tbz2 smoke: bzip2 command unavailable\n")
	}
	if raw, ok := buildNestedTXZ(
		"txz outer content "+suffix,
		"txz inner content "+suffix,
	); ok {
		archiveFileIDs = append(archiveFileIDs, smokeCompressedTarArchiveUpload(baseCtx, dep, fm, user, archivesDir, "nested.txz", raw, "txz", "nested.txz"))
	} else {
		fmt.Printf("skip txz smoke: xz command unavailable\n")
	}
	if raw, ok := buildNestedLZMA(
		"lzma outer content "+suffix,
		"lzma inner content "+suffix,
	); ok {
		archiveFileIDs = append(archiveFileIDs, smokeCompressedTarArchiveUpload(baseCtx, dep, fm, user, archivesDir, "nested.lzma", raw, "lzma", "nested.lzma"))
	} else {
		fmt.Printf("skip lzma smoke: xz command unavailable\n")
	}
	if raw, ok := buildNestedAr(
		"ar outer content "+suffix,
		"ar inner content "+suffix,
	); ok {
		archiveFileIDs = append(archiveFileIDs, smokeArchiveUpload(baseCtx, dep, fm, user, archivesDir, "nested.ar", raw, "ar"))
	} else {
		fmt.Printf("skip ar smoke: ar command unavailable\n")
	}
	if raw, ok := buildNestedCPIO(
		"cpio outer content "+suffix,
		"cpio inner content "+suffix,
	); ok {
		archiveFileIDs = append(archiveFileIDs, smokeArchiveUpload(baseCtx, dep, fm, user, archivesDir, "nested.cpio", raw, "cpio"))
	} else {
		fmt.Printf("skip cpio smoke: cpio command unavailable\n")
	}
	if raw, ok := buildNested7z(
		"7z outer content "+suffix,
		"7z inner content "+suffix,
	); ok {
		archiveFileIDs = append(archiveFileIDs, smokeArchiveUpload(baseCtx, dep, fm, user, archivesDir, "nested.7z", raw, "7z"))
	} else {
		fmt.Printf("skip 7z smoke: 7z command unavailable\n")
	}
	if raw, ok := buildNestedRar(
		"rar outer content "+suffix,
		"rar inner content "+suffix,
	); ok {
		archiveFileIDs = append(archiveFileIDs, smokeRarArchiveUpload(baseCtx, dep, fm, user, archivesDir, "nested.rar", raw, "rar"))
	} else {
		fmt.Printf("skip rar smoke: rar command unavailable\n")
	}

	sidecarDisabledFileID, err := smokeSidecarToggle(baseCtx, dep, fm, user, docsDir, suffix)
	must(err, "run sidecar toggle smoke")
	fmt.Printf("sidecar toggle ok file_id=%d\n", sidecarDisabledFileID)

	batchFiles := make([]fs.File, 0, 16)
	batchFileIDs := make([]int, 0, 16)
	batchSourceURIs := make([]*fs.URI, 0, 16)
	for i := 0; i < 16; i++ {
		name := fmt.Sprintf("batch-%02d.txt", i)
		content := fmt.Sprintf("batch smoke content %02d %s", i, suffix)
		file, _, err := uploadText(baseCtx, fm, batchSrcDir.Join(name), content)
		must(err, "upload batch file "+name)
		batchFiles = append(batchFiles, file)
		batchFileIDs = append(batchFileIDs, file.ID())
		batchSourceURIs = append(batchSourceURIs, batchSrcDir.Join(name))
	}
	must(drainFTSTasksForFiles(baseCtx, dep, user, batchFileIDs...), "drain batch upload tasks")
	must(refreshES(), "refresh ES after batch upload")
	for i, file := range batchFiles {
		assertDocPath(file.ID(), batchSrcDir.Join(fmt.Sprintf("batch-%02d.txt", i)).String(), "batch upload")
	}

	batchMoveCtx, batchMoveCID := withCorrelation(baseCtx)
	must(fm.MoveOrCopy(batchMoveCtx, batchSourceURIs, batchDstDir, false), "batch move files")
	_ = batchMoveCID
	must(drainFTSTasksForFiles(baseCtx, dep, user, batchFileIDs...), "drain batch move tasks")
	must(refreshES(), "refresh ES after batch move")
	for i, fileID := range batchFileIDs {
		assertDocPath(fileID, batchDstDir.Join(fmt.Sprintf("batch-%02d.txt", i)).String(), "batch move")
	}

	batchRenameCtx, batchRenameCID := withCorrelation(baseCtx)
	_, err = fm.Rename(batchRenameCtx, batchDstDir, "batch-final")
	must(err, "rename batch folder")
	_ = batchRenameCID
	must(drainFTSTasksForFiles(baseCtx, dep, user, batchFileIDs...), "drain batch rename(folder) tasks")
	must(refreshES(), "refresh ES after batch folder rename")
	batchFinalDir := root.Join("batch-final")
	for i, fileID := range batchFileIDs {
		assertDocPath(fileID, batchFinalDir.Join(fmt.Sprintf("batch-%02d.txt", i)).String(), "batch folder rename")
	}

	deleteBatchURIs := make([]*fs.URI, 0, 4)
	deleteBatchIDs := make([]int, 0, 4)
	for i := 0; i < 4; i++ {
		deleteBatchURIs = append(deleteBatchURIs, batchFinalDir.Join(fmt.Sprintf("batch-%02d.txt", i)))
		deleteBatchIDs = append(deleteBatchIDs, batchFileIDs[i])
	}
	deleteBatchCtx, deleteBatchCID := withCorrelation(baseCtx)
	must(fm.Delete(deleteBatchCtx, deleteBatchURIs), "batch soft delete files")
	_ = deleteBatchCID
	must(drainFTSTasksForFiles(baseCtx, dep, user, deleteBatchIDs...), "drain batch soft delete tasks")
	must(refreshES(), "refresh ES after batch soft delete")
	for _, fileID := range deleteBatchIDs {
		assertDocMissing(fileID, "batch soft delete")
	}

	trashedBatchFile, err := fm.TraverseFile(baseCtx, deleteBatchIDs[0])
	must(err, "resolve trashed batch file")
	restoreBatchCtx, restoreBatchCID := withCorrelation(baseCtx)
	must(fm.Restore(restoreBatchCtx, trashedBatchFile.Uri(true)), "restore batch file")
	_ = restoreBatchCID
	must(drainFTSTasksForFiles(baseCtx, dep, user, deleteBatchIDs[0]), "drain batch restore tasks")
	must(refreshES(), "refresh ES after batch restore")
	restoredBatchFile, err := fm.TraverseFile(baseCtx, deleteBatchIDs[0])
	must(err, "resolve restored batch file")
	assertDocPath(deleteBatchIDs[0], restoredBatchFile.Uri(true).String(), "batch restore")

	deleteCtx, deleteCID := withCorrelation(baseCtx)
	must(fm.Delete(deleteCtx, []*fs.URI{aDir.Join("root-renamed.txt")}), "soft delete copied file")
	_ = deleteCID
	must(drainFTSTasksForFiles(baseCtx, dep, user, copiedFileID), "drain soft delete tasks")
	must(refreshES(), "refresh ES after soft delete")
	assertDocMissing(copiedFileID, "soft delete")
	trashedCopy, err := fm.TraverseFile(baseCtx, copiedFileID)
	must(err, "resolve trashed copy file")
	fmt.Printf("soft delete copied_file=%d trash_path=%s\n", copiedFileID, trashedCopy.Uri(true).String())

	restoreCtx, restoreCID := withCorrelation(baseCtx)
	must(fm.Restore(restoreCtx, trashedCopy.Uri(true)), "restore copied file")
	_ = restoreCID
	must(drainFTSTasksForFiles(baseCtx, dep, user, copiedFileID), "drain restore tasks")
	must(refreshES(), "refresh ES after restore")
	restoredCopy, err := fm.TraverseFile(baseCtx, copiedFileID)
	must(err, "resolve restored copy file")
	assertDocPath(copiedFileID, restoredCopy.Uri(true).String(), "restore")

	hardDeleteCtx, hardDeleteCID := withCorrelation(baseCtx)
	must(fm.Delete(hardDeleteCtx, []*fs.URI{restoredCopy.Uri(true)}, fs.WithSysSkipSoftDelete(true)), "hard delete restored copy")
	_ = hardDeleteCID
	must(drainFTSTasksForFiles(baseCtx, dep, user, copiedFileID), "drain hard delete tasks")
	must(refreshES(), "refresh ES after hard delete")
	assertDocMissing(copiedFileID, "hard delete")

	cleanupCtx, cleanupCID := withCorrelation(baseCtx)
	cleanupErr := fm.Delete(cleanupCtx, []*fs.URI{root}, fs.WithSysSkipSoftDelete(true))
	if cleanupErr == nil {
		_ = cleanupCID
		cleanupErr = drainFTSTasksForFiles(baseCtx, dep, user, append(append([]int{
			rootFileID,
			childFileID,
			copiedChildID,
			copiedFileID,
			docxFileID,
			pdfFileID,
			sidecarDisabledFileID,
		}, archiveFileIDs...), batchFileIDs...)...)
	}
	if cleanupErr == nil {
		cleanupErr = refreshES()
	}
	if cleanupErr != nil {
		fmt.Fprintf(os.Stderr, "cleanup warning: %v\n", cleanupErr)
	} else {
		fmt.Printf("cleanup root=%s\n", root.String())
	}
}

func createFolder(ctx context.Context, fm manager.FileManager, uri *fs.URI) error {
	_, err := fm.Create(ctx, uri, types.FileTypeFolder)
	return err
}

func uploadText(ctx context.Context, fm manager.FileManager, uri *fs.URI, content string) (fs.File, uuid.UUID, error) {
	return uploadBytes(ctx, fm, uri, []byte(content))
}

func uploadBytes(ctx context.Context, fm manager.FileManager, uri *fs.URI, data []byte) (fs.File, uuid.UUID, error) {
	reader := bytes.NewReader(data)
	req := &fs.UploadRequest{
		Props: &fs.UploadProps{
			Uri:  uri,
			Size: int64(len(data)),
		},
		File:   io.NopCloser(reader),
		Seeker: reader,
	}

	opCtx, cid := withCorrelation(ctx)
	file, err := fm.Update(opCtx, req)
	return file, cid, err
}

func smokeArchiveUpload(
	ctx context.Context,
	dep dependency.Dep,
	fm manager.FileManager,
	user *ent.User,
	archivesDir *fs.URI,
	fileName string,
	data []byte,
	label string,
) int {
	fileID := smokeArchiveUploadBase(ctx, dep, fm, user, archivesDir, fileName, data, label)
	assertDocHasAttachment(fileID, "outer.txt", label+" upload attachment")
	assertDocAttachmentHierarchy(fileID, "inner.zip", "", 0, label+" archive root")
	assertDocAttachmentHierarchy(fileID, "nested.txt", "attachments/inner.zip", 1, label+" nested attachment")
	assertManifestHierarchy(ctx, fm, archivesDir.Join(fileName), "attachments/inner.zip/nested.txt", "attachments/inner.zip", 1, label+" manifest hierarchy")
	logBuiltDoc(ctx, dep, user, fileID, "upload "+label+" archive build")
	return fileID
}

func smokeCompressedTarArchiveUpload(
	ctx context.Context,
	dep dependency.Dep,
	fm manager.FileManager,
	user *ent.User,
	archivesDir *fs.URI,
	fileName string,
	data []byte,
	label string,
	rootAttachmentName string,
) int {
	fileID := smokeArchiveUploadBase(ctx, dep, fm, user, archivesDir, fileName, data, label)
	rootAttachmentID := "attachments/" + rootAttachmentName
	assertDocHasAttachment(fileID, rootAttachmentName, label+" compressed tar root")
	assertDocHasAttachment(fileID, "outer.txt", label+" upload attachment")
	assertDocAttachmentHierarchy(fileID, rootAttachmentName, "", 0, label+" outer container root")
	assertDocAttachmentHierarchy(fileID, "inner.zip", rootAttachmentID, 1, label+" archive root")
	assertDocAttachmentHierarchy(fileID, "nested.txt", rootAttachmentID+"/inner.zip", 2, label+" nested attachment")
	assertManifestHierarchy(ctx, fm, archivesDir.Join(fileName), rootAttachmentID+"/inner.zip/nested.txt", rootAttachmentID+"/inner.zip", 2, label+" manifest hierarchy")
	logBuiltDoc(ctx, dep, user, fileID, "upload "+label+" archive build")
	return fileID
}

func smokeRarArchiveUpload(
	ctx context.Context,
	dep dependency.Dep,
	fm manager.FileManager,
	user *ent.User,
	archivesDir *fs.URI,
	fileName string,
	data []byte,
	label string,
) int {
	fileID := smokeArchiveUploadBase(ctx, dep, fm, user, archivesDir, fileName, data, label)
	if docHasAttachment(fileID, "outer.txt") {
		assertDocHasAttachment(fileID, "outer.txt", label+" upload attachment")
		assertDocAttachmentHierarchy(fileID, "inner.zip", "", 0, label+" archive root")
		assertDocAttachmentHierarchy(fileID, "nested.txt", "attachments/inner.zip", 1, label+" nested attachment")
		assertManifestHierarchy(ctx, fm, archivesDir.Join(fileName), "attachments/inner.zip/nested.txt", "attachments/inner.zip", 1, label+" manifest hierarchy")
		logBuiltDoc(ctx, dep, user, fileID, "upload "+label+" archive build")
		return fileID
	}

	if os.Getenv("FTS_RAR_REQUIRED") == "1" {
		panic(fmt.Sprintf("%s recursive extraction required but outer.txt attachment was not extracted for file_id=%d", label, fileID))
	}

	doc := mustGetDoc(fileID)
	names := make([]string, 0, len(doc.Source.Attachments))
	for _, attachment := range doc.Source.Attachments {
		names = append(names, attachment.Name)
	}
	fmt.Printf("%s compatibility limitation file_id=%d attachments=%v content_excerpt=%q\n", label, fileID, names, previewText(doc.Source.Content))
	logBuiltDoc(ctx, dep, user, fileID, "upload "+label+" archive build")
	return fileID
}

func smokeArchiveUploadBase(
	ctx context.Context,
	dep dependency.Dep,
	fm manager.FileManager,
	user *ent.User,
	archivesDir *fs.URI,
	fileName string,
	data []byte,
	label string,
) int {
	file, _, err := uploadBytes(ctx, fm, archivesDir.Join(fileName), data)
	must(err, "upload "+label+" archive")
	fileID := file.ID()
	raw := logSource(ctx, fm, label+" archive source", file)
	logTikaProbe(ctx, dep, fm, file, raw, label+" archive tika")
	must(drainFTSTasksForFiles(ctx, dep, user, fileID), "drain "+label+" archive upload tasks")
	must(refreshES(), "refresh ES after "+label+" archive upload")
	assertPGSidecarStored(ctx, dep, fileID, fileName)
	return fileID
}

func smokeWinmailUpload(
	ctx context.Context,
	dep dependency.Dep,
	fm manager.FileManager,
	user *ent.User,
	docsDir *fs.URI,
	samplePath string,
) bool {
	raw, err := os.ReadFile(samplePath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("winmail smoke skip sample_missing=%s\n", samplePath)
			return false
		}
		panic(fmt.Sprintf("read winmail sample %s: %v", samplePath, err))
	}

	file, _, err := uploadBytes(ctx, fm, docsDir.Join("winmail.dat"), raw)
	must(err, "upload winmail.dat")
	fileID := file.ID()
	fileRaw := logSource(ctx, fm, "winmail source", file)
	logTikaProbe(ctx, dep, fm, file, fileRaw, "winmail tika")
	must(drainFTSTasksForFiles(ctx, dep, user, fileID), "drain winmail upload tasks")
	must(refreshES(), "refresh ES after winmail upload")

	assertDocContentContains(fileID, "Five files from Hell!", "winmail upload message body")
	assertDocContentContains(fileID, "The quick brown fox jumps over the lazy dog", "winmail upload embedded text")
	assertPGSidecarStored(ctx, dep, fileID, "winmail.dat")

	for _, name := range []string{
		"message.rtf",
		"quick.doc",
		"quick.html",
		"quick.pdf",
		"quick.txt",
		"quick.xml",
	} {
		assertDocHasAttachment(fileID, name, "winmail upload attachment")
		assertDocAttachmentHierarchy(fileID, name, "", 0, "winmail attachment root")
		assertManifestHierarchy(ctx, fm, docsDir.Join("winmail.dat"), "attachments/"+name, "", 0, "winmail manifest root")
	}

	logBuiltDoc(ctx, dep, user, fileID, "upload winmail build")
	return true
}

func smokeEMLUpload(
	ctx context.Context,
	dep dependency.Dep,
	fm manager.FileManager,
	user *ent.User,
	docsDir *fs.URI,
	fileName string,
	data []byte,
	wantBody string,
	wantAttachmentText string,
) int {
	file, _, err := uploadBytes(ctx, fm, docsDir.Join(fileName), data)
	must(err, "upload "+fileName)
	fileID := file.ID()
	fileRaw := logSource(ctx, fm, fileName+" source", file)
	logTikaProbe(ctx, dep, fm, file, fileRaw, fileName+" tika")
	must(drainFTSTasksForFiles(ctx, dep, user, fileID), "drain "+fileName+" upload tasks")
	must(refreshES(), "refresh ES after "+fileName+" upload")

	assertDocContentContains(fileID, wantBody, fileName+" body")
	assertDocContentContains(fileID, wantAttachmentText, fileName+" attachment text")
	assertPGSidecarStored(ctx, dep, fileID, fileName)
	assertDocHasAttachment(fileID, "eml-attachment.txt", fileName+" attachment")
	logBuiltDoc(ctx, dep, user, fileID, "upload "+fileName+" build")
	return fileID
}

func smokeMboxUpload(
	ctx context.Context,
	dep dependency.Dep,
	fm manager.FileManager,
	user *ent.User,
	docsDir *fs.URI,
	fileName string,
	data []byte,
	wantBody string,
	wantAttachmentText string,
) int {
	file, _, err := uploadBytes(ctx, fm, docsDir.Join(fileName), data)
	must(err, "upload "+fileName)
	fileID := file.ID()
	fileRaw := logSource(ctx, fm, fileName+" source", file)
	logTikaProbe(ctx, dep, fm, file, fileRaw, fileName+" tika")
	must(drainFTSTasksForFiles(ctx, dep, user, fileID), "drain "+fileName+" upload tasks")
	must(refreshES(), "refresh ES after "+fileName+" upload")

	assertDocContentContains(fileID, wantBody, fileName+" body")
	assertDocContentContains(fileID, wantAttachmentText, fileName+" attachment text")
	assertPGSidecarStored(ctx, dep, fileID, fileName)
	assertDocHasAttachment(fileID, "0.eml", fileName+" embedded mail root")
	assertDocAttachmentHierarchy(fileID, "0.eml", "", 0, fileName+" embedded mail root")
	logBuiltDoc(ctx, dep, user, fileID, "upload "+fileName+" build")
	return fileID
}

func smokeBoundaryTextUpload(
	ctx context.Context,
	dep dependency.Dep,
	fm manager.FileManager,
	user *ent.User,
	docsDir *fs.URI,
	fileName string,
	data []byte,
	wantText string,
	strict bool,
) int {
	file, _, err := uploadBytes(ctx, fm, docsDir.Join(fileName), data)
	must(err, "upload "+fileName)
	fileID := file.ID()
	fileRaw := logSource(ctx, fm, fileName+" source", file)
	logTikaProbe(ctx, dep, fm, file, fileRaw, fileName+" tika")
	must(drainFTSTasksForFiles(ctx, dep, user, fileID), "drain "+fileName+" upload tasks")
	must(refreshES(), "refresh ES after "+fileName+" upload")

	doc := mustGetDoc(fileID)
	contentOK := strings.Contains(doc.Source.Content, wantText)
	fmt.Printf("%s boundary text content_ok=%t content_excerpt=%q\n", fileName, contentOK, previewText(doc.Source.Content))
	if strict && !contentOK {
		panic(fmt.Sprintf("%s boundary text content mismatch: want %q got %q", fileName, wantText, previewText(doc.Source.Content)))
	}
	assertPGSidecarStored(ctx, dep, fileID, fileName)
	logBuiltDoc(ctx, dep, user, fileID, "upload "+fileName+" build")
	return fileID
}

func smokeBoundaryArchiveUpload(
	ctx context.Context,
	dep dependency.Dep,
	fm manager.FileManager,
	user *ent.User,
	archivesDir *fs.URI,
	fileName string,
	data []byte,
	label string,
	wantText string,
	wantNames []string,
	strict bool,
) int {
	file, _, err := uploadBytes(ctx, fm, archivesDir.Join(fileName), data)
	must(err, "upload "+label+" archive")
	fileID := file.ID()
	raw := logSource(ctx, fm, label+" archive source", file)
	logTikaProbe(ctx, dep, fm, file, raw, label+" archive tika")

	drainErr := drainFTSTasksForFiles(ctx, dep, user, fileID)
	if drainErr != nil {
		fmt.Printf("%s boundary archive drain_err=%v\n", label, drainErr)
		if strict {
			panic(fmt.Sprintf("%s boundary archive drain failed: %v", label, drainErr))
		}
	}

	refreshErr := refreshES()
	if refreshErr != nil {
		fmt.Printf("%s boundary archive refresh_err=%v\n", label, refreshErr)
		if strict {
			panic(fmt.Sprintf("%s boundary archive refresh failed: %v", label, refreshErr))
		}
	}

	manifestPath, entityID, indexKey, sidecarErr := checkPGSidecarStored(ctx, dep, fileID)
	if sidecarErr != nil {
		fmt.Printf("%s boundary archive pg_err=%v\n", label, sidecarErr)
		if strict {
			panic(fmt.Sprintf("%s boundary archive pg sidecar failed: %v", label, sidecarErr))
		}
	} else {
		fmt.Printf("%s boundary archive pg_ok=true manifest=%s entity=%s index=%s\n", label, manifestPath, entityID, indexKey)
	}

	doc, docErr := getDoc(fileID)
	if docErr != nil {
		fmt.Printf("%s boundary archive es_err=%v\n", label, docErr)
		if strict {
			panic(fmt.Sprintf("%s boundary archive es lookup failed: %v", label, docErr))
		}
		return fileID
	}
	if !doc.Found {
		fmt.Printf("%s boundary archive es_found=false\n", label)
		if strict {
			panic(fmt.Sprintf("%s boundary archive es doc missing for file_id=%d", label, fileID))
		}
		return fileID
	}

	contentOK := strings.Contains(doc.Source.Content, wantText)
	nameStatus := map[string]bool{}
	for _, wantName := range wantNames {
		nameStatus[wantName] = docHasAttachment(fileID, wantName)
	}
	fmt.Printf("%s boundary archive content_ok=%t names=%v attachments=%v\n", label, contentOK, nameStatus, attachmentNames(doc.Source.Attachments))
	printAttachmentTree(fileID, label+" attachment tree")
	if strict && !contentOK {
		panic(fmt.Sprintf("%s boundary archive content mismatch: want %q got %q", label, wantText, previewText(doc.Source.Content)))
	}
	if strict {
		for name, ok := range nameStatus {
			if !ok {
				panic(fmt.Sprintf("%s boundary archive expected attachment %q, got %v", label, name, attachmentNames(doc.Source.Attachments)))
			}
		}
	}
	logBuiltDoc(ctx, dep, user, fileID, "upload "+label+" build")
	return fileID
}

func attachmentNames(items []esAttachment) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	sort.Strings(names)
	return names
}

func printAttachmentTree(fileID int, label string) {
	doc := mustGetDoc(fileID)
	for _, attachment := range doc.Source.Attachments {
		fmt.Printf("%s file_id=%d name=%q type=%s depth=%d parent_attachment_id=%q path=%q\n",
			label, fileID, attachment.Name, attachment.Type, attachment.Depth, attachment.ParentAttachmentID, attachment.Path)
	}
}

func withCorrelation(ctx context.Context) (context.Context, uuid.UUID) {
	cid := uuid.Must(uuid.NewV4())
	return context.WithValue(ctx, logging.CorrelationIDCtx{}, cid), cid
}

func drainFTSTasksForFiles(ctx context.Context, dep dependency.Dep, user *ent.User, fileIDs ...int) error {
	fileIDs = uniquePositiveInts(fileIDs)
	if len(fileIDs) == 0 {
		return nil
	}
	if strings.TrimSpace(os.Getenv("FTS_SMOKE_EXTERNAL_MASTER")) != "" {
		return waitFTSTasksForFiles(ctx, dep, fileIDs...)
	}

	for round := 0; round < 40; round++ {
		taskMap := map[int]*ent.Task{}
		for _, fileID := range fileIDs {
			matches, err := dep.TaskClient().FindPendingByPrivateStateContains(
				ctx,
				strconv.Itoa(fileID),
				queue.FullTextIndexTaskType,
				queue.FullTextDeleteTaskType,
				queue.FullTextCopyTaskType,
				queue.FullTextChangeOwnerTaskType,
			)
			if err != nil {
				return fmt.Errorf("find pending tasks for file %d: %w", fileID, err)
			}
			for _, model := range matches {
				if !taskReferencesFileID(model.PrivateState, fileID) {
					continue
				}
				taskMap[model.ID] = model
			}
		}

		if len(taskMap) == 0 {
			return nil
		}

		tasks := make([]*ent.Task, 0, len(taskMap))
		for _, model := range taskMap {
			tasks = append(tasks, model)
		}
		sort.Slice(tasks, func(i, j int) bool {
			return tasks[i].ID < tasks[j].ID
		})
		fmt.Printf("drain round=%d files=%v task_ids=%v\n", round+1, fileIDs, taskIDs(tasks))

		for _, model := range tasks {
			if smokePreferredContentNodeID > 0 && model.Type == queue.FullTextIndexTaskType {
				if err := forcePreferredContentProcessingNode(dep, model, smokePreferredContentNodeID); err != nil {
					return fmt.Errorf("force preferred content processing node for task %d: %w", model.ID, err)
				}
			}

			taskExec, err := queue.NewTaskFromModel(model)
			if err != nil {
				return fmt.Errorf("new task from model %d: %w", model.ID, err)
			}

			execCtx := context.WithValue(ctx, dependency.DepCtx{}, dep)
			execCtx = context.WithValue(execCtx, inventory.UserCtx{}, user)
			execCtx = context.WithValue(execCtx, inventory.UserIDCtx{}, user.ID)
			execCtx = context.WithValue(execCtx, logging.CorrelationIDCtx{}, model.CorrelationID)
			status, err := taskExec.Do(execCtx)
			if err != nil {
				return fmt.Errorf("task %d (%s): %w", model.ID, model.Type, err)
			}

			switch status {
			case taskmodel.StatusCompleted:
				if err := dep.TaskClient().SetCompleteByID(execCtx, model.ID); err != nil {
					return fmt.Errorf("complete task %d: %w", model.ID, err)
				}
			case taskmodel.StatusSuspending, taskmodel.StatusProcessing, taskmodel.StatusQueued:
				if err := persistTaskState(execCtx, dep, taskExec.Model(), status); err != nil {
					return fmt.Errorf("persist task %d (%s) status=%s: %w", model.ID, model.Type, status, err)
				}
				logSlaveAwaitState(taskExec.Model())
			default:
				return fmt.Errorf("task %d (%s) finished with unexpected status %s", model.ID, model.Type, status)
			}
		}

		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("pending FTS tasks remained after max drain rounds for files %v", fileIDs)
}

func waitFTSTasksForFiles(ctx context.Context, dep dependency.Dep, fileIDs ...int) error {
	for round := 0; round < 120; round++ {
		pendingMap := map[int]*ent.Task{}
		errorMap := map[int]*ent.Task{}
		for _, fileID := range fileIDs {
			matches, err := dep.DBClient().Task.Query().
				Where(
					taskmodel.PrivateStateContains(strconv.Itoa(fileID)),
					taskmodel.TypeIn(
						queue.FullTextIndexTaskType,
						queue.FullTextDeleteTaskType,
						queue.FullTextCopyTaskType,
						queue.FullTextChangeOwnerTaskType,
						queue.DocumentInspectTaskType,
					),
				).
				All(ctx)
			if err != nil {
				return fmt.Errorf("query tasks for file %d: %w", fileID, err)
			}

			for _, model := range matches {
				if !taskReferencesFileID(model.PrivateState, fileID) {
					continue
				}
				switch model.Status {
				case taskmodel.StatusQueued, taskmodel.StatusProcessing, taskmodel.StatusSuspending:
					pendingMap[model.ID] = model
				case taskmodel.StatusError:
					errorMap[model.ID] = model
				}
			}
		}

		if len(errorMap) > 0 {
			tasks := make([]*ent.Task, 0, len(errorMap))
			for _, model := range errorMap {
				tasks = append(tasks, model)
			}
			sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
			model := tasks[0]
			return fmt.Errorf("task %d (%s) failed: %s", model.ID, model.Type, model.PublicState.Error)
		}

		if len(pendingMap) == 0 {
			return nil
		}

		tasks := make([]*ent.Task, 0, len(pendingMap))
		for _, model := range pendingMap {
			tasks = append(tasks, model)
		}
		sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
		fmt.Printf("wait round=%d files=%v task_ids=%v\n", round+1, fileIDs, taskIDs(tasks))
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("pending FTS tasks remained after max wait rounds for files %v", fileIDs)
}

func forcePreferredContentProcessingNode(dep dependency.Dep, model *ent.Task, preferredNodeID int) error {
	state, err := parseSmokeFullTextIndexTaskState(model.PrivateState)
	if err != nil {
		return nil
	}
	if state.NodeID == preferredNodeID {
		return nil
	}
	state.NodeID = preferredNodeID
	stateBytes, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if _, err := dep.TaskClient().UpdatePrivateState(context.Background(), model, string(stateBytes)); err != nil {
		return err
	}
	return nil
}

func persistTaskState(ctx context.Context, dep dependency.Dep, taskModel *ent.Task, status taskmodel.Status) error {
	if taskModel == nil {
		return fmt.Errorf("missing task model")
	}
	_, err := dep.DBClient().Task.UpdateOneID(taskModel.ID).
		SetStatus(status).
		SetPublicState(taskModel.PublicState).
		SetPrivateState(taskModel.PrivateState).
		Save(ctx)
	return err
}

func logSlaveAwaitState(taskModel *ent.Task) {
	if taskModel == nil || taskModel.Type != queue.FullTextIndexTaskType {
		return
	}
	state, err := parseSmokeFullTextIndexTaskState(taskModel.PrivateState)
	if err != nil {
		return
	}
	if state.Phase != "await_slave_extract" {
		return
	}
	fmt.Printf(
		"await slave extract task_id=%d node_id=%d slave_task_id=%d resume_time=%d\n",
		taskModel.ID,
		state.NodeID,
		state.SlaveID,
		taskModel.PublicState.ResumeTime,
	)
}

func parseSmokeFullTextIndexTaskState(raw string) (*smokeFullTextIndexTaskState, error) {
	state := &smokeFullTextIndexTaskState{}
	if err := json.Unmarshal([]byte(raw), state); err != nil {
		return nil, err
	}
	return state, nil
}

func taskReferencesFileID(raw string, fileID int) bool {
	if strings.TrimSpace(raw) == "" || fileID <= 0 {
		return false
	}

	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return false
	}

	return jsonPayloadReferencesFileID(payload, fileID)
}

func jsonPayloadReferencesFileID(payload any, fileID int) bool {
	switch value := payload.(type) {
	case map[string]any:
		for key, item := range value {
			switch {
			case key == "file_id" || strings.HasSuffix(key, "_file_id"):
				if jsonNumericEquals(item, fileID) {
					return true
				}
			case key == "file_ids" || strings.HasSuffix(key, "_file_ids"):
				if items, ok := item.([]any); ok {
					for _, candidate := range items {
						if jsonNumericEquals(candidate, fileID) {
							return true
						}
					}
				}
			}
			if jsonPayloadReferencesFileID(item, fileID) {
				return true
			}
		}
	case []any:
		for _, item := range value {
			if jsonPayloadReferencesFileID(item, fileID) {
				return true
			}
		}
	}

	return false
}

func jsonNumericEquals(value any, target int) bool {
	switch n := value.(type) {
	case float64:
		return int(n) == target
	case int:
		return n == target
	case int64:
		return int(n) == target
	case json.Number:
		v, err := n.Int64()
		return err == nil && int(v) == target
	case string:
		v, err := strconv.Atoi(strings.TrimSpace(n))
		return err == nil && v == target
	default:
		return false
	}
}

func refreshES() error {
	req, err := http.NewRequest(http.MethodPost, esEndpoint+"/"+esIndex+"/_refresh", nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("refresh es: %s %s", resp.Status, strings.TrimSpace(string(body)))
	}

	return nil
}

func getDoc(fileID int) (*esDoc, error) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/%s/_doc/%d", esEndpoint, esIndex, fileID), nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &esDoc{}, nil
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get doc %d: %s %s", fileID, resp.Status, strings.TrimSpace(string(body)))
	}

	doc := &esDoc{}
	if err := json.NewDecoder(resp.Body).Decode(doc); err != nil {
		return nil, err
	}

	return doc, nil
}

func mustGetDoc(fileID int) *esDoc {
	doc, err := getDoc(fileID)
	must(err, fmt.Sprintf("get es doc %d", fileID))
	if !doc.Found {
		panic(fmt.Sprintf("expected es doc for file %d to exist", fileID))
	}
	return doc
}

func ensureDocSeeded(ctx context.Context, dep dependency.Dep, user *ent.User, fileID int, op string) *esDoc {
	doc, err := getDoc(fileID)
	must(err, fmt.Sprintf("get es doc %d", fileID))
	if doc.Found {
		return doc
	}

	fmt.Printf("%s missing file_id=%d; seeding index manually to continue downstream sync validation\n", op, fileID)
	docModel, _, err := manager.BuildFTSFileDocument(ctx, dep, user, fileID)
	must(err, fmt.Sprintf("build search document for seed %d", fileID))
	must(dep.SearchIndexer(ctx).UpsertFile(ctx, docModel), fmt.Sprintf("seed es doc %d", fileID))
	fileModel, err := dep.FileClient().GetByID(ctx, fileID)
	must(err, fmt.Sprintf("load file model for seed %d", fileID))
	must(dep.FileClient().UpsertMetadata(ctx, fileModel, map[string]string{
		dbfs.FullTextIndexKey: hashid.EncodeEntityID(dep.HashIDEncoder(), docModel.EntityID),
	}, map[string]bool{}), fmt.Sprintf("patch fts metadata for seed %d", fileID))
	must(refreshES(), "refresh ES after seed")
	return mustGetDoc(fileID)
}

func assertDocPath(fileID int, wantPath, op string) {
	doc := mustGetDoc(fileID)
	if doc.Source.PathText != wantPath {
		panic(fmt.Sprintf("%s: unexpected path for file %d: got %q want %q", op, fileID, doc.Source.PathText, wantPath))
	}
	fmt.Printf("%s ok file_id=%d path=%s content_len=%d\n", op, fileID, doc.Source.PathText, len(strings.TrimSpace(doc.Source.Content)))
}

func assertDocMissing(fileID int, op string) {
	doc, err := getDoc(fileID)
	must(err, fmt.Sprintf("get es doc %d", fileID))
	if doc.Found {
		panic(fmt.Sprintf("%s: expected es doc for file %d to be deleted, still found path=%q", op, fileID, doc.Source.PathText))
	}
	fmt.Printf("%s ok file_id=%d deleted_from_es=true\n", op, fileID)
}

func assertDocContentContains(fileID int, want, op string) {
	doc := mustGetDoc(fileID)
	if !strings.Contains(doc.Source.Content, want) {
		panic(fmt.Sprintf("%s: expected content for file %d to contain %q, got %q", op, fileID, want, previewText(doc.Source.Content)))
	}
	fmt.Printf("%s ok file_id=%d content_contains=%q attachments=%d\n", op, fileID, want, len(doc.Source.Attachments))
}

func assertDocHasAttachment(fileID int, wantName, op string) {
	doc := mustGetDoc(fileID)
	for _, attachment := range doc.Source.Attachments {
		if attachment.Name == wantName {
			fmt.Printf("%s ok file_id=%d attachment=%s type=%s depth=%d\n", op, fileID, attachment.Name, attachment.Type, attachment.Depth)
			return
		}
	}

	names := make([]string, 0, len(doc.Source.Attachments))
	for _, attachment := range doc.Source.Attachments {
		names = append(names, attachment.Name)
	}
	panic(fmt.Sprintf("%s: expected attachment %q for file %d, got %v", op, wantName, fileID, names))
}

func docHasAttachment(fileID int, wantName string) bool {
	doc := mustGetDoc(fileID)
	for _, attachment := range doc.Source.Attachments {
		if attachment.Name == wantName {
			return true
		}
	}

	return false
}

func assertDocAttachmentHierarchy(fileID int, wantName, wantParentAttachmentID string, wantDepth int, op string) {
	doc := mustGetDoc(fileID)
	resolvedParentID := wantParentAttachmentID
	if wantParentAttachmentID != "" {
		for _, candidate := range doc.Source.Attachments {
			if candidate.ID == wantParentAttachmentID || candidate.Path == wantParentAttachmentID {
				resolvedParentID = candidate.ID
				break
			}
			if strings.HasSuffix(candidate.ID, ":"+wantParentAttachmentID) || strings.HasSuffix(candidate.Path, "/"+wantParentAttachmentID) {
				resolvedParentID = candidate.ID
				break
			}
		}
	}
	for _, attachment := range doc.Source.Attachments {
		if attachment.Name != wantName {
			continue
		}
		parentMatches := false
		if wantParentAttachmentID == "" {
			parentMatches = attachment.ParentAttachmentID == "" && (attachment.ParentID == "" || attachment.ParentID == strconv.Itoa(fileID))
		} else {
			parentMatches = attachment.ParentAttachmentID == resolvedParentID || attachment.ParentID == resolvedParentID
		}
		if !parentMatches || attachment.Depth != wantDepth {
			panic(fmt.Sprintf(
				"%s: unexpected hierarchy for file %d attachment %q: got parent_id=%q parent_attachment_id=%q depth=%d want parent=%q resolved_parent=%q depth=%d",
				op,
				fileID,
				wantName,
				attachment.ParentID,
				attachment.ParentAttachmentID,
				attachment.Depth,
				wantParentAttachmentID,
				resolvedParentID,
				wantDepth,
			))
		}
		fmt.Printf(
			"%s ok file_id=%d attachment=%s parent_id=%q parent_attachment_id=%q resolved_parent=%q depth=%d\n",
			op,
			fileID,
			attachment.Name,
			attachment.ParentID,
			attachment.ParentAttachmentID,
			resolvedParentID,
			attachment.Depth,
		)
		return
	}

	panic(fmt.Sprintf("%s: attachment %q not found for file %d", op, wantName, fileID))
}

func assertDocHasImageAttachment(fileID int, op string) {
	doc := mustGetDoc(fileID)
	for _, attachment := range doc.Source.Attachments {
		if strings.HasPrefix(strings.ToLower(attachment.Type), "image") || strings.HasPrefix(strings.ToLower(attachment.Name), "image") {
			fmt.Printf("%s ok file_id=%d attachment=%s type=%s depth=%d\n", op, fileID, attachment.Name, attachment.Type, attachment.Depth)
			return
		}
	}
	for _, attachment := range doc.Source.Attachments {
		if strings.HasPrefix(strings.ToLower(attachment.Path), "cloudreve/fts-sidecar/") && strings.Contains(strings.ToLower(attachment.Name), ".png") {
			fmt.Printf("%s ok file_id=%d attachment=%s type=%s depth=%d\n", op, fileID, attachment.Name, attachment.Type, attachment.Depth)
			return
		}
	}

	names := make([]string, 0, len(doc.Source.Attachments))
	for _, attachment := range doc.Source.Attachments {
		names = append(names, fmt.Sprintf("%s(%s)", attachment.Name, attachment.Type))
	}
	panic(fmt.Sprintf("%s: expected image attachment for file %d, got %v", op, fileID, names))
}

func mustURI(raw string) *fs.URI {
	uri, err := fs.NewUriFromString(raw)
	must(err, "parse uri")
	return uri
}

func must(err error, step string) {
	if err != nil {
		panic(fmt.Sprintf("%s: %v", step, err))
	}
}

func uniquePositiveInts(ids []int) []int {
	seen := map[int]struct{}{}
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func taskIDs(tasks []*ent.Task) []int {
	ids := make([]int, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return ids
}

func logSource(ctx context.Context, fm manager.FileManager, label string, file fs.File) []byte {
	if file == nil {
		return nil
	}

	source, err := fm.GetEntitySource(ctx, file.PrimaryEntityID())
	if err != nil {
		fmt.Printf("%s file_id=%d get_source_err=%v\n", label, file.ID(), err)
		return nil
	}
	defer source.Close()

	raw, err := io.ReadAll(source)
	if err != nil {
		fmt.Printf("%s file_id=%d read_err=%v\n", label, file.ID(), err)
		return nil
	}

	preview := string(raw)
	if len(preview) > 80 {
		preview = preview[:80]
	}
	preview = strings.ReplaceAll(preview, "\n", "\\n")
	fmt.Printf(
		"%s file_id=%d file_size=%d entity_size=%d source_bytes=%d preview=%q\n",
		label,
		file.ID(),
		file.Size(),
		file.PrimaryEntity().Size(),
		len(raw),
		preview,
	)
	return raw
}

func logTikaProbe(ctx context.Context, dep dependency.Dep, fm manager.FileManager, file fs.File, raw []byte, label string) {
	tika, ok := dep.TextExtractor(ctx).(*tikaextractor.TikaExtractor)
	if !ok || file == nil {
		return
	}

	fromBytes, err := tika.ExtractFile(ctx, bytes.NewReader(raw), file.Name())
	if err != nil {
		fmt.Printf("%s file_id=%d bytes_err=%v\n", label, file.ID(), err)
	} else {
		fmt.Printf("%s file_id=%d bytes_len=%d bytes_preview=%q\n", label, file.ID(), len(strings.TrimSpace(fromBytes)), previewText(fromBytes))
	}

	source, err := fm.GetEntitySource(ctx, file.PrimaryEntityID())
	if err != nil {
		fmt.Printf("%s file_id=%d source_err=%v\n", label, file.ID(), err)
		return
	}
	defer source.Close()

	fromSource, err := tika.ExtractFile(ctx, source, file.Name())
	if err != nil {
		fmt.Printf("%s file_id=%d entity_source_err=%v\n", label, file.ID(), err)
		return
	}
	fmt.Printf("%s file_id=%d entity_source_len=%d entity_source_preview=%q\n", label, file.ID(), len(strings.TrimSpace(fromSource)), previewText(fromSource))
}

func previewText(text string) string {
	text = strings.ReplaceAll(text, "\n", "\\n")
	if len(text) > 80 {
		return text[:80]
	}
	return text
}

func logBuiltDoc(ctx context.Context, dep dependency.Dep, user *ent.User, fileID int, label string) {
	doc, _, err := manager.BuildFTSFileDocument(ctx, dep, user, fileID)
	if err != nil {
		fmt.Printf("%s file_id=%d build_err=%v\n", label, fileID, err)
		return
	}

	raw, err := json.Marshal(doc)
	if err != nil {
		fmt.Printf("%s file_id=%d marshal_err=%v\n", label, fileID, err)
		return
	}

	fmt.Printf(
		"%s file_id=%d doc_content_len=%d excerpt=%q json_has_content=%t attachments=%d\n",
		label,
		fileID,
		len(strings.TrimSpace(doc.Content)),
		previewText(doc.Content),
		bytes.Contains(raw, []byte(`"content"`)),
		len(doc.Attachments),
	)
}

func assertPGSidecarStored(ctx context.Context, dep dependency.Dep, fileID int, label string) {
	manifestPath, entityID, indexKey, err := checkPGSidecarStored(ctx, dep, fileID)
	if err != nil {
		panic(fmt.Sprintf("%s: %v", label, err))
	}
	fmt.Printf("%s pg ok file_id=%d manifest=%s entity=%s index=%s\n", label, fileID, manifestPath, entityID, indexKey)
}

func checkPGSidecarStored(ctx context.Context, dep dependency.Dep, fileID int) (string, string, string, error) {
	loadCtx := context.WithValue(ctx, inventory.LoadFileMetadata{}, true)
	fileModel, err := dep.FileClient().GetByID(loadCtx, fileID)
	if err != nil {
		return "", "", "", fmt.Errorf("load file model for pg sidecar file %d: %w", fileID, err)
	}

	metadata := map[string]string{}
	for _, item := range fileModel.Edges.Metadata {
		metadata[item.Name] = item.Value
	}

	manifestPath := metadata[dbfs.FTSSidecarManifestKey]
	entityID := metadata[dbfs.FTSSidecarEntityIDKey]
	indexKey := metadata[dbfs.FullTextIndexKey]
	if manifestPath == "" || entityID == "" || indexKey == "" {
		return "", "", "", fmt.Errorf("missing pg fulltext metadata for file %d manifest=%q entity=%q index=%q", fileID, manifestPath, entityID, indexKey)
	}
	if !strings.Contains(manifestPath, fmt.Sprintf("/%d/", fileID)) {
		return "", "", "", fmt.Errorf("sidecar manifest path does not belong to file %d: %q", fileID, manifestPath)
	}

	localPath := util.RelativePath(manifestPath)
	if _, err := os.Stat(localPath); err == nil {
		return manifestPath, entityID, indexKey, nil
	}

	entityIDInt, err := strconv.Atoi(entityID)
	if err != nil {
		return "", "", "", fmt.Errorf("parse sidecar entity id for file %d: %w", fileID, err)
	}

	entityModel, err := dep.DBClient().Entity.Query().Where(ententity.IDEQ(entityIDInt)).Only(ctx)
	if err != nil {
		return "", "", "", fmt.Errorf("load sidecar entity for file %d: %w", fileID, err)
	}

	policy, err := dep.StoragePolicyClient().GetPolicyByID(ctx, entityModel.StoragePolicyEntities)
	if err != nil {
		return "", "", "", fmt.Errorf("load sidecar policy for file %d: %w", fileID, err)
	}

	if strings.EqualFold(policy.Type, string(types.PolicyTypeS3)) && !policy.IsPrivate {
		base := strings.TrimRight(policy.Server, "/")
		escapedPath := strings.TrimLeft((&url.URL{Path: manifestPath}).EscapedPath(), "/")
		manifestURL := fmt.Sprintf("%s/%s/%s", base, policy.BucketName, escapedPath)
		resp, reqErr := http.Get(manifestURL)
		if reqErr != nil {
			return "", "", "", fmt.Errorf("sidecar manifest missing locally and failed to request remote object for file %d url=%q err=%v", fileID, manifestURL, reqErr)
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return "", "", "", fmt.Errorf("sidecar manifest missing locally and remote object unavailable for file %d url=%q status=%s body=%q", fileID, manifestURL, resp.Status, strings.TrimSpace(string(body)))
		}

		return manifestPath, entityID, indexKey, nil
	}

	return "", "", "", fmt.Errorf("sidecar manifest missing on disk for file %d path=%q and no public remote policy fallback available", fileID, localPath)
}

func assertPGNoSidecarStored(ctx context.Context, dep dependency.Dep, fileID int, label string) {
	loadCtx := context.WithValue(ctx, inventory.LoadFileMetadata{}, true)
	fileModel, err := dep.FileClient().GetByID(loadCtx, fileID)
	must(err, "load file model for pg no sidecar "+label)

	metadata := map[string]string{}
	for _, item := range fileModel.Edges.Metadata {
		metadata[item.Name] = item.Value
	}

	if metadata[dbfs.FTSSidecarManifestKey] != "" || metadata[dbfs.FTSSidecarEntityIDKey] != "" {
		panic(fmt.Sprintf("%s: expected no pg sidecar metadata for file %d, got manifest=%q entity=%q", label, fileID, metadata[dbfs.FTSSidecarManifestKey], metadata[dbfs.FTSSidecarEntityIDKey]))
	}

	fmt.Printf("%s pg ok file_id=%d sidecar_enabled=false\n", label, fileID)
}

func assertManifestHierarchy(
	ctx context.Context,
	fm manager.FileManager,
	uri *fs.URI,
	wantID, wantParentID string,
	wantDepth int,
	label string,
) {
	manifest, err := fm.GetFTSSidecar(ctx, uri)
	must(err, "load sidecar manifest "+label)
	for _, item := range manifest.Objects {
		if item.ID != wantID {
			continue
		}
		if item.ParentID != wantParentID || item.Depth != wantDepth {
			panic(fmt.Sprintf(
				"%s: unexpected manifest hierarchy for %q: got parent=%q depth=%d want parent=%q depth=%d",
				label,
				wantID,
				item.ParentID,
				item.Depth,
				wantParentID,
				wantDepth,
			))
		}
		fmt.Printf("%s pg ok object=%s parent_id=%q depth=%d path=%s\n", label, item.ID, item.ParentID, item.Depth, item.Path)
		return
	}

	panic(fmt.Sprintf("%s: sidecar object %q not found", label, wantID))
}

func ensureSmokeFTSSettings(ctx context.Context, dep dependency.Dep) error {
	settings := map[string]string{
		"siteURL":                         "http://127.0.0.1:5212",
		"fts_enabled":                     "1",
		"fts_index_type":                  "elasticsearch",
		"fts_elasticsearch_endpoint":      "http://127.0.0.1:9200",
		"fts_extractor_type":              "tika",
		"fts_tika_endpoint":               "http://127.0.0.1:9998",
		"fts_tika_document_enabled":       "1",
		"fts_tika_archive_enabled":        "1",
		"fts_tika_sidecar_enabled":        "1",
		"fts_tika_sidecar_text_enabled":   "1",
		"fts_tika_sidecar_assets_enabled": "1",
		"fts_tika_extract_inline_images":  "1",
		"fts_tika_document_exts":          "pdf,txt,text,md,markdown,csv,tsv,html,htm,xhtml,xml,rtf,epub,fb2,chm,mif,doc,dot,docx,docm,dotx,dotm,wps,wks,wri,hwp,one,wpd,xls,xlt,xla,xlc,xlm,xlw,xlsx,xlsm,xltx,xltm,xlsb,xlam,qpw,ppt,pps,pot,pptx,pptm,ppsx,ppsm,potx,potm,sldx,sldm,ppam,vsd,vst,vss,vsdx,vstx,vssx,vsdm,vstm,vssm,pub,mpp,xps,dwfx,odt,fodt,ott,odm,oth,ods,fods,ots,odp,fodp,otp,odg,fodg,otg,odc,odf,odb,odi,sxw,stw,sxg,sxc,stc,sxi,sti,sxd,std,sxm,pages,numbers,key,eml,mht,mhtml,nws,msg,pst,mbox,tnef",
		"fts_tika_archive_exts":           "zip,tar,tgz,tbz,tbz2,txz,tlz,7z,rar,ar,gz,z,bz,bz2,xz,lzma,lz4,br,snappy,sz,pack200,cpio,arj,dump,jar,war,ear",
	}
	if err := dep.SettingClient().Set(ctx, settings); err != nil {
		return err
	}

	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}
	if err := dep.KV().Delete(appsetting.KvSettingPrefix, keys...); err != nil {
		return err
	}

	return nil
}

func ensureSmokeContentProcessingSlave(ctx context.Context, dep dependency.Dep) (*ent.Node, error) {
	server := strings.TrimSpace(os.Getenv("FTS_SMOKE_SLAVE_URL"))
	if server == "" {
		return nil, nil
	}
	secret := strings.TrimSpace(os.Getenv("FTS_SMOKE_SLAVE_KEY"))
	if secret == "" {
		secret = "1234567890123456789012345678901234567890123456789012345678901234"
	}

	capabilities := &boolset.BooleanSet{}
	boolset.Set(types.NodeCapabilityContentProcessing, true, capabilities)

	existing, err := dep.DBClient().Node.Query().
		Where(entnode.ServerEQ(server)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}

	model := &ent.Node{
		Name:         "__fts_slave_smoke__",
		Server:       server,
		SlaveKey:     secret,
		Status:       entnode.StatusActive,
		Type:         entnode.TypeSlave,
		Capabilities: capabilities,
		Settings:     &types.NodeSetting{},
		Weight:       100,
	}
	if existing != nil {
		model.ID = existing.ID
	}

	nodeModel, err := dep.NodeClient().Upsert(ctx, model)
	if err != nil {
		return nil, err
	}

	np, err := dep.NodePool(ctx)
	if err == nil {
		np.Upsert(ctx, nodeModel)
	}

	return nodeModel, nil
}

func smokeSidecarToggle(ctx context.Context, dep dependency.Dep, fm manager.FileManager, user *ent.User, docsDir *fs.URI, suffix string) (int, error) {
	original, err := dep.SettingClient().Gets(ctx, []string{
		"fts_tika_sidecar_enabled",
		"fts_tika_sidecar_text_enabled",
		"fts_tika_sidecar_assets_enabled",
	})
	if err != nil {
		return 0, err
	}

	restore := func() {
		if err := dep.SettingClient().Set(ctx, original); err != nil {
			fmt.Fprintf(os.Stderr, "restore sidecar settings warning: %v\n", err)
		}
		keys := make([]string, 0, len(original))
		for key := range original {
			keys = append(keys, key)
		}
		_ = dep.KV().Delete(appsetting.KvSettingPrefix, keys...)
	}
	defer restore()

	disabledSettings := map[string]string{
		"fts_tika_sidecar_enabled":        "0",
		"fts_tika_sidecar_text_enabled":   "1",
		"fts_tika_sidecar_assets_enabled": "1",
	}
	if err := dep.SettingClient().Set(ctx, disabledSettings); err != nil {
		return 0, err
	}
	_ = dep.KV().Delete(appsetting.KvSettingPrefix, "fts_tika_sidecar_enabled", "fts_tika_sidecar_text_enabled", "fts_tika_sidecar_assets_enabled")

	reloadCtx := context.WithValue(ctx, dependency.ReloadCtx{}, true)
	fmt.Printf("sidecar disabled extractor=%T\n", dep.TextExtractor(reloadCtx))

	disabledFile, _, err := uploadText(ctx, fm, docsDir.Join("sidecar-off.txt"), "sidecar disabled content "+suffix)
	if err != nil {
		return 0, err
	}
	disabledFileID := disabledFile.ID()
	disabledRaw := logSource(ctx, fm, "sidecar disabled source", disabledFile)
	logTikaProbe(ctx, dep, fm, disabledFile, disabledRaw, "sidecar disabled tika")
	if err := drainFTSTasksForFiles(ctx, dep, user, disabledFileID); err != nil {
		return 0, err
	}
	if err := refreshES(); err != nil {
		return 0, err
	}
	assertDocContentContains(disabledFileID, "sidecar disabled content "+suffix, "sidecar disabled upload")
	assertPGNoSidecarStored(ctx, dep, disabledFileID, "sidecar disabled")
	logBuiltDoc(ctx, dep, user, disabledFileID, "sidecar disabled build")

	if err := dep.SettingClient().Set(ctx, original); err != nil {
		return 0, err
	}
	_ = dep.KV().Delete(appsetting.KvSettingPrefix, "fts_tika_sidecar_enabled", "fts_tika_sidecar_text_enabled", "fts_tika_sidecar_assets_enabled")

	return disabledFileID, nil
}

func buildMinimalDocx(text string) []byte {
	var buffer bytes.Buffer
	zw := zip.NewWriter(&buffer)

	writeZipFile(zw, "[Content_Types].xml", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Default Extension="png" ContentType="image/png"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`))
	writeZipFile(zw, "_rels/.rels", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`))
	writeZipFile(zw, "word/document.xml", []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>%s</w:t></w:r></w:p>
    <w:sectPr/>
  </w:body>
</w:document>`, text)))
	writeZipFile(zw, "word/media/image1.png", tinyPNG())

	if err := zw.Close(); err != nil {
		panic(fmt.Sprintf("close docx zip: %v", err))
	}

	return buffer.Bytes()
}

func buildMinimalPDFWithEmbeddedFile(text, attachmentName, attachmentContent string) []byte {
	text = strings.ReplaceAll(text, "\\", "\\\\")
	text = strings.ReplaceAll(text, "(", "\\(")
	text = strings.ReplaceAll(text, ")", "\\)")
	attachmentName = strings.ReplaceAll(attachmentName, "\\", "\\\\")
	attachmentName = strings.ReplaceAll(attachmentName, "(", "\\(")
	attachmentName = strings.ReplaceAll(attachmentName, ")", "\\)")

	attachmentRaw := attachmentContent
	attachmentContent = strings.ReplaceAll(attachmentContent, "\\", "\\\\")
	attachmentContent = strings.ReplaceAll(attachmentContent, "(", "\\(")
	attachmentContent = strings.ReplaceAll(attachmentContent, ")", "\\)")

	content := strings.Join([]string{
		"BT",
		"/F1 18 Tf",
		"72 720 Td",
		fmt.Sprintf("(%s) Tj", text),
		"ET",
		"q",
		"72 0 0 72 72 620 cm",
		"BI",
		"/W 1",
		"/H 1",
		"/BPC 8",
		"/CS /RGB",
		"/F /AHx",
		"ID",
		"FF0000>",
		"EI",
		"Q",
	}, "\n") + "\n"

	objects := []string{
		fmt.Sprintf("<< /Type /Catalog /Pages 2 0 R /Names << /EmbeddedFiles << /Names [(%s) 6 0 R] >> >> >>", attachmentName),
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Type /Filespec /F (%s) /UF (%s) /Desc (%s) /EF << /F 7 0 R >> >>", attachmentName, attachmentName, attachmentContent),
		fmt.Sprintf("<< /Type /EmbeddedFile /Subtype /text#2Fplain /Params << /Size %d >> /Length %d >>\nstream\n%sendstream", len(attachmentRaw), len(attachmentRaw), attachmentRaw),
	}

	var buffer bytes.Buffer
	buffer.WriteString("%PDF-1.4\n")
	offsets := make([]int, 0, len(objects)+1)
	offsets = append(offsets, 0)
	for i, object := range objects {
		offsets = append(offsets, buffer.Len())
		buffer.WriteString(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", i+1, object))
	}

	xrefOffset := buffer.Len()
	buffer.WriteString(fmt.Sprintf("xref\n0 %d\n", len(objects)+1))
	buffer.WriteString("0000000000 65535 f \n")
	for i := 1; i < len(offsets); i++ {
		buffer.WriteString(fmt.Sprintf("%010d 00000 n \n", offsets[i]))
	}
	buffer.WriteString(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefOffset))

	return buffer.Bytes()
}

func buildNestedZip(outerText, innerText string) []byte {
	inner := buildSimpleZip(map[string][]byte{
		"nested.txt": []byte(innerText),
	})

	return buildSimpleZip(map[string][]byte{
		"outer.txt": []byte(outerText),
		"inner.zip": inner,
	})
}

func buildNestedTar(outerText, innerText string) []byte {
	inner := buildSimpleZip(map[string][]byte{
		"nested.txt": []byte(innerText),
	})

	var buffer bytes.Buffer
	tw := tar.NewWriter(&buffer)
	writeTarFile(tw, "outer.txt", []byte(outerText))
	writeTarFile(tw, "inner.zip", inner)
	if err := tw.Close(); err != nil {
		panic(fmt.Sprintf("close tar: %v", err))
	}

	return buffer.Bytes()
}

func buildNestedTGZ(outerText, innerText string) []byte {
	var buffer bytes.Buffer
	zw := gzip.NewWriter(&buffer)
	if _, err := zw.Write(buildNestedTar(outerText, innerText)); err != nil {
		panic(fmt.Sprintf("write tgz: %v", err))
	}
	if err := zw.Close(); err != nil {
		panic(fmt.Sprintf("close tgz: %v", err))
	}

	return buffer.Bytes()
}

func buildNestedTBZ2(outerText, innerText string) ([]byte, bool) {
	return buildCompressedTarCommandArchive("bzip2", []string{"-c"}, outerText, innerText)
}

func buildNestedTXZ(outerText, innerText string) ([]byte, bool) {
	return buildCompressedTarCommandArchive("xz", []string{"-c"}, outerText, innerText)
}

func buildNestedLZMA(outerText, innerText string) ([]byte, bool) {
	return buildCompressedTarCommandArchive("xz", []string{"--format=lzma", "-c"}, outerText, innerText)
}

func buildNestedCPIO(outerText, innerText string) ([]byte, bool) {
	if _, err := exec.LookPath("cpio"); err != nil {
		return nil, false
	}

	tempDir, err := os.MkdirTemp("", "fts-cpio-*")
	if err != nil {
		panic(fmt.Sprintf("create temp dir for cpio: %v", err))
	}
	defer os.RemoveAll(tempDir)

	if err := os.WriteFile(filepath.Join(tempDir, "inner.zip"), buildSimpleZip(map[string][]byte{
		"nested.txt": []byte(innerText),
	}), 0o644); err != nil {
		panic(fmt.Sprintf("write inner zip for cpio: %v", err))
	}
	if err := os.WriteFile(filepath.Join(tempDir, "outer.txt"), []byte(outerText), 0o644); err != nil {
		panic(fmt.Sprintf("write outer text for cpio: %v", err))
	}

	cmd := exec.Command("sh", "-lc", "printf 'outer.txt\ninner.zip\n' | cpio -o -H newc --quiet")
	cmd.Dir = tempDir
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		panic(fmt.Sprintf(
			"cpio create archive failed: %v output=%s stderr=%s",
			err,
			strings.TrimSpace(string(output)),
			strings.TrimSpace(stderr.String()),
		))
	}

	return output, true
}

func buildNestedAr(outerText, innerText string) ([]byte, bool) {
	return buildNestedCommandArchive("ar", "nested.ar", []string{"rcs"}, outerText, innerText)
}

func buildNestedJar(outerText, innerText string) []byte {
	inner := buildSimpleZip(map[string][]byte{
		"nested.txt": []byte(innerText),
	})

	return buildSimpleZip(map[string][]byte{
		"META-INF/MANIFEST.MF": []byte("Manifest-Version: 1.0\nCreated-By: Cloudreve FTS Smoke\n"),
		"outer.txt":            []byte(outerText),
		"inner.zip":            inner,
	})
}

func buildNested7z(outerText, innerText string) ([]byte, bool) {
	return buildNestedCommandArchive("7z", "nested.7z", []string{"a", "-bd", "-y"}, outerText, innerText)
}

func buildNestedRar(outerText, innerText string) ([]byte, bool) {
	return buildNestedCommandArchive("rar", "nested.rar", []string{"a", "-ma4", "-ep", "-idq"}, outerText, innerText)
}

func buildCompressedTarCommandArchive(cmdName string, args []string, outerText, innerText string) ([]byte, bool) {
	if _, err := exec.LookPath(cmdName); err != nil {
		return nil, false
	}

	tempDir, err := os.MkdirTemp("", "fts-compressed-*")
	if err != nil {
		panic(fmt.Sprintf("create temp dir for %s: %v", cmdName, err))
	}
	defer os.RemoveAll(tempDir)

	tarPath := filepath.Join(tempDir, "nested.tar")
	if err := os.WriteFile(tarPath, buildNestedTar(outerText, innerText), 0o644); err != nil {
		panic(fmt.Sprintf("write nested tar for %s: %v", cmdName, err))
	}

	cmdArgs := append(append([]string{}, args...), tarPath)
	cmd := exec.Command(cmdName, cmdArgs...)
	output, err := cmd.Output()
	if err != nil {
		panic(fmt.Sprintf("%s compress archive failed: %v", cmdName, err))
	}

	return output, true
}

func buildNestedCommandArchive(cmdName, archiveName string, baseArgs []string, outerText, innerText string) ([]byte, bool) {
	if _, err := exec.LookPath(cmdName); err != nil {
		return nil, false
	}

	tempDir, err := os.MkdirTemp("", "fts-archive-*")
	if err != nil {
		panic(fmt.Sprintf("create temp dir for %s: %v", cmdName, err))
	}
	defer os.RemoveAll(tempDir)

	innerZipPath := filepath.Join(tempDir, "inner.zip")
	if err := os.WriteFile(innerZipPath, buildSimpleZip(map[string][]byte{
		"nested.txt": []byte(innerText),
	}), 0o644); err != nil {
		panic(fmt.Sprintf("write inner zip for %s: %v", cmdName, err))
	}
	if err := os.WriteFile(filepath.Join(tempDir, "outer.txt"), []byte(outerText), 0o644); err != nil {
		panic(fmt.Sprintf("write outer text for %s: %v", cmdName, err))
	}

	args := append(append([]string{}, baseArgs...), archiveName, "outer.txt", "inner.zip")
	cmd := exec.Command(cmdName, args...)
	cmd.Dir = tempDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		panic(fmt.Sprintf("%s create archive failed: %v output=%s", cmdName, err, strings.TrimSpace(string(output))))
	}

	raw, err := os.ReadFile(filepath.Join(tempDir, archiveName))
	if err != nil {
		panic(fmt.Sprintf("read %s archive: %v", cmdName, err))
	}

	return raw, true
}

func buildSmokeEML(subject, body, attachmentName, attachmentText string) []byte {
	encoded := base64.StdEncoding.EncodeToString([]byte(attachmentText + "\n"))
	return []byte(strings.Join([]string{
		"From: alice@example.com",
		"To: bob@example.com",
		"Subject: " + subject,
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="boundary-smoke"`,
		"",
		"--boundary-smoke",
		`Content-Type: text/plain; charset="utf-8"`,
		"",
		body,
		"",
		"--boundary-smoke",
		`Content-Type: text/plain; name="` + attachmentName + `"`,
		"Content-Transfer-Encoding: base64",
		`Content-Disposition: attachment; filename="` + attachmentName + `"`,
		"",
		encoded,
		"--boundary-smoke--",
		"",
	}, "\r\n"))
}

func buildSmokeMbox(eml []byte) []byte {
	var buffer bytes.Buffer
	buffer.WriteString("From MAILER-DAEMON ")
	buffer.WriteString(time.Now().UTC().Format(time.ANSIC))
	buffer.WriteByte('\n')
	buffer.Write(eml)
	if !bytes.HasSuffix(eml, []byte("\n")) {
		buffer.WriteByte('\n')
	}
	return buffer.Bytes()
}

func encodeTextWithIconv(charset, text string) []byte {
	cmd := exec.Command("iconv", "-f", "UTF-8", "-t", charset)
	cmd.Stdin = strings.NewReader(text)
	out, err := cmd.Output()
	if err != nil {
		panic(fmt.Sprintf("iconv encode %s failed: %v", charset, err))
	}
	return out
}

func buildUTF8ChineseNestedZip(text string) []byte {
	inner := buildSimpleZip(map[string][]byte{
		"二级目录/中文内容.txt": []byte(text),
	})
	return buildSimpleZip(map[string][]byte{
		"中文目录/外层说明.txt": []byte(text),
		"中文目录/内层.zip":   inner,
	})
}

func buildEncodedChineseNestedZip(charset, text string) []byte {
	inner := buildRawEncodedZip(map[string][]byte{
		"二级目录/中文内容.txt": encodeTextWithIconv(charset, text),
	}, charset)
	return buildRawEncodedZip(map[string][]byte{
		"中文目录/外层说明.txt": encodeTextWithIconv(charset, text),
		"中文目录/内层.zip":   inner,
	}, charset)
}

func buildRawEncodedZip(files map[string][]byte, charset string) []byte {
	var buffer bytes.Buffer
	zw := zip.NewWriter(&buffer)
	for name, data := range files {
		header := &zip.FileHeader{
			Name:   string(encodeTextWithIconv(charset, name)),
			Method: zip.Deflate,
		}
		header.NonUTF8 = true
		writer, err := zw.CreateHeader(header)
		if err != nil {
			panic(fmt.Sprintf("create encoded zip entry %s (%s): %v", name, charset, err))
		}
		if _, err := writer.Write(data); err != nil {
			panic(fmt.Sprintf("write encoded zip entry %s (%s): %v", name, charset, err))
		}
	}
	if err := zw.Close(); err != nil {
		panic(fmt.Sprintf("close encoded zip (%s): %v", charset, err))
	}
	return buffer.Bytes()
}

func buildChineseNestedCommandArchive(cmdName, archiveName string, baseArgs []string, text string) ([]byte, bool) {
	if _, err := exec.LookPath(cmdName); err != nil {
		return nil, false
	}

	tempDir, err := os.MkdirTemp("", "fts-chinese-archive-*")
	if err != nil {
		panic(fmt.Sprintf("create temp dir for %s chinese archive: %v", cmdName, err))
	}
	defer os.RemoveAll(tempDir)

	chineseDir := filepath.Join(tempDir, "中文目录")
	nestedDir := filepath.Join(chineseDir, "二级目录")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		panic(fmt.Sprintf("create chinese nested dir for %s: %v", cmdName, err))
	}
	if err := os.WriteFile(filepath.Join(chineseDir, "外层说明.txt"), []byte(text), 0o644); err != nil {
		panic(fmt.Sprintf("write chinese outer text for %s: %v", cmdName, err))
	}
	if err := os.WriteFile(filepath.Join(nestedDir, "中文内容.txt"), []byte(text), 0o644); err != nil {
		panic(fmt.Sprintf("write chinese nested text for %s: %v", cmdName, err))
	}
	if err := os.WriteFile(filepath.Join(chineseDir, "内层.zip"), buildSimpleZip(map[string][]byte{
		"二级目录/中文内容.txt": []byte(text),
	}), 0o644); err != nil {
		panic(fmt.Sprintf("write chinese inner zip for %s: %v", cmdName, err))
	}

	args := append(append([]string{}, baseArgs...), archiveName, "中文目录")
	cmd := exec.Command(cmdName, args...)
	cmd.Dir = tempDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		panic(fmt.Sprintf("%s create chinese archive failed: %v output=%s", cmdName, err, strings.TrimSpace(string(output))))
	}

	raw, err := os.ReadFile(filepath.Join(tempDir, archiveName))
	if err != nil {
		panic(fmt.Sprintf("read chinese %s archive: %v", cmdName, err))
	}

	return raw, true
}

func buildSimpleZip(files map[string][]byte) []byte {
	var buffer bytes.Buffer
	zw := zip.NewWriter(&buffer)
	for name, data := range files {
		writeZipFile(zw, name, data)
	}

	if err := zw.Close(); err != nil {
		panic(fmt.Sprintf("close zip: %v", err))
	}

	return buffer.Bytes()
}

func writeZipFile(zw *zip.Writer, name string, data []byte) {
	writer, err := zw.Create(name)
	if err != nil {
		panic(fmt.Sprintf("create zip entry %s: %v", name, err))
	}
	if _, err := writer.Write(data); err != nil {
		panic(fmt.Sprintf("write zip entry %s: %v", name, err))
	}
}

func writeTarFile(tw *tar.Writer, name string, data []byte) {
	if err := tw.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0o644,
		Size: int64(len(data)),
	}); err != nil {
		panic(fmt.Sprintf("create tar entry %s: %v", name, err))
	}
	if _, err := tw.Write(data); err != nil {
		panic(fmt.Sprintf("write tar entry %s: %v", name, err))
	}
}

func tinyPNG() []byte {
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9c, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
		0x00, 0x03, 0x01, 0x01, 0x00, 0xc9, 0xfe, 0x92,
		0xef, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
		0x44, 0xae, 0x42, 0x60, 0x82,
	}
}
