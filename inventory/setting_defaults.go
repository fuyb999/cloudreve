package inventory

import (
	"context"
	"fmt"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/setting"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
)

func ensureDefaultSettings(ctx context.Context, l logging.Logger, client *ent.Client, keys ...string) error {
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

		if _, err := client.Setting.Create().SetName(key).SetValue(value).Save(ctx); err != nil {
			return fmt.Errorf("failed to create setting %q: %w", key, err)
		}

		l.Info("Inserted missing default setting %q.", key)
	}

	return nil
}
