package dbfs

import (
	"context"
	"fmt"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/cache"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

var myNavigatorCapability = &boolset.BooleanSet{}
var myNavigatorReadonlyCapability = &boolset.BooleanSet{}

type hiddenPublicRootAccessCtxKey struct{}

// NewMyNavigator creates a navigator for user's "my" file system.
func NewMyNavigator(u *ent.User, fileClient inventory.FileClient, userClient inventory.UserClient, l logging.Logger,
	config *setting.DBFS, hasher hashid.Encoder, publicService *publicshare.Service) Navigator {
	n := &myNavigator{
		user:          u,
		l:             l,
		fileClient:    fileClient,
		userClient:    userClient,
		config:        config,
		publicService: publicService,
		targetUsers:   make(map[int]*ent.User),
	}
	n.baseNavigator = newBaseNavigator(fileClient, n.filter, u, hasher, config)
	return n
}

type myNavigator struct {
	l          logging.Logger
	user       *ent.User
	fileClient inventory.FileClient
	userClient inventory.UserClient

	config        *setting.DBFS
	publicService *publicshare.Service
	*baseNavigator
	root           *File
	rootUserID     int
	targetUsers    map[int]*ent.User
	disableRecycle bool
	persist        func()
	publicRootID   int
	publicRootSet  bool
}

func (n *myNavigator) Recycle() {
	if n.persist != nil {
		n.persist()
		n.persist = nil
	}
	if n.root != nil && !n.disableRecycle {
		n.root.Recycle()
	}
}

func (n *myNavigator) PersistState(kv cache.Driver, key string) {
	n.disableRecycle = true
	n.persist = func() {
		kv.Set(key, n.root, ContextHintTTL)
	}
}

func (n *myNavigator) RestoreState(s State) error {
	n.disableRecycle = true
	if state, ok := s.(*File); ok {
		if n.targetUsers == nil {
			n.targetUsers = make(map[int]*ent.User)
		}
		n.root = state
		if state != nil && !state.IsNil() {
			n.rootUserID = state.OwnerID()
			if state.OwnerModel != nil {
				n.targetUsers[state.OwnerModel.ID] = state.OwnerModel
			}
		}
		return nil
	}

	return fmt.Errorf("invalid state type: %T", s)
}

func (n *myNavigator) To(ctx context.Context, path *fs.URI) (*File, error) {
	fsUid, err := n.hasher.Decode(path.ID(hashid.EncodeUserID(n.hasher, n.user.ID)), hashid.UserID)
	if err != nil {
		return nil, fs.ErrPathNotExist.WithError(fmt.Errorf("invalid user id"))
	}
	if fsUid != n.user.ID && !n.isAdmin() {
		return nil, ErrPermissionDenied
	}

	if n.root == nil || n.rootUserID != fsUid {
		// Anonymous user does not have a root folder.
		if inventory.IsAnonymousUser(n.user) {
			return nil, ErrLoginRequired
		}

		targetUser, err := n.targetUser(ctx, fsUid)
		if err != nil {
			return nil, fs.ErrPathNotExist.WithError(fmt.Errorf("user not found: %w", err))
		}

		if targetUser.Status != user.StatusActive && !inventory.UserIsAdmin(n.user) {
			return nil, fs.ErrPathNotExist.WithError(fmt.Errorf("inactive user"))
		}

		rootFile, err := n.fileClient.Root(ctx, targetUser)
		if err != nil {
			n.l.Info("User's root folder not found: %s, will initialize it.", err)
			return nil, ErrFsNotInitialized
		}

		n.root = newFile(nil, rootFile)
		rootPath := path.Root()
		n.root.Path[pathIndexRoot], n.root.Path[pathIndexUser] = rootPath, rootPath
		n.root.OwnerModel = targetUser
		n.root.disableView = fsUid != n.user.ID
		n.root.IsUserRoot = true
		n.root.CapabilitiesBs = n.rootCapabilities(fsUid)
		n.rootUserID = fsUid
	}

	current, lastAncestor := n.root, n.root
	elements := path.Elements()
	for index, element := range elements {
		lastAncestor = current
		current, err = n.walkNext(ctx, current, element, index == len(elements)-1)
		if err != nil {
			return lastAncestor, fmt.Errorf("failed to walk into %q: %w", element, err)
		}
	}

	return current, nil
}

func (n *myNavigator) targetUser(ctx context.Context, userID int) (*ent.User, error) {
	if n.targetUsers == nil {
		n.targetUsers = make(map[int]*ent.User)
	}

	if userID == n.user.ID && n.user != nil && n.user.Edges.Group != nil {
		n.targetUsers[userID] = n.user
		return n.user, nil
	}

	if targetUser, ok := n.targetUsers[userID]; ok {
		return targetUser, nil
	}

	loadCtx := context.WithValue(ctx, inventory.LoadUserGroup{}, true)
	targetUser, err := n.userClient.GetByID(loadCtx, userID)
	if err != nil {
		return nil, err
	}

	n.targetUsers[userID] = targetUser
	return targetUser, nil
}

func (n *myNavigator) Children(ctx context.Context, parent *File, args *ListArgs) (*ListResult, error) {
	return n.baseNavigator.children(ctx, parent, args)
}

func (n *myNavigator) walkNext(ctx context.Context, root *File, next string, isLeaf bool) (*File, error) {
	return n.baseNavigator.walkNext(ctx, root, next, isLeaf)
}

func (n *myNavigator) filter(ctx context.Context, f *File) (*File, bool) {
	if n.isHiddenPublicFile(ctx, f) {
		return nil, false
	}

	return f, true
}

func (n *myNavigator) publicRoot(ctx context.Context) int {
	if n.publicRootSet || n.publicService == nil {
		return n.publicRootID
	}

	rootID, err := n.publicService.RootID(ctx)
	if err != nil {
		if n.l != nil {
			n.l.Warning("Failed to resolve public root id for my navigator filter: %v", err)
		}
		n.publicRootSet = true
		return 0
	}

	n.publicRootID = rootID
	n.publicRootSet = true
	return n.publicRootID
}

func (n *myNavigator) isHiddenPublicFile(ctx context.Context, f *File) bool {
	if f == nil || f.IsNil() {
		return false
	}
	if allowed, _ := ctx.Value(hiddenPublicRootAccessCtxKey{}).(bool); allowed {
		return false
	}

	publicRootID := n.publicRoot(ctx)
	if publicRootID == 0 {
		return false
	}

	for current := f; current != nil; current = current.Parent {
		if current.ID() == publicRootID {
			return true
		}
	}

	return false
}

func withHiddenPublicRootAccess(ctx context.Context, target *File) context.Context {
	if ctx == nil || target == nil {
		return ctx
	}

	uri := target.Uri(false)
	if uri == nil || uri.FileSystem() != constants.FileSystemPublic {
		return ctx
	}

	return context.WithValue(ctx, hiddenPublicRootAccessCtxKey{}, true)
}

func (n *myNavigator) Capabilities(isSearching bool) *fs.NavigatorProps {
	res := &fs.NavigatorProps{
		Capability:            myNavigatorCapability,
		OrderDirectionOptions: fullOrderDirectionOption,
		OrderByOptions:        fullOrderByOption,
		MaxPageSize:           n.config.MaxPageSize,
	}
	if isSearching {
		res.OrderByOptions = nil
		res.OrderDirectionOptions = nil
	}

	return res
}

func (n *myNavigator) isAdmin() bool {
	return inventory.UserIsAdmin(n.user)
}

func (n *myNavigator) rootCapabilities(targetUserID int) *boolset.BooleanSet {
	if targetUserID != n.user.ID {
		return myNavigatorReadonlyCapability
	}

	return n.Capabilities(false).Capability
}

func (n *myNavigator) Walk(ctx context.Context, levelFiles []*File, limit, depth int, f WalkFunc) error {
	if depth < 0 {
		depth = int(^uint(0) >> 1)
	}
	allowed, _ := ctx.Value(hiddenPublicRootAccessCtxKey{}).(bool)

	if len(levelFiles) == 0 {
		return nil
	}

	if limit <= 0 {
		return ErrFileCountLimitedReached
	}

	pageSize := defaultPageSize
	if n.config != nil && n.config.MaxPageSize > 0 {
		pageSize = n.config.MaxPageSize
	}

	walked := 0
	currentLevel := levelFiles
	for level := 0; len(currentLevel) > 0 && depth >= 0; level++ {
		visible := currentLevel
		if !allowed {
			visible = make([]*File, 0, len(currentLevel))
			for _, current := range currentLevel {
				if filtered, ok := n.filter(ctx, current); ok {
					visible = append(visible, filtered)
				}
			}
		}

		if len(visible) == 0 {
			break
		}

		stop := false
		if len(visible) > limit-walked {
			visible = visible[:limit-walked]
			stop = true
		}

		if err := f(visible, level); err != nil {
			return err
		}

		if stop {
			return ErrFileCountLimitedReached
		}

		walked += len(visible)
		if walked >= limit {
			return ErrFileCountLimitedReached
		}

		if depth == 0 {
			break
		}
		depth--

		nextLevel := make([]*File, 0)
		for _, parent := range visible {
			if !parent.CanHaveChildren() {
				continue
			}

			token := ""
			for {
				res, err := n.Children(ctx, parent, &ListArgs{
					Page: &inventory.PaginationArgs{
						UseCursorPagination: true,
						PageToken:           token,
						PageSize:            pageSize,
					},
				})
				if err != nil {
					return err
				}

				nextLevel = append(nextLevel, res.Files...)
				if res.Pagination == nil || res.Pagination.NextPageToken == "" {
					break
				}

				token = res.Pagination.NextPageToken
			}
		}

		currentLevel = nextLevel
	}

	return nil
}

func (n *myNavigator) FollowTx(ctx context.Context) (func(), error) {
	if _, ok := ctx.Value(inventory.TxCtx{}).(*inventory.Tx); !ok {
		return nil, fmt.Errorf("navigator: no inherited transaction found in context")
	}
	newFileClient, _, _, err := inventory.WithTx(ctx, n.fileClient)
	if err != nil {
		return nil, err
	}

	newUserClient, _, _, err := inventory.WithTx(ctx, n.userClient)

	oldFileClient, oldUserClient := n.fileClient, n.userClient
	revert := func() {
		n.fileClient = oldFileClient
		n.userClient = oldUserClient
		n.baseNavigator.fileClient = oldFileClient
	}

	n.fileClient = newFileClient
	n.userClient = newUserClient
	n.baseNavigator.fileClient = newFileClient
	return revert, nil
}

func (n *myNavigator) ExecuteHook(ctx context.Context, hookType fs.HookType, file *File) error {
	return nil
}

func (n *myNavigator) GetView(ctx context.Context, file *File) *types.ExplorerView {
	return file.View()
}
