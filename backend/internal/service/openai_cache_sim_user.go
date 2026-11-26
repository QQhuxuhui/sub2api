package service

import (
	"context"
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/cachesim"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// By-user cache simulation (Responses entry only). The real upper bound of a
// request comes from the request itself: a continued conversation carries its
// whole history, and everything before the last block of model output is the
// previous request's input — exactly what a real prompt cache could hit. A
// request without model output starts a new conversation and is not lifted.
// The user's window (decaying) and current-hour totals only steer how much to
// lift; consistency between the client and billing reuses the account
// simulation's State machinery unchanged.

// OpenAICacheSimUserPlanKind tells what a request's plan does.
type OpenAICacheSimUserPlanKind int

const (
	// OpenAICacheSimUserActive: a Responses request of a user under the
	// by-user simulation.
	OpenAICacheSimUserActive OpenAICacheSimUserPlanKind = iota + 1
	// OpenAICacheSimUserOff: the user opted out explicitly; nothing changes.
	OpenAICacheSimUserOff
)

// Request entries Prepare distinguishes.
const (
	OpenAICacheSimEntryResponses = "responses"
	OpenAICacheSimEntryCompat    = "compat"
)

// Kinds of a request's history.
const (
	openAICacheSimHistoryNew          = "new"
	openAICacheSimHistoryContinuation = "continuation"
	openAICacheSimHistoryUnknownItem  = "unknown_item"
	openAICacheSimHistoryParseFailed  = "parse_failed"
)

// OpenAICacheSimUserPlan is a request's by-user simulation plan.
type OpenAICacheSimUserPlan struct {
	UserID int64
	Config OpenAICacheSimConfig
	Source string
	Kind   OpenAICacheSimUserPlanKind
	// HistoryRatio is the share of the prompt that the previous request
	// already sent (0 = new conversation or not determinable).
	HistoryRatio float64
}

type openAICacheSimUserPlanKey struct{}

// WithOpenAICacheSimUserPlan attaches a plan to the context.
func WithOpenAICacheSimUserPlan(ctx context.Context, plan *OpenAICacheSimUserPlan) context.Context {
	if plan == nil {
		return ctx
	}
	return context.WithValue(ctx, openAICacheSimUserPlanKey{}, plan)
}

// OpenAICacheSimUserPlanFromContext returns the request's plan, or nil.
func OpenAICacheSimUserPlanFromContext(ctx context.Context) *OpenAICacheSimUserPlan {
	if ctx == nil {
		return nil
	}
	plan, _ := ctx.Value(openAICacheSimUserPlanKey{}).(*OpenAICacheSimUserPlan)
	return plan
}

// PrepareOpenAICacheSimUser attaches the by-user plan of an OpenAI request.
// Callers pass only requests whose resolved platform is OpenAI. Without an
// active or opted-out user nothing is attached (zero cost, unchanged
// behaviour); compatibility entries of active users keep the account
// simulation and get no plan either.
func (s *OpenAIGatewayService) PrepareOpenAICacheSimUser(c *gin.Context, apiKey *APIKey, body []byte, entry string) {
	if s == nil || c == nil || c.Request == nil || apiKey == nil || apiKey.UserID <= 0 {
		return
	}
	ctx := c.Request.Context()
	mode := resolveOpenAICacheSimUserMode(ctx, s.openAICacheSimSettingRepo(), apiKey.UserID)
	var plan *OpenAICacheSimUserPlan
	switch {
	case mode.Off():
		plan = &OpenAICacheSimUserPlan{UserID: apiKey.UserID, Config: mode.Config, Source: mode.Source, Kind: OpenAICacheSimUserOff}
	case mode.Active && entry == OpenAICacheSimEntryResponses:
		ratio, kind := openAICacheSimHistoryRatio(s.cfg, body)
		openAICacheSimHistoryStatsAdd(kind)
		plan = &OpenAICacheSimUserPlan{UserID: apiKey.UserID, Config: mode.Config, Source: mode.Source, Kind: OpenAICacheSimUserActive, HistoryRatio: ratio}
	default:
		return
	}
	c.Request = c.Request.WithContext(WithOpenAICacheSimUserPlan(ctx, plan))
}

func (s *OpenAIGatewayService) openAICacheSimSettingRepo() SettingRepository {
	if s == nil || s.settingService == nil {
		return nil
	}
	return s.settingService.settingRepo
}

// ---- history ratio ----

// openAICacheSimImageBytes stands in for an image part's raw size: base64
// data would dwarf the text around it while costing comparatively few tokens.
const openAICacheSimImageBytes = 4096

// Model output item types besides isCodexToolCallContextItemType's.
var openAICacheSimModelOutputTypes = map[string]struct{}{
	"reasoning": {}, "web_search_call": {}, "file_search_call": {}, "computer_call": {},
	"code_interpreter_call": {}, "image_generation_call": {}, "mcp_call": {}, "mcp_list_tools": {},
	"mcp_approval_request": {}, "compaction": {}, "compaction_summary": {},
}

// Client input item types besides role-carrying messages and *_output items.
var openAICacheSimClientInputTypes = map[string]struct{}{
	"mcp_approval_response": {}, "item_reference": {}, "compaction_trigger": {},
	"additional_tools": {}, "agent_message": {},
}

type openAICacheSimItemClass int

const (
	openAICacheSimItemClient openAICacheSimItemClass = iota
	openAICacheSimItemModel
	openAICacheSimItemUnknown
)

func classifyOpenAICacheSimItem(item gjson.Result) openAICacheSimItemClass {
	typ := strings.TrimSpace(item.Get("type").String())
	role := strings.TrimSpace(item.Get("role").String())
	if role == "assistant" && (typ == "" || typ == "message") {
		return openAICacheSimItemModel
	}
	if typ == "" || typ == "message" {
		switch role {
		case "user", "system", "developer":
			return openAICacheSimItemClient
		}
		return openAICacheSimItemUnknown
	}
	if isCodexToolCallContextItemType(typ) {
		return openAICacheSimItemModel
	}
	if _, ok := openAICacheSimModelOutputTypes[typ]; ok {
		return openAICacheSimItemModel
	}
	if strings.HasSuffix(typ, "_output") {
		return openAICacheSimItemClient
	}
	if _, ok := openAICacheSimClientInputTypes[typ]; ok {
		return openAICacheSimItemClient
	}
	return openAICacheSimItemUnknown
}

// openAICacheSimRawSize is a JSON value's raw size with every data: URL
// string counted as openAICacheSimImageBytes (base64 images would dwarf the
// text around them while costing comparatively few tokens). It scans the raw
// bytes only: no string value is decoded, so escaped text allocates nothing.
func openAICacheSimRawSize(v gjson.Result) int {
	if !v.Exists() {
		return 0
	}
	raw := v.Raw
	size := len(raw)
	const marker = `"data:`
	for off := 0; ; {
		k := strings.Index(raw[off:], marker)
		if k < 0 {
			break
		}
		start := off + k
		end := start + len(marker)
		for end < len(raw) && raw[end] != '"' {
			if raw[end] == '\\' {
				end++
			}
			end++
		}
		if end >= len(raw) {
			break
		}
		if n := end + 1 - start; n > openAICacheSimImageBytes {
			size -= n - openAICacheSimImageBytes
		}
		off = end + 1
	}
	return size
}

// openAICacheSimHistoryRatio is the share of the prompt (instructions, tools,
// input) that precedes the last block of model output in the input — the
// previous request's input. It reads the input as the Responses entry
// normalizes it (legacy messages-only bodies included).
func openAICacheSimHistoryRatio(cfg *config.Config, body []byte) (float64, string) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return 0, openAICacheSimHistoryParseFailed
	}
	normalized, changed, err := normalizeOpenAIResponsesLegacyIngressWithConfig(cfg, body)
	if err != nil {
		return 0, openAICacheSimHistoryParseFailed
	}
	if changed {
		body = normalized
	}
	// Read without copying the body (it may be megabytes and the request has
	// not taken its concurrency slot yet): a zero-copy string view and a
	// single ForEach pass, no Result slice for the input.
	view := unsafe.String(unsafe.SliceData(body), len(body))
	input := gjson.Get(view, "input")
	if !input.IsArray() {
		return 0, openAICacheSimHistoryNew
	}
	head := openAICacheSimRawSize(gjson.Get(view, "instructions")) + openAICacheSimRawSize(gjson.Get(view, "tools"))
	total, blockStart, lastBlockStart := 0, 0, -1
	prevModel, unknown := false, false
	input.ForEach(func(_, item gjson.Result) bool {
		switch classifyOpenAICacheSimItem(item) {
		case openAICacheSimItemUnknown:
			unknown = true
			return false
		case openAICacheSimItemModel:
			if !prevModel {
				blockStart = total
			}
			lastBlockStart = blockStart
			prevModel = true
		default:
			prevModel = false
		}
		total += openAICacheSimRawSize(item) + 1
		return true
	})
	if unknown {
		return 0, openAICacheSimHistoryUnknownItem
	}
	if lastBlockStart < 0 {
		return 0, openAICacheSimHistoryNew
	}
	prefix, all := head+lastBlockStart, head+total
	if all <= 0 || prefix <= 0 {
		return 0, openAICacheSimHistoryContinuation
	}
	ratio := float64(prefix) / float64(all)
	if ratio > 1 {
		ratio = 1
	}
	return ratio, openAICacheSimHistoryContinuation
}

// ---- decision ----

// openAICacheSimUserLimit is the most a request may show as cached: cap% of
// its input, never into the cache-creation share, in whole 128-token blocks.
func openAICacheSimUserLimit(cfg OpenAICacheSimConfig, total, creation int) int {
	capBP := openAICacheSimBasisPoints(cfg.Cap)
	byCap := int(int64(total) * capBP / 10000)
	limit := min(byCap, total-max(creation, 0))
	if limit <= 0 {
		return 0
	}
	return limit / openAICacheSimGranularity * openAICacheSimGranularity
}

// openAICacheSimUserCeiling is the request's real upper bound (0 = no lift).
func openAICacheSimUserCeiling(cfg OpenAICacheSimConfig, ratio float64, responsesShape bool, total, cached, creation int) int {
	if !responsesShape || ratio <= 0 || cached <= 0 || total < openAICacheSimMinPromptTokens {
		return 0
	}
	history := int(float64(total) * ratio)
	history = history / openAICacheSimGranularity * openAICacheSimGranularity
	return min(history, openAICacheSimUserLimit(cfg, total, creation))
}

// OpenAICacheSimUserWindow is the user's decaying window and current hour.
type OpenAICacheSimUserWindow struct {
	WT, WC float64 // decaying total input / shown cache reads
	HT, HC int64   // current-hour totals
}

// openAICacheSimUserDecide is the reference decision (the Lua script in the
// repository implements the same arithmetic atomically). Rates are basis
// points and every comparison multiplies before dividing, so exact boundaries
// such as 58% of 12800 = 7424 are not lost to floating point.
func openAICacheSimUserDecide(win OpenAICacheSimUserWindow, total, cached, ceiling int, loBP, hiBP, targetBP int64) int {
	if ceiling <= cached || total <= 0 {
		return cached
	}
	t := int64(total)
	if win.HT > 0 && win.HC*10000 < loBP*win.HT {
		targetBP = hiBP
	}
	wantW := (float64(targetBP)*(win.WT+float64(t)) - 10000*win.WC) / 10000
	wantH := float64(targetBP*(win.HT+t)-10000*win.HC) / 10000
	want := math.Max(wantW, wantH)
	room := floorDiv(hiBP*(win.HT+t)-10000*win.HC, 10000)
	g := int64(openAICacheSimGranularity)
	upper := openAICacheSimMinI64(room, int64(ceiling))
	sim := int64(cached)
	if w := int64(want); w > 0 {
		cand := openAICacheSimMinI64(w, upper)
		cand = floorDiv(cand, g) * g
		sim = openAICacheSimMaxI64(sim, cand)
	}
	// Still under lo for the hour: the smallest block that reaches it, if
	// it fits under both bounds.
	if (win.HC+sim)*10000 < loBP*(win.HT+t) {
		need := ceilDiv(loBP*(win.HT+t)-10000*win.HC, 10000)
		need = ceilDiv(need, g) * g
		if need <= upper {
			sim = openAICacheSimMaxI64(sim, need)
		}
	}
	if sim < int64(cached) {
		sim = int64(cached)
	}
	if sim > t {
		sim = t
	}
	return int(sim)
}

func openAICacheSimMinI64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func openAICacheSimMaxI64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func ceilDiv(a, b int64) int64 {
	return -floorDiv(-a, b)
}

// openAICacheSimUserTargetBP draws the request's target within [lo, hi].
func openAICacheSimUserTargetBP(cfg OpenAICacheSimConfig, rnd float64) (lo, hi, target int64) {
	lo, hi = openAICacheSimBasisPoints(cfg.Lo), openAICacheSimBasisPoints(cfg.Hi)
	if rnd < 0 || rnd >= 1 {
		rnd = 0.5
	}
	return lo, hi, lo + int64(rnd*float64(hi-lo)+0.5)
}

// simulateUser is the by-user decision of one request: the real upper bound
// from the request, the amount from the user's window (atomically in Redis).
func (r *openAICacheSimRequest) simulateUser(total, cached, creation int, responsesShape bool) int {
	plan := r.user
	ceiling := openAICacheSimUserCeiling(plan.Config, plan.HistoryRatio, responsesShape, total, cached, creation)
	r.reachable = max(cached, ceiling)
	store := r.userStore
	if store == nil {
		return cached
	}
	lo, hi, target := openAICacheSimUserTargetBP(plan.Config, rand.Float64())
	in := OpenAICacheSimUserDecideInput{
		UserID: plan.UserID, Hour: openAICacheSimUserHour(time.Now()), HalfLife: plan.Config.HalfLife(),
		Total: total, Cached: cached, Ceiling: max(ceiling, cached), LoBP: lo, HiBP: hi, TargetBP: target,
	}
	sim, ok := openAICacheSimUserCallWithin(r.ctx, openAICacheSimUserDecideWait, func(ctx context.Context) (int, error) {
		return store.DecideAndCount(ctx, in)
	})
	if !ok {
		return cached
	}
	r.userCounted = true
	return max(sim, cached)
}

const (
	openAICacheSimUserDecideWait = 300 * time.Millisecond
	openAICacheSimUserCountWait  = 500 * time.Millisecond
)

// openAICacheSimUserCallWithin runs a store call with its own deadline: the
// shared Redis client may ignore context deadlines on socket reads.
func openAICacheSimUserCallWithin(parent context.Context, wait time.Duration, call func(ctx context.Context) (int, error)) (int, bool) {
	if parent == nil || parent.Err() != nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), wait)
	type result struct {
		v   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		defer cancel()
		v, err := call(ctx)
		done <- result{v, err}
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case res := <-done:
		if res.err != nil {
			logger.L().Warn("openai_cache_sim.user_store_failed", zap.Error(res.err))
			return 0, false
		}
		return res.v, true
	case <-timer.C:
		logger.L().Warn("openai_cache_sim.user_store_timeout")
		return 0, false
	}
}

var openAICacheSimShanghai = func() *time.Location {
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return loc
	}
	return time.FixedZone("CST", 8*3600)
}()

// OpenAICacheSimShanghai is the time zone of the by-user hour (the dashboard
// acceptance query uses the same).
func OpenAICacheSimShanghai() *time.Location { return openAICacheSimShanghai }

func openAICacheSimUserHour(t time.Time) string {
	return t.In(openAICacheSimShanghai).Format("2006010215")
}

// ---- store ----

// OpenAICacheSimUserDecideInput is one atomic decide-and-count call.
type OpenAICacheSimUserDecideInput struct {
	UserID   int64
	Hour     string
	HalfLife time.Duration
	Total    int
	Cached   int
	Ceiling  int
	LoBP     int64
	HiBP     int64
	TargetBP int64
}

// OpenAICacheSimUserStore keeps the users' windows (Redis, shared).
type OpenAICacheSimUserStore interface {
	DecideAndCount(ctx context.Context, in OpenAICacheSimUserDecideInput) (int, error)
	Count(ctx context.Context, userID int64, hour string, halfLife time.Duration, total, cached int, hourOnly bool) error
	PeekUser(ctx context.Context, userID int64, halfLife time.Duration) (OpenAICacheSimWindow, error)
}

var openAICacheSimUserStoreHolder atomic.Pointer[openAICacheSimUserStoreBox]

type openAICacheSimUserStoreBox struct{ store OpenAICacheSimUserStore }

// SetOpenAICacheSimUserStore publishes the store (wiring, once).
func SetOpenAICacheSimUserStore(store OpenAICacheSimUserStore) {
	openAICacheSimUserStoreHolder.Store(&openAICacheSimUserStoreBox{store: store})
}

func currentOpenAICacheSimUserStore() OpenAICacheSimUserStore {
	if box := openAICacheSimUserStoreHolder.Load(); box != nil {
		return box.store
	}
	return nil
}

// countOpenAICacheSimUser adds a request to the user's totals without
// deciding (hourOnly: the current hour only).
func countOpenAICacheSimUser(ctx context.Context, userID int64, cfg OpenAICacheSimConfig, total, cached int, hourOnly bool) {
	store := currentOpenAICacheSimUserStore()
	if store == nil || userID <= 0 || total <= 0 {
		return
	}
	hour := openAICacheSimUserHour(time.Now())
	openAICacheSimUserCallWithin(ctx, openAICacheSimUserCountWait, func(ctx context.Context) (int, error) {
		return 0, store.Count(ctx, userID, hour, cfg.HalfLife(), total, cached, hourOnly)
	})
}

// ---- recording ----

// resolveOpenAICacheSimUserForRecord records a request carrying a by-user
// plan. It reuses the account simulation's state branches; the windows were
// counted when deciding, so only requests the decision did not count are
// counted here.
func (s *OpenAIGatewayService) resolveOpenAICacheSimUserForRecord(ctx context.Context, plan *OpenAICacheSimUserPlan, account *Account, result *OpenAIForwardResult, inboundEndpoint string) openAICacheSimRecord {
	total := result.Usage.InputTokens
	cached := result.Usage.CacheReadInputTokens
	rec := openAICacheSimRecord{cfg: account.OpenAICacheSimulation(), RawTotal: total, RawCached: cached, SimCached: cached}
	if plan.Kind != OpenAICacheSimUserActive {
		return rec // opted out: nothing changes, nothing counted
	}
	raw := cached
	if !openAICacheSimEligibleResultWith(result, inboundEndpoint, true) {
		countOpenAICacheSimUser(ctx, plan.UserID, plan.Config, total, cached, true)
		return rec
	}
	rec.Reachable = &raw
	state := cachesim.FromContext(ctx)
	if state == nil || state.Excluded() {
		rec.Reachable = nil
		countOpenAICacheSimUser(ctx, plan.UserID, plan.Config, total, cached, true)
		return rec
	}
	reachable, counted, haveResult := state.UserResult()
	switch {
	case state.IsApplied(total, cached):
		rawTotal, rawCached, simCached, _ := state.Snapshot()
		rec.RawTotal, rec.RawCached, rec.SimCached = rawTotal, rawCached, simCached
		rec.Active = true
	case !state.Decided():
		// The rewrite never saw a usage: the client got the raw value.
		countOpenAICacheSimUser(ctx, plan.UserID, plan.Config, total, cached, false)
		return rec
	default:
		simCached, ok := state.Apply(total, cached)
		if !ok {
			rawTotal, rawCached, _, _ := state.Snapshot()
			if rawTotal != total || rawCached != cached {
				logger.L().Warn("openai_cache_sim.usage_mismatch",
					zap.Int64("account_id", account.ID), zap.Int64("user_id", plan.UserID),
					zap.Int("rewrite_total", rawTotal), zap.Int("rewrite_cached", rawCached),
					zap.Int("billing_total", total), zap.Int("billing_cached", cached))
				if !counted {
					countOpenAICacheSimUser(ctx, plan.UserID, plan.Config, total, cached, false)
				}
				return rec
			}
		}
		rec.SimCached = simCached
		rec.Active = true
	}
	if haveResult {
		v := max(reachable, rec.RawCached)
		rec.Reachable = &v
	} else {
		v := rec.RawCached
		rec.Reachable = &v
	}
	if !counted {
		countOpenAICacheSimUser(ctx, plan.UserID, plan.Config, rec.RawTotal, rec.SimCached, false)
	}
	// The account's own window keeps its live ratio on the page.
	if store := s.openAICacheSimStore(); store != nil && rec.cfg.Enabled {
		addCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 500*time.Millisecond)
		defer cancel()
		if err := store.Add(addCtx, account.ID, rec.RawTotal, rec.SimCached, rec.cfg.HalfLife()); err != nil {
			logger.L().Warn("openai_cache_sim.window_add_failed", zap.Int64("account_id", account.ID), zap.Error(err))
		}
	}
	return rec
}

// ---- history stats ----

var openAICacheSimHistoryStats = struct {
	mu     sync.Mutex
	counts map[string]int64
	since  time.Time
}{counts: map[string]int64{}, since: time.Now()}

const openAICacheSimHistoryStatsEvery = 10 * time.Minute

func openAICacheSimHistoryStatsAdd(kind string) {
	st := &openAICacheSimHistoryStats
	st.mu.Lock()
	st.counts[kind]++
	if time.Since(st.since) < openAICacheSimHistoryStatsEvery {
		st.mu.Unlock()
		return
	}
	counts := st.counts
	st.counts = map[string]int64{}
	st.since = time.Now()
	st.mu.Unlock()
	logger.L().Info("openai_cache_sim.history_stats",
		zap.Int64("new", counts[openAICacheSimHistoryNew]),
		zap.Int64("continuation", counts[openAICacheSimHistoryContinuation]),
		zap.Int64("unknown_item", counts[openAICacheSimHistoryUnknownItem]),
		zap.Int64("parse_failed", counts[openAICacheSimHistoryParseFailed]))
}

// OpenAICacheSimUserDecideReference exposes the reference decision so the
// repository's Lua script can be checked against it.
func OpenAICacheSimUserDecideReference(win OpenAICacheSimUserWindow, total, cached, ceiling int, loBP, hiBP, targetBP int64) int {
	return openAICacheSimUserDecide(win, total, cached, ceiling, loBP, hiBP, targetBP)
}
