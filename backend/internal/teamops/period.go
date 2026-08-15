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

// naturalDays 返回 [start, end) 覆盖的自然日数。取两端各自的日历日期放到 UTC 里相减，
// UTC 没有夏令时，所以差值就是自然日数，不受区间内夏令时切换影响。
func naturalDays(start, end time.Time) int {
	s := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	e := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
	return int(e.Sub(s).Hours() / 24)
}

// newPeriod 用左闭右开的两个零点边界组装 Period，EndDate 取右开边界的前一天（含当日口径）。
func newPeriod(start, end time.Time, userTZ string) Period {
	return Period{
		Start: start, End: end,
		StartDate: start.Format(dateLayout),
		EndDate:   end.AddDate(0, 0, -1).Format(dateLayout),
		Timezone:  userTZ,
	}
}

// DerivePrev 返回与 cur 自然日数相等、紧邻其前的区间。
//
// 平移按自然日走（AddDate 是夏令时感知的），不按墙上时长走：界面上要明写对比区间
// （设计文档 §4.4），用户读的是自然日期。按时长平移时，跨夏令时的区间起点会落在 23:00 或
// 01:00 这类非零点上，展示出来的日期与真实查询边界能差一天。代价是跨夏令时的两段墙上时长
// 相差 1 小时，这比显示一个错误日期轻。
func DerivePrev(cur Period, userTZ string) Period {
	days := naturalDays(cur.Start, cur.End)
	return newPeriod(cur.Start.AddDate(0, 0, -days), cur.Start, userTZ)
}

// DeriveCompare 按设计文档 §4.4 的规则给出对比区间。后端只收到 start_date / end_date、
// 收不到前端的 preset 名，所以周期类型只能从日期形态推断：
//
//   - 整月（某月 1 日 → 该月最后一天）：上月整月。
//   - 月初至今（某月 1 日 → 同月某天）：上月 1 日 → 上月同一天；上月没有那一天则截断到上月最后一天。
//   - 其余（自定义区间、以及恰好 1 个自然日的「今天」）：等长紧邻，即 DerivePrev。
//     单日不单列分支：等长紧邻对 1 天的区间给出的就是前一个自然日，与 §4.4 的「昨天全天」一致。
//     行为由 TestDeriveCompare_SingleDayUsesPreviousDay 钉住，DerivePrev 若改语义会立刻变红。
//
// 整月必须先判、且不能靠「同一天 + 截断」来代替：上月比本月长时两者结果不同，
// 比如 4 月整月按「同一天」会得到 3 月 1–30 日，而正确答案是 3 月整月。
//
// 某月 1 日当天（比如 08-01–08-01）既是「月初至今」也只有 1 天，两种口径的答案不同：
// 月规则给上月 1 日，前一日规则给上月最后一天。这里按月规则处理，与默认视图是本月保持一致。
func DeriveCompare(cur Period, userTZ string) Period {
	loc := cur.Start.Location()
	lastDay := cur.End.AddDate(0, 0, -1) // 含当日口径下的最后一天零点

	if cur.Start.Day() == 1 && cur.Start.Year() == lastDay.Year() && cur.Start.Month() == lastDay.Month() {
		prevFirst := cur.Start.AddDate(0, -1, 0)
		if lastDay.Day() == daysInMonth(cur.Start) {
			// 整月：右开边界正是本月 1 日
			return newPeriod(prevFirst, cur.Start, userTZ)
		}
		day := lastDay.Day()
		if dim := daysInMonth(prevFirst); day > dim {
			day = dim
		}
		prevLast := time.Date(prevFirst.Year(), prevFirst.Month(), day, 0, 0, 0, 0, loc)
		return newPeriod(prevFirst, prevLast.AddDate(0, 0, 1), userTZ)
	}

	return DerivePrev(cur, userTZ)
}

// daysInMonth 返回 t 所在月的天数。
func daysInMonth(t time.Time) int {
	first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	return first.AddDate(0, 1, 0).AddDate(0, 0, -1).Day()
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
