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

// CreateOrder 余额订单金额走 balanceOrderAmounts：USDT 在赠送/折扣两种配置下都不享优惠。
func TestBalanceOrderAmountsExcludeUSDTFromRechargeBonus(t *testing.T) {
	tiers := []RechargeBonusTier{{MinAmount: 100, BonusPercent: 20}}

	bonusCfg := &PaymentConfig{BalanceRechargeMultiplier: 1, RechargeBonusTiers: tiers, RechargeBonusMode: RechargeBonusModeBonus}
	credited, payBase, bonus := balanceOrderAmounts(bonusCfg, payment.TypeUSDT, 100, "USD")
	require.InDelta(t, 100, credited, 1e-9)
	require.InDelta(t, 100, payBase, 1e-9)
	require.Zero(t, bonus)
	credited, payBase, bonus = balanceOrderAmounts(bonusCfg, payment.TypeAlipay, 100, "CNY")
	require.InDelta(t, 120, credited, 1e-9, "非 USDT 赠送模式照上游")
	require.InDelta(t, 100, payBase, 1e-9)
	require.InDelta(t, 20, bonus, 1e-9)

	discountCfg := &PaymentConfig{BalanceRechargeMultiplier: 1, RechargeBonusTiers: tiers, RechargeBonusMode: RechargeBonusModeDiscount}
	credited, payBase, bonus = balanceOrderAmounts(discountCfg, payment.TypeUSDT, 100, "USD")
	require.InDelta(t, 100, credited, 1e-9)
	require.InDelta(t, 100, payBase, 1e-9, "USDT 实付基数不打折")
	require.Zero(t, bonus)
	credited, payBase, _ = balanceOrderAmounts(discountCfg, payment.TypeAlipay, 100, "CNY")
	require.InDelta(t, 100, credited, 1e-9)
	require.InDelta(t, 80, payBase, 1e-9, "非 USDT 折扣模式照上游")
}
