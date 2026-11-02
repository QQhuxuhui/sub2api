//go:build unit

package service

import (
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestUSDTOrdersDoNotUseRechargeBonus(t *testing.T) {
	tiers := []RechargeBonusTier{{MinAmount: 100, BonusPercent: 10}}
	for _, mode := range []string{RechargeBonusModeBonus, RechargeBonusModeDiscount} {
		t.Run(mode, func(t *testing.T) {
			cfg := &PaymentConfig{BalanceRechargeMultiplier: 1, RechargeBonusTiers: tiers, RechargeBonusMode: mode}

			usdt := quoteRechargeBonus(rechargeBonusConfigForPaymentType(cfg, payment.TypeUSDT), 150, "USD")
			require.InDelta(t, 150, usdt.PayBase, 1e-9)
			require.InDelta(t, 150, usdt.Credited, 1e-9)
			require.Zero(t, usdt.Bonus)
			require.Zero(t, usdt.Percent)
			require.Len(t, cfg.RechargeBonusTiers, 1, "不能改动共享配置")

			other := quoteRechargeBonus(rechargeBonusConfigForPaymentType(cfg, payment.TypeAlipay), 150, "USD")
			require.Positive(t, other.Percent, "其他支付方式优惠不变")
		})
	}
}

func TestUSDTRebateBaseIsCreditedAmount(t *testing.T) {
	order := &dbent.PaymentOrder{OrderType: payment.OrderTypeBalance, Amount: 148.5, BonusAmount: 0}
	require.InDelta(t, 148.5, paymentOrderAmountWithoutBonus(order), 1e-9)
}
