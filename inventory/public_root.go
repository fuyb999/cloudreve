package inventory

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	entauditlog "github.com/cloudreve/Cloudreve/v4/ent/auditlog"
	entdavaccount "github.com/cloudreve/Cloudreve/v4/ent/davaccount"
	ententity "github.com/cloudreve/Cloudreve/v4/ent/entity"
	entexternalidentity "github.com/cloudreve/Cloudreve/v4/ent/externalidentity"
	entfile "github.com/cloudreve/Cloudreve/v4/ent/file"
	entfsevent "github.com/cloudreve/Cloudreve/v4/ent/fsevent"
	entoauthgrant "github.com/cloudreve/Cloudreve/v4/ent/oauthgrant"
	entpasskey "github.com/cloudreve/Cloudreve/v4/ent/passkey"
	entsetting "github.com/cloudreve/Cloudreve/v4/ent/setting"
	entshare "github.com/cloudreve/Cloudreve/v4/ent/share"
	entsyncthingdevice "github.com/cloudreve/Cloudreve/v4/ent/syncthingdevice"
	enttask "github.com/cloudreve/Cloudreve/v4/ent/task"
	entuser "github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
)

const publicRootFileIDSettingKey = "public_root_file_id"

func ensureSystemPublicRootSupport(ctx context.Context, l logging.Logger, client *ent.Client, dbType conf.DBType) error {
	tx, err := client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("failed to start public root initialization transaction: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	txClient := tx.Client()
	owner, err := ensureSystemPublicRootOwner(ctx, txClient)
	if err != nil {
		return err
	}

	root, err := ensureSystemPublicRoot(ctx, txClient, dbType, owner.ID)
	if err != nil {
		return err
	}

	if err := NewSettingClient(txClient, nil).Set(ctx, map[string]string{
		publicRootFileIDSettingKey: strconv.Itoa(root.ID),
	}); err != nil {
		return fmt.Errorf("failed to persist public root id: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit public root initialization: %w", err)
	}
	committed = true

	if l != nil {
		l.Debug("Ensured hidden public root id=%d owner=%d", root.ID, owner.ID)
	}

	return nil
}

func ensureSystemPublicRootOwner(ctx context.Context, client *ent.Client) (*ent.User, error) {
	return EnsurePublicSystemOwner(ctx, client)
}

func EnsurePublicSystemOwner(ctx context.Context, client *ent.Client) (*ent.User, error) {
	if client == nil {
		return nil, fmt.Errorf("public system owner client is nil")
	}

	userClient := NewUserClient(client)
	loadCtx := context.WithValue(ctx, LoadUserGroup{}, true)

	owner, err := userClient.GetByID(loadCtx, constants.PublicSystemOwnerID)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("failed to query public system owner by id: %w", err)
	}
	if owner != nil && !IsInternalSystemUser(owner) {
		return nil, fmt.Errorf("public system owner id %d is occupied by non-system user", constants.PublicSystemOwnerID)
	}

	if owner == nil {
		legacyOwner, legacyErr := userClient.GetByEmail(loadCtx, constants.PublicSystemOwnerEmail)
		if legacyErr != nil && !ent.IsNotFound(legacyErr) {
			return nil, fmt.Errorf("failed to query public system owner: %w", legacyErr)
		}
		if legacyOwner != nil && legacyOwner.ID != constants.PublicSystemOwnerID {
			owner, err = migrateLegacyPublicSystemOwner(ctx, client, legacyOwner)
			if err != nil {
				return nil, err
			}
		}
	}

	if owner == nil {
		owner, err = createPublicSystemOwner(ctx, client, constants.PublicSystemOwnerID)
		if err != nil {
			return nil, err
		}
	}

	owner, err = normalizePublicSystemOwner(ctx, client, owner)
	if err != nil {
		return nil, err
	}

	owner, err = userClient.GetByID(loadCtx, owner.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload public system owner: %w", err)
	}

	return owner, nil
}

func createPublicSystemOwner(ctx context.Context, client *ent.Client, rawID int) (*ent.User, error) {
	created, err := NewUserClient(client).Create(ctx, &NewUserArgs{
		RawID:    rawID,
		Username: constants.PublicSystemOwnerUsername,
		Email:    constants.PublicSystemOwnerEmail,
		Nick:     constants.PublicSystemOwnerNick,
		Status:   entuser.StatusInactive,
		GroupID:  1,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create public system owner: %w", err)
	}

	return created, nil
}

func normalizePublicSystemOwner(ctx context.Context, client *ent.Client, owner *ent.User) (*ent.User, error) {
	if owner == nil {
		return nil, fmt.Errorf("public system owner is nil")
	}

	username := ""
	if owner.Username != nil {
		username = strings.TrimSpace(*owner.Username)
	}
	needsUpdate := owner.Status != entuser.StatusInactive ||
		owner.GroupUsers != 1 ||
		!strings.EqualFold(strings.TrimSpace(owner.Email), constants.PublicSystemOwnerEmail) ||
		username != constants.PublicSystemOwnerUsername ||
		strings.TrimSpace(owner.Nick) != constants.PublicSystemOwnerNick
	if !needsUpdate {
		return owner, nil
	}

	updated, err := client.User.UpdateOneID(owner.ID).
		SetUsername(constants.PublicSystemOwnerUsername).
		SetEmail(constants.PublicSystemOwnerEmail).
		SetNick(constants.PublicSystemOwnerNick).
		SetStatus(entuser.StatusInactive).
		SetGroupID(1).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to normalize public system owner: %w", err)
	}

	return updated, nil
}

func migrateLegacyPublicSystemOwner(ctx context.Context, client *ent.Client, legacyOwner *ent.User) (*ent.User, error) {
	if legacyOwner == nil {
		return nil, fmt.Errorf("legacy public system owner is nil")
	}

	legacyUsername := legacyPublicSystemOwnerUsername(legacyOwner.ID)
	legacyEmail := legacyPublicSystemOwnerEmail(legacyOwner.ID)
	if _, err := client.User.UpdateOneID(legacyOwner.ID).
		SetUsername(legacyUsername).
		SetEmail(legacyEmail).
		SetNick(constants.PublicSystemOwnerNick).
		SetStatus(entuser.StatusInactive).
		SetGroupID(1).
		Save(ctx); err != nil {
		return nil, fmt.Errorf("failed to detach legacy public system owner: %w", err)
	}

	owner, err := createPublicSystemOwner(ctx, client, constants.PublicSystemOwnerID)
	if err != nil {
		return nil, err
	}

	if err := reassignUserReferences(ctx, client, legacyOwner.ID, owner.ID); err != nil {
		return nil, err
	}

	if err := client.User.DeleteOneID(legacyOwner.ID).Exec(ctx); err != nil {
		return nil, fmt.Errorf("failed to delete legacy public system owner: %w", err)
	}

	return owner, nil
}

func reassignUserReferences(ctx context.Context, client *ent.Client, fromID, toID int) error {
	if fromID == toID {
		return nil
	}

	if _, err := client.File.Update().Where(entfile.OwnerIDEQ(fromID)).SetOwnerID(toID).Save(ctx); err != nil {
		return fmt.Errorf("failed to migrate file owners from %d to %d: %w", fromID, toID, err)
	}
	if _, err := client.DavAccount.Update().Where(entdavaccount.OwnerIDEQ(fromID)).SetOwnerID(toID).Save(ctx); err != nil {
		return fmt.Errorf("failed to migrate dav account owners from %d to %d: %w", fromID, toID, err)
	}
	if _, err := client.SyncthingDevice.Update().Where(entsyncthingdevice.OwnerIDEQ(fromID)).SetOwnerID(toID).Save(ctx); err != nil {
		return fmt.Errorf("failed to migrate syncthing owners from %d to %d: %w", fromID, toID, err)
	}
	if _, err := client.Share.Update().Where(entshare.HasUserWith(entuser.ID(fromID))).SetUserID(toID).Save(ctx); err != nil {
		return fmt.Errorf("failed to migrate shares from %d to %d: %w", fromID, toID, err)
	}
	if _, err := client.Passkey.Update().Where(entpasskey.UserIDEQ(fromID)).SetUserID(toID).Save(ctx); err != nil {
		return fmt.Errorf("failed to migrate passkeys from %d to %d: %w", fromID, toID, err)
	}
	if _, err := client.Task.Update().Where(enttask.UserTasksEQ(fromID)).SetUserID(toID).Save(ctx); err != nil {
		return fmt.Errorf("failed to migrate tasks from %d to %d: %w", fromID, toID, err)
	}
	if _, err := client.FsEvent.Update().Where(entfsevent.UserFseventEQ(fromID)).SetUserID(toID).Save(ctx); err != nil {
		return fmt.Errorf("failed to migrate fs events from %d to %d: %w", fromID, toID, err)
	}
	if _, err := client.Entity.Update().Where(ententity.CreatedByEQ(fromID)).SetUserID(toID).Save(ctx); err != nil {
		return fmt.Errorf("failed to migrate entities from %d to %d: %w", fromID, toID, err)
	}
	if _, err := client.OAuthGrant.Update().Where(entoauthgrant.UserIDEQ(fromID)).SetUserID(toID).Save(ctx); err != nil {
		return fmt.Errorf("failed to migrate oauth grants from %d to %d: %w", fromID, toID, err)
	}
	if _, err := client.ExternalIdentity.Update().Where(entexternalidentity.UserIDEQ(fromID)).SetUserID(toID).Save(ctx); err != nil {
		return fmt.Errorf("failed to migrate external identities from %d to %d: %w", fromID, toID, err)
	}
	if _, err := client.AuditLog.Update().Where(entauditlog.UserIDEQ(fromID)).SetUserID(toID).Save(ctx); err != nil {
		return fmt.Errorf("failed to migrate audit logs from %d to %d: %w", fromID, toID, err)
	}

	return nil
}

func legacyPublicSystemOwnerUsername(id int) string {
	return fmt.Sprintf("__cloudreve_public_root_legacy_%d__", id)
}

func legacyPublicSystemOwnerEmail(id int) string {
	return fmt.Sprintf("__cloudreve_public_root__-legacy-%d@internal.cloudreve", id)
}

func ensureSystemPublicRoot(ctx context.Context, client *ent.Client, dbType conf.DBType, ownerID int) (*ent.File, error) {
	fileClient := NewFileClient(client, dbType, nil)

	root, err := loadConfiguredPublicRoot(ctx, client)
	if err != nil {
		return nil, err
	}

	if root == nil {
		existingRoot, rootErr := client.File.Query().
			Where(
				entfile.OwnerIDEQ(ownerID),
				entfile.Not(entfile.HasParent()),
				entfile.Name(RootFolderName),
			).
			First(ctx)
		if rootErr == nil {
			root = existingRoot
		} else if !ent.IsNotFound(rootErr) {
			return nil, fmt.Errorf("failed to query public system root: %w", rootErr)
		}
	}

	if root != nil && root.Type != int(types.FileTypeFolder) {
		root = nil
	}

	if root == nil {
		created, createErr := fileClient.CreateFolder(ctx, nil, &CreateFolderParameters{
			Owner: ownerID,
			Name:  RootFolderName,
		})
		if createErr != nil {
			return nil, fmt.Errorf("failed to create hidden public root: %w", createErr)
		}
		root = created
	}

	needsUpdate := root.OwnerID != ownerID ||
		root.Type != int(types.FileTypeFolder) ||
		root.IsSymbolic ||
		root.Name != RootFolderName ||
		root.FileChildren > 0
	if needsUpdate {
		updated, updateErr := client.File.UpdateOneID(root.ID).
			SetOwnerID(ownerID).
			SetType(int(types.FileTypeFolder)).
			SetIsSymbolic(false).
			SetName(RootFolderName).
			SetFileExt(fileExtValue(RootFolderName, int(types.FileTypeFolder))).
			ClearParent().
			Save(ctx)
		if updateErr != nil {
			return nil, fmt.Errorf("failed to normalize hidden public root: %w", updateErr)
		}
		root = updated
	}

	return root, nil
}

func loadConfiguredPublicRoot(ctx context.Context, client *ent.Client) (*ent.File, error) {
	value, err := client.Setting.Query().Where(entsetting.Name(publicRootFileIDSettingKey)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query public root setting: %w", err)
	}

	rootID, err := strconv.Atoi(strings.TrimSpace(value.Value))
	if err != nil || rootID <= 0 {
		return nil, nil
	}

	root, err := client.File.Query().Where(entfile.ID(rootID)).First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query configured public root %d: %w", rootID, err)
	}

	return root, nil
}
