package dbfs

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

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
	projectedRoots []*File
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
	for _, projected := range n.projectedRoots {
		if projected != nil {
			projected.Recycle()
		}
	}
	n.projectedRoots = nil
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

	targetID := file.ID()
	targetOwnerID := file.OwnerID()
	targetPath := strings.TrimSpace(file.Model.TreePath)

	var (
		matched      publicshare.RootGrant
		matchedDepth = -1
	)
	for _, grant := range n.visibility.RootGrants {
		if grant.RootOwnerID != 0 && grant.RootOwnerID != targetOwnerID {
			continue
		}

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

	if parent == n.root && (args == nil || args.Search == nil) {
		n.current = parent
		return n.projectRootChildren(ctx, args, visibility)
	}

	argsCopy := ListArgs{}
	if args != nil {
		argsCopy = *args
	}
	argsCopy.ExtraPredicate = publicshare.ToEntPredicate(visibility.Filter)
	n.current = parent
	return n.baseNavigator.children(ctx, parent, &argsCopy)
}

func (n *publicNavigator) projectRootChildren(ctx context.Context, args *ListArgs, visibility *publicshare.VisibilityResult) (*ListResult, error) {
	for _, projected := range n.projectedRoots {
		if projected != nil {
			projected.Recycle()
		}
	}
	n.projectedRoots = nil

	if visibility == nil || len(visibility.RootGrants) == 0 {
		return &ListResult{
			Files:      nil,
			MixedType:  false,
			Pagination: buildProjectedPagination(args, 0, 0),
		}, nil
	}

	projected := make([]*File, 0, len(visibility.RootGrants))
	seen := make(map[int]struct{}, len(visibility.RootGrants))
	for _, grant := range visibility.RootGrants {
		if grant.RootFileID <= 0 {
			continue
		}
		if _, ok := seen[grant.RootFileID]; ok {
			continue
		}
		seen[grant.RootFileID] = struct{}{}

		file, err := n.projectRootGrant(ctx, grant)
		if err != nil {
			n.l.Warning("Failed to project public root grant %d: %v", grant.RootFileID, err)
			continue
		}

		filtered, ok := n.filter(ctx, file)
		if !ok {
			file.Recycle()
			continue
		}

		projected = append(projected, filtered)
		n.projectedRoots = append(n.projectedRoots, filtered)
	}

	sort.Slice(projected, func(i, j int) bool {
		left, right := projected[i], projected[j]
		if left.Type() != right.Type() {
			return left.Type() == types.FileTypeFolder
		}

		leftPath, rightPath := "", ""
		if left.Path[pathIndexUser] != nil {
			leftPath = left.Path[pathIndexUser].PathTrimmed()
		}
		if right.Path[pathIndexUser] != nil {
			rightPath = right.Path[pathIndexUser].PathTrimmed()
		}
		if !strings.EqualFold(left.Name(), right.Name()) {
			return strings.ToLower(left.Name()) < strings.ToLower(right.Name())
		}
		return leftPath < rightPath
	})

	offset, limit := projectedPageWindow(args, n.config.MaxPageSize, len(projected))
	end := offset + limit
	if end > len(projected) {
		end = len(projected)
	}

	paged := projected
	if offset < len(projected) {
		paged = projected[offset:end]
	} else {
		paged = nil
	}

	return &ListResult{
		Files:      paged,
		MixedType:  hasMixedProjectedTypes(projected),
		Pagination: buildProjectedPagination(args, len(projected), end),
	}, nil
}

func (n *publicNavigator) projectRootGrant(ctx context.Context, grant publicshare.RootGrant) (*File, error) {
	target, err := n.fileClient.GetByID(context.WithValue(ctx, inventory.LoadFileMetadata{}, true), grant.RootFileID)
	if err != nil {
		return nil, fmt.Errorf("failed to load file %d: %w", grant.RootFileID, err)
	}

	relativeElements, err := n.relativeElementsFromPublicRoot(ctx, target)
	if err != nil {
		return nil, err
	}

	projected := newFile(nil, target)
	projected.Parent = n.root
	projected.mu = n.root.mu
	projected.CapabilitiesBs = n.root.CapabilitiesBs

	if n.root.Path[pathIndexRoot] != nil {
		projected.Path[pathIndexRoot] = n.root.Path[pathIndexRoot].Join(relativeElements...)
	}

	projected.Path[pathIndexUser] = newPublicUri().Join(relativeElements...)
	return projected, nil
}

func (n *publicNavigator) relativeElementsFromPublicRoot(ctx context.Context, target *ent.File) ([]string, error) {
	if target == nil {
		return nil, fmt.Errorf("public target is nil")
	}
	if n.root == nil || n.root.Model == nil {
		return nil, fmt.Errorf("public root is not initialized")
	}

	ancestors, err := n.fileClient.GetAncestorFiles(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("failed to load ancestors for %d: %w", target.ID, err)
	}

	rootIndex := -1
	for i, ancestor := range ancestors {
		if ancestor != nil && ancestor.ID == n.root.Model.ID {
			rootIndex = i
			break
		}
	}
	if rootIndex < 0 {
		return nil, fmt.Errorf("target %d is not under public root %d", target.ID, n.root.Model.ID)
	}

	elements := make([]string, 0, len(ancestors)-rootIndex-1)
	for _, ancestor := range ancestors[rootIndex+1:] {
		if ancestor == nil || strings.TrimSpace(ancestor.Name) == "" {
			continue
		}
		elements = append(elements, ancestor.Name)
	}
	if len(elements) == 0 {
		return nil, fmt.Errorf("target %d does not have a projected relative path", target.ID)
	}

	return elements, nil
}

func projectedPageWindow(args *ListArgs, defaultPageSize, total int) (int, int) {
	if total == 0 {
		return 0, 0
	}

	pageSize := defaultPageSize
	if pageSize <= 0 || pageSize > total {
		pageSize = total
	}

	if args == nil || args.Page == nil {
		return 0, pageSize
	}

	if args.Page.PageSize > 0 {
		pageSize = args.Page.PageSize
	}

	if args.Page.UseCursorPagination {
		offset, err := strconv.Atoi(strings.TrimSpace(args.Page.PageToken))
		if err != nil || offset < 0 {
			offset = 0
		}
		return offset, pageSize
	}

	page := args.Page.Page
	if page < 0 {
		page = 0
	}
	return page * pageSize, pageSize
}

func buildProjectedPagination(args *ListArgs, total, end int) *inventory.PaginationResults {
	pageSize := total
	if pageSize <= 0 {
		pageSize = 0
	}
	page := 0
	isCursor := false
	nextToken := ""
	if args != nil && args.Page != nil {
		if args.Page.PageSize > 0 {
			pageSize = args.Page.PageSize
		}
		page = args.Page.Page
		if page < 0 {
			page = 0
		}
		isCursor = args.Page.UseCursorPagination
		if isCursor && end < total {
			nextToken = strconv.Itoa(end)
		}
	}

	return &inventory.PaginationResults{
		Page:          page,
		PageSize:      pageSize,
		TotalItems:    total,
		NextPageToken: nextToken,
		IsCursor:      isCursor,
	}
}

func hasMixedProjectedTypes(files []*File) bool {
	if len(files) <= 1 {
		return false
	}

	hasFolder := false
	hasFile := false
	for _, item := range files {
		if item == nil {
			continue
		}
		if item.Type() == types.FileTypeFolder {
			hasFolder = true
		} else {
			hasFile = true
		}
		if hasFolder && hasFile {
			return true
		}
	}

	return false
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
