package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type cacheStatsAccountRepo struct {
	AccountRepository
	accounts []Account
}

func (r *cacheStatsAccountRepo) ListByPlatform(_ context.Context, platform string) ([]Account, error) {
	var accounts []Account
	for _, account := range r.accounts {
		if account.Platform == platform && account.Status == StatusActive {
			accounts = append(accounts, account)
		}
	}
	return accounts, nil
}

func (r *cacheStatsAccountRepo) ListAllWithFilters(_ context.Context, platform, accountType, status, search string, groupID int64, privacyMode string) ([]Account, error) {
	var accounts []Account
	for _, account := range r.accounts {
		if account.Platform == platform && (status == "" || account.Status == status) {
			accounts = append(accounts, account)
		}
	}
	return accounts, nil
}

type cacheStatsUsageRepo struct {
	OpenAICacheStatsRepository
	usage []OpenAICacheAccountUsage
	// byChunk 按分片起点返回不同结果（桶起点 = 分片起点）；calls 记录每次查询的 [start,end)。
	byChunk map[time.Time][]OpenAICacheAccountUsage
	calls   [][2]time.Time
	buckets []int
}

func (r *cacheStatsUsageRepo) ListOpenAICacheUsage(_ context.Context, start, end time.Time, bucketSeconds int) ([]OpenAICacheUsageRow, error) {
	r.calls = append(r.calls, [2]time.Time{start, end})
	r.buckets = append(r.buckets, bucketSeconds)
	src := r.usage
	if r.byChunk != nil {
		src = r.byChunk[start.UTC()]
	}
	rows := make([]OpenAICacheUsageRow, 0, len(src))
	for _, u := range src {
		rows = append(rows, OpenAICacheUsageRow{Bucket: start, AccountID: u.AccountID, All: u.All, Text: u.Text})
	}
	return rows, nil
}

func TestOpenAICacheStatsListAccountsIncludesInactiveBeyondPageLimit(t *testing.T) {
	accounts := []Account{{ID: 1, Platform: PlatformOpenAI, Status: StatusActive}}
	for id := int64(2); id <= 1002; id++ {
		status := StatusDisabled
		if id == 1002 {
			status = StatusError
		}
		accounts = append(accounts, Account{ID: id, Platform: PlatformOpenAI, Status: status})
	}
	accounts = append(accounts, Account{ID: 1003, Platform: PlatformAnthropic, Status: StatusActive})
	usage := []OpenAICacheAccountUsage{
		{AccountID: 1, All: OpenAICacheTokenBucket{Requests: 1, BilledCost: 2}},
		{AccountID: 2, All: OpenAICacheTokenBucket{Requests: 2, BilledCost: 3, CostDelta: 1}},
		{AccountID: 1002, All: OpenAICacheTokenBucket{Requests: 3, BilledCost: 4, CostDelta: 2}},
	}
	svc := NewOpenAICacheStatsService(&cacheStatsUsageRepo{usage: usage}, &cacheStatsAccountRepo{accounts: accounts}, nil)
	result, err := svc.ListAccounts(context.Background(), time.Now().Add(-time.Hour), time.Now(), time.UTC)
	require.NoError(t, err)
	require.Len(t, result.Accounts, 1002)
	require.Equal(t, 1002, result.Summary.Accounts)
	require.Equal(t, int64(6), result.Summary.All.Requests)
	require.Equal(t, 9.0, result.Summary.BilledCost)
	require.Equal(t, 12.0, result.Summary.ExpectedCost)
	require.Equal(t, int64(1002), result.Accounts[0].AccountID)
	require.Equal(t, StatusError, result.Accounts[0].Status)
	require.Equal(t, StatusDisabled, result.Accounts[1].Status)
}

func TestSplitOpenAICacheRangeByUserCalendarDay(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	start := time.Date(2026, 10, 20, 0, 0, 0, 0, sh)
	end := time.Date(2026, 10, 23, 0, 0, 0, 0, sh)
	chunks := splitOpenAICacheRange(start, end, sh)
	require.Len(t, chunks, 3)
	for i, c := range chunks {
		require.True(t, c.start.Equal(start.AddDate(0, 0, i)), "chunk %d start", i)
		require.True(t, c.end.Equal(start.AddDate(0, 0, i+1)), "chunk %d end", i)
	}
	// 非零点起点：第一片到下一个零点为止。
	odd := splitOpenAICacheRange(time.Date(2026, 10, 20, 13, 0, 0, 0, sh), time.Date(2026, 10, 21, 6, 0, 0, 0, sh), sh)
	require.Len(t, odd, 2)
	require.True(t, odd[0].end.Equal(time.Date(2026, 10, 21, 0, 0, 0, 0, sh)))
	require.True(t, odd[1].end.Equal(time.Date(2026, 10, 21, 6, 0, 0, 0, sh)))
	// 纽约夏令时切换日（2026-03-08 只有 23 小时）仍按自然日切。
	ny, _ := time.LoadLocation("America/New_York")
	dst := splitOpenAICacheRange(time.Date(2026, 3, 7, 0, 0, 0, 0, ny), time.Date(2026, 3, 10, 0, 0, 0, 0, ny), ny)
	require.Len(t, dst, 3)
	require.Equal(t, 23*time.Hour, dst[1].end.Sub(dst[1].start))
	require.Empty(t, splitOpenAICacheRange(end, start, sh))
}

func TestOpenAICacheStatsChunksSharedBetweenAccountsAndDaily(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 10, 22, 18, 0, 0, 0, sh)
	d0 := time.Date(2026, 10, 21, 0, 0, 0, 0, sh)
	d1 := d0.AddDate(0, 0, 1)
	d2 := d0.AddDate(0, 0, 2) // 与 handler 一致：end 是所选日期次日零点，不是 now
	repo := &cacheStatsUsageRepo{byChunk: map[time.Time][]OpenAICacheAccountUsage{
		d0.UTC(): {
			{AccountID: 1, All: OpenAICacheTokenBucket{Requests: 10, Input: 100, CacheRead: 900, RawInput: 400, RawCacheRead: 600, BilledCost: 5, CostDelta: 1}, Text: OpenAICacheTokenBucket{Requests: 8}},
			{AccountID: 2, All: OpenAICacheTokenBucket{Requests: 5, Input: 500, CacheRead: 500, RawInput: 500, RawCacheRead: 500, BilledCost: 2}},
		},
		d1.UTC(): {
			{AccountID: 1, All: OpenAICacheTokenBucket{Requests: 20, Input: 200, CacheRead: 800, RawInput: 200, RawCacheRead: 800, BilledCost: 7}},
		},
	}}
	accounts := &cacheStatsAccountRepo{accounts: []Account{{ID: 1, Platform: PlatformOpenAI, Status: StatusActive}, {ID: 2, Platform: PlatformOpenAI, Status: StatusActive}}}
	svc := NewOpenAICacheStatsService(repo, accounts, nil)
	svc.now = func() time.Time { return now }

	// 今天按小时切：00:00~18:00 共 18 个小时片，其中整点 00:00 那片起点与 d1 相同，
	// byChunk 只给 00:00 那一小时返回数据，其余小时为空。
	result, err := svc.ListAccounts(context.Background(), d0, d2, sh)
	require.NoError(t, err)
	require.Len(t, repo.calls, 1+18, "昨天一片 + 今天 18 个小时片")
	require.True(t, repo.calls[0][0].Equal(d0) && repo.calls[0][1].Equal(d1), "昨天整段一片")
	require.True(t, repo.calls[1][0].Equal(d1) && repo.calls[1][1].Equal(d1.Add(time.Hour)), "今天第一片是 00:00~01:00")
	require.True(t, repo.calls[18][0].Equal(now.Add(-time.Hour)) && repo.calls[18][1].Equal(now), "最后一片是 17:00~18:00")
	byID := map[int64]OpenAICacheAccountRow{}
	for _, r := range result.Accounts {
		byID[r.AccountID] = r
	}
	require.Equal(t, int64(30), byID[1].Requests)
	require.Equal(t, 12.0, byID[1].BilledCost)
	require.Equal(t, 1.0, byID[1].CostDelta)
	require.InDelta(t, 85.0, *byID[1].All.DisplayRate, 1e-9)
	require.InDelta(t, 70.0, *byID[1].All.RawRate, 1e-9)
	require.Equal(t, int64(5), byID[2].Requests)
	require.Equal(t, int64(35), result.Summary.All.Requests)

	// 按天（全站 / 单账号）复用缓存，不再查库；日期按用户时区。
	days, err := svc.ListDaily(context.Background(), d0, d2, sh, 0)
	require.NoError(t, err)
	require.Len(t, repo.calls, 19)
	require.Equal(t, []string{"2026-10-21", "2026-10-22"}, []string{days[0].Date, days[1].Date})
	require.Equal(t, int64(15), days[0].All.Requests)
	require.Equal(t, int64(20), days[1].All.Requests)
	acc2, err := svc.ListDaily(context.Background(), d0, d2, sh, 2)
	require.NoError(t, err)
	require.Equal(t, int64(5), acc2[0].All.Requests)
	require.Equal(t, int64(0), acc2[1].All.Requests)

	// 一分钟后：17:00~18:00 已成为「已过去的整点小时」命中缓存（created_at 是落库时刻，
	// 整点前的小时不会再有新行），只有新的当前小时片 18:00~19:00 首次查询（终点取整点，键稳定）。
	svc.now = func() time.Time { return now.Add(time.Minute) }
	_, err = svc.ListDaily(context.Background(), d0, d2, sh, 0)
	require.NoError(t, err)
	require.Len(t, repo.calls, 20)
	require.True(t, repo.calls[19][0].Equal(now) && repo.calls[19][1].Equal(now.Add(time.Hour)))
	// 30 秒内再查：当前小时片命中 45 秒缓存，不查库。
	svc.now = func() time.Time { return now.Add(90 * time.Second) }
	_, err = svc.ListAccounts(context.Background(), d0, d2, sh)
	require.NoError(t, err)
	require.Len(t, repo.calls, 20)
	// 超过 45 秒：只重查当前小时片。
	svc.now = func() time.Time { return now.Add(2 * time.Minute) }
	_, err = svc.ListDaily(context.Background(), d0, d2, sh, 0)
	require.NoError(t, err)
	require.Len(t, repo.calls, 21)
	require.True(t, repo.calls[20][0].Equal(now) && repo.calls[20][1].Equal(now.Add(time.Hour)))
}

func TestOpenAICacheStatsTrendGranularities(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 10, 22, 18, 0, 0, 0, sh)
	d0 := time.Date(2026, 10, 21, 0, 0, 0, 0, sh)
	d1 := d0.AddDate(0, 0, 1)
	d2 := d0.AddDate(0, 0, 2)
	// 仓库按 5 分钟桶返回：昨天 00:00 与 00:05 两个桶，今天 09:00 一个桶。
	repo := &bucketCacheStatsRepo{rows: []OpenAICacheUsageRow{
		{Bucket: d0, AccountID: 1, All: OpenAICacheTokenBucket{Requests: 1, Input: 100, CacheRead: 900}},
		{Bucket: d0.Add(5 * time.Minute), AccountID: 1, All: OpenAICacheTokenBucket{Requests: 2, Input: 500, CacheRead: 500}},
		{Bucket: d0.Add(5 * time.Minute), AccountID: 2, All: OpenAICacheTokenBucket{Requests: 4, Input: 400, CacheRead: 600}},
		{Bucket: d1.Add(9 * time.Hour), AccountID: 1, All: OpenAICacheTokenBucket{Requests: 3, Input: 300, CacheRead: 700}},
	}}
	svc := NewOpenAICacheStatsService(repo, &cacheStatsAccountRepo{}, nil)
	svc.now = func() time.Time { return now }

	day, err := svc.ListTrend(context.Background(), d0, d2, sh, 0, OpenAICacheGranularityDay)
	require.NoError(t, err)
	require.Len(t, day, 2)
	require.Equal(t, "2026-10-21", day[0].Date)
	require.Equal(t, int64(7), day[0].All.Requests)
	require.Equal(t, int64(3), day[1].All.Requests)
	require.Equal(t, []int{300, 300}, repo.buckets[:2], "基础桶 5 分钟")

	hour, err := svc.ListTrend(context.Background(), d0, d2, sh, 0, OpenAICacheGranularityHour)
	require.NoError(t, err)
	require.Len(t, hour, 2, "只有有数据的小时出行")
	require.Equal(t, "10-21 00:00", hour[0].Date)
	require.Equal(t, int64(7), hour[0].All.Requests)
	require.Equal(t, "10-22 09:00", hour[1].Date)

	five, err := svc.ListTrend(context.Background(), d0, d2, sh, 1, OpenAICacheGranularity5Min)
	require.NoError(t, err)
	require.Len(t, five, 3)
	require.Equal(t, "10-21 00:05", five[1].Date)
	require.Equal(t, int64(2), five[1].All.Requests, "单账号过滤")

	callsBefore := len(repo.buckets)
	minute, err := svc.ListTrend(context.Background(), d1, d2, sh, 0, OpenAICacheGranularityMinute)
	require.NoError(t, err)
	require.NotEmpty(t, minute)
	require.Equal(t, 60, repo.buckets[callsBefore], "分钟级单独按 60 秒桶查")
	require.Equal(t, OpenAICacheGranularity5Min, NormalizeOpenAICacheGranularity(OpenAICacheGranularityMinute, 72*time.Hour), "超过 2 天自动降级")
	require.Equal(t, OpenAICacheGranularityDay, NormalizeOpenAICacheGranularity("junk", time.Hour))
}

type bucketCacheStatsRepo struct {
	OpenAICacheStatsRepository
	rows    []OpenAICacheUsageRow
	buckets []int
}

func (r *bucketCacheStatsRepo) ListOpenAICacheUsage(_ context.Context, start, end time.Time, bucketSeconds int) ([]OpenAICacheUsageRow, error) {
	r.buckets = append(r.buckets, bucketSeconds)
	var out []OpenAICacheUsageRow
	for _, row := range r.rows {
		if !row.Bucket.Before(start) && row.Bucket.Before(end) {
			out = append(out, row)
		}
	}
	return out, nil
}
