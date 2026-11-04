//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestIQShowcaseRepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewIQShowcaseRepository(integrationDB)
	suffix := time.Now().UnixNano()

	group, err := client.Group.Create().SetName(fmt.Sprintf("iq-showcase-%d", suffix)).SetPlatform(service.PlatformOpenAI).Save(ctx)
	require.NoError(t, err)
	other, err := client.Group.Create().SetName(fmt.Sprintf("iq-showcase-other-%d", suffix)).SetPlatform(service.PlatformOpenAI).Save(ctx)
	require.NoError(t, err)
	user := createEntUser(t, ctx, client, fmt.Sprintf("iq-showcase-%d@example.com", suffix))
	plain := fmt.Sprintf("sk-showcase-%d-ABCD", suffix)
	key, err := client.APIKey.Create().SetUserID(user.ID).SetKey(plain).SetName("probe").SetGroupID(group.ID).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM iq_showcase_items WHERE group_id IN ($1, $2)", group.ID, other.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM api_keys WHERE id = $1", key.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM users WHERE id = $1", user.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM groups WHERE id IN ($1, $2)", group.ID, other.ID)
	})

	// Keys and groups as the form and runner see them.
	opts, err := repo.ListKeyOptions(ctx, group.ID)
	require.NoError(t, err)
	require.Len(t, opts, 1)
	require.Equal(t, "sk-…ABCD", opts[0].Masked)
	require.NotContains(t, opts[0].Masked, plain[3:10])
	runKey, err := repo.GetRunKey(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, plain, runKey.Key)
	require.Equal(t, group.ID, *runKey.GroupID)
	g, err := repo.GetGroup(ctx, group.ID)
	require.NoError(t, err)
	require.Equal(t, group.Name, g.Name)

	keyID := key.ID
	item, err := repo.CreateItem(ctx, &service.IQShowcaseItem{
		Title: "pelican", GroupID: group.ID, APIKeyID: &keyID, Protocol: service.IQShowcaseProtocolOpenAIResponses,
		Model: "gpt-x", Prompt: "draw", IntervalMinutes: 60, MaxCards: 2, Enabled: true,
	})
	require.NoError(t, err)
	require.Equal(t, 1, item.ConfigRev)
	require.Equal(t, "sk-…ABCD", item.APIKeyMasked)
	require.Equal(t, group.Name, item.GroupName)

	// A new item is due at once; a second claim while leased gets nothing.
	due, err := repo.ListDueItemIDs(ctx, 100)
	require.NoError(t, err)
	require.Contains(t, due, item.ID)
	claimed, err := repo.ClaimItem(ctx, item.ID, "tok-1", 7*time.Minute, true)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, "gpt-x", claimed.Model)
	require.True(t, claimed.NextRunAt.After(time.Now().Add(50*time.Minute)), "the schedule moved forward")
	again, err := repo.ClaimItem(ctx, item.ID, "tok-2", 7*time.Minute, false)
	require.NoError(t, err)
	require.Nil(t, again, "a manual run cannot take a held lease")

	card := func(rev int) *service.IQShowcaseResult {
		return &service.IQShowcaseResult{ItemID: item.ID, ConfigRev: rev, GroupID: group.ID, Model: "gpt-x", Success: true, SVG: "<svg/>"}
	}
	owned, err := repo.CompleteRound(ctx, item.ID, "wrong", card(1))
	require.NoError(t, err)
	require.False(t, owned, "only the lease holder may write")
	owned, err = repo.CompleteRound(ctx, item.ID, "tok-1", card(1))
	require.NoError(t, err)
	require.True(t, owned)
	owned, err = repo.CompleteRound(ctx, item.ID, "tok-1", card(1))
	require.NoError(t, err)
	require.False(t, owned, "the lease was released by the first completion")

	// Fill up past max_cards through manual rounds.
	for i := 0; i < 3; i++ {
		tok := fmt.Sprintf("m-%d", i)
		c, err := repo.ClaimItem(ctx, item.ID, tok, 7*time.Minute, false)
		require.NoError(t, err)
		require.NotNil(t, c)
		ok, err := repo.CompleteRound(ctx, item.ID, tok, card(1))
		require.NoError(t, err)
		require.True(t, ok)
	}
	meta, err := repo.ListCardMeta(ctx, item.ID, 1, 10)
	require.NoError(t, err)
	require.Len(t, meta, 2, "only max_cards success cards are kept")
	svgs, err := repo.GetCardSVGs(ctx, []int64{meta[0].ID, meta[1].ID, 999999999})
	require.NoError(t, err)
	require.Len(t, svgs, 2)

	// Failures are kept apart and never push cards out.
	c, err := repo.ClaimItem(ctx, item.ID, "f", 7*time.Minute, false)
	require.NoError(t, err)
	require.NotNil(t, c)
	ok, err := repo.CompleteRound(ctx, item.ID, "f", &service.IQShowcaseResult{ItemID: item.ID, ConfigRev: 1, GroupID: group.ID, Model: "gpt-x", Error: "boom"})
	require.NoError(t, err)
	require.True(t, ok)
	meta, err = repo.ListCardMeta(ctx, item.ID, 1, 10)
	require.NoError(t, err)
	require.Len(t, meta, 2)
	fails := false
	failed, err := repo.ListResults(ctx, item.ID, &fails, 10)
	require.NoError(t, err)
	require.Len(t, failed, 1)
	require.Equal(t, "boom", failed[0].Error)

	// A round that outlived its lease cannot write or release a newer lease.
	c, err = repo.ClaimItem(ctx, item.ID, "old", time.Second, false)
	require.NoError(t, err)
	require.NotNil(t, c)
	time.Sleep(1100 * time.Millisecond)
	c, err = repo.ClaimItem(ctx, item.ID, "new", 7*time.Minute, false)
	require.NoError(t, err)
	require.NotNil(t, c, "an expired lease can be taken over")
	ok, err = repo.CompleteRound(ctx, item.ID, "old", card(1))
	require.NoError(t, err)
	require.False(t, ok)
	held, err := repo.GetItem(ctx, item.ID)
	require.NoError(t, err)
	require.NotNil(t, held.LeaseUntil, "the newer lease is untouched")
	ok, err = repo.CompleteRound(ctx, item.ID, "new", card(1))
	require.NoError(t, err)
	require.True(t, ok)

	// An edit that changes the output starts a new rev and drops old cards.
	item.Model = "gpt-y"
	updated, err := repo.UpdateItem(ctx, item, true)
	require.NoError(t, err)
	require.Equal(t, 2, updated.ConfigRev)
	meta, err = repo.ListCardMeta(ctx, item.ID, 1, 10)
	require.NoError(t, err)
	require.Empty(t, meta)
	// A late result of the old rev is written but pruned at once.
	c, err = repo.ClaimItem(ctx, item.ID, "late", 7*time.Minute, false)
	require.NoError(t, err)
	require.NotNil(t, c)
	ok, err = repo.CompleteRound(ctx, item.ID, "late", card(1))
	require.NoError(t, err)
	require.True(t, ok)
	successes := true
	all, err := repo.ListResults(ctx, item.ID, &successes, 10)
	require.NoError(t, err)
	require.Empty(t, all)

	// The user view only lists enabled items of live groups.
	visible, err := repo.ListVisibleItems(ctx)
	require.NoError(t, err)
	require.True(t, containsShowcaseItem(visible, item.ID))
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET deleted_at = NOW() WHERE id = $1", group.ID)
	require.NoError(t, err)
	visible, err = repo.ListVisibleItems(ctx)
	require.NoError(t, err)
	require.False(t, containsShowcaseItem(visible, item.ID), "a soft-deleted group disappears")
	g, err = repo.GetGroup(ctx, group.ID)
	require.NoError(t, err)
	require.Nil(t, g)
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET deleted_at = NULL WHERE id = $1", group.ID)
	require.NoError(t, err)

	// Maintenance prunes old failures and over-limit cards.
	_, err = integrationDB.ExecContext(ctx, "UPDATE iq_showcase_results SET created_at = NOW() - INTERVAL '40 days' WHERE item_id = $1 AND NOT success", item.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO iq_showcase_results (item_id, config_rev, group_id, model, success, svg)
		SELECT $1, 2, $2, 'gpt-y', TRUE, '<svg/>' FROM generate_series(1, 5)`, item.ID, group.ID)
	require.NoError(t, err)
	require.NoError(t, repo.Maintenance(ctx, 30*24*time.Hour))
	failed, err = repo.ListResults(ctx, item.ID, &fails, 10)
	require.NoError(t, err)
	require.Empty(t, failed)
	meta, err = repo.ListCardMeta(ctx, item.ID, 2, 10)
	require.NoError(t, err)
	require.Len(t, meta, 2)

	require.NoError(t, repo.DeleteItem(ctx, item.ID))
	_, err = repo.GetItem(ctx, item.ID)
	require.ErrorIs(t, err, service.ErrIQShowcaseItemNotFound)
}

func containsShowcaseItem(items []*service.IQShowcaseVisibleItem, id int64) bool {
	for _, it := range items {
		if it.ID == id {
			return true
		}
	}
	return false
}
