//go:build unit

package handler

import (
	"bytes"
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
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// The exact frame CLIProxyAPI sent on gptniubi group 4 (account 42).
const streamDisconnectFrame = `{"error":{"code":"request_timeout","message":"stream error: stream disconnected before completion: stream closed before response.completed","param":null,"type":"invalid_request_error"},"sequence_number":97,"type":"error"}`

const streamDisconnectMessage = "stream error: stream disconnected before completion: stream closed before response.completed"

func upstreamDisconnectBeforeOutput() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n" +
				"event: error\ndata: " + streamDisconnectFrame + "\n\n")),
	}
}

// newAPIKeyResponsesFailoverTestHandler is newOpenAIResponsesFailoverTestHandler
// with two OpenAI API-key accounts, the account type behind CLIProxyAPI.
func newAPIKeyResponsesFailoverTestHandler(t *testing.T, upstream service.HTTPUpstream) *OpenAIGatewayHandler {
	t.Helper()
	account := func(id int64, priority int) service.Account {
		return service.Account{
			ID: id, Name: "apikey-account", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Priority: priority,
			Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://upstream.example"},
		}
	}
	accountRepo := openAIImagesFailoverAccountRepo{accounts: []service.Account{account(1, 0), account(2, 1)}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Security.URLAllowlist.Enabled = false
	gatewayService := service.NewOpenAIGatewayService(
		accountRepo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, upstream,
		nil, nil, nil, nil, nil, nil, nil, nil,
	)
	billingService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingService.Stop)
	h := NewOpenAIGatewayHandler(
		gatewayService,
		service.NewConcurrencyService(nil),
		billingService,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil, nil, nil, nil,
		cfg,
	)
	h.maxAccountSwitches = 10
	return h
}

func TestOpenAIRuleStreamDisconnect_ExhaustedReturns502ServerError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := newAstraProCapturedUpstream(upstreamDisconnectBeforeOutput(), upstreamDisconnectBeforeOutput())
	h := newAPIKeyResponsesFailoverTestHandler(t, upstream)
	enableFailoverPolicy(t, h, []int64{failoverTestGroupID}, func(cfg *service.OpenAIFailoverConfig) { cfg.MaxRuleSwitches = 1 })
	c, rec := newAstraProFailoverContext(t, preludeStreamRequestBody)

	h.Responses(c)

	_, accountIDs, _ := upstream.snapshot()
	require.ElementsMatch(t, []int64{1, 2}, accountIDs, "each account once")
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	require.True(t, strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json"), rec.Header().Get("Content-Type"))
	require.Equal(t, "server_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Equal(t, "request_timeout", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
	require.Contains(t, gjson.GetBytes(rec.Body.Bytes(), "error.message").String(), "stream disconnected before completion")
}

func streamDisconnectRuleFailoverErr() *service.UpstreamFailoverError {
	return &service.UpstreamFailoverError{
		StatusCode:             http.StatusBadGateway,
		FailoverRuleID:         "stream-disconnected",
		OpenAIStreamDisconnect: true,
		ResponseBody:           []byte(`{"error":{"type":"server_error","message":"` + streamDisconnectMessage + `","code":"request_timeout"}}`),
	}
}

func newStreamDisconnectHandlerContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c, rec
}

func TestOpenAIRuleStreamDisconnect_NotStartedOverridesSSEContentType(t *testing.T) {
	c, rec := newStreamDisconnectHandlerContext()
	// The stream handler sets SSE headers before it reads the first frame.
	c.Header("Content-Type", "text/event-stream")

	(&OpenAIGatewayHandler{}).writeOpenAIRuleFailoverExhausted(c, streamDisconnectRuleFailoverErr(), false)

	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.True(t, strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json"), rec.Header().Get("Content-Type"))
	require.Equal(t, "server_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Equal(t, "request_timeout", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
	require.Equal(t, streamDisconnectMessage, gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
}

func responsesFailedEvents(body string) []gjson.Result {
	var events []gjson.Result
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		event := gjson.Parse(strings.TrimPrefix(line, "data: "))
		if event.Get("type").String() == "response.failed" {
			events = append(events, event)
		}
	}
	return events
}

func TestOpenAIRuleStreamDisconnect_StartedEndsWithOneFailedEvent(t *testing.T) {
	c, rec := newStreamDisconnectHandlerContext()
	c.Header("Content-Type", "text/event-stream")
	_, _ = c.Writer.WriteString(": ping\n\n")
	c.Writer.Flush()

	(&OpenAIGatewayHandler{}).writeOpenAIRuleFailoverExhausted(c, streamDisconnectRuleFailoverErr(), true)

	require.Equal(t, http.StatusOK, rec.Code)
	events := responsesFailedEvents(rec.Body.String())
	require.Len(t, events, 1, rec.Body.String())
	require.Equal(t, "request_timeout", events[0].Get("response.error.code").String())
	require.Equal(t, streamDisconnectMessage, events[0].Get("response.error.message").String())
	require.NotContains(t, rec.Body.String(), `{"error"`, "no bare JSON on a committed stream")
}

func TestOpenAIRuleStreamDisconnect_CompactKeepaliveCommittedUsesStreamBranch(t *testing.T) {
	c, rec := newStreamDisconnectHandlerContext()
	c.Request = httptest.NewRequest(http.MethodPost, EndpointResponses, nil)
	service.MarkOpenAICompactClientStream(c)
	stop := service.StartOpenAICompactSSEKeepalive(c, 5*time.Millisecond)
	defer stop()
	require.Eventually(t, c.Writer.Written, time.Second, time.Millisecond)

	// The handler captured streamStarted=false before the heartbeat went out.
	(&OpenAIGatewayHandler{}).writeOpenAIRuleFailoverExhausted(c, streamDisconnectRuleFailoverErr(), false)

	require.Equal(t, http.StatusOK, rec.Code, "the heartbeat already committed the status")
	events := responsesFailedEvents(rec.Body.String())
	require.Len(t, events, 1, rec.Body.String())
	require.Equal(t, "request_timeout", events[0].Get("response.error.code").String())
	require.NotContains(t, rec.Body.String(), `{"error"`, "no bare JSON on a committed stream")
}

func TestOpenAIRuleStreamDisconnect_PreludeTurnsJSONIntoFailedEvent(t *testing.T) {
	r := preludeTestEngine("/v1/responses", func(c *gin.Context) {
		activatePrelude(t, c, false)
		(&OpenAIGatewayHandler{}).writeOpenAIRuleFailoverExhausted(c, streamDisconnectRuleFailoverErr(), false)
		require.Equal(t, http.StatusBadGateway, c.Writer.Status(), "inside, the handler's own status")
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`)))

	require.Equal(t, http.StatusOK, rec.Code, "committed by the early event")
	events := responsesFailedEvents(rec.Body.String())
	require.Len(t, events, 1, rec.Body.String())
	require.Equal(t, "request_timeout", events[0].Get("response.error.code").String())
	require.Equal(t, streamDisconnectMessage, events[0].Get("response.error.message").String())
}

func TestOpenAIRuleStreamDisconnect_UnmarkedRuleErrorKeepsItsShape(t *testing.T) {
	c, rec := newStreamDisconnectHandlerContext()
	fe := &service.UpstreamFailoverError{
		StatusCode:     http.StatusBadRequest,
		FailoverRuleID: "encrypted-content",
		ResponseBody:   []byte(encryptedErrorBody),
	}

	(&OpenAIGatewayHandler{}).writeOpenAIRuleFailoverExhausted(c, fe, false)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Equal(t, "thinking_signature_invalid", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
}

func TestNormalizeOpsUpstreamStreamDisconnect(t *testing.T) {
	inStream := parsedOpsError{ErrorType: "invalid_request_error", Code: "request_timeout", Message: streamDisconnectMessage, StreamFailure: true}
	got := normalizeOpsUpstreamStreamDisconnect(inStream, http.StatusOK)
	require.Equal(t, "upstream_error", got.ErrorType)
	require.Equal(t, http.StatusBadGateway, got.StatusCode)

	explicit5xx := inStream
	explicit5xx.StatusCode = http.StatusServiceUnavailable
	require.Equal(t, http.StatusServiceUnavailable, normalizeOpsUpstreamStreamDisconnect(explicit5xx, http.StatusOK).StatusCode)

	exhaustedJSON := parsedOpsError{ErrorType: "server_error", Code: "request_timeout", Message: streamDisconnectMessage}
	got = normalizeOpsUpstreamStreamDisconnect(exhaustedJSON, http.StatusBadGateway)
	require.Equal(t, "upstream_error", got.ErrorType)
	require.Zero(t, got.StatusCode, "a JSON error keeps the wire status")

	require.Equal(t, exhaustedJSON, normalizeOpsUpstreamStreamDisconnect(exhaustedJSON, http.StatusBadRequest), "a non-stream 4xx is left alone")

	realBadRequest := inStream
	realBadRequest.Code = "context_length_exceeded"
	require.Equal(t, realBadRequest, normalizeOpsUpstreamStreamDisconnect(realBadRequest, http.StatusOK))
}

func TestOpsErrorLoggerMiddleware_StreamDisconnectIsUpstream502(t *testing.T) {
	for name, frame := range map[string]string{
		"as upstream sent it": streamDisconnectFrame,
		"after the rewrite":   strings.Replace(streamDisconnectFrame, "invalid_request_error", "server_error", 1),
	} {
		t.Run(name, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 2)
			gin.SetMode(gin.TestMode)
			ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.POST("/v1/responses", func(c *gin.Context) {
				c.Status(http.StatusOK)
				_, _ = c.Writer.WriteString("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\n")
				_, _ = c.Writer.WriteString("event: error\ndata: " + frame + "\n\n")
			})

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))

			require.Equal(t, int64(1), OpsErrorLogQueueLength())
			job := <-opsErrorLogQueue
			require.Equal(t, http.StatusBadGateway, job.entry.StatusCode)
			require.Equal(t, "upstream_error", job.entry.ErrorType)
			require.Equal(t, "upstream", job.entry.ErrorPhase)
			require.NotNil(t, job.entry.UpstreamStatusCode)
			require.Equal(t, http.StatusBadGateway, *job.entry.UpstreamStatusCode)
		})
	}
}

func TestOpsErrorLoggerMiddleware_StreamDisconnectExhaustedJSONIsUpstream(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 2)
	gin.SetMode(gin.TestMode)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/responses", func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		(&OpenAIGatewayHandler{}).writeOpenAIRuleFailoverExhausted(c, streamDisconnectRuleFailoverErr(), false)
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil)))

	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, int64(1), OpsErrorLogQueueLength())
	job := <-opsErrorLogQueue
	require.Equal(t, http.StatusBadGateway, job.entry.StatusCode)
	require.Equal(t, "upstream_error", job.entry.ErrorType)
	require.Equal(t, "upstream", job.entry.ErrorPhase)
}
