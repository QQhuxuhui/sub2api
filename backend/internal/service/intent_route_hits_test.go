//go:build unit

package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

type recordingHitSink struct{ hits []IntentRouteHit }

func (s *recordingHitSink) add(hit IntentRouteHit) { s.hits = append(s.hits, hit) }

type recordingHitDB struct {
	query string
	args  []any
}

func TestIntentHitInputTokensIncludesCacheCreation(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer db.Close()
	expr := strings.Split(strings.Split(intentRouteHitUsageJoin, "SELECT ul.account_id, ")[1], " AS input_tokens")[0]
	var total int64
	err = db.QueryRow(`WITH usage_logs(input_tokens,cache_read_tokens,cache_creation_tokens) AS (VALUES(100,200,99000)) SELECT ` + expr + ` FROM usage_logs ul`).Scan(&total)
	require.NoError(t, err)
	require.EqualValues(t, 99300, total)
}

type hitSQLError string

func (e hitSQLError) Error() string    { return string(e) }
func (e hitSQLError) SQLState() string { return string(e) }

type scriptedHitDB struct {
	mu      sync.Mutex
	inserts int
	stored  []string
	pruned  chan struct{}
	fail    func(int, []any) error
}

func (d *scriptedHitDB) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("unused")
}
func (d *scriptedHitDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if strings.HasPrefix(query, "DELETE") {
		if d.pruned != nil {
			select {
			case d.pruned <- struct{}{}:
			default:
			}
		}
		return nil, nil
	}
	d.inserts++
	if d.fail != nil {
		if err := d.fail(d.inserts, args); err != nil {
			return nil, err
		}
	}
	for _, v := range args {
		if text, ok := v.(string); ok && (!utf8.ValidString(text) || strings.ContainsRune(text, 0)) {
			return nil, hitSQLError("22021")
		}
	}
	for i := 0; i < len(args); i += intentRouteHitColumns {
		d.stored = append(d.stored, args[i+11].(string))
	}
	return nil, nil
}

func TestIntentHitBatchSanitizesDatabaseText(t *testing.T) {
	db := &scriptedHitDB{}
	l := newIntentRouteHitLog(db)
	l.write([]IntentRouteHit{{Keyword: "key\x00", Model: "bad\xff", Excerpt: "prefix\x00中文\xff"}, {Excerpt: "good"}})
	require.Len(t, db.stored, 2)
	require.Contains(t, db.stored[0], "中文")
	require.Equal(t, "good", db.stored[1])
}

func TestIntentHitBatchRetriesRolledBackTransaction(t *testing.T) {
	db := &scriptedHitDB{fail: func(n int, _ []any) error {
		if n < 3 {
			return hitSQLError("40001")
		}
		return nil
	}}
	l := newIntentRouteHitLog(db)
	l.write([]IntentRouteHit{{Excerpt: "saved once"}})
	require.Equal(t, 3, db.inserts)
	require.Equal(t, []string{"saved once"}, db.stored)
}

func TestIntentHitBatchIsolatesBadRows(t *testing.T) {
	db := &scriptedHitDB{fail: func(_ int, args []any) error {
		for i := 11; i < len(args); i += intentRouteHitColumns {
			if args[i] == "bad" {
				return hitSQLError("22001")
			}
		}
		return nil
	}}
	l := newIntentRouteHitLog(db)
	l.write([]IntentRouteHit{{Excerpt: "one"}, {Excerpt: "bad"}, {Excerpt: "two"}})
	require.Equal(t, []string{"one", "two"}, db.stored)
	require.EqualValues(t, 1, l.dropped.Load())
}

func TestIntentHitBatchDoesNotRetryAmbiguousConnectionFailure(t *testing.T) {
	for _, err := range []error{fmt.Errorf("connection lost after send"), hitSQLError("40003")} {
		db := &scriptedHitDB{fail: func(int, []any) error { return err }}
		l := newIntentRouteHitLog(db)
		l.write([]IntentRouteHit{{Excerpt: "unknown commit"}})
		require.Equal(t, 1, db.inserts)
		require.EqualValues(t, 1, l.dropped.Load())
	}
}

type pruningHitDB struct {
	scriptedHitDB
	entered chan struct{}
}

func (d *pruningHitDB) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	if strings.HasPrefix(q, "DELETE") {
		close(d.entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return d.scriptedHitDB.ExecContext(ctx, q, args...)
}
func TestIntentHitStopCancelsPruningBeforeDraining(t *testing.T) {
	db := &pruningHitDB{entered: make(chan struct{})}
	l := newIntentRouteHitLog(db)
	l.Start()
	<-db.entered
	l.add(IntentRouteHit{Excerpt: "preserved"})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, l.Stop(ctx))
	require.Equal(t, []string{"preserved"}, db.stored)
}

func TestIntentHitConcurrentAdmissionAndStop(t *testing.T) {
	db := &scriptedHitDB{}
	l := newIntentRouteHitLog(db)
	l.Start()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); l.add(IntentRouteHit{Excerpt: "hit"}) }()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, l.Stop(ctx))
	wg.Wait()
	db.mu.Lock()
	stored := len(db.stored)
	db.mu.Unlock()
	require.EqualValues(t, 100, int64(stored)+l.dropped.Load(), "every accepted hit is stored; late admission is counted")
}

type blockingInsertHitDB struct {
	scriptedHitDB
	entered chan struct{}
}

func (d *blockingInsertHitDB) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	if strings.HasPrefix(q, "INSERT") {
		close(d.entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return d.scriptedHitDB.ExecContext(ctx, q, args...)
}
func TestIntentHitStopDeadlineCancelsWriter(t *testing.T) {
	db := &blockingInsertHitDB{entered: make(chan struct{})}
	l := newIntentRouteHitLog(db)
	l.Start()
	for i := 0; i < intentRouteHitBatch; i++ {
		l.add(IntentRouteHit{Excerpt: "hit"})
	}
	select {
	case <-db.entered:
	case <-time.After(time.Second):
		t.Fatal("insert did not start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, l.Stop(ctx), context.Canceled)
	select {
	case <-l.done:
	case <-time.After(time.Second):
		t.Fatal("writer did not exit")
	}
	require.EqualValues(t, intentRouteHitBatch, l.dropped.Load())
}

func TestIntentHitLogStartsPruningAndDrainsOnStop(t *testing.T) {
	db := &scriptedHitDB{pruned: make(chan struct{}, 1)}
	l := newIntentRouteHitLog(db)
	l.Start()
	select {
	case <-db.pruned:
	case <-time.After(time.Second):
		t.Fatal("no cleanup before first hit")
	}
	for i := 0; i < intentRouteHitBatch+3; i++ {
		l.add(IntentRouteHit{Excerpt: fmt.Sprint(i)})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, l.Stop(ctx))
	require.NoError(t, l.Stop(ctx))
	db.mu.Lock()
	count := len(db.stored)
	db.mu.Unlock()
	require.Equal(t, intentRouteHitBatch+3, count)
	l.add(IntentRouteHit{Excerpt: "too late"})
	require.EqualValues(t, 1, l.dropped.Load())
}

func (d *recordingHitDB) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	d.query, d.args = query, args
	return nil, nil
}

func (d *recordingHitDB) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, nil
}

func TestIntentRouter_KeywordHitIsLogged(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	sink := &recordingHitSink{}
	f.svc.hits = sink
	f.saveRouter(t, keywordOnly([]string{"traceback"}, []string{"joke"}))

	in := f.request(`{"model":"gpt-6-astra",` + strings.TrimPrefix(userTurns("here is a TraceBack from prod", "what does it mean"), "{"))
	in.UserID, in.ClientRequestID, in.Endpoint = 7, "abc-123", "/v1/chat/completions"
	require.Equal(t, "coding", f.svc.Decide(ctx, in).Intent)

	require.Len(t, sink.hits, 1)
	hit := sink.hits[0]
	require.Equal(t, IntentRouteHit{
		Time: f.now, GroupID: f.groupID, UserID: 7, APIKeyID: 11, RequestID: "client:abc-123",
		Endpoint: "/v1/chat/completions", Model: "gpt-6-astra", Rule: "coding", Keyword: "traceback",
		UserTurn: 1, MessageChars: len("here is a TraceBack from prod"), Excerpt: "here is a TraceBack from prod",
	}, hit, "the earlier user message matched, and the excerpt keeps its original case")

	f.svc.Decide(ctx, f.request(userTurns("hello there")))
	require.Len(t, sink.hits, 1, "a turn without a keyword is not logged")
}

func TestIntentHitExcerpt(t *testing.T) {
	require.Equal(t, "…cdeFOOghi…", intentHitExcerpt("abcdeFOOghijk", "foo", 3))
	require.Equal(t, "ab", intentHitExcerpt("ab", "zz", 3), "no match keeps the start of the message")
	require.Equal(t, "…型写 1 到 355 的整…", intentHitExcerpt("让模型写 1 到 355 的整数吧", "1 到 355", 3))
}

func TestIntentRouteHitLog_WritesOneInsertPerBatch(t *testing.T) {
	db := &recordingHitDB{}
	l := newIntentRouteHitLog(db)
	at := time.Unix(1_790_000_000, 0)
	l.write([]IntentRouteHit{
		{Time: at, GroupID: 3, Keyword: "draw a", Excerpt: "x"},
		{Time: at, GroupID: 4, Keyword: "动画", UserTurn: 1},
	})
	require.Contains(t, db.query, "($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12), ($13,")
	require.Len(t, db.args, 2*intentRouteHitColumns)
	require.Equal(t, int64(4), db.args[intentRouteHitColumns+1])
	require.Equal(t, "动画", db.args[intentRouteHitColumns+8])
}

func TestIntentRouteHitWhere(t *testing.T) {
	now := time.Unix(1_790_000_000, 0)
	where, args := intentRouteHitWhere(IntentRouteHitQuery{GroupID: 4, Keyword: "动画", Hours: 99999}, now)
	require.Equal(t, "WHERE h.created_at >= $1 AND h.group_id = $2 AND h.keyword = $3", where)
	require.Equal(t, []any{now.Add(-intentRouteHitMaxHours * time.Hour), int64(4), "动画"}, args)

	_, args = intentRouteHitWhere(IntentRouteHitQuery{}, now)
	require.Equal(t, []any{now.Add(-24 * time.Hour)}, args, "the last day by default")
}
