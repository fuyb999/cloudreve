package inventory

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/oauthclient"
	"github.com/cloudreve/Cloudreve/v4/ent/setting"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
)

func TestNormalizeSyncthingOAuthClientScopesIncludesRequiredScopes(t *testing.T) {
	t.Parallel()

	got := normalizeSyncthingOAuthClientScopes([]string{
		types.ScopeProfile,
		types.ScopeEmail,
		types.ScopeFilesWrite,
		types.ScopeFilesWrite,
		"",
	})

	required := []string{
		types.ScopeOpenID,
		types.ScopeOfflineAccess,
		types.ScopeUserInfoRead,
		types.ScopeFilesRead,
		types.ScopeFilesWrite,
	}
	for _, scope := range required {
		if !containsString(got, scope) {
			t.Fatalf("expected required scope %q in %v", scope, got)
		}
	}
}

func TestMigrateOAuthClientSyncthingUpdatesExistingClientScopes(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:migrate-syncthing-oauth?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	_, err = client.OAuthClient.Create().
		SetGUID(OAuthClientSyncthingGUID).
		SetSecret(OAuthClientSyncthingSecret).
		SetName(OAuthClientSyncthingName).
		SetRedirectUris(oauthClientSyncthingRedirectURIs).
		SetScopes([]string{
			types.ScopeProfile,
			types.ScopeEmail,
			types.ScopeOpenID,
			types.ScopeOfflineAccess,
			types.ScopeUserInfoWrite,
			types.ScopeWorkflowWrite,
			types.ScopeFilesWrite,
			types.ScopeSharesWrite,
		}).
		SetProps(&types.OAuthClientProps{
			RefreshTokenTTL: LegacyOAuthTokenTTLSeconds,
		}).
		SetIsEnabled(true).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create legacy syncthing oauth client: %v", err)
	}

	if err := migrateOAuthClientSyncthing(logging.NewConsoleLogger(logging.LevelError), client, ctx); err != nil {
		t.Fatalf("failed to migrate syncthing oauth client: %v", err)
	}

	got, err := client.OAuthClient.Query().Where(oauthclient.GUID(OAuthClientSyncthingGUID)).First(ctx)
	if err != nil {
		t.Fatalf("failed to query migrated syncthing oauth client: %v", err)
	}

	for _, scope := range []string{types.ScopeUserInfoRead, types.ScopeFilesRead, types.ScopeFilesWrite} {
		if !containsString(got.Scopes, scope) {
			t.Fatalf("expected migrated scope %q in %v", scope, got.Scopes)
		}
	}
	if got.Props == nil {
		t.Fatal("expected oauth client props to be populated")
	}
	if got.Props.RefreshTokenTTL != SyncthingOAuthTokenTTLSeconds {
		t.Fatalf("unexpected refresh token ttl: got %d want %d", got.Props.RefreshTokenTTL, SyncthingOAuthTokenTTLSeconds)
	}
	if got.Props.Description == "" {
		t.Fatal("expected oauth client description to be populated")
	}
}

func TestExpandDefaultArchiveViewerExtsPatch(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:migrate-archive-viewer?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	raw, err := json.Marshal([]types.ViewerGroup{{
		Viewers: []types.Viewer{{
			ID:   "archive",
			Exts: []string{"zip", "7z"},
		}},
	}})
	if err != nil {
		t.Fatalf("failed to marshal file viewers: %v", err)
	}

	if _, err := client.Setting.Create().SetName("file_viewers").SetValue(string(raw)).Save(ctx); err != nil {
		t.Fatalf("failed to seed file_viewers setting: %v", err)
	}

	var patch Patch
	for _, candidate := range patches {
		if candidate.Name == "expand_default_archive_viewer_exts" {
			patch = candidate
			break
		}
	}
	if patch.Func == nil {
		t.Fatal("expected archive viewer migration patch to exist")
	}

	if err := patch.Func(logging.NewConsoleLogger(logging.LevelError), client, ctx); err != nil {
		t.Fatalf("failed to apply archive viewer patch: %v", err)
	}

	fileViewersSetting, err := client.Setting.Query().Where(setting.Name("file_viewers")).First(ctx)
	if err != nil {
		t.Fatalf("failed to query file_viewers setting: %v", err)
	}

	var fileViewers []types.ViewerGroup
	if err := json.Unmarshal([]byte(fileViewersSetting.Value), &fileViewers); err != nil {
		t.Fatalf("failed to unmarshal file_viewers setting: %v", err)
	}

	if len(fileViewers) == 0 || len(fileViewers[0].Viewers) == 0 {
		t.Fatalf("unexpected file viewers setting: %+v", fileViewers)
	}

	archiveViewer := fileViewers[0].Viewers[0]
	for _, ext := range defaultArchiveViewerExts {
		if !containsString(archiveViewer.Exts, ext) {
			t.Fatalf("expected archive viewer ext %q in %v", ext, archiveViewer.Exts)
		}
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
