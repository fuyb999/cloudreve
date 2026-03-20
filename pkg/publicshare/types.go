package publicshare

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"entgo.io/ent/dialect/sql"
	"github.com/cloudreve/Cloudreve/v4/ent/file"
	"github.com/cloudreve/Cloudreve/v4/ent/predicate"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
)

const (
	PublicRootFileIDSetting = "public_root_file_id"
	PublicMockStateSetting  = "public_mock_state"

	FolderRuleMetadataKey = "sys:public_rule"
	DefaultRootName       = "公共文件"
)

type Action string

const (
	ActionList       Action = "list"
	ActionDownload   Action = "download"
	ActionDirectLink Action = "direct_link"
	ActionArchive    Action = "archive"
	ActionUpload     Action = "upload"
	ActionCreate     Action = "create"
	ActionRename     Action = "rename"
	ActionDelete     Action = "delete"
	ActionDeleteRoot Action = "delete_root"
	ActionShare      Action = "share"
	ActionCopy       Action = "copy"
	ActionMove       Action = "move"
	ActionMetadata   Action = "metadata"
)

var ActionOrder = []Action{
	ActionList,
	ActionDownload,
	ActionDirectLink,
	ActionArchive,
	ActionUpload,
	ActionCreate,
	ActionRename,
	ActionDelete,
	ActionDeleteRoot,
	ActionShare,
	ActionCopy,
	ActionMove,
	ActionMetadata,
}

type PrincipalOperator string

const (
	PrincipalOpAnd PrincipalOperator = "and"
	PrincipalOpOr  PrincipalOperator = "or"
	PrincipalOpNot PrincipalOperator = "not"
)

type PrincipalMatchKind string

const (
	PrincipalMatchAnyone               PrincipalMatchKind = "anyone"
	PrincipalMatchUser                 PrincipalMatchKind = "user"
	PrincipalMatchDepartment           PrincipalMatchKind = "department"
	PrincipalMatchDepartmentAncestor   PrincipalMatchKind = "department_ancestor"
	PrincipalMatchDepartmentDescendant PrincipalMatchKind = "department_descendant"
	PrincipalMatchGroup                PrincipalMatchKind = "group"
)

type PrincipalMatch struct {
	Kind   PrincipalMatchKind `json:"kind"`
	Values []string           `json:"values,omitempty"`
}

type PrincipalExpr struct {
	Operator PrincipalOperator `json:"operator,omitempty"`
	Children []*PrincipalExpr  `json:"children,omitempty"`
	Match    *PrincipalMatch   `json:"match,omitempty"`
}

type Rule struct {
	Name       string                    `json:"name,omitempty"`
	Visibility *PrincipalExpr            `json:"visibility,omitempty"`
	Actions    map[Action]*PrincipalExpr `json:"actions,omitempty"`
}

type Department struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ParentID string `json:"parent_id,omitempty"`
}

type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type UserProfile struct {
	UserID       int      `json:"user_id"`
	DepartmentID string   `json:"department_id,omitempty"`
	GroupIDs     []string `json:"group_ids,omitempty"`
}

type MockState struct {
	Departments []Department  `json:"departments,omitempty"`
	Groups      []Group       `json:"groups,omitempty"`
	Profiles    []UserProfile `json:"profiles,omitempty"`
}

type FileFilterOperator string

const (
	FileFilterOpAnd FileFilterOperator = "and"
	FileFilterOpOr  FileFilterOperator = "or"
	FileFilterOpNot FileFilterOperator = "not"
)

type FileFilterMatchKind string

const (
	FileFilterMatchTrue       FileFilterMatchKind = "true"
	FileFilterMatchFalse      FileFilterMatchKind = "false"
	FileFilterMatchOwnerIDIn  FileFilterMatchKind = "owner_id_in"
	FileFilterMatchFileIDIn   FileFilterMatchKind = "file_id_in"
	FileFilterMatchTreePathIn FileFilterMatchKind = "tree_path_prefix_in"
)

type FileFilterMatch struct {
	Kind         FileFilterMatchKind `json:"kind"`
	IntValues    []int               `json:"int_values,omitempty"`
	StringValues []string            `json:"string_values,omitempty"`
}

type FileFilterExpr struct {
	Operator FileFilterOperator `json:"operator,omitempty"`
	Children []*FileFilterExpr  `json:"children,omitempty"`
	Match    *FileFilterMatch   `json:"match,omitempty"`
}

type RootGrant struct {
	RootFileID   int             `json:"root_file_id"`
	RootOwnerID  int             `json:"root_owner_id"`
	RootName     string          `json:"root_name,omitempty"`
	RootTreePath string          `json:"root_tree_path,omitempty"`
	Actions      map[Action]bool `json:"actions,omitempty"`
}

type VisibilityResult struct {
	Filter     *FileFilterExpr `json:"filter,omitempty"`
	RootGrants []RootGrant     `json:"root_grants,omitempty"`
}

type ActionDecision struct {
	Allowed    bool            `json:"allowed"`
	Action     Action          `json:"action"`
	RootFileID int             `json:"root_file_id,omitempty"`
	Actions    map[Action]bool `json:"actions,omitempty"`
	Reason     string          `json:"reason,omitempty"`
}

func DefaultMockState() *MockState {
	return &MockState{
		Departments: []Department{
			{ID: "hq", Name: "总部"},
			{ID: "rd", Name: "研发中心", ParentID: "hq"},
			{ID: "rd-backend", Name: "后端组", ParentID: "rd"},
			{ID: "rd-frontend", Name: "前端组", ParentID: "rd"},
			{ID: "ops", Name: "运维中心", ParentID: "hq"},
			{ID: "sales", Name: "销售中心", ParentID: "hq"},
		},
		Groups: []Group{
			{ID: "management", Name: "管理组"},
			{ID: "project-a", Name: "项目A"},
			{ID: "project-b", Name: "项目B"},
		},
	}
}

func (r *Rule) Normalize() {
	if r == nil {
		return
	}
	if r.Actions == nil {
		r.Actions = make(map[Action]*PrincipalExpr)
	}
}

func (r *Rule) ExprFor(action Action) *PrincipalExpr {
	if r == nil {
		return nil
	}

	r.Normalize()
	if expr, ok := r.Actions[action]; ok && expr != nil {
		return expr
	}

	switch action {
	case ActionList, ActionDownload:
		return r.Visibility
	case ActionDirectLink, ActionArchive:
		// 直链和打包在第一版里默认继承下载语义，避免历史规则升级后全部失效。
		return r.ExprFor(ActionDownload)
	case ActionDeleteRoot:
		// delete_root 仅用于保护授权根本身，未单独配置时退回 delete，保证旧策略兼容。
		return r.ExprFor(ActionDelete)
	default:
		return nil
	}
}

// RootGrantActionAllowed 会把“一级目录删除”解释成 delete_root。
// 当目标刚好是授权根目录且策略显式声明了 delete_root 时，优先使用该动作；
// 否则继续沿用普通 delete 语义，避免历史授权全部失效。
func RootGrantActionAllowed(targetFileID int, grant RootGrant, action Action) bool {
	if grant.Actions == nil {
		return false
	}

	if action == ActionDelete && targetFileID > 0 && targetFileID == grant.RootFileID {
		if allowed, ok := grant.Actions[ActionDeleteRoot]; ok {
			return allowed
		}
	}

	return grant.Actions[action]
}

func FalseFilter() *FileFilterExpr {
	return &FileFilterExpr{
		Match: &FileFilterMatch{Kind: FileFilterMatchFalse},
	}
}

func TrueFilter() *FileFilterExpr {
	return &FileFilterExpr{
		Match: &FileFilterMatch{Kind: FileFilterMatchTrue},
	}
}

func BuildVisibilityFilter(grants []RootGrant) *FileFilterExpr {
	if len(grants) == 0 {
		return FalseFilter()
	}

	byOwner := make(map[int][]string)
	for _, grant := range grants {
		if strings.TrimSpace(grant.RootTreePath) == "" {
			continue
		}

		byOwner[grant.RootOwnerID] = append(byOwner[grant.RootOwnerID], grant.RootTreePath)
	}

	if len(byOwner) == 0 {
		return FalseFilter()
	}

	ownerIDs := make([]int, 0, len(byOwner))
	for ownerID := range byOwner {
		ownerIDs = append(ownerIDs, ownerID)
	}
	sort.Ints(ownerIDs)

	branches := make([]*FileFilterExpr, 0, len(ownerIDs))
	for _, ownerID := range ownerIDs {
		prefixes := uniqueSortedStrings(byOwner[ownerID])
		branches = append(branches, &FileFilterExpr{
			Operator: FileFilterOpAnd,
			Children: []*FileFilterExpr{
				{
					Match: &FileFilterMatch{
						Kind:      FileFilterMatchOwnerIDIn,
						IntValues: []int{ownerID},
					},
				},
				{
					Match: &FileFilterMatch{
						Kind:         FileFilterMatchTreePathIn,
						StringValues: prefixes,
					},
				},
			},
		})
	}

	if len(branches) == 1 {
		return branches[0]
	}

	return &FileFilterExpr{
		Operator: FileFilterOpOr,
		Children: branches,
	}
}

func RootGrantWithinTree(rootTreePath string, grant RootGrant) bool {
	rootPath := strings.TrimSpace(rootTreePath)
	grantPath := strings.TrimSpace(grant.RootTreePath)
	if rootPath == "" || grantPath == "" {
		return false
	}

	return grantPath == rootPath || strings.HasPrefix(grantPath, rootPath+".")
}

func ProjectedRootAlias(hasher hashid.Encoder, grant RootGrant) string {
	name := strings.TrimSpace(grant.RootName)
	if name == "" {
		name = fmt.Sprintf("public-%d", grant.RootFileID)
	}

	suffix := strconv.Itoa(grant.RootFileID)
	if hasher != nil && grant.RootFileID > 0 {
		if encoded := strings.TrimSpace(hashid.EncodeFileID(hasher, grant.RootFileID)); encoded != "" {
			suffix = encoded
		}
	}

	return fmt.Sprintf("%s__%s", name, suffix)
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	uniq := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		uniq[value] = struct{}{}
	}

	res := make([]string, 0, len(uniq))
	for value := range uniq {
		res = append(res, value)
	}
	sort.Strings(res)
	return res
}

func ToEntPredicate(expr *FileFilterExpr) predicate.File {
	if expr == nil {
		return nil
	}

	if expr.Match != nil {
		switch expr.Match.Kind {
		case FileFilterMatchTrue:
			return nil
		case FileFilterMatchFalse:
			return file.IDLT(0)
		case FileFilterMatchOwnerIDIn:
			if len(expr.Match.IntValues) == 0 {
				return file.IDLT(0)
			}
			return file.OwnerIDIn(expr.Match.IntValues...)
		case FileFilterMatchFileIDIn:
			if len(expr.Match.IntValues) == 0 {
				return file.IDLT(0)
			}
			return file.IDIn(expr.Match.IntValues...)
		case FileFilterMatchTreePathIn:
			prefixes := uniqueSortedStrings(expr.Match.StringValues)
			if len(prefixes) == 0 {
				return file.IDLT(0)
			}

			return func(s *sql.Selector) {
				pathColumn := s.C(file.FieldTreePath)
				s.Where(sql.P(func(b *sql.Builder) {
					if len(prefixes) > 1 {
						b.WriteByte('(')
					}
					for i, prefix := range prefixes {
						if i > 0 {
							b.WriteString(" OR ")
						}
						b.WriteString(pathColumn).WriteString(" <@ text2ltree(").Arg(prefix).WriteByte(')')
					}
					if len(prefixes) > 1 {
						b.WriteByte(')')
					}
				}))
			}
		default:
			return file.IDLT(0)
		}
	}

	children := make([]predicate.File, 0, len(expr.Children))
	for _, child := range expr.Children {
		if child == nil {
			continue
		}

		p := ToEntPredicate(child)
		if p != nil {
			children = append(children, p)
		}
	}

	switch expr.Operator {
	case FileFilterOpAnd:
		if len(children) == 0 {
			return nil
		}
		return file.And(children...)
	case FileFilterOpOr:
		if len(children) == 0 {
			return file.IDLT(0)
		}
		return file.Or(children...)
	case FileFilterOpNot:
		if len(children) == 0 {
			return nil
		}
		return file.Not(children[0])
	default:
		return nil
	}
}

func ToElasticsearchFilter(expr *FileFilterExpr) map[string]any {
	if expr == nil {
		return nil
	}

	if expr.Match != nil {
		switch expr.Match.Kind {
		case FileFilterMatchTrue:
			return map[string]any{"match_all": map[string]any{}}
		case FileFilterMatchFalse:
			return map[string]any{"bool": map[string]any{"must_not": []any{map[string]any{"match_all": map[string]any{}}}}}
		case FileFilterMatchOwnerIDIn:
			return map[string]any{"terms": map[string]any{"owner_id": expr.Match.IntValues}}
		case FileFilterMatchFileIDIn:
			return map[string]any{"terms": map[string]any{"file_id": expr.Match.IntValues}}
		case FileFilterMatchTreePathIn:
			should := make([]any, 0, len(expr.Match.StringValues)*2)
			for _, prefix := range uniqueSortedStrings(expr.Match.StringValues) {
				should = append(should,
					map[string]any{"term": map[string]any{"tree_path": prefix}},
					map[string]any{"prefix": map[string]any{"tree_path": prefix + "."}},
				)
			}
			return map[string]any{
				"bool": map[string]any{
					"should":               should,
					"minimum_should_match": 1,
				},
			}
		default:
			return map[string]any{"bool": map[string]any{"must_not": []any{map[string]any{"match_all": map[string]any{}}}}}
		}
	}

	children := make([]any, 0, len(expr.Children))
	for _, child := range expr.Children {
		if child == nil {
			continue
		}
		filter := ToElasticsearchFilter(child)
		if filter != nil {
			children = append(children, filter)
		}
	}

	switch expr.Operator {
	case FileFilterOpAnd:
		if len(children) == 0 {
			return nil
		}
		return map[string]any{"bool": map[string]any{"filter": children}}
	case FileFilterOpOr:
		if len(children) == 0 {
			return map[string]any{"bool": map[string]any{"must_not": []any{map[string]any{"match_all": map[string]any{}}}}}
		}
		return map[string]any{"bool": map[string]any{"should": children, "minimum_should_match": 1}}
	case FileFilterOpNot:
		if len(children) == 0 {
			return nil
		}
		return map[string]any{"bool": map[string]any{"must_not": []any{children[0]}}}
	default:
		return nil
	}
}

func ToMeilisearchFilter(expr *FileFilterExpr) string {
	if expr == nil {
		return ""
	}

	if expr.Match != nil {
		switch expr.Match.Kind {
		case FileFilterMatchTrue:
			return ""
		case FileFilterMatchFalse:
			return "file_id < 0"
		case FileFilterMatchOwnerIDIn:
			if len(expr.Match.IntValues) == 0 {
				return "file_id < 0"
			}

			values := make([]string, 0, len(expr.Match.IntValues))
			for _, value := range expr.Match.IntValues {
				values = append(values, strconv.Itoa(value))
			}
			return fmt.Sprintf("owner_id IN [%s]", strings.Join(values, ", "))
		case FileFilterMatchFileIDIn:
			if len(expr.Match.IntValues) == 0 {
				return "file_id < 0"
			}

			values := make([]string, 0, len(expr.Match.IntValues))
			for _, value := range expr.Match.IntValues {
				values = append(values, strconv.Itoa(value))
			}
			return fmt.Sprintf("file_id IN [%s]", strings.Join(values, ", "))
		case FileFilterMatchTreePathIn:
			prefixes := uniqueSortedStrings(expr.Match.StringValues)
			if len(prefixes) == 0 {
				return "file_id < 0"
			}

			parts := make([]string, 0, len(prefixes))
			for _, prefix := range prefixes {
				parts = append(parts,
					fmt.Sprintf(`(tree_path = "%s" OR tree_path STARTS WITH "%s.")`, prefix, prefix),
				)
			}
			if len(parts) == 1 {
				return parts[0]
			}
			return "(" + strings.Join(parts, " OR ") + ")"
		default:
			return "file_id < 0"
		}
	}

	children := make([]string, 0, len(expr.Children))
	for _, child := range expr.Children {
		if child == nil {
			continue
		}
		filter := ToMeilisearchFilter(child)
		if filter == "" && expr.Operator != FileFilterOpNot {
			continue
		}
		children = append(children, filter)
	}

	switch expr.Operator {
	case FileFilterOpAnd:
		if len(children) == 0 {
			return ""
		}
		return "(" + strings.Join(children, " AND ") + ")"
	case FileFilterOpOr:
		if len(children) == 0 {
			return "file_id < 0"
		}
		return "(" + strings.Join(children, " OR ") + ")"
	case FileFilterOpNot:
		if len(children) == 0 {
			return ""
		}
		return "(NOT " + children[0] + ")"
	default:
		return ""
	}
}
