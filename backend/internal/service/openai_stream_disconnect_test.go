//go:build unit

package service

import (
	"context"
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

// The exact frame CLIProxyAPI sent on gptniubi group 4 (account 42).
const streamDisconnectSample = `{"error":{"code":"request_timeout","message":"stream error: stream disconnected before completion: stream closed before response.completed","param":null,"type":"invalid_request_error"},"sequence_number":97,"type":"error"}`

func streamDisconnectAPIKeyAccount() *Account {
	return &Account{ID: 42, Name: "cpa", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
}

func TestIsOpenAIUpstreamStreamDisconnect(t *testing.T) {
	msg := "stream error: stream disconnected before completion: stream closed before response.completed"
	require.True(t, IsOpenAIUpstreamStreamDisconnect("request_timeout", msg))
	require.True(t, IsOpenAIUpstreamStreamDisconnect(" request_timeout ", "Stream Disconnected Before Completion"))
	require.True(t, IsOpenAIUpstreamStreamDisconnect("request_timeout", "x: stream closed before response.completed"))

	require.False(t, IsOpenAIUpstreamStreamDisconnect("context_length_exceeded", msg), "a real client error code wins")
	require.False(t, IsOpenAIUpstreamStreamDisconnect("", msg), "the code is required")
	require.False(t, IsOpenAIUpstreamStreamDisconnect("request_timeout", "request timed out waiting for input"), "a timeout that is not a disconnect")
}

func TestRewriteOpenAIStreamDisconnectErrorType(t *testing.T) {
	account := streamDisconnectAPIKeyAccount()

	rewritten, ok := rewriteOpenAIStreamDisconnectErrorType([]byte(streamDisconnectSample), "error", account)
	require.True(t, ok)
	require.Equal(t, "server_error", gjson.GetBytes(rewritten, "error.type").String())
	// Everything else is byte-for-byte what upstream sent.
	require.Equal(t, strings.Replace(streamDisconnectSample, `"type":"invalid_request_error"`, `"type":"server_error"`, 1), string(rewritten))

	t.Run("left alone", func(t *testing.T) {
		oauth := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
		cases := map[string]struct {
			data      string
			eventType string
			account   *Account
		}{
			"oauth account":         {streamDisconnectSample, "error", oauth},
			"nil account":           {streamDisconnectSample, "error", nil},
			"response.failed event": {streamDisconnectSample, "response.failed", account},
			"real 400":              {strings.Replace(streamDisconnectSample, "request_timeout", "context_length_exceeded", 1), "error", account},
			"already server_error":  {strings.Replace(streamDisconnectSample, "invalid_request_error", "server_error", 1), "error", account},
			"no error.type":         {`{"error":{"code":"request_timeout","message":"stream disconnected before completion"},"type":"error"}`, "error", account},
			"flat error":            {`{"type":"error","code":"request_timeout","message":"stream disconnected before completion"}`, "error", account},
			"not json":              {strings.TrimSuffix(streamDisconnectSample, "}"), "error", account},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				out, changed := rewriteOpenAIStreamDisconnectErrorType([]byte(tc.data), tc.eventType, tc.account)
				require.False(t, changed)
				require.Equal(t, tc.data, string(out))
			})
		}
	})
}

func streamDisconnectRuleError() *UpstreamFailoverError {
	body := []byte(`{"error":{"type":"invalid_request_error","message":"stream error: stream disconnected before completion: stream closed before response.completed","code":"request_timeout"}}`)
	return newOpenAIRuleFailoverError("stream-disconnected", http.StatusBadRequest, nil, body, "stream error")
}

func TestMarkOpenAIRuleStreamDisconnect(t *testing.T) {
	account := streamDisconnectAPIKeyAccount()

	fe := markOpenAIRuleStreamDisconnect(streamDisconnectRuleError(), account, "error", []byte(streamDisconnectSample))
	require.True(t, fe.OpenAIStreamDisconnect)
	require.Equal(t, http.StatusBadGateway, fe.StatusCode)
	require.Equal(t, "server_error", gjson.GetBytes(fe.ResponseBody, "error.type").String())
	require.Equal(t, "request_timeout", gjson.GetBytes(fe.ResponseBody, "error.code").String())
	require.Equal(t, "stream error: stream disconnected before completion: stream closed before response.completed",
		gjson.GetBytes(fe.ResponseBody, "error.message").String())
	require.Equal(t, "stream-disconnected", fe.FailoverRuleID)

	t.Run("left alone", func(t *testing.T) {
		oauth := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
		notRule := streamDisconnectRuleError()
		notRule.FailoverRuleID = ""
		cases := map[string]struct {
			fe        *UpstreamFailoverError
			account   *Account
			eventType string
			payload   string
		}{
			"not a rule switch":     {notRule, account, "error", streamDisconnectSample},
			"oauth account":         {streamDisconnectRuleError(), oauth, "error", streamDisconnectSample},
			"response.failed event": {streamDisconnectRuleError(), account, "response.failed", streamDisconnectSample},
			"real 400":              {streamDisconnectRuleError(), account, "error", strings.Replace(streamDisconnectSample, "request_timeout", "context_length_exceeded", 1)},
			"malformed json":        {streamDisconnectRuleError(), account, "error", strings.TrimSuffix(streamDisconnectSample, "}")},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				before := string(tc.fe.ResponseBody)
				status := tc.fe.StatusCode
				out := markOpenAIRuleStreamDisconnect(tc.fe, tc.account, tc.eventType, []byte(tc.payload))
				require.False(t, out.OpenAIStreamDisconnect)
				require.Equal(t, status, out.StatusCode)
				require.Equal(t, before, string(out.ResponseBody))
			})
		}
	})

	require.Nil(t, markOpenAIRuleStreamDisconnect(nil, account, "error", []byte(streamDisconnectSample)))
}

func runStreamDisconnectStream(t *testing.T, upstream string, ctx context.Context) (string, error) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{cfg: &config.Config{}, toolCorrector: NewCodexToolCorrector()}
	recorder := newOpenAIResponseFlushRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstream)),
	}
	_, err := svc.handleStreamingResponse(ctx, resp, c, streamDisconnectAPIKeyAccount(), time.Now(), "gpt-5", "gpt-5")
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.body.String(), err
}

func lastSSEErrorData(t *testing.T, body string) string {
	t.Helper()
	var last string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") && gjson.Get(strings.TrimPrefix(line, "data: "), "type").String() == "error" {
			last = strings.TrimPrefix(line, "data: ")
		}
	}
	require.NotEmpty(t, last, "the error event reached the client: %s", body)
	return last
}

func TestOpenAIStreamDisconnectAfterOutputReachesClientAsServerError(t *testing.T) {
	const delta = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"

	body, _ := runStreamDisconnectStream(t, delta+"event: error\ndata: "+streamDisconnectSample+"\n\n", context.Background())
	event := lastSSEErrorData(t, body)
	require.Equal(t, "server_error", gjson.Get(event, "error.type").String())
	require.Equal(t, "request_timeout", gjson.Get(event, "error.code").String())
	require.Equal(t, "stream error: stream disconnected before completion: stream closed before response.completed", gjson.Get(event, "error.message").String())
	require.Equal(t, int64(97), gjson.Get(event, "sequence_number").Int())

	contextWindow := strings.Replace(streamDisconnectSample, "request_timeout", "context_length_exceeded", 1)
	body, _ = runStreamDisconnectStream(t, delta+"event: error\ndata: "+contextWindow+"\n\n", context.Background())
	require.Equal(t, "invalid_request_error", gjson.Get(lastSSEErrorData(t, body), "error.type").String(), "a real 400 keeps its type")
}

func TestOpenAIStreamDisconnectBeforeOutputRuleSwitchIsMarked(t *testing.T) {
	ctx := WithOpenAIFailoverPolicy(context.Background(), enabledFailoverPolicy(DefaultOpenAIFailoverRules()...))
	const created = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n"

	body, err := runStreamDisconnectStream(t, created+"event: error\ndata: "+streamDisconnectSample+"\n\n", ctx)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, "stream-disconnected", failoverErr.FailoverRuleID)
	require.True(t, failoverErr.OpenAIStreamDisconnect)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Equal(t, "server_error", gjson.GetBytes(failoverErr.ResponseBody, "error.type").String())
	require.Equal(t, "request_timeout", gjson.GetBytes(failoverErr.ResponseBody, "error.code").String())
	require.NotContains(t, body, "stream disconnected", "nothing of the failed attempt reaches the client")
}

func TestOpenAIStreamDisconnectNonStreamingRuleSwitchIsMarked(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	ctx := WithOpenAIFailoverPolicy(context.Background(), enabledFailoverPolicy(DefaultOpenAIFailoverRules()...))
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	body := sseTerminalBody("error", streamDisconnectSample)

	result, err := svc.handleSSEToJSON(newNonStreamingSSEResponse(), c, streamDisconnectAPIKeyAccount(), body, "model", "model")

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, "stream-disconnected", failoverErr.FailoverRuleID)
	require.True(t, failoverErr.OpenAIStreamDisconnect)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Equal(t, "request_timeout", gjson.GetBytes(failoverErr.ResponseBody, "error.code").String())
	require.Empty(t, rec.Body.String())
}
