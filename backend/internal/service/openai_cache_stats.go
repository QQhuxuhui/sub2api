package service

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

// OpenAICacheTokenBucket 是一组请求的缓存口径汇总：Input / CacheRead 为展示值
// （客户看到的、计费用的），RawInput / RawCacheRead 为上游原始值。
type OpenAICacheTokenBucket struct {
	Requests      int64   `json:"requests"`
	ZeroCacheReqs int64   `json:"zero_cache_requests"`
	Input         int64   `json:"input_tokens"`
	CacheRead     int64   `json:"cache_read_tokens"`
	CacheCreation int64   `json:"cache_creation_tokens"`
	RawInput      int64   `json:"raw_input_tokens"`
	RawCacheRead  int64   `json:"raw_cache_read_tokens"`
	BilledCost    float64 `json:"billed_cost"`
	CostDelta     float64 `json:"cost_delta"`
}

// DisplayRate 展示缓存率（百分比），与后台仪表盘同口径；无请求时为 nil。
func (b OpenAICacheTokenBucket) DisplayRate() *float64 {
	return cacheRatePct(b.CacheRead, b.Input+b.CacheRead+b.CacheCreation)
}

// RawRate 真实缓存率（百分比）。
func (b OpenAICacheTokenBucket) RawRate() *float64 {
	return cacheRatePct(b.RawCacheRead, b.RawInput+b.RawCacheRead+b.CacheCreation)
}

func cacheRatePct(cached, total int64) *float64 {
	if total <= 0 {
		return nil
	}
	v := float64(cached) * 100 / float64(total)
	return &v
}

// OpenAICacheAccountUsage 是单个账号在时间范围内的汇总。
type OpenAICacheAccountUsage struct {
	AccountID int64
	All       OpenAICacheTokenBucket
	Text      OpenAICacheTokenBucket
}

// OpenAICacheUsageRow 是仓库返回的一行：某个时间桶内某账号的汇总。
type OpenAICacheUsageRow struct {
	Bucket    time.Time
	AccountID int64
	All       OpenAICacheTokenBucket
	Text      OpenAICacheTokenBucket
}

// OpenAICacheStatsRepository 读取 usage_logs 的缓存口径聚合（按时间桶 × 账号）。
type OpenAICacheStatsRepository interface {
	ListOpenAICacheUsage(ctx context.Context, startTime, endTime time.Time, bucketSeconds int) ([]OpenAICacheUsageRow, error)
}

// 走势颗粒度。
const (
	OpenAICacheGranularityDay    = "day"
	OpenAICacheGranularityHour   = "hour"
	OpenAICacheGranularity5Min   = "5min"
	OpenAICacheGranularityMinute = "minute"
)

const (
	// openAICacheBaseBucketSeconds 缓存里的基础桶：5 分钟。天/小时/5 分钟都由它合并得到。
	openAICacheBaseBucketSeconds = 300
	// openAICacheMinuteBucketSeconds 分钟级单独一套缓存键，只在范围不超过 2 天时允许。
	openAICacheMinuteBucketSeconds = 60
	openAICacheMinuteMaxRange      = 48 * time.Hour
)

// NormalizeOpenAICacheGranularity 校验颗粒度；分钟级在超长范围下自动降到 5 分钟。
func NormalizeOpenAICacheGranularity(g string, rangeLen time.Duration) string {
	switch g {
	case OpenAICacheGranularityHour, OpenAICacheGranularity5Min:
		return g
	case OpenAICacheGranularityMinute:
		if rangeLen > openAICacheMinuteMaxRange {
			return OpenAICacheGranularity5Min
		}
		return g
	default:
		return OpenAICacheGranularityDay
	}
}

const (
	// openAICacheClosedChunkTTL 已过去的自然日：只会有极少量异步落库的迟到写入，缓存久一点。
	openAICacheClosedChunkTTL = 15 * time.Minute
	// openAICacheClosedHourTTL 今天已过去的整点小时。
	openAICacheClosedHourTTL = 10 * time.Minute
	// openAICacheOpenChunkTTL 当前小时（仍在写入）。
	openAICacheOpenChunkTTL = 45 * time.Second
	openAICacheMaxChunks    = 92
)

// openAICacheChunk 是一个自然日（用户时区）分片的聚合结果（基础桶 × 账号的行）。
type openAICacheChunk struct {
	start, end time.Time
	rows       []OpenAICacheUsageRow
}

type openAICacheChunkEntry struct {
	loadedAt time.Time
	rows     []OpenAICacheUsageRow
}

// OpenAICacheAccountRow 是管理页「上游缓存」一行。
type OpenAICacheAccountRow struct {
	AccountID   int64  `json:"account_id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	IsShadow    bool   `json:"is_shadow"`
	Schedulable bool   `json:"schedulable"`
	// 状态明细，供前端复用账号页的状态指示（错误 / 限流 / 过载 / 临时不可调度 / 过期）。
	ErrorMessage            string     `json:"error_message,omitempty"`
	RateLimitResetAt        *time.Time `json:"rate_limit_reset_at,omitempty"`
	OverloadUntil           *time.Time `json:"overload_until,omitempty"`
	TempUnschedulableUntil  *time.Time `json:"temp_unschedulable_until,omitempty"`
	TempUnschedulableReason string     `json:"temp_unschedulable_reason,omitempty"`
	ExpiresAt               *time.Time `json:"expires_at,omitempty"`
	LastUsedAt              *time.Time `json:"last_used_at,omitempty"`

	Simulation OpenAICacheSimConfigView `json:"simulation"`

	All  OpenAICacheRateView `json:"all"`
	Text OpenAICacheRateView `json:"text"`

	Requests      int64   `json:"requests"`
	ZeroCacheReqs int64   `json:"zero_cache_requests"`
	ZeroCachePct  float64 `json:"zero_cache_pct"`
	BilledCost    float64 `json:"billed_cost"`
	ExpectedCost  float64 `json:"expected_cost"`
	CostDelta     float64 `json:"cost_delta"`

	// Window 是开了模拟的账号在 Redis 里的实时窗口（半衰期加权）。
	Window *OpenAICacheWindowView `json:"window,omitempty"`
}

// OpenAICacheSimConfigView 是配置的对外形态。
type OpenAICacheSimConfigView struct {
	Enabled         bool    `json:"enabled"`
	Lo              float64 `json:"lo"`
	Hi              float64 `json:"hi"`
	HalfLifeMinutes int     `json:"half_life_minutes"`
	Cap             float64 `json:"cap"`
}

// NewOpenAICacheSimConfigView 把账号级配置转成对外形态（handler 返回用）。
func NewOpenAICacheSimConfigView(cfg OpenAICacheSimConfig) OpenAICacheSimConfigView { return openAICacheSimConfigView(cfg) }

func openAICacheSimConfigView(cfg OpenAICacheSimConfig) OpenAICacheSimConfigView {
	return OpenAICacheSimConfigView{Enabled: cfg.Enabled, Lo: cfg.Lo, Hi: cfg.Hi, HalfLifeMinutes: cfg.HalfLifeMinutes, Cap: cfg.Cap}
}

// OpenAICacheRateView 是一个口径的缓存率视图。
type OpenAICacheRateView struct {
	Requests    int64    `json:"requests"`
	RawRate     *float64 `json:"raw_rate"`
	DisplayRate *float64 `json:"display_rate"`
	OpenAICacheTokenBucket
}

func openAICacheRateView(b OpenAICacheTokenBucket) OpenAICacheRateView {
	return OpenAICacheRateView{Requests: b.Requests, RawRate: b.RawRate(), DisplayRate: b.DisplayRate(), OpenAICacheTokenBucket: b}
}

// OpenAICacheWindowView 是实时窗口视图。
type OpenAICacheWindowView struct {
	Total       float64  `json:"total_tokens"`
	Cached      float64  `json:"cached_tokens"`
	DisplayRate *float64 `json:"display_rate"`
	UpdatedAt   *string  `json:"updated_at,omitempty"`
}

// OpenAICacheDailyRow 是走势的一个时间桶（天 / 小时 / 5 分钟 / 分钟）。
type OpenAICacheDailyRow struct {
	// Date 是展示标签（天：2006-01-02；更细：01-02 15:04），BucketStart 是桶起点。
	Date         string              `json:"date"`
	BucketStart  time.Time           `json:"bucket_start"`
	All          OpenAICacheRateView `json:"all"`
	Text         OpenAICacheRateView `json:"text"`
	BilledCost   float64             `json:"billed_cost"`
	ExpectedCost float64             `json:"expected_cost"`
	CostDelta    float64             `json:"cost_delta"`
}

// OpenAICacheSummary 是范围内全站 OpenAI 的汇总卡片。
type OpenAICacheSummary struct {
	Accounts     int                 `json:"accounts"`
	All          OpenAICacheRateView `json:"all"`
	Text         OpenAICacheRateView `json:"text"`
	BilledCost   float64             `json:"billed_cost"`
	ExpectedCost float64             `json:"expected_cost"`
	CostDelta    float64             `json:"cost_delta"`
}

// OpenAICacheAccountsResult 是账号列表接口的返回。
type OpenAICacheAccountsResult struct {
	Summary  OpenAICacheSummary      `json:"summary"`
	Accounts []OpenAICacheAccountRow `json:"accounts"`
}

// OpenAICacheStatsService 组装「上游缓存」管理页数据。
//
// usage_logs 一天 100 万行级别，整段范围直接聚合要秒级。这里把范围按用户时区的自然日
// 切片，每片只查一次「按账号聚合」并缓存（已关闭的日子 15 分钟、今天 45 秒），账号表、
// 全站按天、单账号按天都从同一份分片推导；并发相同分片用 singleflight 合并。
type OpenAICacheStatsService struct {
	repo        OpenAICacheStatsRepository
	accountRepo AccountRepository
	store       OpenAICacheSimWindowStore

	now   func() time.Time
	mu    sync.Mutex
	cache map[string]openAICacheChunkEntry
	sf    singleflight.Group
}

// NewOpenAICacheStatsService 构造。
func NewOpenAICacheStatsService(repo OpenAICacheStatsRepository, accountRepo AccountRepository, store OpenAICacheSimWindowStore) *OpenAICacheStatsService {
	return &OpenAICacheStatsService{repo: repo, accountRepo: accountRepo, store: store, now: time.Now, cache: make(map[string]openAICacheChunkEntry)}
}

// splitOpenAICacheRange 按 loc 的自然日切片：第一片从 start 到下一个零点，最后一片止于 end。
func splitOpenAICacheRange(start, end time.Time, loc *time.Location) []openAICacheChunk {
	if loc == nil {
		loc = time.UTC
	}
	var chunks []openAICacheChunk
	cur := start
	for cur.Before(end) && len(chunks) < openAICacheMaxChunks {
		l := cur.In(loc)
		next := time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, loc)
		if !next.After(cur) {
			next = cur.Add(24 * time.Hour)
		}
		if next.After(end) {
			next = end
		}
		chunks = append(chunks, openAICacheChunk{start: cur, end: next})
		cur = next
	}
	return chunks
}

func openAICacheChunkKey(c openAICacheChunk) string {
	return strconv.FormatInt(c.start.Unix(), 10) + ":" + strconv.FormatInt(c.end.Unix(), 10)
}

// loadChunks 读取（或从缓存取）每个分片在 baseSeconds 桶下的按账号聚合。
func (s *OpenAICacheStatsService) loadChunks(ctx context.Context, chunks []openAICacheChunk, baseSeconds int) ([]openAICacheChunk, error) {
	out := make([]openAICacheChunk, len(chunks))
	for i, c := range chunks {
		rows, err := s.loadChunk(ctx, c, baseSeconds)
		if err != nil {
			return nil, err
		}
		out[i] = openAICacheChunk{start: c.start, end: c.end, rows: rows}
	}
	return out, nil
}

// loadChunk 读取一个自然日分片。已结束的日子整段查一次；进行中的日子（含 now）按小时
// 再切：当天的新页还没被 vacuum 标成全可见，index-only 仍要回表，整天实时查要秒级，
// 而已过去的整点小时几乎不再变化，可以缓存 10 分钟，只有当前小时每 45 秒重查。
func (s *OpenAICacheStatsService) loadChunk(ctx context.Context, c openAICacheChunk, baseSeconds int) ([]OpenAICacheUsageRow, error) {
	now := s.now()
	if c.end.Before(now) {
		return s.loadChunkCached(ctx, c, baseSeconds, openAICacheClosedChunkTTL)
	}
	// 小时片的终点取整点而不是 now：键稳定，45 秒 TTL 才真正起作用（未来时刻没有行，
	// 查到整点与查到 now 结果一致）。小时片内的桶仍以自然日零点为基准对齐。
	var out []OpenAICacheUsageRow
	for cur := c.start; cur.Before(c.end) && cur.Before(now); {
		next := cur.Truncate(time.Hour).Add(time.Hour)
		if next.After(c.end) {
			next = c.end
		}
		ttl := openAICacheClosedHourTTL
		if !next.Before(now) {
			ttl = openAICacheOpenChunkTTL
		}
		rows, err := s.loadChunkCached(ctx, openAICacheChunk{start: cur, end: next}, baseSeconds, ttl)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		cur = next
	}
	return out, nil
}

func (s *OpenAICacheStatsService) loadChunkCached(ctx context.Context, c openAICacheChunk, baseSeconds int, ttl time.Duration) ([]OpenAICacheUsageRow, error) {
	key := openAICacheChunkKey(c) + ":" + strconv.Itoa(baseSeconds)
	now := s.now()
	s.mu.Lock()
	if entry, ok := s.cache[key]; ok && now.Sub(entry.loadedAt) < ttl {
		s.mu.Unlock()
		return entry.rows, nil
	}
	s.mu.Unlock()

	v, err, _ := s.sf.Do(key, func() (any, error) {
		rows, err := s.repo.ListOpenAICacheUsage(ctx, c.start, c.end, baseSeconds)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.cache[key] = openAICacheChunkEntry{loadedAt: s.now(), rows: rows}
		s.evictExpiredLocked(s.now())
		s.mu.Unlock()
		return rows, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]OpenAICacheUsageRow), nil
}

func (s *OpenAICacheStatsService) evictExpiredLocked(now time.Time) {
	// 细桶条目一天约几万行，过期就清，不等条目数攒多。
	if len(s.cache) < 64 {
		return
	}
	for k, e := range s.cache {
		if now.Sub(e.loadedAt) > openAICacheClosedChunkTTL {
			delete(s.cache, k)
		}
	}
}

func sumChunkByAccount(chunks []openAICacheChunk) map[int64]OpenAICacheAccountUsage {
	byID := make(map[int64]OpenAICacheAccountUsage)
	for _, c := range chunks {
		for _, u := range c.rows {
			acc := byID[u.AccountID]
			acc.AccountID = u.AccountID
			addBucket(&acc.All, u.All)
			addBucket(&acc.Text, u.Text)
			byID[u.AccountID] = acc
		}
	}
	return byID
}

// openAICacheTrendBucket 把基础桶起点归到目标颗粒度的桶起点（用户时区）。
func openAICacheTrendBucket(t time.Time, granularity string, loc *time.Location) time.Time {
	l := t.In(loc)
	switch granularity {
	case OpenAICacheGranularityDay:
		return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
	case OpenAICacheGranularityHour:
		return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), 0, 0, 0, loc)
	default:
		return t
	}
}

func openAICacheTrendLabel(t time.Time, granularity string, loc *time.Location) string {
	l := t.In(loc)
	if granularity == OpenAICacheGranularityDay {
		return l.Format("2006-01-02")
	}
	return l.Format("01-02 15:04")
}

func addBucket(dst *OpenAICacheTokenBucket, src OpenAICacheTokenBucket) {
	dst.Requests += src.Requests
	dst.ZeroCacheReqs += src.ZeroCacheReqs
	dst.Input += src.Input
	dst.CacheRead += src.CacheRead
	dst.CacheCreation += src.CacheCreation
	dst.RawInput += src.RawInput
	dst.RawCacheRead += src.RawCacheRead
	dst.BilledCost += src.BilledCost
	dst.CostDelta += src.CostDelta
}

// ListAccounts 返回范围内所有 OpenAI 账号的缓存口径汇总（没有用量的账号也列出，数字为零）。
// loc 是用户时区，决定自然日分片边界。
func (s *OpenAICacheStatsService) ListAccounts(ctx context.Context, startTime, endTime time.Time, loc *time.Location) (*OpenAICacheAccountsResult, error) {
	// Include disabled/error accounts and avoid the paginated query's size limit:
	// historical usage must remain visible when an account stops being active.
	accounts, err := s.accountRepo.ListAllWithFilters(ctx, PlatformOpenAI, "", "", "", 0, "")
	if err != nil {
		return nil, err
	}
	chunks, err := s.loadChunks(ctx, splitOpenAICacheRange(startTime, endTime, loc), openAICacheBaseBucketSeconds)
	if err != nil {
		return nil, err
	}
	usageByID := sumChunkByAccount(chunks)

	result := &OpenAICacheAccountsResult{Accounts: make([]OpenAICacheAccountRow, 0, len(accounts))}
	var allSum, textSum OpenAICacheTokenBucket
	for i := range accounts {
		account := &accounts[i]
		u := usageByID[account.ID]
		cfg := account.OpenAICacheSimulation()
		row := OpenAICacheAccountRow{
			AccountID:               account.ID,
			Name:                    account.Name,
			Type:                    account.Type,
			Status:                  account.Status,
			IsShadow:                account.IsShadow(),
			Schedulable:             account.Schedulable,
			ErrorMessage:            account.ErrorMessage,
			RateLimitResetAt:        account.RateLimitResetAt,
			OverloadUntil:           account.OverloadUntil,
			TempUnschedulableUntil:  account.TempUnschedulableUntil,
			TempUnschedulableReason: account.TempUnschedulableReason,
			ExpiresAt:               account.ExpiresAt,
			LastUsedAt:              account.LastUsedAt,
			Simulation:              openAICacheSimConfigView(cfg),
			All:                     openAICacheRateView(u.All),
			Text:                    openAICacheRateView(u.Text),
			Requests:                u.All.Requests,
			ZeroCacheReqs:           u.All.ZeroCacheReqs,
			BilledCost:              u.All.BilledCost,
			CostDelta:               u.All.CostDelta,
			ExpectedCost:            u.All.BilledCost + u.All.CostDelta,
		}
		if u.All.Requests > 0 {
			row.ZeroCachePct = float64(u.All.ZeroCacheReqs) * 100 / float64(u.All.Requests)
		}
		if cfg.Enabled && s.store != nil {
			if win, err := s.store.Peek(ctx, account.ID, cfg.HalfLife()); err != nil {
				logger.L().Warn("openai_cache_stats.window_peek_failed", zap.Int64("account_id", account.ID), zap.Error(err))
			} else {
				view := &OpenAICacheWindowView{Total: win.Total, Cached: win.Cached}
				if r := win.Ratio(); r >= 0 {
					pct := r * 100
					view.DisplayRate = &pct
				}
				if !win.UpdatedAt.IsZero() {
					ts := win.UpdatedAt.Format(time.RFC3339)
					view.UpdatedAt = &ts
				}
				row.Window = view
			}
		}
		addBucket(&allSum, u.All)
		addBucket(&textSum, u.Text)
		result.Accounts = append(result.Accounts, row)
	}
	// 默认按让利降序，再按请求数降序。
	sort.SliceStable(result.Accounts, func(i, j int) bool {
		if result.Accounts[i].CostDelta != result.Accounts[j].CostDelta {
			return result.Accounts[i].CostDelta > result.Accounts[j].CostDelta
		}
		return result.Accounts[i].Requests > result.Accounts[j].Requests
	})
	result.Summary = OpenAICacheSummary{
		Accounts:     len(result.Accounts),
		All:          openAICacheRateView(allSum),
		Text:         openAICacheRateView(textSum),
		BilledCost:   allSum.BilledCost,
		CostDelta:    allSum.CostDelta,
		ExpectedCost: allSum.BilledCost + allSum.CostDelta,
	}
	return result, nil
}

// ListTrend 返回按 granularity（day / hour / 5min / minute）分桶的走势；accountID 为 0 时为全站 OpenAI。
// 桶边界按用户时区：天与小时按自然日/整点，5 分钟与分钟从自然日零点起等分。
func (s *OpenAICacheStatsService) ListTrend(ctx context.Context, startTime, endTime time.Time, loc *time.Location, accountID int64, granularity string) ([]OpenAICacheDailyRow, error) {
	if loc == nil {
		loc = time.UTC
	}
	granularity = NormalizeOpenAICacheGranularity(granularity, endTime.Sub(startTime))
	base := openAICacheBaseBucketSeconds
	if granularity == OpenAICacheGranularityMinute {
		base = openAICacheMinuteBucketSeconds
	}
	chunks, err := s.loadChunks(ctx, splitOpenAICacheRange(startTime, endTime, loc), base)
	if err != nil {
		return nil, err
	}
	type agg struct{ all, text OpenAICacheTokenBucket }
	byBucket := make(map[int64]*agg)
	var order []time.Time
	for _, c := range chunks {
		for _, u := range c.rows {
			if accountID > 0 && u.AccountID != accountID {
				continue
			}
			b := openAICacheTrendBucket(u.Bucket, granularity, loc)
			key := b.Unix()
			a, ok := byBucket[key]
			if !ok {
				a = &agg{}
				byBucket[key] = a
				order = append(order, b)
			}
			addBucket(&a.all, u.All)
			addBucket(&a.text, u.Text)
		}
	}
	// 天颗粒度保持原行为：范围内每一天都出一行（没有数据也给零），便于画图对齐。
	if granularity == OpenAICacheGranularityDay {
		order = order[:0]
		for _, c := range chunks {
			b := openAICacheTrendBucket(c.start, granularity, loc)
			if _, ok := byBucket[b.Unix()]; !ok {
				byBucket[b.Unix()] = &agg{}
			}
			order = append(order, b)
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i].Before(order[j]) })
	rows := make([]OpenAICacheDailyRow, 0, len(order))
	for _, b := range order {
		a := byBucket[b.Unix()]
		rows = append(rows, OpenAICacheDailyRow{
			Date:         openAICacheTrendLabel(b, granularity, loc),
			BucketStart:  b,
			All:          openAICacheRateView(a.all),
			Text:         openAICacheRateView(a.text),
			BilledCost:   a.all.BilledCost,
			CostDelta:    a.all.CostDelta,
			ExpectedCost: a.all.BilledCost + a.all.CostDelta,
		})
	}
	return rows, nil
}

// ListDaily 等价于 ListTrend(day)。
func (s *OpenAICacheStatsService) ListDaily(ctx context.Context, startTime, endTime time.Time, loc *time.Location, accountID int64) ([]OpenAICacheDailyRow, error) {
	return s.ListTrend(ctx, startTime, endTime, loc, accountID, OpenAICacheGranularityDay)
}
