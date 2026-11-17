package service

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/typesafe"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"golang.org/x/sync/singleflight"
)

// 本文件是模型广场「按分组有效模型枚举」的扩展，独立于上游的 model_plaza_service.go：
// 上游文件里只有两个挂点（ModelPlazaService.ext 字段、ListGroups 里的 appendListingModels 调用）。

// PlazaModelLister 给出一个分组对外可见的模型 ID（与 GET /v1/models 同源）。
type PlazaModelLister interface {
	ListingModelIDsForGroup(ctx context.Context, group *Group) []string
}

// modelPlazaExt 承载广场扩展的全部状态，避免往上游结构体里加多个字段。
type modelPlazaExt struct {
	lister PlazaModelLister

	healthSource PlazaHealthSource
	healthMu     sync.Mutex
	healthCache  map[string]plazaHealthCacheEntry
	healthFlight singleflight.Group
	healthTTL    time.Duration
	now          func() time.Time
}

func (s *ModelPlazaService) plazaExt() *modelPlazaExt {
	if s.ext == nil {
		s.ext = &modelPlazaExt{}
	}
	return s.ext
}

// SetModelLister 注入分组模型枚举来源；未注入时广场只展示渠道声明的模型（上游原行为）。
// 仅在装配阶段调用。
func (s *ModelPlazaService) SetModelLister(l PlazaModelLister) {
	if s == nil || l == nil {
		return
	}
	s.plazaExt().lister = l
}

// ProvideModelPlazaService 在上游构造函数之上接入广场扩展（模型枚举 + 健康数据）。
func ProvideModelPlazaService(
	channelRepo ChannelRepository,
	groupRepo GroupRepository,
	pricingService *PricingService,
	billingService *BillingService,
	resolver *ModelPricingResolver,
	gatewayService *GatewayService,
	monitorV2 *ChannelMonitorV2Service,
) *ModelPlazaService {
	svc := NewModelPlazaService(channelRepo, groupRepo, pricingService, billingService, resolver)
	if gatewayService != nil {
		svc.SetModelLister(gatewayService)
	}
	if monitorV2 != nil {
		svc.SetHealthSource(monitorV2)
	}
	return svc
}

// appendListingModels 把各分组经 /v1/models 同源枚举得到、但渠道没有声明的模型追加进广场分组。
// 渠道已声明的条目（含其定价）保持不动；composite 分组不处理。
func (s *ModelPlazaService) appendListingModels(ctx context.Context, groups []Group, byGroup map[int64]*PlazaGroup) {
	if s == nil || s.ext == nil || s.ext.lister == nil {
		return
	}
	for i := range groups {
		g := &groups[i]
		if g.Platform == PlatformComposite {
			continue
		}
		pg, ok := byGroup[g.ID]
		if !ok {
			continue
		}
		ids := s.ext.lister.ListingModelIDsForGroup(ctx, g)
		if len(ids) == 0 {
			continue
		}
		seen := make(map[string]struct{}, len(pg.Models)+len(ids))
		for j := range pg.Models {
			seen[pg.Models[j].Platform+"\x00"+pg.Models[j].Name] = struct{}{}
		}
		for _, id := range ids {
			name := strings.TrimSpace(id)
			if name == "" {
				continue
			}
			key := g.Platform + "\x00" + name
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			pg.Models = append(pg.Models, PlazaModel{
				Name:     name,
				Platform: g.Platform,
				Pricing:  s.listingModelSeedPricing(ctx, g, name),
			})
		}
	}
}

// listingModelSeedPricing 给追加的模型准备初始定价，口径与实际计费一致：
// 先查该分组渠道对这个模型的定价（能命中通配符条目），再走与渠道模型相同的全局兜底。
// 非 token 计费（按次/图片/视频）必须在这里带上，否则 fillDisplayPricing 无从给出价格。
func (s *ModelPlazaService) listingModelSeedPricing(ctx context.Context, g *Group, name string) *ChannelModelPricing {
	if s.resolver != nil {
		if p := s.resolver.lookupChannelPricingNormalized(ctx, g.ID, name); p != nil {
			clone := p.Clone()
			return &clone
		}
	}
	seed := []SupportedModel{{Name: name, Platform: g.Platform}}
	fillGlobalPricingFallback(s.pricingService, seed)
	return seed[0].Pricing
}

// ListingModelIDsForGroup 返回分组对外可见的模型 ID，逐步对应 GatewayHandler.Models 的
// 非固定清单、非 composite 路径。handler 里的辅助函数在本文件保留等价副本（不移动上游代码），
// 由 handler 包的 gateway_models_listing_parity_test.go 对比真实 /v1/models 输出守住一致。
func (s *GatewayService) ListingModelIDsForGroup(ctx context.Context, group *Group) []string {
	if s == nil || group == nil || group.Platform == PlatformComposite {
		return nil
	}
	platform := group.Platform
	groupID := group.ID
	available := s.GetAvailableModels(ctx, &groupID, platform)
	if group.ModelAllowlistEnabled() {
		source := plazaModelListingSource(platform, available, plazaDefaultModelIDsForPlatform(platform))
		return group.ModelAllowlist.FilterForListing(source)
	}
	if len(available) > 0 || (platform == PlatformOpenAI && available != nil) {
		return available
	}
	return plazaListingFallbackModelIDs(platform)
}

// plazaModelListingSource 对应 handler.modelListingSource。
func plazaModelListingSource(platform string, availableModels, fallbackModels []string) []string {
	if platform == PlatformOpenAI && availableModels != nil {
		return availableModels
	}
	if len(availableModels) == 0 {
		return fallbackModels
	}
	if platform == PlatformAnthropic {
		return plazaMergeModelIDs(availableModels, fallbackModels)
	}
	return availableModels
}

// plazaDefaultModelIDsForPlatform 对应 handler.defaultModelIDsForPlatform（不含 composite 分支）。
func plazaDefaultModelIDsForPlatform(platform string) []string {
	switch platform {
	case PlatformOpenAI:
		return openai.DefaultModelIDs()
	case PlatformGemini:
		ids := make([]string, 0, len(geminicli.DefaultModels))
		for _, model := range geminicli.DefaultModels {
			ids = append(ids, model.ID)
		}
		return ids
	case PlatformAntigravity:
		models := antigravity.DefaultModels()
		ids := make([]string, 0, len(models))
		for _, model := range models {
			ids = append(ids, model.ID)
		}
		return ids
	case PlatformAnthropic:
		return claude.DefaultModelIDs()
	case PlatformGrok:
		return xai.DefaultModelIDs()
	case PlatformOpenCodeGo:
		return DefaultOpenCodeGoModelIDs()
	case PlatformTypeSafe:
		return []string{"jev-latest"}
	default:
		return plazaClaudeDefaultModelIDs()
	}
}

// plazaListingFallbackModelIDs 对应 GatewayHandler.Models 末尾「无可用模型」时的回落。
// 注意它与 plazaDefaultModelIDsForPlatform 不是同一张表（Antigravity/OpenCodeGo 落到 Claude 默认清单）。
func plazaListingFallbackModelIDs(platform string) []string {
	switch platform {
	case PlatformOpenAI:
		ids := make([]string, 0, len(openai.DefaultModels))
		for _, model := range openai.DefaultModels {
			ids = append(ids, model.ID)
		}
		return ids
	case PlatformGemini:
		ids := make([]string, 0, len(geminicli.DefaultModels))
		for _, model := range geminicli.DefaultModels {
			ids = append(ids, model.ID)
		}
		return ids
	case PlatformGrok:
		return xai.DefaultModelIDs()
	case PlatformTypeSafe:
		return []string{typesafe.JevLatestModel}
	default:
		return plazaClaudeDefaultModelIDs()
	}
}

func plazaClaudeDefaultModelIDs() []string {
	ids := make([]string, 0, len(claude.DefaultModels))
	for _, model := range claude.DefaultModels {
		ids = append(ids, model.ID)
	}
	return ids
}

// plazaMergeModelIDs 对应 handler.mergeModelIDs。
func plazaMergeModelIDs(primary, secondary []string) []string {
	seen := make(map[string]struct{}, len(primary)+len(secondary))
	merged := make([]string, 0, len(primary)+len(secondary))
	for _, models := range [][]string{primary, secondary} {
		for _, model := range models {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			if _, ok := seen[model]; ok {
				continue
			}
			seen[model] = struct{}{}
			merged = append(merged, model)
		}
	}
	return merged
}
