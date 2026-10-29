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

// OpenAIStreamPrelude is one group's setting for sending an early,
// protocol-neutral first event on OpenAI streaming requests. Its own table,
// independent of the group's intent router, referencing the group by ID only.
type OpenAIStreamPrelude struct{ ent.Schema }

func (OpenAIStreamPrelude) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "openai_stream_preludes"}}
}

func (OpenAIStreamPrelude) Mixin() []ent.Mixin {
	return []ent.Mixin{mixins.TimeMixin{}}
}

func (OpenAIStreamPrelude) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("group_id"),
		field.JSON("config", domain.OpenAIStreamPreludeConfig{}).Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
	}
}

func (OpenAIStreamPrelude) Indexes() []ent.Index {
	return []ent.Index{index.Fields("group_id").Unique()}
}
