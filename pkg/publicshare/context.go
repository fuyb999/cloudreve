package publicshare

import (
	"context"
	"encoding/json"
	"strings"
)

// VisibilityOverrideCtx carries a resolved public visibility snapshot for
// background jobs and stateless RPC without the original OIDC token.
type VisibilityOverrideCtx struct{}

func VisibilityOverrideFromContext(ctx context.Context) *VisibilityResult {
	if ctx == nil {
		return nil
	}

	visibility, _ := ctx.Value(VisibilityOverrideCtx{}).(*VisibilityResult)
	return visibility
}

func EncodeVisibilityOverride(visibility *VisibilityResult) string {
	if visibility == nil {
		return ""
	}

	raw, err := json.Marshal(visibility)
	if err != nil {
		return ""
	}

	return string(raw)
}

func DecodeVisibilityOverride(raw string) (*VisibilityResult, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	visibility := &VisibilityResult{}
	if err := json.Unmarshal([]byte(raw), visibility); err != nil {
		return nil, err
	}

	if visibility.Filter == nil {
		visibility.Filter = BuildVisibilityFilter(visibility.RootGrants)
	}

	return visibility, nil
}
