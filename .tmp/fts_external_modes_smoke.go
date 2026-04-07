package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/IBM/sarama"
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
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/kafka"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	appsetting "github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
)

const (
	baseConfigPath    = ".tmp/fts_real_smoke.ini"
	defaultBroker     = "127.0.0.1:9092"
	externalPolicyID  = 2
	adminGroupID      = 1
	defaultSiteURL    = "http://127.0.0.1:5212"
	elasticsearchURL  = "http://127.0.0.1:9200"
	tikaEndpoint      = "http://127.0.0.1:9998"
	searchIndex       = "cloudreve_files"
	waitJobTimeout    = 25 * time.Second
	waitIndexTimeout  = 45 * time.Second
	waitProcessTapTTL = 8 * time.Second
	resultSettleDelay = 3 * time.Second
)

type smokeCase struct {
	Name               string
	Mode               string
	UseGlobalKafka     bool
	FileName           string
	Content            []byte
	ExpectExternalJob  bool
	AssertNoDocInspect bool
	ExpectESContent    string
	ExternalContent    string
	ExpectReason       string
	AcceptReasons      []string
	ExpectQualityHint  string
}

type kafkaTopics struct {
	Process string
	Result  string
	Error   string
	Group   string
}

type processTap struct {
	client kafka.Client
	stream chan externalProcessMessage
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
		Bucket string `json:"bucket"`
		Path   string `json:"path"`
	} `json:"source"`
	Options struct {
		RecursiveAttachments bool `json:"recursive_attachments,omitempty"`
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
		Content      string            `json:"content,omitempty"`
		Metadata     map[string]string `json:"metadata,omitempty"`
		Warnings     []string          `json:"warnings,omitempty"`
		QualityScore float64           `json:"quality_score,omitempty"`
	} `json:"root"`
}

type esDoc struct {
	Found  bool `json:"found"`
	Source struct {
		FileID  int    `json:"file_id"`
		Content string `json:"content"`
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
	SawAwaitSlave bool
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

func buildCases() []smokeCase {
	cases := []smokeCase{
		{
			Name:              "primary_private_external_success",
			Mode:              "primary",
			UseGlobalKafka:    false,
			FileName:          "primary-probe.txt",
			Content:           []byte("primary local text should be replaced by external result"),
			ExpectExternalJob: true,
			ExpectESContent:   "primary external result",
			ExternalContent:   "primary external result",
			ExpectReason:      "primary",
		},
		{
			Name:              "primary_global_external_success",
			Mode:              "primary",
			UseGlobalKafka:    true,
			FileName:          "primary-global-probe.txt",
			Content:           []byte("primary global local text should be replaced by external result"),
			ExpectExternalJob: true,
			ExpectESContent:   "primary global external result",
			ExternalContent:   "primary global external result",
			ExpectReason:      "primary",
		},
		{
			Name:              "fallback_on_error_private_local_ok",
			Mode:              "fallback_on_error",
			UseGlobalKafka:    false,
			FileName:          "fallback-local-ok.txt",
			Content:           []byte("fallback_on_error should keep healthy local content"),
			ExpectExternalJob: false,
			ExpectESContent:   "fallback_on_error should keep healthy local content",
		},
		{
			Name:               "fallback_on_error_global_empty_index_local_only",
			Mode:               "fallback_on_error",
			UseGlobalKafka:     true,
			FileName:           "fallback-empty.pdf",
			Content:            []byte{},
			ExpectExternalJob:  false,
			AssertNoDocInspect: true,
			ExpectESContent:    "",
		},
		{
			Name:              "fallback_on_quality_private_local_ok",
			Mode:              "fallback_on_error_or_quality",
			UseGlobalKafka:    false,
			FileName:          "quality-local-ok.txt",
			Content:           []byte("this is a healthy extraction result with enough printable text to stay local"),
			ExpectExternalJob: false,
			ExpectESContent:   "healthy extraction result",
		},
		{
			Name:              "fallback_on_quality_private_external_success",
			Mode:              "fallback_on_error_or_quality",
			UseGlobalKafka:    false,
			FileName:          "quality-bad.txt",
			Content:           []byte("□□□□ □□□□ □□□□"),
			ExpectExternalJob: true,
			ExpectESContent:   "quality fallback external result",
			ExternalContent:   "quality fallback external result",
			ExpectReason:      "quality_rejected",
			ExpectQualityHint: "font_issue_box_glyphs",
		},
		{
			Name:              "fallback_on_quality_global_external_success",
			Mode:              "fallback_on_error_or_quality",
			UseGlobalKafka:    true,
			FileName:          "quality-bad-global.txt",
			Content:           []byte("□□□□ □□□□ □□□□"),
			ExpectExternalJob: true,
			ExpectESContent:   "quality global external result",
			ExternalContent:   "quality global external result",
			ExpectReason:      "quality_rejected",
			ExpectQualityHint: "font_issue_box_glyphs",
		},
		{
			Name:              "fallback_on_quality_docx_local_ok",
			Mode:              "fallback_on_error_or_quality",
			UseGlobalKafka:    false,
			FileName:          "sample.docx",
			Content:           buildMinimalDocx("docx office local text"),
			ExpectExternalJob: false,
			ExpectESContent:   "docx office local text",
		},
		{
			Name:              "fallback_on_quality_xlsx_local_ok",
			Mode:              "fallback_on_error_or_quality",
			UseGlobalKafka:    false,
			FileName:          "sample.xlsx",
			Content:           buildMinimalXLSX("xlsx office local text"),
			ExpectExternalJob: false,
			ExpectESContent:   "xlsx office local text",
		},
		{
			Name:              "fallback_on_quality_pptx_local_ok",
			Mode:              "fallback_on_error_or_quality",
			UseGlobalKafka:    false,
			FileName:          "sample.pptx",
			Content:           buildMinimalPPTX("pptx office local text"),
			ExpectExternalJob: false,
			ExpectESContent:   "pptx office local text",
		},
		{
			Name:              "fallback_on_quality_pdf_local_ok",
			Mode:              "fallback_on_error_or_quality",
			UseGlobalKafka:    false,
			FileName:          "sample.pdf",
			Content:           buildMinimalPDFWithEmbeddedFile("pdf office local text", "attached.txt", "pdf attachment text"),
			ExpectExternalJob: false,
			ExpectESContent:   "pdf office local text",
		},
		{
			Name:              "fallback_on_quality_zip_local_ok",
			Mode:              "fallback_on_error_or_quality",
			UseGlobalKafka:    false,
			FileName:          "nested.zip",
			Content:           buildNestedZip("zip outer text", "zip inner text"),
			ExpectExternalJob: false,
			ExpectESContent:   "zip inner text",
		},
		{
			Name:              "fallback_on_quality_tar_local_ok",
			Mode:              "fallback_on_error_or_quality",
			UseGlobalKafka:    false,
			FileName:          "nested.tar",
			Content:           buildNestedTar("tar outer text", "tar inner text"),
			ExpectExternalJob: false,
			ExpectESContent:   "tar inner text",
		},
		{
			Name:              "fallback_on_quality_tgz_local_ok",
			Mode:              "fallback_on_error_or_quality",
			UseGlobalKafka:    false,
			FileName:          "nested.tgz",
			Content:           buildNestedTGZ("tgz outer text", "tgz inner text"),
			ExpectExternalJob: false,
			ExpectESContent:   "tgz inner text",
		},
		{
			Name:              "fallback_on_quality_jar_local_ok",
			Mode:              "fallback_on_error_or_quality",
			UseGlobalKafka:    false,
			FileName:          "nested.jar",
			Content:           buildNestedJar("jar outer text", "jar inner text"),
			ExpectExternalJob: false,
			ExpectESContent:   "jar inner text",
		},
		{
			Name:              "fallback_on_quality_war_local_ok",
			Mode:              "fallback_on_error_or_quality",
			UseGlobalKafka:    false,
			FileName:          "nested.war",
			Content:           buildNestedJar("war outer text", "war inner text"),
			ExpectExternalJob: false,
			ExpectESContent:   "war inner text",
		},
		{
			Name:              "fallback_on_quality_ear_local_ok",
			Mode:              "fallback_on_error_or_quality",
			UseGlobalKafka:    false,
			FileName:          "nested.ear",
			Content:           buildNestedJar("ear outer text", "ear inner text"),
			ExpectExternalJob: false,
			ExpectESContent:   "ear inner text",
		},
	}

	if raw, ok := buildNestedTBZ2("tbz2 outer text", "tbz2 inner text"); ok {
		cases = append(cases, smokeCase{
			Name:              "fallback_on_quality_tbz2_local_ok",
			Mode:              "fallback_on_error_or_quality",
			UseGlobalKafka:    false,
			FileName:          "nested.tbz2",
			Content:           raw,
			ExpectExternalJob: false,
			ExpectESContent:   "tbz2 inner text",
		})
	} else {
		fmt.Printf("skip sample case=fallback_on_quality_tbz2_local_ok reason=bzip2 unavailable\n")
	}
	if raw, ok := buildNestedTXZ("txz outer text", "txz inner text"); ok {
		cases = append(cases, smokeCase{
			Name:              "fallback_on_quality_txz_local_ok",
			Mode:              "fallback_on_error_or_quality",
			UseGlobalKafka:    false,
			FileName:          "nested.txz",
			Content:           raw,
			ExpectExternalJob: false,
			ExpectESContent:   "txz inner text",
		})
	} else {
		fmt.Printf("skip sample case=fallback_on_quality_txz_local_ok reason=xz unavailable\n")
	}
	if raw, ok := buildNestedCPIO("cpio outer text", "cpio inner text"); ok {
		cases = append(cases, smokeCase{
			Name:              "fallback_on_quality_cpio_local_ok",
			Mode:              "fallback_on_error_or_quality",
			UseGlobalKafka:    false,
			FileName:          "nested.cpio",
			Content:           raw,
			ExpectExternalJob: false,
			ExpectESContent:   "cpio inner text",
		})
	} else {
		fmt.Printf("skip sample case=fallback_on_quality_cpio_local_ok reason=cpio unavailable\n")
	}
	if raw, ok := buildNested7z("7z outer text", "7z inner text"); ok {
		cases = append(cases, smokeCase{
			Name:              "fallback_on_quality_7z_local_ok",
			Mode:              "fallback_on_error_or_quality",
			UseGlobalKafka:    false,
			FileName:          "nested.7z",
			Content:           raw,
			ExpectExternalJob: false,
			ExpectESContent:   "7z inner text",
		})
	} else {
		fmt.Printf("skip sample case=fallback_on_quality_7z_local_ok reason=7z unavailable\n")
	}
	if raw, ok := buildNestedRar("rar outer text", "rar inner text"); ok {
		cases = append(cases, smokeCase{
			Name:              "fallback_on_quality_rar_local_ok",
			Mode:              "fallback_on_error_or_quality",
			UseGlobalKafka:    false,
			FileName:          "nested.rar",
			Content:           raw,
			ExpectExternalJob: false,
			ExpectESContent:   "rar inner text",
		})
	} else {
		fmt.Printf("skip sample case=fallback_on_quality_rar_local_ok reason=rar unavailable\n")
	}

	return cases
}

func main() {
	util.UseWorkingDir = true

	cases, err := selectCases(buildCases())
	must(err, "select smoke cases")
	topics, err := loadRuntimeTopics()
	must(err, "load runtime topics")

	originalPolicyID, err := readAdminGroupPolicy(baseConfigPath)
	must(err, "read admin group policy")
	defer func() {
		restoreErr := restoreAdminGroupPolicy(baseConfigPath, originalPolicyID)
		if restoreErr != nil {
			panic(fmt.Sprintf("restore admin group policy: %v", restoreErr))
		}
	}()

	fmt.Printf(
		"selected smoke cases total=%d mode=%s use_global_kafka=%t process_topic=%s\n",
		len(cases),
		cases[0].Mode,
		cases[0].UseGlobalKafka,
		topics.Process,
	)

	for _, tc := range cases {
		if err := runSmokeCase(baseConfigPath, tc, topics); err != nil {
			panic(fmt.Sprintf("%s failed: %v", tc.Name, err))
		}
	}

	fmt.Printf("all external mode smoke cases passed total=%d\n", len(cases))
}

func runSmokeCase(configPath string, tc smokeCase, topics kafkaTopics) error {
	logger := logging.NewConsoleLogger(logging.LevelDebug)
	dep := dependency.NewDependency(
		dependency.WithConfigPath(configPath),
		dependency.WithLogger(logger),
		dependency.WithRequiredDbVersion(constants.BackendVersion),
	)
	defer dep.Shutdown(context.Background())

	ctx := context.WithValue(context.Background(), dependency.DepCtx{}, dep)
	ctx = context.WithValue(ctx, inventory.LoadTaskUser{}, true)
	if err := ensureSmokeFTSSettings(ctx, dep, tc, topics); err != nil {
		return fmt.Errorf("apply smoke fts settings: %w", err)
	}
	if err := setAdminGroupPolicy(ctx, dep, externalPolicyID); err != nil {
		return fmt.Errorf("set admin group policy: %w", err)
	}

	tap, err := newProcessTap(defaultBroker, topics.Process, tc.Name)
	if err != nil {
		return fmt.Errorf("start process tap: %w", err)
	}
	defer tap.close()

	slaveNodeID, err := readSmokeSlaveNodeID(ctx, dep)
	if err != nil {
		return fmt.Errorf("read smoke slave node id: %w", err)
	}
	if slaveNodeID == 0 {
		return fmt.Errorf("content processing slave node __fts_slave_smoke__ is not registered")
	}

	user, err := dep.UserClient().GetLoginUserByID(ctx, 1)
	if err != nil {
		return fmt.Errorf("load user: %w", err)
	}
	ctx = context.WithValue(ctx, inventory.UserCtx{}, user)
	ctx = context.WithValue(ctx, inventory.UserIDCtx{}, user.ID)
	accessToken, err := issueAccessToken(ctx, dep, user)
	if err != nil {
		return fmt.Errorf("issue local access token: %w", err)
	}

	targetName := fmt.Sprintf("__fts_external_modes_%s_%d_%s", tc.Name, time.Now().UnixNano(), tc.FileName)
	targetURI := "cloudreve://my/" + targetName
	if err := uploadViaMasterHTTP(accessToken, targetURI, tc.Content); err != nil {
		return fmt.Errorf("upload via master http: %w", err)
	}
	fileID, err := waitUploadedFileID(ctx, dep, user.ID, targetName, int64(len(tc.Content)), waitJobTimeout)
	if err != nil {
		return fmt.Errorf("resolve uploaded file id: %w", err)
	}

	fmt.Printf("case=%s uploaded file_id=%d uri=%s use_global_kafka=%t mode=%s\n", tc.Name, fileID, targetURI, tc.UseGlobalKafka, tc.Mode)
	dispatch, err := waitForSlaveDispatch(ctx, dep, fileID, slaveNodeID, waitJobTimeout)
	if err != nil {
		return fmt.Errorf("wait slave dispatch: %w", err)
	}
	fmt.Printf(
		"case=%s slave_dispatch_ok file_id=%d task_id=%d node_id=%d slave_task_id=%d external_request=%s\n",
		tc.Name,
		fileID,
		dispatch.TaskID,
		dispatch.NodeID,
		dispatch.SlaveID,
		dispatch.ExternalJobID,
	)

	if tc.ExpectExternalJob {
		return runExternalCase(ctx, dep, tap, fileID, tc, topics)
	}

	if err := waitForFTSCompletion(ctx, dep, fileID, waitIndexTimeout); err != nil {
		return fmt.Errorf("wait local task completion: %w", err)
	}
	if err := waitForIndexedContent(fileID, tc.ExpectESContent, waitIndexTimeout); err != nil {
		return fmt.Errorf("wait local index: %w", err)
	}
	if tc.AssertNoDocInspect {
		if err := assertNoDocumentInspectTask(ctx, dep, fileID, waitProcessTapTTL); err != nil {
			return err
		}
	}
	if err := assertNoExternalJob(ctx, dep, fileID, waitProcessTapTTL); err != nil {
		return err
	}
	if msg, ok := waitForProcessMessageByFile(tap, fileID, waitProcessTapTTL); ok {
		return fmt.Errorf("unexpected process message request_id=%s file_id=%d", msg.RequestID, msg.File.FileID)
	}

	fmt.Printf("case=%s local_only_ok file_id=%d\n", tc.Name, fileID)
	return nil
}

func runExternalCase(ctx context.Context, dep dependency.Dep, tap *processTap, fileID int, tc smokeCase, topics kafkaTopics) error {
	job := waitLatestExternalJob(ctx, dep, fileID, waitJobTimeout)
	if job == nil {
		return fmt.Errorf("external job not created for file_id=%d", fileID)
	}
	if !matchTriggerReason(job.TriggerReason, tc) {
		return fmt.Errorf("unexpected trigger reason got=%q want=%q", job.TriggerReason, tc.ExpectReason)
	}
	if tc.ExpectQualityHint != "" && !strings.Contains(job.QualityReport, tc.ExpectQualityHint) {
		return fmt.Errorf("quality report missing hint %q report=%q", tc.ExpectQualityHint, job.QualityReport)
	}

	msg, ok := waitForProcessMessage(tap, job.RequestID, fileID, waitProcessTapTTL)
	if !ok {
		return fmt.Errorf("process topic did not receive message for request_id=%s", job.RequestID)
	}
	fmt.Printf("case=%s process_message_ok request_id=%s topic=%s\n", tc.Name, job.RequestID, topics.Process)
	time.Sleep(resultSettleDelay)

	if err := publishSyntheticExternalResult(job, defaultBroker, topics.Result, tc.ExternalContent); err != nil {
		return fmt.Errorf("publish synthetic result: %w", err)
	}

	job = waitExternalJobStatus(ctx, dep, job.RequestID, "success", waitJobTimeout)
	if job == nil {
		return fmt.Errorf("external job did not reach success request_id=%s", msg.RequestID)
	}

	if err := waitForFTSCompletion(ctx, dep, fileID, waitIndexTimeout); err != nil {
		return fmt.Errorf("wait external task completion: %w", err)
	}
	if err := waitForIndexedContent(fileID, tc.ExpectESContent, waitIndexTimeout); err != nil {
		return fmt.Errorf("wait external index: %w", err)
	}
	job = waitExternalJobManifest(ctx, dep, job.RequestID, waitIndexTimeout)
	if job == nil {
		return fmt.Errorf("external job did not finalize manifest request_id=%s", msg.RequestID)
	}
	if strings.TrimSpace(job.ManifestPath) == "" {
		return fmt.Errorf("external manifest path is empty request_id=%s", msg.RequestID)
	}

	fmt.Printf("case=%s external_ok file_id=%d request_id=%s manifest=%s\n", tc.Name, fileID, job.RequestID, job.ManifestPath)
	return nil
}

func matchTriggerReason(got string, tc smokeCase) bool {
	if len(tc.AcceptReasons) == 0 {
		return got == tc.ExpectReason
	}

	for _, want := range tc.AcceptReasons {
		if got == want {
			return true
		}
	}

	return false
}

func selectCases(cases []smokeCase) ([]smokeCase, error) {
	mode := strings.TrimSpace(firstNonEmptyEnv("FTS_SMOKE_MODE", "CR_SETTING_fts_external_mode"))
	if mode == "" {
		return nil, fmt.Errorf("FTS_SMOKE_MODE or CR_SETTING_fts_external_mode is required")
	}

	useGlobalRaw := strings.TrimSpace(firstNonEmptyEnv("FTS_SMOKE_USE_GLOBAL_KAFKA", "CR_SETTING_fts_external_use_global_kafka"))
	if useGlobalRaw == "" {
		return nil, fmt.Errorf("FTS_SMOKE_USE_GLOBAL_KAFKA or CR_SETTING_fts_external_use_global_kafka is required")
	}
	useGlobal, err := parseBoolEnv(useGlobalRaw)
	if err != nil {
		return nil, fmt.Errorf("parse use_global_kafka: %w", err)
	}

	casePattern := strings.TrimSpace(os.Getenv("FTS_SMOKE_CASE_PATTERN"))
	selected := make([]smokeCase, 0, len(cases))
	for _, tc := range cases {
		if tc.Mode != mode || tc.UseGlobalKafka != useGlobal {
			continue
		}
		if casePattern != "" && !strings.Contains(tc.Name, casePattern) && !strings.Contains(tc.FileName, casePattern) {
			continue
		}
		selected = append(selected, tc)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no smoke cases matched mode=%s use_global_kafka=%t pattern=%q", mode, useGlobal, casePattern)
	}

	return selected, nil
}

func loadRuntimeTopics() (kafkaTopics, error) {
	process := strings.TrimSpace(firstNonEmptyEnv("FTS_SMOKE_PROCESS_TOPIC", "CR_SETTING_fts_external_kafka_process_topic"))
	result := strings.TrimSpace(firstNonEmptyEnv("FTS_SMOKE_RESULT_TOPIC", "CR_SETTING_fts_external_kafka_result_topic"))
	errTopic := strings.TrimSpace(firstNonEmptyEnv("FTS_SMOKE_ERROR_TOPIC", "CR_SETTING_fts_external_kafka_error_topic"))
	group := strings.TrimSpace(firstNonEmptyEnv("FTS_SMOKE_CONSUMER_GROUP", "CR_SETTING_fts_external_kafka_consumer_group"))
	if process == "" || result == "" || errTopic == "" {
		return kafkaTopics{}, fmt.Errorf("runtime topics are incomplete process=%q result=%q error=%q", process, result, errTopic)
	}
	if group == "" {
		group = "cloudreve-fts-external"
	}

	return kafkaTopics{
		Process: process,
		Result:  result,
		Error:   errTopic,
		Group:   group,
	}, nil
}

func restoreAdminGroupPolicy(configPath string, originalPolicyID int) error {
	dep := dependency.NewDependency(
		dependency.WithConfigPath(configPath),
		dependency.WithLogger(logging.NewConsoleLogger(logging.LevelError)),
		dependency.WithRequiredDbVersion(constants.BackendVersion),
	)
	defer dep.Shutdown(context.Background())

	return setAdminGroupPolicy(context.Background(), dep, originalPolicyID)
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
	evidence := &slaveDispatchEvidence{}
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
				return nil, fmt.Errorf("task %d (%s) failed: %s", model.ID, model.Type, model.PublicState.Error)
			}

			state, err := parseSmokeTaskState(model.PrivateState)
			if err != nil {
				continue
			}
			if state.Phase != "await_slave_extract" || state.SlaveID == 0 {
				continue
			}
			evidence.TaskID = model.ID
			evidence.NodeID = state.NodeID
			evidence.SlaveID = state.SlaveID
			evidence.ExternalJobID = state.ExternalRequestID
			evidence.SawAwaitSlave = true
			if expectedNodeID > 0 && state.NodeID != expectedNodeID {
				return nil, fmt.Errorf("task %d dispatched to unexpected node got=%d want=%d", model.ID, state.NodeID, expectedNodeID)
			}
			return evidence, nil
		}

		time.Sleep(300 * time.Millisecond)
	}

	return nil, fmt.Errorf("full_text_index task for file_id=%d never entered await_slave_extract", fileID)
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

func assertNoDocumentInspectTask(ctx context.Context, dep dependency.Dep, fileID int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	pattern := fmt.Sprintf("\"file_id\":%d", fileID)
	for time.Now().Before(deadline) {
		tasks, err := dep.DBClient().Task.Query().
			Where(
				taskmodel.TypeEQ(queue.DocumentInspectTaskType),
				taskmodel.PrivateStateContains(pattern),
			).
			All(ctx)
		if err != nil {
			return err
		}
		if len(tasks) == 0 {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}

	return fmt.Errorf("unexpected document_inspect task exists for file_id=%d", fileID)
}

func parseSmokeTaskState(raw string) (*smokeFullTextIndexTaskState, error) {
	state := &smokeFullTextIndexTaskState{}
	if err := json.Unmarshal([]byte(raw), state); err != nil {
		return nil, err
	}
	return state, nil
}

func parseBoolEnv(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("unsupported bool value %q", raw)
	}
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func prepareGlobalKafkaConfig(basePath string) (string, error) {
	raw, err := os.ReadFile(basePath)
	if err != nil {
		return "", err
	}

	lines := strings.Split(string(raw), "\n")
	inKafka := false
	inConsumer := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "[Kafka]":
			inKafka = true
			inConsumer = false
		case "[Kafka.Consumer]":
			inKafka = false
			inConsumer = true
		default:
			if strings.HasPrefix(trimmed, "[") && trimmed != "" {
				inKafka = false
				inConsumer = false
			}
		}

		if inKafka {
			switch {
			case strings.HasPrefix(trimmed, "Enabled ="):
				lines[i] = "Enabled = true"
			case strings.HasPrefix(trimmed, "Brokers ="):
				lines[i] = "Brokers = " + defaultBroker
			case strings.HasPrefix(trimmed, "ClientID ="):
				lines[i] = "ClientID = cloudreve-global-fts-smoke"
			}
		}
		if inConsumer && strings.HasPrefix(trimmed, "InitialOffset =") {
			lines[i] = "InitialOffset = oldest"
		}
	}

	target := filepath.Join(".tmp", "fts_real_smoke_global_kafka.ini")
	if err := os.WriteFile(target, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return "", err
	}
	return target, nil
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

	stream := make(chan externalProcessMessage, 1)
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
		remaining := time.Until(deadline)
		msg, ok := tap.wait(remaining)
		if !ok {
			return nil, false
		}
		if msg.RequestID == requestID && msg.File.FileID == fileID {
			return msg, true
		}
	}

	return nil, false
}

func waitForProcessMessageByFile(tap *processTap, fileID int, timeout time.Duration) (*externalProcessMessage, bool) {
	if tap == nil {
		return nil, false
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		msg, ok := tap.wait(remaining)
		if !ok {
			return nil, false
		}
		if msg.File.FileID == fileID {
			return msg, true
		}
	}

	return nil, false
}

func (t *processTap) close() {
	if t == nil || t.client == nil {
		return
	}
	_ = t.client.Close()
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
		if err := uploadDirectS3Like(session, data); err != nil {
			return fmt.Errorf("direct upload failed: %w", err)
		}
		return nil
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
		end := min(int64(len(data)), start+chunkSize)
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
	writeZipFile(zw, "word/media/image1.png", tinyPNG())

	if err := zw.Close(); err != nil {
		panic(fmt.Sprintf("close docx zip: %v", err))
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

func assertNoExternalJob(ctx context.Context, dep dependency.Dep, fileID int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		exists, err := dep.DBClient().FTSExternalJob.Query().
			Where(entftsexternaljob.FileIDEQ(fileID)).
			Exist(ctx)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("unexpected external job created for file_id=%d", fileID)
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

	message := externalResultMessage{
		Version:       1,
		RequestID:     job.RequestID,
		SnapshotToken: job.SnapshotToken,
		Status:        "success",
	}
	message.Provider.Name = "external-mode-smoke"
	message.Provider.Version = "1.0.0"
	message.Root.Content = content
	message.Root.Metadata = map[string]string{"source": "external-mode-smoke"}
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

func waitForIndexedContent(fileID int, want string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := refreshES(); err != nil {
			return err
		}
		doc, err := getESDoc(fileID)
		if err == nil && doc.Found && strings.Contains(doc.Source.Content, want) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	doc, err := getESDoc(fileID)
	if err != nil {
		return err
	}
	return fmt.Errorf("es content mismatch file_id=%d want_contains=%q got=%q", fileID, want, doc.Source.Content)
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

func readAdminGroupPolicy(configPath string) (int, error) {
	dep := dependency.NewDependency(
		dependency.WithConfigPath(configPath),
		dependency.WithLogger(logging.NewConsoleLogger(logging.LevelError)),
		dependency.WithRequiredDbVersion(constants.BackendVersion),
	)
	defer dep.Shutdown(context.Background())
	ctx := context.Background()
	group, err := dep.DBClient().Group.Get(ctx, adminGroupID)
	if err != nil {
		return 0, err
	}
	return group.StoragePolicyID, nil
}

func setAdminGroupPolicy(ctx context.Context, dep dependency.Dep, policyID int) error {
	return dep.DBClient().Group.UpdateOneID(adminGroupID).SetStoragePolicyID(policyID).Exec(ctx)
}

func restoreBaseline(configPath string, originalPolicyID int) error {
	dep := dependency.NewDependency(
		dependency.WithConfigPath(configPath),
		dependency.WithLogger(logging.NewConsoleLogger(logging.LevelError)),
		dependency.WithRequiredDbVersion(constants.BackendVersion),
	)
	defer dep.Shutdown(context.Background())
	defer manager.CloseFTSExternalKafka()

	ctx := context.Background()
	settings := buildBaseSettings(smokeCase{
		Mode:           "primary",
		UseGlobalKafka: false,
	}, kafkaTopics{
		Process: "process",
		Result:  "result",
		Error:   "error",
		Group:   "cloudreve-fts-external",
	})
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
	return setAdminGroupPolicy(ctx, dep, originalPolicyID)
}

func ensureSmokeFTSSettings(ctx context.Context, dep dependency.Dep, tc smokeCase, topics kafkaTopics) error {
	settings := buildBaseSettings(tc, topics)
	if err := dep.SettingClient().Set(ctx, settings); err != nil {
		return err
	}

	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}
	return dep.KV().Delete(appsetting.KvSettingPrefix, keys...)
}

func buildBaseSettings(tc smokeCase, topics kafkaTopics) map[string]string {
	settings := map[string]string{
		"siteURL":                                     defaultSiteURL,
		"fts_enabled":                                 "1",
		"fts_index_type":                              "elasticsearch",
		"fts_extractor_type":                          "tika",
		"fts_external_enabled":                        "1",
		"fts_external_mode":                           tc.Mode,
		"fts_external_use_global_kafka":               boolString(tc.UseGlobalKafka),
		"fts_external_timeout_seconds":                "20",
		"fts_external_retry_max":                      "0",
		"fts_external_recursive_attachments":          "1",
		"fts_external_skip_encrypted_files":           "1",
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
		"fts_elasticsearch_endpoint":                  elasticsearchURL,
		"fts_tika_endpoint":                           tikaEndpoint,
		"fts_tika_document_enabled":                   "1",
		"fts_tika_archive_enabled":                    "1",
		"fts_tika_sidecar_enabled":                    "1",
		"fts_tika_sidecar_text_enabled":               "1",
		"fts_tika_sidecar_assets_enabled":             "1",
		"fts_tika_extract_inline_images":              "1",
		"fts_tika_document_exts":                      "txt,text,md,markdown,csv,tsv,html,htm,xhtml,xml,rtf,pdf,eml,mbox,tnef",
		"fts_tika_archive_exts":                       "zip,tar,tgz,tbz,tbz2,txz,tlz,7z,rar,ar,gz,z,bz,bz2,xz,lzma,lz4,br,snappy,sz,pack200,cpio,arj,dump,jar,war,ear",
	}
	return settings
}

func boolString(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func must(err error, message string) {
	if err != nil {
		panic(fmt.Sprintf("%s: %v", message, err))
	}
}
