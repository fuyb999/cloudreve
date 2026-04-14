package inventory

import (
	"context"
	"strings"
)

// OIDCAccessTokenCtx 在当前请求上下文中保存上游统一认证中心签发的 access token。
// Cloudreve 在启用统一认证后，可以复用这枚 token 去访问 Yudao 的运行时授权接口。
type OIDCAccessTokenCtx struct{}

// OIDCAccessTokenFromContext 读取当前请求上下文中的上游 access token。
func OIDCAccessTokenFromContext(ctx context.Context) string {
	token, _ := ctx.Value(OIDCAccessTokenCtx{}).(string)
	return strings.TrimSpace(token)
}

// OIDCGrantTypeCtx 在当前请求上下文中保存上游统一认证中心 introspection 返回的 grant_type。
type OIDCGrantTypeCtx struct{}

// OIDCGrantTypeFromContext 读取当前请求上下文中的 grant_type。
func OIDCGrantTypeFromContext(ctx context.Context) string {
	grantType, _ := ctx.Value(OIDCGrantTypeCtx{}).(string)
	return strings.TrimSpace(grantType)
}
