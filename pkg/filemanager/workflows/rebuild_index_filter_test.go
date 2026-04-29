package workflows

import (
	"testing"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
)

func TestShouldIndexRebuildURI(t *testing.T) {
	trashURI := mustRebuildTestURI(t, "cloudreve://trash/7e3f9ef5-62de-4c53-8b76-4fe89d9f3e57/a.txt")
	if got, want := trashURI.FileSystem(), constants.FileSystemTrash; got != want {
		t.Fatalf("unexpected trash filesystem: got %q want %q", got, want)
	}

	tests := []struct {
		name string
		uri  *fs.URI
		want bool
	}{
		{
			name: "nil uri",
			uri:  nil,
			want: true,
		},
		{
			name: "my uri",
			uri:  mustRebuildTestURI(t, "cloudreve:///docs/readme.txt"),
			want: true,
		},
		{
			name: "public uri",
			uri:  mustRebuildTestURI(t, "cloudreve://public/team/spec.md"),
			want: true,
		},
		{
			name: "trash uri",
			uri:  trashURI,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldIndexRebuildURI(tt.uri); got != tt.want {
				t.Fatalf("unexpected shouldIndexRebuildURI result: got %v want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesRebuildStoragePolicy(t *testing.T) {
	doc := &searcher.SearchFileDocument{
		StoragePolicyID: 3,
	}

	if !matchesRebuildStoragePolicy(doc, nil) {
		t.Fatal("expected empty filter to match")
	}

	if !matchesRebuildStoragePolicy(doc, []int{3, 7}) {
		t.Fatal("expected file storage policy to match")
	}

	if matchesRebuildStoragePolicy(doc, []int{9}) {
		t.Fatal("expected unmatched storage policy to be filtered")
	}

	doc.LatestVersion = &searcher.SearchFileVersionDocument{StoragePolicyID: 11}
	if !matchesRebuildStoragePolicy(doc, []int{11}) {
		t.Fatal("expected latest version storage policy to override file storage policy")
	}
	if matchesRebuildStoragePolicy(doc, []int{3}) {
		t.Fatal("expected latest version storage policy to override file storage policy filter")
	}
}

func TestShouldIndexRebuildFile(t *testing.T) {
	tests := []struct {
		name        string
		fileModel   *ent.File
		syncFolders bool
		want        bool
	}{
		{
			name:        "nil file",
			fileModel:   nil,
			syncFolders: false,
			want:        false,
		},
		{
			name:        "regular file always indexed",
			fileModel:   &ent.File{Type: int(types.FileTypeFile)},
			syncFolders: false,
			want:        true,
		},
		{
			name:        "folder skipped when sync disabled",
			fileModel:   &ent.File{Type: int(types.FileTypeFolder)},
			syncFolders: false,
			want:        false,
		},
		{
			name:        "folder indexed when sync enabled",
			fileModel:   &ent.File{Type: int(types.FileTypeFolder)},
			syncFolders: true,
			want:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldIndexRebuildFile(tt.fileModel, tt.syncFolders); got != tt.want {
				t.Fatalf("unexpected shouldIndexRebuildFile result: got %v want %v", got, tt.want)
			}
		})
	}
}

func TestFilterRebuildFiles(t *testing.T) {
	files := []*ent.File{
		{ID: 1, Type: int(types.FileTypeFile)},
		{ID: 2, Type: int(types.FileTypeFolder)},
		{ID: 3, Type: int(types.FileTypeFile)},
		{ID: 4, Type: int(types.FileTypeFolder)},
	}

	filtered, skipped := filterRebuildFiles(files, false)
	if skipped != 2 {
		t.Fatalf("unexpected skipped count: got %d want %d", skipped, 2)
	}
	if len(filtered) != 2 {
		t.Fatalf("unexpected filtered length: got %d want %d", len(filtered), 2)
	}
	if filtered[0].ID != 1 || filtered[1].ID != 3 {
		t.Fatalf("unexpected filtered files: got ids %d,%d", filtered[0].ID, filtered[1].ID)
	}

	filteredAll, skippedAll := filterRebuildFiles(files, true)
	if skippedAll != 0 {
		t.Fatalf("unexpected skipped count when folder sync enabled: got %d want 0", skippedAll)
	}
	if len(filteredAll) != len(files) {
		t.Fatalf("unexpected filtered length when folder sync enabled: got %d want %d", len(filteredAll), len(files))
	}
}

func mustRebuildTestURI(t *testing.T, raw string) *fs.URI {
	t.Helper()

	uri, err := fs.NewUriFromString(raw)
	if err != nil {
		t.Fatalf("failed to parse uri %q: %v", raw, err)
	}

	return uri
}
