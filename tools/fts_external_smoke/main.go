package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/IBM/sarama"
	aws2 "github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	awss3 "github.com/aws/aws-sdk-go/service/s3"
	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/file"
	entftsexternaljob "github.com/cloudreve/Cloudreve/v4/ent/ftsexternaljob"
	entmetadata "github.com/cloudreve/Cloudreve/v4/ent/metadata"
	entstoragepolicy "github.com/cloudreve/Cloudreve/v4/ent/storagepolicy"
	enttask "github.com/cloudreve/Cloudreve/v4/ent/task"
	entuser "github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/request"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
)

const (
	adminUserID        = 1
	minioEndpoint      = "http://127.0.0.1:9000"
	minioAccessKey     = "minio"
	minioSecretKey     = "minio123456"
	minioRegion        = "us-east-1"
	smokeES            = "http://127.0.0.1:9200"
	smokeBucket        = "cloudreve-fts-real-smoke"
	smokePolicyName    = "FTS Real Smoke S3"
	waitCleanupTimeout = 45 * time.Second
	waitJobTimeout     = 45 * time.Second
	waitFinalTimeout   = 70 * time.Second
)

type resultMessage struct {
	Version       int    `json:"version"`
	RequestID     string `json:"request_id"`
	SnapshotToken string `json:"snapshot_token"`
	Status        string `json:"status"`
	Provider      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"provider"`
	Root struct {
		Content      string            `json:"content"`
		Metadata     map[string]string `json:"metadata,omitempty"`
		Warnings     []string          `json:"warnings,omitempty"`
		QualityScore float64           `json:"quality_score"`
	} `json:"root"`
}

type smokeRun struct {
	Index     int
	FileName  string
	ObjectKey string
	FileID    int
	EntityID  int
	Job       *ent.FTSExternalJob
}

type esDoc struct {
	Found bool `json:"found"`
}

func main() {
	util.UseWorkingDir = true
	ctx := context.Background()
	batchCount := envInt("REAL_FTS_SMOKE_BATCH_COUNT", 1)
	cleanupPrefix := strings.TrimSpace(os.Getenv("REAL_FTS_SMOKE_CLEANUP_PREFIX"))
	cleanupFileIDs := parseCSVInts(os.Getenv("REAL_FTS_SMOKE_CLEANUP_FILE_IDS"))
	resultDelay := envInt("REAL_FTS_SMOKE_RESULT_DELAY_SECONDS", 0)
	skipResult := envBool("REAL_FTS_SMOKE_SKIP_RESULT", false)
	configPath, projectRoot, err := resolveConfigPath()
	if err != nil {
		panic(err)
	}
	if err := os.Chdir(projectRoot); err != nil {
		panic(fmt.Errorf("chdir project root: %w", err))
	}
	dbPath := filepath.Join(projectRoot, ".tmp", "data", "cloudreve.db")
	if err := os.Setenv("CR_CONF_Database.Type", "sqlite"); err != nil {
		panic(err)
	}
	if err := os.Setenv("CR_CONF_Database.DBFile", dbPath); err != nil {
		panic(err)
	}

	dep := dependency.NewDependency(
		dependency.WithConfigPath(configPath),
		dependency.WithLogger(logging.NewConsoleLogger(logging.LevelInformational)),
		dependency.WithRequiredDbVersion(constants.BackendVersion),
	)
	defer dep.Shutdown(context.Background())
	request.GeneralClient = dep.RequestClient()
	dep.ContentProcessingQueue(ctx).Start()

	if err := ensureBucket(ctx); err != nil {
		panic(err)
	}

	policy, err := ensureSmokePolicy(ctx, dep)
	if err != nil {
		panic(err)
	}
	fmt.Printf("policy_id=%d name=%s type=%s bucket=%s\n", policy.ID, policy.Name, policy.Type, policy.BucketName)

	adminUser, err := loadAdminUser(ctx, dep)
	if err != nil {
		panic(err)
	}
	esIndex := dep.SettingProvider().FTSIndexElasticsearch(ctx).Index

	if cleanupPrefix != "" || len(cleanupFileIDs) > 0 {
		if err := cleanupSmokeFiles(ctx, dep, adminUser, esIndex, cleanupPrefix, cleanupFileIDs); err != nil {
			panic(err)
		}
		return
	}

	if batchCount < 1 {
		batchCount = 1
	}
	if batchCount > 1 {
		fmt.Printf("batch_count=%d\n", batchCount)
	}

	uri, err := fs.NewUriFromString("cloudreve://my")
	if err != nil {
		panic(err)
	}

	for i := 1; i <= batchCount; i++ {
		run, createErr := createSmokeRun(ctx, dep, adminUser, policy.ID, uri, i)
		if createErr != nil {
			panic(createErr)
		}
		if resultDelay > 0 {
			fmt.Printf("[%d/%d] result_publish_delay_seconds=%d\n", run.Index, batchCount, resultDelay)
			time.Sleep(time.Duration(resultDelay) * time.Second)
		}
		if skipResult {
			fmt.Printf("[%d/%d] result_publish_skipped request_id=%s\n", run.Index, batchCount, run.Job.RequestID)
		} else {
			if err := publishResult(run.Job); err != nil {
				panic(err)
			}
			fmt.Printf("[%d/%d] result_published request_id=%s\n", run.Index, batchCount, run.Job.RequestID)
		}
		finalJob, finalTask, waitErr := waitForFinalization(ctx, dep, run.FileID, run.Job.RequestID, waitFinalTimeout)
		if waitErr != nil {
			panic(waitErr)
		}
		printFinalization(ctx, dep, run, finalJob, finalTask, batchCount)
	}
}

func createSmokeRun(ctx context.Context, dep dependency.Dep, adminUser *ent.User, policyID int, uri *fs.URI, index int) (smokeRun, error) {
	fileName := fmt.Sprintf("fts-real-smoke-%s-%02d-%d.txt", time.Now().Format("20060102-150405"), index, time.Now().UnixNano())
	objectKey := filepath.ToSlash(filepath.Join("fts-real-smoke", fileName))
	content := []byte(strings.Join([]string{
		"第三方全文抽取真实联调样例。",
		"该文件先直接写入 MinIO/S3，再通过 Cloudreve ImportPhysical 导入，用于验证外部全文抽取、Kafka process/result 队列与 Elasticsearch 建索引的完整闭环。",
		"若链路正常，应自动创建 fts_external_jobs 记录，并在消费 result topic 后写入 sidecar 与索引。",
	}, "\n"))
	if err := putSmokeObject(ctx, objectKey, content); err != nil {
		return smokeRun{}, err
	}
	fmt.Printf("[%d] minio_object key=%s\n", index, objectKey)

	fileID, entityID, err := importSmokeFile(ctx, dep, adminUser, uri, policyID, fileName, objectKey, int64(len(content)))
	if err != nil {
		return smokeRun{}, err
	}
	fmt.Printf("[%d] uploaded file_id=%d entity_id=%d uri=%s\n", index, fileID, entityID, uri.String())

	job, err := waitForExternalJob(ctx, dep, fileID, waitJobTimeout)
	if err != nil {
		latestTasks, _ := dep.DBClient().Task.Query().Where(enttask.TypeEQ("full_text_index")).Order(ent.Desc(enttask.FieldID)).Limit(5).All(ctx)
		fmt.Printf("latest_full_text_tasks=%d\n", len(latestTasks))
		for _, t := range latestTasks {
			fmt.Printf("task id=%d status=%s updated_at=%s public=%s\n", t.ID, t.Status, t.UpdatedAt.Format(time.RFC3339), string(mustJSON(t.PublicState)))
		}
		return smokeRun{}, err
	}
	fmt.Printf("[%d] job_created id=%d request_id=%s status=%s trigger=%s attempt=%d\n", index, job.ID, job.RequestID, job.Status, job.TriggerReason, job.Attempt)

	return smokeRun{
		Index:     index,
		FileName:  fileName,
		ObjectKey: objectKey,
		FileID:    fileID,
		EntityID:  entityID,
		Job:       job,
	}, nil
}

func printFinalization(ctx context.Context, dep dependency.Dep, run smokeRun, finalJob *ent.FTSExternalJob, finalTask *ent.Task, batchCount int) {
	finalizationMode := "external_success"
	if finalJob.Status == "error" && strings.Contains(finalJob.ErrorPayload, "local fallback triggered") {
		finalizationMode = "local_fallback"
	}
	fmt.Printf("[%d/%d] finalization_mode=%s\n", run.Index, batchCount, finalizationMode)
	fmt.Printf("[%d/%d] final_job status=%s manifest_path=%s completed_at=%s\n", run.Index, batchCount, finalJob.Status, finalJob.ManifestPath, finalJob.CompletedAt.Format(time.RFC3339))
	if finalTask != nil {
		fmt.Printf("[%d/%d] final_task id=%d status=%s updated_at=%s\n", run.Index, batchCount, finalTask.ID, finalTask.Status, finalTask.UpdatedAt.Format(time.RFC3339))
	}

	meta, err := dep.DBClient().Metadata.Query().
		Where(entmetadata.FileIDEQ(run.FileID)).
		All(ctx)
	if err == nil {
		var manifestVal, entityVal, indexVal string
		for _, m := range meta {
			if m.Name == "sys:fts_sidecar_manifest" {
				manifestVal = m.Value
			}
			if m.Name == "sys:fts_sidecar_entity_id" {
				entityVal = m.Value
			}
			if m.Name == "sys:fulltext_index" {
				indexVal = m.Value
			}
		}
		fmt.Printf("[%d/%d] file_metadata manifest=%s entity=%s index=%s\n", run.Index, batchCount, manifestVal, entityVal, indexVal)
	}
}

func cleanupSmokeFiles(ctx context.Context, dep dependency.Dep, adminUser *ent.User, esIndex, prefix string, explicitIDs []int) error {
	targetIDs := map[int]struct{}{}
	for _, id := range explicitIDs {
		if id > 0 {
			targetIDs[id] = struct{}{}
		}
	}
	if prefix != "" {
		files, err := dep.DBClient().File.Query().
			Where(file.NameHasPrefix(prefix)).
			Order(ent.Asc(file.FieldID)).
			All(ctx)
		if err != nil {
			return fmt.Errorf("query files by prefix %q: %w", prefix, err)
		}
		for _, item := range files {
			targetIDs[item.ID] = struct{}{}
		}
	}
	if len(targetIDs) == 0 {
		fmt.Println("cleanup matched no files")
		return nil
	}

	ids := make([]int, 0, len(targetIDs))
	for id := range targetIDs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	fmt.Printf("cleanup matched file_ids=%v\n", ids)

	fm := manager.NewFileManager(dep, adminUser)
	defer fm.Recycle()
	opCtx := context.WithValue(ctx, inventory.UserCtx{}, adminUser)
	opCtx = context.WithValue(opCtx, inventory.UserIDCtx{}, adminUser.ID)

	for _, id := range ids {
		traversed, err := fm.TraverseFile(opCtx, id)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "not found") {
				fmt.Printf("cleanup skip file_id=%d reason=not_found\n", id)
				continue
			}
			return fmt.Errorf("traverse file %d: %w", id, err)
		}
		uri := traversed.Uri(true)
		if err := fm.Delete(opCtx, []*fs.URI{uri}, fs.WithSysSkipSoftDelete(true)); err != nil {
			return fmt.Errorf("delete file %d (%s): %w", id, uri.String(), err)
		}
		fmt.Printf("cleanup deleted file_id=%d uri=%s\n", id, uri.String())
	}

	if err := waitForCleanup(ctx, dep, esIndex, ids, waitCleanupTimeout); err != nil {
		return err
	}
	fmt.Printf("cleanup completed file_ids=%v\n", ids)
	return nil
}

func ensureBucket(ctx context.Context) error {
	sess, err := session.NewSession(&aws2.Config{
		Credentials:      credentials.NewStaticCredentials(minioAccessKey, minioSecretKey, ""),
		Endpoint:         aws2.String(minioEndpoint),
		Region:           aws2.String(minioRegion),
		S3ForcePathStyle: aws2.Bool(true),
	})
	if err != nil {
		return fmt.Errorf("create minio session: %w", err)
	}
	svc := awss3.New(sess)
	_, err = svc.HeadBucketWithContext(ctx, &awss3.HeadBucketInput{Bucket: aws2.String(smokeBucket)})
	if err == nil {
		return nil
	}
	if _, err := svc.CreateBucketWithContext(ctx, &awss3.CreateBucketInput{Bucket: aws2.String(smokeBucket)}); err != nil {
		if !strings.Contains(err.Error(), awss3.ErrCodeBucketAlreadyOwnedByYou) && !strings.Contains(err.Error(), awss3.ErrCodeBucketAlreadyExists) {
			return fmt.Errorf("create bucket %s: %w", smokeBucket, err)
		}
	}
	return nil
}

func ensureSmokePolicy(ctx context.Context, dep dependency.Dep) (*ent.StoragePolicy, error) {
	existing, err := dep.DBClient().StoragePolicy.Query().Where(entstoragepolicy.NameEQ(smokePolicyName)).Only(ctx)
	if err == nil {
		if !existing.IsPrivate {
			updated, updateErr := dep.StoragePolicyClient().Upsert(ctx, &ent.StoragePolicy{
				ID:         existing.ID,
				Name:       existing.Name,
				Type:       existing.Type,
				Server:     existing.Server,
				BucketName: existing.BucketName,
				IsPrivate:  true,
				AccessKey:  existing.AccessKey,
				SecretKey:  existing.SecretKey,
				Settings:   existing.Settings,
			})
			if updateErr != nil {
				return nil, updateErr
			}
			return updated, nil
		}
		return existing, nil
	}
	settings := types.PolicySetting{
		Region:           minioRegion,
		S3ForcePathStyle: true,
		Relay:            true,
	}
	policy := &ent.StoragePolicy{
		Name:       smokePolicyName,
		Type:       string(types.PolicyTypeS3),
		Server:     minioEndpoint,
		BucketName: smokeBucket,
		IsPrivate:  true,
		AccessKey:  minioAccessKey,
		SecretKey:  minioSecretKey,
		Settings:   &settings,
	}
	created, err := dep.StoragePolicyClient().Upsert(ctx, policy)
	if err != nil {
		return nil, err
	}
	return created, nil
}

func loadAdminUser(ctx context.Context, dep dependency.Dep) (*ent.User, error) {
	user, err := dep.UserClient().GetLoginUserByID(ctx, adminUserID)
	if err == nil {
		return user, nil
	}
	fallback, fallbackErr := dep.DBClient().User.Query().Where(entuser.IDEQ(adminUserID)).Only(ctx)
	if fallbackErr != nil {
		return nil, fmt.Errorf("load admin user: %w; fallback: %v", err, fallbackErr)
	}
	return fallback, nil
}

func putSmokeObject(ctx context.Context, objectKey string, content []byte) error {
	sess, err := session.NewSession(&aws2.Config{
		Credentials:      credentials.NewStaticCredentials(minioAccessKey, minioSecretKey, ""),
		Endpoint:         aws2.String(minioEndpoint),
		Region:           aws2.String(minioRegion),
		S3ForcePathStyle: aws2.Bool(true),
	})
	if err != nil {
		return fmt.Errorf("create minio session for put object: %w", err)
	}
	svc := awss3.New(sess)
	_, err = svc.PutObjectWithContext(ctx, &awss3.PutObjectInput{
		Bucket:      aws2.String(smokeBucket),
		Key:         aws2.String(objectKey),
		Body:        bytes.NewReader(content),
		ContentType: aws2.String("text/plain; charset=utf-8"),
	})
	if err != nil {
		return fmt.Errorf("put smoke object %s: %w", objectKey, err)
	}
	return nil
}

func importSmokeFile(ctx context.Context, dep dependency.Dep, user *ent.User, dst *fs.URI, policyID int, fileName, objectKey string, size int64) (int, int, error) {
	m := manager.NewFileManager(dep, user)
	defer m.Recycle()
	uploadCtx := context.WithValue(ctx, inventory.UserCtx{}, user)

	if err := m.ImportPhysical(uploadCtx, dst, policyID, fs.PhysicalObject{
		Source:       objectKey,
		RelativePath: fileName,
		Size:         size,
		LastModify:   time.Now(),
		IsDir:        false,
	}, true); err != nil {
		return 0, 0, fmt.Errorf("import physical object: %w", err)
	}

	fileModel, err := dep.DBClient().File.Query().Where(file.NameEQ(fileName)).Order(ent.Desc(file.FieldID)).First(uploadCtx)
	if err != nil {
		return 0, 0, fmt.Errorf("load imported file %s: %w", fileName, err)
	}
	return fileModel.ID, fileModel.PrimaryEntity, nil
}

func waitForExternalJob(ctx context.Context, dep dependency.Dep, fileID int, timeout time.Duration) (*ent.FTSExternalJob, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		job, err := dep.DBClient().FTSExternalJob.Query().
			Where(entftsexternaljob.FileIDEQ(fileID)).
			Order(ent.Desc(entftsexternaljob.FieldID)).
			First(ctx)
		if err == nil {
			return job, nil
		}
		time.Sleep(1 * time.Second)
	}
	return nil, fmt.Errorf("timeout waiting for external job for file_id=%d", fileID)
}

func publishResult(job *ent.FTSExternalJob) error {
	cfg := sarama.NewConfig()
	cfg.Version = sarama.V3_7_0_0
	cfg.Producer.RequiredAcks = sarama.WaitForAll
	cfg.Producer.Return.Successes = true
	cfg.Producer.Idempotent = true
	cfg.Net.MaxOpenRequests = 1

	producer, err := sarama.NewSyncProducer([]string{"127.0.0.1:9092"}, cfg)
	if err != nil {
		return fmt.Errorf("create kafka producer: %w", err)
	}
	defer producer.Close()

	payload := resultMessage{
		Version:       1,
		RequestID:     job.RequestID,
		SnapshotToken: job.SnapshotToken,
		Status:        "success",
	}
	payload.Provider.Name = "real-smoke-runner"
	payload.Provider.Version = "1.0.0"
	payload.Root.Content = "这是由真实 smoke test 通过 Kafka result topic 回写的外部抽取内容，用于验证 Cloudreve 第三方全文抽取链路。"
	payload.Root.QualityScore = 0.98
	payload.Root.Metadata = map[string]string{"lang": "zh-CN", "source": "real-smoke"}

	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal result payload: %w", err)
	}

	_, _, err = producer.SendMessage(&sarama.ProducerMessage{
		Topic: "result",
		Key:   sarama.StringEncoder(job.RequestID),
		Value: sarama.ByteEncoder(raw),
		Headers: []sarama.RecordHeader{
			{Key: []byte("content-type"), Value: []byte("application/json")},
		},
	})
	if err != nil {
		return fmt.Errorf("publish result topic: %w", err)
	}
	return nil
}

func waitForCleanup(ctx context.Context, dep dependency.Dep, indexName string, fileIDs []int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := refreshES(indexName); err != nil {
			return err
		}
		pending := 0
		for _, id := range fileIDs {
			exists, err := dep.DBClient().File.Query().Where(file.IDEQ(id)).Exist(ctx)
			if err != nil {
				return fmt.Errorf("check file %d exists: %w", id, err)
			}
			doc, err := getESDoc(indexName, id)
			if err != nil {
				return err
			}
			if exists || doc.Found {
				pending++
			}
		}
		if pending == 0 {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timeout waiting cleanup for file_ids=%v", fileIDs)
}

func waitForFinalization(ctx context.Context, dep dependency.Dep, fileID int, requestID string, timeout time.Duration) (*ent.FTSExternalJob, *ent.Task, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		job, err := dep.DBClient().FTSExternalJob.Query().Where(entftsexternaljob.RequestIDEQ(requestID)).Only(ctx)
		if err != nil {
			return nil, nil, err
		}
		var latestTask *ent.Task
		latestTask, _ = dep.DBClient().Task.Query().Where(enttask.TypeEQ("full_text_index")).Order(ent.Desc(enttask.FieldID)).First(ctx)
		meta := map[string]string{}
		metaRows, metaErr := dep.DBClient().Metadata.Query().Where(entmetadata.FileIDEQ(fileID)).All(ctx)
		if metaErr == nil {
			for _, row := range metaRows {
				meta[row.Name] = row.Value
			}
		}
		manifestPath := job.ManifestPath
		if manifestPath == "" {
			manifestPath = meta["sys:fts_sidecar_manifest"]
		}
		hasIndexedDoc := meta["sys:fulltext_index"] != ""
		fallbackCompleted := job.Status == "error" && strings.Contains(job.ErrorPayload, "local fallback triggered")
		if manifestPath != "" && hasIndexedDoc && latestTask != nil && latestTask.Status == "completed" && (job.Status == "success" || fallbackCompleted) {
			job.ManifestPath = manifestPath
			return job, latestTask, nil
		}
		time.Sleep(2 * time.Second)
	}
	job, _ := dep.DBClient().FTSExternalJob.Query().Where(entftsexternaljob.RequestIDEQ(requestID)).Only(ctx)
	latestTask, _ := dep.DBClient().Task.Query().Where(enttask.TypeEQ("full_text_index")).Order(ent.Desc(enttask.FieldID)).First(ctx)
	return job, latestTask, fmt.Errorf("timeout waiting for finalization file_id=%d request_id=%s", fileID, requestID)
}

func mustJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}

func refreshES(indexName string) error {
	req, err := http.NewRequest(http.MethodPost, smokeES+"/"+indexName+"/_refresh", nil)
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

func getESDoc(indexName string, fileID int) (*esDoc, error) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/%s/_doc/%d", smokeES, indexName, fileID), nil)
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
		return nil, fmt.Errorf("get es doc %d: %s %s", fileID, resp.Status, strings.TrimSpace(string(body)))
	}
	doc := &esDoc{}
	if err := json.NewDecoder(resp.Body).Decode(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	var v int
	if _, err := fmt.Sscanf(raw, "%d", &v); err != nil {
		return fallback
	}
	return v
}

func envBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if raw == "" {
		return fallback
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func parseCSVInts(raw string) []int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.Atoi(part)
		if err != nil || v <= 0 {
			continue
		}
		out = append(out, v)
	}
	return out
}

func resolveConfigPath() (string, string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", "", fmt.Errorf("runtime caller unavailable")
	}
	current := filepath.Dir(thisFile)
	for i := 0; i < 6; i++ {
		configPath := filepath.Join(current, ".tmp", "data", "conf.ini")
		if _, err := os.Stat(configPath); err == nil {
			return configPath, current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return "", "", fmt.Errorf("unable to locate .tmp/data/conf.ini from %s", thisFile)
}
