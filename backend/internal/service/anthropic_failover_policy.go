package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/tidwall/gjson"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// A group that opts in gets an ordered status + keyword rule table on the
// Anthropic /v1/messages paths. A matching rule turns an upstream HTTP 4xx the
// gateway would hand to the customer into an account switch. It only
// supplements the existing verdict: statuses that already fail over never
// reach the table, and the switch never touches the account's state.

const (
	// SettingKeyAnthropicFailoverPolicy holds the JSON AnthropicFailoverConfig.
	SettingKeyAnthropicFailoverPolicy = "anthropic_failover_policy"

	AnthropicFailoverActionFailover = "failover"
	AnthropicFailoverActionReturn   = "return"

	// AnthropicFailoverReasonStream is the fixed statistics reason for a switch
	// caused by an in-stream error before the first real upstream event.
	AnthropicFailoverReasonStream = "stream_before_first_event"

	anthropicFailoverDefaultMaxRuleSwitches = 3
	anthropicFailoverMaxSwitchesLimit       = 10
	anthropicFailoverMaxRules               = 100
	anthropicFailoverMaxKeywordsPerRule     = 20
	anthropicFailoverMatchBodyFallbackBytes = 2048

	anthropicFailoverConfigTTL   = 30 * time.Second
	anthropicFailoverStatsPrefix = "anthropic_failover:stats:"
	anthropicFailoverRecentKey   = "anthropic_failover:recent"
	anthropicFailoverRecentMax   = 300
	anthropicFailoverStatsDays   = 7
	anthropicFailoverStatsTTL    = 15 * 24 * time.Hour
)

// AnthropicFailoverRule decides one class of upstream HTTP 4xx. StatusCode 0
// matches any eligible status. Any keyword matches, case insensitive.
type AnthropicFailoverRule struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Enabled    bool     `json:"enabled"`
	StatusCode int      `json:"status_code"`
	Keywords   []string `json:"keywords"`
	Action     string   `json:"action"`
}

// AnthropicFailoverConfig is the stored setting.
type AnthropicFailoverConfig struct {
	EnabledGroupIDs []int64                 `json:"enabled_group_ids"`
	MaxRuleSwitches int                     `json:"max_rule_switches"`
	Rules           []AnthropicFailoverRule `json:"rules"`
}

func defaultAnthropicFailoverConfig() AnthropicFailoverConfig {
	return AnthropicFailoverConfig{
		EnabledGroupIDs: []int64{},
		MaxRuleSwitches: anthropicFailoverDefaultMaxRuleSwitches,
		Rules:           []AnthropicFailoverRule{},
	}
}

// NormalizeAnthropicFailoverConfig trims and validates a config.
func NormalizeAnthropicFailoverConfig(cfg AnthropicFailoverConfig) (AnthropicFailoverConfig, error) {
	invalid := func(format string, args ...any) (AnthropicFailoverConfig, error) {
		return cfg, infraerrors.BadRequest("ANTHROPIC_FAILOVER_CONFIG_INVALID", fmt.Sprintf(format, args...))
	}
	if cfg.MaxRuleSwitches < 1 || cfg.MaxRuleSwitches > anthropicFailoverMaxSwitchesLimit {
		return invalid("max_rule_switches must be between 1 and %d", anthropicFailoverMaxSwitchesLimit)
	}
	if len(cfg.Rules) > anthropicFailoverMaxRules {
		return invalid("at most %d rules", anthropicFailoverMaxRules)
	}

	groups := make([]int64, 0, len(cfg.EnabledGroupIDs))
	seenGroups := make(map[int64]struct{}, len(cfg.EnabledGroupIDs))
	for _, id := range cfg.EnabledGroupIDs {
		if id <= 0 {
			return invalid("invalid group id %d", id)
		}
		if _, dup := seenGroups[id]; dup {
			continue
		}
		seenGroups[id] = struct{}{}
		groups = append(groups, id)
	}
	cfg.EnabledGroupIDs = groups

	seenRules := make(map[string]struct{}, len(cfg.Rules))
	rules := make([]AnthropicFailoverRule, 0, len(cfg.Rules))
	for i, rule := range cfg.Rules {
		rule.ID = strings.TrimSpace(rule.ID)
		if rule.ID == "" {
			rule.ID = "rule-" + strconv.FormatInt(time.Now().UnixNano()+int64(i), 36)
		}
		if strings.Contains(rule.ID, ":") {
			return invalid("rule id %q must not contain ':'", rule.ID)
		}
		if _, dup := seenRules[rule.ID]; dup {
			return invalid("duplicate rule id %q", rule.ID)
		}
		seenRules[rule.ID] = struct{}{}
		rule.Name = strings.TrimSpace(rule.Name)
		if rule.StatusCode != 0 {
			if rule.StatusCode < 400 || rule.StatusCode > 499 {
				return invalid("rule %q: status_code must be empty or 4xx", rule.ID)
			}
			if rule.StatusCode == 401 || rule.StatusCode == 403 || rule.StatusCode == 429 {
				return invalid("rule %q: status %d already switches accounts", rule.ID, rule.StatusCode)
			}
		}
		keywords := make([]string, 0, len(rule.Keywords))
		for _, keyword := range rule.Keywords {
			if keyword = strings.TrimSpace(keyword); keyword != "" {
				keywords = append(keywords, keyword)
			}
		}
		if len(keywords) == 0 {
			return invalid("rule %q needs at least one keyword", rule.ID)
		}
		if len(keywords) > anthropicFailoverMaxKeywordsPerRule {
			return invalid("rule %q: at most %d keywords", rule.ID, anthropicFailoverMaxKeywordsPerRule)
		}
		rule.Keywords = keywords
		if rule.Action != AnthropicFailoverActionFailover && rule.Action != AnthropicFailoverActionReturn {
			return invalid("rule %q: action must be failover or return", rule.ID)
		}
		rules = append(rules, rule)
	}
	cfg.Rules = rules
	return cfg, nil
}

// AnthropicFailoverPolicy is the immutable hot-path copy shared by every
// request of the enabled groups.
type AnthropicFailoverPolicy struct {
	maxRuleSwitches int
	rules           []compiledAnthropicFailoverRule
}

type compiledAnthropicFailoverRule struct {
	id       string
	status   int
	keywords []string
	failover bool
}

func newAnthropicFailoverPolicy(cfg AnthropicFailoverConfig) *AnthropicFailoverPolicy {
	policy := &AnthropicFailoverPolicy{maxRuleSwitches: cfg.MaxRuleSwitches}
	for _, rule := range cfg.Rules {
		if !rule.Enabled {
			continue
		}
		compiled := compiledAnthropicFailoverRule{
			id:       rule.ID,
			status:   rule.StatusCode,
			failover: rule.Action == AnthropicFailoverActionFailover,
		}
		for _, keyword := range rule.Keywords {
			compiled.keywords = append(compiled.keywords, strings.ToLower(keyword))
		}
		policy.rules = append(policy.rules, compiled)
	}
	return policy
}

// MaxRuleSwitches caps the account switches rules may trigger per request.
func (p *AnthropicFailoverPolicy) MaxRuleSwitches() int {
	if p == nil {
		return 0
	}
	return p.maxRuleSwitches
}

// match returns the ID of the first matching rule when it switches accounts.
// The first match decides, so a `return` rule shields an error from a broader
// failover rule below it.
func (p *AnthropicFailoverPolicy) match(statusCode int, text string) string {
	if p == nil {
		return ""
	}
	text = strings.ToLower(text)
	for i := range p.rules {
		rule := &p.rules[i]
		if rule.status != 0 && rule.status != statusCode {
			continue
		}
		hit := false
		for _, keyword := range rule.keywords {
			if strings.Contains(text, keyword) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		if rule.failover {
			return rule.id
		}
		return ""
	}
	return ""
}

type anthropicFailoverPolicyContextKey struct{}

// WithAnthropicFailoverPolicy carries the group's policy to the forward path.
// A nil policy is stored too, so a fallback group overrides the previous one.
func WithAnthropicFailoverPolicy(ctx context.Context, policy *AnthropicFailoverPolicy) context.Context {
	return context.WithValue(ctx, anthropicFailoverPolicyContextKey{}, policy)
}

// AnthropicFailoverPolicyFromContext returns the request's policy, nil when
// its group did not opt in.
func AnthropicFailoverPolicyFromContext(ctx context.Context) *AnthropicFailoverPolicy {
	if ctx == nil {
		return nil
	}
	policy, _ := ctx.Value(anthropicFailoverPolicyContextKey{}).(*AnthropicFailoverPolicy)
	return policy
}

// anthropicFailoverMatchText is what keywords are compared with: the structured
// error fields, because the rest of a body can echo request content. A body
// without them (HTML, plain text) falls back to its head.
func anthropicFailoverMatchText(body []byte) string {
	parts := []string{
		extractUpstreamErrorMessage(body),
		gjson.GetBytes(body, "error.type").String(),
		extractUpstreamErrorCode(body),
	}
	text := strings.TrimSpace(strings.Join(parts, " "))
	if text != "" {
		return text
	}
	if len(body) > anthropicFailoverMatchBodyFallbackBytes {
		body = body[:anthropicFailoverMatchBodyFallbackBytes]
	}
	return string(body)
}

// tryFailoverByRule turns an upstream HTTP 4xx the gateway would return to the
// customer into an account switch when the group's rule table says so. The
// account is neither cooled down nor retried.
func (s *GatewayService) tryFailoverByRule(ctx context.Context, c *gin.Context, resp *http.Response, account *Account, retryExhausted, passthrough bool) *UpstreamFailoverError {
	if resp == nil || resp.StatusCode < 400 || resp.StatusCode >= 500 || s.shouldFailoverUpstreamError(resp.StatusCode) {
		return nil
	}
	policy := AnthropicFailoverPolicyFromContext(ctx)
	if policy == nil || len(policy.rules) == 0 {
		return nil
	}
	respBody, readErr := s.readUpstreamErrorBody(resp)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(respBody))
	if readErr != nil {
		return nil
	}
	ruleID := policy.match(resp.StatusCode, anthropicFailoverMatchText(respBody))
	if ruleID == "" {
		return nil
	}

	upstreamDetail := ""
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = truncateString(string(respBody), maxBytes)
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:            opsUpstreamProxyID(account),
		ProxyName:          opsUpstreamProxyName(account),
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  resp.Header.Get("x-request-id"),
		Passthrough:        passthrough,
		// The ops failover counters key on the part before the first colon.
		Kind:    "failover:rule:" + ruleID,
		Message: sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(respBody))),
		Detail:  upstreamDetail,
	})
	logger.LegacyPrintf("service.gateway", "Account %d: upstream %d matched failover rule %s, switching account", account.ID, resp.StatusCode, ruleID)

	return &UpstreamFailoverError{
		StatusCode:         resp.StatusCode,
		ResponseBody:       respBody,
		FailoverRuleID:     ruleID,
		RuleRetryExhausted: retryExhausted,
	}
}

// WriteRuleFailoverExhausted shows the customer the error a rule switch ended
// on exactly as it would have looked without the rule table.
func (s *GatewayService) WriteRuleFailoverExhausted(c *gin.Context, platform string, failoverErr *UpstreamFailoverError) {
	if failoverErr == nil {
		return
	}
	upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(failoverErr.ResponseBody)))
	setOpsUpstreamError(c, failoverErr.StatusCode, upstreamMsg, "")
	presentUpstreamHTTPError(c, platform, failoverErr.StatusCode, failoverErr.ResponseBody, failoverErr.RuleRetryExhausted)
}

// AnthropicFailoverOutcome is what one request did with account switching.
// Reason is the first cause only: a rule ID or AnthropicFailoverReasonStream.
type AnthropicFailoverOutcome struct {
	GroupID    int64
	UserID     int64
	APIKeyID   int64
	Reason     string
	StatusCode int
	Switches   int
	Accounts   []int64
	Recovered  bool
	Canceled   bool
}

// AnthropicFailoverEvent is one row of the recent list.
type AnthropicFailoverEvent struct {
	Time       time.Time `json:"time"`
	GroupID    int64     `json:"group_id"`
	UserID     int64     `json:"user_id"`
	APIKeyID   int64     `json:"api_key_id"`
	Reason     string    `json:"reason"`
	StatusCode int       `json:"status_code"`
	Switches   int       `json:"switches"`
	Accounts   []int64   `json:"accounts"`
	Recovered  bool      `json:"recovered"`
	Canceled   bool      `json:"canceled"`
}

// AnthropicFailoverRuleStats counts one rule over the statistics window.
type AnthropicFailoverRuleStats struct {
	RuleID    string `json:"rule_id"`
	Hits      int64  `json:"hits"`
	Recovered int64  `json:"recovered"`
}

// AnthropicFailoverStats is the statistics part of the admin page.
type AnthropicFailoverStats struct {
	Rules           []AnthropicFailoverRuleStats `json:"rules"`
	StreamHits      int64                        `json:"stream_hits"`
	StreamRecovered int64                        `json:"stream_recovered"`
	WindowDays      int                          `json:"window_days"`
}

// AnthropicFailoverOverview is the admin page payload.
type AnthropicFailoverOverview struct {
	Config AnthropicFailoverConfig  `json:"config"`
	Stats  AnthropicFailoverStats   `json:"stats"`
	Recent []AnthropicFailoverEvent `json:"recent"`
}

type anthropicFailoverSnapshot struct {
	cfg      AnthropicFailoverConfig
	groups   map[int64]struct{}
	policy   *AnthropicFailoverPolicy
	loadedAt time.Time
}

// AnthropicFailoverPolicyService owns the setting and its statistics.
type AnthropicFailoverPolicyService struct {
	settings SettingRepository
	rdb      *redis.Client

	snapshot  atomic.Pointer[anthropicFailoverSnapshot]
	reloading atomic.Bool
	// publishMu orders snapshot publication: a save bumps version, so a refresh
	// that started reading before the save can not put the old rules back.
	publishMu sync.Mutex
	version   uint64
	writes    chan struct{}
}

// NewAnthropicFailoverPolicyService builds the service.
func NewAnthropicFailoverPolicyService(settings SettingRepository, rdb *redis.Client) *AnthropicFailoverPolicyService {
	return &AnthropicFailoverPolicyService{
		settings: settings,
		rdb:      rdb,
		writes:   make(chan struct{}, 64),
	}
}

// PolicyForGroup returns the policy when the group opted in, nil otherwise.
func (s *AnthropicFailoverPolicyService) PolicyForGroup(ctx context.Context, groupID *int64) *AnthropicFailoverPolicy {
	if s == nil || s.settings == nil || groupID == nil {
		return nil
	}
	snapshot := s.currentSnapshot(ctx)
	if snapshot == nil {
		return nil
	}
	if _, enabled := snapshot.groups[*groupID]; !enabled {
		return nil
	}
	return snapshot.policy
}

func (s *AnthropicFailoverPolicyService) currentSnapshot(ctx context.Context) *anthropicFailoverSnapshot {
	snapshot := s.snapshot.Load()
	if snapshot == nil {
		return s.reload(ctx)
	}
	if time.Since(snapshot.loadedAt) >= anthropicFailoverConfigTTL && s.reloading.CompareAndSwap(false, true) {
		go func() {
			defer s.reloading.Store(false)
			reloadCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s.reload(reloadCtx)
		}()
	}
	return snapshot
}

func (s *AnthropicFailoverPolicyService) currentVersion() uint64 {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	return s.version
}

// reload keeps the previous snapshot when the setting can not be read, and
// drops its result when a save was published meanwhile.
func (s *AnthropicFailoverPolicyService) reload(ctx context.Context) *anthropicFailoverSnapshot {
	startVersion := s.currentVersion()
	cfg, err := s.loadConfig(ctx)

	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	previous := s.snapshot.Load()
	if s.version != startVersion && previous != nil {
		return previous
	}
	if err != nil {
		slog.Warn("anthropic_failover.config_load_failed", "error", err)
		if previous != nil {
			refreshed := *previous
			refreshed.loadedAt = time.Now()
			s.snapshot.Store(&refreshed)
			return &refreshed
		}
		cfg = defaultAnthropicFailoverConfig()
	}
	return s.storeLocked(cfg)
}

func (s *AnthropicFailoverPolicyService) storeLocked(cfg AnthropicFailoverConfig) *anthropicFailoverSnapshot {
	snapshot := &anthropicFailoverSnapshot{
		cfg:      cfg,
		groups:   make(map[int64]struct{}, len(cfg.EnabledGroupIDs)),
		policy:   newAnthropicFailoverPolicy(cfg),
		loadedAt: time.Now(),
	}
	for _, id := range cfg.EnabledGroupIDs {
		snapshot.groups[id] = struct{}{}
	}
	s.snapshot.Store(snapshot)
	return snapshot
}

func (s *AnthropicFailoverPolicyService) loadConfig(ctx context.Context) (AnthropicFailoverConfig, error) {
	raw, err := s.settings.GetValue(ctx, SettingKeyAnthropicFailoverPolicy)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return defaultAnthropicFailoverConfig(), nil
		}
		return AnthropicFailoverConfig{}, err
	}
	if strings.TrimSpace(raw) == "" {
		return defaultAnthropicFailoverConfig(), nil
	}
	var cfg AnthropicFailoverConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return AnthropicFailoverConfig{}, fmt.Errorf("parse %s: %w", SettingKeyAnthropicFailoverPolicy, err)
	}
	return NormalizeAnthropicFailoverConfig(cfg)
}

// Config returns the stored setting, the defaults when nothing was saved.
func (s *AnthropicFailoverPolicyService) Config(ctx context.Context) (AnthropicFailoverConfig, error) {
	return s.loadConfig(ctx)
}

// UpdateConfig validates, saves and applies the setting on this instance.
func (s *AnthropicFailoverPolicyService) UpdateConfig(ctx context.Context, cfg AnthropicFailoverConfig) (AnthropicFailoverConfig, error) {
	cfg, err := NormalizeAnthropicFailoverConfig(cfg)
	if err != nil {
		return cfg, err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	if err := s.settings.Set(ctx, SettingKeyAnthropicFailoverPolicy, string(raw)); err != nil {
		return cfg, err
	}
	s.version++
	s.storeLocked(cfg)
	return cfg, nil
}

// RecordOutcome counts one request. It never blocks the request: with Redis
// slow, records are dropped rather than queued.
func (s *AnthropicFailoverPolicyService) RecordOutcome(outcome AnthropicFailoverOutcome) {
	if s == nil || s.rdb == nil || outcome.Reason == "" {
		return
	}
	select {
	case s.writes <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-s.writes }()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.writeOutcome(ctx, outcome)
	}()
}

func anthropicFailoverStatsKey(day time.Time) string {
	return anthropicFailoverStatsPrefix + day.Format("2006-01-02")
}

func anthropicFailoverStatsFields(reason string) (hit, recovered string) {
	if reason == AnthropicFailoverReasonStream {
		return "stream:hit", "stream:recovered"
	}
	return "rule:" + reason + ":hit", "rule:" + reason + ":recovered"
}

func (s *AnthropicFailoverPolicyService) writeOutcome(ctx context.Context, outcome AnthropicFailoverOutcome) {
	now := time.Now()
	key := anthropicFailoverStatsKey(now)
	hitField, recoveredField := anthropicFailoverStatsFields(outcome.Reason)
	pipe := s.rdb.Pipeline()
	pipe.HIncrBy(ctx, key, hitField, 1)
	if outcome.Recovered {
		pipe.HIncrBy(ctx, key, recoveredField, 1)
	}
	pipe.Expire(ctx, key, anthropicFailoverStatsTTL)
	if raw, err := json.Marshal(AnthropicFailoverEvent{
		Time:       now,
		GroupID:    outcome.GroupID,
		UserID:     outcome.UserID,
		APIKeyID:   outcome.APIKeyID,
		Reason:     outcome.Reason,
		StatusCode: outcome.StatusCode,
		Switches:   outcome.Switches,
		Accounts:   outcome.Accounts,
		Recovered:  outcome.Recovered,
		Canceled:   outcome.Canceled,
	}); err == nil {
		pipe.LPush(ctx, anthropicFailoverRecentKey, raw)
		pipe.LTrim(ctx, anthropicFailoverRecentKey, 0, anthropicFailoverRecentMax-1)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Warn("anthropic_failover.record_failed", "error", err)
	}
}

// Overview assembles the admin page.
func (s *AnthropicFailoverPolicyService) Overview(ctx context.Context) (*AnthropicFailoverOverview, error) {
	cfg, err := s.Config(ctx)
	if err != nil {
		// A broken stored value must not lock the admin out of fixing it.
		slog.Warn("anthropic_failover.config_invalid", "error", err)
		cfg = defaultAnthropicFailoverConfig()
	}
	overview := &AnthropicFailoverOverview{
		Config: cfg,
		Stats:  AnthropicFailoverStats{Rules: []AnthropicFailoverRuleStats{}, WindowDays: anthropicFailoverStatsDays},
		Recent: []AnthropicFailoverEvent{},
	}
	if s.rdb == nil {
		return overview, nil
	}

	totals := make(map[string]int64)
	now := time.Now()
	for i := 0; i < anthropicFailoverStatsDays; i++ {
		values, err := s.rdb.HGetAll(ctx, anthropicFailoverStatsKey(now.AddDate(0, 0, -i))).Result()
		if err != nil {
			return nil, err
		}
		for field, value := range values {
			n, _ := strconv.ParseInt(value, 10, 64)
			totals[field] += n
		}
	}
	overview.Stats.StreamHits = totals["stream:hit"]
	overview.Stats.StreamRecovered = totals["stream:recovered"]
	for _, rule := range cfg.Rules {
		hitField, recoveredField := anthropicFailoverStatsFields(rule.ID)
		overview.Stats.Rules = append(overview.Stats.Rules, AnthropicFailoverRuleStats{
			RuleID:    rule.ID,
			Hits:      totals[hitField],
			Recovered: totals[recoveredField],
		})
	}

	rows, err := s.rdb.LRange(ctx, anthropicFailoverRecentKey, 0, anthropicFailoverRecentMax-1).Result()
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		var event AnthropicFailoverEvent
		if json.Unmarshal([]byte(row), &event) == nil {
			overview.Recent = append(overview.Recent, event)
		}
	}
	return overview, nil
}
