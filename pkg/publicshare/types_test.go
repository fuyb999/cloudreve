package publicshare

import (
	"context"
	"reflect"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	entfile "github.com/cloudreve/Cloudreve/v4/ent/file"
	entuser "github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
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
		{RootFileID: 11, RootOwnerID: 10, RootTreePath: "1.2", Scope: RootGrantScopeSubtree},
		{RootFileID: 12, RootOwnerID: 10, RootTreePath: "1.3", Scope: RootGrantScopeSubtree},
		{RootFileID: 13, RootOwnerID: 20, RootTreePath: "2.5", Scope: RootGrantScopeSubtree},
	}

	filter := BuildVisibilityFilter(grants)
	if filter == nil || filter.Match == nil || filter.Match.Kind != FileFilterMatchTreePathIn {
		t.Fatalf("unexpected filter: %#v", filter)
	}

	expectedES := map[string]any{
		"bool": map[string]any{
			"minimum_should_match": 1,
			"should": []any{
				map[string]any{"term": map[string]any{"tree_path": "1.2"}},
				map[string]any{"prefix": map[string]any{"tree_path": "1.2."}},
				map[string]any{"term": map[string]any{"tree_path": "1.3"}},
				map[string]any{"prefix": map[string]any{"tree_path": "1.3."}},
				map[string]any{"term": map[string]any{"tree_path": "2.5"}},
				map[string]any{"prefix": map[string]any{"tree_path": "2.5."}},
			},
		},
	}

	if got := ToElasticsearchFilter(filter); !reflect.DeepEqual(got, expectedES) {
		t.Fatalf("unexpected elasticsearch filter: %#v", got)
	}

	expectedMeili := `((tree_path = "1.2" OR tree_path STARTS WITH "1.2.") OR (tree_path = "1.3" OR tree_path STARTS WITH "1.3.") OR (tree_path = "2.5" OR tree_path STARTS WITH "2.5."))`
	if got := ToMeilisearchFilter(filter); got != expectedMeili {
		t.Fatalf("unexpected meilisearch filter: %s", got)
	}
}

func TestBuildVisibilityFilterIncludesSelfScopeFileIDs(t *testing.T) {
	filter := BuildVisibilityFilter([]RootGrant{
		{RootFileID: 20, RootOwnerID: 7, RootTreePath: "10.20", Scope: RootGrantScopeSelf},
		{RootFileID: 30, RootOwnerID: 7, RootTreePath: "10.30", Scope: RootGrantScopeSubtree},
	})

	if filter == nil || filter.Operator != FileFilterOpOr || len(filter.Children) != 2 {
		t.Fatalf("expected mixed self/subtree visibility filter, got %#v", filter)
	}

	targetSelf := &ent.File{ID: 20, TreePath: "10.20"}
	targetSelfChild := &ent.File{ID: 21, TreePath: "10.20.21"}
	targetSubtreeChild := &ent.File{ID: 31, TreePath: "10.30.31"}

	if !MatchFileFilter(filter, targetSelf) {
		t.Fatal("expected self scope root to stay visible")
	}
	if MatchFileFilter(filter, targetSelfChild) {
		t.Fatal("expected self scope child to stay hidden")
	}
	if !MatchFileFilter(filter, targetSubtreeChild) {
		t.Fatal("expected subtree child to stay visible")
	}
}

func TestToEntPredicateTreePathInSupportsSQLite(t *testing.T) {
	ctx := context.Background()
	client, err := ent.Open("sqlite3", "file:publicshare-tree-path-filter?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}
	defer client.Close()

	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	group, err := client.Group.Create().SetName("User").SetPermissions(&boolset.BooleanSet{}).Save(ctx)
	if err != nil {
		t.Fatalf("failed to create group: %v", err)
	}

	owner, err := client.User.Create().
		SetUsername("sqlite-owner").
		SetEmail("sqlite-owner@example.com").
		SetNick("sqlite-owner").
		SetStatus(entuser.StatusActive).
		SetGroupID(group.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create owner: %v", err)
	}

	createFile := func(name string, treePath string) *ent.File {
		fi, createErr := client.File.Create().
			SetName(name).
			SetFileExt("").
			SetType(int(types.FileTypeFolder)).
			SetOwnerID(owner.ID).
			SetTreePath(treePath).
			Save(ctx)
		if createErr != nil {
			t.Fatalf("failed to create file %s: %v", name, createErr)
		}
		return fi
	}

	createFile("exact", "1.2")
	createFile("descendant", "1.2.3")
	createFile("prefix-trap", "1.20")
	createFile("other", "2.1")

	filter := &FileFilterExpr{
		Match: &FileFilterMatch{
			Kind:         FileFilterMatchTreePathIn,
			StringValues: []string{"1.2"},
		},
	}

	files, err := client.File.Query().
		Where(ToEntPredicate(filter)).
		Order(entfile.ByTreePath()).
		All(ctx)
	if err != nil {
		t.Fatalf("failed to query filtered files: %v", err)
	}

	got := make([]string, 0, len(files))
	for _, fi := range files {
		got = append(got, fi.TreePath)
	}

	expected := []string{"1.2", "1.2.3"}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("unexpected tree path matches: got %v want %v", got, expected)
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

func TestRootGrantForAncestorsDoesNotMatchSelfScopeDescendant(t *testing.T) {
	target := &ent.File{ID: 99, TreePath: "10.20.30"}
	grants := []RootGrant{
		{RootFileID: 20, RootTreePath: "10.20", Scope: RootGrantScopeSelf},
	}

	if _, ok := rootGrantForAncestors(target, grants); ok {
		t.Fatal("expected self scope root grant not to match descendant")
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

func TestRootGrantCoversFileRespectsSelfScope(t *testing.T) {
	grant := RootGrant{
		RootFileID:   20,
		RootTreePath: "10.20",
		Scope:        RootGrantScopeSelf,
	}

	if !RootGrantCoversFile(grant, 20, "10.20") {
		t.Fatal("expected self scope grant to cover itself")
	}
	if RootGrantCoversFile(grant, 21, "10.20.21") {
		t.Fatal("expected self scope grant not to cover descendants")
	}
}

func TestRootGrantCoversGrantRespectsSelfScope(t *testing.T) {
	parent := RootGrant{
		RootFileID:   20,
		RootTreePath: "10.20",
		Scope:        RootGrantScopeSelf,
	}
	child := RootGrant{
		RootFileID:   21,
		RootTreePath: "10.20.21",
		Scope:        RootGrantScopeSubtree,
	}

	if RootGrantCoversGrant(parent, child) {
		t.Fatal("expected self scope grant not to cover child grants")
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

func TestMatchFileFilter(t *testing.T) {
	target := &ent.File{ID: 40, OwnerID: 7, TreePath: "10.20.30.40"}

	filter := &FileFilterExpr{
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
	}

	if MatchFileFilter(filter, target) {
		t.Fatalf("expected target to be excluded by filter")
	}
	if !MatchFileFilter(&FileFilterExpr{
		Match: &FileFilterMatch{
			Kind:         FileFilterMatchTreePathIn,
			StringValues: []string{"10.20"},
		},
	}, target) {
		t.Fatalf("expected target to match parent subtree filter")
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
