package dbfs

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/file"
	"github.com/cloudreve/Cloudreve/v4/ent/metadata"
	"github.com/cloudreve/Cloudreve/v4/ent/predicate"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/cache"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/samber/lo"
)

var (
	trashNavigatorCapability = &boolset.BooleanSet{}
	defaultTrashView         = &types.ExplorerView{
		View: "list",
		Columns: []types.ListViewColumn{
			{
				Type: 0,
			},
			{
				Type: 2,
			},
			{
				Type: 8,
			},
			{
				Type: 7,
			},
		},
	}
)

// NewTrashNavigator creates a navigator for user's "trash" file system.
func NewTrashNavigator(u *ent.User, fileClient inventory.FileClient, l logging.Logger, config *setting.DBFS,
	hasher hashid.Encoder) Navigator {
	return &trashNavigator{
		user:          u,
		l:             l,
		fileClient:    fileClient,
		config:        config,
		baseNavigator: newBaseNavigator(fileClient, defaultFilter, u, hasher, config),
	}
}

type trashNavigator struct {
	l          logging.Logger
	user       *ent.User
	fileClient inventory.FileClient
	config     *setting.DBFS

	*baseNavigator
}

func (t *trashNavigator) Recycle() {

}

func (n *trashNavigator) PersistState(kv cache.Driver, key string) {
}

func (n *trashNavigator) RestoreState(s State) error {
	return nil
}

func (t *trashNavigator) To(ctx context.Context, path *fs.URI) (*File, error) {
	// Anonymous user does not have a trash folder.
	if inventory.IsAnonymousUser(t.user) {
		return nil, ErrLoginRequired
	}

	elements := path.Elements()
	if len(elements) > 1 {
		// Trash folder is a flatten tree, only 1 layer is supported.
		return nil, fs.ErrPathNotExist.WithError(fmt.Errorf("invalid Path %q", path))
	}

	if len(elements) == 0 {
		// Trash folder has no root.
		return nil, nil
	}

	current, err := t.findVisibleTrashFileByName(ctx, elements[0])
	if err != nil {
		return nil, fmt.Errorf("failed to walk into %q: %w", elements[0], err)
	}

	current.Path[pathIndexUser] = newTrashUri(current.Model.Name)
	current.Path[pathIndexRoot] = current.Path[pathIndexUser]
	current.OwnerModel = &ent.User{ID: current.Model.OwnerID}
	current.CapabilitiesBs = trashNavigatorCapability
	return current, nil
}

func (t *trashNavigator) Children(ctx context.Context, parent *File, args *ListArgs) (*ListResult, error) {
	if parent != nil {
		return nil, fs.ErrPathNotExist
	}

	res, err := t.listVisibleTrashFiles(ctx, args, "")
	if err != nil {
		return nil, err
	}

	// Adding user uri for each file.
	for i := 0; i < len(res.Files); i++ {
		res.Files[i].Path[pathIndexUser] = newTrashUri(res.Files[i].Model.Name)
		res.Files[i].Path[pathIndexRoot] = res.Files[i].Path[pathIndexUser]
		res.Files[i].CapabilitiesBs = trashNavigatorCapability
	}

	return res, nil
}

func (t *trashNavigator) currentUserHash() string {
	if t == nil || t.user == nil {
		return ""
	}

	return hashid.EncodeUserID(t.hasher, t.user.ID)
}

func (t *trashNavigator) visiblePredicate() predicate.File {
	predicates := []predicate.File{file.OwnerIDEQ(t.user.ID)}
	if userHash := strings.TrimSpace(t.currentUserHash()); userHash != "" {
		predicates = append(predicates, file.HasMetadataWith(
			metadata.Name(MetadataTrashVisibility),
			metadata.Value(userHash),
		))
	}

	return file.Or(predicates...)
}

func (t *trashNavigator) listVisibleTrashFiles(ctx context.Context, args *ListArgs, exactName string) (*ListResult, error) {
	if args == nil {
		args = &ListArgs{}
	}

	page := args.Page
	if page == nil {
		page = &inventory.PaginationArgs{
			PageSize:            defaultPageSize,
			UseCursorPagination: true,
		}
	}

	ctx = context.WithValue(ctx, inventory.LoadFilePublicMetadata{}, true)

	extraPredicate := t.visiblePredicate()
	if exactName != "" {
		extraPredicate = file.And(extraPredicate, file.Name(exactName))
	}
	if args.ExtraPredicate != nil {
		extraPredicate = file.And(extraPredicate, args.ExtraPredicate)
	}

	params := &inventory.ListFileParameters{
		PaginationArgs: page,
		ExtraPredicate: extraPredicate,
	}
	if args.Search != nil {
		params.Search = args.Search
		params.MixedType = true
	}

	res, err := t.fileClient.GetChildFiles(ctx, params, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get children: %w", err)
	}

	return &ListResult{
		Files: lo.Map(res.Files, func(model *ent.File, _ int) *File {
			materialized := newFile(nil, model)
			materialized.Path[pathIndexUser] = newTrashUri(model.Name)
			materialized.Path[pathIndexRoot] = materialized.Path[pathIndexUser]
			materialized.CapabilitiesBs = trashNavigatorCapability
			return materialized
		}),
		MixedType:  res.MixedType,
		Pagination: res.PaginationResults,
	}, nil
}

func (t *trashNavigator) findVisibleTrashFileByName(ctx context.Context, name string) (*File, error) {
	res, err := t.listVisibleTrashFiles(ctx, &ListArgs{
		Page: &inventory.PaginationArgs{
			PageSize:            1,
			UseCursorPagination: true,
		},
	}, name)
	if err != nil {
		return nil, err
	}
	if len(res.Files) == 0 {
		return nil, fs.ErrPathNotExist.WithError(fmt.Errorf("trash file %q not found", name))
	}

	return res.Files[0], nil
}

func (t *trashNavigator) Capabilities(isSearching bool) *fs.NavigatorProps {
	res := &fs.NavigatorProps{
		Capability:            trashNavigatorCapability,
		OrderDirectionOptions: fullOrderDirectionOption,
		OrderByOptions:        fullOrderByOption,
		MaxPageSize:           t.config.MaxPageSize,
	}

	if isSearching {
		res.OrderByOptions = searchLimitedOrderByOption
	}

	return res
}

func (t *trashNavigator) Walk(ctx context.Context, levelFiles []*File, limit, depth int, f WalkFunc) error {
	return t.baseNavigator.walk(ctx, levelFiles, limit, depth, f)
}

func (n *trashNavigator) FollowTx(ctx context.Context) (func(), error) {
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

func (n *trashNavigator) ExecuteHook(ctx context.Context, hookType fs.HookType, file *File) error {
	return nil
}

func (n *trashNavigator) GetView(ctx context.Context, file *File) *types.ExplorerView {
	if view, ok := n.user.Settings.FsViewMap[string(constants.FileSystemTrash)]; ok {
		return &view
	}
	return defaultTrashView
}
