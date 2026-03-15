package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SyncthingDevice holds the schema definition for the SyncthingDevice entity.
type SyncthingDevice struct {
	ent.Schema
}

// Fields of the SyncthingDevice.
func (SyncthingDevice) Fields() []ent.Field {
	return []ent.Field{
		field.Int("owner_id"),
		field.String("device_id").
			MaxLen(255),
		field.String("short_id").
			MaxLen(64).
			Optional(),
		field.String("last_ip").
			MaxLen(255).
			Optional(),
		field.String("api_key").
			MaxLen(255).
			Sensitive().
			Optional(),
		field.JSON("json_raw", map[string]any{}).
			Default(map[string]any{}).
			Optional(),
		field.String("bind_uri").
			MaxLen(2048).
			Optional(),
		field.String("client_version").
			MaxLen(255).
			Optional(),
		field.String("platform").
			MaxLen(255).
			Optional(),
		field.Time("last_seen_at").
			Optional().
			Nillable(),
		field.Time("last_sync_at").
			Optional().
			Nillable(),
		field.Bool("online").
			Default(false),
		field.Bool("is_bound").
			Default(true),
		field.Bool("cloud_sync_enabled").
			Default(true),
		field.String("management_action").
			MaxLen(64).
			Optional(),
		field.String("management_action_id").
			MaxLen(64).
			Optional(),
		field.JSON("management_action_payload", map[string]any{}).
			Default(map[string]any{}).
			Optional(),
		field.Time("management_action_updated_at").
			Optional().
			Nillable(),
	}
}

// Edges of the SyncthingDevice.
func (SyncthingDevice) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner", User.Type).
			Ref("syncthing_devices").
			Field("owner_id").
			Unique().
			Required(),
	}
}

// Indexes of the SyncthingDevice.
func (SyncthingDevice) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("owner_id", "device_id").
			Unique(),
		index.Fields("owner_id", "online"),
		index.Fields("owner_id", "last_seen_at"),
	}
}

func (SyncthingDevice) Mixin() []ent.Mixin {
	return []ent.Mixin{
		CommonMixin{},
	}
}
