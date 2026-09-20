package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
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

	if usedBy, err := s.orderUsingTxHash(ctx, normalizedHash); err != nil {
		return nil, fmt.Errorf("check tx hash reuse: %w", err)
	} else if usedBy != "" {
		return nil, infraerrors.Conflict("TX_ALREADY_USED", "this transaction already settled order "+usedBy)
	}

	transfers, network, err := fetchSettlementTransfers(ctx, newTransferFetcher(target.ChainRPC), req, target.Network)
	if err != nil {
		return nil, err
	}
	matched, received := matchSettlementTransfers(transfers, network, target)
	if len(matched) == 0 {
		return nil, infraerrors.BadRequest("ADDRESS_MISMATCH", manualSettleAddressMismatchMessage(transfers, target))
	}
	first := matched[0]
	if first.BlockTime.Before(o.CreatedAt.Add(-manualSettleClockSkew)) || first.BlockTime.After(o.CreatedAt.Add(manualSettleMaxAge)) {
		return nil, infraerrors.BadRequest("TX_OUTSIDE_ORDER_WINDOW", fmt.Sprintf(
			"transaction time %s does not belong to this order (created %s)",
			first.BlockTime.UTC().Format(time.RFC3339), o.CreatedAt.UTC().Format(time.RFC3339)))
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

	previousStatus := o.Status
	updated, err := s.entClient.PaymentOrder.Update().Where(
		paymentorder.IDEQ(o.ID),
		paymentorder.StatusIn(OrderStatusPending, OrderStatusExpired, OrderStatusCancelled),
	).SetStatus(OrderStatusPaid).SetPaidAt(time.Now()).ClearFailedAt().ClearFailedReason().Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update to PAID: %w", err)
	}
	if updated == 0 {
		return nil, infraerrors.Conflict("INVALID_STATUS", "order status changed while settling, reload and check it")
	}
	operator := strings.TrimSpace(req.Operator)
	if operator == "" {
		operator = "admin"
	}
	slog.Info("order settled manually by tx hash", "orderID", o.ID, "txHash", first.TxHash, "network", network, "operator", operator)
	s.writeAuditLog(ctx, o.ID, auditActionManualSettled, operator, map[string]any{
		"txHash":          normalizedHash,
		"network":         network,
		"token":           first.Token,
		"from":            first.From,
		"to":              first.To,
		"receivedAmount":  received.String(),
		"expectedAmount":  expected.String(),
		"shortfall":       shortfall.String(),
		"blockTime":       first.BlockTime.UTC().Format(time.RFC3339),
		"previous_status": previousStatus,
		"tradeNo":         tradeNo,
	})
	result.Settled = true
	result.OrderStatus = OrderStatusPaid
	if err := s.executeFulfillment(ctx, o.ID); err != nil {
		// The order is PAID and audited; the regular retry path can finish it.
		return result, infraerrors.InternalServer("FULFILLMENT_FAILED", "order marked paid but fulfillment failed, use retry: "+err.Error())
	}
	if cur, getErr := s.entClient.PaymentOrder.Get(ctx, o.ID); getErr == nil {
		result.OrderStatus = cur.Status
	}
	return result, nil
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

// fetchSettlementTransfers looks the hash up on the requested network, or
// probes the candidates in order until one of them knows it.
func fetchSettlementTransfers(ctx context.Context, fetcher transferFetcher, req ManualSettleRequest, gatewayNetwork string) ([]chainverify.Transfer, string, error) {
	candidates := chainverify.CandidateNetworks(req.TxHash, gatewayNetwork)
	if req.Network != "" {
		candidates = []string{chainverify.NormalizeNetwork(req.Network)}
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

// orderUsingTxHash returns the order id that already consumed the hash, either
// through a gateway callback (ORDER_PAID carries txHash) or a manual settle.
func (s *PaymentService) orderUsingTxHash(ctx context.Context, normalizedHash string) (string, error) {
	row, err := s.entClient.PaymentAuditLog.Query().Where(
		paymentauditlog.ActionIn("ORDER_PAID", auditActionManualSettled),
		paymentauditlog.DetailContainsFold(normalizedHash),
	).First(ctx)
	if dbent.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return row.OrderID, nil
}

// paymentTxHashFromMetadata extracts the on-chain hash a crypto gateway
// reported, normalized the way manual settlement stores it.
func paymentTxHashFromMetadata(metadata map[string]string) string {
	hash, err := chainverify.NormalizeTxHash(metadata["block_transaction_id"])
	if err != nil {
		return ""
	}
	return hash
}
