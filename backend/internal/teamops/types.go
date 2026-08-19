package teamops

import "time"

// Row 是表格里的一行：要么是一个归属（合并了多把令牌），要么是一把没设归属的令牌。
//
// DeltaPct / DeltaAbs / IsAnomaly 不由仓储层填充。环比与异常判定要用到「上期是否可比」
// （见 period.go 的 ApplyRetention），那是服务层的结论，SQL 里算不出来。
// 同理 AvgCost / CacheHitRate / TopModel / TopModelShare / Daily 也都是服务层填的：
// 前两个由仓储层给出的原始分子分母算出（算法与概览条共用一份），
// 后三个来自 EnrichRows 这条**当页补充查询**，且允许整条失败而降级。
//
// ⚠️ 金额字段的口径不对称，前端不能混用：
//   - CurrentCost 是**展示值**，已按当页做过最大余数法分配（见 money.go 的 AllocateDisplay），
//     保证 Σ 各行 == 对账条总额。代价是单行金额与原值最多差 1 分。
//   - PrevCost 是**原值**，没有做过分配（分配只对当期做，上期不上对账条）。
//   - DeltaAbs / DeltaPct 由**分配前金额的展示值**算出（两侧各自 roundCents 到分，
//     见 service.go 的 displayDelta），与概览条上的环比同一份算法。
//
// 所以 CurrentCost − PrevCost 与 DeltaAbs 可以差 1~2 分。前端要显示上期金额就自己
// 格式化 PrevCost，**不要拿它与 CurrentCost 做减法**，减出来的数与 DeltaAbs 对不上。
type Row struct {
	GroupKey    string  `json:"group_key"`
	DisplayName string  `json:"display_name"`
	ByOwner     bool    `json:"by_owner"`
	KeyCount    int     `json:"key_count"`
	KeyCountAll int     `json:"key_count_all"`
	AllDeleted  bool    `json:"all_deleted"`
	MaskedKey   *string `json:"masked_key"`

	CurrentCost  float64    `json:"current_cost"`
	PrevCost     float64    `json:"prev_cost"`
	DeltaPct     *float64   `json:"delta_pct"`
	DeltaAbs     *float64   `json:"delta_abs"`
	Requests     int64      `json:"requests"`
	PrevRequests int64      `json:"prev_requests"`
	LastUsedAt   *time.Time `json:"last_used_at"`
	IsAnomaly    bool       `json:"is_anomaly"`

	// AvgCost 是本期平均单次消耗，由**分配前**的 CurrentCost 除以 Requests 得出。
	// 用分配后的展示金额算的话，同一行的「平均单次 × 请求数」会与展示金额差最多 1 分 ——
	// 而这一列存在的意义就是让人拿它跟全局平均比（前端按 1.5 倍标黄），
	// 分母只有个位数时那 1 分会被放大成肉眼可见的偏差。Requests==0 时为 0，不是 NaN：
	// NaN 在 encoding/json 里直接让整个响应序列化失败，页面白屏。
	AvgCost float64 `json:"avg_cost"`
	// TopModel / TopModelShare 来自 EnrichRows。本期该分组花费最高的模型与它占本行花费的
	// 百分比（0~100）。分组本期没花过钱时两者都是 nil —— 「零消耗时花得最多的模型」
	// 不是一个有意义的问题，给个模型名会让前端把 0% 画成一根空占比条。
	TopModel      *string  `json:"top_model"`
	TopModelShare *float64 `json:"top_model_share"`
	// CacheHitRate 是 token 口径的缓存命中率（0~100），分母为 0 时 nil。
	// 「没有输入 token」与「命中率 0%」是两回事，前者该显示「—」而不是「0%」。
	CacheHitRate *float64 `json:"cache_hit_rate"`
	// Daily 是本期逐自然日消耗，长度恒等于本期自然日数，缺的日子补 0。
	// 前端画 sparkline 靠下标对齐日期，长度不齐会让整条趋势线错位。
	// EnrichRows 失败时降级成空数组（前端据此不画线），不是 null。
	Daily []float64 `json:"daily"`

	// 以下三个字段是仓储层 → 服务层的内部管道，一律 json:"-" 不下发：
	//
	//   - InputTokens / CacheCreationTokens / CacheReadTokens 是 CacheHitRate 的原始 token。
	//     下发它们等于多三个前端用不上却要维护的契约键，而命中率算法必须与概览条同源。
	//   - KeyIDs 是本行合并了哪几把令牌。EnrichRows 的入参是 api_key_id 集合，而
	//     group_key 对归属行是 'o:<名字>'，从它反推不出令牌 —— 只能由主查询顺带带出来。
	//     它含软删令牌（口径与金额一致：钱花过就得能被解释），否则补充查询会漏掉
	//     「令牌删了但账还在」那部分消耗，趋势线与本行金额对不上。
	InputTokens         int64   `json:"-"`
	CacheCreationTokens int64   `json:"-"`
	CacheReadTokens     int64   `json:"-"`
	KeyIDs              []int64 `json:"-"`
}

// EnrichQuery 是 EnrichRows 的入参：**当页**行的令牌集合 + 本期区间。
//
// 不接收上期：TopModel / Daily 都只描述本期，多查一段上期就是白扫一遍 13GB 的表。
type EnrichQuery struct {
	UserID int64
	KeyIDs []int64
	Cur    Period
}

// RowEnrichment 是一个分组的补充维度原料，key 是与主查询逐字相同的 group_key。
//
// 这里刻意只放**原料**而不放结论（不放 TopModel/TopModelShare）：选谁当主力模型、
// 并列时怎么定序、总额为 0 时算不算有主力模型，这三条都是展示口径的裁决，
// 与环比、异常一样属于服务层。放在仓储层就只能靠真库集成测试去钉，钉不动并列这类分支。
type RowEnrichment struct {
	// Models 是本期该分组逐模型的消耗。空 map 表示本期没有任何日志。
	Models map[string]float64
	// Daily 是本期逐日消耗，key 是 'YYYY-MM-DD'，按 Period.Timezone 分桶。
	// 它与区间边界、前端日期下标使用同一用户时区，因此每个自然日一一对应一个点。
	Daily map[string]float64
}

// RowQuery 是 ListRows 的入参。Cur / Prev 由 period.go 构造，仓储层直接用它们的
// [Start, End) 边界，不再自己解析日期。
type RowQuery struct {
	UserID   int64
	Cur      Period
	Prev     Period
	Sort     string // name | cost | delta
	Order    string // asc | desc
	Q        string
	Page     int
	PageSize int
}

// Conclusion 是概览条下面那句结论。判定逻辑属于结论句阶段，本阶段服务层恒返回 nil，
// 前端据此渲染中性句（设计文档 §7）。类型现在就定下来，是为了 /summary 的响应形状
// 从第一版起就带上 conclusion 这个键，前端不必为它做兼容分支。
type Conclusion struct {
	Kind        string `json:"kind"` // growth | absolute | new
	GroupKey    string `json:"group_key"`
	DisplayName string `json:"display_name"`
	Text        string `json:"text"`
	ExtraCount  int    `json:"extra_count"`
}

// Summary 是看板顶部的汇总。TotalCost 与各行 CurrentCost 之和恒等，
// 由 TestSummaryEqualsSumOfRows 钉住：两处口径一旦分叉，页面上就会出现
// 「明细加起来不等于总数」这种没人能自查的账。
type Summary struct {
	TotalCost  float64
	PrevCost   float64
	TopRowCost float64
	RowCount   int
	KeyCount   int
	// DeletedKeyCount 是本期成行的分组里已软删的令牌数。对账条要写
	// 「含 N 把已删除令牌的历史消耗」，而 KeyCount 只数存续令牌 —— 前端算不出 N。
	DeletedKeyCount int
	OwnedKeyCount   int
	Requests        int64
	PrevRequests    int64
	// ActiveRowCount 是本期 current_cost > 0 的分组数，概览条「活跃成员 6/8」的分子。
	// 前端算不出来：它只拿得到当页的行，而分母 RowCount 是全量。
	ActiveRowCount int
	// 三类 prompt token 是全量命中率的原料，与行级共用同一份算法。
	InputTokens         int64
	CacheCreationTokens int64
	CacheReadTokens     int64
	// CacheSavedCost 是**近似**的缓存节省额：逐行按同一行的输入单价给缓存读 token 估价，
	// 减去缓存读费用后应用该行的用户计费倍率（口径见 rich-contract「缓存节省怎么算」）。
	// 可能为负（缓存读价高于输入价的畸形定价），一律不 clamp —— clamp 会把定价配错的
	// 分组伪装成正常，而那正是这个数字唯一能帮上忙的场景。
	CacheSavedCost float64
}
