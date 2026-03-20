package inventory

import (
	"context"
	"fmt"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
)

// ensureUsernamesSupport 为历史用户补齐 username 字段。
// 登录链路已切到 username，因此这里在每次启动时做一次幂等修复，
// 避免旧库中只有 email 的用户无法继续登录或被 SSO 正确绑定。
func ensureUsernamesSupport(ctx context.Context, l logging.Logger, client *ent.Client) error {
	users, err := client.User.Query().
		Where(user.Or(user.UsernameIsNil(), user.UsernameEQ(""))).
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to query users with empty username: %w", err)
	}

	for _, item := range users {
		username, err := NextAvailableUsername(ctx, client, fallbackUsernameCandidate(item.Email, item.Nick), item.ID)
		if err != nil {
			return err
		}

		if _, err := client.User.UpdateOneID(item.ID).SetUsername(username).Save(ctx); err != nil {
			return fmt.Errorf("failed to backfill username for user %d: %w", item.ID, err)
		}

		l.Info("Backfilled username %q for user %d", username, item.ID)
	}

	return nil
}

func NextAvailableUsername(ctx context.Context, client *ent.Client, base string, excludeID int) (string, error) {
	base = NormalizeUsername(base)
	if base == "" {
		base = fmt.Sprintf("user%d", excludeID)
	}

	candidate := base
	suffix := 1
	for {
		query := client.User.Query().Where(user.UsernameEqualFold(candidate))
		if excludeID > 0 {
			query = query.Where(user.IDNEQ(excludeID))
		}

		existed, err := query.Exist(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to check username collision for %q: %w", candidate, err)
		}
		if !existed {
			return candidate, nil
		}

		candidate = fmt.Sprintf("%s_%d", base, suffix)
		suffix++
	}
}
