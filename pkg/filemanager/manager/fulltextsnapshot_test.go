package manager

import (
	"context"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
)

func TestBuildFTSSearchPathTextPrefersPublicPath(t *testing.T) {
	ownerURI := mustURI(t, "cloudreve://owner@my/公共文件/研发部/说明.txt")
	publicURI := mustURI(t, "cloudreve://public/研发部/说明.txt")

	pathText := buildFTSSearchPathText(ownerURI, publicURI)
	if got, want := pathText, publicURI.String()+"\n"+ownerURI.String(); got != want {
		t.Fatalf("unexpected path text: got %q want %q", got, want)
	}
}

func TestBuildFTSSearchPathTextKeepsOwnerPathForNonPublicFile(t *testing.T) {
	ownerURI := mustURI(t, "cloudreve://owner@my/docs/readme.txt")

	pathText := buildFTSSearchPathText(ownerURI, nil)
	if got, want := pathText, ownerURI.String(); got != want {
		t.Fatalf("unexpected path text: got %q want %q", got, want)
	}
}

func TestResolvePublicSearchURIBuildsPublicPathFromRootAncestors(t *testing.T) {
	hasher, err := hashid.New("test-salt")
	if err != nil {
		t.Fatalf("failed to create hasher: %v", err)
	}

	fileModel := &ent.File{
		ID:      12,
		OwnerID: 1,
		Name:    "说明.txt",
	}
	fileClient := &testFileClient{
		ancestorByID: map[int][]*ent.File{
			12: {
				{ID: 1, Name: inventory.RootFolderName},
				{ID: 9, Name: publicshare.DefaultRootName},
				{ID: 10, Name: "研发部"},
				{ID: 12, Name: "说明.txt"},
			},
		},
	}
	m := &manager{
		l:      logging.NewConsoleLogger(logging.LevelError),
		dep:    testDep{fileClient: fileClient, settingClient: testSettingClient{values: map[string]string{publicshare.PublicRootFileIDSetting: "9"}}, registry: queue.NewTaskRegistry()},
		hasher: hasher,
	}

	got := m.resolvePublicSearchURI(context.Background(), fileModel)
	if got == nil {
		t.Fatalf("expected public uri")
	}
	want := mustURI(t, "cloudreve://public/研发部/说明.txt")
	if got.String() != want.String() {
		t.Fatalf("unexpected public uri: got %q want %q", got.String(), want.String())
	}
}
