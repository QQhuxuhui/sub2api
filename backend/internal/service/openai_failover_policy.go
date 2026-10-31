package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// A group that opts in keeps errors away from its downstream customers on the
// /v1/responses HTTP path:
//
//   - an ordered status + keyword rule table turns errors the platform would
//     return into account switches. It only supplements the existing verdict:
//     what already fails over stays as is, what is deliberately request-scoped
//     (cyber policy, context window) can not be overridden;
//   - waiting for a busy account's slot ends early with a switch instead of an
//     error;
//   - plaintext left in agent_message encrypted_content items is rewritten
//     before the first upstream attempt.
//
// Accounts marked as not supporting encrypted tool arguments are a separate,
// site-wide scheduling filter: see OpenAINoEncryptedArgsExtraKey.

const (
	// SettingKeyOpenAIFailoverPolicy holds the JSON OpenAIFailoverConfig.
	SettingKeyOpenAIFailoverPolicy = "openai_failover_policy"

	// OpenAINoEncryptedArgsExtraKey marks an upstream that answers tool
	// parameters declared `"encrypted": true` in plaintext. Codex stores what it
	// gets as encrypted_content, which poisons the conversation for every
	// upstream that really decrypts it.
	OpenAINoEncryptedArgsExtraKey = "openai_no_encrypted_args"

	OpenAIFailoverActionFailover = "failover"
	OpenAIFailoverActionReturn   = "return"

	openAIFailoverDefaultSlotWaitMs      = 3000
	openAIFailoverDefaultMaxRuleSwitches = 3
	openAIFailoverDefaultMaxSlotSwitches = 5
	openAIFailoverMinSlotWaitMs          = 500
	openAIFailoverMaxSlotWaitMs          = 10000
	openAIFailoverMaxSwitchesLimit       = 10
	openAIFailoverMaxRules               = 100
	openAIFailoverMaxKeywordsPerRule     = 20

	openAIFailoverConfigTTL   = 30 * time.Second
	openAIFailoverStatsPrefix = "openai_failover:stats:"
	openAIFailoverRecentKey   = "openai_failover:recent"
	openAIFailoverRecentMax   = 300
	openAIFailoverStatsDays   = 7
	openAIFailoverStatsTTL    = 15 * 24 * time.Hour

	// Fernet tokens — what the upstream hands out — always start with this.
	openAIEncryptedContentPrefix = "gAAAA"
)

// OpenAIFailoverRule decides one class of upstream error. StatusCode 0 matches
// any error, including failures delivered inside an HTTP 200 stream; a set
// status matches HTTP errors only. Any keyword of Keywords matches (case
// insensitive); no keywords means the status alone decides.
type OpenAIFailoverRule struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Enabled    bool     `json:"enabled"`
	StatusCode int      `json:"status_code"`
	Keywords   []string `json:"keywords"`
	Action     string   `json:"action"`
}

// OpenAIFailoverConfig is the site-wide setting.
type OpenAIFailoverConfig struct {
	EnabledGroupIDs []int64              `json:"enabled_group_ids"`
	SlotWaitMs      int                  `json:"slot_wait_ms"`
	MaxRuleSwitches int                  `json:"max_rule_switches"`
	MaxSlotSwitches int                  `json:"max_slot_switches"`
	Rules           []OpenAIFailoverRule `json:"rules"`
}

// DefaultOpenAIFailoverRules is the table a fresh install starts from.
func DefaultOpenAIFailoverRules() []OpenAIFailoverRule {
	failover := func(id, name string, status int, keywords ...string) OpenAIFailoverRule {
		return OpenAIFailoverRule{ID: id, Name: name, Enabled: true, StatusCode: status, Keywords: keywords, Action: OpenAIFailoverActionFailover}
	}
	return []OpenAIFailoverRule{
		failover("encrypted-content", "加密内容解不开", 0,
			"encrypted function output content could not be decrypted",
			"could not be verified",
			"encrypted continuation context"),
		failover("stream-disconnected", "上游断流", 0, "stream disconnected before completion"),
		failover("sharing-usage-limit", "账号共享额度用尽", 0, "sharing usage limit"),
		failover("enable-thinking", "上游不认 enable_thinking", 0, "enable_thinking"),
		failover("invalid-parameters", "上游参数校验不一致", 400, "invalid parameters"),
		failover("responses-lite", "Responses Lite 参数不兼容", 400, "responses-lite requires"),
		failover("upstream-request-failed", "上游笼统失败", 400, "upstream request failed"),
		failover("http-408", "上游超时", 408),
	}
}

func defaultOpenAIFailoverConfig() OpenAIFailoverConfig {
	return OpenAIFailoverConfig{
		EnabledGroupIDs: []int64{},
		SlotWaitMs:      openAIFailoverDefaultSlotWaitMs,
		MaxRuleSwitches: openAIFailoverDefaultMaxRuleSwitches,
		MaxSlotSwitches: openAIFailoverDefaultMaxSlotSwitches,
		Rules:           DefaultOpenAIFailoverRules(),
	}
}

// NormalizeOpenAIFailoverConfig trims and validates a config coming from the
// admin page.
func NormalizeOpenAIFailoverConfig(cfg OpenAIFailoverConfig) (OpenAIFailoverConfig, error) {
	invalid := func(format string, args ...any) (OpenAIFailoverConfig, error) {
		return cfg, infraerrors.BadRequest("OPENAI_FAILOVER_CONFIG_INVALID", fmt.Sprintf(format, args...))
	}
	if cfg.SlotWaitMs < openAIFailoverMinSlotWaitMs || cfg.SlotWaitMs > openAIFailoverMaxSlotWaitMs {
		return invalid("slot_wait_ms must be between %d and %d", openAIFailoverMinSlotWaitMs, openAIFailoverMaxSlotWaitMs)
	}
	if cfg.MaxRuleSwitches < 1 || cfg.MaxRuleSwitches > openAIFailoverMaxSwitchesLimit {
		return invalid("max_rule_switches must be between 1 and %d", openAIFailoverMaxSwitchesLimit)
	}
	if cfg.MaxSlotSwitches < 0 || cfg.MaxSlotSwitches > openAIFailoverMaxSwitchesLimit {
		return invalid("max_slot_switches must be between 0 and %d", openAIFailoverMaxSwitchesLimit)
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
		if _, dup := seenRules[rule.ID]; dup {
			return invalid("duplicate rule id %q", rule.ID)
		}
		seenRules[rule.ID] = struct{}{}
		rule.Name = strings.TrimSpace(rule.Name)
		if rule.StatusCode != 0 && (rule.StatusCode < 400 || rule.StatusCode > 499) {
			return invalid("rule %q: status_code must be empty or 4xx", rule.ID)
		}
		keywords := make([]string, 0, len(rule.Keywords))
		for _, keyword := range rule.Keywords {
			if keyword = strings.TrimSpace(keyword); keyword != "" {
				keywords = append(keywords, keyword)
			}
		}
		if len(keywords) > openAIFailoverMaxKeywordsPerRule {
			return invalid("rule %q: at most %d keywords", rule.ID, openAIFailoverMaxKeywordsPerRule)
		}
		rule.Keywords = keywords
		if rule.StatusCode == 0 && len(keywords) == 0 {
			return invalid("rule %q needs a status code or a keyword", rule.ID)
		}
		if rule.Action != OpenAIFailoverActionFailover && rule.Action != OpenAIFailoverActionReturn {
			return invalid("rule %q: action must be failover or return", rule.ID)
		}
		rules = append(rules, rule)
	}
	cfg.Rules = rules
	return cfg, nil
}

// OpenAIFailoverPolicy is the immutable hot-path copy shared by every request
// of the enabled groups.
type OpenAIFailoverPolicy struct {
	slotWait        time.Duration
	maxRuleSwitches int
	maxSlotSwitches int
	rules           []compiledOpenAIFailoverRule
}

type compiledOpenAIFailoverRule struct {
	id       string
	status   int
	keywords []string
	failover bool
}

func newOpenAIFailoverPolicy(cfg OpenAIFailoverConfig) *OpenAIFailoverPolicy {
	policy := &OpenAIFailoverPolicy{
		slotWait:        time.Duration(cfg.SlotWaitMs) * time.Millisecond,
		maxRuleSwitches: cfg.MaxRuleSwitches,
		maxSlotSwitches: cfg.MaxSlotSwitches,
	}
	for _, rule := range cfg.Rules {
		if !rule.Enabled {
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

// SlotWait caps the wait for a busy account's slot.
func (p *OpenAIFailoverPolicy) SlotWait() time.Duration { return p.slotWait }

// MaxRuleSwitches caps the account switches rules may trigger per request.
func (p *OpenAIFailoverPolicy) MaxRuleSwitches() int { return p.maxRuleSwitches }

// MaxSlotSwitches caps the account switches busy slots may trigger per request.
func (p *OpenAIFailoverPolicy) MaxSlotSwitches() int { return p.maxSlotSwitches }

// match returns the ID of the first matching rule that switches accounts. The
// first match decides, so a `return` rule above shields an error from a broader
// failover rule below it. statusCode 0 means the error arrived inside a stream.
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
		if len(rule.keywords) > 0 {
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
		}
		if rule.failover {
			return rule.id
		}
		return ""
	}
	return ""
}

type openAIFailoverPolicyContextKey struct{}
type openAIEncryptedArgsContextKey struct{}

// WithOpenAIFailoverPolicy attaches the group's policy to the request.
func WithOpenAIFailoverPolicy(ctx context.Context, policy *OpenAIFailoverPolicy) context.Context {
	if policy == nil {
		return ctx
	}
	return context.WithValue(ctx, openAIFailoverPolicyContextKey{}, policy)
}

// OpenAIFailoverPolicyFromContext returns the request's policy, nil when its
// group has not opted in or the path does not take part.
func OpenAIFailoverPolicyFromContext(ctx context.Context) *OpenAIFailoverPolicy {
	if ctx == nil {
		return nil
	}
	policy, _ := ctx.Value(openAIFailoverPolicyContextKey{}).(*OpenAIFailoverPolicy)
	return policy
}

// WithOpenAIEncryptedArgsRequired keeps accounts marked with
// OpenAINoEncryptedArgsExtraKey out of this request's selection.
func WithOpenAIEncryptedArgsRequired(ctx context.Context) context.Context {
	return context.WithValue(ctx, openAIEncryptedArgsContextKey{}, true)
}

func openAIEncryptedArgsRequired(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	required, _ := ctx.Value(openAIEncryptedArgsContextKey{}).(bool)
	return required
}

// IsOpenAINoEncryptedArgs reports the admin's mark.
func (a *Account) IsOpenAINoEncryptedArgs() bool {
	if a == nil || a.Extra == nil {
		return false
	}
	marked, _ := a.Extra[OpenAINoEncryptedArgsExtraKey].(bool)
	return marked
}

// OpenAIRequestDeclaresEncryptedArgs reports a tool parameter declared
// `"encrypted": true`, however the client formatted its JSON. Only the tools
// count: a conversation can quote the same text without declaring anything.
func OpenAIRequestDeclaresEncryptedArgs(body []byte) bool {
	return openAIDeclaresEncryptedArg(gjson.GetBytes(body, "tools"), 0)
}

// openAIDeclaresEncryptedArg walks a tool definition. The depth limit keeps a
// hostile body from making each level rescan the rest of it; real schemas are
// nowhere near it.
func openAIDeclaresEncryptedArg(value gjson.Result, depth int) bool {
	if !value.IsObject() && !value.IsArray() {
		return false
	}
	if depth >= openAIEncryptedArgsMaxDepth {
		return false
	}
	found := false
	value.ForEach(func(key, child gjson.Result) bool {
		found = (key.Type == gjson.String && key.String() == "encrypted" && child.Type == gjson.True) ||
			openAIDeclaresEncryptedArg(child, depth+1)
		return !found
	})
	return found
}

// RewriteOpenAIPlaintextEncryptedContent turns agent_message encrypted_content
// items that hold plaintext into input_text and returns how many it changed.
// An upstream that does not honor `"encrypted": true` answers in plaintext and
// Codex stores that as encrypted_content; every later turn then fails with
// "could not be decrypted" on upstreams that really decrypt it, on any account.
// Plaintext elsewhere (function_call arguments) is accepted upstream.
func RewriteOpenAIPlaintextEncryptedContent(body []byte) ([]byte, int) {
	if !bytes.Contains(body, []byte(`"agent_message"`)) || !bytes.Contains(body, []byte(`"encrypted_content"`)) {
		return body, 0
	}
	type target struct {
		path string
		text string
	}
	var targets []target
	gjson.GetBytes(body, "input").ForEach(func(itemIndex, item gjson.Result) bool {
		if item.Get("type").String() != "agent_message" {
			return true
		}
		item.Get("content").ForEach(func(partIndex, part gjson.Result) bool {
			if part.Get("type").String() != "encrypted_content" {
				return true
			}
			value := part.Get("encrypted_content")
			if value.Type != gjson.String || value.String() == "" || strings.HasPrefix(value.String(), openAIEncryptedContentPrefix) {
				return true
			}
			targets = append(targets, target{
				path: fmt.Sprintf("input.%d.content.%d", itemIndex.Int(), partIndex.Int()),
				text: value.String(),
			})
			return true
		})
		return true
	})
	rewritten := 0
	for _, t := range targets {
		next, err := sjson.SetBytes(body, t.path, map[string]string{"type": "input_text", "text": t.text})
		if err != nil {
			continue
		}
		body = next
		rewritten++
	}
	return body, rewritten
}

// matchOpenAIFailoverRule decides an error the platform is about to return. An
// empty result leaves it to the existing logic. statusCode 0 means the error
// arrived inside a stream.
func matchOpenAIFailoverRule(c *gin.Context, statusCode int, message string, payload []byte) string {
	if c == nil || c.Request == nil {
		return ""
	}
	policy := OpenAIFailoverPolicyFromContext(c.Request.Context())
	if policy == nil {
		return ""
	}
	if hit, _, _ := detectOpenAICyberPolicy(payload); hit {
		return ""
	}
	if isOpenAIContextWindowError(message, payload) {
		return ""
	}
	// Only the message and code: the body of a structured error can echo
	// request content.
	text := message + " " + extractUpstreamErrorCode(payload) + " " + gjson.GetBytes(payload, "response.error.code").String()
	return policy.match(statusCode, text)
}

// newOpenAIRuleFailoverError switches accounts without any of the account side
// effects: rules cover errors that say nothing bad about the account, so it is
// neither cooled down nor retried.
func newOpenAIRuleFailoverError(ruleID string, statusCode int, headers http.Header, body []byte, message string) *UpstreamFailoverError {
	failoverErr := newOpenAIUpstreamFailoverError(statusCode, headers, body, message, false)
	failoverErr.RetryableOnSameAccount = false
	failoverErr.RequestScopedTransient = false
	failoverErr.FailoverRuleID = ruleID
	return failoverErr
}

// newOpenAIRuleStreamFailoverError is newOpenAIRuleFailoverError for a failure
// delivered inside a stream. The transport status was 200, so the semantic one
// is 400: the same status the event would have reached ops with.
func (s *OpenAIGatewayService) newOpenAIRuleStreamFailoverError(
	c *gin.Context,
	account *Account,
	passthrough bool,
	upstreamRequestID string,
	ruleID string,
	payload []byte,
	message string,
	headers http.Header,
) *UpstreamFailoverError {
	message = s.recordOpenAIStreamUpstreamError(c, account, passthrough, upstreamRequestID, "failover", payload, sanitizeUpstreamErrorMessage(strings.TrimSpace(message)))
	errorPayload := gin.H{"type": openAIUpstreamClientErrorFallbackType, "message": message}
	code := gjson.GetBytes(payload, "response.error.code").String()
	if code == "" {
		code = extractUpstreamErrorCode(payload)
	}
	if code != "" {
		errorPayload["code"] = code
	}
	body, _ := json.Marshal(gin.H{"error": errorPayload})
	return newOpenAIRuleFailoverError(ruleID, http.StatusBadRequest, nil, body, message)
}

// OpenAIFailoverOutcome is what one request did with the policy.
type OpenAIFailoverOutcome struct {
	GroupID      int64
	UserID       int64
	APIKeyID     int64
	RuleID       string
	StatusCode   int
	RuleSwitches int
	SlotSwitches int
	Rewrites     int
	Accounts     []int64
	Recovered    bool
}

// OpenAIFailoverEvent is one row of the recent list.
type OpenAIFailoverEvent struct {
	Time         time.Time `json:"time"`
	GroupID      int64     `json:"group_id"`
	UserID       int64     `json:"user_id"`
	APIKeyID     int64     `json:"api_key_id"`
	RuleID       string    `json:"rule_id"`
	StatusCode   int       `json:"status_code"`
	RuleSwitches int       `json:"rule_switches"`
	SlotSwitches int       `json:"slot_switches"`
	Rewrites     int       `json:"rewrites"`
	Accounts     []int64   `json:"accounts"`
	Recovered    bool      `json:"recovered"`
}

// OpenAIFailoverRuleStats counts the requests a rule hit.
type OpenAIFailoverRuleStats struct {
	RuleID    string `json:"rule_id"`
	Hits      int64  `json:"hits"`
	Recovered int64  `json:"recovered"`
}

// OpenAIFailoverStats summarizes the last openAIFailoverStatsDays days.
type OpenAIFailoverStats struct {
	Rules             []OpenAIFailoverRuleStats `json:"rules"`
	SlotSwitched      int64                     `json:"slot_switched"`
	SlotRecovered     int64                     `json:"slot_recovered"`
	RewrittenRequests int64                     `json:"rewritten_requests"`
	RewrittenItems    int64                     `json:"rewritten_items"`
}

// OpenAIFailoverOverview is the admin page's payload.
type OpenAIFailoverOverview struct {
	Config   OpenAIFailoverConfig    `json:"config"`
	Stats    OpenAIFailoverStats     `json:"stats"`
	Recent   []OpenAIFailoverEvent   `json:"recent"`
	Accounts []OpenAIFailoverAccount `json:"accounts"`
}

// OpenAIFailoverAccount is one row of the encrypted-arguments list.
type OpenAIFailoverAccount struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Type             string `json:"type"`
	Status           string `json:"status"`
	Schedulable      bool   `json:"schedulable"`
	NoEncryptedArgs  bool   `json:"no_encrypted_args"`
	CanCheck         bool   `json:"can_check"`
	DefaultCheckName string `json:"default_check_model"`
}

type openAIFailoverSnapshot struct {
	cfg      OpenAIFailoverConfig
	groups   map[int64]struct{}
	policy   *OpenAIFailoverPolicy
	loadedAt time.Time
}

// OpenAIFailoverPolicyService owns the setting, its statistics and the
// encrypted-arguments check.
type OpenAIFailoverPolicyService struct {
	settings SettingRepository
	rdb      *redis.Client
	accounts AccountRepository
	upstream HTTPUpstream

	snapshot  atomic.Pointer[openAIFailoverSnapshot]
	reloading atomic.Bool
	writes    chan struct{}
}

// NewOpenAIFailoverPolicyService builds the service.
func NewOpenAIFailoverPolicyService(settings SettingRepository, rdb *redis.Client, accounts AccountRepository, upstream HTTPUpstream) *OpenAIFailoverPolicyService {
	return &OpenAIFailoverPolicyService{
		settings: settings,
		rdb:      rdb,
		accounts: accounts,
		upstream: upstream,
		writes:   make(chan struct{}, 64),
	}
}

// PolicyForGroup returns the policy when the group opted in, nil otherwise.
func (s *OpenAIFailoverPolicyService) PolicyForGroup(ctx context.Context, groupID *int64) *OpenAIFailoverPolicy {
	if s == nil || groupID == nil {
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

// reload keeps the previous snapshot when the setting can not be read.
func (s *OpenAIFailoverPolicyService) reload(ctx context.Context) *openAIFailoverSnapshot {
	cfg, err := s.loadConfig(ctx)
	if err != nil {
		slog.Warn("openai_failover.config_load_failed", "error", err)
		previous := s.snapshot.Load()
		if previous != nil {
			refreshed := *previous
			refreshed.loadedAt = time.Now()
			s.snapshot.Store(&refreshed)
			return &refreshed
		}
		cfg = defaultOpenAIFailoverConfig()
	}
	return s.store(cfg)
}

func (s *OpenAIFailoverPolicyService) store(cfg OpenAIFailoverConfig) *openAIFailoverSnapshot {
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

func (s *OpenAIFailoverPolicyService) loadConfig(ctx context.Context) (OpenAIFailoverConfig, error) {
	raw, err := s.settings.GetValue(ctx, SettingKeyOpenAIFailoverPolicy)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return defaultOpenAIFailoverConfig(), nil
		}
		return OpenAIFailoverConfig{}, err
	}
	if strings.TrimSpace(raw) == "" {
		return defaultOpenAIFailoverConfig(), nil
	}
	var cfg OpenAIFailoverConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return OpenAIFailoverConfig{}, fmt.Errorf("parse %s: %w", SettingKeyOpenAIFailoverPolicy, err)
	}
	return NormalizeOpenAIFailoverConfig(cfg)
}

// Config returns the stored setting, the defaults when nothing was saved.
func (s *OpenAIFailoverPolicyService) Config(ctx context.Context) (OpenAIFailoverConfig, error) {
	return s.loadConfig(ctx)
}

// UpdateConfig validates, saves and applies the setting.
func (s *OpenAIFailoverPolicyService) UpdateConfig(ctx context.Context, cfg OpenAIFailoverConfig) (OpenAIFailoverConfig, error) {
	cfg, err := NormalizeOpenAIFailoverConfig(cfg)
	if err != nil {
		return cfg, err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	if err := s.settings.Set(ctx, SettingKeyOpenAIFailoverPolicy, string(raw)); err != nil {
		return cfg, err
	}
	s.store(cfg)
	return cfg, nil
}

// RecordOutcome counts one request. It never blocks the request: with Redis
// slow, records are dropped rather than queued.
func (s *OpenAIFailoverPolicyService) RecordOutcome(outcome OpenAIFailoverOutcome) {
	if s == nil || s.rdb == nil {
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

func (s *OpenAIFailoverPolicyService) writeOutcome(ctx context.Context, outcome OpenAIFailoverOutcome) {
	now := time.Now()
	key := openAIFailoverStatsKey(now)
	pipe := s.rdb.Pipeline()
	increment := func(field string, by int64) { pipe.HIncrBy(ctx, key, field, by) }
	if outcome.RuleID != "" {
		increment("rule:"+outcome.RuleID+":hit", 1)
		if outcome.Recovered {
			increment("rule:"+outcome.RuleID+":recovered", 1)
		}
	}
	if outcome.SlotSwitches > 0 {
		increment("slot:switched", 1)
		if outcome.Recovered {
			increment("slot:recovered", 1)
		}
	}
	if outcome.Rewrites > 0 {
		increment("rewrite:requests", 1)
		increment("rewrite:items", int64(outcome.Rewrites))
	}
	pipe.Expire(ctx, key, openAIFailoverStatsTTL)
	// A rewrite alone is routine; only switches belong in the recent list.
	if outcome.RuleID != "" || outcome.SlotSwitches > 0 {
		if raw, err := json.Marshal(OpenAIFailoverEvent{
			Time:         now,
			GroupID:      outcome.GroupID,
			UserID:       outcome.UserID,
			APIKeyID:     outcome.APIKeyID,
			RuleID:       outcome.RuleID,
			StatusCode:   outcome.StatusCode,
			RuleSwitches: outcome.RuleSwitches,
			SlotSwitches: outcome.SlotSwitches,
			Rewrites:     outcome.Rewrites,
			Accounts:     outcome.Accounts,
			Recovered:    outcome.Recovered,
		}); err == nil {
			pipe.LPush(ctx, openAIFailoverRecentKey, raw)
			pipe.LTrim(ctx, openAIFailoverRecentKey, 0, openAIFailoverRecentMax-1)
		}
	}
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Warn("openai_failover.record_failed", "error", err)
	}
}

// Overview assembles the admin page.
func (s *OpenAIFailoverPolicyService) Overview(ctx context.Context) (*OpenAIFailoverOverview, error) {
	cfg, err := s.Config(ctx)
	if err != nil {
		return nil, err
	}
	overview := &OpenAIFailoverOverview{Config: cfg, Recent: []OpenAIFailoverEvent{}}
	if overview.Stats, err = s.stats(ctx, cfg); err != nil {
		return nil, err
	}
	if s.rdb != nil {
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
	}
	accounts, err := s.accounts.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		return nil, err
	}
	overview.Accounts = make([]OpenAIFailoverAccount, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if account.IsShadow() {
			continue
		}
		overview.Accounts = append(overview.Accounts, OpenAIFailoverAccount{
			ID:               account.ID,
			Name:             account.Name,
			Type:             account.Type,
			Status:           account.Status,
			Schedulable:      account.Schedulable,
			NoEncryptedArgs:  account.IsOpenAINoEncryptedArgs(),
			CanCheck:         account.IsOpenAIApiKey(),
			DefaultCheckName: defaultOpenAIEncryptedArgsCheckModel(account),
		})
	}
	sort.Slice(overview.Accounts, func(i, j int) bool {
		left, right := &overview.Accounts[i], &overview.Accounts[j]
		if left.NoEncryptedArgs != right.NoEncryptedArgs {
			return left.NoEncryptedArgs
		}
		if left.Schedulable != right.Schedulable {
			return left.Schedulable
		}
		return left.ID < right.ID
	})
	return overview, nil
}

func (s *OpenAIFailoverPolicyService) stats(ctx context.Context, cfg OpenAIFailoverConfig) (OpenAIFailoverStats, error) {
	stats := OpenAIFailoverStats{Rules: []OpenAIFailoverRuleStats{}}
	if s.rdb == nil {
		return stats, nil
	}
	totals := map[string]int64{}
	now := time.Now()
	for day := 0; day < openAIFailoverStatsDays; day++ {
		fields, err := s.rdb.HGetAll(ctx, openAIFailoverStatsKey(now.AddDate(0, 0, -day))).Result()
		if err != nil {
			return stats, err
		}
		for field, raw := range fields {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err == nil {
				totals[field] += value
			}
		}
	}
	stats.SlotSwitched = totals["slot:switched"]
	stats.SlotRecovered = totals["slot:recovered"]
	stats.RewrittenRequests = totals["rewrite:requests"]
	stats.RewrittenItems = totals["rewrite:items"]
	// Rules keep their table order; deleted rules still with counts follow.
	seen := make(map[string]struct{}, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		seen[rule.ID] = struct{}{}
		stats.Rules = append(stats.Rules, OpenAIFailoverRuleStats{
			RuleID:    rule.ID,
			Hits:      totals["rule:"+rule.ID+":hit"],
			Recovered: totals["rule:"+rule.ID+":recovered"],
		})
	}
	var orphaned []string
	for field := range totals {
		id, isHit := strings.CutSuffix(strings.TrimPrefix(field, "rule:"), ":hit")
		if !isHit || !strings.HasPrefix(field, "rule:") {
			continue
		}
		if _, known := seen[id]; !known {
			orphaned = append(orphaned, id)
		}
	}
	sort.Strings(orphaned)
	for _, id := range orphaned {
		stats.Rules = append(stats.Rules, OpenAIFailoverRuleStats{
			RuleID:    id,
			Hits:      totals["rule:"+id+":hit"],
			Recovered: totals["rule:"+id+":recovered"],
		})
	}
	return stats, nil
}
