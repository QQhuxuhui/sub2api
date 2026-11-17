//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type plazaListerStub struct {
	byGroup map[int64][]string
	calls   []int64
}

func (s *plazaListerStub) ListingModelIDsForGroup(_ context.Context, group *Group) []string {
	s.calls = append(s.calls, group.ID)
	return s.byGroup[group.ID]
}

func TestListGroups_AppendsListingModelsForGroupsWithoutChannels(t *testing.T) {
	channels := []Channel{plazaPricedChannel(1, "ch", []int64{10}, PlatformAnthropic, "claude-sonnet")}
	groups := []Group{
		{ID: 10, Name: "bound", Platform: PlatformAnthropic, RateMultiplier: 1},
		{ID: 20, Name: "unbound", Platform: PlatformOpenAI, RateMultiplier: 2},
	}
	svc := newPlazaService(channels, groups, nil)
	svc.SetModelLister(&plazaListerStub{byGroup: map[int64][]string{
		10: {"claude-sonnet", "claude-opus", " ", "claude-opus"},
		20: {"gpt-b", "gpt-a"},
	}})

	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 2, "没有绑定渠道的分组也应出现")

	bound := plazaModelsByName(out[0].Models)
	require.Len(t, out[0].Models, 2, "渠道已声明的模型不重复追加，空名与重复名被忽略")
	require.NotNil(t, bound["claude-sonnet"].Pricing, "渠道声明的条目保留其定价")
	require.InDelta(t, 3e-6, *bound["claude-sonnet"].Pricing.InputPrice, 1e-15)
	require.Equal(t, PlatformAnthropic, bound["claude-opus"].Platform)

	require.Equal(t, int64(20), out[1].ID)
	require.Equal(t, []string{"gpt-a", "gpt-b"}, []string{out[1].Models[0].Name, out[1].Models[1].Name})
	require.Equal(t, PlatformOpenAI, out[1].Models[0].Platform)
}

func TestListGroups_ListingSkipsCompositeGroups(t *testing.T) {
	lister := &plazaListerStub{byGroup: map[int64][]string{30: {"gpt-a"}}}
	svc := newPlazaService(nil, []Group{{ID: 30, Name: "mix", Platform: PlatformComposite}}, nil)
	svc.SetModelLister(lister)

	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Empty(t, out)
	require.Empty(t, lister.calls, "composite 分组不走同源枚举")
}

func TestListGroups_ListingModelUsesWildcardChannelNonTokenPricing(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode BillingMode
	}{
		{"per_request", BillingModePerRequest},
		{"image", BillingModeImage},
		{"video", BillingModeVideo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channels := []Channel{{
				ID: 1, Name: "ch", Status: StatusActive, GroupIDs: []int64{10},
				ModelPricing: []ChannelModelPricing{{
					Platform:        PlatformAnthropic,
					Models:          []string{"claude-*"},
					BillingMode:     tc.mode,
					PerRequestPrice: testPtrFloat64(0.05),
				}},
			}}
			groups := []Group{{ID: 10, Name: "g", Platform: PlatformAnthropic, RateMultiplier: 1}}
			svc := newPlazaServiceWithBilling(channels, groups, map[int64]string{10: PlatformAnthropic}, nil)
			svc.SetModelLister(&plazaListerStub{byGroup: map[int64][]string{10: {"claude-custom"}}})

			out, err := svc.ListGroups(context.Background())
			require.NoError(t, err)
			require.Len(t, out, 1)
			model := plazaModelsByName(out[0].Models)["claude-custom"]
			require.NotNil(t, model.Pricing, "渠道通配符的非 token 定价不能丢成待配置")
			require.Equal(t, tc.mode, model.Pricing.BillingMode)
			require.NotNil(t, model.Pricing.PerRequestPrice)
			require.InDelta(t, 0.05, *model.Pricing.PerRequestPrice, 1e-12)
		})
	}
}

func TestListGroups_ListingModelPricedFromCatalogWithoutChannel(t *testing.T) {
	groups := []Group{{ID: 10, Name: "g", Platform: PlatformOpenAI, RateMultiplier: 1, LongContextPricingEnabled: true}}
	svc := newPlazaServiceWithBilling(nil, groups, map[int64]string{10: PlatformOpenAI},
		newStubPricingServiceFromJSON(t, openAILadderCatalogJSON))
	svc.SetModelLister(&plazaListerStub{byGroup: map[int64][]string{10: {"gpt-5.4", "model-without-any-price"}}})

	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 1)
	models := plazaModelsByName(out[0].Models)
	require.NotNil(t, models["gpt-5.4"].Pricing, "无渠道条目的模型按计费目录给价")
	require.InDelta(t, 2.5e-6, *models["gpt-5.4"].Pricing.InputPrice, 1e-15)
	require.Contains(t, models, "model-without-any-price", "查不到价的模型仍然列出")
}

func TestGatewayListingModelIDsForGroup_NilAndComposite(t *testing.T) {
	var nilSvc *GatewayService
	require.Nil(t, nilSvc.ListingModelIDsForGroup(context.Background(), &Group{ID: 1, Platform: PlatformOpenAI}))
	svc := &GatewayService{}
	require.Nil(t, svc.ListingModelIDsForGroup(context.Background(), nil))
	require.Nil(t, svc.ListingModelIDsForGroup(context.Background(), &Group{ID: 1, Platform: PlatformComposite}))
}

func TestPlazaListingFallbackDiffersFromAllowlistDefaults(t *testing.T) {
	// /v1/models 的两张回落表不同：无可用模型且未开白名单时 Antigravity 落到 Claude 默认清单。
	require.Equal(t, plazaClaudeDefaultModelIDs(), plazaListingFallbackModelIDs(PlatformAntigravity))
	require.NotEqual(t, plazaListingFallbackModelIDs(PlatformAntigravity), plazaDefaultModelIDsForPlatform(PlatformAntigravity))
}

func TestListGroups_HideExclusiveDropsExclusiveGroups(t *testing.T) {
	channels := []Channel{
		plazaPricedChannel(1, "ch-public", []int64{10}, PlatformAnthropic, "claude-sonnet"),
		plazaPricedChannel(2, "ch-exclusive", []int64{20}, PlatformAnthropic, "claude-opus"),
	}
	groups := []Group{
		{ID: 10, Name: "public", Platform: PlatformAnthropic, RateMultiplier: 1},
		{ID: 20, Name: "exclusive-with-channel", Platform: PlatformAnthropic, RateMultiplier: 2, IsExclusive: true},
		{ID: 30, Name: "exclusive-listing-only", Platform: PlatformOpenAI, RateMultiplier: 3, IsExclusive: true},
	}
	lister := &plazaListerStub{byGroup: map[int64][]string{10: {"claude-haiku"}, 30: {"gpt-a"}}}

	svc := newPlazaService(channels, groups, nil)
	svc.SetModelLister(lister)
	svc.SetHideExclusiveGroups(true)
	require.True(t, svc.HidesExclusiveGroups())

	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 1, "专属分组不进广场，无论模型来自渠道还是同源枚举")
	require.Equal(t, int64(10), out[0].ID)
	require.Len(t, out[0].Models, 2)
	require.Equal(t, []int64{10}, lister.calls, "专属分组不做模型枚举")

	// 关闭时保持上游行为：专属分组照常返回，由 handler 按授权裁剪。
	svc.SetHideExclusiveGroups(false)
	out, err = svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 3)

	// 只开隐藏、未注入枚举来源时也生效。
	bare := newPlazaService(channels, groups, nil)
	bare.SetHideExclusiveGroups(true)
	out, err = bare.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 1)

	// 未接入扩展（上游原始构造）时不隐藏。
	require.False(t, newPlazaService(channels, groups, nil).HidesExclusiveGroups())
}

func TestProvideModelPlazaService_HidesExclusiveGroups(t *testing.T) {
	svc := ProvideModelPlazaService(nil, nil, nil, nil, nil, nil, nil)
	require.True(t, svc.HidesExclusiveGroups(), "装配出的广场服务只展示公开分组")
}
