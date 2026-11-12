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
	"reflect"
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
// OpenAI HTTP paths (/v1/responses, /v1/chat/completions, /v1/messages). A
// matching rule turns an upstream HTTP 4xx the gateway would hand to the
// customer into an account switch. It only supplements the existing verdict:
// errors that already fail over never reach the table, cyber-policy and
// context-window errors are never switched, and the switch never touches the
// account's state.
//
// The stored JSON shares its key and rule shape with the gptniubi branch,
// which also keeps slot-wait fields this branch does not implement. Those
// fields, and anything else this code does not know, are kept on save.

const (
	// SettingKeyOpenAIFailoverPolicy holds the JSON OpenAIFailoverConfig.
	SettingKeyOpenAIFailoverPolicy = "openai_failover_policy"

	OpenAIFailoverActionFailover = "failover"
	OpenAIFailoverActionReturn   = "return"

	openAIFailoverDefaultMaxRuleSwitches = 3
	openAIFailoverMaxSwitchesLimit       = 10
	openAIFailoverMaxRules               = 100
	openAIFailoverMaxKeywordsPerRule     = 20
	openAIFailoverMatchBodyFallbackBytes = 2048

	// Values the gptniubi branch requires for its slot-wait fields; written
	// on a first save so that branch can load a config created here.
	openAIFailoverCompatSlotWaitMs      = 3000
	openAIFailoverCompatMaxSlotSwitches = 5

	openAIFailoverConfigTTL   = 30 * time.Second
	openAIFailoverStatsPrefix = "openai_failover:stats:"
	openAIFailoverRecentKey   = "openai_failover:recent"
	openAIFailoverRecentMax   = 300
	openAIFailoverStatsDays   = 7
	openAIFailoverStatsTTL    = 15 * 24 * time.Hour
)

// OpenAIFailoverRule decides one class of upstream HTTP 4xx. StatusCode 0
// matches any eligible status. Any keyword matches, case insensitive; a rule
// without keywords matches on its status alone.
type OpenAIFailoverRule struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Enabled    bool     `json:"enabled"`
	StatusCode int      `json:"status_code"`
	Keywords   []string `json:"keywords"`
	Action     string   `json:"action"`
}

// OpenAIFailoverConfig is the stored setting.
type OpenAIFailoverConfig struct {
	EnabledGroupIDs []int64              `json:"enabled_group_ids"`
	MaxRuleSwitches int                  `json:"max_rule_switches"`
	Rules           []OpenAIFailoverRule `json:"rules"`
}

func defaultOpenAIFailoverConfig() OpenAIFailoverConfig {
	return OpenAIFailoverConfig{
		EnabledGroupIDs: []int64{},
		MaxRuleSwitches: openAIFailoverDefaultMaxRuleSwitches,
		Rules:           []OpenAIFailoverRule{},
	}
}

// openAIFailoverStatusAlreadySwitches reports statuses the OpenAI gateway
// already moves to another account; a rule on them would never be reached.
func openAIFailoverStatusAlreadySwitches(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests
}

// openAIFailoverNormalizeMode selects how strict NormalizeOpenAIFailoverConfig is.
type openAIFailoverNormalizeMode struct {
	// lenient accepts what another branch may have stored (rules on statuses
	// that already switch accounts) instead of rejecting the whole table.
	lenient bool
	// unchanged reports a stored rule the admin did not edit; it is written
	// back as is even when the edit-time checks would reject it.
	unchanged func(OpenAIFailoverRule) bool
}

// NormalizeOpenAIFailoverConfig trims and validates a config for saving.
func NormalizeOpenAIFailoverConfig(cfg OpenAIFailoverConfig) (OpenAIFailoverConfig, error) {
	return normalizeOpenAIFailoverConfig(cfg, openAIFailoverNormalizeMode{})
}

func normalizeOpenAIFailoverConfig(cfg OpenAIFailoverConfig, mode openAIFailoverNormalizeMode) (OpenAIFailoverConfig, error) {
	invalid := func(format string, args ...any) (OpenAIFailoverConfig, error) {
		return cfg, infraerrors.BadRequest("OPENAI_FAILOVER_CONFIG_INVALID", fmt.Sprintf(format, args...))
	}
	if cfg.MaxRuleSwitches < 1 || cfg.MaxRuleSwitches > openAIFailoverMaxSwitchesLimit {
		if !mode.lenient {
			return invalid("max_rule_switches must be between 1 and %d", openAIFailoverMaxSwitchesLimit)
		}
		cfg.MaxRuleSwitches = openAIFailoverDefaultMaxRuleSwitches
	}
	if len(cfg.Rules) > openAIFailoverMaxRules {
		return invalid("at most %d rules", openAIFailoverMaxRules)
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
	rules := make([]OpenAIFailoverRule, 0, len(cfg.Rules))
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
		keywords := make([]string, 0, len(rule.Keywords))
		for _, keyword := range rule.Keywords {
			if keyword = strings.TrimSpace(keyword); keyword != "" {
				keywords = append(keywords, keyword)
			}
		}
		rule.Keywords = keywords
		if rule.StatusCode != 0 && (rule.StatusCode < 400 || rule.StatusCode > 499) {
			return invalid("rule %q: status_code must be empty or 4xx", rule.ID)
		}
		if len(keywords) == 0 && rule.StatusCode == 0 {
			return invalid("rule %q needs a status code or at least one keyword", rule.ID)
		}
		if len(keywords) > openAIFailoverMaxKeywordsPerRule {
			return invalid("rule %q: at most %d keywords", rule.ID, openAIFailoverMaxKeywordsPerRule)
		}
		if rule.Action != OpenAIFailoverActionFailover && rule.Action != OpenAIFailoverActionReturn {
			return invalid("rule %q: action must be failover or return", rule.ID)
		}
		if openAIFailoverStatusAlreadySwitches(rule.StatusCode) && !mode.lenient &&
			(mode.unchanged == nil || !mode.unchanged(rule)) {
			return invalid("rule %q: status %d already switches accounts", rule.ID, rule.StatusCode)
		}
		rules = append(rules, rule)
	}
	cfg.Rules = rules
	return cfg, nil
}

// OpenAIFailoverPolicy is the immutable hot-path copy shared by every request
// of the enabled groups.
type OpenAIFailoverPolicy struct {
	maxRuleSwitches int
	rules           []compiledOpenAIFailoverRule
}

type compiledOpenAIFailoverRule struct {
	id       string
	status   int
	keywords []string
	failover bool
}

func newOpenAIFailoverPolicy(cfg OpenAIFailoverConfig) *OpenAIFailoverPolicy {
	policy := &OpenAIFailoverPolicy{maxRuleSwitches: cfg.MaxRuleSwitches}
	for _, rule := range cfg.Rules {
		// Rules kept from another branch on statuses that already switch
		// accounts stay stored but never decide here.
		if !rule.Enabled || openAIFailoverStatusAlreadySwitches(rule.StatusCode) {
			continue
		}
		compiled := compiledOpenAIFailoverRule{
			id:       rule.ID,
			status:   rule.StatusCode,
			failover: rule.Action == OpenAIFailoverActionFailover,
		}
		for _, keyword := range rule.Keywords {
			compiled.keywords = append(compiled.keywords, strings.ToLower(keyword))
		}
		policy.rules = append(policy.rules, compiled)
	}
	return policy
}

// MaxRuleSwitches caps the account switches rules may trigger per request.
func (p *OpenAIFailoverPolicy) MaxRuleSwitches() int {
	if p == nil {
		return 0
	}
	return p.maxRuleSwitches
}

// match returns the ID of the first matching rule when it switches accounts.
// The first match decides, so a `return` rule shields an error from a broader
// failover rule below it.
func (p *OpenAIFailoverPolicy) match(statusCode int, text string) string {
	if p == nil {
		return ""
	}
	text = strings.ToLower(text)
	for i := range p.rules {
		rule := &p.rules[i]
		if rule.status != 0 && rule.status != statusCode {
			continue
		}
		hit := len(rule.keywords) == 0
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

type openAIFailoverPolicyContextKey struct{}

// WithOpenAIFailoverPolicy carries the group's policy to the forward path.
// A nil policy is stored too, so a fallback group overrides the previous one.
func WithOpenAIFailoverPolicy(ctx context.Context, policy *OpenAIFailoverPolicy) context.Context {
	return context.WithValue(ctx, openAIFailoverPolicyContextKey{}, policy)
}

// OpenAIFailoverPolicyFromContext returns the request's policy, nil when its
// group did not opt in.
func OpenAIFailoverPolicyFromContext(ctx context.Context) *OpenAIFailoverPolicy {
	if ctx == nil {
		return nil
	}
	policy, _ := ctx.Value(openAIFailoverPolicyContextKey{}).(*OpenAIFailoverPolicy)
	return policy
}

type openAIRuleReplayContextKey struct{}

// withOpenAIRuleReplay marks the request while the error a rule switch ended
// on is shown to the customer: account handling is skipped, the switch
// already decided nothing is wrong with the account.
func withOpenAIRuleReplay(ctx context.Context) context.Context {
	return context.WithValue(ctx, openAIRuleReplayContextKey{}, true)
}

func isOpenAIRuleReplay(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	replay, _ := ctx.Value(openAIRuleReplayContextKey{}).(bool)
	return replay
}

// openAIFailoverMatchText is what keywords are compared with: the structured
// error fields, because the rest of a body can echo request content. A body
// without them (HTML, plain text) falls back to its head.
func openAIFailoverMatchText(upstreamMsg string, body []byte) string {
	parts := []string{
		upstreamMsg,
		extractUpstreamErrorMessage(body),
		gjson.GetBytes(body, "error.type").String(),
		extractUpstreamErrorCode(body),
	}
	text := strings.TrimSpace(strings.Join(parts, " "))
	if text != "" {
		return text
	}
	if len(body) > openAIFailoverMatchBodyFallbackBytes {
		body = body[:openAIFailoverMatchBodyFallbackBytes]
	}
	return string(body)
}

// matchOpenAIFailoverRule decides an upstream HTTP 4xx the existing logic is
// about to hand to the customer. An empty result leaves it to that logic.
func matchOpenAIFailoverRule(c *gin.Context, statusCode int, upstreamMsg string, body []byte) string {
	if c == nil || c.Request == nil || statusCode < 400 || statusCode >= 500 {
		return ""
	}
	ctx := c.Request.Context()
	if isOpenAIRuleReplay(ctx) {
		return ""
	}
	policy := OpenAIFailoverPolicyFromContext(ctx)
	if policy == nil || len(policy.rules) == 0 {
		return ""
	}
	if hit, _, _ := detectOpenAICyberPolicy(body); hit {
		return ""
	}
	if isOpenAIContextWindowError(upstreamMsg, body) {
		return ""
	}
	return policy.match(statusCode, openAIFailoverMatchText(upstreamMsg, body))
}

// openAIRuleReplayFunc shows the customer an upstream error the way the path
// that received it would have without the rule table.
type openAIRuleReplayFunc func(c *gin.Context, resp *http.Response)

// tryOpenAIFailoverByRule returns an account switch for an upstream HTTP 4xx
// matching the group's rule table, nil otherwise. The account is neither
// cooled down nor retried; replay is how the error is shown if every switch
// fails.
func tryOpenAIFailoverByRule(c *gin.Context, account *Account, resp *http.Response, body []byte, upstreamMsg string, passthrough bool, replay openAIRuleReplayFunc) *UpstreamFailoverError {
	if resp == nil || account == nil {
		return nil
	}
	ruleID := matchOpenAIFailoverRule(c, resp.StatusCode, upstreamMsg, body)
	if ruleID == "" {
		return nil
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
		Message: upstreamMsg,
	})
	logger.LegacyPrintf("service.openai_gateway", "Account %d: upstream %d matched failover rule %s, switching account", account.ID, resp.StatusCode, ruleID)

	status := resp.StatusCode
	headers := resp.Header.Clone()
	bodyCopy := append([]byte(nil), body...)
	failoverErr := &UpstreamFailoverError{
		StatusCode:        status,
		ResponseBody:      bodyCopy,
		ResponseHeaders:   headers,
		NextAccountAction: NextAccountRetry,
		FailoverRuleID:    ruleID,
	}
	if replay != nil {
		failoverErr.RuleReplay = func(c *gin.Context) {
			replayResp := &http.Response{
				StatusCode: status,
				Header:     headers.Clone(),
				Body:       io.NopCloser(bytes.NewReader(bodyCopy)),
			}
			if c != nil && c.Request != nil {
				c.Request = c.Request.WithContext(withOpenAIRuleReplay(c.Request.Context()))
			}
			replay(c, replayResp)
		}
	}
	return failoverErr
}

// OpenAIFailoverOutcome is what one request did with rule switches.
type OpenAIFailoverOutcome struct {
	GroupID    int64
	UserID     int64
	APIKeyID   int64
	RuleID     string
	StatusCode int
	Switches   int
	Accounts   []int64
	Recovered  bool
	Canceled   bool
}

// OpenAIFailoverEvent is one row of the recent list.
type OpenAIFailoverEvent struct {
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

// OpenAIFailoverRuleStats counts one rule over the statistics window.
type OpenAIFailoverRuleStats struct {
	RuleID    string `json:"rule_id"`
	Hits      int64  `json:"hits"`
	Recovered int64  `json:"recovered"`
}

// OpenAIFailoverStats is the statistics part of the admin page.
type OpenAIFailoverStats struct {
	Rules      []OpenAIFailoverRuleStats `json:"rules"`
	WindowDays int                       `json:"window_days"`
}

// OpenAIFailoverOverview is the admin page payload.
type OpenAIFailoverOverview struct {
	Config OpenAIFailoverConfig  `json:"config"`
	Stats  OpenAIFailoverStats   `json:"stats"`
	Recent []OpenAIFailoverEvent `json:"recent"`
}

type openAIFailoverSnapshot struct {
	cfg      OpenAIFailoverConfig
	groups   map[int64]struct{}
	policy   *OpenAIFailoverPolicy
	loadedAt time.Time
}

// OpenAIFailoverPolicyService owns the setting and its statistics. A save
// applies on this instance at once; other instances pick it up when their
// cached copy expires (openAIFailoverConfigTTL).
type OpenAIFailoverPolicyService struct {
	settings SettingRepository
	rdb      *redis.Client

	snapshot  atomic.Pointer[openAIFailoverSnapshot]
	reloading atomic.Bool
	// publishMu orders snapshot publication: a save bumps version, so a refresh
	// that started reading before the save can not put the old rules back.
	publishMu sync.Mutex
	version   uint64
	writes    chan struct{}
}

// NewOpenAIFailoverPolicyService builds the service.
func NewOpenAIFailoverPolicyService(settings SettingRepository, rdb *redis.Client) *OpenAIFailoverPolicyService {
	return &OpenAIFailoverPolicyService{
		settings: settings,
		rdb:      rdb,
		writes:   make(chan struct{}, 64),
	}
}

// PolicyForGroup returns the policy when the group opted in, nil otherwise.
func (s *OpenAIFailoverPolicyService) PolicyForGroup(ctx context.Context, groupID *int64) *OpenAIFailoverPolicy {
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

func (s *OpenAIFailoverPolicyService) currentSnapshot(ctx context.Context) *openAIFailoverSnapshot {
	snapshot := s.snapshot.Load()
	if snapshot == nil {
		return s.reload(ctx)
	}
	if time.Since(snapshot.loadedAt) >= openAIFailoverConfigTTL && s.reloading.CompareAndSwap(false, true) {
		go func() {
			defer s.reloading.Store(false)
			reloadCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s.reload(reloadCtx)
		}()
	}
	return snapshot
}

func (s *OpenAIFailoverPolicyService) currentVersion() uint64 {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	return s.version
}

// reload keeps the previous snapshot when the setting can not be read, and
// drops its result when a save was published meanwhile.
func (s *OpenAIFailoverPolicyService) reload(ctx context.Context) *openAIFailoverSnapshot {
	startVersion := s.currentVersion()
	cfg, err := s.loadConfig(ctx)

	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	previous := s.snapshot.Load()
	if s.version != startVersion && previous != nil {
		return previous
	}
	if err != nil {
		slog.Warn("openai_failover.config_load_failed", "error", err)
		if previous != nil {
			refreshed := *previous
			refreshed.loadedAt = time.Now()
			s.snapshot.Store(&refreshed)
			return &refreshed
		}
		cfg = defaultOpenAIFailoverConfig()
	}
	return s.storeLocked(cfg)
}

func (s *OpenAIFailoverPolicyService) storeLocked(cfg OpenAIFailoverConfig) *openAIFailoverSnapshot {
	snapshot := &openAIFailoverSnapshot{
		cfg:      cfg,
		groups:   make(map[int64]struct{}, len(cfg.EnabledGroupIDs)),
		policy:   newOpenAIFailoverPolicy(cfg),
		loadedAt: time.Now(),
	}
	for _, id := range cfg.EnabledGroupIDs {
		snapshot.groups[id] = struct{}{}
	}
	s.snapshot.Store(snapshot)
	return snapshot
}

// loadRaw returns the stored JSON, "" when nothing was saved.
func (s *OpenAIFailoverPolicyService) loadRaw(ctx context.Context) (string, error) {
	raw, err := s.settings.GetValue(ctx, SettingKeyOpenAIFailoverPolicy)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(raw), nil
}

func parseStoredOpenAIFailoverConfig(raw string) (OpenAIFailoverConfig, error) {
	if raw == "" {
		return defaultOpenAIFailoverConfig(), nil
	}
	var cfg OpenAIFailoverConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return OpenAIFailoverConfig{}, fmt.Errorf("parse %s: %w", SettingKeyOpenAIFailoverPolicy, err)
	}
	if cfg.EnabledGroupIDs == nil {
		cfg.EnabledGroupIDs = []int64{}
	}
	if cfg.Rules == nil {
		cfg.Rules = []OpenAIFailoverRule{}
	}
	return normalizeOpenAIFailoverConfig(cfg, openAIFailoverNormalizeMode{lenient: true})
}

func (s *OpenAIFailoverPolicyService) loadConfig(ctx context.Context) (OpenAIFailoverConfig, error) {
	raw, err := s.loadRaw(ctx)
	if err != nil {
		return OpenAIFailoverConfig{}, err
	}
	return parseStoredOpenAIFailoverConfig(raw)
}

// Config returns the stored setting, the defaults when nothing was saved.
func (s *OpenAIFailoverPolicyService) Config(ctx context.Context) (OpenAIFailoverConfig, error) {
	return s.loadConfig(ctx)
}

// UpdateConfig validates, saves and applies the setting on this instance.
func (s *OpenAIFailoverPolicyService) UpdateConfig(ctx context.Context, cfg OpenAIFailoverConfig) (OpenAIFailoverConfig, error) {
	raw, err := s.loadRaw(ctx)
	if err != nil {
		return cfg, err
	}
	stored := map[string]OpenAIFailoverRule{}
	if existing, parseErr := parseStoredOpenAIFailoverConfig(raw); parseErr == nil {
		for _, rule := range existing.Rules {
			stored[rule.ID] = rule
		}
	}
	cfg, err = normalizeOpenAIFailoverConfig(cfg, openAIFailoverNormalizeMode{
		unchanged: func(rule OpenAIFailoverRule) bool {
			old, ok := stored[rule.ID]
			return ok && reflect.DeepEqual(old, rule)
		},
	})
	if err != nil {
		return cfg, err
	}
	merged, err := mergeOpenAIFailoverConfigJSON(raw, cfg)
	if err != nil {
		return cfg, err
	}
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	if err := s.settings.Set(ctx, SettingKeyOpenAIFailoverPolicy, merged); err != nil {
		return cfg, err
	}
	s.version++
	s.storeLocked(cfg)
	return cfg, nil
}

// mergeOpenAIFailoverConfigJSON writes cfg over the stored JSON, keeping every
// field this branch does not know, top level and per rule (matched by id).
func mergeOpenAIFailoverConfigJSON(raw string, cfg OpenAIFailoverConfig) (string, error) {
	top := map[string]json.RawMessage{}
	storedRules := map[string]map[string]json.RawMessage{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &top); err != nil {
			top = map[string]json.RawMessage{}
		}
		var rules []map[string]json.RawMessage
		if rawRules, ok := top["rules"]; ok && json.Unmarshal(rawRules, &rules) == nil {
			for _, rule := range rules {
				var id string
				if json.Unmarshal(rule["id"], &id) == nil && id != "" {
					storedRules[id] = rule
				}
			}
		}
	}
	if top == nil {
		top = map[string]json.RawMessage{}
	}
	set := func(m map[string]json.RawMessage, key string, value any) error {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		m[key] = encoded
		return nil
	}
	if err := set(top, "enabled_group_ids", cfg.EnabledGroupIDs); err != nil {
		return "", err
	}
	if err := set(top, "max_rule_switches", cfg.MaxRuleSwitches); err != nil {
		return "", err
	}
	if _, ok := top["slot_wait_ms"]; !ok {
		if err := set(top, "slot_wait_ms", openAIFailoverCompatSlotWaitMs); err != nil {
			return "", err
		}
	}
	if _, ok := top["max_slot_switches"]; !ok {
		if err := set(top, "max_slot_switches", openAIFailoverCompatMaxSlotSwitches); err != nil {
			return "", err
		}
	}
	rules := make([]map[string]json.RawMessage, 0, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		entry := map[string]json.RawMessage{}
		for key, value := range storedRules[rule.ID] {
			entry[key] = value
		}
		for key, value := range map[string]any{
			"id":          rule.ID,
			"name":        rule.Name,
			"enabled":     rule.Enabled,
			"status_code": rule.StatusCode,
			"keywords":    rule.Keywords,
			"action":      rule.Action,
		} {
			if err := set(entry, key, value); err != nil {
				return "", err
			}
		}
		rules = append(rules, entry)
	}
	if err := set(top, "rules", rules); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(top)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// RecordOutcome counts one request. It never blocks the request: with Redis
// slow, records are dropped rather than queued.
func (s *OpenAIFailoverPolicyService) RecordOutcome(outcome OpenAIFailoverOutcome) {
	if s == nil || s.rdb == nil || outcome.RuleID == "" {
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

func openAIFailoverStatsKey(day time.Time) string {
	return openAIFailoverStatsPrefix + day.Format("2006-01-02")
}

func openAIFailoverStatsFields(ruleID string) (hit, recovered string) {
	return "rule:" + ruleID + ":hit", "rule:" + ruleID + ":recovered"
}

func (s *OpenAIFailoverPolicyService) writeOutcome(ctx context.Context, outcome OpenAIFailoverOutcome) {
	now := time.Now()
	key := openAIFailoverStatsKey(now)
	hitField, recoveredField := openAIFailoverStatsFields(outcome.RuleID)
	pipe := s.rdb.Pipeline()
	pipe.HIncrBy(ctx, key, hitField, 1)
	if outcome.Recovered {
		pipe.HIncrBy(ctx, key, recoveredField, 1)
	}
	pipe.Expire(ctx, key, openAIFailoverStatsTTL)
	if raw, err := json.Marshal(OpenAIFailoverEvent{
		Time:       now,
		GroupID:    outcome.GroupID,
		UserID:     outcome.UserID,
		APIKeyID:   outcome.APIKeyID,
		Reason:     outcome.RuleID,
		StatusCode: outcome.StatusCode,
		Switches:   outcome.Switches,
		Accounts:   outcome.Accounts,
		Recovered:  outcome.Recovered,
		Canceled:   outcome.Canceled,
	}); err == nil {
		pipe.LPush(ctx, openAIFailoverRecentKey, raw)
		pipe.LTrim(ctx, openAIFailoverRecentKey, 0, openAIFailoverRecentMax-1)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Warn("openai_failover.record_failed", "error", err)
	}
}

// Overview assembles the admin page.
func (s *OpenAIFailoverPolicyService) Overview(ctx context.Context) (*OpenAIFailoverOverview, error) {
	cfg, err := s.Config(ctx)
	if err != nil {
		// A broken stored value must not lock the admin out of fixing it.
		slog.Warn("openai_failover.config_invalid", "error", err)
		cfg = defaultOpenAIFailoverConfig()
	}
	overview := &OpenAIFailoverOverview{
		Config: cfg,
		Stats:  OpenAIFailoverStats{Rules: []OpenAIFailoverRuleStats{}, WindowDays: openAIFailoverStatsDays},
		Recent: []OpenAIFailoverEvent{},
	}
	if s.rdb == nil {
		return overview, nil
	}

	totals := make(map[string]int64)
	now := time.Now()
	for i := 0; i < openAIFailoverStatsDays; i++ {
		values, err := s.rdb.HGetAll(ctx, openAIFailoverStatsKey(now.AddDate(0, 0, -i))).Result()
		if err != nil {
			return nil, err
		}
		for field, value := range values {
			n, _ := strconv.ParseInt(value, 10, 64)
			totals[field] += n
		}
	}
	for _, rule := range cfg.Rules {
		hitField, recoveredField := openAIFailoverStatsFields(rule.ID)
		overview.Stats.Rules = append(overview.Stats.Rules, OpenAIFailoverRuleStats{
			RuleID:    rule.ID,
			Hits:      totals[hitField],
			Recovered: totals[recoveredField],
		})
	}

	rows, err := s.rdb.LRange(ctx, openAIFailoverRecentKey, 0, openAIFailoverRecentMax-1).Result()
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		var event OpenAIFailoverEvent
		if json.Unmarshal([]byte(row), &event) == nil {
			overview.Recent = append(overview.Recent, event)
		}
	}
	return overview, nil
}
