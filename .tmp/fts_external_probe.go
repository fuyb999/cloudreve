package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	appsetting "github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
)

func main() {
	util.UseWorkingDir = true
	logger := logging.NewConsoleLogger(logging.LevelDebug)
	dep := dependency.NewDependency(
		dependency.WithConfigPath(".tmp/fts_real_smoke.ini"),
		dependency.WithLogger(logger),
		dependency.WithRequiredDbVersion(constants.BackendVersion),
	)
	defer dep.Shutdown(context.Background())

	ctx := context.WithValue(context.Background(), dependency.DepCtx{}, dep)
	ctx = context.WithValue(ctx, inventory.LoadTaskUser{}, true)
	reloadCtx := context.WithValue(ctx, dependency.ReloadCtx{}, true)

	if err := ensureSmokeFTSSettings(ctx, dep); err != nil {
		panic(fmt.Sprintf("prepare smoke fts settings: %v", err))
	}

	if err := manager.ReloadFTSExternalKafka(reloadCtx, dep); err != nil {
		panic(fmt.Sprintf("reload external kafka: %v", err))
	}
	contentQueue := dep.ContentProcessingQueue(reloadCtx)
	contentQueue.Start()
	defer contentQueue.Shutdown()

	user, err := dep.UserClient().GetLoginUserByID(ctx, 1)
	if err != nil {
		panic(fmt.Sprintf("load user: %v", err))
	}
	ctx = context.WithValue(ctx, inventory.UserCtx{}, user)
	ctx = context.WithValue(ctx, inventory.UserIDCtx{}, user.ID)

	fm := manager.NewFileManager(dep, user)
	defer fm.Recycle()

	suffix := time.Now().Format("20060102_150405")
	dir, err := fs.NewUriFromString("cloudreve://my/__fts_external_probe_" + suffix)
	if err != nil {
		panic(fmt.Sprintf("build dir uri: %v", err))
	}
	if _, err = fm.Create(ctx, dir, types.FileTypeFolder); err != nil {
		panic(fmt.Sprintf("create dir: %v", err))
	}

	content := "external kafka probe " + suffix
	dst := dir.Join("probe.txt")
	reader := bytes.NewReader([]byte(content))
	req := &fs.UploadRequest{
		Props:  &fs.UploadProps{Uri: dst, Size: int64(len(content))},
		File:   io.NopCloser(reader),
		Seeker: reader,
	}
	file, err := fm.Update(ctx, req)
	if err != nil {
		panic(fmt.Sprintf("upload file: %v", err))
	}

	fmt.Printf("uploaded file_id=%d uri=%s\n", file.ID(), dst.String())
	fmt.Println("waiting 12s for external process publish...")
	time.Sleep(12 * time.Second)
	fmt.Println("probe done")
}

func ensureSmokeFTSSettings(ctx context.Context, dep dependency.Dep) error {
	settings := map[string]string{
		"siteURL":                         "http://127.0.0.1:5212",
		"fts_enabled":                     "1",
		"fts_index_type":                  "elasticsearch",
		"fts_extractor_type":              "tika",
		"fts_elasticsearch_endpoint":      "http://127.0.0.1:9200",
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
	return dep.KV().Delete(appsetting.KvSettingPrefix, keys...)
}
