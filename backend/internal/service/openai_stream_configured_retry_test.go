//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

type streamRetryAccountRepo struct{ AccountRepository }

const (
	streamRetryModelNotFound = `{"type":"response.failed","response":{"id":"resp_1","error":{"code":"model_not_found","type":"invalid_request_error","message":"The model gpt-x does not exist"}}}`
	streamRetryOverloaded    = `{"type":"response.failed","response":{"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded"}}}`
)

func newStreamRetryService() *OpenAIGatewayService {
	return &OpenAIGatewayService{
		accountRepo: streamRetryAccountRepo{},
		cfg: &config.Config{Gateway: config.GatewayConfig{
			MaxLineSize: defaultMaxLineSize,
		}},
	}
}

func streamRetryAccount(pool bool) *Account {
	return &Account{
		ID:          1047,
		Name:        "relay",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"pool_mode": pool},
	}
}

// newStreamRetryContext builds a request whose context carries the list; a
// nil codes argument leaves the context without one, like an entry point that
// does not mount it (WebSocket, images).
func newStreamRetryContext(codes ...string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	if codes != nil {
		c.Request = c.Request.WithContext(WithOpenAIStreamFailedRetryCodes(c.Request.Context(), compileOpenAIStreamFailedRetryCodes(codes)))
	}
	return c, rec
}

func streamRetrySSE(events ...string) *http.Response {
	var b strings.Builder
	for _, event := range events {
		b.WriteString("event: " + gjson.Get(event, "type").String() + "\n")
		b.WriteString("data: " + event + "\n\n")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(b.String())),
		Header:     http.Header{"X-Request-Id": []string{"rid-1"}, "Content-Type": []string{"text/event-stream"}},
	}
}

func requireConfiguredRetry(t *testing.T, err error, wantSameAccount bool) *UpstreamFailoverError {
	t.Helper()
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "want a failover error, got %v", err)
	require.Equal(t, OpenAIStreamConfiguredRetryReason, failoverErr.Reason)
	require.Equal(t, http.StatusBadRequest, failoverErr.StatusCode)
	require.Equal(t, GatewayFailureScopeRequest, failoverErr.Scope)
	require.Equal(t, NextAccountRetry, failoverErr.NextAccountAction)
	require.Equal(t, wantSameAccount, failoverErr.RetryableOnSameAccount)
	require.False(t, failoverErr.RequestScopedTransient)
	require.Equal(t, "model_not_found", gjson.GetBytes(failoverErr.ResponseBody, "error.code").String())
	require.Equal(t, "invalid_request_error", gjson.GetBytes(failoverErr.ResponseBody, "error.type").String())
	require.Equal(t, "The model gpt-x does not exist", gjson.GetBytes(failoverErr.ResponseBody, "error.message").String())
	return failoverErr
}

func TestOpenAIStreamConfiguredRetryError(t *testing.T) {
	svc := newStreamRetryService()
	headers := http.Header{"X-Request-Id": []string{"rid-1"}}
	call := func(c *gin.Context, s *OpenAIGatewayService, account *Account, eventType, payload string) *UpstreamFailoverError {
		p := []byte(payload)
		return s.openAIStreamConfiguredRetryError(c, account, false, eventType, p, extractOpenAISSEErrorMessage(p), "rid-1", headers)
	}

	t.Run("listed code on a pool account retries the same account", func(t *testing.T) {
		c, _ := newStreamRetryContext("model_not_found")
		got := call(c, svc, streamRetryAccount(true), "response.failed", streamRetryModelNotFound)
		require.NotNil(t, got)
		requireConfiguredRetry(t, got, true)
		require.Equal(t, "rid-1", got.ResponseHeaders.Get("X-Request-Id"))
		require.False(t, got.ShouldReportAccountScheduleFailure())
		require.True(t, got.ShouldRetryNextAccount())
		_, _, countsAgainstHealth := classifyOpenAIAPIKeyHealthFailure(got)
		require.False(t, countsAgainstHealth)
	})
	t.Run("non-pool account switches without same-account retry", func(t *testing.T) {
		c, _ := newStreamRetryContext("model_not_found")
		got := call(c, svc, streamRetryAccount(false), "response.failed", streamRetryModelNotFound)
		requireConfiguredRetry(t, got, false)
		_, _, countsAgainstHealth := classifyOpenAIAPIKeyHealthFailure(got)
		require.False(t, countsAgainstHealth)
	})
	t.Run("code matching ignores case and whitespace, missing type falls back", func(t *testing.T) {
		c, _ := newStreamRetryContext("model_not_found")
		got := call(c, svc, streamRetryAccount(true), " response.failed ", `{"type":"response.failed","error":{"code":" Model_Not_Found ","message":"gone"}}`)
		require.NotNil(t, got)
		require.Equal(t, "Model_Not_Found", gjson.GetBytes(got.ResponseBody, "error.code").String())
		require.Equal(t, "invalid_request_error", gjson.GetBytes(got.ResponseBody, "error.type").String())
	})

	misses := []struct {
		name      string
		codes     []string
		svc       *OpenAIGatewayService
		account   *Account
		eventType string
		payload   string
	}{
		{"empty list", []string{}, svc, streamRetryAccount(true), "response.failed", streamRetryModelNotFound},
		{"list not mounted", nil, svc, streamRetryAccount(true), "response.failed", streamRetryModelNotFound},
		{"code not listed", []string{"model_not_found"}, svc, streamRetryAccount(true), "response.failed", streamRetryOverloaded},
		{"bare error event with a listed code", []string{"model_not_found"}, svc, streamRetryAccount(true), "error", `{"type":"error","error":{"code":"model_not_found","type":"invalid_request_error","message":"gone"}}`},
		{"no structured code, only a message", []string{"model_not_found"}, svc, streamRetryAccount(true), "response.failed", `{"type":"response.failed","response":{"error":{"message":"model_not_found: the model does not exist"}}}`},
		{"code only inside embedded JSON", []string{"model_not_found"}, svc, streamRetryAccount(true), "response.failed", `{"type":"response.failed","response":{"error":{"message":"{\"error\":{\"code\":\"model_not_found\"}}"}}}`},
		{"moderation marker in type", []string{"model_not_found"}, svc, streamRetryAccount(true), "response.failed", `{"type":"response.failed","response":{"error":{"code":"model_not_found","type":"content_filter","message":"gone"}}}`},
		{"moderation marker in message", []string{"model_not_found"}, svc, streamRetryAccount(true), "response.failed", `{"type":"response.failed","response":{"error":{"code":"model_not_found","message":"This is not allowed by our safety system"}}}`},
		{"cyber policy", []string{"cyber_block"}, svc, streamRetryAccount(true), "response.failed", `{"type":"response.failed","response":{"error":{"code":"cyber_block","message":"This request has been flagged for potentially high-risk cyber activity"}}}`},
		{"context window", []string{"too_long"}, svc, streamRetryAccount(true), "response.failed", `{"type":"response.failed","response":{"error":{"code":"too_long","message":"Your input exceeds the context window of this model"}}}`},
		{"service without account repo", []string{"model_not_found"}, &OpenAIGatewayService{}, streamRetryAccount(true), "response.failed", streamRetryModelNotFound},
		{"nil account", []string{"model_not_found"}, svc, nil, "response.failed", streamRetryModelNotFound},
		{"account of another gateway", []string{"model_not_found"}, svc, &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}, "response.failed", streamRetryModelNotFound},
	}
	for _, tc := range misses {
		t.Run("miss: "+tc.name, func(t *testing.T) {
			c, _ := newStreamRetryContext(tc.codes...)
			if tc.codes != nil && len(tc.codes) == 0 {
				c.Request = c.Request.WithContext(WithOpenAIStreamFailedRetryCodes(c.Request.Context(), map[string]struct{}{}))
			}
			require.Nil(t, call(c, tc.svc, tc.account, tc.eventType, tc.payload))
			_, recorded := c.Get(OpsUpstreamErrorsKey)
			require.False(t, recorded, "a miss must not record an upstream error")
		})
	}
}

// The eligibility is a copy of the HTTP path's precondition for failing over
// an HTTP 400 model_not_found. If upstream changes that precondition this
// fails, and openAIStreamConfiguredRetryEligible has to follow.
func TestOpenAIStreamConfiguredRetryEligibleMatchesHTTPPath(t *testing.T) {
	body := []byte(`{"error":{"code":"model_not_found","type":"invalid_request_error","message":"The model does not exist"}}`)
	services := map[string]*OpenAIGatewayService{
		"with repo":    newStreamRetryService(),
		"without repo": {},
	}
	accounts := map[string]*Account{
		"nil":              nil,
		"openai api key":   streamRetryAccount(false),
		"openai pool":      streamRetryAccount(true),
		"openai oauth":     {ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		"anthropic":        {ID: 4, Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
		"gemini":           {ID: 5, Platform: PlatformGemini, Type: AccountTypeAPIKey},
		"platform not set": {ID: 6},
	}
	for svcName, svc := range services {
		for accountName, account := range accounts {
			httpVerdict := svc.shouldFailoverOpenAIUpstreamResponse(account, http.StatusBadRequest, "The model does not exist", body)
			require.Equal(t, httpVerdict, svc.openAIStreamConfiguredRetryEligible(account), "%s / %s", svcName, accountName)
		}
	}
}

func TestShouldReportAccountScheduleFailureOnlyChangesForConfiguredRetry(t *testing.T) {
	require.False(t, (&UpstreamFailoverError{Reason: OpenAIStreamConfiguredRetryReason, StatusCode: 400}).ShouldReportAccountScheduleFailure())
	require.True(t, (&UpstreamFailoverError{StatusCode: 502}).ShouldReportAccountScheduleFailure())
	require.True(t, (&UpstreamFailoverError{StatusCode: 400, Reason: OpenAIHTTPContinuationUnsupportedReason}).ShouldReportAccountScheduleFailure())
	require.False(t, (&UpstreamFailoverError{Stage: GatewayFailureStageAccountAuth, Scope: GatewayFailureScopeProvider}).ShouldReportAccountScheduleFailure())
	require.True(t, (&UpstreamFailoverError{Stage: GatewayFailureStageAccountAuth, Scope: GatewayFailureScopeAccount}).ShouldReportAccountScheduleFailure())
}

// Each stream reader that asks the list, driven through its real read loop.
func TestOpenAIStreamConfiguredRetryAtEveryHook(t *testing.T) {
	created := `{"type":"response.created","response":{"id":"resp_1"}}`
	hooks := map[string]func(c *gin.Context, svc *OpenAIGatewayService, account *Account, resp *http.Response) error{
		"responses forward": func(c *gin.Context, svc *OpenAIGatewayService, account *Account, resp *http.Response) error {
			_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "gpt-x", "gpt-x")
			return err
		},
		"responses passthrough": func(c *gin.Context, svc *OpenAIGatewayService, account *Account, resp *http.Response) error {
			_, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "gpt-x", "gpt-x")
			return err
		},
		"responses sse to json": func(c *gin.Context, svc *OpenAIGatewayService, account *Account, resp *http.Response) error {
			body, _ := io.ReadAll(resp.Body)
			_, err := svc.handleSSEToJSON(resp, c, account, body, "gpt-x", "gpt-x")
			return err
		},
		"responses passthrough sse to json": func(c *gin.Context, svc *OpenAIGatewayService, account *Account, resp *http.Response) error {
			body, _ := io.ReadAll(resp.Body)
			_, err := svc.handlePassthroughSSEToJSON(resp, c, account, body, "gpt-x", "gpt-x")
			return err
		},
		"chat completions stream": func(c *gin.Context, svc *OpenAIGatewayService, account *Account, resp *http.Response) error {
			_, err := svc.handleChatStreamingResponse(resp, c, account, "gpt-x", "gpt-x", "gpt-x", time.Now(), 0)
			return err
		},
		"messages stream": func(c *gin.Context, svc *OpenAIGatewayService, account *Account, resp *http.Response) error {
			_, err := svc.handleAnthropicStreamingResponse(resp, c, account, "gpt-x", "gpt-x", "gpt-x", time.Now())
			return err
		},
	}
	for name, run := range hooks {
		// The Chat Completions and Messages converters turn response.created
		// into the first client chunk, so there the stream is only replayable
		// when the failure is the first event.
		events := []string{created, streamRetryModelNotFound}
		if name == "chat completions stream" || name == "messages stream" {
			events = []string{streamRetryModelNotFound}
		}
		t.Run(name+": listed code retries", func(t *testing.T) {
			c, rec := newStreamRetryContext("model_not_found")
			err := run(c, newStreamRetryService(), streamRetryAccount(true), streamRetrySSE(events...))
			requireConfiguredRetry(t, err, true)
			require.Zero(t, rec.Body.Len(), "nothing may reach the client before the retry")
		})
		t.Run(name+": empty list keeps the old result", func(t *testing.T) {
			c, _ := newStreamRetryContext()
			err := run(c, newStreamRetryService(), streamRetryAccount(true), streamRetrySSE(events...))
			var failoverErr *UpstreamFailoverError
			if errors.As(err, &failoverErr) {
				require.NotEqual(t, OpenAIStreamConfiguredRetryReason, failoverErr.Reason)
			}
			unlisted, _ := newStreamRetryContext("some_other_code")
			unlistedErr := run(unlisted, newStreamRetryService(), streamRetryAccount(true), streamRetrySSE(events...))
			require.Equal(t, err == nil, unlistedErr == nil)
			require.Equal(t, errors.As(err, &failoverErr), errors.As(unlistedErr, &failoverErr))
		})
	}
}

func TestOpenAIStreamConfiguredRetryNotAfterOutputStarted(t *testing.T) {
	c, rec := newStreamRetryContext("model_not_found")
	resp := streamRetrySSE(
		`{"type":"response.created","response":{"id":"resp_1"}}`,
		`{"type":"response.output_text.delta","delta":"hello"}`,
		streamRetryModelNotFound,
	)
	_, err := newStreamRetryService().handleStreamingResponse(c.Request.Context(), resp, c, streamRetryAccount(true), time.Now(), "gpt-x", "gpt-x")
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.Contains(t, rec.Body.String(), "hello")
	require.Contains(t, rec.Body.String(), "model_not_found")
}

// Without the list the Responses stream still hands the failed event to the
// client, exactly as before the list existed.
func TestOpenAIStreamConfiguredRetryOffForwardsFailedEvent(t *testing.T) {
	for name, c := range map[string]func() (*gin.Context, *httptest.ResponseRecorder){
		"not mounted": func() (*gin.Context, *httptest.ResponseRecorder) { return newStreamRetryContext() },
		"empty": func() (*gin.Context, *httptest.ResponseRecorder) {
			c, rec := newStreamRetryContext()
			c.Request = c.Request.WithContext(WithOpenAIStreamFailedRetryCodes(context.Background(), map[string]struct{}{}))
			return c, rec
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, rec := c()
			resp := streamRetrySSE(`{"type":"response.created","response":{"id":"resp_1"}}`, streamRetryModelNotFound)
			_, err := newStreamRetryService().handleStreamingResponse(ctx.Request.Context(), resp, ctx, streamRetryAccount(true), time.Now(), "gpt-x", "gpt-x")
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr))
			require.Contains(t, rec.Body.String(), "model_not_found")
		})
	}
}

// The buffered Chat Completions and Messages readers rebuild the terminal
// event and lose its type, so they stay on the old classifier whatever the
// list says.
func TestOpenAIStreamConfiguredRetrySkipsBufferedCompatReaders(t *testing.T) {
	readers := map[string]func(c *gin.Context, svc *OpenAIGatewayService, resp *http.Response) error{
		"chat completions buffered": func(c *gin.Context, svc *OpenAIGatewayService, resp *http.Response) error {
			_, err := svc.handleChatBufferedStreamingResponse(resp, c, streamRetryAccount(true), "gpt-x", "gpt-x", "gpt-x", time.Now())
			return err
		},
		"messages buffered": func(c *gin.Context, svc *OpenAIGatewayService, resp *http.Response) error {
			_, err := svc.handleAnthropicBufferedStreamingResponse(resp, c, streamRetryAccount(true), "gpt-x", "gpt-x", "gpt-x", time.Now())
			return err
		},
	}
	for name, run := range readers {
		t.Run(name, func(t *testing.T) {
			outcome := func(codes ...string) (bool, GatewayFailureReason, int) {
				c, _ := newStreamRetryContext(codes...)
				err := run(c, newStreamRetryService(), streamRetrySSE(`{"type":"response.created","response":{"id":"resp_1"}}`, streamRetryModelNotFound))
				var failoverErr *UpstreamFailoverError
				if errors.As(err, &failoverErr) {
					return true, failoverErr.Reason, failoverErr.StatusCode
				}
				return false, "", 0
			}
			offFailover, offReason, offStatus := outcome()
			onFailover, onReason, onStatus := outcome("model_not_found")
			require.Equal(t, offFailover, onFailover)
			require.Equal(t, offReason, onReason)
			require.Equal(t, offStatus, onStatus)
			require.NotEqual(t, OpenAIStreamConfiguredRetryReason, onReason)
		})
	}
}

func TestNormalizeOpenAIStreamFailedRetryCodes(t *testing.T) {
	got, err := normalizeOpenAIStreamFailedRetryCodes(nil, false)
	require.NoError(t, err)
	require.Equal(t, []string{"model_not_found"}, got)

	got, err = normalizeOpenAIStreamFailedRetryCodes([]string{}, false)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, got)

	got, err = normalizeOpenAIStreamFailedRetryCodes([]string{" Model_Not_Found ", "model_not_found", "deployment.not-ready"}, false)
	require.NoError(t, err)
	require.Equal(t, []string{"model_not_found", "deployment.not-ready"}, got)

	for _, bad := range []string{"", "   ", "has space", "中文", strings.Repeat("a", 65), "content_policy_violation", "content_filter", "safety_block", "context_length_exceeded"} {
		_, err = normalizeOpenAIStreamFailedRetryCodes([]string{bad}, false)
		require.Error(t, err, "%q", bad)
		got, err = normalizeOpenAIStreamFailedRetryCodes([]string{bad, "model_not_found"}, true)
		require.NoError(t, err)
		require.Equal(t, []string{"model_not_found"}, got)
	}

	many := make([]string, 0, 21)
	for i := 0; i < 21; i++ {
		many = append(many, "code_"+string(rune('a'+i)))
	}
	_, err = normalizeOpenAIStreamFailedRetryCodes(many, false)
	require.Error(t, err)
	_, err = normalizeOpenAIStreamFailedRetryCodes(many[:20], false)
	require.NoError(t, err)
}

func TestOpenAIFailoverConfigStreamFailedRetryCodes(t *testing.T) {
	ctx := context.Background()
	storedCodes := func(repo *failoverSettingRepo) gjson.Result {
		return gjson.Get(repo.data[SettingKeyOpenAIFailoverPolicy], "stream_failed_retry_codes")
	}
	listed := func(svc *OpenAIFailoverPolicyService, code string) bool {
		_, ok := svc.StreamFailedRetryCodes(ctx)[code]
		return ok
	}

	t.Run("never saved means the default list", func(t *testing.T) {
		svc := NewOpenAIFailoverPolicyService(newFailoverSettingRepo(), nil)
		cfg, err := svc.Config(ctx)
		require.NoError(t, err)
		require.Equal(t, []string{"model_not_found"}, cfg.StreamFailedRetryCodes)
		require.True(t, listed(svc, "model_not_found"))
	})
	t.Run("stored without the key means the default list", func(t *testing.T) {
		repo := newFailoverSettingRepo()
		repo.data[SettingKeyOpenAIFailoverPolicy] = `{"enabled_group_ids":[45],"max_rule_switches":3,"rules":[]}`
		svc := NewOpenAIFailoverPolicyService(repo, nil)
		require.True(t, listed(svc, "model_not_found"))
	})
	t.Run("stored empty list turns it off", func(t *testing.T) {
		repo := newFailoverSettingRepo()
		repo.data[SettingKeyOpenAIFailoverPolicy] = `{"enabled_group_ids":[],"max_rule_switches":3,"rules":[],"stream_failed_retry_codes":[]}`
		svc := NewOpenAIFailoverPolicyService(repo, nil)
		require.Empty(t, svc.StreamFailedRetryCodes(ctx))
		cfg, err := svc.Config(ctx)
		require.NoError(t, err)
		require.NotNil(t, cfg.StreamFailedRetryCodes)
		require.Empty(t, cfg.StreamFailedRetryCodes)
	})
	t.Run("save writes the key, also when empty, and applies at once", func(t *testing.T) {
		repo := newFailoverSettingRepo()
		svc := NewOpenAIFailoverPolicyService(repo, nil)
		cfg := openAIRuleFixture()
		cfg.StreamFailedRetryCodes = []string{" Deployment_Not_Ready ", "model_not_found", "model_not_found"}
		saved, err := svc.UpdateConfig(ctx, cfg)
		require.NoError(t, err)
		require.Equal(t, []string{"deployment_not_ready", "model_not_found"}, saved.StreamFailedRetryCodes)
		require.JSONEq(t, `["deployment_not_ready","model_not_found"]`, storedCodes(repo).Raw)
		require.True(t, listed(svc, "deployment_not_ready"))

		cfg.StreamFailedRetryCodes = []string{}
		_, err = svc.UpdateConfig(ctx, cfg)
		require.NoError(t, err)
		require.True(t, storedCodes(repo).Exists())
		require.JSONEq(t, `[]`, storedCodes(repo).Raw)
		require.Empty(t, svc.StreamFailedRetryCodes(ctx))
	})
	t.Run("a save without the field keeps the stored list", func(t *testing.T) {
		repo := newFailoverSettingRepo()
		svc := NewOpenAIFailoverPolicyService(repo, nil)
		cfg := openAIRuleFixture()
		cfg.StreamFailedRetryCodes = []string{}
		_, err := svc.UpdateConfig(ctx, cfg)
		require.NoError(t, err)

		var fromOldClient OpenAIFailoverConfig
		require.NoError(t, json.Unmarshal([]byte(`{"enabled_group_ids":[45],"max_rule_switches":3,"rules":[]}`), &fromOldClient))
		require.Nil(t, fromOldClient.StreamFailedRetryCodes)
		saved, err := svc.UpdateConfig(ctx, fromOldClient)
		require.NoError(t, err)
		require.Empty(t, saved.StreamFailedRetryCodes)
		require.JSONEq(t, `[]`, storedCodes(repo).Raw)

		first := NewOpenAIFailoverPolicyService(newFailoverSettingRepo(), nil)
		saved, err = first.UpdateConfig(ctx, fromOldClient)
		require.NoError(t, err)
		require.Equal(t, []string{"model_not_found"}, saved.StreamFailedRetryCodes)
	})
	t.Run("invalid codes are rejected and nothing is stored", func(t *testing.T) {
		repo := newFailoverSettingRepo()
		svc := NewOpenAIFailoverPolicyService(repo, nil)
		cfg := openAIRuleFixture()
		cfg.StreamFailedRetryCodes = []string{"content_policy_violation"}
		_, err := svc.UpdateConfig(ctx, cfg)
		require.Error(t, err)
		require.Empty(t, repo.data)
	})
	t.Run("unknown stored fields survive a save", func(t *testing.T) {
		repo := newFailoverSettingRepo()
		repo.data[SettingKeyOpenAIFailoverPolicy] = `{"enabled_group_ids":[],"max_rule_switches":3,"rules":[],"slot_wait_ms":4000,"from_another_branch":{"a":1}}`
		svc := NewOpenAIFailoverPolicyService(repo, nil)
		_, err := svc.UpdateConfig(ctx, openAIRuleFixture())
		require.NoError(t, err)
		stored := repo.data[SettingKeyOpenAIFailoverPolicy]
		require.Equal(t, int64(4000), gjson.Get(stored, "slot_wait_ms").Int())
		require.Equal(t, int64(1), gjson.Get(stored, "from_another_branch.a").Int())
		require.JSONEq(t, `["model_not_found"]`, gjson.Get(stored, "stream_failed_retry_codes").Raw)
	})
	t.Run("a failed load keeps the previous list", func(t *testing.T) {
		repo := newFailoverSettingRepo()
		svc := NewOpenAIFailoverPolicyService(repo, nil)
		cfg := openAIRuleFixture()
		cfg.StreamFailedRetryCodes = []string{}
		_, err := svc.UpdateConfig(ctx, cfg)
		require.NoError(t, err)
		repo.getErr = errors.New("db down")
		svc.reload(ctx)
		require.Empty(t, svc.StreamFailedRetryCodes(ctx))
	})
	t.Run("nil service has no list", func(t *testing.T) {
		var svc *OpenAIFailoverPolicyService
		require.Nil(t, svc.StreamFailedRetryCodes(ctx))
	})
}
