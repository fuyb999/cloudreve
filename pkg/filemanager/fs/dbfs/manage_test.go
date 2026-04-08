package dbfs

import (
	"fmt"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
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
