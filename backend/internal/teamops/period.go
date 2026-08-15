package teamops

import (
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// MaxRangeDays 是自定义区间的天数上限。既有先例是 handler 里 API Key 日用量的 90 天上限
// （maxAPIKeyDailyUsageDays）。这里取 92 天而不是更长：usage_logs 的保留期本身就是 90 天，
// 更长的区间只会让用户打出一个又慢又不全的查询。
const MaxRangeDays = 92

var (
	ErrRangeTooLong  = errors.New("date range exceeds 92 days")
	ErrRangeInverted = errors.New("start_date must not be after end_date")
)

const dateLayout = "2006-01-02"

// Period 是一个左闭右开的时间区间 [Start, End)。
type Period struct {
	Start     time.Time
	End       time.Time
	StartDate string
	EndDate   string
	Timezone  string
}

type PeriodPair struct {
	Cur                Period
	Prev               Period
	Comparable         bool
	IncomparableReason string
}

// ParsePeriod 解析 start_date / end_date，语义与 usage_handler.go 里用户用量的区间解析一致：
//   - 用 timezone.ParseInUserLocation 在用户时区解析（非法时区静默回落服务器时区，不报错）
//   - end_date 含当日：内部 +1 天作为右开边界
func ParsePeriod(startDate, endDate, userTZ string, now time.Time) (Period, error) {
	if startDate == "" || endDate == "" {
		// 默认本月 1 日 → 今天。时区回落语义与 timezone.ParseInUserLocation 一致：
		// userTZ 为空或非法时用服务器时区，不报错。
		n := now.In(timezone.Location())
		if userTZ != "" {
			if loc, err := time.LoadLocation(userTZ); err == nil {
				n = now.In(loc)
			}
		}
		startDate = time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, n.Location()).Format(dateLayout)
		endDate = n.Format(dateLayout)
	}

	start, err := timezone.ParseInUserLocation(dateLayout, startDate, userTZ)
	if err != nil {
		return Period{}, fmt.Errorf("invalid start_date: %w", err)
	}
	endDay, err := timezone.ParseInUserLocation(dateLayout, endDate, userTZ)
	if err != nil {
		return Period{}, fmt.Errorf("invalid end_date: %w", err)
	}
	if endDay.Before(start) {
		return Period{}, ErrRangeInverted
	}
	end := endDay.AddDate(0, 0, 1) // 右开边界：end_date 含当日

	// 按自然日比较而不是按时长：AddDate 是夏令时感知的，跨回拨那天有 25 小时，
	// 用 end.Sub(start) 与 92*24h 比会把一个合法的 92 个自然日区间误拒。
	if end.After(start.AddDate(0, 0, MaxRangeDays)) {
		return Period{}, ErrRangeTooLong
	}

	return Period{
		Start: start, End: end,
		StartDate: startDate, EndDate: endDate,
		Timezone: userTZ,
	}, nil
}

// DerivePrev 返回与 cur 等长、紧邻其前的区间。
func DerivePrev(cur Period, userTZ string) Period {
	d := cur.End.Sub(cur.Start)
	start := cur.Start.Add(-d)
	return Period{
		Start: start, End: cur.Start,
		StartDate: start.Format(dateLayout),
		EndDate:   cur.Start.Add(-time.Nanosecond).Format(dateLayout),
		Timezone:  userTZ,
	}
}

// ApplyRetention 判定上期是否越过保留边界。越界则 Comparable=false，环比一律不给数。
// 见设计文档 §4.5 认输规则：宁可承认算不出来，也不给一个看起来正常但是错的数字。
func ApplyRetention(p PeriodPair, retentionDays int, now time.Time) PeriodPair {
	if retentionDays <= 0 {
		p.Comparable = true
		return p
	}
	cutoff := now.AddDate(0, 0, -retentionDays)
	if p.Prev.Start.Before(cutoff) {
		p.Comparable = false
		p.IncomparableReason = "prev_period_beyond_retention"
	} else {
		p.Comparable = true
	}
	return p
}

// CurBeyondRetention 判定本期是否越界。用于对账条降级（设计文档 §4.5 第 4 条）。
func CurBeyondRetention(cur Period, retentionDays int, now time.Time) bool {
	if retentionDays <= 0 {
		return false
	}
	return cur.Start.Before(now.AddDate(0, 0, -retentionDays))
}
