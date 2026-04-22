package publicshare

import (
	"context"
	"reflect"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	entuser "github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/cache"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
)

func TestCreateRootFolderUsesRequesterOwnerInsteadOfHiddenRootOwner(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:publicshare-create-root-folder-owner?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	logger := logging.NewConsoleLogger(logging.LevelError)
	if _, err := inventory.InitializeDBClient(
		logger,
		client,
		cache.NewMemoStore("", logger),
		"0.0.1",
		conf.SQLiteDB,
	); err != nil {
		t.Fatalf("failed to initialize db client: %v", err)
	}

	requester, err := client.User.Create().
		SetUsername("public-owner").
		SetEmail("public-owner@example.com").
		SetNick("public-owner").
		SetStatus(entuser.StatusActive).
		SetGroupID(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create requester: %v", err)
	}

	hasher, err := hashid.New("test-salt")
	if err != nil {
		t.Fatalf("failed to create hashid encoder: %v", err)
	}

	service := NewService(
		logging.NewConsoleLogger(logging.LevelError),
		inventory.NewFileClient(client, conf.SQLiteDB, nil),
		inventory.NewSettingClient(client, nil),
		hasher,
	)

	folder, err := service.CreateRootFolder(ctx, requester, "研发文档", &Rule{})
	if err != nil {
		t.Fatalf("failed to create public root folder: %v", err)
	}

	root, err := service.Root(ctx)
	if err != nil {
		t.Fatalf("failed to reload hidden public root: %v", err)
	}

	if root.OwnerID == requester.ID {
		t.Fatalf("hidden public root owner should remain system-owned, got requester %d", requester.ID)
	}
	if folder.OwnerID != requester.ID {
		t.Fatalf("public root folder should use requester owner: got %d want %d", folder.OwnerID, requester.ID)
	}
	if folder.FileChildren != root.ID {
		t.Fatalf("public root folder parent mismatch: got %d want %d", folder.FileChildren, root.ID)
	}
}

func TestBuildVisibilityFilterUsesTreePathOnlyForPublicSubtree(t *testing.T) {
	filter := BuildVisibilityFilter([]RootGrant{
		{RootFileID: 100, RootOwnerID: -1, RootTreePath: "1.100"},
		{RootFileID: 200, RootOwnerID: 88, RootTreePath: "1.200"},
	})

	if filter == nil || filter.Match == nil {
		t.Fatalf("expected visibility filter match")
	}
	if filter.Match.Kind != FileFilterMatchTreePathIn {
		t.Fatalf("unexpected filter kind: %s", filter.Match.Kind)
	}
	if !reflect.DeepEqual(filter.Match.StringValues, []string{"1.100", "1.200"}) {
		t.Fatalf("unexpected tree path prefixes: %+v", filter.Match.StringValues)
	}
}

func TestResolveVisibleURIReturnsPublicSubtreePath(t *testing.T) {
	hasher, err := hashid.New("test-salt")
	if err != nil {
		t.Fatalf("failed to create hasher: %v", err)
	}

	service := NewService(
		logging.NewConsoleLogger(logging.LevelError),
		&adminOverrideTestFileClient{},
		testSettingClient{values: map[string]string{
			PublicRootFileIDSetting: "9",
		}},
		hasher,
	)

	target := &ent.File{ID: 12, Name: "说明.txt", TreePath: "1.9.10.12"}
	service.fileClient = &adminOverrideTestFileClient{
		root: &ent.File{ID: 9, Name: inventory.RootFolderName},
		ancestorByTarget: map[int][]*ent.File{
			12: {
				{ID: 1, Name: inventory.RootFolderName},
				{ID: 9, Name: inventory.RootFolderName},
				{ID: 10, Name: "研发部"},
				{ID: 12, Name: "说明.txt"},
			},
		},
	}

	uri, err := service.ResolveVisibleURI(context.Background(), target, &VisibilityResult{
		RootGrants: []RootGrant{
			{
				RootFileID:   9,
				RootName:     DefaultRootName,
				RootTreePath: "1.9",
				Actions:      map[Action]bool{ActionList: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("failed to resolve visible uri: %v", err)
	}
	if got, want := uri.String(), BuildPublicURI().Join("研发部", "说明.txt").String(); got != want {
		t.Fatalf("unexpected visible uri: got %q want %q", got, want)
	}
}

func TestResolveVisibleURIReturnsProjectedRootAliasPath(t *testing.T) {
	hasher, err := hashid.New("test-salt")
	if err != nil {
		t.Fatalf("failed to create hasher: %v", err)
	}

	service := NewService(
		logging.NewConsoleLogger(logging.LevelError),
		&adminOverrideTestFileClient{},
		testSettingClient{values: map[string]string{
			PublicRootFileIDSetting: "9",
		}},
		hasher,
	)

	target := &ent.File{ID: 22, Name: "方案.docx", TreePath: "1.9.20.22"}
	service.fileClient = &adminOverrideTestFileClient{
		root: &ent.File{ID: 9, Name: inventory.RootFolderName},
		ancestorByTarget: map[int][]*ent.File{
			22: {
				{ID: 1, Name: inventory.RootFolderName},
				{ID: 9, Name: inventory.RootFolderName},
				{ID: 20, Name: "部门空间"},
				{ID: 22, Name: "方案.docx"},
			},
		},
	}

	grant := RootGrant{
		RootFileID:   20,
		RootName:     "部门空间",
		RootTreePath: "1.9.20",
		Actions:      map[Action]bool{ActionList: true},
	}
	uri, err := service.ResolveVisibleURI(context.Background(), target, &VisibilityResult{
		RootGrants: []RootGrant{grant},
	})
	if err != nil {
		t.Fatalf("failed to resolve visible uri: %v", err)
	}
	want := BuildPublicURI().Join(ProjectedRootAlias(hasher, grant), "方案.docx").String()
	if got := uri.String(); got != want {
		t.Fatalf("unexpected projected visible uri: got %q want %q", got, want)
	}
}
