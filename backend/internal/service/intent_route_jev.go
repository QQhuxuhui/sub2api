package service

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqljson"
	"github.com/redis/go-redis/v9"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/intentrouter"
	"github.com/Wei-Shaw/sub2api/ent/predicate"
	"github.com/Wei-Shaw/sub2api/internal/domain"
)

const (
	intentJevEndpoint            = "https://api.typesafe.ai/v1/systemone"
	intentJevDefaultModel        = "jev-latest"
	intentJevDefaultThreshold    = 0.5
	intentJevDefaultMaxTurns     = 3
	intentJevDefaultTimeoutMS    = 1000
	intentJevDefaultSample       = 100
	intentJevDefaultLowBalance   = 1.0
	intentJevUSDPerMTok          = 0.042 // jev-1.13/1.14 list price, input tokens only
	intentJevCNYPerUSD           = 7.0
	intentJevStatsDays           = 8
	intentJevFailuresBeforePause = 2
	intentJevFirstPause          = time.Minute
	intentJevMaxPause            = 10 * time.Minute
	// Only the latest user message is judged, cut to this many runes: probes
	// say what they are early, and input tokens are what Jev bills.
	intentJevMaxTextRunes   = 4000
	intentJevMaxAnswerBytes = 64 << 10
	// A Jev hit has no keyword to center on; reviewers get the message head.
	intentJevHitExcerpt = 320

	// The question Jev answers with a probability. Measured on captured
	// traffic (docs/2026-10-28_jev-probe-classifier.md): about 98% of probes
	// caught and 1.2% of real short requests misjudged at a 0.5 threshold.
	intentJevProbeQuestion = "The `state` is the latest message a client sent to an AI model through an API relay. " +
		"It is a test or probe of the AI model itself (a benchmark puzzle with one fixed answer, a model-fingerprinting question, " +
		"an 'SVG of a pelican riding a bicycle' style drawing benchmark, a random number or random choice sampling test, " +
		"an arithmetic or string task demanding output in an exact format, or a connectivity check like 'reply OK' / 'hi'), " +
		"sent to judge which model answers or how capable it is, rather than a genuine request from someone doing real work."
)

// intentJevDayZone is the zone daily statistics roll over in (the operators'
// day, not UTC).
var intentJevDayZone = time.FixedZone("CST", 8*3600)

// intentJevAnswer is one Jev verdict and what it was billed.
type intentJevAnswer struct {
	P      float64
	Tokens int64
}

// intentJevStatusError is a non-200 answer from Jev.
type intentJevStatusError struct {
	Status int
	Body   string
}

func (e *intentJevStatusError) Error() string {
	return fmt.Sprintf("jev returned %d: %s", e.Status, e.Body)
}

// intentJevAccountProblem reports errors that retrying cannot fix — a bad
// key, an unpaid account, a blocked region — which switch Jev off.
func intentJevAccountProblem(err error) (int, bool) {
	var statusErr *intentJevStatusError
	if !errors.As(err, &statusErr) {
		return 0, false
	}
	switch statusErr.Status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusUnavailableForLegalReasons:
		return statusErr.Status, true
	}
	return 0, false
}

// intentProbeJudge asks Jev how likely a text is a probe of the model.
type intentProbeJudge interface {
	ProbeProbability(ctx context.Context, apiKey, model, text string) (intentJevAnswer, error)
}

type httpIntentProbeJudge struct {
	client   *http.Client
	endpoint string
}

func newHTTPIntentProbeJudge() *httpIntentProbeJudge {
	// The per-request context carries the real deadline.
	return &httpIntentProbeJudge{client: &http.Client{Timeout: 30 * time.Second}, endpoint: intentJevEndpoint}
}

func (j *httpIntentProbeJudge) ProbeProbability(ctx context.Context, apiKey, model, text string) (intentJevAnswer, error) {
	body, err := json.Marshal(map[string]any{
		"state": truncateRunes(text, intentJevMaxTextRunes),
		"model": model,
		"questions": map[string]any{
			"is_probe": map[string]any{"type": "noul", "instructions": intentJevProbeQuestion},
		},
	})
	if err != nil {
		return intentJevAnswer{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.endpoint, bytes.NewReader(body))
	if err != nil {
		return intentJevAnswer{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := j.client.Do(req)
	if err != nil {
		return intentJevAnswer{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, intentJevMaxAnswerBytes))
	if err != nil {
		return intentJevAnswer{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return intentJevAnswer{}, &intentJevStatusError{Status: resp.StatusCode, Body: truncateRunes(strings.TrimSpace(string(raw)), 200)}
	}
	var parsed struct {
		Answers struct {
			IsProbe struct {
				Noul *float64 `json:"noul"`
			} `json:"is_probe"`
		} `json:"answers"`
		Usage struct {
			InputTokens int64 `json:"input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return intentJevAnswer{}, fmt.Errorf("jev answer is not json: %w", err)
	}
	p := parsed.Answers.IsProbe.Noul
	if p == nil || *p < 0 || *p > 1 {
		return intentJevAnswer{}, fmt.Errorf("jev answer has no probability: %s", truncateRunes(string(raw), 200))
	}
	return intentJevAnswer{P: *p, Tokens: parsed.Usage.InputTokens}, nil
}

// intentJevConfig is the hot-path copy of a router's Jev settings.
type intentJevConfig struct {
	Revision      string
	Enabled       bool
	APIKey        string
	Rule          string
	Threshold     float64
	MaxTurns      int
	Timeout       time.Duration
	Model         string
	SamplePercent int
	AllUsers      bool
	UserIDs       map[int64]struct{}
	APIKeyIDs     map[int64]struct{}
}

func newIntentJevConfig(apiKey string, in domain.IntentJevConfig) intentJevConfig {
	in = intentJevWithDefaults(in)
	cfg := intentJevConfig{
		Revision:      in.Revision,
		Enabled:       in.Enabled,
		APIKey:        strings.TrimSpace(apiKey),
		Rule:          strings.TrimSpace(in.Rule),
		Threshold:     in.Threshold,
		MaxTurns:      in.MaxTurns,
		Timeout:       time.Duration(in.TimeoutMS) * time.Millisecond,
		Model:         in.Model,
		SamplePercent: in.SamplePercent,
		AllUsers:      in.Scope != domain.IntentJevScopeListed,
		UserIDs:       make(map[int64]struct{}, len(in.UserIDs)),
		APIKeyIDs:     make(map[int64]struct{}, len(in.APIKeyIDs)),
	}
	for _, id := range in.UserIDs {
		cfg.UserIDs[id] = struct{}{}
	}
	for _, id := range in.APIKeyIDs {
		cfg.APIKeyIDs[id] = struct{}{}
	}
	return cfg
}

// intentJevWithDefaults fills unset fields, so a router saved before Jev
// existed (or before a field was added) reads with the defaults in force.
func intentJevWithDefaults(jev domain.IntentJevConfig) domain.IntentJevConfig {
	if jev.Threshold == 0 {
		jev.Threshold = intentJevDefaultThreshold
	}
	if jev.MaxTurns == 0 {
		jev.MaxTurns = intentJevDefaultMaxTurns
	}
	if jev.TimeoutMS == 0 {
		jev.TimeoutMS = intentJevDefaultTimeoutMS
	}
	if jev.Model = strings.TrimSpace(jev.Model); jev.Model == "" {
		jev.Model = intentJevDefaultModel
	}
	if jev.Scope == "" {
		jev.Scope = domain.IntentJevScopeAll
	}
	if jev.SamplePercent == 0 {
		jev.SamplePercent = intentJevDefaultSample
	}
	if jev.LowBalanceUSD == nil {
		low := intentJevDefaultLowBalance
		jev.LowBalanceUSD = &low
	}
	if jev.UserIDs == nil {
		jev.UserIDs = []int64{}
	}
	if jev.APIKeyIDs == nil {
		jev.APIKeyIDs = []int64{}
	}
	return jev
}

// covers reports whether a request of this user and API key is judged at all.
func (j *intentJevConfig) covers(userID, apiKeyID int64) bool {
	if j.AllUsers {
		return true
	}
	if _, ok := j.UserIDs[userID]; ok && userID > 0 {
		return true
	}
	_, ok := j.APIKeyIDs[apiKeyID]
	return ok && apiKeyID > 0
}

// jevRule is the active rule probes are routed to, if Jev is usable at all.
func (c *intentRouterConfig) jevRule() (domain.IntentRule, bool) {
	if c == nil || !c.Jev.Enabled || c.Jev.APIKey == "" {
		return domain.IntentRule{}, false
	}
	return c.rule(c.Jev.Rule)
}

// ---- health: pausing and switching off ----

// intentJevHealth is this instance's view of whether Jev can be asked.
type intentJevHealth struct {
	failures    int
	pauseUntil  time.Time
	lastPause   time.Duration
	reason      string
	lastSuccess time.Time
	// disabled is set the moment an account-level error arrives, before the
	// switch-off reaches the database, so no further request waits on Jev.
	// It holds only for the configuration version it happened under: once the
	// router is saved anywhere (say an admin re-enables Jev on another
	// instance), the new version is asked again.
	disabled         bool
	disabledRevision string
}

func (s *IntentRouterService) jevHealthOf(groupID int64) *intentJevHealth {
	if s.jevHealth == nil {
		s.jevHealth = make(map[int64]*intentJevHealth)
	}
	h := s.jevHealth[groupID]
	if h == nil {
		h = &intentJevHealth{}
		s.jevHealth[groupID] = h
	}
	return h
}

func (s *IntentRouterService) jevAvailable(groupID int64, revision string) bool {
	s.jevMu.Lock()
	defer s.jevMu.Unlock()
	h := s.jevHealthOf(groupID)
	if h.disabled && h.disabledRevision != revision {
		h.disabled, h.disabledRevision = false, ""
	}
	return !h.disabled && !s.now().Before(h.pauseUntil)
}

// jevFailure counts a temporary failure: two in a row pause Jev for a minute;
// a failure right after a pause pauses again for twice as long, up to ten
// minutes.
func (s *IntentRouterService) jevFailure(groupID int64, reason string) {
	s.jevMu.Lock()
	defer s.jevMu.Unlock()
	h := s.jevHealthOf(groupID)
	h.failures++
	h.reason = reason
	pause := time.Duration(0)
	switch {
	case h.lastPause > 0:
		pause = min(h.lastPause*2, intentJevMaxPause)
	case h.failures >= intentJevFailuresBeforePause:
		pause = intentJevFirstPause
	}
	if pause > 0 {
		h.pauseUntil, h.lastPause, h.failures = s.now().Add(pause), pause, 0
	}
}

func (s *IntentRouterService) jevSuccess(groupID int64) {
	s.jevMu.Lock()
	defer s.jevMu.Unlock()
	h := s.jevHealthOf(groupID)
	h.failures, h.lastPause, h.pauseUntil, h.reason = 0, 0, time.Time{}, ""
	h.lastSuccess = s.now()
}

// jevReset forgets the group's health, e.g. after an admin saved the router.
func (s *IntentRouterService) jevReset(groupID int64) {
	s.jevMu.Lock()
	defer s.jevMu.Unlock()
	delete(s.jevHealth, groupID)
}

// jevSwitchOff turns Jev off for the group at once and in the database, with
// the reason the admin page shows. It only ever turns Jev off; an admin turns
// it back on. The database write applies only to the configuration the failed
// call was made with — same revision, same key: if an admin saved meanwhile
// (a new key, Jev re-enabled), that newer configuration is left alone.
func (s *IntentRouterService) jevSwitchOff(cfg *intentRouterConfig, status int, detail string) {
	groupID, revision, apiKey := cfg.GroupID, cfg.Jev.Revision, cfg.Jev.APIKey
	s.jevMu.Lock()
	h := s.jevHealthOf(groupID)
	already := h.disabled && h.disabledRevision == revision
	h.disabled, h.disabledRevision, h.reason = true, revision, fmt.Sprintf("%d %s", status, detail)
	s.jevMu.Unlock()
	if already || s.entClient == nil {
		return
	}
	slog.Warn("intent_route.jev_switched_off", "group_id", groupID, "status", status, "detail", detail)
	s.spawn(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		row, err := s.entClient.IntentRouter.Query().Where(intentrouter.GroupIDEQ(groupID)).Only(ctx)
		if err != nil {
			if !dbent.IsNotFound(err) {
				slog.Warn("intent_route.jev_switch_off_failed", "group_id", groupID, "error", err)
			}
			return
		}
		jev := row.Jev
		if !jev.Enabled || jev.Revision != revision || row.JevAPIKey != apiKey {
			return // off already, or saved since: the newer configuration decides
		}
		now := s.now()
		jev.Enabled, jev.AutoDisabledStatus, jev.AutoDisabledDetail, jev.AutoDisabledAt = false, status, detail, &now
		jev.Revision = newIntentJevRevision()
		// Compare-and-set on the revision, so a save racing this write wins.
		n, err := s.entClient.IntentRouter.Update().
			Where(intentrouter.IDEQ(row.ID), intentrouter.JevAPIKeyEQ(apiKey),
				predicate.IntentRouter(func(sel *entsql.Selector) {
					sel.Where(sqljson.ValueEQ(sel.C(intentrouter.FieldJev), revision, sqljson.Path("revision")))
				})).
			SetJev(jev).Save(ctx)
		if err != nil || n == 0 {
			if err != nil {
				slog.Warn("intent_route.jev_switch_off_failed", "group_id", groupID, "error", err)
			}
			return
		}
		_ = s.reloadConfigs(ctx)
	})
}

// newIntentJevRevision draws a revision token; 128 random bits never repeat.
func newIntentJevRevision() string {
	var b [16]byte
	_, _ = cryptorand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ---- the decision ----

// judgeProbe asks Jev about a keyword miss and returns the rule to route it
// to. Requests outside the scope, conversations longer than MaxTurns, turns
// without user text and the share not sampled are not sent; neither is
// anything while Jev is paused or switched off. Any failure means "no
// preference".
func (s *IntentRouterService) judgeProbe(ctx context.Context, cfg *intentRouterConfig, in IntentRouteInput, view intentRequestView, text string) (domain.IntentRule, float64, bool) {
	rule, ok := cfg.jevRule()
	if !ok || s.judge == nil || text == "" || !cfg.Jev.covers(in.UserID, in.APIKeyID) || len(view.turns()) > cfg.Jev.MaxTurns {
		return domain.IntentRule{}, 0, false
	}
	if !s.jevAvailable(in.GroupID, cfg.Jev.Revision) {
		return domain.IntentRule{}, 0, false
	}
	if s.rollPercent() >= cfg.Jev.SamplePercent {
		s.jevCount(in.GroupID, intentJevStatSkipped, 1)
		return domain.IntentRule{}, 0, false
	}
	started := s.now()
	judgeCtx, cancel := context.WithTimeout(ctx, cfg.Jev.Timeout)
	answer, err := s.judge.ProbeProbability(judgeCtx, cfg.Jev.APIKey, cfg.Jev.Model, text)
	cancel()
	latency := s.now().Sub(started).Milliseconds()
	s.jevCount(in.GroupID, intentJevStatCalls, 1)
	if err != nil {
		s.jevCount(in.GroupID, intentJevStatErrors, 1)
		detail := truncateRunes(err.Error(), 300)
		s.record(IntentRouteEvent{GroupID: in.GroupID, Kind: "error", LatencyMS: latency, Detail: "jev: " + detail})
		if status, account := intentJevAccountProblem(err); account {
			s.jevSwitchOff(cfg, status, detail)
		} else {
			var statusErr *intentJevStatusError
			if errors.As(err, &statusErr) && (statusErr.Status == http.StatusBadRequest || statusErr.Status == http.StatusUnprocessableEntity) {
				slog.Error("intent_route.jev_rejected_request", "group_id", in.GroupID, "error", detail)
			}
			s.jevFailure(in.GroupID, detail)
		}
		return domain.IntentRule{}, 0, false
	}
	s.jevSuccess(in.GroupID)
	s.jevSpend(in.GroupID, answer.Tokens)
	if answer.P < cfg.Jev.Threshold {
		return domain.IntentRule{}, answer.P, false
	}
	s.jevCount(in.GroupID, intentJevStatProbes, 1)
	s.record(IntentRouteEvent{GroupID: in.GroupID, Kind: "jev", Intent: rule.Name, LatencyMS: latency, Detail: fmt.Sprintf("p=%.2f", answer.P)})
	return rule, answer.P, true
}

func (s *IntentRouterService) rollPercent() int {
	if s.roll != nil {
		return s.roll()
	}
	return rand.IntN(100)
}

// ---- statistics ----

const (
	intentJevStatCalls   = "calls"
	intentJevStatSkipped = "skipped"
	intentJevStatProbes  = "probes"
	intentJevStatErrors  = "errors"
	intentJevStatTokens  = "tokens"
)

// intentJevStats keeps daily counters and the tokens spent since the admin
// last entered the balance. Redis-backed so restarts do not lose them.
type intentJevStats interface {
	Add(ctx context.Context, groupID int64, day, field string, n int64) error
	AddSpent(ctx context.Context, groupID int64, tokens int64) error
	ResetSpent(ctx context.Context, groupID int64) error
	Day(ctx context.Context, groupID int64, day string) (map[string]int64, error)
	Spent(ctx context.Context, groupID int64) (int64, error)
}

func (s *IntentRouterService) jevDay(t time.Time) string {
	return t.In(intentJevDayZone).Format("20060102")
}

func (s *IntentRouterService) jevCount(groupID int64, field string, n int64) {
	if s.jevStats == nil {
		return
	}
	day := s.jevDay(s.now())
	s.detached(func(ctx context.Context) error { return s.jevStats.Add(ctx, groupID, day, field, n) })
}

func (s *IntentRouterService) jevSpend(groupID int64, tokens int64) {
	if s.jevStats == nil || tokens <= 0 {
		return
	}
	day := s.jevDay(s.now())
	s.detached(func(ctx context.Context) error {
		if err := s.jevStats.Add(ctx, groupID, day, intentJevStatTokens, tokens); err != nil {
			return err
		}
		return s.jevStats.AddSpent(ctx, groupID, tokens)
	})
}

type redisIntentJevStats struct{ rdb *redis.Client }

func intentJevStatsKey(groupID int64, day string) string {
	return fmt.Sprintf("intent_route:jev:stats:%d:%s", groupID, day)
}

func intentJevSpentKey(groupID int64) string {
	return fmt.Sprintf("intent_route:jev:spent:%d", groupID)
}

func (r *redisIntentJevStats) Add(ctx context.Context, groupID int64, day, field string, n int64) error {
	key := intentJevStatsKey(groupID, day)
	pipe := r.rdb.Pipeline()
	pipe.HIncrBy(ctx, key, field, n)
	pipe.Expire(ctx, key, intentJevStatsDays*24*time.Hour)
	_, err := pipe.Exec(ctx)
	return err
}

func (r *redisIntentJevStats) AddSpent(ctx context.Context, groupID int64, tokens int64) error {
	return r.rdb.IncrBy(ctx, intentJevSpentKey(groupID), tokens).Err()
}

func (r *redisIntentJevStats) ResetSpent(ctx context.Context, groupID int64) error {
	return r.rdb.Del(ctx, intentJevSpentKey(groupID)).Err()
}

func (r *redisIntentJevStats) Day(ctx context.Context, groupID int64, day string) (map[string]int64, error) {
	raw, err := r.rdb.HGetAll(ctx, intentJevStatsKey(groupID, day)).Result()
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(raw))
	for field, value := range raw {
		var n int64
		_, _ = fmt.Sscan(value, &n)
		out[field] = n
	}
	return out, nil
}

func (r *redisIntentJevStats) Spent(ctx context.Context, groupID int64) (int64, error) {
	n, err := r.rdb.Get(ctx, intentJevSpentKey(groupID)).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return n, err
}

// ---- admin status ----

// IntentJevStatus is what the admin page shows about a group's Jev.
type IntentJevStatus struct {
	// State is off | normal | paused | auto_disabled.
	State              string     `json:"state"`
	LastSuccessAt      *time.Time `json:"last_success_at,omitempty"`
	PausedUntil        *time.Time `json:"paused_until,omitempty"`
	Reason             string     `json:"reason,omitempty"`
	AutoDisabledStatus int        `json:"auto_disabled_status,omitempty"`
	AutoDisabledDetail string     `json:"auto_disabled_detail,omitempty"`
	AutoDisabledAt     *time.Time `json:"auto_disabled_at,omitempty"`
	// Days are the daily counters, newest first.
	Days       []IntentJevDayStats `json:"days"`
	USDPerMTok float64             `json:"usd_per_mtok"`
	CNYPerUSD  float64             `json:"cny_per_usd"`
	// Balance is nil until the admin entered one.
	Balance *IntentJevBalance `json:"balance,omitempty"`
}

type IntentJevDayStats struct {
	Date    string  `json:"date"`
	Calls   int64   `json:"calls"`
	Skipped int64   `json:"skipped"`
	Probes  int64   `json:"probes"`
	Errors  int64   `json:"errors"`
	Tokens  int64   `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
}

type IntentJevBalance struct {
	SetUSD        float64   `json:"set_usd"`
	SetAt         time.Time `json:"set_at"`
	SpentTokens   int64     `json:"spent_tokens"`
	SpentUSD      float64   `json:"spent_usd"`
	EstimatedUSD  float64   `json:"estimated_usd"`
	LowBalanceUSD float64   `json:"low_balance_usd"`
	Low           bool      `json:"low"`
}

func intentJevCostUSD(tokens int64) float64 {
	return float64(tokens) * intentJevUSDPerMTok / 1e6
}

func (s *IntentRouterService) JevStatus(ctx context.Context, groupID int64) (*IntentJevStatus, error) {
	view, err := s.GetRouter(ctx, groupID)
	if err != nil {
		return nil, err
	}
	jev := view.Jev
	status := &IntentJevStatus{State: "off", Days: []IntentJevDayStats{}, USDPerMTok: intentJevUSDPerMTok, CNYPerUSD: intentJevCNYPerUSD}

	s.jevMu.Lock()
	h := s.jevHealthOf(groupID)
	health := *h
	s.jevMu.Unlock()
	if !health.lastSuccess.IsZero() {
		t := health.lastSuccess
		status.LastSuccessAt = &t
	}
	switch {
	case !jev.Enabled && jev.AutoDisabledAt != nil:
		status.State = "auto_disabled"
		status.AutoDisabledStatus, status.AutoDisabledDetail, status.AutoDisabledAt = jev.AutoDisabledStatus, jev.AutoDisabledDetail, jev.AutoDisabledAt
	case !jev.Enabled:
		status.State = "off"
	case health.disabled && health.disabledRevision == jev.Revision:
		// Switched off a moment ago; the database write is on its way.
		status.State, status.Reason = "auto_disabled", health.reason
	case s.now().Before(health.pauseUntil):
		t := health.pauseUntil
		status.State, status.PausedUntil, status.Reason = "paused", &t, health.reason
	default:
		status.State = "normal"
	}

	if s.jevStats != nil {
		today := s.now()
		for i := 0; i < intentJevStatsDays; i++ {
			day := s.jevDay(today.AddDate(0, 0, -i))
			counts, err := s.jevStats.Day(ctx, groupID, day)
			if err != nil {
				return nil, fmt.Errorf("read jev stats: %w", err)
			}
			status.Days = append(status.Days, IntentJevDayStats{
				Date: day, Calls: counts[intentJevStatCalls], Skipped: counts[intentJevStatSkipped],
				Probes: counts[intentJevStatProbes], Errors: counts[intentJevStatErrors],
				Tokens: counts[intentJevStatTokens], CostUSD: intentJevCostUSD(counts[intentJevStatTokens]),
			})
		}
	}
	if jev.BalanceSetAt != nil {
		balance := &IntentJevBalance{SetUSD: jev.BalanceUSD, SetAt: *jev.BalanceSetAt, LowBalanceUSD: *jev.LowBalanceUSD}
		if s.jevStats != nil {
			spent, err := s.jevStats.Spent(ctx, groupID)
			if err != nil {
				return nil, fmt.Errorf("read jev spending: %w", err)
			}
			balance.SpentTokens = spent
		}
		balance.SpentUSD = intentJevCostUSD(balance.SpentTokens)
		balance.EstimatedUSD = balance.SetUSD - balance.SpentUSD
		balance.Low = balance.EstimatedUSD < balance.LowBalanceUSD
		status.Balance = balance
	}
	return status, nil
}
