package admin

import (
	"testing"

	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
)

func TestResolveAdminTaskTypeFilter(t *testing.T) {
	tests := []struct {
		name        string
		taskType    string
		wantTypes   []string
		wantFilters []inventory.TaskTypeFilter
	}{
		{
			name:      "content processing aggregate",
			taskType:  contentProcessingFilterValue,
			wantTypes: []string{queue.FullTextIndexTaskType, queue.FullTextDeleteTaskType, queue.MediaMetaTaskType, queue.DocumentInspectTaskType},
			wantFilters: []inventory.TaskTypeFilter{
				{Type: queue.SlaveContentProcessingTaskType, PrivateStateContains: contentProcessingKindFullTextExtract},
				{Type: queue.SlaveContentProcessingTaskType, PrivateStateContains: contentProcessingKindMediaMetaExtract},
				{Type: queue.SlaveContentProcessingTaskType, PrivateStateContains: contentProcessingKindDocumentInspect},
				{Type: queue.SlaveContentProcessingTaskType, PrivateStateContains: contentProcessingKindThumbnailGenerate},
			},
		},
		{
			name:      "full text index",
			taskType:  queue.FullTextIndexTaskType,
			wantTypes: []string{queue.FullTextIndexTaskType, queue.FullTextDeleteTaskType},
			wantFilters: []inventory.TaskTypeFilter{
				{Type: queue.SlaveContentProcessingTaskType, PrivateStateContains: contentProcessingKindFullTextExtract},
			},
		},
		{
			name:      "media meta",
			taskType:  queue.MediaMetaTaskType,
			wantTypes: []string{queue.MediaMetaTaskType},
			wantFilters: []inventory.TaskTypeFilter{
				{Type: queue.SlaveContentProcessingTaskType, PrivateStateContains: contentProcessingKindMediaMetaExtract},
			},
		},
		{
			name:      "document inspect",
			taskType:  queue.DocumentInspectTaskType,
			wantTypes: []string{queue.DocumentInspectTaskType},
			wantFilters: []inventory.TaskTypeFilter{
				{Type: queue.SlaveContentProcessingTaskType, PrivateStateContains: contentProcessingKindDocumentInspect},
			},
		},
		{
			name:     "thumbnail generate",
			taskType: "thumbnail_generate",
			wantFilters: []inventory.TaskTypeFilter{
				{Type: queue.SlaveContentProcessingTaskType, PrivateStateContains: contentProcessingKindThumbnailGenerate},
			},
		},
		{
			name:      "plain raw type",
			taskType:  queue.RemoteDownloadTaskType,
			wantTypes: []string{queue.RemoteDownloadTaskType},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			gotTypes, gotFilters := resolveAdminTaskTypeFilter(tt.taskType)
			if len(gotTypes) != len(tt.wantTypes) {
				t.Fatalf("unexpected type count: got %d want %d", len(gotTypes), len(tt.wantTypes))
			}
			for i := range gotTypes {
				if gotTypes[i] != tt.wantTypes[i] {
					t.Fatalf("unexpected raw type at %d: got %s want %s", i, gotTypes[i], tt.wantTypes[i])
				}
			}
			if len(gotFilters) != len(tt.wantFilters) {
				t.Fatalf("unexpected filter count: got %d want %d", len(gotFilters), len(tt.wantFilters))
			}
			for i := range gotFilters {
				if gotFilters[i] != tt.wantFilters[i] {
					t.Fatalf("unexpected filter at %d: got %+v want %+v", i, gotFilters[i], tt.wantFilters[i])
				}
			}
		})
	}
}

func TestExpandCleanupTaskTypesAndFilters(t *testing.T) {
	taskTypes := []string{
		contentProcessingFilterValue,
		queue.RemoteDownloadTaskType,
	}

	gotTypes := expandCleanupTaskTypes(taskTypes)
	wantTypes := []string{
		queue.FullTextIndexTaskType,
		queue.FullTextDeleteTaskType,
		queue.MediaMetaTaskType,
		queue.DocumentInspectTaskType,
		queue.RemoteDownloadTaskType,
	}
	if len(gotTypes) != len(wantTypes) {
		t.Fatalf("unexpected expanded type count: got %d want %d", len(gotTypes), len(wantTypes))
	}
	for i := range gotTypes {
		if gotTypes[i] != wantTypes[i] {
			t.Fatalf("unexpected expanded type at %d: got %s want %s", i, gotTypes[i], wantTypes[i])
		}
	}

	gotFilters := expandCleanupTaskTypeFilters(taskTypes)
	wantFilters := []inventory.TaskTypeFilter{
		{Type: queue.SlaveContentProcessingTaskType, PrivateStateContains: contentProcessingKindFullTextExtract},
		{Type: queue.SlaveContentProcessingTaskType, PrivateStateContains: contentProcessingKindMediaMetaExtract},
		{Type: queue.SlaveContentProcessingTaskType, PrivateStateContains: contentProcessingKindDocumentInspect},
		{Type: queue.SlaveContentProcessingTaskType, PrivateStateContains: contentProcessingKindThumbnailGenerate},
	}
	if len(gotFilters) != len(wantFilters) {
		t.Fatalf("unexpected expanded filter count: got %d want %d", len(gotFilters), len(wantFilters))
	}
	for i := range gotFilters {
		if gotFilters[i] != wantFilters[i] {
			t.Fatalf("unexpected expanded filter at %d: got %+v want %+v", i, gotFilters[i], wantFilters[i])
		}
	}
}
