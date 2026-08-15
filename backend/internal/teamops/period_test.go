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

func TestDerivePrev_EqualLengthAdjacent(t *testing.T) {
	t.Parallel()
	cur, err := ParsePeriod("2026-08-01", "2026-08-15", "Asia/Shanghai", time.Now())
	require.NoError(t, err)
	prev := DerivePrev(cur, "Asia/Shanghai")
	require.Equal(t, cur.End.Sub(cur.Start), prev.End.Sub(prev.Start), "上期与本期等长")
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

func TestCurBeyondRetention(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	cur, err := ParsePeriod("2026-05-16", "2026-08-15", "UTC", now)
	require.NoError(t, err)

	require.True(t, CurBeyondRetention(cur, 90, now), "起点 05-16 早于 90 天保留边界 05-17")
	require.False(t, CurBeyondRetention(cur, 92, now), "起点 05-16 晚于 92 天保留边界 05-15")
	require.False(t, CurBeyondRetention(cur, 0, now), "保留期未配置时不判越界")
}
