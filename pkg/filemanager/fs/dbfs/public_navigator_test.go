package dbfs

import (
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/file"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
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
