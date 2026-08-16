package service

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 网关热路径上的 apiKey.Group **不是**从库里读的，而是从 Redis 里的鉴权快照
// （APIKeyAuthGroupSnapshot）物化出来的（见 snapshotToAPIKey）。而这个快照的字段是
// 一个个手写列举的 —— 于是「Group 上加了新字段但忘了往快照里加」不会报错、不会编译失败，
// 只会让该字段在网关侧恒为零值。
//
// 这个坑刚踩过一次：上游 v0.1.177 给 Group 加了 LongContextPricingEnabled 与
// ModelPricing（分组逐模型定价），但它自己的快照里 0 命中，结果是
//   - 管理员在分组里配的逐模型价在计费时完全读不到（等于没配）
//   - model_pricing_resolver.go 判 longContextPricingEnabled 恒 false
//     → 渠道区间定价被压平到最低档、长上下文阶梯静默失效
//
// 两条都直接算错钱，且不报错。
//
// 所以这里不断言「快照必须含 Group 的全部字段」（有些字段确实不该进），
// 而是要求**刻意不进快照的字段必须显式登记**。新增字段时这条会红，
// 强制有人做一次决定：要么投影它，要么写进下面的清单并说明为什么不需要。
func TestEveryGroupFieldIsEitherProjectedOrExplicitlyExcluded(t *testing.T) {
	// key = Group 的字段名，value = 不进快照的理由。
	excluded := map[string]string{
		"Description":                  "纯展示文案，网关链路不读",
		"Hydrated":                     "加载状态标志，快照本身即已 hydrate",
		"DuplicateOperationID":         "仅管理端去重用，不参与网关判定与计费",
		"DefaultValidityDays":          "建密钥时用一次，网关链路不读",
		"BatchImageDiscountMultiplier": "批量出图任务在自己的服务里查库，不走鉴权快照",
		"BatchImageHoldMultiplier":     "同上",
		"SortOrder":                    "列表排序，纯展示",
		"RequireOAuthOnly":             "建号/绑定期校验，不在网关热路径",
		"RequirePrivacySet":            "同上",
		"CreatedAt":                    "元数据，网关链路不读",
		"UpdatedAt":                    "元数据，网关链路不读",
		"AccountGroups":                "关联实体，快照要保持小；调度另有自己的账号查询",
		"AccountCount":                 "统计派生值，会过期，不该进缓存",
		"ActiveAccountCount":           "同上",
		"RateLimitedAccountCount":      "同上",
	}

	groupFields := exportedFieldNames(reflect.TypeOf(Group{}))
	snapshotFields := map[string]struct{}{}
	for _, f := range exportedFieldNames(reflect.TypeOf(APIKeyAuthGroupSnapshot{})) {
		snapshotFields[f] = struct{}{}
	}

	var unaccounted []string
	for _, f := range groupFields {
		if _, ok := snapshotFields[f]; ok {
			continue
		}
		if _, ok := excluded[f]; ok {
			continue
		}
		unaccounted = append(unaccounted, f)
	}
	sort.Strings(unaccounted)
	require.Empty(t, unaccounted,
		"Group 新增了字段，但既没进鉴权快照、也没登记为「刻意不进」：\n  %s\n\n"+
			"网关热路径的 apiKey.Group 是从快照物化的，漏投影的字段在那里恒为零值且不报错。\n"+
			"请二选一：(a) 加进 APIKeyAuthGroupSnapshot + snapshotFromAPIKey + snapshotToAPIKey\n"+
			"三处，并 bump apiKeyAuthSnapshotVersion；(b) 加进本用例的 excluded 清单并写明理由。",
		strings.Join(unaccounted, "\n  "))

	// 反向：清单里登记的字段必须真的还在 Group 上，否则是过期条目。
	var stale []string
	groupSet := map[string]struct{}{}
	for _, f := range groupFields {
		groupSet[f] = struct{}{}
	}
	for f := range excluded {
		if _, ok := groupSet[f]; !ok {
			stale = append(stale, f)
		}
		if _, ok := snapshotFields[f]; ok {
			stale = append(stale, f+"（已进快照，应从清单移除）")
		}
	}
	sort.Strings(stale)
	require.Empty(t, stale, "excluded 清单里的过期条目：\n  %s", strings.Join(stale, "\n  "))
}

// 分组逐模型定价必须扛过完整的 L2 JSON 往返。
//
// 断言选值刻意避开零值：LongContextPricingEnabled 用 true（漏投影时得到 false）、
// ModelPricing 用非空切片（漏投影时得到 nil）。用 false / 空切片的话，
// 「投影了」与「没投影」产出同一个结果，这条用例就成了空断言。
func TestAPIKeyAuthSnapshotModelPricingRoundtrip(t *testing.T) {
	svc := &APIKeyService{}
	apiKey := profitAuthTestAPIKey()
	price := 3.5
	apiKey.Group.LongContextPricingEnabled = true
	apiKey.Group.ModelPricing = []ChannelModelPricing{{
		Platform:    PlatformOpenAI,
		Models:      []string{"gpt-image-2"},
		BillingMode: BillingModeToken,
		InputPrice:  &price,
	}}

	snapshot := svc.snapshotFromAPIKey(context.Background(), apiKey)
	require.NotNil(t, snapshot)
	require.True(t, snapshot.Group.LongContextPricingEnabled,
		"投影阶段就丢了 —— snapshotFromAPIKey 里缺这一行")
	require.Len(t, snapshot.Group.ModelPricing, 1, "投影阶段就丢了 ModelPricing")

	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: snapshot})
	require.NoError(t, err)
	var decoded APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(payload, &decoded))

	restored := svc.snapshotToAPIKey("k", decoded.Snapshot)
	require.NotNil(t, restored)
	require.NotNil(t, restored.Group)
	require.True(t, restored.Group.LongContextPricingEnabled,
		"还原阶段丢了 —— snapshotToAPIKey 里缺这一行；后果是区间定价被压平到最低档")
	require.Len(t, restored.Group.ModelPricing, 1,
		"还原阶段丢了 ModelPricing —— 后果是分组逐模型价在计费时读不到，等于没配")
	require.Equal(t, []string{"gpt-image-2"}, restored.Group.ModelPricing[0].Models)
	require.NotNil(t, restored.Group.ModelPricing[0].InputPrice)
	require.InDelta(t, 3.5, *restored.Group.ModelPricing[0].InputPrice, 1e-9,
		"价格数值必须原样保真，不能只是切片长度对得上")
}

func exportedFieldNames(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // 非导出
		}
		out = append(out, f.Name)
	}
	return out
}
