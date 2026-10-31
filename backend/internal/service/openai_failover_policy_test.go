//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func failoverRule(status int, action string, keywords ...string) OpenAIFailoverRule {
	return OpenAIFailoverRule{ID: "r", Enabled: true, StatusCode: status, Keywords: keywords, Action: action}
}

func TestOpenAIFailoverPolicyMatch(t *testing.T) {
	policy := newOpenAIFailoverPolicy(OpenAIFailoverConfig{Rules: []OpenAIFailoverRule{
		{ID: "off", Enabled: false, Keywords: []string{"boom"}, Action: OpenAIFailoverActionFailover},
		{ID: "keep", Enabled: true, Keywords: []string{"must contain the word"}, Action: OpenAIFailoverActionReturn},
		{ID: "any", Enabled: true, Keywords: []string{"Could Not Be Verified", "continuation"}, Action: OpenAIFailoverActionFailover},
		{ID: "only-400", Enabled: true, StatusCode: 400, Keywords: []string{"invalid parameters"}, Action: OpenAIFailoverActionFailover},
		{ID: "only-status", Enabled: true, StatusCode: 408, Action: OpenAIFailoverActionFailover},
	}})

	require.Equal(t, "any", policy.match(400, "item could not be verified"), "case insensitive")
	require.Equal(t, "any", policy.match(0, "encrypted continuation context"), "no status: streams too")
	require.Equal(t, "only-400", policy.match(400, "Invalid parameters. Please modify"))
	require.Empty(t, policy.match(0, "Invalid parameters"), "a set status never matches a stream failure")
	require.Equal(t, "only-status", policy.match(408, "anything"))
	require.Empty(t, policy.match(400, "boom"), "disabled rule")
	require.Empty(t, policy.match(400, "must contain the word json, could not be verified"), "first match decides")
	require.Empty(t, (*OpenAIFailoverPolicy)(nil).match(400, "x"))
}

func TestNormalizeOpenAIFailoverConfig(t *testing.T) {
	base := defaultOpenAIFailoverConfig()

	normalized, err := NormalizeOpenAIFailoverConfig(base)
	require.NoError(t, err)
	require.Len(t, normalized.Rules, len(DefaultOpenAIFailoverRules()))

	for name, change := range map[string]func(*OpenAIFailoverConfig){
		"wait too short": func(c *OpenAIFailoverConfig) { c.SlotWaitMs = 1 },
		"zero rule cap":  func(c *OpenAIFailoverConfig) { c.MaxRuleSwitches = 0 },
		"negative slot":  func(c *OpenAIFailoverConfig) { c.MaxSlotSwitches = -1 },
		"bad group":      func(c *OpenAIFailoverConfig) { c.EnabledGroupIDs = []int64{0} },
		"status 500": func(c *OpenAIFailoverConfig) {
			c.Rules = []OpenAIFailoverRule{failoverRule(500, OpenAIFailoverActionFailover, "x")}
		},
		"bad action": func(c *OpenAIFailoverConfig) { c.Rules = []OpenAIFailoverRule{failoverRule(400, "ignore", "x")} },
		"empty condition": func(c *OpenAIFailoverConfig) {
			c.Rules = []OpenAIFailoverRule{failoverRule(0, OpenAIFailoverActionFailover, "  ")}
		},
		"duplicate id": func(c *OpenAIFailoverConfig) {
			c.Rules = []OpenAIFailoverRule{failoverRule(400, OpenAIFailoverActionFailover), failoverRule(404, OpenAIFailoverActionFailover)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			change(&cfg)
			_, err := NormalizeOpenAIFailoverConfig(cfg)
			require.Error(t, err)
		})
	}

	cfg := base
	cfg.EnabledGroupIDs = []int64{4, 4, 3}
	cfg.Rules = []OpenAIFailoverRule{{Enabled: true, Keywords: []string{" a ", ""}, Action: OpenAIFailoverActionFailover}}
	normalized, err = NormalizeOpenAIFailoverConfig(cfg)
	require.NoError(t, err)
	require.Equal(t, []int64{4, 3}, normalized.EnabledGroupIDs)
	require.Equal(t, []string{"a"}, normalized.Rules[0].Keywords)
	require.NotEmpty(t, normalized.Rules[0].ID)
}

func TestDefaultOpenAIFailoverRulesCoverObservedErrors(t *testing.T) {
	policy := newOpenAIFailoverPolicy(defaultOpenAIFailoverConfig())
	for _, c := range []struct {
		status int
		text   string
		rule   string
	}{
		{0, "Encrypted function output content could not be decrypted or decoded.", "encrypted-content"},
		{400, "The encrypted content for item amsg_x could not be verified. Reason: encrypted content hydration failed", "encrypted-content"},
		{400, "The upstream could not validate encrypted continuation context. Rebuild the conversation", "encrypted-content"},
		{400, "stream error: stream disconnected before completion: stream closed", "stream-disconnected"},
		{400, "The ChatGPT user has reached their subscription sharing usage limit", "sharing-usage-limit"},
		{400, "Unsupported parameter: enable_thinking", "enable-thinking"},
		{400, "Invalid parameters. Please modify and try again.", "invalid-parameters"},
		{400, "x-openai-internal-codex-responses-lite requires reasoning.context", "responses-lite"},
		{400, "Upstream request failed", "upstream-request-failed"},
		{408, "request body read timed out", "http-408"},
	} {
		require.Equal(t, c.rule, policy.match(c.status, c.text), c.text)
	}
	for _, text := range []string{
		"Your input exceeds the context window of this model",
		"Response input messages must contain the word 'json' in some form",
		"The image you provided requires 33919 patches after processing",
		"Invalid prompt: your prompt was flagged as potentially violating our usage policy",
		"This content was flagged for possible biological risk",
	} {
		require.Empty(t, policy.match(400, text), text)
	}
	require.Empty(t, policy.match(404, "Item with id 'ig_1' not found"), "404 stays with the existing logic")
}

func TestRewriteOpenAIPlaintextEncryptedContent(t *testing.T) {
	body := []byte(`{"input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"encrypted_content agent_message"}]},
		{"type":"agent_message","content":[{"type":"input_text","text":"head"},{"type":"encrypted_content","encrypted_content":"plain \"quoted\" 中文"}]},
		{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"gAAAAABcipher"}]},
		{"type":"reasoning","encrypted_content":"not-touched"}
	]}`)
	out, count := RewriteOpenAIPlaintextEncryptedContent(body)
	require.Equal(t, 1, count)
	require.True(t, gjson.ValidBytes(out))
	part := gjson.GetBytes(out, "input.1.content.1")
	require.Equal(t, "input_text", part.Get("type").String())
	require.Equal(t, `plain "quoted" 中文`, part.Get("text").String())
	require.False(t, part.Get("encrypted_content").Exists())
	require.Equal(t, "gAAAAABcipher", gjson.GetBytes(out, "input.2.content.0.encrypted_content").String())
	require.Equal(t, "not-touched", gjson.GetBytes(out, "input.3.encrypted_content").String())

	untouched := []byte(`{"input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"gAAAAAB"},{"type":"encrypted_content","encrypted_content":""}]}]}`)
	same, count := RewriteOpenAIPlaintextEncryptedContent(untouched)
	require.Zero(t, count)
	require.Equal(t, untouched, same)
}

func TestOpenAIRequestDeclaresEncryptedArgs(t *testing.T) {
	declared := func(parameter string) []byte {
		return []byte(`{"input":"hi","tools":[{"type":"function","name":"send","parameters":{"properties":{"message":` + parameter + `}}}]}`)
	}
	for _, parameter := range []string{
		`{"type":"string","encrypted":true}`,
		`{"type":"string","encrypted": true}`,
		`{"type":"string","encrypted" : true}`,
		`{"type":"string","encrypted"  :   true}`,
		"{\"type\":\"string\",\n\t\"encrypted\"\r\n:\n\ttrue\n}",
		`{"type":"string","encr\u0079pted":true}`,
	} {
		require.True(t, OpenAIRequestDeclaresEncryptedArgs(declared(parameter)), parameter)
	}
	namespaced := []byte(`{"tools":[{"type":"namespace","tools":[{"type":"function","parameters":{"properties":{"message":{"encrypted" : true}}}}]}]}`)
	require.True(t, OpenAIRequestDeclaresEncryptedArgs(namespaced), "namespaced tools nest deeper")

	for _, body := range []string{
		`{"tools":[{"parameters":{"properties":{"message":{"encrypted":false}}}}]}`,
		`{"tools":[{"parameters":{"properties":{"encrypted":{"type":"boolean"}}}}]}`,
		`{"tools":[{"description":"set \"encrypted\": true to hide it"}]}`,
		`{"input":[{"type":"message","content":"{\"encrypted\": true}"}],"metadata":{"encrypted":true}}`,
		`{"input":"no tools"}`,
	} {
		require.False(t, OpenAIRequestDeclaresEncryptedArgs([]byte(body)), body)
	}

	// Nesting is bounded so a hostile body cannot turn the walk quadratic.
	nested := []byte(`{"tools":` + strings.Repeat("[", 200000) + `{"encrypted":true}` + strings.Repeat("]", 200000) + `}`)
	require.False(t, OpenAIRequestDeclaresEncryptedArgs(nested))
}

func TestEligibilityExcludesMarkedAccountOnlyWhenRequired(t *testing.T) {
	marked := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Extra: map[string]any{OpenAINoEncryptedArgsExtraKey: true}}
	plain := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	required := WithOpenAIEncryptedArgsRequired(context.Background())

	require.Equal(t, "encrypted_args_unsupported", openAICompatibleAccountEligibilityFailureReasonBeforeProfit(required, marked, PlatformOpenAI, "gpt-6-astra", false, ""))
	require.NotEqual(t, "encrypted_args_unsupported", openAICompatibleAccountEligibilityFailureReasonBeforeProfit(context.Background(), marked, PlatformOpenAI, "gpt-6-astra", false, ""))
	require.NotEqual(t, "encrypted_args_unsupported", openAICompatibleAccountEligibilityFailureReasonBeforeProfit(required, plain, PlatformOpenAI, "gpt-6-astra", false, ""))
}

func TestParseOpenAIEncryptedArgsCheckStream(t *testing.T) {
	cipher := "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"arguments\":\"{\\\"target\\\":\\\"a\\\",\\\"message\\\":\\\"gAAAAABx\\\"}\"}}\n\n"
	message, err := parseOpenAIEncryptedArgsCheckStream(strings.NewReader(cipher))
	require.NoError(t, err)
	require.Equal(t, "gAAAAABx", message)

	_, err = parseOpenAIEncryptedArgsCheckStream(strings.NewReader("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"nope\"}}}\n\n"))
	require.ErrorContains(t, err, "nope")

	_, err = parseOpenAIEncryptedArgsCheckStream(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n"))
	require.Error(t, err)

	require.Equal(t, OpenAIEncryptedArgsSupported, openAIEncryptedArgsVerdict(3, 0))
	require.Equal(t, OpenAIEncryptedArgsUnsupported, openAIEncryptedArgsVerdict(0, 3))
	require.Equal(t, OpenAIEncryptedArgsMixed, openAIEncryptedArgsVerdict(2, 1))
	require.Equal(t, OpenAIEncryptedArgsUnknown, openAIEncryptedArgsVerdict(0, 0))
}

func TestCheckEncryptedArgsDeclaresEncryptedParameter(t *testing.T) {
	body, err := openAIEncryptedArgsCheckBody("gpt-6-astra")
	require.NoError(t, err)
	require.True(t, OpenAIRequestDeclaresEncryptedArgs(body))
	require.Equal(t, "required", gjson.GetBytes(body, "tool_choice").String())
}

type failoverCheckAccountRepo struct {
	AccountRepository
	account *Account
}

func (r *failoverCheckAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}

func TestCheckEncryptedArgsResolvesTheModelLikeARealRequest(t *testing.T) {
	const fallback = openAIEncryptedArgsDefaultModel
	passthrough := map[string]any{"openai_passthrough": true}
	tests := []struct {
		name      string
		mapping   map[string]any
		extra     map[string]any
		requested string
		want      string
		wantErr   bool
	}{
		{name: "no mapping uses the default", want: fallback},
		{name: "default exact mapping", mapping: map[string]any{fallback: "real-a"}, want: "real-a"},
		{name: "default through a wildcard", mapping: map[string]any{"gpt-*": "real-a"}, want: "real-a"},
		{name: "wildcard only is never sent verbatim", mapping: map[string]any{"*": "real-a"}, want: "real-a"},
		{name: "alias of another model", mapping: map[string]any{"alias-b": "real-b"}, want: "real-b"},
		{name: "wildcard that does not cover the default", mapping: map[string]any{"claude-*": "real-c"}, want: "real-c"},
		{name: "alias preferred over a wildcard", mapping: map[string]any{"claude-*": "real-c", "alias-b": "real-b"}, want: "real-b"},
		{name: "requested alias is mapped", mapping: map[string]any{"alias-b": "real-b"}, requested: "alias-b", want: "real-b"},
		{name: "requested model outside the mapping stays", mapping: map[string]any{"alias-b": "real-b"}, requested: "other", want: "other"},
		{name: "passthrough ignores a leftover mapping", mapping: map[string]any{fallback: "real-a"}, extra: passthrough, want: fallback},
		{name: "no concrete name to send", mapping: map[string]any{"claude-*": "claude-*"}, wantErr: true},
		{name: "wildcard typed by hand", mapping: map[string]any{"alias-b": "real-b"}, requested: "gpt-*", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			credentials := map[string]any{"api_key": "sk-test", "base_url": "https://api.example.test"}
			if tt.mapping != nil {
				credentials["model_mapping"] = tt.mapping
			}
			upstream := &httpUpstreamRecorder{}
			for i := 0; i < openAIEncryptedArgsCheckRounds; i++ {
				upstream.responses = append(upstream.responses, &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader(`data: {"type":"response.output_item.done","item":{"type":"function_call","arguments":"{\"message\":\"gAAAAABcipher\"}"}}` + "\n\n")),
				})
			}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: credentials, Extra: tt.extra}
			svc := &OpenAIFailoverPolicyService{accounts: &failoverCheckAccountRepo{account: account}, upstream: upstream}

			result, err := svc.CheckEncryptedArgs(context.Background(), account.ID, tt.requested)
			if tt.wantErr {
				require.Error(t, err)
				require.Empty(t, upstream.bodies, "nothing to probe with: no upstream call")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, result.Model)
			require.Equal(t, OpenAIEncryptedArgsSupported, result.Verdict)
			require.Len(t, upstream.bodies, openAIEncryptedArgsCheckRounds)
			for _, body := range upstream.bodies {
				require.Equal(t, tt.want, gjson.GetBytes(body, "model").String())
			}
		})
	}
}

func failoverPassthroughFixture(t *testing.T, policy *OpenAIFailoverPolicy, resp *http.Response, stream bool) (*gin.Context, *httptest.ResponseRecorder, *OpenAIGatewayService, *Account, []byte) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	ctx := context.Background()
	if policy != nil {
		ctx = WithOpenAIFailoverPolicy(ctx, policy)
	}
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil)).WithContext(ctx)
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: &httpUpstreamRecorder{resp: resp},
	}
	account := &Account{
		ID: 127, Name: "acc", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.example.test"},
		Extra:       map[string]any{"openai_passthrough": true}, Status: StatusActive, Schedulable: true,
	}
	body := []byte(fmt.Sprintf(`{"model":"gpt-5.2","stream":%t,"input":"hello"}`, stream))
	return c, rec, svc, account, body
}

func enabledFailoverPolicy(rules ...OpenAIFailoverRule) *OpenAIFailoverPolicy {
	return newOpenAIFailoverPolicy(OpenAIFailoverConfig{SlotWaitMs: 3000, MaxRuleSwitches: 3, MaxSlotSwitches: 5, Rules: rules})
}

func TestPassthroughHTTPErrorFollowsFailoverRules(t *testing.T) {
	const upstreamBody = `{"error":{"code":"thinking_signature_invalid","message":"Encrypted function output content could not be decrypted or decoded.","type":"invalid_request_error"}}`
	newResp := func() *http.Response {
		return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(upstreamBody))}
	}

	t.Run("a matching rule switches accounts", func(t *testing.T) {
		c, _, svc, account, body := failoverPassthroughFixture(t, enabledFailoverPolicy(DefaultOpenAIFailoverRules()...), newResp(), false)
		_, err := svc.Forward(c.Request.Context(), c, account, body)
		var failoverErr *UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.Equal(t, "encrypted-content", failoverErr.FailoverRuleID)
		require.Equal(t, http.StatusBadRequest, failoverErr.StatusCode)
		require.False(t, failoverErr.RetryableOnSameAccount)
		require.Contains(t, string(failoverErr.ResponseBody), "could not be decrypted")
		require.False(t, c.Writer.Written(), "nothing reaches the client before the switch")
	})

	t.Run("without a policy the error is returned as before", func(t *testing.T) {
		c, rec, svc, account, body := failoverPassthroughFixture(t, nil, newResp(), false)
		_, err := svc.Forward(c.Request.Context(), c, account, body)
		var failoverErr *UpstreamFailoverError
		require.False(t, errors.As(err, &failoverErr))
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.NotContains(t, rec.Body.String(), "could not be decrypted", "passthrough masks upstream errors, as it always has")
	})

	t.Run("a return rule keeps the error", func(t *testing.T) {
		policy := enabledFailoverPolicy(failoverRule(400, OpenAIFailoverActionReturn, "could not be decrypted"), failoverRule(0, OpenAIFailoverActionFailover, "could not be decrypted"))
		c, rec, svc, account, body := failoverPassthroughFixture(t, policy, newResp(), false)
		_, err := svc.Forward(c.Request.Context(), c, account, body)
		var failoverErr *UpstreamFailoverError
		require.False(t, errors.As(err, &failoverErr))
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestPassthroughStreamFailureFollowsFailoverRules(t *testing.T) {
	const failed = `{"type":"response.failed","sequence_number":1,"response":{"status":"failed","error":{"code":"thinking_signature_invalid","message":"Encrypted function output content could not be decrypted or decoded.","type":"invalid_request_error"}}}`
	newResp := func(events ...string) *http.Response {
		var sse strings.Builder
		for _, event := range events {
			sse.WriteString("data: " + event + "\n\n")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse.String()))}
	}

	t.Run("before any output the rule switches accounts", func(t *testing.T) {
		c, rec, svc, account, body := failoverPassthroughFixture(t, enabledFailoverPolicy(DefaultOpenAIFailoverRules()...), newResp(`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1"}}`, failed), true)
		_, err := svc.Forward(c.Request.Context(), c, account, body)
		var failoverErr *UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.Equal(t, "encrypted-content", failoverErr.FailoverRuleID)
		require.False(t, failoverErr.RetryableOnSameAccount)
		require.NotContains(t, rec.Body.String(), "could not be decrypted", "the failure must not reach the client")
	})

	t.Run("after visible output the stream cannot be replayed", func(t *testing.T) {
		delta := `{"type":"response.output_text.delta","sequence_number":1,"delta":"hello"}`
		c, rec, svc, account, body := failoverPassthroughFixture(t, enabledFailoverPolicy(DefaultOpenAIFailoverRules()...), newResp(delta, failed), true)
		_, err := svc.Forward(c.Request.Context(), c, account, body)
		var failoverErr *UpstreamFailoverError
		require.False(t, errors.As(err, &failoverErr))
		require.Contains(t, rec.Body.String(), "hello")
		require.Contains(t, rec.Body.String(), "could not be decrypted")
	})

	t.Run("without a policy the failure is delivered as before", func(t *testing.T) {
		c, rec, svc, account, body := failoverPassthroughFixture(t, nil, newResp(failed), true)
		_, err := svc.Forward(c.Request.Context(), c, account, body)
		var failoverErr *UpstreamFailoverError
		require.False(t, errors.As(err, &failoverErr))
		require.Contains(t, rec.Body.String(), "could not be decrypted")
	})
}
