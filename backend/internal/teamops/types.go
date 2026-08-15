package teamops

import "time"

// Row 是表格里的一行：要么是一个归属（合并了多把令牌），要么是一把没设归属的令牌。
//
// DeltaPct / DeltaAbs / IsAnomaly 不由仓储层填充。环比与异常判定要用到「上期是否可比」
// （见 period.go 的 ApplyRetention），那是服务层的结论，SQL 里算不出来。
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

// Summary 是看板顶部的汇总。TotalCost 与各行 CurrentCost 之和恒等，
// 由 TestSummaryEqualsSumOfRows 钉住：两处口径一旦分叉，页面上就会出现
// 「明细加起来不等于总数」这种没人能自查的账。
type Summary struct {
	TotalCost     float64
	PrevCost      float64
	TopRowCost    float64
	RowCount      int
	KeyCount      int
	OwnedKeyCount int
	Requests      int64
	PrevRequests  int64
}
