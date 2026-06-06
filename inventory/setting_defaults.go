package inventory

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/setting"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
)

func ensureAllDefaultSettings(ctx context.Context, l logging.Logger, client *ent.Client) error {
	keys := make([]string, 0, len(DefaultSettings))
	for key := range DefaultSettings {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	return ensureDefaultSettings(ctx, l, client, keys...)
}

func ensureDefaultSettings(ctx context.Context, l logging.Logger, client *ent.Client, keys ...string) error {
	settingClient := NewSettingClient(client, nil)
	for _, key := range keys {
		value, ok := DefaultSettings[key]
		if !ok {
			continue
		}

		exists, err := client.Setting.Query().Where(setting.NameEQ(key)).Exist(ctx)
		if err != nil {
			return fmt.Errorf("failed to query setting %q: %w", key, err)
		}
		if exists {
			continue
		}

		if override, ok := os.LookupEnv(EnvDefaultOverwritePrefix + key); ok {
			l.Info("Override default setting %q with env value %q", key, override)
			value = override
		}

		if err := settingClient.Set(ctx, map[string]string{key: value}); err != nil {
			return fmt.Errorf("failed to create setting %q: %w", key, err)
		}

		l.Info("Inserted missing default setting %q.", key)
	}

	return nil
}

func removeDeprecatedSettings(ctx context.Context, l logging.Logger, client *ent.Client, keys ...string) error {
	for _, key := range keys {
		affected, err := client.Setting.Delete().Where(setting.NameEQ(key)).Exec(ctx)
		if err != nil {
			return fmt.Errorf("failed to delete deprecated setting %q: %w", key, err)
		}
		if affected > 0 {
			l.Info("Removed deprecated setting %q.", key)
		}
	}

	return nil
}
