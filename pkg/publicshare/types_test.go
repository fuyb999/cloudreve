package publicshare

import (
	"reflect"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
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
