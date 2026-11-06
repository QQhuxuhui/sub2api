//go:build integration

package service

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// Runs the template store's SQL against a real PostgreSQL:
// INTENT_TEMPLATE_PG_DSN=postgres://... go test -tags=integration -run IntentTemplatePG ./internal/service/
func TestIntentTemplatePG_Store(t *testing.T) {
	dsn := os.Getenv("INTENT_TEMPLATE_PG_DSN")
	if dsn == "" {
		t.Skip("INTENT_TEMPLATE_PG_DSN not set")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer db.Close()
	ctx := context.Background()
	migration, err := os.ReadFile("../../migrations/259_intent_route_templates.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `DROP TABLE IF EXISTS intent_route_templates`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err, "idempotent")

	store := &pgIntentTemplateStore{db: db}
	t0 := time.Date(2026, 11, 6, 1, 0, 0, 0, time.UTC)
	keyA, tplA := intentTemplateOf(pgFPProbe)
	keyB, tplB := intentTemplateOf("hello")

	batch1 := mergeIntentTemplateObservations([]intentTemplateObservation{
		{Key: keyA, Template: tplA, Sample: pgFPProbe, TextChars: 99, Time: t0, GroupID: 4, UserID: 7, ReasonA: true, Final: intentTemplateFinalHealth, Probe: 0.92, Health: pgFptr(0.72)},
		{Key: keyA, Template: tplA, Sample: pgFPProbe, TextChars: 99, Time: t0.Add(time.Second), GroupID: 4, UserID: 8, ReasonB: true, Final: intentTemplateFinalRoute, Probe: 0.96, Health: pgFptr(0.65)},
		{Key: keyB, Template: tplB, Sample: "hello", TextChars: 5, Time: t0, GroupID: 3, UserID: 9, ReasonC: true, Final: intentTemplateFinalNormal, Probe: 0.7},
	})
	require.NoError(t, store.upsert(ctx, batch1))
	require.NoError(t, store.upsert(ctx, mergeIntentTemplateObservations([]intentTemplateObservation{
		{Key: keyA, Template: tplA, Sample: "newer sample", TextChars: 12, Time: t0.Add(time.Hour), GroupID: 3, UserID: 7, ReasonA: true, Final: intentTemplateFinalHealth, Probe: 0.90, Health: pgFptr(0.78)},
	})))

	items, total, err := store.list(ctx, IntentRouteTemplateQuery{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	a := items[0]
	require.Equal(t, keyA, a.Key, "most seen first")
	require.Equal(t, IntentTemplatePending, a.Status)
	require.Equal(t, int64(3), a.SeenCount)
	require.Equal(t, [3]int64{2, 1, 0}, [3]int64{a.ReasonA, a.ReasonB, a.ReasonC})
	require.Equal(t, [3]int64{1, 2, 0}, [3]int64{a.VerdictRoute, a.VerdictHealth, a.VerdictNormal})
	require.InDelta(t, 0.90, *a.ProbeMin, 1e-6)
	require.InDelta(t, 0.96, *a.ProbeMax, 1e-6)
	require.InDelta(t, 0.65, *a.HealthMin, 1e-6)
	require.InDelta(t, 0.78, *a.HealthMax, 1e-6)
	require.Equal(t, []int64{7, 8}, a.UserIDs, "newest first, distinct")
	require.Equal(t, []int64{3, 4}, a.GroupIDs)
	require.Equal(t, "newer sample", a.Sample)
	require.Equal(t, 12, a.TextChars)
	require.True(t, a.FirstSeenAt.Equal(t0))
	require.True(t, a.LastSeenAt.Equal(t0.Add(time.Hour)))
	require.Nil(t, items[1].HealthMin)

	got, _, err := store.list(ctx, IntentRouteTemplateQuery{Reason: "c", Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, keyB, got[0].Key)
	got, _, err = store.list(ctx, IntentRouteTemplateQuery{Search: `"FP"`, Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Len(t, got, 1, "search is case-insensitive")
	got, _, err = store.list(ctx, IntentRouteTemplateQuery{Search: "100%_", Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Empty(t, got, "LIKE wildcards are escaped")

	decided, err := store.decide(ctx, keyA, IntentTemplateProbe, 0, 1, t0.Add(2*time.Hour))
	require.NoError(t, err)
	require.Equal(t, IntentTemplateProbe, decided.Status)
	require.Equal(t, int64(1), decided.Revision)
	require.Equal(t, int64(1), *decided.DecidedBy)
	_, err = store.decide(ctx, keyA, IntentTemplateNormal, 0, 1, t0)
	require.ErrorIs(t, err, errIntentTemplateConflict)
	_, err = store.decide(ctx, intentTemplateKeyMissing, IntentTemplateNormal, 0, 1, t0)
	require.ErrorIs(t, err, errIntentTemplateNotFound)

	// Observations after a judgement add counts but never change it.
	require.NoError(t, store.upsert(ctx, mergeIntentTemplateObservations([]intentTemplateObservation{{Key: keyA, Template: tplA, Time: t0.Add(3 * time.Hour), ReasonB: true, Final: intentTemplateFinalRoute, Probe: 0.9}})))
	labels, err := store.decided(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string]string{keyA: IntentTemplateProbe}, labels)

	revoked, err := store.revoke(ctx, keyA, 1)
	require.NoError(t, err)
	require.Equal(t, IntentTemplatePending, revoked.Status)
	require.Nil(t, revoked.DecidedAt)
	require.Equal(t, int64(2), revoked.Revision)
	_, err = store.decide(ctx, keyA, IntentTemplateHealth, 2, 1, t0)
	require.NoError(t, err)

	// Pruning: an old pending row goes, an old judged row loses only its samples.
	_, err = db.ExecContext(ctx, `UPDATE intent_route_templates SET last_seen_at = $1`, t0.Add(-31*24*time.Hour))
	require.NoError(t, err)
	require.NoError(t, store.prune(ctx, t0))
	all, total, err := store.list(ctx, IntentRouteTemplateQuery{Status: "all", Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, keyA, all[0].Key)
	require.Empty(t, all[0].Sample)
	require.Empty(t, all[0].UserIDs)
	require.Equal(t, IntentTemplateHealth, all[0].Status)
}

// A row judged while the prune DELETE waits for its lock must survive: PG
// re-checks only the DELETE's own WHERE after the wait, not the subquery's.
func TestIntentTemplatePG_PruneKeepsRowJudgedDuringDelete(t *testing.T) {
	dsn := os.Getenv("INTENT_TEMPLATE_PG_DSN")
	if dsn == "" {
		t.Skip("INTENT_TEMPLATE_PG_DSN not set")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer db.Close()
	ctx := context.Background()
	migration, err := os.ReadFile("../../migrations/259_intent_route_templates.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `DROP TABLE IF EXISTS intent_route_templates`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)

	store := &pgIntentTemplateStore{db: db}
	t0 := time.Date(2026, 11, 6, 1, 0, 0, 0, time.UTC)
	key, tpl := intentTemplateOf(pgFPProbe)
	require.NoError(t, store.upsert(ctx, mergeIntentTemplateObservations([]intentTemplateObservation{
		{Key: key, Template: tpl, Sample: pgFPProbe, Time: t0.Add(-31 * 24 * time.Hour), ReasonA: true, Final: intentTemplateFinalHealth, Probe: 0.92, Health: pgFptr(0.72)},
	})))

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SELECT 1 FROM intent_route_templates WHERE template_key = $1 FOR UPDATE`, key)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- store.prune(ctx, t0) }()
	require.Eventually(t, func() bool {
		var waiting int
		_ = db.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query LIKE 'DELETE FROM intent_route_templates%'`).Scan(&waiting)
		return waiting > 0
	}, 5*time.Second, 20*time.Millisecond, "prune blocks on the locked row")
	_, err = tx.ExecContext(ctx, `UPDATE intent_route_templates SET status = 'probe', revision = revision + 1, decided_at = $2 WHERE template_key = $1`, key, t0)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.NoError(t, <-done)

	labels, err := store.decided(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string]string{key: IntentTemplateProbe}, labels)
}

const intentTemplateKeyMissing = "0000000000000000000000000000000000000000000000000000000000000000"

const pgFPProbe = "Reply with exactly this JSON and nothing else, no markdown code fence, no explanation:\n{\"fp\":\"7Q2K\",\"calc\":391}"

func pgFptr(v float64) *float64 { return &v }
