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

type plazaHealthStoreStub struct {
	rows    []PlazaHourlyRow
	through time.Time
	err     error
	calls   int
	ids     []int64
	start   time.Time
	end     time.Time
	// keepThrough 为 true 时不自动补数据截止时间，用于测试过期与无数据场景。
	keepThrough bool
}

func (s *plazaHealthStoreStub) PlazaHourlyHealth(_ context.Context, ids []int64, start, end time.Time) (*PlazaHourlyHealth, error) {
	s.calls++
	s.ids, s.start, s.end = ids, start, end
	if s.err != nil {
		return nil, s.err
	}
	through := s.through
	if through.IsZero() && !s.keepThrough {
		// 默认模拟聚合正常：数据截止到当前小时的起点（测试里的 now 都在其后几分钟内）。
		through = end.Add(-time.Hour)
	}
	return &PlazaHourlyHealth{DataThrough: through, Rows: s.rows}, nil
}

func newPlazaHealthService(store PlazaHealthStore, now time.Time) *ModelPlazaService {
	svc := NewModelPlazaService(nil, nil, nil, nil, nil)
	svc.SetHealthStore(store)
	svc.ext.now = func() time.Time { return now }
	// 默认把桩里的每一行都当作广场清单内的线路；需要验证清单过滤的用例自行覆盖 listedLines。
	svc.ext.listedLines = func(context.Context) (map[plazaHealthLineKey]struct{}, error) {
		listed := map[plazaHealthLineKey]struct{}{}
		if stub, ok := store.(*plazaHealthStoreStub); ok {
			for _, r := range stub.rows {
				listed[plazaHealthLineKey{r.Platform, r.GroupID, r.Model}] = struct{}{}
			}
		}
		return listed, nil
	}
	return svc
}

func plazaHourRow(platform string, groupID int64, model string, hour time.Time, success, failed int64) PlazaHourlyRow {
	return PlazaHourlyRow{
		Platform: platform, GroupID: groupID, Model: model, Hour: hour,
		Success: success, Failed: failed, TTFTSumMs: success * 1200, TTFTCount: success,
	}
}

func TestPlazaHealth_BuildsWhitelistedLinesFromHoursWithCalls(t *testing.T) {
	now := time.Date(2026, 11, 17, 5, 7, 0, 0, time.UTC)
	end := time.Date(2026, 11, 17, 6, 0, 0, 0, time.UTC)
	current := end.Add(-time.Hour)

	rows := []PlazaHourlyRow{
		// gpt-x：两个不相邻的小时有调用，中间没调用的小时不产生采样。
		plazaHourRow("openai", 3, "gpt-x", current.Add(-5*time.Hour), 80, 20),
		plazaHourRow("openai", 3, "gpt-x", current, 507, 0),
		// 同名模型分布在两个平台、两个分组：名次按模型名合计。
		plazaHourRow("anthropic", 11, "shared", current, 70, 30),
		plazaHourRow("gemini", 20, "shared", current, 555, 0),
		// 只有 1 次调用也计入，没有最低样本数门槛。
		plazaHourRow("openai", 3, "tiny", current, 1, 0),
		// 上次调用在 3 天前：仍有采样，但不参与近 24 小时的名次。
		plazaHourRow("openai", 3, "idle", current.Add(-72*time.Hour), 9, 1),
		plazaHourRow("openai", 3, ChannelMonitorV2OtherModel, current, 9999, 0),
		plazaHourRow("openai", 3, "unknown", current, 9999, 0),
		plazaHourRow("openai", 0, "no-group", current, 9999, 0),
		plazaHourRow("openai", 3, "empty", current, 0, 0),
	}
	store := &plazaHealthStoreStub{rows: rows}
	svc := newPlazaHealthService(store, now)

	got, err := svc.Health(context.Background(), []int64{20, 3, 11, 3})
	require.NoError(t, err)
	require.True(t, got.Available)
	require.Equal(t, []int64{3, 11, 20}, store.ids, "分组范围去重排序后原样下传")
	require.Equal(t, end, store.end, "窗口含当前未走完的小时")
	require.Equal(t, end.Add(-7*24*time.Hour), store.start, "最远回看 7 天")

	lines := map[string]PlazaHealthLine{}
	for _, l := range got.Lines {
		lines[l.Platform+"/"+l.Model] = l
	}
	require.Len(t, got.Lines, 5, "__other__、unknown、无分组与零计数的行被跳过")

	require.Equal(t, 1, *lines["anthropic/shared"].Rank, "shared 合计 655 > gpt-x 607")
	require.Equal(t, 1, *lines["gemini/shared"].Rank, "同名模型各线路同一名次")
	require.Equal(t, 2, *lines["openai/gpt-x"].Rank)
	require.Equal(t, 3, *lines["openai/tiny"].Rank)
	require.Nil(t, lines["openai/idle"].Rank, "近 24 小时无调用的模型没有名次")

	gptx := lines["openai/gpt-x"]
	require.Equal(t, []PlazaHealthSample{
		{T: current.Add(-5 * time.Hour).Unix(), E: 0.2},
		{T: current.Unix(), E: 0},
	}, gptx.Samples, "只有有调用的小时，按时间升序")
	require.InDelta(t, 20.0/607.0, gptx.ErrorRate, 1e-4, "合计失败占比按次数加权")
	require.Equal(t, "healthy", gptx.State)
	require.Equal(t, int64(1200), *gptx.TTFTMs)

	require.Equal(t, "warning", lines["anthropic/shared"].State, "成功率 70% 归波动")
	require.Equal(t, "healthy", lines["openai/tiny"].State)
	require.Len(t, lines["openai/idle"].Samples, 1)

	body, err := json.Marshal(got)
	require.NoError(t, err)
	for _, forbidden := range []string{"request", "token", "rpm", "tpm", "count", "success", "9999", "507", "555"} {
		require.NotContains(t, strings.ToLower(string(body)), forbidden, "返回体不得包含计数类信息")
	}
}

func TestPlazaHealth_IgnoredErrorsCountForRankButNotForSamples(t *testing.T) {
	now := time.Date(2026, 11, 17, 5, 7, 0, 0, time.UTC)
	current := time.Date(2026, 11, 17, 5, 0, 0, 0, time.UTC)
	limited := plazaHourRow("openai", 3, "limited", current, 10, 0)
	limited.Ignored = 500 // 大量被限流：仍是用得最多的模型
	onlyIgnored := PlazaHourlyRow{Platform: "openai", GroupID: 3, Model: "limited", Hour: current.Add(-time.Hour), Ignored: 40}
	ghost := PlazaHourlyRow{Platform: "openai", GroupID: 3, Model: "ghost", Hour: current, Ignored: 7}
	rows := []PlazaHourlyRow{onlyIgnored, limited, plazaHourRow("openai", 3, "steady", current, 100, 0), ghost}

	got, err := newPlazaHealthService(&plazaHealthStoreStub{rows: rows}, now).Health(context.Background(), []int64{3})
	require.NoError(t, err)
	lines := map[string]PlazaHealthLine{}
	for _, l := range got.Lines {
		lines[l.Model] = l
	}
	require.Len(t, got.Lines, 2, "只有被忽略类错误的线路没有采样，不出线路")
	require.Equal(t, 1, *lines["limited"].Rank, "名次按原始调用数：550 > 100")
	require.Equal(t, 2, *lines["steady"].Rank)
	require.Equal(t, map[string]int{"limited": 1, "steady": 2, "ghost": 3}, got.Ranks, "没有线路的模型仍有名次")
	require.Len(t, lines["limited"].Samples, 1, "只有被忽略类错误的小时不产生采样")
	require.Zero(t, lines["limited"].ErrorRate, "被忽略类错误不计入失败")
}

func TestPlazaHealth_ModelWithOnlyIgnoredCallsKeepsItsRank(t *testing.T) {
	now := time.Date(2026, 11, 17, 5, 7, 0, 0, time.UTC)
	current := time.Date(2026, 11, 17, 5, 0, 0, 0, time.UTC)
	rows := []PlazaHourlyRow{
		{Platform: "openai", GroupID: 3, Model: "a", Hour: current, Ignored: 500},
		plazaHourRow("openai", 3, "b", current, 100, 0),
	}
	got, err := newPlazaHealthService(&plazaHealthStoreStub{rows: rows}, now).Health(context.Background(), []int64{3})
	require.NoError(t, err)
	require.Len(t, got.Lines, 1, "a 没有可计采样，没有状态条")
	require.Equal(t, "b", got.Lines[0].Model)
	require.Equal(t, map[string]int{"a": 1, "b": 2}, got.Ranks, "调用量第一的 a 仍排第一")

	body, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(body), "500")
	require.NotContains(t, string(body), "100")
}

func TestPlazaHealth_OnlyReturnsLinesListedInThePlaza(t *testing.T) {
	now := time.Date(2026, 11, 17, 5, 7, 0, 0, time.UTC)
	current := time.Date(2026, 11, 17, 5, 0, 0, 0, time.UTC)
	rows := []PlazaHourlyRow{
		// 已上架但调用全被忽略：没有状态条，仍有名次。
		{Platform: "openai", GroupID: 3, Model: "listed-ignored", Hour: current, Ignored: 50},
		plazaHourRow("openai", 3, "listed", current, 10, 0),
		// 客户端误填的名字 / 未上架的内部部署名：无论是否有可计调用都不能出现。
		{Platform: "openai", GroupID: 3, Model: "secret-internal-deployment", Hour: current, Ignored: 9000},
		plazaHourRow("openai", 3, "typo-model", current, 0, 800),
		// 模型名在清单里，但属于另一个分组、另一个平台的线路。
		plazaHourRow("openai", 4, "listed", current, 700, 0),
		plazaHourRow("gemini", 3, "listed", current, 700, 0),
	}
	svc := newPlazaHealthService(&plazaHealthStoreStub{rows: rows}, now)
	svc.ext.listedLines = func(context.Context) (map[plazaHealthLineKey]struct{}, error) {
		return map[plazaHealthLineKey]struct{}{
			{"openai", 3, "listed"}:         {},
			{"openai", 3, "listed-ignored"}: {},
		}, nil
	}
	got, err := svc.Health(context.Background(), []int64{3, 4})
	require.NoError(t, err)
	require.Len(t, got.Lines, 1)
	require.Equal(t, "listed", got.Lines[0].Model)
	require.Equal(t, int64(3), got.Lines[0].GroupID)
	require.Equal(t, map[string]int{"listed-ignored": 1, "listed": 2}, got.Ranks, "名次只统计清单内的线路")

	body, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(body), "secret-internal-deployment")
	require.NotContains(t, string(body), "typo-model")

	boom := errors.New("listing down")
	svc.ext.listedLines = func(context.Context) (map[plazaHealthLineKey]struct{}, error) { return nil, boom }
	svc.ext.now = func() time.Time { return now.Add(time.Minute) }
	_, err = svc.Health(context.Background(), []int64{3, 4})
	require.ErrorIs(t, err, boom, "拿不到清单时报错，不退化成不过滤")
}

func TestPlazaHealth_ListedLinesComeFromThePlazaListing(t *testing.T) {
	svc := newPlazaService(
		[]Channel{plazaPricedChannel(1, "ch", []int64{10}, PlatformGemini, "gemini-a")},
		[]Group{{ID: 10, Name: "g", Platform: PlatformGemini, RateMultiplier: 1}}, nil)
	listed, err := svc.plazaListedLines(context.Background(), time.Now())
	require.NoError(t, err)
	require.Equal(t, map[plazaHealthLineKey]struct{}{{PlatformGemini, 10, "gemini-a"}: {}}, listed)
}

func TestPlazaHealth_KeepsLatestSamplesOnly(t *testing.T) {
	now := time.Date(2026, 11, 17, 5, 7, 0, 0, time.UTC)
	current := time.Date(2026, 11, 17, 5, 0, 0, 0, time.UTC)
	rows := make([]PlazaHourlyRow, 0, 40)
	// 乱序给出 40 个小时：最早的 16 个全失败，最近的 24 个全成功。
	for i := 39; i >= 0; i-- {
		hour := current.Add(-time.Duration(i) * time.Hour)
		if i >= plazaHealthSampleLimit {
			rows = append(rows, plazaHourRow("openai", 3, "gpt-x", hour, 0, 10))
		} else {
			rows = append(rows, plazaHourRow("openai", 3, "gpt-x", hour, 10, 0))
		}
	}
	rows[0], rows[39] = rows[39], rows[0]
	got, err := newPlazaHealthService(&plazaHealthStoreStub{rows: rows}, now).Health(context.Background(), []int64{3})
	require.NoError(t, err)
	require.Len(t, got.Lines, 1)
	line := got.Lines[0]
	require.Len(t, line.Samples, plazaHealthSampleLimit)
	require.Equal(t, current.Add(-23*time.Hour).Unix(), line.Samples[0].T)
	require.Equal(t, current.Unix(), line.Samples[23].T)
	require.Zero(t, line.ErrorRate, "摘要只统计保留下来的采样")
	require.Equal(t, "healthy", line.State)
}

func TestPlazaHealth_DelistedModelDisappearsWithinTheResultCachePeriod(t *testing.T) {
	now := time.Date(2026, 11, 17, 5, 7, 0, 0, time.UTC)
	current := time.Date(2026, 11, 17, 5, 0, 0, 0, time.UTC)
	store := &plazaHealthStoreStub{rows: []PlazaHourlyRow{
		plazaHourRow("openai", 3, "keep", current, 10, 0),
		plazaHourRow("openai", 3, "delisted", current, 90, 0),
	}}
	svc := newPlazaHealthService(store, now)
	listed := map[plazaHealthLineKey]struct{}{{"openai", 3, "keep"}: {}, {"openai", 3, "delisted"}: {}}
	svc.ext.listedLines = func(context.Context) (map[plazaHealthLineKey]struct{}, error) { return listed, nil }

	got, err := svc.Health(context.Background(), []int64{3})
	require.NoError(t, err)
	require.Len(t, got.Lines, 2)

	// 下架一个模型；清单复用期过后、结果缓存期（30 秒）内再查。
	listed = map[plazaHealthLineKey]struct{}{{"openai", 3, "keep"}: {}}
	svc.ext.now = func() time.Time { return now.Add(plazaListedLinesTTL + time.Second) }
	got, err = svc.Health(context.Background(), []int64{3})
	require.NoError(t, err)
	require.Len(t, got.Lines, 1)
	require.Equal(t, "keep", got.Lines[0].Model)
	require.Equal(t, map[string]int{"keep": 1}, got.Ranks)
}

func TestPlazaHealthRatio_NeverOverstatesSuccess(t *testing.T) {
	cases := []struct {
		failed, total int64
		want          float64
	}{
		{0, 100, 0}, {1000, 10000, 0.1}, {3000, 10000, 0.3}, {100, 100, 1},
		{300, 2999, 0.1001}, // 成功率 89.9967%：不能舍成 0.1（会被画成绿色并显示 90.00%）
		{2700, 8999, 0.3001},
		{1, 1000000, 0.0001}, // 有失败就不为 0
	}
	for _, c := range cases {
		require.Equal(t, c.want, plazaHealthRatio(c.failed, c.total), "%d/%d", c.failed, c.total)
	}
}

func TestPlazaHealthState_Thresholds(t *testing.T) {
	cases := []struct {
		success, failed int64
		want            string
	}{
		{100, 0, "healthy"}, {90, 10, "healthy"}, {899, 101, "warning"},
		{70, 30, "warning"}, {699, 301, "critical"}, {0, 1, "critical"}, {1, 0, "healthy"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, plazaHealthState(c.success, c.failed), "%d/%d", c.success, c.failed)
	}
}

func TestPlazaHealth_CachesPerGroupSet(t *testing.T) {
	now := time.Date(2026, 11, 17, 5, 7, 0, 0, time.UTC)
	store := &plazaHealthStoreStub{}
	svc := newPlazaHealthService(store, now)

	_, err := svc.Health(context.Background(), []int64{1, 2})
	require.NoError(t, err)
	_, err = svc.Health(context.Background(), []int64{2, 1})
	require.NoError(t, err)
	require.Equal(t, 1, store.calls, "同一分组集合在缓存期内只查一次")

	_, err = svc.Health(context.Background(), []int64{1})
	require.NoError(t, err)
	require.Equal(t, 2, store.calls, "不同分组集合不共享缓存")

	svc.ext.now = func() time.Time { return now.Add(31 * time.Second) }
	_, err = svc.Health(context.Background(), []int64{1, 2})
	require.NoError(t, err)
	require.Equal(t, 3, store.calls, "缓存过期后重新查询")
}

func TestPlazaHealth_UnavailableAndErrorPaths(t *testing.T) {
	now := time.Date(2026, 11, 17, 5, 7, 0, 0, time.UTC)

	bare := NewModelPlazaService(nil, nil, nil, nil, nil)
	got, err := bare.Health(context.Background(), []int64{1})
	require.NoError(t, err)
	require.False(t, got.Available, "未注入健康源")
	require.NotNil(t, got.Lines)

	empty := &plazaHealthStoreStub{}
	got, err = newPlazaHealthService(empty, now).Health(context.Background(), nil)
	require.NoError(t, err)
	require.True(t, got.Available)
	require.Empty(t, got.Lines)
	require.Zero(t, empty.calls, "没有可用分组时不查询")

	boom := errors.New("db down")
	failing := &plazaHealthStoreStub{err: boom}
	failingSvc := newPlazaHealthService(failing, now)
	_, err = failingSvc.Health(context.Background(), []int64{1})
	require.ErrorIs(t, err, boom)
	_, err = failingSvc.Health(context.Background(), []int64{1})
	require.ErrorIs(t, err, boom)
	require.Equal(t, 2, failing.calls, "失败结果不缓存")
}

func TestPlazaHealth_HidesStaleAggregation(t *testing.T) {
	now := time.Date(2026, 11, 17, 5, 7, 0, 0, time.UTC)
	rows := []PlazaHourlyRow{plazaHourRow("openai", 3, "gpt-x", now.Truncate(time.Hour), 500, 0)}

	// 聚合停在 3 小时前：旧数据仍是全成功，但不能当作当前状态展示。
	stale := &plazaHealthStoreStub{keepThrough: true, through: now.Add(-3 * time.Hour), rows: rows}
	staleSvc := newPlazaHealthService(stale, now)
	got, err := staleSvc.Health(context.Background(), []int64{3})
	require.NoError(t, err)
	require.False(t, got.Available, "聚合数据过期时不展示健康")
	require.Empty(t, got.Lines)

	// 聚合恢复后立即恢复展示（过期结果不进缓存）。
	stale.through = now.Add(-2 * time.Minute)
	got, err = staleSvc.Health(context.Background(), []int64{3})
	require.NoError(t, err)
	require.True(t, got.Available)
	require.Len(t, got.Lines, 1)
	require.Equal(t, 2, stale.calls)

	// 刚好在容忍范围内仍然展示。
	edge := &plazaHealthStoreStub{keepThrough: true, through: now.Add(-plazaHealthMaxLag), rows: rows}
	got, err = newPlazaHealthService(edge, now).Health(context.Background(), []int64{3})
	require.NoError(t, err)
	require.True(t, got.Available)

	// 还没有任何聚合数据。
	never := &plazaHealthStoreStub{keepThrough: true, rows: rows}
	got, err = newPlazaHealthService(never, now).Health(context.Background(), []int64{3})
	require.NoError(t, err)
	require.False(t, got.Available)
}
