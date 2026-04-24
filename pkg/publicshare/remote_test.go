package publicshare

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
)

type testSettingClient struct {
	values map[string]string
}

func (t testSettingClient) SetClient(newClient *ent.Client) inventory.TxOperator { return t }
func (t testSettingClient) GetClient() *ent.Client                               { return nil }
func (t testSettingClient) Get(ctx context.Context, name string) (string, error) {
	return t.values[name], nil
}
func (t testSettingClient) Set(ctx context.Context, settings map[string]string) error { return nil }
func (t testSettingClient) Gets(ctx context.Context, names []string) (map[string]string, error) {
	res := make(map[string]string, len(names))
	for _, name := range names {
		res[name] = t.values[name]
	}
	return res, nil
}

func TestConstrainRemoteDecisionToVisibilityRejectsInvisibleTarget(t *testing.T) {
	target := &ent.File{ID: 30, TreePath: "10.20.30"}
	decision := &ActionDecision{
		Allowed: true,
		Action:  ActionUpload,
		Actions: map[Action]bool{
			ActionUpload: true,
		},
		Reason: "allowed",
	}

	constrained := constrainRemoteDecisionToVisibility(target, ActionUpload, &VisibilityResult{
		RootGrants: []RootGrant{
			{RootFileID: 99, RootTreePath: "9.99", Actions: map[Action]bool{ActionUpload: true}},
		},
	}, decision)

	if constrained.Allowed {
		t.Fatalf("expected invisible target to be rejected")
	}
	if constrained.Reason != "root_not_visible" {
		t.Fatalf("unexpected deny reason: %s", constrained.Reason)
	}
}

func TestConstrainRemoteDecisionToVisibilityProtectsRootDelete(t *testing.T) {
	target := &ent.File{ID: 20, TreePath: "10.20"}
	decision := &ActionDecision{
		Allowed: true,
		Action:  ActionDelete,
		Actions: map[Action]bool{
			ActionDelete: true,
		},
		Reason: "allowed",
	}

	constrained := constrainRemoteDecisionToVisibility(target, ActionDelete, &VisibilityResult{
		RootGrants: []RootGrant{
			{
				RootFileID:   20,
				RootTreePath: "10.20",
				Actions: map[Action]bool{
					ActionDelete:     true,
					ActionDeleteRoot: false,
				},
			},
		},
	}, decision)

	if constrained.Allowed {
		t.Fatalf("expected root delete to be rejected")
	}
	if constrained.Reason != "root_delete_protected" {
		t.Fatalf("unexpected deny reason: %s", constrained.Reason)
	}
	if constrained.Actions[ActionDelete] {
		t.Fatalf("expected delete action to be removed for root")
	}
}

func TestConstrainRemoteDecisionToVisibilityUsesDeepestGrant(t *testing.T) {
	target := &ent.File{ID: 40, TreePath: "10.20.30.40"}
	decision := &ActionDecision{
		Allowed: true,
		Action:  ActionUpload,
		Actions: map[Action]bool{
			ActionUpload: true,
		},
		Reason: "allowed",
	}

	constrained := constrainRemoteDecisionToVisibility(target, ActionUpload, &VisibilityResult{
		RootGrants: []RootGrant{
			{
				RootFileID:   20,
				RootTreePath: "10.20",
				Actions: map[Action]bool{
					ActionUpload: true,
				},
			},
			{
				RootFileID:   30,
				RootTreePath: "10.20.30",
				Actions: map[Action]bool{
					ActionUpload: false,
				},
			},
		},
	}, decision)

	if constrained.RootFileID != 30 {
		t.Fatalf("expected deepest root grant to win, got %d", constrained.RootFileID)
	}
	if constrained.Allowed {
		t.Fatalf("expected deepest root grant to deny upload")
	}
}

func TestConstrainRemoteDecisionToVisibilityDoesNotInheritSelfScopeToDescendants(t *testing.T) {
	target := &ent.File{ID: 21, TreePath: "10.20.21"}
	decision := &ActionDecision{
		Allowed: true,
		Action:  ActionUpload,
		Actions: map[Action]bool{
			ActionUpload: true,
		},
		Reason: "allowed",
	}

	constrained := constrainRemoteDecisionToVisibility(target, ActionUpload, &VisibilityResult{
		RootGrants: []RootGrant{
			{
				RootFileID:   20,
				RootTreePath: "10.20",
				Scope:        RootGrantScopeSelf,
				Actions: map[Action]bool{
					ActionUpload: true,
				},
			},
		},
	}, decision)

	if constrained.Allowed {
		t.Fatalf("expected self scope grant not to authorize descendants")
	}
	if constrained.Reason != "root_not_visible" {
		t.Fatalf("unexpected deny reason: %s", constrained.Reason)
	}
}

func TestConstrainRemoteDecisionToVisibilityRejectsTargetExcludedByFilter(t *testing.T) {
	target := &ent.File{ID: 40, OwnerID: 7, TreePath: "10.20.30.40"}
	decision := &ActionDecision{
		Allowed: true,
		Action:  ActionUpload,
		Actions: map[Action]bool{
			ActionList:   true,
			ActionUpload: true,
		},
		Reason: "nearest_policy_allowed",
	}

	constrained := constrainRemoteDecisionToVisibility(target, ActionUpload, &VisibilityResult{
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
	}, decision)

	if constrained == nil || constrained.Allowed {
		t.Fatalf("expected excluded target to be rejected, got %+v", constrained)
	}
	if constrained.Reason != "root_not_visible" {
		t.Fatalf("unexpected deny reason: %s", constrained.Reason)
	}
}

func TestCheckActionRemoteAllowsWritablePublicRoot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case remoteActionCheckPath:
			if got := r.Header.Get("Authorization"); got != "Bearer test-cloudreve-token" {
				t.Fatalf("unexpected auth header: %s", got)
			}
			_, _ = w.Write([]byte(`{"code":0,"msg":"","data":{"allowed":true,"action":"create","resourceFileId":1,"reason":"nearest_policy_allowed","actions":{"list":true,"create":true,"upload":true}}}`))
		case remoteVisibilityPath:
			_, _ = w.Write([]byte(`{"code":0,"msg":"","data":{"rootGrants":[{"fileId":1,"ownerId":-1,"treePath":"f1","name":"公共文件","actions":{"list":true,"create":true,"upload":true}}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service := &Service{
		settingClient: testSettingClient{values: map[string]string{
			PublicRootFileIDSetting: "1",
			oidcWellKnownSettingKey: server.URL + "/.well-known/openid-configuration",
		}},
	}

	target := &ent.File{ID: 1, OwnerID: -1, TreePath: "f1", Type: 1, Name: "公共文件"}
	decision, err := service.checkActionRemote(context.Background(), "test-cloudreve-token", target, ActionCreate)
	if err != nil {
		t.Fatalf("checkActionRemote returned error: %v", err)
	}
	if decision == nil {
		t.Fatal("expected non-nil action decision")
	}
	if !decision.Allowed {
		t.Fatalf("expected public root create to be allowed, got reason=%s actions=%v", decision.Reason, decision.Actions)
	}
	if !decision.Actions[ActionCreate] || !decision.Actions[ActionUpload] {
		t.Fatalf("expected public root write actions to be preserved, got %v", decision.Actions)
	}
}

func TestUnifiedAuthzEnabledRequiresRemoteMode(t *testing.T) {
	service := &Service{
		settingClient: testSettingClient{values: map[string]string{
			oidcEnabledSettingKey:    "1",
			oidcConfigModeSettingKey: "remote",
		}},
	}

	if !service.UnifiedAuthzEnabled(context.Background()) {
		t.Fatal("expected remote mode to enable unified authz")
	}

	service.settingClient = testSettingClient{values: map[string]string{
		oidcEnabledSettingKey:    "1",
		oidcConfigModeSettingKey: "standard",
	}}
	if service.UnifiedAuthzEnabled(context.Background()) {
		t.Fatal("expected standard mode to disable unified authz")
	}
}
