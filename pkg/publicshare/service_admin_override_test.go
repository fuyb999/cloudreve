package publicshare

import (
	"context"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
)

func TestCheckActionByFileAdminIgnoresVisibilityOverride(t *testing.T) {
	publicRoot := &ent.File{
		ID:           10,
		OwnerID:      -1,
		Name:         inventory.RootFolderName,
		Type:         int(types.FileTypeFolder),
		FileChildren: 0,
	}
	publicChildRoot := &ent.File{
		ID:           20,
		OwnerID:      7,
		Name:         "team-space",
		Type:         int(types.FileTypeFolder),
		TreePath:     "10.20",
		FileChildren: 10,
	}
	service := &Service{
		fileClient: &adminOverrideTestFileClient{
			root:     publicRoot,
			children: []*ent.File{publicChildRoot},
		},
		settingClient: testSettingClient{values: map[string]string{
			PublicRootFileIDSetting: "10",
		}},
	}
	target := &ent.File{
		ID:           21,
		FileChildren: 20,
		OwnerID:      7,
		TreePath:     "10.20.21",
	}
	override := &VisibilityResult{
		RootGrants: []RootGrant{
			{
				RootFileID:   20,
				RootOwnerID:  7,
				RootTreePath: "10.20",
				Actions: map[Action]bool{
					ActionList:   true,
					ActionUpload: false,
					ActionCreate: false,
				},
			},
		},
	}
	admin := &ent.User{
		ID: 1,
		Edges: ent.UserEdges{
			Group: &ent.Group{
				Permissions: testAdminPermissions(),
			},
		},
	}
	ctx := context.WithValue(context.Background(), VisibilityOverrideCtx{}, override)

	decision, err := service.CheckActionByFile(ctx, admin, target, ActionUpload)
	if err != nil {
		t.Fatalf("unexpected CheckActionByFile error: %v", err)
	}
	if decision == nil || !decision.Allowed {
		t.Fatalf("expected admin upload decision to stay allowed, got %+v", decision)
	}
	if !decision.Actions[ActionCreate] || !decision.Actions[ActionUpload] {
		t.Fatalf("expected admin actions to stay fully allowed, got %+v", decision.Actions)
	}
	if got, want := decision.RootFileID, 20; got != want {
		t.Fatalf("unexpected root file id: got %d want %d", got, want)
	}
}

type adminOverrideTestFileClient struct {
	inventory.FileClient
	root             *ent.File
	children         []*ent.File
	ancestorByTarget map[int][]*ent.File
}

func (c *adminOverrideTestFileClient) GetByID(ctx context.Context, id int) (*ent.File, error) {
	if c.root != nil && c.root.ID == id {
		return c.root, nil
	}

	return nil, &ent.NotFoundError{}
}

func (c *adminOverrideTestFileClient) GetChildFiles(ctx context.Context, args *inventory.ListFileParameters, ownerID int, roots ...*ent.File) (*inventory.ListFileResult, error) {
	if len(roots) == 1 && c.root != nil && roots[0] != nil && roots[0].ID == c.root.ID {
		return &inventory.ListFileResult{Files: append([]*ent.File(nil), c.children...)}, nil
	}

	return &inventory.ListFileResult{}, nil
}

func (c *adminOverrideTestFileClient) GetAncestorFiles(ctx context.Context, target *ent.File) ([]*ent.File, error) {
	if target != nil && c.ancestorByTarget != nil {
		if items, ok := c.ancestorByTarget[target.ID]; ok {
			return append([]*ent.File(nil), items...), nil
		}
	}

	return nil, inventory.ErrTreePathQueryUnavailable
}

func testAdminPermissions() *boolset.BooleanSet {
	permissions := &boolset.BooleanSet{}
	boolset.Sets(map[types.GroupPermission]bool{
		types.GroupPermissionIsAdmin: true,
	}, permissions)
	return permissions
}

func TestDecisionFromVisibilityRejectsMissingUploadGrant(t *testing.T) {
	target := &ent.File{
		ID:           22,
		FileChildren: 20,
		TreePath:     "10.20.22",
		Edges: ent.FileEdges{
			Parent: &ent.File{ID: 20},
		},
	}

	decision := decisionFromVisibility(target, ActionUpload, &VisibilityResult{
		RootGrants: []RootGrant{
			{
				RootFileID:   20,
				RootOwnerID:  7,
				RootTreePath: "10.20",
				Actions: map[Action]bool{
					ActionList:   true,
					ActionUpload: false,
				},
			},
		},
	})

	if decision == nil || decision.Allowed {
		t.Fatalf("expected upload to be denied when grant does not allow it, got %+v", decision)
	}
}

func TestDecisionFromVisibilityRejectsTargetExcludedByVisibilityFilter(t *testing.T) {
	target := &ent.File{
		ID:       40,
		OwnerID:  7,
		TreePath: "10.20.30.40",
	}

	decision := decisionFromVisibility(target, ActionList, &VisibilityResult{
		Filter: &FileFilterExpr{
			Operator: FileFilterOpAnd,
			Children: []*FileFilterExpr{
				{
					Match: &FileFilterMatch{
						Kind:         FileFilterMatchTreePathIn,
						StringValues: []string{"10.20"},
					},
				},
				{
					Operator: FileFilterOpNot,
					Children: []*FileFilterExpr{
						{
							Match: &FileFilterMatch{
								Kind:         FileFilterMatchTreePathIn,
								StringValues: []string{"10.20.30"},
							},
						},
					},
				},
			},
		},
		RootGrants: []RootGrant{
			{
				RootFileID:   20,
				RootOwnerID:  7,
				RootTreePath: "10.20",
				Actions: map[Action]bool{
					ActionList:   true,
					ActionUpload: true,
				},
			},
		},
	})

	if decision == nil || decision.Allowed {
		t.Fatalf("expected excluded target to be denied, got %+v", decision)
	}
	if decision.Reason != "root_not_visible" {
		t.Fatalf("unexpected deny reason: %s", decision.Reason)
	}
}
