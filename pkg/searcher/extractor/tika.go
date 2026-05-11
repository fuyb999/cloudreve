package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

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
	mimeMapping map[string]string
	maxFileSize int64
	endpoint    string
}

type ArtifactOptions struct {
	ExtractInlineImages bool
}

const tikaSniffPreviewBytes = 4096

var explicitTikaBaseNameContentTypes = map[string]string{
	"dockerfile":      "text/plain",
	"containerfile":   "text/plain",
	"makefile":        "text/plain",
	"jenkinsfile":     "text/plain",
	"procfile":        "text/plain",
	"caddyfile":       "text/plain",
	"justfile":        "text/plain",
	"tiltfile":        "text/plain",
	"taskfile":        "text/plain",
	"podfile":         "text/plain",
	"fastfile":        "text/plain",
	"appfile":         "text/plain",
	"pipfile":         "text/plain",
	"buckfile":        "text/plain",
	"workspace":       "text/plain",
	"build":           "text/plain",
	"pkgbuild":        "text/plain",
	"apkbuild":        "text/plain",
	"readme":          "text/plain",
	"license":         "text/plain",
	"copying":         "text/plain",
	"changelog":       "text/plain",
	"authors":         "text/plain",
	"contributors":    "text/plain",
	"gradlew":         "application/x-sh",
	"mvnw":            "application/x-sh",
	"configure":       "application/x-sh",
	"gemfile":         "text/plain",
	"rakefile":        "text/plain",
	"brewfile":        "text/plain",
	"vagrantfile":     "text/plain",
	".env":            "text/plain",
	".env.example":    "text/plain",
	".gitignore":      "text/plain",
	".gitattributes":  "text/plain",
	".dockerignore":   "text/plain",
	".editorconfig":   "text/plain",
	".npmrc":          "text/plain",
	".yarnrc":         "text/plain",
	".bazelrc":        "text/plain",
	".bazelversion":   "text/plain",
	".tool-versions":  "text/plain",
	".envrc":          "application/x-sh",
	".prettierrc":     "text/plain",
	".eslintrc":       "text/plain",
	".stylelintrc":    "text/plain",
	".babelrc":        "text/plain",
	".yamllint":       "text/plain",
	".flake8":         "text/plain",
	".gitmodules":     "text/plain",
	".mailmap":        "text/plain",
	".npmignore":      "text/plain",
	".prettierignore": "text/plain",
	".eslintignore":   "text/plain",
	".pypirc":         "text/plain",
	".terraformrc":    "text/plain",
	".vimrc":          "text/plain",
	".tmux.conf":      "text/plain",
	".bashrc":         "application/x-sh",
	".bash_aliases":   "application/x-sh",
	".bash_logout":    "application/x-sh",
	".zshrc":          "application/x-sh",
	".zshenv":         "application/x-sh",
	".zlogin":         "application/x-sh",
	".profile":        "application/x-sh",
	".bash_profile":   "application/x-sh",
	".zprofile":       "application/x-sh",
}

var explicitTikaContentTypes = map[string]string{
	".js":         "application/javascript",
	".mjs":        "application/javascript",
	".cjs":        "application/javascript",
	".bat":        "text/plain",
	".cmd":        "text/plain",
	".css":        "text/css",
	".scss":       "text/plain",
	".less":       "text/plain",
	".ts":         "text/plain",
	".tsx":        "text/plain",
	".jsx":        "text/plain",
	".ps1":        "text/plain",
	".psm1":       "text/plain",
	".psd1":       "text/plain",
	".json":       "application/json",
	".json5":      "text/plain",
	".yaml":       "text/plain",
	".yml":        "text/plain",
	".xml":        "application/xml",
	".toml":       "text/plain",
	".ini":        "text/plain",
	".cfg":        "text/plain",
	".cnf":        "text/plain",
	".conf":       "text/plain",
	".properties": "text/plain",
	".sh":         "application/x-sh",
	".bash":       "application/x-sh",
	".zsh":        "application/x-sh",
	".fish":       "text/plain",
	".ksh":        "application/x-sh",
	".py":         "text/plain",
	".rbw":        "text/plain",
	".pl":         "text/plain",
	".pm":         "text/plain",
	".lua":        "text/plain",
	".go":         "text/plain",
	".mod":        "text/plain",
	".sum":        "text/plain",
	".java":       "text/plain",
	".kt":         "text/plain",
	".kts":        "text/plain",
	".groovy":     "text/plain",
	".gradle":     "text/plain",
	".scala":      "text/plain",
	".swift":      "text/plain",
	".dart":       "text/plain",
	".r":          "text/plain",
	".sql":        "application/x-sql",
	".hql":        "text/plain",
	".graphql":    "text/plain",
	".gql":        "text/plain",
	".php":        "text/plain",
	".rb":         "text/plain",
	".rs":         "text/plain",
	".c":          "text/plain",
	".h":          "text/plain",
	".cpp":        "text/plain",
	".hpp":        "text/plain",
	".cc":         "text/plain",
	".hh":         "text/plain",
	".proto":      "text/plain",
	".hcl":        "text/plain",
	".tf":         "text/plain",
	".tfvars":     "text/plain",
	".rego":       "text/plain",
	".service":    "text/plain",
	".socket":     "text/plain",
	".mount":      "text/plain",
	".target":     "text/plain",
	".timer":      "text/plain",
	".path":       "text/plain",
	".unit":       "text/plain",
	".repo":       "text/plain",
	".list":       "text/plain",
	".desktop":    "text/plain",
	".lock":       "text/plain",
	".vue":        "text/plain",
	".svelte":     "text/plain",
	".txt":        "text/plain",
	".log":        "text/plain",
}

// NewTikaExtractor creates a new TikaExtractor.
func NewTikaExtractor(client request.Client, settings setting.Provider, l logging.Logger, cfg *setting.FTSTikaExtractorSetting) *TikaExtractor {
	var exts []string
	var maxFileSize int64
	var endpoint string
	if cfg != nil {
		exts = cfg.Exts
		maxFileSize = cfg.MaxFileSize
		endpoint = cfg.Endpoint
	}

	return &TikaExtractor{
		client:      client,
		settings:    settings,
		l:           l,
		exts:        exts,
		mimeMapping: tikaMimeMappingFromSettings(settings, l),
		maxFileSize: maxFileSize,
		endpoint:    endpoint,
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

func (t *TikaExtractor) DetectFileContentType(fileName string) string {
	return tikaContentTypeByNameWithMimeMapping(fileName, t.mimeMapping)
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
	preparedReader, preparedHeaders, err := prepareTikaRequestWithMimeMapping(reader, headers, fileName, t.mimeMapping)
	if err != nil {
		return nil, err
	}
	resp := t.client.Request(
		"PUT",
		endpoint,
		preparedReader,
		request.WithContext(ctx),
		request.WithHeader(preparedHeaders),
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

func tikaMimeMappingFromSettings(settings setting.Provider, l logging.Logger) map[string]string {
	if settings == nil {
		return nil
	}

	raw, ok := safeMimeMapping(settings, l)
	if !ok {
		return nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	mapping := make(map[string]string)
	if err := json.Unmarshal([]byte(raw), &mapping); err != nil {
		if l != nil {
			l.Error("Failed to unmarshal tika mime mapping: %s, fallback to empty mapping", err)
		}
		return nil
	}

	if len(mapping) == 0 {
		return nil
	}

	normalized := make(map[string]string, len(mapping))
	for ext, contentType := range mapping {
		ext = strings.ToLower(strings.TrimSpace(ext))
		contentType = normalizeTextLikeContentType(strings.TrimSpace(contentType))
		if ext == "" || contentType == "" {
			continue
		}

		normalized[ext] = contentType
	}

	if len(normalized) == 0 {
		return nil
	}

	return normalized
}

func safeMimeMapping(settings setting.Provider, l logging.Logger) (raw string, ok bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if l != nil {
				l.Warning("Failed to load tika mime mapping from settings: %v, fallback to empty mapping", recovered)
			}
			raw = ""
			ok = false
		}
	}()

	return settings.MimeMapping(context.Background()), true
}

func prepareTikaRequest(reader io.Reader, headers http.Header, fileName string) (io.Reader, http.Header, error) {
	return prepareTikaRequestWithMimeMapping(reader, headers, fileName, nil)
}

func prepareTikaRequestWithMimeMapping(reader io.Reader, headers http.Header, fileName string, mimeMapping map[string]string) (io.Reader, http.Header, error) {
	if headers == nil {
		headers = http.Header{}
	}

	sniffedReader, sniffedContentType, err := sniffTikaRequestBody(reader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to prepare tika request body: %w", err)
	}

	headers = fileAwareHeadersWithMimeMapping(headers, fileName, mimeMapping)
	if mergedContentType := mergeTikaContentType(headers.Get("Content-Type"), sniffedContentType); mergedContentType != "" {
		headers.Set("Content-Type", mergedContentType)
	}

	return sniffedReader, headers, nil
}

func mergeTikaContentType(existing, sniffed string) string {
	existing = strings.TrimSpace(existing)
	sniffed = strings.TrimSpace(sniffed)
	switch {
	case existing == "":
		return sniffed
	case sniffed == "":
		return existing
	}

	existingMediaType, existingParams, err := mime.ParseMediaType(existing)
	if err != nil {
		return existing
	}

	sniffedMediaType, sniffedParams, err := mime.ParseMediaType(sniffed)
	if err != nil {
		return existing
	}

	if charset := strings.TrimSpace(sniffedParams["charset"]); charset != "" &&
		existingParams["charset"] == "" &&
		isCharsetCompatibleTikaContentType(existingMediaType) {
		existingParams["charset"] = charset
		if merged := mime.FormatMediaType(existingMediaType, existingParams); merged != "" {
			return merged
		}

		return existingMediaType
	}

	if existingMediaType == "text/plain" && isStructuredTextTikaContentType(sniffedMediaType) {
		return sniffed
	}

	return existing
}

func sniffTikaRequestBody(reader io.Reader) (io.Reader, string, error) {
	if reader == nil {
		return nil, "", nil
	}

	preview, err := io.ReadAll(io.LimitReader(reader, tikaSniffPreviewBytes))
	if err != nil {
		return nil, "", fmt.Errorf("failed to read preview bytes: %w", err)
	}

	return io.MultiReader(bytes.NewReader(preview), reader), tikaContentTypeByContent(preview), nil
}

func fileAwareHeaders(headers http.Header, fileName string) http.Header {
	return fileAwareHeadersWithMimeMapping(headers, fileName, nil)
}

func fileAwareHeadersWithMimeMapping(headers http.Header, fileName string, mimeMapping map[string]string) http.Header {
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
	if contentType := tikaContentTypeByNameWithMimeMapping(fileName, mimeMapping); contentType != "" {
		headers.Set("Content-Type", contentType)
	}

	return headers
}

func tikaContentTypeByContent(preview []byte) string {
	preview = trimUTF8BOM(bytes.TrimSpace(preview))
	if len(preview) == 0 {
		return ""
	}

	if contentType := tikaContentTypeByTextPreview(preview); contentType != "" {
		return contentType
	}

	if !utf8.Valid(preview) && isLikelyGB18030Text(preview) {
		return "text/plain; charset=gb18030"
	}

	detected := normalizeTextLikeContentType(http.DetectContentType(preview))
	switch detected {
	case "text/plain", "text/html", "text/xml", "application/xml", "application/json":
		return detected
	}

	if isLikelyUTF8Text(preview) {
		return "text/plain"
	}

	return ""
}

func tikaContentTypeByTextPreview(preview []byte) string {
	trimmed := bytes.TrimSpace(preview)
	if len(trimmed) == 0 {
		return ""
	}

	lower := bytes.ToLower(trimmed)
	firstLine := strings.ToLower(string(firstPreviewLine(lower)))
	if bytes.HasPrefix(trimmed, []byte("#!")) {
		switch {
		case strings.Contains(firstLine, "sh"), strings.Contains(firstLine, "bash"), strings.Contains(firstLine, "zsh"):
			return "application/x-sh"
		case strings.Contains(firstLine, "node"), strings.Contains(firstLine, "deno"), strings.Contains(firstLine, "bun"):
			return "application/javascript"
		}
	}

	if (trimmed[0] == '{' || trimmed[0] == '[') && json.Valid(trimmed) {
		return "application/json"
	}

	switch {
	case bytes.HasPrefix(lower, []byte("<!doctype html")), bytes.HasPrefix(lower, []byte("<html")):
		return "text/html"
	case bytes.HasPrefix(lower, []byte("<?xml")), bytes.HasPrefix(lower, []byte("<rss")), bytes.HasPrefix(lower, []byte("<feed")), bytes.HasPrefix(lower, []byte("<svg")):
		return "application/xml"
	}

	return ""
}

func firstPreviewLine(preview []byte) []byte {
	if idx := bytes.IndexByte(preview, '\n'); idx >= 0 {
		return preview[:idx]
	}

	return preview
}

func trimUTF8BOM(preview []byte) []byte {
	return bytes.TrimPrefix(preview, []byte{0xEF, 0xBB, 0xBF})
}

func isLikelyUTF8Text(preview []byte) bool {
	if len(preview) == 0 || !utf8.Valid(preview) {
		return false
	}

	controlBytes := 0
	for _, b := range preview {
		if b == 0 {
			return false
		}

		if (b < 0x20 || b == 0x7f) && b != '\n' && b != '\r' && b != '\t' && b != '\f' {
			controlBytes++
		}
	}

	return float64(controlBytes)/float64(len(preview)) <= 0.02
}

func isLikelyGB18030Text(preview []byte) bool {
	if len(preview) == 0 {
		return false
	}

	asciiTextBytes := 0
	multiByteTextBytes := 0
	invalidBytes := 0
	for i := 0; i < len(preview); {
		b := preview[i]
		switch {
		case b == 0:
			return false
		case isASCIITextByte(b):
			asciiTextBytes++
			i++
		case b < 0x20 || b == 0x7f || b == 0x80 || b == 0xff:
			invalidBytes++
			i++
		case isGB18030FourByteSequence(preview, i):
			multiByteTextBytes += 4
			i += 4
		case isGBKTwoByteSequence(preview, i):
			multiByteTextBytes += 2
			i += 2
		default:
			invalidBytes++
			i++
		}
	}

	if multiByteTextBytes == 0 {
		return false
	}

	textBytes := asciiTextBytes + multiByteTextBytes
	if textBytes == 0 {
		return false
	}

	return float64(invalidBytes)/float64(len(preview)) <= 0.02 &&
		float64(textBytes)/float64(len(preview)) >= 0.9
}

func isASCIITextByte(b byte) bool {
	if b >= 0x20 && b <= 0x7e {
		return true
	}

	switch b {
	case '\n', '\r', '\t', '\f':
		return true
	default:
		return false
	}
}

func isGBKTwoByteSequence(preview []byte, i int) bool {
	if i+1 >= len(preview) {
		return false
	}

	first, second := preview[i], preview[i+1]
	return first >= 0x81 && first <= 0xfe &&
		second >= 0x40 && second <= 0xfe &&
		second != 0x7f
}

func isGB18030FourByteSequence(preview []byte, i int) bool {
	if i+3 >= len(preview) {
		return false
	}

	first, second, third, fourth := preview[i], preview[i+1], preview[i+2], preview[i+3]
	return first >= 0x81 && first <= 0xfe &&
		second >= 0x30 && second <= 0x39 &&
		third >= 0x81 && third <= 0xfe &&
		fourth >= 0x30 && fourth <= 0x39
}

func tikaContentTypeByName(fileName string) string {
	return tikaContentTypeByNameWithMimeMapping(fileName, nil)
}

func tikaContentTypeByNameWithMimeMapping(fileName string, mimeMapping map[string]string) string {
	lowerName := strings.ToLower(strings.TrimSpace(fileName))
	baseName := strings.ToLower(strings.TrimSpace(filepath.Base(lowerName)))
	if strings.EqualFold(baseName, "winmail.dat") {
		return "application/vnd.ms-tnef"
	}
	if contentType, ok := explicitTikaBaseNameContentTypes[baseName]; ok {
		return contentType
	}
	if strings.HasPrefix(baseName, ".env.") {
		return "text/plain"
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
	} {
		if strings.HasSuffix(lowerName, item.suffix) {
			return item.mime
		}
	}

	ext := strings.ToLower(filepath.Ext(lowerName))
	if ext == "" {
		return ""
	}

	if contentType, ok := mimeMapping[ext]; ok {
		return normalizeTextLikeContentType(contentType)
	}

	if contentType, ok := explicitTikaContentTypes[ext]; ok {
		return contentType
	}

	switch ext {
	case ".zip":
		return "application/zip"
	case ".jar", ".war", ".ear":
		return "application/java-archive"
	case ".7z":
		return "application/x-7z-compressed"
	case ".rar":
		return "application/x-rar-compressed"
	case ".tar":
		return "application/x-tar"
	case ".ar":
		return "application/x-archive"
	case ".gz":
		return "application/gzip"
	case ".z":
		return "application/x-compress"
	case ".bz2":
		return "application/x-bzip2"
	case ".bz":
		return "application/x-bzip"
	case ".xz":
		return "application/x-xz"
	case ".lzma":
		return "application/x-lzma"
	case ".lz4":
		return "application/x-lz4"
	case ".br":
		return "application/x-brotli"
	case ".snappy", ".sz":
		return "application/x-snappy"
	case ".pack200", ".pack":
		return "application/x-java-pack200"
	case ".cpio":
		return "application/x-cpio"
	case ".arj":
		return "application/x-arj"
	case ".dump":
		return "application/x-tika-unix-dump"
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

	switch strings.ToLower(mediaType) {
	case "application/x-java-archive":
		mediaType = "application/java-archive"
	}

	normalized := mime.FormatMediaType(mediaType, params)
	if normalized == "" {
		return mediaType
	}

	return normalized
}

func isCharsetCompatibleTikaContentType(contentType string) bool {
	if strings.HasPrefix(contentType, "text/") {
		return true
	}

	switch contentType {
	case "application/javascript",
		"application/x-javascript",
		"application/typescript",
		"application/json",
		"application/xml",
		"application/x-sh",
		"application/x-perl",
		"application/x-sql",
		"application/mbox",
		"message/rfc822":
		return true
	default:
		return false
	}
}

func isStructuredTextTikaContentType(contentType string) bool {
	switch contentType {
	case "application/javascript",
		"application/x-javascript",
		"application/typescript",
		"application/json",
		"application/xml",
		"application/x-sh",
		"text/html":
		return true
	default:
		return false
	}
}
