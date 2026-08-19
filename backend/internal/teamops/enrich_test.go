//go:build unit

package teamops

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---------- AvgCost / CacheHitRate（finalizeRows 里算，与概览条共用算法） ----------

// AvgCost 必须由**分配前**的金额算出。
//
// 选例避开不动点：CurrentCost 取 10.004，AllocateDisplay 会把它取整成 10.00，
// 于是「用分配前」得 5.002、「用分配后」得 5.000 —— 两条路径的结果不同，
// 断言才能把它们分开。取 10.00 这种恰好取整不变的数，两种实现同解，等于没测。
func TestFinalizeRows_AvgCostUsesPreAllocationCost(t *testing.T) {
	t.Parallel()
	rows := []Row{{CurrentCost: 10.004, Requests: 2}}

	finalizeRows(rows, false)

	require.InDelta(t, 10.00, rows[0].CurrentCost, 1e-9, "前提：分配会把这一行改写成 10.00")
	require.InDelta(t, 5.002, rows[0].AvgCost, 1e-9,
		"平均单次用的是分配前金额；用分配后的会得到 5.000")
}

// 请求数为 0 时不能做除法：0 除得到的是 NaN，而 NaN 在 encoding/json 里
// 直接让整个响应序列化失败——页面白屏，日志里只有一句 "json: unsupported value"。
func TestFinalizeRows_AvgCostIsZeroWhenNoRequests(t *testing.T) {
	t.Parallel()
	rows := []Row{{CurrentCost: 5, Requests: 0}}

	finalizeRows(rows, false)

	require.Zero(t, rows[0].AvgCost)
	require.False(t, math.IsNaN(rows[0].AvgCost) || math.IsInf(rows[0].AvgCost, 0))
	_, err := json.Marshal(rows)
	require.NoError(t, err, "NaN / ±Inf 会让整个 /rows 响应序列化失败")
}

// 命中率是 token 口径：100 * cache_read / (input + cache_creation + cache_read)。
//
// 300 / 200 / 100 这组三类 token 全不相等，所以漏掉缓存写入、分子分母写反、
// 忘了乘 100 等变异都会算出不同的数。
func TestFinalizeRows_CacheHitRateIsTokenWeighted(t *testing.T) {
	t.Parallel()
	rows := []Row{{InputTokens: 300, CacheCreationTokens: 200, CacheReadTokens: 100}}

	finalizeRows(rows, false)

	require.NotNil(t, rows[0].CacheHitRate)
	require.InDelta(t, 100.0/6.0, *rows[0].CacheHitRate, 1e-9)
}

// 「有输入 token 但一次都没命中」必须是 0%，不是「—」。
// 这条钉的是认输判据取的是**分母**而不是分子：写成 `if cacheRead == 0 { return nil }`
// 的实现在这里会返回 nil。
func TestFinalizeRows_CacheHitRateIsZeroWhenInputTokensNeverHitCache(t *testing.T) {
	t.Parallel()
	rows := []Row{{InputTokens: 400, CacheReadTokens: 0}}

	finalizeRows(rows, false)

	require.NotNil(t, rows[0].CacheHitRate, "分母不为零就有命中率，哪怕是 0%")
	require.Zero(t, *rows[0].CacheHitRate)
}

// 分母为 0 时必须 nil：「本期没有任何输入 token」与「命中率 0%」是两回事。
func TestFinalizeRows_CacheHitRateNilWhenNoTokensAtAll(t *testing.T) {
	t.Parallel()
	rows := []Row{{CurrentCost: 12, Requests: 3}}

	finalizeRows(rows, false)

	require.Nil(t, rows[0].CacheHitRate)
}

// ---------- SummaryDTO 上的新字段 ----------

func TestSummary_ExposesRequestsActiveRowsAndCacheMetrics(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{summary: Summary{
		TotalCost: 100, PrevCost: 80,
		RowCount: 8, ActiveRowCount: 6,
		Requests: 4200, PrevRequests: 1700,
		InputTokens: 700, CacheReadTokens: 300,
		CacheSavedCost: 12.345,
	}}
	s := newTestService(repo, 90, fixedNow)

	dto, err := s.Summary(context.Background(), 1, "2026-08-01", "2026-08-15", "UTC")
	require.NoError(t, err)

	require.EqualValues(t, 4200, dto.Requests)
	require.EqualValues(t, 1700, dto.PrevRequests)
	// 6/8 两个数不相等，所以「活跃数直接取 RowCount」这种变异会红。
	require.Equal(t, 6, dto.ActiveRowCount)
	require.Equal(t, 8, dto.RowCount)
	require.NotNil(t, dto.CacheHitRate)
	require.InDelta(t, 30.0, *dto.CacheHitRate, 1e-9, "700/300 分子分母不等，写反会得到 70")
	// 不取整：roundCents 会把 12.345 变成 12.35。缓存节省没有任何恒等式要维持，
	// 取整只会把不足一分的节省显示成 0.00。
	require.InDelta(t, 12.345, dto.CacheSavedCost, 1e-12)
}

// 缓存节省为负说明这个分组的缓存读单价高于输入单价（定价配错了），
// clamp 到 0 会把它伪装成正常——而这正是这个数字唯一能帮上忙的场景。
func TestSummary_CacheSavedCostKeepsNegativeValue(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{summary: Summary{CacheSavedCost: -3.25}}
	s := newTestService(repo, 90, fixedNow)

	dto, err := s.Summary(context.Background(), 1, "2026-08-01", "2026-08-15", "UTC")
	require.NoError(t, err)
	require.InDelta(t, -3.25, dto.CacheSavedCost, 1e-12)
}

func TestSummary_CacheHitRateNilWhenNoTokens(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{summary: Summary{TotalCost: 100, Requests: 3}}
	s := newTestService(repo, 90, fixedNow)

	dto, err := s.Summary(context.Background(), 1, "2026-08-01", "2026-08-15", "UTC")
	require.NoError(t, err)
	require.Nil(t, dto.CacheHitRate)
}

// ---------- 当页补充查询的拼装 ----------

// rowsService 造一个「一页一行」的服务层，省掉每条用例重复的样板。
func rowsService(t *testing.T, repo *fakeRepo) *Service {
	t.Helper()
	return newTestService(repo, 90, fixedNow)
}

// 三天区间 + 只有首尾两天有消耗：长度、补零、下标对齐三件事一次钉住。
//
// 模型侧取 30 / 70 而不是 50 / 50：取相等的话「选最大」与「选最小」同解，等于没测。
func TestRows_EnrichmentFillsTopModelShareAndDaily(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{
		rows: []Row{{GroupKey: "o:wang", KeyIDs: []int64{1, 2}, CurrentCost: 10, Requests: 5}},
		enriched: map[string]RowEnrichment{
			"o:wang": {
				Models: map[string]float64{"claude-haiku": 30, "claude-opus": 70},
				Daily:  map[string]float64{"2026-08-01": 4, "2026-08-03": 6},
			},
		},
	}

	rows, _, err := rowsService(t, repo).Rows(context.Background(), 1,
		"2026-08-01", "2026-08-03", "UTC", "cost", "desc", "", 1, 20)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	require.NotNil(t, rows[0].TopModel)
	require.Equal(t, "claude-opus", *rows[0].TopModel, "主力模型是花得最多的那个")
	require.NotNil(t, rows[0].TopModelShare)
	require.InDelta(t, 70.0, *rows[0].TopModelShare, 1e-9, "70/(30+70)")
	require.Equal(t, []float64{4, 0, 6}, rows[0].Daily,
		"长度等于本期自然日数，中间没消耗的那天补 0")
}

// 并列时必须定序，否则同一页刷新两次会显示两个不同的主力模型 ——
// 用户会当 bug 报上来，而「每次都不一样」查起来极贵。
//
// 跑 50 轮：Go 的 map 迭代顺序每次都重新随机，靠迭代顺序决定结果的实现
// 在 50 轮里存活的概率是 2^-50。
func TestRows_TopModelTieBreaksOnModelName(t *testing.T) {
	t.Parallel()
	for i := 0; i < 50; i++ {
		repo := &fakeRepo{
			rows: []Row{{GroupKey: "k:1", KeyIDs: []int64{1}}},
			enriched: map[string]RowEnrichment{
				"k:1": {Models: map[string]float64{"b-model": 50, "a-model": 50}},
			},
		}
		rows, _, err := rowsService(t, repo).Rows(context.Background(), 1,
			"2026-08-01", "2026-08-03", "UTC", "cost", "desc", "", 1, 20)
		require.NoError(t, err)
		require.NotNil(t, rows[0].TopModel)
		require.Equal(t, "a-model", *rows[0].TopModel, "第 %d 轮：并列时取名字最小的", i)
	}
}

// 分组本期一分没花时不给主力模型：占比的分母就是那个 0，算出来是 NaN / ±Inf，
// 两者都会让整个 /rows 响应序列化失败。
func TestRows_NoTopModelWhenGroupSpentNothing(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{
		rows: []Row{{GroupKey: "k:1", KeyIDs: []int64{1}}},
		enriched: map[string]RowEnrichment{
			"k:1": {Models: map[string]float64{"claude-opus": 0}},
		},
	}

	rows, _, err := rowsService(t, repo).Rows(context.Background(), 1,
		"2026-08-01", "2026-08-03", "UTC", "cost", "desc", "", 1, 20)
	require.NoError(t, err)
	require.Nil(t, rows[0].TopModel)
	require.Nil(t, rows[0].TopModelShare)
	require.Equal(t, []float64{0, 0, 0}, rows[0].Daily,
		"查成功但没花钱 → 一串 0（前端画贴底的线），不是空数组")

	_, err = json.Marshal(rows)
	require.NoError(t, err, "占比若算成 NaN / Inf，整页响应会序列化失败")
}

// 补充查询失败必须降级而不是整页报错：这三个维度是锦上添花，
// 而金额、环比、请求数才是用户打开这个页面要看的东西。
//
// 替身在出错时**同时**返回一份非空 map（见 fakeRepo.EnrichRows），
// 所以「忽略 error、照用返回值」这种实现会在这里被抓住。
func TestRows_EnrichFailureDegradesInsteadOfFailingThePage(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{
		rows:      []Row{{GroupKey: "k:1", KeyIDs: []int64{1}, CurrentCost: 120, PrevCost: 100, Requests: 4}},
		total:     1,
		enrichErr: errors.New("pq: canceling statement due to statement timeout"),
		enriched: map[string]RowEnrichment{
			"k:1": {Models: map[string]float64{"claude-opus": 9}, Daily: map[string]float64{"2026-08-01": 9}},
		},
	}

	rows, total, err := rowsService(t, repo).Rows(context.Background(), 1,
		"2026-08-01", "2026-08-03", "UTC", "cost", "desc", "", 1, 20)
	require.NoError(t, err, "补充查询挂了不该让整页 500")
	require.EqualValues(t, 1, total)
	require.Len(t, rows, 1)

	require.Nil(t, rows[0].TopModel)
	require.Nil(t, rows[0].TopModelShare)
	require.NotNil(t, rows[0].Daily, "降级也要给数组，null 会在前端 v-for 上炸")
	require.Empty(t, rows[0].Daily, "空数组 → 前端不画线；画一条平的假线会被读成「真的没花钱」")

	// 主功能原样保留
	require.InDelta(t, 120.0, rows[0].CurrentCost, 1e-9)
	require.NotNil(t, rows[0].DeltaPct)
	require.InDelta(t, 30.0, rows[0].AvgCost, 1e-9, "120 / 4")
}

// 补充查询的入参必须是**当页所有行**的令牌并集（去重），区间必须是本期而不是上期。
func TestRows_EnrichReceivesDedupedPageKeyIDsAndCurrentPeriod(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{rows: []Row{
		{GroupKey: "o:a", KeyIDs: []int64{3, 1}},
		{GroupKey: "o:b", KeyIDs: []int64{1, 7}}, // 1 重复出现
	}}

	_, _, err := rowsService(t, repo).Rows(context.Background(), 42,
		"2026-08-01", "2026-08-15", "UTC", "cost", "desc", "", 1, 20)
	require.NoError(t, err)

	require.Equal(t, 1, repo.enrichCalls)
	require.EqualValues(t, 42, repo.gotEnrich.UserID)
	require.ElementsMatch(t, []int64{1, 3, 7}, repo.gotEnrich.KeyIDs)
	require.Len(t, repo.gotEnrich.KeyIDs, 3,
		"重复的令牌会让它的日志被算两遍，占比与趋势线直接翻倍")
	// 本期 08-01–08-15；上期是 07-01–07-15，两者起点不同，传错立刻红。
	require.Equal(t, "2026-08-01", repo.gotEnrich.Cur.StartDate)
	require.Equal(t, "2026-08-15", repo.gotEnrich.Cur.EndDate)
}

// 空页不该发补充查询：那是一次确定返回零行的往返。
func TestRows_SkipsEnrichQueryWhenPageIsEmpty(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{rows: nil, total: 0}

	rows, _, err := rowsService(t, repo).Rows(context.Background(), 1,
		"2026-08-01", "2026-08-15", "UTC", "cost", "desc", "", 9, 20)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.Zero(t, repo.enrichCalls)
}

// ---------- dailyLabels / spreadDaily ----------

// 跨月的四天区间：off-by-one（少一天/多一天）与「月末不进位」两类错各自算出不同的数组。
func TestDailyLabels_CoversEveryNaturalDayInclusive(t *testing.T) {
	t.Parallel()
	cur, err := ParsePeriod("2026-07-30", "2026-08-02", "UTC", fixedNow)
	require.NoError(t, err)

	require.Equal(t,
		[]string{"2026-07-30", "2026-07-31", "2026-08-01", "2026-08-02"},
		dailyLabels(cur))
}

// SQL 与标签都按用户时区生成，正常输入会逐日一一对应。即使仓储返回脏的越界桶，
// 也不能把它夹进首尾格，否则一个趋势点会混合两个不同自然日。
func TestSpreadDaily_MapsOnlyMatchingUserDates(t *testing.T) {
	t.Parallel()
	labels := []string{"2026-08-01", "2026-08-02", "2026-08-03"}
	buckets := map[string]float64{
		"2026-07-31": 5, // 早于首日 → 忽略
		"2026-08-02": 2,
		"2026-08-04": 9, // 晚于末日 → 忽略
	}

	require.Equal(t, []float64{0, 2, 0}, spreadDaily(buckets, labels))
}

func TestSpreadDaily_ReturnsNonNilEmptySliceForEmptyLabels(t *testing.T) {
	t.Parallel()
	got := spreadDaily(map[string]float64{"2026-08-01": 3}, nil)
	require.NotNil(t, got)
	require.Empty(t, got)
}

// 分组本期一条日志都没有（Models 是空 map）时同样不该有主力模型。
// 这条与 TestRows_NoTopModelWhenGroupSpentNothing 分开：那条测的是「有模型但都是 0」，
// 这条测的是「连模型都没有」，两者走的是 topModel 里不同的分支。
func TestTopModel_NoModelsMeansNoTop(t *testing.T) {
	t.Parallel()
	name, share, ok := topModel(nil)
	require.False(t, ok, "空 map 不该有主力模型")
	require.Empty(t, name)
	require.Zero(t, share)
}
