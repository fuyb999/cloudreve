package mime

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"mime"
	"path"
)

type MimeDetector interface {
	// TypeByName returns the mime type by file name.
	TypeByName(ext string) string
}

type mimeDetector struct {
	mapping map[string]string
}

func NewMimeDetector(ctx context.Context, settings setting.Provider, l logging.Logger) MimeDetector {
	mapping, err := mappingFromSettings(ctx, settings)
	if err != nil && l != nil {
		l.Error("Failed to unmarshal mime mapping: %s, fallback to empty mapping", err)
	}

	return &mimeDetector{
		mapping: mapping,
	}
}

func (d *mimeDetector) TypeByName(p string) string {
	ext := strings.ToLower(path.Ext(strings.TrimSpace(p)))
	if m, ok := d.mapping[ext]; ok {
		return m
	}

	m := mime.TypeByExtension(ext)
	if m != "" {
		return m
	}

	// Fallback
	return "application/octet-stream"
}

func mappingFromSettings(ctx context.Context, settings setting.Provider) (map[string]string, error) {
	if settings == nil {
		return map[string]string{}, nil
	}

	raw, ok := safeMimeMapping(ctx, settings)
	if !ok {
		return map[string]string{}, nil
	}

	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]string{}, nil
	}

	parsed := make(map[string]string)
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return map[string]string{}, err
	}

	normalized := make(map[string]string, len(parsed))
	for ext, contentType := range parsed {
		ext = strings.ToLower(strings.TrimSpace(ext))
		contentType = strings.TrimSpace(contentType)
		if ext == "" || contentType == "" {
			continue
		}

		normalized[ext] = contentType
	}

	return normalized, nil
}

func safeMimeMapping(ctx context.Context, settings setting.Provider) (raw string, ok bool) {
	defer func() {
		if recover() != nil {
			raw = ""
			ok = false
		}
	}()

	return settings.MimeMapping(ctx), true
}
