package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/internal/domain"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// MonitorProbeAnswer is one group's setting for answering sub2api
// channel-monitor challenges by program. Its own table, independent of the
// group's intent router, referencing the group by ID only.
type MonitorProbeAnswer struct{ ent.Schema }

func (MonitorProbeAnswer) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "monitor_probe_answers"}}
}

func (MonitorProbeAnswer) Mixin() []ent.Mixin {
	return []ent.Mixin{mixins.TimeMixin{}}
}

func (MonitorProbeAnswer) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("group_id"),
		field.JSON("config", domain.MonitorProbeAnswerConfig{}).Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
	}
}

func (MonitorProbeAnswer) Indexes() []ent.Index {
	return []ent.Index{index.Fields("group_id").Unique()}
}
