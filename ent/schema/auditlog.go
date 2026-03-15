package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AuditLog holds the schema definition for the AuditLog entity.
type AuditLog struct {
	ent.Schema
}

// Fields of the AuditLog.
func (AuditLog) Fields() []ent.Field {
	return []ent.Field{
		field.Int("type"),
		field.String("correlation_id").Optional(),
		field.String("ip").Optional(),
		field.JSON("content", map[string]any{}).Optional(),
		field.Int("user_id").Optional(),
		field.Int("file_id").Optional(),
		field.Int("entity_id").Optional(),
		field.Int("share_id").Optional(),
	}
}

// Edges of the AuditLog.
func (AuditLog) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("audit_logs").
			Field("user_id").
			Unique(),
		edge.From("file", File.Type).
			Ref("audit_logs").
			Field("file_id").
			Unique(),
		edge.From("entity", Entity.Type).
			Ref("audit_logs").
			Field("entity_id").
			Unique(),
		edge.From("share", Share.Type).
			Ref("audit_logs").
			Field("share_id").
			Unique(),
	}
}

func (AuditLog) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("type"),
		index.Fields("created_at"),
		index.Fields("correlation_id"),
		index.Fields("user_id"),
		index.Fields("file_id"),
		index.Fields("entity_id"),
		index.Fields("share_id"),
	}
}

func (AuditLog) Mixin() []ent.Mixin {
	return []ent.Mixin{
		CommonMixin{},
	}
}
