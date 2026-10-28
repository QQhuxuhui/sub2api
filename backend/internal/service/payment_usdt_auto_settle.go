package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/paymentproviderinstance"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/chainverify"
)

// Automatic settlement of short USDT payments.
//
// The gateway only recognizes a transfer whose amount equals the quote, so a
// payer who withdraws 150 USDT from an exchange and receives 149.93 is never
// called back. For USDT-quoted orders the auto-settler watches the merchant's
// receiving addresses on chain and settles such a transfer itself when it can
// be attributed beyond doubt:
//
//   - the amount is within max(2 USDT, 2%) of exactly one site's order
//     (several open orders of the same user count as one: the newest wins);
//   - the transfer happened between order creation and two hours after expiry;
//   - the gateway has not matched the transaction to any order, and no other
//     gateway order on the shared address (other sites included) is waiting
//     for a similar amount.
//
// Anything else is left for an admin to settle by hash. The balance credited is
// what arrived, at the rate locked when the order was created.
const (
	auditActionAutoSettled = "ORDER_AUTO_SETTLED"
	auditActionAutoSkipped = "ORDER_AUTO_SETTLE_SKIPPED"
	claimSourceAuto        = "chain_auto"
	autoSettleOperator     = "system:auto-settle"

	autoSettleGraceAfterExpiry = 2 * time.Hour
	// Orders created further back than this cannot have a transfer in window.
	autoSettleOrderLookback = 6 * time.Hour
	// A transfer is kept for re-evaluation (exact amount still owned by the
	// gateway, RPC hiccups) at most this long.
	autoSettleTransferTTL = 3 * time.Hour
	// Public BSC nodes serve logs for about an hour; never start further back.
	autoSettleMaxCatchUpBlocks = 6000
	autoSettleScanChunk        = 2000
	// Gateway orders created this long before a transfer still compete for
	// it: the longest order lifetime plus the late-payment grace, with slack.
	autoSettleGatewayLookback = 3 * time.Hour
)

var (
	autoSettleToleranceFloor = decimal.NewFromInt(2)
	autoSettleToleranceRatio = decimal.RequireFromString("0.02")
)

// incomingScanner is the part of chainverify.Verifier the auto-settler uses.
type incomingScanner interface {
	transferFetcher
	HeadBlock(ctx context.Context, network string) (uint64, error)
	ScanIncoming(ctx context.Context, network, token string, addresses []string, fromBlock, toBlock uint64) ([]chainverify.IncomingTransfer, error)
}

// newIncomingScanner is swapped in tests.
var newIncomingScanner = func(overrides map[string][]string) incomingScanner {
	return chainverify.New(overrides)
}

type autoSettleTransfer struct {
	hash               string
	network            string
	token              string
	to                 string
	amount             decimal.Decimal
	blockTime          time.Time
	firstSeen          time.Time
	multipleRecipients bool
}

// usdtAutoSettler keeps, per instance and network, the scan cursor and the
// transfers awaiting a decision. Instances may share a receiving address, so
// one instance giving up on a transfer must not take it from another. State
// lives in memory: after a restart the scan resumes from recent blocks.
type usdtAutoSettler struct {
	mu     sync.Mutex
	queues map[string]*autoSettleQueue
	now    func() time.Time
}

type autoSettleQueue struct {
	cursor    uint64
	transfers map[string]*autoSettleTransfer
	skipped   map[string]struct{}
}

func newUSDTAutoSettler() *usdtAutoSettler {
	return &usdtAutoSettler{queues: map[string]*autoSettleQueue{}, now: time.Now}
}

func (a *usdtAutoSettler) queueFor(key string) *autoSettleQueue {
	q, ok := a.queues[key]
	if !ok {
		q = &autoSettleQueue{transfers: map[string]*autoSettleTransfer{}, skipped: map[string]struct{}{}}
		a.queues[key] = q
	}
	return q
}

// AutoSettleUSDTTransfers runs one scan-and-settle pass over every enabled
// USDT-quoted Epusdt instance with a gateway admin login. It returns how many
// orders were settled.
func (s *PaymentService) AutoSettleUSDTTransfers(ctx context.Context) (int, error) {
	a := s.usdtAutoSettler
	if a == nil {
		return 0, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	instances, err := s.entClient.PaymentProviderInstance.Query().Where(
		paymentproviderinstance.EnabledEQ(true),
		paymentproviderinstance.ProviderKeyEQ(payment.TypeEpusdt),
	).All(ctx)
	if err != nil {
		return 0, fmt.Errorf("query epusdt instances: %w", err)
	}
	settled := 0
	var firstErr error
	for _, inst := range instances {
		n, err := s.autoSettleInstance(ctx, a, inst)
		settled += n
		if err != nil {
			slog.Warn("[USDTAutoSettle] instance pass failed", "instance", inst.ID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	a.expire()
	return settled, firstErr
}

func (s *PaymentService) autoSettleInstance(ctx context.Context, a *usdtAutoSettler, inst *dbent.PaymentProviderInstance) (int, error) {
	cfg, err := s.loadBalancer.GetInstanceConfig(ctx, int64(inst.ID))
	if err != nil {
		return 0, err
	}
	if !usdtAutoSettleReady(inst, cfg) {
		return 0, nil
	}
	prov, err := s.createProviderFromInstance(ctx, inst)
	if err != nil {
		return 0, err
	}
	ledger := prov.(payment.GatewayLedgerProvider)
	settler := prov.(payment.OnChainSettlementProvider)
	network, token, addresses := ledger.AutoSettleScope()
	network = chainverify.NormalizeNetwork(network)
	instanceID := strconv.FormatInt(int64(inst.ID), 10)
	now := a.now()
	orders, err := s.entClient.PaymentOrder.Query().Where(
		paymentorder.ProviderInstanceIDEQ(instanceID),
		paymentorder.StatusIn(OrderStatusPending, OrderStatusExpired, OrderStatusCancelled),
		paymentorder.CreatedAtGTE(now.Add(-autoSettleOrderLookback)),
	).All(ctx)
	if err != nil {
		return 0, fmt.Errorf("query candidate orders: %w", err)
	}
	quoted := orders[:0]
	for _, o := range orders {
		if orderUSDTQuote(o) != nil && strings.TrimSpace(o.PaymentTradeNo) != "" {
			quoted = append(quoted, o)
		}
	}

	overrides, _ := chainverify.ParseEndpointOverrides(cfg["chainRpc"])
	scanner := newIncomingScanner(overrides)
	q := a.queueFor(instanceID + "/" + network)
	if err := a.scan(ctx, scanner, q, network, token, addresses, len(quoted) > 0); err != nil {
		return 0, err
	}
	if len(quoted) == 0 {
		return 0, nil
	}

	pass := &autoSettlePass{
		svc:       s,
		settler:   settler,
		ledger:    ledger,
		scanner:   scanner,
		orders:    quoted,
		targets:   map[int64]*payment.OnChainSettlementTarget{},
		gateway:   map[string][]payment.GatewayOrder{},
		now:       now,
		addresses: addresses,
	}
	settled := 0
	hashes := make([]string, 0, len(q.transfers))
	for h, t := range q.transfers {
		if containsAddress(network, addresses, t.to) {
			hashes = append(hashes, h)
		}
	}
	sort.Strings(hashes)
	for _, h := range hashes {
		t := q.transfers[h]
		decision := pass.evaluate(ctx, t)
		switch decision.kind {
		case autoSettleDone:
			settled++
			delete(q.transfers, h)
		case autoSettleDrop:
			delete(q.transfers, h)
		case autoSettleSkip:
			delete(q.transfers, h)
			q.skipped[h] = struct{}{}
			recordAutoSettleSkip(ctx, s, t, decision)
		case autoSettleRetry:
			if decision.reason != "" {
				slog.Info("[USDTAutoSettle] transfer deferred", "tx", h, "reason", decision.reason)
			}
		}
	}
	return settled, nil
}

// scan advances the cursor and queues new incoming transfers. With no order
// that could own a transfer it only fast-forwards the cursor.
func (a *usdtAutoSettler) scan(ctx context.Context, scanner incomingScanner, q *autoSettleQueue, network, token string, addresses []string, active bool) error {
	head, err := scanner.HeadBlock(ctx, network)
	if err != nil {
		return fmt.Errorf("head block: %w", err)
	}
	conf := chainverify.MinConfirmations(network)
	if head <= conf {
		return nil
	}
	safe := head - conf
	cursor := q.cursor
	if !active {
		q.cursor = safe
		return nil
	}
	from := cursor + 1
	if cursor == 0 || safe-cursor > autoSettleMaxCatchUpBlocks {
		if cursor != 0 {
			slog.Warn("[USDTAutoSettle] scan gap, older blocks skipped", "network", network, "cursor", cursor, "safe", safe)
		}
		from = 0
		if safe > autoSettleMaxCatchUpBlocks {
			from = safe - autoSettleMaxCatchUpBlocks
		}
	}
	for from <= safe {
		to := from + autoSettleScanChunk - 1
		if to > safe {
			to = safe
		}
		found, err := scanner.ScanIncoming(ctx, network, token, addresses, from, to)
		if err != nil {
			return fmt.Errorf("scan %d-%d: %w", from, to, err)
		}
		q.add(a.now(), network, addresses, found)
		q.cursor = to
		from = to + 1
	}
	return nil
}

func (q *autoSettleQueue) add(now time.Time, network string, addresses []string, found []chainverify.IncomingTransfer) {
	// Several transfers to our addresses in one transaction add up, the way
	// manual settlement counts them.
	batch := map[string]*autoSettleTransfer{}
	var order []string
	for _, tr := range found {
		hash, err := chainverify.NormalizeTxHash(tr.TxHash)
		if err != nil {
			continue
		}
		if _, done := q.skipped[hash]; done {
			continue
		}
		if _, queued := q.transfers[hash]; queued {
			continue
		}
		if t, ok := batch[hash]; ok {
			t.amount = t.amount.Add(tr.Amount)
			t.multipleRecipients = t.multipleRecipients || !chainverify.SameAddress(network, t.to, tr.To)
			continue
		}
		to := tr.To
		for _, addr := range addresses {
			if chainverify.SameAddress(network, addr, tr.To) {
				to = addr
				break
			}
		}
		batch[hash] = &autoSettleTransfer{
			hash:      hash,
			network:   network,
			token:     tr.Token,
			to:        to,
			amount:    tr.Amount,
			blockTime: tr.BlockTime,
			firstSeen: now,
		}
		order = append(order, hash)
	}
	for _, h := range order {
		q.transfers[h] = batch[h]
	}
}

func (a *usdtAutoSettler) expire() {
	now := a.now()
	for _, q := range a.queues {
		for h, t := range q.transfers {
			if now.Sub(t.firstSeen) > autoSettleTransferTTL {
				slog.Warn("[USDTAutoSettle] transfer left unsettled", "tx", h, "amount", t.amount.String())
				delete(q.transfers, h)
			}
		}
		if len(q.skipped) > 10000 {
			q.skipped = map[string]struct{}{}
		}
	}
}

// recordSkip writes one audit line per candidate order so the admin sees in
// the order history why a payment was not settled automatically.
func recordAutoSettleSkip(ctx context.Context, s *PaymentService, t *autoSettleTransfer, d autoSettleDecision) {
	slog.Warn("[USDTAutoSettle] transfer needs manual settlement", "tx", t.hash, "amount", t.amount.String(), "reason", d.reason, "orders", d.orderIDs)
	for _, id := range d.orderIDs {
		s.writeAuditLog(ctx, id, auditActionAutoSkipped, autoSettleOperator, map[string]any{
			"txHash":         t.hash,
			"network":        t.network,
			"receivedAmount": t.amount.String(),
			"blockTime":      t.blockTime.UTC().Format(time.RFC3339),
			"reason":         d.reason,
			"detail":         d.detail,
		})
	}
}

type autoSettleDecisionKind int

const (
	autoSettleRetry autoSettleDecisionKind = iota
	autoSettleDone
	autoSettleDrop
	autoSettleSkip
)

type autoSettleDecision struct {
	kind     autoSettleDecisionKind
	reason   string
	detail   string
	orderIDs []int64
}

func retryDecision(reason string) autoSettleDecision {
	return autoSettleDecision{kind: autoSettleRetry, reason: reason}
}

// autoSettlePass caches gateway reads for one instance pass.
type autoSettlePass struct {
	svc          *PaymentService
	settler      payment.OnChainSettlementProvider
	ledger       payment.GatewayLedgerProvider
	scanner      incomingScanner
	orders       []*dbent.PaymentOrder
	targets      map[int64]*payment.OnChainSettlementTarget
	gateway      map[string][]payment.GatewayOrder
	gatewaySince map[string]time.Time
	now          time.Time
	addresses    []string
}

type autoSettleCandidate struct {
	order    *dbent.PaymentOrder
	target   *payment.OnChainSettlementTarget
	expected decimal.Decimal
}

func autoSettleTolerance(expected decimal.Decimal) decimal.Decimal {
	return decimal.Max(expected.Mul(autoSettleToleranceRatio), autoSettleToleranceFloor)
}

func (p *autoSettlePass) evaluate(ctx context.Context, t *autoSettleTransfer) autoSettleDecision {
	if usedBy, err := p.svc.orderUsingTxHash(ctx, t.hash); err != nil {
		return retryDecision("claim lookup failed: " + err.Error())
	} else if usedBy != "" {
		return autoSettleDecision{kind: autoSettleDrop}
	}

	var candidates []autoSettleCandidate
	for _, o := range p.orders {
		if t.blockTime.Before(o.CreatedAt.Add(-manualSettleClockSkew)) || t.blockTime.After(o.ExpiresAt.Add(autoSettleGraceAfterExpiry)) {
			continue
		}
		target, err := p.target(ctx, o)
		if err != nil {
			return retryDecision(fmt.Sprintf("gateway quote of order %d unavailable: %v", o.ID, err))
		}
		if chainverify.NormalizeNetwork(target.Network) != t.network || !strings.EqualFold(target.Token, t.token) {
			continue
		}
		expected, err := decimal.NewFromString(strings.TrimSpace(target.ExpectedAmount))
		if err != nil || !expected.IsPositive() {
			continue
		}
		if t.amount.Sub(expected).Abs().GreaterThan(autoSettleTolerance(expected)) {
			continue
		}
		candidates = append(candidates, autoSettleCandidate{order: o, target: target, expected: expected})
	}
	if len(candidates) == 0 {
		return autoSettleDecision{kind: autoSettleDrop}
	}
	ids := make([]int64, 0, len(candidates))
	users := map[int64]struct{}{}
	for _, c := range candidates {
		ids = append(ids, c.order.ID)
		users[c.order.UserID] = struct{}{}
	}
	if t.multipleRecipients {
		return autoSettleDecision{kind: autoSettleSkip, reason: "MULTIPLE_RECEIVING_ADDRESSES", detail: "one transaction pays several receiving addresses; verify each transfer before settling", orderIDs: ids}
	}
	if len(users) > 1 {
		return autoSettleDecision{kind: autoSettleSkip, reason: "AMBIGUOUS_ORDERS", detail: "several users have open orders matching this amount", orderIDs: ids}
	}
	// One payer with several open orders: the newest one created before the
	// transfer is the one they were paying.
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].order.CreatedAt.After(candidates[j].order.CreatedAt) })
	chosen := candidates[0]
	for _, c := range candidates {
		if !c.order.CreatedAt.After(t.blockTime) {
			chosen = c
			break
		}
	}

	// The gateway still owns an exact payment to an open order; it will call back.
	if t.amount.Equal(chosen.expected) && chosen.order.Status == OrderStatusPending {
		return retryDecision("")
	}

	gatewayOrders, err := p.gatewayOrders(ctx, t)
	if err != nil {
		return retryDecision("gateway ledger unavailable: " + err.Error())
	}
	ours := map[string]struct{}{}
	for _, c := range candidates {
		ours[strings.TrimSpace(c.order.PaymentTradeNo)] = struct{}{}
		ours[strings.TrimSpace(c.order.OutTradeNo)] = struct{}{}
	}
	for _, g := range gatewayOrders {
		if sameTxHash(g.TxHash, t.hash) {
			// The gateway matched this transfer itself; its callback settles
			// whichever order it belongs to.
			return autoSettleDecision{kind: autoSettleDrop}
		}
	}
	for _, g := range gatewayOrders {
		switch g.Status {
		case payment.GatewayOrderWaiting, payment.GatewayOrderExpired:
		case payment.GatewayOrderPaid:
			// A paid order with a known hash was paid by another transfer. One
			// without a hash (a cashier sub-order's parent) may have been paid
			// by this very transfer, so it still competes.
			if strings.TrimSpace(g.TxHash) != "" {
				continue
			}
		default:
			continue
		}
		if _, mine := ours[g.TradeNo]; mine {
			continue
		}
		if _, mine := ours[g.ParentTradeNo]; mine {
			continue
		}
		if _, mine := ours[g.OrderID]; mine {
			continue
		}
		if g.CreatedAt.After(t.blockTime.Add(manualSettleClockSkew)) || !g.ActualAmount.IsPositive() {
			continue
		}
		if t.amount.Sub(g.ActualAmount).Abs().LessThanOrEqual(autoSettleTolerance(g.ActualAmount)) {
			return autoSettleDecision{
				kind:     autoSettleSkip,
				reason:   "GATEWAY_ORDER_CONFLICT",
				detail:   fmt.Sprintf("gateway order %s (%s %s, %s) could also own this transfer", g.TradeNo, g.ActualAmount.String(), g.Token, g.Status),
				orderIDs: []int64{chosen.order.ID},
			}
		}
	}

	return p.settle(ctx, t, chosen)
}

func (p *autoSettlePass) settle(ctx context.Context, t *autoSettleTransfer, c autoSettleCandidate) autoSettleDecision {
	transfers, err := p.scanner.Transfers(ctx, t.network, t.hash)
	if err != nil {
		if errors.Is(err, chainverify.ErrTxFailed) {
			return autoSettleDecision{kind: autoSettleDrop}
		}
		return retryDecision("verify transaction: " + err.Error())
	}
	matched, received := matchSettlementTransfers(transfers, t.network, c.target)
	if len(matched) == 0 {
		return autoSettleDecision{kind: autoSettleDrop}
	}
	for _, tr := range matched[1:] {
		if !chainverify.SameAddress(t.network, matched[0].To, tr.To) {
			return autoSettleDecision{kind: autoSettleSkip, reason: "MULTIPLE_RECEIVING_ADDRESSES", detail: "verified transaction pays several receiving addresses; verify each transfer before settling", orderIDs: []int64{c.order.ID}}
		}
	}
	if received.Sub(c.expected).Abs().GreaterThan(autoSettleTolerance(c.expected)) {
		return autoSettleDecision{kind: autoSettleSkip, reason: "AMOUNT_MISMATCH", detail: "verified amount " + received.String() + " is outside the tolerance", orderIDs: []int64{c.order.ID}}
	}
	if paidBy, err := hashlessPaidOrderNear(ctx, p.svc.entClient, matched[0].BlockTime, c.order.ID); err != nil {
		return retryDecision("hashless check: " + err.Error())
	} else if paidBy != 0 {
		return autoSettleDecision{kind: autoSettleSkip, reason: "HASHLESS_PAYMENT_NEARBY", detail: fmt.Sprintf("order %d was credited around this time without a transaction hash", paidBy), orderIDs: []int64{c.order.ID}}
	}
	// Re-read: the order may have been paid by the gateway since the pass began.
	fresh, err := p.svc.entClient.PaymentOrder.Get(ctx, c.order.ID)
	if err != nil {
		return retryDecision("reload order: " + err.Error())
	}
	if !manualSettleAllowedStatus(fresh.Status) {
		return autoSettleDecision{kind: autoSettleDrop}
	}
	outcome, err := p.svc.commitChainSettlement(ctx, fresh, chainSettlement{
		txHash:       t.hash,
		network:      t.network,
		transfers:    matched,
		received:     received,
		expected:     c.expected,
		quoteNetwork: c.target.Network,
		tradeNo:      strings.TrimSpace(fresh.PaymentTradeNo),
		operator:     autoSettleOperator,
		claimSource:  claimSourceAuto,
		auditAction:  auditActionAutoSettled,
	})
	if err != nil {
		return autoSettleDecision{kind: autoSettleSkip, reason: "SETTLE_FAILED", detail: err.Error(), orderIDs: []int64{c.order.ID}}
	}
	slog.Info("[USDTAutoSettle] order settled", "orderID", fresh.ID, "tx", t.hash, "received", received.String(), "expected", c.expected.String(), "credited", outcome.creditedAmount)
	if outcome.fulfillErr != nil {
		slog.Error("[USDTAutoSettle] fulfillment failed after settlement", "orderID", fresh.ID, "error", outcome.fulfillErr)
	}
	return autoSettleDecision{kind: autoSettleDone}
}

func (p *autoSettlePass) target(ctx context.Context, o *dbent.PaymentOrder) (*payment.OnChainSettlementTarget, error) {
	if t, ok := p.targets[o.ID]; ok {
		return t, nil
	}
	t, err := p.settler.OnChainSettlementTarget(ctx, strings.TrimSpace(o.PaymentTradeNo))
	if err != nil {
		return nil, err
	}
	p.targets[o.ID] = t
	return t, nil
}

func (p *autoSettlePass) gatewayOrders(ctx context.Context, t *autoSettleTransfer) ([]payment.GatewayOrder, error) {
	// Discovery time and block time can be far apart. Cache only the interval
	// actually queried and extend it whenever an older transfer needs it.
	key := strings.ToLower(t.network + "/" + t.token + "/" + t.to)
	from := t.blockTime.Add(-autoSettleGatewayLookback)
	orders, ok := p.gateway[key]
	since, covered := p.gatewaySince[key]
	if !ok || !covered || from.Before(since) {
		var err error
		orders, err = p.ledger.GatewayOrdersSince(ctx, t.network, t.token, t.to, from)
		if err != nil {
			return nil, err
		}
		if p.gateway == nil {
			p.gateway = make(map[string][]payment.GatewayOrder)
		}
		if p.gatewaySince == nil {
			p.gatewaySince = make(map[string]time.Time)
		}
		p.gateway[key] = orders
		p.gatewaySince[key] = from
	}
	out := make([]payment.GatewayOrder, 0, len(orders))
	for _, g := range orders {
		if !g.CreatedAt.Before(from) {
			out = append(out, g)
		}
	}
	return out, nil
}

// usdtAutoSettleReady reports whether the auto-settler will watch this
// instance, with the same checks autoSettleInstance applies.
func usdtAutoSettleReady(inst *dbent.PaymentProviderInstance, cfg map[string]string) bool {
	if inst == nil || !isUSDTQuotedConfig(inst.ProviderKey, cfg) {
		return false
	}
	prov, err := createPaymentProviderFromInstance(inst.ProviderKey, strconv.FormatInt(int64(inst.ID), 10), cfg)
	if err != nil {
		return false
	}
	ledger, ok := prov.(payment.GatewayLedgerProvider)
	if !ok || !ledger.AutoSettleConfigured() {
		return false
	}
	if _, ok := prov.(payment.OnChainSettlementProvider); !ok {
		return false
	}
	network, token, addresses := ledger.AutoSettleScope()
	return chainverify.ScanSupported(network) && token != "" && len(addresses) > 0
}

func containsAddress(network string, addresses []string, addr string) bool {
	for _, a := range addresses {
		if chainverify.SameAddress(network, a, addr) {
			return true
		}
	}
	return false
}

func sameTxHash(a, b string) bool {
	na, errA := chainverify.NormalizeTxHash(a)
	nb, errB := chainverify.NormalizeTxHash(b)
	return errA == nil && errB == nil && na == nb
}
