package user

import (
	"context"
	"fmt"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/cache"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

const registerEnabledSettingName = "register_enabled"

func isFirstRegisteredUser(newUser *ent.User) bool {
	return newUser != nil && newUser.ID == 1
}

func disableOpenRegistrationAfterFirstSignup(
	ctx context.Context,
	settingClient inventory.SettingClient,
	newUser *ent.User,
) error {
	if !isFirstRegisteredUser(newUser) {
		return nil
	}

	if err := settingClient.Set(ctx, map[string]string{registerEnabledSettingName: "0"}); err != nil {
		return fmt.Errorf("failed to disable public registration after first signup: %w", err)
	}

	return nil
}

func invalidateOpenRegistrationCache(kv cache.Driver, newUser *ent.User) error {
	if !isFirstRegisteredUser(newUser) || kv == nil {
		return nil
	}

	if err := kv.Delete(setting.KvSettingPrefix, registerEnabledSettingName); err != nil {
		return fmt.Errorf("failed to clear public registration cache: %w", err)
	}

	return nil
}

func promoteFirstOIDCIdentityUserToAdmin(ctx context.Context, currentUser *ent.User, identity *ent.ExternalIdentity) (*ent.User, error) {
	if currentUser == nil || identity == nil || identity.ID != 1 || currentUser.GroupUsers == 1 {
		return currentUser, nil
	}

	promotedUser, err := currentUser.Update().SetGroupID(1).Save(ctx)
	if err != nil {
		return currentUser, fmt.Errorf("failed to promote first OIDC identity user to admin: %w", err)
	}

	return promotedUser, nil
}
