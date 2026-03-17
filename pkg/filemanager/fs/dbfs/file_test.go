package dbfs

import (
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
)

func TestFileRecycleHandlesCycle(t *testing.T) {
	root := &File{
		Model:    &ent.File{ID: 1, Name: "public", Type: int(types.FileTypeFolder)},
		Children: map[string]*File{},
	}

	child := &File{
		Model:    &ent.File{ID: 2, Name: "dept", Type: int(types.FileTypeFolder)},
		Parent:   root,
		Children: map[string]*File{},
	}
	root.Children[child.Name()] = child
	child.Children[child.Name()] = child

	root.Recycle()
}

func TestPublicNavigatorRecycleProjectedRootsDetachesRootCache(t *testing.T) {
	root := newFile(nil, &ent.File{ID: 1, Name: "public", Type: int(types.FileTypeFolder)})

	child := newFile(root, &ent.File{ID: 2, Name: "dept", Type: int(types.FileTypeFolder)})
	n := &publicNavigator{
		root:           root,
		projectedRoots: []*File{child},
	}

	n.recycleProjectedRoots()

	if len(root.Children) != 0 {
		t.Fatalf("projected root child should be detached before recycle, got %d cached children", len(root.Children))
	}

	root.Recycle()
}

func TestFileResolveOwnerURIRebasesProjectedPath(t *testing.T) {
	ownerURI, err := fs.NewUriFromString("cloudreve://owner@my/Team/Docs")
	if err != nil {
		t.Fatalf("failed to parse owner uri: %v", err)
	}
	userURI, err := fs.NewUriFromString("cloudreve://public/docs__abc")
	if err != nil {
		t.Fatalf("failed to parse user uri: %v", err)
	}
	targetURI, err := fs.NewUriFromString("cloudreve://public/docs__abc/specs/design.md")
	if err != nil {
		t.Fatalf("failed to parse target uri: %v", err)
	}

	projected := &File{
		Model:    &ent.File{ID: 2, Name: "Docs", Type: int(types.FileTypeFolder)},
		Children: map[string]*File{},
		Path: [2]*fs.URI{
			ownerURI,
			userURI,
		},
	}

	resolved := projected.ResolveOwnerURI(targetURI)
	if resolved == nil {
		t.Fatalf("resolved uri should not be nil")
	}
	if got, want := resolved.String(), "cloudreve://owner@my/Team/Docs/specs/design.md"; got != want {
		t.Fatalf("unexpected resolved uri: got %q, want %q", got, want)
	}
}
