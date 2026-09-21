//go:build unit

package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/chainverify"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	manualSettleTxHash  = "0x2a003b114b896199d75998e3712f8cc1f32118ed62ff38419d397282b183c404"
	manualSettleAddress = "0x4c1349a30c3a91d2cd69329c48dfb02c10d812c7"
)

type manualSettleProvider struct {
	paymentOrderLifecycleQueryProvider
	target *payment.OnChainSettlementTarget
}

func (p *manualSettleProvider) OnChainSettlementTarget(context.Context, string) (*payment.OnChainSettlementTarget, error) {
	return p.target, nil
}

type manualSettleFetcher struct {
	byNetwork map[string][]chainverify.Transfer
	errs      map[string]error
	probed    []string
}

func (f *manualSettleFetcher) Transfers(_ context.Context, network, _ string) ([]chainverify.Transfer, error) {
	f.probed = append(f.probed, network)
	if err := f.errs[network]; err != nil {
		return nil, err
	}
	if trs, ok := f.byNetwork[network]; ok {
		return trs, nil
	}
	return nil, chainverify.ErrTxNotFound
}

type manualSettleFixture struct {
	svc      *PaymentService
	client   *dbent.Client
	order    *dbent.PaymentOrder
	userRepo *mockUserRepo
	fetcher  *manualSettleFetcher
	provider *manualSettleProvider
}

func newManualSettleFixture(t *testing.T, status string, received string) *manualSettleFixture {
	t.Helper()
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)

	user, err := client.User.Create().SetEmail("settle@example.com").SetPasswordHash("hash").SetUsername("settle-user").Save(ctx)
	require.NoError(t, err)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).SetUserEmail(user.Email).SetUserName(user.Username).
		SetAmount(30).SetPayAmount(30).SetFeeRate(0).
		SetRechargeCode("MANUAL-SETTLE-CODE").
		SetOutTradeNo("sub2_manual_settle").
		SetPaymentType(payment.TypeUSDT).
		SetPaymentTradeNo("gateway-trade-1").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(status).
		SetExpiresAt(time.Now().Add(-time.Hour)).
		SetClientIP("127.0.0.1").SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	userRepo := &mockUserRepo{getByIDUser: &User{ID: user.ID, Email: user.Email, Username: user.Username}}
	userRepo.updateBalanceFn = func(_ context.Context, _ int64, amount float64) error {
		userRepo.getByIDUser.Balance += amount
		return nil
	}
	redeemRepo := &paymentOrderLifecycleRedeemRepo{codesByCode: map[string]*RedeemCode{
		order.RechargeCode: {ID: 1, Code: order.RechargeCode, Type: RedeemTypeBalance, Value: order.Amount, Status: StatusUnused},
	}}
	provider := &manualSettleProvider{
		paymentOrderLifecycleQueryProvider: paymentOrderLifecycleQueryProvider{key: payment.TypeEpusdt},
		target: &payment.OnChainSettlementTarget{
			Network: "binance", Token: "USDT", ReceiveAddress: manualSettleAddress, ExpectedAmount: "4.48",
		},
	}
	registry := payment.NewRegistry()
	registry.Register(provider)

	fetcher := &manualSettleFetcher{byNetwork: map[string][]chainverify.Transfer{
		"binance": {{
			Network: "binance", TxHash: manualSettleTxHash, Token: "USDT",
			From: "0xeb2d2f1b8c558a40207669291fda468e50c8a0bb", To: manualSettleAddress,
			Amount: decimal.RequireFromString(received), BlockTime: order.CreatedAt.Add(3 * time.Minute), Confirmations: 100,
		}},
	}}
	previous := newTransferFetcher
	newTransferFetcher = func(map[string][]string) transferFetcher { return fetcher }
	t.Cleanup(func() { newTransferFetcher = previous })

	return &manualSettleFixture{
		svc: &PaymentService{
			entClient:       client,
			registry:        registry,
			redeemService:   NewRedeemService(redeemRepo, userRepo, nil, nil, nil, client, nil, nil),
			userRepo:        userRepo,
			providersLoaded: true,
		},
		client: client, order: order, userRepo: userRepo, fetcher: fetcher, provider: provider,
	}
}

func requireManualSettleReason(t *testing.T, err error, reason string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, infraerrors.Reason(err), err.Error())
}

func TestAdminSettleOrderByTxHashSettlesShortPaidExpiredOrder(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusExpired, "4.47")

	preview, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash, DryRun: true})
	require.NoError(t, err)
	require.False(t, preview.Settled)
	require.Equal(t, "4.47", preview.ReceivedAmount)
	require.Equal(t, "0.01", preview.Shortfall)
	require.Equal(t, "binance", preview.Network)
	reloaded, err := f.client.PaymentOrder.Get(ctx, f.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusExpired, reloaded.Status, "dry run must not touch the order")
	require.Zero(t, f.userRepo.getByIDUser.Balance)

	result, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash, Operator: "admin:7"})
	require.NoError(t, err)
	require.True(t, result.Settled)
	require.Equal(t, OrderStatusCompleted, result.OrderStatus)
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance, "the order is credited in full")

	reloaded, err = f.client.PaymentOrder.Get(ctx, f.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
	require.Equal(t, 30.0, reloaded.PayAmount)
	require.Equal(t, "gateway-trade-1", reloaded.PaymentTradeNo)

	audit, err := f.client.PaymentAuditLog.Query().Where(
		paymentauditlog.OrderIDEQ(strconv.FormatInt(f.order.ID, 10)),
		paymentauditlog.ActionEQ(auditActionManualSettled),
	).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, "admin:7", audit.Operator)
	require.Contains(t, audit.Detail, "2a003b114b896199d75998e3712f8cc1f32118ed62ff38419d397282b183c404")
	require.Contains(t, audit.Detail, `"shortfall":"0.01"`)
}

func TestAdminSettleOrderByTxHashRejectsReusedHash(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	// Another order was already paid by this transaction through the gateway callback.
	f.svc.writeAuditLog(ctx, 999, "ORDER_PAID", payment.TypeEpusdt, map[string]any{
		"tradeNo": "other", "txHash": "2A003B114B896199D75998E3712F8CC1F32118ED62FF38419D397282B183C404",
	})
	// Migration 240 has materialized this historical audit before serving traffic.
	_, err := f.client.PaymentTransactionClaim.Create().SetTxHash(strings.TrimPrefix(manualSettleTxHash, "0x")).SetOrderID(999).Save(ctx)
	require.NoError(t, err)

	_, err = f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	requireManualSettleReason(t, err, "TX_ALREADY_USED")
	require.Empty(t, f.fetcher.probed, "reuse is rejected before touching the chain")

}

func TestAdminSettleOrderByTxHashCannotSettleTwoOrdersWithOneHash(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	second, err := f.client.PaymentOrder.Create().
		SetUserID(f.order.UserID).SetUserEmail(f.order.UserEmail).SetUserName(f.order.UserName).
		SetAmount(30).SetPayAmount(30).SetFeeRate(0).
		SetRechargeCode("MANUAL-SETTLE-CODE-2").SetOutTradeNo("sub2_manual_settle_2").
		SetPaymentType(payment.TypeUSDT).SetPaymentTradeNo("gateway-trade-2").
		SetOrderType(payment.OrderTypeBalance).SetStatus(OrderStatusExpired).
		SetExpiresAt(time.Now().Add(-time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	_, err = f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	require.NoError(t, err)
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance)

	_, err = f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	requireManualSettleReason(t, err, "INVALID_STATUS")
	_, err = f.svc.AdminSettleOrderByTxHash(ctx, second.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	requireManualSettleReason(t, err, "TX_ALREADY_USED")
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance, "the same transfer is never credited twice")
}

func TestAdminSettleOrderByTxHashGuards(t *testing.T) {
	ctx := context.Background()

	t.Run("completed order", func(t *testing.T) {
		f := newManualSettleFixture(t, OrderStatusCompleted, "4.48")
		_, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
		requireManualSettleReason(t, err, "INVALID_STATUS")
	})

	t.Run("malformed hash", func(t *testing.T) {
		f := newManualSettleFixture(t, OrderStatusPending, "4.48")
		_, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: "0x1234"})
		requireManualSettleReason(t, err, "INVALID_TX_HASH")
	})

	t.Run("payer never picked a chain", func(t *testing.T) {
		f := newManualSettleFixture(t, OrderStatusExpired, "4.48")
		f.provider.target = &payment.OnChainSettlementTarget{}
		_, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
		requireManualSettleReason(t, err, "NO_CHAIN_QUOTE")
	})

	t.Run("paid to someone else", func(t *testing.T) {
		f := newManualSettleFixture(t, OrderStatusExpired, "4.48")
		f.fetcher.byNetwork["binance"][0].To = "0x000000000000000000000000000000000000dead"
		_, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
		requireManualSettleReason(t, err, "ADDRESS_MISMATCH")
		require.Zero(t, f.userRepo.getByIDUser.Balance)
	})

	t.Run("trusted address after network switch", func(t *testing.T) {
		f := newManualSettleFixture(t, OrderStatusExpired, "4.48")
		switched := "0x1111111111111111111111111111111111111111"
		f.fetcher.byNetwork = map[string][]chainverify.Transfer{"polygon": {{
			Network: "polygon", TxHash: manualSettleTxHash, Token: "USDC", To: switched,
			Amount: decimal.RequireFromString("4.48"), BlockTime: f.order.CreatedAt.Add(time.Minute), Confirmations: 99,
		}}}
		f.provider.target.TrustedAddresses = []string{switched}
		result, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash, DryRun: true})
		require.NoError(t, err)
		require.Equal(t, "polygon", result.Network)
		require.Equal(t, []string{"binance", "polygon"}, f.fetcher.probed, "gateway network is probed first")
	})

	t.Run("transaction older than the order", func(t *testing.T) {
		f := newManualSettleFixture(t, OrderStatusExpired, "4.48")
		f.fetcher.byNetwork["binance"][0].BlockTime = f.order.CreatedAt.Add(-5 * time.Minute)
		_, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
		requireManualSettleReason(t, err, "TX_OUTSIDE_ORDER_WINDOW")
	})

	t.Run("far above the quote is someone else's payment", func(t *testing.T) {
		f := newManualSettleFixture(t, OrderStatusExpired, "50")
		_, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
		requireManualSettleReason(t, err, "AMOUNT_MISMATCH")
		require.Zero(t, f.userRepo.getByIDUser.Balance)
	})

	t.Run("small overpayment is fine", func(t *testing.T) {
		f := newManualSettleFixture(t, OrderStatusExpired, "5")
		result, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash, DryRun: true})
		require.NoError(t, err)
		require.Equal(t, "0", result.Shortfall)
	})

	t.Run("transaction a day after the order", func(t *testing.T) {
		f := newManualSettleFixture(t, OrderStatusExpired, "4.48")
		f.fetcher.byNetwork["binance"][0].BlockTime = f.order.CreatedAt.Add(25 * time.Hour)
		_, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
		requireManualSettleReason(t, err, "TX_OUTSIDE_ORDER_WINDOW")
	})

	t.Run("shortfall too large", func(t *testing.T) {
		f := newManualSettleFixture(t, OrderStatusExpired, "2.9")
		_, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
		requireManualSettleReason(t, err, "SHORTFALL_TOO_LARGE")
		require.Zero(t, f.userRepo.getByIDUser.Balance)
	})

	t.Run("not final yet", func(t *testing.T) {
		f := newManualSettleFixture(t, OrderStatusPending, "4.48")
		f.fetcher.errs = map[string]error{"binance": fmt.Errorf("%w: 2 of 5 on binance", chainverify.ErrUnconfirmed)}
		_, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
		requireManualSettleReason(t, err, "TX_UNCONFIRMED")
	})

	t.Run("unknown everywhere", func(t *testing.T) {
		f := newManualSettleFixture(t, OrderStatusPending, "4.48")
		f.fetcher.byNetwork = nil
		_, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
		requireManualSettleReason(t, err, "TX_NOT_FOUND")
		require.Equal(t, []string{"binance", "polygon", "ethereum"}, f.fetcher.probed, "a 0x hash is never probed on tron")
	})
}

func TestManualSettleAllowedShortfall(t *testing.T) {
	for expected, want := range map[string]string{
		"4.48": "1.5",  // floor of 1.5 tokens covers a TRC20 exchange fee
		"1":    "0.5",  // ...but never more than half the quote
		"100":  "20",   // 20% on larger orders
		"2.5":  "1.25", // cap wins over the floor
	} {
		got := manualSettleAllowedShortfall(decimal.RequireFromString(expected))
		require.True(t, got.Equal(decimal.RequireFromString(want)), "expected %s: got %s want %s", expected, got, want)
	}
}

func TestConfirmPaymentAuditsGatewayTxHash(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	err := f.svc.confirmPayment(ctx, f.order.ID, "gateway-trade-1", 30, payment.TypeEpusdt, map[string]string{
		"block_transaction_id": manualSettleTxHash,
	})
	require.NoError(t, err)
	usedBy, err := f.svc.orderUsingTxHash(ctx, "2a003b114b896199d75998e3712f8cc1f32118ed62ff38419d397282b183c404")
	require.NoError(t, err)
	require.Equal(t, strconv.FormatInt(f.order.ID, 10), usedBy)
}

// Regression coverage for cross-order settlement and quote currency invariants.
type settlementControlledFetcher func(context.Context, string, string) ([]chainverify.Transfer, error)

func (fn settlementControlledFetcher) Transfers(ctx context.Context, network, hash string) ([]chainverify.Transfer, error) {
	return fn(ctx, network, hash)
}

func newSecondSettlementOrder(t *testing.T, f *manualSettleFixture) *dbent.PaymentOrder {
	t.Helper()
	o, err := f.client.PaymentOrder.Create().
		SetUserID(f.order.UserID).SetUserEmail(f.order.UserEmail).SetUserName(f.order.UserName).
		SetAmount(30).SetPayAmount(30).SetFeeRate(0).
		SetRechargeCode("REVIEW-SECOND-CODE").SetOutTradeNo("sub2_review_second").
		SetPaymentType(payment.TypeUSDT).SetPaymentTradeNo("gateway-trade-2").
		SetOrderType(payment.OrderTypeBalance).SetStatus(OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("api.example.com").Save(context.Background())
	require.NoError(t, err)
	repo := f.svc.redeemService.redeemRepo.(*paymentOrderLifecycleRedeemRepo)
	repo.codesByCode[o.RechargeCode] = &RedeemCode{ID: 2, Code: o.RechargeCode, Type: RedeemTypeBalance, Value: o.Amount, Status: StatusUnused}
	return o
}

func TestPaymentConcurrentManualSettleMustConsumeHashOnce(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	second := newSecondSettlementOrder(t, f)
	arrived := make(chan chan struct{}, 2)
	transfers := f.fetcher.byNetwork["binance"]
	newTransferFetcher = func(map[string][]string) transferFetcher {
		return settlementControlledFetcher(func(_ context.Context, _, _ string) ([]chainverify.Transfer, error) {
			gate := make(chan struct{})
			arrived <- gate
			<-gate
			return transfers, nil
		})
	}
	firstDone := make(chan error, 1)
	go func() {
		_, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
		firstDone <- err
	}()
	firstGate := <-arrived
	secondDone := make(chan error, 1)
	go func() {
		_, err := f.svc.AdminSettleOrderByTxHash(ctx, second.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
		secondDone <- err
	}()
	secondGate := <-arrived
	// Both requests have already passed the lookup, before either writes a claim.
	// Serialize actual fulfillment to avoid involving unrelated mock/data races.
	close(firstGate)
	require.NoError(t, <-firstDone)
	close(secondGate)
	secondErr := <-secondDone
	requireManualSettleReason(t, secondErr, "TX_ALREADY_USED")
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance, "one on-chain transfer must credit at most one order")
}

func TestPaymentCallbackAfterManualSettleMustConsumeHashOnce(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	second := newSecondSettlementOrder(t, f)
	_, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	require.NoError(t, err)
	err = f.svc.confirmPayment(ctx, second.ID, "gateway-trade-2", 30, payment.TypeEpusdt, map[string]string{"block_transaction_id": manualSettleTxHash})
	requireManualSettleReason(t, err, "TX_ALREADY_USED")
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance, "callback must respect a hash already consumed by manual settlement")
}

func TestPaymentQueryThenCallbackMustPreserveHashClaim(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	second := newSecondSettlementOrder(t, f)
	// Epusdt.QueryOrder emits these fields but has no block_transaction_id.
	f.provider.resp = &payment.QueryOrderResponse{TradeNo: "gateway-trade-1", Amount: 30, Status: payment.ProviderStatusPaid,
		Metadata: map[string]string{"token": "USDT", "network": "binance", "actual_amount": "4.48"}}
	f.svc.reconcilePaid(ctx, f.order)
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance, "a payment the gateway confirms is credited even without a hash")
	// The real signed webhook arrives after the periodic query completed the order.
	require.NoError(t, f.svc.confirmPayment(ctx, f.order.ID, "gateway-trade-1", 30, payment.TypeEpusdt,
		map[string]string{"block_transaction_id": manualSettleTxHash}))
	_, err := f.svc.AdminSettleOrderByTxHash(ctx, second.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	requireManualSettleReason(t, err, "TX_ALREADY_USED")
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance, "a queried payment's hash must remain unavailable for another order")
}

func TestPaymentNativeCoinQuoteMustNotBeComparedOneToOneWithStablecoin(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "0.2")
	// SOL is a selectable native asset in the gateway; the trusted BSC wallet
	// is configured for settling cases where a payer switches networks.
	f.provider.target.Token = "SOL"
	f.provider.target.Network = "solana"
	f.provider.target.ExpectedAmount = "0.2"
	f.provider.target.ReceiveAddress = "solana-order-receiving-address"
	f.provider.target.TrustedAddresses = []string{manualSettleAddress}
	result, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	if result != nil {
		t.Logf("expected=%s %s; received=%s %s; settled=%v; credited balance=%v", result.ExpectedAmount, result.ExpectedToken, result.ReceivedAmount, result.Token, result.Settled, f.userRepo.getByIDUser.Balance)
	}
	requireManualSettleReason(t, err, "UNSUPPORTED_QUOTE_TOKEN")
	require.Zero(t, f.userRepo.getByIDUser.Balance)
}

func TestPaymentLateCallbackRegistersHashForCompletedOrder(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusCompleted, "4.48")
	second := newSecondSettlementOrder(t, f)
	require.NoError(t, f.svc.confirmPayment(ctx, f.order.ID, "gateway-trade-1", 30, payment.TypeEpusdt,
		map[string]string{"block_transaction_id": manualSettleTxHash}))
	require.Zero(t, f.userRepo.getByIDUser.Balance, "a completed order is not fulfilled again")
	_, err := f.svc.AdminSettleOrderByTxHash(ctx, second.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	requireManualSettleReason(t, err, "TX_ALREADY_USED")
	require.Zero(t, f.userRepo.getByIDUser.Balance)
}

// A parent order paid through a sub-order (the payer switched network in the
// cashier) is notified with the parent row, which carries no chain hash.
func TestPaymentEpusdtHashlessCallbackIsCreditedOncePerTrade(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	hashless := map[string]string{"token": "USDT"}
	for i := 0; i < 3; i++ { // the gateway retries its callback
		require.NoError(t, f.svc.confirmPayment(ctx, f.order.ID, "gateway-trade-1", 30, payment.TypeEpusdt, hashless))
	}
	order, err := f.client.PaymentOrder.Get(ctx, f.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, order.Status)
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance, "credited exactly once")

	claims, err := f.client.PaymentTransactionClaim.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, "epusdt-trade:gateway-trade-1", claims[0].TxHash)
	paid, err := f.client.PaymentAuditLog.Query().Where(paymentauditlog.ActionEQ("ORDER_PAID")).Only(ctx)
	require.NoError(t, err)
	require.Contains(t, paid.Detail, `"claimKey":"epusdt-trade:gateway-trade-1"`)
	require.NotContains(t, paid.Detail, "txHash", "a trade key must not pose as a chain hash")
	linked, err := f.client.PaymentAuditLog.Query().Where(paymentauditlog.ActionEQ("ORDER_TX_LINKED")).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, linked, "callback retries add no audit noise")

	// A confirmation with neither hash nor trade number has nothing to key on.
	err = f.svc.confirmPayment(ctx, f.order.ID, " ", 30, payment.TypeEpusdt, hashless)
	requireManualSettleReason(t, err, "MISSING_TRADE_NO")
}

// The gateway query API never reports a hash; it is the only safety net when
// a callback is lost (the gateway retries once), so it must still credit.
func TestPaymentEpusdtPaidQueryCreditsThenCallbackLinksHash(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	second := newSecondSettlementOrder(t, f)
	f.provider.resp = &payment.QueryOrderResponse{TradeNo: f.order.PaymentTradeNo, Amount: 30, Status: payment.ProviderStatusPaid}

	require.Equal(t, checkPaidResultAlreadyPaid, f.svc.reconcilePaid(ctx, f.order))
	current, err := f.client.PaymentOrder.Get(ctx, f.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, current.Status)
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance)

	// The signed callback arrives afterwards, twice, with the real hash.
	withHash := map[string]string{"block_transaction_id": manualSettleTxHash}
	for i := 0; i < 2; i++ {
		require.NoError(t, f.svc.confirmPayment(ctx, f.order.ID, "gateway-trade-1", 30, payment.TypeEpusdt, withHash))
	}
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance, "linking the hash never credits again")
	linked, err := f.client.PaymentAuditLog.Query().Where(paymentauditlog.ActionEQ("ORDER_TX_LINKED")).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, linked, "the hash is audited once, not per retry")

	_, err = f.svc.AdminSettleOrderByTxHash(ctx, second.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	requireManualSettleReason(t, err, "TX_ALREADY_USED")
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance)
}

// The abuse this guards against: leave order B unpaid, pay order A through a
// switched network (credited, but its hash is never reported), then present
// A's transfer as the payment of B.
func TestPaymentManualSettleRefusesTransferNearHashlessPayment(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	second := newSecondSettlementOrder(t, f)
	require.NoError(t, f.svc.confirmPayment(ctx, f.order.ID, "gateway-trade-1", 30, payment.TypeEpusdt, map[string]string{"token": "USDT"}))
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance)

	f.fetcher.byNetwork["binance"][0].BlockTime = time.Now().Add(-time.Minute)
	for _, dryRun := range []bool{true, false} {
		_, err := f.svc.AdminSettleOrderByTxHash(ctx, second.ID, ManualSettleRequest{TxHash: manualSettleTxHash, DryRun: dryRun})
		requireManualSettleReason(t, err, "HASHLESS_PAYMENT_NEARBY")
		require.ErrorContains(t, err, fmt.Sprintf("order %d ", f.order.ID))
	}
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance, "the transfer is not credited a second time")

	// Once the first order's own hash is known, it no longer casts doubt.
	otherHash := strings.Repeat("b", 64)
	require.NoError(t, f.svc.confirmPayment(ctx, f.order.ID, "gateway-trade-1", 30, payment.TypeEpusdt, map[string]string{"block_transaction_id": otherHash}))
	_, err := f.svc.AdminSettleOrderByTxHash(ctx, second.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	require.NoError(t, err)
	require.Equal(t, 60.0, f.userRepo.getByIDUser.Balance)
}

func TestPaymentManualSettleIgnoresHashlessPaymentFromAnotherTime(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	second := newSecondSettlementOrder(t, f)
	// A genuinely disjoint order lifetime, not just an old callback timestamp.
	_, err := f.client.ExecContext(ctx, "UPDATE payment_orders SET created_at = ?, expires_at = ? WHERE id = ?", time.Now().Add(-72*time.Hour), time.Now().Add(-48*time.Hour), f.order.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.confirmPayment(ctx, f.order.ID, "gateway-trade-1", 30, payment.TypeEpusdt, map[string]string{"token": "USDT"}))
	_, err = f.client.PaymentOrder.UpdateOneID(f.order.ID).SetPaidAt(time.Now().Add(-6 * time.Hour)).Save(ctx)
	require.NoError(t, err)

	f.fetcher.byNetwork["binance"][0].BlockTime = time.Now().Add(-time.Minute)
	_, err = f.svc.AdminSettleOrderByTxHash(ctx, second.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	require.NoError(t, err)
	require.Equal(t, 60.0, f.userRepo.getByIDUser.Balance)
}

func TestPaymentManualSettlementRollsBackWhenAuditFails(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	second := newSecondSettlementOrder(t, f)
	failAudit := true
	f.client.PaymentAuditLog.Use(func(next dbent.Mutator) dbent.Mutator {
		return dbent.MutateFunc(func(ctx context.Context, m dbent.Mutation) (dbent.Value, error) {
			if failAudit {
				return nil, fmt.Errorf("injected audit failure")
			}
			return next.Mutate(ctx, m)
		})
	})
	_, err := f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	require.ErrorContains(t, err, "injected audit failure")
	current, err := f.client.PaymentOrder.Get(ctx, f.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPending, current.Status)
	require.Zero(t, f.userRepo.getByIDUser.Balance)
	// A rolled-back transaction must release its hash claim.
	failAudit = false
	_, err = f.svc.AdminSettleOrderByTxHash(ctx, second.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	require.NoError(t, err)
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance)
}

func TestPaymentEpusdtPaidQueryBlocksCancelAndExpiryByCrediting(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	f.provider.resp = &payment.QueryOrderResponse{TradeNo: f.order.PaymentTradeNo, Amount: 30, Status: payment.ProviderStatusPaid}
	outcome, err := f.svc.CancelOrder(ctx, f.order.ID, f.order.UserID)
	require.NoError(t, err)
	require.Equal(t, checkPaidResultAlreadyPaid, outcome)
	current, err := f.client.PaymentOrder.Get(ctx, f.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, current.Status, "a paid order is credited, not left pending forever")
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance)
	require.Zero(t, f.provider.cancelCalls)
}

func TestPaymentRepeatedAndAdditionalCallbacksClaimWithoutExtraCredit(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	second := newSecondSettlementOrder(t, f)
	otherHash := strings.Repeat("a", 64)
	for _, hash := range []string{manualSettleTxHash, manualSettleTxHash, otherHash} {
		require.NoError(t, f.svc.confirmPayment(ctx, f.order.ID, f.order.PaymentTradeNo, 30, payment.TypeEpusdt,
			map[string]string{"block_transaction_id": hash}))
	}
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance)
	_, err := f.svc.AdminSettleOrderByTxHash(ctx, second.ID, ManualSettleRequest{TxHash: otherHash})
	requireManualSettleReason(t, err, "TX_ALREADY_USED")
}

func TestPaymentOpaqueTransactionReferencesPreserveCase(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	for _, ref := range []string{"SolanaSignatureAbC", "SolanaSignatureabc"} {
		require.NoError(t, f.svc.confirmPayment(ctx, f.order.ID, f.order.PaymentTradeNo, 30, payment.TypeEpusdt,
			map[string]string{"block_transaction_id": ref}))
		usedBy, err := f.svc.orderUsingTxHash(ctx, "epusdt:"+ref)
		require.NoError(t, err)
		require.Equal(t, strconv.FormatInt(f.order.ID, 10), usedBy)
	}
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance)
}

func TestPaymentCallbackRollsBackTransactionClaimOnAuditFailure(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	f.client.PaymentAuditLog.Use(func(next dbent.Mutator) dbent.Mutator {
		return dbent.MutateFunc(func(context.Context, dbent.Mutation) (dbent.Value, error) {
			return nil, fmt.Errorf("injected callback audit failure")
		})
	})
	err := f.svc.confirmPayment(ctx, f.order.ID, f.order.PaymentTradeNo, 30, payment.TypeEpusdt,
		map[string]string{"block_transaction_id": manualSettleTxHash})
	require.ErrorContains(t, err, "injected callback audit failure")
	current, err := f.client.PaymentOrder.Get(ctx, f.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPending, current.Status)
	hash, err := chainverify.NormalizeTxHash(manualSettleTxHash)
	require.NoError(t, err)
	usedBy, err := f.svc.orderUsingTxHash(ctx, hash)
	require.NoError(t, err)
	require.Empty(t, usedBy)
	require.Zero(t, f.userRepo.getByIDUser.Balance)
}

func TestPaymentMigratedLegacyClaimPreventsCallbackReuse(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	// A current-order audit must not mask a migrated conflicting order's claim.
	f.svc.writeAuditLog(ctx, f.order.ID, "ORDER_PAID", payment.TypeEpusdt, map[string]any{"txHash": manualSettleTxHash})
	f.svc.writeAuditLog(ctx, 999, "ORDER_PAID", payment.TypeEpusdt, map[string]any{"txHash": strings.ToUpper(manualSettleTxHash)})
	_, err := f.client.PaymentTransactionClaim.Create().SetTxHash(strings.TrimPrefix(manualSettleTxHash, "0x")).SetOrderID(999).Save(ctx)
	require.NoError(t, err)
	err = f.svc.confirmPayment(ctx, f.order.ID, f.order.PaymentTradeNo, 30, payment.TypeEpusdt,
		map[string]string{"block_transaction_id": manualSettleTxHash})
	requireManualSettleReason(t, err, "TX_ALREADY_USED")
	require.Zero(t, f.userRepo.getByIDUser.Balance)
}

func TestPaymentOldExpiredCallbackLeavesHashForManualSettlement(t *testing.T) {
	ctx := context.Background()
	f := newManualSettleFixture(t, OrderStatusExpired, "4.48")
	_, err := f.client.PaymentOrder.UpdateOneID(f.order.ID).SetUpdatedAt(time.Now().Add(-time.Hour)).Save(ctx)
	require.NoError(t, err)
	err = f.svc.confirmPayment(ctx, f.order.ID, f.order.PaymentTradeNo, 30, payment.TypeEpusdt,
		map[string]string{"block_transaction_id": manualSettleTxHash})
	require.NoError(t, err)
	require.Zero(t, f.userRepo.getByIDUser.Balance)
	_, err = f.svc.AdminSettleOrderByTxHash(ctx, f.order.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
	require.NoError(t, err)
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance)
}
