package publicshare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
)

const (
	oidcEnabledSettingKey   = "oidc_enabled"
	oidcWellKnownSettingKey = "oidc_wellknown_url"

	remoteVisibilityPath  = "/system-api/cloudreve/authz/visibility"
	remoteActionCheckPath = "/system-api/cloudreve/authz/action-check"

	remoteAuthzTimeout = 5 * time.Second
)

type remoteEnvelope[T any] struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data T      `json:"data"`
}

type remoteVisibilityResult struct {
	RootGrants    []remoteRootGrant     `json:"rootGrants"`
	FileFilterAST *remoteFileFilterExpr `json:"fileFilterAst"`
}

type remoteRootGrant struct {
	FileID   int64           `json:"fileId"`
	OwnerID  int64           `json:"ownerId"`
	TreePath string          `json:"treePath"`
	Name     string          `json:"name"`
	Actions  map[string]bool `json:"actions"`
}

type remoteFileFilterExpr struct {
	Operator string                  `json:"operator"`
	Children []*remoteFileFilterExpr `json:"children"`
	Match    *remoteFileFilterMatch  `json:"match"`
}

type remoteFileFilterMatch struct {
	Kind         string   `json:"kind"`
	IntValues    []int64  `json:"intValues"`
	StringValues []string `json:"stringValues"`
}

type remoteActionCheckRequest struct {
	Action string               `json:"action"`
	Target remoteTargetResource `json:"target"`
}

type remoteTargetResource struct {
	FileID   int64  `json:"fileId"`
	OwnerID  int64  `json:"ownerId"`
	TreePath string `json:"treePath"`
	Type     int    `json:"type"`
	Name     string `json:"name"`
}

type remoteActionDecision struct {
	Allowed        bool            `json:"allowed"`
	Action         string          `json:"action"`
	ResourceFileID int64           `json:"resourceFileId"`
	Reason         string          `json:"reason"`
	Actions        map[string]bool `json:"actions"`
}

// UnifiedAuthzEnabled 返回当前是否处于 “OIDC 打开即由统一认证中心接管公共文件授权” 模式。
func (s *Service) UnifiedAuthzEnabled(ctx context.Context) bool {
	raw, err := s.settingClient.Get(ctx, oidcEnabledSettingKey)
	if err != nil {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (s *Service) resolveVisibilityRemote(ctx context.Context, accessToken string) (*VisibilityResult, error) {
	baseURL, err := s.remoteAuthzBaseURL(ctx)
	if err != nil {
		return nil, err
	}

	payload, err := remoteRequest[remoteVisibilityResult](ctx, http.MethodGet, baseURL+remoteVisibilityPath, accessToken, nil)
	if err != nil {
		return nil, err
	}

	return toLocalVisibilityResult(payload), nil
}

func (s *Service) checkActionRemote(ctx context.Context, accessToken string, target *ent.File, action Action) (*ActionDecision, error) {
	if target == nil {
		return &ActionDecision{Allowed: false, Action: action, Reason: "target_not_found"}, nil
	}

	rootID, rootErr := s.RootID(ctx)
	if rootErr == nil && rootID > 0 && target.ID == rootID {
		return virtualPublicRootDecision(target, action), nil
	}

	baseURL, err := s.remoteAuthzBaseURL(ctx)
	if err != nil {
		return nil, err
	}

	payload, err := remoteRequest[remoteActionDecision](ctx, http.MethodPost, baseURL+remoteActionCheckPath, accessToken, &remoteActionCheckRequest{
		Action: string(action),
		Target: remoteTargetResource{
			FileID:   int64(target.ID),
			OwnerID:  int64(target.OwnerID),
			TreePath: target.TreePath,
			Type:     target.Type,
			Name:     target.Name,
		},
	})
	if err != nil {
		return nil, err
	}

	decision := toLocalActionDecision(payload, target, action)

	visibility, err := s.resolveVisibilityRemote(ctx, accessToken)
	if err != nil {
		return nil, err
	}

	return constrainRemoteDecisionToVisibility(target, action, visibility, decision), nil
}

func (s *Service) remoteAuthzBaseURL(ctx context.Context) (string, error) {
	raw, err := s.settingClient.Get(ctx, oidcWellKnownSettingKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("oidc well-known url is not configured")
	}

	wellKnown := strings.TrimSpace(raw)
	baseURL := strings.TrimSuffix(wellKnown, "/.well-known/openid-configuration")
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" || baseURL == wellKnown {
		return "", fmt.Errorf("failed to derive unified authz base url from oidc well-known url")
	}

	return baseURL, nil
}

func remoteRequest[T any](ctx context.Context, method string, target string, accessToken string, requestBody any) (*T, error) {
	var bodyReader io.Reader
	if requestBody != nil {
		body, err := json.Marshal(requestBody)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal remote authz request: %w", err)
		}
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create remote authz request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: remoteAuthzTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call remote authz endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read remote authz response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if appErr := parseRemoteAppError(body); appErr != nil {
			return nil, appErr
		}
		return nil, serializer.NewError(serializer.CodeInternalSetting,
			fmt.Sprintf("remote authz endpoint returned status %d", resp.StatusCode),
			errors.New(strings.TrimSpace(string(body))))
	}

	envelope := &remoteEnvelope[T]{}
	if err := json.Unmarshal(body, envelope); err != nil {
		return nil, fmt.Errorf("failed to parse remote authz response: %w", err)
	}
	if envelope.Code != 0 {
		// 远端运行时授权已经给出了明确业务码，直接透传为 AppError，
		// 避免上层再次包装成 “parent not exist” 之类误导性的路径错误。
		return nil, newRemoteAppError(envelope.Code, envelope.Msg)
	}

	return &envelope.Data, nil
}

func parseRemoteAppError(body []byte) error {
	envelope := &remoteEnvelope[json.RawMessage]{}
	if err := json.Unmarshal(body, envelope); err != nil || envelope.Code == 0 {
		return nil
	}

	return newRemoteAppError(envelope.Code, envelope.Msg)
}

func newRemoteAppError(code int, msg string) error {
	normalized := strings.TrimSpace(msg)
	if normalized == "" {
		normalized = "remote authz rejected request"
	}

	return serializer.NewError(code, normalized, nil)
}

func toLocalVisibilityResult(payload *remoteVisibilityResult) *VisibilityResult {
	if payload == nil {
		return &VisibilityResult{
			Filter: FalseFilter(),
		}
	}

	result := &VisibilityResult{
		Filter:     toLocalFilterExpr(payload.FileFilterAST),
		RootGrants: make([]RootGrant, 0, len(payload.RootGrants)),
	}
	if result.Filter == nil {
		result.Filter = FalseFilter()
	}

	for _, grant := range payload.RootGrants {
		result.RootGrants = append(result.RootGrants, RootGrant{
			RootFileID:   int(grant.FileID),
			RootOwnerID:  int(grant.OwnerID),
			RootName:     grant.Name,
			RootTreePath: grant.TreePath,
			Actions:      toLocalActions(grant.Actions),
		})
	}

	return result
}

func toLocalFilterExpr(expr *remoteFileFilterExpr) *FileFilterExpr {
	if expr == nil {
		return nil
	}

	res := &FileFilterExpr{
		Operator: FileFilterOperator(expr.Operator),
	}
	if expr.Match != nil {
		res.Match = &FileFilterMatch{
			Kind:         FileFilterMatchKind(expr.Match.Kind),
			StringValues: append([]string(nil), expr.Match.StringValues...),
		}
		if len(expr.Match.IntValues) > 0 {
			res.Match.IntValues = make([]int, 0, len(expr.Match.IntValues))
			for _, value := range expr.Match.IntValues {
				res.Match.IntValues = append(res.Match.IntValues, int(value))
			}
		}
	}

	if len(expr.Children) > 0 {
		res.Children = make([]*FileFilterExpr, 0, len(expr.Children))
		for _, child := range expr.Children {
			res.Children = append(res.Children, toLocalFilterExpr(child))
		}
	}

	return res
}

func toLocalActionDecision(payload *remoteActionDecision, target *ent.File, action Action) *ActionDecision {
	if payload == nil {
		return &ActionDecision{
			Allowed: false,
			Action:  action,
			Reason:  "empty_remote_decision",
		}
	}

	rootFileID := int(payload.ResourceFileID)
	if rootFileID == 0 && target != nil {
		rootFileID = target.ID
	}

	return &ActionDecision{
		Allowed:    payload.Allowed,
		Action:     Action(payload.Action),
		RootFileID: rootFileID,
		Actions:    toLocalActions(payload.Actions),
		Reason:     payload.Reason,
	}
}

func virtualPublicRootDecision(target *ent.File, action Action) *ActionDecision {
	actions := make(map[Action]bool, len(ActionOrder))
	for _, candidate := range ActionOrder {
		actions[candidate] = false
	}
	actions[ActionList] = true

	allowed := action == ActionList
	reason := "virtual_public_root_readonly"
	if allowed {
		reason = "allowed"
	}

	return &ActionDecision{
		Allowed:    allowed,
		Action:     action,
		RootFileID: target.ID,
		Actions:    actions,
		Reason:     reason,
	}
}

func constrainRemoteDecisionToVisibility(target *ent.File, action Action, visibility *VisibilityResult, decision *ActionDecision) *ActionDecision {
	if target == nil {
		return &ActionDecision{Allowed: false, Action: action, Reason: "target_not_found"}
	}

	if decision == nil {
		decision = &ActionDecision{Allowed: false, Action: action, Reason: "empty_remote_decision"}
	}

	if visibility == nil {
		visibility = &VisibilityResult{}
	}

	rootGrant, found := rootGrantForAncestors(target, visibility.RootGrants)
	if !found {
		return &ActionDecision{
			Allowed:    false,
			Action:     action,
			RootFileID: target.ID,
			Actions:    denyAllActions(),
			Reason:     "root_not_visible",
		}
	}

	actions := intersectDecisionActions(rootGrant, target.ID, decision.Actions)
	allowed := decision.Allowed && actions[action]
	if action == ActionList {
		allowed = true
	}

	reason := strings.TrimSpace(decision.Reason)
	switch {
	case allowed:
		if reason == "" {
			reason = "allowed"
		}
	case action == ActionDelete && target.ID == rootGrant.RootFileID && !RootGrantActionAllowed(target.ID, rootGrant, ActionDelete):
		reason = "root_delete_protected"
	default:
		if reason == "" || reason == "allowed" {
			reason = "action_denied"
		}
	}

	return &ActionDecision{
		Allowed:    allowed,
		Action:     action,
		RootFileID: rootGrant.RootFileID,
		Actions:    actions,
		Reason:     reason,
	}
}

func intersectDecisionActions(grant RootGrant, targetFileID int, decisionActions map[Action]bool) map[Action]bool {
	res := make(map[Action]bool, len(ActionOrder))
	useDecisionActions := len(decisionActions) > 0

	for _, action := range ActionOrder {
		visibleAllowed := action == ActionList || RootGrantActionAllowed(targetFileID, grant, action)
		if useDecisionActions {
			res[action] = visibleAllowed && decisionActions[action]
			continue
		}

		res[action] = visibleAllowed
	}

	res[ActionList] = true
	return res
}

func denyAllActions() map[Action]bool {
	res := make(map[Action]bool, len(ActionOrder))
	for _, action := range ActionOrder {
		res[action] = false
	}
	return res
}

func toLocalActions(actions map[string]bool) map[Action]bool {
	if len(actions) == 0 {
		return map[Action]bool{}
	}

	res := make(map[Action]bool, len(actions))
	for key, value := range actions {
		res[Action(key)] = value
	}
	return res
}

func oidcAccessTokenFromContext(ctx context.Context) string {
	return inventory.OIDCAccessTokenFromContext(ctx)
}
