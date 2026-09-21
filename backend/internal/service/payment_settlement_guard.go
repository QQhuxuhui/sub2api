package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymenttransactionclaim"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	paymentSettlementLockID int64 = 0x5355423250415901
	claimSourceManual             = "manual"
	claimSourceHashless           = "gateway_hashless"
	claimSourceGateway            = "gateway_hash"
)

// ErrPaymentReviewRequired is emitted only after confirmation evidence and its
// review audit have committed. Webhooks can acknowledge this durable outcome.
var ErrPaymentReviewRequired = infraerrors.Conflict("PAYMENT_REVIEW_REQUIRED", "gateway confirmation saved; transaction ownership requires review")

// SQLite exists only in unit fixtures. Production exclusion is transaction-local
// in PostgreSQL, shared by every instance of the application.
var paymentSettlementTestGate = make(chan struct{}, 1)

func beginPaymentSettlement(ctx context.Context, client *dbent.Client) (*dbent.Tx, func(), error) {
	releaseGate := func() {}
	if client.Driver().Dialect() != dialect.Postgres {
		select {
		case paymentSettlementTestGate <- struct{}{}:
			releaseGate = func() { <-paymentSettlementTestGate }
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	tx, err := client.Tx(ctx)
	if err != nil {
		releaseGate()
		return nil, nil, err
	}
	var once sync.Once
	finish := func() { once.Do(func() { _ = tx.Rollback(); releaseGate() }) }
	if client.Driver().Dialect() == dialect.Postgres {
		var rows sql.Rows
		if err := tx.Client().Driver().Query(ctx, "SELECT pg_advisory_xact_lock($1)", []any{paymentSettlementLockID}, &rows); err != nil {
			finish()
			return nil, nil, err
		}
		if err := rows.Close(); err != nil {
			finish()
			return nil, nil, err
		}
		// Migration 240 fences old binaries that do not implement this protocol.
		// Transaction-local: the marker cannot leak through the connection pool.
		if _, err := tx.Client().ExecContext(ctx, "SELECT set_config('sub2api.payment_settlement_protocol', '240', true)"); err != nil {
			finish()
			return nil, nil, err
		}
	}
	return tx, finish, nil
}

func settlementWindowEnd(o *dbent.PaymentOrder) time.Time {
	end := o.CreatedAt.Add(manualSettleMaxAge)
	if o.ExpiresAt.After(end) {
		end = o.ExpiresAt
	}
	return end
}

// Only an unresolved hashless confirmation creates ambiguity. Query through
// correlated EXISTS, never a list of every historical claim ID.
func hashlessPaidOrderNear(ctx context.Context, client *dbent.Client, blockTime time.Time, exceptOrderID int64) (int64, error) {
	claim, err := client.PaymentTransactionClaim.Query().Where(
		paymenttransactionclaim.SourceEQ(claimSourceHashless),
		paymenttransactionclaim.OrderIDNEQ(exceptOrderID),
		paymenttransactionclaim.Or(paymenttransactionclaim.OrderCreatedAtIsNil(), paymenttransactionclaim.OrderCreatedAtLTE(blockTime.Add(manualSettleClockSkew))),
		paymenttransactionclaim.Or(paymenttransactionclaim.OrderWindowEndIsNil(), paymenttransactionclaim.OrderWindowEndGTE(blockTime)),
		func(s *sql.Selector) {
			linked := sql.Table(paymenttransactionclaim.Table).As("linked")
			s.Where(sql.Not(sql.Exists(sql.Select(linked.C("id")).From(linked).Where(sql.And(
				sql.ColumnsEQ(linked.C("order_id"), s.C("order_id")),
				sql.Not(sql.HasPrefix(linked.C("tx_hash"), gatewayTradeClaimPrefix)),
			)))))
		},
	).Order(dbent.Asc(paymenttransactionclaim.FieldID)).First(ctx)
	if dbent.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return claim.OrderID, nil
}

func manualPaymentForHashlessOrder(ctx context.Context, client *dbent.Client, o *dbent.PaymentOrder) (int64, error) {
	claim, err := client.PaymentTransactionClaim.Query().Where(
		paymenttransactionclaim.SourceEQ(claimSourceManual),
		paymenttransactionclaim.OrderIDNEQ(o.ID),
		paymenttransactionclaim.Or(paymenttransactionclaim.TransferTimeIsNil(), paymenttransactionclaim.And(
			paymenttransactionclaim.TransferTimeGTE(o.CreatedAt.Add(-manualSettleClockSkew)),
			paymenttransactionclaim.TransferTimeLTE(settlementWindowEnd(o)),
		)),
	).Order(dbent.Asc(paymenttransactionclaim.FieldID)).First(ctx)
	if dbent.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return claim.OrderID, nil
}

func paymentReviewRequired(otherOrderID int64) error {
	return infraerrors.Conflict("PAYMENT_REVIEW_REQUIRED", fmt.Sprintf(
		"gateway payment confirmation saved for review: its transfer may already have settled order %d; obtain the actual transaction hash to resolve", otherOrderID))
}

func recordSettlementClaim(ctx context.Context, client *dbent.Client, o *dbent.PaymentOrder, key, source string, transferTime *time.Time, evidence map[string]any) error {
	_, err := client.PaymentTransactionClaim.Update().Where(paymenttransactionclaim.TxHashEQ(key), paymenttransactionclaim.OrderIDEQ(o.ID)).
		SetSource(source).SetOrderCreatedAt(o.CreatedAt).SetOrderWindowEnd(settlementWindowEnd(o)).
		SetNillableTransferTime(transferTime).SetEvidence(evidence).Save(ctx)
	return err
}
