package dbfs

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/file"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/cache"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/samber/lo"
)

var publicNavigatorCapability = &boolset.BooleanSet{}

func init() {
	boolset.Sets(map[NavigatorCapability]bool{
		NavigatorCapabilityCreateFile:     true,
		NavigatorCapabilityRenameFile:     true,
		NavigatorCapabilityCopyFile:       true,
		NavigatorCapabilityMoveFile:       true,
		NavigatorCapabilityDirectLink:     true,
		NavigatorCapabilityCreateArchive:  true,
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
	n.recycleProjectedRoots()
	if n.root != nil {
		n.root.Recycle()
	}
}

func (n *publicNavigator) recycleProjectedRoots() {
	for _, projected := range n.projectedRoots {
		if projected == nil {
			continue
		}

		// 根页投影出来的节点里，有一部分会顺手挂进 root.Children 作为路径缓存；
		// 如果这里直接 Recycle，而 root 还持有该引用，后面 root.Recycle() 会再次回收同一对象，
		// 进而把同一个 *File 重复放回对象池，最终污染对象图。
		if parent := projected.Parent; parent != nil && parent.mu != nil {
			cacheKey := n.projectedCacheKey(projected)
			parent.mu.Lock()
			if current, ok := parent.Children[cacheKey]; ok && current == projected {
				delete(parent.Children, cacheKey)
			} else if current, ok := parent.Children[projected.Name()]; ok && current == projected {
				delete(parent.Children, projected.Name())
			}
			parent.mu.Unlock()
		}

		projected.Recycle()
	}
	n.projectedRoots = nil
}

func (n *publicNavigator) projectedCacheKey(projected *File) string {
	if projected == nil {
		return ""
	}
	if projected.Parent == n.root && projected.Path[pathIndexUser] != nil {
		if alias := strings.TrimSpace(projected.Path[pathIndexUser].Name()); alias != "" {
			return alias
		}
	}
	return projected.Name()
}

func (n *publicNavigator) PersistState(kv cache.Driver, key string) {
	// 公共文件导航包含大量请求期内的投影节点和 alias 缓存。
	// 这些对象一旦跨请求共享，就会在高频刷新时相互回收/覆盖，导致 nil model、死等和随机 panic。
	// 因此公共文件禁用 navigator 状态缓存，每次请求都重新构建 root。
	n.disableRecycle = false
	n.persist = nil
}

func (n *publicNavigator) RestoreState(s State) error {
	n.disableRecycle = false
	n.root = nil
	return nil
}

func (n *publicNavigator) refreshVisibility(ctx context.Context) (*publicshare.VisibilityResult, error) {
	visibility, err := n.publicService.ResolveVisibility(ctx, n.user)
	if err != nil {
		return nil, err
	}

	n.visibility = visibility
	return visibility, nil
}

func (n *publicNavigator) fallbackRootCapabilities() *boolset.BooleanSet {
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

func (n *publicNavigator) rootCapabilities(ctx context.Context) *boolset.BooleanSet {
	if n.root == nil || n.root.Model == nil || n.root.ID() <= 0 {
		return n.fallbackRootCapabilities()
	}

	decision, err := n.publicService.CheckActionByFile(ctx, n.user, n.root.Model, publicshare.ActionCreate)
	if err != nil || decision == nil {
		return n.fallbackRootCapabilities()
	}

	capabilities := capabilitySetFromActions(decision.Actions)
	if capabilities == nil {
		return n.fallbackRootCapabilities()
	}

	// 隐藏根本身始终允许进入和查看；真正能否在其下创建一级目录由授权服务返回的动作集控制。
	boolset.Sets(map[NavigatorCapability]bool{
		NavigatorCapabilityListChildren: true,
		NavigatorCapabilityEnterFolder:  true,
		NavigatorCapabilityInfo:         true,
	}, capabilities)
	return capabilities
}

func capabilitySetFromActions(actions map[publicshare.Action]bool) *boolset.BooleanSet {
	res := &boolset.BooleanSet{}
	boolset.Sets(map[NavigatorCapability]bool{
		NavigatorCapabilityListChildren:   true,
		NavigatorCapabilityEnterFolder:    true,
		NavigatorCapabilityInfo:           true,
		NavigatorCapabilityGenerateThumb:  actions[publicshare.ActionDownload],
		NavigatorCapabilityDownloadFile:   actions[publicshare.ActionDownload],
		NavigatorCapabilityDirectLink:     actions[publicshare.ActionDirectLink],
		NavigatorCapabilityCreateArchive:  actions[publicshare.ActionArchive],
		NavigatorCapabilityUploadFile:     actions[publicshare.ActionUpload],
		NavigatorCapabilityCreateFile:     actions[publicshare.ActionCreate],
		NavigatorCapabilityRenameFile:     actions[publicshare.ActionRename],
		NavigatorCapabilityCopyFile:       actions[publicshare.ActionCopy],
		NavigatorCapabilityMoveFile:       actions[publicshare.ActionMove],
		NavigatorCapabilityDeleteFile:     actions[publicshare.ActionDelete],
		NavigatorCapabilitySoftDelete:     actions[publicshare.ActionDelete],
		NavigatorCapabilityShare:          actions[publicshare.ActionShare],
		NavigatorCapabilityUpdateMetadata: actions[publicshare.ActionMetadata],
		NavigatorCapabilityModifyProps:    actions[publicshare.ActionMetadata],
		NavigatorCapabilityLockFile: actions[publicshare.ActionUpload] || actions[publicshare.ActionCreate] ||
			actions[publicshare.ActionRename] || actions[publicshare.ActionDelete] || actions[publicshare.ActionMetadata] ||
			actions[publicshare.ActionCopy] || actions[publicshare.ActionMove] || actions[publicshare.ActionShare],
	}, res)
	return res
}

func capabilitySetFromGrant(file *File, grant publicshare.RootGrant) *boolset.BooleanSet {
	actions := grant.Actions
	deleteAllowed := false
	if file != nil {
		deleteAllowed = publicshare.RootGrantActionAllowed(file.ID(), grant, publicshare.ActionDelete)
	}

	res := &boolset.BooleanSet{}
	boolset.Sets(map[NavigatorCapability]bool{
		NavigatorCapabilityListChildren:   true,
		NavigatorCapabilityEnterFolder:    true,
		NavigatorCapabilityInfo:           true,
		NavigatorCapabilityGenerateThumb:  actions[publicshare.ActionDownload],
		NavigatorCapabilityDownloadFile:   actions[publicshare.ActionDownload],
		NavigatorCapabilityDirectLink:     actions[publicshare.ActionDirectLink],
		NavigatorCapabilityCreateArchive:  actions[publicshare.ActionArchive],
		NavigatorCapabilityUploadFile:     actions[publicshare.ActionUpload],
		NavigatorCapabilityCreateFile:     actions[publicshare.ActionCreate],
		NavigatorCapabilityRenameFile:     actions[publicshare.ActionRename],
		NavigatorCapabilityCopyFile:       actions[publicshare.ActionCopy],
		NavigatorCapabilityMoveFile:       actions[publicshare.ActionMove],
		NavigatorCapabilityDeleteFile:     deleteAllowed,
		NavigatorCapabilitySoftDelete:     deleteAllowed,
		NavigatorCapabilityShare:          actions[publicshare.ActionShare],
		NavigatorCapabilityUpdateMetadata: actions[publicshare.ActionMetadata],
		NavigatorCapabilityModifyProps:    actions[publicshare.ActionMetadata],
		NavigatorCapabilityLockFile: actions[publicshare.ActionUpload] || actions[publicshare.ActionCreate] ||
			actions[publicshare.ActionRename] || deleteAllowed || actions[publicshare.ActionMetadata] ||
			actions[publicshare.ActionCopy] || actions[publicshare.ActionMove] || actions[publicshare.ActionShare],
	}, res)
	return res
}

func (n *publicNavigator) grantForFile(file *File) (publicshare.RootGrant, bool) {
	if file == nil || file.IsNil() || n.visibility == nil {
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
	if file == nil || file.IsNil() {
		return nil, false
	}

	if file == n.root {
		file.CapabilitiesBs = n.rootCapabilities(ctx)
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

	file.CapabilitiesBs = capabilitySetFromGrant(file, grant)
	return file, true
}

func (n *publicNavigator) To(ctx context.Context, path *fs.URI) (*File, error) {
	if n.root != nil && n.root.IsNil() {
		n.root = nil
	}
	if n.root == nil {
		rootModel, err := n.publicService.Root(ctx)
		if err != nil {
			// 公共文件真实根是系统隐藏根。
			// 当它尚未初始化时，任何已登录用户进入公共文件都允许触发一次补建，
			// 避免系统首次使用还依赖管理员先手动点开公共文件。
			if n.user != nil {
				rootModel, err = n.publicService.EnsureRoot(ctx, n.user)
				if err != nil {
					rootModel = nil
				}
			}
		}

		rootUri := newPublicUri()
		ownerUri := rootUri
		if rootModel != nil {
			ownerUri, err = n.publicService.RootOwnerURI(ctx, rootModel)
		}
		if err != nil || rootModel == nil {
			ownerUri = rootUri
		}

		if rootModel != nil {
			displayRoot := rootModel
			if rootModel.Name == inventory.RootFolderName {
				cloned := *rootModel
				cloned.Name = publicshare.DefaultRootName
				displayRoot = &cloned
			}
			n.root = newFile(nil, displayRoot)
		} else {
			n.root = newFile(nil, &ent.File{
				Name:    publicshare.DefaultRootName,
				Type:    int(types.FileTypeFolder),
				OwnerID: lo.Ternary(n.user != nil, n.user.ID, 0),
			})
		}
		n.root.Path[pathIndexRoot] = ownerUri
		n.root.Path[pathIndexUser] = rootUri
		if rootModel != nil {
			n.root.OwnerModel = &ent.User{ID: rootModel.OwnerID}
		} else {
			n.root.OwnerModel = n.user
		}
		n.root.IsUserRoot = true
		n.root.CapabilitiesBs = n.rootCapabilities(ctx)
	}

	if _, err := n.refreshVisibility(ctx); err != nil {
		return nil, fmt.Errorf("failed to resolve public visibility: %w", err)
	}

	current, lastAncestor := n.root, n.root
	elements := path.Elements()
	for index, element := range elements {
		lastAncestor = current
		if current == n.root {
			if cached, ok := n.projectedRootFromCache(element); ok {
				filtered, visible := n.filter(ctx, cached)
				if !visible {
					return lastAncestor, fs.ErrPathNotExist.WithError(fmt.Errorf("public file is not visible"))
				}
				current = filtered
				continue
			}

			projected, ok, projectErr := n.resolveProjectedTopLevel(ctx, element)
			if projectErr != nil {
				return lastAncestor, projectErr
			}
			if ok {
				filtered, visible := n.filter(ctx, projected)
				if !visible {
					return lastAncestor, fs.ErrPathNotExist.WithError(fmt.Errorf("public file is not visible"))
				}
				current = filtered
				continue
			}
		}

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

func (n *publicNavigator) projectedRootFromCache(alias string) (*File, bool) {
	if n.root == nil || n.root.mu == nil {
		return nil, false
	}

	n.root.mu.Lock()
	defer n.root.mu.Unlock()
	child, ok := n.root.Children[alias]
	return child, ok
}

func (n *publicNavigator) resolveProjectedTopLevel(ctx context.Context, alias string) (*File, bool, error) {
	for _, grant := range n.visibility.RootGrants {
		if n.root != nil && n.root.Model != nil && grant.RootFileID == n.root.Model.ID {
			continue
		}
		if publicshare.ProjectedRootAlias(n.hasher, grant) != alias {
			continue
		}

		file, err := n.projectRootGrant(ctx, grant)
		if err != nil {
			return nil, false, fmt.Errorf("failed to resolve projected public root %d: %w", grant.RootFileID, err)
		}
		return file, true, nil
	}

	return nil, false, nil
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
	n.recycleProjectedRoots()

	if visibility == nil || len(visibility.RootGrants) == 0 {
		return &ListResult{
			Files:      nil,
			MixedType:  false,
			Pagination: buildProjectedPagination(args, 0, 0),
		}, nil
	}

	displayGrants := topLevelProjectedRootGrants(visibility.RootGrants)
	projected := make([]*File, 0, len(displayGrants))
	seen := make(map[int]struct{}, len(displayGrants))
	expandedPublicRoot := false
	for _, grant := range displayGrants {
		if grant.RootFileID <= 0 {
			continue
		}

		// 当授权根就是“真实公共根目录”时，根页应该展示它当前可见的一级子节点，
		// 而不是把公共根自身再投影成一个列表项。
		if n.root != nil && n.root.Model != nil && grant.RootFileID == n.root.Model.ID {
			if expandedPublicRoot {
				continue
			}
			expandedPublicRoot = true

			children, err := n.projectPublicRootGrantChildren(ctx, visibility)
			if err != nil {
				n.l.Warning("Failed to expand public root grant %d: %v", grant.RootFileID, err)
				continue
			}

			for _, child := range children {
				if child == nil || child.IsNil() {
					continue
				}
				if _, ok := seen[child.ID()]; ok {
					child.Recycle()
					continue
				}

				seen[child.ID()] = struct{}{}
				projected = append(projected, child)
				n.projectedRoots = append(n.projectedRoots, child)
			}
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
		if file.IsNil() {
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

	sortProjectedRootChildren(projected, args)

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
		Files: paged,
		// 公共根目录按普通目录语义返回，前端 grid 视图会继续把文件夹置顶、文件置底分区展示。
		MixedType:  false,
		Pagination: buildProjectedPagination(args, len(projected), end),
	}, nil
}

func (n *publicNavigator) projectPublicRootGrantChildren(ctx context.Context, visibility *publicshare.VisibilityResult) ([]*File, error) {
	if n.root == nil || n.root.Model == nil {
		return nil, fmt.Errorf("public root is not initialized")
	}

	listCtx := context.WithValue(ctx, inventory.LoadFileMetadata{}, true)
	pageToken := ""
	res := make([]*File, 0)
	filter := publicshare.ToEntPredicate(visibility.Filter)
	pageSize := n.config.MaxPageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}

	for {
		children, err := n.fileClient.GetChildFiles(listCtx, &inventory.ListFileParameters{
			PaginationArgs: &inventory.PaginationArgs{
				PageSize:            pageSize,
				UseCursorPagination: true,
				PageToken:           pageToken,
			},
			ExtraPredicate: filter,
		}, n.user.ID, n.root.Model)
		if err != nil {
			return nil, fmt.Errorf("failed to load public root children: %w", err)
		}

		for _, model := range children.Files {
			if model == nil {
				continue
			}
			file := newFile(n.root, model)
			if file.IsNil() {
				continue
			}
			if ownerURI, ownerErr := n.ownerURIForTarget(ctx, model); ownerErr == nil {
				file.Path[pathIndexRoot] = ownerURI
			}
			filtered, ok := n.filter(ctx, file)
			if !ok {
				file.Recycle()
				continue
			}
			res = append(res, filtered)
		}

		if children.NextPageToken == "" {
			break
		}
		pageToken = children.NextPageToken
	}

	return res, nil
}

func (n *publicNavigator) projectRootGrant(ctx context.Context, grant publicshare.RootGrant) (*File, error) {
	target, err := n.fileClient.GetByID(context.WithValue(ctx, inventory.LoadFileMetadata{}, true), grant.RootFileID)
	if err != nil {
		return nil, fmt.Errorf("failed to load file %d: %w", grant.RootFileID, err)
	}
	if target == nil {
		return nil, fmt.Errorf("file %d is empty", grant.RootFileID)
	}
	if _, err := n.relativeElementsFromPublicRoot(ctx, target); err != nil {
		return nil, err
	}

	projected := newFile(nil, target)
	projected.Parent = n.root
	projected.mu = n.root.mu
	projected.CapabilitiesBs = n.root.CapabilitiesBs

	ownerURI, err := n.ownerURIForTarget(ctx, target)
	if err != nil {
		return nil, err
	}
	projected.Path[pathIndexRoot] = ownerURI

	alias := publicshare.ProjectedRootAlias(n.hasher, grant)
	projected.Path[pathIndexUser] = newPublicUri().Join(alias)
	if n.root != nil && n.root.mu != nil {
		n.root.mu.Lock()
		n.root.Children[alias] = projected
		n.root.mu.Unlock()
	}
	return projected, nil
}

func topLevelProjectedRootGrants(grants []publicshare.RootGrant) []publicshare.RootGrant {
	if len(grants) <= 1 {
		return grants
	}

	sorted := append([]publicshare.RootGrant(nil), grants...)
	sort.SliceStable(sorted, func(i, j int) bool {
		leftDepth := projectedGrantDepth(sorted[i])
		rightDepth := projectedGrantDepth(sorted[j])
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		if sorted[i].RootOwnerID != sorted[j].RootOwnerID {
			return sorted[i].RootOwnerID < sorted[j].RootOwnerID
		}
		if sorted[i].RootTreePath != sorted[j].RootTreePath {
			return sorted[i].RootTreePath < sorted[j].RootTreePath
		}
		return sorted[i].RootFileID < sorted[j].RootFileID
	})

	filtered := make([]publicshare.RootGrant, 0, len(sorted))
	for _, grant := range sorted {
		skip := false
		for _, existing := range filtered {
			if existing.RootOwnerID != grant.RootOwnerID {
				continue
			}
			if publicshare.RootGrantWithinTree(existing.RootTreePath, grant) {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, grant)
		}
	}

	return filtered
}

func projectedGrantDepth(grant publicshare.RootGrant) int {
	treePath := strings.TrimSpace(grant.RootTreePath)
	if treePath == "" {
		return int(^uint(0) >> 1)
	}

	return len(strings.Split(treePath, "."))
}

func (n *publicNavigator) ownerURIForTarget(ctx context.Context, target *ent.File) (*fs.URI, error) {
	if target == nil {
		return nil, fmt.Errorf("public target is nil")
	}

	ancestors, err := n.fileClient.GetAncestorFiles(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("failed to load owner ancestors for %d: %w", target.ID, err)
	}

	ownerURI := newMyUri()
	if n.user == nil || target.OwnerID != n.user.ID {
		ownerURI = newMyIDUri(hashid.EncodeUserID(n.hasher, target.OwnerID))
	}

	for _, ancestor := range ancestors {
		if ancestor == nil || ancestor.Name == inventory.RootFolderName {
			continue
		}
		ownerURI = ownerURI.Join(ancestor.Name)
	}

	return ownerURI, nil
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

func sortProjectedRootChildren(files []*File, args *ListArgs) {
	orderBy := file.FieldID
	orderDirection := inventory.OrderDirectionAsc
	if args != nil && args.Page != nil {
		if strings.TrimSpace(args.Page.OrderBy) != "" {
			orderBy = strings.TrimSpace(args.Page.OrderBy)
		}
		if args.Page.Order != "" {
			orderDirection = args.Page.Order
		}
	}

	descending := orderDirection == inventory.OrderDirectionDesc
	sort.SliceStable(files, func(i, j int) bool {
		left, right := files[i], files[j]
		if left == nil || left.IsNil() {
			return false
		}
		if right == nil || right.IsNil() {
			return true
		}

		// 与个人目录一致：目录始终在文件前面，排序字段只作用于同类型项。
		if left.Type() != right.Type() {
			return left.Type() == types.FileTypeFolder
		}

		cmp := compareProjectedRootFile(left, right, orderBy)
		if cmp == 0 {
			cmp = compareProjectedRootPath(left, right)
		}
		if descending {
			return cmp > 0
		}
		return cmp < 0
	})
}

func compareProjectedRootFile(left, right *File, orderBy string) int {
	switch orderBy {
	case file.FieldName:
		if cmp := compareCaseFolded(left.Name(), right.Name()); cmp != 0 {
			return cmp
		}
	case file.FieldSize:
		if cmp := compareInt64(left.Size(), right.Size()); cmp != 0 {
			return cmp
		}
	case file.FieldUpdatedAt:
		if cmp := compareTime(left.UpdatedAt(), right.UpdatedAt()); cmp != 0 {
			return cmp
		}
	default:
		// 当前个人目录 created_at/default 最终也落到 ID 排序，这里保持一致，避免公共目录行为分叉。
	}

	return compareInt(left.ID(), right.ID())
}

func compareProjectedRootPath(left, right *File) int {
	leftPath, rightPath := "", ""
	if left.Path[pathIndexUser] != nil {
		leftPath = left.Path[pathIndexUser].PathTrimmed()
	}
	if right.Path[pathIndexUser] != nil {
		rightPath = right.Path[pathIndexUser].PathTrimmed()
	}
	return strings.Compare(leftPath, rightPath)
}

func compareCaseFolded(left, right string) int {
	leftFolded := strings.ToLower(strings.TrimSpace(left))
	rightFolded := strings.ToLower(strings.TrimSpace(right))
	if leftFolded != rightFolded {
		return strings.Compare(leftFolded, rightFolded)
	}
	return strings.Compare(left, right)
}

func compareInt(left, right int) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func compareInt64(left, right int64) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func compareTime(left, right time.Time) int {
	switch {
	case left.Before(right):
		return -1
	case left.After(right):
		return 1
	default:
		return 0
	}
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
	if n.user != nil && n.user.Settings != nil {
		if view, ok := n.user.Settings.FsViewMap[string(constants.FileSystemPublic)]; ok {
			return &view
		}
	}
	return getDefaultView()
}

func newPublicUri() *fs.URI {
	res, _ := fs.NewUriFromString(fmt.Sprintf("%s://%s", constants.CloudreveScheme, constants.FileSystemPublic))
	return res
}
