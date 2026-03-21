package queue

import "testing"

func TestDisplayType(t *testing.T) {
	tests := []struct {
		name         string
		taskType     string
		privateState string
		want         string
	}{
		{
			name:     "full text delete maps to full text index",
			taskType: FullTextDeleteTaskType,
			want:     FullTextIndexTaskType,
		},
		{
			name:         "slave full text extract maps to full text index",
			taskType:     SlaveContentProcessingTaskType,
			privateState: `{"kind":"full_text_extract"}`,
			want:         FullTextIndexTaskType,
		},
		{
			name:         "slave media meta maps to media meta",
			taskType:     SlaveContentProcessingTaskType,
			privateState: `{"kind":"media_meta_extract"}`,
			want:         MediaMetaTaskType,
		},
		{
			name:         "slave thumbnail maps to thumbnail generate",
			taskType:     SlaveContentProcessingTaskType,
			privateState: `{"kind":"thumbnail_generate"}`,
			want:         ContentProcessingTaskKindThumbnailGenerate,
		},
		{
			name:         "slave document inspect maps to document inspect",
			taskType:     SlaveContentProcessingTaskType,
			privateState: `{"kind":"document_inspect"}`,
			want:         DocumentInspectTaskType,
		},
		{
			name:         "invalid slave state falls back to raw type",
			taskType:     SlaveContentProcessingTaskType,
			privateState: `{"kind":`,
			want:         SlaveContentProcessingTaskType,
		},
		{
			name:     "plain task type stays unchanged",
			taskType: RemoteDownloadTaskType,
			want:     RemoteDownloadTaskType,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := DisplayType(tt.taskType, tt.privateState); got != tt.want {
				t.Fatalf("unexpected display type: got %s want %s", got, tt.want)
			}
		})
	}
}
