package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Intent routing cache smoothing.
//
// Upstream prompt caches belong to one account: a turn intent routing moves to
// another account starts cold there, and the turn after it comes back to an
// account that may have seen nothing of the routed turn. With the router's
// cache_smooth_enabled on, such a turn reports to the client (and is billed
// at) the cache hit the conversation would have had without the switch: about
// the previous turn's whole input, which a single account would have cached.
//
// Every text turn of the conversation leaves a baseline in Redis the moment
// the cache simulation decides on its usage, before the client has the whole
// response: the next turn may follow at once, long before the asynchronous
// usage record runs.
const (
	intentCacheSmoothTTL       = 10 * time.Minute
	intentCacheSmoothReadWait  = 100 * time.Millisecond
	intentCacheSmoothWriteWait = 50 * time.Millisecond
	intentCacheSmoothKeyPrefix = "intent:cachesmooth:"
	// Like the account simulation: no smoothing below the caching minimum,
	// at most 95% of the input, whole 128-token blocks.
	intentCacheSmoothMinTokens   = 1024
	intentCacheSmoothCapPercent  = 95
	intentCacheSmoothGranularity = 128
)

// intentCacheSmoothBaseline is what the latest turn of a conversation left:
// the raw upstream input of its final usage (Total, cache reads included) and
// the cache reads the client was shown for it (Cached; nil in baselines
// written before it existed), when it was recorded (At, unix ms), when the
// turn started (Seq, unix ns as a decimal string, the write order), the
// account that served it and whether intent routing moved it.
//
// Accounts count the same input differently (one reports a third of
// another's tokens), so smoothing carries the shown cache rate Cached/Total
// over, never an absolute token count.
type intentCacheSmoothBaseline struct {
	Total     int    `json:"t"`
	Cached    *int   `json:"c,omitempty"`
	At        int64  `json:"at"`
	Seq       string `json:"s"`
	AccountID int64  `json:"a"`
	Routed    bool   `json:"r"`
}

type intentCacheSmoothStore interface {
	Get(ctx context.Context, key string) (*intentCacheSmoothBaseline, error)
	// Put stores b unless the stored baseline started later (larger Seq).
	Put(ctx context.Context, key string, b intentCacheSmoothBaseline, ttl time.Duration) error
}

// IntentCacheSmoothPlan is one request's view of its conversation's baseline.
// It travels on the IntentRouteDecision; nil means smoothing is off.
type IntentCacheSmoothPlan struct {
	key      string
	routed   bool
	seq      string
	baseline *intentCacheSmoothBaseline
	store    intentCacheSmoothStore
	now      func() time.Time
	once     sync.Once
}

// CacheSmoothPlan returns the decision's smoothing plan, or nil.
func (d *IntentRouteDecision) CacheSmoothPlan() *IntentCacheSmoothPlan {
	if d == nil {
		return nil
	}
	return d.cacheSmooth
}

// IntentCacheSmoothPlanFromContext returns the request's smoothing plan, or nil.
func IntentCacheSmoothPlanFromContext(ctx context.Context) *IntentCacheSmoothPlan {
	return IntentRouteDecisionFromContext(ctx).CacheSmoothPlan()
}

// PrevShown reports the previous turn's shown cache reads and raw input (the
// cache rate the client saw, as integers) when this turn, served by
// accountID, lost the conversation's cache to an intent-routing switch: it
// was moved to another account, or it is the first turn after a routed one
// and is served elsewhere. A turn served where the previous one was, a
// baseline older than the TTL, one without a shown count, or one whose client
// saw no cache gets nothing.
func (p *IntentCacheSmoothPlan) PrevShown(accountID int64) (cached, total int, ok bool) {
	if p == nil || p.baseline == nil || accountID <= 0 {
		return 0, 0, false
	}
	b := p.baseline
	if b.Total <= 0 || b.Cached == nil || *b.Cached <= 0 || *b.Cached > b.Total || b.AccountID == accountID {
		return 0, 0, false
	}
	if p.clock().Sub(time.UnixMilli(b.At)) > intentCacheSmoothTTL {
		return 0, 0, false
	}
	if !p.routed && !b.Routed {
		return 0, 0, false
	}
	return *b.Cached, b.Total, true
}

// Commit records this turn as the conversation's baseline, once: the raw
// input of the final usage the client got and the cache reads it was shown.
// The caller waits at most intentCacheSmoothWriteWait; a slow write only
// costs the next turn its smoothing.
func (p *IntentCacheSmoothPlan) Commit(accountID int64, rawTotal, shownCached int) {
	if p == nil || p.store == nil || accountID <= 0 || rawTotal <= 0 {
		return
	}
	shown := min(max(shownCached, 0), rawTotal)
	p.once.Do(func() {
		b := intentCacheSmoothBaseline{Total: rawTotal, Cached: &shown, At: p.clock().UnixMilli(), Seq: p.seq, AccountID: accountID, Routed: p.routed}
		ctx, cancel := context.WithTimeout(context.Background(), intentCacheSmoothWriteWait)
		done := make(chan error, 1)
		go func() { done <- p.store.Put(ctx, p.key, b, intentCacheSmoothTTL) }()
		select {
		case err := <-done:
			if err != nil {
				slog.Warn("intent_route.cache_smooth_write_failed", "error", err)
			}
		case <-ctx.Done():
			slog.Warn("intent_route.cache_smooth_write_timeout")
		}
		cancel()
	})
}

func (p *IntentCacheSmoothPlan) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// intentCacheSmoothCandidate is the cache read a turn of total input tokens
// (creation of them written to the cache) reports after a switch: the
// previous turn's shown cache rate (prevCached/prevTotal) applied to total,
// within the cap, in whole blocks, or 0 below the caching minimum. Integer
// arithmetic throughout: a float rate can land a hair under a block boundary
// and round a whole block (or the whole candidate) away.
func intentCacheSmoothCandidate(prevCached, prevTotal, total, creation int) int {
	if prevCached <= 0 || prevTotal <= 0 || total < intentCacheSmoothMinTokens {
		return 0
	}
	want := int(int64(prevCached) * int64(total) / int64(prevTotal))
	limit := min(want, int(int64(total)*intentCacheSmoothCapPercent/100), total-max(creation, 0))
	if limit <= 0 {
		return 0
	}
	sim := limit / intentCacheSmoothGranularity * intentCacheSmoothGranularity
	if sim < intentCacheSmoothMinTokens {
		return 0
	}
	return sim
}

// intentCacheSmoothKey identifies a conversation for smoothing: the routing
// session (API key included) within one group and one model, whose caches
// never mix. Empty when the conversation cannot be identified.
func intentCacheSmoothKey(groupID int64, model, sessionKey string) string {
	if sessionKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("cachesmooth\x00" + strconv.FormatInt(groupID, 10) + "\x00" + strings.ToLower(strings.TrimSpace(model)) + "\x00" + sessionKey))
	return hex.EncodeToString(sum[:16])
}

// cacheSmoothPlan builds the plan of a request of a group with smoothing on,
// reading the conversation's baseline within intentCacheSmoothReadWait. Nil
// when the conversation cannot be identified or there is no store.
func (s *IntentRouterService) cacheSmoothPlan(ctx context.Context, cfg *intentRouterConfig, in IntentRouteInput, routed bool) *IntentCacheSmoothPlan {
	if s == nil || s.smoothStore == nil || cfg == nil || !cfg.CacheSmoothEnabled {
		return nil
	}
	view := newIntentRequestView(in.Body)
	key := intentCacheSmoothKey(in.GroupID, intentRequestModel(in), intentSessionKey(in.APIKeyID, in.Header, view))
	if key == "" {
		return nil
	}
	now := s.now
	if now == nil {
		now = time.Now
	}
	plan := &IntentCacheSmoothPlan{
		key:    key,
		routed: routed,
		seq:    strconv.FormatInt(now().UnixNano(), 10),
		store:  s.smoothStore,
		now:    now,
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), intentCacheSmoothReadWait)
	type result struct {
		b   *intentCacheSmoothBaseline
		err error
	}
	// The shared Redis client can ignore context deadlines during socket
	// reads (see nextRequestVersion): bound the wait independently.
	done := make(chan result, 1)
	go func() { b, err := s.smoothStore.Get(readCtx, key); done <- result{b, err} }()
	select {
	case r := <-done:
		if r.err == nil && readCtx.Err() == nil {
			plan.baseline = r.b
		} else if r.err != nil {
			slog.Debug("intent_route.cache_smooth_read_failed", "group_id", in.GroupID, "error", r.err)
		}
	case <-readCtx.Done():
		slog.Debug("intent_route.cache_smooth_read_timeout", "group_id", in.GroupID)
	}
	cancel()
	return plan
}

// ---- redis ----

type redisIntentCacheSmoothStore struct{ rdb *redis.Client }

func (r *redisIntentCacheSmoothStore) Get(ctx context.Context, key string) (*intentCacheSmoothBaseline, error) {
	raw, err := r.rdb.Get(ctx, intentCacheSmoothKeyPrefix+key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var b intentCacheSmoothBaseline
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, nil
	}
	return &b, nil
}

// Seq is a decimal string of unix nanoseconds, too large for Lua's doubles:
// compare it as a string (a longer one is larger).
var intentCacheSmoothPutScript = redis.NewScript(`
local old = redis.call('GET', KEYS[1])
if old then
 local ok, state = pcall(cjson.decode, old)
 if ok and type(state) == 'table' and type(state.s) == 'string' then
  local o, n = state.s, ARGV[2]
  if #o > #n or (#o == #n and o > n) then return 0 end
 end
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[3])
return 1
`)

func (r *redisIntentCacheSmoothStore) Put(ctx context.Context, key string, b intentCacheSmoothBaseline, ttl time.Duration) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return intentCacheSmoothPutScript.Run(ctx, r.rdb, []string{intentCacheSmoothKeyPrefix + key}, raw, b.Seq, ttl.Milliseconds()).Err()
}

// NewIntentCacheSmoothDecisionForTest returns a decision that carries only a
// smoothing plan (no store, no baseline), for tests outside this package.
func NewIntentCacheSmoothDecisionForTest(groupID int64) *IntentRouteDecision {
	return &IntentRouteDecision{GroupID: groupID, cacheSmooth: &IntentCacheSmoothPlan{key: "test"}}
}
