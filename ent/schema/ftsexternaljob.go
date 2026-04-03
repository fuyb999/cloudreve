package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// FTSExternalJob holds the schema definition for the FTSExternalJob entity.
type FTSExternalJob struct {
	ent.Schema
}

// Fields of the FTSExternalJob.
func (FTSExternalJob) Fields() []ent.Field {
	return []ent.Field{
		field.String("request_id").Unique(),
		field.String("status").
			Default("queued"),
		field.Int("file_id"),
		field.Int("owner_id"),
		field.Int("entity_id"),
		field.String("snapshot_token"),
		field.String("mode").Default(""),
		field.String("trigger_reason").Default(""),
		field.Int("attempt").Default(1),
		field.Text("result_payload").Optional(),
		field.Text("error_payload").Optional(),
		field.String("manifest_path").Optional(),
		field.Text("quality_report").Optional(),
		field.Time("requested_at").Default(time.Now),
		field.Time("deadline_at"),
		field.Time("completed_at").Optional().Nillable(),
	}
}

// Indexes of the FTSExternalJob.
func (FTSExternalJob) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("file_id", "entity_id"),
		index.Fields("status", "deadline_at"),
	}
}

func (FTSExternalJob) Mixin() []ent.Mixin {
	return []ent.Mixin{
		CommonMixin{},
	}
}
