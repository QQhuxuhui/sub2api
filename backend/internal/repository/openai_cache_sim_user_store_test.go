package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newUserStoreForTest(t *testing.T) (service.OpenAICacheSimUserStore, *miniredis.Miniredis) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewOpenAICacheSimUserStore(client), server
}

// The Lua script and the Go reference decide the same, on the same windows
// (half-life long enough that decay is negligible within the test).
func TestOpenAICacheSimUserStore_MatchesReference(t *testing.T) {
	cases := []struct {
		name                   string
		seed                   [][2]int // prior (total, shown) counted into window and hour
		total, cached, ceiling int
		lo, hi, target         int64
	}{
		{"lift", nil, 10000, 2000, 9500, 8200, 8800, 8500},
		{"round up", nil, 2048, 1024, 1920, 8200, 8800, 8500},
		{"58 exact", nil, 12800, 1024, 9000, 5800, 5800, 5800},
		{"50.16 exact", nil, 160000, 1024, 100000, 5016, 5016, 5016},
		{"granularity", nil, 1408, 1024, 1280, 8200, 8800, 8500},
		{"catch up", [][2]int{{50000, 30000}}, 10000, 2000, 9500, 8200, 8800, 8200},
		{"at hi", [][2]int{{100000, 88000}}, 10000, 2000, 9500, 8200, 8800, 8500},
		{"no ceiling", nil, 10000, 2000, 2000, 8200, 8800, 8500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := newUserStoreForTest(t)
			ctx := context.Background()
			win := service.OpenAICacheSimUserWindow{}
			for _, s := range tc.seed {
				require.NoError(t, store.Count(ctx, 7, "2026112610", 24*time.Hour, s[0], s[1], false))
				win.WT += float64(s[0])
				win.WC += float64(s[1])
				win.HT += int64(s[0])
				win.HC += int64(s[1])
			}
			want := service.OpenAICacheSimUserDecideReference(win, tc.total, tc.cached, tc.ceiling, tc.lo, tc.hi, tc.target)
			got, err := store.DecideAndCount(ctx, service.OpenAICacheSimUserDecideInput{
				UserID: 7, Hour: "2026112610", HalfLife: 24 * time.Hour,
				Total: tc.total, Cached: tc.cached, Ceiling: tc.ceiling, LoBP: tc.lo, HiBP: tc.hi, TargetBP: tc.target,
			})
			require.NoError(t, err)
			require.Equal(t, want, got)
		})
	}
}

func TestOpenAICacheSimUserStore_CountsAndHourOnly(t *testing.T) {
	store, server := newUserStoreForTest(t)
	ctx := context.Background()
	require.NoError(t, store.Count(ctx, 7, "2026112610", time.Hour, 1000, 500, true))
	require.False(t, server.Exists(openAICacheSimUserWindowKey(7)), "hour-only does not touch the window")
	require.Equal(t, "1000", server.HGet(openAICacheSimUserHourKey(7, "2026112610"), "t"))
	require.NoError(t, store.Count(ctx, 7, "2026112610", time.Hour, 1000, 500, false))
	win, err := store.PeekUser(ctx, 7, time.Hour)
	require.NoError(t, err)
	require.InDelta(t, 1000, win.Total, 1)
	require.Equal(t, "2000", server.HGet(openAICacheSimUserHourKey(7, "2026112610"), "t"))
	// A new hour starts from zero.
	_, err = store.DecideAndCount(ctx, service.OpenAICacheSimUserDecideInput{UserID: 7, Hour: "2026112611", HalfLife: time.Hour, Total: 10000, Cached: 2000, Ceiling: 9500, LoBP: 8200, HiBP: 8800, TargetBP: 8500})
	require.NoError(t, err)
	require.Equal(t, "10000", server.HGet(openAICacheSimUserHourKey(7, "2026112611"), "t"))
}
