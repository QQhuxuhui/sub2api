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
// 不变量：Start 与 End 都是同一时区里某个自然日的**首个有效时刻**；End 是 end_date 次日的
// 首个有效时刻（end_date 含当日）。所有构造都经由 ParsePeriod / DerivePrev / DeriveCompare，
// 它们保证这条不变量成立；下游的日期推断（整月、月初至今等）依赖它。
//
// 「首个有效时刻」通常就是本地零点，但不能写死成零点：America/Santiago、America/Havana
// 这类时区的夏令时前拨正好落在 00:00，那一天的 00:00–01:00 整段不存在，time.Date 会把
// 不存在的零点归一到**前一天** 23:00。写死零点的话，查 2026-09-06（Santiago）会从 09-05
// 23:00 开始（多查前一天一小时），右开边界又落在 09-06 23:00（漏掉请求日最后一小时），
// 而且展示出来的日期还会比真实边界早一天。构造统一走 dayStart。
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
//   - 在用户时区解析（非法时区静默回落服务器时区，不报错）
//   - end_date 含当日：内部 +1 天作为右开边界
//   - 两侧各自独立补默认：起点缺则取本月 1 日，终点缺则取今天。只传一侧时，
//     用户显式传的那一侧必须原样保留，不能连带被默认值覆盖。
//
// 日期字符串先按自然日解析（civilDay 表示，见下），日历加减做完之后才由 dayStart
// 落到用户时区的具体时刻——顺序反过来就会在「午夜不存在」的时区里把边界算歪。
func ParsePeriod(startDate, endDate, userTZ string, now time.Time) (Period, error) {
	loc := userLocation(userTZ)
	if startDate == "" || endDate == "" {
		n := now.In(loc)
		if startDate == "" {
			startDate = time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, time.UTC).Format(dateLayout)
		}
		if endDate == "" {
			endDate = n.Format(dateLayout)
		}
	}

	startDay, err := parseCivilDay(startDate)
	if err != nil {
		return Period{}, fmt.Errorf("invalid start_date: %w", err)
	}
	endDay, err := parseCivilDay(endDate)
	if err != nil {
		return Period{}, fmt.Errorf("invalid end_date: %w", err)
	}
	if endDay.Before(startDay) {
		return Period{}, ErrRangeInverted
	}
	endDay = endDay.AddDate(0, 0, 1) // 右开边界：end_date 含当日

	// 按自然日比较而不是按墙上时长：跨夏令时的区间会多/少一小时，
	// 用 End.Sub(Start) 与 92*24h 比会把一个合法的 92 个自然日区间误拒。
	if civilDays(startDay, endDay) > MaxRangeDays {
		return Period{}, ErrRangeTooLong
	}

	return newPeriod(startDay, endDay, loc), nil
}

// userLocation 解析用户时区。回落语义与 timezone.ParseInUserLocation 一致：
// userTZ 为空或非法时用服务器时区，不报错。
func userLocation(userTZ string) *time.Location {
	if userTZ != "" {
		if loc, err := time.LoadLocation(userTZ); err == nil {
			return loc
		}
	}
	return timezone.Location()
}

// civilDay 把一个时刻按它在自己时区里的日历日期，表示成 UTC 零点的 time.Time。
//
// 本文件所有日历加减都在这个表示上做，理由与 service.go 的 retentionCutDate 一样：
// UTC 没有夏令时，加减一天/一个月永远不会撞上「本地那个时刻不存在」而被 time.Date
// 悄悄归一到相邻时刻。落回真实时区这一步只在 dayStart 里做一次。
func civilDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// parseCivilDay 按 YYYY-MM-DD 解析一个自然日。解析放在 UTC 里做、只取日历字段：
// 直接在用户时区里解析的话，「午夜不存在」的那一天在解析这一步就已经被归一到前一天了。
func parseCivilDay(s string) (time.Time, error) {
	return time.Parse(dateLayout, s)
}

// civilDays 返回 [from, to) 覆盖的自然日数，两端都是 civilDay 的表示。
func civilDays(from, to time.Time) int {
	return int(to.Sub(from).Hours() / 24)
}

// dayStart 返回 day 这个自然日在 loc 里的**首个有效时刻**。
//
// 绝大多数情况下就是本地零点。零点不存在时（夏令时前拨正好发生在 00:00，
// 如 America/Santiago 2026-09-06、America/Havana 2026-03-08），time.Date 会把它归一到
// 缺口**之前**那一侧（前一天 23:00）；那个时刻所在时区区间的结束时刻正是缺口的另一端，
// 也就是这一天真正的第一个时刻（Santiago：2026-09-06T01:00:00-03:00）。
func dayStart(day time.Time, loc *time.Location) time.Time {
	y, m, d := day.Date()
	t := time.Date(y, m, d, 0, 0, 0, 0, loc)
	if ty, tm, td := t.Date(); ty == y && tm == m && td == d {
		return t
	}
	if _, end := t.ZoneBounds(); !end.IsZero() && end.After(t) {
		// 整天都被跳过的极端时区（Pacific/Apia 2011-12-30）在这里会得到下一天的起点，
		// 于是 [Start, End) 退化成空区间 —— 那一天确实不存在，查不出数据才是对的。
		return end
	}
	// 兜底：ZoneBounds 给不出后界（此后再无跳变）。现实 tzdata 里走不到，
	// 零点不存在必然意味着紧接着有一次跳变。
	return t
}

// newPeriod 用左闭右开的两个自然日（civilDay 表示，endDay 为右开）组装 Period。
// 展示用的 StartDate / EndDate 直接取自自然日本身而不是回算时刻：时刻在缺口日上是 01:00，
// 再用 AddDate 去回推日期又会踩一次同样的归一化。
func newPeriod(startDay, endDay time.Time, loc *time.Location) Period {
	return Period{
		Start:     dayStart(startDay, loc),
		End:       dayStart(endDay, loc),
		StartDate: startDay.Format(dateLayout),
		EndDate:   endDay.AddDate(0, 0, -1).Format(dateLayout),
		Timezone:  loc.String(),
	}
}

// DerivePrev 返回与 cur 自然日数相等、紧邻其前的区间。时区从 cur 推导，不另外接收参数：
// 计算本来就只能在 cur 自己的时区里做，多一个时区入参只会让调用方传出自相矛盾的 Period。
//
// 平移按自然日走（日历加减在 UTC 里做完再由 dayStart 落回本地），不按墙上时长走：界面上要明写对比区间
// （设计文档 §4.4），用户读的是自然日期。按时长平移时，跨夏令时的区间起点会落在 23:00 或
// 01:00 这类非零点上，展示出来的日期与真实查询边界能差一天。代价是跨夏令时的两段墙上时长
// 相差 1 小时，这比显示一个错误日期轻。
func DerivePrev(cur Period) Period {
	loc := cur.Start.Location()
	first := civilDay(cur.Start)
	days := civilDays(first, civilDay(cur.End))
	return newPeriod(first.AddDate(0, 0, -days), first, loc)
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
	first := civilDay(cur.Start)
	lastDay := civilDay(cur.End).AddDate(0, 0, -1) // 含当日口径下的最后一天

	if first.Day() == 1 && first.Year() == lastDay.Year() && first.Month() == lastDay.Month() {
		prevFirst := first.AddDate(0, -1, 0)
		if lastDay.Day() == daysInMonth(first) {
			// 整月：右开边界正是本月 1 日
			return newPeriod(prevFirst, first, loc)
		}
		day := lastDay.Day()
		if dim := daysInMonth(prevFirst); day > dim {
			day = dim
		}
		prevLast := time.Date(prevFirst.Year(), prevFirst.Month(), day, 0, 0, 0, 0, time.UTC)
		return newPeriod(prevFirst, prevLast.AddDate(0, 0, 1), loc)
	}

	return DerivePrev(cur)
}

// daysInMonth 返回 day（civilDay 表示）所在月的天数。加减都在 UTC 里做，
// 否则本地时区里 +1 月 −1 天有可能落进夏令时缺口，被归一之后少数出一天。
func daysInMonth(day time.Time) int {
	first := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
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
