package inventory

import (
	"reflect"
	"strings"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
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
