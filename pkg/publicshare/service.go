package publicshare

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	entfile "github.com/cloudreve/Cloudreve/v4/ent/file"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/samber/lo"
)

type Service struct {
	l             logging.Logger
	fileClient    inventory.FileClient
	settingClient inventory.SettingClient
	hasher        hashid.Encoder
}

type RootBinding struct {
	File *ent.File `json:"file"`
	Rule *Rule     `json:"rule"`
}

func NewService(l logging.Logger, fileClient inventory.FileClient, settingClient inventory.SettingClient, hasher hashid.Encoder) *Service {
	return &Service{
		l:             l,
		fileClient:    fileClient,
		settingClient: settingClient,
		hasher:        hasher,
	}
}

// RootID returns configured public root ID.
// It returns 0 when the public root has not been configured yet.
func (s *Service) RootID(ctx context.Context) (int, error) {
	value, err := s.settingClient.Get(ctx, PublicRootFileIDSetting)
	if err != nil || strings.TrimSpace(value) == "" {
		return 0, nil
	}

	rootID, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid public root id: %w", err)
	}

	return rootID, nil
}

func (s *Service) Root(ctx context.Context) (*ent.File, error) {
	rootID, err := s.RootID(ctx)
	if err != nil {
		return nil, err
	}
	if rootID == 0 {
		return nil, fmt.Errorf("public root not configured")
	}

	ctx = context.WithValue(ctx, inventory.LoadFileMetadata{}, true)
	root, err := s.fileClient.GetByID(ctx, rootID)
	if err != nil {
		return nil, fmt.Errorf("failed to get public root: %w", err)
	}

	return root, nil
}

func (s *Service) EnsureRoot(ctx context.Context, owner *ent.User) (*ent.File, error) {
	root, err := s.Root(ctx)
	if err == nil {
		return root, nil
	}

	tx, txErr := s.fileClient.GetClient().Tx(ctx)
	if txErr != nil {
		return nil, fmt.Errorf("failed to start public root transaction: %w", txErr)
	}

	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	txService := NewService(
		s.l,
		s.fileClient.SetClient(tx.Client()).(inventory.FileClient),
		s.settingClient.SetClient(tx.Client()).(inventory.SettingClient),
		s.hasher,
	)

	systemOwner, ensureOwnerErr := txService.ensureSystemOwner(ctx)
	if ensureOwnerErr != nil {
		return nil, ensureOwnerErr
	}

	publicRoot, ensureRootErr := txService.ensureHiddenRootFile(ctx, systemOwner.ID)
	if ensureRootErr != nil {
		return nil, ensureRootErr
	}

	if err := txService.settingClient.Set(ctx, map[string]string{
		PublicRootFileIDSetting: strconv.Itoa(publicRoot.ID),
	}); err != nil {
		return nil, fmt.Errorf("failed to persist public root id: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit public root transaction: %w", err)
	}
	committed = true

	return publicRoot, nil
}

func (s *Service) RootOwnerURI(ctx context.Context, root *ent.File) (*fs.URI, error) {
	if root == nil {
		return nil, fmt.Errorf("public root not found")
	}
	if isHiddenPublicRoot(root) {
		return BuildPublicURI().Join(DefaultRootName), nil
	}

	ancestors, err := s.fileClient.GetAncestorFiles(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("failed to get public root ancestors: %w", err)
	}

	base, err := fs.NewUriFromString(fmt.Sprintf("%s://%s@%s", constants.CloudreveScheme,
		hashid.EncodeUserID(s.hasher, root.OwnerID), constants.FileSystemMy))
	if err != nil {
		return nil, err
	}

	for _, ancestor := range ancestors {
		if ancestor.Name == inventory.RootFolderName {
			continue
		}
		base = base.Join(ancestor.Name)
	}

	return base, nil
}

func isHiddenPublicRoot(root *ent.File) bool {
	return root != nil && root.Name == inventory.RootFolderName && root.FileChildren == 0
}

func (s *Service) ensureSystemOwner(ctx context.Context) (*ent.User, error) {
	return inventory.EnsurePublicSystemOwner(ctx, s.fileClient.GetClient())
}

func (s *Service) ensureHiddenRootFile(ctx context.Context, ownerID int) (*ent.File, error) {
	root, err := s.Root(ctx)
	if err != nil {
		root = nil
	}

	if root == nil {
		existingRoot, rootErr := s.fileClient.GetClient().File.Query().
			Where(
				entfile.OwnerIDEQ(ownerID),
				entfile.Not(entfile.HasParent()),
				entfile.Name(inventory.RootFolderName),
			).
			First(ctx)
		if rootErr == nil {
			root = existingRoot
		} else if !ent.IsNotFound(rootErr) {
			return nil, fmt.Errorf("failed to query public system root: %w", rootErr)
		}
	}

	if root == nil {
		created, createErr := s.fileClient.CreateFolder(ctx, nil, &inventory.CreateFolderParameters{
			Owner: ownerID,
			Name:  inventory.RootFolderName,
		})
		if createErr != nil {
			return nil, fmt.Errorf("failed to create hidden public root: %w", createErr)
		}
		root = created
	}

	needsUpdate := root.OwnerID != ownerID ||
		root.Type != int(types.FileTypeFolder) ||
		root.IsSymbolic ||
		root.Name != inventory.RootFolderName ||
		root.FileChildren > 0
	if needsUpdate {
		updated, updateErr := s.fileClient.GetClient().File.UpdateOneID(root.ID).
			SetOwnerID(ownerID).
			SetType(int(types.FileTypeFolder)).
			SetIsSymbolic(false).
			SetName(inventory.RootFolderName).
			SetFileExt("").
			ClearParent().
			Save(ctx)
		if updateErr != nil {
			return nil, fmt.Errorf("failed to normalize hidden public root: %w", updateErr)
		}
		root = updated
	}

	return root, nil
}

func (s *Service) MockState(ctx context.Context) (*MockState, error) {
	raw, err := s.settingClient.Get(ctx, PublicMockStateSetting)
	if err != nil || strings.TrimSpace(raw) == "" {
		return DefaultMockState(), nil
	}

	state := &MockState{}
	if err := json.Unmarshal([]byte(raw), state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal mock public state: %w", err)
	}

	if len(state.Departments) == 0 && len(state.Groups) == 0 {
		defaultState := DefaultMockState()
		state.Departments = defaultState.Departments
		state.Groups = defaultState.Groups
	}

	return state, nil
}

func (s *Service) SaveMockState(ctx context.Context, state *MockState) error {
	if state == nil {
		state = DefaultMockState()
	}

	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to marshal mock public state: %w", err)
	}

	return s.settingClient.Set(ctx, map[string]string{
		PublicMockStateSetting: string(raw),
	})
}

func (s *Service) UpsertProfile(ctx context.Context, profile UserProfile) (*MockState, error) {
	state, err := s.MockState(ctx)
	if err != nil {
		return nil, err
	}

	found := false
	for i := range state.Profiles {
		if state.Profiles[i].UserID == profile.UserID {
			state.Profiles[i] = profile
			found = true
			break
		}
	}
	if !found {
		state.Profiles = append(state.Profiles, profile)
	}

	if err := s.SaveMockState(ctx, state); err != nil {
		return nil, err
	}

	return state, nil
}

func (s *Service) ListRoots(ctx context.Context) ([]RootBinding, error) {
	root, err := s.Root(ctx)
	if err != nil {
		return nil, nil
	}

	ctx = context.WithValue(ctx, inventory.LoadFileMetadata{}, true)
	children, err := s.fileClient.GetChildFiles(ctx, &inventory.ListFileParameters{
		PaginationArgs: &inventory.PaginationArgs{
			PageSize:            1000,
			UseCursorPagination: true,
		},
	}, 0, root)
	if err != nil {
		return nil, fmt.Errorf("failed to list public roots: %w", err)
	}

	return lo.Map(children.Files, func(item *ent.File, _ int) RootBinding {
		return RootBinding{
			File: item,
			Rule: RuleFromMetadata(item),
		}
	}), nil
}

func (s *Service) CreateRootFolder(ctx context.Context, owner *ent.User, name string, rule *Rule) (*ent.File, error) {
	root, err := s.EnsureRoot(ctx, owner)
	if err != nil {
		return nil, err
	}

	folderOwnerID := root.OwnerID
	if owner != nil && owner.ID > 0 {
		folderOwnerID = owner.ID
	}

	folder, err := s.fileClient.CreateFolder(ctx, root, &inventory.CreateFolderParameters{
		Owner: folderOwnerID,
		Name:  strings.TrimSpace(name),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create public folder: %w", err)
	}

	if rule != nil {
		if err := s.SaveRule(ctx, folder, rule); err != nil {
			return nil, err
		}
	}

	return folder, nil
}

func (s *Service) SaveRule(ctx context.Context, target *ent.File, rule *Rule) error {
	if target == nil {
		return fmt.Errorf("target folder is nil")
	}

	if rule == nil {
		rule = &Rule{}
	}
	rule.Normalize()

	raw, err := json.Marshal(rule)
	if err != nil {
		return fmt.Errorf("failed to marshal public rule: %w", err)
	}

	if err := s.fileClient.UpsertMetadata(ctx, target, map[string]string{
		FolderRuleMetadataKey: string(raw),
	}, nil); err != nil {
		return fmt.Errorf("failed to save public rule metadata: %w", err)
	}

	target.SetMetadata([]*ent.Metadata{
		{Name: FolderRuleMetadataKey, Value: string(raw), IsPublic: true},
	})
	return nil
}

func RuleFromMetadata(target *ent.File) *Rule {
	if target == nil || target.Edges.Metadata == nil {
		return &Rule{}
	}

	for _, meta := range target.Edges.Metadata {
		if meta.Name != FolderRuleMetadataKey {
			continue
		}

		rule := &Rule{}
		if err := json.Unmarshal([]byte(meta.Value), rule); err != nil {
			return &Rule{}
		}

		rule.Normalize()
		return rule
	}

	return &Rule{}
}

func isAdminUser(user *ent.User) bool {
	return user != nil && user.Edges.Group != nil && user.Edges.Group.Permissions.Enabled(int(types.GroupPermissionIsAdmin))
}

func (s *Service) ResolveVisibility(ctx context.Context, user *ent.User) (*VisibilityResult, error) {
	if visibility := VisibilityOverrideFromContext(ctx); visibility != nil {
		return visibility, nil
	}

	if isAdminUser(user) {
		return s.resolveVisibilityLocal(ctx, user)
	}

	if s.UnifiedAuthzEnabled(ctx) {
		accessToken := oidcAccessTokenFromContext(ctx)
		if accessToken == "" {
			return &VisibilityResult{
				Filter:     FalseFilter(),
				RootGrants: nil,
			}, nil
		}

		return s.resolveVisibilityRemote(ctx, accessToken)
	}

	return s.resolveVisibilityLocal(ctx, user)
}

func (s *Service) resolveVisibilityLocal(ctx context.Context, user *ent.User) (*VisibilityResult, error) {
	bindings, err := s.ListRoots(ctx)
	if err != nil {
		return nil, err
	}

	if len(bindings) == 0 {
		return &VisibilityResult{
			Filter:     FalseFilter(),
			RootGrants: nil,
		}, nil
	}

	if isAdminUser(user) {
		grants := lo.Map(bindings, func(item RootBinding, _ int) RootGrant {
			return RootGrant{
				RootFileID:   item.File.ID,
				RootOwnerID:  item.File.OwnerID,
				RootName:     item.File.Name,
				RootTreePath: item.File.TreePath,
				Actions: lo.SliceToMap(ActionOrder, func(action Action) (Action, bool) {
					return action, true
				}),
			}
		})

		return &VisibilityResult{
			Filter:     BuildVisibilityFilter(grants),
			RootGrants: grants,
		}, nil
	}

	state, err := s.MockState(ctx)
	if err != nil {
		return nil, err
	}

	profile := state.ProfileForUser(user)
	index := state.Index()
	userHash := ""
	if user != nil {
		userHash = hashid.EncodeUserID(s.hasher, user.ID)
	}

	grants := make([]RootGrant, 0, len(bindings))
	for _, binding := range bindings {
		rule := binding.Rule
		if rule == nil {
			rule = &Rule{}
		}

		if !EvaluatePrincipalExpr(rule.Visibility, user, userHash, profile, index) {
			continue
		}

		actions := make(map[Action]bool, len(ActionOrder))
		for _, action := range ActionOrder {
			actions[action] = EvaluatePrincipalExpr(rule.ExprFor(action), user, userHash, profile, index)
		}
		actions[ActionList] = true

		grants = append(grants, RootGrant{
			RootFileID:   binding.File.ID,
			RootOwnerID:  binding.File.OwnerID,
			RootName:     binding.File.Name,
			RootTreePath: binding.File.TreePath,
			Actions:      actions,
		})
	}

	return &VisibilityResult{
		Filter:     BuildVisibilityFilter(grants),
		RootGrants: grants,
	}, nil
}

func (s *Service) CheckActionByFile(ctx context.Context, user *ent.User, target *ent.File, action Action) (*ActionDecision, error) {
	if visibility := VisibilityOverrideFromContext(ctx); visibility != nil {
		return decisionFromVisibility(target, action, visibility), nil
	}

	if isAdminUser(user) {
		return s.checkActionByFileLocal(ctx, user, target, action)
	}

	if s.UnifiedAuthzEnabled(ctx) {
		accessToken := oidcAccessTokenFromContext(ctx)
		if accessToken == "" {
			return &ActionDecision{
				Allowed: false,
				Action:  action,
				Reason:  "oidc_access_token_missing",
			}, nil
		}

		return s.checkActionRemote(ctx, accessToken, target, action)
	}

	return s.checkActionByFileLocal(ctx, user, target, action)
}

func (s *Service) checkActionByFileLocal(ctx context.Context, user *ent.User, target *ent.File, action Action) (*ActionDecision, error) {
	if target == nil {
		return &ActionDecision{Allowed: false, Action: action, Reason: "target_not_found"}, nil
	}

	if root, err := s.Root(ctx); err == nil && root.ID == target.ID {
		actions := lo.SliceToMap(ActionOrder, func(action Action) (Action, bool) {
			return action, isAdminUser(user)
		})
		actions[ActionList] = true

		return &ActionDecision{
			Allowed:    actions[action],
			Action:     action,
			RootFileID: root.ID,
			Actions:    actions,
			Reason:     lo.If(actions[action], "allowed").Else("action_denied"),
		}, nil
	}

	visibility, err := s.ResolveVisibility(ctx, user)
	if err != nil {
		return nil, err
	}

	return decisionFromVisibility(target, action, visibility), nil
}

func decisionFromVisibility(target *ent.File, action Action, visibility *VisibilityResult) *ActionDecision {
	if target == nil {
		return &ActionDecision{Allowed: false, Action: action, Reason: "target_not_found"}
	}
	if visibility == nil {
		visibility = &VisibilityResult{}
	}

	rootGrant, found := rootGrantForAncestors(target, visibility.RootGrants)
	if !found {
		return &ActionDecision{Allowed: false, Action: action, Reason: "root_not_visible"}
	}

	actions := make(map[Action]bool, len(ActionOrder))
	for _, candidate := range ActionOrder {
		actions[candidate] = candidate == ActionList || RootGrantActionAllowed(target.ID, rootGrant, candidate)
	}
	actions[ActionList] = true

	allowed := actions[action]
	reason := lo.If(allowed, "allowed").Else("action_denied")
	if action == ActionDelete && target.ID == rootGrant.RootFileID && !allowed {
		if _, ok := rootGrant.Actions[ActionDeleteRoot]; ok {
			reason = "root_delete_protected"
		}
	}

	return &ActionDecision{
		Allowed:    allowed,
		Action:     action,
		RootFileID: rootGrant.RootFileID,
		Actions:    actions,
		Reason:     reason,
	}
}

func rootGrantForAncestors(target *ent.File, grants []RootGrant) (RootGrant, bool) {
	if target == nil {
		return RootGrant{}, false
	}

	targetPath := strings.TrimSpace(target.TreePath)
	targetID := target.ID
	var (
		matched      RootGrant
		matchedDepth = -1
	)
	for _, grant := range grants {
		if grant.RootFileID == targetID {
			return grant, true
		}

		grantPath := strings.TrimSpace(grant.RootTreePath)
		if grantPath == "" || targetPath == "" {
			continue
		}
		if targetPath != grantPath && !strings.HasPrefix(targetPath, grantPath+".") {
			continue
		}

		depth := len(strings.Split(grantPath, "."))
		if depth > matchedDepth {
			matched = grant
			matchedDepth = depth
		}
	}

	return matched, matchedDepth >= 0
}

type stateIndex struct {
	deptByID   map[string]Department
	parentByID map[string]string
	groupSet   map[string]struct{}
}

func (s *MockState) Index() *stateIndex {
	index := &stateIndex{
		deptByID:   make(map[string]Department, len(s.Departments)),
		parentByID: make(map[string]string, len(s.Departments)),
		groupSet:   make(map[string]struct{}, len(s.Groups)),
	}

	for _, dept := range s.Departments {
		index.deptByID[dept.ID] = dept
		index.parentByID[dept.ID] = dept.ParentID
	}

	for _, group := range s.Groups {
		index.groupSet[group.ID] = struct{}{}
	}

	return index
}

func (s *MockState) ProfileForUser(user *ent.User) *UserProfile {
	if user == nil {
		return &UserProfile{}
	}

	for _, profile := range s.Profiles {
		if profile.UserID == user.ID {
			profileCopy := profile
			return &profileCopy
		}
	}

	return &UserProfile{UserID: user.ID}
}

func EvaluatePrincipalExpr(expr *PrincipalExpr, user *ent.User, userHash string, profile *UserProfile, index *stateIndex) bool {
	if expr == nil {
		return false
	}

	if expr.Match != nil {
		switch expr.Match.Kind {
		case PrincipalMatchAnyone:
			return true
		case PrincipalMatchUser:
			for _, value := range expr.Match.Values {
				value = strings.TrimSpace(value)
				if value == "" {
					continue
				}
				if value == userHash || (user != nil && value == user.Email) {
					return true
				}
			}
			return false
		case PrincipalMatchDepartment:
			return containsString(expr.Match.Values, profile.DepartmentID)
		case PrincipalMatchDepartmentAncestor:
			for _, value := range expr.Match.Values {
				if isAncestorDepartment(index, profile.DepartmentID, value) {
					return true
				}
			}
			return false
		case PrincipalMatchDepartmentDescendant:
			for _, value := range expr.Match.Values {
				if isAncestorDepartment(index, value, profile.DepartmentID) {
					return true
				}
			}
			return false
		case PrincipalMatchGroup:
			groupSet := make(map[string]struct{}, len(profile.GroupIDs))
			for _, groupID := range profile.GroupIDs {
				groupSet[groupID] = struct{}{}
			}
			for _, value := range expr.Match.Values {
				if _, ok := groupSet[value]; ok {
					return true
				}
			}
			return false
		default:
			return false
		}
	}

	switch expr.Operator {
	case PrincipalOpAnd:
		if len(expr.Children) == 0 {
			return false
		}
		for _, child := range expr.Children {
			if !EvaluatePrincipalExpr(child, user, userHash, profile, index) {
				return false
			}
		}
		return true
	case PrincipalOpOr:
		for _, child := range expr.Children {
			if EvaluatePrincipalExpr(child, user, userHash, profile, index) {
				return true
			}
		}
		return false
	case PrincipalOpNot:
		if len(expr.Children) == 0 {
			return false
		}
		return !EvaluatePrincipalExpr(expr.Children[0], user, userHash, profile, index)
	default:
		return false
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == strings.TrimSpace(target) && target != "" {
			return true
		}
	}
	return false
}

func isAncestorDepartment(index *stateIndex, ancestorID, targetID string) bool {
	if index == nil || ancestorID == "" || targetID == "" {
		return false
	}

	current := targetID
	for current != "" {
		if current == ancestorID {
			return true
		}
		current = index.parentByID[current]
	}

	return false
}

func BuildPublicURI() *fs.URI {
	uri, _ := fs.NewUriFromString(fmt.Sprintf("%s://%s", constants.CloudreveScheme, constants.FileSystemPublic))
	return uri
}
