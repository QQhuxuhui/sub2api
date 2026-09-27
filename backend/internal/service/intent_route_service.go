package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
)

const (
	intentRouteConfigTTL      = 15 * time.Second
	intentRouteConfigLoadWait = 2 * time.Second
	intentRouteStoreTimeout   = 300 * time.Millisecond
	intentRouteMaxEvents      = 500
	intentRouteMaxBodyBytes   = 16 << 20

	// After this many classifier failures in a row the group stops classifying
	// for a while: a broken classifier must cost requests nothing but one
	// timeout now and then, not one timeout each.
	intentRouteBreakerThreshold = 5
	intentRouteBreakerCooldown  = 30 * time.Second

	intentRouteSessionKeyPrefix = "intent_route:s:"
	intentRouteEventsKey        = "intent_route:events"
)

// intentRouterConfig is the hot-path copy of one group's router.
type intentRouterConfig struct {
	GroupID            int64
	Enabled            bool
	ClassifierBaseURL  string
	ClassifierAPIKey   string
	ClassifierProtocol string
	ClassifierModel    string
	ClassifierTimeout  time.Duration
	CacheTTL           time.Duration
	MaxInputChars      int
	Rules              []domain.IntentRule
}

// activeRules are the rules a request can actually be routed by.
func (c *intentRouterConfig) activeRules() []domain.IntentRule {
	if c == nil {
		return nil
	}
	rules := make([]domain.IntentRule, 0, len(c.Rules))
	for _, rule := range c.Rules {
		if rule.Enabled && strings.TrimSpace(rule.Name) != "" && len(rule.AccountIDs) > 0 {
			rules = append(rules, rule)
		}
	}
	return rules
}

// usable reports whether the router can route anything: it needs active rules
// and at least one way of matching them — a classifier, or keywords.
func (c *intentRouterConfig) usable() bool {
	if c == nil || !c.Enabled {
		return false
	}
	return len(c.classifierRules()) > 0 || c.hasKeywords()
}

func (c *intentRouterConfig) classifierReady() bool {
	return c != nil && c.ClassifierModel != "" && c.ClassifierAPIKey != ""
}

// classifierRules are the active rules the classifier chooses among: those
// with a description. Keyword-only rules are never offered to the model.
// Empty when no classifier is configured.
func (c *intentRouterConfig) classifierRules() []domain.IntentRule {
	if !c.classifierReady() {
		return nil
	}
	var rules []domain.IntentRule
	for _, rule := range c.activeRules() {
		if strings.TrimSpace(rule.Description) != "" {
			rules = append(rules, rule)
		}
	}
	return rules
}

func (c *intentRouterConfig) hasKeywords() bool {
	for _, rule := range c.activeRules() {
		if len(rule.Keywords) > 0 {
			return true
		}
	}
	return false
}

// matchKeywords returns the first active rule, in configured order, that has a
// keyword occurring in text (case-insensitive), and the keyword that matched.
func (c *intentRouterConfig) matchKeywords(text string) (domain.IntentRule, string, bool) {
	if text == "" {
		return domain.IntentRule{}, "", false
	}
	lower := strings.ToLower(text)
	for _, rule := range c.activeRules() {
		for _, keyword := range rule.Keywords {
			if k := strings.ToLower(strings.TrimSpace(keyword)); k != "" && strings.Contains(lower, k) {
				return rule, keyword, true
			}
		}
	}
	return domain.IntentRule{}, "", false
}

// scopeAccountIDs lists every account any rule names, once each — including
// rules that are switched off. The scope answers "could an earlier turn have
// been routed here?", and a rule (or the whole router) may have been disabled
// since; a conversation already in flight must still be able to continue.
func (c *intentRouterConfig) scopeAccountIDs() []int64 {
	if c == nil {
		return nil
	}
	seen := make(map[int64]struct{})
	var scope []int64
	for _, rule := range c.Rules {
		for _, id := range rule.AccountIDs {
			if _, dup := seen[id]; !dup && id > 0 {
				seen[id] = struct{}{}
				scope = append(scope, id)
			}
		}
	}
	return scope
}

func (c *intentRouterConfig) rule(name string) (domain.IntentRule, bool) {
	for _, rule := range c.activeRules() {
		if rule.Name == name {
			return rule, true
		}
	}
	return domain.IntentRule{}, false
}

// intentRouteSession is what is remembered per conversation.
type intentRouteSession struct {
	// Intent is "" when the conversation matched no rule; that is remembered
	// too, so an unmatched conversation is not re-classified every turn.
	Intent    string `json:"i"`
	AccountID int64  `json:"a,omitempty"`
	Version   int64  `json:"v,omitempty"`
	Sequence  int64  `json:"q,omitempty"`
}

// IntentRouteEvent is one line of the admin-facing routing log.
type IntentRouteEvent struct {
	Time      time.Time `json:"time"`
	GroupID   int64     `json:"group_id"`
	Kind      string    `json:"kind"` // keyword | classified | cached | routed | error | skipped
	Intent    string    `json:"intent,omitempty"`
	AccountID int64     `json:"account_id,omitempty"`
	LatencyMS int64     `json:"latency_ms,omitempty"`
	Detail    string    `json:"detail,omitempty"`
}

// intentRouteStore keeps conversation decisions and the routing log.
//
// Session writes run detached from the request, so they may land in any order,
// and parallel requests of one conversation write concurrently. The three
// write kinds are therefore chosen so that no arrival order loses information:
// PinSession replaces a value only when its request version and attempt
// sequence are newer. Redis assigns versions across all gateway instances.
type intentRouteStore interface {
	GetSession(ctx context.Context, groupID int64, sessionKey string) (*intentRouteSession, error)
	NextVersion(ctx context.Context) (int64, error)
	// InitSession records a fresh classification unless the conversation is
	// already known — it can never erase a pin written a moment earlier.
	InitSession(ctx context.Context, groupID int64, sessionKey string, session intentRouteSession, ttl time.Duration) error
	// TouchSession extends the lifetime without rewriting the value.
	TouchSession(ctx context.Context, groupID int64, sessionKey string, ttl time.Duration) error
	// PinSession stores the intent together with the account that served it.
	PinSession(ctx context.Context, groupID int64, sessionKey string, session intentRouteSession, ttl time.Duration) error
	DeleteSession(ctx context.Context, groupID int64, sessionKey string, observed intentRouteSession) error
	ClearSessions(ctx context.Context, groupID int64) (int64, error)
	AppendEvent(ctx context.Context, event IntentRouteEvent) error
	ListEvents(ctx context.Context, limit int) ([]IntentRouteEvent, error)
}

// IntentRouteInput is what the gateway middleware hands over for a request.
type IntentRouteInput struct {
	GroupID  int64
	APIKeyID int64
	Header   http.Header
	Body     []byte
}

// IntentRouterService classifies requests of groups that opted in and turns the
// result into an IntentRouteDecision.
//
// Its contract with the request path: Decide never returns an error and never
// blocks longer than the router's classifier timeout. Whatever goes wrong —
// unreachable classifier, unparsable answer, cache outage, bad configuration —
// the request is simply scheduled as if the feature did not exist.
type IntentRouterService struct {
	entClient  *dbent.Client
	store      intentRouteStore
	classifier intentClassifier

	configs      atomic.Pointer[map[int64]*intentRouterConfig]
	configLoaded atomic.Int64 // unix nanos of the last successful load
	configMu     sync.Mutex
	reloading    atomic.Bool

	breakerMu sync.Mutex
	breakers  map[int64]*intentRouteBreaker
	now       func() time.Time
	// spawn runs work off the request path; tests make it synchronous.
	spawn func(func())
}

type intentRouteBreaker struct {
	failures  int
	openUntil time.Time
}

func NewIntentRouterService(entClient *dbent.Client, rdb *redis.Client, cfg *config.Config) *IntentRouterService {
	port := 8080
	if cfg != nil && cfg.Server.Port > 0 {
		port = cfg.Server.Port
	}
	var store intentRouteStore
	if rdb != nil {
		store = &redisIntentRouteStore{rdb: rdb}
	}
	return &IntentRouterService{
		entClient: entClient,
		store:     store,
		classifier: &httpIntentClassifier{
			// The per-request context carries the real deadline.
			client:      &http.Client{Timeout: 30 * time.Second},
			selfBaseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		},
		breakers: make(map[int64]*intentRouteBreaker),
		now:      time.Now,
		spawn:    func(work func()) { go work() },
	}
}

// Enabled reports whether the group has a usable router. It is the only cost
// intent routing adds to requests of groups that do not use it: one map read.
func (s *IntentRouterService) Enabled(ctx context.Context, groupID int64) bool {
	return s.config(ctx, groupID).usable()
}

// ScopeOnly is the decision for a group whose router exists but is not
// classifying right now (switched off, or incomplete): no preference, just the
// scope, so follow-ups of conversations routed earlier still find their
// account. nil when the group has no router or the router names no accounts.
func (s *IntentRouterService) ScopeOnly(ctx context.Context, groupID int64) *IntentRouteDecision {
	if s == nil {
		return nil
	}
	cfg := s.config(ctx, groupID)
	scope := cfg.scopeAccountIDs()
	if len(scope) == 0 {
		return nil
	}
	return &IntentRouteDecision{GroupID: groupID, ScopeAccountIDs: scope}
}

// MaxBodyBytes is the largest request body worth buffering for classification.
func (s *IntentRouterService) MaxBodyBytes() int64 { return intentRouteMaxBodyBytes }

// Decide classifies the request (or recalls its conversation's decision).
//
// nil means the group does not use intent routing. For a group that does, a
// decision is always returned: with AccountIDs when the turn matched a rule,
// and with only ScopeAccountIDs when it did not (no match, classifier trouble,
// nothing to classify) — such a turn is scheduled normally, but state an
// earlier routed turn left on one of the router's accounts stays reachable.
func (s *IntentRouterService) Decide(ctx context.Context, in IntentRouteInput) *IntentRouteDecision {
	if s == nil {
		return nil
	}
	cfg := s.config(ctx, in.GroupID)
	if !cfg.usable() {
		return nil
	}
	decision := s.classify(ctx, cfg, in)
	if decision == nil {
		decision = &IntentRouteDecision{GroupID: cfg.GroupID}
	}
	decision.ScopeAccountIDs = cfg.scopeAccountIDs()
	return decision
}

// classify returns the matched rule's decision, or nil for "no preference".
func (s *IntentRouterService) classify(ctx context.Context, cfg *intentRouterConfig, in IntentRouteInput) (decision *IntentRouteDecision) {
	// Nothing in here may ever take a request down.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("intent_route.panic", "group_id", in.GroupID, "panic", r)
			decision = nil
		}
	}()
	view := intentRequestView{body: in.Body}
	sessionKey := intentSessionKey(in.APIKeyID, in.Header, view)

	var session *intentRouteSession
	var version int64
	if sessionKey != "" && s.store != nil {
		// Reserve the write order before reading the session: a slow read must
		// not let an older request overwrite a later keyword decision. Both
		// operations share one time budget.
		storeCtx, cancel := context.WithTimeout(ctx, intentRouteStoreTimeout)
		version = s.nextRequestVersion(storeCtx, sessionKey)
		if storeCtx.Err() == nil {
			type readResult struct {
				session *intentRouteSession
				err     error
			}
			done := make(chan readResult, 1)
			go func() {
				got, err := s.store.GetSession(storeCtx, in.GroupID, sessionKey)
				done <- readResult{got, err}
			}()
			select {
			case result := <-done:
				if result.err == nil {
					session = result.session
				}
			case <-storeCtx.Done():
			}
		}
		cancel()
	}

	// Keywords are checked on every turn against the latest user message, and
	// a hit overrides whatever the conversation was routed by before.
	if rule, keyword, ok := cfg.matchKeywords(view.lastUserText(0)); ok {
		if session != nil && session.Intent == rule.Name {
			// Same intent as before: keep the conversation on its account.
			s.record(IntentRouteEvent{GroupID: in.GroupID, Kind: "keyword", Intent: rule.Name, AccountID: session.AccountID, Detail: keyword})
			s.detached(func(ctx context.Context) error {
				return s.store.TouchSession(ctx, in.GroupID, sessionKey, cfg.CacheTTL)
			})
			return s.newDecision(cfg, rule, sessionKey, session.AccountID, false, version)
		}
		s.record(IntentRouteEvent{GroupID: in.GroupID, Kind: "keyword", Intent: rule.Name, Detail: keyword})
		return s.newDecision(cfg, rule, sessionKey, 0, true, version)
	}

	if session != nil {
		if session.Intent == "" {
			return nil
		}
		// A rule that was renamed, disabled or emptied no longer binds its sessions.
		if rule, ok := cfg.rule(session.Intent); ok {
			s.record(IntentRouteEvent{GroupID: in.GroupID, Kind: "cached", Intent: rule.Name, AccountID: session.AccountID})
			// Sliding expiry: a conversation in progress keeps its route; only
			// one left idle for the whole TTL is classified afresh.
			s.detached(func(ctx context.Context) error {
				return s.store.TouchSession(ctx, in.GroupID, sessionKey, cfg.CacheTTL)
			})
			return s.newDecision(cfg, rule, sessionKey, session.AccountID, false, version)
		}
		// Stale entry: make room now, so the new classification below is not
		// rejected as "already known".
		storeCtx, cancel := context.WithTimeout(ctx, intentRouteStoreTimeout)
		_ = s.store.DeleteSession(storeCtx, in.GroupID, sessionKey, *session)
		cancel()
	}

	rules := cfg.classifierRules()
	if len(rules) == 0 {
		// Keyword-only router: an unmatched turn is scheduled normally.
		return nil
	}
	text := view.lastUserText(cfg.MaxInputChars)
	if text == "" {
		return nil
	}
	if !s.breakerAllows(in.GroupID) {
		s.record(IntentRouteEvent{GroupID: in.GroupID, Kind: "skipped", Detail: "classifier cooling down after repeated failures"})
		return nil
	}

	started := s.now()
	classifyCtx, cancel := context.WithTimeout(ctx, cfg.ClassifierTimeout)
	answer, err := s.classifier.Classify(classifyCtx, cfg, text)
	cancel()
	latency := s.now().Sub(started).Milliseconds()
	if err != nil {
		s.breakerFailure(in.GroupID)
		slog.Warn("intent_route.classify_failed", "group_id", in.GroupID, "latency_ms", latency, "error", err)
		s.record(IntentRouteEvent{GroupID: in.GroupID, Kind: "error", LatencyMS: latency, Detail: truncateRunes(err.Error(), 300)})
		return nil
	}
	intent, understood := matchIntentLabel(answer, rules)
	if !understood {
		s.breakerFailure(in.GroupID)
		slog.Warn("intent_route.answer_not_understood", "group_id", in.GroupID, "answer", truncateRunes(answer, 120))
		s.record(IntentRouteEvent{GroupID: in.GroupID, Kind: "error", LatencyMS: latency, Detail: "answer not understood: " + truncateRunes(answer, 120)})
		return nil
	}
	s.breakerSuccess(in.GroupID)
	s.record(IntentRouteEvent{GroupID: in.GroupID, Kind: "classified", Intent: intent, LatencyMS: latency})
	if s.store != nil && sessionKey != "" {
		s.detached(func(ctx context.Context) error {
			return s.store.InitSession(ctx, in.GroupID, sessionKey, intentRouteSession{Intent: intent}, cfg.CacheTTL)
		})
	}
	if intent == "" {
		return nil
	}
	rule, ok := cfg.rule(intent)
	if !ok {
		return nil
	}
	return s.newDecision(cfg, rule, sessionKey, 0, false, version)
}

// nextRequestVersion reserves write order before any potentially slow
// classification. Redis assigns versions across gateway instances.
func (s *IntentRouterService) nextRequestVersion(ctx context.Context, sessionKey string) int64 {
	if s.store == nil || sessionKey == "" {
		return 0
	}
	storeCtx, cancel := context.WithTimeout(ctx, intentRouteStoreTimeout)
	// The shared Redis client can ignore context deadlines during socket
	// reads. Bound the caller's wait independently; the buffered result
	// lets an abandoned allocation finish without blocking a goroutine.
	type allocation struct {
		version int64
		err     error
	}
	done := make(chan allocation, 1)
	go func() { v, err := s.store.NextVersion(storeCtx); done <- allocation{v, err} }()
	var version int64
	select {
	case result := <-done:
		if result.err == nil && storeCtx.Err() == nil {
			version = result.version
		}
	case <-storeCtx.Done():
	}
	cancel()
	return version
}

// newDecision builds the routed decision for rule. override (a keyword hit that
// changes the conversation's intent) records the new intent at once, versioned
// like a pin, so it replaces the conversation's earlier intent and account even
// if no target account ends up serving this turn.
func (s *IntentRouterService) newDecision(cfg *intentRouterConfig, rule domain.IntentRule, sessionKey string, pinned int64, override bool, version int64) *IntentRouteDecision {
	groupID, ttl, intent := cfg.GroupID, cfg.CacheTTL, rule.Name
	var sequence atomic.Int64
	if override && s.store != nil && sessionKey != "" && version > 0 {
		seq := sequence.Add(1)
		s.detached(func(ctx context.Context) error {
			return s.store.PinSession(ctx, groupID, sessionKey, intentRouteSession{Intent: intent, Version: version, Sequence: seq}, ttl)
		})
	}
	return &IntentRouteDecision{
		GroupID:         groupID,
		Intent:          intent,
		AccountIDs:      append([]int64(nil), rule.AccountIDs...),
		PinnedAccountID: pinned,
		onSelect: func(accountID int64) {
			s.record(IntentRouteEvent{GroupID: groupID, Kind: "routed", Intent: intent, AccountID: accountID})
			if s.store == nil || sessionKey == "" || version <= 0 {
				return
			}
			seq := sequence.Add(1)
			s.detached(func(ctx context.Context) error {
				return s.store.PinSession(ctx, groupID, sessionKey, intentRouteSession{Intent: intent, AccountID: accountID, Version: version, Sequence: seq}, ttl)
			})
		},
	}
}

// detached runs a store write off the request path: a slow or failing Redis
// must not add to request latency, and its failure is not the user's problem.
// Writes started this way may complete in any order — see intentRouteStore.
func (s *IntentRouterService) detached(write func(ctx context.Context) error) {
	s.spawn(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := write(ctx); err != nil {
			slog.Debug("intent_route.store_write_failed", "error", err)
		}
	})
}

func (s *IntentRouterService) record(event IntentRouteEvent) {
	if s.store == nil {
		return
	}
	event.Time = s.now()
	s.spawn(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.store.AppendEvent(ctx, event)
	})
}

// ---- configuration cache ----

// config never blocks a request on the database except for the very first
// load of the process; afterwards a stale snapshot is served while a refresh
// runs in the background.
func (s *IntentRouterService) config(ctx context.Context, groupID int64) *intentRouterConfig {
	snapshot := s.configs.Load()
	if snapshot == nil {
		loadCtx, cancel := context.WithTimeout(ctx, intentRouteConfigLoadWait)
		_ = s.reloadConfigs(loadCtx)
		cancel()
		snapshot = s.configs.Load()
	} else if s.now().UnixNano()-s.configLoaded.Load() > int64(intentRouteConfigTTL) && s.reloading.CompareAndSwap(false, true) {
		go func() {
			defer s.reloading.Store(false)
			loadCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.reloadConfigs(loadCtx)
		}()
	}
	if snapshot == nil {
		return nil
	}
	return (*snapshot)[groupID]
}

func (s *IntentRouterService) reloadConfigs(ctx context.Context) error {
	if s.entClient == nil {
		return errors.New("intent router: no database client")
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	rows, err := s.entClient.IntentRouter.Query().All(ctx)
	if err != nil {
		slog.Warn("intent_route.config_load_failed", "error", err)
		// Keep serving the previous snapshot; mark the attempt so a dead
		// database is not hammered on every request.
		if s.configs.Load() == nil {
			empty := map[int64]*intentRouterConfig{}
			s.configs.Store(&empty)
		}
		s.configLoaded.Store(s.now().UnixNano())
		return err
	}
	next := make(map[int64]*intentRouterConfig, len(rows))
	for _, row := range rows {
		next[row.GroupID] = intentRouterConfigFromEnt(row)
	}
	s.configs.Store(&next)
	s.configLoaded.Store(s.now().UnixNano())
	return nil
}

// InvalidateConfig makes a saved router take effect on this instance at once;
// other instances follow within intentRouteConfigTTL.
func (s *IntentRouterService) InvalidateConfig(ctx context.Context) {
	_ = s.reloadConfigs(ctx)
}

func intentRouterConfigFromEnt(row *dbent.IntentRouter) *intentRouterConfig {
	cfg := &intentRouterConfig{
		GroupID:            row.GroupID,
		Enabled:            row.Enabled,
		ClassifierBaseURL:  strings.TrimSpace(row.ClassifierBaseURL),
		ClassifierAPIKey:   strings.TrimSpace(row.ClassifierAPIKey),
		ClassifierProtocol: strings.TrimSpace(row.ClassifierProtocol),
		ClassifierModel:    strings.TrimSpace(row.ClassifierModel),
		ClassifierTimeout:  time.Duration(row.ClassifierTimeoutMs) * time.Millisecond,
		CacheTTL:           time.Duration(row.CacheTTLSeconds) * time.Second,
		MaxInputChars:      row.MaxInputChars,
		Rules:              row.Rules,
	}
	if cfg.ClassifierTimeout <= 0 {
		cfg.ClassifierTimeout = 3 * time.Second
	}
	if cfg.MaxInputChars <= 0 {
		cfg.MaxInputChars = 2000
	}
	return cfg
}

// ---- breaker ----

func (s *IntentRouterService) breakerAllows(groupID int64) bool {
	s.breakerMu.Lock()
	defer s.breakerMu.Unlock()
	b := s.breakers[groupID]
	return b == nil || !s.now().Before(b.openUntil)
}

func (s *IntentRouterService) breakerFailure(groupID int64) {
	s.breakerMu.Lock()
	defer s.breakerMu.Unlock()
	b := s.breakers[groupID]
	if b == nil {
		b = &intentRouteBreaker{}
		s.breakers[groupID] = b
	}
	b.failures++
	if b.failures >= intentRouteBreakerThreshold {
		b.failures = 0
		b.openUntil = s.now().Add(intentRouteBreakerCooldown)
	}
}

func (s *IntentRouterService) breakerSuccess(groupID int64) {
	s.breakerMu.Lock()
	defer s.breakerMu.Unlock()
	delete(s.breakers, groupID)
}

// ---- redis store ----

type redisIntentRouteStore struct{ rdb *redis.Client }

func intentRouteSessionRedisKey(groupID int64, sessionKey string) string {
	return fmt.Sprintf("%s%d:%s", intentRouteSessionKeyPrefix, groupID, sessionKey)
}

func (r *redisIntentRouteStore) GetSession(ctx context.Context, groupID int64, sessionKey string) (*intentRouteSession, error) {
	raw, err := r.rdb.Get(ctx, intentRouteSessionRedisKey(groupID, sessionKey)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var session intentRouteSession
	if err := json.Unmarshal(raw, &session); err != nil {
		return nil, nil
	}
	return &session, nil
}

func (r *redisIntentRouteStore) InitSession(ctx context.Context, groupID int64, sessionKey string, session intentRouteSession, ttl time.Duration) error {
	raw, err := json.Marshal(session)
	if err != nil {
		return err
	}
	return r.rdb.SetNX(ctx, intentRouteSessionRedisKey(groupID, sessionKey), raw, ttl).Err()
}

func (r *redisIntentRouteStore) TouchSession(ctx context.Context, groupID int64, sessionKey string, ttl time.Duration) error {
	return r.rdb.Expire(ctx, intentRouteSessionRedisKey(groupID, sessionKey), ttl).Err()
}

// The counter has no expiry: expired/cleared sessions must not reuse versions.
func (r *redisIntentRouteStore) NextVersion(ctx context.Context) (int64, error) {
	return r.rdb.Incr(ctx, "intent_route:version").Result()
}

var intentRoutePinScript = redis.NewScript(`
local old = redis.call('GET', KEYS[1])
if old then
 local ok, state = pcall(cjson.decode, old)
 if ok and type(state) == 'table' then
  local v = tonumber(state.v) or 0
  local q = tonumber(state.q) or 0
  local nextv = tonumber(ARGV[2])
  local nextq = tonumber(ARGV[3])
  if v > nextv or (v == nextv and q >= nextq) then return 0 end
 end
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[4])
return 1
`)

func (r *redisIntentRouteStore) PinSession(ctx context.Context, groupID int64, sessionKey string, session intentRouteSession, ttl time.Duration) error {
	raw, err := json.Marshal(session)
	if err != nil {
		return err
	}
	if session.Version <= 0 || session.Sequence <= 0 {
		return nil
	}
	return intentRoutePinScript.Run(ctx, r.rdb, []string{intentRouteSessionRedisKey(groupID, sessionKey)}, raw, strconv.FormatInt(session.Version, 10), strconv.FormatInt(session.Sequence, 10), ttl.Milliseconds()).Err()
}

var intentRouteDeleteScript = redis.NewScript(`
local raw = redis.call('GET', KEYS[1])
if not raw then return 0 end
local ok, state = pcall(cjson.decode, raw)
if not ok or type(state) ~= 'table' then return 0 end
if (tonumber(state.v) or 0) ~= tonumber(ARGV[1]) then return 0 end
if (tonumber(state.q) or 0) ~= tonumber(ARGV[2]) then return 0 end
if (state.i or '') ~= ARGV[3] then return 0 end
if (tonumber(state.a) or 0) ~= tonumber(ARGV[4]) then return 0 end
return redis.call('DEL', KEYS[1])
`)

func (r *redisIntentRouteStore) DeleteSession(ctx context.Context, groupID int64, sessionKey string, observed intentRouteSession) error {
	return intentRouteDeleteScript.Run(ctx, r.rdb, []string{intentRouteSessionRedisKey(groupID, sessionKey)},
		strconv.FormatInt(observed.Version, 10), strconv.FormatInt(observed.Sequence, 10),
		observed.Intent, strconv.FormatInt(observed.AccountID, 10)).Err()
}

// ClearSessions forgets every conversation of a group (groupID > 0) or of all
// groups (groupID == 0). Clearing is a rare admin action, so it walks the
// keyspace with SCAN instead of taxing every request with a generation lookup.
func (r *redisIntentRouteStore) ClearSessions(ctx context.Context, groupID int64) (int64, error) {
	pattern := intentRouteSessionKeyPrefix + "*"
	if groupID > 0 {
		pattern = fmt.Sprintf("%s%d:*", intentRouteSessionKeyPrefix, groupID)
	}
	var (
		cursor  uint64
		removed int64
	)
	for {
		keys, next, err := r.rdb.Scan(ctx, cursor, pattern, 500).Result()
		if err != nil {
			return removed, err
		}
		if len(keys) > 0 {
			n, err := r.rdb.Unlink(ctx, keys...).Result()
			if err != nil {
				return removed, err
			}
			removed += n
		}
		if cursor = next; cursor == 0 {
			return removed, nil
		}
	}
}

func (r *redisIntentRouteStore) AppendEvent(ctx context.Context, event IntentRouteEvent) error {
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	pipe := r.rdb.Pipeline()
	pipe.LPush(ctx, intentRouteEventsKey, raw)
	pipe.LTrim(ctx, intentRouteEventsKey, 0, intentRouteMaxEvents-1)
	_, err = pipe.Exec(ctx)
	return err
}

func (r *redisIntentRouteStore) ListEvents(ctx context.Context, limit int) ([]IntentRouteEvent, error) {
	if limit <= 0 || limit > intentRouteMaxEvents {
		limit = intentRouteMaxEvents
	}
	rows, err := r.rdb.LRange(ctx, intentRouteEventsKey, 0, int64(limit-1)).Result()
	if err != nil {
		return nil, err
	}
	events := make([]IntentRouteEvent, 0, len(rows))
	for _, row := range rows {
		var event IntentRouteEvent
		if json.Unmarshal([]byte(row), &event) == nil {
			events = append(events, event)
		}
	}
	return events, nil
}
