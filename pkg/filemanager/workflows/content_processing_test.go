package workflows

import (
	"encoding/json"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/driver"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
)

func TestSlaveContentProcessingTaskSummarize(t *testing.T) {
	testCases := []struct {
		name     string
		state    *SlaveContentProcessingTaskState
		expected map[string]any
	}{
		{
			name: "full text extract",
			state: mustSlaveContentProcessingState(t,
				ContentProcessingTaskKindFullTextExtract,
				&manager.SlaveFullTextExtractPayload{
					FileID:   801,
					OwnerID:  701,
					FileName: "report.pdf",
					FileSize: 4096,
					Entity:   &ent.Entity{ID: 901},
					Policy:   &ent.StoragePolicy{ID: 31},
				},
				&manager.SlaveFullTextExtractResult{
					EntityID:     901,
					ManifestPath: "fts/901/manifest.json",
				},
			),
			expected: map[string]any{
				"kind":          "full_text_extract",
				"file_id":       801,
				"owner_id":      701,
				"file_name":     "report.pdf",
				"file_size":     int64(4096),
				"entity_id":     901,
				"policy_id":     31,
				"manifest_path": "fts/901/manifest.json",
			},
		},
		{
			name: "media meta extract",
			state: mustSlaveContentProcessingState(t,
				ContentProcessingTaskKindMediaMetaExtract,
				&manager.SlaveMediaMetaExtractPayload{
					FileName: "clip.mp4",
					FileExt:  "mp4",
					Language: "zh-CN",
					Entity:   &ent.Entity{ID: 902},
					Policy:   &ent.StoragePolicy{ID: 32},
				},
				&manager.SlaveMediaMetaExtractResult{
					EntityID: 902,
					Metas: []driver.MediaMeta{
						{Type: "video", Key: "duration", Value: "10"},
						{Type: "video", Key: "width", Value: "1920"},
					},
				},
			),
			expected: map[string]any{
				"kind":       "media_meta_extract",
				"file_name":  "clip.mp4",
				"file_ext":   "mp4",
				"language":   "zh-CN",
				"entity_id":  902,
				"policy_id":  32,
				"meta_count": 2,
			},
		},
		{
			name: "thumbnail generate",
			state: mustSlaveContentProcessingState(t,
				ContentProcessingTaskKindThumbnailGenerate,
				&manager.SlaveThumbnailGeneratePayload{
					FileID:  803,
					OwnerID: 703,
					Ext:     "png",
					URI:     "cloudreve:///thumb/source.png",
					Entity:  &ent.Entity{ID: 903},
					Policy:  &ent.StoragePolicy{ID: 33},
				},
				&manager.SlaveThumbnailGenerateResult{
					FileID:       803,
					EntityID:     903,
					SavePath:     "thumb/903.png",
					Size:         8192,
					NotAvailable: false,
				},
			),
			expected: map[string]any{
				"kind":          "thumbnail_generate",
				"file_id":       803,
				"owner_id":      703,
				"ext":           "png",
				"src":           "cloudreve:///thumb/source.png",
				"entity_id":     903,
				"policy_id":     33,
				"save_path":     "thumb/903.png",
				"size":          int64(8192),
				"not_available": false,
			},
		},
		{
			name: "document inspect",
			state: mustSlaveContentProcessingState(t,
				ContentProcessingTaskKindDocumentInspect,
				&manager.SlaveDocumentInspectPayload{
					FileName: "inspect.docx",
					FileSize: 16384,
					Entity:   &ent.Entity{ID: 904},
					Policy:   &ent.StoragePolicy{ID: 34},
				},
				&manager.DocumentInspection{
					EntityID: 904,
					MimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
					Parser:   "org.apache.tika.parser.microsoft.ooxml.OOXMLParser",
					Language: "zh",
					Title:    "inspect",
					Author:   "tester",
					Metadata: map[string]string{"meta:author": "tester", "dc:title": "inspect"},
				},
			),
			expected: map[string]any{
				"kind":           "document_inspect",
				"file_name":      "inspect.docx",
				"file_size":      int64(16384),
				"entity_id":      904,
				"policy_id":      34,
				"mime_type":      "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
				"parser":         "org.apache.tika.parser.microsoft.ooxml.OOXMLParser",
				"language":       "zh",
				"title":          "inspect",
				"author":         "tester",
				"metadata_count": 2,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			stateRaw, err := json.Marshal(tc.state)
			if err != nil {
				t.Fatalf("failed to marshal state: %v", err)
			}

			task := &SlaveContentProcessingTask{
				InMemoryTask: &queue.InMemoryTask{
					DBTask: &queue.DBTask{
						Task: &ent.Task{
							Type:         queue.SlaveContentProcessingTaskType,
							PrivateState: string(stateRaw),
						},
					},
				},
			}

			summary := task.Summarize(nil)
			if summary == nil {
				t.Fatal("expected summary")
			}

			for key, expected := range tc.expected {
				if got := summary.Props[key]; got != expected {
					t.Fatalf("unexpected summary prop %q: got %#v want %#v", key, got, expected)
				}
			}
		})
	}
}

func mustSlaveContentProcessingState(t *testing.T, kind ContentProcessingTaskKind, payload any, result any) *SlaveContentProcessingTaskState {
	t.Helper()

	var (
		payloadRaw []byte
		resultRaw  []byte
		err        error
	)
	if payload != nil {
		payloadRaw, err = json.Marshal(payload)
		if err != nil {
			t.Fatalf("failed to marshal payload: %v", err)
		}
	}
	if result != nil {
		resultRaw, err = json.Marshal(result)
		if err != nil {
			t.Fatalf("failed to marshal result: %v", err)
		}
	}

	return &SlaveContentProcessingTaskState{
		Kind:    kind,
		Payload: payloadRaw,
		Result:  resultRaw,
	}
}
