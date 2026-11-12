package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const openAIRuleTestKeyword = "must be passed back to the API"

var openAIRuleTestPassbackBody = `{"error":{"message":"The ` + "`reasoning_text`" + ` in the thinking mode must be passed back to the API. (request_id: 990fa4d9)","type":"invalid_request_error"}}`

// openAIRuleUpstream answers the accounts in reject with the DeepSeek
// reasoning pass-back 400 and every other account with a completed response.
type openAIRuleUpstream struct {
	service.HTTPUpstream
	mu     sync.Mutex
	reject map[int64]bool
	hits   []int64
}

func (u *openAIRuleUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.ReadAll(req.Body)
	}
	u.mu.Lock()
	u.hits = append(u.hits, accountID)
	reject := u.reject[accountID]
	u.mu.Unlock()
	if reject {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"req-up"}},
			Body:       io.NopCloser(strings.NewReader(openAIRuleTestPassbackBody)),
		}, nil
	}
	completed := `{"type":"response.completed","response":{"id":"resp_ok","object":"response","model":"deepseek-v4.1-flash","status":"completed",` +
		`"output":[{"type":"message","id":"msg_ok","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],` +
		`"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("event: response.completed\ndata: " + completed + "\n\n")),
	}, nil
}

func (u *openAIRuleUpstream) accountHits() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.hits...)
}

type openAIRuleSettingRepo struct {
	service.SettingRepository
	mu   sync.Mutex
	data map[string]string
}

func (r *openAIRuleSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.data[key]; ok {
		return v, nil
	}
	return "", service.ErrSettingNotFound
}

func (r *openAIRuleSettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[key] = value
	return nil
}

type openAIRuleHarness struct {
	handler  *OpenAIGatewayHandler
	router   *gin.Engine
	repo     *grokCredentialHandlerRepo
	upstream *openAIRuleUpstream
	cleanup  func()
}

// newOpenAIRuleHarness builds group 45 with two OpenAI API-key accounts. Both
// carry an account-level temp-unschedulable rule on the same 400, so any
// account handling shows up in repo.setTempIDs.
func newOpenAIRuleHarness(t *testing.T, reject map[int64]bool, rulesEnabled bool) *openAIRuleHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	groupID := int64(45)
	newAccount := func(id int64, priority int) service.Account {
		return service.Account{
			ID: id, Name: "acc", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Concurrency: 5, Priority: priority,
			Credentials: map[string]any{
				"api_key":                    "sk-test",
				"base_url":                   "https://upstream.example",
				"temp_unschedulable_enabled": true,
				"temp_unschedulable_rules": []any{map[string]any{
					"error_code": 400, "keywords": []any{"passed back"}, "duration_minutes": 10, "scope": "account",
				}},
			},
			Extra: map[string]any{"openai_responses_supported": true},
		}
	}
	repo := &grokCredentialHandlerRepo{accounts: []service.Account{newAccount(1184, 1), newAccount(1165, 2)}, missingOnGet: map[int64]bool{}}
	upstream := &openAIRuleUpstream{reject: reject}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.MaxAccountSwitches = 3
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	rateLimit := service.NewRateLimitService(repo, nil, cfg, nil, nil)
	gateway := service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), rateLimit, billingCache, upstream,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil,
		nil,
	)
	cache := &concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	}
	h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(cache), billingCache, &service.APIKeyService{}, nil, nil, nil, nil, cfg)
	settings := &openAIRuleSettingRepo{data: map[string]string{}}
	policyService := service.NewOpenAIFailoverPolicyService(settings, nil)
	enabled := []int64{}
	if rulesEnabled {
		enabled = []int64{groupID}
	}
	_, err := policyService.UpdateConfig(context.Background(), service.OpenAIFailoverConfig{
		EnabledGroupIDs: enabled,
		MaxRuleSwitches: 3,
		Rules: []service.OpenAIFailoverRule{{
			ID: "reasoning-passback", Enabled: true, StatusCode: 400,
			Keywords: []string{openAIRuleTestKeyword}, Action: service.OpenAIFailoverActionFailover,
		}},
	})
	require.NoError(t, err)
	h.openAIFailoverPolicyService = policyService

	apiKey := &service.APIKey{
		ID: 394, GroupID: &groupID,
		User:  &service.User{ID: 148, Status: service.StatusActive},
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, AllowMessagesDispatch: true},
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.User.ID, Concurrency: 5})
		c.Next()
	})
	router.POST("/v1/chat/completions", h.ChatCompletions)
	router.POST("/v1/messages", h.Messages)
	router.POST("/v1/responses", h.Responses)
	return &openAIRuleHarness{handler: h, router: router, repo: repo, upstream: upstream, cleanup: billingCache.Stop}
}

func (hh *openAIRuleHarness) do(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	hh.router.ServeHTTP(rec, req)
	return rec
}

var openAIRuleEndpoints = []struct {
	name string
	path string
	body string
}{
	{"chat completions", "/v1/chat/completions", `{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}],"stream":false}`},
	{"messages", "/v1/messages", `{"model":"deepseek-v4.1-flash","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`},
	{"responses", "/v1/responses", `{"model":"deepseek-v4.1-flash","input":"hi","stream":false}`},
}

func TestOpenAIFailoverRuleSwitchesAccountWithoutPenalty(t *testing.T) {
	for _, endpoint := range openAIRuleEndpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			hh := newOpenAIRuleHarness(t, map[int64]bool{1184: true}, true)
			defer hh.cleanup()

			rec := hh.do(t, endpoint.path, endpoint.body)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, []int64{1184, 1165}, hh.upstream.accountHits())
			require.Empty(t, hh.repo.setTempIDs, "rule switch must not touch the account")
			require.Empty(t, hh.repo.errorIDs())
			require.Empty(t, hh.repo.rateLimitedAccountIDs())
		})
	}
}

func TestOpenAIFailoverRuleExhaustedLooksLikeNoRules(t *testing.T) {
	for _, endpoint := range openAIRuleEndpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			both := map[int64]bool{1184: true, 1165: true}

			withRules := newOpenAIRuleHarness(t, both, true)
			defer withRules.cleanup()
			got := withRules.do(t, endpoint.path, endpoint.body)
			require.Equal(t, []int64{1184, 1165}, withRules.upstream.accountHits())
			require.Empty(t, withRules.repo.setTempIDs, "replaying the last error must not touch the account")

			// The same single upstream error without the rule table. The
			// account-level temp rule makes that path switch accounts by
			// itself, so the reference only lets the first account answer.
			reference := newOpenAIRuleHarness(t, map[int64]bool{1184: true}, false)
			defer reference.cleanup()
			reference.repo.accounts = reference.repo.accounts[:1]
			for i := range reference.repo.accounts {
				delete(reference.repo.accounts[i].Credentials, "temp_unschedulable_enabled")
			}
			want := reference.do(t, endpoint.path, endpoint.body)

			require.Equal(t, want.Code, got.Code, "got %s\nwant %s", got.Body.String(), want.Body.String())
			require.Equal(t, http.StatusBadRequest, got.Code)
			require.JSONEq(t, want.Body.String(), got.Body.String())
			require.Contains(t, got.Body.String(), openAIRuleTestKeyword)
		})
	}
}

func TestOpenAIFailoverRuleDisabledGroupKeepsBehaviour(t *testing.T) {
	hh := newOpenAIRuleHarness(t, map[int64]bool{1184: true}, false)
	defer hh.cleanup()

	rec := hh.do(t, "/v1/chat/completions", openAIRuleEndpoints[0].body)

	// Without the table the account's own temp rule decides: it is paused.
	require.Equal(t, []int64{1184}, hh.repo.setTempIDs)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestOpenAIFailoverRuleBudget(t *testing.T) {
	hh := newOpenAIRuleHarness(t, map[int64]bool{1184: true, 1165: true}, true)
	defer hh.cleanup()
	hh.repo.accounts = append(hh.repo.accounts, hh.repo.accounts[1], hh.repo.accounts[1])
	hh.repo.accounts[2].ID = 2001
	hh.repo.accounts[3].ID = 2002
	hh.upstream.reject[2001] = true
	hh.upstream.reject[2002] = true

	settings := &openAIRuleSettingRepo{data: map[string]string{}}
	policyService := service.NewOpenAIFailoverPolicyService(settings, nil)
	_, err := policyService.UpdateConfig(context.Background(), service.OpenAIFailoverConfig{
		EnabledGroupIDs: []int64{45}, MaxRuleSwitches: 1,
		Rules: []service.OpenAIFailoverRule{{ID: "r", Enabled: true, StatusCode: 400, Keywords: []string{openAIRuleTestKeyword}, Action: service.OpenAIFailoverActionFailover}},
	})
	require.NoError(t, err)
	hh.handler.openAIFailoverPolicyService = policyService
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(openAIRuleEndpoints[0].body))
	req.Header.Set("Content-Type", "application/json")
	hh.router.ServeHTTP(rec, req)

	require.Len(t, hh.upstream.accountHits(), 2, "max_rule_switches=1 allows one switch")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
}
