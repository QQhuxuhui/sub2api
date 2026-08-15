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
	// ErrRangeTooLong 表示区间跨度超过 MaxRangeDays 个自然日。消息里的天数由常量插值，
	// 免得改了常量而消息还停在旧数字上。
	ErrRangeTooLong = fmt.Errorf("date range exceeds %d days", MaxRangeDays)
	// ErrRangeInverted 表示 start_date 晚于 end_date。同一天不算倒挂。
	ErrRangeInverted = errors.New("start_date must not be after end_date")
)

// dateLayout 是全站对外的日期格式，与 start_date / end_date 参数一致。
const dateLayout = "2006-01-02"

// Period 是一个左闭右开的时间区间 [Start, End)。
//
// 不变量：Start 与 End 都是同一时区里的自然日零点；End 是 end_date 次日零点（end_date 含当日）。
// 所有构造都经由 ParsePeriod / DerivePrev / DeriveCompare，它们保证这条不变量成立；
// 下游的日期推断（整月、月初至今等）依赖它。
//
// Timezone 记录**实际生效**的时区名，不是请求里传来的原始字符串：非法时区会静默回落到
// 服务器时区，此时回填原始字符串会让这个字段与 Start / End 描述的不是同一回事，
// 而界面要明写对比区间（设计文档 §4.4），字段一撒谎就又是一处对不上账。
type Period struct {
	Start     time.Time
	End       time.Time
	StartDate string
	EndDate   string
	Timezone  string
}

// PeriodPair 是「本期 + 对比区间」以及两者是否可比的结论。
// Comparable 为 false 时 IncomparableReason 给出机器可读的原因，供界面渲染认输文案；
// Comparable 为 true 时 IncomparableReason 必为空。
type PeriodPair struct {
	Cur                Period
	Prev               Period
	Comparable         bool
	IncomparableReason string
}

// ParsePeriod 解析 start_date / end_date，语义与 usage_handler.go 里用户用量的区间解析一致：
//   - 用 timezone.ParseInUserLocation 在用户时区解析（非法时区静默回落服务器时区，不报错）
//   - end_date 含当日：内部 +1 天作为右开边界
//   - 两侧各自独立补默认：起点缺则取本月 1 日，终点缺则取今天。只传一侧时，
//     用户显式传的那一侧必须原样保留，不能连带被默认值覆盖。
func ParsePeriod(startDate, endDate, userTZ string, now time.Time) (Period, error) {
	if startDate == "" || endDate == "" {
		n := nowInUserLocation(now, userTZ)
		if startDate == "" {
			startDate = time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, n.Location()).Format(dateLayout)
		}
		if endDate == "" {
			endDate = n.Format(dateLayout)
		}
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

	return newPeriod(start, end), nil
}

// nowInUserLocation 把 now 换算到用户时区。回落语义与 timezone.ParseInUserLocation 一致：
// userTZ 为空或非法时用服务器时区，不报错。
func nowInUserLocation(now time.Time, userTZ string) time.Time {
	if userTZ != "" {
		if loc, err := time.LoadLocation(userTZ); err == nil {
			return now.In(loc)
		}
	}
	return now.In(timezone.Location())
}

// naturalDays 返回 [start, end) 覆盖的自然日数。取两端各自的日历日期放到 UTC 里相减，
// UTC 没有夏令时，所以差值就是自然日数，不受区间内夏令时切换影响。
func naturalDays(start, end time.Time) int {
	s := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	e := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
	return int(e.Sub(s).Hours() / 24)
}

// newPeriod 用左闭右开的两个零点边界组装 Period。EndDate 按含当日口径取右开边界的前一天，
// Timezone 取边界实际所在的时区，保证这三者描述的是同一件事。
func newPeriod(start, end time.Time) Period {
	return Period{
		Start: start, End: end,
		StartDate: start.Format(dateLayout),
		EndDate:   end.AddDate(0, 0, -1).Format(dateLayout),
		Timezone:  start.Location().String(),
	}
}

// DerivePrev 返回与 cur 自然日数相等、紧邻其前的区间。时区从 cur 推导，不另外接收参数：
// 计算本来就只能在 cur 自己的时区里做，多一个时区入参只会让调用方传出自相矛盾的 Period。
//
// 平移按自然日走（AddDate 是夏令时感知的），不按墙上时长走：界面上要明写对比区间
// （设计文档 §4.4），用户读的是自然日期。按时长平移时，跨夏令时的区间起点会落在 23:00 或
// 01:00 这类非零点上，展示出来的日期与真实查询边界能差一天。代价是跨夏令时的两段墙上时长
// 相差 1 小时，这比显示一个错误日期轻。
func DerivePrev(cur Period) Period {
	days := naturalDays(cur.Start, cur.End)
	return newPeriod(cur.Start.AddDate(0, 0, -days), cur.Start)
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
// 形态推断有三处天然二义。日期形态相同、意图不同，后端无从区分，只能定死一种解释；
// 三条裁决各有一条测试钉住（TestDeriveCompare_FirstDayOfMonthUsesMonthRule、
// TestDeriveCompare_ShortWindowStartingOnFirstUsesMonthRule、
// TestDeriveCompare_MonthEndTreatedAsFullMonth），调整分支顺序会让它们变红：
//
//  1. 某月 1 日当天（08-01–08-01）：既是「月初至今」也只有 1 天。按月规则给上月 1 日，
//     而不是前一日规则的上月最后一天，与默认视图是本月保持一致。
//  2. 近 N 天的起点正好撞上某月 1 日（08-01–08-07 这种 7 天窗口）：按月规则给上月 1–7 日，
//     而不是紧邻的上月 25–31 日。7 / 14 / 30 天窗口分别在每月 7、14、30 号触发，每月约 3 天。
//  3. 月末的「本月」退化成整月形态（04-01–04-30）：按整月规则给上月整月，
//     而不是 §4.4「本月」行字面的「上月 1 日至同一天」（3 月 1–30 日）。否则与「上月」行的
//     语义冲突——同一个形态不可能既算本月又算上月。
func DeriveCompare(cur Period) Period {
	loc := cur.Start.Location()
	lastDay := cur.End.AddDate(0, 0, -1) // 含当日口径下的最后一天零点

	if cur.Start.Day() == 1 && cur.Start.Year() == lastDay.Year() && cur.Start.Month() == lastDay.Month() {
		prevFirst := cur.Start.AddDate(0, -1, 0)
		if lastDay.Day() == daysInMonth(cur.Start) {
			// 整月：右开边界正是本月 1 日
			return newPeriod(prevFirst, cur.Start)
		}
		day := lastDay.Day()
		if dim := daysInMonth(prevFirst); day > dim {
			day = dim
		}
		prevLast := time.Date(prevFirst.Year(), prevFirst.Month(), day, 0, 0, 0, 0, loc)
		return newPeriod(prevFirst, prevLast.AddDate(0, 0, 1))
	}

	return DerivePrev(cur)
}

// daysInMonth 返回 t 所在月的天数。
func daysInMonth(t time.Time) int {
	first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	return first.AddDate(0, 1, 0).AddDate(0, 0, -1).Day()
}

// ApplyRetention 判定上期是否越过保留边界。越界则 Comparable=false，环比一律不给数。
// 见设计文档 §4.5 认输规则：宁可承认算不出来，也不给一个看起来正常但是错的数字。
//
// 反过来同样是故障：上期明明在保留期内却不给环比，上线后没人会报 bug——用户只会以为
// 本来就没这个数。所以两侧都要判准，且判定为可比时必须清掉原因字段，
// 不留「Comparable=true 却带着原因」这种自相矛盾的状态。
func ApplyRetention(p PeriodPair, retentionDays int, now time.Time) PeriodPair {
	if retentionDays <= 0 {
		// 保留期未配置：无从判定越界，按可比处理
		p.Comparable = true
		p.IncomparableReason = ""
		return p
	}
	cutoff := now.AddDate(0, 0, -retentionDays)
	if p.Prev.Start.Before(cutoff) {
		p.Comparable = false
		p.IncomparableReason = "prev_period_beyond_retention"
	} else {
		p.Comparable = true
		p.IncomparableReason = ""
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
