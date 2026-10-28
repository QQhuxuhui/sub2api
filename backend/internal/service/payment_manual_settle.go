package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/paymenttransactionclaim"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/chainverify"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	auditActionManualSettled = "ORDER_MANUAL_SETTLED"

	// The transfer must belong to this order's lifetime. Chain data is public
	// and receiving addresses are shared by every order (and by every site
	// using the same gateway wallet), so a loose window would let someone open
	// an order and claim a stranger's transfer of a similar amount. Exchanges
	// may take a while to broadcast, hence the day; the skew only covers clocks.
	manualSettleClockSkew = 2 * time.Minute
	manualSettleMaxAge    = 24 * time.Hour
)

var (
	// A short payment is accepted up to max(20% of the quote, 1.5 tokens) —
	// enough for a TRC20 exchange withdrawal fee on a small order — but never
	// more than half of the quote. Anything beyond that is not "a fee got
	// deducted" any more and must be handled by adjusting the balance by hand.
	// The same band applies upwards: a transfer far above the quote is most
	// likely somebody else's payment to the shared address, not this order's.
	manualSettleShortfallRatio = decimal.RequireFromString("0.2")
	manualSettleShortfallFloor = decimal.RequireFromString("1.5")
	manualSettleShortfallCap   = decimal.RequireFromString("0.5")
)

// ManualSettleRequest is an admin request to settle an order by tx hash.
type ManualSettleRequest struct {
	TxHash string
	// Network is optional; when empty the gateway's network is probed first,
	// then the other networks of the same address family.
	Network string
	// DryRun only verifies and reports, without touching the order.
	DryRun   bool
	Operator string
}

// ManualSettleResult is what the chain says about the transaction, next to
// what the gateway expected.
type ManualSettleResult struct {
	OrderID          int64     `json:"order_id"`
	OrderStatus      string    `json:"order_status"`
	Settled          bool      `json:"settled"`
	TxHash           string    `json:"tx_hash"`
	Network          string    `json:"network"`
	Token            string    `json:"token"`
	From             string    `json:"from"`
	To               string    `json:"to"`
	ReceivedAmount   string    `json:"received_amount"`
	ExpectedAmount   string    `json:"expected_amount"`
	ExpectedNetwork  string    `json:"expected_network"`
	ExpectedToken    string    `json:"expected_token"`
	Shortfall        string    `json:"shortfall"`
	AllowedShortfall string    `json:"allowed_shortfall"`
	BlockTime        time.Time `json:"block_time"`
	Confirmations    int64     `json:"confirmations"`
}

// transferFetcher is the part of chainverify.Verifier the service needs.
type transferFetcher interface {
	Transfers(ctx context.Context, network, txHash string) ([]chainverify.Transfer, error)
}

// newTransferFetcher is swapped in tests.
var newTransferFetcher = func(overrides map[string][]string) transferFetcher {
	return chainverify.New(overrides)
}

// AdminSettleOrderByTxHash verifies an on-chain transfer against an unpaid
// crypto order and, unless DryRun, marks it paid and runs fulfillment. It
// exists for transfers the gateway could not match on its own — it matches by
// exact amount, so an exchange withdrawal that lost a fee on the way never
// triggers a callback.
func (s *PaymentService) AdminSettleOrderByTxHash(ctx context.Context, orderID int64, req ManualSettleRequest) (*ManualSettleResult, error) {
	o, err := s.entClient.PaymentOrder.Get(ctx, orderID)
	if err != nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if !manualSettleAllowedStatus(o.Status) {
		return nil, infraerrors.BadRequest("INVALID_STATUS", "only pending, expired or cancelled orders can be settled by transaction hash")
	}
	tradeNo := strings.TrimSpace(o.PaymentTradeNo)
	if tradeNo == "" {
		return nil, infraerrors.BadRequest("MISSING_TRADE_NO", "order has no gateway trade number")
	}
	normalizedHash, err := chainverify.NormalizeTxHash(req.TxHash)
	if err != nil {
		return nil, infraerrors.BadRequest("INVALID_TX_HASH", err.Error())
	}
	if req.Network != "" && !chainverify.SupportedNetwork(req.Network) {
		return nil, infraerrors.BadRequest("UNSUPPORTED_NETWORK", "unsupported network: "+req.Network)
	}

	prov, err := s.getOrderProvider(ctx, o)
	if err != nil {
		return nil, infraerrors.BadRequest("PROVIDER_UNAVAILABLE", "payment provider for this order is unavailable: "+err.Error())
	}
	settler, ok := prov.(payment.OnChainSettlementProvider)
	if !ok {
		return nil, infraerrors.BadRequest("UNSUPPORTED_PROVIDER", "this payment provider does not support settling by transaction hash")
	}
	target, err := settler.OnChainSettlementTarget(ctx, tradeNo)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("GATEWAY_UNAVAILABLE", "failed to read the order from the gateway: "+err.Error())
	}
	expected, err := decimal.NewFromString(strings.TrimSpace(target.ExpectedAmount))
	if err != nil || !expected.IsPositive() {
		return nil, infraerrors.BadRequest("NO_CHAIN_QUOTE", "the payer never picked a network for this order, so the gateway has no amount to verify against")
	}
	if !manualSettleStablecoin(target.Token) {
		return nil, infraerrors.BadRequest("UNSUPPORTED_QUOTE_TOKEN", "settling by transaction hash requires a USDT or USDC quote; native coin amounts use a different unit")
	}

	if usedBy, err := s.orderUsingTxHash(ctx, normalizedHash); err != nil {
		return nil, fmt.Errorf("check tx hash reuse: %w", err)
	} else if usedBy != "" {
		return nil, infraerrors.Conflict("TX_ALREADY_USED", "this transaction already settled order "+usedBy)
	}

	transfers, network, err := fetchSettlementTransfers(ctx, newTransferFetcher(target.ChainRPC), req, target.Network)
	if err != nil {
		return nil, err
	}
	matchTarget := target
	networkSwitched := false
	if expectedNetwork := chainverify.NormalizeNetwork(target.Network); expectedNetwork != "" && expectedNetwork != network {
		// The gateway reports only the parent order, so a payer who switched
		// chains in the cashier shows up here as a mismatch. An admin who picks
		// that chain explicitly may settle it, but only against the addresses
		// configured on the instance: the parent's own address is a quote for
		// another chain and proves nothing about this one.
		if len(target.TrustedAddresses) == 0 {
			return nil, infraerrors.BadRequest("NETWORK_MISMATCH", fmt.Sprintf(
				"the transaction is on %s but the order quote is for %s", network, expectedNetwork))
		}
		switched := *target
		switched.ReceiveAddress = ""
		matchTarget = &switched
		networkSwitched = true
	}
	matched, received := matchSettlementTransfers(transfers, network, matchTarget)
	if len(matched) == 0 && networkSwitched && !hasSettlementAddress(transfers, network, matchTarget) {
		return nil, infraerrors.BadRequest("NETWORK_MISMATCH", fmt.Sprintf(
			"the transaction is on %s, the order quote is for %s, and it does not pay any receiving address configured on the payment instance",
			network, chainverify.NormalizeNetwork(target.Network)))
	}
	if len(matched) == 0 {
		if hasSettlementAddress(transfers, network, matchTarget) {
			return nil, infraerrors.BadRequest("TOKEN_MISMATCH", fmt.Sprintf(
				"the transaction reaches the order address but uses %s instead of %s", settlementTransferTokens(transfers, network, matchTarget), target.Token))
		}
		return nil, infraerrors.BadRequest("ADDRESS_MISMATCH", manualSettleAddressMismatchMessage(transfers, target))
	}
	first := matched[0]
	if first.BlockTime.Before(o.CreatedAt.Add(-manualSettleClockSkew)) || first.BlockTime.After(o.CreatedAt.Add(manualSettleMaxAge)) {
		return nil, infraerrors.BadRequest("TX_OUTSIDE_ORDER_WINDOW", fmt.Sprintf(
			"transaction time %s does not belong to this order (created %s)",
			first.BlockTime.UTC().Format(time.RFC3339), o.CreatedAt.UTC().Format(time.RFC3339)))
	}

	// A payment fulfilled without a known chain hash may be this very transfer.
	if paidBy, err := hashlessPaidOrderNear(ctx, s.entClient, first.BlockTime, o.ID); err != nil {
		return nil, fmt.Errorf("check hashless payments: %w", err)
	} else if paidBy != 0 {
		return nil, infraerrors.Conflict("HASHLESS_PAYMENT_NEARBY", fmt.Sprintf(
			"order %d was credited around this transfer's time without a recorded transaction hash, so this transfer may already have paid it; check in the gateway admin which order holds this hash, and adjust the balance manually if it really belongs to this order",
			paidBy)).WithMetadata(map[string]string{"order_id": strconv.FormatInt(paidBy, 10)})
	}

	shortfall := decimal.Max(expected.Sub(received), decimal.Zero)
	allowed := manualSettleAllowedShortfall(expected)
	result := &ManualSettleResult{
		OrderID:          o.ID,
		OrderStatus:      o.Status,
		TxHash:           first.TxHash,
		Network:          network,
		Token:            first.Token,
		From:             first.From,
		To:               first.To,
		ReceivedAmount:   received.String(),
		ExpectedAmount:   expected.String(),
		ExpectedNetwork:  target.Network,
		ExpectedToken:    target.Token,
		Shortfall:        shortfall.String(),
		AllowedShortfall: allowed.String(),
		BlockTime:        first.BlockTime,
		Confirmations:    first.Confirmations,
	}
	if received.Sub(expected).GreaterThan(allowed) {
		return nil, infraerrors.BadRequest("AMOUNT_MISMATCH", fmt.Sprintf(
			"received %s %s but the order expects %s; a transfer this far above the quote is probably not this order's payment — adjust the balance manually if it is",
			received.String(), first.Token, expected.String()))
	}
	if shortfall.GreaterThan(allowed) {
		return nil, infraerrors.BadRequest("SHORTFALL_TOO_LARGE", fmt.Sprintf(
			"received %s %s but the order expects %s; a shortfall above %s cannot be settled automatically — adjust the balance manually",
			received.String(), first.Token, expected.String(), allowed.String()))
	}
	if req.DryRun {
		return result, nil
	}

	operator := strings.TrimSpace(req.Operator)
	if operator == "" {
		operator = "admin"
	}
	settled, err := s.commitChainSettlement(ctx, o, chainSettlement{
		txHash:          normalizedHash,
		network:         network,
		transfers:       matched,
		received:        received,
		expected:        expected,
		quoteNetwork:    target.Network,
		networkSwitched: networkSwitched,
		tradeNo:         tradeNo,
		operator:        operator,
		claimSource:     claimSourceManual,
		auditAction:     auditActionManualSettled,
	})
	if err != nil {
		return nil, err
	}
	slog.Info("order settled manually by tx hash", "orderID", o.ID, "txHash", first.TxHash, "network", network, "operator", operator)
	result.Settled = true
	result.OrderStatus = OrderStatusPaid
	if settled.fulfillErr != nil {
		// The order is PAID and audited; the regular retry path can finish it.
		return result, infraerrors.InternalServer("FULFILLMENT_FAILED", "order marked paid but fulfillment failed, use retry: "+settled.fulfillErr.Error())
	}
	if cur, getErr := s.entClient.PaymentOrder.Get(ctx, o.ID); getErr == nil {
		result.OrderStatus = cur.Status
	}
	return result, nil
}

// chainSettlement is a verified on-chain payment about to settle an order.
type chainSettlement struct {
	txHash          string
	network         string
	transfers       []chainverify.Transfer
	received        decimal.Decimal
	expected        decimal.Decimal
	quoteNetwork    string
	networkSwitched bool
	tradeNo         string
	operator        string
	claimSource     string
	auditAction     string
	extraAudit      map[string]any
}

type chainSettlementOutcome struct {
	creditedAmount float64
	fulfillErr     error
}

// commitChainSettlement claims the transaction, marks the order PAID and runs
// fulfillment. A USDT-quoted order is credited for what actually arrived, at
// its locked rate; any other order keeps its original amount.
func (s *PaymentService) commitChainSettlement(ctx context.Context, o *dbent.PaymentOrder, cs chainSettlement) (*chainSettlementOutcome, error) {
	first := cs.transfers[0]
	previousStatus := o.Status
	tx, finish, err := beginPaymentSettlement(ctx, s.entClient)
	if err != nil {
		return nil, fmt.Errorf("begin chain settlement: %w", err)
	}
	defer finish()
	client := tx.Client()
	// The preview is advisory. Recheck under the same cross-process lock used
	// by hashless confirmations before committing ownership.
	if paidBy, err := hashlessPaidOrderNear(ctx, client, first.BlockTime, o.ID); err != nil {
		return nil, err
	} else if paidBy != 0 {
		return nil, infraerrors.Conflict("HASHLESS_PAYMENT_NEARBY", fmt.Sprintf("order %d has an unresolved hashless confirmation", paidBy))
	}
	if _, err := claimPaymentTransaction(ctx, client, o.ID, cs.txHash); err != nil {
		return nil, err
	}
	if err := recordSettlementClaim(ctx, client, o, cs.txHash, cs.claimSource, &first.BlockTime, map[string]any{
		"network": cs.network, "token": first.Token, "destination": first.To,
		"received_amount": cs.received.String(), "transfers": cs.transfers,
	}); err != nil {
		return nil, err
	}
	resolved, err := client.PaymentTransactionClaim.Update().Where(
		paymenttransactionclaim.OrderIDEQ(o.ID), paymenttransactionclaim.ReviewPendingEQ(true),
	).SetReviewPending(false).Save(ctx)
	if err != nil {
		return nil, err
	}
	if resolved > 0 {
		if err := writePaymentAudit(ctx, client, o.ID, "PAYMENT_REVIEW_RESOLVED", cs.operator, map[string]any{"txHash": cs.txHash}); err != nil {
			return nil, err
		}
	}
	update := client.PaymentOrder.Update().Where(
		paymentorder.IDEQ(o.ID),
		paymentorder.StatusIn(OrderStatusPending, OrderStatusExpired, OrderStatusCancelled),
	).SetStatus(OrderStatusPaid).SetPaidAt(time.Now()).ClearFailedAt().ClearFailedReason()
	snapshot := make(map[string]any, len(o.ProviderSnapshot)+1)
	for k, v := range o.ProviderSnapshot {
		snapshot[k] = v
	}
	snapshot[snapshotKeySettledBy] = settledByFromClaimSource(cs.claimSource)
	update.SetProviderSnapshot(snapshot)
	outcome := &chainSettlementOutcome{creditedAmount: o.Amount}
	quote := orderUSDTQuote(o)
	if quote != nil {
		snapshot[snapshotKeyGatewayAmount] = decimal.NewFromFloat(paymentGatewayAmount(o)).String()
		outcome.creditedAmount = usdtQuotedCredit(o, cs.received)
		update.SetAmount(outcome.creditedAmount).SetPayAmount(cs.received.Round(2).InexactFloat64())
	}
	updated, err := update.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update to PAID: %w", err)
	}
	if updated == 0 {
		return nil, infraerrors.Conflict("INVALID_STATUS", "order status changed while settling, reload and check it")
	}
	detail := map[string]any{
		"txHash":          cs.txHash,
		"network":         cs.network,
		"token":           first.Token,
		"from":            first.From,
		"to":              first.To,
		"receivedAmount":  cs.received.String(),
		"expectedAmount":  cs.expected.String(),
		"shortfall":       decimal.Max(cs.expected.Sub(cs.received), decimal.Zero).String(),
		"blockTime":       first.BlockTime.UTC().Format(time.RFC3339),
		"quoteNetwork":    cs.quoteNetwork,
		"networkSwitched": cs.networkSwitched,
		"previous_status": previousStatus,
		"tradeNo":         cs.tradeNo,
	}
	if quote != nil {
		detail["usdtCnyRate"] = quote.rate.String()
		detail["originalAmount"] = o.Amount
		detail["creditedAmount"] = outcome.creditedAmount
	}
	for k, v := range cs.extraAudit {
		detail[k] = v
	}
	if err := writePaymentAudit(ctx, client, o.ID, cs.auditAction, cs.operator, detail); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit chain settlement: %w", err)
	}
	finish()
	outcome.fulfillErr = s.executeFulfillment(ctx, o.ID)
	return outcome, nil
}

// snapshotKeySettledBy marks orders settled from chain data instead of a
// gateway confirmation: "auto" or "manual".
const snapshotKeySettledBy = "settled_by"

func settledByFromClaimSource(source string) string {
	if source == claimSourceAuto {
		return "auto"
	}
	return "manual"
}

// PaymentOrderSettledBy reports how a crypto order was settled from chain data
// ("auto", "manual"), or "" for a regular gateway confirmation.
func PaymentOrderSettledBy(o *dbent.PaymentOrder) string {
	if o == nil {
		return ""
	}
	return psSnapshotStringValue(o.ProviderSnapshot[snapshotKeySettledBy])
}

func manualSettleAllowedStatus(status string) bool {
	switch status {
	case OrderStatusPending, OrderStatusExpired, OrderStatusCancelled:
		return true
	default:
		return false
	}
}

func manualSettleAllowedShortfall(expected decimal.Decimal) decimal.Decimal {
	allowed := decimal.Max(expected.Mul(manualSettleShortfallRatio), manualSettleShortfallFloor)
	return decimal.Min(allowed, expected.Mul(manualSettleShortfallCap))
}

// fetchSettlementTransfers looks the hash up on the requested network. When
// the order has no selected network yet, it probes candidate networks.
func fetchSettlementTransfers(ctx context.Context, fetcher transferFetcher, req ManualSettleRequest, gatewayNetwork string) ([]chainverify.Transfer, string, error) {
	candidates := chainverify.CandidateNetworks(req.TxHash, gatewayNetwork)
	if req.Network != "" {
		candidates = []string{chainverify.NormalizeNetwork(req.Network)}
	} else if normalizedGatewayNetwork := chainverify.NormalizeNetwork(gatewayNetwork); normalizedGatewayNetwork != "" {
		// A quote is bound to the chain selected in the cashier. Reusing the
		// merchant address on another EVM network must not make that network a
		// valid fallback for this order.
		candidates = []string{normalizedGatewayNetwork}
	}
	var lastErr error
	for _, network := range candidates {
		transfers, err := fetcher.Transfers(ctx, network, req.TxHash)
		switch {
		case err == nil:
			return transfers, network, nil
		case errors.Is(err, chainverify.ErrTxFailed):
			return nil, "", infraerrors.BadRequest("TX_FAILED", "the transaction failed on chain ("+network+")")
		case chainverify.IsUnconfirmed(err):
			return nil, "", infraerrors.BadRequest("TX_UNCONFIRMED", "the transaction is not final yet, try again in a minute: "+err.Error())
		case errors.Is(err, chainverify.ErrTxNotFound):
			continue
		default:
			lastErr = err
		}
	}
	if lastErr != nil {
		return nil, "", infraerrors.ServiceUnavailable("CHAIN_RPC_UNAVAILABLE", "could not reach the chain RPC: "+lastErr.Error())
	}
	return nil, "", infraerrors.BadRequest("TX_NOT_FOUND", "transaction not found on "+strings.Join(candidates, ", "))
}

// matchSettlementTransfers keeps the transfers that landed on an address the
// merchant owns for this order and sums them up.
func matchSettlementTransfers(transfers []chainverify.Transfer, network string, target *payment.OnChainSettlementTarget) ([]chainverify.Transfer, decimal.Decimal) {
	addresses := append([]string{target.ReceiveAddress}, target.TrustedAddresses...)
	var matched []chainverify.Transfer
	total := decimal.Zero
	for _, tr := range transfers {
		if !strings.EqualFold(strings.TrimSpace(tr.Token), strings.TrimSpace(target.Token)) {
			continue
		}
		for _, addr := range addresses {
			if chainverify.SameAddress(network, tr.To, addr) {
				matched = append(matched, tr)
				total = total.Add(tr.Amount)
				break
			}
		}
	}
	return matched, total
}

func hasSettlementAddress(transfers []chainverify.Transfer, network string, target *payment.OnChainSettlementTarget) bool {
	addresses := append([]string{target.ReceiveAddress}, target.TrustedAddresses...)
	for _, tr := range transfers {
		for _, addr := range addresses {
			if chainverify.SameAddress(network, tr.To, addr) {
				return true
			}
		}
	}
	return false
}

func settlementTransferTokens(transfers []chainverify.Transfer, network string, target *payment.OnChainSettlementTarget) string {
	seen := make(map[string]struct{})
	var tokens []string
	addresses := append([]string{target.ReceiveAddress}, target.TrustedAddresses...)
	for _, tr := range transfers {
		for _, addr := range addresses {
			if chainverify.SameAddress(network, tr.To, addr) {
				name := strings.TrimSpace(tr.Token)
				if name != "" {
					if _, ok := seen[name]; !ok {
						seen[name] = struct{}{}
						tokens = append(tokens, name)
					}
				}
				break
			}
		}
	}
	return strings.Join(tokens, ", ")
}

func manualSettleAddressMismatchMessage(transfers []chainverify.Transfer, target *payment.OnChainSettlementTarget) string {
	if len(transfers) == 0 {
		return "the transaction contains no supported stablecoin transfer"
	}
	recipients := make([]string, 0, len(transfers))
	for _, tr := range transfers {
		recipients = append(recipients, tr.To)
	}
	return fmt.Sprintf("the transaction pays %s, not this order's receiving address %s; if the payer switched network in the cashier, add that network's receiving address to the instance's trusted addresses",
		strings.Join(recipients, ", "), target.ReceiveAddress)
}

func manualSettleStablecoin(token string) bool {
	switch strings.ToUpper(strings.TrimSpace(token)) {
	case "USDT", "USDC", "USDC.E":
		return true
	default:
		return false
	}
}
