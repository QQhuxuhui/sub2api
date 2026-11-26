package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type userStatsRepoStub struct {
	rows  []OpenAICacheUserUsageRow
	calls int
}

func (r *userStatsRepoStub) ListOpenAICacheUsage(context.Context, time.Time, time.Time, int) ([]OpenAICacheUsageRow, error) {
	return nil, nil
}

func (r *userStatsRepoStub) ListOpenAICacheUserUsage(_ context.Context, start, end time.Time, _ int) ([]OpenAICacheUserUsageRow, error) {
	r.calls++
	var out []OpenAICacheUserUsageRow
	for _, row := range r.rows {
		if !row.Bucket.Before(start) && row.Bucket.Before(end) {
			out = append(out, row)
		}
	}
	return out, nil
}

func (r *userStatsRepoStub) ListOpenAICacheUserNames(_ context.Context, ids []int64) (map[int64]OpenAICacheUserName, error) {
	out := map[int64]OpenAICacheUserName{}
	for _, id := range ids {
		out[id] = OpenAICacheUserName{Email: "u@example.com"}
	}
	return out, nil
}

func TestOpenAICacheStatsUser_ListAndTrendAggregatePerUser(t *testing.T) {
	resetOpenAICacheSimUserCacheForTest()
	t.Cleanup(resetOpenAICacheSimUserCacheForTest)
	loc := OpenAICacheSimShanghai()
	day := time.Date(2026, 11, 20, 0, 0, 0, 0, loc)
	b := func(h int, user int64, input, cached, reach int64) OpenAICacheUserUsageRow {
		return OpenAICacheUserUsageRow{
			Bucket: day.Add(time.Duration(h) * time.Hour), UserID: user,
			All:       OpenAICacheTokenBucket{Requests: 1, Input: input, CacheRead: cached},
			Reachable: reach, Covered: input + cached,
		}
	}
	repo := &userStatsRepoStub{rows: []OpenAICacheUserUsageRow{
		b(1, 7, 2000, 8000, 9000), b(2, 7, 9000, 1000, 1000), b(1, 8, 100, 900, 900),
	}}
	svc := NewOpenAICacheStatsService(repo, nil, nil)
	settings := &SettingService{settingRepo: &contentModerationTestSettingRepo{values: map[string]string{}}}
	_, err := settings.SetOpenAICacheSimGlobal(context.Background(), userSimConfig(82, 88))
	require.NoError(t, err)

	res, err := svc.ListUsers(context.Background(), settings, day, day.AddDate(0, 0, 1))
	require.NoError(t, err)
	require.Len(t, res.Users, 2)
	require.Equal(t, int64(7), res.Users[0].UserID)
	require.Equal(t, int64(2), res.Users[0].Requests)

	trend, err := svc.ListUserTrend(context.Background(), settings, day, day.AddDate(0, 0, 1), 7, OpenAICacheGranularityHour)
	require.NoError(t, err)
	require.Len(t, trend, 2, "only user 7's buckets")
	require.Equal(t, OpenAICacheVerdictStrategyShort, trend[0].Verdict) // 80% shown, 90% reachable
	require.Equal(t, OpenAICacheVerdictCeilingShort, trend[1].Verdict)  // 10% shown, 10% reachable
	require.Equal(t, 1, repo.calls, "the day's chunk is cached between list and trend")
}

func TestOpenAICacheStatsUser_CacheBounded(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	svc := NewOpenAICacheStatsService(&userStatsRepoStub{}, nil, nil)
	svc.now = func() time.Time { return now }
	svc.userCache = map[string]openAICacheUserChunkEntry{
		"expired": {loadedAt: now.Add(-time.Hour), rows: make([]OpenAICacheUserUsageRow, 10)},
		"old":     {loadedAt: now.Add(-2 * time.Minute), rows: make([]OpenAICacheUserUsageRow, openAICacheUserCacheMaxRows)},
		"new":     {loadedAt: now, rows: make([]OpenAICacheUserUsageRow, 10)},
	}
	svc.trimUserCacheLocked(now)
	require.NotContains(t, svc.userCache, "expired")
	require.NotContains(t, svc.userCache, "old")
	require.Contains(t, svc.userCache, "new")
}

func TestOpenAICacheStatsUser_OversizedChunkNotCached(t *testing.T) {
	day := time.Date(2026, 11, 20, 0, 0, 0, 0, OpenAICacheSimShanghai())
	rows := make([]OpenAICacheUserUsageRow, openAICacheUserCacheMaxRows+1)
	for i := range rows {
		rows[i] = OpenAICacheUserUsageRow{Bucket: day, UserID: int64(i)}
	}
	repo := &userStatsRepoStub{rows: rows}
	svc := NewOpenAICacheStatsService(repo, nil, nil)
	count := 0
	require.NoError(t, svc.eachUserChunkRow(context.Background(), day, day.AddDate(0, 0, 1), openAICacheBaseBucketSeconds, func(OpenAICacheUserUsageRow) { count++ }))
	require.Equal(t, len(rows), count)
	require.Empty(t, svc.userCache)
}
