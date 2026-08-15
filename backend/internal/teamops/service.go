package teamops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// defaultRetentionDays 是配置缺省时的保留窗口，与 dashboard_aggregation.retention.usage_logs_days
// 的默认值一致。配置读出 0 时不能当成「没有保留期」：那会让越界判定全线放行，
// 页面转而拿一段已被物理清理的区间算环比。
const defaultRetentionDays = 90

// ErrInvalidDate 标记 start_date / end_date 无法按 YYYY-MM-DD 解析。
//
// 解析层对格式错误只回 fmt.Errorf("invalid start_date: %w", err)，没有可判定的哨兵。
// handler 若靠错误文本前缀分类，那句措辞改一次，用户填错日期收到的就从 400 变成 500 ——
// 界面显示「服务器错误，请稍后重试」，而用户会拿着同一个错日期一直重试。
var ErrInvalidDate = errors.New("invalid date parameter")

// teamRepo 是 Service 依赖的仓储能力面。
//
// 之所以取接口而不是直接持有 *Repo：服务层负责的是对比区间推断、环比认输规则与
// 展示金额分配，这三件事出错时页面照常渲染，只是数字是错的。没有替身就只能靠集成测试
// 覆盖它们，而集成测试跑的是真库，钉不住「区间该取哪一段」这类纯逻辑结论。
type teamRepo interface {
	Summary(ctx context.Context, userID int64, cur, prev Period) (Summary, error)
	ListRows(ctx context.Context, q RowQuery) ([]Row, int64, error)
}

// Service 是团队消耗看板的服务层。
type Service struct {
	repo          teamRepo
	retentionDays int
	// now 只为让时间相关的结论（对比区间、保留期越界）在测试里可钉死，生产路径恒为 time.Now。
	now func() time.Time
}

// NewService 构造服务层。retentionDays 取自 dashboard_aggregation.retention.usage_logs_days。
func NewService(repo teamRepo, retentionDays int) *Service {
	if retentionDays <= 0 {
		retentionDays = defaultRetentionDays
	}
	return &Service{repo: repo, retentionDays: retentionDays, now: time.Now}
}

// PeriodDTO 是回显给界面的本期区间。Timezone 是**实际生效**的时区名，不是请求里的原始字符串。
type PeriodDTO struct {
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
	Timezone  string `json:"timezone"`
}

// CompareDTO 是对比区间。界面要把它逐字写出来（设计文档 §4.4），
// 所以 Comparable 为 false 时 Reason 必须给出机器可读的原因供渲染认输文案。
type CompareDTO struct {
	StartDate  string  `json:"start_date"`
	EndDate    string  `json:"end_date"`
	Comparable bool    `json:"comparable"`
	Reason     *string `json:"reason"`
}

// RetentionWarning 表示本期区间的起点早于保留边界，页面上的合计只是「可查消耗」而非全量。
type RetentionWarning struct {
	Kind    string `json:"kind"`     // period_partial
	CutDate string `json:"cut_date"` // 可查数据的起始日
}

// SummaryDTO 是 /user/team/summary 的响应体。
//
// TotalCost 是按分取整后的展示值，TotalCostRaw 是未取整原值：验收断言拿后者与
// /usage/stats 的合计逐位比对（设计文档 §10.3），取整会让那条恒等式在分位上失守。
type SummaryDTO struct {
	Period           PeriodDTO         `json:"period"`
	Compare          CompareDTO        `json:"compare"`
	TotalCost        float64           `json:"total_cost"`
	TotalCostRaw     float64           `json:"total_cost_raw"`
	PrevCost         float64           `json:"prev_cost"`
	DeltaPct         *float64          `json:"delta_pct"`
	DeltaAbs         *float64          `json:"delta_abs"`
	RowCount         int               `json:"row_count"`
	KeyCount         int               `json:"key_count"`
	OwnedKeyCount    int               `json:"owned_key_count"`
	TopRowCost       float64           `json:"top_row_cost"`
	RetentionWarning *RetentionWarning `json:"retention_warning"`
	Conclusion       *Conclusion       `json:"conclusion"`
}

// resolvePeriods 把请求里的日期参数解析成「本期 + 对比区间 + 是否可比」。
//
// 三件事必须一起做，缺任何一件都会产出一个看起来正常的错数字：
//
//   - TrimSpace：解析层对 " " 返回格式错误而不是走默认分支。少这一步，前端传个空格
//     就收到 400，而 /usage 的同名参数对同样的输入是正常的（它先 trim 再判空）。
//   - DeriveCompare 而不是 DerivePrev：后者只实现设计文档 §4.4 表格里「自定义」那一行。
//     用错的话默认视图「本月 08/01–08/15」会被拿去跟 07/17–07/31 比——数字正常、无报错，
//     但比的是错的东西，而界面上还会把那个错误的对比区间写出来。
//   - ApplyRetention：DeriveCompare 只算区间，不判它是否越过保留边界。漏掉它就会拿一段
//     可能已被物理清理的区间算环比，且不报任何错。它还会无条件重写 Comparable 与
//     IncomparableReason，所以任何其他不可比原因都只能叠在它**之后**。
func (s *Service) resolvePeriods(startDate, endDate, tz string, now time.Time) (PeriodPair, error) {
	cur, err := ParsePeriod(strings.TrimSpace(startDate), strings.TrimSpace(endDate), strings.TrimSpace(tz), now)
	if err != nil {
		if errors.Is(err, ErrRangeTooLong) || errors.Is(err, ErrRangeInverted) {
			return PeriodPair{}, err
		}
		return PeriodPair{}, fmt.Errorf("%w: %w", ErrInvalidDate, err)
	}
	return ApplyRetention(PeriodPair{Cur: cur, Prev: DeriveCompare(cur)}, s.retentionDays, now), nil
}

// Summary 给出看板顶部的概览。
func (s *Service) Summary(ctx context.Context, userID int64, startDate, endDate, tz string) (*SummaryDTO, error) {
	now := s.now()
	pair, err := s.resolvePeriods(startDate, endDate, tz, now)
	if err != nil {
		return nil, err
	}

	sum, err := s.repo.Summary(ctx, userID, pair.Cur, pair.Prev)
	if err != nil {
		return nil, err
	}

	dto := &SummaryDTO{
		Period: PeriodDTO{
			StartDate: pair.Cur.StartDate,
			EndDate:   pair.Cur.EndDate,
			Timezone:  pair.Cur.Timezone,
		},
		Compare: CompareDTO{
			StartDate:  pair.Prev.StartDate,
			EndDate:    pair.Prev.EndDate,
			Comparable: pair.Comparable,
		},
		TotalCost:     roundCents(sum.TotalCost),
		TotalCostRaw:  sum.TotalCost,
		PrevCost:      roundCents(sum.PrevCost),
		RowCount:      sum.RowCount,
		KeyCount:      sum.KeyCount,
		OwnedKeyCount: sum.OwnedKeyCount,
		TopRowCost:    roundCents(sum.TopRowCost),
	}

	if !pair.Comparable {
		reason := pair.IncomparableReason
		dto.Compare.Reason = &reason
	} else if dto.PrevCost > 0 {
		// 环比用展示值算，保证界面上「本期 − 上期 == 环比额」逐位成立。
		// 判分母也用展示值：上期不足一分时界面上的分母就是 0.00，
		// 此时给出的百分比既没有意义也无法被用户验证，一律认输。
		pct := (dto.TotalCost - dto.PrevCost) / dto.PrevCost * 100
		abs := roundCents(dto.TotalCost - dto.PrevCost)
		dto.DeltaPct = &pct
		dto.DeltaAbs = &abs
	}

	if CurBeyondRetention(pair.Cur, s.retentionDays, now) {
		dto.RetentionWarning = &RetentionWarning{
			Kind: "period_partial",
			// 切点按用户时区格式化，与 period / compare 里的日期同一口径。
			CutDate: now.In(pair.Cur.Start.Location()).AddDate(0, 0, -s.retentionDays).Format(dateLayout),
		}
	}

	return dto, nil
}

// Rows 给出当页的分组行与分组总行数。
func (s *Service) Rows(ctx context.Context, userID int64, startDate, endDate, tz, sort, order, q string, page, pageSize int) ([]Row, int64, error) {
	now := s.now()
	pair, err := s.resolvePeriods(startDate, endDate, tz, now)
	if err != nil {
		return nil, 0, err
	}

	rows, total, err := s.repo.ListRows(ctx, RowQuery{
		UserID:   userID,
		Cur:      pair.Cur,
		Prev:     pair.Prev,
		Sort:     sort,
		Order:    order,
		Q:        q,
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		return nil, 0, err
	}

	finalizeRows(rows, pair.Comparable)
	return rows, total, nil
}

// finalizeRows 把仓储层给出的原始行补成可直接渲染的行：先算环比，再把展示金额摊到分。
//
// 两步合成一个函数是为了让调用方没有把顺序写反的机会：分配会改写 CurrentCost，
// 若先分配再算环比，环比就建立在被人为摊过 ±1 分的金额上——一个只在特定金额组合下
// 出现的错百分比。
//
// 分配的分母取**当页**各行之和，而不是 /summary 的全量合计：前端只渲染当页，
// 用全量合计当分母会让差额变成「全量 − 当页」这么大的一坨，摊下去能把某一行摊成负数。
// 这里从 rows 自己算分母，调用方传不进错的那一个。
func finalizeRows(rows []Row, comparable bool) {
	values := make([]float64, len(rows))
	var pageTotal float64
	for i := range rows {
		values[i] = rows[i].CurrentCost
		pageTotal += rows[i].CurrentCost

		// 上期不可比、或上期为 0 时环比在数学上无定义，一律留空。
		// 填 0 除的结果会得到 ±Inf，而 Inf 在 encoding/json 里直接让整个响应序列化失败——
		// 页面白屏，日志里只有一行 "json: unsupported value"，看不出是哪一行数据引起的。
		if !comparable || rows[i].PrevCost <= 0 {
			continue
		}
		pct := (rows[i].CurrentCost - rows[i].PrevCost) / rows[i].PrevCost * 100
		abs := rows[i].CurrentCost - rows[i].PrevCost
		rows[i].DeltaPct = &pct
		rows[i].DeltaAbs = &abs
	}

	alloc := AllocateDisplay(pageTotal, values)
	for i := range rows {
		rows[i].CurrentCost = alloc[i]
	}
}
