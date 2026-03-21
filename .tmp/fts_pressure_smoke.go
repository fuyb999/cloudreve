package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	taskmodel "github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/gofrs/uuid"
)

const (
	pressureUserID = 1
	pressureES     = "http://127.0.0.1:9200"
)

type pressureDoc struct {
	Found  bool `json:"found"`
	Source struct {
		PathText string `json:"path_text"`
		Content  string `json:"content"`
	} `json:"_source"`
}

func main() {
	logger := logging.NewConsoleLogger(logging.LevelDebug)
	dep := dependency.NewDependency(
		dependency.WithConfigPath(".tmp/fts_real_smoke.ini"),
		dependency.WithLogger(logger),
		dependency.WithRequiredDbVersion(constants.BackendVersion),
	)
	defer dep.Shutdown(context.Background())

	ctx := context.WithValue(context.Background(), dependency.DepCtx{}, dep)
	ctx = context.WithValue(ctx, inventory.LoadTaskUser{}, true)

	user, err := dep.UserClient().GetLoginUserByID(ctx, pressureUserID)
	must(err, "load login user")
	ctx = context.WithValue(ctx, inventory.UserCtx{}, user)
	ctx = context.WithValue(ctx, inventory.UserIDCtx{}, user.ID)

	indexName := dep.SettingProvider().FTSIndexElasticsearch(ctx).Index
	count := envInt("FTS_PRESSURE_COUNT", 64)
	restoreCount := min(envInt("FTS_PRESSURE_RESTORE", 8), count/2)
	deleteCount := min(envInt("FTS_PRESSURE_DELETE", count/2), count)

	fm := manager.NewFileManager(dep, user)
	defer fm.Recycle()

	suffix := time.Now().Format("20060102_150405")
	root := mustURI(fmt.Sprintf("cloudreve://my/__fts_pressure_%s", suffix))
	srcDir := root.Join("src")
	dstDir := root.Join("dst")

	must(createFolder(ctx, fm, root), "create pressure root")
	must(createFolder(ctx, fm, srcDir), "create pressure src")
	must(createFolder(ctx, fm, dstDir), "create pressure dst")

	fileIDs := make([]int, 0, count)
	sourceURIs := make([]*fs.URI, 0, count)
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("pressure-%03d.txt", i)
		content := fmt.Sprintf("pressure content %03d %s", i, suffix)
		file, _, err := uploadText(ctx, fm, srcDir.Join(name), content)
		must(err, "upload pressure file "+name)
		fileIDs = append(fileIDs, file.ID())
		sourceURIs = append(sourceURIs, srcDir.Join(name))
	}

	must(drainFTSTasksForFiles(ctx, dep, user, fileIDs...), "drain pressure upload")
	must(refreshES(indexName), "refresh es after pressure upload")
	assertPressureState(indexName, fileIDs, func(i int) string {
		return srcDir.Join(fmt.Sprintf("pressure-%03d.txt", i)).String()
	}, "upload")

	moveCtx, moveCID := withCorrelation(ctx)
	must(fm.MoveOrCopy(moveCtx, sourceURIs, dstDir, false), "pressure batch move")
	_ = moveCID
	must(drainFTSTasksForFiles(ctx, dep, user, fileIDs...), "drain pressure move")
	must(refreshES(indexName), "refresh es after pressure move")
	assertPressureState(indexName, fileIDs, func(i int) string {
		return dstDir.Join(fmt.Sprintf("pressure-%03d.txt", i)).String()
	}, "move")

	renameCtx, renameCID := withCorrelation(ctx)
	_, err = fm.Rename(renameCtx, dstDir, "final")
	must(err, "pressure rename folder")
	_ = renameCID
	finalDir := root.Join("final")
	must(drainFTSTasksForFiles(ctx, dep, user, fileIDs...), "drain pressure folder rename")
	must(refreshES(indexName), "refresh es after pressure folder rename")
	assertPressureState(indexName, fileIDs, func(i int) string {
		return finalDir.Join(fmt.Sprintf("pressure-%03d.txt", i)).String()
	}, "folder rename")

	deleteIDs := append([]int(nil), fileIDs[:deleteCount]...)
	deleteURIs := make([]*fs.URI, 0, deleteCount)
	for i := 0; i < deleteCount; i++ {
		deleteURIs = append(deleteURIs, finalDir.Join(fmt.Sprintf("pressure-%03d.txt", i)))
	}
	deleteCtx, deleteCID := withCorrelation(ctx)
	must(fm.Delete(deleteCtx, deleteURIs), "pressure batch soft delete")
	_ = deleteCID
	must(drainFTSTasksForFiles(ctx, dep, user, deleteIDs...), "drain pressure soft delete")
	must(refreshES(indexName), "refresh es after pressure soft delete")
	assertMissingCount(indexName, deleteIDs, deleteCount, "soft delete")

	restoreIDs := append([]int(nil), deleteIDs[:restoreCount]...)
	for _, fileID := range restoreIDs {
		trashed, err := fm.TraverseFile(ctx, fileID)
		must(err, fmt.Sprintf("load trashed pressure file %d", fileID))
		restoreCtx, restoreCID := withCorrelation(ctx)
		must(fm.Restore(restoreCtx, trashed.Uri(true)), fmt.Sprintf("restore pressure file %d", fileID))
		_ = restoreCID
	}
	must(drainFTSTasksForFiles(ctx, dep, user, restoreIDs...), "drain pressure restore")
	must(refreshES(indexName), "refresh es after pressure restore")
	assertPressureState(indexName, restoreIDs, func(i int) string {
		return finalDir.Join(fmt.Sprintf("pressure-%03d.txt", i)).String()
	}, "restore subset")

	cleanupCtx, cleanupCID := withCorrelation(ctx)
	cleanupErr := fm.Delete(cleanupCtx, []*fs.URI{root}, fs.WithSysSkipSoftDelete(true))
	if cleanupErr == nil {
		_ = cleanupCID
		cleanupErr = drainFTSTasksForFiles(ctx, dep, user, fileIDs...)
	}
	if cleanupErr == nil {
		cleanupErr = refreshES(indexName)
	}
	if cleanupErr != nil {
		fmt.Fprintf(os.Stderr, "cleanup warning: %v\n", cleanupErr)
	}

	fmt.Printf("pressure ok files=%d deleted=%d restored=%d index=%s\n", count, deleteCount, restoreCount, indexName)
}

func assertPressureState(indexName string, fileIDs []int, wantPath func(i int) string, label string) {
	missing := 0
	mismatch := 0
	emptyContent := 0
	for i, fileID := range fileIDs {
		doc, err := getDoc(indexName, fileID)
		must(err, fmt.Sprintf("get pressure doc %d", fileID))
		if !doc.Found {
			missing++
			continue
		}
		if doc.Source.PathText != wantPath(i) {
			mismatch++
		}
		if strings.TrimSpace(doc.Source.Content) == "" {
			emptyContent++
		}
	}

	if missing > 0 || mismatch > 0 || emptyContent > 0 {
		panic(fmt.Sprintf("%s pressure check failed: missing=%d mismatch=%d empty_content=%d", label, missing, mismatch, emptyContent))
	}
	fmt.Printf("%s pressure ok files=%d missing=%d mismatch=%d empty_content=%d\n", label, len(fileIDs), missing, mismatch, emptyContent)
}

func assertMissingCount(indexName string, fileIDs []int, want int, label string) {
	missing := 0
	for _, fileID := range fileIDs {
		doc, err := getDoc(indexName, fileID)
		must(err, fmt.Sprintf("get pressure missing doc %d", fileID))
		if !doc.Found {
			missing++
		}
	}
	if missing != want {
		panic(fmt.Sprintf("%s pressure missing mismatch: got=%d want=%d", label, missing, want))
	}
	fmt.Printf("%s pressure ok expected_missing=%d actual_missing=%d\n", label, want, missing)
}

func createFolder(ctx context.Context, fm manager.FileManager, uri *fs.URI) error {
	_, err := fm.Create(ctx, uri, types.FileTypeFolder)
	return err
}

func uploadText(ctx context.Context, fm manager.FileManager, uri *fs.URI, content string) (fs.File, uuid.UUID, error) {
	reader := bytes.NewReader([]byte(content))
	req := &fs.UploadRequest{
		Props: &fs.UploadProps{
			Uri:  uri,
			Size: int64(len(content)),
		},
		File:   io.NopCloser(reader),
		Seeker: reader,
	}
	opCtx, cid := withCorrelation(ctx)
	file, err := fm.Update(opCtx, req)
	return file, cid, err
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

	for round := 0; round < 16; round++ {
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
		sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })

		for _, model := range tasks {
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
			if status != taskmodel.StatusCompleted {
				return fmt.Errorf("task %d (%s) finished with status %s", model.ID, model.Type, status)
			}
			if err := dep.TaskClient().SetCompleteByID(execCtx, model.ID); err != nil {
				return fmt.Errorf("complete task %d: %w", model.ID, err)
			}
		}
	}

	return fmt.Errorf("pending pressure tasks remained for files %v", fileIDs)
}

func refreshES(indexName string) error {
	req, err := http.NewRequest(http.MethodPost, pressureES+"/"+indexName+"/_refresh", nil)
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

func getDoc(indexName string, fileID int) (*pressureDoc, error) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/%s/_doc/%d", pressureES, indexName, fileID), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &pressureDoc{}, nil
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get doc %d: %s %s", fileID, resp.Status, strings.TrimSpace(string(body)))
	}

	doc := &pressureDoc{}
	if err := json.NewDecoder(resp.Body).Decode(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func mustURI(raw string) *fs.URI {
	uri, err := fs.NewUriFromString(raw)
	must(err, "parse uri")
	return uri
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

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func must(err error, step string) {
	if err != nil {
		panic(fmt.Sprintf("%s: %v", step, err))
	}
}
