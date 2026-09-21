package service

import (
	"context"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// CryptoQuote is what the gateway expects to ARRIVE for a crypto order, in
// token units. It is empty until the payer has picked a network in the cashier.
type CryptoQuote struct {
	Network string `json:"network"`
	Token   string `json:"token"`
	// Amount is a decimal string; "" means the gateway has no quote yet.
	Amount string `json:"amount"`
}

// GetOrderCryptoQuote reads the gateway quote of the user's own order so the
// payment page can tell a payer withdrawing from an exchange how much to enter:
// exchanges deduct their fee from the withdrawn amount, while the gateway only
// matches a transfer that arrives with exactly the quoted amount.
//
// When the payer switches network inside the cashier the gateway tracks the new
// quote in a sub-order this lookup cannot see, so callers must present the
// figure as "check it against the cashier", not as authoritative.
func (s *PaymentService) GetOrderCryptoQuote(ctx context.Context, orderID, userID int64) (*CryptoQuote, error) {
	o, err := s.GetOrder(ctx, orderID, userID)
	if err != nil {
		return nil, err
	}
	if o.Status != OrderStatusPending {
		return nil, infraerrors.BadRequest("INVALID_STATUS", "a quote is only available while the order is pending")
	}
	tradeNo := strings.TrimSpace(o.PaymentTradeNo)
	if tradeNo == "" {
		return &CryptoQuote{}, nil
	}
	prov, err := s.getOrderProvider(ctx, o)
	if err != nil {
		return nil, infraerrors.BadRequest("PROVIDER_UNAVAILABLE", "payment provider for this order is unavailable")
	}
	settler, ok := prov.(payment.OnChainSettlementProvider)
	if !ok {
		return nil, infraerrors.BadRequest("UNSUPPORTED_PROVIDER", "this payment method has no on-chain quote")
	}
	target, err := settler.OnChainSettlementTarget(ctx, tradeNo)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("GATEWAY_UNAVAILABLE", "failed to read the quote from the gateway")
	}
	quote := &CryptoQuote{Network: target.Network, Token: target.Token}
	if amount, err := decimal.NewFromString(strings.TrimSpace(target.ExpectedAmount)); err == nil && amount.IsPositive() {
		quote.Amount = amount.String()
	}
	return quote, nil
}
