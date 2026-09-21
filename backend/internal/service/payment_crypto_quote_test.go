//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

func TestGetOrderCryptoQuote(t *testing.T) {
	ctx := context.Background()

	t.Run("returns what the gateway expects to arrive", func(t *testing.T) {
		f := newManualSettleFixture(t, OrderStatusPending, "4.48")
		f.provider.target.ExpectedAmount = "4.480"
		quote, err := f.svc.GetOrderCryptoQuote(ctx, f.order.ID, f.order.UserID)
		require.NoError(t, err)
		require.Equal(t, &CryptoQuote{Network: "binance", Token: "USDT", Amount: "4.48"}, quote)
	})

	t.Run("empty until the payer picks a network", func(t *testing.T) {
		f := newManualSettleFixture(t, OrderStatusPending, "4.48")
		f.provider.target = &payment.OnChainSettlementTarget{}
		quote, err := f.svc.GetOrderCryptoQuote(ctx, f.order.ID, f.order.UserID)
		require.NoError(t, err)
		require.Empty(t, quote.Amount)
	})

	t.Run("only the owner may read it", func(t *testing.T) {
		f := newManualSettleFixture(t, OrderStatusPending, "4.48")
		_, err := f.svc.GetOrderCryptoQuote(ctx, f.order.ID, f.order.UserID+1)
		requireManualSettleReason(t, err, "FORBIDDEN")
	})

	t.Run("only while the order is pending", func(t *testing.T) {
		f := newManualSettleFixture(t, OrderStatusCompleted, "4.48")
		_, err := f.svc.GetOrderCryptoQuote(ctx, f.order.ID, f.order.UserID)
		requireManualSettleReason(t, err, "INVALID_STATUS")
	})
}
