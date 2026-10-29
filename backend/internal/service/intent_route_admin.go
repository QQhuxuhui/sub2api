package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/ent/apikey"
	"github.com/Wei-Shaw/sub2api/ent/intentrouter"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	intentRouteMaxRules           = 20
	intentRouteMaxAccountsPerRule = 50
	intentRouteMinTimeoutMS       = 300
	intentRouteMaxTimeoutMS       = 15000
	intentRouteMinCacheTTL        = 60
	intentRouteMaxCacheTTL        = 7 * 24 * 3600
	intentRouteMinInputChars      = 100
	intentRouteMaxInputChars      = 20000
	intentRouteMaxKeywordsPerRule = 100
	intentJevMinTimeoutMS         = 200
	intentJevMaxTimeoutMS         = 5000
	intentJevMaxTurnsLimit        = 50
	intentJevMaxScopeIDs          = 500
	intentRouteMaxExcludedModels  = 200
	intentRouteMaxKeywordChars    = 1000000
)

// validIntentUnavailableStatus lists the statuses a router may report "the
// matched rule's accounts are all unavailable" with. 429 makes a downstream
// gateway cool the whole key down; 502/503 only make it fail over.
func validIntentUnavailableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable:
		return true
	}
	return false
}

func excludedModelsOrEmpty(models []string) []string {
	if models == nil {
		return []string{}
	}
	return models
}

// normalizeIntentExcludedModels trims the excluded models and drops empty
// ones and case-insensitive duplicates, keeping the configured order.
func normalizeIntentExcludedModels(models []string) ([]string, error) {
	if len(models) > intentRouteMaxExcludedModels {
		return nil, infraerrors.BadRequest("INTENT_TOO_MANY_EXCLUDED_MODELS", fmt.Sprintf("at most %d excluded models", intentRouteMaxExcludedModels))
	}
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if strings.TrimSuffix(model, "*") == "" {
			return nil, infraerrors.BadRequest("INTENT_EXCLUDED_MODEL_ALL", "\"*\" would exclude every model; switch the router off instead")
		}
		if len([]rune(model)) > 128 {
			return nil, infraerrors.BadRequest("INTENT_EXCLUDED_MODEL_TOO_LONG", "excluded model names are limited to 128 characters")
		}
		key := strings.ToLower(model)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model)
	}
	return out, nil
}

// IntentRouterView is a router as the admin UI sees it. The classifier API key
// never leaves the server; the UI only learns whether one is stored.
type IntentRouterView struct {
	GroupID                    int64               `json:"group_id"`
	Enabled                    bool                `json:"enabled"`
	ClassifierBaseURL          string              `json:"classifier_base_url"`
	ClassifierAPIKeyConfigured bool                `json:"classifier_api_key_configured"`
	ClassifierProtocol         string              `json:"classifier_protocol"`
	ClassifierModel            string              `json:"classifier_model"`
	ClassifierTimeoutMS        int                 `json:"classifier_timeout_ms"`
	CacheTTLSeconds            int                 `json:"cache_ttl_seconds"`
	MaxInputChars              int                 `json:"max_input_chars"`
	KeywordMaxChars            int                 `json:"keyword_max_chars"`
	UnavailableStatus          int                 `json:"unavailable_status"`
	Rules                      []domain.IntentRule `json:"rules"`
	// ExcludedModels skip intent routing entirely; a trailing * matches a prefix.
	ExcludedModels      []string `json:"excluded_models"`
	JevAPIKeyConfigured bool     `json:"jev_api_key_configured"`
	// JevScope names the users and keys Jev is scoped to, for display.
	JevScope  IntentJevScopeLabels   `json:"jev_scope"`
	Jev       domain.IntentJevConfig `json:"jev"`
	UpdatedAt time.Time              `json:"updated_at"`
}

// IntentRouterInput is a full router document. An empty ClassifierAPIKey keeps
// the stored one, so the UI can save without ever holding the secret.
type IntentRouterInput struct {
	Enabled             bool                `json:"enabled"`
	ClassifierBaseURL   string              `json:"classifier_base_url"`
	ClassifierAPIKey    string              `json:"classifier_api_key"`
	ClassifierProtocol  string              `json:"classifier_protocol"`
	ClassifierModel     string              `json:"classifier_model"`
	ClassifierTimeoutMS int                 `json:"classifier_timeout_ms"`
	CacheTTLSeconds     int                 `json:"cache_ttl_seconds"`
	MaxInputChars       int                 `json:"max_input_chars"`
	KeywordMaxChars     int                 `json:"keyword_max_chars"`  // 0 = keywords match messages of any length
	UnavailableStatus   int                 `json:"unavailable_status"` // 429, 502 or 503; 0 means 503
	Rules               []domain.IntentRule `json:"rules"`
	ExcludedModels      []string            `json:"excluded_models"`
	// JevAPIKey, like ClassifierAPIKey, keeps the stored key when empty.
	JevAPIKey string                 `json:"jev_api_key"`
	Jev       domain.IntentJevConfig `json:"jev"`
	// JevSetBalanceUSD, when present, is the balance just read off the
	// TypeSafe console: the estimate restarts from it.
	JevSetBalanceUSD *float64 `json:"jev_set_balance_usd,omitempty"`
}

// IntentClassifyTestResult is the outcome of trying a text against a router.
type IntentClassifyTestResult struct {
	// MatchedBy is "keyword" when a keyword decided (the classifier was not
	// asked), "jev" when Jev judged the text, "classifier" when the model was
	// asked, "" when neither applied.
	MatchedBy  string  `json:"matched_by"`
	Keyword    string  `json:"keyword,omitempty"`
	Answer     string  `json:"answer"`
	Intent     string  `json:"intent"`
	Understood bool    `json:"understood"`
	AccountIDs []int64 `json:"account_ids"`
	LatencyMS  int64   `json:"latency_ms"`
	// ProbeProbability is Jev's answer when Jev was asked.
	ProbeProbability *float64 `json:"probe_probability,omitempty"`
	// HealthProbability is Jev's health-check answer, when health checks are
	// on; HealthCheck reports it reached the health threshold.
	HealthProbability *float64 `json:"health_probability,omitempty"`
	HealthCheck       bool     `json:"health_check,omitempty"`
}

func intentRouterViewFromEnt(row *dbent.IntentRouter) *IntentRouterView {
	rules := row.Rules
	if rules == nil {
		rules = []domain.IntentRule{}
	}
	return &IntentRouterView{
		GroupID:                    row.GroupID,
		Enabled:                    row.Enabled,
		ClassifierBaseURL:          row.ClassifierBaseURL,
		ClassifierAPIKeyConfigured: strings.TrimSpace(row.ClassifierAPIKey) != "",
		ClassifierProtocol:         row.ClassifierProtocol,
		ClassifierModel:            row.ClassifierModel,
		ClassifierTimeoutMS:        row.ClassifierTimeoutMs,
		CacheTTLSeconds:            row.CacheTTLSeconds,
		MaxInputChars:              row.MaxInputChars,
		KeywordMaxChars:            row.KeywordMaxChars,
		UnavailableStatus:          row.UnavailableStatus,
		Rules:                      rules,
		ExcludedModels:             excludedModelsOrEmpty(row.ExcludedModels),
		JevAPIKeyConfigured:        strings.TrimSpace(row.JevAPIKey) != "",
		Jev:                        intentJevWithDefaults(row.Jev),
		UpdatedAt:                  row.UpdatedAt,
	}
}

// IntentJevScopeLabels names the users and API keys of a Jev scope; IDs no
// longer found (deleted) are left out, so the page can flag them.
type IntentJevScopeLabels struct {
	Users   []IntentJevUserLabel   `json:"users"`
	APIKeys []IntentJevAPIKeyLabel `json:"api_keys"`
}

type IntentJevUserLabel struct {
	ID       int64  `json:"id"`
	Email    string `json:"email"`
	Username string `json:"username"`
}

type IntentJevAPIKeyLabel struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	UserID int64  `json:"user_id"`
}

// resolveIntentJevKeys fills in the owner of every listed API key and checks
// that each key exists and belongs to a listed user.
func (s *IntentRouterService) resolveIntentJevKeys(ctx context.Context, jev *domain.IntentJevConfig) error {
	if len(jev.APIKeys) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(jev.APIKeys))
	for _, key := range jev.APIKeys {
		ids = append(ids, key.ID)
	}
	rows, err := s.entClient.APIKey.Query().Where(apikey.IDIn(ids...)).All(ctx)
	if err != nil {
		return fmt.Errorf("load jev api keys: %w", err)
	}
	owners := make(map[int64]int64, len(rows))
	for _, row := range rows {
		owners[row.ID] = row.UserID
	}
	listed := make(map[int64]struct{}, len(jev.UserIDs))
	for _, id := range jev.UserIDs {
		listed[id] = struct{}{}
	}
	for i := range jev.APIKeys {
		owner, ok := owners[jev.APIKeys[i].ID]
		if !ok {
			return infraerrors.BadRequest("INTENT_JEV_API_KEY_NOT_FOUND", fmt.Sprintf("API key %d does not exist", jev.APIKeys[i].ID)).
				WithMetadata(map[string]string{"api_key_id": fmt.Sprint(jev.APIKeys[i].ID)})
		}
		if _, ok := listed[owner]; !ok {
			return infraerrors.BadRequest("INTENT_JEV_API_KEY_USER_NOT_LISTED", fmt.Sprintf("API key %d belongs to user %d, who is not chosen", jev.APIKeys[i].ID, owner)).
				WithMetadata(map[string]string{"api_key_id": fmt.Sprint(jev.APIKeys[i].ID), "user_id": fmt.Sprint(owner)})
		}
		jev.APIKeys[i].UserID = owner
	}
	return nil
}

// withJevScopeLabels names the users and keys of the views' Jev scopes, with
// one query for each kind across all views.
func (s *IntentRouterService) withJevScopeLabels(ctx context.Context, views ...*IntentRouterView) error {
	var userIDs, keyIDs []int64
	for _, v := range views {
		userIDs = append(userIDs, v.Jev.UserIDs...)
		for _, key := range v.Jev.APIKeys {
			keyIDs = append(keyIDs, key.ID)
		}
	}
	users := map[int64]IntentJevUserLabel{}
	keys := map[int64]IntentJevAPIKeyLabel{}
	if len(userIDs) > 0 {
		rows, err := s.entClient.User.Query().Where(user.IDIn(userIDs...)).All(ctx)
		if err != nil {
			return fmt.Errorf("load jev users: %w", err)
		}
		for _, row := range rows {
			users[row.ID] = IntentJevUserLabel{ID: row.ID, Email: row.Email, Username: row.Username}
		}
	}
	if len(keyIDs) > 0 {
		rows, err := s.entClient.APIKey.Query().Where(apikey.IDIn(keyIDs...)).All(ctx)
		if err != nil {
			return fmt.Errorf("load jev api keys: %w", err)
		}
		for _, row := range rows {
			keys[row.ID] = IntentJevAPIKeyLabel{ID: row.ID, Name: row.Name, UserID: row.UserID}
		}
	}
	for _, v := range views {
		v.JevScope = IntentJevScopeLabels{Users: []IntentJevUserLabel{}, APIKeys: []IntentJevAPIKeyLabel{}}
		for _, id := range v.Jev.UserIDs {
			if label, ok := users[id]; ok {
				v.JevScope.Users = append(v.JevScope.Users, label)
			}
		}
		for _, key := range v.Jev.APIKeys {
			if label, ok := keys[key.ID]; ok {
				v.JevScope.APIKeys = append(v.JevScope.APIKeys, label)
			}
		}
	}
	return nil
}

func (s *IntentRouterService) ListRouters(ctx context.Context) ([]*IntentRouterView, error) {
	rows, err := s.entClient.IntentRouter.Query().Order(dbent.Asc(intentrouter.FieldGroupID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list intent routers: %w", err)
	}
	views := make([]*IntentRouterView, 0, len(rows))
	for _, row := range rows {
		views = append(views, intentRouterViewFromEnt(row))
	}
	if err := s.withJevScopeLabels(ctx, views...); err != nil {
		return nil, err
	}
	return views, nil
}

func (s *IntentRouterService) GetRouter(ctx context.Context, groupID int64) (*IntentRouterView, error) {
	row, err := s.entClient.IntentRouter.Query().Where(intentrouter.GroupIDEQ(groupID)).Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, infraerrors.NotFound("INTENT_ROUTER_NOT_FOUND", "this group has no intent router")
	}
	if err != nil {
		return nil, fmt.Errorf("get intent router: %w", err)
	}
	view := intentRouterViewFromEnt(row)
	if err := s.withJevScopeLabels(ctx, view); err != nil {
		return nil, err
	}
	return view, nil
}

// SaveRouter creates or replaces the router of a group.
func (s *IntentRouterService) SaveRouter(ctx context.Context, groupID int64, in IntentRouterInput) (*IntentRouterView, error) {
	group, err := s.entClient.Group.Get(ctx, groupID)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, infraerrors.NotFound("GROUP_NOT_FOUND", "group not found")
		}
		return nil, fmt.Errorf("load group: %w", err)
	}
	existing, err := s.entClient.IntentRouter.Query().Where(intentrouter.GroupIDEQ(groupID)).Only(ctx)
	if err != nil && !dbent.IsNotFound(err) {
		return nil, fmt.Errorf("load intent router: %w", err)
	}
	if err := s.normalizeIntentRouterInput(ctx, &in, group.Platform, existing); err != nil {
		return nil, err
	}

	var saved *dbent.IntentRouter
	if existing == nil {
		saved, err = s.entClient.IntentRouter.Create().
			SetGroupID(groupID).SetEnabled(in.Enabled).
			SetClassifierBaseURL(in.ClassifierBaseURL).SetClassifierAPIKey(in.ClassifierAPIKey).
			SetClassifierProtocol(in.ClassifierProtocol).SetClassifierModel(in.ClassifierModel).
			SetClassifierTimeoutMs(in.ClassifierTimeoutMS).SetCacheTTLSeconds(in.CacheTTLSeconds).
			SetMaxInputChars(in.MaxInputChars).SetKeywordMaxChars(in.KeywordMaxChars).SetUnavailableStatus(in.UnavailableStatus).SetRules(in.Rules).
			SetJevAPIKey(in.JevAPIKey).SetJev(in.Jev).SetExcludedModels(in.ExcludedModels).Save(ctx)
	} else {
		saved, err = s.entClient.IntentRouter.UpdateOneID(existing.ID).
			SetEnabled(in.Enabled).
			SetClassifierBaseURL(in.ClassifierBaseURL).SetClassifierAPIKey(in.ClassifierAPIKey).
			SetClassifierProtocol(in.ClassifierProtocol).SetClassifierModel(in.ClassifierModel).
			SetClassifierTimeoutMs(in.ClassifierTimeoutMS).SetCacheTTLSeconds(in.CacheTTLSeconds).
			SetMaxInputChars(in.MaxInputChars).SetKeywordMaxChars(in.KeywordMaxChars).SetUnavailableStatus(in.UnavailableStatus).SetRules(in.Rules).
			SetJevAPIKey(in.JevAPIKey).SetJev(in.Jev).SetExcludedModels(in.ExcludedModels).Save(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("save intent router: %w", err)
	}
	// A save is the admin's word: forget pauses and any switch-off in flight.
	s.jevReset(groupID)
	if in.JevSetBalanceUSD != nil && s.jevStats != nil {
		if err := s.jevStats.ResetSpent(ctx, groupID); err != nil {
			slog.Warn("intent_route.jev_reset_spent_failed", "group_id", groupID, "error", err)
		}
	}
	s.InvalidateConfig(ctx)
	view := intentRouterViewFromEnt(saved)
	if err := s.withJevScopeLabels(ctx, view); err != nil {
		return nil, err
	}
	return view, nil
}

func (s *IntentRouterService) DeleteRouter(ctx context.Context, groupID int64) error {
	if _, err := s.entClient.IntentRouter.Delete().Where(intentrouter.GroupIDEQ(groupID)).Exec(ctx); err != nil {
		return fmt.Errorf("delete intent router: %w", err)
	}
	s.InvalidateConfig(ctx)
	if s.store != nil {
		_, _ = s.store.ClearSessions(ctx, groupID)
	}
	return nil
}

func (s *IntentRouterService) normalizeIntentRouterInput(ctx context.Context, in *IntentRouterInput, groupPlatform string, existing *dbent.IntentRouter) error {
	bad := func(reason, message string) error { return infraerrors.BadRequest(reason, message) }

	in.ClassifierBaseURL = strings.TrimRight(strings.TrimSpace(in.ClassifierBaseURL), "/")
	if in.ClassifierBaseURL != "" {
		parsed, err := url.Parse(in.ClassifierBaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return bad("INTENT_INVALID_BASE_URL", "classifier base url must be an absolute http(s) URL, or empty to use this server")
		}
	}
	in.ClassifierAPIKey = strings.TrimSpace(in.ClassifierAPIKey)
	if in.ClassifierAPIKey == "" && existing != nil {
		in.ClassifierAPIKey = existing.ClassifierAPIKey
	}
	in.ClassifierModel = strings.TrimSpace(in.ClassifierModel)
	switch in.ClassifierProtocol = strings.TrimSpace(in.ClassifierProtocol); in.ClassifierProtocol {
	case "":
		in.ClassifierProtocol = domain.IntentClassifierProtocolOpenAIChat
	case domain.IntentClassifierProtocolOpenAIChat, domain.IntentClassifierProtocolGemini:
	default:
		return bad("INTENT_INVALID_PROTOCOL", "classifier protocol must be openai_chat or gemini")
	}
	if in.ClassifierTimeoutMS == 0 {
		in.ClassifierTimeoutMS = 3000
	}
	if in.CacheTTLSeconds == 0 {
		in.CacheTTLSeconds = 7200
	}
	if in.MaxInputChars == 0 {
		in.MaxInputChars = 2000
	}
	if in.ClassifierTimeoutMS < intentRouteMinTimeoutMS || in.ClassifierTimeoutMS > intentRouteMaxTimeoutMS {
		return bad("INTENT_INVALID_TIMEOUT", fmt.Sprintf("classifier timeout must be %d-%d ms", intentRouteMinTimeoutMS, intentRouteMaxTimeoutMS))
	}
	if in.CacheTTLSeconds < intentRouteMinCacheTTL || in.CacheTTLSeconds > intentRouteMaxCacheTTL {
		return bad("INTENT_INVALID_CACHE_TTL", fmt.Sprintf("cache ttl must be %d-%d seconds", intentRouteMinCacheTTL, intentRouteMaxCacheTTL))
	}
	if in.MaxInputChars < intentRouteMinInputChars || in.MaxInputChars > intentRouteMaxInputChars {
		return bad("INTENT_INVALID_MAX_INPUT", fmt.Sprintf("max input chars must be %d-%d", intentRouteMinInputChars, intentRouteMaxInputChars))
	}
	if in.KeywordMaxChars < 0 || in.KeywordMaxChars > intentRouteMaxKeywordChars {
		return bad("INTENT_INVALID_KEYWORD_MAX_CHARS", fmt.Sprintf("keyword max chars must be 0-%d (0 = no limit)", intentRouteMaxKeywordChars))
	}
	if in.UnavailableStatus == 0 {
		in.UnavailableStatus = http.StatusServiceUnavailable
	}
	if !validIntentUnavailableStatus(in.UnavailableStatus) {
		return bad("INTENT_INVALID_UNAVAILABLE_STATUS", "unavailable status must be 429, 502 or 503")
	}
	classifierReady := in.ClassifierModel != "" && in.ClassifierAPIKey != ""
	excluded, err := normalizeIntentExcludedModels(in.ExcludedModels)
	if err != nil {
		return err
	}
	in.ExcludedModels = excluded
	in.JevAPIKey = strings.TrimSpace(in.JevAPIKey)
	if in.JevAPIKey == "" && existing != nil {
		in.JevAPIKey = existing.JevAPIKey
	}
	var stored domain.IntentJevConfig
	if existing != nil {
		stored = existing.Jev
	}
	if err := normalizeIntentJev(&in.Jev, stored, in.JevSetBalanceUSD, in.JevAPIKey, in.Rules, s.now()); err != nil {
		return err
	}
	if err := s.resolveIntentJevKeys(ctx, &in.Jev); err != nil {
		return err
	}
	// The rule Jev routes probes to may have no matcher of its own — also
	// while Jev is off, or a switch-off (by an admin or by the server) would
	// leave a router that can no longer be saved.
	jevRule := strings.ToLower(strings.TrimSpace(in.Jev.Rule))
	healthRule := strings.ToLower(strings.TrimSpace(in.Jev.HealthRule))
	jevTarget := func(name string) bool { return name == jevRule || (healthRule != "" && name == healthRule) }

	if len(in.Rules) > intentRouteMaxRules {
		return bad("INTENT_TOO_MANY_RULES", fmt.Sprintf("at most %d rules", intentRouteMaxRules))
	}
	names := make(map[string]struct{}, len(in.Rules))
	accountIDs := make(map[int64]struct{})
	for i := range in.Rules {
		rule := &in.Rules[i]
		rule.Name = strings.TrimSpace(rule.Name)
		rule.Description = strings.TrimSpace(rule.Description)
		if rule.Name == "" {
			return bad("INTENT_RULE_NAME_REQUIRED", "every rule needs a name")
		}
		if strings.EqualFold(rule.Name, intentNoneLabel) {
			return bad("INTENT_RULE_NAME_RESERVED", intentNoneLabel+" is reserved for \"no rule applies\"")
		}
		if len([]rune(rule.Name)) > 40 || len([]rune(rule.Description)) > 500 {
			return bad("INTENT_RULE_TOO_LONG", "rule names are limited to 40 characters and descriptions to 500")
		}
		key := strings.ToLower(rule.Name)
		if _, dup := names[key]; dup {
			return infraerrors.BadRequest("INTENT_RULE_NAME_DUPLICATE", "rule names must be unique: "+rule.Name).
				WithMetadata(map[string]string{"name": rule.Name})
		}
		names[key] = struct{}{}
		if err := normalizeIntentKeywords(rule); err != nil {
			return err
		}
		if rule.Description == "" && len(rule.Keywords) == 0 && !jevTarget(key) {
			return infraerrors.BadRequest("INTENT_RULE_MATCHER_REQUIRED", "rule \""+rule.Name+"\" needs keywords or a description: keywords are matched directly, the description is what the classifier decides by").
				WithMetadata(map[string]string{"name": rule.Name})
		}
		if len(rule.AccountIDs) == 0 {
			return infraerrors.BadRequest("INTENT_RULE_ACCOUNTS_REQUIRED", "rule \""+rule.Name+"\" needs at least one account").
				WithMetadata(map[string]string{"name": rule.Name})
		}
		if len(rule.AccountIDs) > intentRouteMaxAccountsPerRule {
			return bad("INTENT_RULE_TOO_MANY_ACCOUNTS", fmt.Sprintf("at most %d accounts per rule", intentRouteMaxAccountsPerRule))
		}
		seen := make(map[int64]struct{}, len(rule.AccountIDs))
		unique := rule.AccountIDs[:0]
		for _, id := range rule.AccountIDs {
			if _, dup := seen[id]; dup || id <= 0 {
				continue
			}
			seen[id] = struct{}{}
			accountIDs[id] = struct{}{}
			unique = append(unique, id)
		}
		rule.AccountIDs = unique
	}
	// One name must not be contained in another, or a chatty answer such as
	// "this is code review" could not be told apart from the rule "code".
	for a := range names {
		for b := range names {
			if a != b && strings.Contains(a, b) {
				return infraerrors.BadRequest("INTENT_RULE_NAME_OVERLAP", "rule names must not contain one another: \""+b+"\" is part of \""+a+"\"").
					WithMetadata(map[string]string{"inner": b, "outer": a})
			}
		}
	}
	if in.Rules == nil {
		in.Rules = []domain.IntentRule{}
	}
	if in.Enabled && !classifierReady {
		if !intentRulesHaveKeywords(in.Rules) && jevRule == "" {
			return bad("INTENT_CLASSIFIER_INCOMPLETE", "an enabled router needs a classifier model and API key, rules with keywords, or Jev")
		}
		for _, rule := range in.Rules {
			if rule.Enabled && len(rule.Keywords) == 0 && !jevTarget(strings.ToLower(rule.Name)) {
				return infraerrors.BadRequest("INTENT_RULE_UNREACHABLE", "rule \""+rule.Name+"\" has no keywords and no classifier is configured, so it can never match").
					WithMetadata(map[string]string{"name": rule.Name})
			}
		}
	}
	return s.validateIntentRuleAccounts(ctx, accountIDs, groupPlatform)
}

// normalizeIntentJev applies defaults and checks the Jev settings. Disabled
// settings are kept (so the admin can switch Jev off and on again) but only
// need to be well-formed; enabled ones need a key and an existing rule.
func normalizeIntentJev(jev *domain.IntentJevConfig, stored domain.IntentJevConfig, setBalance *float64, apiKey string, rules []domain.IntentRule, now time.Time) error {
	bad := func(reason, message string) error { return infraerrors.BadRequest(reason, message) }
	*jev = intentJevWithDefaults(*jev)
	jev.Rule = strings.TrimSpace(jev.Rule)
	jev.HealthRule = strings.TrimSpace(jev.HealthRule)
	// Server-owned fields: the balance baseline changes only through
	// setBalance, the switch-off record only through the server — and is
	// cleared once an admin turns Jev on again.
	jev.BalanceUSD, jev.BalanceSetAt = stored.BalanceUSD, stored.BalanceSetAt
	jev.AutoDisabledStatus, jev.AutoDisabledDetail, jev.AutoDisabledAt = stored.AutoDisabledStatus, stored.AutoDisabledDetail, stored.AutoDisabledAt
	jev.Revision = newIntentJevRevision()
	if jev.Enabled {
		jev.AutoDisabledStatus, jev.AutoDisabledDetail, jev.AutoDisabledAt = 0, "", nil
	}
	if setBalance != nil {
		if *setBalance < 0 || *setBalance > 1e6 {
			return bad("INTENT_JEV_INVALID_BALANCE", "jev balance must be 0-1000000 USD")
		}
		at := now
		jev.BalanceUSD, jev.BalanceSetAt = *setBalance, &at
	}
	if jev.SamplePercent < 1 || jev.SamplePercent > 100 {
		return bad("INTENT_JEV_INVALID_SAMPLE", "jev sample percent must be 1-100")
	}
	if *jev.LowBalanceUSD < 0 || *jev.LowBalanceUSD > 1e6 {
		return bad("INTENT_JEV_INVALID_LOW_BALANCE", "jev low balance alert must be 0-1000000 USD")
	}
	if jev.Threshold <= 0 || jev.Threshold > 1 {
		return bad("INTENT_JEV_INVALID_THRESHOLD", "jev threshold must be greater than 0 and at most 1")
	}
	if jev.HealthThreshold <= 0 || jev.HealthThreshold > 1 {
		return bad("INTENT_JEV_INVALID_HEALTH_THRESHOLD", "jev health-check threshold must be greater than 0 and at most 1")
	}
	if jev.HealthRule != "" {
		found := false
		for i := range rules {
			if strings.EqualFold(strings.TrimSpace(rules[i].Name), jev.HealthRule) {
				jev.HealthRule, found = strings.TrimSpace(rules[i].Name), true
				break
			}
		}
		switch {
		case found:
		case jev.Enabled && jev.HealthEnabled:
			return infraerrors.BadRequest("INTENT_JEV_HEALTH_RULE_UNKNOWN", "the health-check rule \""+jev.HealthRule+"\" does not exist").
				WithMetadata(map[string]string{"name": jev.HealthRule})
		default:
			// Health checks (or Jev) are off: a rule deleted or renamed since
			// leaves a stale reference that must not block the save.
			jev.HealthRule = ""
		}
	}
	if jev.MaxTurns < 1 || jev.MaxTurns > intentJevMaxTurnsLimit {
		return bad("INTENT_JEV_INVALID_MAX_TURNS", fmt.Sprintf("jev max turns must be 1-%d", intentJevMaxTurnsLimit))
	}
	if jev.TimeoutMS < intentJevMinTimeoutMS || jev.TimeoutMS > intentJevMaxTimeoutMS {
		return bad("INTENT_JEV_INVALID_TIMEOUT", fmt.Sprintf("jev timeout must be %d-%d ms", intentJevMinTimeoutMS, intentJevMaxTimeoutMS))
	}
	if len([]rune(jev.Model)) > 64 {
		return bad("INTENT_JEV_INVALID_MODEL", "jev model name is limited to 64 characters")
	}
	if jev.Scope != domain.IntentJevScopeAll && jev.Scope != domain.IntentJevScopeListed {
		return bad("INTENT_JEV_INVALID_SCOPE", "jev scope must be all or listed")
	}
	var err error
	if jev.UserIDs, err = normalizeIntentJevIDs(jev.UserIDs); err != nil {
		return err
	}
	seenKeys := make(map[int64]struct{}, len(jev.APIKeys))
	keys := make([]domain.IntentJevAPIKey, 0, len(jev.APIKeys))
	for _, key := range jev.APIKeys {
		if _, dup := seenKeys[key.ID]; dup || key.ID <= 0 {
			continue
		}
		seenKeys[key.ID] = struct{}{}
		keys = append(keys, domain.IntentJevAPIKey{ID: key.ID})
	}
	if len(keys) > intentJevMaxScopeIDs {
		return bad("INTENT_JEV_SCOPE_TOO_LARGE", fmt.Sprintf("at most %d API keys", intentJevMaxScopeIDs))
	}
	jev.APIKeys = keys
	if !jev.Enabled {
		return nil
	}
	if apiKey == "" {
		return bad("INTENT_JEV_KEY_REQUIRED", "enter the Jev API key to enable Jev")
	}
	found := false
	for i := range rules {
		if strings.EqualFold(strings.TrimSpace(rules[i].Name), jev.Rule) {
			jev.Rule, found = strings.TrimSpace(rules[i].Name), true
			break
		}
	}
	if !found {
		return bad("INTENT_JEV_RULE_REQUIRED", "choose the rule whose accounts serve requests Jev judges to be probes")
	}
	if jev.Scope == domain.IntentJevScopeListed && len(jev.UserIDs) == 0 {
		return bad("INTENT_JEV_SCOPE_EMPTY", "choose at least one user, or apply Jev to everyone")
	}
	return nil
}

func normalizeIntentJevIDs(ids []int64) ([]int64, error) {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup || id <= 0 {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) > intentJevMaxScopeIDs {
		return nil, infraerrors.BadRequest("INTENT_JEV_SCOPE_TOO_LARGE", fmt.Sprintf("at most %d users and %d API keys", intentJevMaxScopeIDs, intentJevMaxScopeIDs))
	}
	return out, nil
}

// normalizeIntentKeywords trims the rule's keywords and drops empty ones and
// case-insensitive duplicates, keeping the configured order.
func normalizeIntentKeywords(rule *domain.IntentRule) error {
	if len(rule.Keywords) > intentRouteMaxKeywordsPerRule {
		return infraerrors.BadRequest("INTENT_RULE_TOO_MANY_KEYWORDS", fmt.Sprintf("at most %d keywords per rule", intentRouteMaxKeywordsPerRule)).
			WithMetadata(map[string]string{"name": rule.Name, "max": fmt.Sprint(intentRouteMaxKeywordsPerRule)})
	}
	seen := make(map[string]struct{}, len(rule.Keywords))
	keywords := make([]string, 0, len(rule.Keywords))
	for _, keyword := range rule.Keywords {
		keyword = strings.TrimSpace(keyword)
		if keyword == "" {
			continue
		}
		key := strings.ToLower(keyword)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		keywords = append(keywords, keyword)
	}
	rule.Keywords = keywords
	return nil
}

func intentRulesHaveKeywords(rules []domain.IntentRule) bool {
	for _, rule := range rules {
		if rule.Enabled && len(rule.Keywords) > 0 && len(rule.AccountIDs) > 0 {
			return true
		}
	}
	return false
}

// validateIntentRuleAccounts rejects targets that could never serve a request
// arriving on the group: accounts that do not exist, or whose platform speaks
// a different upstream protocol than the group's.
func (s *IntentRouterService) validateIntentRuleAccounts(ctx context.Context, ids map[int64]struct{}, groupPlatform string) error {
	if len(ids) == 0 {
		return nil
	}
	list := make([]int64, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	rows, err := s.entClient.Account.Query().Where(account.IDIn(list...)).All(ctx)
	if err != nil {
		return fmt.Errorf("load rule accounts: %w", err)
	}
	found := make(map[int64]*dbent.Account, len(rows))
	for _, row := range rows {
		found[row.ID] = row
	}
	for _, id := range list {
		row, ok := found[id]
		if !ok {
			return infraerrors.BadRequest("INTENT_ACCOUNT_NOT_FOUND", fmt.Sprintf("account %d does not exist", id)).
				WithMetadata(map[string]string{"account_id": fmt.Sprint(id)})
		}
		if !intentAccountPlatformCompatible(groupPlatform, row.Platform) {
			return infraerrors.BadRequest("INTENT_ACCOUNT_PLATFORM_MISMATCH",
				fmt.Sprintf("account %d (%s, platform %s) cannot serve requests of a %s group", id, row.Name, row.Platform, groupPlatform)).
				WithMetadata(map[string]string{"account_id": fmt.Sprint(id), "account_platform": row.Platform, "group_platform": groupPlatform})
		}
	}
	return nil
}

// intentAccountPlatformCompatible mirrors what selection accepts: the same
// platform, or an Antigravity account for Claude/Gemini groups (mixed
// scheduling — whether the account opted in is re-checked at request time).
// Group platforms this function does not know (composite groups resolve their
// platform per request) are not second-guessed here.
func intentAccountPlatformCompatible(groupPlatform, accountPlatform string) bool {
	switch groupPlatform {
	case PlatformAnthropic, PlatformGemini:
		return accountPlatform == groupPlatform || accountPlatform == PlatformAntigravity
	case PlatformOpenAI, PlatformAntigravity:
		return accountPlatform == groupPlatform
	default:
		return true
	}
}

// TestClassify runs a text through a group's saved router — enabled or not, so
// rules can be tuned before they affect traffic. Unlike Decide it reports
// failures, because here the caller is the admin who needs to see them.
func (s *IntentRouterService) TestClassify(ctx context.Context, groupID int64, text string) (*IntentClassifyTestResult, error) {
	text = stripIntentInjectedText(text)
	if text == "" {
		return nil, infraerrors.BadRequest("INTENT_TEST_TEXT_REQUIRED", "enter a message to classify")
	}
	row, err := s.entClient.IntentRouter.Query().Where(intentrouter.GroupIDEQ(groupID)).Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, infraerrors.NotFound("INTENT_ROUTER_NOT_FOUND", "save the router before testing it")
	}
	if err != nil {
		return nil, fmt.Errorf("load intent router: %w", err)
	}
	cfg := intentRouterConfigFromEnt(row)
	if len(cfg.activeRules()) == 0 {
		return nil, infraerrors.BadRequest("INTENT_NO_ACTIVE_RULES", "enable at least one rule first")
	}
	// Same order as live traffic: keywords first, the classifier only after.
	if rule, keyword, ok := cfg.matchKeywords(text); ok {
		return &IntentClassifyTestResult{MatchedBy: "keyword", Keyword: keyword, Intent: rule.Name, Understood: true, AccountIDs: rule.AccountIDs}, nil
	}
	// Then Jev — whenever a key is stored, even switched off, so a topped-up
	// account can be checked before Jev is turned back on. Its scope, turn
	// limit, sampling and pauses do not apply, and its errors here neither
	// pause nor switch it off: the admin is asking how this text is judged.
	var jevResult *IntentClassifyTestResult
	if cfg.Jev.APIKey != "" && s.judge != nil {
		started := s.now()
		judgeCtx, cancel := context.WithTimeout(ctx, cfg.Jev.Timeout)
		answer, err := s.judge.ProbeProbability(judgeCtx, cfg.Jev.APIKey, cfg.Jev.Model, text, cfg.Jev.HealthEnabled)
		cancel()
		latency := s.now().Sub(started).Milliseconds()
		if err != nil {
			meta := map[string]string{"latency_ms": fmt.Sprint(latency)}
			if status, account := intentJevAccountProblem(err); account {
				meta["status"] = fmt.Sprint(status)
			}
			return nil, infraerrors.BadRequest("INTENT_JEV_FAILED", "jev call failed: "+truncateRunes(err.Error(), 300)).WithMetadata(meta)
		}
		p := answer.P
		jevResult = &IntentClassifyTestResult{MatchedBy: "jev", Answer: fmt.Sprintf("p=%.2f", p), Understood: true, LatencyMS: latency, ProbeProbability: &p, HealthProbability: answer.Health, AccountIDs: []int64{}}
		if answer.Health != nil && *answer.Health >= cfg.Jev.HealthThreshold {
			// A health check: its own rule, or ordinary scheduling.
			jevResult.HealthCheck = true
			if rule, ok := cfg.rule(cfg.Jev.HealthRule); ok {
				jevResult.Intent, jevResult.AccountIDs = rule.Name, rule.AccountIDs
			}
			return jevResult, nil
		}
		if rule, ok := cfg.rule(cfg.Jev.Rule); ok && p >= cfg.Jev.Threshold {
			jevResult.Intent, jevResult.AccountIDs = rule.Name, rule.AccountIDs
			return jevResult, nil
		}
	}
	rules := cfg.classifierRules()
	if len(rules) == 0 {
		if jevResult != nil {
			return jevResult, nil
		}
		if !cfg.classifierReady() {
			for _, rule := range cfg.activeRules() {
				if len(rule.Keywords) == 0 && rule.Name != cfg.Jev.Rule {
					return nil, infraerrors.BadRequest("INTENT_CLASSIFIER_INCOMPLETE", "set the classifier model and API key first")
				}
			}
		}
		// Keyword-only router (or no rule the classifier may pick): no match.
		return &IntentClassifyTestResult{Understood: true, AccountIDs: []int64{}}, nil
	}
	started := s.now()
	classifyCtx, cancel := context.WithTimeout(ctx, cfg.ClassifierTimeout)
	defer cancel()
	answer, err := s.classifier.Classify(classifyCtx, cfg, truncateRunes(text, cfg.MaxInputChars))
	latency := s.now().Sub(started).Milliseconds()
	if err != nil {
		return nil, infraerrors.BadRequest("INTENT_CLASSIFIER_FAILED", "classifier call failed: "+truncateRunes(err.Error(), 300)).
			WithMetadata(map[string]string{"latency_ms": fmt.Sprint(latency)})
	}
	result := &IntentClassifyTestResult{MatchedBy: "classifier", Answer: truncateRunes(answer, 300), LatencyMS: latency, AccountIDs: []int64{}}
	result.Intent, result.Understood = matchIntentLabel(answer, rules)
	if rule, ok := cfg.rule(result.Intent); ok {
		result.AccountIDs = rule.AccountIDs
	}
	return result, nil
}

// ClearCache forgets remembered conversations of one group (or all, with 0).
func (s *IntentRouterService) ClearCache(ctx context.Context, groupID int64) (int64, error) {
	if s.store == nil {
		return 0, nil
	}
	removed, err := s.store.ClearSessions(ctx, groupID)
	if err != nil {
		return removed, fmt.Errorf("clear intent route cache: %w", err)
	}
	return removed, nil
}

func (s *IntentRouterService) RecentEvents(ctx context.Context, limit int) ([]IntentRouteEvent, error) {
	if s.store == nil {
		return []IntentRouteEvent{}, nil
	}
	events, err := s.store.ListEvents(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list intent route events: %w", err)
	}
	return events, nil
}
