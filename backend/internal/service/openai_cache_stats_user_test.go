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
