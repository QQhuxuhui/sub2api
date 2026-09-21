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

// IntentRouter is the intent-routing configuration of one group: requests that
// arrive on the group are classified by a cheap model and sent to the accounts
// of the matching rule. It deliberately lives in its own table and references
// groups/accounts by ID only, so the feature stays detachable from the core
// scheduling schema.
type IntentRouter struct{ ent.Schema }

func (IntentRouter) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "intent_routers"}}
}

func (IntentRouter) Mixin() []ent.Mixin {
	return []ent.Mixin{mixins.TimeMixin{}}
}

func (IntentRouter) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("group_id"),
		field.Bool("enabled").Default(false),
		// Empty means "this server itself" (loopback), so any account type the
		// gateway can serve is usable for classification.
		field.String("classifier_base_url").Default("").MaxLen(512),
		field.String("classifier_api_key").Default("").Sensitive().MaxLen(512),
		field.String("classifier_protocol").Default(domain.IntentClassifierProtocolOpenAIChat).MaxLen(32),
		field.String("classifier_model").Default("").MaxLen(128),
		field.Int("classifier_timeout_ms").Default(3000),
		field.Int("cache_ttl_seconds").Default(7200),
		field.Int("max_input_chars").Default(2000),
		field.JSON("rules", []domain.IntentRule{}).Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
	}
}

func (IntentRouter) Indexes() []ent.Index {
	return []ent.Index{index.Fields("group_id").Unique()}
}
