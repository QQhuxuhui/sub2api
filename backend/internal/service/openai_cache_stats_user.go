package service

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// By-user view of the 「上游缓存」page. Buckets and day boundaries are always
// Asia/Shanghai, the time zone of the by-user hour and its acceptance query.

// OpenAICacheUserUsageRow is one (time bucket, user) aggregate.
type OpenAICacheUserUsageRow struct {
	Bucket time.Time
	UserID int64
	All    OpenAICacheTokenBucket
	Text   OpenAICacheTokenBucket
	// Reachable: sum of reachable cache reads (rows without a value count
	// their actual cache reads). Covered: input of rows that have a value.
	Reachable int64
	Covered   int64
}

// OpenAICacheUserName identifies a user on the page.
type OpenAICacheUserName struct {
	Email    string
	Username string
}

// OpenAICacheUserStatsRepository is the by-user part of the stats repository.
type OpenAICacheUserStatsRepository interface {
	ListOpenAICacheUserUsage(ctx context.Context, startTime, endTime time.Time, bucketSeconds int) ([]OpenAICacheUserUsageRow, error)
	ListOpenAICacheUserNames(ctx context.Context, ids []int64) (map[int64]OpenAICacheUserName, error)
}

// Verdicts of a user's bucket against the effective lower bound.
const (
	OpenAICacheVerdictOK            = "ok"
	OpenAICacheVerdictCeilingShort  = "ceiling_short"
	OpenAICacheVerdictStrategyShort = "strategy_short"
)

// OpenAICacheUserRow is one row of the by-user table.
type OpenAICacheUserRow struct {
	UserID        int64                     `json:"user_id"`
	Email         string                    `json:"email"`
	Username      string                    `json:"username"`
	Source        string                    `json:"source"`
	Active        bool                      `json:"active"`
	Config        *OpenAICacheSimConfigView `json:"config"`
	Override      *OpenAICacheSimConfigView `json:"override"`
	All           OpenAICacheRateView       `json:"all"`
	Text          OpenAICacheRateView       `json:"text"`
	Requests      int64                     `json:"requests"`
	ZeroCacheReqs int64                     `json:"zero_cache_requests"`
	ZeroCachePct  float64                   `json:"zero_cache_pct"`
	BilledCost    float64                   `json:"billed_cost"`
	ExpectedCost  float64                   `json:"expected_cost"`
	CostDelta     float64                   `json:"cost_delta"`
	ReachableRate *float64                  `json:"reachable_rate"`
	CoverageRate  *float64                  `json:"coverage_rate"`
	Verdict       string                    `json:"verdict"`
	Window        *OpenAICacheWindowView    `json:"window,omitempty"`
}

// OpenAICacheUsersResult is the by-user list.
type OpenAICacheUsersResult struct {
	TimeZone  string                   `json:"timezone"`
	Global    OpenAICacheSimConfigView `json:"global"`
	Overrides int                      `json:"overrides"`
	Users     []OpenAICacheUserRow     `json:"users"`
}

// OpenAICacheUserTrendRow is one bucket of a user's trend.
type OpenAICacheUserTrendRow struct {
	Date          string              `json:"date"`
	BucketStart   time.Time           `json:"bucket_start"`
	All           OpenAICacheRateView `json:"all"`
	Text          OpenAICacheRateView `json:"text"`
	ReachableRate *float64            `json:"reachable_rate"`
	CoverageRate  *float64            `json:"coverage_rate"`
	Verdict       string              `json:"verdict"`
}

type openAICacheUserAgg struct {
	all, text          OpenAICacheTokenBucket
	reachable, covered int64
}

func (a *openAICacheUserAgg) add(u OpenAICacheUserUsageRow) {
	addBucket(&a.all, u.All)
	addBucket(&a.text, u.Text)
	a.reachable += u.Reachable
	a.covered += u.Covered
}

// openAICacheUserVerdict classifies a bucket; empty when the user is not
// under the by-user simulation or there was no input.
func openAICacheUserVerdict(mode OpenAICacheSimUserMode, a openAICacheUserAgg) (reachable, coverage *float64, verdict string) {
	denom := a.all.Input + a.all.CacheRead + a.all.CacheCreation
	if denom <= 0 {
		return nil, nil, ""
	}
	reachable = cacheRatePct(a.reachable, denom)
	coverage = cacheRatePct(a.covered, denom)
	if !mode.Active {
		return reachable, coverage, ""
	}
	loBP := openAICacheSimBasisPoints(mode.Config.Lo)
	switch {
	case a.all.CacheRead*10000 >= loBP*denom:
		verdict = OpenAICacheVerdictOK
	case a.reachable*10000 < loBP*denom:
		verdict = OpenAICacheVerdictCeilingShort
	default:
		verdict = OpenAICacheVerdictStrategyShort
	}
	return reachable, coverage, verdict
}

func (s *OpenAICacheStatsService) userRepo() (OpenAICacheUserStatsRepository, error) {
	repo, ok := s.repo.(OpenAICacheUserStatsRepository)
	if !ok {
		return nil, errors.New("openai cache stats: by-user queries unavailable")
	}
	return repo, nil
}

// loadUserChunks mirrors loadChunks for the by-user aggregate (same chunking
// and cache lifetimes, separate keys).
func (s *OpenAICacheStatsService) loadUserChunks(ctx context.Context, startTime, endTime time.Time, baseSeconds int) ([]OpenAICacheUserUsageRow, error) {
	repo, err := s.userRepo()
	if err != nil {
		return nil, err
	}
	var out []OpenAICacheUserUsageRow
	now := s.now()
	for _, c := range splitOpenAICacheRange(startTime, endTime, openAICacheSimShanghai) {
		if c.end.Before(now) {
			rows, err := s.loadUserChunkCached(ctx, repo, c, baseSeconds, openAICacheClosedChunkTTL)
			if err != nil {
				return nil, err
			}
			out = append(out, rows...)
			continue
		}
		for cur := c.start; cur.Before(c.end) && cur.Before(now); {
			next := cur.Truncate(time.Hour).Add(time.Hour)
			if next.After(c.end) {
				next = c.end
			}
			ttl := openAICacheClosedHourTTL
			if !next.Before(now) {
				ttl = openAICacheOpenChunkTTL
			}
			rows, err := s.loadUserChunkCached(ctx, repo, openAICacheChunk{start: cur, end: next}, baseSeconds, ttl)
			if err != nil {
				return nil, err
			}
			out = append(out, rows...)
			cur = next
		}
	}
	return out, nil
}

type openAICacheUserChunkEntry struct {
	loadedAt time.Time
	rows     []OpenAICacheUserUsageRow
}

func (s *OpenAICacheStatsService) loadUserChunkCached(ctx context.Context, repo OpenAICacheUserStatsRepository, c openAICacheChunk, baseSeconds int, ttl time.Duration) ([]OpenAICacheUserUsageRow, error) {
	key := "user:" + openAICacheChunkKey(c) + ":" + strconv.Itoa(baseSeconds)
	now := s.now()
	s.mu.Lock()
	if s.userCache == nil {
		s.userCache = make(map[string]openAICacheUserChunkEntry)
	}
	if entry, ok := s.userCache[key]; ok && now.Sub(entry.loadedAt) < ttl {
		s.mu.Unlock()
		return entry.rows, nil
	}
	s.mu.Unlock()
	v, err, _ := s.sf.Do(key, func() (any, error) {
		rows, err := repo.ListOpenAICacheUserUsage(ctx, c.start, c.end, baseSeconds)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.userCache[key] = openAICacheUserChunkEntry{loadedAt: s.now(), rows: rows}
		if len(s.userCache) >= 64 {
			for k, e := range s.userCache {
				if s.now().Sub(e.loadedAt) > openAICacheClosedChunkTTL {
					delete(s.userCache, k)
				}
			}
		}
		s.mu.Unlock()
		return rows, nil
	})
	if err != nil {
		return nil, err
	}
	rows, _ := v.([]OpenAICacheUserUsageRow)
	return rows, nil
}

// ListUsers returns every user with OpenAI usage in the range, plus users
// with an override (even without usage).
func (s *OpenAICacheStatsService) ListUsers(ctx context.Context, settings *SettingService, startTime, endTime time.Time) (*OpenAICacheUsersResult, error) {
	repo, err := s.userRepo()
	if err != nil {
		return nil, err
	}
	rows, err := s.loadUserChunks(ctx, startTime, endTime, openAICacheBaseBucketSeconds)
	if err != nil {
		return nil, err
	}
	byUser := map[int64]*openAICacheUserAgg{}
	for _, u := range rows {
		a := byUser[u.UserID]
		if a == nil {
			a = &openAICacheUserAgg{}
			byUser[u.UserID] = a
		}
		a.add(u)
	}
	global, err := settings.GetOpenAICacheSimGlobal(ctx)
	if err != nil {
		return nil, err
	}
	overrides, err := settings.GetOpenAICacheSimOverrides(ctx)
	if err != nil {
		return nil, err
	}
	for id := range overrides {
		if byUser[id] == nil {
			byUser[id] = &openAICacheUserAgg{}
		}
	}
	ids := make([]int64, 0, len(byUser))
	for id := range byUser {
		ids = append(ids, id)
	}
	names, err := repo.ListOpenAICacheUserNames(ctx, ids)
	if err != nil {
		return nil, err
	}
	modes := settings.OpenAICacheSimUserModes(ctx, ids)
	userStore := currentOpenAICacheSimUserStore()
	result := &OpenAICacheUsersResult{
		TimeZone:  openAICacheSimShanghai.String(),
		Global:    openAICacheSimConfigView(global),
		Overrides: len(overrides),
		Users:     make([]OpenAICacheUserRow, 0, len(ids)),
	}
	for _, id := range ids {
		a := byUser[id]
		mode := modes[id]
		row := OpenAICacheUserRow{
			UserID: id, Email: names[id].Email, Username: names[id].Username,
			Source: mode.Source, Active: mode.Active,
			All: openAICacheRateView(a.all), Text: openAICacheRateView(a.text),
			Requests: a.all.Requests, ZeroCacheReqs: a.all.ZeroCacheReqs,
			BilledCost: a.all.BilledCost, CostDelta: a.all.CostDelta, ExpectedCost: a.all.BilledCost + a.all.CostDelta,
		}
		if mode.Source != OpenAICacheSimSourceAccount {
			view := openAICacheSimConfigView(mode.Config)
			row.Config = &view
		}
		if cfg, ok := overrides[id]; ok {
			view := openAICacheSimConfigView(cfg)
			row.Override = &view
		}
		if a.all.Requests > 0 {
			row.ZeroCachePct = float64(a.all.ZeroCacheReqs) * 100 / float64(a.all.Requests)
		}
		row.ReachableRate, row.CoverageRate, row.Verdict = openAICacheUserVerdict(mode, *a)
		if mode.Active && userStore != nil {
			if win, err := userStore.PeekUser(ctx, id, mode.Config.HalfLife()); err != nil {
				logger.L().Warn("openai_cache_stats.user_window_peek_failed", zap.Int64("user_id", id), zap.Error(err))
			} else if win.Total > 0 {
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
		result.Users = append(result.Users, row)
	}
	sort.SliceStable(result.Users, func(i, j int) bool {
		if result.Users[i].Requests != result.Users[j].Requests {
			return result.Users[i].Requests > result.Users[j].Requests
		}
		return result.Users[i].UserID < result.Users[j].UserID
	})
	return result, nil
}

// ListUserTrend returns a user's buckets (day / hour / 5min / minute, minute
// only within two days on 60-second base buckets, like the account trend).
func (s *OpenAICacheStatsService) ListUserTrend(ctx context.Context, settings *SettingService, startTime, endTime time.Time, userID int64, granularity string) ([]OpenAICacheUserTrendRow, error) {
	loc := openAICacheSimShanghai
	granularity = NormalizeOpenAICacheGranularity(granularity, endTime.Sub(startTime))
	base := openAICacheBaseBucketSeconds
	if granularity == OpenAICacheGranularityMinute {
		base = openAICacheMinuteBucketSeconds
	}
	rows, err := s.loadUserChunks(ctx, startTime, endTime, base)
	if err != nil {
		return nil, err
	}
	mode := settings.OpenAICacheSimUserModes(ctx, []int64{userID})[userID]
	byBucket := map[int64]*openAICacheUserAgg{}
	var order []time.Time
	for _, u := range rows {
		if u.UserID != userID {
			continue
		}
		b := openAICacheTrendBucket(u.Bucket, granularity, loc)
		a, ok := byBucket[b.Unix()]
		if !ok {
			a = &openAICacheUserAgg{}
			byBucket[b.Unix()] = a
			order = append(order, b)
		}
		a.add(u)
	}
	if granularity == OpenAICacheGranularityDay {
		order = order[:0]
		for _, c := range splitOpenAICacheRange(startTime, endTime, loc) {
			b := openAICacheTrendBucket(c.start, granularity, loc)
			if _, ok := byBucket[b.Unix()]; !ok {
				byBucket[b.Unix()] = &openAICacheUserAgg{}
			}
			order = append(order, b)
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i].Before(order[j]) })
	out := make([]OpenAICacheUserTrendRow, 0, len(order))
	for _, b := range order {
		a := byBucket[b.Unix()]
		row := OpenAICacheUserTrendRow{
			Date: openAICacheTrendLabel(b, granularity, loc), BucketStart: b,
			All: openAICacheRateView(a.all), Text: openAICacheRateView(a.text),
		}
		row.ReachableRate, row.CoverageRate, row.Verdict = openAICacheUserVerdict(mode, *a)
		out = append(out, row)
	}
	return out, nil
}
