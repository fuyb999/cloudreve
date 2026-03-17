package publicshare

import (
	"reflect"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
)

func TestEvaluatePrincipalExprNested(t *testing.T) {
	state := DefaultMockState()
	state.Profiles = []UserProfile{
		{
			UserID:       7,
			DepartmentID: "rd-backend",
			GroupIDs:     []string{"project-a"},
		},
	}

	user := &ent.User{
		ID:    7,
		Email: "alice@example.com",
	}
	profile := state.ProfileForUser(user)
	index := state.Index()

	expr := &PrincipalExpr{
		Operator: PrincipalOpAnd,
		Children: []*PrincipalExpr{
			{
				Operator: PrincipalOpOr,
				Children: []*PrincipalExpr{
					{
						Match: &PrincipalMatch{
							Kind:   PrincipalMatchUser,
							Values: []string{"alice@example.com"},
						},
					},
					{
						Match: &PrincipalMatch{
							Kind:   PrincipalMatchGroup,
							Values: []string{"project-b"},
						},
					},
				},
			},
			{
				Match: &PrincipalMatch{
					Kind:   PrincipalMatchDepartmentDescendant,
					Values: []string{"rd"},
				},
			},
			{
				Operator: PrincipalOpNot,
				Children: []*PrincipalExpr{
					{
						Match: &PrincipalMatch{
							Kind:   PrincipalMatchGroup,
							Values: []string{"project-b"},
						},
					},
				},
			},
		},
	}

	if !EvaluatePrincipalExpr(expr, user, "", profile, index) {
		t.Fatalf("expected nested principal expression to match")
	}
}

func TestBuildVisibilityFilterConversions(t *testing.T) {
	grants := []RootGrant{
		{RootFileID: 11, RootOwnerID: 10, RootTreePath: "1.2"},
		{RootFileID: 12, RootOwnerID: 10, RootTreePath: "1.3"},
		{RootFileID: 13, RootOwnerID: 20, RootTreePath: "2.5"},
	}

	filter := BuildVisibilityFilter(grants)
	if filter == nil || filter.Operator != FileFilterOpOr || len(filter.Children) != 2 {
		t.Fatalf("unexpected filter: %#v", filter)
	}

	expectedES := map[string]any{
		"bool": map[string]any{
			"minimum_should_match": 1,
			"should": []any{
				map[string]any{
					"bool": map[string]any{
						"filter": []any{
							map[string]any{"terms": map[string]any{"owner_id": []int{10}}},
							map[string]any{
								"bool": map[string]any{
									"minimum_should_match": 1,
									"should": []any{
										map[string]any{"term": map[string]any{"tree_path": "1.2"}},
										map[string]any{"prefix": map[string]any{"tree_path": "1.2."}},
										map[string]any{"term": map[string]any{"tree_path": "1.3"}},
										map[string]any{"prefix": map[string]any{"tree_path": "1.3."}},
									},
								},
							},
						},
					},
				},
				map[string]any{
					"bool": map[string]any{
						"filter": []any{
							map[string]any{"terms": map[string]any{"owner_id": []int{20}}},
							map[string]any{
								"bool": map[string]any{
									"minimum_should_match": 1,
									"should": []any{
										map[string]any{"term": map[string]any{"tree_path": "2.5"}},
										map[string]any{"prefix": map[string]any{"tree_path": "2.5."}},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	if got := ToElasticsearchFilter(filter); !reflect.DeepEqual(got, expectedES) {
		t.Fatalf("unexpected elasticsearch filter: %#v", got)
	}

	expectedMeili := `((owner_id IN [10] AND ((tree_path = "1.2" OR tree_path STARTS WITH "1.2.") OR (tree_path = "1.3" OR tree_path STARTS WITH "1.3."))) OR (owner_id IN [20] AND (tree_path = "2.5" OR tree_path STARTS WITH "2.5.")))`
	if got := ToMeilisearchFilter(filter); got != expectedMeili {
		t.Fatalf("unexpected meilisearch filter: %s", got)
	}
}

func TestRootGrantForAncestors(t *testing.T) {
	target := &ent.File{ID: 99, TreePath: "10.20.30"}
	grants := []RootGrant{
		{RootFileID: 20, RootTreePath: "10.20"},
	}

	grant, ok := rootGrantForAncestors(target, grants)
	if !ok {
		t.Fatalf("expected matching root grant")
	}

	if grant.RootFileID != 20 {
		t.Fatalf("unexpected root grant: %#v", grant)
	}
}

func TestRootGrantWithinTree(t *testing.T) {
	grant := RootGrant{RootFileID: 20, RootTreePath: "10.20.30"}
	if !RootGrantWithinTree("10.20", grant) {
		t.Fatalf("expected grant to be within root tree")
	}
	if RootGrantWithinTree("10.21", grant) {
		t.Fatalf("unexpected root tree match")
	}
}

func TestProjectedRootAlias(t *testing.T) {
	encoder, err := hashid.New("test-salt")
	if err != nil {
		t.Fatalf("failed to create hashid encoder: %v", err)
	}

	alias := ProjectedRootAlias(encoder, RootGrant{
		RootFileID: 42,
		RootName:   "研发文档",
	})
	if alias == "" {
		t.Fatalf("projected alias should not be empty")
	}
	if alias == "研发文档" {
		t.Fatalf("projected alias should include stable suffix, got %q", alias)
	}
}

func TestRuleExprForFallbackActions(t *testing.T) {
	visibility := &PrincipalExpr{
		Match: &PrincipalMatch{Kind: PrincipalMatchAnyone},
	}
	downloadExpr := &PrincipalExpr{
		Match: &PrincipalMatch{Kind: PrincipalMatchUser, Values: []string{"alice@example.com"}},
	}

	rule := &Rule{
		Visibility: visibility,
		Actions: map[Action]*PrincipalExpr{
			ActionDownload: downloadExpr,
		},
	}

	if got := rule.ExprFor(ActionDirectLink); got != downloadExpr {
		t.Fatalf("direct link should fallback to download expr, got %#v", got)
	}
	if got := rule.ExprFor(ActionArchive); got != downloadExpr {
		t.Fatalf("archive should fallback to download expr, got %#v", got)
	}
	if got := rule.ExprFor(ActionDeleteRoot); got != nil {
		t.Fatalf("delete_root should require delete rule before fallback, got %#v", got)
	}

	deleteExpr := &PrincipalExpr{
		Match: &PrincipalMatch{Kind: PrincipalMatchUser, Values: []string{"42"}},
	}
	rule.Actions = map[Action]*PrincipalExpr{
		ActionDelete: deleteExpr,
	}
	if got := rule.ExprFor(ActionDeleteRoot); got != deleteExpr {
		t.Fatalf("delete_root should fallback to delete expr, got %#v", got)
	}

	rule.Actions = map[Action]*PrincipalExpr{}
	if got := rule.ExprFor(ActionDirectLink); got != visibility {
		t.Fatalf("direct link should fallback to visibility when download expr missing, got %#v", got)
	}
	if got := rule.ExprFor(ActionArchive); got != visibility {
		t.Fatalf("archive should fallback to visibility when download expr missing, got %#v", got)
	}
	if got := rule.ExprFor(ActionCopy); got != nil {
		t.Fatalf("copy should require explicit grant, got %#v", got)
	}
}

func TestRootGrantActionAllowed(t *testing.T) {
	grant := RootGrant{
		RootFileID: 100,
		Actions: map[Action]bool{
			ActionDelete:     true,
			ActionDeleteRoot: false,
		},
	}

	if RootGrantActionAllowed(100, grant, ActionDelete) {
		t.Fatalf("root delete should honor delete_root override")
	}

	if !RootGrantActionAllowed(101, grant, ActionDelete) {
		t.Fatalf("subtree delete should keep normal delete permission")
	}

	delete(grant.Actions, ActionDeleteRoot)
	if !RootGrantActionAllowed(100, grant, ActionDelete) {
		t.Fatalf("root delete should fallback to delete when delete_root is missing")
	}
}
