package dbfs

import (
	"context"
	"fmt"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/cache"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

func TestTopLevelMoveCopyTargets(t *testing.T) {
	targets := []navigatorFileTarget{
		{file: mustTestFile(t, 1, "my", "", "/a/b")},
		{file: mustTestFile(t, 2, "my", "", "/a")},
		{file: mustTestFile(t, 3, "my", "", "/c")},
		{file: mustTestFile(t, 4, "my", "", "/a/b/c")},
		{file: mustTestFile(t, 5, "my", "", "/c")},
	}

	filtered := topLevelNavigatorFileTargets(targets, "current-user")
	if len(filtered) != 2 {
		t.Fatalf("unexpected filtered target count: %d", len(filtered))
	}

	if filtered[0].file.ID() != 2 || filtered[1].file.ID() != 3 {
		t.Fatalf("unexpected filtered ids: %d, %d", filtered[0].file.ID(), filtered[1].file.ID())
	}
}

func TestTopLevelMoveCopyTargetsKeepsDifferentOwners(t *testing.T) {
	targets := []navigatorFileTarget{
		{file: mustTestFile(t, 1, "my", "owner-a", "/a")},
		{file: mustTestFile(t, 2, "my", "owner-b", "/a/b")},
	}

	filtered := topLevelNavigatorFileTargets(targets, "current-user")
	if len(filtered) != 2 {
		t.Fatalf("unexpected filtered target count across owners: %d", len(filtered))
	}
}

func TestTopLevelDBFSTargets(t *testing.T) {
	targets := []*File{
		mustTestFile(t, 1, "my", "", "/a/b"),
		mustTestFile(t, 2, "my", "", "/a"),
		mustTestFile(t, 3, "my", "", "/c/d"),
		mustTestFile(t, 4, "my", "", "/c"),
	}

	filtered := topLevelDBFSTargets(targets, "current-user")
	if len(filtered) != 2 {
		t.Fatalf("unexpected filtered target count: %d", len(filtered))
	}

	if filtered[0].ID() != 2 || filtered[1].ID() != 4 {
		t.Fatalf("unexpected filtered ids: %d, %d", filtered[0].ID(), filtered[1].ID())
	}
}

func TestCopyFilesLoadsDestinationOwnerGroupBeforeCapacity(t *testing.T) {
	group := &ent.Group{
		ID:         3,
		MaxStorage: 1024,
		Settings: &types.GroupSetting{
			MaxWalkedFiles: 10,
		},
		Permissions: &boolset.BooleanSet{},
	}
	currentUser := &ent.User{
		ID:      1,
		Storage: 32,
	}
	currentUser.SetGroup(group)

	loadedOwner := &ent.User{
		ID:      2,
		Storage: 64,
	}
	loadedOwner.SetGroup(group)

	publicURI, err := fs.NewUriFromString("cloudreve://public/team")
	if err != nil {
		t.Fatalf("failed to parse public uri: %v", err)
	}
	destination := &File{
		Model: &ent.File{
			ID:      10,
			Name:    "team",
			Type:    int(types.FileTypeFolder),
			OwnerID: loadedOwner.ID,
		},
		Path: [2]*fs.URI{publicURI, publicURI},
	}
	destination.OwnerModel = &ent.User{ID: loadedOwner.ID}

	source := &File{
		Model: &ent.File{
			ID:           20,
			Name:         "report.txt",
			Type:         int(types.FileTypeFile),
			OwnerID:      currentUser.ID,
			FileChildren: 9,
			Size:         8,
		},
		OwnerModel: currentUser,
	}

	dbfs := &DBFS{
		user:       currentUser,
		userClient: &copyFilesTestUserClient{users: map[int]*ent.User{loadedOwner.ID: loadedOwner}},
		ownerCache: make(map[int]*ent.User),
	}
	fileClient := &copyFilesTestFileClient{
		copiedFile: &ent.File{
			ID:           30,
			Name:         source.Name(),
			Type:         int(types.FileTypeFile),
			OwnerID:      loadedOwner.ID,
			FileChildren: destination.ID(),
			Size:         source.Size(),
		},
	}

	newTargets, diff, indexDiff, err := dbfs.copyFiles(
		context.Background(),
		map[Navigator][]*File{
			copyFilesTestNavigator{}: {source},
		},
		destination,
		fileClient,
	)
	if err != nil {
		t.Fatalf("copyFiles returned unexpected error: %v", err)
	}
	if destination.Owner() != loadedOwner {
		t.Fatalf("expected destination owner to be replaced with loaded owner")
	}
	if _, err := destination.Owner().Edges.GroupOrErr(); err != nil {
		t.Fatalf("expected destination owner group to be loaded: %v", err)
	}
	if fileClient.copyCalls != 1 {
		t.Fatalf("expected file client copy to be called once, got %d", fileClient.copyCalls)
	}
	if newTargets[source.ID()] != fileClient.copiedFile {
		t.Fatalf("unexpected copied target map: %#v", newTargets)
	}
	if diff[loadedOwner.ID] != source.Size() {
		t.Fatalf("unexpected storage diff: %#v", diff)
	}
	if indexDiff != nil {
		t.Fatalf("did not expect index diff, got %#v", indexDiff)
	}
}

func TestIndexDiffWalkURIUsesPublicVisibleURIForProjectedFiles(t *testing.T) {
	ownerURI, err := fs.NewUriFromString("cloudreve://my/公共文件/team/report.txt")
	if err != nil {
		t.Fatalf("failed to parse owner uri: %v", err)
	}
	visibleURI, err := fs.NewUriFromString("cloudreve://public/team/report.txt")
	if err != nil {
		t.Fatalf("failed to parse visible uri: %v", err)
	}

	target := &File{
		Model: &ent.File{
			ID:      42,
			Name:    "report.txt",
			Type:    int(types.FileTypeFile),
			OwnerID: 1,
		},
		Path: [2]*fs.URI{
			ownerURI,
			visibleURI,
		},
	}

	got := indexDiffWalkURI(target)
	if got == nil {
		t.Fatalf("expected walk URI")
	}
	if got.String() != visibleURI.String() {
		t.Fatalf("unexpected walk URI: got %q want %q", got.String(), visibleURI.String())
	}
}

func TestCanMoveOrCopyToRestoreAllowsPublicDestination(t *testing.T) {
	src, err := fs.NewUriFromString(fmt.Sprintf("%s://%s/%s", constants.CloudreveScheme, constants.FileSystemTrash, "dummy"))
	if err != nil {
		t.Fatalf("failed to parse source uri: %v", err)
	}

	dstPublic, err := fs.NewUriFromString(fmt.Sprintf("%s://%s/%s", constants.CloudreveScheme, constants.FileSystemPublic, "gate6"))
	if err != nil {
		t.Fatalf("failed to parse public destination uri: %v", err)
	}

	dstMy, err := fs.NewUriFromString(fmt.Sprintf("%s://%s/%s", constants.CloudreveScheme, constants.FileSystemMy, "gate6"))
	if err != nil {
		t.Fatalf("failed to parse my destination uri: %v", err)
	}

	if !canMoveOrCopyTo(src, dstPublic, false) {
		t.Fatalf("expected restore from trash to public to be allowed")
	}
	if !canMoveOrCopyTo(src, dstMy, false) {
		t.Fatalf("expected restore from trash to my to remain allowed")
	}
}

func TestCanMoveOrCopyToShareCopyAllowsMyAndPublic(t *testing.T) {
	src, err := fs.NewUriFromString(fmt.Sprintf("%s://%s/%s", constants.CloudreveScheme, constants.FileSystemShare, "dummy"))
	if err != nil {
		t.Fatalf("failed to parse source uri: %v", err)
	}

	dstPublic, err := fs.NewUriFromString(fmt.Sprintf("%s://%s/%s", constants.CloudreveScheme, constants.FileSystemPublic, "gate6"))
	if err != nil {
		t.Fatalf("failed to parse public destination uri: %v", err)
	}

	dstMy, err := fs.NewUriFromString(fmt.Sprintf("%s://%s/%s", constants.CloudreveScheme, constants.FileSystemMy, "gate6"))
	if err != nil {
		t.Fatalf("failed to parse my destination uri: %v", err)
	}

	if !canMoveOrCopyTo(src, dstPublic, true) {
		t.Fatalf("expected copy from share to public to be allowed")
	}
	if !canMoveOrCopyTo(src, dstMy, true) {
		t.Fatalf("expected copy from share to my to be allowed")
	}
}

func TestShouldQueueFullTextCopyUsesExistingIndexMetadata(t *testing.T) {
	if !shouldQueueFullTextCopy(
		map[string]string{FullTextIndexKey: "fts-doc-1"},
		0,
		false,
		setting.FTSExtractorTypeNone,
		nil,
	) {
		t.Fatalf("expected existing index metadata to force full_text_copy queue")
	}
}

func TestShouldQueueFullTextCopyQueuesTikaEligibleFileWithoutIndexMetadata(t *testing.T) {
	if !shouldQueueFullTextCopy(
		map[string]string{},
		1024,
		true,
		setting.FTSExtractorTypeTika,
		&setting.FTSTikaExtractorSetting{MaxFileSize: 2048},
	) {
		t.Fatalf("expected tika-eligible copied file without index metadata to queue full_text_copy")
	}
}

func TestShouldQueueFullTextCopySkipsEmptyOrOversizedTikaFileWithoutIndexMetadata(t *testing.T) {
	if shouldQueueFullTextCopy(
		map[string]string{},
		0,
		true,
		setting.FTSExtractorTypeTika,
		&setting.FTSTikaExtractorSetting{MaxFileSize: 2048},
	) {
		t.Fatalf("expected empty copied file to skip full_text_copy")
	}

	if shouldQueueFullTextCopy(
		map[string]string{},
		4096,
		true,
		setting.FTSExtractorTypeTika,
		&setting.FTSTikaExtractorSetting{MaxFileSize: 2048},
	) {
		t.Fatalf("expected oversized copied file to skip full_text_copy")
	}
}

func TestShouldQueueFullTextCopySkipsWhenFTSDisabledWithoutExistingIndex(t *testing.T) {
	if shouldQueueFullTextCopy(
		map[string]string{},
		1024,
		false,
		setting.FTSExtractorTypeTika,
		&setting.FTSTikaExtractorSetting{MaxFileSize: 2048},
	) {
		t.Fatalf("expected copied file without existing index metadata to skip queue when FTS is disabled")
	}
}

func TestIsPublicFileMutationTarget(t *testing.T) {
	publicURI, err := fs.NewUriFromString("cloudreve://public/team-alpha")
	if err != nil {
		t.Fatalf("failed to parse public uri: %v", err)
	}
	myURI, err := fs.NewUriFromString("cloudreve://my/docs")
	if err != nil {
		t.Fatalf("failed to parse my uri: %v", err)
	}

	if !isPublicFileMutationTarget(&File{
		Model: &ent.File{ID: 1, Name: "team-alpha"},
		Path:  [2]*fs.URI{publicURI, publicURI},
	}) {
		t.Fatal("expected public file target to be recognized")
	}
	if isPublicFileMutationTarget(&File{
		Model: &ent.File{ID: 2, Name: "docs"},
		Path:  [2]*fs.URI{myURI, myURI},
	}) {
		t.Fatal("did not expect my file target to be recognized as public")
	}
	if isPublicFileMutationTarget(nil) {
		t.Fatal("did not expect nil target to be public")
	}
}

func mustTestFile(t *testing.T, id int, host, userInfo, filePath string) *File {
	t.Helper()

	raw := fmt.Sprintf("cloudreve://%s%s%s", userInfoPrefix(userInfo), host, filePath)
	uri, err := fs.NewUriFromString(raw)
	if err != nil {
		t.Fatalf("failed to parse uri %q: %v", raw, err)
	}

	return &File{
		Model:    &ent.File{ID: id, Name: uri.Name()},
		Children: map[string]*File{},
		Path: [2]*fs.URI{
			uri,
			uri,
		},
	}
}

func userInfoPrefix(userInfo string) string {
	if userInfo == "" {
		return ""
	}

	return userInfo + "@"
}

type copyFilesTestNavigator struct{}

func (copyFilesTestNavigator) Recycle() {}

func (copyFilesTestNavigator) To(context.Context, *fs.URI) (*File, error) {
	return nil, fmt.Errorf("not implemented")
}

func (copyFilesTestNavigator) Children(context.Context, *File, *ListArgs) (*ListResult, error) {
	return nil, fmt.Errorf("not implemented")
}

func (copyFilesTestNavigator) Capabilities(bool) *fs.NavigatorProps { return nil }

func (copyFilesTestNavigator) Walk(ctx context.Context, levelFiles []*File, limit, depth int, f WalkFunc) error {
	return f(levelFiles, 0)
}

func (copyFilesTestNavigator) PersistState(cache.Driver, string) {}

func (copyFilesTestNavigator) RestoreState(State) error { return nil }

func (copyFilesTestNavigator) FollowTx(context.Context) (func(), error) {
	return func() {}, nil
}

func (copyFilesTestNavigator) ExecuteHook(context.Context, fs.HookType, *File) error { return nil }

func (copyFilesTestNavigator) GetView(context.Context, *File) *types.ExplorerView { return nil }

type copyFilesTestUserClient struct {
	inventory.UserClient
	users map[int]*ent.User
}

func (c *copyFilesTestUserClient) GetByID(ctx context.Context, id int) (*ent.User, error) {
	user, ok := c.users[id]
	if !ok {
		return nil, fmt.Errorf("user %d not found", id)
	}
	if _, ok := ctx.Value(inventory.LoadUserGroup{}).(bool); !ok {
		return nil, fmt.Errorf("expected LoadUserGroup context")
	}
	return user, nil
}

type copyFilesTestFileClient struct {
	inventory.FileClient
	copiedFile *ent.File
	copyCalls  int
}

func (c *copyFilesTestFileClient) Copy(ctx context.Context, args *inventory.CopyParameter) (map[int][]*ent.File, inventory.StorageDiff, error) {
	c.copyCalls++
	if len(args.Files) != 1 {
		return nil, nil, fmt.Errorf("expected one source file, got %d", len(args.Files))
	}
	source := args.Files[0]
	dstAncestors := args.DstMap[source.FileChildren]
	if len(dstAncestors) == 0 || dstAncestors[0] == nil {
		return nil, nil, fmt.Errorf("destination map was not populated")
	}

	return map[int][]*ent.File{
		source.ID: {c.copiedFile},
	}, inventory.StorageDiff{
		dstAncestors[0].OwnerID: source.Size,
	}, nil
}
