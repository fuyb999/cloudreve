package dbfs

import (
	"encoding/gob"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	"github.com/samber/lo"
)

func init() {
	gob.Register(File{})
	gob.Register(shareNavigatorState{})
	gob.Register(map[string]*File{})
	gob.Register(map[int]*File{})
}

func getDefaultView() *types.ExplorerView {
	return &types.ExplorerView{
		PageSize:  defaultPageSize,
		View:      "grid",
		Thumbnail: true,
	}
}

type (
	File struct {
		Model             *ent.File
		Children          map[string]*File
		Parent            *File
		Path              [2]*fs.URI
		OwnerModel        *ent.User
		IsUserRoot        bool
		CapabilitiesBs    *boolset.BooleanSet
		FileExtendedInfo  *fs.FileExtendedInfo
		FileFolderSummary *fs.FolderSummary

		disableView bool
		mu          *sync.Mutex
	}
)

const (
	MetadataSysPrefix           = "sys:"
	MetadataUploadSessionPrefix = MetadataSysPrefix + "upload_session"
	MetadataUploadSessionID     = MetadataUploadSessionPrefix + "_id"
	MetadataSharedRedirect      = MetadataSysPrefix + "shared_redirect"
	MetadataRestoreUri          = MetadataSysPrefix + "restore_uri"
	MetadataExpectedCollectTime = MetadataSysPrefix + "expected_collect_time"
	MetadataSharedOwner         = MetadataSysPrefix + "shared_owner"

	ThumbMetadataPrefix = "thumb:"
	ThumbDisabledKey    = ThumbMetadataPrefix + "disabled"

	FullTextIndexKey      = MetadataSysPrefix + "fulltext_index"
	FTSSidecarManifestKey = MetadataSysPrefix + "fts_sidecar_manifest"
	FTSSidecarEntityIDKey = MetadataSysPrefix + "fts_sidecar_entity_id"

	pathIndexRoot = 0
	pathIndexUser = 1
)

func (f *File) Name() string {
	if f == nil || f.Model == nil {
		return ""
	}
	return f.Model.Name
}

func (f *File) IsNil() bool {
	return f == nil || f.Model == nil
}

func (f *File) DisplayName() string {
	if uri, ok := f.Metadata()[MetadataRestoreUri]; ok {
		restoreUri, err := fs.NewUriFromString(uri)
		if err != nil {
			return f.Name()
		}

		return path.Base(restoreUri.Path())
	}

	return f.Name()
}

func (f *File) CanHaveChildren() bool {
	return f.Type() == types.FileTypeFolder && !f.IsSymbolic()
}

func (f *File) Ext() string {
	return util.Ext(f.Name())
}

func (f *File) ID() int {
	if f == nil || f.Model == nil {
		return 0
	}
	return f.Model.ID
}

func (f *File) IsSymbolic() bool {
	if f == nil || f.Model == nil {
		return false
	}
	return f.Model.IsSymbolic
}

func (f *File) Type() types.FileType {
	if f == nil || f.Model == nil {
		return 0
	}
	return types.FileType(f.Model.Type)
}

func (f *File) Size() int64 {
	if f == nil || f.Model == nil {
		return 0
	}
	return f.Model.Size
}

func (f *File) SizeUsed() int64 {
	return lo.SumBy(f.Entities(), func(item fs.Entity) int64 {
		return item.Size()
	})
}

func (f *File) UpdatedAt() time.Time {
	if f == nil || f.Model == nil {
		return time.Time{}
	}
	return f.Model.UpdatedAt
}

func (f *File) CreatedAt() time.Time {
	if f == nil || f.Model == nil {
		return time.Time{}
	}
	return f.Model.CreatedAt
}

func (f *File) ExtendedInfo() *fs.FileExtendedInfo {
	return f.FileExtendedInfo
}

func (f *File) Owner() *ent.User {
	parent := f
	for parent != nil {
		if parent.OwnerModel != nil {
			return parent.OwnerModel
		}
		parent = parent.Parent
	}

	return nil
}

func (f *File) OwnerID() int {
	if f == nil || f.Model == nil {
		return 0
	}
	return f.Model.OwnerID
}

func (f *File) Shared() bool {
	if f == nil || f.Model == nil {
		return false
	}
	return len(f.Model.Edges.Shares) > 0
}

func (f *File) Metadata() map[string]string {
	if f == nil || f.Model == nil {
		return nil
	}
	if f.Model.Edges.Metadata == nil {
		return nil
	}
	return lo.Associate(f.Model.Edges.Metadata, func(item *ent.Metadata) (string, string) {
		return item.Name, item.Value
	})
}

// Uri returns the URI of the file.
// If isRoot is true, the URI will be returned from owner's view.
// Otherwise, the URI will be returned from user's view.
func (f *File) Uri(isRoot bool) *fs.URI {
	index := 1
	if isRoot {
		index = 0
	}
	if f.Path[index] != nil || f.Parent == nil {
		return f.Path[index]
	}

	// Find the root file
	elements := make([]string, 0)
	parent := f
	for parent.Parent != nil && parent.Path[index] == nil {
		elements = append([]string{parent.Name()}, elements...)
		parent = parent.Parent
	}

	if parent.Path[index] == nil {
		return nil
	}

	return parent.Path[index].Join(elements...)
}

// View returns the view setting of the file, can be inherited from parent.
func (f *File) View() *types.ExplorerView {
	// If owner has disabled view sync, return nil
	owner := f.Owner()
	if owner != nil && owner.Settings != nil && owner.Settings.DisableViewSync {
		return nil
	}

	// If navigator has disabled view sync, return nil
	userRoot := f.UserRoot()
	if userRoot == nil || userRoot.disableView {
		return nil
	}

	current := f
	for current != nil {
		if current.Model.Props != nil && current.Model.Props.View != nil {
			return current.Model.Props.View
		}
		current = current.Parent
	}

	return getDefaultView()
}

// UserRoot return the root file from user's view.
func (f *File) UserRoot() *File {
	root := f
	for root != nil && !root.IsUserRoot {
		root = root.Parent
	}

	return root
}

// Root return the root file from owner's view.
func (f *File) Root() *File {
	root := f
	for root.Parent != nil {
		root = root.Parent
	}

	return root
}

// RootUri return the URI of the user root file under owner's view.
func (f *File) RootUri() *fs.URI {
	return f.UserRoot().Uri(true)
}

// ResolveOwnerURI 将当前视图下的用户路径重写成 owner 视图下的真实路径。
// 对普通目录它等价于 RootUri + 相对路径；对公共文件的虚拟投影目录，则可以正确绕过别名段。
func (f *File) ResolveOwnerURI(target *fs.URI) *fs.URI {
	if f == nil {
		return nil
	}

	baseOwner := f.Uri(true)
	if baseOwner == nil {
		return nil
	}
	if target == nil {
		return baseOwner
	}

	baseUser := f.Uri(false)
	if baseUser == nil {
		return f.RootUri().JoinRaw(target.PathTrimmed())
	}

	relative := strings.TrimPrefix(target.Path(), baseUser.Path())
	return baseOwner.JoinRaw(relative)
}

func (f *File) Replace(model *ent.File) *File {
	f.mu.Lock()
	delete(f.Parent.Children, f.Model.Name)
	f.mu.Unlock()

	defer f.Recycle()
	replaced := newFile(f.Parent, model)
	if f.IsRootFile() {
		// If target is a root file, the user path should remain the same.
		replaced.Path[pathIndexUser] = f.Path[pathIndexUser]
	}

	return replaced
}

// Ancestors return all ancestors of the file, until the owner root is reached.
func (f *File) Ancestors() []*File {
	return f.AncestorsChain()[1:]
}

// AncestorsChain return all ancestors of the file (including itself), until the owner root is reached.
func (f *File) AncestorsChain() []*File {
	ancestors := make([]*File, 0)
	parent := f
	for parent != nil {
		ancestors = append(ancestors, parent)
		parent = parent.Parent
	}

	return ancestors
}

func (f *File) PolicyID() int {
	root := f
	return root.Model.StoragePolicyFiles
}

// IsRootFolder return true if the file is the root folder under user's view.
func (f *File) IsRootFolder() bool {
	return f.Type() == types.FileTypeFolder && f.IsRootFile()
}

// IsRootFile return true if the file is the root file under user's view.
func (f *File) IsRootFile() bool {
	uri := f.Uri(false)
	p := uri.Path()
	return f.Model.Name == inventory.RootFolderName || p == fs.Separator || p == ""
}

func (f *File) Entities() []fs.Entity {
	return lo.Map(f.Model.Edges.Entities, func(item *ent.Entity, index int) fs.Entity {
		return fs.NewEntity(item)
	})
}

func (f *File) PrimaryEntity() fs.Entity {
	primary, _ := lo.Find(f.Model.Edges.Entities, func(item *ent.Entity) bool {
		return item.Type == int(types.EntityTypeVersion) && item.ID == f.Model.PrimaryEntity
	})
	if primary != nil {
		return fs.NewEntity(primary)
	}

	return fs.NewEmptyEntity(f.Owner())
}

func (f *File) PrimaryEntityID() int {
	return f.Model.PrimaryEntity
}

func (f *File) FolderSummary() *fs.FolderSummary {
	return f.FileFolderSummary
}

func (f *File) Capabilities() *boolset.BooleanSet {
	return f.CapabilitiesBs
}

func resetFileState(f *File, model *ent.File) *File {
	if f.Children == nil {
		f.Children = make(map[string]*File)
	} else {
		clear(f.Children)
	}

	f.Model = model
	f.Parent = nil
	f.Path = [2]*fs.URI{}
	f.OwnerModel = nil
	f.IsUserRoot = false
	f.CapabilitiesBs = nil
	f.FileExtendedInfo = nil
	f.FileFolderSummary = nil
	f.disableView = false
	f.mu = nil
	return f
}

func newFile(parent *File, model *ent.File) *File {
	// 公共文件投影会频繁跨层拼装 File 树。之前这里为了省分配使用 sync.Pool，
	// 但一旦出现重复回收，同一指针就可能在不同请求里被复用，最终导致 nil model / 自引用 / 随机 panic。
	// 当前优先保证稳定性，直接按需分配新对象。
	f := resetFileState(&File{}, model)

	if parent != nil {
		f.Parent = parent
		parent.mu.Lock()
		parent.Children[model.Name] = f
		if parent.Path[pathIndexUser] != nil {
			f.Path[pathIndexUser] = parent.Path[pathIndexUser].Join(model.Name)
		}

		if parent.Path[pathIndexRoot] != nil {
			f.Path[pathIndexRoot] = parent.Path[pathIndexRoot].Join(model.Name)
		}

		f.CapabilitiesBs = parent.CapabilitiesBs
		f.mu = parent.mu
		parent.mu.Unlock()
	} else {
		f.mu = &sync.Mutex{}
	}

	return f
}

func newParentFile(parent *ent.File, child *File) *File {
	newParent := newFile(nil, parent)
	newParent.Children[child.Name()] = child
	child.Parent = newParent
	newParent.mu = child.mu
	return newParent
}

func (f *File) Recycle() {
	if f == nil {
		return
	}

	// 公共文件投影场景里如果误形成了环引用，递归回收会直接 stack overflow。
	// 改成显式栈 + visited 后，即使图结构被污染，也能安全回收并避免重复 Put 同一对象。
	stack := []*File{f}
	visited := make(map[*File]struct{}, 8)
	for len(stack) > 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		if current == nil {
			continue
		}
		if _, ok := visited[current]; ok {
			continue
		}
		visited[current] = struct{}{}

		for _, child := range current.Children {
			if child != nil {
				stack = append(stack, child)
			}
		}

		resetFileState(current, nil)
	}
}
