package explorer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
	"github.com/gin-gonic/gin"
)

func TestBuildFileResponseNormalizesHistoricalPublicTopLevelName(t *testing.T) {
	hasher, err := hashid.New("test-salt")
	if err != nil {
		t.Fatalf("failed to create hasher: %v", err)
	}

	fileID := 24
	dirtyName := "omx-tika-pubtxt-20260501-142102.txt__" + hashid.EncodeFileID(hasher, fileID)
	publicURI, err := fs.NewUriFromString("cloudreve://public/" + dirtyName)
	if err != nil {
		t.Fatalf("failed to build uri: %v", err)
	}

	f := &dbfs.File{
		Model: &ent.File{
			ID:        fileID,
			Name:      dirtyName,
			Type:      int(types.FileTypeFile),
			OwnerID:   1,
			CreatedAt: time.Unix(1700000000, 0),
			UpdatedAt: time.Unix(1700000100, 0),
		},
		Path: [2]*fs.URI{publicURI, publicURI},
		OwnerModel: &ent.User{
			ID: 1,
		},
		CapabilitiesBs: &boolset.BooleanSet{},
	}

	resp := BuildFileResponse(context.Background(), &ent.User{ID: 1}, f, hasher, nil)
	if resp == nil {
		t.Fatal("expected response")
	}
	if got, want := resp.Name, "omx-tika-pubtxt-20260501-142102.txt"; got != want {
		t.Fatalf("unexpected normalized name: got %q want %q", got, want)
	}
	if got, want := resp.Path, constants.CloudreveScheme+"://public/omx-tika-pubtxt-20260501-142102.txt"; got != want {
		t.Fatalf("unexpected normalized path: got %q want %q", got, want)
	}
}

func TestApplyPublicVisibilityForURIsStoresOverrideForPublicURI(t *testing.T) {
	hasher, err := hashid.New("test-salt")
	if err != nil {
		t.Fatalf("failed to create hasher: %v", err)
	}

	dep := testExplorerPublicVisibilityDep{
		hasher: hasher,
		settingClient: testExplorerSettingClient{values: map[string]string{
			publicshare.PublicRootFileIDSetting: "20",
		}},
		fileClient: testExplorerFileClient{
			fileByID: map[int]*ent.File{
				20: {
					ID:       20,
					OwnerID:  7,
					Name:     "公共目录",
					Type:     int(types.FileTypeFolder),
					TreePath: "1.20",
				},
			},
			childrenByRootID: map[int][]*ent.File{
				20: {
					{
						ID:       21,
						OwnerID:  7,
						Name:     "项目",
						Type:     int(types.FileTypeFolder),
						TreePath: "1.20.21",
					},
				},
			},
		},
	}

	gin.SetMode(gin.TestMode)
	publicURI := mustExplorerURI(t, "cloudreve://public/项目/report.txt")

	var visibility *publicshare.VisibilityResult
	router := gin.New()
	router.ContextWithFallback = true
	router.GET("/", func(c *gin.Context) {
		if err := applyPublicVisibilityForURIs(c, dep, &ent.User{
			ID: 7,
			Edges: ent.UserEdges{
				Group: &ent.Group{Permissions: adminPermissions()},
			},
		}, publicURI); err != nil {
			t.Fatalf("failed to apply public visibility: %v", err)
		}
		visibility = publicshare.VisibilityOverrideFromContext(c)
	})
	router.ServeHTTP(httptest.NewRecorder(), mustRequest(t))

	if visibility == nil {
		t.Fatal("expected public visibility override in request context")
	}
	if len(visibility.RootGrants) != 1 || visibility.RootGrants[0].RootFileID != 21 {
		t.Fatalf("unexpected visibility grants: %+v", visibility.RootGrants)
	}
}

func TestApplyPublicVisibilityForURIsSkipsPersonalURI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var visibility *publicshare.VisibilityResult
	router := gin.New()
	router.ContextWithFallback = true
	router.GET("/", func(c *gin.Context) {
		if err := applyPublicVisibilityForURIs(c, testExplorerPublicVisibilityDep{}, &ent.User{ID: 7}, mustExplorerURI(t, "cloudreve:///个人.txt")); err != nil {
			t.Fatalf("unexpected personal uri visibility error: %v", err)
		}
		visibility = publicshare.VisibilityOverrideFromContext(c)
	})
	router.ServeHTTP(httptest.NewRecorder(), mustRequest(t))

	if visibility != nil {
		t.Fatalf("did not expect visibility override for personal uri: %+v", visibility)
	}
}

func mustRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	return req
}

func mustExplorerURI(t *testing.T, raw string) *fs.URI {
	t.Helper()
	uri, err := fs.NewUriFromString(raw)
	if err != nil {
		t.Fatalf("failed to parse uri %q: %v", raw, err)
	}
	return uri
}

func adminPermissions() *boolset.BooleanSet {
	permissions := &boolset.BooleanSet{}
	boolset.Sets(map[types.GroupPermission]bool{types.GroupPermissionIsAdmin: true}, permissions)
	return permissions
}

type testExplorerPublicVisibilityDep struct {
	settingClient inventory.SettingClient
	fileClient    inventory.FileClient
	hasher        hashid.Encoder
}

func (d testExplorerPublicVisibilityDep) Logger() logging.Logger {
	return logging.NewConsoleLogger(logging.LevelError)
}

func (d testExplorerPublicVisibilityDep) SettingClient() inventory.SettingClient {
	return d.settingClient
}

func (d testExplorerPublicVisibilityDep) FileClient() inventory.FileClient {
	return d.fileClient
}

func (d testExplorerPublicVisibilityDep) HashIDEncoder() hashid.Encoder {
	return d.hasher
}

type testExplorerSettingClient struct {
	inventory.SettingClient
	values map[string]string
}

func (c testExplorerSettingClient) Get(ctx context.Context, name string) (string, error) {
	if value, ok := c.values[name]; ok {
		return value, nil
	}
	return "", nil
}

type testExplorerFileClient struct {
	inventory.FileClient
	fileByID         map[int]*ent.File
	childrenByRootID map[int][]*ent.File
}

func (c testExplorerFileClient) GetByID(ctx context.Context, id int) (*ent.File, error) {
	if file, ok := c.fileByID[id]; ok {
		return file, nil
	}
	return nil, &ent.NotFoundError{}
}

func (c testExplorerFileClient) GetChildFiles(ctx context.Context, args *inventory.ListFileParameters, ownerID int, roots ...*ent.File) (*inventory.ListFileResult, error) {
	if len(roots) == 0 || roots[0] == nil {
		return &inventory.ListFileResult{}, nil
	}

	children := append([]*ent.File(nil), c.childrenByRootID[roots[0].ID]...)
	return &inventory.ListFileResult{Files: children}, nil
}
