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
	entnode "github.com/cloudreve/Cloudreve/v4/ent/node"
	taskmodel "github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/workflows"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	appsetting "github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	"github.com/gofrs/uuid"
)

const (
	smokeUserID = 1
	esEndpoint  = "http://127.0.0.1:9200"
)

var (
	esIndex                     = "cloudreve_files"
	smokePreferredContentNodeID int
)

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

type smokeFullTextCopyTaskState struct {
	Uri            *fs.URI `json:"uri"`
	OriginalFileID int     `json:"original_file_id"`
	FileID         int     `json:"file_id"`
	OwnerID        int     `json:"owner_id"`
	EntityID       int     `json:"entity_id"`
	Phase          string  `json:"phase,omitempty"`
	WaitReason     string  `json:"wait_reason,omitempty"`
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
	must(err, "load smoke user")
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
	esIndex = dep.SettingProvider().FTSIndexElasticsearch(reloadCtx).Index

	fm := manager.NewFileManager(dep, user)
	defer fm.Recycle()

	suffix := time.Now().Format("20060102_150405")
	root := mustURI("cloudreve://my/__share_save_follow_smoke_" + suffix)
	srcDir := root.Join("src")
	saveDir := root.Join("saved")

	must(createFolder(baseCtx, fm, root), "create root folder")
	must(createFolder(baseCtx, fm, srcDir), "create src folder")
	must(createFolder(baseCtx, fm, saveDir), "create save folder")

	pdfText := "share save follow extract pdf " + suffix
	attachmentText := "share save follow extract attachment " + suffix
	sourceURI := srcDir.Join("follow-save.pdf")
	sourceFile, uploadCID, err := uploadBytes(baseCtx, fm, sourceURI, buildMinimalPDFWithEmbeddedFile(pdfText, "attached.txt", attachmentText))
	must(err, "upload source pdf")
	fmt.Printf("source upload ok file_id=%d uri=%s cid=%s\n", sourceFile.ID(), sourceURI.String(), uploadCID)

	sourceTasks, err := waitPendingFullTextIndexTasksForFile(baseCtx, dep, sourceFile.ID(), 10*time.Second)
	must(err, "wait source full_text_index")
	fmt.Printf("source pending full_text_index task_ids=%v\n", taskIDs(sourceTasks))

	shareCtx, shareCID := withCorrelation(baseCtx)
	shareTask, err := workflows.NewShareSaveTask(shareCtx, user, sourceURI.String(), saveDir.String())
	must(err, "build share save task")
	shareStatus, err := shareTask.Do(shareCtx)
	must(err, "execute share save task")
	if shareStatus != taskmodel.StatusCompleted {
		panic(fmt.Sprintf("share save returned unexpected status: %s", shareStatus))
	}
	fmt.Printf("share save ok cid=%s src=%s dst=%s\n", shareCID, sourceURI.String(), saveDir.String())

	copiedURI := saveDir.Join("follow-save.pdf")
	copiedFile, err := fm.Get(baseCtx, copiedURI)
	must(err, "load copied file")
	fmt.Printf("copied file ok file_id=%d uri=%s\n", copiedFile.ID(), copiedURI.String())

	copyTaskModel, err := waitPendingFullTextCopyTaskForFile(baseCtx, dep, copiedFile.ID(), 10*time.Second)
	must(err, "wait full_text_copy task")
	fmt.Printf(
		"copy task pending task_id=%d source_file_id=%d target_file_id=%d\n",
		copyTaskModel.ID,
		mustParseFullTextCopyTaskState(copyTaskModel.PrivateState).OriginalFileID,
		mustParseFullTextCopyTaskState(copyTaskModel.PrivateState).FileID,
	)

	copyIndexTasks, err := findAllFullTextIndexTasksForFile(baseCtx, dep, copiedFile.ID())
	must(err, "query copied file full_text_index tasks before copy execution")
	if len(copyIndexTasks) != 0 {
		panic(fmt.Sprintf("copied file %d should not have direct full_text_index tasks before copy execution, got task_ids=%v", copiedFile.ID(), taskIDs(copyIndexTasks)))
	}
	fmt.Printf("copied file direct full_text_index task count=0 before copy execution\n")

	firstStatus, firstCopyModel, err := executeTaskOnce(baseCtx, dep, user, copyTaskModel)
	must(err, "execute copy task first time")
	if firstStatus != taskmodel.StatusSuspending {
		panic(fmt.Sprintf("copy task first run should suspend while source extraction is pending, got %s", firstStatus))
	}

	firstCopyState := mustParseFullTextCopyTaskState(firstCopyModel.PrivateState)
	if firstCopyState.Phase != "await_source_extract" {
		panic(fmt.Sprintf("copy task first run should enter await_source_extract, got phase=%q", firstCopyState.Phase))
	}
	fmt.Printf(
		"copy task suspended task_id=%d phase=%s wait_reason=%s\n",
		firstCopyModel.ID,
		firstCopyState.Phase,
		firstCopyState.WaitReason,
	)

	must(drainFullTextIndexTasksForFile(baseCtx, dep, user, sourceFile.ID()), "drain source full_text_index")
	must(refreshES(), "refresh es after source extraction")
	assertDocPath(sourceFile.ID(), sourceURI.String(), "source indexed")
	assertDocContentContains(sourceFile.ID(), pdfText, "source indexed content")
	assertDocHasAttachment(sourceFile.ID(), "attached.txt", "source indexed attachment")
	assertFileFTSMetadataExists(baseCtx, dep, sourceFile.ID(), "source file metadata")

	reloadedCopyTask, err := dep.DBClient().Task.Get(baseCtx, firstCopyModel.ID)
	must(err, "reload copy task after source extraction")
	if reloadedCopyTask.Status != taskmodel.StatusCompleted {
		secondStatus, secondCopyModel, err := executeTaskOnce(baseCtx, dep, user, reloadedCopyTask)
		must(err, "execute copy task second time")
		if secondStatus != taskmodel.StatusCompleted {
			panic(fmt.Sprintf("copy task second run should complete after source extraction, got %s", secondStatus))
		}
		fmt.Printf("copy task completed task_id=%d status=%s\n", secondCopyModel.ID, secondStatus)
	} else {
		fmt.Printf("copy task already completed by running master task_id=%d\n", reloadedCopyTask.ID)
	}

	must(refreshES(), "refresh es after copied file rebuild")
	assertDocPath(copiedFile.ID(), copiedURI.String(), "copied file indexed")
	assertDocContentContains(copiedFile.ID(), pdfText, "copied file indexed content")
	assertDocHasAttachment(copiedFile.ID(), "attached.txt", "copied file indexed attachment")
	assertFileFTSMetadataExists(baseCtx, dep, copiedFile.ID(), "copied file metadata")

	copyIndexTasks, err = findAllFullTextIndexTasksForFile(baseCtx, dep, copiedFile.ID())
	must(err, "query copied file full_text_index tasks after copy execution")
	if len(copyIndexTasks) != 0 {
		panic(fmt.Sprintf("copied file %d should not have full_text_index tasks after copy execution, got task_ids=%v", copiedFile.ID(), taskIDs(copyIndexTasks)))
	}

	fmt.Printf(
		"share save follow extract smoke ok root=%s source_file_id=%d copied_file_id=%d\n",
		root.String(),
		sourceFile.ID(),
		copiedFile.ID(),
	)
}

func createFolder(ctx context.Context, fm manager.FileManager, uri *fs.URI) error {
	_, err := fm.Create(ctx, uri, types.FileTypeFolder)
	return err
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

func withCorrelation(ctx context.Context) (context.Context, uuid.UUID) {
	cid := uuid.Must(uuid.NewV4())
	return context.WithValue(ctx, logging.CorrelationIDCtx{}, cid), cid
}

func waitPendingFullTextIndexTasksForFile(ctx context.Context, dep dependency.Dep, fileID int, timeout time.Duration) ([]*ent.Task, error) {
	deadline := time.Now().Add(timeout)
	for {
		tasks, err := findPendingFullTextIndexTasksForFile(ctx, dep, fileID)
		if err != nil {
			return nil, err
		}
		if len(tasks) > 0 {
			return tasks, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for pending full_text_index task for file %d", fileID)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func waitPendingFullTextCopyTaskForFile(ctx context.Context, dep dependency.Dep, fileID int, timeout time.Duration) (*ent.Task, error) {
	deadline := time.Now().Add(timeout)
	for {
		taskModel, err := findPendingFullTextCopyTaskForFile(ctx, dep, fileID)
		if err != nil {
			return nil, err
		}
		if taskModel != nil {
			return taskModel, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for pending full_text_copy task for file %d", fileID)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func drainFullTextIndexTasksForFile(ctx context.Context, dep dependency.Dep, user *ent.User, fileID int) error {
	for round := 0; round < 40; round++ {
		tasks, err := findPendingFullTextIndexTasksForFile(ctx, dep, fileID)
		if err != nil {
			return err
		}
		if len(tasks) == 0 {
			return nil
		}

		fmt.Printf("source drain round=%d file_id=%d task_ids=%v\n", round+1, fileID, taskIDs(tasks))
		for _, model := range tasks {
			if smokePreferredContentNodeID > 0 {
				if err := forcePreferredContentProcessingNode(dep, model, smokePreferredContentNodeID); err != nil {
					return fmt.Errorf("force preferred content node for task %d: %w", model.ID, err)
				}
			}

			status, updatedModel, err := executeTaskOnce(ctx, dep, user, model)
			if err != nil {
				return fmt.Errorf("execute source full_text_index task %d: %w", model.ID, err)
			}

			state := mustParseFullTextIndexTaskState(updatedModel.PrivateState)
			fmt.Printf(
				"source task step task_id=%d status=%s phase=%s node_id=%d slave_id=%d\n",
				updatedModel.ID,
				status,
				state.Phase,
				state.NodeID,
				state.SlaveID,
			)
		}

		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("pending source full_text_index tasks remained for file %d after max rounds", fileID)
}

func findPendingFullTextIndexTasksForFile(ctx context.Context, dep dependency.Dep, fileID int) ([]*ent.Task, error) {
	matches, err := dep.DBClient().Task.Query().
		Where(
			taskmodel.TypeEQ(queue.FullTextIndexTaskType),
			taskmodel.StatusIn(taskmodel.StatusQueued, taskmodel.StatusProcessing, taskmodel.StatusSuspending),
			taskmodel.PrivateStateContains(strconv.Itoa(fileID)),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}

	filtered := make([]*ent.Task, 0, len(matches))
	for _, model := range matches {
		state, err := parseFullTextIndexTaskState(model.PrivateState)
		if err != nil {
			continue
		}
		if fullTextIndexStateTargetsFile(state, fileID) {
			filtered = append(filtered, model)
		}
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].ID < filtered[j].ID
	})
	return filtered, nil
}

func findAllFullTextIndexTasksForFile(ctx context.Context, dep dependency.Dep, fileID int) ([]*ent.Task, error) {
	matches, err := dep.DBClient().Task.Query().
		Where(
			taskmodel.TypeEQ(queue.FullTextIndexTaskType),
			taskmodel.PrivateStateContains(strconv.Itoa(fileID)),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}

	filtered := make([]*ent.Task, 0, len(matches))
	for _, model := range matches {
		state, err := parseFullTextIndexTaskState(model.PrivateState)
		if err != nil {
			continue
		}
		if fullTextIndexStateTargetsFile(state, fileID) {
			filtered = append(filtered, model)
		}
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].ID < filtered[j].ID
	})
	return filtered, nil
}

func findPendingFullTextCopyTaskForFile(ctx context.Context, dep dependency.Dep, fileID int) (*ent.Task, error) {
	matches, err := dep.DBClient().Task.Query().
		Where(
			taskmodel.TypeEQ(queue.FullTextCopyTaskType),
			taskmodel.StatusIn(taskmodel.StatusQueued, taskmodel.StatusProcessing, taskmodel.StatusSuspending),
			taskmodel.PrivateStateContains(strconv.Itoa(fileID)),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}

	filtered := make([]*ent.Task, 0, len(matches))
	for _, model := range matches {
		state, err := parseFullTextCopyTaskState(model.PrivateState)
		if err != nil {
			continue
		}
		if state.FileID == fileID {
			filtered = append(filtered, model)
		}
	}

	if len(filtered) == 0 {
		return nil, nil
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].ID < filtered[j].ID
	})
	return filtered[0], nil
}

func executeTaskOnce(ctx context.Context, dep dependency.Dep, user *ent.User, model *ent.Task) (taskmodel.Status, *ent.Task, error) {
	if model == nil {
		return "", nil, fmt.Errorf("missing task model")
	}

	taskExec, err := queue.NewTaskFromModel(model)
	if err != nil {
		return "", nil, err
	}

	execCtx := context.WithValue(ctx, dependency.DepCtx{}, dep)
	execCtx = context.WithValue(execCtx, inventory.UserCtx{}, user)
	execCtx = context.WithValue(execCtx, inventory.UserIDCtx{}, user.ID)
	execCtx = context.WithValue(execCtx, logging.CorrelationIDCtx{}, model.CorrelationID)

	status, err := taskExec.Do(execCtx)
	if err != nil {
		return status, nil, err
	}

	switch status {
	case taskmodel.StatusCompleted:
		if err := dep.TaskClient().SetCompleteByID(execCtx, model.ID); err != nil {
			return status, nil, err
		}
	case taskmodel.StatusQueued, taskmodel.StatusProcessing, taskmodel.StatusSuspending:
		if err := persistTaskState(execCtx, dep, taskExec.Model(), status); err != nil {
			return status, nil, err
		}
	default:
		return status, nil, fmt.Errorf("unexpected task status %s", status)
	}

	updatedModel, err := dep.DBClient().Task.Get(execCtx, model.ID)
	if err != nil {
		return status, nil, err
	}
	return status, updatedModel, nil
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

func forcePreferredContentProcessingNode(dep dependency.Dep, model *ent.Task, preferredNodeID int) error {
	state, err := parseFullTextIndexTaskState(model.PrivateState)
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

func parseFullTextIndexTaskState(raw string) (*smokeFullTextIndexTaskState, error) {
	state := &smokeFullTextIndexTaskState{}
	if strings.TrimSpace(raw) == "" {
		return state, nil
	}
	if err := json.Unmarshal([]byte(raw), state); err != nil {
		return nil, err
	}
	return state, nil
}

func mustParseFullTextIndexTaskState(raw string) *smokeFullTextIndexTaskState {
	state, err := parseFullTextIndexTaskState(raw)
	must(err, "parse full_text_index task state")
	return state
}

func parseFullTextCopyTaskState(raw string) (*smokeFullTextCopyTaskState, error) {
	state := &smokeFullTextCopyTaskState{}
	if strings.TrimSpace(raw) == "" {
		return state, nil
	}
	if err := json.Unmarshal([]byte(raw), state); err != nil {
		return nil, err
	}
	return state, nil
}

func mustParseFullTextCopyTaskState(raw string) *smokeFullTextCopyTaskState {
	state, err := parseFullTextCopyTaskState(raw)
	must(err, "parse full_text_copy task state")
	return state
}

func fullTextIndexStateTargetsFile(state *smokeFullTextIndexTaskState, fileID int) bool {
	if state == nil || fileID <= 0 {
		return false
	}
	if state.FileID == fileID {
		return true
	}
	for _, candidate := range state.FileIDs {
		if candidate == fileID {
			return true
		}
	}
	for _, candidate := range state.Files {
		if candidate.FileID == fileID {
			return true
		}
	}
	return state.Active != nil && state.Active.FileID == fileID
}

func taskIDs(tasks []*ent.Task) []int {
	ids := make([]int, 0, len(tasks))
	for _, taskModel := range tasks {
		ids = append(ids, taskModel.ID)
	}
	return ids
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

func assertDocPath(fileID int, wantPath, label string) {
	doc := mustGetDoc(fileID)
	if doc.Source.PathText != wantPath {
		panic(fmt.Sprintf("%s: unexpected path for file %d: got %q want %q", label, fileID, doc.Source.PathText, wantPath))
	}
	fmt.Printf("%s ok file_id=%d path=%s content_len=%d\n", label, fileID, doc.Source.PathText, len(strings.TrimSpace(doc.Source.Content)))
}

func assertDocContentContains(fileID int, want, label string) {
	doc := mustGetDoc(fileID)
	if !strings.Contains(doc.Source.Content, want) {
		panic(fmt.Sprintf("%s: expected content for file %d to contain %q, got %q", label, fileID, want, previewText(doc.Source.Content)))
	}
	fmt.Printf("%s ok file_id=%d content_contains=%q attachments=%d\n", label, fileID, want, len(doc.Source.Attachments))
}

func assertDocHasAttachment(fileID int, wantName, label string) {
	doc := mustGetDoc(fileID)
	for _, attachment := range doc.Source.Attachments {
		if attachment.Name == wantName {
			fmt.Printf("%s ok file_id=%d attachment=%s type=%s depth=%d\n", label, fileID, attachment.Name, attachment.Type, attachment.Depth)
			return
		}
	}

	names := make([]string, 0, len(doc.Source.Attachments))
	for _, attachment := range doc.Source.Attachments {
		names = append(names, attachment.Name)
	}
	panic(fmt.Sprintf("%s: expected attachment %q for file %d, got %v", label, wantName, fileID, names))
}

func assertFileFTSMetadataExists(ctx context.Context, dep dependency.Dep, fileID int, label string) {
	loadCtx := context.WithValue(ctx, inventory.LoadFileMetadata{}, true)
	fileModel, err := dep.FileClient().GetByID(loadCtx, fileID)
	must(err, label+" load file metadata")

	metadata := map[string]string{}
	for _, item := range fileModel.Edges.Metadata {
		metadata[item.Name] = item.Value
	}

	manifestPath := metadata[dbfs.FTSSidecarManifestKey]
	entityID := metadata[dbfs.FTSSidecarEntityIDKey]
	indexKey := metadata[dbfs.FullTextIndexKey]
	if manifestPath == "" || entityID == "" || indexKey == "" {
		panic(fmt.Sprintf("%s: missing fts metadata for file %d manifest=%q entity=%q index=%q", label, fileID, manifestPath, entityID, indexKey))
	}
	if !strings.Contains(manifestPath, fmt.Sprintf("/%d/", fileID)) {
		panic(fmt.Sprintf("%s: manifest path %q does not belong to file %d", label, manifestPath, fileID))
	}

	fmt.Printf("%s ok file_id=%d manifest=%s entity=%s index=%s\n", label, fileID, manifestPath, entityID, indexKey)
}

func previewText(text string) string {
	text = strings.ReplaceAll(text, "\n", "\\n")
	if len(text) > 80 {
		return text[:80]
	}
	return text
}

func ensureSmokeFTSSettings(ctx context.Context, dep dependency.Dep) error {
	settings := map[string]string{
		"siteURL":                         "http://127.0.0.1:5212",
		"fts_enabled":                     "1",
		"fts_index_type":                  "elasticsearch",
		"fts_elasticsearch_endpoint":      esEndpoint,
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
