//go:build unit

package service

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/chainverify"
)

const (
	autoSettleTxHash = "0xdaf592caf35c9cdd0c71d020b709f841aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	autoSettleRate   = "7.1"
)

type autoSettleProvider struct {
	paymentOrderLifecycleQueryProvider
	configured bool
	targets    map[string]*payment.OnChainSettlementTarget
	gateway    []payment.GatewayOrder
	gatewayErr error
}

func (p *autoSettleProvider) OnChainSettlementTarget(_ context.Context, tradeNo string) (*payment.OnChainSettlementTarget, error) {
	if t, ok := p.targets[tradeNo]; ok {
		return t, nil
	}
	return nil, fmt.Errorf("unknown trade %s", tradeNo)
}

func (p *autoSettleProvider) AutoSettleConfigured() bool { return p.configured }

func (p *autoSettleProvider) AutoSettleScope() (string, string, []string) {
	return "binance", "USDT", []string{manualSettleAddress}
}

func (p *autoSettleProvider) GatewayOrdersSince(_ context.Context, _, _, _ string, since time.Time) ([]payment.GatewayOrder, error) {
	var out []payment.GatewayOrder
	for _, g := range p.gateway {
		if !g.CreatedAt.Before(since) {
			out = append(out, g)
		}
	}
	return out, p.gatewayErr
}

type autoSettleScanner struct {
	overrides map[string][]string
	head      uint64
	incoming  []chainverify.IncomingTransfer
	transfers map[string][]chainverify.Transfer
	scans     int
}

func (s *autoSettleScanner) HeadBlock(context.Context, string) (uint64, error) { return s.head, nil }

func (s *autoSettleScanner) ScanIncoming(_ context.Context, _, _ string, _ []string, from, to uint64) ([]chainverify.IncomingTransfer, error) {
	s.scans++
	var out []chainverify.IncomingTransfer
	for _, tr := range s.incoming {
		if tr.BlockNumber >= from && tr.BlockNumber <= to {
			out = append(out, tr)
		}
	}
	return out, nil
}

func (s *autoSettleScanner) Transfers(_ context.Context, _ string, hash string) ([]chainverify.Transfer, error) {
	hash, _ = chainverify.NormalizeTxHash(hash)
	if trs, ok := s.transfers[hash]; ok {
		return trs, nil
	}
	return nil, chainverify.ErrTxNotFound
}

type usdtConfigLoadBalancer struct {
	captureLoadBalancer
	cfg map[string]string
}

// autoSettleRedeemRepo lets fulfillment create the order's redeem code.
type autoSettleRedeemRepo struct {
	paymentOrderLifecycleRedeemRepo
}

func (r *autoSettleRedeemRepo) Create(_ context.Context, code *RedeemCode) error {
	code.ID = int64(len(r.codesByCode) + 1)
	cloned := *code
	r.codesByCode[code.Code] = &cloned
	return nil
}

func (lb *usdtConfigLoadBalancer) GetInstanceConfig(context.Context, int64) (map[string]string, error) {
	cfg := map[string]string{"currency": "USD"}
	for k, v := range lb.cfg {
		cfg[k] = v
	}
	return cfg, nil
}

type autoSettleFixture struct {
	t        *testing.T
	svc      *PaymentService
	client   *dbent.Client
	provider *autoSettleProvider
	scanner  *autoSettleScanner
	userRepo *mockUserRepo
	redeem   *autoSettleRedeemRepo
	instance string
	orderIn  string
	now      time.Time
}

func newAutoSettleFixture(t *testing.T) *autoSettleFixture {
	t.Helper()
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)
	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeEpusdt).SetName("usdt").SetConfig("{}").
		SetSupportedTypes("usdt").SetEnabled(true).Save(ctx)
	require.NoError(t, err)

	userRepo := &mockUserRepo{getByIDUser: &User{ID: 1}}
	userRepo.updateBalanceFn = func(_ context.Context, _ int64, amount float64) error {
		userRepo.getByIDUser.Balance += amount
		return nil
	}
	redeem := &autoSettleRedeemRepo{paymentOrderLifecycleRedeemRepo{codesByCode: map[string]*RedeemCode{}}}
	provider := &autoSettleProvider{
		paymentOrderLifecycleQueryProvider: paymentOrderLifecycleQueryProvider{key: payment.TypeEpusdt},
		configured:                         true,
		targets:                            map[string]*payment.OnChainSettlementTarget{},
	}
	scanner := &autoSettleScanner{head: 1000, transfers: map[string][]chainverify.Transfer{}}

	prevFactory := createPaymentProviderFromInstance
	createPaymentProviderFromInstance = func(string, string, map[string]string) (payment.Provider, error) { return provider, nil }
	prevScanner := newIncomingScanner
	newIncomingScanner = func(overrides map[string][]string) incomingScanner {
		scanner.overrides = overrides
		return scanner
	}
	t.Cleanup(func() {
		createPaymentProviderFromInstance = prevFactory
		newIncomingScanner = prevScanner
	})

	now := time.Now()
	settler := newUSDTAutoSettler()
	settler.now = func() time.Time { return now }
	svc := &PaymentService{
		entClient:       client,
		registry:        payment.NewRegistry(),
		loadBalancer:    &usdtConfigLoadBalancer{},
		redeemService:   NewRedeemService(redeem, userRepo, nil, nil, nil, client, nil, nil),
		userRepo:        userRepo,
		providersLoaded: true,
		usdtAutoSettler: settler,
	}
	return &autoSettleFixture{
		t: t, svc: svc, client: client, provider: provider, scanner: scanner,
		userRepo: userRepo, redeem: redeem, instance: strconv.FormatInt(inst.ID, 10), now: now,
	}
}

func (f *autoSettleFixture) orderInstance() string {
	if f.orderIn != "" {
		return f.orderIn
	}
	return f.instance
}

// order creates a USDT-quoted order of usdt quoted by the gateway as expected.
func (f *autoSettleFixture) order(userEmail, status, trade string, usdt, expected string, created time.Time) *dbent.PaymentOrder {
	f.t.Helper()
	ctx := context.Background()
	user, err := f.client.User.Query().Where().All(ctx)
	require.NoError(f.t, err)
	var uid int64
	for _, u := range user {
		if u.Email == userEmail {
			uid = u.ID
		}
	}
	if uid == 0 {
		u, err := f.client.User.Create().SetEmail(userEmail).SetPasswordHash("hash").SetUsername(userEmail).Save(ctx)
		require.NoError(f.t, err)
		uid = u.ID
	}
	q := &usdtQuote{rate: decimal.RequireFromString(autoSettleRate), multiplier: 1}
	amount := decimal.RequireFromString(usdt)
	o, err := f.client.PaymentOrder.Create().
		SetUserID(uid).SetUserEmail(userEmail).SetUserName(userEmail).
		SetAmount(q.credit(amount)).SetPayAmount(amount.InexactFloat64()).SetFeeRate(0).
		SetRechargeCode("CODE-" + trade).SetOutTradeNo("sub2_" + trade).
		SetPaymentType(payment.TypeUSDT).SetPaymentTradeNo(trade).
		SetOrderType(payment.OrderTypeBalance).SetStatus(status).
		SetCreatedAt(created).SetExpiresAt(created.Add(10 * time.Minute)).
		SetClientIP("127.0.0.1").SetSrcHost("api.example.com").
		SetProviderInstanceID(f.orderInstance()).SetProviderKey(payment.TypeEpusdt).
		SetProviderSnapshot(q.applyToSnapshot(map[string]any{"schema_version": 2, "provider_key": payment.TypeEpusdt, "currency": "USD"})).
		Save(ctx)
	require.NoError(f.t, err)
	f.provider.targets[trade] = &payment.OnChainSettlementTarget{
		Network: "binance", Token: "USDT", ReceiveAddress: manualSettleAddress, ExpectedAmount: expected,
	}
	return o
}

func (f *autoSettleFixture) transfer(hash, amount string, at time.Time, block uint64) {
	amt := decimal.RequireFromString(amount)
	tr := chainverify.Transfer{
		Network: "binance", TxHash: hash, Token: "USDT", From: "0xe2fc31f816a9b94326492132018c3aecc4a93ae1",
		To: manualSettleAddress, Amount: amt, BlockTime: at, Confirmations: 50,
	}
	f.scanner.incoming = append(f.scanner.incoming, chainverify.IncomingTransfer{Transfer: tr, BlockNumber: block})
	normalized, _ := chainverify.NormalizeTxHash(hash)
	f.scanner.transfers[normalized] = []chainverify.Transfer{tr}
}

func (f *autoSettleFixture) queue() *autoSettleQueue {
	return f.svc.usdtAutoSettler.queueFor(f.instance + "/binance")
}

func (f *autoSettleFixture) run() int {
	f.t.Helper()
	n, err := f.svc.AutoSettleUSDTTransfers(context.Background())
	require.NoError(f.t, err)
	return n
}

func (f *autoSettleFixture) reload(id int64) *dbent.PaymentOrder {
	o, err := f.client.PaymentOrder.Get(context.Background(), id)
	require.NoError(f.t, err)
	return o
}

func (f *autoSettleFixture) audits(id int64, action string) []*dbent.PaymentAuditLog {
	logs, err := f.client.PaymentAuditLog.Query().Where(
		paymentauditlog.OrderIDEQ(strconv.FormatInt(id, 10)), paymentauditlog.ActionEQ(action),
	).All(context.Background())
	require.NoError(f.t, err)
	return logs
}

// The 2026-10-27 case: 150.15 quoted, 150 withdrawn from an exchange, 149.93
// arrived after the order expired.
func TestAutoSettleCreditsShortPaymentAtLockedRate(t *testing.T) {
	f := newAutoSettleFixture(t)
	o := f.order("a@example.com", OrderStatusExpired, "trade-a", "150", "150.15", f.now.Add(-30*time.Minute))
	f.transfer(autoSettleTxHash, "149.93", f.now.Add(-25*time.Minute), 900)

	require.Equal(t, 1, f.run())

	got := f.reload(o.ID)
	require.Equal(t, OrderStatusCompleted, got.Status)
	require.Equal(t, 1064.5, got.Amount, "149.93 × 7.1")
	require.Equal(t, 149.93, got.PayAmount)
	require.Equal(t, "auto", PaymentOrderSettledBy(got))
	require.Equal(t, 1064.5, f.userRepo.getByIDUser.Balance)
	logs := f.audits(o.ID, auditActionAutoSettled)
	require.Len(t, logs, 1)
	require.Equal(t, autoSettleOperator, logs[0].Operator)
	require.Contains(t, logs[0].Detail, `"receivedAmount":"149.93"`)
	require.Contains(t, logs[0].Detail, `"usdtCnyRate":"7.1"`)

	// The same transfer is never settled twice.
	f.queue().cursor = 0
	require.Equal(t, 0, f.run())
}

func TestAutoSettleLeavesExactPaymentToGatewayWhilePending(t *testing.T) {
	f := newAutoSettleFixture(t)
	o := f.order("a@example.com", OrderStatusPending, "trade-a", "150", "150", f.now.Add(-2*time.Minute))
	f.transfer(autoSettleTxHash, "150", f.now.Add(-time.Minute), 900)

	require.Equal(t, 0, f.run())
	require.Equal(t, OrderStatusPending, f.reload(o.ID).Status)
	require.Contains(t, f.queue().transfers, autoSettleTxHash[2:], "kept for the next pass")

	// The gateway let the order expire without a callback: settle it now.
	_, err := f.client.PaymentOrder.UpdateOneID(o.ID).SetStatus(OrderStatusExpired).Save(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, f.run())
	require.Equal(t, OrderStatusCompleted, f.reload(o.ID).Status)
}

func TestAutoSettleSkipsWhenAnotherSitesGatewayOrderCouldOwnTransfer(t *testing.T) {
	f := newAutoSettleFixture(t)
	o := f.order("a@example.com", OrderStatusExpired, "trade-a", "150", "150.01", f.now.Add(-30*time.Minute))
	f.transfer(autoSettleTxHash, "149.93", f.now.Add(-25*time.Minute), 900)
	f.provider.gateway = []payment.GatewayOrder{
		{TradeNo: "trade-a", OrderID: "sub2_trade-a", ActualAmount: decimal.RequireFromString("150.01"), Status: payment.GatewayOrderExpired, CreatedAt: f.now.Add(-30 * time.Minute)},
		{TradeNo: "other-site", OrderID: "tomapi-1", ActualAmount: decimal.RequireFromString("150"), Status: payment.GatewayOrderExpired, Token: "USDT", CreatedAt: f.now.Add(-40 * time.Minute)},
	}

	require.Equal(t, 0, f.run())
	require.Equal(t, OrderStatusExpired, f.reload(o.ID).Status)
	skips := f.audits(o.ID, auditActionAutoSkipped)
	require.Len(t, skips, 1)
	require.Contains(t, skips[0].Detail, "GATEWAY_ORDER_CONFLICT")
	require.Contains(t, skips[0].Detail, "other-site")

	// Reported once, not on every pass.
	f.queue().cursor = 0
	require.Equal(t, 0, f.run())
	require.Len(t, f.audits(o.ID, auditActionAutoSkipped), 1)
}

func TestAutoSettleDropsTransferTheGatewayAlreadyMatched(t *testing.T) {
	f := newAutoSettleFixture(t)
	o := f.order("a@example.com", OrderStatusExpired, "trade-a", "150", "150.01", f.now.Add(-30*time.Minute))
	f.transfer(autoSettleTxHash, "149.93", f.now.Add(-25*time.Minute), 900)
	f.provider.gateway = []payment.GatewayOrder{
		{TradeNo: "other-site", ActualAmount: decimal.RequireFromString("149.93"), Status: payment.GatewayOrderPaid, TxHash: autoSettleTxHash[2:], CreatedAt: f.now.Add(-26 * time.Minute)},
	}

	require.Equal(t, 0, f.run())
	require.Equal(t, OrderStatusExpired, f.reload(o.ID).Status)
	require.Empty(t, f.audits(o.ID, auditActionAutoSkipped))
}

func TestAutoSettleSkipsWhenSeveralUsersMatch(t *testing.T) {
	f := newAutoSettleFixture(t)
	a := f.order("a@example.com", OrderStatusPending, "trade-a", "150", "150", f.now.Add(-5*time.Minute))
	b := f.order("b@example.com", OrderStatusPending, "trade-b", "150", "150.01", f.now.Add(-4*time.Minute))
	f.transfer(autoSettleTxHash, "149.5", f.now.Add(-time.Minute), 900)

	require.Equal(t, 0, f.run())
	require.Equal(t, OrderStatusPending, f.reload(a.ID).Status)
	require.Equal(t, OrderStatusPending, f.reload(b.ID).Status)
	require.Len(t, f.audits(a.ID, auditActionAutoSkipped), 1)
	require.Len(t, f.audits(b.ID, auditActionAutoSkipped), 1)
}

func TestAutoSettlePicksNewestOrderOfTheSamePayer(t *testing.T) {
	f := newAutoSettleFixture(t)
	older := f.order("a@example.com", OrderStatusCancelled, "trade-old", "100", "100", f.now.Add(-20*time.Minute))
	newer := f.order("a@example.com", OrderStatusPending, "trade-new", "100", "100.01", f.now.Add(-10*time.Minute))
	f.transfer(autoSettleTxHash, "99.2", f.now.Add(-5*time.Minute), 900)

	require.Equal(t, 1, f.run())
	require.Equal(t, OrderStatusCancelled, f.reload(older.ID).Status)
	require.Equal(t, OrderStatusCompleted, f.reload(newer.ID).Status)
	require.Equal(t, 704.32, f.reload(newer.ID).Amount)
}

func TestAutoSettleIgnoresTransfersOutsideToleranceOrWindow(t *testing.T) {
	f := newAutoSettleFixture(t)
	o := f.order("a@example.com", OrderStatusExpired, "trade-a", "150", "150", f.now.Add(-4*time.Hour+time.Minute))
	f.transfer(autoSettleTxHash, "146.9", f.now.Add(-4*time.Hour+3*time.Minute), 900)
	f.transfer("0x"+"b1"+autoSettleTxHash[4:], "149.9", o.CreatedAt.Add(-10*time.Minute), 901)
	f.transfer("0x"+"c1"+autoSettleTxHash[4:], "149.9", o.ExpiresAt.Add(3*time.Hour), 902)

	require.Equal(t, 0, f.run())
	require.Equal(t, OrderStatusExpired, f.reload(o.ID).Status)
	require.Empty(t, f.audits(o.ID, auditActionAutoSkipped))
}

func TestAutoSettleNeedsGatewayAdminLogin(t *testing.T) {
	f := newAutoSettleFixture(t)
	f.provider.configured = false
	o := f.order("a@example.com", OrderStatusExpired, "trade-a", "150", "150.01", f.now.Add(-30*time.Minute))
	f.transfer(autoSettleTxHash, "149.93", f.now.Add(-25*time.Minute), 900)

	require.Equal(t, 0, f.run())
	require.Zero(t, f.scanner.scans)
	require.Equal(t, OrderStatusExpired, f.reload(o.ID).Status)
}

func TestAutoSettleOnlyFastForwardsCursorWithoutOpenOrders(t *testing.T) {
	f := newAutoSettleFixture(t)
	f.transfer(autoSettleTxHash, "149.93", f.now.Add(-25*time.Minute), 900)

	require.Equal(t, 0, f.run())
	require.Zero(t, f.scanner.scans)
	require.Equal(t, uint64(1000-chainverify.MinConfirmations("binance")), f.queue().cursor)
}

func TestPrepareUSDTQuote(t *testing.T) {
	prev := usdtCNYRate
	t.Cleanup(func() { usdtCNYRate = prev })
	usdtCNYRate = func(context.Context) (decimal.Decimal, error) { return decimal.RequireFromString("7.1"), nil }
	cfg := &PaymentConfig{BalanceRechargeMultiplier: 1}
	ctx := context.Background()

	q, err := prepareUSDTQuote(ctx, CreateOrderRequest{Amount: 150, OrderType: payment.OrderTypeBalance}, cfg)
	require.NoError(t, err)
	require.Equal(t, 1065.0, q.credit(decimal.NewFromInt(150)))

	_, err = prepareUSDTQuote(ctx, CreateOrderRequest{Amount: 150.5, OrderType: payment.OrderTypeBalance}, cfg)
	requireManualSettleReason(t, err, "INVALID_AMOUNT")
	_, err = prepareUSDTQuote(ctx, CreateOrderRequest{Amount: 9, OrderType: payment.OrderTypeBalance}, cfg)
	requireManualSettleReason(t, err, "INVALID_AMOUNT")
	_, err = prepareUSDTQuote(ctx, CreateOrderRequest{Amount: 150, OrderType: payment.OrderTypeSubscription}, cfg)
	requireManualSettleReason(t, err, "USDT_QUOTE_BALANCE_ONLY")

	usdtCNYRate = func(context.Context) (decimal.Decimal, error) { return decimal.Zero, fmt.Errorf("down") }
	_, err = prepareUSDTQuote(ctx, CreateOrderRequest{Amount: 150, OrderType: payment.OrderTypeBalance}, cfg)
	requireManualSettleReason(t, err, "USDT_RATE_UNAVAILABLE")
}

// Review 2026-10-27 #1: a hashless gateway confirmation must see automatic
// settlements as well as manual ones, or the same transfer is credited twice.
func TestAutoSettleClaimBlocksHashlessConfirmationOfOverlappingOrder(t *testing.T) {
	f := newAutoSettleFixture(t)
	settled := f.order("a@example.com", OrderStatusExpired, "trade-a", "150", "150.01", f.now.Add(-30*time.Minute))
	f.transfer(autoSettleTxHash, "149.93", f.now.Add(-25*time.Minute), 900)
	require.Equal(t, 1, f.run())

	other := f.order("b@example.com", OrderStatusPending, "trade-b", "150", "149.93", f.now.Add(-28*time.Minute))
	other, err := f.client.PaymentOrder.UpdateOneID(other.ID).SetExpiresAt(f.now.Add(time.Minute)).Save(context.Background())
	require.NoError(t, err)
	paidBy, err := manualPaymentForHashlessOrder(context.Background(), f.client, other)
	require.NoError(t, err)
	require.Equal(t, settled.ID, paidBy)
}

// Review #2: with a recharge fee the quote is above the credited share; a
// short payment must not be credited more than paying the quote in full.
func TestAutoSettleCreditFollowsQuoteIncludingFee(t *testing.T) {
	f := newAutoSettleFixture(t)
	o := f.order("a@example.com", OrderStatusExpired, "trade-a", "100", "105", f.now.Add(-30*time.Minute))
	// 100 USDT credited at 7.1 = 710, quoted 105 USDT with a 5% fee.
	o, err := f.client.PaymentOrder.UpdateOneID(o.ID).SetAmount(710).SetPayAmount(105).Save(context.Background())
	require.NoError(t, err)
	f.transfer(autoSettleTxHash, "104.9", f.now.Add(-25*time.Minute), 900)

	require.Equal(t, 1, f.run())
	got := f.reload(o.ID)
	require.Equal(t, 709.32, got.Amount, "710 × 104.9 / 105")
	require.LessOrEqual(t, got.Amount, 710.0)
}

// Review #3: the gateway read is shared by every transfer of a pass; an older
// transfer evaluated after a newer one still needs its own, older window.
func TestAutoSettleGatewayWindowFollowsEachTransfer(t *testing.T) {
	f := newAutoSettleFixture(t)
	f.order("x@example.com", OrderStatusPending, "trade-x", "50", "50.01", f.now.Add(-15*time.Minute))
	old := f.order("y@example.com", OrderStatusExpired, "trade-y", "150", "150.01", f.now.Add(-3*time.Hour))
	f.transfer("0x0a"+autoSettleTxHash[4:], "49.9", f.now.Add(-10*time.Minute), 900)
	f.transfer("0x0b"+autoSettleTxHash[4:], "149.93", f.now.Add(-175*time.Minute), 901)
	f.provider.gateway = []payment.GatewayOrder{
		{TradeNo: "other-site", OrderID: "tomapi-1", ActualAmount: decimal.RequireFromString("150"), Status: payment.GatewayOrderExpired, Token: "USDT", CreatedAt: f.now.Add(-5 * time.Hour)},
	}

	require.Equal(t, 1, f.run(), "only the 50 USDT order settles")
	require.Equal(t, OrderStatusExpired, f.reload(old.ID).Status)
	skips := f.audits(old.ID, auditActionAutoSkipped)
	require.Len(t, skips, 1)
	require.Contains(t, skips[0].Detail, "other-site")
}

// Review #4: instances sharing a receiving address keep separate queues.
func TestAutoSettleInstancesSharingAddressDoNotDropEachOthersTransfers(t *testing.T) {
	f := newAutoSettleFixture(t)
	f.order("a@example.com", OrderStatusPending, "trade-a", "20", "20", f.now.Add(-30*time.Minute))
	second, err := f.client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeEpusdt).SetName("usdt-2").SetConfig("{}").
		SetSupportedTypes("usdt").SetEnabled(true).Save(context.Background())
	require.NoError(t, err)
	f.orderIn = strconv.FormatInt(second.ID, 10)
	o := f.order("b@example.com", OrderStatusExpired, "trade-b", "150", "150.01", f.now.Add(-30*time.Minute))
	f.transfer(autoSettleTxHash, "149.93", f.now.Add(-25*time.Minute), 900)

	require.Equal(t, 1, f.run())
	require.Equal(t, OrderStatusCompleted, f.reload(o.ID).Status)
}

// Review #5: the instance's chainRpc overrides reach the scanner.
func TestAutoSettleUsesInstanceChainRPC(t *testing.T) {
	f := newAutoSettleFixture(t)
	f.svc.loadBalancer = &usdtConfigLoadBalancer{cfg: map[string]string{"chainRpc": "binance=https://bsc.example"}}
	f.order("a@example.com", OrderStatusPending, "trade-a", "150", "150", f.now.Add(-time.Minute))

	f.run()
	require.Equal(t, []string{"https://bsc.example"}, f.scanner.overrides["binance"])
}

// Review #6: the payment page promises automatic settlement only when the
// auto-settler would actually watch the instance.
func TestUSDTAutoSettleReadyMatchesWhatTheSettlerWatches(t *testing.T) {
	inst := &dbent.PaymentProviderInstance{ID: 1, ProviderKey: payment.TypeEpusdt}
	base := func() map[string]string {
		return map[string]string{
			"pid": "1001", "secretKey": "s", "apiBase": "http://epusdt:8000", "notifyUrl": "https://site/notify",
			"currency": "USD", "token": "usdt", "network": "binance",
			"receiveAddresses": "0x4c1349a30c3a91d2cd69329c48dfb02c10d812c7",
			"adminUsername":    "reader", "adminPassword": "pw",
		}
	}
	require.True(t, usdtAutoSettleReady(inst, base()))

	cfg := base()
	delete(cfg, "adminUsername")
	delete(cfg, "adminPassword")
	require.False(t, usdtAutoSettleReady(inst, cfg), "no gateway admin login")

	cfg = base()
	cfg["receiveAddresses"] = ""
	require.False(t, usdtAutoSettleReady(inst, cfg), "no receiving address")

	cfg = base()
	cfg["network"] = "tron"
	cfg["receiveAddresses"] = "TP525BEN7X9N1pfkVdc9Pv1W43M2LzU6pe"
	require.False(t, usdtAutoSettleReady(inst, cfg), "TRON is not scanned yet")

	cfg = base()
	cfg["currency"] = "CNY"
	require.False(t, usdtAutoSettleReady(inst, cfg), "not USDT-quoted")
}

// Review E-002: a paid gateway order without a transaction hash (the parent of
// a cashier sub-order) may have been paid by this transfer, so it competes.
func TestAutoSettleSkipsWhenPaidGatewayOrderHasNoHash(t *testing.T) {
	f := newAutoSettleFixture(t)
	o := f.order("a@example.com", OrderStatusPending, "trade-a", "100", "100", f.now.Add(-5*time.Minute))
	f.transfer(autoSettleTxHash, "99.9", f.now.Add(-time.Minute), 900)
	f.provider.gateway = []payment.GatewayOrder{
		{TradeNo: "foreign-paid", Status: payment.GatewayOrderPaid, ActualAmount: decimal.RequireFromString("99.9"), CreatedAt: f.now.Add(-4 * time.Minute)},
		{TradeNo: "foreign-hashed", Status: payment.GatewayOrderPaid, TxHash: "0xbeef", ActualAmount: decimal.RequireFromString("99.9"), CreatedAt: f.now.Add(-4 * time.Minute)},
	}

	require.Equal(t, 0, f.run())
	require.Equal(t, OrderStatusPending, f.reload(o.ID).Status)
	skips := f.audits(o.ID, auditActionAutoSkipped)
	require.Len(t, skips, 1)
	require.Contains(t, skips[0].Detail, "foreign-paid")
}

// Review E-003: a gateway confirmation of a quote shifted to tell orders apart
// credits what arrived, the same way chain settlement does.
func TestGatewayConfirmationCreditsActualUSDTOfQuotedOrder(t *testing.T) {
	f := newAutoSettleFixture(t)
	o := f.order("a@example.com", OrderStatusPending, "trade-a", "150", "150.15", f.now.Add(-5*time.Minute))
	err := f.svc.confirmPayment(context.Background(), o.ID, o.PaymentTradeNo, 150, payment.TypeEpusdt,
		map[string]string{"block_transaction_id": autoSettleTxHash, "actual_amount": "150.15"})
	require.NoError(t, err)
	got := f.reload(o.ID)
	require.Equal(t, 1066.07, got.Amount, "1065 × 150.15 / 150")
	require.Equal(t, 150.15, got.PayAmount)

	// A reported amount far from the quote is not trusted.
	o2 := f.order("b@example.com", OrderStatusPending, "trade-b", "150", "150", f.now.Add(-5*time.Minute))
	err = f.svc.confirmPayment(context.Background(), o2.ID, o2.PaymentTradeNo, 150, payment.TypeEpusdt,
		map[string]string{"block_transaction_id": "0x" + "c1" + autoSettleTxHash[4:], "actual_amount": "300"})
	require.NoError(t, err)
	require.Equal(t, 1065.0, f.reload(o2.ID).Amount)
}
