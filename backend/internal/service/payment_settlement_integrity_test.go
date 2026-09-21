//go:build unit

package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymenttransactionclaim"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestPaymentHashlessConfirmationAfterManualSettlementNeedsReview(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	second := newSecondSettlementOrder(t, f)
	_, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	require.NoError(t, err)
	err = f.svc.confirmPayment(ctx, second.ID, second.PaymentTradeNo, 30, payment.TypeEpusdt, nil)
	requireManualSettleReason(t, err, "PAYMENT_REVIEW_REQUIRED")
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance)
	order, err := f.client.PaymentOrder.Get(ctx, second.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPending, order.Status)
	count, err := f.client.PaymentAuditLog.Query().Where(paymentauditlog.ActionEQ("PAYMENT_REVIEW_REQUIRED")).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count, "confirmation evidence must survive the error response")
	for i := 0; i < 3; i++ {
		requireManualSettleReason(t, f.svc.confirmPayment(ctx, second.ID, second.PaymentTradeNo, 30, payment.TypeEpusdt, nil), "PAYMENT_REVIEW_REQUIRED")
	}
	count, err = f.client.PaymentAuditLog.Query().Where(paymentauditlog.ActionEQ("PAYMENT_REVIEW_REQUIRED")).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count, "retries retain one review record")
	// Review may outlive normal callback grace; evidence must still resolve it.
	_, err = f.client.PaymentOrder.UpdateOneID(second.ID).SetStatus(OrderStatusExpired).SetUpdatedAt(time.Now().Add(-time.Hour)).Save(ctx)
	require.NoError(t, err)
	// A later signed callback identifies a different transfer and resolves review.
	err = f.svc.confirmPayment(ctx, second.ID, second.PaymentTradeNo, 30, payment.TypeEpusdt,
		map[string]string{"block_transaction_id": strings.Repeat("a", 64)})
	require.NoError(t, err)
	require.Equal(t, 60.0, f.userRepo.getByIDUser.Balance)
	n, err := f.client.PaymentTransactionClaim.Query().Where(paymenttransactionclaim.ReviewPendingEQ(true)).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestPaymentReviewAuditFailureRollsBackConfirmation(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	second := newSecondSettlementOrder(t, f)
	_, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	require.NoError(t, err)
	f.client.PaymentAuditLog.Use(func(next dbent.Mutator) dbent.Mutator {
		return dbent.MutateFunc(func(ctx context.Context, m dbent.Mutation) (dbent.Value, error) {
			return nil, fmt.Errorf("review audit unavailable")
		})
	})
	err = f.svc.confirmPayment(ctx, second.ID, second.PaymentTradeNo, 30, payment.TypeEpusdt, nil)
	require.ErrorContains(t, err, "review audit unavailable")
	n, err := f.client.PaymentTransactionClaim.Query().Where(paymenttransactionclaim.OrderIDEQ(second.ID)).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance)
}

func TestPaymentClaimLookupNeverReadsAuditText(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	f.client.PaymentAuditLog.Intercept(dbent.InterceptFunc(func(next dbent.Querier) dbent.Querier {
		return dbent.QuerierFunc(func(context.Context, dbent.Query) (dbent.Value, error) {
			return nil, fmt.Errorf("audit reads prohibited on claim hot path")
		})
	}))
	require.NoError(t, f.svc.confirmPayment(ctx, f.order.ID, f.order.PaymentTradeNo, 30, payment.TypeEpusdt, map[string]string{"block_transaction_id": manualSettleTxHash}))
	owner, err := f.svc.orderUsingTxHash(ctx, strings.TrimPrefix(manualSettleTxHash, "0x"))
	require.NoError(t, err)
	require.NotEmpty(t, owner)
}

func TestPaymentHashlessEvidenceSurvivesOrderDeletion(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	second := newSecondSettlementOrder(t, f)
	require.NoError(t, f.svc.confirmPayment(ctx, f.order.ID, f.order.PaymentTradeNo, 30, payment.TypeEpusdt, nil))
	require.NoError(t, f.client.PaymentOrder.DeleteOneID(f.order.ID).Exec(ctx))
	_, err := f.svc.AdminSettleOrderByTxHash(ctx, second.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	requireManualSettleReason(t, err, "HASHLESS_PAYMENT_NEARBY")
}

func TestPaymentHashlessOrderOutsideManualTransferLifetimeCreditsNormally(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	second := newSecondSettlementOrder(t, f)
	_, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	require.NoError(t, err)
	// This order started after the earlier transfer, so that transfer cannot pay it.
	_, err = f.client.ExecContext(ctx, "UPDATE payment_orders SET created_at = ? WHERE id = ?", time.Now().Add(time.Hour), second.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.confirmPayment(ctx, second.ID, second.PaymentTradeNo, 30, payment.TypeEpusdt, nil))
	require.Equal(t, 60.0, f.userRepo.getByIDUser.Balance)
}

func TestPaymentDelayedHashlessConfirmationStillNeedsReview(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	second := newSecondSettlementOrder(t, f)
	created := time.Now().Add(-12 * time.Hour)
	_, err := f.client.ExecContext(ctx, "UPDATE payment_orders SET created_at = ? WHERE id = ?", created, f.order.ID)
	require.NoError(t, err)
	_, err = f.client.ExecContext(ctx, "UPDATE payment_orders SET created_at = ? WHERE id = ?", created, second.ID)
	require.NoError(t, err)
	f.fetcher.byNetwork["binance"][0].BlockTime = created.Add(time.Minute)
	_, err = f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	require.NoError(t, err)
	err = f.svc.confirmPayment(ctx, second.ID, second.PaymentTradeNo, 30, payment.TypeEpusdt, nil)
	requireManualSettleReason(t, err, "PAYMENT_REVIEW_REQUIRED")
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance)
}

func TestPaymentReviewCanBeResolvedByVerifiedManualHash(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	second := newSecondSettlementOrder(t, f)
	_, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	require.NoError(t, err)
	requireManualSettleReason(t, f.svc.confirmPayment(ctx, second.ID, second.PaymentTradeNo, 30, payment.TypeEpusdt, nil), "PAYMENT_REVIEW_REQUIRED")
	_, err = f.svc.AdminSettleOrderByTxHash(ctx, second.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	requireManualSettleReason(t, err, "TX_ALREADY_USED")
	n, err := f.client.PaymentTransactionClaim.Query().Where(paymenttransactionclaim.ReviewPendingEQ(true)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	otherHash := "0x" + strings.Repeat("d", 64)
	f.fetcher.byNetwork["binance"][0].TxHash = otherHash
	_, err = f.svc.AdminSettleOrderByTxHash(ctx, second.ID, ManualSettleRequest{TxHash: otherHash})
	require.NoError(t, err)
	require.Equal(t, 60.0, f.userRepo.getByIDUser.Balance)
	n, err = f.client.PaymentTransactionClaim.Query().Where(paymenttransactionclaim.ReviewPendingEQ(true)).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestPaymentLateCallbackEnrichesManualEvidenceWithoutReplacingIt(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	_, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	require.NoError(t, err)
	require.NoError(t, f.svc.confirmPayment(ctx, f.order.ID, f.order.PaymentTradeNo, 30, payment.TypeEpusdt, map[string]string{"block_transaction_id": manualSettleTxHash}))
	claim, err := f.client.PaymentTransactionClaim.Query().Where(paymenttransactionclaim.TxHashEQ(strings.TrimPrefix(manualSettleTxHash, "0x"))).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, claimSourceManual, claim.Source)
	require.NotNil(t, claim.TransferTime)
	require.Contains(t, claim.Evidence, "transfers")
	require.Contains(t, claim.Evidence, "gateway_confirmation")
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance)
}
