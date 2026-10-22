package service

import (
	"context"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
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

// OpenAICacheDailyUsage 是按天汇总（仓库返回）。
type OpenAICacheDailyUsage struct {
	Date string
	All  OpenAICacheTokenBucket
	Text OpenAICacheTokenBucket
}

// OpenAICacheStatsRepository 读取 usage_logs 的缓存口径聚合。
type OpenAICacheStatsRepository interface {
	ListOpenAICacheAccountUsage(ctx context.Context, startTime, endTime time.Time) ([]OpenAICacheAccountUsage, error)
	ListOpenAICacheDailyUsage(ctx context.Context, startTime, endTime time.Time, accountID int64) ([]OpenAICacheDailyUsage, error)
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
type OpenAICacheStatsService struct {
	repo        OpenAICacheStatsRepository
	accountRepo AccountRepository
	store       OpenAICacheSimWindowStore
}

// NewOpenAICacheStatsService 构造。
func NewOpenAICacheStatsService(repo OpenAICacheStatsRepository, accountRepo AccountRepository, store OpenAICacheSimWindowStore) *OpenAICacheStatsService {
	return &OpenAICacheStatsService{repo: repo, accountRepo: accountRepo, store: store}
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
func (s *OpenAICacheStatsService) ListAccounts(ctx context.Context, startTime, endTime time.Time) (*OpenAICacheAccountsResult, error) {
	// Include disabled/error accounts and avoid the paginated query's size limit:
	// historical usage must remain visible when an account stops being active.
	accounts, err := s.accountRepo.ListAllWithFilters(ctx, PlatformOpenAI, "", "", "", 0, "")
	if err != nil {
		return nil, err
	}
	usage, err := s.repo.ListOpenAICacheAccountUsage(ctx, startTime, endTime)
	if err != nil {
		return nil, err
	}
	usageByID := make(map[int64]OpenAICacheAccountUsage, len(usage))
	for _, u := range usage {
		usageByID[u.AccountID] = u
	}

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

// ListDaily 返回按天汇总；accountID 为 0 时为全站 OpenAI。
func (s *OpenAICacheStatsService) ListDaily(ctx context.Context, startTime, endTime time.Time, accountID int64) ([]OpenAICacheDailyRow, error) {
	daily, err := s.repo.ListOpenAICacheDailyUsage(ctx, startTime, endTime, accountID)
	if err != nil {
		return nil, err
	}
	rows := make([]OpenAICacheDailyRow, 0, len(daily))
	for _, d := range daily {
		rows = append(rows, OpenAICacheDailyRow{
			Date:         d.Date,
			All:          openAICacheRateView(d.All),
			Text:         openAICacheRateView(d.Text),
			BilledCost:   d.All.BilledCost,
			CostDelta:    d.All.CostDelta,
			ExpectedCost: d.All.BilledCost + d.All.CostDelta,
		})
	}
	return rows, nil
}
