package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ExternalIdentity 用来保存第三方身份平台和 Cloudreve 本地用户之间的一对一映射。
// 第一版统一认证先落在这张表上，后续如果把公共文件权限中心迁移到 Yudao，
// 也可以继续复用这里保存的外部主键、部门信息和原始 claims。
type ExternalIdentity struct {
	ent.Schema
}

func (ExternalIdentity) Fields() []ent.Field {
	return []ent.Field{
		// provider 用于区分身份来源，当前预留为 oidc，后续也方便扩展其他统一认证源。
		field.String("provider").
			MaxLen(32),
		// issuer + subject 组合是 OIDC 世界里的稳定外部唯一标识。
		field.String("issuer").
			MaxLen(255),
		field.String("subject").
			MaxLen(255),
		// 以下字段都是为了把远端常用身份属性冗余到本地，减少后续权限判断时的二次解析成本。
		field.String("external_user_id").
			MaxLen(255).
			Optional(),
		field.String("tenant_id").
			MaxLen(255).
			Optional(),
		field.String("department_id").
			MaxLen(255).
			Optional(),
		field.String("email").
			MaxLen(255).
			Optional(),
		field.String("username").
			MaxLen(255).
			Optional(),
		field.String("nickname").
			MaxLen(255).
			Optional(),
		field.String("avatar").
			MaxLen(2048).
			Optional(),
		field.JSON("claims", map[string]any{}).
			Default(map[string]any{}),
		// user_id 指向 Cloudreve 本地影子用户，后续 owner_id/tree_path 仍沿用本地用户体系。
		field.Int("user_id"),
	}
}

func (ExternalIdentity) Edges() []ent.Edge {
	return []ent.Edge{
		// 一个外部身份在 Cloudreve 内只绑定一个用户，防止出现多本地账号映射同一远端账号。
		edge.From("user", User.Type).
			Ref("external_identities").
			Field("user_id").
			Unique().
			Required(),
	}
}

func (ExternalIdentity) Indexes() []ent.Index {
	return []ent.Index{
		// 统一认证回调先按 provider + issuer + subject 命中，确保绑定关系唯一。
		index.Fields("provider", "issuer", "subject").Unique(),
		index.Fields("user_id"),
		index.Fields("provider", "user_id"),
	}
}

func (ExternalIdentity) Mixin() []ent.Mixin {
	return []ent.Mixin{
		CommonMixin{},
	}
}
