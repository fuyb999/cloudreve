package publicsvc

import (
	"fmt"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	acl "github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/gin-gonic/gin"
)

type (
	RemoteVisibilityParamCtx struct{}
	RemoteVisibilityService  struct{}

	RemoteCheckParamCtx struct{}
	RemoteCheckService  struct {
		Uri    string     `json:"uri" binding:"required"`
		Action acl.Action `json:"action" binding:"required,oneof=list download upload create rename delete metadata"`
	}

	AdminPublicRootParamCtx struct{}
	AdminPublicRootService  struct{}

	AdminPublicFolderCreateParamCtx struct{}
	AdminPublicFolderCreateService  struct {
		Name string   `json:"name" binding:"required,min=1,max=255"`
		Rule acl.Rule `json:"rule"`
	}

	AdminPublicFolderRuleParamCtx struct{}
	AdminPublicFolderRuleService  struct {
		Uri  string   `json:"uri" binding:"required"`
		Rule acl.Rule `json:"rule"`
	}

	AdminPublicMockStateParamCtx struct{}
	AdminPublicMockStateService  struct {
		State acl.MockState `json:"state"`
	}

	AdminPublicProfileParamCtx struct{}
	AdminPublicProfileService  struct {
		Profile acl.UserProfile `json:"profile" binding:"required"`
	}
)

type PublicFolderResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Owner     string    `json:"owner"`
	OwnerID   int       `json:"owner_id"`
	TreePath  string    `json:"tree_path,omitempty"`
	PublicURI string    `json:"public_uri"`
	OwnerURI  string    `json:"owner_uri"`
	Type      int       `json:"type"`
	Rule      *acl.Rule `json:"rule,omitempty"`
}

type VisibleRootResponse struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	Owner     string              `json:"owner"`
	OwnerID   int                 `json:"owner_id"`
	TreePath  string              `json:"tree_path,omitempty"`
	PublicURI string              `json:"public_uri"`
	Actions   map[acl.Action]bool `json:"actions,omitempty"`
}

type RemoteVisibilityResponse struct {
	PublicURI           string                `json:"public_uri"`
	Visibility          *acl.VisibilityResult `json:"visibility,omitempty"`
	EntFilterAST        *acl.FileFilterExpr   `json:"ent_filter_ast,omitempty"`
	ElasticsearchFilter map[string]any        `json:"elasticsearch_filter,omitempty"`
	MeilisearchFilter   string                `json:"meilisearch_filter,omitempty"`
	VisibleRoots        []VisibleRootResponse `json:"visible_roots,omitempty"`
}

type RemoteCheckResponse struct {
	Uri      string              `json:"uri"`
	Decision *acl.ActionDecision `json:"decision,omitempty"`
}

type PublicRootResponse struct {
	Initialized bool                  `json:"initialized"`
	Root        *PublicFolderResponse `json:"root,omitempty"`
}

func newService(c *gin.Context) *acl.Service {
	dep := dependency.FromContext(c)
	return acl.NewService(dep.Logger(), dep.FileClient(), dep.SettingClient(), dep.HashIDEncoder())
}

func encodedOwner(hasher hashid.Encoder, ownerID int) string {
	return hashid.EncodeUserID(hasher, ownerID)
}

func buildFolderResponse(c *gin.Context, file *ent.File, rule *acl.Rule, ownerBase *fs.URI, isRoot bool) *PublicFolderResponse {
	if file == nil {
		return nil
	}

	dep := dependency.FromContext(c)
	publicURI := acl.BuildPublicURI()
	ownerURI := publicURI
	if ownerBase != nil {
		ownerURI = ownerBase
	}

	if !isRoot {
		publicURI = publicURI.Join(file.Name)
		ownerURI = ownerURI.Join(file.Name)
	}

	if rule == nil {
		rule = acl.RuleFromMetadata(file)
	}

	return &PublicFolderResponse{
		ID:        hashid.EncodeFileID(dep.HashIDEncoder(), file.ID),
		Name:      file.Name,
		Owner:     encodedOwner(dep.HashIDEncoder(), file.OwnerID),
		OwnerID:   file.OwnerID,
		TreePath:  file.TreePath,
		PublicURI: publicURI.String(),
		OwnerURI:  ownerURI.String(),
		Type:      int(file.Type),
		Rule:      rule,
	}
}

func buildVisibleRoots(c *gin.Context, grants []acl.RootGrant) []VisibleRootResponse {
	if len(grants) == 0 {
		return nil
	}

	dep := dependency.FromContext(c)
	res := make([]VisibleRootResponse, 0, len(grants))
	for _, grant := range grants {
		res = append(res, VisibleRootResponse{
			ID:        hashid.EncodeFileID(dep.HashIDEncoder(), grant.RootFileID),
			Name:      grant.RootName,
			Owner:     encodedOwner(dep.HashIDEncoder(), grant.RootOwnerID),
			OwnerID:   grant.RootOwnerID,
			TreePath:  grant.RootTreePath,
			PublicURI: acl.BuildPublicURI().Join(grant.RootName).String(),
			Actions:   grant.Actions,
		})
	}

	return res
}

func parsePublicURI(raw string) (*fs.URI, error) {
	uri, err := fs.NewUriFromString(raw)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeParamErr, "invalid public uri", err)
	}

	if uri.FileSystem() != constants.FileSystemPublic {
		return nil, serializer.NewError(serializer.CodeParamErr, "uri must use cloudreve://public protocol", nil)
	}

	return uri, nil
}

func resolvePublicFile(c *gin.Context, raw string, opts ...fs.Option) (*dbfs.File, *fs.URI, error) {
	uri, err := parsePublicURI(raw)
	if err != nil {
		return nil, nil, err
	}

	dep := dependency.FromContext(c)
	user := inventory.UserFromContext(c)
	m := manager.NewFileManager(dep, user)
	defer m.Recycle()

	baseOpts := []fs.Option{dbfs.WithFilePublicMetadata()}
	baseOpts = append(baseOpts, opts...)

	file, err := m.Get(c, uri, baseOpts...)
	if err != nil {
		return nil, nil, serializer.NewError(serializer.CodeNotFound, "public file not found", err)
	}

	resolved, ok := file.(*dbfs.File)
	if !ok {
		return nil, nil, serializer.NewError(serializer.CodeInternalSetting, "unexpected public file implementation", fmt.Errorf("type %T", file))
	}

	return resolved, uri, nil
}

func resolveTopLevelFolder(c *gin.Context, raw string) (*dbfs.File, error) {
	file, uri, err := resolvePublicFile(c, raw)
	if err != nil {
		return nil, err
	}

	if len(uri.Elements()) != 1 {
		return nil, serializer.NewError(serializer.CodeParamErr, "rule can only be applied to top-level public folders", nil)
	}

	if file.Type() != types.FileTypeFolder {
		return nil, serializer.NewError(serializer.CodeParamErr, "target must be a folder", nil)
	}

	return file, nil
}

func (s *RemoteVisibilityService) Get(c *gin.Context) (*RemoteVisibilityResponse, error) {
	service := newService(c)
	user := inventory.UserFromContext(c)
	visibility, err := service.ResolveVisibility(c, user)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "failed to resolve public visibility", err)
	}

	return &RemoteVisibilityResponse{
		PublicURI:           acl.BuildPublicURI().String(),
		Visibility:          visibility,
		EntFilterAST:        visibility.Filter,
		ElasticsearchFilter: acl.ToElasticsearchFilter(visibility.Filter),
		MeilisearchFilter:   acl.ToMeilisearchFilter(visibility.Filter),
		VisibleRoots:        buildVisibleRoots(c, visibility.RootGrants),
	}, nil
}

func (s *RemoteCheckService) Check(c *gin.Context) (*RemoteCheckResponse, error) {
	uri, err := parsePublicURI(s.Uri)
	if err != nil {
		return nil, err
	}

	dep := dependency.FromContext(c)
	user := inventory.UserFromContext(c)
	m := manager.NewFileManager(dep, user)
	defer m.Recycle()

	targetFile, err := m.Get(c, uri, dbfs.WithFilePublicMetadata())
	if err != nil {
		return &RemoteCheckResponse{
			Uri: s.Uri,
			Decision: &acl.ActionDecision{
				Allowed: false,
				Action:  s.Action,
				Reason:  "target_not_found",
			},
		}, nil
	}

	target, ok := targetFile.(*dbfs.File)
	if !ok {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "unexpected public file implementation", fmt.Errorf("type %T", targetFile))
	}

	service := newService(c)
	decision, err := service.CheckActionByFile(c, user, target.Model, s.Action)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "failed to check public permission", err)
	}

	return &RemoteCheckResponse{
		Uri:      s.Uri,
		Decision: decision,
	}, nil
}

func (s *AdminPublicRootService) Get(c *gin.Context) (*PublicRootResponse, error) {
	service := newService(c)
	root, err := service.Root(c)
	if err != nil {
		return &PublicRootResponse{Initialized: false}, nil
	}

	ownerURI, ownerErr := service.RootOwnerURI(c, root)
	if ownerErr != nil {
		ownerURI = acl.BuildPublicURI()
	}

	return &PublicRootResponse{
		Initialized: true,
		Root:        buildFolderResponse(c, root, nil, ownerURI, true),
	}, nil
}

func (s *AdminPublicRootService) Ensure(c *gin.Context) (*PublicRootResponse, error) {
	service := newService(c)
	user := inventory.UserFromContext(c)
	root, err := service.EnsureRoot(c, user)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "failed to ensure public root", err)
	}

	ownerURI, ownerErr := service.RootOwnerURI(c, root)
	if ownerErr != nil {
		ownerURI = acl.BuildPublicURI()
	}

	return &PublicRootResponse{
		Initialized: true,
		Root:        buildFolderResponse(c, root, nil, ownerURI, true),
	}, nil
}

func ListPublicFolders(c *gin.Context) ([]PublicFolderResponse, error) {
	service := newService(c)
	bindings, err := service.ListRoots(c)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "failed to list public folders", err)
	}

	if len(bindings) == 0 {
		return nil, nil
	}

	root, err := service.Root(c)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "failed to load public root", err)
	}

	ownerURI, ownerErr := service.RootOwnerURI(c, root)
	if ownerErr != nil {
		ownerURI = acl.BuildPublicURI()
	}

	res := make([]PublicFolderResponse, 0, len(bindings))
	for _, binding := range bindings {
		res = append(res, *buildFolderResponse(c, binding.File, binding.Rule, ownerURI, false))
	}

	return res, nil
}

func (s *AdminPublicFolderCreateService) Create(c *gin.Context) (*PublicFolderResponse, error) {
	service := newService(c)
	user := inventory.UserFromContext(c)
	folder, err := service.CreateRootFolder(c, user, s.Name, &s.Rule)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "failed to create public folder", err)
	}

	root, err := service.Root(c)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "failed to reload public root", err)
	}

	ownerURI, ownerErr := service.RootOwnerURI(c, root)
	if ownerErr != nil {
		ownerURI = acl.BuildPublicURI()
	}

	return buildFolderResponse(c, folder, &s.Rule, ownerURI, false), nil
}

func (s *AdminPublicFolderRuleService) Update(c *gin.Context) (*PublicFolderResponse, error) {
	service := newService(c)
	target, err := resolveTopLevelFolder(c, s.Uri)
	if err != nil {
		return nil, err
	}

	if err := service.SaveRule(c, target.Model, &s.Rule); err != nil {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "failed to save public rule", err)
	}

	root, rootErr := service.Root(c)
	if rootErr != nil {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "failed to load public root", rootErr)
	}

	ownerURI, ownerErr := service.RootOwnerURI(c, root)
	if ownerErr != nil {
		ownerURI = acl.BuildPublicURI()
	}

	return buildFolderResponse(c, target.Model, &s.Rule, ownerURI, false), nil
}

func (s *AdminPublicMockStateService) Get(c *gin.Context) (*acl.MockState, error) {
	state, err := newService(c).MockState(c)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "failed to load public mock state", err)
	}

	return state, nil
}

func (s *AdminPublicMockStateService) Update(c *gin.Context) (*acl.MockState, error) {
	service := newService(c)
	if err := service.SaveMockState(c, &s.State); err != nil {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "failed to save public mock state", err)
	}

	state, err := service.MockState(c)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "failed to reload public mock state", err)
	}

	return state, nil
}

func (s *AdminPublicProfileService) Upsert(c *gin.Context) (*acl.MockState, error) {
	state, err := newService(c).UpsertProfile(c, s.Profile)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "failed to upsert public profile", err)
	}

	return state, nil
}
