//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const streamRetryTestMessage = "The model deepseek-v4.1-flash does not exist"

// streamRetryFailedSSE is an upstream that answers HTTP 200 and then fails
// inside the stream before any output. leadWithCreated adds the
// response.created event real Responses upstreams send first.
func streamRetryFailedSSE(leadWithCreated bool) *http.Response {
	var b strings.Builder
	if leadWithCreated {
		b.WriteString("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n")
	}
	b.WriteString("event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_1\",\"error\":{\"code\":\"model_not_found\",\"type\":\"invalid_request_error\",\"message\":\"" + streamRetryTestMessage + "\"}}}\n\n")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"req-up"}},
		Body:       io.NopCloser(strings.NewReader(b.String())),
	}
}

// newStreamRetryHarness reuses the rule-table harness (group 45, accounts
// 1184 then 1165) with the accounts in failing answering the in-stream
// failure. Account 1184 is a pool-mode account with two same-account retries.
func newStreamRetryHarness(t *testing.T, failing map[int64]bool, leadWithCreated bool) *openAIRuleHarness {
	t.Helper()
	hh := newOpenAIRuleHarness(t, nil, false)
	hh.upstream.respond = func(accountID int64) *http.Response {
		if failing[accountID] {
			return streamRetryFailedSSE(leadWithCreated)
		}
		return nil
	}
	hh.repo.accounts[0].Credentials["pool_mode"] = true
	hh.repo.accounts[0].Credentials["pool_mode_retry_count"] = 2
	return hh
}

func setStreamRetryCodes(t *testing.T, hh *openAIRuleHarness, codes []string) {
	t.Helper()
	cfg, err := hh.handler.openAIFailoverPolicyService.Config(context.Background())
	require.NoError(t, err)
	cfg.StreamFailedRetryCodes = codes
	_, err = hh.handler.openAIFailoverPolicyService.UpdateConfig(context.Background(), cfg)
	require.NoError(t, err)
}

func requireNoAccountPenalty(t *testing.T, hh *openAIRuleHarness) {
	t.Helper()
	require.Empty(t, hh.repo.setTempIDs)
	require.Empty(t, hh.repo.errorIDs())
	require.Empty(t, hh.repo.rateLimitedAccountIDs())
}

var streamRetryEndpoints = []struct {
	name            string
	path            string
	body            string
	leadWithCreated bool
	anthropicShape  bool
}{
	{"responses stream", "/v1/responses", `{"model":"deepseek-v4.1-flash","input":"hi","stream":true}`, true, false},
	{"responses non-stream answered with SSE", "/v1/responses", `{"model":"deepseek-v4.1-flash","input":"hi","stream":false}`, true, false},
	// The Chat Completions and Messages converters emit a client chunk for
	// response.created, so only a failure that comes first is replayable.
	{"chat completions stream", "/v1/chat/completions", `{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`, false, false},
	{"messages stream", "/v1/messages", `{"model":"deepseek-v4.1-flash","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":true}`, false, true},
}

func TestOpenAIStreamConfiguredRetryRecoversOnAnotherAccount(t *testing.T) {
	for _, endpoint := range streamRetryEndpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			hh := newStreamRetryHarness(t, map[int64]bool{1184: true}, endpoint.leadWithCreated)
			defer hh.cleanup()

			rec := hh.do(t, endpoint.path, endpoint.body)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			// One attempt plus the account's two pool-mode retries, then the switch.
			require.Equal(t, []int64{1184, 1184, 1184, 1165}, hh.upstream.accountHits())
			require.NotContains(t, rec.Body.String(), "model_not_found")
			requireNoAccountPenalty(t, hh)
		})
	}
}

func TestOpenAIStreamConfiguredRetryExhaustedAnswers400(t *testing.T) {
	for _, endpoint := range streamRetryEndpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			hh := newStreamRetryHarness(t, map[int64]bool{1184: true, 1165: true}, endpoint.leadWithCreated)
			defer hh.cleanup()

			rec := hh.do(t, endpoint.path, endpoint.body)

			require.Equal(t, []int64{1184, 1184, 1184, 1165}, hh.upstream.accountHits())
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
			require.True(t, gjson.Valid(rec.Body.String()), rec.Body.String())
			require.Equal(t, streamRetryTestMessage, gjson.Get(rec.Body.String(), "error.message").String())
			require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
			if endpoint.anthropicShape {
				require.Equal(t, "error", gjson.Get(rec.Body.String(), "type").String())
			} else {
				require.Equal(t, "model_not_found", gjson.Get(rec.Body.String(), "error.code").String())
			}
			requireNoAccountPenalty(t, hh)
		})
	}
}

// With the list emptied the Responses stream hands the failed event to the
// client on HTTP 200, as it did before the list existed.
func TestOpenAIStreamConfiguredRetryOffKeepsForwarding(t *testing.T) {
	hh := newStreamRetryHarness(t, map[int64]bool{1184: true}, true)
	defer hh.cleanup()
	setStreamRetryCodes(t, hh, []string{})

	rec := hh.do(t, "/v1/responses", streamRetryEndpoints[0].body)

	require.Equal(t, []int64{1184}, hh.upstream.accountHits())
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "response.failed")
	require.Contains(t, rec.Body.String(), "model_not_found")
}

func TestOpenAIStreamConfiguredRetryPassthroughAccount(t *testing.T) {
	hh := newStreamRetryHarness(t, map[int64]bool{1184: true, 1165: true}, true)
	defer hh.cleanup()
	for i := range hh.repo.accounts {
		hh.repo.accounts[i].Extra["openai_passthrough"] = true
	}

	rec := hh.do(t, "/v1/responses", streamRetryEndpoints[0].body)

	require.Equal(t, []int64{1184, 1184, 1184, 1165}, hh.upstream.accountHits())
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	require.Equal(t, "model_not_found", gjson.Get(rec.Body.String(), "error.code").String())
	require.Equal(t, streamRetryTestMessage, gjson.Get(rec.Body.String(), "error.message").String())
	requireNoAccountPenalty(t, hh)
}

// A listed code other than model_not_found has no exhaustion branch of its
// own in the handler; the list's branch answers it.
func TestOpenAIStreamConfiguredRetryExhaustedOtherCode(t *testing.T) {
	hh := newStreamRetryHarness(t, nil, true)
	defer hh.cleanup()
	setStreamRetryCodes(t, hh, []string{"deployment_not_ready"})
	hh.upstream.respond = func(int64) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader("event: response.failed\ndata: " +
				`{"type":"response.failed","response":{"error":{"code":"deployment_not_ready","message":"warming up"}}}` + "\n\n")),
		}
	}

	rec := hh.do(t, "/v1/responses", streamRetryEndpoints[0].body)

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, "deployment_not_ready", gjson.Get(rec.Body.String(), "error.code").String())
	require.Equal(t, "warming up", gjson.Get(rec.Body.String(), "error.message").String())
}

func TestWriteOpenAIStreamConfiguredRetryExhaustedLeavesOtherErrors(t *testing.T) {
	hh := newStreamRetryHarness(t, nil, true)
	defer hh.cleanup()
	h := hh.handler
	body := []byte(`{"error":{"code":"model_not_found","type":"invalid_request_error","message":"gone"}}`)
	listed := &service.UpstreamFailoverError{StatusCode: 400, Reason: service.OpenAIStreamConfiguredRetryReason, ResponseBody: body}

	c, rec := newOpenAIStreamRetryTestContext()
	require.False(t, h.writeOpenAIStreamConfiguredRetryExhausted(c, nil, false, false))
	require.False(t, h.writeOpenAIStreamConfiguredRetryExhausted(c, &service.UpstreamFailoverError{StatusCode: 400, ResponseBody: body}, false, false))
	require.False(t, h.writeOpenAIStreamConfiguredRetryExhausted(c, listed, true, false), "bytes already on the wire")
	require.False(t, c.Writer.Written())

	require.True(t, h.writeOpenAIStreamConfiguredRetryExhausted(c, listed, false, false))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.False(t, h.writeOpenAIStreamConfiguredRetryExhausted(c, listed, false, false), "already written")
}

func newOpenAIStreamRetryTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c, rec
}
