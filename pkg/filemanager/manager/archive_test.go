package manager

import (
	"testing"

	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
)

func TestTopLevelArchiveURIs(t *testing.T) {
	uris := mustArchiveURIs(t,
		"cloudreve://my/a/b",
		"cloudreve://my/a",
		"cloudreve://my/c/d",
		"cloudreve://my/c",
		"cloudreve://my/c",
	)

	filtered := topLevelArchiveURIs(uris, "current-user")
	if len(filtered) != 2 {
		t.Fatalf("unexpected filtered uri count: %d", len(filtered))
	}

	if filtered[0].String() != "cloudreve://my/a" || filtered[1].String() != "cloudreve://my/c" {
		t.Fatalf("unexpected filtered uris: %s, %s", filtered[0], filtered[1])
	}
}

func TestTopLevelArchiveURIsKeepDifferentOwners(t *testing.T) {
	uris := mustArchiveURIs(t,
		"cloudreve://owner-a@my/a",
		"cloudreve://owner-b@my/a/b",
	)

	filtered := topLevelArchiveURIs(uris, "current-user")
	if len(filtered) != 2 {
		t.Fatalf("unexpected filtered uri count across owners: %d", len(filtered))
	}
}

func mustArchiveURIs(t *testing.T, raw ...string) []*fs.URI {
	t.Helper()

	res, err := fs.NewUriFromStrings(raw...)
	if err != nil {
		t.Fatalf("failed to parse uris: %v", err)
	}

	return res
}
