package dbfs

import (
	"context"
	"fmt"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	entuser "github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
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

func TestMyNavigatorToRejectsOtherUserRootForNonAdmin(t *testing.T) {
	hasher, err := hashid.New("test-salt")
	if err != nil {
		t.Fatalf("failed to create hasher: %v", err)
	}

	targetUser := &ent.User{ID: 2, Status: entuser.StatusActive}
	rootModel := testFolderModel(10, 2, 0, "")
	fileClient := &testMyNavigatorFileClient{
		children: map[int][]*ent.File{},
		roots:    map[int]*ent.File{2: rootModel},
	}
	navigator := &myNavigator{
		user:       &ent.User{ID: 1, Edges: ent.UserEdges{Group: &ent.Group{Permissions: testPermissions()}}},
		l:          logging.NewConsoleLogger(logging.LevelError),
		fileClient: fileClient,
		userClient: &testMyNavigatorUserClient{users: map[int]*ent.User{2: targetUser}},
		config:     &setting.DBFS{MaxPageSize: 100},
	}
	navigator.baseNavigator = newBaseNavigator(fileClient, navigator.filter, navigator.user, hasher, navigator.config)

	targetPath, err := fs.NewUriFromString(fs.NewMyUri(hashid.EncodeUserID(hasher, 2)))
	if err != nil {
		t.Fatalf("failed to build target uri: %v", err)
	}

	if _, err := navigator.To(context.Background(), targetPath); err == nil {
		t.Fatal("expected non-admin access to other user root to be rejected")
	}
}

func TestMyNavigatorToAllowsAdminAccessToOtherUserRootReadOnly(t *testing.T) {
	hasher, err := hashid.New("test-salt")
	if err != nil {
		t.Fatalf("failed to create hasher: %v", err)
	}

	targetUser := &ent.User{ID: 2, Status: entuser.StatusActive}
	rootModel := testFolderModel(10, 2, 0, "")
	fileClient := &testMyNavigatorFileClient{
		children: map[int][]*ent.File{},
		roots:    map[int]*ent.File{2: rootModel},
	}
	adminUser := &ent.User{
		ID: 1,
		Edges: ent.UserEdges{
			Group: &ent.Group{Permissions: testPermissions(types.GroupPermissionIsAdmin)},
		},
	}
	navigator := &myNavigator{
		user:       adminUser,
		l:          logging.NewConsoleLogger(logging.LevelError),
		fileClient: fileClient,
		userClient: &testMyNavigatorUserClient{users: map[int]*ent.User{2: targetUser}},
		config:     &setting.DBFS{MaxPageSize: 100},
	}
	navigator.baseNavigator = newBaseNavigator(fileClient, navigator.filter, navigator.user, hasher, navigator.config)

	targetPath, err := fs.NewUriFromString(fs.NewMyUri(hashid.EncodeUserID(hasher, 2)))
	if err != nil {
		t.Fatalf("failed to build target uri: %v", err)
	}

	root, err := navigator.To(context.Background(), targetPath)
	if err != nil {
		t.Fatalf("expected admin access to other user root, got error: %v", err)
	}
	if root == nil {
		t.Fatal("expected root file")
	}
	if root.Owner() == nil || root.Owner().ID != 2 {
		t.Fatalf("unexpected root owner: %+v", root.Owner())
	}
	if root.View() != nil {
		t.Fatal("expected foreign user root to disable view sync")
	}
	if root.Capabilities() == nil {
		t.Fatal("expected root capabilities")
	}
	if root.Capabilities().Enabled(int(NavigatorCapabilityCreateFile)) {
		t.Fatal("expected foreign user root to be read-only for create_file")
	}
	if !root.Capabilities().Enabled(int(NavigatorCapabilityDownloadFile)) {
		t.Fatal("expected foreign user root to allow downloads")
	}
	if !root.Capabilities().Enabled(int(NavigatorCapabilityListChildren)) {
		t.Fatal("expected foreign user root to allow listing children")
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
	roots    map[int]*ent.File
	files    map[int]*ent.File
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

func (c *testMyNavigatorFileClient) Root(ctx context.Context, user *ent.User) (*ent.File, error) {
	if c.roots == nil {
		return nil, fmt.Errorf("root not found")
	}
	root, ok := c.roots[user.ID]
	if !ok {
		return nil, fmt.Errorf("root not found")
	}
	return root, nil
}

func (c *testMyNavigatorFileClient) GetByID(ctx context.Context, id int) (*ent.File, error) {
	if c.files != nil {
		if file, ok := c.files[id]; ok {
			return file, nil
		}
	}
	return nil, fmt.Errorf("file %d not found", id)
}

func (c *testMyNavigatorFileClient) GetChildFile(ctx context.Context, root *ent.File, ownerID int, child string, eagerLoading bool) (*ent.File, error) {
	if root == nil {
		return nil, fmt.Errorf("root not found")
	}

	for _, item := range c.children[root.ID] {
		if item != nil && item.Name == child {
			return item, nil
		}
	}

	return nil, fmt.Errorf("child %q not found", child)
}

type testMyNavigatorUserClient struct {
	inventory.UserClient
	users map[int]*ent.User
}

func (c *testMyNavigatorUserClient) GetByID(ctx context.Context, id int) (*ent.User, error) {
	user, ok := c.users[id]
	if !ok {
		return nil, fmt.Errorf("user %d not found", id)
	}
	return user, nil
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

func testPermissions(perms ...types.GroupPermission) *boolset.BooleanSet {
	bs := &boolset.BooleanSet{}
	for _, perm := range perms {
		boolset.Set(perm, true, bs)
	}
	return bs
}
