package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// ImageCount 在两条链路上都不诚实（Gemini 按模型名兜底成 1、OpenAI 以 parsed.N 起步），
// 所以免单判定必须只认 ImageOutputsObserved，且严格区分 nil 与 0。
func TestForwardResultIsEmptyImageResponse(t *testing.T) {
	tests := []struct {
		name   string
		result *ForwardResult
		want   bool
	}{
		{name: "nil result", result: nil, want: false},
		{
			name:   "观测到 0 张：这才是空返回",
			result: &ForwardResult{ImageCount: 1, ImageOutputsObserved: intPtr(0)},
			want:   true,
		},
		{
			name:   "观测不到：宁可收费也不能凭猜测免单",
			result: &ForwardResult{ImageCount: 1, ImageOutputsObserved: nil},
			want:   false,
		},
		{
			name:   "真回了图",
			result: &ForwardResult{ImageCount: 1, ImageOutputsObserved: intPtr(1)},
			want:   false,
		},
		{
			name:   "非生图请求：ImageCount 为 0 时无所谓空不空",
			result: &ForwardResult{ImageCount: 0, ImageOutputsObserved: intPtr(0)},
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.result.IsEmptyImageResponse())
		})
	}
}

func TestOpenAIForwardResultIsEmptyImageResponse(t *testing.T) {
	require.True(t, (&OpenAIForwardResult{ImageCount: 2, ImageOutputsObserved: intPtr(0)}).IsEmptyImageResponse())
	require.False(t, (&OpenAIForwardResult{ImageCount: 2, ImageOutputsObserved: nil}).IsEmptyImageResponse())
	require.False(t, (&OpenAIForwardResult{ImageCount: 2, ImageOutputsObserved: intPtr(2)}).IsEmptyImageResponse())
	require.False(t, (*OpenAIForwardResult)(nil).IsEmptyImageResponse())
}

func TestMatchEmptyResponseBillingRule(t *testing.T) {
	rules := []EmptyResponseBillingRule{
		{ID: 1, GroupID: int64Ptr(12), Model: "gemini-3.1-flash-image", Enabled: true},
		{ID: 2, GroupID: int64Ptr(21), Model: "", Enabled: true},
		{ID: 3, GroupID: nil, Model: "gpt-image-2", Enabled: true},
		{ID: 4, GroupID: int64Ptr(99), Model: "", Enabled: false},
	}

	tests := []struct {
		name    string
		groupID *int64
		models  []string
		wantID  int64
	}{
		{name: "分组+模型都命中", groupID: int64Ptr(12), models: []string{"gemini-3.1-flash-image"}, wantID: 1},
		{name: "模型名大小写与空白不敏感", groupID: int64Ptr(12), models: []string{"  GEMINI-3.1-Flash-Image "}, wantID: 1},
		{name: "分组对但模型不对", groupID: int64Ptr(12), models: []string{"gemini-3-pro-image"}, wantID: 0},
		{name: "空模型规则覆盖该分组全部模型", groupID: int64Ptr(21), models: []string{"gemini-3-pro-image"}, wantID: 2},
		{name: "空分组规则跨分组生效", groupID: int64Ptr(777), models: []string{"gpt-image-2"}, wantID: 3},
		{name: "已停用的规则不生效", groupID: int64Ptr(99), models: []string{"any"}, wantID: 0},
		{name: "无分组上下文时不命中限定分组的规则", groupID: nil, models: []string{"gemini-3.1-flash-image"}, wantID: 0},
		{name: "候选模型任一命中即可", groupID: int64Ptr(12), models: []string{"nana-banana-2", "gemini-3.1-flash-image"}, wantID: 1},
		{name: "无规则集", groupID: int64Ptr(12), models: nil, wantID: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchEmptyResponseBillingRule(rules, tt.groupID, tt.models)
			if tt.wantID == 0 {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, tt.wantID, got.ID)
		})
	}
}

func TestWaiveCostForEmptyResponse(t *testing.T) {
	cost := &CostBreakdown{
		InputCost: 0.01, ImageInputCost: 0.02, OutputCost: 0.03, ImageOutputCost: 0.04,
		CacheCreationCost: 0.05, CacheReadCost: 0.06, TotalCost: 0.21, ActualCost: 0.15,
		BillingMode: string(BillingModeToken), LongContextBillingApplied: true,
	}
	waived := waiveCostForEmptyResponse(cost)

	require.Zero(t, waived.InputCost)
	require.Zero(t, waived.ImageInputCost)
	require.Zero(t, waived.OutputCost)
	require.Zero(t, waived.ImageOutputCost)
	require.Zero(t, waived.CacheCreationCost)
	require.Zero(t, waived.CacheReadCost)
	require.Zero(t, waived.TotalCost)
	require.Zero(t, waived.ActualCost)
	// BillingMode 要留着：用量行仍需表达「本来按什么模式计费」。
	require.Equal(t, string(BillingModeToken), waived.BillingMode)
	require.True(t, waived.LongContextBillingApplied)
	// 不得就地改写调用方的 cost —— 日志里还要报被免掉多少钱。
	require.Equal(t, 0.15, cost.ActualCost)

	require.NotNil(t, waiveCostForEmptyResponse(nil))
}

func TestResolveEmptyResponseBillingWaiver(t *testing.T) {
	rules := []EmptyResponseBillingRule{{ID: 7, GroupID: int64Ptr(12), Enabled: true}}
	cost := &CostBreakdown{ActualCost: 0.15}

	waiver := resolveEmptyResponseBillingWaiver(rules, true, int64Ptr(12), []string{"m"}, cost)
	require.True(t, waiver.Applied)
	require.Equal(t, int64(7), waiver.RuleID)
	require.Equal(t, 0.15, waiver.WaivedCost)

	require.False(t, resolveEmptyResponseBillingWaiver(rules, false, int64Ptr(12), []string{"m"}, cost).Applied)
	require.False(t, resolveEmptyResponseBillingWaiver(nil, true, int64Ptr(12), []string{"m"}, cost).Applied)
	require.False(t, resolveEmptyResponseBillingWaiver(rules, true, int64Ptr(21), []string{"m"}, cost).Applied)
}

type stubEmptyResponseBillingRepo struct {
	rules []EmptyResponseBillingRule
	err   error
	calls int
}

func (s *stubEmptyResponseBillingRepo) ListEnabledByUserID(context.Context, int64) ([]EmptyResponseBillingRule, error) {
	s.calls++
	return s.rules, s.err
}

func TestUserEmptyResponseBillingResolverReadsFreshRulesAndFailsClosed(t *testing.T) {
	repo := &stubEmptyResponseBillingRepo{rules: []EmptyResponseBillingRule{{ID: 1, Enabled: true}}}
	resolver := newUserEmptyResponseBillingResolver(repo, "test")

	require.Len(t, resolver.Resolve(context.Background(), 42), 1)
	require.Len(t, resolver.Resolve(context.Background(), 42), 1)
	require.Equal(t, 2, repo.calls, "计费规则必须跨实例读取到数据库最新值")

	// 读不到规则时返回 nil = 照常收费。错误地免费直接漏收入，错误地收费可人工退。
	failing := &stubEmptyResponseBillingRepo{err: errors.New("db down")}
	require.Nil(t, newUserEmptyResponseBillingResolver(failing, "test").Resolve(context.Background(), 42))
}

func requireEmptyResponseWaiverAudit(t *testing.T, log *UsageLog, ruleID int64, waivedCost float64) {
	t.Helper()
	require.NotNil(t, log)
	require.True(t, log.EmptyResponseBillingWaived)
	require.Equal(t, ruleID, log.EmptyResponseBillingRuleID)
	require.InDelta(t, waivedCost, log.EmptyResponseWaivedCost, 1e-12)
}
