//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const (
	ssePingFrame         = "event: ping\ndata: {\"type\": \"ping\"}\n\n"
	sseMessageStartFrame = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"model\",\"usage\":{\"input_tokens\":3}}}\n\n"
	// What a sub2api upstream writes once its own stream has started: no event line.
	sseDataOnlyConcurrencyError = `{"type":"error","error":{"type":"rate_limit_error","code":"gateway_concurrency_limit","message":"Concurrency limit exceeded for user, please retry later"}}`
)

func runStreamForTest(t *testing.T, upstream string) (*httptest.ResponseRecorder, *streamingResult, error) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := newMinimalGatewayService()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(upstream))}
	result, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "model", "model", false)
	return rec, result, err
}

// A successful stream must reach the client byte for byte, pings included.
func TestHandleStreamingResponse_SuccessStreamUnchanged(t *testing.T) {
	upstream := ssePingFrame +
		sseMessageStartFrame +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		ssePingFrame +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	rec, result, err := runStreamForTest(t, upstream)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, upstream, rec.Body.String())
	require.Equal(t, 3, result.usage.InputTokens)
	require.Equal(t, 2, result.usage.OutputTokens)
}

func TestHandleStreamingResponse_ErrorBeforeFirstRealEvent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		upstream string
		written  string
	}{
		{"data-only error after pings", ssePingFrame + ssePingFrame + "data: " + sseDataOnlyConcurrencyError + "\n\n", ssePingFrame + ssePingFrame},
		{"data-only error first", "data: " + sseDataOnlyConcurrencyError + "\n\n", ""},
		{"named error after ping", ssePingFrame + "event: error\ndata: " + sseDataOnlyConcurrencyError + "\n\n", ssePingFrame},
		{"data-only ping then error", "data: {\"type\": \"ping\"}\n\ndata: " + sseDataOnlyConcurrencyError + "\n\n", "event: ping\ndata: {\"type\": \"ping\"}\n\n"},
		{"comment heartbeat then error", ": keepalive\n\ndata: " + sseDataOnlyConcurrencyError + "\n\n", ": keepalive\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, result, err := runStreamForTest(t, tc.upstream)
			require.Nil(t, result)
			var sseErr *sseStreamErrorEventError
			require.True(t, errors.As(err, &sseErr), "got %v", err)
			require.True(t, sseErr.BeforeSemanticOutput)
			require.JSONEq(t, sseDataOnlyConcurrencyError, sseErr.RawData)
			require.Equal(t, tc.written, rec.Body.String(), "the error frame must not reach the client")
		})
	}
}

// Once real content went out nothing changes: a data-only error frame is still
// forwarded as before and the stream ends without a terminal event.
func TestHandleStreamingResponse_DataOnlyErrorAfterRealEventUnchanged(t *testing.T) {
	rec, _, err := runStreamForTest(t, ssePingFrame+sseMessageStartFrame+"data: "+sseDataOnlyConcurrencyError+"\n\n")
	require.Error(t, err)
	var sseErr *sseStreamErrorEventError
	require.False(t, errors.As(err, &sseErr))
	require.Contains(t, err.Error(), "missing terminal event")
	require.Equal(t, ssePingFrame+sseMessageStartFrame+"event: error\ndata: "+sseDataOnlyConcurrencyError+"\n\n", rec.Body.String())
}

func TestHandleStreamingResponse_NamedErrorAfterRealEventKeepsOutputStarted(t *testing.T) {
	_, _, err := runStreamForTest(t, sseMessageStartFrame+"event: error\ndata: "+sseDataOnlyConcurrencyError+"\n\n")
	var sseErr *sseStreamErrorEventError
	require.True(t, errors.As(err, &sseErr))
	require.False(t, sseErr.BeforeSemanticOutput)
}

// Anything that is not a recognised heartbeat commits conservatively.
func TestHandleStreamingResponse_UnknownEventCommits(t *testing.T) {
	for _, first := range []string{
		"data: not-json\n\n",
		"event: something\n\n",
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
	} {
		_, _, err := runStreamForTest(t, first+"data: "+sseDataOnlyConcurrencyError+"\n\n")
		var sseErr *sseStreamErrorEventError
		require.False(t, errors.As(err, &sseErr), "after %q the error frame must be forwarded as before", first)
	}
}

func TestSSEStreamErrorEventFailoverClassification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := func(errType string) string {
		return `{"type":"error","error":{"type":"` + errType + `","message":"m"}}`
	}
	for _, tc := range []struct {
		name          string
		errType       string
		outputStarted bool
		preCommit     bool
		wantStatus    int
		wantSafe      bool
	}{
		// 标准路径、真实内容之前
		{"pre-commit rate limit", "rate_limit_error", false, true, http.StatusTooManyRequests, true},
		{"pre-commit overloaded", "overloaded_error", false, true, 529, true},
		{"pre-commit other", "upstream_error", false, true, http.StatusForbidden, true},
		// 标准路径、真实内容之后：与改动前一致
		{"post-commit rate limit", "rate_limit_error", true, false, http.StatusForbidden, false},
		{"post-commit overloaded", "overloaded_error", true, false, http.StatusForbidden, false},
		// 转换路径（chat completions / responses）：与改动前一致
		{"compat no output rate limit", "rate_limit_error", false, false, http.StatusForbidden, false},
		{"compat no output overloaded", "overloaded_error", false, false, 529, false},
		{"compat output started", "overloaded_error", true, false, http.StatusForbidden, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			repo := &rateLimitAccountRepoStub{}
			svc := newMinimalGatewayService()
			svc.rateLimitService = &RateLimitService{accountRepo: repo}
			account := &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"pool_mode": true}}
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}

			failoverErr := svc.sseStreamErrorEventFailover(context.Background(), c, account, resp, body(tc.errType), "model", tc.outputStarted, tc.preCommit)
			require.Equal(t, tc.wantStatus, failoverErr.StatusCode)
			require.Equal(t, tc.wantSafe, failoverErr.SafeToFailoverAfterWrite)
			require.False(t, failoverErr.RetryableOnSameAccount)
			require.Empty(t, failoverErr.FailoverRuleID)
			require.Zero(t, repo.setErrorCalls+repo.tempCalls+repo.rateLimitedCalls, "a pool-mode account is never marked")
		})
	}
}
