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

// OpenAICacheAccountUsage 是单个账号在时间范围内的汇总（仓库返回）。
type OpenAICacheAccountUsage struct {
	AccountID int64
	All       OpenAICacheTokenBucket
	Text      OpenAICacheTokenBucket
}

// OpenAICacheStatsRepository 读取 usage_logs 的缓存口径聚合（一段时间内按账号）。
type OpenAICacheStatsRepository interface {
	ListOpenAICacheAccountUsage(ctx context.Context, startTime, endTime time.Time) ([]OpenAICacheAccountUsage, error)
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

// openAICacheChunk 是一个自然日（用户时区）分片的聚合结果。
type openAICacheChunk struct {
	start, end time.Time
	usage      []OpenAICacheAccountUsage
}

type openAICacheChunkEntry struct {
	loadedAt time.Time
	usage    []OpenAICacheAccountUsage
}

// OpenAICacheAccountRow 是管理页「上游缓存」一行。
type OpenAICacheAccountRow struct {
	AccountID   int64  `json:"account_id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	IsShadow    bool   `json:"is_shadow"`
	Schedulable bool   `json:"schedulable"`

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

// OpenAICacheDailyRow 是按天一行。
type OpenAICacheDailyRow struct {
	Date         string              `json:"date"`
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

// loadChunks 读取（或从缓存取）每个分片的按账号聚合。
func (s *OpenAICacheStatsService) loadChunks(ctx context.Context, chunks []openAICacheChunk) ([]openAICacheChunk, error) {
	out := make([]openAICacheChunk, len(chunks))
	for i, c := range chunks {
		usage, err := s.loadChunk(ctx, c)
		if err != nil {
			return nil, err
		}
		out[i] = openAICacheChunk{start: c.start, end: c.end, usage: usage}
	}
	return out, nil
}

// loadChunk 读取一个自然日分片。已结束的日子整段查一次；进行中的日子（含 now）按小时
// 再切：当天的新页还没被 vacuum 标成全可见，index-only 仍要回表，整天实时查要秒级，
// 而已过去的整点小时几乎不再变化，可以缓存 10 分钟，只有当前小时每 45 秒重查。
func (s *OpenAICacheStatsService) loadChunk(ctx context.Context, c openAICacheChunk) ([]OpenAICacheAccountUsage, error) {
	now := s.now()
	if c.end.Before(now) {
		return s.loadChunkCached(ctx, c, openAICacheClosedChunkTTL)
	}
	// 小时片的终点取整点而不是 now：键稳定，45 秒 TTL 才真正起作用（未来时刻没有行，
	// 查到整点与查到 now 结果一致）。
	byID := make(map[int64]OpenAICacheAccountUsage)
	for cur := c.start; cur.Before(c.end) && cur.Before(now); {
		next := cur.Truncate(time.Hour).Add(time.Hour)
		if next.After(c.end) {
			next = c.end
		}
		ttl := openAICacheClosedHourTTL
		if !next.Before(now) {
			ttl = openAICacheOpenChunkTTL
		}
		usage, err := s.loadChunkCached(ctx, openAICacheChunk{start: cur, end: next}, ttl)
		if err != nil {
			return nil, err
		}
		for _, u := range usage {
			acc := byID[u.AccountID]
			acc.AccountID = u.AccountID
			addBucket(&acc.All, u.All)
			addBucket(&acc.Text, u.Text)
			byID[u.AccountID] = acc
		}
		cur = next
	}
	out := make([]OpenAICacheAccountUsage, 0, len(byID))
	for _, u := range byID {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out, nil
}

func (s *OpenAICacheStatsService) loadChunkCached(ctx context.Context, c openAICacheChunk, ttl time.Duration) ([]OpenAICacheAccountUsage, error) {
	key := openAICacheChunkKey(c)
	now := s.now()
	s.mu.Lock()
	if entry, ok := s.cache[key]; ok && now.Sub(entry.loadedAt) < ttl {
		s.mu.Unlock()
		return entry.usage, nil
	}
	s.mu.Unlock()

	v, err, _ := s.sf.Do(key, func() (any, error) {
		usage, err := s.repo.ListOpenAICacheAccountUsage(ctx, c.start, c.end)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.cache[key] = openAICacheChunkEntry{loadedAt: s.now(), usage: usage}
		s.evictExpiredLocked(s.now())
		s.mu.Unlock()
		return usage, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]OpenAICacheAccountUsage), nil
}

func (s *OpenAICacheStatsService) evictExpiredLocked(now time.Time) {
	if len(s.cache) < 512 {
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
		for _, u := range c.usage {
			acc := byID[u.AccountID]
			acc.AccountID = u.AccountID
			addBucket(&acc.All, u.All)
			addBucket(&acc.Text, u.Text)
			byID[u.AccountID] = acc
		}
	}
	return byID
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
	chunks, err := s.loadChunks(ctx, splitOpenAICacheRange(startTime, endTime, loc))
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
			AccountID:     account.ID,
			Name:          account.Name,
			Type:          account.Type,
			Status:        account.Status,
			IsShadow:      account.IsShadow(),
			Schedulable:   account.Schedulable,
			Simulation:    openAICacheSimConfigView(cfg),
			All:           openAICacheRateView(u.All),
			Text:          openAICacheRateView(u.Text),
			Requests:      u.All.Requests,
			ZeroCacheReqs: u.All.ZeroCacheReqs,
			BilledCost:    u.All.BilledCost,
			CostDelta:     u.All.CostDelta,
			ExpectedCost:  u.All.BilledCost + u.All.CostDelta,
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

// ListDaily 返回按用户时区自然日的汇总；accountID 为 0 时为全站 OpenAI。
func (s *OpenAICacheStatsService) ListDaily(ctx context.Context, startTime, endTime time.Time, loc *time.Location, accountID int64) ([]OpenAICacheDailyRow, error) {
	if loc == nil {
		loc = time.UTC
	}
	chunks, err := s.loadChunks(ctx, splitOpenAICacheRange(startTime, endTime, loc))
	if err != nil {
		return nil, err
	}
	rows := make([]OpenAICacheDailyRow, 0, len(chunks))
	for _, c := range chunks {
		var all, text OpenAICacheTokenBucket
		for _, u := range c.usage {
			if accountID > 0 && u.AccountID != accountID {
				continue
			}
			addBucket(&all, u.All)
			addBucket(&text, u.Text)
		}
		rows = append(rows, OpenAICacheDailyRow{
			Date:         c.start.In(loc).Format("2006-01-02"),
			All:          openAICacheRateView(all),
			Text:         openAICacheRateView(text),
			BilledCost:   all.BilledCost,
			CostDelta:    all.CostDelta,
			ExpectedCost: all.BilledCost + all.CostDelta,
		})
	}
	return rows, nil
}
