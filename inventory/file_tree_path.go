package inventory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/file"
	"github.com/cloudreve/Cloudreve/v4/ent/predicate"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/samber/lo"
)

const fileTreePathIndexName = "files_tree_path_gist"
const maxPostgresInteger = int(^uint32(0) >> 1)

var ErrTreePathQueryUnavailable = errors.New("tree path query is unavailable")

type treePathSearchToken struct {
	Depth     int        `json:"depth"`
	ID        int        `json:"-"`
	IDHash    string     `json:"id,omitempty"`
	Name      string     `json:"name,omitempty"`
	Size      int64      `json:"size,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

func fileTreeLabel(id int) string {
	return fmt.Sprintf("f%d", id)
}

func joinFileTreePath(parentPath string, id int) string {
	label := fileTreeLabel(id)
	if strings.TrimSpace(parentPath) == "" {
		return label
	}

	return parentPath + "." + label
}

func fileTreePathDepth(path string) int {
	path = strings.TrimSpace(path)
	if path == "" {
		return 0
	}

	return strings.Count(path, ".") + 1
}

func fileTreePathHasPrefix(path string, prefix string) bool {
	path = strings.TrimSpace(path)
	prefix = strings.TrimSpace(prefix)
	if path == "" || prefix == "" {
		return false
	}

	return path == prefix || strings.HasPrefix(path, prefix+".")
}

func ensurePostgresLtree(ctx context.Context, client *ent.Client, dbType conf.DBType) error {
	if dbType != conf.PostgresDB {
		return nil
	}

	if _, err := client.File.ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS ltree"); err != nil {
		return fmt.Errorf("failed to ensure postgres ltree extension: %w", err)
	}

	return nil
}

func ensureFileTreePathSupport(ctx context.Context, l logging.Logger, client *ent.Client, dbType conf.DBType) error {
	if dbType != conf.PostgresDB {
		return nil
	}

	if _, err := client.File.ExecContext(ctx,
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON files USING GIST (tree_path) WHERE tree_path IS NOT NULL", fileTreePathIndexName)); err != nil {
		return fmt.Errorf("failed to ensure tree path index: %w", err)
	}

	missing, err := client.File.Query().
		Where(file.Or(file.TreePathEQ(""), file.TreePathIsNil())).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("failed to inspect tree path state: %w", err)
	}

	if !missing {
		return nil
	}

	l.Info("Backfilling file tree paths...")
	if _, err := client.File.ExecContext(ctx, `
WITH RECURSIVE tree AS (
    SELECT id, file_children, text2ltree('f' || id::text) AS tree_path
    FROM files
    WHERE file_children IS NULL
  UNION ALL
    SELECT f.id, f.file_children, tree.tree_path || text2ltree('f' || f.id::text)
    FROM files f
    JOIN tree ON f.file_children = tree.id
)
UPDATE files AS target
SET tree_path = tree.tree_path
FROM tree
WHERE target.id = tree.id
  AND target.tree_path IS DISTINCT FROM tree.tree_path
`); err != nil {
		return fmt.Errorf("failed to backfill file tree paths: %w", err)
	}

	return nil
}

func (f *fileClient) updateFileTreePath(ctx context.Context, target *ent.File, treePath string) error {
	if f.dbType != conf.PostgresDB || target == nil {
		return nil
	}

	treePath = strings.TrimSpace(treePath)
	if treePath == "" || target.TreePath == treePath {
		target.TreePath = treePath
		return nil
	}

	if err := f.rebuildTreePathSubtree(ctx, target, treePath); err != nil {
		return err
	}

	target.TreePath = treePath
	return nil
}

func (f *fileClient) rebuildTreePathSubtree(ctx context.Context, root *ent.File, newPrefix string) error {
	if f.dbType != conf.PostgresDB || root == nil {
		return nil
	}

	if _, err := f.client.File.ExecContext(ctx, `
WITH RECURSIVE tree AS (
    SELECT id, $2::ltree AS tree_path
    FROM files
    WHERE id = $1
  UNION ALL
    SELECT child.id, tree.tree_path || text2ltree('f' || child.id::text)
    FROM files AS child
    JOIN tree ON child.file_children = tree.id
)
UPDATE files AS target
SET tree_path = tree.tree_path
FROM tree
WHERE target.id = tree.id
  AND target.tree_path IS DISTINCT FROM tree.tree_path
`, root.ID, newPrefix); err != nil {
		return fmt.Errorf("failed to rebuild file %d tree path subtree: %w", root.ID, err)
	}

	root.TreePath = newPrefix
	return nil
}

func (f *fileClient) relocateTreePathSubtree(ctx context.Context, root *ent.File, parent *ent.File) error {
	if f.dbType != conf.PostgresDB || root == nil {
		return nil
	}

	oldPrefix := strings.TrimSpace(root.TreePath)
	newPrefix := joinFileTreePath("", root.ID)
	if parent != nil {
		newPrefix = joinFileTreePath(parent.TreePath, root.ID)
	}

	if oldPrefix == "" {
		return f.rebuildTreePathSubtree(ctx, root, newPrefix)
	}

	if oldPrefix == newPrefix {
		root.TreePath = newPrefix
		return nil
	}

	if _, err := f.client.File.ExecContext(ctx, `
UPDATE files
SET tree_path = CASE
    WHEN nlevel(tree_path) = nlevel($1::ltree) THEN $2::ltree
    ELSE $2::ltree || subpath(tree_path, nlevel($1::ltree))
END
WHERE tree_path <@ $1::ltree
`, oldPrefix, newPrefix); err != nil {
		return fmt.Errorf("failed to relocate file tree path subtree: %w", err)
	}

	root.TreePath = newPrefix
	return nil
}

func treePathOrderByDepthAndPath() file.OrderOption {
	return func(s *sql.Selector) {
		s.OrderExpr(sql.Expr("nlevel(tree_path)"))
		s.OrderBy(file.FieldTreePath)
		s.OrderBy(file.FieldID)
	}
}

func treePathSearchOrder(args *ListFileParameters) []file.OrderOption {
	orderTerm := getOrderTerm(args.Order)
	order := []file.OrderOption{
		func(s *sql.Selector) {
			s.OrderExpr(sql.Expr("nlevel(tree_path)"))
		},
	}

	switch args.OrderBy {
	case file.FieldName:
		order = append(order, file.ByName(orderTerm))
	case file.FieldSize:
		order = append(order, file.BySize(orderTerm))
	case file.FieldUpdatedAt:
		order = append(order, file.ByUpdatedAt(orderTerm))
	default:
	}

	order = append(order, file.ByID(orderTerm))
	return order
}

func treePathVisibleSubtreeCondition(pathColumn, rootExpr string, includeSelf bool, maxDepth int) string {
	conditions := []string{
		fmt.Sprintf("%s <@ %s", pathColumn, rootExpr),
		fmt.Sprintf(`NOT EXISTS (
    SELECT 1
    FROM files AS symbolic_ancestor
    WHERE symbolic_ancestor.is_symbolic
      AND symbolic_ancestor.tree_path IS NOT NULL
      AND symbolic_ancestor.tree_path != %s
      AND symbolic_ancestor.tree_path <@ %s
      AND symbolic_ancestor.tree_path @> %s
)`, pathColumn, rootExpr, pathColumn),
	}

	if !includeSelf {
		conditions = append(conditions, fmt.Sprintf("%s != %s", pathColumn, rootExpr))
	}

	if _, ok := treePathVisibleSubtreeMaxLevel("", maxDepth); ok {
		conditions = append(conditions, fmt.Sprintf("nlevel(%s) <= ?", pathColumn))
	}

	return strings.Join(conditions, " AND ")
}

func treePathVisibleSubtreeArgs(prefix string, includeSelf bool, maxDepth int) []any {
	args := []any{prefix, prefix}
	if !includeSelf {
		args = append(args, prefix)
	}
	if maxLevel, ok := treePathVisibleSubtreeMaxLevel(prefix, maxDepth); ok {
		args = append(args, maxLevel)
	}
	return args
}

func treePathVisibleSubtreePredicate(prefix string, includeSelf bool, maxDepth int) predicate.File {
	return func(s *sql.Selector) {
		pathColumn := s.C(file.FieldTreePath)
		s.Where(sql.P(func(b *sql.Builder) {
			b.WriteString(pathColumn).WriteString(" <@ text2ltree(").Arg(prefix).WriteByte(')')
			b.WriteString(" AND NOT EXISTS (SELECT 1 FROM ").WriteString(file.Table).WriteString(" AS symbolic_ancestor WHERE ")
			b.WriteString("symbolic_ancestor.").WriteString(file.FieldIsSymbolic).WriteString(" AND ")
			b.WriteString("symbolic_ancestor.").WriteString(file.FieldTreePath).WriteString(" IS NOT NULL AND ")
			b.WriteString("symbolic_ancestor.").WriteString(file.FieldTreePath).WriteString(" != ").WriteString(pathColumn).WriteString(" AND ")
			b.WriteString("symbolic_ancestor.").WriteString(file.FieldTreePath).WriteString(" <@ text2ltree(").Arg(prefix).WriteByte(')').WriteString(" AND ")
			b.WriteString("symbolic_ancestor.").WriteString(file.FieldTreePath).WriteString(" @> ").WriteString(pathColumn)
			b.WriteByte(')')
			if !includeSelf {
				b.WriteString(" AND ").WriteString(pathColumn).WriteString(" != text2ltree(").Arg(prefix).WriteByte(')')
			}
			if maxLevel, ok := treePathVisibleSubtreeMaxLevel(prefix, maxDepth); ok {
				b.WriteString(" AND nlevel(").WriteString(pathColumn).WriteString(") <= ").Arg(maxLevel)
			}
		}))
	}
}

func treePathVisibleSubtreeMaxLevel(prefix string, maxDepth int) (int, bool) {
	if maxDepth < 0 {
		return 0, false
	}

	rootDepth := fileTreePathDepth(prefix)
	// PostgreSQL 的 nlevel() 返回 integer，超出 int32 上限时直接退化为无限深度查询，
	// 避免“无限遍历”哨兵值在这里发生整型溢出。
	if maxDepth > maxPostgresInteger-rootDepth {
		return 0, false
	}

	return rootDepth + maxDepth, true
}

func treePathSearchTokenFromString(s string, hasher hashid.Encoder) (*treePathSearchToken, error) {
	sB64Decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64 for tree path search token: %w", err)
	}

	token := &treePathSearchToken{}
	if err := json.Unmarshal(sB64Decoded, token); err != nil {
		return nil, fmt.Errorf("failed to unmarshal tree path search token: %w", err)
	}

	id, err := hasher.Decode(token.IDHash, hashid.FileID)
	if err != nil {
		return nil, fmt.Errorf("failed to decode tree path search token id: %w", err)
	}

	token.ID = id
	return token, nil
}

func (p *treePathSearchToken) Encode(hasher hashid.Encoder) (string, error) {
	p.IDHash = hashid.EncodeFileID(hasher, p.ID)
	res, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("failed to marshal tree path search token: %w", err)
	}

	return base64.StdEncoding.EncodeToString(res), nil
}

func treePathSearchCursorPredicate(token *treePathSearchToken, args *ListFileParameters) predicate.File {
	return func(s *sql.Selector) {
		desc := args.Order == OrderDirectionDesc
		var expr string
		var exprArgs []any
		switch args.OrderBy {
		case file.FieldName:
			if desc {
				expr = "(nlevel(tree_path) > ? OR (nlevel(tree_path) = ? AND (name < ? OR (name = ? AND id < ?))))"
			} else {
				expr = "(nlevel(tree_path) > ? OR (nlevel(tree_path) = ? AND (name > ? OR (name = ? AND id > ?))))"
			}
			exprArgs = []any{token.Depth, token.Depth, token.Name, token.Name, token.ID}
		case file.FieldSize:
			if desc {
				expr = "(nlevel(tree_path) > ? OR (nlevel(tree_path) = ? AND (size < ? OR (size = ? AND id < ?))))"
			} else {
				expr = "(nlevel(tree_path) > ? OR (nlevel(tree_path) = ? AND (size > ? OR (size = ? AND id > ?))))"
			}
			exprArgs = []any{token.Depth, token.Depth, token.Size, token.Size, token.ID}
		case file.FieldUpdatedAt:
			if desc {
				expr = "(nlevel(tree_path) > ? OR (nlevel(tree_path) = ? AND (updated_at < ? OR (updated_at = ? AND id < ?))))"
			} else {
				expr = "(nlevel(tree_path) > ? OR (nlevel(tree_path) = ? AND (updated_at > ? OR (updated_at = ? AND id > ?))))"
			}
			exprArgs = []any{token.Depth, token.Depth, token.UpdatedAt, token.UpdatedAt, token.ID}
		default:
			if desc {
				expr = "(nlevel(tree_path) > ? OR (nlevel(tree_path) = ? AND id < ?))"
			} else {
				expr = "(nlevel(tree_path) > ? OR (nlevel(tree_path) = ? AND id > ?))"
			}
			exprArgs = []any{token.Depth, token.Depth, token.ID}
		}

		s.Where(sql.ExprP(expr, exprArgs...))
	}
}

func getTreePathSearchNextToken(hasher hashid.Encoder, last *ent.File, args *ListFileParameters) (string, error) {
	token := &treePathSearchToken{
		Depth: fileTreePathDepth(last.TreePath),
		ID:    last.ID,
	}

	switch args.OrderBy {
	case file.FieldName:
		token.Name = last.Name
	case file.FieldSize:
		token.Size = last.Size
	case file.FieldUpdatedAt:
		token.UpdatedAt = &last.UpdatedAt
	}

	return token.Encode(hasher)
}

func topLevelTreePathRoots(files []*ent.File) []*ent.File {
	sorted := append([]*ent.File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool {
		left := fileTreePathDepth(sorted[i].TreePath)
		right := fileTreePathDepth(sorted[j].TreePath)
		if left == right {
			return sorted[i].ID < sorted[j].ID
		}

		return left < right
	})

	roots := make([]*ent.File, 0, len(sorted))
	for _, candidate := range sorted {
		if lo.SomeBy(roots, func(existing *ent.File) bool {
			return fileTreePathHasPrefix(candidate.TreePath, existing.TreePath)
		}) {
			continue
		}

		roots = append(roots, candidate)
	}

	return roots
}
