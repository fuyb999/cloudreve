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

type registrationDisabledCtx struct{}

func isFirstRegisteredUser(ctx context.Context, userClient inventory.UserClient, newUser *ent.User) (bool, error) {
	if newUser == nil || userClient == nil || inventory.IsInternalSystemUser(newUser) {
		return false, nil
	}

	total, err := userClient.CountByTimeRange(ctx, nil, nil)
	if err != nil {
		return false, fmt.Errorf("failed to count visible users: %w", err)
	}

	return total == 1, nil
}

func disableOpenRegistrationAfterFirstSignup(
	ctx context.Context,
	userClient inventory.UserClient,
	settingClient inventory.SettingClient,
	newUser *ent.User,
) (bool, error) {
	firstRegisteredUser, err := isFirstRegisteredUser(ctx, userClient, newUser)
	if err != nil {
		return false, err
	}
	if !firstRegisteredUser {
		return false, nil
	}

	if err := settingClient.Set(ctx, map[string]string{registerEnabledSettingName: "0"}); err != nil {
		return false, fmt.Errorf("failed to disable public registration after first signup: %w", err)
	}

	return true, nil
}

func invalidateOpenRegistrationCache(kv cache.Driver, disabled bool) error {
	if !disabled || kv == nil {
		return nil
	}

	if err := kv.Delete(setting.KvSettingPrefix, registerEnabledSettingName); err != nil {
		return fmt.Errorf("failed to clear public registration cache: %w", err)
	}

	return nil
}

func promoteFirstOIDCIdentityUserToAdmin(ctx context.Context, currentUser *ent.User, identity *ent.ExternalIdentity) (*ent.User, error) {
	if currentUser == nil || identity == nil || currentUser.ID != 1 || currentUser.GroupUsers == 1 {
		return currentUser, nil
	}

	promotedUser, err := currentUser.Update().SetGroupID(1).Save(ctx)
	if err != nil {
		return currentUser, fmt.Errorf("failed to promote first OIDC identity user to admin: %w", err)
	}

	return promotedUser, nil
}
