//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseOpsAlertRuleErrorFilter(t *testing.T) {
	t.Parallel()

	// JSON 反序列化后数字是 float64,同时兼容字符串数字;畸形值忽略
	out := parseOpsAlertRuleErrorFilter(map[string]any{
		"status_codes":             []any{float64(502), "529", true, float64(-1)},
		"error_types":              []any{"overloaded_error", "", 42},
		"error_phase":              " upstream ",
		"error_owner":              "provider",
		"include_business_limited": true,
	})
	require.Equal(t, []int{502, 529}, out.StatusCodes)
	require.Equal(t, []string{"overloaded_error"}, out.ErrorTypes)
	require.Equal(t, "upstream", out.ErrorPhase)
	require.Equal(t, "provider", out.ErrorOwner)
	require.True(t, out.IncludeBusinessLimited)

	// nil / 空 map / 类型完全不符 → 零值
	require.Equal(t, opsAlertRuleErrorFilter{}, parseOpsAlertRuleErrorFilter(nil))
	require.Equal(t, opsAlertRuleErrorFilter{}, parseOpsAlertRuleErrorFilter(map[string]any{
		"status_codes": "not-an-array",
		"error_phase":  123,
	}))
}

func TestComputeRuleMetricErrorCount(t *testing.T) {
	t.Parallel()

	var gotFilter *OpsErrorLogFilter
	mock := &opsRepoMock{
		CountErrorLogsFn: func(ctx context.Context, filter *OpsErrorLogFilter) (int64, error) {
			gotFilter = filter
			return 17, nil
		},
	}
	svc := &OpsAlertEvaluatorService{opsRepo: mock}

	start := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	groupID := int64(3)
	rule := &OpsAlertRule{
		ID: 1, MetricType: "error_count", Operator: ">", Threshold: 10,
		Filters: map[string]any{
			"platform":     "anthropic",
			"status_codes": []any{float64(502)},
			"error_owner":  "provider",
		},
	}

	value, ok := svc.computeRuleMetric(context.Background(), rule, nil, start, end, "anthropic", &groupID)
	require.True(t, ok)
	require.Equal(t, 17.0, value)

	require.NotNil(t, gotFilter)
	require.Equal(t, start, *gotFilter.StartTime)
	require.Equal(t, end, *gotFilter.EndTime)
	require.Equal(t, "anthropic", gotFilter.Platform)
	require.Equal(t, int64(3), *gotFilter.GroupID)
	require.Equal(t, []int{502}, gotFilter.StatusCodes)
	require.Equal(t, "provider", gotFilter.Owner)
	// 默认排除业务限制错误(View 为空 = errors 口径)
	require.Equal(t, "", gotFilter.View)

	// include_business_limited=true → View=all
	rule.Filters["include_business_limited"] = true
	_, ok = svc.computeRuleMetric(context.Background(), rule, nil, start, end, "anthropic", &groupID)
	require.True(t, ok)
	require.Equal(t, "all", gotFilter.View)
}

func TestComputeRuleMetricErrorCountRepoError(t *testing.T) {
	t.Parallel()
	mock := &opsRepoMock{
		CountErrorLogsFn: func(ctx context.Context, filter *OpsErrorLogFilter) (int64, error) {
			return 0, context.DeadlineExceeded
		},
	}
	svc := &OpsAlertEvaluatorService{opsRepo: mock}
	rule := &OpsAlertRule{ID: 1, MetricType: "error_count", Operator: ">", Threshold: 10}
	_, ok := svc.computeRuleMetric(context.Background(), rule, nil, time.Now().Add(-time.Minute), time.Now(), "", nil)
	require.False(t, ok)
}
