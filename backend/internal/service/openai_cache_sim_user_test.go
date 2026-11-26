package service

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/cachesim"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// memCacheSimUserStore implements the user store with the Go reference
// decision (the Redis Lua script mirrors it; see the repository test).
type memCacheSimUserStore struct {
	mu      sync.Mutex
	win     map[int64]*OpenAICacheSimUserWindow
	decides int
	counts  int
}

func (m *memCacheSimUserStore) get(id int64) *OpenAICacheSimUserWindow {
	if m.win == nil {
		m.win = map[int64]*OpenAICacheSimUserWindow{}
	}
	w := m.win[id]
	if w == nil {
		w = &OpenAICacheSimUserWindow{}
		m.win[id] = w
	}
	return w
}

func (m *memCacheSimUserStore) DecideAndCount(_ context.Context, in OpenAICacheSimUserDecideInput) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.decides++
	w := m.get(in.UserID)
	sim := openAICacheSimUserDecide(*w, in.Total, in.Cached, in.Ceiling, in.LoBP, in.HiBP, in.TargetBP)
	w.WT += float64(in.Total)
	w.WC += float64(sim)
	w.HT += int64(in.Total)
	w.HC += int64(sim)
	return sim, nil
}

func (m *memCacheSimUserStore) Count(_ context.Context, userID int64, _ string, _ time.Duration, total, cached int, hourOnly bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counts++
	w := m.get(userID)
	if !hourOnly {
		w.WT += float64(total)
		w.WC += float64(cached)
	}
	w.HT += int64(total)
	w.HC += int64(cached)
	return nil
}

func (m *memCacheSimUserStore) PeekUser(_ context.Context, userID int64, _ time.Duration) (OpenAICacheSimWindow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.get(userID)
	return OpenAICacheSimWindow{Total: w.WT, Cached: w.WC}, nil
}

func userSimConfig(lo, hi float64) OpenAICacheSimConfig {
	cfg := defaultOpenAICacheSimConfig()
	cfg.Enabled, cfg.Lo, cfg.Hi = true, lo, hi
	return cfg
}

func TestOpenAICacheSimHistoryRatio(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		kind  string
		ratio func(float64) bool
	}{
		{"string input", `{"input":"hi"}`, openAICacheSimHistoryNew, func(r float64) bool { return r == 0 }},
		{"only user", `{"instructions":"sys","input":[{"role":"user","content":"hi"}]}`, openAICacheSimHistoryNew, func(r float64) bool { return r == 0 }},
		{"compaction trigger is client input", `{"input":[{"role":"user","content":"` + longText(4000) + `"},{"type":"compaction_trigger"}]}`, openAICacheSimHistoryNew, func(r float64) bool { return r == 0 }},
		{"unknown item", `{"input":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"},{"type":"mystery"}]}`, openAICacheSimHistoryUnknownItem, func(r float64) bool { return r == 0 }},
		{"agent loop", `{"input":[{"role":"user","content":"` + longText(1000) + `"},{"type":"function_call","call_id":"1","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"1","output":"` + longText(1000) + `"}]}`, openAICacheSimHistoryContinuation, func(r float64) bool { return r > 0.4 && r < 0.6 }},
		{"mcp tool call", `{"input":[{"role":"user","content":"` + longText(1000) + `"},{"type":"mcp_tool_call","id":"1"},{"type":"mcp_tool_call_output","id":"1","output":"x"}]}`, openAICacheSimHistoryContinuation, func(r float64) bool { return r > 0.9 }},
		{"reasoning+call one block", `{"input":[{"role":"user","content":"` + longText(1000) + `"},{"type":"reasoning","summary":[]},{"type":"function_call","call_id":"1","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"1","output":"` + longText(1000) + `"}]}`, openAICacheSimHistoryContinuation, func(r float64) bool { return r > 0.4 && r < 0.6 }},
		{"image counted flat", `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,` + longText(200000) + `"}]},{"role":"assistant","content":"ok"},{"role":"user","content":"` + longText(4096) + `"}]}`, openAICacheSimHistoryContinuation, func(r float64) bool { return r > 0.3 && r < 0.7 }},
		{"previous_response_id only", `{"previous_response_id":"resp_1","input":[{"role":"user","content":"next"}]}`, openAICacheSimHistoryNew, func(r float64) bool { return r == 0 }},
		{"bad json", `{`, openAICacheSimHistoryParseFailed, func(r float64) bool { return r == 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, kind := openAICacheSimHistoryRatio(nil, []byte(tc.body))
			require.Equal(t, tc.kind, kind)
			require.True(t, tc.ratio(r), "ratio %v", r)
		})
	}
}

func TestOpenAICacheSimHistoryRatio_LegacyMessagesMatchesInput(t *testing.T) {
	// A messages-only body is read exactly as the Responses entry normalizes it.
	legacy := []byte(`{"messages":[{"role":"user","content":"` + longText(5000) + `"},{"role":"assistant","content":"done"},{"role":"user","content":"more"}]}`)
	normalized, changed, err := normalizeOpenAIResponsesLegacyIngressWithConfig(nil, legacy)
	require.NoError(t, err)
	require.True(t, changed)
	rl, kl := openAICacheSimHistoryRatio(nil, legacy)
	rn, kn := openAICacheSimHistoryRatio(nil, normalized)
	require.Equal(t, openAICacheSimHistoryContinuation, kl)
	require.Equal(t, kn, kl)
	require.Equal(t, rn, rl)
	require.Greater(t, rl, 0.9)
}

func longText(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a' + byte(i%26)
	}
	return string(b)
}

func TestOpenAICacheSimUserDecide(t *testing.T) {
	bp := openAICacheSimBasisPoints
	cases := []struct {
		name                   string
		win                    OpenAICacheSimUserWindow
		total, cached, ceiling int
		lo, hi, target         float64
		want                   int
	}{
		{"no ceiling", OpenAICacheSimUserWindow{}, 10000, 2000, 0, 82, 88, 85, 2000},
		{"upstream above ceiling", OpenAICacheSimUserWindow{}, 10000, 6000, 5000, 82, 88, 85, 6000},
		{"lift to target", OpenAICacheSimUserWindow{}, 10000, 2000, 9500, 82, 88, 85, 8448},
		{"truncated to ceiling", OpenAICacheSimUserWindow{}, 10000, 2000, 4096, 82, 88, 85, 4096},
		{"round up to reach lo", OpenAICacheSimUserWindow{}, 2048, 1024, 1920, 82, 88, 85, 1792},
		{"exact boundary 58%", OpenAICacheSimUserWindow{}, 12800, 1024, 9000, 58, 58, 58, 7424},
		{"exact boundary 50.16%", OpenAICacheSimUserWindow{}, 160000, 1024, 100000, 50.16, 50.16, 50.16, 80256},
		{"granularity cannot fit", OpenAICacheSimUserWindow{}, 1408, 1024, 1280, 82, 88, 85, 1152},
		{"hour already at hi", OpenAICacheSimUserWindow{WT: 100000, WC: 88000, HT: 100000, HC: 88000}, 10000, 2000, 9500, 82, 88, 85, 5376},
		{"old window debt cannot push hour past hi", OpenAICacheSimUserWindow{WT: 1000000, WC: 600000, HT: 0, HC: 0}, 10000, 6500, 9500, 82, 88, 85, 8704},
		{"catch up to hi", OpenAICacheSimUserWindow{WT: 50000, WC: 30000, HT: 50000, HC: 30000}, 10000, 2000, 9500, 82, 88, 82, 9472},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := openAICacheSimUserDecide(tc.win, tc.total, tc.cached, tc.ceiling, bp(tc.lo), bp(tc.hi), bp(tc.target))
			require.Equal(t, tc.want, got)
			require.Zero(t, got%128*boolInt(got != tc.cached), "lifted values are whole blocks")
		})
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestOpenAICacheSimUserCeiling(t *testing.T) {
	cfg := userSimConfig(82, 88)
	require.Zero(t, openAICacheSimUserCeiling(cfg, 0, true, 10000, 2000, 0), "new conversation")
	require.Zero(t, openAICacheSimUserCeiling(cfg, 0.9, true, 10000, 0, 0), "zero cache")
	require.Zero(t, openAICacheSimUserCeiling(cfg, 0.9, true, 1000, 500, 0), "under 1024")
	require.Zero(t, openAICacheSimUserCeiling(cfg, 0.9, false, 10000, 2000, 0), "not Responses shape")
	require.Equal(t, 8960, openAICacheSimUserCeiling(cfg, 0.9, true, 10000, 2000, 0))
	require.Equal(t, 6912, openAICacheSimUserCeiling(cfg, 0.9, true, 10000, 2000, 3000), "creation share excluded")
	cfg.Cap = 50
	require.Equal(t, 4992, openAICacheSimUserCeiling(cfg, 0.9, true, 10000, 2000, 0), "cap")
}

func TestOpenAICacheSimUserMode_Precedence(t *testing.T) {
	resetOpenAICacheSimUserCacheForTest()
	t.Cleanup(resetOpenAICacheSimUserCacheForTest)
	repo := &contentModerationTestSettingRepo{values: map[string]string{}}
	settings := &SettingService{settingRepo: repo}
	ctx := context.Background()

	require.Equal(t, OpenAICacheSimSourceAccount, resolveOpenAICacheSimUserMode(ctx, repo, 7).Source)

	_, err := settings.SetOpenAICacheSimGlobal(ctx, userSimConfig(82, 88))
	require.NoError(t, err)
	m := resolveOpenAICacheSimUserMode(ctx, repo, 7)
	require.Equal(t, OpenAICacheSimSourceGlobal, m.Source)
	require.True(t, m.Active)

	off := userSimConfig(82, 88)
	off.Enabled = false
	require.NoError(t, settings.SetOpenAICacheSimUser(ctx, 7, &off))
	on := userSimConfig(90, 95)
	require.NoError(t, settings.SetOpenAICacheSimUser(ctx, 8, &on))
	m = resolveOpenAICacheSimUserMode(ctx, repo, 7)
	require.True(t, m.Off())
	require.Equal(t, 90.0, resolveOpenAICacheSimUserMode(ctx, repo, 8).Config.Lo)

	// Both users' saves are kept (read-modify-write).
	overrides, err := settings.GetOpenAICacheSimOverrides(ctx)
	require.NoError(t, err)
	require.Len(t, overrides, 2)

	globalOff := userSimConfig(82, 88)
	globalOff.Enabled = false
	_, err = settings.SetOpenAICacheSimGlobal(ctx, globalOff)
	require.NoError(t, err)
	require.True(t, resolveOpenAICacheSimUserMode(ctx, repo, 8).Active, "override on works with global off")
	require.Equal(t, OpenAICacheSimSourceAccount, resolveOpenAICacheSimUserMode(ctx, repo, 9).Source)

	require.NoError(t, settings.SetOpenAICacheSimUser(ctx, 7, nil))
	require.Equal(t, OpenAICacheSimSourceAccount, resolveOpenAICacheSimUserMode(ctx, repo, 7).Source, "inherit again")
}

func activeUserPlan(ratio float64) *OpenAICacheSimUserPlan {
	return &OpenAICacheSimUserPlan{UserID: 20, Config: userSimConfig(82, 88), Source: OpenAICacheSimSourceGlobal, Kind: OpenAICacheSimUserActive, HistoryRatio: ratio}
}

func TestOpenAICacheSimUser_RewriteAndRecordAgree(t *testing.T) {
	store := &memCacheSimUserStore{}
	SetOpenAICacheSimUserStore(store)
	t.Cleanup(func() { SetOpenAICacheSimUserStore(nil) })
	c, _ := newRewriteTestContext(t, "gpt-5.4")
	state := cachesim.New()
	ctx := WithOpenAICacheSimUserPlan(cachesim.WithState(c.Request.Context(), state), activeUserPlan(0.95))
	c.Request = c.Request.WithContext(ctx)
	account := simTestAccount(map[string]any{}) // account simulation off: by-user still applies
	data := []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":10000,"input_tokens_details":{"cached_tokens":2000},"output_tokens":7}}}`)
	out := newOpenAIResponseModelRewrite(c, account, "gpt-5.4", "gpt-5.4").applyJSON(data)
	shown := int(gjson.GetBytes(out, "response.usage.input_tokens_details.cached_tokens").Int())
	require.Greater(t, shown, 2000)
	require.LessOrEqual(t, shown, 9472)
	require.Equal(t, 1, store.decides)

	repo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	require.NoError(t, svc.RecordUsage(ctx, &OpenAIRecordUsageInput{Result: &OpenAIForwardResult{RequestID: "u1", Model: "gpt-5.4", UpstreamModel: "gpt-5.4", Usage: OpenAIUsage{InputTokens: 10000, CacheReadInputTokens: 2000, OutputTokens: 7}}, APIKey: &APIKey{ID: 10, UserID: 20}, User: &User{ID: 20}, Account: account}))
	require.Equal(t, shown, repo.lastLog.CacheReadTokens, "client and billing agree")
	require.NotNil(t, repo.lastLog.CacheSimReachableTokens)
	require.Equal(t, 9472, *repo.lastLog.CacheSimReachableTokens)
	require.Zero(t, store.counts, "the decision already counted the request")
}

func TestOpenAICacheSimUser_NewConversationNotLifted(t *testing.T) {
	store := &memCacheSimUserStore{}
	SetOpenAICacheSimUserStore(store)
	t.Cleanup(func() { SetOpenAICacheSimUserStore(nil) })
	c, _ := newRewriteTestContext(t, "gpt-5.4")
	c.Request = c.Request.WithContext(WithOpenAICacheSimUserPlan(cachesim.WithState(c.Request.Context(), cachesim.New()), activeUserPlan(0)))
	data := []byte(`{"usage":{"input_tokens":10000,"input_tokens_details":{"cached_tokens":2000},"output_tokens":7}}`)
	out := newOpenAIResponseModelRewrite(c, simTestAccount(simEnabledExtra(85, 85)), "gpt-5.4", "gpt-5.4").applyJSON(data)
	require.Equal(t, int64(2000), gjson.GetBytes(out, "usage.input_tokens_details.cached_tokens").Int(), "account simulation is replaced, not stacked")
}

func TestOpenAICacheSimUser_ChatShapeNotLifted(t *testing.T) {
	SetOpenAICacheSimUserStore(&memCacheSimUserStore{})
	t.Cleanup(func() { SetOpenAICacheSimUserStore(nil) })
	c, _ := newRewriteTestContext(t, "gpt-5.4")
	c.Request = c.Request.WithContext(WithOpenAICacheSimUserPlan(cachesim.WithState(c.Request.Context(), cachesim.New()), activeUserPlan(0.95)))
	data := []byte(`{"object":"chat.completion","usage":{"prompt_tokens":10000,"prompt_tokens_details":{"cached_tokens":2000},"completion_tokens":7}}`)
	out := newOpenAIResponseModelRewrite(c, simTestAccount(map[string]any{}), "gpt-5.4", "gpt-5.4").applyJSON(data)
	require.Equal(t, int64(2000), gjson.GetBytes(out, "usage.prompt_tokens_details.cached_tokens").Int())
}

func TestOpenAICacheSimUser_OffChangesNothing(t *testing.T) {
	c, _ := newRewriteTestContext(t, "gpt-5.4")
	plan := &OpenAICacheSimUserPlan{UserID: 20, Kind: OpenAICacheSimUserOff, Source: OpenAICacheSimSourceUser}
	c.Request = c.Request.WithContext(WithOpenAICacheSimUserPlan(cachesim.WithState(c.Request.Context(), cachesim.New()), plan))
	require.Nil(t, openAICacheSimForRequest(c, simTestAccount(simEnabledExtra(85, 85))), "account simulation does not apply either")
}

func TestOpenAICacheSimUser_UndecidedRecordedRawAndCounted(t *testing.T) {
	store := &memCacheSimUserStore{}
	SetOpenAICacheSimUserStore(store)
	t.Cleanup(func() { SetOpenAICacheSimUserStore(nil) })
	ctx := WithOpenAICacheSimUserPlan(cachesim.WithState(context.Background(), cachesim.New()), activeUserPlan(0.95))
	repo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	require.NoError(t, svc.RecordUsage(ctx, &OpenAIRecordUsageInput{Result: &OpenAIForwardResult{RequestID: "u2", Model: "gpt-5.4", UpstreamModel: "gpt-5.4", Usage: OpenAIUsage{InputTokens: 10000, CacheReadInputTokens: 2000, OutputTokens: 7}}, APIKey: &APIKey{ID: 10, UserID: 20}, User: &User{ID: 20}, Account: simTestAccount(map[string]any{})}))
	require.Equal(t, 2000, repo.lastLog.CacheReadTokens)
	require.NotNil(t, repo.lastLog.CacheSimReachableTokens)
	require.Equal(t, 2000, *repo.lastLog.CacheSimReachableTokens)
	require.Equal(t, 1, store.counts)
}

func TestOpenAICacheSimUserVerdict(t *testing.T) {
	mode := OpenAICacheSimUserMode{Config: userSimConfig(82, 88), Source: OpenAICacheSimSourceGlobal, Active: true}
	// 1024 fully reachable text tokens + 9216 zero-cache embeddings tokens.
	a := openAICacheUserAgg{all: OpenAICacheTokenBucket{Input: 9216, CacheRead: 1024}, reachable: 1024, covered: 1024}
	_, _, v := openAICacheUserVerdict(mode, a)
	require.Equal(t, OpenAICacheVerdictCeilingShort, v)
	a = openAICacheUserAgg{all: OpenAICacheTokenBucket{Input: 3000, CacheRead: 7000}, reachable: 9000, covered: 10000}
	_, _, v = openAICacheUserVerdict(mode, a)
	require.Equal(t, OpenAICacheVerdictStrategyShort, v)
	a = openAICacheUserAgg{all: OpenAICacheTokenBucket{Input: 1500, CacheRead: 8500}, reachable: 9000, covered: 10000}
	_, _, v = openAICacheUserVerdict(mode, a)
	require.Equal(t, OpenAICacheVerdictOK, v)
	_, _, v = openAICacheUserVerdict(OpenAICacheSimUserMode{Source: OpenAICacheSimSourceUser}, a)
	require.Empty(t, v, "opted-out user")
	_, _, v = openAICacheUserVerdict(mode, openAICacheUserAgg{})
	require.Empty(t, v, "no input")
}

func TestOpenAICacheSimHistoryRatio_LargeBodyBoundedAllocations(t *testing.T) {
	// Escaped multi-line text (as in code and JSON tool output) must not be
	// decoded: neither per-item allocations nor body-sized buffers.
	escaped := strings.Repeat(`line \"x\"\n`, 40)
	items := make([]byte, 0, 1<<21)
	items = append(items, `{"input":[`...)
	for i := 0; i < 2000; i++ {
		if i > 0 {
			items = append(items, ',')
		}
		items = append(items, `{"role":"user","content":"`+escaped+`"},{"type":"function_call","call_id":"c","name":"f","arguments":"{\"a\":1}"},{"type":"function_call_output","call_id":"c","output":"`+escaped+`"}`...)
	}
	items = append(items, `]}`...)
	run := func() {
		r, kind := openAICacheSimHistoryRatio(nil, items)
		if kind != openAICacheSimHistoryContinuation || r <= 0.9 {
			panic("unexpected ratio")
		}
	}
	run()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	run()
	runtime.ReadMemStats(&after)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(len(items)/20), "allocations stay far below the body size")
}

func TestOpenAICacheSimRawSize_DataURLCountedFlat(t *testing.T) {
	big := `{"type":"input_image","image_url":"data:image/png;base64,` + longText(100000) + `"}`
	require.Less(t, openAICacheSimRawSize(gjson.Parse(big)), 4200)
	small := `{"text":"see data:x"}`
	require.Equal(t, len(small), openAICacheSimRawSize(gjson.Parse(small)))
}

func TestOpenAICacheSimRawSize_EscapedQuotesAndHTTPSImages(t *testing.T) {
	text := `{"role":"user","content":"log says \"data:\" then ` + longText(64000) + `"}`
	require.Equal(t, len(text)-2, openAICacheSimRawSize(gjson.Parse(text)), "escaped quotes inside text are not string starts (each counts one byte)")
	img := `{"type":"input_image","image_url":"https://example.com/a.png"}`
	require.Equal(t, len(img)+openAICacheSimImageBytes, openAICacheSimRawSize(gjson.Parse(img)))
}

func TestOpenAICacheSimHistoryRatio_EscapedDataTextDoesNotInflate(t *testing.T) {
	body := `{"input":[{"role":"user","content":"` + longText(16000) + `"},{"role":"assistant","content":"ok"},{"role":"user","content":"log says \"data:\" then ` + longText(64000) + `"}]}`
	r, kind := openAICacheSimHistoryRatio(nil, []byte(body))
	require.Equal(t, openAICacheSimHistoryContinuation, kind)
	require.InDelta(t, 0.2, r, 0.01)
}

func TestOpenAICacheSimHistoryRatio_NativeTopLevelStringsNotDecoded(t *testing.T) {
	instructions := strings.Repeat(`rule \"x\"\n`, 20000)
	body := []byte(`{"instructions":"` + instructions + `","model":"gpt","input":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`)
	openAICacheSimHistoryRatio(nil, body)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	openAICacheSimHistoryRatio(nil, body)
	runtime.ReadMemStats(&after)
	// instructions are read once (bounded by the system prompt), the legacy
	// probe never decodes the native body.
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(len(body)*2))
}

func TestOpenAICacheSimRawSize_InputImageOnlyAsTypeValue(t *testing.T) {
	// A tool parameter named input_image, or text reading input_image, is not an image.
	tool := `[{"type":"function","name":"f","parameters":{"type":"object","properties":{"input_image":{"type":"string"}}}}]`
	require.Equal(t, len(tool), openAICacheSimRawSize(gjson.Parse(tool)))
	text := `{"role":"user","content":"input_image"}`
	require.Equal(t, len(text), openAICacheSimRawSize(gjson.Parse(text)))
	img := `{"type" : "input_image","image_url":"https://example.com/a.png"}`
	require.Equal(t, len(img)-2+openAICacheSimImageBytes, openAICacheSimRawSize(gjson.Parse(img)))
}

func TestOpenAICacheSimHistoryRatio_StringInputNotDecoded(t *testing.T) {
	body := []byte(`{"model":"gpt","input":"` + strings.Repeat(`line \"x\"\n`, 50000) + `"}`)
	r, kind := openAICacheSimHistoryRatio(nil, body)
	require.Equal(t, openAICacheSimHistoryNew, kind)
	require.Zero(t, r)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	openAICacheSimHistoryRatio(nil, body)
	runtime.ReadMemStats(&after)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(len(body)/4), "a string input is never decoded")
}

func TestOpenAICacheSimHistoryRatio_NullInputWithLegacyMessages(t *testing.T) {
	// input:null with messages is normalized from messages, as the Responses entry does.
	body := []byte(`{"input":null,"messages":[{"role":"user","content":"` + longText(5000) + `"},{"role":"assistant","content":"done"},{"role":"user","content":"more"}]}`)
	r, kind := openAICacheSimHistoryRatio(nil, body)
	require.Equal(t, openAICacheSimHistoryContinuation, kind)
	require.Greater(t, r, 0.9)
}

func TestOpenAICacheSimUser_SaveKeepsUnparsableEntries(t *testing.T) {
	resetOpenAICacheSimUserCacheForTest()
	t.Cleanup(resetOpenAICacheSimUserCacheForTest)
	repo := &contentModerationTestSettingRepo{values: map[string]string{
		SettingKeyOpenAICacheSimUsers: `{"5":{"enabled":true,"lo":90,"hi":80},"6":{"enabled":true,"lo":82,"hi":88}}`,
	}}
	settings := &SettingService{settingRepo: repo}
	ctx := context.Background()
	on := userSimConfig(85, 90)
	require.NoError(t, settings.SetOpenAICacheSimUser(ctx, 7, &on))
	stored := repo.values[SettingKeyOpenAICacheSimUsers]
	require.Contains(t, stored, `"5":{"enabled":true,"lo":90,"hi":80}`, "a bad entry is kept for the admin to fix")
	require.True(t, gjson.Get(stored, "6").Exists())
	require.True(t, gjson.Get(stored, "7").Exists())
	require.NoError(t, settings.SetOpenAICacheSimUser(ctx, 6, nil))
	require.False(t, gjson.Get(repo.values[SettingKeyOpenAICacheSimUsers], "6").Exists())

	repo.values[SettingKeyOpenAICacheSimUsers] = `{"5":`
	require.Error(t, settings.SetOpenAICacheSimUser(ctx, 7, &on), "corrupt JSON is refused, not overwritten")
	require.Equal(t, `{"5":`, repo.values[SettingKeyOpenAICacheSimUsers])
}

func TestOpenAICacheSimHistoryRatio_UploadedFileCountsAsNewInput(t *testing.T) {
	// A PDF uploaded this turn as a data: URL weighs like an image, not zero.
	body := `{"input":[{"role":"user","content":"` + longText(4096) + `"},{"role":"assistant","content":"ok"},{"role":"user","content":[{"type":"input_file","filename":"a.pdf","file_data":"data:application/pdf;base64,` + longText(300000) + `"}]}]}`
	r, kind := openAICacheSimHistoryRatio(nil, []byte(body))
	require.Equal(t, openAICacheSimHistoryContinuation, kind)
	require.InDelta(t, 0.5, r, 0.05)
}

func TestOpenAICacheSimRawSize_EscapedTypeSpellingsCount(t *testing.T) {
	for _, part := range []string{
		`{"type":"input_file","file_data":"data:application/pdf;base64,AAAA"}`,
		`{"type":"input_image","image_url":"https://example.com/a.png"}`,
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}`,
	} {
		require.Greater(t, openAICacheSimRawSize(gjson.Parse(part)), openAICacheSimImageBytes-1, part)
	}
}

func TestOpenAICacheSimRawSize_FullyEscapedAndToolImages(t *testing.T) {
	esc := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			fmt.Fprintf(&b, `\u%04x`, r)
		}
		return b.String()
	}
	// Fully escaped type name still counts once.
	part := `{"type":"` + esc("input_image") + `","image_url":"https://example.com/a.png"}`
	require.Equal(t, len(part)-5*len("input_image")+openAICacheSimImageBytes, openAICacheSimRawSize(gjson.Parse(part)))
	// Escaped data: prefix still drops the payload; a data-URL image counts once.
	img := `{"type":"input_image","image_url":"data` + `:` + `image/png;base64,` + longText(100000) + `"}`
	require.Less(t, openAICacheSimRawSize(gjson.Parse(img)), openAICacheSimImageBytes+200)
	// A tool returning an image data URI directly weighs one attachment.
	out := `{"type":"function_call_output","call_id":"c","output":"data:image/png;base64,` + longText(100000) + `"}`
	got := openAICacheSimRawSize(gjson.Parse(out))
	require.Greater(t, got, openAICacheSimImageBytes)
	require.Less(t, got, openAICacheSimImageBytes+200)
}

func TestOpenAICacheSimHistoryRatio_Round4Shapes(t *testing.T) {
	// A repeated top-level input is never lifted.
	dup := `{"input":[{"role":"user","content":"` + longText(4096) + `"},{"role":"assistant","content":"ok"},{"role":"user","content":"x"}],"commands":[],"input":[{"role":"user","content":"x"}]}`
	r, kind := openAICacheSimHistoryRatio(nil, []byte(dup))
	require.Zero(t, r)
	require.Equal(t, openAICacheSimHistoryUnknownItem, kind)

	// Layout whitespace does not change the ratio.
	plain := `{"input":[{"role":"user","content":"` + longText(4096) + `"},{"role":"assistant","content":"ok"},{"role":"user","content":"` + longText(12288) + `"}]}`
	padded := `{"input":[{"role":"user",` + strings.Repeat(" \n", 98304) + `"content":"` + longText(4096) + `"},{"role":"assistant","content":"ok"},{"role":"user","content":"` + longText(12288) + `"}]}`
	rp, _ := openAICacheSimHistoryRatio(nil, []byte(plain))
	rq, _ := openAICacheSimHistoryRatio(nil, []byte(padded))
	require.InDelta(t, rp, rq, 0.001)

	// An upper-case DATA: scheme drops its payload like data:.
	img := `{"type":"input_image","image_url":"DATA:image/png;base64,` + longText(100000) + `"}`
	require.Less(t, openAICacheSimRawSize(gjson.Parse(img)), openAICacheSimImageBytes+200)
}

func TestOpenAICacheSimHistoryRatio_EscapedTextAndRepeatedRole(t *testing.T) {
	esc := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			fmt.Fprintf(&b, `\u%04x`, r)
		}
		return b.String()
	}
	hist := longText(4096)
	plain := `{"input":[{"role":"user","content":"` + hist + `"},{"role":"assistant","content":"ok"},{"role":"user","content":"` + longText(12288) + `"}]}`
	escaped := `{"input":[{"role":"user","content":"` + esc(hist) + `"},{"role":"assistant","content":"ok"},{"role":"user","content":"` + longText(12288) + `"}]}`
	rp, _ := openAICacheSimHistoryRatio(nil, []byte(plain))
	re, _ := openAICacheSimHistoryRatio(nil, []byte(escaped))
	require.InDelta(t, rp, re, 0.001, "escaped text weighs what it decodes to")

	// An item that repeats role is unknown: never lifted.
	dup := `{"input":[{"role":"user","content":"` + hist + `"},{"role":"assistant","role":"user","content":"x"},{"role":"user","content":"y"}]}`
	r, kind := openAICacheSimHistoryRatio(nil, []byte(dup))
	require.Zero(t, r)
	require.Equal(t, openAICacheSimHistoryUnknownItem, kind)
}
