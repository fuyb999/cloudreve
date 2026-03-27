package extractor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"golang.org/x/text/encoding/simplifiedchinese"
)

type testTikaSettingProvider struct {
	setting.Provider
	mimeMapping string
}

func (s testTikaSettingProvider) MimeMapping(ctx context.Context) string {
	return s.mimeMapping
}

func TestFileAwareHeadersAddsNameAndContentType(t *testing.T) {
	headers := fileAwareHeaders(http.Header{
		"Accept": {"text/plain"},
	}, "report.txt")

	if got := headers.Get("Content-Disposition"); got != `attachment; filename="report.txt"` {
		t.Fatalf("unexpected content disposition: %q", got)
	}
	if got := headers.Get("Content-Type"); got != "text/plain" {
		t.Fatalf("unexpected content type: %q", got)
	}
	if got := headers.Get("Accept"); got != "text/plain" {
		t.Fatalf("unexpected accept header: %q", got)
	}
}

func TestFileAwareHeadersFallsBackForKnownOfficeTypes(t *testing.T) {
	headers := fileAwareHeaders(nil, "report.docx")
	if got := headers.Get("Content-Type"); got != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		t.Fatalf("unexpected docx content type: %q", got)
	}
}

func TestFileAwareHeadersSupportsArchiveTypes(t *testing.T) {
	cases := map[string]string{
		"bundle.zip":     "application/zip",
		"bundle.tar":     "application/x-tar",
		"bundle.tar.gz":  "application/gzip",
		"bundle.tgz":     "application/gzip",
		"bundle.7z":      "application/x-7z-compressed",
		"bundle.rar":     "application/x-rar-compressed",
		"bundle.ar":      "application/x-archive",
		"bundle.tar.xz":  "application/x-xz",
		"bundle.tar.bz2": "application/x-bzip2",
		"bundle.z":       "application/x-compress",
		"bundle.bz":      "application/x-bzip",
		"bundle.br":      "application/x-brotli",
		"bundle.lz4":     "application/x-lz4",
		"bundle.snappy":  "application/x-snappy",
		"bundle.pack200": "application/x-java-pack200",
		"bundle.dump":    "application/x-tika-unix-dump",
	}

	for fileName, want := range cases {
		headers := fileAwareHeaders(nil, fileName)
		if got := headers.Get("Content-Type"); got != want {
			t.Fatalf("unexpected content type for %s: got %q want %q", fileName, got, want)
		}
	}
}

func TestFileAwareHeadersSupportsChineseArchiveTypes(t *testing.T) {
	cases := []struct {
		fileName            string
		expectedName        string
		expectedContentType string
	}{
		{
			fileName:            "中文.zip",
			expectedName:        "中文.zip",
			expectedContentType: "application/zip",
		},
		{
			fileName:            "目录/中文.rar",
			expectedName:        "中文.rar",
			expectedContentType: "application/x-rar-compressed",
		},
		{
			fileName:            "压缩包/测试.7z",
			expectedName:        "测试.7z",
			expectedContentType: "application/x-7z-compressed",
		},
	}

	for _, tc := range cases {
		headers := fileAwareHeaders(nil, tc.fileName)
		if got := headers.Get("Content-Disposition"); got != `attachment; filename="`+tc.expectedName+`"` {
			t.Fatalf("%s: unexpected content disposition: %q", tc.fileName, got)
		}
		if got := headers.Get("Content-Type"); got != tc.expectedContentType {
			t.Fatalf("%s: unexpected content type: got %q want %q", tc.fileName, got, tc.expectedContentType)
		}
	}
}

func TestFileAwareHeadersSupportsTNEFByName(t *testing.T) {
	headers := fileAwareHeaders(nil, "winmail.dat")
	if got := headers.Get("Content-Type"); got != "application/vnd.ms-tnef" {
		t.Fatalf("unexpected tnef content type: %q", got)
	}
}

func TestFileAwareHeadersSupportsMailTypes(t *testing.T) {
	cases := map[string]string{
		"mail.eml":   "message/rfc822",
		"mail.mht":   "message/rfc822",
		"mail.mhtml": "message/rfc822",
		"mail.nws":   "message/rfc822",
		"mail.mbox":  "application/mbox",
		"mail.msg":   "application/vnd.ms-outlook",
	}

	for fileName, want := range cases {
		headers := fileAwareHeaders(nil, fileName)
		if got := headers.Get("Content-Type"); got != want {
			t.Fatalf("unexpected mail content type for %s: got %q want %q", fileName, got, want)
		}
	}
}

func TestFileAwareHeadersSupportsCodeAndConfigTypes(t *testing.T) {
	cases := map[string]string{
		"app.js":          "application/javascript",
		"module.mjs":      "application/javascript",
		"script.cjs":      "application/javascript",
		"run.bat":         "text/plain",
		"run.cmd":         "text/plain",
		"style.css":       "text/css",
		"style.scss":      "text/plain",
		"style.less":      "text/plain",
		"index.ts":        "text/plain",
		"index.tsx":       "text/plain",
		"view.jsx":        "text/plain",
		"build.ps1":       "text/plain",
		"config.yaml":     "text/plain",
		"config.yml":      "text/plain",
		"config.json5":    "text/plain",
		"config.toml":     "text/plain",
		"config.ini":      "text/plain",
		"config.cfg":      "text/plain",
		"config.cnf":      "text/plain",
		"app.conf":        "text/plain",
		"app.properties":  "text/plain",
		"shell.ksh":       "application/x-sh",
		"main.py":         "text/plain",
		"main.pl":         "text/plain",
		"main.pm":         "text/plain",
		"main.lua":        "text/plain",
		"main.go":         "text/plain",
		"go.mod":          "text/plain",
		"go.sum":          "text/plain",
		"main.java":       "text/plain",
		"main.kt":         "text/plain",
		"build.gradle":    "text/plain",
		"build.kts":       "text/plain",
		"main.scala":      "text/plain",
		"main.swift":      "text/plain",
		"main.dart":       "text/plain",
		"query.sql":       "application/x-sql",
		"schema.graphql":  "text/plain",
		"schema.gql":      "text/plain",
		"main.proto":      "text/plain",
		"main.tf":         "text/plain",
		"vars.tfvars":     "text/plain",
		"policy.rego":     "text/plain",
		"main.php":        "text/plain",
		"main.rb":         "text/plain",
		"main.rs":         "text/plain",
		"main.c":          "text/plain",
		"main.h":          "text/plain",
		"main.cpp":        "text/plain",
		"main.hpp":        "text/plain",
		"main.cc":         "text/plain",
		"main.hh":         "text/plain",
		"app.service":     "text/plain",
		"packages.list":   "text/plain",
		"repo.repo":       "text/plain",
		"desktop.desktop": "text/plain",
		"deps.lock":       "text/plain",
		"App.vue":         "text/plain",
		"App.svelte":      "text/plain",
		"notes.log":       "text/plain",
	}

	for fileName, want := range cases {
		headers := fileAwareHeaders(nil, fileName)
		if got := headers.Get("Content-Type"); got != want {
			t.Fatalf("unexpected code/config content type for %s: got %q want %q", fileName, got, want)
		}
	}
}

func TestFileAwareHeadersSupportsBasenameOnlyTextFiles(t *testing.T) {
	cases := map[string]string{
		"Dockerfile":    "text/plain",
		"Makefile":      "text/plain",
		"Caddyfile":     "text/plain",
		"Justfile":      "text/plain",
		"Pipfile":       "text/plain",
		"README":        "text/plain",
		"LICENSE":       "text/plain",
		"gradlew":       "application/x-sh",
		"mvnw":          "application/x-sh",
		"configure":     "application/x-sh",
		".env":          "text/plain",
		".env.local":    "text/plain",
		".gitignore":    "text/plain",
		".prettierrc":   "text/plain",
		".eslintrc":     "text/plain",
		".envrc":        "application/x-sh",
		".editorconfig": "text/plain",
		".pypirc":       "text/plain",
		".tmux.conf":    "text/plain",
		".bashrc":       "application/x-sh",
		".zshenv":       "application/x-sh",
	}

	for fileName, want := range cases {
		headers := fileAwareHeaders(nil, fileName)
		if got := headers.Get("Content-Type"); got != want {
			t.Fatalf("unexpected basename content type for %s: got %q want %q", fileName, got, want)
		}
	}
}

func TestNormalizeTextLikeContentTypeStripsForcedUTF8Charset(t *testing.T) {
	cases := map[string]string{
		"text/plain; charset=utf-8":       "text/plain",
		"text/html; charset=UTF-8":        "text/html",
		"application/json; charset=utf-8": "application/json",
		"application/pdf":                 "application/pdf",
	}

	for input, want := range cases {
		if got := normalizeTextLikeContentType(input); got != want {
			t.Fatalf("unexpected normalized content type for %q: got %q want %q", input, got, want)
		}
	}
}

func TestFileAwareHeadersSkipsEmptyFileName(t *testing.T) {
	headers := fileAwareHeaders(http.Header{}, "")
	if got := headers.Get("Content-Disposition"); got != "" {
		t.Fatalf("unexpected content disposition for empty file name: %q", got)
	}
	if got := headers.Get("Content-Type"); got != "" {
		t.Fatalf("unexpected content type for empty file name: %q", got)
	}
}

func TestPrepareTikaRequestSniffsPlainTextForUnknownExtension(t *testing.T) {
	body := "console.log('hello');\nconst answer = 42;\n"
	reader, headers, err := prepareTikaRequest(strings.NewReader(body), http.Header{
		"Accept": {"text/plain"},
	}, "snippet.custom")
	if err != nil {
		t.Fatalf("unexpected prepare request error: %v", err)
	}

	if got := headers.Get("Content-Type"); got != "text/plain" {
		t.Fatalf("unexpected sniffed content type: %q", got)
	}
	if got := headers.Get("Content-Disposition"); got != `attachment; filename="snippet.custom"` {
		t.Fatalf("unexpected content disposition: %q", got)
	}

	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("unexpected reader error: %v", err)
	}
	if string(raw) != body {
		t.Fatalf("unexpected preserved body: got %q want %q", string(raw), body)
	}
}

func TestPrepareTikaRequestDoesNotForceBinaryToText(t *testing.T) {
	reader, headers, err := prepareTikaRequest(strings.NewReader("\x00\x01\x02\x03binary"), nil, "payload.unknown")
	if err != nil {
		t.Fatalf("unexpected prepare request error: %v", err)
	}

	if got := headers.Get("Content-Type"); got != "" {
		t.Fatalf("unexpected content type for binary payload: %q", got)
	}

	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("unexpected reader error: %v", err)
	}
	if string(raw) != "\x00\x01\x02\x03binary" {
		t.Fatalf("unexpected preserved binary body: %q", string(raw))
	}
}

func TestPrepareTikaRequestSniffsLegacyChineseTextWithoutExtension(t *testing.T) {
	cases := []struct {
		name    string
		encoder func([]byte) ([]byte, error)
	}{
		{
			name: "gbk",
			encoder: func(input []byte) ([]byte, error) {
				return simplifiedchinese.GBK.NewEncoder().Bytes(input)
			},
		},
		{
			name: "gb18030-gb2312-compatible",
			encoder: func(input []byte) ([]byte, error) {
				return simplifiedchinese.GB18030.NewEncoder().Bytes(input)
			},
		},
		{
			name: "gb2312",
			encoder: func(input []byte) ([]byte, error) {
				return []byte{
					0xd6, 0xd0, 0xce, 0xc4, 0xd7, 0xa2, 0xca, 0xcd, 0x3d, 0xc6,
					0xf4, 0xd3, 0xc3, 0x0a, 0x6e, 0x61, 0x6d, 0x65, 0x3d, 0x63,
					0x6c, 0x6f, 0x75, 0x64, 0x72, 0x65, 0x76, 0x65, 0x0a,
				}, nil
			},
		},
	}

	source := []byte("中文注释=启用\nname=cloudreve\n")
	for _, tc := range cases {
		encoded, err := tc.encoder(source)
		if err != nil {
			t.Fatalf("%s: unexpected encode error: %v", tc.name, err)
		}

		reader, headers, err := prepareTikaRequest(bytes.NewReader(encoded), nil, "payload.unknown")
		if err != nil {
			t.Fatalf("%s: unexpected prepare request error: %v", tc.name, err)
		}

		if got := headers.Get("Content-Type"); got != "text/plain; charset=gb18030" {
			t.Fatalf("%s: unexpected legacy content type: %q", tc.name, got)
		}

		raw, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("%s: unexpected reader error: %v", tc.name, err)
		}
		if !bytes.Equal(raw, encoded) {
			t.Fatalf("%s: unexpected preserved body", tc.name)
		}
	}
}

func TestPrepareTikaRequestKeepsUTF8TextWithoutLegacyCharset(t *testing.T) {
	cases := []struct {
		name         string
		fileName     string
		body         string
		expectedType string
	}{
		{
			name:         "plain-text",
			fileName:     "中文.txt",
			body:         "中文内容\nhello\n",
			expectedType: "text/plain",
		},
		{
			name:         "javascript",
			fileName:     "脚本.js",
			body:         "const title = '中文';\nconsole.log(title)\n",
			expectedType: "application/javascript",
		},
		{
			name:         "markdown",
			fileName:     "说明.md",
			body:         "# 标题\n\n正文\n",
			expectedType: "text/markdown",
		},
	}

	for _, tc := range cases {
		reader, headers, err := prepareTikaRequest(strings.NewReader(tc.body), nil, tc.fileName)
		if err != nil {
			t.Fatalf("%s: unexpected prepare request error: %v", tc.name, err)
		}

		if got := headers.Get("Content-Type"); got != tc.expectedType {
			t.Fatalf("%s: unexpected content type: got %q want %q", tc.name, got, tc.expectedType)
		}

		raw, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("%s: unexpected reader error: %v", tc.name, err)
		}
		if string(raw) != tc.body {
			t.Fatalf("%s: unexpected preserved body: got %q want %q", tc.name, string(raw), tc.body)
		}
	}
}

func TestPrepareTikaRequestAddsLegacyCharsetToKnownTextExtensions(t *testing.T) {
	cases := []struct {
		name         string
		fileName     string
		expectedType string
	}{
		{
			name:         "plain-text",
			fileName:     "notes.txt",
			expectedType: "text/plain; charset=gb18030",
		},
		{
			name:         "javascript",
			fileName:     "script.js",
			expectedType: "application/javascript; charset=gb18030",
		},
	}

	encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("中文内容\nconsole.log('hi')\n"))
	if err != nil {
		t.Fatalf("unexpected encode error: %v", err)
	}

	for _, tc := range cases {
		reader, headers, err := prepareTikaRequest(bytes.NewReader(encoded), nil, tc.fileName)
		if err != nil {
			t.Fatalf("%s: unexpected prepare request error: %v", tc.name, err)
		}

		if got := headers.Get("Content-Type"); got != tc.expectedType {
			t.Fatalf("%s: unexpected merged content type: got %q want %q", tc.name, got, tc.expectedType)
		}

		raw, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("%s: unexpected reader error: %v", tc.name, err)
		}
		if !bytes.Equal(raw, encoded) {
			t.Fatalf("%s: unexpected preserved body", tc.name)
		}
	}
}

func TestPrepareTikaRequestAddsLegacyCharsetToConfiguredCodeTypes(t *testing.T) {
	cases := []struct {
		name         string
		fileName     string
		mimeMapping  map[string]string
		expectedType string
	}{
		{
			name:         "legacy-javascript",
			fileName:     "script.js",
			mimeMapping:  map[string]string{".js": "application/x-javascript"},
			expectedType: "application/x-javascript; charset=gb18030",
		},
		{
			name:         "typescript",
			fileName:     "main.ts",
			mimeMapping:  map[string]string{".ts": "application/typescript"},
			expectedType: "application/typescript; charset=gb18030",
		},
	}

	encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("中文内容\nconst answer = 42\n"))
	if err != nil {
		t.Fatalf("unexpected encode error: %v", err)
	}

	for _, tc := range cases {
		reader, headers, err := prepareTikaRequestWithMimeMapping(bytes.NewReader(encoded), nil, tc.fileName, tc.mimeMapping)
		if err != nil {
			t.Fatalf("%s: unexpected prepare request error: %v", tc.name, err)
		}

		if got := headers.Get("Content-Type"); got != tc.expectedType {
			t.Fatalf("%s: unexpected merged content type: got %q want %q", tc.name, got, tc.expectedType)
		}

		raw, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("%s: unexpected reader error: %v", tc.name, err)
		}
		if !bytes.Equal(raw, encoded) {
			t.Fatalf("%s: unexpected preserved body", tc.name)
		}
	}
}

func TestPrepareTikaRequestPromotesGenericTextTypeFromContent(t *testing.T) {
	body := "<!DOCTYPE html><html><body>Hello</body></html>"
	reader, headers, err := prepareTikaRequest(strings.NewReader(body), nil, "page.txt")
	if err != nil {
		t.Fatalf("unexpected prepare request error: %v", err)
	}

	if got := headers.Get("Content-Type"); got != "text/html" {
		t.Fatalf("unexpected promoted content type: %q", got)
	}

	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("unexpected reader error: %v", err)
	}
	if string(raw) != body {
		t.Fatalf("unexpected preserved body: got %q want %q", string(raw), body)
	}
}

func TestPrepareTikaRequestUsesConfiguredMimeMapping(t *testing.T) {
	extractor := NewTikaExtractor(
		nil,
		testTikaSettingProvider{mimeMapping: `{".custom":"text/plain",".ts":"application/typescript; charset=utf-8"}`},
		logging.NewConsoleLogger(logging.LevelError),
		&setting.FTSTikaExtractorSetting{},
	)

	cases := map[string]string{
		"snippet.custom": "text/plain",
		"index.ts":       "application/typescript",
	}

	for fileName, want := range cases {
		body := "const answer = 42;\n"
		reader, headers, err := prepareTikaRequestWithMimeMapping(strings.NewReader(body), nil, fileName, extractor.mimeMapping)
		if err != nil {
			t.Fatalf("%s: unexpected prepare request error: %v", fileName, err)
		}

		if got := headers.Get("Content-Type"); got != want {
			t.Fatalf("%s: unexpected configured content type: got %q want %q", fileName, got, want)
		}

		raw, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("%s: unexpected reader error: %v", fileName, err)
		}
		if string(raw) != body {
			t.Fatalf("%s: unexpected preserved body: got %q want %q", fileName, string(raw), body)
		}
	}
}

func TestNewTikaExtractorHandlesNilSettingsAndConfig(t *testing.T) {
	extractor := NewTikaExtractor(nil, nil, logging.NewConsoleLogger(logging.LevelError), nil)
	if extractor == nil {
		t.Fatal("expected extractor")
	}

	headers := fileAwareHeadersWithMimeMapping(nil, "index.ts", extractor.mimeMapping)
	if got := headers.Get("Content-Type"); got != "text/plain" {
		t.Fatalf("unexpected fallback content type: %q", got)
	}
}

func TestPrepareTikaRequestSniffsStructuredTextWithoutExtension(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantType string
	}{
		{
			name:     "json",
			body:     "{\n  \"name\": \"cloudreve\"\n}\n",
			wantType: "application/json",
		},
		{
			name:     "html",
			body:     "<!DOCTYPE html><html><body>Hello</body></html>",
			wantType: "text/html",
		},
		{
			name:     "xml",
			body:     "<?xml version=\"1.0\"?><root><item>1</item></root>",
			wantType: "application/xml",
		},
		{
			name:     "shell",
			body:     "#!/usr/bin/env bash\necho hello\n",
			wantType: "application/x-sh",
		},
		{
			name:     "node",
			body:     "#!/usr/bin/env node\nconsole.log('hello')\n",
			wantType: "application/javascript",
		},
	}

	for _, tc := range cases {
		reader, headers, err := prepareTikaRequest(strings.NewReader(tc.body), nil, "payload")
		if err != nil {
			t.Fatalf("%s: unexpected prepare request error: %v", tc.name, err)
		}
		if got := headers.Get("Content-Type"); got != tc.wantType {
			t.Fatalf("%s: unexpected content type: got %q want %q", tc.name, got, tc.wantType)
		}

		raw, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("%s: unexpected reader error: %v", tc.name, err)
		}
		if string(raw) != tc.body {
			t.Fatalf("%s: unexpected preserved body: got %q want %q", tc.name, string(raw), tc.body)
		}
	}
}
