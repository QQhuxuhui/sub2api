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

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// An upstream waiting for capacity sends these and then fails about 20 seconds
// later, nothing having been produced yet.
var openAIKeepaliveStatusEvents = []struct {
	eventType string
	data      string
}{
	{eventType: "keepalive", data: `{"type":"keepalive","sequence_number":2}`},
	{eventType: "response.queued", data: `{"type":"response.queued","sequence_number":2,"response":{"id":"resp_k","status":"queued"}}`},
	{eventType: "response.metadata", data: `{"type":"response.metadata","sequence_number":2,"metadata":{"moderation":"pending"}}`},
}

const (
	openAIKeepaliveCreatedData   = `{"type":"response.created","sequence_number":0,"response":{"id":"resp_k","status":"in_progress"}}`
	openAIKeepaliveDeltaData     = `{"type":"response.output_text.delta","sequence_number":3,"delta":"hello"}`
	openAIKeepaliveCompletedData = `{"type":"response.completed","sequence_number":4,"response":{"id":"resp_k","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`
	openAIKeepaliveFailedData    = `{"type":"response.failed","sequence_number":3,"response":{"id":"resp_k","status":"failed","error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}}`
)

// openAIKeepaliveSSE renders one event; the event line is optional upstream, so
// the type may be found only in the data.
func openAIKeepaliveSSE(withEventLine bool, eventType, data string) string {
	if withEventLine {
		return "event: " + eventType + "\ndata: " + data + "\n\n"
	}
	return "data: " + data + "\n\n"
}

type openAIKeepaliveHandler struct {
	name string
	run  func(*OpenAIGatewayService, *gin.Context, *http.Response, *Account) (*int, error)
}

var openAIKeepaliveHandlers = []openAIKeepaliveHandler{
	{
		name: "native",
		run: func(svc *OpenAIGatewayService, c *gin.Context, resp *http.Response, account *Account) (*int, error) {
			result, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
			if result == nil {
				return nil, err
			}
			return result.firstTokenMs, err
		},
	},
	{
		name: "passthrough",
		run: func(svc *OpenAIGatewayService, c *gin.Context, resp *http.Response, account *Account) (*int, error) {
			result, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
			if result == nil {
				return nil, err
			}
			return result.firstTokenMs, err
		},
	},
}

func newOpenAIKeepaliveStreamTest(t *testing.T, body io.ReadCloser) (*OpenAIGatewayService, *gin.Context, *httptest.ResponseRecorder, *http.Response, *Account) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"X-Request-Id": []string{"rid-keepalive"}},
		Body:       body,
	}
	return svc, c, rec, resp, &Account{ID: 116, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Name: "cpa"}
}

func forEachOpenAIKeepaliveCase(t *testing.T, run func(t *testing.T, handler openAIKeepaliveHandler, withEventLine bool, eventType, data string)) {
	t.Helper()
	for _, handler := range openAIKeepaliveHandlers {
		for _, event := range openAIKeepaliveStatusEvents {
			for _, withEventLine := range []bool{true, false} {
				name := handler.name + "/" + event.eventType + "/data_only"
				if withEventLine {
					name = handler.name + "/" + event.eventType + "/event_line"
				}
				t.Run(name, func(t *testing.T) {
					run(t, handler, withEventLine, event.eventType, event.data)
				})
			}
		}
	}
}

func TestOpenAIStreamKeepaliveEventsAreNotOutput(t *testing.T) {
	for _, event := range openAIKeepaliveStatusEvents {
		t.Run(event.eventType, func(t *testing.T) {
			require.True(t, openAIStreamEventIsMetadata(event.eventType))
			require.True(t, openAIStreamEventIsMetadata(" "+event.eventType+" "))
			require.False(t, openAIStreamDataStartsClientOutput(event.data, event.eventType))
			require.False(t, openAIStreamDataStartsSemanticTTFT(event.data, event.eventType))
			require.False(t, openAIStreamDataStartsSemanticTTFT(event.data, ""), "the type may only be in the data")
			require.False(t, openAIStreamDataStartsVisibleOutput(event.data, event.eventType))
			require.False(t, isOpenAIWSTokenEvent(event.eventType))
		})
	}
}

func TestOpenAIStreamKeepaliveThenOverloadFailsOverWithoutWriting(t *testing.T) {
	forEachOpenAIKeepaliveCase(t, func(t *testing.T, handler openAIKeepaliveHandler, withEventLine bool, eventType, data string) {
		stream := openAIKeepaliveSSE(withEventLine, "response.created", openAIKeepaliveCreatedData) +
			openAIKeepaliveSSE(withEventLine, eventType, data) +
			openAIKeepaliveSSE(withEventLine, "response.failed", openAIKeepaliveFailedData)
		svc, c, rec, resp, account := newOpenAIKeepaliveStreamTest(t, io.NopCloser(strings.NewReader(stream)))

		_, err := handler.run(svc, c, resp, account)

		var failoverErr *UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.Equal(t, http.StatusServiceUnavailable, failoverErr.ClientStatusCode)
		require.False(t, c.Writer.Written())
		require.Empty(t, rec.Body.String())
	})
}

func TestOpenAIStreamKeepaliveGoesOutInOrderWithTheRealOutput(t *testing.T) {
	forEachOpenAIKeepaliveCase(t, func(t *testing.T, handler openAIKeepaliveHandler, withEventLine bool, eventType, data string) {
		reader, writer := io.Pipe()
		go func() {
			defer func() { _ = writer.Close() }()
			_, _ = io.WriteString(writer, openAIKeepaliveSSE(withEventLine, "response.created", openAIKeepaliveCreatedData))
			_, _ = io.WriteString(writer, openAIKeepaliveSSE(withEventLine, eventType, data))
			time.Sleep(120 * time.Millisecond)
			_, _ = io.WriteString(writer, openAIKeepaliveSSE(withEventLine, "response.output_text.delta", openAIKeepaliveDeltaData))
			_, _ = io.WriteString(writer, openAIKeepaliveSSE(withEventLine, "response.completed", openAIKeepaliveCompletedData))
		}()
		svc, c, rec, resp, account := newOpenAIKeepaliveStreamTest(t, reader)

		firstTokenMs, err := handler.run(svc, c, resp, account)

		require.NoError(t, err)
		body := rec.Body.String()
		created := strings.Index(body, `"type":"response.created"`)
		status := strings.Index(body, `"type":"`+eventType+`"`)
		output := strings.Index(body, "hello")
		require.GreaterOrEqual(t, created, 0, body)
		require.Greater(t, status, created, "the frame keeps its place")
		require.Greater(t, output, status)
		require.NotNil(t, firstTokenMs)
		require.GreaterOrEqual(t, *firstTokenMs, 100, "semantic TTFT starts at the output, not at the status event")
	})
}

func TestOpenAIStreamKeepaliveAfterRealOutputDoesNotFailOver(t *testing.T) {
	forEachOpenAIKeepaliveCase(t, func(t *testing.T, handler openAIKeepaliveHandler, withEventLine bool, eventType, data string) {
		stream := openAIKeepaliveSSE(withEventLine, "response.created", openAIKeepaliveCreatedData) +
			openAIKeepaliveSSE(withEventLine, "response.output_text.delta", openAIKeepaliveDeltaData) +
			openAIKeepaliveSSE(withEventLine, eventType, data) +
			openAIKeepaliveSSE(withEventLine, "response.failed", openAIKeepaliveFailedData)
		svc, c, rec, resp, account := newOpenAIKeepaliveStreamTest(t, io.NopCloser(strings.NewReader(stream)))

		_, err := handler.run(svc, c, resp, account)

		var failoverErr *UpstreamFailoverError
		require.False(t, errors.As(err, &failoverErr), "the stream cannot be replayed once output went out")
		require.Contains(t, rec.Body.String(), "hello")
	})
}

func TestOpenAINativeKeepaliveDoesNotDisarmFirstOutputTimeout(t *testing.T) {
	for _, event := range openAIKeepaliveStatusEvents {
		t.Run(event.eventType, func(t *testing.T) {
			reader, writer := io.Pipe()
			writerDone := make(chan struct{})
			go func() {
				defer close(writerDone)
				defer func() { _ = writer.Close() }()
				_, _ = io.WriteString(writer, openAIKeepaliveSSE(true, "response.created", openAIKeepaliveCreatedData))
				_, _ = io.WriteString(writer, openAIKeepaliveSSE(true, event.eventType, event.data))
				time.Sleep(200 * time.Millisecond)
			}()
			svc, c, rec, resp, account := newOpenAIKeepaliveStreamTest(t, reader)
			svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1

			// The attempt started two seconds ago, so the guard is already due.
			_, err := svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now().Add(-2*time.Second), "model", "model")

			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusGatewayTimeout, failoverErr.StatusCode)
			require.Contains(t, string(failoverErr.ResponseBody), "first_output_timeout")
			require.Empty(t, rec.Body.String())
			select {
			case <-writerDone:
			case <-time.After(time.Second):
				t.Fatal("synthetic upstream writer did not exit")
			}
		})
	}
}

func runOpenAIKeepaliveBridgeTurn(t *testing.T, body io.ReadCloser, turn int) (*OpenAIForwardResult, [][]byte, error) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"X-Request-Id": []string{"rid-ws-bridge-keepalive"}},
		Body:       body,
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 14, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
	var writes [][]byte

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test", payload, len(payload),
		"gpt-5", "", "", "", "", turn,
		func(message []byte) error {
			writes = append(writes, append([]byte(nil), message...))
			return nil
		},
	)
	return result, writes, err
}

func TestOpenAIWSHTTPBridgeKeepaliveThenOverloadFailsOverOnFirstTurn(t *testing.T) {
	for _, event := range openAIKeepaliveStatusEvents {
		t.Run(event.eventType, func(t *testing.T) {
			stream := openAIKeepaliveSSE(false, "", openAIKeepaliveCreatedData) +
				openAIKeepaliveSSE(false, "", event.data) +
				openAIKeepaliveSSE(false, "", openAIKeepaliveFailedData)

			result, writes, err := runOpenAIKeepaliveBridgeTurn(t, io.NopCloser(strings.NewReader(stream)), 1)

			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Empty(t, writes)
		})
	}
}

func TestOpenAIWSHTTPBridgeKeepaliveGoesOutInOrderWithTheRealOutput(t *testing.T) {
	for _, event := range openAIKeepaliveStatusEvents {
		t.Run(event.eventType, func(t *testing.T) {
			reader, writer := io.Pipe()
			go func() {
				defer func() { _ = writer.Close() }()
				_, _ = io.WriteString(writer, openAIKeepaliveSSE(false, "", openAIKeepaliveCreatedData))
				_, _ = io.WriteString(writer, openAIKeepaliveSSE(false, "", event.data))
				time.Sleep(120 * time.Millisecond)
				_, _ = io.WriteString(writer, openAIKeepaliveSSE(false, "", openAIKeepaliveDeltaData))
				_, _ = io.WriteString(writer, openAIKeepaliveSSE(false, "", openAIKeepaliveCompletedData))
			}()

			result, writes, err := runOpenAIKeepaliveBridgeTurn(t, reader, 1)

			require.NoError(t, err)
			require.Len(t, writes, 4)
			require.Contains(t, string(writes[0]), `"type":"response.created"`)
			require.Contains(t, string(writes[1]), `"type":"`+event.eventType+`"`)
			require.Contains(t, string(writes[2]), "hello")
			require.Contains(t, string(writes[3]), `"type":"response.completed"`)
			require.NotNil(t, result.FirstTokenMs)
			require.GreaterOrEqual(t, *result.FirstTokenMs, 100, "TTFT starts at the output, not at the status event")
		})
	}
}

func TestOpenAIWSHTTPBridgeKeepaliveAfterRealOutputDoesNotFailOver(t *testing.T) {
	for _, event := range openAIKeepaliveStatusEvents {
		t.Run(event.eventType, func(t *testing.T) {
			stream := openAIKeepaliveSSE(false, "", openAIKeepaliveCreatedData) +
				openAIKeepaliveSSE(false, "", openAIKeepaliveDeltaData) +
				openAIKeepaliveSSE(false, "", event.data) +
				openAIKeepaliveSSE(false, "", openAIKeepaliveFailedData)

			_, writes, err := runOpenAIKeepaliveBridgeTurn(t, io.NopCloser(strings.NewReader(stream)), 1)

			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr), "the stream cannot be replayed once output went out")
			require.Len(t, writes, 4)
			require.Contains(t, string(writes[1]), "hello")
		})
	}
}

func TestOpenAIWSHTTPBridgeKeepaliveOnLaterTurnGoesStraightToTheClient(t *testing.T) {
	stream := openAIKeepaliveSSE(false, "", openAIKeepaliveCreatedData) +
		openAIKeepaliveSSE(false, "", openAIKeepaliveStatusEvents[0].data) +
		openAIKeepaliveSSE(false, "", openAIKeepaliveFailedData)

	_, writes, err := runOpenAIKeepaliveBridgeTurn(t, io.NopCloser(strings.NewReader(stream)), 2)

	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "only the first turn stages and fails over")
	require.NotEmpty(t, writes)
}
