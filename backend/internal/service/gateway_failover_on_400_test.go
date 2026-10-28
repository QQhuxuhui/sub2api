//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Upstream relays that vet the client (attribution header, client version,
// third-party apps) answer 400 for requests another account would accept.
const relayClientCheck400 = `{"type":"error","error":{"type":"invalid_request_error","message":"检测到 Claude Code 请求可能被第三方中转改写。若您正在使用 CCS、CCH、new-api、sub2api 等中转服务，请开启请求体透传后重试"}}`

func failoverOn400TestService(enabled bool, keywords ...string) *GatewayService {
	return &GatewayService{cfg: &config.Config{
		Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}},
		Gateway:  config.GatewayConfig{FailoverOn400: enabled, FailoverOn400Keywords: keywords},
	}}
}

func TestShouldFailoverOn400ConfiguredKeywords(t *testing.T) {
	svc := failoverOn400TestService(true, " 第三方中转 ", "THIRD-PARTY APPS")

	require.True(t, svc.shouldFailoverOn400([]byte(relayClientCheck400)))
	require.True(t, svc.shouldFailoverOn400([]byte(`{"error":{"message":"Third-party apps now draw from your extra usage, not your plan limits."}}`)), "case-insensitive")
	require.False(t, svc.shouldFailoverOn400([]byte(`{"error":{"message":"max_tokens: must be greater than 0"}}`)))
	// Built-in compatibility keywords keep working alongside configured ones.
	require.True(t, svc.shouldFailoverOn400([]byte(`{"error":{"message":"messages.1.content.0: invalid signature in thinking block"}}`)))

	require.False(t, failoverOn400TestService(true).shouldFailoverOn400([]byte(relayClientCheck400)), "no keyword configured")
}

func TestShouldFailoverOn400RawBodyKeywords(t *testing.T) {
	svc := failoverOn400TestService(true, " 第三方中转 ", "THIRD-PARTY APPS", " ")
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"plain text", "第三方中转", true},
		{"HTML", "<html>Third-party apps</html>", true},
		{"error string", `{"error":"第三方中转"}`, true},
		{"empty message", `{"error":{"message":"","reason":"第三方中转"}}`, true},
		{"unmatched", "invalid request", false},
		{"empty body", "", false},
		{"built-in remains message-only", "thinking", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, svc.shouldFailoverOn400([]byte(tc.body)))
		})
	}
}

func TestForwardConfigured400WithCustomErrorCodes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []struct {
		name        string
		passthrough bool
	}{
		{"standard", false},
		{"passthrough", true},
	} {
		t.Run(route.name, func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				enabled  bool
				keyword  string
				failover bool
			}{
				{"matched", true, "第三方中转", true},
				{"disabled", false, "第三方中转", false},
				{"unmatched", true, "客户端版本过低", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
					repo := &rateLimitAccountRepoStub{}
					svc := failoverOn400TestService(tc.enabled, tc.keyword)
					svc.rateLimitService = &RateLimitService{accountRepo: repo}
					svc.settingService = NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{}}, svc.cfg)
					svc.httpUpstream = &anthropicHTTPUpstreamRecorder{resp: newFailoverOn400Response()}
					account := newAnthropicAPIKeyAccountForTest()
					account.Extra["anthropic_passthrough"] = route.passthrough
					account.Credentials["custom_error_codes_enabled"] = true
					account.Credentials["custom_error_codes"] = []any{float64(418)}
					parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"x","messages":[{"role":"user","content":"hello"}]}`)), PlatformAnthropic)
					require.NoError(t, err)

					result, err := svc.Forward(context.Background(), c, account, parsed)
					require.Nil(t, result)
					require.Error(t, err)
					if tc.failover {
						var failoverErr *UpstreamFailoverError
						require.ErrorAs(t, err, &failoverErr)
						require.Equal(t, http.StatusBadRequest, failoverErr.StatusCode)
						require.JSONEq(t, relayClientCheck400, string(failoverErr.ResponseBody))
						require.False(t, c.Writer.Written())
					} else {
						require.False(t, errorsAsFailover(err))
						require.Equal(t, http.StatusBadGateway, rec.Code, "preserve existing custom error-code handling")
					}
					require.Zero(t, repo.setErrorCalls+repo.tempCalls)
				})
			}
		})
	}
}

func newFailoverOn400Response() *http.Response {
	return &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(relayClientCheck400))),
	}
}

func TestTryFailoverOn400MovesRequestWithoutMarkingAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	repo := &rateLimitAccountRepoStub{}
	svc := failoverOn400TestService(true, "第三方中转")
	svc.rateLimitService = &RateLimitService{accountRepo: repo}
	account := newAnthropicAPIKeyAccountForTest()
	account.Credentials["custom_error_codes_enabled"] = true
	account.Credentials["custom_error_codes"] = []any{float64(418)}

	resp := newFailoverOn400Response()
	failoverErr := svc.tryFailoverOn400(context.Background(), c, resp, account, "claude-opus-5-5", false)
	require.NotNil(t, failoverErr)
	require.Equal(t, http.StatusBadRequest, failoverErr.StatusCode)
	require.Contains(t, string(failoverErr.ResponseBody), "第三方中转")
	require.False(t, c.Writer.Written(), "the handler decides what the client sees")
	require.Zero(t, repo.setErrorCalls+repo.tempCalls, "only this request skips the account")

	// Disabled, or an unknown 400: ordinary handling, body still readable.
	for _, svc := range []*GatewayService{failoverOn400TestService(false, "第三方中转"), failoverOn400TestService(true, "客户端版本过低")} {
		resp := newFailoverOn400Response()
		require.Nil(t, svc.tryFailoverOn400(context.Background(), c, resp, account, "claude-opus-5-5", false))
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.JSONEq(t, relayClientCheck400, string(body))
	}
}

func TestAnthropicAPIKeyPassthroughFailsOverOnConfigured400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newCtx := func() (*gin.Context, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		return c, rec
	}

	svc := failoverOn400TestService(true, "第三方中转")
	svc.httpUpstream = &anthropicHTTPUpstreamRecorder{resp: newFailoverOn400Response()}
	c, _ := newCtx()
	result, err := svc.forwardAnthropicAPIKeyPassthrough(context.Background(), c, newAnthropicAPIKeyAccountForTest(), []byte(`{"model":"x"}`), "x", "x", false, time.Now())
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadRequest, failoverErr.StatusCode)
	require.False(t, c.Writer.Written())

	// Without the switch the 400 is answered as before.
	off := failoverOn400TestService(false, "第三方中转")
	off.httpUpstream = &anthropicHTTPUpstreamRecorder{resp: newFailoverOn400Response()}
	c, rec := newCtx()
	_, err = off.forwardAnthropicAPIKeyPassthrough(context.Background(), c, newAnthropicAPIKeyAccountForTest(), []byte(`{"model":"x"}`), "x", "x", false, time.Now())
	require.Error(t, err)
	require.False(t, errorsAsFailover(err))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "第三方中转")
}

func errorsAsFailover(err error) bool {
	_, ok := err.(*UpstreamFailoverError)
	return ok
}
