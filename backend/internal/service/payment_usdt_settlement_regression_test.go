//go:build unit

package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/ent/paymenttransactionclaim"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/chainverify"
)

func TestPaymentSettlementRegressionHashlessActualAmountStillAcceptsLaterHash(t *testing.T) {
	f := newAutoSettleFixture(t)
	o := f.order("a@example.com", OrderStatusPending, "first", "150", "150.15", f.now.Add(-5*time.Minute))
	ctx := context.Background()
	require.NoError(t, f.svc.confirmPayment(ctx, o.ID, o.PaymentTradeNo, 150, payment.TypeEpusdt, map[string]string{"actual_amount": "150.15"}))
	err := f.svc.confirmPayment(ctx, o.ID, o.PaymentTradeNo, 150, payment.TypeEpusdt, map[string]string{"actual_amount": "150.15", "block_transaction_id": autoSettleTxHash})
	claims, e := f.client.PaymentTransactionClaim.Query().Where(paymenttransactionclaim.OrderIDEQ(o.ID)).All(ctx)
	require.NoError(t, e)
	for _, c := range claims {
		t.Logf("claim=%s source=%s", c.TxHash, c.Source)
	}
	t.Logf("status=%s pay_amount=%v balance=%v", f.reload(o.ID).Status, f.reload(o.ID).PayAmount, f.userRepo.getByIDUser.Balance)
	require.NoError(t, err, "later signed callback must link the transaction after a hashless query")
	linked, err := f.client.PaymentTransactionClaim.Query().Where(paymenttransactionclaim.OrderIDEQ(o.ID), paymenttransactionclaim.TxHashEQ(strings.TrimPrefix(autoSettleTxHash, "0x"))).Exist(ctx)
	require.NoError(t, err)
	require.True(t, linked)
	require.Equal(t, 1066.07, f.userRepo.getByIDUser.Balance)
	blockedBy, err := hashlessPaidOrderNear(ctx, f.client, f.now, o.ID+1)
	require.NoError(t, err)
	require.Zero(t, blockedBy)
}
func TestPaymentSettlementRegressionRepeatGatewayCallbackRemainsIdempotent(t *testing.T) {
	f := newAutoSettleFixture(t)
	o := f.order("a@example.com", OrderStatusPending, "first", "150", "150.15", f.now.Add(-5*time.Minute))
	md := map[string]string{"actual_amount": "150.15", "block_transaction_id": autoSettleTxHash}
	require.NoError(t, f.svc.confirmPayment(context.Background(), o.ID, o.PaymentTradeNo, 150, payment.TypeEpusdt, md))
	require.NoError(t, f.svc.confirmPayment(context.Background(), o.ID, o.PaymentTradeNo, 150, payment.TypeEpusdt, md))
}
func TestPaymentSettlementRegressionDelayedQueueCoversOldCompetingGatewayOrder(t *testing.T) {
	f := newAutoSettleFixture(t)
	o := f.order("a@example.com", OrderStatusExpired, "first", "100", "100", f.now.Add(-4*time.Hour-5*time.Minute))
	f.transfer(autoSettleTxHash, "99.9", f.now.Add(-4*time.Hour), 900)
	// Discovered 2h30 ago, still within the 3h queue TTL.
	q := f.queue()
	q.add(f.now.Add(-150*time.Minute), "binance", []string{manualSettleAddress}, f.scanner.incoming)
	q.cursor = f.scanner.head - chainverify.MinConfirmations("binance")
	f.provider.gateway = []payment.GatewayOrder{{TradeNo: "other-site", ActualAmount: decimal.RequireFromString("100"), Status: payment.GatewayOrderExpired, CreatedAt: f.now.Add(-390 * time.Minute)}}
	n := f.run()
	t.Logf("settled=%d status=%s", n, f.reload(o.ID).Status)
	require.Zero(t, n, "gateway window must start from the actual transfer time, not its discovery time")
}

const regressionAddressB = "0x1111111111111111111111111111111111111111"

type regressionMultiAddressProvider struct {
	*autoSettleProvider
	queried []string
}

func (p *regressionMultiAddressProvider) AutoSettleScope() (string, string, []string) {
	return "binance", "USDT", []string{manualSettleAddress, regressionAddressB}
}
func (p *regressionMultiAddressProvider) GatewayOrdersSince(_ context.Context, _, _, addr string, since time.Time) ([]payment.GatewayOrder, error) {
	p.queried = append(p.queried, addr)
	var out []payment.GatewayOrder
	for _, g := range p.gateway {
		if strings.EqualFold(g.ReceiveAddress, addr) && !g.CreatedAt.Before(since) {
			out = append(out, g)
		}
	}
	return out, nil
}
func TestPaymentSettlementRegressionBatchTransferChecksAllReceivingAddresses(t *testing.T) {
	f := newAutoSettleFixture(t)
	o := f.order("a@example.com", OrderStatusPending, "first", "100", "100", f.now.Add(-5*time.Minute))
	f.provider.targets[o.PaymentTradeNo].TrustedAddresses = []string{regressionAddressB}
	f.transfer(autoSettleTxHash, "50", f.now.Add(-time.Minute), 900)
	second := f.scanner.incoming[0]
	second.To = regressionAddressB
	second.Amount = decimal.RequireFromString("49.9")
	second.LogIndex = 1
	f.scanner.incoming = append(f.scanner.incoming, second)
	hash, _ := chainverify.NormalizeTxHash(autoSettleTxHash)
	f.scanner.transfers[hash] = append(f.scanner.transfers[hash], second.Transfer)
	p := &regressionMultiAddressProvider{autoSettleProvider: f.provider}
	p.gateway = []payment.GatewayOrder{{TradeNo: "other-site-b", ReceiveAddress: regressionAddressB, ActualAmount: decimal.RequireFromString("49.9"), Status: payment.GatewayOrderWaiting, CreatedAt: f.now.Add(-4 * time.Minute)}}
	createPaymentProviderFromInstance = func(string, string, map[string]string) (payment.Provider, error) { return p, nil }
	n := f.run()
	t.Logf("settled=%d queried=%v balance=%v", n, p.queried, f.userRepo.getByIDUser.Balance)
	require.Zero(t, n, "batch transaction includes a payment matching another merchant at the second address")
	require.Equal(t, OrderStatusPending, f.reload(o.ID).Status)
	require.Zero(t, f.userRepo.getByIDUser.Balance)
	logs := f.audits(o.ID, auditActionAutoSkipped)
	require.Len(t, logs, 1)
	require.Contains(t, logs[0].Detail, "MULTIPLE_RECEIVING_ADDRESSES")
}

func TestPaymentSettlementRegressionCallbackAfterAutoSettlement(t *testing.T) {
	f := newAutoSettleFixture(t)
	o := f.order("a@example.com", OrderStatusPending, "first", "150", "150.15", f.now.Add(-5*time.Minute))
	f.transfer(autoSettleTxHash, "149.9", f.now.Add(-time.Minute), 900)
	require.Equal(t, 1, f.run())
	balance := f.userRepo.getByIDUser.Balance
	ctx := context.Background()
	err := f.svc.confirmPayment(ctx, o.ID, o.PaymentTradeNo, 150, payment.TypeEpusdt, map[string]string{"actual_amount": "149.9", "block_transaction_id": autoSettleTxHash})
	require.NoError(t, err)
	require.Equal(t, balance, f.userRepo.getByIDUser.Balance)
	require.Equal(t, 149.9, f.reload(o.ID).PayAmount)
}
func TestPaymentSettlementRegressionStillRejectsWrongGatewayAmount(t *testing.T) {
	f := newAutoSettleFixture(t)
	o := f.order("a@example.com", OrderStatusPending, "first", "150", "150.15", f.now.Add(-5*time.Minute))
	ctx := context.Background()
	md := map[string]string{"actual_amount": "150.15", "block_transaction_id": autoSettleTxHash}
	require.NoError(t, f.svc.confirmPayment(ctx, o.ID, o.PaymentTradeNo, 150, payment.TypeEpusdt, md))
	err := f.svc.confirmPayment(ctx, o.ID, o.PaymentTradeNo, 150.15, payment.TypeEpusdt, md)
	require.ErrorContains(t, err, "amount mismatch")
	require.Equal(t, 1066.07, f.userRepo.getByIDUser.Balance)
}
func TestPaymentSettlementRegressionSingleAddressMultipleLogs(t *testing.T) {
	f := newAutoSettleFixture(t)
	o := f.order("a@example.com", OrderStatusPending, "first", "100", "100", f.now.Add(-5*time.Minute))
	f.transfer(autoSettleTxHash, "50", f.now.Add(-time.Minute), 900)
	second := f.scanner.incoming[0]
	second.Amount = decimal.RequireFromString("49.9")
	second.LogIndex = 1
	second.To = "0x" + strings.ToUpper(strings.TrimPrefix(second.To, "0x"))
	f.scanner.incoming = append(f.scanner.incoming, second)
	hash, _ := chainverify.NormalizeTxHash(autoSettleTxHash)
	f.scanner.transfers[hash] = append(f.scanner.transfers[hash], second.Transfer)
	require.Equal(t, 1, f.run())
	require.Equal(t, 709.29, f.reload(o.ID).Amount)
}
func TestPaymentSettlementRegressionReceiptDetectsAdditionalRecipient(t *testing.T) {
	f := newAutoSettleFixture(t)
	o := f.order("a@example.com", OrderStatusPending, "first", "100", "100", f.now.Add(-5*time.Minute))
	f.provider.targets[o.PaymentTradeNo].TrustedAddresses = []string{regressionAddressB}
	f.transfer(autoSettleTxHash, "99", f.now.Add(-time.Minute), 900)
	hash, _ := chainverify.NormalizeTxHash(autoSettleTxHash)
	second := f.scanner.transfers[hash][0]
	second.To = regressionAddressB
	second.Amount = decimal.RequireFromString("0.9")
	f.scanner.transfers[hash] = append(f.scanner.transfers[hash], second)
	require.Zero(t, f.run(), "full receipt must be checked even when the scan only returned one recipient")
	require.Equal(t, OrderStatusPending, f.reload(o.ID).Status)
	require.Len(t, f.audits(o.ID, auditActionAutoSkipped), 1)
}

func TestPaymentSettlementRegressionCreationLocksGatewayAmount(t *testing.T) {
	f := newAutoSettleFixture(t)
	seed := f.order("a@example.com", OrderStatusCancelled, "seed", "150", "150", f.now.Add(-5*time.Minute))
	ctx := context.Background()
	q := &usdtQuote{rate: decimal.RequireFromString(autoSettleRate), multiplier: 1}
	req := CreateOrderRequest{UserID: seed.UserID, PaymentType: payment.TypeUSDT, OrderType: payment.OrderTypeBalance, Amount: 150, usdtQuote: q}
	cfg := &PaymentConfig{MaxPendingOrders: 10, OrderTimeoutMin: 10}
	sel := &payment.InstanceSelection{InstanceID: f.instance, ProviderKey: payment.TypeEpusdt, Config: map[string]string{"currency": "USD"}}
	o, err := f.svc.createOrderInTx(ctx, req, &User{ID: seed.UserID, Email: seed.UserEmail, Username: seed.UserName}, nil, cfg, 1065, 150, 0, 150, sel)
	require.NoError(t, err)
	require.Equal(t, "150", f.reload(o.ID).ProviderSnapshot["usdt_gateway_amount"])
	require.Equal(t, 150.0, paymentGatewayAmount(f.reload(o.ID)))
}
