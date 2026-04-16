package publicshare

import (
	"context"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
)

func TestDecodeVisibilityOverrideRebuildsFilter(t *testing.T) {
	encoded := `{"root_grants":[{"root_file_id":20,"root_owner_id":7,"root_tree_path":"10.20","actions":{"list":true,"upload":true}}]}`

	visibility, err := DecodeVisibilityOverride(encoded)
	if err != nil {
		t.Fatalf("failed to decode visibility override: %v", err)
	}
	if visibility == nil {
		t.Fatal("expected decoded visibility override")
	}
	if visibility.Filter == nil {
		t.Fatal("expected filter to be rebuilt from root grants")
	}
	if len(visibility.RootGrants) != 1 || visibility.RootGrants[0].RootFileID != 20 {
		t.Fatalf("unexpected visibility override: %+v", visibility.RootGrants)
	}
}

func TestResolveVisibilityUsesOverrideWithoutOIDCToken(t *testing.T) {
	service := &Service{
		settingClient: testSettingClient{values: map[string]string{
			oidcEnabledSettingKey:    "1",
			oidcConfigModeSettingKey: "remote",
		}},
	}
	override := &VisibilityResult{
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
	}
	ctx := context.WithValue(context.Background(), VisibilityOverrideCtx{}, override)

	visibility, err := service.ResolveVisibility(ctx, &ent.User{ID: 7})
	if err != nil {
		t.Fatalf("failed to resolve visibility with override: %v", err)
	}
	if visibility != override {
		t.Fatalf("expected override visibility to be returned directly")
	}
}

func TestCheckActionByFileUsesOverrideWithoutOIDCToken(t *testing.T) {
	service := &Service{
		settingClient: testSettingClient{values: map[string]string{
			oidcEnabledSettingKey:    "1",
			oidcConfigModeSettingKey: "remote",
		}},
	}
	override := &VisibilityResult{
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
	}
	ctx := context.WithValue(context.Background(), VisibilityOverrideCtx{}, override)
	target := &ent.File{ID: 30, OwnerID: 7, TreePath: "10.20.30"}

	decision, err := service.CheckActionByFile(ctx, &ent.User{ID: 7}, target, ActionUpload)
	if err != nil {
		t.Fatalf("failed to check action with override: %v", err)
	}
	if decision == nil || !decision.Allowed {
		t.Fatalf("expected upload to be allowed by override, got %+v", decision)
	}
	if decision.RootFileID != 20 {
		t.Fatalf("unexpected root file id: %d", decision.RootFileID)
	}
}
