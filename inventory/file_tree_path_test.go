package inventory

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	sqlbuilder "entgo.io/ent/dialect/sql"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/file"
	entuser "github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"golang.org/x/tools/container/intsets"
)

func TestJoinFileTreePath(t *testing.T) {
	if got := joinFileTreePath("", 12); got != "f12" {
		t.Fatalf("unexpected root tree path: %q", got)
	}

	if got := joinFileTreePath("f1.f9", 12); got != "f1.f9.f12" {
		t.Fatalf("unexpected child tree path: %q", got)
	}
}

func TestTopLevelTreePathRoots(t *testing.T) {
	files := []*ent.File{
		{ID: 1, TreePath: "f1"},
		{ID: 2, TreePath: "f1.f2"},
		{ID: 3, TreePath: "f1.f2.f3"},
		{ID: 4, TreePath: "f4"},
	}

	roots := topLevelTreePathRoots(files)
	if len(roots) != 2 {
		t.Fatalf("unexpected root count: %d", len(roots))
	}

	if roots[0].ID != 1 || roots[1].ID != 4 {
		t.Fatalf("unexpected root ids: %d, %d", roots[0].ID, roots[1].ID)
	}
}

func TestTreePathVisibleSubtreeCondition(t *testing.T) {
	condition := treePathVisibleSubtreeCondition("f.tree_path", "text2ltree($1)", false, 2)
	for _, expected := range []string{
		"f.tree_path <@ text2ltree($1)",
		"symbolic_ancestor.tree_path != f.tree_path",
		"symbolic_ancestor.tree_path <@ text2ltree($1)",
		"symbolic_ancestor.tree_path @> f.tree_path",
		"f.tree_path != text2ltree($1)",
		"nlevel(f.tree_path) <= ?",
	} {
		if !strings.Contains(condition, expected) {
			t.Fatalf("condition %q does not contain %q", condition, expected)
		}
	}
}

func TestTreePathVisibleSubtreeArgs(t *testing.T) {
	args := treePathVisibleSubtreeArgs("f1.f2", false, 2)
	expected := []any{"f1.f2", "f1.f2", "f1.f2", 4}
	if !reflect.DeepEqual(args, expected) {
		t.Fatalf("unexpected args: %#v", args)
	}

	args = treePathVisibleSubtreeArgs("f1", true, -1)
	expected = []any{"f1", "f1"}
	if !reflect.DeepEqual(args, expected) {
		t.Fatalf("unexpected args for includeSelf: %#v", args)
	}
}

func TestTreePathVisibleSubtreeArgsIgnoresOverflowDepth(t *testing.T) {
	args := treePathVisibleSubtreeArgs("f1.f2", false, intsets.MaxInt)
	expected := []any{"f1.f2", "f1.f2", "f1.f2"}
	if !reflect.DeepEqual(args, expected) {
		t.Fatalf("unexpected args for overflow depth: %#v", args)
	}
}

func TestTreePathVisibleSubtreeConditionIgnoresOverflowDepth(t *testing.T) {
	condition := treePathVisibleSubtreeCondition("f.tree_path", "text2ltree($1)", false, intsets.MaxInt)
	if strings.Contains(condition, "nlevel(f.tree_path) <= ?") {
		t.Fatalf("overflow depth should not append nlevel condition: %q", condition)
	}
}

func TestIndexableTreePathCondition(t *testing.T) {
	condition := indexableTreePathCondition("f.tree_path")
	for _, expected := range []string{
		"FROM files AS active_root",
		"active_root.name = ?",
		"active_root.tree_path IS NOT NULL",
		"active_root.tree_path @> f.tree_path",
	} {
		if !strings.Contains(condition, expected) {
			t.Fatalf("condition %q does not contain %q", condition, expected)
		}
	}
}

func TestIndexableTreePathPredicateQuery(t *testing.T) {
	builder := sqlbuilder.Dialect(dialect.Postgres)
	table := builder.Table(file.Table)
	selector := builder.Select(table.C(file.FieldID)).From(table)
	indexableTreePathPredicate()(selector)

	query, args := selector.Query()
	for _, expected := range []string{
		`WHERE EXISTS (SELECT 1 FROM files AS active_root`,
		`active_root.name = $1`,
		`active_root.tree_path @> "files"."tree_path"`,
	} {
		if !strings.Contains(query, expected) {
			t.Fatalf("query %q does not contain %q", query, expected)
		}
	}
	if !reflect.DeepEqual(args, []any{RootFolderName}) {
		t.Fatalf("unexpected args: %#v", args)
	}
}

func TestSQLiteCreateFolderMaintainsTreePath(t *testing.T) {
	ctx := context.Background()
	client, err := ent.Open("sqlite3", "file:sqlite-tree-path-create?mode=memory&cache=shared&_fk=1")
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
		SetUsername("tree-owner").
		SetEmail("tree-owner@example.com").
		SetNick("tree-owner").
		SetStatus(entuser.StatusActive).
		SetGroupID(group.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create owner: %v", err)
	}

	fileClient := NewFileClient(client, conf.SQLiteDB, nil)
	root, err := fileClient.CreateFolder(ctx, nil, &CreateFolderParameters{
		Owner: owner.ID,
		Name:  "root",
	})
	if err != nil {
		t.Fatalf("failed to create root: %v", err)
	}

	child, err := fileClient.CreateFolder(ctx, root, &CreateFolderParameters{
		Owner: owner.ID,
		Name:  "child",
	})
	if err != nil {
		t.Fatalf("failed to create child: %v", err)
	}

	grandChild, err := fileClient.CreateFolder(ctx, child, &CreateFolderParameters{
		Owner: owner.ID,
		Name:  "grand-child",
	})
	if err != nil {
		t.Fatalf("failed to create grand child: %v", err)
	}

	wantRootPath := joinFileTreePath("", root.ID)
	wantChildPath := joinFileTreePath(wantRootPath, child.ID)
	wantGrandChildPath := joinFileTreePath(wantChildPath, grandChild.ID)
	if root.TreePath != wantRootPath {
		t.Fatalf("unexpected root tree path: got %q want %q", root.TreePath, wantRootPath)
	}
	if child.TreePath != wantChildPath {
		t.Fatalf("unexpected child tree path: got %q want %q", child.TreePath, wantChildPath)
	}
	if grandChild.TreePath != wantGrandChildPath {
		t.Fatalf("unexpected grand child tree path: got %q want %q", grandChild.TreePath, wantGrandChildPath)
	}
}

func TestSQLiteRelocateTreePathSubtree(t *testing.T) {
	ctx := context.Background()
	client, err := ent.Open("sqlite3", "file:sqlite-tree-path-relocate?mode=memory&cache=shared&_fk=1")
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
		SetUsername("move-owner").
		SetEmail("move-owner@example.com").
		SetNick("move-owner").
		SetStatus(entuser.StatusActive).
		SetGroupID(group.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create owner: %v", err)
	}

	fileClient := NewFileClient(client, conf.SQLiteDB, nil)
	srcRoot, err := fileClient.CreateFolder(ctx, nil, &CreateFolderParameters{
		Owner: owner.ID,
		Name:  "src-root",
	})
	if err != nil {
		t.Fatalf("failed to create src root: %v", err)
	}
	dstRoot, err := fileClient.CreateFolder(ctx, nil, &CreateFolderParameters{
		Owner: owner.ID,
		Name:  "dst-root",
	})
	if err != nil {
		t.Fatalf("failed to create dst root: %v", err)
	}
	child, err := fileClient.CreateFolder(ctx, srcRoot, &CreateFolderParameters{
		Owner: owner.ID,
		Name:  "child",
	})
	if err != nil {
		t.Fatalf("failed to create child: %v", err)
	}
	grandChild, err := fileClient.CreateFolder(ctx, child, &CreateFolderParameters{
		Owner: owner.ID,
		Name:  "grand-child",
	})
	if err != nil {
		t.Fatalf("failed to create grand child: %v", err)
	}

	if err := fileClient.SetParent(ctx, []*ent.File{child}, dstRoot); err != nil {
		t.Fatalf("failed to relocate child: %v", err)
	}

	reloadedChild, err := fileClient.GetByID(ctx, child.ID)
	if err != nil {
		t.Fatalf("failed to reload child: %v", err)
	}
	reloadedGrandChild, err := fileClient.GetByID(ctx, grandChild.ID)
	if err != nil {
		t.Fatalf("failed to reload grand child: %v", err)
	}

	wantChildPath := joinFileTreePath(dstRoot.TreePath, child.ID)
	wantGrandChildPath := joinFileTreePath(wantChildPath, grandChild.ID)
	if reloadedChild.TreePath != wantChildPath {
		t.Fatalf("unexpected relocated child tree path: got %q want %q", reloadedChild.TreePath, wantChildPath)
	}
	if reloadedGrandChild.TreePath != wantGrandChildPath {
		t.Fatalf("unexpected relocated grand child tree path: got %q want %q", reloadedGrandChild.TreePath, wantGrandChildPath)
	}
}

func TestEnsureFileTreePathSupportBackfillsSQLiteData(t *testing.T) {
	ctx := context.Background()
	client, err := ent.Open("sqlite3", "file:sqlite-tree-path-backfill?mode=memory&cache=shared&_fk=1")
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
		SetUsername("backfill-owner").
		SetEmail("backfill-owner@example.com").
		SetNick("backfill-owner").
		SetStatus(entuser.StatusActive).
		SetGroupID(group.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create owner: %v", err)
	}

	root, err := client.File.Create().
		SetOwnerID(owner.ID).
		SetType(1).
		SetName("root").
		SetFileExt("").
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create root: %v", err)
	}
	child, err := client.File.Create().
		SetOwnerID(owner.ID).
		SetType(1).
		SetName("child").
		SetFileExt("").
		SetParentID(root.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create child: %v", err)
	}

	if err := ensureFileTreePathSupport(ctx, logging.NewConsoleLogger(logging.LevelError), client, conf.SQLiteDB); err != nil {
		t.Fatalf("failed to ensure sqlite tree path support: %v", err)
	}

	reloadedRoot, err := client.File.Get(ctx, root.ID)
	if err != nil {
		t.Fatalf("failed to reload root: %v", err)
	}
	reloadedChild, err := client.File.Get(ctx, child.ID)
	if err != nil {
		t.Fatalf("failed to reload child: %v", err)
	}

	wantRootPath := joinFileTreePath("", root.ID)
	wantChildPath := joinFileTreePath(wantRootPath, child.ID)
	if reloadedRoot.TreePath != wantRootPath {
		t.Fatalf("unexpected backfilled root tree path: got %q want %q", reloadedRoot.TreePath, wantRootPath)
	}
	if reloadedChild.TreePath != wantChildPath {
		t.Fatalf("unexpected backfilled child tree path: got %q want %q", reloadedChild.TreePath, wantChildPath)
	}
}

func TestSQLiteTreePathQueriesDoNotRequirePostgres(t *testing.T) {
	ctx := context.Background()
	client, err := ent.Open("sqlite3", "file:sqlite-tree-path-query-fallback?mode=memory&cache=shared&_fk=1")
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
		SetUsername("query-owner").
		SetEmail("query-owner@example.com").
		SetNick("query-owner").
		SetStatus(entuser.StatusActive).
		SetGroupID(group.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create owner: %v", err)
	}

	fileClient := NewFileClient(client, conf.SQLiteDB, nil)
	root, err := fileClient.CreateFolder(ctx, nil, &CreateFolderParameters{Owner: owner.ID, Name: "root"})
	if err != nil {
		t.Fatalf("failed to create root: %v", err)
	}
	child, err := fileClient.CreateFolder(ctx, root, &CreateFolderParameters{Owner: owner.ID, Name: "child"})
	if err != nil {
		t.Fatalf("failed to create child: %v", err)
	}
	grandChild, err := fileClient.CreateFolder(ctx, child, &CreateFolderParameters{Owner: owner.ID, Name: "grand-child"})
	if err != nil {
		t.Fatalf("failed to create grand child: %v", err)
	}

	ancestors, err := fileClient.GetAncestorFiles(ctx, grandChild)
	if err != nil {
		t.Fatalf("failed to query ancestors in sqlite: %v", err)
	}
	if len(ancestors) != 3 || ancestors[0].ID != root.ID || ancestors[1].ID != child.ID || ancestors[2].ID != grandChild.ID {
		t.Fatalf("unexpected sqlite ancestors: %#v", ancestors)
	}

	subtree, err := fileClient.GetSubtreeFiles(ctx, root, -1, 10)
	if err != nil {
		t.Fatalf("failed to query subtree in sqlite: %v", err)
	}
	if len(subtree) != 2 || subtree[0].ID != child.ID || subtree[1].ID != grandChild.ID {
		t.Fatalf("unexpected sqlite subtree order: %#v", subtree)
	}

	summary, err := fileClient.SummarizeSubtree(ctx, root, 10)
	if err != nil {
		t.Fatalf("failed to summarize subtree in sqlite: %v", err)
	}
	if summary.Folders != 2 || !summary.Completed {
		t.Fatalf("unexpected sqlite subtree summary: %#v", summary)
	}
}
