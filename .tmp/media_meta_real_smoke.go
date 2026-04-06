package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	entfile "github.com/cloudreve/Cloudreve/v4/ent/file"
	taskmodel "github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/auth"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
)

const (
	mediaMetaSmokeUserID = 1
	mediaMetaSiteURL     = "http://127.0.0.1:5212"
)

type apiResponse struct {
	Code  int             `json:"code"`
	Msg   string          `json:"msg"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

type uploadSessionResponse struct {
	SessionID   string   `json:"session_id"`
	ChunkSize   int64    `json:"chunk_size"`
	UploadURLs  []string `json:"upload_urls,omitempty"`
	CompleteURL string   `json:"completeURL,omitempty"`
	URI         string   `json:"uri,omitempty"`
}

type mediaMetaTaskState struct {
	FileID  int    `json:"file_id,omitempty"`
	OwnerID int    `json:"owner_id,omitempty"`
	EntityID int   `json:"entity_id,omitempty"`
	Phase   string `json:"phase,omitempty"`
	NodeID  int    `json:"node_id,omitempty"`
	SlaveID int    `json:"slave_id,omitempty"`
}

func main() {
	util.UseWorkingDir = true
	dep := dependency.NewDependency(
		dependency.WithConfigPath(".tmp/fts_real_smoke_global_kafka.ini"),
		dependency.WithLogger(logging.NewConsoleLogger(logging.LevelDebug)),
		dependency.WithRequiredDbVersion(constants.BackendVersion),
	)
	defer dep.Shutdown(context.Background())

	ctx := context.WithValue(context.Background(), dependency.DepCtx{}, dep)
	ctx = context.WithValue(ctx, inventory.LoadTaskUser{}, true)

	user, err := dep.UserClient().GetLoginUserByID(ctx, mediaMetaSmokeUserID)
	must(err, "load login user")
	ctx = context.WithValue(ctx, inventory.UserCtx{}, user)
	ctx = context.WithValue(ctx, inventory.UserIDCtx{}, user.ID)

	token, err := issueAccessToken(ctx, dep, user)
	must(err, "issue access token")

	name := fmt.Sprintf("__media_meta_smoke_%d.png", time.Now().UnixNano())
	targetURI := "cloudreve://my/" + name
	payload := tinyPNG()

	must(uploadViaMasterHTTP(token, targetURI, payload), "upload png via master")
	fileID, err := waitUploadedFileID(ctx, dep, user.ID, name, int64(len(payload)), 25*time.Second)
	must(err, "resolve uploaded png")

	taskID, sawAwait, err := waitMediaMetaTask(ctx, dep, fileID, 45*time.Second)
	must(err, "wait media meta task")
	if !sawAwait {
		panic(fmt.Sprintf("media_meta task for file %d never entered await_slave_extract", fileID))
	}

	fmt.Printf("media_meta smoke ok file_id=%d task_id=%d name=%s saw_await_slave=%t\n", fileID, taskID, name, sawAwait)
}

func issueAccessToken(ctx context.Context, dep dependency.Dep, user *ent.User) (string, error) {
	token, err := dep.TokenAuth().Issue(ctx, &auth.IssueTokenArgs{User: user})
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

	createReq, err := http.NewRequest(http.MethodPut, mediaMetaSiteURL+constants.APIPrefix+"/file/upload", bytes.NewReader(raw))
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
	if len(session.UploadURLs) > 0 || session.CompleteURL != "" {
		return fmt.Errorf("unexpected direct-upload credential for uri=%s, relay upload is required", uri)
	}

	uploadReq, err := http.NewRequest(
		http.MethodPost,
		fmt.Sprintf("%s%s/file/upload/%s/0", mediaMetaSiteURL, constants.APIPrefix, session.SessionID),
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

func waitMediaMetaTask(ctx context.Context, dep dependency.Dep, fileID int, timeout time.Duration) (int, bool, error) {
	deadline := time.Now().Add(timeout)
	sawAwait := false
	taskID := 0
	for time.Now().Before(deadline) {
		tasks, err := dep.DBClient().Task.Query().
			Where(
				taskmodel.PrivateStateContains(fmt.Sprintf("\"file_id\":%d", fileID)),
				taskmodel.TypeEQ(queue.MediaMetaTaskType),
			).
			Order(ent.Asc(taskmodel.FieldID)).
			All(ctx)
		if err != nil {
			return 0, sawAwait, err
		}

		for _, model := range tasks {
			taskID = model.ID
			state := &mediaMetaTaskState{}
			if err := json.Unmarshal([]byte(model.PrivateState), state); err == nil {
				if state.Phase == "await_slave_extract" && state.SlaveID > 0 {
					sawAwait = true
					fmt.Printf("media_meta await_slave task_id=%d node_id=%d slave_task_id=%d\n", model.ID, state.NodeID, state.SlaveID)
				}
			}

			switch model.Status {
			case taskmodel.StatusCompleted:
				return model.ID, sawAwait, nil
			case taskmodel.StatusError:
				return model.ID, sawAwait, fmt.Errorf("media_meta task %d failed: %s", model.ID, model.PublicState.Error)
			}
		}

		time.Sleep(500 * time.Millisecond)
	}

	return taskID, sawAwait, fmt.Errorf("media_meta task for file %d did not complete in time", fileID)
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

func must(err error, message string) {
	if err != nil {
		panic(fmt.Sprintf("%s: %v", message, err))
	}
}
