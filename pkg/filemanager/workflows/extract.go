package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/cluster"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	tikaextractor "github.com/cloudreve/Cloudreve/v4/pkg/searcher/extractor"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	"github.com/gofrs/uuid"
	"github.com/mholt/archives"
)

type (
	ExtractArchiveTask struct {
		*queue.DBTask

		l        logging.Logger
		state    *ExtractArchiveTaskState
		progress queue.Progresses
		node     cluster.Node
	}
	ExtractArchiveTaskPhase string
	ExtractArchiveTaskState struct {
		Uri              string                        `json:"uri,omitempty"`
		Encoding         string                        `json:"encoding,omitempty"`
		Dst              string                        `json:"dst,omitempty"`
		TempPath         string                        `json:"temp_path,omitempty"`
		TempZipFilePath  string                        `json:"temp_zip_file_path,omitempty"`
		ProcessedCursor  string                        `json:"processed_cursor,omitempty"`
		SlaveTaskID      int                           `json:"slave_task_id,omitempty"`
		Password         string                        `json:"password,omitempty"`
		FileMask         []string                      `json:"file_mask,omitempty"`
		PublicVisibility *publicshare.VisibilityResult `json:"public_visibility,omitempty"`
		NodeState        `json:",inline"`
		Phase            ExtractArchiveTaskPhase `json:"phase,omitempty"`
	}
)

const (
	ExtractArchivePhaseNotStarted         ExtractArchiveTaskPhase = ""
	ExtractArchivePhaseDownloadZip        ExtractArchiveTaskPhase = "download_zip"
	ExtractArchivePhaseAwaitSlaveComplete ExtractArchiveTaskPhase = "await_slave_complete"

	ProgressTypeExtractCount = "extract_count"
	ProgressTypeExtractSize  = "extract_size"
	ProgressTypeDownload     = "download"

	SummaryKeySrc         = "src"
	SummaryKeySrcPhysical = "src_physical"
	SummaryKeyDst         = "dst"
)

var errArchiveTempDownloadRequired = errors.New("archive temp download required")

type (
	archiveExtractionSource interface {
		io.ReadSeekCloser
		io.ReaderAt

		Entity() fs.Entity
		IsLocal() bool
	}

	preparedArchiveExtraction struct {
		format       archives.Format
		extractor    archives.Extractor
		decompressor archives.Decompressor
		readStream   io.Reader
		closer       io.Closer
	}

	archiveCreateDirFunc  func(context.Context, *fs.URI) error
	archiveUploadFileFunc func(context.Context, *fs.URI, int64, *time.Time, io.ReadCloser) error
)

func init() {
	queue.RegisterResumableTaskFactory(queue.ExtractArchiveTaskType, NewExtractArchiveTaskFromModel)
}

// NewExtractArchiveTask creates a new ExtractArchiveTask
func NewExtractArchiveTask(ctx context.Context, src, dst, encoding, password string, mask []string, visibility *publicshare.VisibilityResult) (queue.Task, error) {
	state := &ExtractArchiveTaskState{
		Uri:              src,
		Dst:              dst,
		Encoding:         encoding,
		NodeState:        NodeState{},
		Password:         password,
		FileMask:         mask,
		PublicVisibility: visibility,
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal state: %w", err)
	}

	t := &ExtractArchiveTask{
		DBTask: &queue.DBTask{
			Task: &ent.Task{
				Type:          queue.ExtractArchiveTaskType,
				CorrelationID: logging.NillableCorrelationID(ctx),
				PrivateState:  string(stateBytes),
				PublicState:   &types.TaskPublicState{},
			},
			DirectOwner: inventory.UserFromContext(ctx),
		},
	}
	return t, nil
}

func NewExtractArchiveTaskFromModel(task *ent.Task) queue.Task {
	return &ExtractArchiveTask{
		DBTask: &queue.DBTask{
			Task: task,
		},
	}
}

func (m *ExtractArchiveTask) Do(ctx context.Context) (task.Status, error) {
	dep := dependency.FromContext(ctx)
	m.l = dep.Logger()

	m.Lock()
	if m.progress == nil {
		m.progress = make(queue.Progresses)
	}
	m.Unlock()

	// unmarshal state
	state := &ExtractArchiveTaskState{}
	if err := json.Unmarshal([]byte(m.State()), state); err != nil {
		return task.StatusError, fmt.Errorf("failed to unmarshal state: %w", err)
	}
	m.state = state
	if m.state.PublicVisibility != nil {
		ctx = context.WithValue(ctx, publicshare.VisibilityOverrideCtx{}, m.state.PublicVisibility)
	}

	// select node
	node, err := allocateNode(ctx, dep, &m.state.NodeState, types.NodeCapabilityExtractArchive)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to allocate node: %w", err)
	}
	m.node = node

	next := task.StatusCompleted

	if node.IsMaster() {
		switch m.state.Phase {
		case ExtractArchivePhaseNotStarted:
			next, err = m.masterExtractArchive(ctx, dep)
		case ExtractArchivePhaseDownloadZip:
			next, err = m.masterDownloadZip(ctx, dep)
		default:
			next, err = task.StatusError, fmt.Errorf("unknown phase %q: %w", m.state.Phase, queue.CriticalErr)
		}
	} else {
		switch m.state.Phase {
		case ExtractArchivePhaseNotStarted:
			next, err = m.createSlaveExtractTask(ctx, dep)
		case ExtractArchivePhaseAwaitSlaveComplete:
			next, err = m.awaitSlaveExtractComplete(ctx, dep)
		default:
			next, err = task.StatusError, fmt.Errorf("unknown phase %q: %w", m.state.Phase, queue.CriticalErr)
		}
	}

	newStateStr, marshalErr := json.Marshal(m.state)
	if marshalErr != nil {
		return task.StatusError, fmt.Errorf("failed to marshal state: %w", marshalErr)
	}

	m.Lock()
	m.Task.PrivateState = string(newStateStr)
	m.Unlock()
	return next, err
}

func (p *preparedArchiveExtraction) Close() error {
	if p == nil || p.closer == nil {
		return nil
	}

	return p.closer.Close()
}

func prepareArchiveExtraction(
	ctx context.Context,
	dep dependency.Dep,
	l logging.Logger,
	fileName string,
	source archiveExtractionSource,
	tempArchivePath string,
	encoding string,
	password string,
) (*preparedArchiveExtraction, error) {
	format, readStream, err := archives.Identify(ctx, fileName, source)
	if err == nil {
		if extractor, ok := format.(archives.Extractor); ok {
			if requiresArchiveRandomAccess(format) {
				if source.IsLocal() {
					if _, err := source.Seek(0, io.SeekStart); err != nil {
						return nil, fmt.Errorf("failed to seek entity source: %w", err)
					}
					readStream = source
				} else {
					if tempArchivePath == "" {
						return nil, errArchiveTempDownloadRequired
					}

					archiveFile, err := os.Open(tempArchivePath)
					if err != nil {
						return nil, fmt.Errorf("failed to open temp archive file: %w", err)
					}

					return &preparedArchiveExtraction{
						format:     format,
						extractor:  applyArchiveExtractorOptions(extractor, encoding, password, l),
						readStream: archiveFile,
						closer:     archiveFile,
					}, nil
				}
			}

			return &preparedArchiveExtraction{
				format:     format,
				extractor:  applyArchiveExtractorOptions(extractor, encoding, password, l),
				readStream: readStream,
			}, nil
		}

		if decompressor, ok := format.(archives.Decompressor); ok {
			return &preparedArchiveExtraction{
				format:       format,
				decompressor: decompressor,
				readStream:   readStream,
			}, nil
		}
	}

	localErr := err
	if localErr == nil && format != nil {
		localErr = fmt.Errorf("not supported archive format %q", format.Extension())
	}

	if _, err := source.Seek(0, io.SeekStart); err != nil {
		if localErr != nil {
			return nil, localErr
		}
		return nil, fmt.Errorf("failed to seek entity source: %w", err)
	}

	tika, err := manager.BuildArchiveTikaExtractor(ctx, dep)
	if err != nil {
		if localErr != nil {
			return nil, localErr
		}
		return nil, err
	}

	raw, err := tika.UnpackAllFile(ctx, source, fileName, tikaextractor.ArtifactOptions{})
	if err != nil {
		if localErr != nil {
			return nil, fmt.Errorf("failed to identify archive locally: %w; tika fallback failed: %v", localErr, err)
		}
		return nil, fmt.Errorf("failed to unpack archive with tika: %w", err)
	}

	reader := bytes.NewReader(raw)
	return &preparedArchiveExtraction{
		format:     archives.Zip{},
		extractor:  archives.Zip{},
		readStream: reader,
	}, nil
}

func requiresArchiveRandomAccess(format archives.Format) bool {
	if format == nil {
		return false
	}

	switch strings.ToLower(format.Extension()) {
	case ".zip", ".7z":
		return true
	default:
		return false
	}
}

func applyArchiveExtractorOptions(extractor archives.Extractor, encoding string, password string, l logging.Logger) archives.Extractor {
	if zipExtractor, ok := extractor.(archives.Zip); ok {
		if encoding != "" {
			l.Info("Using encoding %q for zip archive", encoding)
			textEncoding, ok := manager.ResolveZipTextEncoding(encoding)
			if !ok {
				l.Warning("Unknown encoding %q, fallback to default encoding", encoding)
			} else {
				zipExtractor.TextEncoding = textEncoding
				extractor = zipExtractor
			}
		}
	} else if rarExtractor, ok := extractor.(archives.Rar); ok && password != "" {
		rarExtractor.Password = password
		extractor = rarExtractor
	} else if sevenZipExtractor, ok := extractor.(archives.SevenZip); ok && password != "" {
		sevenZipExtractor.Password = password
		extractor = sevenZipExtractor
	}

	return extractor
}

func addArchiveProgress(progress *queue.Progress, diff int64) {
	if progress == nil {
		return
	}

	atomic.AddInt64(&progress.Current, diff)
}

func extractArchiveEntries(
	ctx context.Context,
	extractor archives.Extractor,
	readStream io.Reader,
	dst *fs.URI,
	processedCursor *string,
	fileMask []string,
	countProgress *queue.Progress,
	sizeProgress *queue.Progress,
	l logging.Logger,
	createDir archiveCreateDirFunc,
	uploadFile archiveUploadFileFunc,
) error {
	needSkipToCursor := processedCursor != nil && *processedCursor != ""
	var (
		regularFilesSeen      int
		regularFilesExtracted int
		firstOpenErr          error
	)

	err := extractor.Extract(ctx, readStream, func(ctx context.Context, f archives.FileInfo) error {
		if needSkipToCursor && f.NameInArchive != *processedCursor {
			addArchiveProgress(countProgress, 1)
			addArchiveProgress(sizeProgress, f.Size())
			l.Info("File %q already processed, skipping...", f.NameInArchive)
			return nil
		}

		if processedCursor != nil && *processedCursor == f.NameInArchive {
			addArchiveProgress(countProgress, 1)
			addArchiveProgress(sizeProgress, f.Size())
			needSkipToCursor = false
			return nil
		}

		rawPath := util.FormSlash(f.NameInArchive)
		savePath := dst.JoinRaw(rawPath)

		if len(fileMask) > 0 && !isFileInMask(rawPath, fileMask) {
			l.Debug("File %q is not in the mask, skipping...", f.NameInArchive)
			addArchiveProgress(countProgress, 1)
			addArchiveProgress(sizeProgress, f.Size())
			return nil
		}

		if !strings.HasPrefix(savePath.Path(), util.FillSlash(path.Clean(dst.Path()))) {
			l.Warning("Path %q is not legit, skipping...", f.NameInArchive)
			addArchiveProgress(countProgress, 1)
			addArchiveProgress(sizeProgress, f.Size())
			return nil
		}

		if f.FileInfo.IsDir() {
			if err := createDir(ctx, savePath); err != nil {
				l.Warning("Failed to create directory %q: %s, skipping...", rawPath, err)
			}

			addArchiveProgress(countProgress, 1)
			if processedCursor != nil {
				*processedCursor = f.NameInArchive
			}
			return nil
		}

		if !f.Mode().IsRegular() {
			l.Warning("Skipping special archive entry %q with mode %q", rawPath, f.Mode())
			addArchiveProgress(countProgress, 1)
			addArchiveProgress(sizeProgress, f.Size())
			if processedCursor != nil {
				*processedCursor = f.NameInArchive
			}
			return nil
		}

		regularFilesSeen++
		fileStream, err := f.Open()
		if err != nil {
			if firstOpenErr == nil {
				firstOpenErr = fmt.Errorf("failed to open file %q in archive file: %w", rawPath, err)
			}
			l.Warning("Failed to open file %q in archive file: %s, skipping...", rawPath, err)
			return nil
		}
		defer fileStream.Close()

		modTime := f.FileInfo.ModTime().Local()
		if err := uploadFile(ctx, savePath, f.Size(), &modTime, fileStream); err != nil {
			return fmt.Errorf("failed to upload file %q in archive file: %w", rawPath, err)
		}

		regularFilesExtracted++
		addArchiveProgress(countProgress, 1)
		if processedCursor != nil {
			*processedCursor = f.NameInArchive
		}
		return nil
	})
	if err != nil {
		return err
	}
	if regularFilesSeen > 0 && regularFilesExtracted == 0 && firstOpenErr != nil {
		return firstOpenErr
	}
	return nil
}

func extractCompressedArchiveFile(
	ctx context.Context,
	decompressor archives.Decompressor,
	readStream io.Reader,
	archiveName string,
	formatExtension string,
	dst *fs.URI,
	processedCursor *string,
	fileMask []string,
	countProgress *queue.Progress,
	l logging.Logger,
	uploadFile archiveUploadFileFunc,
) error {
	rawPath := manager.SingleCompressedArchiveEntryName(archiveName, formatExtension)
	if processedCursor != nil && *processedCursor == rawPath {
		addArchiveProgress(countProgress, 1)
		return nil
	}

	savePath := dst.JoinRaw(rawPath)
	if len(fileMask) > 0 && !isFileInMask(rawPath, fileMask) {
		l.Debug("File %q is not in the mask, skipping...", rawPath)
		addArchiveProgress(countProgress, 1)
		return nil
	}

	if !strings.HasPrefix(savePath.Path(), util.FillSlash(path.Clean(dst.Path()))) {
		l.Warning("Path %q is not legit, skipping...", rawPath)
		addArchiveProgress(countProgress, 1)
		return nil
	}

	fileStream, err := decompressor.OpenReader(readStream)
	if err != nil {
		return fmt.Errorf("failed to open compressed file %q: %w", archiveName, err)
	}
	defer fileStream.Close()

	tempFile, err := os.CreateTemp("", "cloudreve-archive-single-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file for %q: %w", archiveName, err)
	}
	tempName := tempFile.Name()
	defer func() {
		tempFile.Close()
		_ = os.Remove(tempName)
	}()

	size, err := io.Copy(tempFile, fileStream)
	if err != nil {
		return fmt.Errorf("failed to materialize compressed file %q: %w", archiveName, err)
	}

	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("failed to rewind temp file for %q: %w", archiveName, err)
	}

	if err := uploadFile(ctx, savePath, size, nil, tempFile); err != nil {
		return fmt.Errorf("failed to upload decompressed file %q: %w", rawPath, err)
	}

	addArchiveProgress(countProgress, 1)
	if processedCursor != nil {
		*processedCursor = rawPath
	}
	return nil
}

func (m *ExtractArchiveTask) createSlaveExtractTask(ctx context.Context, dep dependency.Dep) (task.Status, error) {
	uri, err := fs.NewUriFromString(m.state.Uri)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to parse src uri: %s (%w)", err, queue.CriticalErr)
	}

	user := inventory.UserFromContext(ctx)
	fm := manager.NewFileManager(dep, user)

	// Get entity source to extract
	archiveFile, err := fm.Get(ctx, uri, dbfs.WithFileEntities(), dbfs.WithRequiredCapabilities(dbfs.NavigatorCapabilityDownloadFile), dbfs.WithNotRoot())
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to get archive file: %s (%w)", err, queue.CriticalErr)
	}

	// Validate file size
	if user.Edges.Group.Settings.DecompressSize > 0 && archiveFile.Size() > user.Edges.Group.Settings.DecompressSize {
		return task.StatusError,
			fmt.Errorf("file size %d exceeds the limit %d (%w)", archiveFile.Size(), user.Edges.Group.Settings.DecompressSize, queue.CriticalErr)
	}

	// Create slave task
	storagePolicyClient := dep.StoragePolicyClient()
	policy, err := storagePolicyClient.GetPolicyByID(ctx, archiveFile.PrimaryEntity().PolicyID())
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to get policy: %w", err)
	}

	masterKey, _ := dep.MasterEncryptKeyVault(ctx).GetMasterKey(ctx)
	entityModel, err := decryptEntityKeyIfNeeded(masterKey, archiveFile.PrimaryEntity().Model())
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to decrypt entity key for archive file %q: %s", archiveFile.DisplayName(), err)
	}

	payload := &SlaveExtractArchiveTaskState{
		FileName:         archiveFile.DisplayName(),
		Entity:           entityModel,
		Policy:           policy,
		Encoding:         m.state.Encoding,
		Dst:              m.state.Dst,
		UserID:           user.ID,
		Password:         m.state.Password,
		FileMask:         m.state.FileMask,
		PublicVisibility: m.state.PublicVisibility,
	}

	payloadStr, err := json.Marshal(payload)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to marshal payload: %w", err)
	}

	taskId, err := m.node.CreateTask(ctx, queue.SlaveExtractArchiveType, string(payloadStr))
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to create slave task: %w", err)
	}

	m.state.Phase = ExtractArchivePhaseAwaitSlaveComplete
	m.state.SlaveTaskID = taskId
	m.ResumeAfter((10 * time.Second))
	return task.StatusSuspending, nil
}

func (m *ExtractArchiveTask) awaitSlaveExtractComplete(ctx context.Context, dep dependency.Dep) (task.Status, error) {
	t, err := m.node.GetTask(ctx, m.state.SlaveTaskID, true)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to get slave task: %w", err)
	}

	m.Lock()
	m.state.NodeState.progress = t.Progress
	m.Unlock()

	if t.Status == task.StatusError {
		return task.StatusError, fmt.Errorf("slave task failed: %s (%w)", t.Error, queue.CriticalErr)
	}

	if t.Status == task.StatusCanceled {
		return task.StatusError, fmt.Errorf("slave task canceled (%w)", queue.CriticalErr)
	}

	if t.Status == task.StatusCompleted {
		return task.StatusCompleted, nil
	}

	m.l.Info("Slave task %d is still compressing, resume after 30s.", m.state.SlaveTaskID)
	m.ResumeAfter((time.Second * 30))
	return task.StatusSuspending, nil
}

func (m *ExtractArchiveTask) masterExtractArchive(ctx context.Context, dep dependency.Dep) (task.Status, error) {
	uri, err := fs.NewUriFromString(m.state.Uri)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to parse src uri: %s (%w)", err, queue.CriticalErr)
	}

	dst, err := fs.NewUriFromString(m.state.Dst)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to parse dst uri: %s (%w)", err, queue.CriticalErr)
	}

	user := inventory.UserFromContext(ctx)
	fm := manager.NewFileManager(dep, user)

	// Get entity source to extract
	archiveFile, err := fm.Get(ctx, uri, dbfs.WithFileEntities(), dbfs.WithRequiredCapabilities(dbfs.NavigatorCapabilityDownloadFile), dbfs.WithNotRoot())
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to get archive file: %s (%w)", err, queue.CriticalErr)
	}

	// Validate file size
	if user.Edges.Group.Settings.DecompressSize > 0 && archiveFile.Size() > user.Edges.Group.Settings.DecompressSize {
		return task.StatusError,
			fmt.Errorf("file size %d exceeds the limit %d (%w)", archiveFile.Size(), user.Edges.Group.Settings.DecompressSize, queue.CriticalErr)
	}

	es, err := fm.GetEntitySource(ctx, 0, fs.WithEntity(archiveFile.PrimaryEntity()))
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to get entity source: %w", err)
	}

	defer es.Close()

	m.l.Info("Extracting archive %q to %q", uri, m.state.Dst)
	prepared, err := prepareArchiveExtraction(ctx, dep, m.l, archiveFile.DisplayName(), es, m.state.TempZipFilePath, m.state.Encoding, m.state.Password)
	if errors.Is(err, errArchiveTempDownloadRequired) {
		m.state.Phase = ExtractArchivePhaseDownloadZip
		m.ResumeAfter(0)
		return task.StatusSuspending, nil
	}
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to prepare archive extraction: %w", err)
	}
	defer prepared.Close()

	m.l.Info("Archive file %q format identified as %q", uri, prepared.format.Extension())

	m.Lock()
	m.progress[ProgressTypeExtractCount] = &queue.Progress{}
	m.progress[ProgressTypeExtractSize] = &queue.Progress{}
	countProgress := m.progress[ProgressTypeExtractCount]
	sizeProgress := m.progress[ProgressTypeExtractSize]
	m.Unlock()

	uploadFile := func(ctx context.Context, savePath *fs.URI, size int64, lastModified *time.Time, file io.ReadCloser) error {
		fileData := &fs.UploadRequest{
			Props: &fs.UploadProps{
				Uri:          savePath,
				Size:         size,
				LastModified: lastModified,
			},
			ProgressFunc: func(current, diff int64, total int64) {
				addArchiveProgress(sizeProgress, diff)
			},
			File: file,
		}

		_, err := fm.Update(ctx, fileData, fs.WithNoEntityType())
		return err
	}

	if prepared.extractor != nil {
		err = extractArchiveEntries(
			ctx,
			prepared.extractor,
			prepared.readStream,
			dst,
			&m.state.ProcessedCursor,
			m.state.FileMask,
			countProgress,
			sizeProgress,
			m.l,
			func(ctx context.Context, savePath *fs.URI) error {
				_, err := fm.Create(ctx, savePath, types.FileTypeFolder)
				return err
			},
			uploadFile,
		)
	} else {
		err = extractCompressedArchiveFile(
			ctx,
			prepared.decompressor,
			prepared.readStream,
			archiveFile.DisplayName(),
			prepared.format.Extension(),
			dst,
			&m.state.ProcessedCursor,
			m.state.FileMask,
			countProgress,
			m.l,
			uploadFile,
		)
	}

	if err != nil {
		return task.StatusError, fmt.Errorf("failed to extract archive: %w", err)
	}

	return task.StatusCompleted, nil
}

func (m *ExtractArchiveTask) masterDownloadZip(ctx context.Context, dep dependency.Dep) (task.Status, error) {
	uri, err := fs.NewUriFromString(m.state.Uri)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to parse src uri: %s (%w)", err, queue.CriticalErr)
	}

	user := inventory.UserFromContext(ctx)
	fm := manager.NewFileManager(dep, user)

	// Get entity source to extract
	archiveFile, err := fm.Get(ctx, uri, dbfs.WithFileEntities(), dbfs.WithRequiredCapabilities(dbfs.NavigatorCapabilityDownloadFile), dbfs.WithNotRoot())
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to get archive file: %s (%w)", err, queue.CriticalErr)
	}

	es, err := fm.GetEntitySource(ctx, 0, fs.WithEntity(archiveFile.PrimaryEntity()))
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to get entity source: %w", err)
	}

	defer es.Close()

	// For non-local entity, we need to download the whole zip file first
	tempPath, err := prepareTempFolder(ctx, dep, m)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to prepare temp folder: %w", err)
	}
	m.state.TempPath = tempPath

	fileName := fmt.Sprintf("%s.zip", uuid.Must(uuid.NewV4()))
	zipFilePath := filepath.Join(
		m.state.TempPath,
		fileName,
	)

	zipFile, err := util.CreatNestedFile(zipFilePath)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to create zip file: %w", err)
	}

	m.Lock()
	m.progress[ProgressTypeDownload] = &queue.Progress{Total: es.Entity().Size()}
	m.Unlock()

	defer zipFile.Close()
	if _, err := io.Copy(zipFile, util.NewCallbackReader(es, func(i int64) {
		atomic.AddInt64(&m.progress[ProgressTypeDownload].Current, i)
	})); err != nil {
		zipFile.Close()
		if err := os.Remove(zipFilePath); err != nil {
			m.l.Warning("Failed to remove temp zip file %q: %s", zipFilePath, err)
		}
		return task.StatusError, fmt.Errorf("failed to copy zip file to local temp: %w", err)
	}

	m.Lock()
	delete(m.progress, ProgressTypeDownload)
	m.Unlock()
	m.state.TempZipFilePath = zipFilePath
	m.state.Phase = ExtractArchivePhaseNotStarted
	m.ResumeAfter(0)
	return task.StatusSuspending, nil
}

func (m *ExtractArchiveTask) Summarize(hasher hashid.Encoder) *queue.Summary {
	if m.state == nil {
		if err := json.Unmarshal([]byte(m.State()), &m.state); err != nil {
			return nil
		}
	}

	return &queue.Summary{
		NodeID: m.state.NodeID,
		Phase:  string(m.state.Phase),
		Props: map[string]any{
			SummaryKeySrc: m.state.Uri,
			SummaryKeyDst: m.state.Dst,
		},
	}
}

func (m *ExtractArchiveTask) Progress(ctx context.Context) queue.Progresses {
	m.Lock()
	defer m.Unlock()

	if m.state.NodeState.progress != nil {
		merged := make(queue.Progresses)
		for k, v := range m.progress {
			merged[k] = v
		}

		for k, v := range m.state.NodeState.progress {
			merged[k] = v
		}

		return merged
	}
	return m.progress
}

func (m *ExtractArchiveTask) Cleanup(ctx context.Context) error {
	if m.state.TempPath != "" {
		time.Sleep(time.Duration(1) * time.Second)
		return os.RemoveAll(m.state.TempPath)
	}

	return nil
}

type (
	SlaveExtractArchiveTask struct {
		*queue.InMemoryTask

		l        logging.Logger
		state    *SlaveExtractArchiveTaskState
		progress queue.Progresses
		node     cluster.Node
	}

	SlaveExtractArchiveTaskState struct {
		FileName         string                        `json:"file_name"`
		Entity           *ent.Entity                   `json:"entity"`
		Policy           *ent.StoragePolicy            `json:"policy"`
		Encoding         string                        `json:"encoding,omitempty"`
		Dst              string                        `json:"dst,omitempty"`
		UserID           int                           `json:"user_id"`
		TempPath         string                        `json:"temp_path,omitempty"`
		TempZipFilePath  string                        `json:"temp_zip_file_path,omitempty"`
		ProcessedCursor  string                        `json:"processed_cursor,omitempty"`
		Password         string                        `json:"password,omitempty"`
		FileMask         []string                      `json:"file_mask,omitempty"`
		PublicVisibility *publicshare.VisibilityResult `json:"public_visibility,omitempty"`
	}
)

// NewSlaveExtractArchiveTask creates a new SlaveExtractArchiveTask from raw private state
func NewSlaveExtractArchiveTask(ctx context.Context, props *types.SlaveTaskProps, id int, state string) queue.Task {
	return &SlaveExtractArchiveTask{
		InMemoryTask: &queue.InMemoryTask{
			DBTask: &queue.DBTask{
				Task: &ent.Task{
					ID:            id,
					CorrelationID: logging.NillableCorrelationID(ctx),
					PublicState: &types.TaskPublicState{
						SlaveTaskProps: props,
					},
					PrivateState: state,
				},
			},
		},

		progress: make(queue.Progresses),
	}
}

func (m *SlaveExtractArchiveTask) Do(ctx context.Context) (task.Status, error) {
	ctx = prepareSlaveTaskCtx(ctx, m.Model().PublicState.SlaveTaskProps)
	dep := dependency.FromContext(ctx)
	m.l = dep.Logger()
	np, err := dep.NodePool(ctx)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to get node pool: %w", err)
	}

	m.node, err = np.Get(ctx, types.NodeCapabilityNone, 0)
	if err != nil || !m.node.IsMaster() {
		return task.StatusError, fmt.Errorf("failed to get master node: %w", err)
	}

	fm := manager.NewFileManager(dep, nil)

	// unmarshal state
	state := &SlaveExtractArchiveTaskState{}
	if err := json.Unmarshal([]byte(m.State()), state); err != nil {
		return task.StatusError, fmt.Errorf("failed to unmarshal state: %w", err)
	}

	m.state = state
	if m.state.PublicVisibility != nil {
		ctx = context.WithValue(ctx, publicshare.VisibilityOverrideCtx{}, m.state.PublicVisibility)
	}
	m.Lock()
	if m.progress == nil {
		m.progress = make(queue.Progresses)
	}
	m.progress[ProgressTypeExtractCount] = &queue.Progress{}
	m.progress[ProgressTypeExtractSize] = &queue.Progress{}
	m.Unlock()

	dst, err := fs.NewUriFromString(m.state.Dst)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to parse dst uri: %s (%w)", err, queue.CriticalErr)
	}

	// 1. Get entity source
	entity := fs.NewEntity(m.state.Entity)
	es, err := fm.GetEntitySource(ctx, 0, fs.WithEntity(entity), fs.WithPolicy(fm.CastStoragePolicyOnSlave(ctx, m.state.Policy)))
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to get entity source: %w", err)
	}

	defer es.Close()

	prepared, err := prepareArchiveExtraction(ctx, dep, m.l, m.state.FileName, es, m.state.TempZipFilePath, m.state.Encoding, m.state.Password)
	if errors.Is(err, errArchiveTempDownloadRequired) {
		if _, err = es.Seek(0, io.SeekStart); err != nil {
			return task.StatusError, fmt.Errorf("failed to seek entity source: %w", err)
		}

		tempPath, err := prepareTempFolder(ctx, dep, m)
		if err != nil {
			return task.StatusError, fmt.Errorf("failed to prepare temp folder: %w", err)
		}
		m.state.TempPath = tempPath

		fileName := fmt.Sprintf("%s.zip", uuid.Must(uuid.NewV4()))
		zipFilePath := filepath.Join(
			m.state.TempPath,
			fileName,
		)
		zipFile, err := util.CreatNestedFile(zipFilePath)
		if err != nil {
			return task.StatusError, fmt.Errorf("failed to create zip file: %w", err)
		}

		m.Lock()
		m.progress[ProgressTypeDownload] = &queue.Progress{Total: es.Entity().Size()}
		m.Unlock()

		defer zipFile.Close()
		if _, err := io.Copy(zipFile, util.NewCallbackReader(es, func(i int64) {
			addArchiveProgress(m.progress[ProgressTypeDownload], i)
		})); err != nil {
			return task.StatusError, fmt.Errorf("failed to copy zip file to local temp: %w", err)
		}

		zipFile.Close()
		m.state.TempZipFilePath = zipFilePath
		m.Lock()
		delete(m.progress, ProgressTypeDownload)
		m.Unlock()
		if _, err = es.Seek(0, io.SeekStart); err != nil {
			return task.StatusError, fmt.Errorf("failed to rewind entity source after local temp download: %w", err)
		}

		prepared, err = prepareArchiveExtraction(ctx, dep, m.l, m.state.FileName, es, m.state.TempZipFilePath, m.state.Encoding, m.state.Password)
	}
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to prepare archive extraction: %w", err)
	}
	defer prepared.Close()

	m.l.Info("Archive file %q format identified as %q", m.state.FileName, prepared.format.Extension())

	countProgress := m.progress[ProgressTypeExtractCount]
	sizeProgress := m.progress[ProgressTypeExtractSize]
	uploadFile := func(ctx context.Context, savePath *fs.URI, size int64, lastModified *time.Time, file io.ReadCloser) error {
		fileData := &fs.UploadRequest{
			Props: &fs.UploadProps{
				Uri:          savePath,
				Size:         size,
				LastModified: lastModified,
			},
			ProgressFunc: func(current, diff int64, total int64) {
				addArchiveProgress(sizeProgress, diff)
			},
			File: file,
		}

		_, err := fm.Update(ctx, fileData, fs.WithNode(m.node), fs.WithStatelessUserID(m.state.UserID), fs.WithNoEntityType())
		return err
	}

	if prepared.extractor != nil {
		err = extractArchiveEntries(
			ctx,
			prepared.extractor,
			prepared.readStream,
			dst,
			&m.state.ProcessedCursor,
			m.state.FileMask,
			countProgress,
			sizeProgress,
			m.l,
			func(ctx context.Context, savePath *fs.URI) error {
				_, err := fm.Create(ctx, savePath, types.FileTypeFolder, fs.WithNode(m.node), fs.WithStatelessUserID(m.state.UserID))
				return err
			},
			uploadFile,
		)
	} else {
		err = extractCompressedArchiveFile(
			ctx,
			prepared.decompressor,
			prepared.readStream,
			m.state.FileName,
			prepared.format.Extension(),
			dst,
			&m.state.ProcessedCursor,
			m.state.FileMask,
			countProgress,
			m.l,
			uploadFile,
		)
	}

	if err != nil {
		return task.StatusError, fmt.Errorf("failed to extract archive: %w", err)
	}

	return task.StatusCompleted, nil
}

func (m *SlaveExtractArchiveTask) Cleanup(ctx context.Context) error {
	if m.state.TempPath != "" {
		time.Sleep(time.Duration(1) * time.Second)
		return os.RemoveAll(m.state.TempPath)
	}

	return nil
}

func (m *SlaveExtractArchiveTask) Progress(ctx context.Context) queue.Progresses {
	m.Lock()
	defer m.Unlock()
	return m.progress
}

func isFileInMask(path string, mask []string) bool {
	if len(mask) == 0 {
		return true
	}

	for _, m := range mask {
		if path == m || strings.HasPrefix(path, m+"/") {
			return true
		}
	}

	return false
}
