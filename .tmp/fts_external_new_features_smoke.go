package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/IBM/sarama"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	entfile "github.com/cloudreve/Cloudreve/v4/ent/file"
	entftsexternaljob "github.com/cloudreve/Cloudreve/v4/ent/ftsexternaljob"
	entnode "github.com/cloudreve/Cloudreve/v4/ent/node"
	taskmodel "github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/auth"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/kafka"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
)

const (
	baseConfigPath      = ".tmp/fts_real_smoke.ini"
	defaultBroker       = "127.0.0.1:9092"
	defaultSiteURL      = "http://127.0.0.1:5212"
	elasticsearchURL    = "http://127.0.0.1:9200"
	searchIndex         = "cloudreve_files"
	externalPolicyID    = 2
	adminGroupID        = 1
	waitUploadTimeout   = 30 * time.Second
	waitDispatchTimeout = 45 * time.Second
	waitExternalTimeout = 60 * time.Second
	waitIndexTimeout    = 75 * time.Second
	waitProcessTimeout  = 30 * time.Second
	settingsSettleDelay = 4 * time.Second
	resultSettleDelay   = 2 * time.Second
)

type smokeCase struct {
	Name               string
	Mode               string
	OCREnabled         bool
	FileName           string
	Content            []byte
	WantTriggerReason  string
	WantRootContent    string
	WantAttachmentName string
	WantAttachmentPath string
	WantAttachmentType string
	WantAttachmentMime string
	WantAttachmentText string
}

type kafkaTopics struct {
	Process string
	Result  string
	Error   string
	Group   string
}

type apiResponse struct {
	Code  int             `json:"code"`
	Msg   string          `json:"msg"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

type uploadSessionResponse struct {
	SessionID      string   `json:"session_id"`
	ChunkSize      int64    `json:"chunk_size"`
	UploadURLs     []string `json:"upload_urls,omitempty"`
	CompleteURL    string   `json:"completeURL,omitempty"`
	URI            string   `json:"uri,omitempty"`
	CallbackSecret string   `json:"callback_secret,omitempty"`
	StoragePolicy  *struct {
		Type string `json:"type"`
	} `json:"storage_policy,omitempty"`
}

type processTap struct {
	client kafka.Client
	stream chan externalProcessMessage
}

type externalFTSObjectReference struct {
	PolicyID int    `json:"policy_id,omitempty"`
	Bucket   string `json:"bucket,omitempty"`
	Path     string `json:"path,omitempty"`
}

type externalProcessMessage struct {
	Version       int    `json:"version"`
	RequestID     string `json:"request_id"`
	SnapshotToken string `json:"snapshot_token"`
	File          struct {
		FileID   int    `json:"file_id"`
		OwnerID  int    `json:"owner_id"`
		EntityID int    `json:"entity_id"`
		Name     string `json:"name"`
		Size     int64  `json:"size"`
		MimeType string `json:"mime_type,omitempty"`
		Ext      string `json:"ext,omitempty"`
	} `json:"file"`
	Source struct {
		PolicyID int    `json:"policy_id,omitempty"`
		Bucket   string `json:"bucket"`
		Path     string `json:"path"`
	} `json:"source"`
	Options struct {
		RecursiveAttachments bool `json:"recursive_attachments,omitempty"`
		OCREnabled           bool `json:"ocr_enabled,omitempty"`
	} `json:"options,omitempty"`
}

type externalResultMessage struct {
	Version       int    `json:"version"`
	RequestID     string `json:"request_id"`
	SnapshotToken string `json:"snapshot_token"`
	Status        string `json:"status,omitempty"`
	Provider      struct {
		Name    string `json:"name,omitempty"`
		Version string `json:"version,omitempty"`
	} `json:"provider,omitempty"`
	Root struct {
		Content         string                      `json:"content,omitempty"`
		ContentRef      *externalFTSObjectReference `json:"content_ref,omitempty"`
		ContentPolicyID int                         `json:"content_policy_id,omitempty"`
		ContentBucket   string                      `json:"content_bucket,omitempty"`
		ContentPath     string                      `json:"content_path,omitempty"`
		Metadata        map[string]string           `json:"metadata,omitempty"`
		Warnings        []string                    `json:"warnings,omitempty"`
		QualityScore    float64                     `json:"quality_score,omitempty"`
	} `json:"root"`
	Attachments []struct {
		ID              string                      `json:"id,omitempty"`
		ParentID        string                      `json:"parent_id,omitempty"`
		Depth           int                         `json:"depth,omitempty"`
		Type            string                      `json:"type,omitempty"`
		Name            string                      `json:"name,omitempty"`
		Path            string                      `json:"path,omitempty"`
		MimeType        string                      `json:"mime_type,omitempty"`
		Size            int64                       `json:"size,omitempty"`
		Metadata        map[string]string           `json:"metadata,omitempty"`
		Content         string                      `json:"content,omitempty"`
		ContentRef      *externalFTSObjectReference `json:"content_ref,omitempty"`
		ContentPolicyID int                         `json:"content_policy_id,omitempty"`
		ContentBucket   string                      `json:"content_bucket,omitempty"`
		ContentPath     string                      `json:"content_path,omitempty"`
	} `json:"attachments,omitempty"`
}

type esAttachment struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Type    string `json:"type"`
	Mime    string `json:"mime_type"`
	Content string `json:"content"`
}

type esDoc struct {
	Found  bool `json:"found"`
	Source struct {
		FileID      int            `json:"file_id"`
		FileName    string         `json:"file_name"`
		Content     string         `json:"content"`
		Attachments []esAttachment `json:"attachments"`
	} `json:"_source"`
}

type smokeFullTextIndexTaskState struct {
	Phase             string `json:"phase,omitempty"`
	NodeID            int    `json:"node_id,omitempty"`
	SlaveID           int    `json:"slave_id,omitempty"`
	ExternalRequestID string `json:"external_request_id,omitempty"`
}

type slaveDispatchEvidence struct {
	TaskID        int
	NodeID        int
	SlaveID       int
	ExternalJobID string
}

func main() {
	util.UseWorkingDir = true
	dep := dependency.NewDependency(
		dependency.WithConfigPath(baseConfigPath),
		dependency.WithLogger(logging.NewConsoleLogger(logging.LevelDebug)),
		dependency.WithRequiredDbVersion(constants.BackendVersion),
	)
	defer dep.Shutdown(context.Background())

	ctx := context.WithValue(context.Background(), dependency.DepCtx{}, dep)
	ctx = context.WithValue(ctx, inventory.LoadTaskUser{}, true)

	user, err := dep.UserClient().GetLoginUserByID(ctx, 1)
	must(err, "load admin user")
	ctx = context.WithValue(ctx, inventory.UserCtx{}, user)
	ctx = context.WithValue(ctx, inventory.UserIDCtx{}, user.ID)

	token, err := issueAccessToken(ctx, dep, user)
	must(err, "issue access token")

	policy, err := dep.StoragePolicyClient().GetPolicyByID(ctx, externalPolicyID)
	must(err, "load external policy")
	if policy.BucketName == "" {
		panic("external policy bucket is empty")
	}

	originalGroupPolicy, err := readAdminGroupPolicy(ctx, dep)
	must(err, "read admin group policy")
	if originalGroupPolicy != externalPolicyID {
		must(setAdminGroupPolicy(ctx, dep, externalPolicyID), "switch admin group policy to smoke external policy")
	}
	defer func() {
		restoreCtx := context.WithValue(context.Background(), dependency.DepCtx{}, dep)
		if err := setAdminGroupPolicy(restoreCtx, dep, originalGroupPolicy); err != nil {
			fmt.Printf("warning restore admin group policy failed: %v\n", err)
		}
	}()

	restoreKeys := sortedSettingKeys(buildSettings("primary", false, kafkaTopics{}))
	originalSettings, err := fetchAdminSettings(token, restoreKeys)
	must(err, "fetch current admin settings")
	defer func() {
		if len(originalSettings) == 0 {
			return
		}
		if err := restoreAdminSettings(token, originalSettings); err != nil {
			fmt.Printf("warning restore admin settings failed: %v\n", err)
			return
		}
		time.Sleep(settingsSettleDelay)
	}()

	slaveNodeID, err := readSmokeSlaveNodeID(ctx, dep)
	must(err, "read smoke slave node")
	if slaveNodeID == 0 {
		panic("content processing slave __fts_slave_smoke__ is not registered")
	}

	cases := []smokeCase{
		{
			Name:               "content_ref_hydration",
			Mode:               "primary",
			OCREnabled:         false,
			FileName:           "content-ref-probe.txt",
			Content:            []byte("local text should be ignored by referenced external payload"),
			WantTriggerReason:  "primary",
			WantRootContent:    "referenced root content from object storage only",
			WantAttachmentName: "embedded-note.txt",
			WantAttachmentPath: "external/embedded-note.txt",
			WantAttachmentType: "attachment",
			WantAttachmentMime: "text/plain",
			WantAttachmentText: "referenced attachment content from object storage only",
		},
		{
			Name:               "pdf_content_ref_hydration",
			Mode:               "primary",
			OCREnabled:         false,
			FileName:           "content-ref-probe.pdf",
			Content:            buildMinimalPDFWithEmbeddedFile("local pdf text should be replaced", "attached.txt", "local pdf attachment"),
			WantTriggerReason:  "primary",
			WantRootContent:    "referenced root content for pdf sample",
			WantAttachmentName: "attached.txt",
			WantAttachmentPath: "pdf/attached.txt",
			WantAttachmentType: "attachment",
			WantAttachmentMime: "text/plain",
			WantAttachmentText: "referenced attachment content for pdf sample",
		},
		{
			Name:               "eml_content_ref_hydration",
			Mode:               "primary",
			OCREnabled:         false,
			FileName:           "content-ref-probe.eml",
			Content:            buildSmokeEML("FTS external EML smoke", "local eml body should be replaced", "eml-attachment.txt", "local eml attachment"),
			WantTriggerReason:  "primary",
			WantRootContent:    "referenced root content for eml sample",
			WantAttachmentName: "eml-attachment.txt",
			WantAttachmentPath: "mail/eml-attachment.txt",
			WantAttachmentType: "attachment",
			WantAttachmentMime: "text/plain",
			WantAttachmentText: "referenced attachment content for eml sample",
		},
		{
			Name:               "mbox_content_ref_hydration",
			Mode:               "primary",
			OCREnabled:         false,
			FileName:           "content-ref-probe.mbox",
			Content:            buildSmokeMbox(buildSmokeEML("FTS external MBOX smoke", "local mbox body should be replaced", "mbox-attachment.txt", "local mbox attachment")),
			WantTriggerReason:  "primary",
			WantRootContent:    "referenced root content for mbox sample",
			WantAttachmentName: "mbox-attachment.txt",
			WantAttachmentPath: "mail/mbox-attachment.txt",
			WantAttachmentType: "attachment",
			WantAttachmentMime: "text/plain",
			WantAttachmentText: "referenced attachment content for mbox sample",
		},
		{
			Name:               "zip_content_ref_hydration",
			Mode:               "primary",
			OCREnabled:         false,
			FileName:           "content-ref-probe.zip",
			Content:            buildNestedZip("zip outer local text", "zip inner local text"),
			WantTriggerReason:  "primary",
			WantRootContent:    "referenced root content for zip sample",
			WantAttachmentName: "nested.txt",
			WantAttachmentPath: "inner.zip/nested.txt",
			WantAttachmentType: "attachment",
			WantAttachmentMime: "text/plain",
			WantAttachmentText: "referenced attachment content for zip sample",
		},
		{
			Name:               "tgz_content_ref_hydration",
			Mode:               "primary",
			OCREnabled:         false,
			FileName:           "content-ref-probe.tgz",
			Content:            buildNestedTGZ("tgz outer local text", "tgz inner local text"),
			WantTriggerReason:  "primary",
			WantRootContent:    "referenced root content for tgz sample",
			WantAttachmentName: "nested.txt",
			WantAttachmentPath: "inner.zip/nested.txt",
			WantAttachmentType: "attachment",
			WantAttachmentMime: "text/plain",
			WantAttachmentText: "referenced attachment content for tgz sample",
		},
		{
			Name:               "jar_content_ref_hydration",
			Mode:               "primary",
			OCREnabled:         false,
			FileName:           "content-ref-probe.jar",
			Content:            buildNestedJar("jar outer local text", "jar inner local text"),
			WantTriggerReason:  "primary",
			WantRootContent:    "referenced root content for jar sample",
			WantAttachmentName: "nested.txt",
			WantAttachmentPath: "inner.zip/nested.txt",
			WantAttachmentType: "attachment",
			WantAttachmentMime: "text/plain",
			WantAttachmentText: "referenced attachment content for jar sample",
		},
		{
			Name:               "xlsx_content_ref_hydration",
			Mode:               "primary",
			OCREnabled:         false,
			FileName:           "content-ref-probe.xlsx",
			Content:            buildMinimalXLSX("local xlsx text should be replaced"),
			WantTriggerReason:  "primary",
			WantRootContent:    "referenced root content for xlsx sample",
			WantAttachmentName: "sheet-note.txt",
			WantAttachmentPath: "xlsx/sheet-note.txt",
			WantAttachmentType: "attachment",
			WantAttachmentMime: "text/plain",
			WantAttachmentText: "referenced attachment content for xlsx sample",
		},
		{
			Name:               "pptx_content_ref_hydration",
			Mode:               "primary",
			OCREnabled:         false,
			FileName:           "content-ref-probe.pptx",
			Content:            buildMinimalPPTX("local pptx text should be replaced"),
			WantTriggerReason:  "primary",
			WantRootContent:    "referenced root content for pptx sample",
			WantAttachmentName: "slide-note.txt",
			WantAttachmentPath: "ppt/slide-note.txt",
			WantAttachmentType: "attachment",
			WantAttachmentMime: "text/plain",
			WantAttachmentText: "referenced attachment content for pptx sample",
		},
		{
			Name:               "ocr_enabled_fallback",
			Mode:               "fallback_on_error_or_quality",
			OCREnabled:         true,
			FileName:           "ocr-probe.docx",
			Content:            buildMinimalDocx("healthy docx baseline text for ocr fallback smoke"),
			WantTriggerReason:  "ocr_candidates_ready",
			WantRootContent:    "external root content generated because OCR candidates were detected",
			WantAttachmentName: "image1.png",
			WantAttachmentPath: "word/media/image1.png",
			WantAttachmentType: "image",
			WantAttachmentMime: "image/png",
			WantAttachmentText: "ocr text extracted from image1.png by third party",
		},
	}
	if raw, ok := buildNested7z("7z outer local text", "7z inner local text"); ok {
		cases = append(cases, smokeCase{
			Name:               "sevenz_content_ref_hydration",
			Mode:               "primary",
			OCREnabled:         false,
			FileName:           "content-ref-probe.7z",
			Content:            raw,
			WantTriggerReason:  "primary",
			WantRootContent:    "referenced root content for 7z sample",
			WantAttachmentName: "nested.txt",
			WantAttachmentPath: "inner.zip/nested.txt",
			WantAttachmentType: "attachment",
			WantAttachmentMime: "text/plain",
			WantAttachmentText: "referenced attachment content for 7z sample",
		})
	}

	for _, tc := range cases {
		must(runCase(ctx, dep, token, policy, slaveNodeID, tc), "run "+tc.Name)
	}

	fmt.Printf("all new feature smoke cases passed total=%d\n", len(cases))
}

func runCase(
	ctx context.Context,
	dep dependency.Dep,
	token string,
	policy *ent.StoragePolicy,
	slaveNodeID int,
	tc smokeCase,
) error {
	topics := buildTopics(tc.Name)
	if err := ensureKafkaTopics(defaultBroker, topics.Process, topics.Result, topics.Error); err != nil {
		return fmt.Errorf("ensure kafka topics: %w", err)
	}

	if err := patchAdminSettings(token, buildSettings(tc.Mode, tc.OCREnabled, topics)); err != nil {
		return fmt.Errorf("patch admin settings: %w", err)
	}
	time.Sleep(settingsSettleDelay)

	tap, err := newProcessTap(defaultBroker, topics.Process, tc.Name)
	if err != nil {
		return fmt.Errorf("start process tap: %w", err)
	}
	defer tap.close()

	targetName := fmt.Sprintf("__fts_new_features_%s_%d_%s", tc.Name, time.Now().UnixNano(), tc.FileName)
	targetURI := "cloudreve://my/" + targetName
	if err := uploadViaMasterHTTP(token, targetURI, tc.Content); err != nil {
		return fmt.Errorf("upload via master http: %w", err)
	}

	fileID, err := waitUploadedFileID(ctx, dep, 1, targetName, int64(len(tc.Content)), waitUploadTimeout)
	if err != nil {
		return fmt.Errorf("resolve uploaded file id: %w", err)
	}
	fmt.Printf("case=%s uploaded file_id=%d uri=%s\n", tc.Name, fileID, targetURI)

	dispatch, err := waitForSlaveDispatch(ctx, dep, fileID, slaveNodeID, waitDispatchTimeout)
	if err != nil {
		return fmt.Errorf("wait slave dispatch: %w", err)
	}
	fmt.Printf(
		"case=%s slave_dispatch_ok file_id=%d task_id=%d node_id=%d slave_task_id=%d\n",
		tc.Name,
		fileID,
		dispatch.TaskID,
		dispatch.NodeID,
		dispatch.SlaveID,
	)

	job := waitLatestExternalJob(ctx, dep, fileID, waitExternalTimeout)
	if job == nil {
		return fmt.Errorf("external job not created for file_id=%d", fileID)
	}
	if job.TriggerReason != tc.WantTriggerReason {
		return fmt.Errorf("unexpected trigger reason for file_id=%d got=%q want=%q", fileID, job.TriggerReason, tc.WantTriggerReason)
	}

	msg, ok := waitForProcessMessage(tap, job.RequestID, fileID, waitProcessTimeout)
	if !ok {
		return fmt.Errorf("process topic did not receive message request_id=%s file_id=%d", job.RequestID, fileID)
	}
	if !msg.Options.RecursiveAttachments {
		return fmt.Errorf("process message recursive_attachments is false request_id=%s", job.RequestID)
	}
	if msg.Options.OCREnabled != tc.OCREnabled {
		return fmt.Errorf("process message ocr_enabled mismatch got=%t want=%t request_id=%s", msg.Options.OCREnabled, tc.OCREnabled, job.RequestID)
	}
	fmt.Printf(
		"case=%s process_message_ok request_id=%s source_policy=%d source_bucket=%s ocr_enabled=%t\n",
		tc.Name,
		job.RequestID,
		msg.Source.PolicyID,
		msg.Source.Bucket,
		msg.Options.OCREnabled,
	)

	rootRefPath := path.Join("third-party-results", tc.Name, fmt.Sprintf("%d", time.Now().UnixNano()), "root.txt")
	attachmentRefPath := path.Join("third-party-results", tc.Name, fmt.Sprintf("%d", time.Now().UnixNano()), "attachments", tc.WantAttachmentName+".txt")
	if err := putPolicyObject(ctx, policy, rootRefPath, "text/plain; charset=utf-8", []byte(tc.WantRootContent)); err != nil {
		return fmt.Errorf("upload referenced root content: %w", err)
	}
	if err := putPolicyObject(ctx, policy, attachmentRefPath, "text/plain; charset=utf-8", []byte(tc.WantAttachmentText)); err != nil {
		return fmt.Errorf("upload referenced attachment content: %w", err)
	}

	time.Sleep(resultSettleDelay)
	result := buildResultMessage(tc, job, policy, fileID, rootRefPath, attachmentRefPath)
	if err := publishExternalResult(defaultBroker, topics.Result, result); err != nil {
		return fmt.Errorf("publish external result: %w", err)
	}

	job = waitExternalJobStatus(ctx, dep, job.RequestID, "success", waitExternalTimeout)
	if job == nil {
		return fmt.Errorf("external job did not reach success request_id=%s", result.RequestID)
	}
	if err := waitForFTSCompletion(ctx, dep, fileID, waitIndexTimeout); err != nil {
		return fmt.Errorf("wait fts completion: %w", err)
	}
	job = waitExternalJobManifest(ctx, dep, job.RequestID, waitIndexTimeout)
	if job == nil || strings.TrimSpace(job.ManifestPath) == "" {
		return fmt.Errorf("external manifest path is empty request_id=%s", result.RequestID)
	}

	doc, err := waitForIndexedDocument(fileID, tc.WantRootContent, tc.WantAttachmentName, tc.WantAttachmentText, waitIndexTimeout)
	if err != nil {
		return err
	}
	fmt.Printf(
		"case=%s indexed_ok file_id=%d request_id=%s manifest=%s attachment=%s attachment_type=%s\n",
		tc.Name,
		fileID,
		job.RequestID,
		job.ManifestPath,
		tc.WantAttachmentName,
		findAttachment(doc.Source.Attachments, tc.WantAttachmentName).Type,
	)

	return nil
}

func buildResultMessage(
	tc smokeCase,
	job *ent.FTSExternalJob,
	policy *ent.StoragePolicy,
	fileID int,
	rootRefPath string,
	attachmentRefPath string,
) externalResultMessage {
	message := externalResultMessage{
		Version:       1,
		RequestID:     job.RequestID,
		SnapshotToken: job.SnapshotToken,
		Status:        "success",
	}
	message.Provider.Name = "fts-new-features-smoke"
	message.Provider.Version = "1.0.0"
	message.Root.Metadata = map[string]string{
		"case": tc.Name,
	}
	message.Root.QualityScore = 0.99
	message.Attachments = []struct {
		ID              string                      `json:"id,omitempty"`
		ParentID        string                      `json:"parent_id,omitempty"`
		Depth           int                         `json:"depth,omitempty"`
		Type            string                      `json:"type,omitempty"`
		Name            string                      `json:"name,omitempty"`
		Path            string                      `json:"path,omitempty"`
		MimeType        string                      `json:"mime_type,omitempty"`
		Size            int64                       `json:"size,omitempty"`
		Metadata        map[string]string           `json:"metadata,omitempty"`
		Content         string                      `json:"content,omitempty"`
		ContentRef      *externalFTSObjectReference `json:"content_ref,omitempty"`
		ContentPolicyID int                         `json:"content_policy_id,omitempty"`
		ContentBucket   string                      `json:"content_bucket,omitempty"`
		ContentPath     string                      `json:"content_path,omitempty"`
	}{
		{
			ID:       "att-1",
			ParentID: fmt.Sprintf("file:%d", fileID),
			Depth:    1,
			Type:     tc.WantAttachmentType,
			Name:     tc.WantAttachmentName,
			Path:     tc.WantAttachmentPath,
			MimeType: tc.WantAttachmentMime,
			Metadata: map[string]string{
				"case": tc.Name,
			},
		},
	}

	if tc.OCREnabled {
		message.Root.ContentPolicyID = policy.ID
		message.Root.ContentBucket = policy.BucketName
		message.Root.ContentPath = rootRefPath
		message.Attachments[0].ContentRef = &externalFTSObjectReference{
			PolicyID: policy.ID,
			Bucket:   policy.BucketName,
			Path:     attachmentRefPath,
		}
		return message
	}

	message.Root.ContentRef = &externalFTSObjectReference{
		PolicyID: policy.ID,
		Bucket:   policy.BucketName,
		Path:     rootRefPath,
	}
	message.Attachments[0].ContentPolicyID = policy.ID
	message.Attachments[0].ContentBucket = policy.BucketName
	message.Attachments[0].ContentPath = attachmentRefPath
	return message
}

func buildSettings(mode string, ocrEnabled bool, topics kafkaTopics) map[string]string {
	return map[string]string{
		"siteURL":                                     defaultSiteURL,
		"fts_enabled":                                 "1",
		"fts_index_type":                              "elasticsearch",
		"fts_extractor_type":                          "tika",
		"fts_elasticsearch_endpoint":                  elasticsearchURL,
		"fts_tika_endpoint":                           "http://127.0.0.1:9998",
		"fts_tika_document_enabled":                   "1",
		"fts_tika_archive_enabled":                    "1",
		"fts_tika_sidecar_enabled":                    "1",
		"fts_tika_sidecar_text_enabled":               "1",
		"fts_tika_sidecar_assets_enabled":             "1",
		"fts_tika_extract_inline_images":              "1",
		"fts_tika_document_exts":                      "pdf,txt,text,md,markdown,csv,tsv,html,htm,xhtml,xml,rtf,doc,docx,xls,xlsx,ppt,pptx,eml,mbox,tnef",
		"fts_tika_archive_exts":                       "zip,tar,tgz,tbz,tbz2,txz,tlz,7z,rar,ar,gz,z,bz,bz2,xz,lzma,lz4,br,snappy,sz,pack200,cpio,arj,dump,jar,war,ear",
		"fts_external_enabled":                        "1",
		"fts_external_mode":                           mode,
		"fts_external_use_global_kafka":               "0",
		"fts_external_timeout_seconds":                "25",
		"fts_external_retry_max":                      "0",
		"fts_external_recursive_attachments":          "1",
		"fts_external_skip_encrypted_files":           "1",
		"fts_external_ocr_enabled":                    boolString(ocrEnabled),
		"fts_external_kafka_brokers":                  defaultBroker,
		"fts_external_kafka_process_topic":            topics.Process,
		"fts_external_kafka_result_topic":             topics.Result,
		"fts_external_kafka_error_topic":              topics.Error,
		"fts_external_kafka_consumer_group":           topics.Group,
		"fts_external_kafka_security_protocol":        "PLAINTEXT",
		"fts_external_quality_enabled":                "1",
		"fts_external_quality_min_text_length":        "6",
		"fts_external_quality_max_replacement_ratio":  "0.5",
		"fts_external_quality_max_control_char_ratio": "0.5",
		"fts_external_quality_min_printable_ratio":    "0.5",
		"fts_external_quality_font_box_min_count":     "4",
		"fts_external_quality_font_box_min_run":       "3",
		"fts_external_quality_font_box_min_ratio":     "0.35",
	}
}

func sortedSettingKeys(settings map[string]string) []string {
	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func fetchAdminSettings(token string, keys []string) (map[string]string, error) {
	payload := map[string]any{
		"keys": keys,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, defaultSiteURL+constants.APIPrefix+"/admin/settings", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var apiResp apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}
	if apiResp.Code != 0 {
		return nil, fmt.Errorf("fetch admin settings failed code=%d msg=%q err=%q", apiResp.Code, apiResp.Msg, apiResp.Error)
	}

	var settings map[string]string
	if err := json.Unmarshal(apiResp.Data, &settings); err != nil {
		return nil, err
	}
	return settings, nil
}

func patchAdminSettings(token string, settings map[string]string) error {
	payload := map[string]any{
		"settings": settings,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPatch, defaultSiteURL+constants.APIPrefix+"/admin/settings", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var apiResp apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return err
	}
	if apiResp.Code != 0 {
		return fmt.Errorf("patch admin settings failed code=%d msg=%q err=%q", apiResp.Code, apiResp.Msg, apiResp.Error)
	}
	return nil
}

func restoreAdminSettings(token string, settings map[string]string) error {
	if len(settings) == 0 {
		return nil
	}

	originalEnabled := strings.TrimSpace(settings["fts_external_enabled"])
	if originalEnabled != "1" {
		if err := patchAdminSettings(token, map[string]string{"fts_external_enabled": "0"}); err != nil {
			return err
		}
		time.Sleep(settingsSettleDelay)
	}

	if err := patchAdminSettings(token, settings); err == nil {
		return nil
	}

	if originalEnabled == "1" {
		return patchAdminSettings(token, map[string]string{"fts_external_enabled": "0"})
	}

	return patchAdminSettings(token, map[string]string{"fts_external_enabled": "0"})
}

func issueAccessToken(ctx context.Context, dep dependency.Dep, user *ent.User) (string, error) {
	token, err := dep.TokenAuth().Issue(ctx, &auth.IssueTokenArgs{
		User: user,
	})
	if err != nil {
		return "", err
	}
	return token.AccessToken, nil
}

func uploadViaMasterHTTP(accessToken, uri string, data []byte) error {
	payload := map[string]any{
		"uri":  uri,
		"size": len(data),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	createReq, err := http.NewRequest(http.MethodPut, defaultSiteURL+constants.APIPrefix+"/file/upload", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	createReq.Header.Set("Authorization", "Bearer "+accessToken)
	createReq.Header.Set("Content-Type", "application/json")

	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		return err
	}
	defer createResp.Body.Close()

	var sessionResp apiResponse
	if err := json.NewDecoder(createResp.Body).Decode(&sessionResp); err != nil {
		return err
	}
	if sessionResp.Code != 0 {
		return fmt.Errorf("create upload session failed code=%d msg=%q err=%q", sessionResp.Code, sessionResp.Msg, sessionResp.Error)
	}

	var session uploadSessionResponse
	if err := json.Unmarshal(sessionResp.Data, &session); err != nil {
		return err
	}
	if session.SessionID == "" {
		return fmt.Errorf("upload session id is empty for uri=%s", uri)
	}
	if len(session.UploadURLs) > 0 {
		return uploadDirectS3Like(session, data)
	}
	if session.ChunkSize > 0 && int64(len(data)) > session.ChunkSize {
		return fmt.Errorf("single chunk upload is insufficient size=%d chunk_size=%d", len(data), session.ChunkSize)
	}

	uploadReq, err := http.NewRequest(
		http.MethodPost,
		fmt.Sprintf("%s%s/file/upload/%s/0", defaultSiteURL, constants.APIPrefix, session.SessionID),
		bytes.NewReader(data),
	)
	if err != nil {
		return err
	}
	uploadReq.Header.Set("Authorization", "Bearer "+accessToken)
	uploadReq.Header.Set("Content-Type", "application/octet-stream")

	uploadResp, err := http.DefaultClient.Do(uploadReq)
	if err != nil {
		return err
	}
	defer uploadResp.Body.Close()

	var finalResp apiResponse
	if err := json.NewDecoder(uploadResp.Body).Decode(&finalResp); err != nil {
		return err
	}
	if finalResp.Code != 0 {
		return fmt.Errorf("upload chunk failed code=%d msg=%q err=%q", finalResp.Code, finalResp.Msg, finalResp.Error)
	}

	return nil
}

func uploadDirectS3Like(session uploadSessionResponse, data []byte) error {
	policyType := "s3"
	if session.StoragePolicy != nil && strings.TrimSpace(session.StoragePolicy.Type) != "" {
		policyType = strings.TrimSpace(session.StoragePolicy.Type)
	}
	if session.CallbackSecret == "" {
		return fmt.Errorf("callback secret is empty for session=%s", session.SessionID)
	}
	if len(session.UploadURLs) == 0 {
		return fmt.Errorf("upload urls are empty for session=%s", session.SessionID)
	}

	chunkSize := session.ChunkSize
	if chunkSize <= 0 {
		chunkSize = int64(len(data))
	}

	partCount := int((int64(len(data)) + chunkSize - 1) / chunkSize)
	if len(data) == 0 {
		partCount = 1
	}
	if len(session.UploadURLs) < partCount {
		return fmt.Errorf("upload urls are insufficient got=%d want=%d", len(session.UploadURLs), partCount)
	}

	partsXML := new(strings.Builder)
	partsXML.WriteString("<CompleteMultipartUpload>")
	for i := 0; i < partCount; i++ {
		start := int64(i) * chunkSize
		end := minInt64(int64(len(data)), start+chunkSize)
		payload := data[start:end]
		etag, err := uploadDirectChunk(session.UploadURLs[i], payload)
		if err != nil {
			return fmt.Errorf("upload part %d failed: %w", i+1, err)
		}
		fmt.Fprintf(partsXML, "<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>", i+1, etag)
	}
	partsXML.WriteString("</CompleteMultipartUpload>")

	if session.CompleteURL != "" {
		req, err := http.NewRequest(http.MethodPost, session.CompleteURL, strings.NewReader(partsXML.String()))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return fmt.Errorf("complete multipart upload failed status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
	}

	callbackURL := fmt.Sprintf("%s%s/callback/%s/%s/%s", defaultSiteURL, constants.APIPrefix, policyType, session.SessionID, session.CallbackSecret)
	callbackReq, err := http.NewRequest(http.MethodGet, callbackURL, nil)
	if err != nil {
		return err
	}
	callbackResp, err := http.DefaultClient.Do(callbackReq)
	if err != nil {
		return err
	}
	defer callbackResp.Body.Close()

	var callbackResult apiResponse
	if err := json.NewDecoder(callbackResp.Body).Decode(&callbackResult); err != nil {
		return err
	}
	if callbackResult.Code != 0 {
		return fmt.Errorf("upload callback failed code=%d msg=%q err=%q", callbackResult.Code, callbackResult.Msg, callbackResult.Error)
	}
	return nil
}

func uploadDirectChunk(url string, payload []byte) (string, error) {
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	etag := strings.TrimSpace(resp.Header.Get("Etag"))
	if etag == "" {
		etag = strings.TrimSpace(resp.Header.Get("ETag"))
	}
	if etag == "" {
		return "", fmt.Errorf("etag header missing")
	}
	return etag, nil
}

func waitUploadedFileID(ctx context.Context, dep dependency.Dep, ownerID int, fileName string, size int64, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		fileModel, err := dep.DBClient().File.Query().
			Where(
				entfile.OwnerIDEQ(ownerID),
				entfile.NameEQ(fileName),
				entfile.SizeEQ(size),
			).
			Order(ent.Desc(entfile.FieldID)).
			First(ctx)
		if err == nil {
			return fileModel.ID, nil
		}
		if !ent.IsNotFound(err) {
			return 0, err
		}
		time.Sleep(300 * time.Millisecond)
	}

	return 0, fmt.Errorf("uploaded file %q not found for owner=%d size=%d", fileName, ownerID, size)
}

func readAdminGroupPolicy(ctx context.Context, dep dependency.Dep) (int, error) {
	group, err := dep.DBClient().Group.Get(ctx, adminGroupID)
	if err != nil {
		return 0, err
	}
	return group.StoragePolicyID, nil
}

func setAdminGroupPolicy(ctx context.Context, dep dependency.Dep, policyID int) error {
	return dep.DBClient().Group.UpdateOneID(adminGroupID).SetStoragePolicyID(policyID).Exec(ctx)
}

func readSmokeSlaveNodeID(ctx context.Context, dep dependency.Dep) (int, error) {
	node, err := dep.DBClient().Node.Query().
		Where(entnode.NameEQ("__fts_slave_smoke__")).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return 0, nil
		}
		return 0, err
	}
	return node.ID, nil
}

func waitForSlaveDispatch(ctx context.Context, dep dependency.Dep, fileID int, expectedNodeID int, timeout time.Duration) (*slaveDispatchEvidence, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tasks, err := dep.DBClient().Task.Query().
			Where(
				taskmodel.PrivateStateContains(strconv.Itoa(fileID)),
				taskmodel.TypeEQ(queue.FullTextIndexTaskType),
			).
			All(ctx)
		if err != nil {
			return nil, err
		}

		for _, model := range tasks {
			if model.Status == taskmodel.StatusError {
				return nil, fmt.Errorf("task %d failed: %s", model.ID, model.PublicState.Error)
			}

			state, err := parseSmokeTaskState(model.PrivateState)
			if err != nil {
				continue
			}
			if state.Phase != "await_slave_extract" || state.SlaveID == 0 {
				continue
			}

			if expectedNodeID > 0 && state.NodeID != expectedNodeID {
				return nil, fmt.Errorf("task %d dispatched to unexpected node got=%d want=%d", model.ID, state.NodeID, expectedNodeID)
			}

			return &slaveDispatchEvidence{
				TaskID:        model.ID,
				NodeID:        state.NodeID,
				SlaveID:       state.SlaveID,
				ExternalJobID: state.ExternalRequestID,
			}, nil
		}

		time.Sleep(300 * time.Millisecond)
	}

	return nil, fmt.Errorf("full_text_index task for file_id=%d never entered await_slave_extract", fileID)
}

func parseSmokeTaskState(raw string) (*smokeFullTextIndexTaskState, error) {
	state := &smokeFullTextIndexTaskState{}
	if err := json.Unmarshal([]byte(raw), state); err != nil {
		return nil, err
	}
	return state, nil
}

func waitForFTSCompletion(ctx context.Context, dep dependency.Dep, fileID int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tasks, err := dep.DBClient().Task.Query().
			Where(
				taskmodel.PrivateStateContains(strconv.Itoa(fileID)),
				taskmodel.TypeIn(
					queue.FullTextIndexTaskType,
					queue.FullTextDeleteTaskType,
					queue.FullTextCopyTaskType,
					queue.FullTextChangeOwnerTaskType,
				),
			).
			All(ctx)
		if err != nil {
			return err
		}

		pending := 0
		for _, model := range tasks {
			switch model.Status {
			case taskmodel.StatusQueued, taskmodel.StatusProcessing, taskmodel.StatusSuspending:
				pending++
			case taskmodel.StatusError:
				return fmt.Errorf("task %d (%s) failed: %s", model.ID, model.Type, model.PublicState.Error)
			}
		}
		if pending == 0 {
			return nil
		}

		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("fts task chain for file_id=%d did not complete in time", fileID)
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

func buildTopics(caseName string) kafkaTopics {
	suffix := fmt.Sprintf("%s.%d", strings.ReplaceAll(caseName, "_", "."), time.Now().UnixNano())
	return kafkaTopics{
		Process: "cloudreve.fts.process." + suffix,
		Result:  "cloudreve.fts.result." + suffix,
		Error:   "cloudreve.fts.error." + suffix,
		Group:   "cloudreve-fts-external." + suffix,
	}
}

func ensureKafkaTopics(broker string, topics ...string) error {
	cfg := sarama.NewConfig()
	cfg.Version = sarama.V3_7_0_0
	cfg.Net.DialTimeout = 10 * time.Second
	cfg.Net.ReadTimeout = 30 * time.Second
	cfg.Net.WriteTimeout = 30 * time.Second

	admin, err := sarama.NewClusterAdmin([]string{broker}, cfg)
	if err != nil {
		return err
	}
	defer admin.Close()

	for _, topic := range topics {
		err := admin.CreateTopic(topic, &sarama.TopicDetail{
			NumPartitions:     1,
			ReplicationFactor: 1,
		}, false)
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return err
		}
	}

	return nil
}

func newProcessTap(broker, topic, suffix string) (*processTap, error) {
	kafkaCfg := *conf.KafkaConfig
	kafkaCfg.Enabled = true
	kafkaCfg.Brokers = broker
	kafkaCfg.SecurityProtocol = "PLAINTEXT"
	kafkaCfg.ClientID = "cloudreve-fts-process-tap-" + strings.ReplaceAll(suffix, "_", "-")
	kafkaCfg.Consumer.InitialOffset = "oldest"

	client, err := kafka.New(&kafkaCfg, logging.NewConsoleLogger(logging.LevelError))
	if err != nil {
		return nil, err
	}

	stream := make(chan externalProcessMessage, 4)
	if err := client.RegisterConsumer(kafka.ConsumerRegistration{
		Name:        "fts-process-tap-" + suffix,
		Group:       "fts-process-tap-" + suffix,
		Topics:      []string{topic},
		Concurrency: 1,
		Handler: func(ctx context.Context, msg *kafka.Message) error {
			var payload externalProcessMessage
			if err := json.Unmarshal(msg.Value, &payload); err != nil {
				return err
			}
			select {
			case stream <- payload:
			default:
			}
			return nil
		},
	}); err != nil {
		_ = client.Close()
		return nil, err
	}

	if err := client.Start(context.Background()); err != nil {
		_ = client.Close()
		return nil, err
	}
	time.Sleep(2 * time.Second)

	return &processTap{client: client, stream: stream}, nil
}

func (t *processTap) close() {
	if t == nil || t.client == nil {
		return
	}
	_ = t.client.Close()
}

func (t *processTap) wait(timeout time.Duration) (*externalProcessMessage, bool) {
	if t == nil {
		return nil, false
	}

	select {
	case msg := <-t.stream:
		return &msg, true
	case <-time.After(timeout):
		return nil, false
	}
}

func waitForProcessMessage(tap *processTap, requestID string, fileID int, timeout time.Duration) (*externalProcessMessage, bool) {
	if tap == nil {
		return nil, false
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		msg, ok := tap.wait(time.Until(deadline))
		if !ok {
			return nil, false
		}
		if msg.RequestID == requestID && msg.File.FileID == fileID {
			return msg, true
		}
	}

	return nil, false
}

func putPolicyObject(ctx context.Context, policy *ent.StoragePolicy, objectPath, mimeType string, data []byte) error {
	if policy == nil {
		return fmt.Errorf("policy is nil")
	}

	sess, err := session.NewSession(&aws.Config{
		Credentials:      credentials.NewStaticCredentials(policy.AccessKey, policy.SecretKey, ""),
		Endpoint:         aws.String(policy.Server),
		Region:           aws.String(policy.Settings.Region),
		S3ForcePathStyle: aws.Bool(policy.Settings.S3ForcePathStyle),
	})
	if err != nil {
		return err
	}

	uploader := s3manager.NewUploader(sess)
	_, err = uploader.UploadWithContext(ctx, &s3manager.UploadInput{
		Bucket:      aws.String(policy.BucketName),
		Key:         aws.String(strings.TrimLeft(objectPath, "/")),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(mimeType),
	})
	return err
}

func publishExternalResult(broker, topic string, message externalResultMessage) error {
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

	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}

	_, _, err = producer.SendMessage(&sarama.ProducerMessage{
		Topic: topic,
		Key:   sarama.StringEncoder(message.RequestID),
		Value: sarama.ByteEncoder(raw),
		Headers: []sarama.RecordHeader{
			{Key: []byte("content-type"), Value: []byte("application/json")},
		},
	})
	return err
}

func waitForIndexedDocument(fileID int, wantRoot, wantAttachmentName, wantAttachmentText string, timeout time.Duration) (*esDoc, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := refreshES(); err != nil {
			return nil, err
		}

		doc, err := getESDoc(fileID)
		if err == nil && doc.Found && strings.Contains(doc.Source.Content, wantRoot) {
			attachment := findAttachment(doc.Source.Attachments, wantAttachmentName)
			if attachment != nil && strings.Contains(attachment.Content, wantAttachmentText) {
				return doc, nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	doc, err := getESDoc(fileID)
	if err != nil {
		return nil, err
	}
	if !doc.Found {
		return nil, fmt.Errorf("es document missing for file_id=%d", fileID)
	}

	attachmentContent := ""
	if attachment := findAttachment(doc.Source.Attachments, wantAttachmentName); attachment != nil {
		attachmentContent = attachment.Content
	}
	return nil, fmt.Errorf(
		"es content mismatch file_id=%d want_root=%q got_root=%q want_attachment=%q got_attachment=%q",
		fileID,
		wantRoot,
		doc.Source.Content,
		wantAttachmentText,
		attachmentContent,
	)
}

func findAttachment(items []esAttachment, name string) *esAttachment {
	for i := range items {
		if items[i].Name == name {
			return &items[i]
		}
	}
	return nil
}

func refreshES() error {
	req, err := http.NewRequest(http.MethodPost, elasticsearchURL+"/"+searchIndex+"/_refresh", nil)
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

func getESDoc(fileID int) (*esDoc, error) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/%s/_doc/%d", elasticsearchURL, searchIndex, fileID), nil)
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
		return nil, fmt.Errorf("get es doc: %s %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var doc esDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, err
	}
	return &doc, nil
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
</w:document>`, escapeXMLText(text))))
	writeZipFile(zw, "word/media/image1.png", ocrCandidatePNG())

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

func buildSmokeEML(subject, body, attachmentName, attachmentText string) []byte {
	lines := []string{
		"From: sender@example.com",
		"To: receiver@example.com",
		fmt.Sprintf("Subject: %s", subject),
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="boundary-smoke"`,
		"",
		"--boundary-smoke",
		`Content-Type: text/plain; charset="utf-8"`,
		"",
		body,
		"",
		"--boundary-smoke",
		fmt.Sprintf(`Content-Type: text/plain; name="%s"`, attachmentName),
		"Content-Transfer-Encoding: 7bit",
		fmt.Sprintf(`Content-Disposition: attachment; filename="%s"`, attachmentName),
		"",
		attachmentText,
		"",
		"--boundary-smoke--",
		"",
	}

	return []byte(strings.Join(lines, "\r\n"))
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

func buildMinimalXLSX(text string) []byte {
	var buffer bytes.Buffer
	zw := zip.NewWriter(&buffer)

	writeZipFile(zw, "[Content_Types].xml", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
  <Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
  <Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/>
  <Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
</Types>`))
	writeZipFile(zw, "_rels/.rels", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`))
	writeZipFile(zw, "xl/workbook.xml", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheets>
    <sheet name="Sheet1" sheetId="1" r:id="rId1"/>
  </sheets>
</workbook>`))
	writeZipFile(zw, "xl/_rels/workbook.xml.rels", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/>
  <Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`))
	writeZipFile(zw, "xl/worksheets/sheet1.xml", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <sheetData>
    <row r="1">
      <c r="A1" t="s"><v>0</v></c>
    </row>
  </sheetData>
</worksheet>`))
	writeZipFile(zw, "xl/sharedStrings.xml", []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="1" uniqueCount="1">
  <si><t>%s</t></si>
</sst>`, escapeXMLText(text))))
	writeZipFile(zw, "xl/styles.xml", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts>
  <fills count="1"><fill><patternFill patternType="none"/></fill></fills>
  <borders count="1"><border/></borders>
  <cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
  <cellXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/></cellXfs>
</styleSheet>`))

	if err := zw.Close(); err != nil {
		panic(fmt.Sprintf("close xlsx zip: %v", err))
	}

	return buffer.Bytes()
}

func buildMinimalPPTX(text string) []byte {
	var buffer bytes.Buffer
	zw := zip.NewWriter(&buffer)

	writeZipFile(zw, "[Content_Types].xml", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>
  <Override PartName="/ppt/slides/slide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>
  <Override PartName="/ppt/slideLayouts/slideLayout1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/>
  <Override PartName="/ppt/slideMasters/slideMaster1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideMaster+xml"/>
  <Override PartName="/ppt/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>
</Types>`))
	writeZipFile(zw, "_rels/.rels", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/>
</Relationships>`))
	writeZipFile(zw, "ppt/presentation.xml", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:presentation xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
  <p:sldMasterIdLst>
    <p:sldMasterId id="2147483648" r:id="rId1"/>
  </p:sldMasterIdLst>
  <p:sldIdLst>
    <p:sldId id="256" r:id="rId2"/>
  </p:sldIdLst>
  <p:sldSz cx="9144000" cy="6858000"/>
  <p:notesSz cx="6858000" cy="9144000"/>
</p:presentation>`))
	writeZipFile(zw, "ppt/_rels/presentation.xml.rels", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="slideMasters/slideMaster1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>
</Relationships>`))
	writeZipFile(zw, "ppt/slides/slide1.xml", []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
  <p:cSld>
    <p:spTree>
      <p:nvGrpSpPr>
        <p:cNvPr id="1" name=""/>
        <p:cNvGrpSpPr/>
        <p:nvPr/>
      </p:nvGrpSpPr>
      <p:grpSpPr/>
      <p:sp>
        <p:nvSpPr>
          <p:cNvPr id="2" name="Title 1"/>
          <p:cNvSpPr/>
          <p:nvPr/>
        </p:nvSpPr>
        <p:spPr/>
        <p:txBody>
          <a:bodyPr/>
          <a:lstStyle/>
          <a:p><a:r><a:t>%s</a:t></a:r></a:p>
        </p:txBody>
      </p:sp>
    </p:spTree>
  </p:cSld>
  <p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr>
</p:sld>`, escapeXMLText(text))))
	writeZipFile(zw, "ppt/slides/_rels/slide1.xml.rels", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>
</Relationships>`))
	writeZipFile(zw, "ppt/slideLayouts/slideLayout1.xml", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sldLayout xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" type="title">
  <p:cSld name="Title Slide">
    <p:spTree>
      <p:nvGrpSpPr>
        <p:cNvPr id="1" name=""/>
        <p:cNvGrpSpPr/>
        <p:nvPr/>
      </p:nvGrpSpPr>
      <p:grpSpPr/>
    </p:spTree>
  </p:cSld>
  <p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr>
</p:sldLayout>`))
	writeZipFile(zw, "ppt/slideLayouts/_rels/slideLayout1.xml.rels", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="../slideMasters/slideMaster1.xml"/>
</Relationships>`))
	writeZipFile(zw, "ppt/slideMasters/slideMaster1.xml", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sldMaster xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
  <p:cSld name="Master">
    <p:spTree>
      <p:nvGrpSpPr>
        <p:cNvPr id="1" name=""/>
        <p:cNvGrpSpPr/>
        <p:nvPr/>
      </p:nvGrpSpPr>
      <p:grpSpPr/>
    </p:spTree>
  </p:cSld>
  <p:clrMap bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" hlink="hlink" folHlink="folHlink"/>
  <p:sldLayoutIdLst>
    <p:sldLayoutId id="1" r:id="rId1"/>
  </p:sldLayoutIdLst>
</p:sldMaster>`))
	writeZipFile(zw, "ppt/slideMasters/_rels/slideMaster1.xml.rels", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="../theme/theme1.xml"/>
</Relationships>`))
	writeZipFile(zw, "ppt/theme/theme1.xml", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<a:theme xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" name="Office Theme">
  <a:themeElements>
    <a:clrScheme name="Office">
      <a:dk1><a:sysClr val="windowText" lastClr="000000"/></a:dk1>
      <a:lt1><a:sysClr val="window" lastClr="FFFFFF"/></a:lt1>
      <a:dk2><a:srgbClr val="1F497D"/></a:dk2>
      <a:lt2><a:srgbClr val="EEECE1"/></a:lt2>
      <a:accent1><a:srgbClr val="4F81BD"/></a:accent1>
      <a:accent2><a:srgbClr val="C0504D"/></a:accent2>
      <a:accent3><a:srgbClr val="9BBB59"/></a:accent3>
      <a:accent4><a:srgbClr val="8064A2"/></a:accent4>
      <a:accent5><a:srgbClr val="4BACC6"/></a:accent5>
      <a:accent6><a:srgbClr val="F79646"/></a:accent6>
      <a:hlink><a:srgbClr val="0000FF"/></a:hlink>
      <a:folHlink><a:srgbClr val="800080"/></a:folHlink>
    </a:clrScheme>
    <a:fontScheme name="Office">
      <a:majorFont><a:latin typeface="Calibri"/></a:majorFont>
      <a:minorFont><a:latin typeface="Calibri"/></a:minorFont>
    </a:fontScheme>
    <a:fmtScheme name="Office"><a:fillStyleLst/><a:lnStyleLst/><a:effectStyleLst/><a:bgFillStyleLst/></a:fmtScheme>
  </a:themeElements>
</a:theme>`))

	if err := zw.Close(); err != nil {
		panic(fmt.Sprintf("close pptx zip: %v", err))
	}

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

func escapeXMLText(raw string) string {
	raw = strings.ReplaceAll(raw, "&", "&amp;")
	raw = strings.ReplaceAll(raw, "<", "&lt;")
	raw = strings.ReplaceAll(raw, ">", "&gt;")
	raw = strings.ReplaceAll(raw, "\"", "&quot;")
	raw = strings.ReplaceAll(raw, "'", "&apos;")
	return raw
}

func ocrCandidatePNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 96, 96))
	for y := 0; y < 96; y++ {
		for x := 0; x < 96; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8((x*17 + y*7) % 255),
				G: uint8((x*11 + y*19) % 255),
				B: uint8((x*5 + y*23) % 255),
				A: 255,
			})
		}
	}

	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		panic(fmt.Sprintf("encode OCR candidate png: %v", err))
	}

	return buffer.Bytes()
}

func boolString(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func must(err error, message string) {
	if err != nil {
		panic(fmt.Sprintf("%s: %v", message, err))
	}
}
