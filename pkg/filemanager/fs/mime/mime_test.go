package mime

import (
	"context"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

type testSettingProvider struct {
	setting.Provider
	mimeMapping string
	panicOnRead bool
}

func (s testSettingProvider) MimeMapping(ctx context.Context) string {
	if s.panicOnRead {
		panic("mime mapping unavailable")
	}

	return s.mimeMapping
}

func TestNewMimeDetectorUsesConfiguredMapping(t *testing.T) {
	detector := NewMimeDetector(context.Background(), testSettingProvider{
		mimeMapping: `{".custom":"text/plain",".js":"application/javascript"}`,
	}, logging.NewConsoleLogger(logging.LevelError))

	cases := map[string]string{
		"demo.custom": "text/plain",
		"APP.JS":      "application/javascript",
	}

	for fileName, want := range cases {
		if got := detector.TypeByName(fileName); got != want {
			t.Fatalf("%s: unexpected content type: got %q want %q", fileName, got, want)
		}
	}
}

func TestNewMimeDetectorFallsBackWithoutSettings(t *testing.T) {
	detector := NewMimeDetector(context.Background(), nil, logging.NewConsoleLogger(logging.LevelError))
	if got := detector.TypeByName("report.pdf"); got != "application/pdf" {
		t.Fatalf("unexpected stdlib fallback content type: %q", got)
	}
}

func TestNewMimeDetectorRecoversFromIncompleteProvider(t *testing.T) {
	detector := NewMimeDetector(context.Background(), testSettingProvider{panicOnRead: true}, logging.NewConsoleLogger(logging.LevelError))
	if got := detector.TypeByName("archive.unknown"); got != "application/octet-stream" {
		t.Fatalf("unexpected fallback content type: %q", got)
	}
}
