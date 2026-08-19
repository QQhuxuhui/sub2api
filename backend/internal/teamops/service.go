package teamops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// defaultRetentionDays 是「清理作业开着、但天数没配出来」时的兜底窗口，与
// dashboard_aggregation.retention.usage_logs_days 的默认值一致。清理开着而窗口读成 0
// 不能当成「没有保留期」：那会让越界判定全线放行，页面转而拿一段已被物理清理的区间算环比。
//
// 反过来，清理**没开**时 0 就是字面意思——日志没人删，什么都不该限。这两种 0 的区别
// 由 NewService 的 retentionEnabled 参数携带，不能只看天数。
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
	EnrichRows(ctx context.Context, q EnrichQuery) (map[string]RowEnrichment, error)
}

// Service 是团队消耗看板的服务层。
type Service struct {
	repo teamRepo
	// retentionDays 是**生效**的保留窗口天数。0 表示没有保留期限制（历史日志是完整的），
	// 这层语义一路传到 ApplyRetention / CurBeyondRetention，别再在中间被改写成默认值。
	retentionDays int
	// now 只为让时间相关的结论（对比区间、保留期越界）在测试里可钉死，生产路径恒为 time.Now。
	now func() time.Time
}

// NewService 构造服务层。
//
// retentionDays 取自 dashboard_aggregation.retention.usage_logs_days；
// retentionEnabled 取自 dashboard_aggregation.enabled —— 保留清理是聚合作业的一部分，
// 作业不跑就没人删 usage_logs。
//
// 两个参数必须一起看：config.go 的整个 retention 校验块都包在 `if c.DashboardAgg.Enabled` 里，
// 关闭时 usage_logs_days 压根不校验（可以是 0，也可能停在 viper 默认值 90）。
// 只看天数的话，关闭聚合的部署会被按 90 天砍——页面藏掉环比、打出「数据不完整」的提示条，
// 而实际上全部日志都在。那种「少给了数」的故障上线后没人会报，用户只会以为本来就没这个数。
func NewService(repo teamRepo, retentionDays int, retentionEnabled bool) *Service {
	switch {
	case !retentionEnabled:
		retentionDays = 0 // 清理不跑：日志完整，无限制
	case retentionDays <= 0:
		retentionDays = defaultRetentionDays // 清理在跑却没给窗口：宁可保守，按默认窗口判越界
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
	DeletedKeyCount  int               `json:"deleted_key_count"`
	OwnedKeyCount    int               `json:"owned_key_count"`
	TopRowCost       float64           `json:"top_row_cost"`
	RetentionWarning *RetentionWarning `json:"retention_warning"`
	Conclusion       *Conclusion       `json:"conclusion"`

	// Requests / PrevRequests 早就在仓储层算着了，只是此前没下发。概览条要拿它算日均，
	// 也是「平均单次」那一格的分母。
	Requests     int64 `json:"requests"`
	PrevRequests int64 `json:"prev_requests"`
	// ActiveRowCount 是概览条「活跃成员 6/8」的分子，分母是 RowCount。
	ActiveRowCount int `json:"active_row_count"`
	// CacheHitRate 是 token 口径的全量命中率（0~100），分母为 0 时 nil，与行级同一份算法。
	CacheHitRate *float64 `json:"cache_hit_rate"`
	// CacheSavedCost 是**近似**的缓存节省额，见 Summary.CacheSavedCost 的口径说明。
	// 这里不走 roundCents：它没有任何「Σ 各行 == 总额」式的恒等式要维持，
	// 而取整会把不足一分的节省显示成 0.00、把 -0.004 序列化成 "-0"。格式化交给前端。
	CacheSavedCost float64 `json:"cache_saved_cost"`
}

// cacheHitRate 是缓存命中率的唯一一份算法，概览条与表格行共用（与 displayDelta 同一个理由：
// 两处分叉过一次就会有两个都说得通、但对不上的数字）。
//
// token 口径而不是请求口径：一次请求可以既读缓存、写缓存又吃新输入，按请求数算等于把
// 「读了 90% 缓存」的请求算成一次完整命中。
// cache_creation_tokens 是未命中的 prompt 写入，必须进入分母；漏掉它会系统性高估
// Anthropic 等把缓存写入单列返回的模型。
//
// 分母为 0 时返回 nil 而不是 0：「本期没有任何输入 token」与「命中率 0%」是两回事，
// 前者该在界面上显示「—」。返回 0 会让一个从没发过请求的成员显示成「缓存命中 0%」，
// 读起来像是他把缓存用坏了。
func cacheHitRate(inputTokens, cacheCreationTokens, cacheReadTokens int64) *float64 {
	denom := inputTokens + cacheCreationTokens + cacheReadTokens
	if denom <= 0 {
		return nil
	}
	rate := 100 * float64(cacheReadTokens) / float64(denom)
	return &rate
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
		TotalCost:       roundCents(sum.TotalCost),
		TotalCostRaw:    sum.TotalCost,
		PrevCost:        roundCents(sum.PrevCost),
		RowCount:        sum.RowCount,
		KeyCount:        sum.KeyCount,
		DeletedKeyCount: sum.DeletedKeyCount,
		OwnedKeyCount:   sum.OwnedKeyCount,
		TopRowCost:      roundCents(sum.TopRowCost),
		Requests:        sum.Requests,
		PrevRequests:    sum.PrevRequests,
		ActiveRowCount:  sum.ActiveRowCount,
		CacheHitRate:    cacheHitRate(sum.InputTokens, sum.CacheCreationTokens, sum.CacheReadTokens),
		CacheSavedCost:  sum.CacheSavedCost,
	}

	if !pair.Comparable {
		reason := pair.IncomparableReason
		dto.Compare.Reason = &reason
	} else if pct, abs, ok := displayDelta(sum.TotalCost, sum.PrevCost); ok {
		dto.DeltaPct = &pct
		dto.DeltaAbs = &abs
	}

	if CurBeyondRetention(pair.Cur, s.retentionDays, now) {
		dto.RetentionWarning = &RetentionWarning{
			Kind: "period_partial",
			// 切点与 CurBeyondRetention 判定用的是**同一个表达式**算出的同一个时刻。
			CutDate: retentionCutDate(now.AddDate(0, 0, -s.retentionDays), pair.Cur.Start.Location()),
		}
	}

	return dto, nil
}

// displayDelta 是环比的唯一一份算法，概览条（Summary）与表格行（finalizeRows）共用。
//
// 三件事全都用**展示值**（各自 roundCents 到分）：
//
//   - 算百分比与环比额：保证界面上「本期 − 上期 == 环比额」逐位成立；
//   - 判分母是否为零：上期不足一分时界面上的分母就是 0.00，此时给出的百分比
//     既没有意义也无法被用户验证，一律认输；
//   - 环比额再取一次整：不取整会把 7042.857142857143 这种数原样塞进 JSON。
//
// 两处必须同源。曾经汇总走展示值、行级走未取整原值，于是 prev_raw=0.004 时
// 页面顶部写着「上期 $0.00 / 环比 —」，而唯一那一行写着「+24900%」；prev_raw=0.005
// 时两个数整整差 2 倍（9900 对 19900）。而设计文档 §6.1 的异常判定第一通道是
// 「环比 ≥ +100%」，这种数会被选成结论句 Top1 顶到概览条上。
//
// ok=false 表示认输，调用方须保持 DeltaPct / DeltaAbs 为 nil：填 0 除的结果是 ±Inf，
// 而 Inf 在 encoding/json 里直接让整个响应序列化失败——页面白屏。
func displayDelta(cur, prev float64) (pct, abs float64, ok bool) {
	c, p := roundCents(cur), roundCents(prev)
	if p <= 0 {
		return 0, 0, false
	}
	return (c - p) / p * 100, roundCents(c - p), true
}

// retentionCutDate 把保留边界那个**时刻**换算成提示条上写的那个**日期**：
// 用户时区里第一个完整可查的自然日。
//
// 直接取边界时刻所在的自然日是不行的：判定比的是时刻（本期起点早于边界即告警），
// 而边界落在那一天的中途，那天只剩半天数据。于是本期起点恰好是边界当天时，
// 提示条会写成「本期 05-17 – 08-15 / 可查数据自 05-17 起」——自相矛盾，
// 而且那半天的数据确实是缺的。向上取到下一个完整自然日之后，
// 告警成立时 cut date 必定晚于本期起点，提示条和判定说的才是同一件事。
//
// 换算在**用户时区**里做，与 period / compare 里的日期同一口径：跨零点时
// 同一个边界时刻在两个时区里落在不同的自然日上。
//
// 「加一天」这一步必须放到 UTC 里做，理由与 period.go 的 civilDays 一样：UTC 没有
// 夏令时。在本地时区上 AddDate 会踩到**午夜不存在**的时区——America/Santiago
// 2025-09-07 的 00:00 直接跳到 01:00，time.Date 把它规范化成前一天 23:00，
// 于是「加一天」加完还落在同一个自然日上，取整白做，自相矛盾的提示条原样复现。
// 判定「本地零点是否早于边界」仍在本地时区做，那是本函数要回答的问题本身。
func retentionCutDate(cutoff time.Time, loc *time.Location) string {
	c := cutoff.In(loc)
	day := time.Date(c.Year(), c.Month(), c.Day(), 0, 0, 0, 0, time.UTC)
	if midnight := time.Date(c.Year(), c.Month(), c.Day(), 0, 0, 0, 0, loc); midnight.Before(c) {
		day = day.AddDate(0, 0, 1)
	}
	return day.Format(dateLayout)
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
	s.enrichRows(ctx, userID, pair.Cur, rows)
	return rows, total, nil
}

// enrichRows 用当页补充查询把 TopModel / TopModelShare / Daily 填上。
//
// **失败一律降级，不往上抛**：这三个维度是锦上添花，而 ListRows 那一页金额、环比、
// 请求数才是这个页面的本体。补充查询超时就整页 500 的话，一次慢查询能让用户连
// 「谁花了多少钱」都看不到 —— 而那正是他打开这个页面要问的问题。
//
// 降级的形态要与「查到了但是零消耗」区分得开：
//   - 查成功、该组本期没花钱 → Daily 是一串 0（长度仍等于本期天数），前端画一条贴底的线；
//   - 查失败 → Daily 是**空数组**，前端据此不画线。画一条平的假线会让人以为
//     这个人本期真的一分没花。
//
// 两种情况下 TopModel 都是 nil，前端一律渲染「—」。
func (s *Service) enrichRows(ctx context.Context, userID int64, cur Period, rows []Row) {
	labels := dailyLabels(cur)

	enriched := map[string]RowEnrichment{}
	ok := false
	if keyIDs := pageKeyIDs(rows); len(keyIDs) > 0 {
		got, err := s.repo.EnrichRows(ctx, EnrichQuery{UserID: userID, KeyIDs: keyIDs, Cur: cur})
		if err == nil {
			enriched, ok = got, true
		}
	}

	for i := range rows {
		if !ok {
			rows[i].Daily = []float64{}
			continue
		}
		e := enriched[rows[i].GroupKey] // 缺键取零值：该组本期没有任何日志
		rows[i].Daily = spreadDaily(e.Daily, labels)
		if model, share, hasTop := topModel(e.Models); hasTop {
			rows[i].TopModel = &model
			rows[i].TopModelShare = &share
		}
	}
}

// pageKeyIDs 把当页各行的令牌集合并成一份去重后的入参。
//
// 去重不是洁癖：同一把令牌出现两次会让 usage_logs ⋈ g 那一步把它的日志各算两遍，
// 主力模型占比与趋势线直接翻倍。理论上 groupKeyExpr 保证一把令牌只落在一个分组里，
// 但那是另一处代码的推论，这里花一个 map 把它变成本函数自己的保证。
func pageKeyIDs(rows []Row) []int64 {
	seen := map[int64]struct{}{}
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		for _, id := range r.KeyIDs {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

// topModel 从逐模型消耗里选出主力模型与它的占比（0~100）。
//
// 总额 <= 0 时认输（hasTop=false）：分组本期没花钱时「花得最多的模型」不是一个有意义的
// 问题，而占比的分母就是那个 0 —— 算出来是 NaN 或 ±Inf，两者在 encoding/json 里
// 都直接让整个响应序列化失败，页面白屏。
//
// 并列时取模型名字典序最小的那个。并列在真实数据里不常见，但一旦发生，
// 不定序会让同一页刷新两次显示两个不同的主力模型 —— 用户会当成 bug 报上来，
// 而这种「每次都不一样」的现象查起来极贵。
func topModel(models map[string]float64) (name string, share float64, hasTop bool) {
	var top float64
	var total float64
	for m, c := range models {
		total += c
		if name == "" || c > top || (c == top && m < name) {
			name, top = m, c
		}
	}
	if total <= 0 {
		return "", 0, false
	}
	return name, 100 * top / total, true
}

// dailyLabels 给出本期逐自然日的日期标签。天数口径与 DerivePrev 用的是同一对
// civilDay / civilDays —— 「本期有几个自然日」在本包只能有一个答案。
//
// 标签与仓储层日桶都取**用户时区**里的自然日（与 period.start_date 同一口径），
// 因此前端拿下标对日期时是一一对应的。
func dailyLabels(cur Period) []string {
	first := civilDay(cur.Start)
	n := civilDays(first, civilDay(cur.End))
	if n <= 0 {
		return nil
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, first.AddDate(0, 0, i).Format(dateLayout))
	}
	return out
}

// spreadDaily 把用户时区的日桶映射到同一时区的日期标签，返回与 labels 等长的数组。
// 仓储层已经按本期边界过滤且用 cur.Timezone 分桶，因此正常返回的每个键都应命中 labels。
// 越界键视为脏数据并忽略，不能夹进首尾格，否则一个点会混合两个不同自然日。
func spreadDaily(buckets map[string]float64, labels []string) []float64 {
	out := make([]float64, len(labels))
	if len(labels) == 0 {
		return out
	}
	idx := make(map[string]int, len(labels))
	for i, d := range labels {
		idx[d] = i
	}
	for day, v := range buckets {
		if i, hit := idx[day]; hit {
			out[i] += v
		}
	}
	return out
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

		// 平均单次与环比一样，用的是**分配前**的金额。放在下面那个分配循环里算的话，
		// 分子会是被人为摊过 ±1 分的展示值：请求数只有个位数时，那 1 分足以让
		// 「显著高于全局平均」的标黄判定翻面。Requests==0 时留 0，不做除法。
		if rows[i].Requests > 0 {
			rows[i].AvgCost = rows[i].CurrentCost / float64(rows[i].Requests)
		}
		rows[i].CacheHitRate = cacheHitRate(
			rows[i].InputTokens, rows[i].CacheCreationTokens, rows[i].CacheReadTokens,
		)

		// 上期不可比时环比一律留空。上期为 0（含「不足一分、展示成 0.00」）
		// 的认输判定在 displayDelta 里，与概览条同一份算法。
		if !comparable {
			continue
		}
		pct, abs, ok := displayDelta(rows[i].CurrentCost, rows[i].PrevCost)
		if !ok {
			continue
		}
		rows[i].DeltaPct = &pct
		rows[i].DeltaAbs = &abs
	}

	alloc := AllocateDisplay(pageTotal, values)
	for i := range rows {
		rows[i].CurrentCost = alloc[i]
	}
}
