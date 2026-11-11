//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

const relayNoChannel422 = `{"error":{"aihubx":{"code":"422001","retryable":false},"message":"请求模型 [claude-opus-5-5] 在 API Key 路由分组 [claude-max] 中暂无可用渠道。","type":"invalid_request_error"},"type":"error"}`

func anthropicFailoverRule(id string, status int, action string, keywords ...string) AnthropicFailoverRule {
	return AnthropicFailoverRule{ID: id, Name: id, Enabled: true, StatusCode: status, Keywords: keywords, Action: action}
}

func anthropicFailoverPolicyForTest(t *testing.T, rules ...AnthropicFailoverRule) *AnthropicFailoverPolicy {
	t.Helper()
	cfg, err := NormalizeAnthropicFailoverConfig(AnthropicFailoverConfig{MaxRuleSwitches: 3, Rules: rules})
	require.NoError(t, err)
	return newAnthropicFailoverPolicy(cfg)
}

func TestNormalizeAnthropicFailoverConfig(t *testing.T) {
	valid := func() AnthropicFailoverConfig {
		return AnthropicFailoverConfig{
			EnabledGroupIDs: []int64{18, 18, 10},
			MaxRuleSwitches: 3,
			Rules:           []AnthropicFailoverRule{anthropicFailoverRule("", 422, AnthropicFailoverActionFailover, " 暂无可用渠道 ", " ")},
		}
	}
	cfg, err := NormalizeAnthropicFailoverConfig(valid())
	require.NoError(t, err)
	require.Equal(t, []int64{18, 10}, cfg.EnabledGroupIDs)
	require.NotEmpty(t, cfg.Rules[0].ID, "an empty id is generated")
	require.Equal(t, []string{"暂无可用渠道"}, cfg.Rules[0].Keywords)

	for name, mutate := range map[string]func(*AnthropicFailoverConfig){
		"switches too low":  func(c *AnthropicFailoverConfig) { c.MaxRuleSwitches = 0 },
		"switches too high": func(c *AnthropicFailoverConfig) { c.MaxRuleSwitches = 11 },
		"bad group":         func(c *AnthropicFailoverConfig) { c.EnabledGroupIDs = []int64{0} },
		"status not 4xx":    func(c *AnthropicFailoverConfig) { c.Rules[0].StatusCode = 500 },
		"status 401":        func(c *AnthropicFailoverConfig) { c.Rules[0].StatusCode = 401 },
		"status 403":        func(c *AnthropicFailoverConfig) { c.Rules[0].StatusCode = 403 },
		"status 429":        func(c *AnthropicFailoverConfig) { c.Rules[0].StatusCode = 429 },
		"no keyword":        func(c *AnthropicFailoverConfig) { c.Rules[0].Keywords = []string{" "} },
		"bad action":        func(c *AnthropicFailoverConfig) { c.Rules[0].Action = "retry" },
		"colon in id":       func(c *AnthropicFailoverConfig) { c.Rules[0].ID = "a:b" },
		"duplicate id":      func(c *AnthropicFailoverConfig) { c.Rules[0].ID = "x"; c.Rules = append(c.Rules, c.Rules[0]) },
		"too many keywords": func(c *AnthropicFailoverConfig) {
			c.Rules[0].Keywords = make([]string, 21)
			fillKeywords(c.Rules[0].Keywords)
		},
		"too many rules":       func(c *AnthropicFailoverConfig) { c.Rules = make([]AnthropicFailoverRule, 101) },
		"status-only wildcard": func(c *AnthropicFailoverConfig) { c.Rules[0].StatusCode = 0; c.Rules[0].Keywords = nil },
	} {
		cfg := valid()
		mutate(&cfg)
		_, err := NormalizeAnthropicFailoverConfig(cfg)
		require.Error(t, err, name)
	}
}

func fillKeywords(keywords []string) {
	for i := range keywords {
		keywords[i] = "k" + string(rune('a'+i))
	}
}

func TestAnthropicFailoverPolicyMatch(t *testing.T) {
	policy := anthropicFailoverPolicyForTest(t,
		anthropicFailoverRule("shield", 400, AnthropicFailoverActionReturn, "prompt is too long"),
		anthropicFailoverRule("any-400", 400, AnthropicFailoverActionFailover, "prompt", "会话不可继续"),
		anthropicFailoverRule("no-channel", 0, AnthropicFailoverActionFailover, "暂无可用渠道"),
		AnthropicFailoverRule{ID: "off", Enabled: false, StatusCode: 404, Keywords: []string{"model"}, Action: AnthropicFailoverActionFailover},
	)
	require.Equal(t, "", policy.match(400, "Prompt is TOO long: 300000 tokens"), "the return rule above shields the broader one")
	require.Equal(t, "any-400", policy.match(400, "PROMPT rejected"))
	require.Equal(t, "any-400", policy.match(400, "当前会话不可继续使用"))
	require.Equal(t, "no-channel", policy.match(422, "…暂无可用渠道。"), "status 0 matches any status")
	require.Equal(t, "no-channel", policy.match(400, "暂无可用渠道"))
	require.Equal(t, "", policy.match(404, "model not found"), "disabled rules never match")
	require.Equal(t, "", policy.match(422, "something else"))
	require.Equal(t, "", (*AnthropicFailoverPolicy)(nil).match(422, "暂无可用渠道"))
	require.Zero(t, (*AnthropicFailoverPolicy)(nil).MaxRuleSwitches())
}

func TestAnthropicFailoverMatchTextUsesStructuredFields(t *testing.T) {
	body := []byte(`{"error":{"message":"bad request","type":"invalid_request_error","code":"E42","echo":"暂无可用渠道"}}`)
	text := anthropicFailoverMatchText(body)
	require.Contains(t, text, "bad request")
	require.Contains(t, text, "invalid_request_error")
	require.Contains(t, text, "E42")
	require.NotContains(t, text, "暂无可用渠道", "the rest of the body can echo request content")

	require.Equal(t, "<html>暂无可用渠道</html>", anthropicFailoverMatchText([]byte("<html>暂无可用渠道</html>")), "unstructured bodies fall back to their head")
	long := bytes.Repeat([]byte("x"), anthropicFailoverMatchBodyFallbackBytes+100)
	require.Len(t, anthropicFailoverMatchText(long), anthropicFailoverMatchBodyFallbackBytes)
}

func newRuleTestContext(policy *AnthropicFailoverPolicy) (*gin.Context, *httptest.ResponseRecorder, context.Context) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	ctx := context.Background()
	if policy != nil {
		ctx = WithAnthropicFailoverPolicy(ctx, policy)
	}
	return c, rec, ctx
}

func upstreamErrorResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
	}
}

func TestTryFailoverByRule(t *testing.T) {
	policy := anthropicFailoverPolicyForTest(t, anthropicFailoverRule("no-channel", 422, AnthropicFailoverActionFailover, "暂无可用渠道"))
	svc := failoverOn400TestService(false)
	repo := &rateLimitAccountRepoStub{}
	svc.rateLimitService = &RateLimitService{accountRepo: repo}
	account := newAnthropicAPIKeyAccountForTest()

	c, rec, ctx := newRuleTestContext(policy)
	failoverErr := svc.tryFailoverByRule(ctx, c, upstreamErrorResponse(422, relayNoChannel422), account, true, false)
	require.NotNil(t, failoverErr)
	require.Equal(t, 422, failoverErr.StatusCode)
	require.Equal(t, "no-channel", failoverErr.FailoverRuleID)
	require.True(t, failoverErr.RuleRetryExhausted)
	require.False(t, failoverErr.RetryableOnSameAccount, "no handler-level same-account retry")
	require.False(t, failoverErr.SafeToFailoverAfterWrite)
	require.JSONEq(t, relayNoChannel422, string(failoverErr.ResponseBody))
	require.False(t, rec.Flushed)
	require.False(t, c.Writer.Written(), "the handler decides what the client sees")
	require.Zero(t, repo.setErrorCalls+repo.tempCalls+repo.rateLimitedCalls, "a rule switch never touches the account")
	events, _ := c.Get(OpsUpstreamErrorsKey)
	require.Contains(t, failoverTestJSON(t, events), `"kind":"failover:rule:no-channel"`, "the ops failover counters key on the first segment")

	for name, tc := range map[string]struct {
		policy *AnthropicFailoverPolicy
		status int
		body   string
	}{
		"group not opted in":      {nil, 422, relayNoChannel422},
		"keyword missing":         {policy, 422, `{"error":{"message":"other"}}`},
		"other status":            {policy, 400, relayNoChannel422},
		"already fails over":      {anthropicFailoverPolicyForTest(t, anthropicFailoverRule("r", 0, AnthropicFailoverActionFailover, "暂无可用渠道")), 403, relayNoChannel422},
		"5xx is out of scope":     {anthropicFailoverPolicyForTest(t, anthropicFailoverRule("r", 0, AnthropicFailoverActionFailover, "暂无可用渠道")), 503, relayNoChannel422},
		"success is not an error": {anthropicFailoverPolicyForTest(t, anthropicFailoverRule("r", 0, AnthropicFailoverActionFailover, "暂无可用渠道")), 200, relayNoChannel422},
		"return rule":             {anthropicFailoverPolicyForTest(t, anthropicFailoverRule("r", 422, AnthropicFailoverActionReturn, "暂无可用渠道")), 422, relayNoChannel422},
	} {
		c, _, ctx := newRuleTestContext(tc.policy)
		resp := upstreamErrorResponse(tc.status, tc.body)
		require.Nil(t, svc.tryFailoverByRule(ctx, c, resp, account, false, false), name)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, tc.body, string(body), "%s: the body stays readable for the ordinary handling", name)
	}
}

func failoverTestJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

// freshResponseUpstream answers every attempt with a new copy of the response,
// so same-account retries see the body too.
type freshResponseUpstream struct {
	mu     sync.Mutex
	status int
	body   string
	calls  int
}

func (u *freshResponseUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	if req != nil && req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	return upstreamErrorResponse(u.status, u.body), nil
}

func (u *freshResponseUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func forwardForRuleTest(t *testing.T, ctx context.Context, c *gin.Context, upstream *freshResponseUpstream, passthrough bool, customErrorCodes bool) (*ForwardResult, error, *rateLimitAccountRepoStub) {
	t.Helper()
	repo := &rateLimitAccountRepoStub{}
	svc := failoverOn400TestService(false)
	svc.rateLimitService = &RateLimitService{accountRepo: repo}
	svc.settingService = NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{}}, svc.cfg)
	svc.httpUpstream = upstream
	account := newAnthropicAPIKeyAccountForTest()
	account.Extra["anthropic_passthrough"] = passthrough
	account.Credentials["pool_mode"] = true
	if customErrorCodes {
		account.Credentials["custom_error_codes_enabled"] = true
		account.Credentials["custom_error_codes"] = []any{float64(418)}
	}
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"x","messages":[{"role":"user","content":"hello"}]}`)), PlatformAnthropic)
	require.NoError(t, err)
	result, err := svc.Forward(ctx, c, account, parsed)
	return result, err, repo
}

func TestForwardRuleFailoverOn422(t *testing.T) {
	policy := anthropicFailoverPolicyForTest(t, anthropicFailoverRule("no-channel", 422, AnthropicFailoverActionFailover, "暂无可用渠道"))
	for _, route := range []struct {
		name        string
		passthrough bool
	}{{"standard", false}, {"passthrough", true}} {
		t.Run(route.name+"/matched", func(t *testing.T) {
			c, _, ctx := newRuleTestContext(policy)
			upstream := &freshResponseUpstream{status: 422, body: relayNoChannel422}
			result, err, repo := forwardForRuleTest(t, ctx, c, upstream, route.passthrough, false)
			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, "no-channel", failoverErr.FailoverRuleID)
			require.False(t, failoverErr.RuleRetryExhausted)
			require.False(t, c.Writer.Written())
			require.Equal(t, 1, upstream.calls)
			require.Zero(t, repo.setErrorCalls+repo.tempCalls+repo.rateLimitedCalls)
		})
		t.Run(route.name+"/group not opted in", func(t *testing.T) {
			c, rec, ctx := newRuleTestContext(nil)
			upstream := &freshResponseUpstream{status: 422, body: relayNoChannel422}
			_, err, _ := forwardForRuleTest(t, ctx, c, upstream, route.passthrough, false)
			require.Error(t, err)
			require.False(t, errorsAsFailover(err))
			require.Equal(t, http.StatusBadGateway, rec.Code, "unchanged: the customer gets the mapped error")
		})
	}
}

// The existing in-Forward retries on the same account run first; the rule only
// replaces what used to be the final answer.
func TestForwardRuleFailoverAfterSameAccountRetries(t *testing.T) {
	policy := anthropicFailoverPolicyForTest(t, anthropicFailoverRule("no-channel", 422, AnthropicFailoverActionFailover, "暂无可用渠道"))
	c, _, ctx := newRuleTestContext(policy)
	upstream := &freshResponseUpstream{status: 422, body: relayNoChannel422}
	_, err, repo := forwardForRuleTest(t, ctx, c, upstream, false, true)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, "no-channel", failoverErr.FailoverRuleID)
	require.True(t, failoverErr.RuleRetryExhausted)
	require.Equal(t, maxRetryAttempts, upstream.calls)
	require.Zero(t, repo.setErrorCalls+repo.tempCalls+repo.rateLimitedCalls)
}

// The env keyword failover for 400 decides first; the table can not shield it.
func TestForwardEnv400FailoverPrecedesRuleTable(t *testing.T) {
	policy := anthropicFailoverPolicyForTest(t, anthropicFailoverRule("shield", 400, AnthropicFailoverActionReturn, "第三方中转"))
	c, _, ctx := newRuleTestContext(policy)
	svc := failoverOn400TestService(true, "第三方中转")
	svc.rateLimitService = &RateLimitService{accountRepo: &rateLimitAccountRepoStub{}}
	svc.settingService = NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{}}, svc.cfg)
	svc.httpUpstream = &freshResponseUpstream{status: 400, body: relayClientCheck400}
	account := newAnthropicAPIKeyAccountForTest()
	account.Extra["anthropic_passthrough"] = false
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"x","messages":[{"role":"user","content":"hello"}]}`)), PlatformAnthropic)
	require.NoError(t, err)
	_, err = svc.Forward(ctx, c, account, parsed)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Empty(t, failoverErr.FailoverRuleID)
}

func TestPresentUpstreamHTTPError(t *testing.T) {
	const upstream400 = "{\n  \"type\": \"error\",\n  \"error\": {\"type\": \"invalid_request_error\", \"message\": \"当前会话不可继续使用\"}\n}"
	for _, tc := range []struct {
		name           string
		status         int
		body           string
		retryExhausted bool
		wantCode       int
		wantJSON       string
		wantRaw        string
		wantFrame      string
	}{
		{name: "400 passes the upstream body through", status: 400, body: upstream400, wantCode: 400, wantRaw: upstream400,
			wantFrame: `{"type":"error","error":{"type":"invalid_request_error","message":"当前会话不可继续使用"}}`},
		{name: "422 maps to the generic 502", status: 422, body: relayNoChannel422, wantCode: 502,
			wantJSON: `{"type":"error","error":{"type":"upstream_error","message":"Upstream request failed"}}`},
		{name: "retry exhausted", status: 422, body: relayNoChannel422, retryExhausted: true, wantCode: 502,
			wantJSON: `{"type":"error","error":{"type":"upstream_error","message":"Upstream request failed after retries"}}`},
		{name: "retry exhausted 400 is not raw", status: 400, body: upstream400, retryExhausted: true, wantCode: 502,
			wantJSON: `{"type":"error","error":{"type":"upstream_error","message":"Upstream request failed after retries"}}`},
		{name: "404", status: 404, body: `{"error":{"message":"nope"}}`, wantCode: 502,
			wantJSON: `{"type":"error","error":{"type":"upstream_error","message":"Upstream request failed"}}`},
	} {
		t.Run(tc.name+"/not started", func(t *testing.T) {
			c, rec, _ := newRuleTestContext(nil)
			presentUpstreamHTTPError(c, PlatformAnthropic, tc.status, []byte(tc.body), tc.retryExhausted)
			require.Equal(t, tc.wantCode, rec.Code)
			require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
			if tc.wantRaw != "" {
				require.Equal(t, tc.wantRaw, rec.Body.String(), "byte for byte")
			} else {
				require.JSONEq(t, tc.wantJSON, rec.Body.String())
			}
		})
		t.Run(tc.name+"/stream started", func(t *testing.T) {
			c, rec, _ := newRuleTestContext(nil)
			startSSEForTest(c)
			presentUpstreamHTTPError(c, PlatformAnthropic, tc.status, []byte(tc.body), tc.retryExhausted)
			require.Equal(t, http.StatusOK, rec.Code)
			want := tc.wantJSON
			if tc.wantFrame != "" {
				want = tc.wantFrame
			}
			frames := sseFramesAfterPing(t, rec.Body.String())
			require.Len(t, frames, 1, "exactly one terminal error frame: %q", rec.Body.String())
			require.Equal(t, "error", frames[0].event)
			require.JSONEq(t, want, frames[0].data)
			require.True(t, IsResponseCommitted(c), "the handler must not append another frame")
		})
	}
}

func startSSEForTest(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Status(http.StatusOK)
	_, _ = c.Writer.WriteString(ssePingFrame)
	c.Writer.Flush()
}

type sseTestFrame struct{ event, data string }

// sseFramesAfterPing parses the whole body as SSE: every line must be a valid
// field line, so raw JSON appended to the stream fails the test.
func sseFramesAfterPing(t *testing.T, body string) []sseTestFrame {
	t.Helper()
	var frames []sseTestFrame
	for _, block := range bytes.Split([]byte(body), []byte("\n\n")) {
		if len(bytes.TrimSpace(block)) == 0 {
			continue
		}
		var frame sseTestFrame
		for _, line := range bytes.Split(block, []byte("\n")) {
			switch {
			case bytes.HasPrefix(line, []byte("event: ")):
				frame.event = string(bytes.TrimPrefix(line, []byte("event: ")))
			case bytes.HasPrefix(line, []byte("data: ")):
				frame.data = string(bytes.TrimPrefix(line, []byte("data: ")))
			default:
				t.Fatalf("not an SSE field line: %q in %q", line, body)
			}
		}
		if frame.event == "ping" {
			continue
		}
		frames = append(frames, frame)
	}
	return frames
}

func TestWriteUpstreamErrorBodyInvalidJSONInStream(t *testing.T) {
	c, rec, _ := newRuleTestContext(nil)
	startSSEForTest(c)
	writeUpstreamErrorBody(c, 400, []byte("<html>bad</html>"))
	frames := sseFramesAfterPing(t, rec.Body.String())
	require.Len(t, frames, 1)
	require.JSONEq(t, `{"type":"error","error":{"type":"upstream_error","message":"Upstream request failed"}}`, frames[0].data)
}

func TestWriteRuleFailoverExhausted(t *testing.T) {
	svc := failoverOn400TestService(false)
	c, rec, _ := newRuleTestContext(nil)
	svc.WriteRuleFailoverExhausted(c, PlatformAnthropic, &UpstreamFailoverError{StatusCode: 400, ResponseBody: []byte(`{"error":{"message":"当前会话不可继续使用"}}`), FailoverRuleID: "r"})
	require.Equal(t, 400, rec.Code)
	require.Equal(t, `{"error":{"message":"当前会话不可继续使用"}}`, rec.Body.String())

	c, rec, _ = newRuleTestContext(nil)
	svc.WriteRuleFailoverExhausted(c, PlatformAnthropic, &UpstreamFailoverError{StatusCode: 422, ResponseBody: []byte(relayNoChannel422), FailoverRuleID: "r", RuleRetryExhausted: true})
	require.Equal(t, 502, rec.Code)
	require.Contains(t, rec.Body.String(), "Upstream request failed after retries")
}

// --- policy service ---

type failoverSettingRepo struct {
	gatewayTTLSettingRepo
	mu        sync.Mutex
	getErr    error
	beforeGet func()
}

func (r *failoverSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	if r.beforeGet != nil {
		r.beforeGet()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return "", r.getErr
	}
	v, ok := r.data[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return v, nil
}

func (r *failoverSettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[key] = value
	return nil
}

func newFailoverSettingRepo() *failoverSettingRepo {
	return &failoverSettingRepo{gatewayTTLSettingRepo: gatewayTTLSettingRepo{data: map[string]string{}}}
}

func groupID(id int64) *int64 { return &id }

func TestAnthropicFailoverPolicyServiceDefaultsAndUpdate(t *testing.T) {
	repo := newFailoverSettingRepo()
	svc := NewAnthropicFailoverPolicyService(repo, nil)
	ctx := context.Background()

	require.Nil(t, svc.PolicyForGroup(ctx, groupID(18)), "nothing saved: every group is off")
	require.Nil(t, svc.PolicyForGroup(ctx, nil))
	require.Nil(t, (*AnthropicFailoverPolicyService)(nil).PolicyForGroup(ctx, groupID(18)))
	cfg, err := svc.Config(ctx)
	require.NoError(t, err)
	require.Empty(t, cfg.EnabledGroupIDs)
	require.Empty(t, cfg.Rules)
	require.Equal(t, 3, cfg.MaxRuleSwitches)

	_, err = svc.UpdateConfig(ctx, AnthropicFailoverConfig{MaxRuleSwitches: 0})
	require.Error(t, err)
	require.Nil(t, svc.PolicyForGroup(ctx, groupID(18)), "a rejected save changes nothing")

	saved, err := svc.UpdateConfig(ctx, AnthropicFailoverConfig{
		EnabledGroupIDs: []int64{18},
		MaxRuleSwitches: 2,
		Rules:           []AnthropicFailoverRule{anthropicFailoverRule("no-channel", 422, AnthropicFailoverActionFailover, "暂无可用渠道")},
	})
	require.NoError(t, err)
	require.Len(t, saved.Rules, 1)
	policy := svc.PolicyForGroup(ctx, groupID(18))
	require.NotNil(t, policy, "a save applies at once on this instance")
	require.Equal(t, 2, policy.MaxRuleSwitches())
	require.Equal(t, "no-channel", policy.match(422, "暂无可用渠道"))
	require.Nil(t, svc.PolicyForGroup(ctx, groupID(10)))
	require.Contains(t, repo.data[SettingKeyAnthropicFailoverPolicy], "no-channel")

	overview, err := svc.Overview(ctx)
	require.NoError(t, err, "no redis: config only")
	require.Len(t, overview.Config.Rules, 1)
	require.Empty(t, overview.Recent)
}

func TestAnthropicFailoverPolicyServiceFailsSafe(t *testing.T) {
	ctx := context.Background()

	repo := newFailoverSettingRepo()
	repo.data[SettingKeyAnthropicFailoverPolicy] = `{"enabled_group_ids":[18],"max_rule_switches":3,"rules":[{"id":"x","enabled":true,"status_code":500,"keywords":["k"],"action":"failover"}]}`
	svc := NewAnthropicFailoverPolicyService(repo, nil)
	require.Nil(t, svc.PolicyForGroup(ctx, groupID(18)), "an invalid stored value turns every group off")
	overview, err := svc.Overview(ctx)
	require.NoError(t, err, "the admin page stays usable to fix it")
	require.Empty(t, overview.Config.Rules)

	// A read error after a good load keeps the previous rules.
	repo = newFailoverSettingRepo()
	svc = NewAnthropicFailoverPolicyService(repo, nil)
	_, err = svc.UpdateConfig(ctx, AnthropicFailoverConfig{EnabledGroupIDs: []int64{18}, MaxRuleSwitches: 3,
		Rules: []AnthropicFailoverRule{anthropicFailoverRule("r", 422, AnthropicFailoverActionFailover, "k")}})
	require.NoError(t, err)
	repo.mu.Lock()
	repo.getErr = errors.New("db down")
	repo.mu.Unlock()
	svc.reload(ctx)
	require.NotNil(t, svc.PolicyForGroup(ctx, groupID(18)))
}

// A refresh that read the old value must not put it back after a save.
func TestAnthropicFailoverPolicyServiceStaleReloadDoesNotOverrideSave(t *testing.T) {
	ctx := context.Background()
	repo := newFailoverSettingRepo()
	svc := NewAnthropicFailoverPolicyService(repo, nil)
	require.Nil(t, svc.PolicyForGroup(ctx, groupID(18)))

	saved := make(chan struct{})
	var once sync.Once
	repo.beforeGet = func() {
		once.Do(func() {
			// The refresh has taken its version; now a save lands before it reads.
			go func() {
				defer close(saved)
				_, err := svc.UpdateConfig(context.Background(), AnthropicFailoverConfig{EnabledGroupIDs: []int64{18}, MaxRuleSwitches: 3})
				require.NoError(t, err)
			}()
			// Hand the refresh the value from before the save.
			repo.mu.Lock()
			repo.getErr = ErrSettingNotFound
			repo.mu.Unlock()
			select {
			case <-saved:
			case <-time.After(2 * time.Second):
				t.Error("save did not finish")
			}
		})
	}
	svc.reload(ctx)
	repo.beforeGet = nil
	repo.mu.Lock()
	repo.getErr = nil
	repo.mu.Unlock()
	require.NotNil(t, svc.PolicyForGroup(ctx, groupID(18)), "the saved config survives the stale refresh")
}

func TestAnthropicFailoverPolicyServiceConcurrentSavesStayConsistent(t *testing.T) {
	ctx := context.Background()
	repo := newFailoverSettingRepo()
	svc := NewAnthropicFailoverPolicyService(repo, nil)
	var wg sync.WaitGroup
	for i := 1; i <= 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := svc.UpdateConfig(ctx, AnthropicFailoverConfig{EnabledGroupIDs: []int64{int64(n)}, MaxRuleSwitches: n})
			require.NoError(t, err)
		}(i)
	}
	wg.Wait()
	var stored AnthropicFailoverConfig
	require.NoError(t, json.Unmarshal([]byte(repo.data[SettingKeyAnthropicFailoverPolicy]), &stored))
	require.NotNil(t, svc.PolicyForGroup(ctx, groupID(stored.EnabledGroupIDs[0])), "the snapshot is the stored value")
	require.Equal(t, stored.MaxRuleSwitches, svc.PolicyForGroup(ctx, groupID(stored.EnabledGroupIDs[0])).MaxRuleSwitches())
}

func TestAnthropicFailoverPolicyServiceStats(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	svc := NewAnthropicFailoverPolicyService(newFailoverSettingRepo(), rdb)
	_, err := svc.UpdateConfig(ctx, AnthropicFailoverConfig{EnabledGroupIDs: []int64{18}, MaxRuleSwitches: 3,
		Rules: []AnthropicFailoverRule{anthropicFailoverRule("no-channel", 422, AnthropicFailoverActionFailover, "暂无可用渠道")}})
	require.NoError(t, err)

	svc.writeOutcome(ctx, AnthropicFailoverOutcome{GroupID: 18, Reason: "no-channel", StatusCode: 422, Switches: 1, Accounts: []int64{1128, 1156}, Recovered: true})
	svc.writeOutcome(ctx, AnthropicFailoverOutcome{GroupID: 18, Reason: "no-channel", StatusCode: 422, Switches: 2, Accounts: []int64{1128}})
	svc.writeOutcome(ctx, AnthropicFailoverOutcome{GroupID: 18, Reason: AnthropicFailoverReasonStream, StatusCode: 429, Switches: 1, Accounts: []int64{1177}, Recovered: true})
	svc.RecordOutcome(AnthropicFailoverOutcome{}) // no reason: nothing recorded

	overview, err := svc.Overview(ctx)
	require.NoError(t, err)
	require.Equal(t, []AnthropicFailoverRuleStats{{RuleID: "no-channel", Hits: 2, Recovered: 1}}, overview.Stats.Rules)
	require.EqualValues(t, 1, overview.Stats.StreamHits)
	require.EqualValues(t, 1, overview.Stats.StreamRecovered)
	require.Len(t, overview.Recent, 3)
	require.Equal(t, AnthropicFailoverReasonStream, overview.Recent[0].Reason, "newest first")
	require.Equal(t, []int64{1177}, overview.Recent[0].Accounts)

	(*AnthropicFailoverPolicyService)(nil).RecordOutcome(AnthropicFailoverOutcome{Reason: "x"})
	NewAnthropicFailoverPolicyService(newFailoverSettingRepo(), nil).RecordOutcome(AnthropicFailoverOutcome{Reason: "x"})
}
