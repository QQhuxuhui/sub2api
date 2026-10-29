package handler

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// preludeTestEngine serves path with handler behind the prelude writer and the
// client-first-byte tracker, the way the gateway routes are layered.
func preludeTestEngine(path string, handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware2.ClientFirstByte())
	r.Use(OpenAIStreamPreludeMiddleware(&service.IntentRouterService{}))
	r.POST(path, handler)
	return r
}

func activatePrelude(t *testing.T, c *gin.Context, chat bool) {
	t.Helper()
	value, ok := c.Get(openAIStreamPreludeWriterKey)
	require.True(t, ok)
	groupID := int64(1)
	value.(*openAIStreamPreludeWriter).arm(preludeTestRequest(chat), groupID, time.Time{})
}

func preludeTestRequest(chat bool) openAIStreamPreludeRequest {
	groupID := int64(1)
	return openAIStreamPreludeRequest{
		apiKey: &service.APIKey{ID: 9, GroupID: &groupID}, userID: 7, model: "gpt-6.1", schedule: "gpt-6.1", chat: chat,
	}
}

// armPrelude arms the writer with the moment after the given delay.
func armPrelude(t *testing.T, c *gin.Context, chat bool, delay time.Duration) *openAIStreamPreludeWriter {
	t.Helper()
	value, ok := c.Get(openAIStreamPreludeWriterKey)
	require.True(t, ok)
	w := value.(*openAIStreamPreludeWriter)
	w.arm(preludeTestRequest(chat), 1, time.Now().Add(delay))
	return w
}

func preludeState(w *openAIStreamPreludeWriter) openAIStreamPreludeState {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}

func TestOpenAIStreamPrelude_RealOutputBeforeTheMomentSendsNothing(t *testing.T) {
	const upstream = "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"
	r := preludeTestEngine("/v1/responses", func(c *gin.Context) {
		w := armPrelude(t, c, false, 80*time.Millisecond)
		c.Header("Content-Type", "text/event-stream")
		c.Status(http.StatusOK)
		c.Writer.Flush()
		_, _ = c.Writer.WriteString(": ping\n\n")
		require.Equal(t, preludeArmed, preludeState(w), "headers and comments do not count as output")
		_, _ = c.Writer.WriteString(upstream)
		c.Writer.Flush()
		require.Equal(t, preludeDormant, preludeState(w))
		time.Sleep(120 * time.Millisecond)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`)))
	require.Equal(t, ": ping\n\n"+upstream, w.Body.String(), "exactly as without the feature")
}

func TestOpenAIStreamPrelude_ErrorBeforeTheMomentKeepsItsStatus(t *testing.T) {
	r := preludeTestEngine("/v1/chat/completions", func(c *gin.Context) {
		armPrelude(t, c, true, 80*time.Millisecond)
		c.Header("Retry-After", "3")
		c.JSON(http.StatusTooManyRequests, gin.H{"error": gin.H{"message": "busy"}})
		time.Sleep(120 * time.Millisecond)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`)))
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "3", w.Header().Get("Retry-After"), "headers set while armed reach the client")
	require.JSONEq(t, `{"error":{"message":"busy"}}`, w.Body.String())
}

func TestOpenAIStreamPrelude_FiresAfterHeadersAndPings(t *testing.T) {
	r := preludeTestEngine("/v1/chat/completions", func(c *gin.Context) {
		w := armPrelude(t, c, true, 30*time.Millisecond)
		start := time.Now()
		c.Header("Content-Type", "text/event-stream")
		c.Status(http.StatusOK)
		c.Writer.Flush()
		_, _ = c.Writer.WriteString(": ping\n\n")
		sizeBefore := c.Writer.Size()
		require.Eventually(t, func() bool { return preludeState(w) == preludeActive }, time.Second, 2*time.Millisecond)
		require.GreaterOrEqual(t, time.Since(start), 25*time.Millisecond, "not before the moment")
		require.Equal(t, sizeBefore, c.Writer.Size(), "the early event is not part of the handler's response")
		_, _ = c.Writer.WriteString(`data: {"id":"up","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"}}]}` + "\n\n")
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`)))
	body := w.Body.String()
	require.True(t, strings.HasPrefix(body, ": ping\n\ndata: "), body)
	lines := sseDataLines(body)
	require.Len(t, lines, 2)
	require.Equal(t, "assistant", gjson.Get(lines[0], "choices.0.delta.role").String())
	require.Equal(t, gjson.Get(lines[0], "id").String(), gjson.Get(lines[1], "id").String())
}

func TestOpenAIStreamPreludeRandomDelay(t *testing.T) {
	for i := 0; i < 200; i++ {
		d := openAIStreamPreludeRandomDelay(800*time.Millisecond, 1800*time.Millisecond)
		require.GreaterOrEqual(t, d, 800*time.Millisecond)
		require.LessOrEqual(t, d, 1800*time.Millisecond)
	}
	require.Equal(t, time.Second, openAIStreamPreludeRandomDelay(time.Second, time.Second))
}

func sseDataLines(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			out = append(out, data)
		}
	}
	return out
}

func TestOpenAIStreamPrelude_ChatPassesStreamWithOneID(t *testing.T) {
	r := preludeTestEngine("/v1/chat/completions", func(c *gin.Context) {
		activatePrelude(t, c, true)
		// Inside, nothing has been written yet: failover stays possible.
		require.Equal(t, -1, c.Writer.Size())
		require.False(t, c.Writer.Written())
		preludeAt, marked := middleware2.ClientFirstByteTracker(c).FirstByteAt()
		require.True(t, marked, "the early event is the client's first byte, as a downstream sees it")

		c.Header("Content-Type", "text/event-stream")
		c.Status(http.StatusOK)
		_, _ = c.Writer.WriteString(`data: {"id":"up-1","object":"chat.completion.chunk","choices":[{"index":0,`)
		c.Writer.Flush()
		_, _ = c.Writer.WriteString(`"delta":{"content":"hi"}}]}` + "\n\n")
		_, _ = c.Writer.WriteString("data: [DONE]\n\n")
		c.Writer.Flush()
		firstAt, marked := middleware2.ClientFirstByteTracker(c).FirstByteAt()
		require.True(t, marked)
		require.Equal(t, preludeAt, firstAt, "real output must not move the first byte")
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`)))

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	lines := sseDataLines(w.Body.String())
	require.Len(t, lines, 3)
	first := gjson.Parse(lines[0])
	require.Equal(t, "assistant", first.Get("choices.0.delta.role").String())
	require.Equal(t, "", first.Get("choices.0.delta.content").String())
	require.True(t, first.Get("choices.0.delta.content").Exists())
	require.Equal(t, "gpt-6.1", first.Get("model").String())
	id := first.Get("id").String()
	require.True(t, strings.HasPrefix(id, "chatcmpl-"))
	require.Equal(t, id, gjson.Get(lines[1], "id").String(), "upstream chunk carries the early id")
	require.Equal(t, "hi", gjson.Get(lines[1], "choices.0.delta.content").String())
	require.Equal(t, "[DONE]", lines[2])
}

func TestOpenAIStreamPrelude_ResponsesErrorBecomesFailedEvent(t *testing.T) {
	r := preludeTestEngine("/v1/responses", func(c *gin.Context) {
		activatePrelude(t, c, false)
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"type": "upstream_error", "message": "boom"}})
		require.Equal(t, http.StatusBadGateway, c.Writer.Status(), "the ops logger sees the real status")
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`)))

	require.Equal(t, http.StatusOK, w.Code, "committed by the early event")
	body := w.Body.String()
	require.True(t, strings.HasPrefix(body, "event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n"), body)
	require.Contains(t, body, "event: response.failed\n")
	lines := sseDataLines(body)
	failed := gjson.Parse(lines[len(lines)-1])
	require.Equal(t, "response.failed", failed.Get("type").String())
	require.Equal(t, "boom", failed.Get("response.error.message").String())
	require.Equal(t, "upstream_error", failed.Get("response.error.code").String())
	require.NotContains(t, body, `{"error"`, "the JSON error is not sent as is")
}

func TestOpenAIStreamPrelude_ChatErrorBecomesErrorEvent(t *testing.T) {
	r := preludeTestEngine("/v1/chat/completions", func(c *gin.Context) {
		activatePrelude(t, c, true)
		c.JSON(http.StatusTooManyRequests, gin.H{"error": gin.H{"type": "rate_limit_error", "code": "x", "message": "slow down"}})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`)))

	require.Contains(t, w.Body.String(), "event: error\n")
	lines := sseDataLines(w.Body.String())
	last := gjson.Parse(lines[len(lines)-1])
	require.Equal(t, "slow down", last.Get("error.message").String())
	require.Equal(t, "rate_limit_error", last.Get("error.type").String())
	require.Equal(t, "x", last.Get("error.code").String())
}

func TestOpenAIStreamPrelude_HeartbeatUntilOutputAndHiddenFromSize(t *testing.T) {
	old := openAIStreamPreludeHeartbeat
	openAIStreamPreludeHeartbeat = 15 * time.Millisecond
	t.Cleanup(func() { openAIStreamPreludeHeartbeat = old })

	r := preludeTestEngine("/v1/responses", func(c *gin.Context) {
		activatePrelude(t, c, false)
		value, _ := c.Get(openAIStreamPreludeWriterKey)
		pw := value.(*openAIStreamPreludeWriter)
		count := func() int {
			pw.mu.Lock()
			defer pw.mu.Unlock()
			return pw.heartbeats
		}
		require.Eventually(t, func() bool { return count() >= 2 }, time.Second, 5*time.Millisecond)
		require.Equal(t, -1, c.Writer.Size(), "heartbeats are not a written response")
		c.Header("Content-Type", "text/event-stream")
		_, _ = c.Writer.WriteString("event: response.created\ndata: {\"type\":\"response.created\"}\n\n")
		c.Writer.Flush()
		before := count()
		time.Sleep(60 * time.Millisecond)
		require.Equal(t, before, count(), "no heartbeat once real output started")
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`)))
	body := rec.Body.String()
	require.GreaterOrEqual(t, strings.Count(body, "event: keepalive"), 3, "the early event and its repeats")
	require.True(t, strings.HasSuffix(body, "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"))
}

func TestOpenAIStreamPrelude_DormantChangesNothing(t *testing.T) {
	r := preludeTestEngine("/v1/chat/completions", func(c *gin.Context) {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "bad"}})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`)))
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.JSONEq(t, `{"error":{"message":"bad"}}`, w.Body.String())
}

func TestOpenAIStreamPreludePath(t *testing.T) {
	require.True(t, openAIStreamPreludePath("/v1/chat/completions"))
	require.True(t, openAIStreamPreludePath("/responses"))
	require.True(t, openAIStreamPreludePath("/backend-api/codex/responses"))
	require.False(t, openAIStreamPreludePath("/v1/responses/compact"))
	require.False(t, openAIStreamPreludePath("/v1/messages"))
}

// The handler keeps setting headers while the timer fires: the timer must not
// touch the map the handler writes (run with -race).
func TestOpenAIStreamPrelude_TimerDoesNotRaceHandlerHeaders(t *testing.T) {
	for i := 0; i < 20; i++ {
		r := preludeTestEngine("/v1/responses", func(c *gin.Context) {
			w := armPrelude(t, c, false, time.Millisecond)
			deadline := time.Now().Add(50 * time.Millisecond)
			for n := 0; preludeState(w) != preludeActive && time.Now().Before(deadline); n++ {
				c.Header("X-Test", strconv.Itoa(n))
				_ = c.Writer.Header().Get("X-Test")
			}
			require.Equal(t, preludeActive, preludeState(w))
		})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`)))
		require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	}
}

func preludeOutcome(t *testing.T, path string, chat bool, handler func(c *gin.Context)) string {
	t.Helper()
	var pw *openAIStreamPreludeWriter
	r := preludeTestEngine(path, func(c *gin.Context) {
		activatePrelude(t, c, chat)
		value, _ := c.Get(openAIStreamPreludeWriterKey)
		pw = value.(*openAIStreamPreludeWriter)
		handler(c)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
	return pw.outcome
}

func TestOpenAIStreamPrelude_OutcomeSeesInStreamFailures(t *testing.T) {
	sse := func(c *gin.Context, parts ...string) {
		c.Header("Content-Type", "text/event-stream")
		for _, part := range parts {
			_, _ = c.Writer.WriteString(part)
			c.Writer.Flush()
		}
	}
	cases := []struct {
		name    string
		path    string
		chat    bool
		parts   []string
		outcome string
	}{
		{"responses completed", "/v1/responses", false, []string{
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"an \\\"error\\\" word\"}\n\n",
			"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n",
		}, service.OpenAIStreamPreludeOutcomeOK},
		{"responses failed split across writes", "/v1/responses", false, []string{
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\nevent: response.fa",
			"iled\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_error\"}}}\n\n",
		}, service.OpenAIStreamPreludeOutcomeFailed},
		{"responses error data without event line", "/v1/responses", false, []string{
			"data: {\"type\":\"error\",\"code\":\"server_is_overloaded\"}\n\n",
		}, service.OpenAIStreamPreludeOutcomeFailed},
		{"chat error object", "/v1/chat/completions", true, []string{
			"data: {\"id\":\"a\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n",
			"data: {\"error\":{\"message\":\"boom\"}}\n\n",
		}, service.OpenAIStreamPreludeOutcomeFailed},
		{"chat done", "/v1/chat/completions", true, []string{
			"data: {\"id\":\"a\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"error\"}}]}\n\n",
			"data: [DONE]\n\n",
		}, service.OpenAIStreamPreludeOutcomeOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := preludeOutcome(t, tc.path, tc.chat, func(c *gin.Context) { sse(c, tc.parts...) })
			require.Equal(t, tc.outcome, got)
		})
	}

	// A stream error the handler marked for ops counts too.
	got := preludeOutcome(t, "/v1/chat/completions", true, func(c *gin.Context) {
		sse(c, "data: {\"id\":\"a\",\"object\":\"chat.completion.chunk\",\"choices\":[]}\n\n")
		service.MarkOpsStreamError(c, "upstream_error", "cut", http.StatusBadGateway)
	})
	require.Equal(t, service.OpenAIStreamPreludeOutcomeFailed, got)
}
