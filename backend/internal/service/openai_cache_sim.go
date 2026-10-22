package service

import (
	"context"
	"math"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/cachesim"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// OpenAICacheSimWindow 是账号滚动窗口（衰减计数器）的当前值：Total 为总输入
// （OpenAI 口径，含缓存读），Cached 为展示给客户的缓存读。
type OpenAICacheSimWindow struct {
	Total     float64
	Cached    float64
	UpdatedAt time.Time
}

// Ratio 返回窗口内的展示缓存率（0~1），窗口为空时返回 -1。
func (w OpenAICacheSimWindow) Ratio() float64 {
	if w.Total <= 0 {
		return -1
	}
	return w.Cached / w.Total
}

// OpenAICacheSimWindowStore 存放每个账号的滚动窗口，多实例共享（Redis 实现见 repository）。
type OpenAICacheSimWindowStore interface {
	Add(ctx context.Context, accountID int64, total, cached int, halfLife time.Duration) error
	Peek(ctx context.Context, accountID int64, halfLife time.Duration) (OpenAICacheSimWindow, error)
}

// openAICacheSimStore 是进程级窗口存储句柄：响应改写层由无状态的纯函数构造，
// 拿不到 service 实例，故与 Codex 身份开关一样发布为进程级快照。
var openAICacheSimStore atomic.Pointer[openAICacheSimStoreHolder]

type openAICacheSimStoreHolder struct {
	store OpenAICacheSimWindowStore
}

// SetOpenAICacheSimWindowStore 发布窗口存储（装配期调用一次）。
func SetOpenAICacheSimWindowStore(store OpenAICacheSimWindowStore) {
	openAICacheSimStore.Store(&openAICacheSimStoreHolder{store: store})
}

func currentOpenAICacheSimStore() OpenAICacheSimWindowStore {
	if holder := openAICacheSimStore.Load(); holder != nil {
		return holder.store
	}
	return nil
}

// simulateOpenAICachedTokens 是纯决策函数：给定账号配置、当前窗口、这一笔的原始
// (total, cached) 与一个 [0,1) 随机数，返回模拟后的 cached。
//
// 规则（grill 定稿）：
//   - 上游 cached 为 0 或 prompt 不足 1024 的请求不碰：首轮对话零缓存是真实世界的硬约束。
//   - 目标在 [lo, hi] 内随机取；需要的 cached = 目标×(窗口总量+本笔总量) − 窗口缓存量。
//   - 单笔上限：不超过 prompt 的 cap%；向下取到 128 的倍数；只往上拉不往下压。
func simulateOpenAICachedTokens(cfg OpenAICacheSimConfig, win OpenAICacheSimWindow, rawTotal, rawCached, rawCreation int, rnd float64) int {
	if !cfg.Enabled || rawCached <= 0 || rawTotal < openAICacheSimMinPromptTokens || rawCached >= rawTotal {
		return rawCached
	}
	if rnd < 0 || rnd >= 1 || math.IsNaN(rnd) {
		rnd = 0.5
	}
	target := (cfg.Lo + rnd*(cfg.Hi-cfg.Lo)) / 100
	want := target*(win.Total+float64(rawTotal)) - win.Cached
	// Cached reads and cache creation occupy disjoint input buckets.
	limit := min(float64(rawTotal)*cfg.Cap/100, float64(max(rawTotal-max(rawCreation, 0), 0)))
	if want > limit {
		want = limit
	}
	sim := int(math.Floor(want/openAICacheSimGranularity)) * openAICacheSimGranularity
	if sim <= rawCached {
		return rawCached
	}
	if sim > rawTotal {
		sim = rawTotal
	}
	return sim
}

// openAICacheSimRequest 是一次请求的模拟器：绑定账号配置、请求状态与窗口存储。
type openAICacheSimRequest struct {
	accountID int64
	cfg       OpenAICacheSimConfig
	state     *cachesim.State
	store     OpenAICacheSimWindowStore
	ctx       context.Context
}

// openAICacheSimForRequest 在账号开了模拟且请求 context 带状态时返回模拟器，否则 nil。
func openAICacheSimForRequest(c *gin.Context, account *Account) *openAICacheSimRequest {
	if c == nil || c.Request == nil || account == nil {
		return nil
	}
	cfg := account.OpenAICacheSimulation()
	if !cfg.Enabled {
		return nil
	}
	state := cachesim.FromContext(c.Request.Context())
	if state == nil {
		return nil
	}
	return &openAICacheSimRequest{
		accountID: account.ID,
		cfg:       cfg,
		state:     state,
		store:     currentOpenAICacheSimStore(),
		ctx:       c.Request.Context(),
	}
}

// decide 做出（或复用）这一笔的决策。窗口读取失败时保守地不加码。
func (r *openAICacheSimRequest) decide(rawTotal, rawCached, rawCreation int) int {
	if r == nil {
		return rawCached
	}
	return r.state.Decide(rawTotal, rawCached, func(total, cached int) int {
		if cached <= 0 || total < openAICacheSimMinPromptTokens {
			return cached
		}
		var win OpenAICacheSimWindow
		if r.store != nil {
			ctx := r.ctx
			if ctx == nil || ctx.Err() != nil {
				ctx = context.Background()
			}
			peekCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 300*time.Millisecond)
			defer cancel()
			w, err := r.store.Peek(peekCtx, r.accountID, r.cfg.HalfLife())
			if err != nil {
				logger.L().Warn("openai_cache_sim.window_peek_failed",
					zap.Int64("account_id", r.accountID), zap.Error(err))
				return cached
			}
			win = w
		}
		return simulateOpenAICachedTokens(r.cfg, win, total, cached, rawCreation, rand.Float64())
	})
}

// apply 把一个 usage 的 (total, cached) 映射为模拟值：未决策时先决策。
func (r *openAICacheSimRequest) apply(total, cached, creation int) (int, bool) {
	if r == nil || total <= 0 || r.state.Excluded() {
		return cached, false
	}
	if !r.state.Decided() {
		sim := r.decide(total, cached, creation)
		return sim, sim != cached
	}
	return r.state.Apply(total, cached)
}

// openAICacheSimUsagePrefixes 是载荷里 usage 对象可能出现的位置，与缓存写入抹除一致。
var openAICacheSimUsagePrefixes = []string{"usage", "response.usage", "message.usage"}

// applyJSON 改写 JSON 文档里所有 usage 对象的缓存读字段。
// anthropicShape 为 true 时 usage 是 Anthropic 口径（input_tokens 与 cache_read_input_tokens 互斥），
// 加码要从 input_tokens 扣到 cache_read_input_tokens；否则是 OpenAI 口径（cached 是 input 的子集），
// 只改 cached_tokens，总量不动。
func (r *openAICacheSimRequest) applyJSON(data []byte, anthropicShape bool) []byte {
	if r == nil {
		return data
	}
	observeOpenAICacheSimState(r.state, data)
	if r.state.Excluded() {
		return data
	}
	updated := data
	for _, prefix := range openAICacheSimUsagePrefixes {
		usage := gjson.GetBytes(updated, prefix)
		if !usage.IsObject() {
			continue
		}
		updated = r.applyUsageAt(updated, prefix, usage, anthropicShape)
	}
	return updated
}

func (r *openAICacheSimRequest) applyUsageAt(data []byte, prefix string, usage gjson.Result, anthropicShape bool) []byte {
	setInt := func(doc []byte, path string, v int) []byte {
		if next, err := sjson.SetBytes(doc, path, v); err == nil {
			return next
		}
		return doc
	}
	if anthropicShape {
		input := usage.Get("input_tokens")
		cacheRead := usage.Get("cache_read_input_tokens")
		if !input.Exists() || !cacheRead.Exists() {
			return data
		}
		creation := int(usage.Get("cache_creation_input_tokens").Int())
		total := int(input.Int()) + int(cacheRead.Int()) + creation
		sim, changed := r.apply(total, int(cacheRead.Int()), creation)
		if !changed {
			return data
		}
		delta := sim - int(cacheRead.Int())
		data = setInt(data, prefix+".cache_read_input_tokens", sim)
		return setInt(data, prefix+".input_tokens", max(int(input.Int())-delta, 0))
	}

	// OpenAI 口径：总量字段 + 缓存读字段（子集）。
	totalField := ""
	for _, f := range []string{"input_tokens", "prompt_tokens"} {
		if usage.Get(f).Exists() {
			totalField = f
			break
		}
	}
	if totalField == "" {
		return data
	}
	total := int(usage.Get(totalField).Int())
	cachedField := ""
	for _, f := range []string{
		"input_tokens_details.cached_tokens",
		"prompt_tokens_details.cached_tokens",
		"cache_read_input_tokens",
		"cache_read_tokens",
		"cached_tokens",
	} {
		if usage.Get(f).Exists() {
			cachedField = f
			break
		}
	}
	if cachedField == "" {
		return data
	}
	cached := int(usage.Get(cachedField).Int())
	sim, changed := r.apply(total, cached, openAICacheCreationTokensFromUsage(usage))
	if !changed {
		return data
	}
	return setInt(data, prefix+"."+cachedField, sim)
}

// applyResponsesUsage 改写 typed Responses usage（复制后修改，不动计费快照）。
func (r *openAICacheSimRequest) applyResponsesUsage(usage *apicompat.ResponsesUsage) *apicompat.ResponsesUsage {
	if r == nil || usage == nil || usage.InputTokensDetails == nil {
		return usage
	}
	sim, changed := r.apply(usage.InputTokens, usage.InputTokensDetails.CachedTokens, usage.CacheCreationInputTokens)
	if !changed {
		return usage
	}
	result := *usage
	details := *usage.InputTokensDetails
	details.CachedTokens = sim
	result.InputTokensDetails = &details
	return &result
}

// applyChatUsage 改写 typed Chat Completions usage。
func (r *openAICacheSimRequest) applyChatUsage(usage *apicompat.ChatUsage) *apicompat.ChatUsage {
	if r == nil || usage == nil || usage.PromptTokensDetails == nil {
		return usage
	}
	sim, changed := r.apply(usage.PromptTokens, usage.PromptTokensDetails.CachedTokens, max(usage.PromptTokensDetails.CacheCreationTokens, usage.PromptTokensDetails.CacheWriteTokens))
	if !changed {
		return usage
	}
	result := *usage
	details := *usage.PromptTokensDetails
	details.CachedTokens = sim
	result.PromptTokensDetails = &details
	return &result
}

// openAICacheSimRecord 是落库时解析出的模拟结果。
type openAICacheSimRecord struct {
	// Active 为 true 表示这一笔参与了模拟（账号开了开关且是文本请求）。
	Active    bool
	RawTotal  int
	RawCached int
	SimCached int
	cfg       OpenAICacheSimConfig
}

// Changed 报告模拟是否真的改了数字。
func (rec openAICacheSimRecord) Changed() bool {
	return rec.Active && rec.SimCached != rec.RawCached
}

// openAICacheSimEligibleResult 判断这笔用量是否属于「文本对话」：生图、视频、音频、
// 搜索、embeddings 一律不参与模拟，也不计入窗口。
func openAICacheSimEligibleResult(result *OpenAIForwardResult, inboundEndpoint string) bool {
	if result == nil {
		return false
	}
	if result.ImageCount > 0 || result.VideoCount > 0 || result.AudioUsage != nil || result.SearchCount > 0 || result.WebSearchCalls > 0 {
		return false
	}
	// WS 一条连接多轮对话，共用一个 request context，与「每请求一次决策」的模型不匹配；
	// 所有 WS 改写路径显式关闭模拟，这里同样排除，两边一致。
	if result.OpenAIWSMode {
		return false
	}
	if result.Usage.ImageOutputTokens > 0 || result.Usage.ImageInputTokens > 0 {
		return false
	}
	if isOpenAIEmbeddingsEndpoint(inboundEndpoint) {
		return false
	}
	return true
}

func isOpenAIEmbeddingsEndpoint(endpoint string) bool {
	return len(endpoint) >= len("embeddings") && containsFold(endpoint, "embeddings")
}

func containsFold(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			a, b := s[i+j], sub[j]
			if 'A' <= a && a <= 'Z' {
				a += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// resolveOpenAICacheSimForRecord 在 RecordUsage 里解析模拟结果：优先沿用改写层已做的
// 决策；改写层没见过 usage（极少数路径）时在这里决策，并把窗口推进一步。
func (s *OpenAIGatewayService) resolveOpenAICacheSimForRecord(ctx context.Context, account *Account, result *OpenAIForwardResult, inboundEndpoint string) openAICacheSimRecord {
	rec := openAICacheSimRecord{}
	if account == nil || result == nil {
		return rec
	}
	cfg := account.OpenAICacheSimulation()
	rec.cfg = cfg
	total := result.Usage.InputTokens
	cached := result.Usage.CacheReadInputTokens
	rec.RawTotal, rec.RawCached, rec.SimCached = total, cached, cached
	if !cfg.Enabled || !openAICacheSimEligibleResult(result, inboundEndpoint) {
		return rec
	}
	state := cachesim.FromContext(ctx)
	if state == nil {
		logger.L().Debug("openai_cache_sim.state_missing", zap.Int64("account_id", account.ID))
		return rec
	}
	if state.Excluded() {
		return rec
	}
	store := s.openAICacheSimStore()
	rec.Active = true
	switch {
	case state.IsApplied(total, cached):
		// 若调用方在解析前已改写，沿用共享状态里的原始值。
		rawTotal, rawCached, simCached, _ := state.Snapshot()
		rec.RawTotal, rec.RawCached, rec.SimCached = rawTotal, rawCached, simCached
	case !state.Decided():
		// 改写层没看到过 usage（客户端拿到的是原样），落库也不改，只把原始值计入窗口。
		logger.L().Debug("openai_cache_sim.undecided_at_record", zap.Int64("account_id", account.ID))
		rec.Active = false
	default:
		simCached, ok := state.Apply(total, cached)
		if !ok {
			rawTotal, rawCached, _, _ := state.Snapshot()
			if rawTotal != total || rawCached != cached {
				// 改写层看到的 usage 与最终计费 usage 不一致：客户端拿到的是原样，这里也不改。
				logger.L().Warn("openai_cache_sim.usage_mismatch",
					zap.Int64("account_id", account.ID),
					zap.Int("rewrite_total", rawTotal), zap.Int("rewrite_cached", rawCached),
					zap.Int("billing_total", total), zap.Int("billing_cached", cached))
				rec.Active = false
				return rec
			}
		}
		rec.SimCached = simCached
	}
	if store != nil {
		addCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 500*time.Millisecond)
		defer cancel()
		if err := store.Add(addCtx, account.ID, rec.RawTotal, rec.SimCached, cfg.HalfLife()); err != nil {
			logger.L().Warn("openai_cache_sim.window_add_failed", zap.Int64("account_id", account.ID), zap.Error(err))
		}
	}
	return rec
}

func (s *OpenAIGatewayService) openAICacheSimStore() OpenAICacheSimWindowStore {
	if s != nil && s.cacheSimStore != nil {
		return s.cacheSimStore
	}
	return currentOpenAICacheSimStore()
}

// SetOpenAICacheSimWindowStore 注入窗口存储（装配期）。同时发布进程级快照供改写层使用。
func (s *OpenAIGatewayService) SetOpenAICacheSimWindowStore(store OpenAICacheSimWindowStore) {
	if s == nil {
		return
	}
	s.cacheSimStore = store
	SetOpenAICacheSimWindowStore(store)
}
