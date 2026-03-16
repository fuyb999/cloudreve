package dbfs

import (
	"context"
	"fmt"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/cache"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

var publicNavigatorCapability = &boolset.BooleanSet{}

func init() {
	boolset.Sets(map[NavigatorCapability]bool{
		NavigatorCapabilityCreateFile:     true,
		NavigatorCapabilityRenameFile:     true,
		NavigatorCapabilityUploadFile:     true,
		NavigatorCapabilityDownloadFile:   true,
		NavigatorCapabilityUpdateMetadata: true,
		NavigatorCapabilityListChildren:   true,
		NavigatorCapabilityGenerateThumb:  true,
		NavigatorCapabilityDeleteFile:     true,
		NavigatorCapabilityLockFile:       true,
		NavigatorCapabilitySoftDelete:     true,
		NavigatorCapabilityInfo:           true,
		NavigatorCapabilityEnterFolder:    true,
		NavigatorCapabilityModifyProps:    true,
	}, publicNavigatorCapability)
}

func NewPublicNavigator(u *ent.User, fileClient inventory.FileClient, l logging.Logger,
	config *setting.DBFS, hasher hashid.Encoder, publicService *publicshare.Service) Navigator {
	n := &publicNavigator{
		user:          u,
		l:             l,
		fileClient:    fileClient,
		config:        config,
		publicService: publicService,
	}
	n.baseNavigator = newBaseNavigator(fileClient, n.filter, u, hasher, config)
	return n
}

type publicNavigator struct {
	l             logging.Logger
	user          *ent.User
	fileClient    inventory.FileClient
	config        *setting.DBFS
	publicService *publicshare.Service
	*baseNavigator

	root           *File
	current        *File
	visibility     *publicshare.VisibilityResult
	disableRecycle bool
	persist        func()
}

func (n *publicNavigator) isAdmin() bool {
	return n.user != nil && n.user.Edges.Group != nil && n.user.Edges.Group.Permissions.Enabled(int(types.GroupPermissionIsAdmin))
}

func (n *publicNavigator) Recycle() {
	if n.persist != nil {
		n.persist()
		n.persist = nil
	}
	if n.root != nil && !n.disableRecycle {
		n.root.Recycle()
	}
}

func (n *publicNavigator) PersistState(kv cache.Driver, key string) {
	n.disableRecycle = true
	n.persist = func() {
		kv.Set(key, n.root, ContextHintTTL)
	}
}

func (n *publicNavigator) RestoreState(s State) error {
	n.disableRecycle = true
	if state, ok := s.(*File); ok {
		n.root = state
		return nil
	}

	return fmt.Errorf("invalid state type: %T", s)
}

func (n *publicNavigator) refreshVisibility(ctx context.Context) (*publicshare.VisibilityResult, error) {
	visibility, err := n.publicService.ResolveVisibility(ctx, n.user)
	if err != nil {
		return nil, err
	}

	n.visibility = visibility
	return visibility, nil
}

func (n *publicNavigator) rootCapabilities() *boolset.BooleanSet {
	res := &boolset.BooleanSet{}
	if n.isAdmin() {
		boolset.Sets(map[NavigatorCapability]bool{
			NavigatorCapabilityCreateFile:     true,
			NavigatorCapabilityRenameFile:     true,
			NavigatorCapabilityUploadFile:     true,
			NavigatorCapabilityDownloadFile:   true,
			NavigatorCapabilityUpdateMetadata: true,
			NavigatorCapabilityListChildren:   true,
			NavigatorCapabilityGenerateThumb:  true,
			NavigatorCapabilityDeleteFile:     true,
			NavigatorCapabilityLockFile:       true,
			NavigatorCapabilitySoftDelete:     true,
			NavigatorCapabilityInfo:           true,
			NavigatorCapabilityEnterFolder:    true,
			NavigatorCapabilityModifyProps:    true,
		}, res)
		return res
	}

	boolset.Sets(map[NavigatorCapability]bool{
		NavigatorCapabilityListChildren: true,
		NavigatorCapabilityEnterFolder:  true,
		NavigatorCapabilityInfo:         true,
	}, res)
	return res
}

func capabilitySetFromActions(actions map[publicshare.Action]bool) *boolset.BooleanSet {
	res := &boolset.BooleanSet{}
	boolset.Sets(map[NavigatorCapability]bool{
		NavigatorCapabilityListChildren:   true,
		NavigatorCapabilityEnterFolder:    true,
		NavigatorCapabilityInfo:           true,
		NavigatorCapabilityGenerateThumb:  actions[publicshare.ActionDownload],
		NavigatorCapabilityDownloadFile:   actions[publicshare.ActionDownload],
		NavigatorCapabilityUploadFile:     actions[publicshare.ActionUpload],
		NavigatorCapabilityCreateFile:     actions[publicshare.ActionCreate],
		NavigatorCapabilityRenameFile:     actions[publicshare.ActionRename],
		NavigatorCapabilityDeleteFile:     actions[publicshare.ActionDelete],
		NavigatorCapabilitySoftDelete:     actions[publicshare.ActionDelete],
		NavigatorCapabilityUpdateMetadata: actions[publicshare.ActionMetadata],
		NavigatorCapabilityModifyProps:    actions[publicshare.ActionMetadata],
		NavigatorCapabilityLockFile: actions[publicshare.ActionUpload] || actions[publicshare.ActionCreate] ||
			actions[publicshare.ActionRename] || actions[publicshare.ActionDelete] || actions[publicshare.ActionMetadata],
	}, res)
	return res
}

func (n *publicNavigator) grantForFile(file *File) (publicshare.RootGrant, bool) {
	if file == nil || n.visibility == nil {
		return publicshare.RootGrant{}, false
	}

	if file == n.root {
		return publicshare.RootGrant{}, true
	}

	current := file
	for current.Parent != nil && current.Parent != n.root {
		current = current.Parent
	}

	for _, grant := range n.visibility.RootGrants {
		if grant.RootFileID == current.ID() {
			return grant, true
		}
	}

	return publicshare.RootGrant{}, false
}

func (n *publicNavigator) filter(ctx context.Context, file *File) (*File, bool) {
	if file == nil {
		return nil, false
	}

	if file == n.root {
		file.CapabilitiesBs = n.rootCapabilities()
		return file, true
	}

	if n.visibility == nil {
		if _, err := n.refreshVisibility(ctx); err != nil {
			n.l.Warning("Failed to refresh public visibility: %v", err)
			return nil, false
		}
	}

	grant, ok := n.grantForFile(file)
	if !ok {
		return nil, false
	}

	file.CapabilitiesBs = capabilitySetFromActions(grant.Actions)
	return file, true
}

func (n *publicNavigator) To(ctx context.Context, path *fs.URI) (*File, error) {
	if n.root == nil {
		rootModel, err := n.publicService.Root(ctx)
		if err != nil {
			if !n.isAdmin() {
				return nil, fs.ErrPathNotExist.WithError(err)
			}

			rootModel, err = n.publicService.EnsureRoot(ctx, n.user)
			if err != nil {
				return nil, fs.ErrPathNotExist.WithError(err)
			}
		}

		rootUri := newPublicUri()
		ownerUri, err := n.publicService.RootOwnerURI(ctx, rootModel)
		if err != nil {
			ownerUri = rootUri
		}

		n.root = newFile(nil, rootModel)
		n.root.Path[pathIndexRoot] = ownerUri
		n.root.Path[pathIndexUser] = rootUri
		n.root.OwnerModel = &ent.User{ID: rootModel.OwnerID}
		n.root.IsUserRoot = true
		n.root.CapabilitiesBs = n.rootCapabilities()
	}

	if _, err := n.refreshVisibility(ctx); err != nil {
		return nil, fmt.Errorf("failed to resolve public visibility: %w", err)
	}

	current, lastAncestor := n.root, n.root
	elements := path.Elements()
	for index, element := range elements {
		lastAncestor = current
		next, err := n.baseNavigator.walkNext(ctx, current, element, index == len(elements)-1)
		if err != nil {
			return lastAncestor, fmt.Errorf("failed to walk into %q: %w", element, err)
		}

		filtered, ok := n.filter(ctx, next)
		if !ok {
			return lastAncestor, fs.ErrPathNotExist.WithError(fmt.Errorf("public file is not visible"))
		}

		current = filtered
	}

	n.current = current
	return current, nil
}

func (n *publicNavigator) Children(ctx context.Context, parent *File, args *ListArgs) (*ListResult, error) {
	visibility, err := n.refreshVisibility(ctx)
	if err != nil {
		return nil, err
	}

	argsCopy := *args
	argsCopy.ExtraPredicate = publicshare.ToEntPredicate(visibility.Filter)
	n.current = parent
	return n.baseNavigator.children(ctx, parent, &argsCopy)
}

func (n *publicNavigator) Capabilities(isSearching bool) *fs.NavigatorProps {
	capability := publicNavigatorCapability
	if n.current != nil && n.current.Capabilities() != nil {
		capability = n.current.Capabilities()
	}

	res := &fs.NavigatorProps{
		Capability:            capability,
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

func (n *publicNavigator) Walk(ctx context.Context, levelFiles []*File, limit, depth int, f WalkFunc) error {
	return n.baseNavigator.walk(ctx, levelFiles, limit, depth, f)
}

func (n *publicNavigator) FollowTx(ctx context.Context) (func(), error) {
	if _, ok := ctx.Value(inventory.TxCtx{}).(*inventory.Tx); !ok {
		return nil, fmt.Errorf("navigator: no inherited transaction found in context")
	}

	newFileClient, _, _, err := inventory.WithTx(ctx, n.fileClient)
	if err != nil {
		return nil, err
	}

	oldFileClient := n.fileClient
	revert := func() {
		n.fileClient = oldFileClient
		n.baseNavigator.fileClient = oldFileClient
	}

	n.fileClient = newFileClient
	n.baseNavigator.fileClient = newFileClient
	return revert, nil
}

func (n *publicNavigator) ExecuteHook(ctx context.Context, hookType fs.HookType, file *File) error {
	return nil
}

func (n *publicNavigator) GetView(ctx context.Context, file *File) *types.ExplorerView {
	return file.View()
}

func newPublicUri() *fs.URI {
	res, _ := fs.NewUriFromString(fmt.Sprintf("%s://%s", constants.CloudreveScheme, constants.FileSystemPublic))
	return res
}
