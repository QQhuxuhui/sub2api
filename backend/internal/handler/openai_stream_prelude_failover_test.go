//go:build unit

package handler

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const preludeStreamRequestBody = `{"model":"gpt-6-astra","stream":true,"input":"hello"}`

func preludeUpstreamStream() *http.Response {
	body := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

// activePreludeContext wraps the test context's writer in an activated prelude
// writer, the state a request in scope is in when the handler starts waiting.
func activePreludeContext(t *testing.T, c *gin.Context) *openAIStreamPreludeWriter {
	t.Helper()
	w := &openAIStreamPreludeWriter{ResponseWriter: c.Writer, c: c, router: &service.IntentRouterService{}, fwdSize: -1}
	c.Writer = w
	c.Set(openAIStreamPreludeWriterKey, w)
	apiKey := c.MustGet("api_key").(*service.APIKey)
	w.arm(openAIStreamPreludeRequest{apiKey: apiKey, userID: 100, model: "gpt-6-astra", schedule: "gpt-6-astra"}, *apiKey.GroupID, time.Time{})
	return w
}

// heartbeatFirstUpstream holds the first attempt until a heartbeat has gone
// out, so the heartbeat lands while the handler is forwarding.
type heartbeatFirstUpstream struct {
	*astraProCapturedUpstream
	w     *openAIStreamPreludeWriter
	calls int
}

func (u *heartbeatFirstUpstream) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	u.calls++
	if u.calls == 1 {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			u.w.mu.Lock()
			n := u.w.heartbeats
			u.w.mu.Unlock()
			if n > 0 {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	return u.astraProCapturedUpstream.Do(req, proxyURL, accountID, concurrency)
}

func TestOpenAIStreamPrelude_FailoverStillSwitchesAccounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	old := openAIStreamPreludeHeartbeat
	openAIStreamPreludeHeartbeat = 5 * time.Millisecond
	t.Cleanup(func() { openAIStreamPreludeHeartbeat = old })
	captured := newAstraProCapturedUpstream(astra403(), preludeUpstreamStream())
	upstream := &heartbeatFirstUpstream{astraProCapturedUpstream: captured}
	h := newOpenAIResponsesFailoverTestHandler(t, upstream)
	c, rec := newAstraProFailoverContext(t, preludeStreamRequestBody)
	w := activePreludeContext(t, c)
	upstream.w = w

	h.Responses(c)
	w.finish()

	require.Positive(t, w.heartbeats, "a heartbeat went out during the first attempt")
	_, accountIDs, _ := captured.snapshot()
	require.ElementsMatch(t, []int64{1, 2}, accountIDs, "a 403 after the early event and a heartbeat still fails over")
	body := rec.Body.String()
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, strings.HasPrefix(body, "event: response.relay_ready\ndata: {\"type\":\"response.relay_ready\"}\n\n"), body)
	require.Contains(t, body, "response.output_text.delta")
	require.Contains(t, body, "event: response.completed")
	require.NotContains(t, body, "Forbidden", "the first account's error never reaches the client")
}

func TestOpenAIStreamPrelude_ExhaustedFailoverEndsWithFailedEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := newAstraProCapturedUpstream(astra403(), astra403())
	h := newOpenAIResponsesFailoverTestHandler(t, upstream)
	c, rec := newAstraProFailoverContext(t, preludeStreamRequestBody)
	w := activePreludeContext(t, c)

	h.Responses(c)
	require.Equal(t, http.StatusBadGateway, c.Writer.Status(), "inside, the handler's own status")
	w.finish()

	_, accountIDs, _ := upstream.snapshot()
	require.Len(t, accountIDs, 2)
	body := rec.Body.String()
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, strings.HasPrefix(body, "event: response.relay_ready\n"), body)
	require.Contains(t, body, "event: response.failed\n")
	require.Contains(t, body, `"status":"failed"`)
}

// fireDuringFirstUpstream holds the first attempt until the armed early event
// has gone out.
type fireDuringFirstUpstream struct {
	*astraProCapturedUpstream
	w     *openAIStreamPreludeWriter
	calls int
}

func (u *fireDuringFirstUpstream) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	u.calls++
	if u.calls == 1 {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && preludeState(u.w) != preludeActive {
			time.Sleep(2 * time.Millisecond)
		}
	}
	return u.astraProCapturedUpstream.Do(req, proxyURL, accountID, concurrency)
}

func TestOpenAIStreamPrelude_FiresDuringForwardAndStillFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	captured := newAstraProCapturedUpstream(astra403(), preludeUpstreamStream())
	upstream := &fireDuringFirstUpstream{astraProCapturedUpstream: captured}
	h := newOpenAIResponsesFailoverTestHandler(t, upstream)
	c, rec := newAstraProFailoverContext(t, preludeStreamRequestBody)
	w := &openAIStreamPreludeWriter{ResponseWriter: c.Writer, c: c, router: &service.IntentRouterService{}, fwdSize: -1}
	c.Writer = w
	c.Set(openAIStreamPreludeWriterKey, w)
	apiKey := c.MustGet("api_key").(*service.APIKey)
	w.arm(openAIStreamPreludeRequest{apiKey: apiKey, userID: 100, model: "gpt-6-astra", schedule: "gpt-6-astra"}, *apiKey.GroupID, time.Now().Add(20*time.Millisecond))
	upstream.w = w

	h.Responses(c)
	w.finish()

	_, accountIDs, _ := captured.snapshot()
	require.ElementsMatch(t, []int64{1, 2}, accountIDs)
	body := rec.Body.String()
	require.True(t, strings.HasPrefix(body, "event: response.relay_ready\n"), body)
	require.Contains(t, body, "event: response.completed")
	require.NotContains(t, body, "Forbidden")
}
