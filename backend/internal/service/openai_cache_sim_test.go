package service

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/cachesim"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// memCacheSimStore 是测试用的内存窗口（不衰减）。
type memCacheSimStore struct {
	mu   sync.Mutex
	wins map[int64]OpenAICacheSimWindow
	adds int
}

func (m *memCacheSimStore) Add(_ context.Context, accountID int64, total, cached int, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.wins == nil {
		m.wins = map[int64]OpenAICacheSimWindow{}
	}
	w := m.wins[accountID]
	w.Total += float64(total)
	w.Cached += float64(cached)
	w.UpdatedAt = time.Now()
	m.wins[accountID] = w
	m.adds++
	return nil
}

func (m *memCacheSimStore) Peek(_ context.Context, accountID int64, _ time.Duration) (OpenAICacheSimWindow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.wins[accountID], nil
}

func simTestAccount(extra map[string]any) *Account {
	return &Account{ID: 77, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: extra}
}

func simEnabledExtra(lo, hi float64) map[string]any {
	return map[string]any{openAICacheSimulationKey: map[string]any{"enabled": true, "lo": lo, "hi": hi}}
}

func TestParseOpenAICacheSimConfig(t *testing.T) {
	cfg := (&Account{Platform: PlatformOpenAI}).OpenAICacheSimulation()
	require.False(t, cfg.Enabled)
	require.Equal(t, 82.0, cfg.Lo)
	require.Equal(t, 88.0, cfg.Hi)
	require.Equal(t, 60, cfg.HalfLifeMinutes)
	require.Equal(t, 95.0, cfg.Cap)

	cfg = simTestAccount(map[string]any{openAICacheSimulationKey: map[string]any{"enabled": true, "lo": float64(80), "hi": 90, "half_life_minutes": float64(30), "cap": 90}}).OpenAICacheSimulation()
	require.True(t, cfg.Enabled)
	require.Equal(t, 80.0, cfg.Lo)
	require.Equal(t, 90.0, cfg.Hi)
	require.Equal(t, 30, cfg.HalfLifeMinutes)
	require.Equal(t, 90.0, cfg.Cap)
	require.Equal(t, 30*time.Minute, cfg.HalfLife())

	require.False(t, (&Account{Platform: PlatformAnthropic, Extra: simEnabledExtra(80, 90)}).OpenAICacheSimulation().Enabled, "非 OpenAI 平台恒关")
	require.False(t, simTestAccount(map[string]any{openAICacheSimulationKey: map[string]any{"enabled": true, "lo": 90, "hi": 80}}).OpenAICacheSimulation().Enabled, "非法配置回退默认（关）")

	require.NoError(t, ValidateOpenAICacheSimulationExtra(PlatformOpenAI, map[string]any{}))
	require.NoError(t, ValidateOpenAICacheSimulationExtra(PlatformAnthropic, map[string]any{openAICacheSimulationKey: "junk"}))
	require.Error(t, ValidateOpenAICacheSimulationExtra(PlatformOpenAI, map[string]any{openAICacheSimulationKey: "junk"}))
	require.Error(t, ValidateOpenAICacheSimulationExtra(PlatformOpenAI, map[string]any{openAICacheSimulationKey: map[string]any{"enabled": "yes"}}))
	require.Error(t, ValidateOpenAICacheSimulationExtra(PlatformOpenAI, map[string]any{openAICacheSimulationKey: map[string]any{"lo": 90, "hi": 80}}))
	require.Error(t, ValidateOpenAICacheSimulationExtra(PlatformOpenAI, map[string]any{openAICacheSimulationKey: map[string]any{"half_life_minutes": 0}}))
	require.Error(t, ValidateOpenAICacheSimulationExtra(PlatformOpenAI, map[string]any{openAICacheSimulationKey: map[string]any{"cap": 20}}))
	require.NoError(t, ValidateOpenAICacheSimulationExtra(PlatformOpenAI, map[string]any{openAICacheSimulationKey: map[string]any{"enabled": true, "lo": 82, "hi": 88, "half_life_minutes": 60, "cap": 95}}))
}

func TestSimulateOpenAICachedTokens(t *testing.T) {
	cfg := OpenAICacheSimConfig{Enabled: true, Lo: 82, Hi: 88, HalfLifeMinutes: 60, Cap: 95}
	// 零缓存 / 小请求 / 关闭 不碰。
	require.Equal(t, 0, simulateOpenAICachedTokens(cfg, OpenAICacheSimWindow{}, 10000, 0, 0, 0.5))
	require.Equal(t, 100, simulateOpenAICachedTokens(cfg, OpenAICacheSimWindow{}, 1000, 100, 0, 0.5))
	require.Equal(t, 100, simulateOpenAICachedTokens(OpenAICacheSimConfig{}, OpenAICacheSimWindow{}, 10000, 100, 0, 0.5))
	// 冷启动：目标 85% → 8500 → 取整到 128 倍数 8448。
	require.Equal(t, 8448, simulateOpenAICachedTokens(cfg, OpenAICacheSimWindow{}, 10000, 2000, 0, 0.5))
	// 窗口远低于目标时顶到上限 95% = 9500 → 9472。
	require.Equal(t, 9472, simulateOpenAICachedTokens(cfg, OpenAICacheSimWindow{Total: 1_000_000, Cached: 100_000}, 10000, 2000, 0, 0.5))
	// 窗口已高于目标：不往下压。
	require.Equal(t, 9000, simulateOpenAICachedTokens(cfg, OpenAICacheSimWindow{Total: 1_000_000, Cached: 950_000}, 10000, 9000, 0, 0.5))
	// 原始已超过目标：原样。
	require.Equal(t, 9000, simulateOpenAICachedTokens(cfg, OpenAICacheSimWindow{}, 10000, 9000, 0, 0.0))
	// 区间两端。
	require.Equal(t, 8192, simulateOpenAICachedTokens(cfg, OpenAICacheSimWindow{}, 10000, 2000, 0, 0.0))
	require.Equal(t, 8704, simulateOpenAICachedTokens(cfg, OpenAICacheSimWindow{}, 10000, 2000, 0, 0.999))
}

func TestOpenAICacheSim_ApplyJSONShapesAndIdempotency(t *testing.T) {
	store := &memCacheSimStore{}
	st := cachesim.New()
	sim := &openAICacheSimRequest{accountID: 1, cfg: OpenAICacheSimConfig{Enabled: true, Lo: 85, Hi: 85, HalfLifeMinutes: 60, Cap: 95}, state: st, store: store, ctx: context.Background()}

	// Responses 口径：只改 cached_tokens，总量不动。
	resp := []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":10000,"input_tokens_details":{"cached_tokens":2000},"output_tokens":5}}}`)
	out := sim.applyJSON(resp, false)
	require.Equal(t, int64(8448), gjson.GetBytes(out, "response.usage.input_tokens_details.cached_tokens").Int())
	require.Equal(t, int64(10000), gjson.GetBytes(out, "response.usage.input_tokens").Int())
	require.Equal(t, int64(5), gjson.GetBytes(out, "response.usage.output_tokens").Int())
	// 再过一遍不叠加。
	again := sim.applyJSON(out, false)
	require.Equal(t, string(out), string(again))
	// 同一请求里 chat 口径的同一 usage（转换路径）也映射到同一个值。
	chat := []byte(`{"usage":{"prompt_tokens":10000,"prompt_tokens_details":{"cached_tokens":2000}}}`)
	require.Equal(t, int64(8448), gjson.GetBytes(sim.applyJSON(chat, false), "usage.prompt_tokens_details.cached_tokens").Int())
	// Anthropic 口径：互斥，从 input 扣到 cache_read。
	anth := []byte(`{"type":"message_delta","usage":{"input_tokens":8000,"cache_read_input_tokens":2000,"cache_creation_input_tokens":0,"output_tokens":5}}`)
	aout := sim.applyJSON(anth, true)
	require.Equal(t, int64(8448), gjson.GetBytes(aout, "usage.cache_read_input_tokens").Int())
	require.Equal(t, int64(1552), gjson.GetBytes(aout, "usage.input_tokens").Int())
	require.Equal(t, string(aout), string(sim.applyJSON(aout, true)), "Anthropic 口径幂等")
	// 不同的 usage（别的 total）不碰。
	other := []byte(`{"usage":{"input_tokens":5000,"input_tokens_details":{"cached_tokens":1000}}}`)
	require.Equal(t, string(other), string(sim.applyJSON(other, false)))

	// typed 路径。
	typed := &apicompat.ResponsesUsage{InputTokens: 10000, InputTokensDetails: &apicompat.ResponsesInputTokensDetails{CachedTokens: 2000}}
	got := sim.applyResponsesUsage(typed)
	require.Equal(t, 2000, typed.InputTokensDetails.CachedTokens, "原对象不动")
	require.Equal(t, 8448, got.InputTokensDetails.CachedTokens)
	chatTyped := &apicompat.ChatUsage{PromptTokens: 10000, PromptTokensDetails: &apicompat.ChatTokenDetails{CachedTokens: 2000}}
	require.Equal(t, 8448, sim.applyChatUsage(chatTyped).PromptTokensDetails.CachedTokens)
}

func TestOpenAICacheSim_ZeroCacheRequestUntouched(t *testing.T) {
	st := cachesim.New()
	sim := &openAICacheSimRequest{accountID: 1, cfg: OpenAICacheSimConfig{Enabled: true, Lo: 85, Hi: 85, HalfLifeMinutes: 60, Cap: 95}, state: st, store: &memCacheSimStore{}, ctx: context.Background()}
	in := []byte(`{"usage":{"input_tokens":10000,"input_tokens_details":{"cached_tokens":0}}}`)
	require.Equal(t, string(in), string(sim.applyJSON(in, false)))
	_, _, simCached, decided := st.Snapshot()
	require.True(t, decided)
	require.Equal(t, 0, simCached)
}

func TestOpenAIResponseRewrite_CacheSimRidesOnRewriteLayer(t *testing.T) {
	SetOpenAICacheSimWindowStore(&memCacheSimStore{})
	t.Cleanup(func() { SetOpenAICacheSimWindowStore(nil) })

	c, _ := newRewriteTestContext(t, "gpt-6.1-sol")
	c.Request = c.Request.WithContext(cachesim.WithState(c.Request.Context(), cachesim.New()))
	off := simTestAccount(nil)
	require.Nil(t, newOpenAIResponseModelRewrite(c, off, "gpt-6.1-sol", "gpt-6.1-sol").cacheSim, "默认关")

	on := simTestAccount(simEnabledExtra(85, 85))
	rw := newOpenAIResponseModelRewrite(c, on, "gpt-6.1-sol", "gpt-6.1-sol")
	require.NotNil(t, rw.cacheSim)
	require.Nil(t, rw.withoutCacheSim().cacheSim)
	require.NotNil(t, rw.forcedOnly().cacheSim)
	line := `data: {"type":"response.completed","response":{"model":"gpt-6.1-sol","usage":{"input_tokens":10000,"input_tokens_details":{"cached_tokens":2000},"output_tokens":1}}}`
	got := rw.applySSELine(line)
	require.Contains(t, got, `"cached_tokens":8448`)
	require.Contains(t, got, `"input_tokens":10000`)
	// 没有 usage 的事件走字符串预检，不解析。
	delta := `data: {"type":"response.output_text.delta","delta":"hi"}`
	require.Equal(t, delta, rw.applySSELine(delta))

	// 没有 request state（中间件未挂）时不模拟。
	bare, _ := newRewriteTestContext(t, "gpt-6.1-sol")
	require.Nil(t, newOpenAIResponseModelRewrite(bare, on, "gpt-6.1-sol", "gpt-6.1-sol").cacheSim)
}

func TestHandleStreamingResponse_CacheSimConsistentWithUsage(t *testing.T) {
	store := &memCacheSimStore{}
	SetOpenAICacheSimWindowStore(store)
	t.Cleanup(func() { SetOpenAICacheSimWindowStore(nil) })

	c, rec := newRewriteTestContext(t, "gpt-6.1-sol")
	state := cachesim.New()
	c.Request = c.Request.WithContext(cachesim.WithState(c.Request.Context(), state))
	account := simTestAccount(simEnabledExtra(85, 85))
	event := `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-6.1-sol","status":"completed","output":[],"usage":{"input_tokens":10000,"input_tokens_details":{"cached_tokens":2000},"output_tokens":7,"total_tokens":10007}}}`
	body := "event: response.completed\n" + event + "\n\ndata: [DONE]\n\n"
	resp := newRewriteTestUpstreamResponse("text/event-stream", body)
	result, err := newRewriteTestService().handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "gpt-6.1-sol", "gpt-6.1-sol")
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), `"cached_tokens":8448`)
	require.NotContains(t, rec.Body.String(), `"cached_tokens":2000`)

	// 计费快照仍是上游原始值；RecordUsage 按同一份决策改成模拟值并留底。
	require.Equal(t, 2000, result.usage.CacheReadInputTokens)
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	ctx := cachesim.WithState(context.Background(), state)
	err = svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
		Result:  &OpenAIForwardResult{RequestID: "resp_1", Model: "gpt-5.4", UpstreamModel: "gpt-5.4", Usage: *result.usage, Duration: time.Second},
		APIKey:  &APIKey{ID: 10},
		User:    &User{ID: 20},
		Account: account,
	})
	require.NoError(t, err)
	log := usageRepo.lastLog
	require.NotNil(t, log)
	require.Equal(t, 8448, log.CacheReadTokens)
	require.Equal(t, 10000-8448, log.InputTokens)
	require.NotNil(t, log.RawCacheReadTokens)
	require.Equal(t, 2000, *log.RawCacheReadTokens)
	require.Equal(t, 8000, *log.RawInputTokens)
	require.Greater(t, log.CacheSimCostDelta, 0.0)
	expectedRaw, err := svc.billingService.CalculateCost("gpt-5.4", UsageTokens{InputTokens: 8000, CacheReadTokens: 2000, OutputTokens: 7}, 1.1)
	require.NoError(t, err)
	require.InDelta(t, expectedRaw.ActualCost-log.ActualCost, log.CacheSimCostDelta, 1e-9)
	require.Equal(t, 1, store.adds, "窗口只推进一次")
	win, _ := store.Peek(ctx, account.ID, time.Hour)
	require.Equal(t, 10000.0, win.Total)
	require.Equal(t, 8448.0, win.Cached)
}

func TestRecordUsage_CacheSimDisabledLeavesRawEqual(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	ctx := cachesim.WithState(context.Background(), cachesim.New())
	err := svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
		Result:  &OpenAIForwardResult{RequestID: "resp_2", Model: "gpt-5.4", UpstreamModel: "gpt-5.4", Usage: OpenAIUsage{InputTokens: 10000, CacheReadInputTokens: 2000, OutputTokens: 7}, Duration: time.Second},
		APIKey:  &APIKey{ID: 10},
		User:    &User{ID: 20},
		Account: simTestAccount(nil),
	})
	require.NoError(t, err)
	log := usageRepo.lastLog
	require.Equal(t, 2000, log.CacheReadTokens)
	require.Equal(t, 2000, *log.RawCacheReadTokens)
	require.Equal(t, 8000, *log.RawInputTokens)
	require.Zero(t, log.CacheSimCostDelta)
}

func TestRecordUsage_CacheSimSkipsImagesAndEmbeddings(t *testing.T) {
	SetOpenAICacheSimWindowStore(&memCacheSimStore{})
	t.Cleanup(func() { SetOpenAICacheSimWindowStore(nil) })
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	account := simTestAccount(simEnabledExtra(85, 85))
	for _, tc := range []struct {
		name   string
		result *OpenAIForwardResult
		ep     string
	}{
		{"image", &OpenAIForwardResult{RequestID: "img", Model: "gpt-5.4", UpstreamModel: "gpt-5.4", ImageCount: 1, Usage: OpenAIUsage{InputTokens: 10000, CacheReadInputTokens: 2000}, Duration: time.Second}, ""},
		{"embeddings", &OpenAIForwardResult{RequestID: "emb", Model: "gpt-5.4", UpstreamModel: "gpt-5.4", Usage: OpenAIUsage{InputTokens: 10000, CacheReadInputTokens: 2000}, Duration: time.Second}, "/v1/embeddings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := cachesim.WithState(context.Background(), cachesim.New())
			err := svc.RecordUsage(ctx, &OpenAIRecordUsageInput{Result: tc.result, APIKey: &APIKey{ID: 10}, User: &User{ID: 20}, Account: account, InboundEndpoint: tc.ep})
			require.NoError(t, err)
			require.Equal(t, 2000, usageRepo.lastLog.CacheReadTokens)
			require.Zero(t, usageRepo.lastLog.CacheSimCostDelta)
		})
	}
	require.True(t, strings.Contains("/v1/embeddings", "embeddings"))
}

func TestRecordUsage_CacheSimUndecidedStateLeavesUsageRaw(t *testing.T) {
	store := &memCacheSimStore{}
	SetOpenAICacheSimWindowStore(store)
	t.Cleanup(func() { SetOpenAICacheSimWindowStore(nil) })
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	account := simTestAccount(simEnabledExtra(85, 85))
	ctx := cachesim.WithState(context.Background(), cachesim.New())
	err := svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
		Result:  &OpenAIForwardResult{RequestID: "resp_u", Model: "gpt-5.4", UpstreamModel: "gpt-5.4", Usage: OpenAIUsage{InputTokens: 10000, CacheReadInputTokens: 2000, OutputTokens: 7}, Duration: time.Second},
		APIKey:  &APIKey{ID: 10},
		User:    &User{ID: 20},
		Account: account,
	})
	require.NoError(t, err)
	require.Equal(t, 2000, usageRepo.lastLog.CacheReadTokens, "改写层没决策过（客户端看到原样）时落库不改")
	require.Zero(t, usageRepo.lastLog.CacheSimCostDelta)
	require.Equal(t, 1, store.adds, "原始值仍计入窗口")

	// WS 模式整体排除。
	wsCtx := cachesim.WithState(context.Background(), cachesim.New())
	err = svc.RecordUsage(wsCtx, &OpenAIRecordUsageInput{
		Result:  &OpenAIForwardResult{RequestID: "resp_ws", Model: "gpt-5.4", UpstreamModel: "gpt-5.4", OpenAIWSMode: true, Usage: OpenAIUsage{InputTokens: 10000, CacheReadInputTokens: 2000, OutputTokens: 7}, Duration: time.Second},
		APIKey:  &APIKey{ID: 10},
		User:    &User{ID: 20},
		Account: account,
	})
	require.NoError(t, err)
	require.Equal(t, 2000, usageRepo.lastLog.CacheReadTokens)
	require.Equal(t, 1, store.adds, "WS 不计入窗口")
}

func TestObserveOpenAICacheSim_ZeroToolUsagePlaceholderDoesNotExclude(t *testing.T) {
	// 线上两家 Codex 中转（与 OpenAI 本身）每个响应都带全零的 tool_usage.image_gen 占位对象。
	placeholder := []byte(`{"id":"resp_1","output":[{"type":"message"}],"tool_usage":{"image_gen":{"input_tokens":0,"input_tokens_details":{"image_tokens":0,"text_tokens":0},"output_tokens":0,"output_tokens_details":{"image_tokens":0,"text_tokens":0},"total_tokens":0},"web_search":{"num_requests":0}},"usage":{"input_tokens":26841,"input_tokens_details":{"cached_tokens":26838},"output_tokens":5}}`)
	st := cachesim.New()
	observeOpenAICacheSimState(st, placeholder)
	require.False(t, st.Excluded(), "全零占位对象不是生图请求")

	withSearch := []byte(`{"output":[{"type":"web_search_call","status":"completed"},{"type":"message"}],"usage":{"input_tokens":5000,"input_tokens_details":{"cached_tokens":2000}}}`)
	st2 := cachesim.New()
	observeOpenAICacheSimState(st2, withSearch)
	require.False(t, st2.Excluded(), "联网搜索仍是文本对话")

	realImage := []byte(`{"output":[{"type":"message"}],"tool_usage":{"image_gen":{"input_tokens":0,"output_tokens":1120,"output_tokens_details":{"image_tokens":1120,"text_tokens":0},"total_tokens":1120}},"usage":{"input_tokens":5000,"input_tokens_details":{"cached_tokens":2000}}}`)
	st3 := cachesim.New()
	observeOpenAICacheSimState(st3, realImage)
	require.True(t, st3.Excluded(), "真的产生了图片 token 才排除")

	imageCall := []byte(`{"output":[{"type":"image_generation_call","status":"completed"}],"usage":{"input_tokens":5000}}`)
	st4 := cachesim.New()
	observeOpenAICacheSimState(st4, imageCall)
	require.True(t, st4.Excluded())

	// 走完整改写层：占位对象不阻止加码。
	sim := &openAICacheSimRequest{accountID: 1, cfg: OpenAICacheSimConfig{Enabled: true, Lo: 85, Hi: 85, HalfLifeMinutes: 60, Cap: 95}, state: cachesim.New(), store: &memCacheSimStore{}, ctx: context.Background()}
	out := sim.applyJSON([]byte(`{"type":"response.completed","response":`+string(placeholder)+`}`), false)
	require.Equal(t, int64(26838), gjson.GetBytes(out, "response.usage.input_tokens_details.cached_tokens").Int(), "原始已超目标：不动")
	sim2 := &openAICacheSimRequest{accountID: 1, cfg: OpenAICacheSimConfig{Enabled: true, Lo: 85, Hi: 85, HalfLifeMinutes: 60, Cap: 95}, state: cachesim.New(), store: &memCacheSimStore{}, ctx: context.Background()}
	low := []byte(`{"type":"response.completed","response":{"tool_usage":{"image_gen":{"total_tokens":0,"input_tokens_details":{"image_tokens":0}}},"usage":{"input_tokens":10000,"input_tokens_details":{"cached_tokens":2000}}}}`)
	out2 := sim2.applyJSON(low, false)
	require.Equal(t, int64(8448), gjson.GetBytes(out2, "response.usage.input_tokens_details.cached_tokens").Int())
}
