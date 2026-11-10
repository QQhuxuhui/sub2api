//go:build unit

package service

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/cachesim"
)

// memSmoothStore keeps baselines in memory with the Redis script's ordering
// rule (a stored baseline that started later is kept).
type memSmoothStore struct {
	mu    sync.Mutex
	data  map[string]intentCacheSmoothBaseline
	gets  int
	puts  int
	block chan struct{} // when set, Get and Put wait on it
}

func (m *memSmoothStore) Get(ctx context.Context, key string) (*intentCacheSmoothBaseline, error) {
	if m.block != nil {
		select {
		case <-m.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets++
	b, ok := m.data[key]
	if !ok {
		return nil, nil
	}
	return &b, nil
}

func (m *memSmoothStore) Put(ctx context.Context, key string, b intentCacheSmoothBaseline, _ time.Duration) error {
	if m.block != nil {
		<-m.block // ignores ctx on purpose, like a stuck socket read
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.puts++
	if m.data == nil {
		m.data = map[string]intentCacheSmoothBaseline{}
	}
	if old, ok := m.data[key]; ok && (len(old.Seq) > len(b.Seq) || (len(old.Seq) == len(b.Seq) && old.Seq > b.Seq)) {
		return nil
	}
	m.data[key] = b
	return nil
}

func (m *memSmoothStore) only(t *testing.T) intentCacheSmoothBaseline {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	require.Len(t, m.data, 1)
	for _, b := range m.data {
		return b
	}
	return intentCacheSmoothBaseline{}
}

var smoothTestNow = time.Unix(1_790_000_000, 0)

func newSmoothPlan(store intentCacheSmoothStore, routed bool, baseline *intentCacheSmoothBaseline) *IntentCacheSmoothPlan {
	return &IntentCacheSmoothPlan{
		key: "k", routed: routed, seq: strconv.FormatInt(smoothTestNow.UnixNano(), 10),
		baseline: baseline, store: store, now: func() time.Time { return smoothTestNow },
	}
}

func smoothBaseline(total int, accountID int64, routed bool, age time.Duration) *intentCacheSmoothBaseline {
	return &intentCacheSmoothBaseline{Total: total, At: smoothTestNow.Add(-age).UnixMilli(), Seq: "1", AccountID: accountID, Routed: routed}
}

func withSmoothPlan(ctx context.Context, plan *IntentCacheSmoothPlan) context.Context {
	return WithIntentRouteDecision(ctx, &IntentRouteDecision{GroupID: 1, cacheSmooth: plan})
}

func TestIntentCacheSmoothCandidate(t *testing.T) {
	cases := []struct {
		name                        string
		prev, total, creation, want int
	}{
		{"previous turn bounds it", 50_000, 60_000, 0, 50_000 / 128 * 128},
		{"95 percent cap", 100_000, 60_000, 0, 57_000 / 128 * 128},
		{"cache writes are not reads", 100_000, 60_000, 10_000, 50_000 / 128 * 128},
		{"below the caching minimum", 900, 60_000, 0, 0},
		{"small turn", 50_000, 1000, 0, 0},
		{"no previous turn", 0, 60_000, 0, 0},
		{"rounds down to whole blocks", 5000, 10_000, 0, 4992},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, intentCacheSmoothCandidate(tc.prev, tc.total, tc.creation))
		})
	}
}

func TestIntentCacheSmoothPlan_WhichTurnsAreSmoothed(t *testing.T) {
	const original, routedAcct = int64(1), int64(2)
	cases := []struct {
		name     string
		routed   bool
		baseline *intentCacheSmoothBaseline
		account  int64
		want     bool
	}{
		{"routed away from the conversation's account", true, smoothBaseline(40_000, original, false, time.Minute), routedAcct, true},
		{"routed to the account it is already on", true, smoothBaseline(40_000, routedAcct, true, time.Minute), routedAcct, false},
		{"first turn back after a routed one", false, smoothBaseline(40_000, routedAcct, true, time.Minute), original, true},
		{"second turn back", false, smoothBaseline(40_000, original, false, time.Minute), original, false},
		{"ordinary turn moved by failover", false, smoothBaseline(40_000, original, false, time.Minute), 3, false},
		{"baseline older than ten minutes", true, smoothBaseline(40_000, original, false, 11*time.Minute), routedAcct, false},
		{"first turn of a conversation", true, nil, routedAcct, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev, ok := newSmoothPlan(nil, tc.routed, tc.baseline).PrevTotal(tc.account)
			require.Equal(t, tc.want, ok)
			if tc.want {
				require.Equal(t, 40_000, prev)
			}
		})
	}
	var nilPlan *IntentCacheSmoothPlan
	_, ok := nilPlan.PrevTotal(1)
	require.False(t, ok)
}

func TestIntentCacheSmoothPlan_CommitWritesOnceAndBoundsTheWait(t *testing.T) {
	store := &memSmoothStore{}
	plan := newSmoothPlan(store, true, nil)
	plan.Commit(9, 30_000)
	plan.Commit(9, 99_999)
	require.Equal(t, 1, store.puts)
	b := store.only(t)
	require.Equal(t, intentCacheSmoothBaseline{Total: 30_000, At: smoothTestNow.UnixMilli(), Seq: plan.seq, AccountID: 9, Routed: true}, b)

	stuck := &memSmoothStore{block: make(chan struct{})}
	t.Cleanup(func() { close(stuck.block) })
	started := time.Now()
	newSmoothPlan(stuck, false, nil).Commit(9, 30_000)
	require.Less(t, time.Since(started), intentCacheSmoothWriteWait+200*time.Millisecond)
}

func TestIntentCacheSmoothRedisStore_KeepsTheLaterStartedTurn(t *testing.T) {
	server := miniredis.RunT(t)
	store := &redisIntentCacheSmoothStore{rdb: redis.NewClient(&redis.Options{Addr: server.Addr()})}
	ctx := context.Background()

	got, err := store.Get(ctx, "conv")
	require.NoError(t, err)
	require.Nil(t, got)

	later := intentCacheSmoothBaseline{Total: 2, At: 2, Seq: "1790000000000000200", AccountID: 2}
	earlier := intentCacheSmoothBaseline{Total: 1, At: 3, Seq: "1790000000000000100", AccountID: 1, Routed: true}
	require.NoError(t, store.Put(ctx, "conv", later, intentCacheSmoothTTL))
	// Started first, finished last: must not overwrite.
	require.NoError(t, store.Put(ctx, "conv", earlier, intentCacheSmoothTTL))
	got, err = store.Get(ctx, "conv")
	require.NoError(t, err)
	require.Equal(t, later, *got)
	require.InDelta(t, intentCacheSmoothTTL.Seconds(), server.TTL(intentCacheSmoothKeyPrefix+"conv").Seconds(), 1)

	// A shorter decimal string is a smaller number; a longer one is larger.
	require.NoError(t, store.Put(ctx, "conv", intentCacheSmoothBaseline{Total: 3, Seq: "999"}, intentCacheSmoothTTL))
	got, _ = store.Get(ctx, "conv")
	require.Equal(t, 2, got.Total)
	require.NoError(t, store.Put(ctx, "conv", intentCacheSmoothBaseline{Total: 4, Seq: "17900000000000000000"}, intentCacheSmoothTTL))
	got, _ = store.Get(ctx, "conv")
	require.Equal(t, 4, got.Total)
}

func TestIntentRouter_CacheSmoothPlanFollowsTheSwitch(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	store := &memSmoothStore{}
	f.svc.smoothStore = store

	// Off: no plan, the store is never touched.
	f.saveRouter(t, keywordOnly([]string{"traceback"}, []string{"讲个笑话"}))
	d := f.svc.Decide(ctx, f.request(userTurns("traceback here")))
	require.Nil(t, d.CacheSmoothPlan())
	require.Zero(t, store.gets)

	f.saveRouter(t, func(in *IntentRouterInput) {
		keywordOnly([]string{"traceback"}, []string{"讲个笑话"})(in)
		in.CacheSmoothEnabled = true
	})
	routed := f.svc.Decide(ctx, f.request(userTurns("traceback here")))
	require.NotEmpty(t, routed.AccountIDs)
	plan := routed.CacheSmoothPlan()
	require.NotNil(t, plan)
	require.True(t, plan.routed)
	require.Equal(t, 1, store.gets)
	require.Equal(t, strconv.FormatInt(f.now.UnixNano(), 10), plan.seq)

	plain := f.svc.Decide(ctx, f.request(userTurns("traceback here", "thanks, now please explain the next step")))
	require.NotNil(t, plain.CacheSmoothPlan())
	require.False(t, plain.CacheSmoothPlan().routed)
	// Same conversation (first user message) and model: same key.
	require.Equal(t, plan.key, plain.CacheSmoothPlan().key)
	other := f.svc.Decide(ctx, f.request(`{"model":"other","messages":[{"role":"user","content":"traceback here"}]}`))
	require.NotEqual(t, plan.key, other.CacheSmoothPlan().key, "models never share a cache")

	// A decision carrying only a plan still travels in the context.
	carried := WithIntentRouteDecision(ctx, &IntentRouteDecision{GroupID: 1, cacheSmooth: plan})
	require.Same(t, plan, IntentCacheSmoothPlanFromContext(carried))
	require.Nil(t, IntentRouteDecisionFromContext(WithIntentRouteDecision(ctx, &IntentRouteDecision{GroupID: 1})))
}

func TestIntentRouter_CacheSmoothReadIsBounded(t *testing.T) {
	f := newIntentRouteFixture(t)
	store := &memSmoothStore{block: make(chan struct{})}
	t.Cleanup(func() { close(store.block) })
	f.svc.smoothStore = store
	f.saveRouter(t, func(in *IntentRouterInput) {
		keywordOnly([]string{"traceback"}, []string{"讲个笑话"})(in)
		in.CacheSmoothEnabled = true
	})
	started := time.Now()
	d := f.svc.Decide(context.Background(), f.request(userTurns("hello")))
	require.Less(t, time.Since(started), intentCacheSmoothReadWait+300*time.Millisecond)
	require.NotNil(t, d.CacheSmoothPlan())
	require.Nil(t, d.CacheSmoothPlan().baseline)
}

func TestIntentRouter_CacheSmoothStoredInRouterConfig(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	f.saveRouter(t, func(in *IntentRouterInput) { in.CacheSmoothEnabled = true })
	view, err := f.svc.GetRouter(ctx, f.groupID)
	require.NoError(t, err)
	require.True(t, view.CacheSmoothEnabled)
	require.True(t, f.svc.config(ctx, f.groupID).CacheSmoothEnabled)
	f.saveRouter(t, nil)
	view, err = f.svc.GetRouter(ctx, f.groupID)
	require.NoError(t, err)
	require.False(t, view.CacheSmoothEnabled)
}

// ---- the simulation layer ----

func smoothRewriteContext(t *testing.T, plan *IntentCacheSmoothPlan) (*cachesim.State, func(*Account) openAIResponseModelRewrite) {
	t.Helper()
	c, _ := newRewriteTestContext(t, "gpt-6.1-sol")
	state := cachesim.New()
	ctx := cachesim.WithState(c.Request.Context(), state)
	if plan != nil {
		ctx = withSmoothPlan(ctx, plan)
	}
	c.Request = c.Request.WithContext(ctx)
	return state, func(account *Account) openAIResponseModelRewrite {
		return newOpenAIResponseModelRewrite(c, account, "gpt-6.1-sol", "gpt-6.1-sol")
	}
}

func TestCacheSmooth_RoutedTurnReportsThePreviousTurnsCache(t *testing.T) {
	SetOpenAICacheSimWindowStore(&memCacheSimStore{})
	t.Cleanup(func() { SetOpenAICacheSimWindowStore(nil) })
	store := &memSmoothStore{}
	plan := newSmoothPlan(store, true, smoothBaseline(40_000, 1, false, time.Minute))
	_, rewrite := smoothRewriteContext(t, plan)
	account := simTestAccount(nil) // no simulation of its own
	rw := rewrite(account)
	require.NotNil(t, rw.cacheSim, "smoothing runs without the account's simulation")

	line := `data: {"type":"response.completed","response":{"model":"gpt-6.1-sol","usage":{"input_tokens":42000,"input_tokens_details":{"cached_tokens":0},"output_tokens":5}}}`
	got := rw.applySSELine(line)
	require.Contains(t, got, `"cached_tokens":39808`) // min(40000, 95% of 42000) in whole 128-token blocks
	require.Contains(t, got, `"input_tokens":42000`)

	// The baseline is in the store as soon as the usage was decided.
	b := store.only(t)
	require.Equal(t, 42_000, b.Total, "the raw input, not the smoothed number")
	require.Equal(t, account.ID, b.AccountID)
	require.True(t, b.Routed)
}

func TestCacheSmooth_NoPlanOrNoSwitchLeavesUsageAlone(t *testing.T) {
	line := `data: {"type":"response.completed","response":{"usage":{"input_tokens":42000,"input_tokens_details":{"cached_tokens":0}}}}`
	_, rewrite := smoothRewriteContext(t, nil)
	require.Nil(t, rewrite(simTestAccount(nil)).cacheSim)

	store := &memSmoothStore{}
	plan := newSmoothPlan(store, false, smoothBaseline(40_000, 77, false, time.Minute)) // same account, not routed
	_, rewrite = smoothRewriteContext(t, plan)
	require.Equal(t, line, rewrite(simTestAccount(nil)).applySSELine(line))
	require.Equal(t, 42_000, store.only(t).Total, "every decided turn still leaves its baseline")
}

func TestCacheSmooth_MissingCacheFieldOnlyOnFinalUsage(t *testing.T) {
	store := &memSmoothStore{}
	plan := newSmoothPlan(store, true, smoothBaseline(40_000, 1, false, time.Minute))
	state, rewrite := smoothRewriteContext(t, plan)
	rw := rewrite(simTestAccount(nil))

	intermediate := `data: {"type":"response.in_progress","response":{"usage":{"input_tokens":10000}}}`
	require.Equal(t, intermediate, rw.applySSELine(intermediate))
	require.False(t, state.Decided(), "an intermediate usage does not fix the decision")

	final := `data: {"type":"response.completed","response":{"usage":{"input_tokens":42000,"output_tokens":5}}}`
	got := rw.applySSELine(final)
	require.Contains(t, got, `"input_tokens_details":{"cached_tokens":39808}`)
	require.Equal(t, 42_000, store.only(t).Total)

	// Chat body and Anthropic message.
	_, rewrite = smoothRewriteContext(t, newSmoothPlan(&memSmoothStore{}, true, smoothBaseline(40_000, 1, false, time.Minute)))
	chat := rewrite(simTestAccount(nil)).applyJSON([]byte(`{"object":"chat.completion","usage":{"prompt_tokens":42000,"completion_tokens":1}}`))
	require.Contains(t, string(chat), `"prompt_tokens_details":{"cached_tokens":39808}`)

	_, rewrite = smoothRewriteContext(t, newSmoothPlan(&memSmoothStore{}, true, smoothBaseline(40_000, 1, false, time.Minute)))
	anth := rewrite(simTestAccount(nil))
	anth.anthropicShape = true
	msg := anth.applyJSON([]byte(`{"type":"message","usage":{"input_tokens":42000,"output_tokens":1}}`))
	require.Contains(t, string(msg), `"cache_read_input_tokens":39808`)
	require.Contains(t, string(msg), `"input_tokens":2192`)
}

func TestCacheSmooth_MissingFieldWithoutPlanUnchanged(t *testing.T) {
	SetOpenAICacheSimWindowStore(&memCacheSimStore{})
	t.Cleanup(func() { SetOpenAICacheSimWindowStore(nil) })
	_, rewrite := smoothRewriteContext(t, nil)
	rw := rewrite(simTestAccount(simEnabledExtra(85, 85)))
	final := `data: {"type":"response.completed","response":{"usage":{"input_tokens":42000,"output_tokens":5}}}`
	require.Equal(t, final, rw.applySSELine(final))
	usage := &apicompat.ChatUsage{PromptTokens: 42000}
	require.Same(t, usage, rw.applyChatUsage(usage))
}

func TestCacheSmooth_FirstBaselineWithoutCacheFields(t *testing.T) {
	// An upstream that never reports cache reads still builds a baseline.
	store := &memSmoothStore{}
	_, rewrite := smoothRewriteContext(t, newSmoothPlan(store, false, nil))
	final := `data: {"type":"response.completed","response":{"usage":{"input_tokens":30000,"output_tokens":5}}}`
	require.Equal(t, final, rewrite(simTestAccount(nil)).applySSELine(final), "nothing to add on a first turn")
	require.Equal(t, 30_000, store.only(t).Total)
}

func TestCacheSmooth_TypedUsageThroughTheWrapper(t *testing.T) {
	newRW := func() openAIResponseModelRewrite {
		_, rewrite := smoothRewriteContext(t, newSmoothPlan(&memSmoothStore{}, true, smoothBaseline(40_000, 1, false, time.Minute)))
		return rewrite(simTestAccount(nil))
	}
	chat := newRW().applyChatUsage(&apicompat.ChatUsage{PromptTokens: 42000})
	require.NotNil(t, chat.PromptTokensDetails)
	require.Equal(t, 39808, chat.PromptTokensDetails.CachedTokens)

	final := newRW().applyResponsesUsage(&apicompat.ResponsesUsage{InputTokens: 42000})
	require.Equal(t, 39808, final.InputTokensDetails.CachedTokens)

	rw := newRW()
	mid := &apicompat.ResponsesStreamEvent{Type: "response.in_progress", Response: &apicompat.ResponsesResponse{Usage: &apicompat.ResponsesUsage{InputTokens: 10000}}}
	rw.applyResponsesEventUsage(mid)
	require.Nil(t, mid.Response.Usage.InputTokensDetails)
	done := &apicompat.ResponsesStreamEvent{Type: "response.completed", Response: &apicompat.ResponsesResponse{Usage: &apicompat.ResponsesUsage{InputTokens: 42000}}}
	rw.applyResponsesEventUsage(done)
	require.Equal(t, 39808, done.Response.Usage.InputTokensDetails.CachedTokens)
}

type countingCacheSimStore struct {
	memCacheSimStore
	peeks int
}

func (m *countingCacheSimStore) Peek(ctx context.Context, accountID int64, h time.Duration) (OpenAICacheSimWindow, error) {
	m.peeks++
	return m.memCacheSimStore.Peek(ctx, accountID, h)
}

func TestCacheSmooth_TakesTheLargerOfAccountSimulationAndSmoothing(t *testing.T) {
	win := &countingCacheSimStore{}
	SetOpenAICacheSimWindowStore(win)
	t.Cleanup(func() { SetOpenAICacheSimWindowStore(nil) })

	// Account simulation off: the window is never read.
	_, rewrite := smoothRewriteContext(t, newSmoothPlan(&memSmoothStore{}, true, smoothBaseline(40_000, 1, false, time.Minute)))
	rewrite(simTestAccount(nil)).applySSELine(`data: {"type":"response.completed","response":{"usage":{"input_tokens":42000,"input_tokens_details":{"cached_tokens":2000}}}}`)
	require.Zero(t, win.peeks)

	// Simulation pulls 2000 -> 85% of 10000; smoothing offers a small previous turn: the larger wins.
	_, rewrite = smoothRewriteContext(t, newSmoothPlan(&memSmoothStore{}, true, smoothBaseline(2048, 1, false, time.Minute)))
	got := rewrite(simTestAccount(simEnabledExtra(85, 85))).applySSELine(`data: {"type":"response.completed","response":{"usage":{"input_tokens":10000,"input_tokens_details":{"cached_tokens":2000}}}}`)
	require.Contains(t, got, `"cached_tokens":8448`)
	require.Equal(t, 1, win.peeks)

	// Smoothing larger than the simulation.
	_, rewrite = smoothRewriteContext(t, newSmoothPlan(&memSmoothStore{}, true, smoothBaseline(40_000, 1, false, time.Minute)))
	got = rewrite(simTestAccount(simEnabledExtra(50, 50))).applySSELine(`data: {"type":"response.completed","response":{"usage":{"input_tokens":42000,"input_tokens_details":{"cached_tokens":2000}}}}`)
	require.Contains(t, got, `"cached_tokens":39808`)
}

// ---- billing ----

func smoothRecord(t *testing.T, ctx context.Context, account *Account, result *OpenAIForwardResult) *UsageLog {
	t.Helper()
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	require.NoError(t, svc.RecordUsage(ctx, &OpenAIRecordUsageInput{Result: result, APIKey: &APIKey{ID: 10}, User: &User{ID: 20}, Account: account}))
	require.NotNil(t, usageRepo.lastLog)
	return usageRepo.lastLog
}

func TestCacheSmooth_BilledAsTheClientSawIt(t *testing.T) {
	win := &memCacheSimStore{}
	SetOpenAICacheSimWindowStore(win)
	t.Cleanup(func() { SetOpenAICacheSimWindowStore(nil) })

	for _, search := range []bool{false, true} {
		plan := newSmoothPlan(&memSmoothStore{}, true, smoothBaseline(40_000, 1, false, time.Minute))
		state, rewrite := smoothRewriteContext(t, plan)
		account := simTestAccount(nil)
		rewrite(account).applySSELine(`data: {"type":"response.completed","response":{"usage":{"input_tokens":42000,"input_tokens_details":{"cached_tokens":0}}}}`)

		ctx := withSmoothPlan(cachesim.WithState(context.Background(), state), plan)
		result := &OpenAIForwardResult{RequestID: "r", Model: "gpt-5.4", UpstreamModel: "gpt-5.4", Usage: OpenAIUsage{InputTokens: 42000, OutputTokens: 5}, Duration: time.Second}
		if search {
			result.WebSearchCalls = 1
		}
		log := smoothRecord(t, ctx, account, result)
		require.Equal(t, 39808, log.CacheReadTokens, "search=%v", search)
		require.Equal(t, 0, *log.RawCacheReadTokens)
		if !search {
			require.Greater(t, log.CacheSimCostDelta, 0.0)
		} // a search request is billed per call, whatever its cache reads
	}
	require.Zero(t, win.adds, "no window for an account without its own simulation")

	// Account with its own simulation: the window counts the final number once.
	plan := newSmoothPlan(&memSmoothStore{}, true, smoothBaseline(40_000, 1, false, time.Minute))
	state, rewrite := smoothRewriteContext(t, plan)
	account := simTestAccount(simEnabledExtra(50, 50))
	rewrite(account).applySSELine(`data: {"type":"response.completed","response":{"usage":{"input_tokens":42000,"input_tokens_details":{"cached_tokens":2000}}}}`)
	ctx := withSmoothPlan(cachesim.WithState(context.Background(), state), plan)
	smoothRecord(t, ctx, account, &OpenAIForwardResult{RequestID: "r2", Model: "gpt-5.4", UpstreamModel: "gpt-5.4", Usage: OpenAIUsage{InputTokens: 42000, CacheReadInputTokens: 2000, OutputTokens: 5}, Duration: time.Second})
	require.Equal(t, 1, win.adds)
	w, _ := win.Peek(ctx, account.ID, time.Hour)
	require.Equal(t, 39808.0, w.Cached)

	// Without a plan, search requests keep leaving the account simulation (unchanged behavior).
	state2 := cachesim.New()
	state2.Decide(42000, 2000, func(int, int) int { return 30000 })
	log := smoothRecord(t, cachesim.WithState(context.Background(), state2), account, &OpenAIForwardResult{RequestID: "r3", Model: "gpt-5.4", UpstreamModel: "gpt-5.4", Usage: OpenAIUsage{InputTokens: 42000, CacheReadInputTokens: 2000}, WebSearchCalls: 1, Duration: time.Second})
	require.Equal(t, 2000, log.CacheReadTokens)
}
