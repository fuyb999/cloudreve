package slave

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/request"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
)

type testRequestClient struct {
	response *request.Response
}

func (c testRequestClient) Apply(opts ...request.Option) {}

func (c testRequestClient) Request(method, target string, body io.Reader, opts ...request.Option) *request.Response {
	return c.response
}

func newResponse(t *testing.T, payload serializer.Response) *request.Response {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal response: %v", err)
	}

	return &request.Response{
		Response: &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(body)),
		},
	}
}

func TestSlaveDownloaderTestReturnsMessage(t *testing.T) {
	downloader := &slaveDownloader{
		client:          testRequestClient{response: newResponse(t, serializer.Response{Code: 0, Data: "ok"})},
		nodeSetting:     &types.NodeSetting{},
		nodeSettingHash: "test",
	}

	message, err := downloader.Test(context.Background())
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if message != "ok" {
		t.Fatalf("unexpected message: %q", message)
	}
}

func TestSlaveDownloaderTestRejectsUnexpectedResponseData(t *testing.T) {
	downloader := &slaveDownloader{
		client: testRequestClient{response: newResponse(t, serializer.Response{
			Code: 0,
			Data: map[string]string{"message": "ok"},
		})},
		nodeSetting:     &types.NodeSetting{},
		nodeSettingHash: "test",
	}

	_, err := downloader.Test(context.Background())
	if err == nil {
		t.Fatal("expected unexpected response data error")
	}
	if !strings.Contains(err.Error(), "unexpected response data") {
		t.Fatalf("unexpected error: %v", err)
	}
}
