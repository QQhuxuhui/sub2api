//go:build unit

package teamops

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fixedNow 是本文件所有时间推断的基准。用真实时钟的话，同一条断言会随运行日期漂移，
// 而周期推断恰恰是「今天是几号」决定结果的逻辑。
var fixedNow = time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

// fakeRepo 是 teamRepo 的内存替身，并记下最后一次调用的入参：
// 服务层算出的「本期 / 对比区间」只在传给仓储的那一刻才可观测，不留证据就断言不了。
type fakeRepo struct {
	summary    Summary
	summaryErr error
	rows       []Row
	total      int64
	rowsErr    error

	gotUserID int64
	gotCur    Period
	gotPrev   Period
	gotQuery  RowQuery
	calls     int
}

func (f *fakeRepo) Summary(_ context.Context, userID int64, cur, prev Period) (Summary, error) {
	f.calls++
	f.gotUserID, f.gotCur, f.gotPrev = userID, cur, prev
	return f.summary, f.summaryErr
}

func (f *fakeRepo) ListRows(_ context.Context, q RowQuery) ([]Row, int64, error) {
	f.calls++
	f.gotQuery = q
	return f.rows, f.total, f.rowsErr
}

// newTestService 用固定时钟构造 Service。
func newTestService(repo teamRepo, retentionDays int, now time.Time) *Service {
	s := NewService(repo, retentionDays)
	s.now = func() time.Time { return now }
	return s
}

// ---------- resolvePeriods ----------

// 默认视图是「本月 1 日至今天」。这条区间的对比区间是上月同期（§4.4 的「本月」行），
// 不是等长紧邻的前一段。两种规则在这条区间上给出完全不同的日期，所以这条断言
// 能把 DerivePrev 与 DeriveCompare 分开：等长紧邻会给出 07-17–07-31。
func TestResolvePeriods_MonthToDateComparesAgainstSameDaysOfLastMonth(t *testing.T) {
	t.Parallel()
	s := newTestService(nil, 90, fixedNow)

	pair, err := s.resolvePeriods("2026-08-01", "2026-08-15", "UTC", fixedNow)
	require.NoError(t, err)
	require.Equal(t, "2026-08-01", pair.Cur.StartDate)
	require.Equal(t, "2026-08-15", pair.Cur.EndDate)
	require.Equal(t, "2026-07-01", pair.Prev.StartDate)
	require.Equal(t, "2026-07-15", pair.Prev.EndDate)
}

// 整月区间的对比是上月整月。7 月 31 天、6 月 30 天，两种规则给出的终点不同
// （等长紧邻是 06-30–07-30），所以这条也分得开。
func TestResolvePeriods_FullMonthComparesAgainstPreviousFullMonth(t *testing.T) {
	t.Parallel()
	s := newTestService(nil, 90, fixedNow)

	pair, err := s.resolvePeriods("2026-07-01", "2026-07-31", "UTC", fixedNow)
	require.NoError(t, err)
	require.Equal(t, "2026-06-01", pair.Prev.StartDate)
	require.Equal(t, "2026-06-30", pair.Prev.EndDate)
}

// 空白串必须与「没传」等价。teamops 的解析层对 " " 会报格式错误而不是走默认分支，
// 少一次 TrimSpace，前端传个空格就会收到 400，而 /usage 那边同样的输入是正常的。
func TestResolvePeriods_BlankDateParametersFallBackToDefaultRange(t *testing.T) {
	t.Parallel()
	s := newTestService(nil, 90, fixedNow)

	blank, err := s.resolvePeriods("  ", "\t", " UTC ", fixedNow)
	require.NoError(t, err)

	empty, err := s.resolvePeriods("", "", "UTC", fixedNow)
	require.NoError(t, err)
	require.Equal(t, empty.Cur.StartDate, blank.Cur.StartDate)
	require.Equal(t, empty.Cur.EndDate, blank.Cur.EndDate)
	require.Equal(t, "2026-08-01", blank.Cur.StartDate)
	require.Equal(t, "2026-08-15", blank.Cur.EndDate)
}

// 上期在保留期内时必须判为可比。这条守的是「漏调 ApplyRetention」——
// PeriodPair 的零值 Comparable 恰好是 false，只断言 false 的用例对漏调是全绿的。
func TestResolvePeriods_MarksPairComparableWhenPrevWithinRetention(t *testing.T) {
	t.Parallel()
	s := newTestService(nil, 90, fixedNow)

	pair, err := s.resolvePeriods("2026-08-01", "2026-08-15", "UTC", fixedNow)
	require.NoError(t, err)
	require.True(t, pair.Comparable)
	require.Empty(t, pair.IncomparableReason)
}

// 上期越过保留边界时必须判为不可比并给出原因，否则页面会拿一段可能已被物理清理的
// 区间算环比，且不报任何错。
func TestResolvePeriods_MarksPairIncomparableWhenPrevBeyondRetention(t *testing.T) {
	t.Parallel()
	// 保留 30 天时边界落在 2026-07-16，上期起点 07-01 在它之前。
	s := newTestService(nil, 30, fixedNow)

	pair, err := s.resolvePeriods("2026-08-01", "2026-08-15", "UTC", fixedNow)
	require.NoError(t, err)
	require.False(t, pair.Comparable)
	require.Equal(t, "prev_period_beyond_retention", pair.IncomparableReason)
}

func TestResolvePeriods_RejectsRangeLongerThanLimit(t *testing.T) {
	t.Parallel()
	s := newTestService(nil, 90, fixedNow)

	_, err := s.resolvePeriods("2026-01-01", "2026-08-15", "UTC", fixedNow)
	require.ErrorIs(t, err, ErrRangeTooLong)
	// 超长必须保持自己的哨兵，不能被并进「格式不对」那一类——
	// 两者的界面文案完全不同，用户按错的那句去改日期是改不出来的。
	require.NotErrorIs(t, err, ErrInvalidDate)
}

func TestResolvePeriods_RejectsInvertedRange(t *testing.T) {
	t.Parallel()
	s := newTestService(nil, 90, fixedNow)

	_, err := s.resolvePeriods("2026-08-15", "2026-08-01", "UTC", fixedNow)
	require.ErrorIs(t, err, ErrRangeInverted)
	require.NotErrorIs(t, err, ErrInvalidDate)
}

// 解析失败必须带上可判定的哨兵。解析层只回 fmt.Errorf("invalid start_date: %w", err)，
// 靠错误文本前缀分类的话，那句措辞改一次，用户填错日期收到的就从 400 变成 500。
func TestResolvePeriods_WrapsUnparsableDateWithSentinel(t *testing.T) {
	t.Parallel()
	s := newTestService(nil, 90, fixedNow)

	_, err := s.resolvePeriods("2026-8-1", "2026-08-15", "UTC", fixedNow)
	require.ErrorIs(t, err, ErrInvalidDate)
}

// 时区参数也要 trim：带空格的时区名 LoadLocation 认不出来，会静默回落到服务器时区，
// 于是用户选的时区被丢掉，区间边界切在另一个时区的零点上——金额差一点点，没有报错。
//
// 断言写成「两个不同时区必须给出不同的边界」而不是「必须等于某个具体时区」：
// 后者在服务器时区恰好就是被测时区的机器上会漏判，而前者在任何机器上都成立
// （不 trim 时两者都回落到同一个服务器时区，边界会相等）。
func TestResolvePeriods_TrimsTimezoneParameter(t *testing.T) {
	t.Parallel()
	s := newTestService(nil, 90, fixedNow)

	tokyo, err := s.resolvePeriods("2026-08-01", "2026-08-15", " Asia/Tokyo ", fixedNow)
	require.NoError(t, err)
	utc, err := s.resolvePeriods("2026-08-01", "2026-08-15", " UTC ", fixedNow)
	require.NoError(t, err)

	require.Equal(t, 9*time.Hour, utc.Cur.Start.Sub(tokyo.Cur.Start),
		"东京零点比 UTC 零点早 9 小时；两者相等说明时区参数被丢掉了")
	require.Equal(t, "Asia/Tokyo", tokyo.Cur.Timezone)
	require.Equal(t, "UTC", utc.Cur.Timezone)
}

// ---------- finalizeRows ----------

func TestFinalizeRows_FillsDeltaWhenComparableAndPrevPositive(t *testing.T) {
	t.Parallel()
	rows := []Row{{CurrentCost: 100, PrevCost: 80}}

	finalizeRows(rows, true)

	require.NotNil(t, rows[0].DeltaPct)
	require.InDelta(t, 25.0, *rows[0].DeltaPct, 1e-9)
	require.NotNil(t, rows[0].DeltaAbs)
	require.InDelta(t, 20.0, *rows[0].DeltaAbs, 1e-9)
}

func TestFinalizeRows_LeavesDeltaNilWhenIncomparable(t *testing.T) {
	t.Parallel()
	rows := []Row{{CurrentCost: 100, PrevCost: 80}}

	finalizeRows(rows, false)

	require.Nil(t, rows[0].DeltaPct)
	require.Nil(t, rows[0].DeltaAbs)
}

// 上期为 0 时环比在数学上无定义。填 0 除的结果会得到 +Inf，
// 而 +Inf 在 encoding/json 里直接让整个响应序列化失败——页面白屏，日志里只有一行
// "json: unsupported value"，看不出是哪一行数据引起的。
func TestFinalizeRows_LeavesDeltaNilWhenPrevIsZero(t *testing.T) {
	t.Parallel()
	rows := []Row{{CurrentCost: 100, PrevCost: 0}}

	finalizeRows(rows, true)

	require.Nil(t, rows[0].DeltaPct)
	require.Nil(t, rows[0].DeltaAbs)
}

// 展示金额分配的分母是当页各行之和。三行各 0.005 各自进位成 0.01（共 3 分），
// 而 0.015 的总额只显示成 0.02（2 分），必须被摊回去。
func TestFinalizeRows_AllocatesDisplayCostsToMatchPageTotal(t *testing.T) {
	t.Parallel()
	rows := []Row{{CurrentCost: 0.005}, {CurrentCost: 0.005}, {CurrentCost: 0.005}}

	finalizeRows(rows, false)

	var cents int64
	for _, r := range rows {
		cents += int64(math.Round(r.CurrentCost * 100))
	}
	require.Equal(t, int64(2), cents)
}

// 环比必须建立在分配之前的原始金额上。分配会把某一行的展示金额摊掉 1 分
// （三行各 0.005 时必然有一行被摊成 0.00），顺序写反的话那一行的环比就会
// 从「与上期持平」变成「-100%」——一个凭空出现的、只在特定金额组合下发生的红字。
func TestFinalizeRows_ComputesDeltaBeforeDisplayAllocation(t *testing.T) {
	t.Parallel()
	rows := []Row{
		{CurrentCost: 0.005, PrevCost: 0.005},
		{CurrentCost: 0.005, PrevCost: 0.005},
		{CurrentCost: 0.005, PrevCost: 0.005},
	}

	finalizeRows(rows, true)

	// 分配确实动了金额，否则这条测试什么都没验证。
	var docked int
	for _, r := range rows {
		if math.Round(r.CurrentCost*100) == 0 {
			docked++
		}
	}
	require.Equal(t, 1, docked, "三行各 0.005 时必有一行被摊掉 1 分")

	for i, r := range rows {
		require.NotNil(t, r.DeltaPct, "第 %d 行", i)
		require.InDelta(t, 0.0, *r.DeltaPct, 1e-9, "第 %d 行", i)
	}
}

// 异常标记属于结论句阶段，本阶段恒为 false。写成断言是为了让「顺手把它填上」的改动
// 必须先来改这条测试，而不是悄悄让界面出现没有判定依据的红点。
func TestFinalizeRows_KeepsAnomalyFlagUnset(t *testing.T) {
	t.Parallel()
	rows := []Row{{CurrentCost: 100, PrevCost: 1}}

	finalizeRows(rows, true)

	require.False(t, rows[0].IsAnomaly)
}

// ---------- Summary ----------

func TestSummary_PassesResolvedPeriodsAndUserToRepo(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{}
	s := newTestService(repo, 90, fixedNow)

	_, err := s.Summary(context.Background(), 42, "2026-08-01", "2026-08-15", "UTC")
	require.NoError(t, err)
	require.Equal(t, int64(42), repo.gotUserID)
	require.Equal(t, "2026-08-01", repo.gotCur.StartDate)
	require.Equal(t, "2026-07-01", repo.gotPrev.StartDate)
	require.Equal(t, "2026-07-15", repo.gotPrev.EndDate)
}

func TestSummary_EchoesRequestedTimezoneWhenItIsUsable(t *testing.T) {
	t.Parallel()
	s := newTestService(&fakeRepo{}, 90, fixedNow)

	dto, err := s.Summary(context.Background(), 1, "2026-08-01", "2026-08-15", "Asia/Shanghai")
	require.NoError(t, err)
	require.Equal(t, "Asia/Shanghai", dto.Period.Timezone)
}

// 非法时区会被静默回落到服务器时区（这是 /usage 既有的语义，不在本页改）。
// 回显必须是**实际生效**的那个，不能把请求里的原文照抄回去：界面上写着
// "Mars/Phobos"，而金额是按另一个时区的自然日切的，用户对不上账也查不出原因。
func TestSummary_EchoesEffectiveTimezoneNotTheRequestedString(t *testing.T) {
	t.Parallel()
	s := newTestService(&fakeRepo{}, 90, fixedNow)

	dto, err := s.Summary(context.Background(), 1, "2026-08-01", "2026-08-15", "Mars/Phobos")
	require.NoError(t, err)
	require.NotEqual(t, "Mars/Phobos", dto.Period.Timezone)
	require.NotEmpty(t, dto.Period.Timezone)
}

func TestSummary_ReportsComparePeriodOnDTO(t *testing.T) {
	t.Parallel()
	s := newTestService(&fakeRepo{}, 90, fixedNow)

	dto, err := s.Summary(context.Background(), 1, "2026-08-01", "2026-08-15", "UTC")
	require.NoError(t, err)
	require.Equal(t, "2026-07-01", dto.Compare.StartDate)
	require.Equal(t, "2026-07-15", dto.Compare.EndDate)
	require.True(t, dto.Compare.Comparable)
	require.Nil(t, dto.Compare.Reason)
}

func TestSummary_RoundsDisplayAmountsAndKeepsRawTotal(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{summary: Summary{
		TotalCost: 3676.0512, PrevCost: 3278.5349, TopRowCost: 1284.4951,
		RowCount: 10, KeyCount: 14, OwnedKeyCount: 7,
	}}
	s := newTestService(repo, 90, fixedNow)

	dto, err := s.Summary(context.Background(), 1, "2026-08-01", "2026-08-15", "UTC")
	require.NoError(t, err)
	require.InDelta(t, 3676.05, dto.TotalCost, 1e-9)
	// 验收断言拿它跟 /usage/stats 的总额比，取整会让这条恒等式在分位上失守。
	require.InDelta(t, 3676.0512, dto.TotalCostRaw, 1e-9)
	require.InDelta(t, 3278.53, dto.PrevCost, 1e-9)
	require.InDelta(t, 1284.50, dto.TopRowCost, 1e-9)
	require.Equal(t, 10, dto.RowCount)
	require.Equal(t, 14, dto.KeyCount)
	require.Equal(t, 7, dto.OwnedKeyCount)
}

// 环比用的是**展示值**（已取整到分），不是原值。界面上写着 120.01 与 100.00，
// 环比额就必须是 20.01；拿原值算出来的 20.002 会让用户当场用计算器证伪。
//
// 选例必须让两种口径算出不同的数：120.006 取整成 120.01、100.004 取整成 100.00，
// 展示口径给 +20.01 / 20.01，原值口径给 +20.0012 / 20.002。
// 整数金额（120 / 100）两种口径同解，钉不住任何东西。
func TestSummary_FillsDeltaFromDisplayAmountsNotRawAmounts(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{summary: Summary{TotalCost: 120.006, PrevCost: 100.004}}
	s := newTestService(repo, 90, fixedNow)

	dto, err := s.Summary(context.Background(), 1, "2026-08-01", "2026-08-15", "UTC")
	require.NoError(t, err)
	require.InDelta(t, 120.01, dto.TotalCost, 1e-9)
	require.InDelta(t, 100.00, dto.PrevCost, 1e-9)
	require.NotNil(t, dto.DeltaAbs)
	require.InDelta(t, 20.01, *dto.DeltaAbs, 1e-6)
	require.NotNil(t, dto.DeltaPct)
	require.InDelta(t, 20.01, *dto.DeltaPct, 1e-6)
	// 界面上「本期 − 上期 == 环比额」必须逐位成立。
	require.InDelta(t, dto.TotalCost-dto.PrevCost, *dto.DeltaAbs, 1e-9)
}

func TestSummary_LeavesDeltaNilWhenPrevIsZero(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{summary: Summary{TotalCost: 120, PrevCost: 0}}
	s := newTestService(repo, 90, fixedNow)

	dto, err := s.Summary(context.Background(), 1, "2026-08-01", "2026-08-15", "UTC")
	require.NoError(t, err)
	require.Nil(t, dto.DeltaPct)
	require.Nil(t, dto.DeltaAbs)
}

// 上期不足一分时展示值是 0.00，环比的分母在界面上就是 0。此时给出一个
// 「+11999900%」既没有意义也无法验证，一律认输。
func TestSummary_LeavesDeltaNilWhenPrevRoundsToZero(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{summary: Summary{TotalCost: 120, PrevCost: 0.001}}
	s := newTestService(repo, 90, fixedNow)

	dto, err := s.Summary(context.Background(), 1, "2026-08-01", "2026-08-15", "UTC")
	require.NoError(t, err)
	require.Nil(t, dto.DeltaPct)
}

func TestSummary_ReportsIncomparableReasonAndDropsDelta(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{summary: Summary{TotalCost: 120, PrevCost: 100}}
	s := newTestService(repo, 30, fixedNow)

	dto, err := s.Summary(context.Background(), 1, "2026-08-01", "2026-08-15", "UTC")
	require.NoError(t, err)
	require.False(t, dto.Compare.Comparable)
	require.NotNil(t, dto.Compare.Reason)
	require.Equal(t, "prev_period_beyond_retention", *dto.Compare.Reason)
	require.Nil(t, dto.DeltaPct)
	require.Nil(t, dto.DeltaAbs)
}

func TestSummary_OmitsRetentionWarningWhenCurrentPeriodIsWithinRetention(t *testing.T) {
	t.Parallel()
	s := newTestService(&fakeRepo{}, 90, fixedNow)

	dto, err := s.Summary(context.Background(), 1, "2026-08-01", "2026-08-15", "UTC")
	require.NoError(t, err)
	require.Nil(t, dto.RetentionWarning)
}

// 本期起点早于保留边界时，对账条要降级成「可查消耗」并说明起点，
// 否则用户会把一个被截断过的数字当成全量。
//
// 边界时刻是 2026-05-17 12:00 UTC，那天只剩半天数据，所以第一个**完整**可查的
// 自然日是 05-18。
func TestSummary_FlagsRetentionWarningWithCutDateWhenCurrentPeriodIsPartial(t *testing.T) {
	t.Parallel()
	s := newTestService(&fakeRepo{}, 90, fixedNow)

	dto, err := s.Summary(context.Background(), 1, "2026-05-16", "2026-08-15", "UTC")
	require.NoError(t, err)
	require.NotNil(t, dto.RetentionWarning)
	require.Equal(t, "period_partial", dto.RetentionWarning.Kind)
	require.Equal(t, "2026-05-18", dto.RetentionWarning.CutDate)
}

// 告警的判定比的是**时刻**，提示条给的是**日期**。本期起点恰好落在边界那一天时，
// 若 cut date 直接取边界时刻所在的自然日，提示条会写成
// 「本期 05-17 – 08-15 / 可查数据自 05-17 起」——自相矛盾，而那天的前半天确实缺数据。
// 告警一旦成立，cut date 必须严格晚于本期起点。
//
// 这条只覆盖零点正常存在的时区；午夜缺口那一类由
// TestSummary_RetentionCutDateIsAfterPeriodStartInTimezoneWithoutMidnight 单独钉。
func TestSummary_RetentionCutDateIsAlwaysAfterPeriodStart(t *testing.T) {
	t.Parallel()
	s := newTestService(&fakeRepo{}, 90, fixedNow)

	// 起点 05-17 00:00 UTC 早于边界 05-17 12:00 UTC，告警成立。
	dto, err := s.Summary(context.Background(), 1, "2026-05-17", "2026-08-15", "UTC")
	require.NoError(t, err)
	require.NotNil(t, dto.RetentionWarning)
	require.Equal(t, "2026-05-17", dto.Period.StartDate)
	require.Greater(t, dto.RetentionWarning.CutDate, dto.Period.StartDate,
		"提示条不能写成「本期自 X 起 / 可查数据自 X 起」")
}

// 「本期起点落在边界当天 → cut date 必须晚一天」这条不变量在**午夜不存在**的时区里
// 需要额外处理，而 UTC / Asia/Shanghai 这类时区无论处理与否结果都一样——
// 用它们做用例，「处理了午夜缺口」和「没处理」输出逐字相同，钉不住任何东西。
//
// America/Santiago 2025-09-07 的 00:00 直接跳到 01:00：在本地时区上做「加一天」，
// time.Date 会把不存在的零点规范化成前一天 23:00，加完还落在 09-06，
// cut date 又变回本期起点那一天，自相矛盾的提示条原样复现。
func TestRetentionCutDate_SurvivesTimezoneWithoutMidnight(t *testing.T) {
	t.Parallel()
	santiago, err := time.LoadLocation("America/Santiago")
	require.NoError(t, err)
	// 边界落在 2025-09-06 01:00 -04:00，也就是「午夜缺口」那天的前一天。
	cutoff := time.Date(2025, 9, 6, 5, 0, 0, 0, time.UTC)

	require.Equal(t, "2025-09-07", retentionCutDate(cutoff, santiago))
}

func TestSummary_RetentionCutDateIsAfterPeriodStartInTimezoneWithoutMidnight(t *testing.T) {
	t.Parallel()
	// now − 90 天 = 2025-09-06 05:00 UTC = 2025-09-06 01:00 -04:00。
	// 本期起点 2025-09-06 00:00 -04:00 早于它，告警成立。
	now := time.Date(2025, 12, 5, 5, 0, 0, 0, time.UTC)
	s := newTestService(&fakeRepo{}, 90, now)

	dto, err := s.Summary(context.Background(), 1, "2025-09-06", "2025-12-05", "America/Santiago")
	require.NoError(t, err)
	require.Equal(t, "2025-09-06", dto.Period.StartDate)
	require.NotNil(t, dto.RetentionWarning)
	require.Equal(t, "2025-09-07", dto.RetentionWarning.CutDate)
	require.Greater(t, dto.RetentionWarning.CutDate, dto.Period.StartDate,
		"提示条不能写成「本期自 X 起 / 可查数据自 X 起」")
}

// cut date 必须在**用户时区**里算，与 period / compare 的日期同一口径。
// 同一个边界时刻在两个时区里落在不同的自然日上，算错时区就会给出一个差一天的提示。
func TestSummary_RetentionCutDateUsesUserTimezone(t *testing.T) {
	t.Parallel()
	// 边界时刻 = 2026-05-17 20:00 UTC = 2026-05-18 04:00 +0800。
	// UTC 用户的第一个完整日是 05-18，上海用户的是 05-19。
	now := time.Date(2026, 8, 15, 20, 0, 0, 0, time.UTC)
	s := newTestService(&fakeRepo{}, 90, now)

	utc, err := s.Summary(context.Background(), 1, "2026-05-16", "2026-08-15", "UTC")
	require.NoError(t, err)
	require.NotNil(t, utc.RetentionWarning)
	require.Equal(t, "2026-05-18", utc.RetentionWarning.CutDate)

	shanghai, err := s.Summary(context.Background(), 1, "2026-05-16", "2026-08-15", "Asia/Shanghai")
	require.NoError(t, err)
	require.NotNil(t, shanghai.RetentionWarning)
	require.Equal(t, "2026-05-19", shanghai.RetentionWarning.CutDate)
}

// 结论句属于后续阶段。恒为 nil 是本阶段对前端的承诺（前端据此渲染中性句）。
func TestSummary_LeavesConclusionNil(t *testing.T) {
	t.Parallel()
	s := newTestService(&fakeRepo{}, 90, fixedNow)

	dto, err := s.Summary(context.Background(), 1, "2026-08-01", "2026-08-15", "UTC")
	require.NoError(t, err)
	require.Nil(t, dto.Conclusion)
}

func TestSummary_PropagatesRepoError(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	s := newTestService(&fakeRepo{summaryErr: boom}, 90, fixedNow)

	_, err := s.Summary(context.Background(), 1, "2026-08-01", "2026-08-15", "UTC")
	require.ErrorIs(t, err, boom)
}

func TestSummary_DoesNotQueryRepoWhenPeriodIsInvalid(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{}
	s := newTestService(repo, 90, fixedNow)

	_, err := s.Summary(context.Background(), 1, "2026-01-01", "2026-08-15", "UTC")
	require.ErrorIs(t, err, ErrRangeTooLong)
	// 区间上限挡的就是"又慢又不全的查询"，参数不合法时那条查询根本不该发出去。
	require.Zero(t, repo.calls)
}

// ---------- Rows ----------

func TestRows_PassesAllQueryParametersToRepo(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{}
	s := newTestService(repo, 90, fixedNow)

	_, _, err := s.Rows(context.Background(), 42,
		"2026-08-01", "2026-08-15", "UTC", "delta", "asc", "王磊", 3, 50)
	require.NoError(t, err)
	require.Equal(t, int64(42), repo.gotQuery.UserID)
	require.Equal(t, "2026-08-01", repo.gotQuery.Cur.StartDate)
	require.Equal(t, "2026-07-01", repo.gotQuery.Prev.StartDate)
	require.Equal(t, "delta", repo.gotQuery.Sort)
	require.Equal(t, "asc", repo.gotQuery.Order)
	require.Equal(t, "王磊", repo.gotQuery.Q)
	require.Equal(t, 3, repo.gotQuery.Page)
	require.Equal(t, 50, repo.gotQuery.PageSize)
}

func TestRows_ReturnsTotalFromRepo(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{rows: []Row{{GroupKey: "k:1"}}, total: 137}
	s := newTestService(repo, 90, fixedNow)

	rows, total, err := s.Rows(context.Background(), 1, "2026-08-01", "2026-08-15", "UTC", "", "", "", 1, 20)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, int64(137), total)
}

func TestRows_FillsDeltaOnReturnedRows(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{rows: []Row{{GroupKey: "k:1", CurrentCost: 120, PrevCost: 100}}}
	s := newTestService(repo, 90, fixedNow)

	rows, _, err := s.Rows(context.Background(), 1, "2026-08-01", "2026-08-15", "UTC", "", "", "", 1, 20)
	require.NoError(t, err)
	require.NotNil(t, rows[0].DeltaPct)
	require.InDelta(t, 20.0, *rows[0].DeltaPct, 1e-9)
}

// 上期不可比时行级环比也必须为空，否则界面上会出现「对比区间不可用」的提示条
// 和一列煞有介事的环比百分比同时存在。
func TestRows_DropsDeltaWhenPrevPeriodIsBeyondRetention(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{rows: []Row{{GroupKey: "k:1", CurrentCost: 120, PrevCost: 100}}}
	s := newTestService(repo, 30, fixedNow)

	rows, _, err := s.Rows(context.Background(), 1, "2026-08-01", "2026-08-15", "UTC", "", "", "", 1, 20)
	require.NoError(t, err)
	require.Nil(t, rows[0].DeltaPct)
	require.Nil(t, rows[0].DeltaAbs)
}

func TestRows_PropagatesRepoError(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	s := newTestService(&fakeRepo{rowsErr: boom}, 90, fixedNow)

	_, _, err := s.Rows(context.Background(), 1, "2026-08-01", "2026-08-15", "UTC", "", "", "", 1, 20)
	require.ErrorIs(t, err, boom)
}

func TestNewService_FallsBackToDefaultRetentionWhenUnconfigured(t *testing.T) {
	t.Parallel()
	require.Equal(t, defaultRetentionDays, NewService(nil, 0).retentionDays)
	require.Equal(t, defaultRetentionDays, NewService(nil, -1).retentionDays)
	require.Equal(t, 45, NewService(nil, 45).retentionDays)
}
