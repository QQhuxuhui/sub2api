//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestUltrafastRegressionInterval(t *testing.T) {
	svc := newTestBillingServiceWithUltrafastCatalog()
	base, err := svc.GetModelPricing("gpt-6.1-sol")
	require.NoError(t, err)
	mult := 2.0
	resolved := &ResolvedPricing{BasePricing: base, Intervals: []PricingInterval{{MinTokens: 100, InputMultiplier: &mult}}, longContextPricingEnabled: true}
	cost, err := svc.calculateTokenCost(resolved, CostInput{Model: "gpt-6.1-sol", Tokens: UsageTokens{InputTokens: 1000}, RateMultiplier: 1, ServiceTier: "ultrafast", Resolver: &ModelPricingResolver{}})
	require.NoError(t, err)
	require.InDelta(t, 0.024, cost.InputCost, 1e-12)
}
func TestUltrafastRegressionFastOverride(t *testing.T) {
	svc := newTestBillingServiceWithUltrafastCatalog()
	fast := 2.5
	p, err := svc.GetModelPricingWithChannel("gpt-6.1-sol", &ChannelModelPricing{FastMultiplier: &fast})
	require.NoError(t, err)
	cost := svc.computeTokenBreakdown(p, UsageTokens{InputTokens: 1000}, 1, "ultrafast", false)
	require.InDelta(t, 0.012, cost.InputCost, 1e-12)
	priority := svc.computeTokenBreakdown(p, UsageTokens{InputTokens: 1000}, 1, "priority", false)
	require.InDelta(t, 0.005, priority.InputCost, 1e-12)
}

func TestUltrafastRegressionIntervalAllTokenPrices(t *testing.T) {
	twice := 2.0
	in, out, write, read := 4e-6, 20e-6, 6e-6, 0.2e-6
	for name, interval := range map[string]PricingInterval{
		"multipliers": {InputMultiplier: &twice, OutputMultiplier: &twice, CacheWriteMultiplier: &twice, CacheReadMultiplier: &twice},
		"absolute":    {InputPrice: &in, OutputPrice: &out, CacheWritePrice: &write, CacheReadPrice: &read},
	} {
		t.Run(name, func(t *testing.T) {
			base := &ModelPricing{
				InputPricePerToken: 2e-6, OutputPricePerToken: 10e-6, CacheCreationPricePerToken: 3e-6, CacheReadPricePerToken: 0.1e-6,
				InputPricePerTokenPriority: 4e-6, OutputPricePerTokenPriority: 20e-6, CacheCreationPricePerTokenPriority: 6e-6, CacheReadPricePerTokenPriority: 0.2e-6,
				InputPricePerTokenUltrafast: 12e-6, OutputPricePerTokenUltrafast: 60e-6, CacheCreationPricePerTokenUltrafast: 18e-6, CacheReadPricePerTokenUltrafast: 0.6e-6,
			}
			pricing := intervalToModelPricing(&interval, base, nil)
			cost := newTestBillingService().computeTokenBreakdown(pricing, UsageTokens{InputTokens: 1000, OutputTokens: 1000, CacheCreationTokens: 1000, CacheReadTokens: 1000}, 1, "ultrafast", false)
			require.InDelta(t, 0.024, cost.InputCost, 1e-12)
			require.InDelta(t, 0.12, cost.OutputCost, 1e-12)
			require.InDelta(t, 0.036, cost.CacheCreationCost, 1e-12)
			require.InDelta(t, 0.0012, cost.CacheReadCost, 1e-12)
			require.InDelta(t, 12e-6, base.InputPricePerTokenUltrafast, 1e-15)
		})
	}
}
func TestUltrafastRegressionAstraFallback(t *testing.T) {
	svc := NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{}})
	cost, err := svc.CalculateCostWithServiceTier("gpt-6-astra", UsageTokens{InputTokens: 1000}, 1, "ultrafast")
	require.NoError(t, err)
	require.InDelta(t, 0.06, cost.InputCost, 1e-12)
}
