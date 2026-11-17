//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type plazaHealthSourceStub struct {
	matrix  *ChannelMonitorV2Matrix
	err     error
	calls   int
	filter  ChannelMonitorV2Filter
	groupBy ChannelMonitorV2GroupBy
	admin   bool
	// keepCoverage 为 true 时不自动补 Coverage，用于测试过期与无数据场景。
	keepCoverage bool
}

func (s *plazaHealthSourceStub) Matrix(_ context.Context, filter ChannelMonitorV2Filter, groupBy ChannelMonitorV2GroupBy, admin bool) (*ChannelMonitorV2Matrix, error) {
	s.calls++
	s.filter, s.groupBy, s.admin = filter, groupBy, admin
	if s.matrix != nil && s.matrix.Coverage.DataThrough.IsZero() && !s.keepCoverage {
		// 默认模拟聚合正常：数据截止到当前桶之前一分钟。
		s.matrix.Coverage.DataThrough = filter.End.Add(-plazaHealthBucket).Add(-time.Minute)
	}
	return s.matrix, s.err
}

func newPlazaHealthService(src PlazaHealthSource, now time.Time) *ModelPlazaService {
	svc := NewModelPlazaService(nil, nil, nil, nil, nil)
	svc.SetHealthSource(src)
	svc.ext.now = func() time.Time { return now }
	return svc
}

func plazaHealthRow(platform string, groupID int64, model string, requests int64, errorRate float64, state string) ChannelMonitorV2MatrixRow {
	gid := groupID
	p50 := int64(1200)
	return ChannelMonitorV2MatrixRow{
		Platform: platform, GroupID: &gid, GroupName: "g", Model: model,
		Metrics: ChannelMonitorV2Metric{
			RequestCount: requests, SuccessRequests: requests, InputTokens: 999, RPM: 12, TPM: 3456,
			ErrorRate: errorRate, TTFT: ChannelMonitorV2Latency{SampleCount: requests, P50Ms: &p50},
		},
		Health: ChannelMonitorV2Health{Overall: "critical", ErrorRate: state},
	}
}

func TestPlazaHealth_BuildsWhitelistedLinesWithRankAndAlignedBuckets(t *testing.T) {
	now := time.Date(2026, 11, 17, 5, 7, 0, 0, time.UTC)
	end := time.Date(2026, 11, 17, 5, 20, 0, 0, time.UTC)
	start := end.Add(-24 * time.Hour)

	busy := plazaHealthRow("openai", 3, "gpt-x", 500, 0.02, "healthy")
	busy.Buckets = []ChannelMonitorV2TrendPoint{
		{BucketStart: start, Metrics: ChannelMonitorV2Metric{RequestCount: 80, ErrorRate: 0.5}, Health: ChannelMonitorV2Health{ErrorRate: "critical"}},
		{BucketStart: start.Add(71 * 20 * time.Minute), Metrics: ChannelMonitorV2Metric{RequestCount: 3, ErrorRate: 0}, Health: ChannelMonitorV2Health{ErrorRate: "unknown"}},
		{BucketStart: start.Add(7 * time.Minute), Metrics: ChannelMonitorV2Metric{ErrorRate: 1}, Health: ChannelMonitorV2Health{ErrorRate: "critical"}},
		{BucketStart: end, Metrics: ChannelMonitorV2Metric{ErrorRate: 1}, Health: ChannelMonitorV2Health{ErrorRate: "critical"}},
	}
	// 同名模型分布在两个平台、两个分组：名次按模型名合计。
	sameNameA := plazaHealthRow("anthropic", 11, "shared", 100, 0.3, "critical")
	sameNameB := plazaHealthRow("gemini", 20, "shared", 450, 0.0, "healthy")
	quiet := plazaHealthRow("openai", 3, "quiet", 0, 0, "unknown")
	other := plazaHealthRow("openai", 3, ChannelMonitorV2OtherModel, 9999, 0, "healthy")
	noGroup := plazaHealthRow("openai", 3, "no-group", 9999, 0, "healthy")
	noGroup.GroupID = nil

	src := &plazaHealthSourceStub{matrix: &ChannelMonitorV2Matrix{Items: []ChannelMonitorV2MatrixRow{busy, sameNameA, sameNameB, quiet, other, noGroup}}}
	svc := newPlazaHealthService(src, now)

	got, err := svc.Health(context.Background(), []int64{20, 3, 11, 3})
	require.NoError(t, err)
	require.True(t, got.Available)
	require.Equal(t, start, got.Window.Start)
	require.Equal(t, end, got.Window.End)
	require.Equal(t, 1200, got.Window.BucketSeconds)

	require.True(t, src.admin, "取未脱敏数据用于排名")
	require.Equal(t, ChannelMonitorV2GroupByPlatformGroupModel, src.groupBy)
	require.True(t, src.filter.RestrictGroups, "分组范围必须受限")
	require.Equal(t, []int64{3, 11, 20}, src.filter.AllowedGroupIDs)
	require.Equal(t, 20*time.Minute, src.filter.Bucket)
	require.Equal(t, start, src.filter.Start)
	require.Equal(t, end, src.filter.End)

	lines := map[string]PlazaHealthLine{}
	for _, l := range got.Lines {
		lines[l.Platform+"/"+l.Model] = l
	}
	require.Len(t, got.Lines, 4, "__other__ 与无分组的行被跳过")

	require.Equal(t, 1, *lines["anthropic/shared"].Rank, "shared 合计 550 > gpt-x 500")
	require.Equal(t, 1, *lines["gemini/shared"].Rank, "同名模型各线路同一名次")
	require.Equal(t, 2, *lines["openai/gpt-x"].Rank)
	require.Nil(t, lines["openai/quiet"].Rank, "无请求的模型没有名次")

	require.Equal(t, "critical", lines["anthropic/shared"].State, "状态只取错误率状态，不取综合状态")
	require.InDelta(t, 0.3, *lines["anthropic/shared"].ErrorRate, 1e-12)
	require.Equal(t, "unknown", lines["openai/quiet"].State)
	require.Nil(t, lines["openai/quiet"].ErrorRate, "样本不足时不给错误率")
	require.Equal(t, int64(1200), *lines["openai/gpt-x"].TTFTP50Ms)

	buckets := lines["openai/gpt-x"].Buckets
	require.Len(t, buckets, 72)
	require.Equal(t, "critical", buckets[0].State)
	require.InDelta(t, 0.5, *buckets[0].ErrorRate, 1e-12)
	require.Equal(t, "unknown", buckets[71].State)
	require.Nil(t, buckets[71].ErrorRate)
	for i := 1; i < 71; i++ {
		require.Equal(t, "unknown", buckets[i].State, "缺失或未对齐的桶补 unknown")
	}

	body, err := json.Marshal(got)
	require.NoError(t, err)
	for _, forbidden := range []string{"request", "token", "rpm", "tpm", "sample", "500", "9999", "3456"} {
		require.NotContains(t, strings.ToLower(string(body)), forbidden, "返回体不得包含计数类信息")
	}
}

func TestPlazaHealth_CachesPerGroupSet(t *testing.T) {
	now := time.Date(2026, 11, 17, 5, 7, 0, 0, time.UTC)
	src := &plazaHealthSourceStub{matrix: &ChannelMonitorV2Matrix{}}
	svc := newPlazaHealthService(src, now)

	_, err := svc.Health(context.Background(), []int64{1, 2})
	require.NoError(t, err)
	_, err = svc.Health(context.Background(), []int64{2, 1})
	require.NoError(t, err)
	require.Equal(t, 1, src.calls, "同一分组集合在缓存期内只查一次")

	_, err = svc.Health(context.Background(), []int64{1})
	require.NoError(t, err)
	require.Equal(t, 2, src.calls, "不同分组集合不共享缓存")

	svc.ext.now = func() time.Time { return now.Add(31 * time.Second) }
	_, err = svc.Health(context.Background(), []int64{1, 2})
	require.NoError(t, err)
	require.Equal(t, 3, src.calls, "缓存过期后重新查询")
}

func TestPlazaHealth_UnavailableAndErrorPaths(t *testing.T) {
	now := time.Date(2026, 11, 17, 5, 7, 0, 0, time.UTC)

	bare := NewModelPlazaService(nil, nil, nil, nil, nil)
	got, err := bare.Health(context.Background(), []int64{1})
	require.NoError(t, err)
	require.False(t, got.Available, "未注入健康源")
	require.NotNil(t, got.Lines)

	disabled := &plazaHealthSourceStub{err: ErrChannelMonitorDisabled}
	got, err = newPlazaHealthService(disabled, now).Health(context.Background(), []int64{1})
	require.NoError(t, err)
	require.False(t, got.Available, "监控未启用")

	empty := &plazaHealthSourceStub{matrix: &ChannelMonitorV2Matrix{}}
	got, err = newPlazaHealthService(empty, now).Health(context.Background(), nil)
	require.NoError(t, err)
	require.True(t, got.Available)
	require.Empty(t, got.Lines)
	require.Zero(t, empty.calls, "没有可用分组时不查询")

	boom := errors.New("db down")
	failing := &plazaHealthSourceStub{err: boom}
	failingSvc := newPlazaHealthService(failing, now)
	_, err = failingSvc.Health(context.Background(), []int64{1})
	require.ErrorIs(t, err, boom)
	_, err = failingSvc.Health(context.Background(), []int64{1})
	require.ErrorIs(t, err, boom)
	require.Equal(t, 2, failing.calls, "失败结果不缓存")
}

func TestPlazaHealth_HidesStaleAggregation(t *testing.T) {
	now := time.Date(2026, 11, 17, 5, 7, 0, 0, time.UTC)
	healthyRow := plazaHealthRow("openai", 3, "gpt-x", 500, 0, "healthy")

	// 聚合停在 3 小时前：旧行仍是 healthy，但不能当作当前状态展示。
	stale := &plazaHealthSourceStub{keepCoverage: true, matrix: &ChannelMonitorV2Matrix{
		Coverage: ChannelMonitorV2Coverage{DataThrough: now.Add(-3 * time.Hour)},
		Items:    []ChannelMonitorV2MatrixRow{healthyRow},
	}}
	staleSvc := newPlazaHealthService(stale, now)
	got, err := staleSvc.Health(context.Background(), []int64{3})
	require.NoError(t, err)
	require.False(t, got.Available, "聚合数据过期时不展示健康")
	require.Empty(t, got.Lines)

	// 聚合恢复后立即恢复展示（过期结果不进缓存）。
	stale.matrix.Coverage.DataThrough = now.Add(-2 * time.Minute)
	got, err = staleSvc.Health(context.Background(), []int64{3})
	require.NoError(t, err)
	require.True(t, got.Available)
	require.Len(t, got.Lines, 1)
	require.Equal(t, 2, stale.calls)

	// 刚好在容忍范围内仍然展示。
	edge := &plazaHealthSourceStub{keepCoverage: true, matrix: &ChannelMonitorV2Matrix{
		Coverage: ChannelMonitorV2Coverage{DataThrough: now.Add(-plazaHealthMaxLag)},
		Items:    []ChannelMonitorV2MatrixRow{healthyRow},
	}}
	got, err = newPlazaHealthService(edge, now).Health(context.Background(), []int64{3})
	require.NoError(t, err)
	require.True(t, got.Available)

	// 还没有任何聚合数据（Coverage 为零值）。
	never := &plazaHealthSourceStub{keepCoverage: true, matrix: &ChannelMonitorV2Matrix{Items: []ChannelMonitorV2MatrixRow{healthyRow}}}
	got, err = newPlazaHealthService(never, now).Health(context.Background(), []int64{3})
	require.NoError(t, err)
	require.False(t, got.Available)
}
