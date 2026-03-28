package publicshare

import (
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
)

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

func TestVirtualPublicRootDecisionIsReadonly(t *testing.T) {
	target := &ent.File{ID: 1}

	decision := virtualPublicRootDecision(target, ActionCreate)
	if decision.Allowed {
		t.Fatalf("expected virtual public root create to be denied")
	}
	if !decision.Actions[ActionList] {
		t.Fatalf("expected virtual public root to remain listable")
	}
	if decision.Actions[ActionCreate] || decision.Actions[ActionUpload] {
		t.Fatalf("expected virtual public root write actions to be disabled")
	}
}
