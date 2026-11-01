//go:build integration

package repository

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type refundFixture struct {
	*userBalanceFixture
	svc   *service.PaymentService
	order *dbent.PaymentOrder
}

// The order has no trade number, so reaching the channel step leaves a
// REFUND_NO_TRADE_NO audit row instead of calling a provider.
func newRefundFixture(t *testing.T, balance, orderAmount float64) *refundFixture {
	t.Helper()
	ctx := context.Background()
	f := newUserBalanceFixture(t, balance)
	suffix := uuid.NewString()
	instance, err := f.client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("alipay-" + suffix).
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetAllowUserRefund(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)
	order, err := f.client.PaymentOrder.Create().
		SetUserID(f.user.ID).
		SetUserEmail(f.user.Email).
		SetUserName("refund").
		SetAmount(orderAmount).
		SetPayAmount(orderAmount).
		SetFeeRate(0).
		SetRechargeCode("R" + suffix[:24]).
		SetOutTradeNo("refund_" + suffix[:24]).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("").
		SetProviderInstanceID(strconv.FormatInt(instance.ID, 10)).
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(service.OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	return &refundFixture{
		userBalanceFixture: f,
		svc:                service.NewPaymentService(f.client, nil, nil, nil, nil, nil, f.users, nil, nil),
		order:              order,
	}
}

func (f *refundFixture) requireChannelReached(t *testing.T, want bool) {
	t.Helper()
	count, err := f.client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(f.order.ID, 10)), paymentauditlog.ActionEQ("REFUND_NO_TRADE_NO")).
		Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, want, count > 0)
}

func TestUserBalanceRefund_EffectiveBalanceShortOfRefundIsRejected(t *testing.T) {
	f := newRefundFixture(t, 100, 100)
	f.deferCharge(t, 80) // users.balance alone would pass
	ctx := context.Background()

	require.Error(t, f.svc.RequestRefund(ctx, f.order.ID, f.user.ID, "reason"))

	plan, early, err := f.svc.PrepareRefund(ctx, f.order.ID, 100, "reason", false, true)
	require.NoError(t, err)
	require.Nil(t, plan)
	require.True(t, early.RequireForce)
	f.requireLedger(t, "100", "80")
}

func TestUserBalanceRefund_SpendingAfterPrepareStopsRefundWithoutForce(t *testing.T) {
	f := newRefundFixture(t, 100, 100)
	ctx := context.Background()

	plan, early, err := f.svc.PrepareRefund(ctx, f.order.ID, 100, "reason", false, true)
	require.NoError(t, err)
	require.Nil(t, early)

	f.deferCharge(t, 30)
	result, err := f.svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.False(t, result.Success)
	require.True(t, result.RequireForce)

	f.requireChannelReached(t, false)
	f.requireLedger(t, "100", "30")
	stored, err := f.client.PaymentOrder.Get(ctx, f.order.ID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusRefundFailed, stored.Status)
}

func TestUserBalanceRefund_SpendingAfterPrepareDeductsPartiallyWithForce(t *testing.T) {
	f := newRefundFixture(t, 100, 100)
	ctx := context.Background()

	plan, early, err := f.svc.PrepareRefund(ctx, f.order.ID, 100, "reason", true, true)
	require.NoError(t, err)
	require.Nil(t, early)

	f.deferCharge(t, 30)
	result, err := f.svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 70.0, result.BalanceDeducted)

	f.requireChannelReached(t, true)
	f.requireLedger(t, "30", "30")
	f.requireEffective(t, 0)
}

func TestUserBalanceRefund_EnoughEffectiveBalanceRefundsInFull(t *testing.T) {
	f := newRefundFixture(t, 100, 50)
	f.deferCharge(t, 30)
	ctx := context.Background()

	require.NoError(t, f.svc.RequestRefund(ctx, f.order.ID, f.user.ID, "reason"))
	plan, early, err := f.svc.PrepareRefund(ctx, f.order.ID, 50, "reason", false, true)
	require.NoError(t, err)
	require.Nil(t, early)

	result, err := f.svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 50.0, result.BalanceDeducted)
	f.requireChannelReached(t, true)
	f.requireLedger(t, "50", "30")
}
