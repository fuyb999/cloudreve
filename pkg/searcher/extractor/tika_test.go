package extractor

import (
	"net/http"
	"testing"
)

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
		"app.js":         "application/javascript",
		"module.mjs":     "application/javascript",
		"script.cjs":     "application/javascript",
		"style.css":      "text/css",
		"style.scss":     "text/plain",
		"style.less":     "text/plain",
		"index.ts":       "text/plain",
		"index.tsx":      "text/plain",
		"view.jsx":       "text/plain",
		"config.yaml":    "text/plain",
		"config.yml":     "text/plain",
		"config.toml":    "text/plain",
		"config.ini":     "text/plain",
		"app.conf":       "text/plain",
		"app.properties": "text/plain",
		"main.py":        "text/plain",
		"main.go":        "text/plain",
		"main.java":      "text/plain",
		"main.kt":        "text/plain",
		"query.sql":      "application/x-sql",
		"main.php":       "text/plain",
		"main.rb":        "text/plain",
		"main.rs":        "text/plain",
		"main.c":         "text/plain",
		"main.h":         "text/plain",
		"main.cpp":       "text/plain",
		"main.hpp":       "text/plain",
		"App.vue":        "text/plain",
		"App.svelte":     "text/plain",
		"notes.log":      "text/plain",
	}

	for fileName, want := range cases {
		headers := fileAwareHeaders(nil, fileName)
		if got := headers.Get("Content-Type"); got != want {
			t.Fatalf("unexpected code/config content type for %s: got %q want %q", fileName, got, want)
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
