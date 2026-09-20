package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// PaymentTransactionClaim permanently associates a payment transaction with
// one order. Claims survive refunds and order deletion to prevent reuse.
type PaymentTransactionClaim struct{ ent.Schema }

func (PaymentTransactionClaim) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "payment_transaction_claims"}}
}

func (PaymentTransactionClaim) Fields() []ent.Field {
	return []ent.Field{
		field.String("tx_hash").NotEmpty().MaxLen(512).Immutable(),
		field.Int64("order_id").Immutable(),
		field.Time("created_at").Default(time.Now).Immutable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (PaymentTransactionClaim) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tx_hash").Unique(),
		index.Fields("order_id"),
	}
}
