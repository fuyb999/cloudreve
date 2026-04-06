package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/IBM/sarama"
	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	entftsexternaljob "github.com/cloudreve/Cloudreve/v4/ent/ftsexternaljob"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	appsetting "github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
)

type roundtripResultMessage struct {
	Version       int    `json:"version"`
	RequestID     string `json:"request_id"`
	SnapshotToken string `json:"snapshot_token"`
	Status        string `json:"status,omitempty"`
	Provider      struct {
		Name    string `json:"name,omitempty"`
		Version string `json:"version,omitempty"`
	} `json:"provider,omitempty"`
	Root struct {
		Content      string            `json:"content,omitempty"`
		Metadata     map[string]string `json:"metadata,omitempty"`
		Warnings     []string          `json:"warnings,omitempty"`
		QualityScore float64           `json:"quality_score,omitempty"`
	} `json:"root"`
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
	dir, err := fs.NewUriFromString("cloudreve://my/__fts_external_roundtrip_" + suffix)
	if err != nil {
		panic(fmt.Sprintf("build dir uri: %v", err))
	}
	if _, err = fm.Create(ctx, dir, types.FileTypeFolder); err != nil {
		panic(fmt.Sprintf("create dir: %v", err))
	}

	content := "external kafka roundtrip " + suffix
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
	fileID := file.ID()
	fmt.Printf("uploaded file_id=%d uri=%s\n", fileID, dst.String())

	job := waitLatestExternalJob(ctx, dep, fileID, 20*time.Second)
	if job == nil {
		panic("external job not created in time")
	}
	fmt.Printf("external job created id=%d request_id=%s status=%s\n", job.ID, job.RequestID, job.Status)

	if err := publishSyntheticExternalResult(job, "127.0.0.1:9092", "result", "loopback result "+suffix); err != nil {
		panic(fmt.Sprintf("publish result: %v", err))
	}
	fmt.Printf("published synthetic result request_id=%s\n", job.RequestID)

	job = waitExternalJobStatus(ctx, dep, job.RequestID, "success", 25*time.Second)
	if job == nil {
		panic("external job was not marked success in time")
	}
	fmt.Printf("external job success request_id=%s\n", job.RequestID)

	job = waitExternalJobManifest(ctx, dep, job.RequestID, 25*time.Second)
	if job == nil {
		panic("external job was not finalized with manifest in time")
	}
	fmt.Printf("external job finalized request_id=%s manifest=%s\n", job.RequestID, job.ManifestPath)

	if !waitESHit(fileID, 25*time.Second) {
		panic(fmt.Sprintf("es document for file_id=%d not found in time", fileID))
	}
	fmt.Printf("es indexed file_id=%d\n", fileID)
}

func waitLatestExternalJob(ctx context.Context, dep dependency.Dep, fileID int, timeout time.Duration) *ent.FTSExternalJob {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		job, err := dep.DBClient().FTSExternalJob.Query().
			Where(entftsexternaljob.FileIDEQ(fileID)).
			Order(ent.Desc(entftsexternaljob.FieldID)).
			First(ctx)
		if err == nil {
			return job
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil
}

func waitExternalJobStatus(ctx context.Context, dep dependency.Dep, requestID, status string, timeout time.Duration) *ent.FTSExternalJob {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		job, err := dep.DBClient().FTSExternalJob.Query().
			Where(entftsexternaljob.RequestIDEQ(requestID)).
			Only(ctx)
		if err == nil && job.Status == status {
			return job
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil
}

func waitExternalJobManifest(ctx context.Context, dep dependency.Dep, requestID string, timeout time.Duration) *ent.FTSExternalJob {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		job, err := dep.DBClient().FTSExternalJob.Query().
			Where(entftsexternaljob.RequestIDEQ(requestID)).
			Only(ctx)
		if err == nil && strings.TrimSpace(job.ManifestPath) != "" {
			return job
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil
}

func waitESHit(fileID int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://127.0.0.1:9200/cloudreve_files/_search?q=file_id:%d", fileID)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr == nil && strings.Contains(string(body), "\"value\":1") {
				return true
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func publishSyntheticExternalResult(job *ent.FTSExternalJob, broker, topic, content string) error {
	cfg := sarama.NewConfig()
	cfg.Version = sarama.V3_7_0_0
	cfg.Producer.RequiredAcks = sarama.WaitForAll
	cfg.Producer.Return.Successes = true
	cfg.Producer.Idempotent = true
	cfg.Net.MaxOpenRequests = 1

	producer, err := sarama.NewSyncProducer([]string{broker}, cfg)
	if err != nil {
		return err
	}
	defer producer.Close()

	message := roundtripResultMessage{
		Version:       1,
		RequestID:     job.RequestID,
		SnapshotToken: job.SnapshotToken,
		Status:        "success",
	}
	message.Provider.Name = "loopback-probe"
	message.Provider.Version = "1.0.0"
	message.Root.Content = content
	message.Root.Metadata = map[string]string{"source": "loopback-probe"}
	message.Root.QualityScore = 0.99

	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}

	_, _, err = producer.SendMessage(&sarama.ProducerMessage{
		Topic: topic,
		Key:   sarama.StringEncoder(job.RequestID),
		Value: sarama.ByteEncoder(raw),
		Headers: []sarama.RecordHeader{
			{Key: []byte("content-type"), Value: []byte("application/json")},
		},
	})
	return err
}

func ensureSmokeFTSSettings(ctx context.Context, dep dependency.Dep) error {
	settings := map[string]string{
		"siteURL":                           "http://127.0.0.1:5212",
		"fts_enabled":                       "1",
		"fts_index_type":                    "elasticsearch",
		"fts_extractor_type":                "tika",
		"fts_external_enabled":              "1",
		"fts_external_mode":                 "primary",
		"fts_external_use_global_kafka":     "0",
		"fts_external_kafka_brokers":        "127.0.0.1:9092",
		"fts_external_kafka_process_topic":  "process",
		"fts_external_kafka_result_topic":   "result",
		"fts_external_kafka_error_topic":    "error",
		"fts_external_kafka_consumer_group": "cloudreve-fts-external",
		"fts_elasticsearch_endpoint":        "http://127.0.0.1:9200",
		"fts_tika_endpoint":                 "http://127.0.0.1:9998",
		"fts_tika_document_enabled":         "1",
		"fts_tika_archive_enabled":          "1",
		"fts_tika_sidecar_enabled":          "1",
		"fts_tika_sidecar_text_enabled":     "1",
		"fts_tika_sidecar_assets_enabled":   "1",
		"fts_tika_extract_inline_images":    "1",
		"fts_tika_document_exts":            "pdf,txt,text,md,markdown,csv,tsv,html,htm,xhtml,xml,rtf,epub,fb2,chm,mif,doc,dot,docx,docm,dotx,dotm,wps,wks,wri,hwp,one,wpd,xls,xlt,xla,xlc,xlm,xlw,xlsx,xlsm,xltx,xltm,xlsb,xlam,qpw,ppt,pps,pot,pptx,pptm,ppsx,ppsm,potx,potm,sldx,sldm,ppam,vsd,vst,vss,vsdx,vstx,vssx,vsdm,vstm,vssm,pub,mpp,xps,dwfx,odt,fodt,ott,odm,oth,ods,fods,ots,odp,fodp,otp,odg,fodg,otg,odc,odf,odb,odi,sxw,stw,sxg,sxc,stc,sxi,sti,sxd,std,sxm,pages,numbers,key,eml,mht,mhtml,nws,msg,pst,mbox,tnef",
		"fts_tika_archive_exts":             "zip,tar,tgz,tbz,tbz2,txz,tlz,7z,rar,ar,gz,z,bz,bz2,xz,lzma,lz4,br,snappy,sz,pack200,cpio,arj,dump,jar,war,ear",
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
