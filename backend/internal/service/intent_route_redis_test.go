//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestIntentRouteRedis_PinOrderingAcrossInstances(t *testing.T) {
	server := miniredis.RunT(t)
	firstClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	secondClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = firstClient.Close(); _ = secondClient.Close() })
	firstStore := &redisIntentRouteStore{rdb: firstClient}
	secondStore := &redisIntentRouteStore{rdb: secondClient}
	ctx := context.Background()
	oldVersion, err := firstStore.NextVersion(ctx)
	require.NoError(t, err)
	newVersion, err := secondStore.NextVersion(ctx)
	require.NoError(t, err)
	require.Greater(t, newVersion, oldVersion)
	old := intentRouteSession{Intent: "coding", AccountID: 2, Version: oldVersion, Sequence: 2}
	newest := intentRouteSession{Intent: "coding", AccountID: 3, Version: newVersion, Sequence: 2}
	require.NoError(t, secondStore.PinSession(ctx, 1, "conversation", newest, time.Minute))
	server.FastForward(10 * time.Second)
	require.NoError(t, firstStore.PinSession(ctx, 1, "conversation", old, time.Hour))
	earlierAttempt := newest
	earlierAttempt.Sequence = 1
	earlierAttempt.AccountID = 4
	require.NoError(t, secondStore.PinSession(ctx, 1, "conversation", earlierAttempt, time.Hour))
	require.NoError(t, secondStore.PinSession(ctx, 1, "conversation", newest, time.Hour))
	require.NoError(t, firstStore.InitSession(ctx, 1, "conversation", intentRouteSession{Intent: "chat"}, time.Hour))
	got, err := firstStore.GetSession(ctx, 1, "conversation")
	require.NoError(t, err)
	require.Equal(t, newest, *got)
	require.Equal(t, 50*time.Second, server.TTL(intentRouteSessionRedisKey(1, "conversation")), "stale or duplicate writes must not refresh expiry")
	// Existing unversioned entries remain readable and can be upgraded.
	require.NoError(t, server.Set(intentRouteSessionRedisKey(1, "legacy"), `{"i":"coding","a":2}`))
	require.NoError(t, secondStore.PinSession(ctx, 1, "legacy", newest, time.Minute))
	got, err = firstStore.GetSession(ctx, 1, "legacy")
	require.NoError(t, err)
	require.Equal(t, newest, *got)
	// Clearing sessions must not reset the shared counter and reuse old versions.
	_, err = firstStore.ClearSessions(ctx, 0)
	require.NoError(t, err)
	next, err := secondStore.NextVersion(ctx)
	require.NoError(t, err)
	require.Greater(t, next, newVersion)
}

func TestIntentRouter_PinOrderingAcrossInstances(t *testing.T) {
	server := miniredis.RunT(t)
	a, b := newIntentRouteFixture(t), newIntentRouteFixture(t)
	for _, f := range []*intentRouteFixture{a, b} {
		client := redis.NewClient(&redis.Options{Addr: server.Addr()})
		t.Cleanup(func() { _ = client.Close() })
		f.svc.store = &redisIntentRouteStore{rdb: client}
		f.saveRouter(t, nil)
		f.classifier.answers = []string{"coding"}
	}
	require.Equal(t, a.groupID, b.groupID)
	var queued []func()
	a.svc.spawn = func(fn func()) { queued = append(queued, fn) }
	a.svc.Decide(context.Background(), a.request(intentTurn1)).markSelected(a.accountID(t, "coding-1"))
	b.svc.Decide(context.Background(), b.request(intentTurn2)).markSelected(b.accountID(t, "coding-2"))
	for _, fn := range queued {
		fn()
	}
	got := b.svc.Decide(context.Background(), b.request(intentTurn2))
	require.Equal(t, b.accountID(t, "coding-2"), got.PinnedAccountID)
}
