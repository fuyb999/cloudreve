package dbfs

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/file"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

func TestPublicNavigatorGrantForFileRejectsNilModel(t *testing.T) {
	n := &publicNavigator{
		visibility: &publicshare.VisibilityResult{
			RootGrants: []publicshare.RootGrant{
				{RootFileID: 1, RootOwnerID: 2, RootTreePath: "1.2"},
			},
		},
	}

	file := &File{}
	if _, ok := n.grantForFile(file); ok {
		t.Fatalf("expected nil model file to be rejected")
	}
}

func TestProjectedRootFromCacheIgnoresRealRootChildren(t *testing.T) {
	n := &publicNavigator{
		root: &File{
			Model:    &ent.File{ID: 1, Name: publicshare.DefaultRootName, Type: int(types.FileTypeFolder)},
			Children: map[string]*File{},
		},
	}
	n.root.mu = &sync.Mutex{}

	realChild := &File{
		Model:  &ent.File{ID: 2, Name: "draft.txt", Type: int(types.FileTypeFile)},
		Parent: n.root,
	}
	n.root.Children["draft.txt"] = realChild

	if got, ok := n.projectedRootFromCache("draft.txt"); ok || got != nil {
		t.Fatalf("expected plain root child cache entry to be ignored, got %+v", got)
	}
}

func TestProjectedRootFromCacheUsesPrefixedAliasOnly(t *testing.T) {
	n := &publicNavigator{
		root: &File{
			Model:    &ent.File{ID: 1, Name: publicshare.DefaultRootName, Type: int(types.FileTypeFolder)},
			Children: map[string]*File{},
		},
	}
	n.root.mu = &sync.Mutex{}

	projected := &File{
		Model:  &ent.File{ID: 2, Name: "team-alpha", Type: int(types.FileTypeFolder)},
		Parent: n.root,
	}
	n.root.Children[projectedRootCacheKey("team-alpha__abc")] = projected

	got, ok := n.projectedRootFromCache("team-alpha__abc")
	if !ok || got != projected {
		t.Fatalf("expected projected root alias lookup to hit prefixed cache entry, got %+v, ok=%v", got, ok)
	}
}

func TestResolveRootChildByDisplayNameMatchesTopLevelPublicFileWithoutSuffix(t *testing.T) {
	hasher, err := hashid.New("test-salt")
	if err != nil {
		t.Fatalf("failed to create hasher: %v", err)
	}

	rootModel := &ent.File{ID: 1, Name: publicshare.DefaultRootName, Type: int(types.FileTypeFolder)}
	n := &publicNavigator{
		user: &ent.User{ID: 1},
		baseNavigator: &baseNavigator{
			hasher: hasher,
			config: &setting.DBFS{MaxPageSize: 100},
		},
		root: &File{
			Model:    rootModel,
			Children: map[string]*File{},
			mu:       &sync.Mutex{},
		},
		config: &setting.DBFS{MaxPageSize: 100},
		visibility: &publicshare.VisibilityResult{
			Filter:     publicshare.TrueFilter(),
			RootGrants: []publicshare.RootGrant{{RootFileID: 1, RootTreePath: "1", Actions: map[publicshare.Action]bool{}}},
		},
		fileClient: &testPublicNavigatorFileClient{
			childrenByParent: map[int][]*ent.File{
				1: {
					{ID: 56, Name: "install.sh", FileChildren: 1, Type: int(types.FileTypeFile), TreePath: "1.56"},
				},
			},
			ancestorsByID: map[int][]*ent.File{
				56: {rootModel},
			},
		},
	}

	got, ok, err := n.resolveRootChildByDisplayName(context.Background(), "install.sh")
	if err != nil {
		t.Fatalf("resolveRootChildByDisplayName returned error: %v", err)
	}
	if !ok || got == nil {
		t.Fatalf("expected to resolve top-level public file by display name")
	}
	if got.Name() != "install.sh" || got.ID() != 56 {
		t.Fatalf("unexpected file resolved: id=%d name=%q", got.ID(), got.Name())
	}
}

func TestResolveRootChildByDisplayNameMatchesHistoricalTopLevelPublicFileWithSuffix(t *testing.T) {
	hasher, err := hashid.New("test-salt")
	if err != nil {
		t.Fatalf("failed to create hasher: %v", err)
	}

	dirtyName := "omx-tika-pubtxt-20260501-142102.txt__" + hashid.EncodeFileID(hasher, 24)
	rootModel := &ent.File{ID: 1, Name: publicshare.DefaultRootName, Type: int(types.FileTypeFolder)}
	n := &publicNavigator{
		user: &ent.User{ID: 1},
		baseNavigator: &baseNavigator{
			hasher: hasher,
			config: &setting.DBFS{MaxPageSize: 100},
		},
		root: &File{
			Model:    rootModel,
			Children: map[string]*File{},
			mu:       &sync.Mutex{},
		},
		config: &setting.DBFS{MaxPageSize: 100},
		visibility: &publicshare.VisibilityResult{
			Filter:     publicshare.TrueFilter(),
			RootGrants: []publicshare.RootGrant{{RootFileID: 1, RootTreePath: "1", Actions: map[publicshare.Action]bool{}}},
		},
		fileClient: &testPublicNavigatorFileClient{
			childrenByParent: map[int][]*ent.File{
				1: {
					{ID: 24, Name: dirtyName, FileChildren: 1, Type: int(types.FileTypeFile), TreePath: "1.24"},
				},
			},
			ancestorsByID: map[int][]*ent.File{
				24: {rootModel},
			},
		},
	}

	got, ok, err := n.resolveRootChildByDisplayName(context.Background(), "omx-tika-pubtxt-20260501-142102.txt")
	if err != nil {
		t.Fatalf("resolveRootChildByDisplayName returned error: %v", err)
	}
	if !ok || got == nil {
		t.Fatalf("expected to resolve historical top-level public file by trimmed display name")
	}
	if got.ID() != 24 {
		t.Fatalf("unexpected file id: got %d want 24", got.ID())
	}
}

func TestPublicNavigatorGetViewUsesPublicFsViewMapWithoutEntDriver(t *testing.T) {
	n := &publicNavigator{
		user: &ent.User{
			ID: 7,
			Settings: &types.UserSetting{
				FsViewMap: map[string]types.ExplorerView{
					string(constants.FileSystemPublic): {
						PageSize:  88,
						View:      "list",
						Thumbnail: false,
					},
				},
			},
		},
	}

	view := n.GetView(context.Background(), nil)
	if view == nil {
		t.Fatalf("expected public view")
	}
	if view.PageSize != 88 || view.View != "list" || view.Thumbnail {
		t.Fatalf("unexpected public view: %+v", *view)
	}
}

func TestPublicNavigatorGetViewFallsBackToDefault(t *testing.T) {
	n := &publicNavigator{
		user: &ent.User{ID: 7},
	}

	view := n.GetView(context.Background(), nil)
	if view == nil {
		t.Fatalf("expected default view")
	}
	if view.PageSize != defaultPageSize || view.View != "grid" || !view.Thumbnail {
		t.Fatalf("unexpected default view: %+v", *view)
	}
}

func TestSortProjectedRootChildrenByNameDescKeepsFoldersFirst(t *testing.T) {
	files := []*File{
		projectedTestFile(1, "Beta", types.FileTypeFolder, 0, time.Unix(10, 0), time.Unix(10, 0)),
		projectedTestFile(2, "Gamma.txt", types.FileTypeFile, 0, time.Unix(20, 0), time.Unix(20, 0)),
		projectedTestFile(3, "Alpha", types.FileTypeFolder, 0, time.Unix(30, 0), time.Unix(30, 0)),
		projectedTestFile(4, "Alpha.txt", types.FileTypeFile, 0, time.Unix(40, 0), time.Unix(40, 0)),
	}

	sortProjectedRootChildren(files, &ListArgs{
		Page: &inventory.PaginationArgs{
			OrderBy: file.FieldName,
			Order:   inventory.OrderDirectionDesc,
		},
	})

	assertProjectedIDs(t, files, []int{1, 3, 2, 4})
}

func TestSortProjectedRootChildrenBySizeDesc(t *testing.T) {
	files := []*File{
		projectedTestFile(1, "small-folder", types.FileTypeFolder, 1, time.Unix(10, 0), time.Unix(10, 0)),
		projectedTestFile(2, "big-file", types.FileTypeFile, 10, time.Unix(20, 0), time.Unix(20, 0)),
		projectedTestFile(3, "big-folder", types.FileTypeFolder, 9, time.Unix(30, 0), time.Unix(30, 0)),
		projectedTestFile(4, "small-file", types.FileTypeFile, 2, time.Unix(40, 0), time.Unix(40, 0)),
	}

	sortProjectedRootChildren(files, &ListArgs{
		Page: &inventory.PaginationArgs{
			OrderBy: file.FieldSize,
			Order:   inventory.OrderDirectionDesc,
		},
	})

	assertProjectedIDs(t, files, []int{3, 1, 2, 4})
}

func TestSortProjectedRootChildrenCreatedAtMatchesPersonalFallback(t *testing.T) {
	files := []*File{
		projectedTestFile(20, "later-created", types.FileTypeFile, 0, time.Unix(10, 0), time.Unix(300, 0)),
		projectedTestFile(10, "earlier-id", types.FileTypeFile, 0, time.Unix(20, 0), time.Unix(100, 0)),
	}

	sortProjectedRootChildren(files, &ListArgs{
		Page: &inventory.PaginationArgs{
			OrderBy: file.FieldCreatedAt,
			Order:   inventory.OrderDirectionAsc,
		},
	})

	// 个人目录 created_at 当前也是退化到 ID 排序，公共根目录需要保持同一语义。
	assertProjectedIDs(t, files, []int{10, 20})
}

func projectedTestFile(id int, name string, fileType types.FileType, size int64, updatedAt, createdAt time.Time) *File {
	return &File{
		Model: &ent.File{
			ID:        id,
			Name:      name,
			Type:      int(fileType),
			Size:      size,
			UpdatedAt: updatedAt,
			CreatedAt: createdAt,
		},
	}
}

func assertProjectedIDs(t *testing.T, files []*File, expected []int) {
	t.Helper()

	if len(files) != len(expected) {
		t.Fatalf("unexpected file count: got %d, want %d", len(files), len(expected))
	}

	for index, expectedID := range expected {
		if got := files[index].ID(); got != expectedID {
			t.Fatalf("unexpected order at index %d: got %d, want %d", index, got, expectedID)
		}
	}
}

func TestCapabilitySetFromActionsIncludesExtendedPublicOperations(t *testing.T) {
	capabilities := capabilitySetFromActions(map[publicshare.Action]bool{
		publicshare.ActionDownload:   true,
		publicshare.ActionDirectLink: true,
		publicshare.ActionArchive:    true,
		publicshare.ActionCopy:       true,
		publicshare.ActionMove:       true,
		publicshare.ActionShare:      true,
	})

	assertCapabilityEnabled(t, capabilities, NavigatorCapabilityDownloadFile)
	assertCapabilityEnabled(t, capabilities, NavigatorCapabilityDirectLink)
	assertCapabilityEnabled(t, capabilities, NavigatorCapabilityCreateArchive)
	assertCapabilityEnabled(t, capabilities, NavigatorCapabilityCopyFile)
	assertCapabilityEnabled(t, capabilities, NavigatorCapabilityMoveFile)
	assertCapabilityEnabled(t, capabilities, NavigatorCapabilityShare)
}

func TestCapabilitySetFromGrantProtectsRootDeleteOnly(t *testing.T) {
	grant := publicshare.RootGrant{
		RootFileID: 10,
		Actions: map[publicshare.Action]bool{
			publicshare.ActionDelete:     true,
			publicshare.ActionDeleteRoot: false,
		},
	}

	rootCapabilities := capabilitySetFromGrant(projectedTestFile(10, "root", types.FileTypeFolder, 0, time.Unix(10, 0), time.Unix(10, 0)), grant)
	if rootCapabilities == nil || rootCapabilities.Enabled(int(NavigatorCapabilityDeleteFile)) {
		t.Fatalf("expected root delete capability to be disabled by delete_root")
	}

	childCapabilities := capabilitySetFromGrant(projectedTestFile(11, "child", types.FileTypeFile, 0, time.Unix(10, 0), time.Unix(10, 0)), grant)
	assertCapabilityEnabled(t, childCapabilities, NavigatorCapabilityDeleteFile)
}

func TestTopLevelProjectedRootGrantsCollapsesDescendants(t *testing.T) {
	grants := []publicshare.RootGrant{
		{RootFileID: 20, RootOwnerID: 1, RootTreePath: "10.20"},
		{RootFileID: 30, RootOwnerID: 1, RootTreePath: "10.20.30"},
		{RootFileID: 40, RootOwnerID: 2, RootTreePath: "10.20.30"},
		{RootFileID: 50, RootOwnerID: 1, RootTreePath: ""},
	}

	filtered := topLevelProjectedRootGrants(grants)
	assertProjectedGrantIDs(t, filtered, []int{20, 50})
}

func TestPublicNavigatorGrantForFileMatchesTreePathAcrossDifferentOwners(t *testing.T) {
	n := &publicNavigator{
		visibility: &publicshare.VisibilityResult{
			RootGrants: []publicshare.RootGrant{
				{RootFileID: 20, RootOwnerID: -1, RootTreePath: "10.20"},
			},
		},
	}

	target := &File{
		Model: &ent.File{
			ID:       88,
			OwnerID:  12345,
			TreePath: "10.20.88",
			Name:     "spec.md",
			Type:     int(types.FileTypeFile),
		},
	}

	grant, ok := n.grantForFile(target)
	if !ok {
		t.Fatalf("expected file under public subtree to inherit grant")
	}
	if grant.RootFileID != 20 {
		t.Fatalf("unexpected matched grant: %+v", grant)
	}
}

type testPublicNavigatorFileClient struct {
	inventory.FileClient
	childrenByParent map[int][]*ent.File
	ancestorsByID    map[int][]*ent.File
}

func (t *testPublicNavigatorFileClient) GetChildFiles(
	_ context.Context,
	_ *inventory.ListFileParameters,
	_ int,
	parent ...*ent.File,
) (*inventory.ListFileResult, error) {
	var files []*ent.File
	for _, item := range parent {
		if item == nil {
			continue
		}
		files = append(files, t.childrenByParent[item.ID]...)
	}

	return &inventory.ListFileResult{
		Files: files,
		PaginationResults: &inventory.PaginationResults{
			PageSize: len(files),
		},
	}, nil
}

func (t *testPublicNavigatorFileClient) GetAncestorFiles(_ context.Context, child *ent.File) ([]*ent.File, error) {
	if child == nil {
		return nil, nil
	}
	return t.ancestorsByID[child.ID], nil
}

func TestPublicNavigatorGrantForFileDoesNotMatchSelfScopeDescendant(t *testing.T) {
	n := &publicNavigator{
		visibility: &publicshare.VisibilityResult{
			RootGrants: []publicshare.RootGrant{
				{RootFileID: 20, RootOwnerID: -1, RootTreePath: "10.20", Scope: publicshare.RootGrantScopeSelf},
			},
		},
	}

	target := &File{
		Model: &ent.File{
			ID:       88,
			OwnerID:  12345,
			TreePath: "10.20.88",
			Name:     "spec.md",
			Type:     int(types.FileTypeFile),
		},
	}

	if _, ok := n.grantForFile(target); ok {
		t.Fatal("expected self scope grant not to match descendant file")
	}
}

func TestPublicNavigatorGrantForFileUsesMoreSpecificDescendantGrant(t *testing.T) {
	n := &publicNavigator{
		visibility: &publicshare.VisibilityResult{
			RootGrants: []publicshare.RootGrant{
				{
					RootFileID:   20,
					RootOwnerID:  -1,
					RootTreePath: "10.20",
					Actions: map[publicshare.Action]bool{
						publicshare.ActionDownload: true,
					},
				},
				{
					RootFileID:   30,
					RootOwnerID:  -1,
					RootTreePath: "10.20.30",
					Actions: map[publicshare.Action]bool{
						publicshare.ActionDownload: false,
					},
				},
			},
		},
	}

	target := &File{
		Model: &ent.File{
			ID:       88,
			OwnerID:  12345,
			TreePath: "10.20.30.88",
			Name:     "blocked.pdf",
			Type:     int(types.FileTypeFile),
		},
	}

	filtered, ok := n.filter(context.Background(), target)
	if !ok || filtered == nil {
		t.Fatal("expected target to remain visible under more specific grant")
	}
	if filtered.Capabilities().Enabled(int(NavigatorCapabilityDownloadFile)) {
		t.Fatalf("expected descendant deny grant to disable download capability")
	}
}

func TestPublicNavigatorFilterRejectsTargetExcludedByVisibilityFilter(t *testing.T) {
	n := &publicNavigator{
		visibility: &publicshare.VisibilityResult{
			Filter: &publicshare.FileFilterExpr{
				Operator: publicshare.FileFilterOpAnd,
				Children: []*publicshare.FileFilterExpr{
					{
						Match: &publicshare.FileFilterMatch{
							Kind:         publicshare.FileFilterMatchTreePathIn,
							StringValues: []string{"10.20"},
						},
					},
					{
						Operator: publicshare.FileFilterOpNot,
						Children: []*publicshare.FileFilterExpr{
							{
								Match: &publicshare.FileFilterMatch{
									Kind:         publicshare.FileFilterMatchTreePathIn,
									StringValues: []string{"10.20.30"},
								},
							},
						},
					},
				},
			},
			RootGrants: []publicshare.RootGrant{
				{
					RootFileID:   20,
					RootOwnerID:  -1,
					RootTreePath: "10.20",
					Actions: map[publicshare.Action]bool{
						publicshare.ActionList:   true,
						publicshare.ActionUpload: true,
					},
				},
			},
		},
	}

	target := &File{
		Model: &ent.File{
			ID:       40,
			OwnerID:  12345,
			TreePath: "10.20.30.40",
			Name:     "secret.docx",
			Type:     int(types.FileTypeFile),
		},
	}

	if filtered, ok := n.filter(context.Background(), target); ok || filtered != nil {
		t.Fatalf("expected target excluded by visibility filter to be rejected, got %+v", filtered)
	}
}

func TestShouldDeferPublicCapabilityCheck(t *testing.T) {
	t.Run("public-root", func(t *testing.T) {
		uri, err := fs.NewUriFromString("cloudreve://public")
		if err != nil {
			t.Fatalf("failed to parse public root uri: %v", err)
		}

		if shouldDeferPublicCapabilityCheck(uri) {
			t.Fatalf("public root should still use navigator capability pre-check")
		}
	})

	t.Run("public-projected-root", func(t *testing.T) {
		uri, err := fs.NewUriFromString("cloudreve://public/34")
		if err != nil {
			t.Fatalf("failed to parse projected public root uri: %v", err)
		}

		if !shouldDeferPublicCapabilityCheck(uri) {
			t.Fatalf("projected public root should defer capability pre-check")
		}
	})

	t.Run("public-subtree", func(t *testing.T) {
		uri, err := fs.NewUriFromString("cloudreve://public/34/child")
		if err != nil {
			t.Fatalf("failed to parse public subtree uri: %v", err)
		}

		if !shouldDeferPublicCapabilityCheck(uri) {
			t.Fatalf("public subtree should defer capability pre-check")
		}
	})

	t.Run("non-public", func(t *testing.T) {
		uri, err := fs.NewUriFromString("cloudreve://my/docs")
		if err != nil {
			t.Fatalf("failed to parse personal uri: %v", err)
		}

		if uri.FileSystem() != constants.FileSystemMy {
			t.Fatalf("unexpected filesystem: %s", uri.FileSystem())
		}
		if shouldDeferPublicCapabilityCheck(uri) {
			t.Fatalf("non-public uri should not defer capability pre-check")
		}
	})
}

func assertCapabilityEnabled(t *testing.T, capabilities *boolset.BooleanSet, capability NavigatorCapability) {
	t.Helper()

	if capabilities == nil || !capabilities.Enabled(int(capability)) {
		t.Fatalf("expected capability %d to be enabled", capability)
	}
}

func assertProjectedGrantIDs(t *testing.T, grants []publicshare.RootGrant, expected []int) {
	t.Helper()

	if len(grants) != len(expected) {
		t.Fatalf("unexpected grant count: got %d, want %d", len(grants), len(expected))
	}

	for index, expectedID := range expected {
		if grants[index].RootFileID != expectedID {
			t.Fatalf("unexpected grant at index %d: got %d, want %d", index, grants[index].RootFileID, expectedID)
		}
	}
}
