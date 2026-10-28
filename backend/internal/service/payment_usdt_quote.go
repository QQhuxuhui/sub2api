package service

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"

	"github.com/shopspring/decimal"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// USDT-quoted orders: an Epusdt instance whose currency is USD is driven in
// whole USDT (the gateway prices USD 1:1 in USDT). The payer sends that many
// USDT from wherever they like; whatever the withdrawal fee leaves is what
// gets credited, converted at the CNY rate locked when the order was created.
const (
	usdtQuoteUnit            = "USDT"
	usdtQuoteMinAmount       = 10
	snapshotKeyQuoteUnit     = "quote_unit"
	snapshotKeyUSDTCNYRate   = "usdt_cny_rate"
	snapshotKeyMultiplier    = "balance_multiplier"
	snapshotKeyGatewayAmount = "usdt_gateway_amount"
)

// usdtCNYRate is swapped in tests.
var usdtCNYRate = payment.USDTCNYRate

type usdtQuote struct {
	rate       decimal.Decimal
	multiplier float64
}

func isUSDTQuotedSelection(sel *payment.InstanceSelection) bool {
	return sel != nil && isUSDTQuotedConfig(sel.ProviderKey, sel.Config)
}

func isUSDTQuotedConfig(providerKey string, cfg map[string]string) bool {
	return strings.TrimSpace(providerKey) == payment.TypeEpusdt && paymentProviderConfigCurrency(providerKey, cfg) == "USD"
}

// prepareUSDTQuote validates a USDT-quoted balance order and locks the rate.
func prepareUSDTQuote(ctx context.Context, req CreateOrderRequest, cfg *PaymentConfig) (*usdtQuote, error) {
	if req.OrderType != payment.OrderTypeBalance {
		return nil, infraerrors.BadRequest("USDT_QUOTE_BALANCE_ONLY", "USDT payments can only be used for balance recharge")
	}
	if req.Amount != math.Trunc(req.Amount) {
		return nil, infraerrors.BadRequest("INVALID_AMOUNT", "USDT amount must be a whole number").
			WithMetadata(map[string]string{"currency": usdtQuoteUnit})
	}
	if req.Amount < usdtQuoteMinAmount {
		return nil, infraerrors.BadRequest("INVALID_AMOUNT", "amount out of range").
			WithMetadata(map[string]string{"min": fmt.Sprintf("%d", usdtQuoteMinAmount), "currency": usdtQuoteUnit})
	}
	rate, err := usdtCNYRate(ctx)
	if err != nil {
		slog.Warn("[PaymentService] usdt/cny rate unavailable", "error", err)
		return nil, infraerrors.ServiceUnavailable("USDT_RATE_UNAVAILABLE", "USDT exchange rate is temporarily unavailable, try again later or use another payment method")
	}
	return &usdtQuote{rate: rate, multiplier: normalizeBalanceRechargeMultiplier(cfg.BalanceRechargeMultiplier)}, nil
}

// credit converts USDT received into balance: USDT × CNY rate × multiplier.
func (q *usdtQuote) credit(usdt decimal.Decimal) float64 {
	cny := usdt.Mul(q.rate).Round(2).InexactFloat64()
	return calculateCreditedBalance(cny, q.multiplier)
}

// usdtQuotedCredit scales the order's credited amount by the share of the
// quoted USDT that actually arrived. The quote already holds the locked rate,
// the multiplier and any recharge fee, so a short or long payment is credited
// by the same rule as a full gateway confirmation.
func usdtQuotedCredit(o *dbent.PaymentOrder, received decimal.Decimal) float64 {
	quoted := decimal.NewFromFloat(o.PayAmount)
	if !quoted.IsPositive() {
		return o.Amount
	}
	return decimal.NewFromFloat(o.Amount).Mul(received).Div(quoted).Round(2).InexactFloat64()
}

// paymentGatewayAmount is the immutable amount sent to the gateway. PayAmount
// may subsequently hold the actual tokens received, including a quote suffix
// or a withdrawal fee. Orders created before this snapshot field was added
// still have their original PayAmount until their first settlement.
func paymentGatewayAmount(o *dbent.PaymentOrder) float64 {
	if orderUSDTQuote(o) != nil {
		if amount, err := decimal.NewFromString(psSnapshotStringValue(o.ProviderSnapshot[snapshotKeyGatewayAmount])); err == nil && amount.IsPositive() {
			return amount.InexactFloat64()
		}
	}
	return o.PayAmount
}

// snapshotWithGatewayAmount copies the snapshot before changing PayAmount so
// later callbacks can still be checked against the original gateway request.
func snapshotWithGatewayAmount(o *dbent.PaymentOrder) map[string]any {
	snapshot := make(map[string]any, len(o.ProviderSnapshot)+1)
	for key, value := range o.ProviderSnapshot {
		snapshot[key] = value
	}
	snapshot[snapshotKeyGatewayAmount] = decimal.NewFromFloat(paymentGatewayAmount(o)).String()
	return snapshot
}

// usdtQuotedGatewayCredit is the credit for a USDT-quoted order confirmed by
// the gateway, from the token amount it reports as received. Amounts outside
// the auto-settle tolerance of the quote are not trusted and keep the
// original credit.
func usdtQuotedGatewayCredit(o *dbent.PaymentOrder, metadata map[string]string) (float64, float64, bool) {
	if orderUSDTQuote(o) == nil {
		return 0, 0, false
	}
	actual, err := decimal.NewFromString(strings.TrimSpace(metadata["actual_amount"]))
	if err != nil || !actual.IsPositive() {
		return 0, 0, false
	}
	quoted := decimal.NewFromFloat(o.PayAmount)
	if !quoted.IsPositive() || actual.Sub(quoted).Abs().GreaterThan(autoSettleTolerance(quoted)) {
		slog.Warn("[PaymentService] gateway actual amount far from USDT quote, keeping quoted credit", "orderID", o.ID, "quoted", quoted.String(), "actual", actual.String())
		return 0, 0, false
	}
	return usdtQuotedCredit(o, actual), actual.Round(2).InexactFloat64(), true
}

func (q *usdtQuote) applyToSnapshot(snapshot map[string]any) map[string]any {
	if snapshot == nil {
		snapshot = map[string]any{"schema_version": 2}
	}
	snapshot[snapshotKeyQuoteUnit] = usdtQuoteUnit
	snapshot[snapshotKeyUSDTCNYRate] = q.rate.String()
	snapshot[snapshotKeyMultiplier] = decimal.NewFromFloat(q.multiplier).String()
	return snapshot
}

// orderUSDTQuote returns the locked quote of a USDT-quoted order, or nil.
func orderUSDTQuote(o *dbent.PaymentOrder) *usdtQuote {
	if o == nil || o.ProviderSnapshot == nil || psSnapshotStringValue(o.ProviderSnapshot[snapshotKeyQuoteUnit]) != usdtQuoteUnit {
		return nil
	}
	rate, err := decimal.NewFromString(psSnapshotStringValue(o.ProviderSnapshot[snapshotKeyUSDTCNYRate]))
	if err != nil || !rate.IsPositive() {
		return nil
	}
	multiplier := defaultBalanceRechargeMultiplier
	if m, err := decimal.NewFromString(psSnapshotStringValue(o.ProviderSnapshot[snapshotKeyMultiplier])); err == nil && m.IsPositive() {
		multiplier = m.InexactFloat64()
	}
	return &usdtQuote{rate: rate, multiplier: multiplier}
}

// cancelPendingUSDTOrders drops the user's earlier unpaid USDT orders so at
// most one is waiting at a time: two open orders of similar amounts would make
// a short transfer impossible to attribute automatically.
func (s *PaymentService) cancelPendingUSDTOrders(ctx context.Context, userID int64, paymentType string) error {
	pending, err := s.entClient.PaymentOrder.Query().Where(
		paymentorder.UserIDEQ(userID),
		paymentorder.PaymentTypeEQ(paymentType),
		paymentorder.StatusEQ(OrderStatusPending),
	).All(ctx)
	if err != nil {
		return fmt.Errorf("query pending usdt orders: %w", err)
	}
	for _, o := range pending {
		if _, err := s.cancelCore(ctx, o, OrderStatusCancelled, fmt.Sprintf("user:%d", userID), "superseded by a new USDT order"); err != nil {
			return err
		}
	}
	return nil
}
