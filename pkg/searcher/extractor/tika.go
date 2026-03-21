package extractor

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/request"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

// TikaExtractor extracts text from documents using Apache Tika.
type TikaExtractor struct {
	client      request.Client
	settings    setting.Provider
	l           logging.Logger
	exts        []string
	maxFileSize int64
	endpoint    string
}

type ArtifactOptions struct {
	ExtractInlineImages bool
}

// NewTikaExtractor creates a new TikaExtractor.
func NewTikaExtractor(client request.Client, settings setting.Provider, l logging.Logger, cfg *setting.FTSTikaExtractorSetting) *TikaExtractor {
	exts := cfg.Exts
	return &TikaExtractor{
		client:      client,
		settings:    settings,
		l:           l,
		exts:        exts,
		maxFileSize: cfg.MaxFileSize,
		endpoint:    cfg.Endpoint,
	}
}

// Exts returns the list of supported file extensions.
func (t *TikaExtractor) Exts() []string {
	return t.exts
}

// MaxFileSize returns the maximum file size for text extraction.
func (t *TikaExtractor) MaxFileSize() int64 {
	return t.maxFileSize
}

// Extract sends the document to Tika and returns the extracted plain text.
func (t *TikaExtractor) Extract(ctx context.Context, reader io.Reader) (string, error) {
	return t.ExtractFile(ctx, reader, "")
}

func (t *TikaExtractor) ExtractFile(ctx context.Context, reader io.Reader, fileName string) (string, error) {
	body, err := t.call(ctx, "/tika", reader, http.Header{
		"Accept": {"text/plain"},
	}, fileName)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(body)), nil
}

func (t *TikaExtractor) RMeta(ctx context.Context, reader io.Reader, opts ArtifactOptions) ([]byte, error) {
	return t.RMetaFile(ctx, reader, "", opts)
}

func (t *TikaExtractor) RMetaFile(ctx context.Context, reader io.Reader, fileName string, opts ArtifactOptions) ([]byte, error) {
	return t.call(ctx, "/rmeta", reader, artifactHeaders(opts, http.Header{
		"Accept": {"application/json"},
	}), fileName)
}

func (t *TikaExtractor) Unpack(ctx context.Context, reader io.Reader, opts ArtifactOptions) ([]byte, error) {
	return t.UnpackFile(ctx, reader, "", opts)
}

func (t *TikaExtractor) UnpackFile(ctx context.Context, reader io.Reader, fileName string, opts ArtifactOptions) ([]byte, error) {
	return t.call(ctx, "/unpack", reader, artifactHeaders(opts, http.Header{
		"Accept": {"application/zip"},
	}), fileName)
}

func (t *TikaExtractor) UnpackAll(ctx context.Context, reader io.Reader, opts ArtifactOptions) ([]byte, error) {
	return t.UnpackAllFile(ctx, reader, "", opts)
}

func (t *TikaExtractor) UnpackAllFile(ctx context.Context, reader io.Reader, fileName string, opts ArtifactOptions) ([]byte, error) {
	return t.call(ctx, "/unpack/all", reader, artifactHeaders(opts, http.Header{
		"Accept": {"application/zip"},
	}), fileName)
}

func (t *TikaExtractor) call(ctx context.Context, path string, reader io.Reader, headers http.Header, fileName string) ([]byte, error) {
	if t.endpoint == "" {
		return nil, fmt.Errorf("tika endpoint not configured")
	}

	endpoint := strings.TrimRight(t.endpoint, "/") + path
	headers = fileAwareHeaders(headers, fileName)
	resp := t.client.Request(
		"PUT",
		endpoint,
		reader,
		request.WithContext(ctx),
		request.WithHeader(headers),
	)
	if resp.Err != nil {
		return nil, fmt.Errorf("tika request failed: %w", resp.Err)
	}
	defer resp.Response.Body.Close()

	if resp.Response.StatusCode != 200 {
		return nil, fmt.Errorf("tika returned status %d", resp.Response.StatusCode)
	}

	body, err := io.ReadAll(resp.Response.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read tika response: %w", err)
	}

	return body, nil
}

func artifactHeaders(opts ArtifactOptions, headers http.Header) http.Header {
	if headers == nil {
		headers = http.Header{}
	}

	if opts.ExtractInlineImages {
		headers.Set("X-Tika-PDFextractInlineImages", "true")
	}

	return headers
}

func fileAwareHeaders(headers http.Header, fileName string) http.Header {
	if headers == nil {
		headers = http.Header{}
	}

	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		return headers
	}

	fileName = strings.TrimSpace(filepath.Base(fileName))
	if fileName == "" {
		return headers
	}

	headers.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	if contentType := tikaContentTypeByName(fileName); contentType != "" {
		headers.Set("Content-Type", contentType)
	}

	return headers
}

func tikaContentTypeByName(fileName string) string {
	lowerName := strings.ToLower(strings.TrimSpace(fileName))
	if strings.EqualFold(filepath.Base(lowerName), "winmail.dat") {
		return "application/vnd.ms-tnef"
	}

	for _, item := range []struct {
		suffix string
		mime   string
	}{
		{".tar.gz", "application/gzip"},
		{".tar.bz2", "application/x-bzip2"},
		{".tar.xz", "application/x-xz"},
		{".tar.lzma", "application/x-lzma"},
		{".tgz", "application/gzip"},
		{".tbz2", "application/x-bzip2"},
		{".tbz", "application/x-bzip2"},
		{".txz", "application/x-xz"},
		{".tlz", "application/x-lzma"},
		{".zip", "application/zip"},
		{".jar", "application/java-archive"},
		{".war", "application/java-archive"},
		{".ear", "application/java-archive"},
		{".7z", "application/x-7z-compressed"},
		{".rar", "application/x-rar-compressed"},
		{".tar", "application/x-tar"},
		{".ar", "application/x-archive"},
		{".gz", "application/gzip"},
		{".z", "application/x-compress"},
		{".bz2", "application/x-bzip2"},
		{".bz", "application/x-bzip"},
		{".xz", "application/x-xz"},
		{".lzma", "application/x-lzma"},
		{".lz4", "application/x-lz4"},
		{".br", "application/x-brotli"},
		{".snappy", "application/x-snappy"},
		{".sz", "application/x-snappy"},
		{".pack200", "application/x-java-pack200"},
		{".pack", "application/x-java-pack200"},
		{".cpio", "application/x-cpio"},
		{".arj", "application/x-arj"},
		{".dump", "application/x-tika-unix-dump"},
	} {
		if strings.HasSuffix(lowerName, item.suffix) {
			return item.mime
		}
	}

	ext := strings.ToLower(filepath.Ext(lowerName))
	if ext == "" {
		return ""
	}

	switch ext {
	case ".md":
		return "text/markdown"
	case ".csv":
		return "text/csv"
	case ".eml", ".mht", ".mhtml", ".nws":
		return "message/rfc822"
	case ".mbox":
		return "application/mbox"
	case ".msg":
		return "application/vnd.ms-outlook"
	case ".tnef":
		return "application/vnd.ms-tnef"
	case ".doc":
		return "application/msword"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xls":
		return "application/vnd.ms-excel"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".ppt":
		return "application/vnd.ms-powerpoint"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ".rtf":
		return "application/rtf"
	case ".odt":
		return "application/vnd.oasis.opendocument.text"
	case ".ods":
		return "application/vnd.oasis.opendocument.spreadsheet"
	case ".odp":
		return "application/vnd.oasis.opendocument.presentation"
	default:
		return normalizeTextLikeContentType(mime.TypeByExtension(ext))
	}
}

func normalizeTextLikeContentType(contentType string) string {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return ""
	}

	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return contentType
	}

	if charset, ok := params["charset"]; ok && strings.EqualFold(charset, "utf-8") {
		delete(params, "charset")
	}

	normalized := mime.FormatMediaType(mediaType, params)
	if normalized == "" {
		return mediaType
	}

	return normalized
}
