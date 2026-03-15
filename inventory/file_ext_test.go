package inventory

import (
	"reflect"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/inventory/types"
)

func TestFileExtValue(t *testing.T) {
	if got := fileExtValue("Photo.JPG", int(types.FileTypeFile)); got != "jpg" {
		t.Fatalf("unexpected file ext: %q", got)
	}

	if got := fileExtValue("folder", int(types.FileTypeFolder)); got != "" {
		t.Fatalf("folder should not have file ext: %q", got)
	}
}

func TestParseSimpleWildcardExtPattern(t *testing.T) {
	ext, suffix, ok := parseSimpleWildcardExtPattern("*.JPG")
	if !ok {
		t.Fatal("expected simple wildcard ext pattern to be recognized")
	}

	if ext != "jpg" || suffix != ".JPG" {
		t.Fatalf("unexpected ext pattern parse result: ext=%q suffix=%q", ext, suffix)
	}

	for _, pattern := range []string{"*", "*.tar.gz", "a*.jpg", "*.jp*g", "*./jpg"} {
		if _, _, ok := parseSimpleWildcardExtPattern(pattern); ok {
			t.Fatalf("pattern %q should not be recognized as file_ext filter", pattern)
		}
	}
}

func TestNormalizeSearchExts(t *testing.T) {
	got := normalizeSearchExts([]string{".JPG", "jpg", " png ", "", ".PNG"})
	want := []string{"jpg", "png"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected normalized ext values: %#v", got)
	}
}
