//go:build unit

package teamops

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParsePeriod_EndDateIsInclusive(t *testing.T) {
	t.Parallel()
	// 语义与 usage_handler.go 的用户用量区间解析一致：end_date 含当日，内部转成右开边界（+1 天）
	p, err := ParsePeriod("2026-08-01", "2026-08-15", "Asia/Shanghai", time.Now())
	require.NoError(t, err)
	require.Equal(t, "2026-08-01T00:00:00+08:00", p.Start.Format(time.RFC3339))
	require.Equal(t, "2026-08-16T00:00:00+08:00", p.End.Format(time.RFC3339),
		"end 必须是 end_date 次日零点（右开），否则会漏掉最后一天")
}

// 这条断言不可被「和上面那条 +08:00 的断言重复」为由简化掉：单点的 +08:00 断言在服务器时区
// 恰好也是 Asia/Shanghai 的机器上不会红（实测过：把实现换成忽略 userTZ 的服务器时区解析，
// 只有这条跨时区比较变红）。只有拿两个不同时区互相比较，才与服务器时区取值无关。
func TestParsePeriod_UserTimezoneShiftsBoundary(t *testing.T) {
	t.Parallel()
	sh, err := ParsePeriod("2026-08-15", "2026-08-15", "Asia/Shanghai", time.Now())
	require.NoError(t, err)
	utc, err := ParsePeriod("2026-08-15", "2026-08-15", "UTC", time.Now())
	require.NoError(t, err)
	require.True(t, sh.Start.Before(utc.Start),
		"上海的 08-15 00:00 对应 UTC 08-14 16:00，必须早于 UTC 的 08-15 00:00")
}

func TestParsePeriod_RejectsTooLongRange(t *testing.T) {
	t.Parallel()
	_, err := ParsePeriod("2026-01-01", "2026-08-15", "UTC", time.Now())
	require.ErrorIs(t, err, ErrRangeTooLong)
}

// naturalDaysBetween 独立于被测实现地数自然日：把两端各自的日历日期拿到 UTC 里相减，
// UTC 没有夏令时，所以差值就是自然日数。测试不复用 period.go 里的同类工具，避免自证。
func naturalDaysBetween(t *testing.T, from, to time.Time) int {
	t.Helper()
	f, err := time.Parse(dateLayout, from.Format(dateLayout))
	require.NoError(t, err)
	e, err := time.Parse(dateLayout, to.Format(dateLayout))
	require.NoError(t, err)
	return int(e.Sub(f).Hours() / 24)
}

// 等长的口径是**自然日数相等**，不是墙上时长相等。这是一次有意的语义变更：
// 界面上要明写对比区间（设计文档 §4.4），用户读的是自然日期；按墙上时长平移会让区间起点
// 落在 23:00 或 01:00 这种非零点上，展示出来的日期与实际查询边界能差一天，正是本页要防的
// 「对不上账」。代价是跨夏令时的两段墙上时长会差 1 小时，这个代价远小于显示一个错误日期。
func TestDerivePrev_EqualLengthAdjacent(t *testing.T) {
	t.Parallel()
	cur, err := ParsePeriod("2026-08-01", "2026-08-15", "Asia/Shanghai", time.Now())
	require.NoError(t, err)
	prev := DerivePrev(cur, "Asia/Shanghai")
	require.Equal(t, naturalDaysBetween(t, cur.Start, cur.End), naturalDaysBetween(t, prev.Start, prev.End),
		"上期与本期自然日数相等")
	require.Equal(t, cur.Start, prev.End, "上期紧邻本期")
}

func TestApplyRetention_MarksIncomparable(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	cur, err := ParsePeriod("2026-08-01", "2026-08-15", "UTC", now)
	require.NoError(t, err)
	pair := PeriodPair{Cur: cur, Prev: DerivePrev(cur, "UTC"), Comparable: true}

	// 保留期只有 20 天时，上期（07/17–08/01）整段越界
	got := ApplyRetention(pair, 20, now)
	require.False(t, got.Comparable)
	require.NotEmpty(t, got.IncomparableReason)
}

func TestParsePeriod_RangeLengthBoundary(t *testing.T) {
	t.Parallel()
	// end_date 含当日，所以 92 天区间的最后一天是起点 + 91 天
	p, err := ParsePeriod("2026-05-16", "2026-08-15", "Asia/Shanghai", time.Now())
	require.NoError(t, err, "整 92 天必须放行")
	require.Equal(t, 92*24*time.Hour, p.End.Sub(p.Start))

	_, err = ParsePeriod("2026-05-15", "2026-08-15", "Asia/Shanghai", time.Now())
	require.ErrorIs(t, err, ErrRangeTooLong, "93 天必须拒绝")
}

func TestParsePeriod_RejectsInvertedRange(t *testing.T) {
	t.Parallel()
	_, err := ParsePeriod("2026-08-15", "2026-08-01", "UTC", time.Now())
	require.ErrorIs(t, err, ErrRangeInverted)

	// 同一天不算倒挂：单日区间是合法的常用查询
	p, err := ParsePeriod("2026-08-15", "2026-08-15", "UTC", time.Now())
	require.NoError(t, err)
	require.Equal(t, 24*time.Hour, p.End.Sub(p.Start))
}

func TestParsePeriod_DefaultsToCurrentMonthInUserTimezone(t *testing.T) {
	t.Parallel()
	p, err := ParsePeriod("", "", "Asia/Shanghai", time.Now())
	require.NoError(t, err)

	start, err := time.Parse(dateLayout, p.StartDate)
	require.NoError(t, err)
	end, err := time.Parse(dateLayout, p.EndDate)
	require.NoError(t, err)
	require.Equal(t, 1, start.Day(), "默认区间从当月 1 日开始")
	require.Equal(t, start.Year(), end.Year())
	require.Equal(t, start.Month(), end.Month(), "默认区间止于当月今天")
	require.False(t, end.Before(start))

	require.Equal(t, "+08:00", p.Start.Format("-07:00"), "边界必须落在用户时区")
	require.Equal(t, p.StartDate+"T00:00:00+08:00", p.Start.Format(time.RFC3339))

	days := int(end.Sub(start).Hours()/24) + 1
	require.Equal(t, p.Start.AddDate(0, 0, days), p.End, "End 仍是 end_date 次日零点")
}

func TestParsePeriod_DefaultsUseInjectedNow(t *testing.T) {
	t.Parallel()
	// 该时刻在 UTC 是 03-14，在上海已经跨到 03-15，默认区间的终点必须跟着用户时区走
	now := time.Date(2026, 3, 14, 20, 0, 0, 0, time.UTC)

	sh, err := ParsePeriod("", "", "Asia/Shanghai", now)
	require.NoError(t, err)
	require.Equal(t, "2026-03-01", sh.StartDate, "默认区间起点是 now 所在月的 1 日，按传入的 now 算")
	require.Equal(t, "2026-03-15", sh.EndDate, "默认区间终点是 now 在用户时区里的当天")
	require.Equal(t, "2026-03-01T00:00:00+08:00", sh.Start.Format(time.RFC3339))
	require.Equal(t, "2026-03-16T00:00:00+08:00", sh.End.Format(time.RFC3339))

	utc, err := ParsePeriod("", "", "UTC", now)
	require.NoError(t, err)
	require.Equal(t, "2026-03-14", utc.EndDate, "同一时刻在 UTC 还是 03-14")
}

func TestParsePeriod_DSTFallBackDoesNotShrinkLimit(t *testing.T) {
	t.Parallel()
	// 纽约 2026-11-01 夏令时回拨，那天有 25 小时。10-01→12-31 是整 92 个自然日，
	// 但墙上时长是 92*24h+1h：上限必须按自然日判，按时长判会把这个合法区间误拒。
	p, err := ParsePeriod("2026-10-01", "2026-12-31", "America/New_York", time.Now())
	require.NoError(t, err, "跨夏令时回拨的整 92 天必须放行")
	require.Equal(t, 92*24*time.Hour+time.Hour, p.End.Sub(p.Start),
		"这段区间的墙上时长确实超过 92*24h，正是时长比较会误判的原因")

	_, err = ParsePeriod("2026-09-30", "2026-12-31", "America/New_York", time.Now())
	require.ErrorIs(t, err, ErrRangeTooLong, "同一时区下 93 个自然日仍然必须拒绝")
}

// 以下四条覆盖设计文档 §4.4 的对比区间规则。后端只收到 start_date/end_date、收不到 preset 名，
// 所以 DeriveCompare 只能从日期形态推断周期类型。
func TestDeriveCompare_MonthToDateUsesPreviousMonthSameDays(t *testing.T) {
	t.Parallel()
	// 默认视图就是本月：08/01–08/15 的对比区间必须是 07/01–07/15，
	// 而不是等长紧邻的 07/17–07/31（拿本月前半月跟上月后半月比是没有意义的对比）
	cur, err := ParsePeriod("2026-08-01", "2026-08-15", "Asia/Shanghai", time.Now())
	require.NoError(t, err)
	cmp := DeriveCompare(cur, "Asia/Shanghai")
	require.Equal(t, "2026-07-01", cmp.StartDate)
	require.Equal(t, "2026-07-15", cmp.EndDate)
	require.Equal(t, "2026-07-01T00:00:00+08:00", cmp.Start.Format(time.RFC3339))
	require.Equal(t, "2026-07-16T00:00:00+08:00", cmp.End.Format(time.RFC3339), "右开边界同样含当日")
}

func TestDeriveCompare_MonthToDateClampsToShorterMonth(t *testing.T) {
	t.Parallel()
	// 用「月初至今」的非整月区间，确保走的是截断分支而不是整月分支
	cur, err := ParsePeriod("2026-03-01", "2026-03-30", "Asia/Shanghai", time.Now())
	require.NoError(t, err)
	cmp := DeriveCompare(cur, "Asia/Shanghai")
	require.Equal(t, "2026-02-01", cmp.StartDate)
	require.Equal(t, "2026-02-28", cmp.EndDate, "2026 年 2 月只有 28 天，截断到 28")

	// 闰年要截到 29 而不是写死 28
	leap, err := ParsePeriod("2028-03-01", "2028-03-30", "Asia/Shanghai", time.Now())
	require.NoError(t, err)
	cmpLeap := DeriveCompare(leap, "Asia/Shanghai")
	require.Equal(t, "2028-02-29", cmpLeap.EndDate, "2028 年 2 月有 29 天")

	// 上月足够长时不截断，老老实实取同一天
	noClamp, err := ParsePeriod("2026-03-01", "2026-03-15", "Asia/Shanghai", time.Now())
	require.NoError(t, err)
	require.Equal(t, "2026-02-15", DeriveCompare(noClamp, "Asia/Shanghai").EndDate, "2 月有 15 日，不该被截短")
}

func TestDeriveCompare_FullMonthUsesPreviousFullMonth(t *testing.T) {
	t.Parallel()
	cur, err := ParsePeriod("2026-08-01", "2026-08-31", "Asia/Shanghai", time.Now())
	require.NoError(t, err)
	cmp := DeriveCompare(cur, "Asia/Shanghai")
	require.Equal(t, "2026-07-01", cmp.StartDate)
	require.Equal(t, "2026-07-31", cmp.EndDate, "整月对上月整月")
	require.Equal(t, "2026-08-01T00:00:00+08:00", cmp.End.Format(time.RFC3339), "上月整月的右开边界正是本月 1 日")

	// 上月比本月长：整月规则与「上月同一天」规则在这里结果不同，必须走整月。
	// 4 月 30 天、3 月 31 天，按「同一天」会得到 03/01–03/30，少算一天。
	apr, err := ParsePeriod("2026-04-01", "2026-04-30", "Asia/Shanghai", time.Now())
	require.NoError(t, err)
	cmpApr := DeriveCompare(apr, "Asia/Shanghai")
	require.Equal(t, "2026-03-01", cmpApr.StartDate)
	require.Equal(t, "2026-03-31", cmpApr.EndDate, "4 月整月对 3 月整月，不是 3 月 1–30 日")

	// 上月比本月短：7 月 31 天、6 月 30 天，取上月整月而不是不存在的 6 月 31 日
	jul, err := ParsePeriod("2026-07-01", "2026-07-31", "Asia/Shanghai", time.Now())
	require.NoError(t, err)
	cmpJul := DeriveCompare(jul, "Asia/Shanghai")
	require.Equal(t, "2026-06-01", cmpJul.StartDate)
	require.Equal(t, "2026-06-30", cmpJul.EndDate)
}

func TestDeriveCompare_SingleDayUsesPreviousDay(t *testing.T) {
	t.Parallel()
	cur, err := ParsePeriod("2026-08-15", "2026-08-15", "Asia/Shanghai", time.Now())
	require.NoError(t, err)
	cmp := DeriveCompare(cur, "Asia/Shanghai")
	require.Equal(t, "2026-08-14", cmp.StartDate)
	require.Equal(t, "2026-08-14", cmp.EndDate)
	require.Equal(t, 1, naturalDaysBetween(t, cmp.Start, cmp.End), "对比区间也是一整个自然日")
}

func TestDeriveCompare_CustomRangeFallsBackToAdjacent(t *testing.T) {
	t.Parallel()
	// 不是月初起、也不是单日，退回等长紧邻
	cur, err := ParsePeriod("2026-08-05", "2026-08-19", "Asia/Shanghai", time.Now())
	require.NoError(t, err)
	cmp := DeriveCompare(cur, "Asia/Shanghai")
	require.Equal(t, DerivePrev(cur, "Asia/Shanghai"), cmp, "自定义区间走等长紧邻")
	require.Equal(t, "2026-07-21", cmp.StartDate)
	require.Equal(t, "2026-08-04", cmp.EndDate)
	require.Equal(t, cur.Start, cmp.End, "紧邻本期")
}

func TestDeriveCompare_LandsOnNaturalDayBoundaryAcrossDST(t *testing.T) {
	t.Parallel()
	// 纽约 2026-03-08 春季前拨、2026-11-01 秋季回拨。两端落点都必须是自然日零点，
	// 偏移量随夏令时变化是正常的，时刻部分不是零点才是错的。
	apr, err := ParsePeriod("2026-04-01", "2026-04-30", "America/New_York", time.Now())
	require.NoError(t, err)
	cmpApr := DeriveCompare(apr, "America/New_York")
	require.Equal(t, "2026-03-01T00:00:00-05:00", cmpApr.Start.Format(time.RFC3339), "起点是 EST 零点")
	require.Equal(t, "2026-04-01T00:00:00-04:00", cmpApr.End.Format(time.RFC3339), "右开边界是 EDT 零点")
	require.Equal(t, "2026-03-01", cmpApr.StartDate)

	// 本期自身跨秋季回拨，退回等长紧邻分支：起点仍须落在自然日零点上
	fall, err := ParsePeriod("2026-10-15", "2026-11-13", "America/New_York", time.Now())
	require.NoError(t, err)
	cmpFall := DeriveCompare(fall, "America/New_York")
	require.Equal(t, "2026-09-15T00:00:00-04:00", cmpFall.Start.Format(time.RFC3339),
		"按墙上时长平移会得到 09-14T23:00，展示日期就会比真实起点早一天")
	require.Equal(t, "2026-09-15", cmpFall.StartDate)
	require.Equal(t, naturalDaysBetween(t, fall.Start, fall.End), naturalDaysBetween(t, cmpFall.Start, cmpFall.End),
		"自然日数相等（墙上时长因回拨差 1 小时，这是有意的）")
}

func TestCurBeyondRetention(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	cur, err := ParsePeriod("2026-05-16", "2026-08-15", "UTC", now)
	require.NoError(t, err)

	require.True(t, CurBeyondRetention(cur, 90, now), "起点 05-16 早于 90 天保留边界 05-17")
	require.False(t, CurBeyondRetention(cur, 92, now), "起点 05-16 晚于 92 天保留边界 05-15")
	require.False(t, CurBeyondRetention(cur, 0, now), "保留期未配置时不判越界")
}
