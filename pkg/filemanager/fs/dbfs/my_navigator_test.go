package dbfs

import (
	"context"
	"fmt"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

func TestMyNavigatorChildrenHidesPublicRoot(t *testing.T) {
	rootModel := testFolderModel(1, 1, 0, "")
	publicModel := testFolderModel(2, 1, 1, publicshare.DefaultRootName)
	docsModel := testFolderModel(3, 1, 1, "docs")

	navigator := newTestMyNavigator(map[int][]*ent.File{
		rootModel.ID: {publicModel, docsModel},
	}, publicModel.ID)

	root := newFile(nil, rootModel)
	root.OwnerModel = &ent.User{ID: 1}
	root.IsUserRoot = true

	res, err := navigator.Children(context.Background(), root, &ListArgs{
		Page: &inventory.PaginationArgs{PageSize: 100},
	})
	if err != nil {
		t.Fatalf("children failed: %v", err)
	}

	if len(res.Files) != 1 {
		t.Fatalf("unexpected visible children count: got %d, want 1", len(res.Files))
	}
	if got := res.Files[0].ID(); got != docsModel.ID {
		t.Fatalf("unexpected visible child: got %d, want %d", got, docsModel.ID)
	}
}

func TestMyNavigatorWalkSkipsPublicRootSubtree(t *testing.T) {
	rootModel := testFolderModel(1, 1, 0, "")
	publicModel := testFolderModel(2, 1, 1, publicshare.DefaultRootName)
	docsModel := testFolderModel(3, 1, 1, "docs")
	publicChildModel := testFileModel(4, 1, 2, "secret.txt")
	docsChildModel := testFileModel(5, 1, 3, "guide.txt")

	navigator := newTestMyNavigator(map[int][]*ent.File{
		rootModel.ID:   {publicModel, docsModel},
		publicModel.ID: {publicChildModel},
		docsModel.ID:   {docsChildModel},
	}, publicModel.ID)

	root := newFile(nil, rootModel)
	root.OwnerModel = &ent.User{ID: 1}
	root.IsUserRoot = true

	visited := make([]int, 0)
	if err := navigator.Walk(context.Background(), []*File{root}, 100, -1, func(files []*File, level int) error {
		for _, file := range files {
			visited = append(visited, file.ID())
		}
		return nil
	}); err != nil {
		t.Fatalf("walk failed: %v", err)
	}

	expected := []int{1, 3, 5}
	if len(visited) != len(expected) {
		t.Fatalf("unexpected visited count: got %d, want %d (%v)", len(visited), len(expected), visited)
	}

	for index, id := range expected {
		if visited[index] != id {
			t.Fatalf("unexpected visited file at index %d: got %d, want %d", index, visited[index], id)
		}
	}
}

func TestMyNavigatorWalkAllowsPublicSubtreeWhenExplicitlyBypassed(t *testing.T) {
	rootModel := testFolderModel(1, 1, 0, "")
	publicModel := testFolderModel(2, 1, 1, publicshare.DefaultRootName)
	publicChildModel := testFileModel(4, 1, 2, "secret.txt")

	navigator := newTestMyNavigator(map[int][]*ent.File{
		rootModel.ID:   {publicModel},
		publicModel.ID: {publicChildModel},
	}, publicModel.ID)

	root := newFile(nil, rootModel)
	root.OwnerModel = &ent.User{ID: 1}
	root.IsUserRoot = true
	publicRoot := newFile(root, publicModel)

	ctx := context.WithValue(context.Background(), hiddenPublicRootAccessCtxKey{}, true)
	visited := make([]int, 0)
	if err := navigator.Walk(ctx, []*File{publicRoot}, 100, -1, func(files []*File, level int) error {
		for _, file := range files {
			visited = append(visited, file.ID())
		}
		return nil
	}); err != nil {
		t.Fatalf("walk with bypass failed: %v", err)
	}

	expected := []int{2, 4}
	if len(visited) != len(expected) {
		t.Fatalf("unexpected visited count: got %d, want %d (%v)", len(visited), len(expected), visited)
	}

	for index, id := range expected {
		if visited[index] != id {
			t.Fatalf("unexpected visited file at index %d: got %d, want %d", index, visited[index], id)
		}
	}
}

func newTestMyNavigator(children map[int][]*ent.File, publicRootID int) *myNavigator {
	fileClient := &testMyNavigatorFileClient{children: children}
	publicService := publicshare.NewService(
		logging.NewConsoleLogger(logging.LevelError),
		fileClient,
		&testMyNavigatorSettingClient{values: map[string]string{
			publicshare.PublicRootFileIDSetting: fmt.Sprintf("%d", publicRootID),
		}},
		nil,
	)

	n := &myNavigator{
		user:          &ent.User{ID: 1},
		l:             logging.NewConsoleLogger(logging.LevelError),
		fileClient:    fileClient,
		config:        &setting.DBFS{MaxPageSize: 100},
		publicService: publicService,
	}
	n.baseNavigator = newBaseNavigator(fileClient, n.filter, n.user, nil, n.config)
	return n
}

func testFolderModel(id, ownerID, parentID int, name string) *ent.File {
	return &ent.File{
		ID:           id,
		OwnerID:      ownerID,
		FileChildren: parentID,
		Name:         name,
		Type:         int(types.FileTypeFolder),
	}
}

func testFileModel(id, ownerID, parentID int, name string) *ent.File {
	return &ent.File{
		ID:           id,
		OwnerID:      ownerID,
		FileChildren: parentID,
		Name:         name,
		Type:         int(types.FileTypeFile),
	}
}

type testMyNavigatorFileClient struct {
	inventory.FileClient
	children map[int][]*ent.File
}

func (c *testMyNavigatorFileClient) GetChildFiles(ctx context.Context, args *inventory.ListFileParameters, ownerID int, roots ...*ent.File) (*inventory.ListFileResult, error) {
	files := make([]*ent.File, 0)
	for _, root := range roots {
		if root == nil {
			continue
		}
		files = append(files, c.children[root.ID]...)
	}

	return &inventory.ListFileResult{
		Files:             files,
		PaginationResults: &inventory.PaginationResults{IsCursor: true},
	}, nil
}

func (c *testMyNavigatorFileClient) GetSubtreeFiles(ctx context.Context, root *ent.File, depth, limit int) ([]*ent.File, error) {
	return nil, inventory.ErrTreePathQueryUnavailable
}

type testMyNavigatorSettingClient struct {
	inventory.SettingClient
	values map[string]string
}

func (c *testMyNavigatorSettingClient) Get(ctx context.Context, name string) (string, error) {
	if value, ok := c.values[name]; ok {
		return value, nil
	}

	return "", fmt.Errorf("setting %q not found", name)
}
