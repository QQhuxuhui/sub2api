package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/ent/intentrouter"
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
)

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
	Rules                      []domain.IntentRule `json:"rules"`
	UpdatedAt                  time.Time           `json:"updated_at"`
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
	Rules               []domain.IntentRule `json:"rules"`
}

// IntentClassifyTestResult is the outcome of trying a text against a router.
type IntentClassifyTestResult struct {
	Answer     string  `json:"answer"`
	Intent     string  `json:"intent"`
	Understood bool    `json:"understood"`
	AccountIDs []int64 `json:"account_ids"`
	LatencyMS  int64   `json:"latency_ms"`
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
		Rules:                      rules,
		UpdatedAt:                  row.UpdatedAt,
	}
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
	return intentRouterViewFromEnt(row), nil
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
			SetMaxInputChars(in.MaxInputChars).SetRules(in.Rules).Save(ctx)
	} else {
		saved, err = s.entClient.IntentRouter.UpdateOneID(existing.ID).
			SetEnabled(in.Enabled).
			SetClassifierBaseURL(in.ClassifierBaseURL).SetClassifierAPIKey(in.ClassifierAPIKey).
			SetClassifierProtocol(in.ClassifierProtocol).SetClassifierModel(in.ClassifierModel).
			SetClassifierTimeoutMs(in.ClassifierTimeoutMS).SetCacheTTLSeconds(in.CacheTTLSeconds).
			SetMaxInputChars(in.MaxInputChars).SetRules(in.Rules).Save(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("save intent router: %w", err)
	}
	s.InvalidateConfig(ctx)
	return intentRouterViewFromEnt(saved), nil
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
	if in.Enabled && (in.ClassifierModel == "" || in.ClassifierAPIKey == "") {
		return bad("INTENT_CLASSIFIER_INCOMPLETE", "an enabled router needs a classifier model and API key")
	}

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
		if rule.Description == "" {
			return infraerrors.BadRequest("INTENT_RULE_DESCRIPTION_REQUIRED", "rule \""+rule.Name+"\" needs a description: it is what the classifier decides by").
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
	return s.validateIntentRuleAccounts(ctx, accountIDs, groupPlatform)
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
	text = strings.TrimSpace(text)
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
	if cfg.ClassifierModel == "" || cfg.ClassifierAPIKey == "" {
		return nil, infraerrors.BadRequest("INTENT_CLASSIFIER_INCOMPLETE", "set the classifier model and API key first")
	}
	if len(cfg.activeRules()) == 0 {
		return nil, infraerrors.BadRequest("INTENT_NO_ACTIVE_RULES", "enable at least one rule first")
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
	result := &IntentClassifyTestResult{Answer: truncateRunes(answer, 300), LatencyMS: latency, AccountIDs: []int64{}}
	result.Intent, result.Understood = matchIntentLabel(answer, cfg.activeRules())
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
