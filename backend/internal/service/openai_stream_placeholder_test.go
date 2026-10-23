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

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// The filler a sub2api-based relay (tntapi) sends before the model answers,
// as captured on 2026-10-23.
const openAITaggedPlaceholderEvents = "event: response.created\n" +
	`data: {"response":{"id":"resp_ft_1","output":[],"status":"in_progress"},"type":"response.created","sequence_number":0}` + "\n\n" +
	"event: response.output_item.added\n" +
	`data: {"item":{"content":[],"id":"item_1","role":"assistant","status":"in_progress","type":"message"},"output_index":0,"sequence_number":2,"type":"response.output_item.added","sub2api_placeholder":true}` + "\n\n" +
	"event: response.content_part.added\n" +
	`data: {"content_index":0,"item_id":"item_1","output_index":0,"part":{"annotations":[],"text":"","type":"output_text"},"sequence_number":3,"type":"response.content_part.added","sub2api_placeholder":true}` + "\n\n" +
	"event: response.output_text.delta\n" +
	`data: {"content_index":0,"delta":" ","item_id":"item_1","output_index":0,"sequence_number":4,"type":"response.output_text.delta","sub2api_placeholder":true}` + "\n\n" +
	"event: response.output_text.done\n" +
	`data: {"content_index":0,"item_id":"item_1","output_index":0,"sequence_number":5,"text":" ","type":"response.output_text.done","sub2api_placeholder":true}` + "\n\n" +
	"event: response.content_part.done\n" +
	`data: {"content_index":0,"item_id":"item_1","output_index":0,"part":{"annotations":[],"text":" ","type":"output_text"},"sequence_number":6,"type":"response.content_part.done","sub2api_placeholder":true}` + "\n\n" +
	"event: response.output_item.done\n" +
	`data: {"item":{"content":[{"text":" ","type":"output_text"}],"id":"item_1","role":"assistant","status":"completed","type":"message"},"output_index":0,"sequence_number":7,"type":"response.output_item.done","sub2api_placeholder":true}` + "\n\n"

const openAIBusyFailedEvent = "event: response.failed\n" +
	`data: {"response":{"error":{"code":"server_error","message":"The service is busy. Please retry later.","type":"server_error"},"id":"resp_ft_1","output":[{"content":[{"text":" ","type":"output_text"}],"id":"item_1","role":"assistant","status":"completed","type":"message"}],"status":"failed"},"sequence_number":9,"type":"response.failed"}` + "\n\n"

func TestOpenAIStreamEventIsPlaceholder(t *testing.T) {
	for _, tc := range []struct {
		name, eventType, data string
		want                  bool
	}{
		{"tagged event", "response.output_item.added", `{"item":{"type":"message","content":[]},"sub2api_placeholder":true}`, true},
		{"space delta", "response.output_text.delta", `{"delta":" "}`, true},
		{"newline and zero-width delta", "response.output_text.delta", `{"delta":"\n​"}`, true},
		{"blank text done", "response.output_text.done", `{"text":"  "}`, true},
		{"blank content part", "response.content_part.done", `{"part":{"type":"output_text","text":" "}}`, true},
		{"blank message item", "response.output_item.done", `{"item":{"type":"message","content":[{"type":"output_text","text":" "}]}}`, true},
		{"type taken from the payload", "", `{"type":"response.output_text.delta","delta":" "}`, true},
		{"real delta", "response.output_text.delta", `{"delta":"Hi"}`, false},
		{"delta starting with space", "response.output_text.delta", `{"delta":" Hi"}`, false},
		{"real message item", "response.output_item.done", `{"item":{"type":"message","content":[{"type":"output_text","text":"Hi"}]}}`, false},
		{"function call item", "response.output_item.added", `{"item":{"type":"function_call","arguments":""}}`, false},
		{"blank function arguments", "response.function_call_arguments.delta", `{"delta":" "}`, false},
		{"terminal event", "response.completed", `{"response":{"output":[]}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, openAIStreamEventIsPlaceholder(tc.data, tc.eventType))
		})
	}
}

func newPlaceholderStreamTest(t *testing.T, body string) (*OpenAIGatewayService, *gin.Context, *httptest.ResponseRecorder, *http.Response) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"X-Request-Id": []string{"rid-placeholder"}},
	}
	return svc, c, rec, resp
}

func TestOpenAIStreamingPlaceholderThenBusyFailsOver(t *testing.T) {
	svc, c, rec, resp := newPlaceholderStreamTest(t, openAITaggedPlaceholderEvents+openAIBusyFailedEvent)

	_, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 98, Platform: PlatformOpenAI, Name: "tntapi"}, time.Now(), "model", "model")

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr, "a failure after filler alone can still go to another account")
	require.Contains(t, string(failoverErr.ResponseBody), "The service is busy")
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String(), "the filler is dropped with the failed attempt")
}

func TestOpenAIStreamingUntaggedBlankFillerThenBusyFailsOver(t *testing.T) {
	filler := strings.ReplaceAll(openAITaggedPlaceholderEvents, `,"sub2api_placeholder":true`, "")
	filler = strings.ReplaceAll(filler, `" "`, `"\n"`)
	svc, c, rec, resp := newPlaceholderStreamTest(t, filler+openAIBusyFailedEvent)

	_, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 98, Platform: PlatformOpenAI, Name: "relay"}, time.Now(), "model", "model")

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Empty(t, rec.Body.String())
}

func TestOpenAIStreamingPlaceholderIsForwardedWithTheRealOutput(t *testing.T) {
	answer := "event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","item_id":"msg_2","output_index":1,"content_index":0,"delta":"Hi"}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_ft_1","status":"completed","usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}}` + "\n\n"
	svc, c, rec, resp := newPlaceholderStreamTest(t, openAITaggedPlaceholderEvents+answer)

	result, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 98, Platform: PlatformOpenAI, Name: "tntapi"}, time.Now(), "model", "model")

	require.NoError(t, err)
	body := rec.Body.String()
	require.Contains(t, body, `"sub2api_placeholder":true`, "the filler still reaches the client, in order, ahead of the answer")
	require.Less(t, strings.Index(body, `"delta":" "`), strings.Index(body, `"delta":"Hi"`))
	require.NotNil(t, result.firstTokenMs)
}

func TestOpenAIStreamingPassthroughPlaceholderThenBusyFailsOver(t *testing.T) {
	_, recorder, writer, err := runPassthroughFlushTest(t, io.NopCloser(strings.NewReader(openAITaggedPlaceholderEvents+openAIBusyFailedEvent)), -1)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Empty(t, recorder.Body.String())
	require.Empty(t, writer.flushBodyLengths)
}

// Seen on 2026-10-24: ideagates sends response.queued and CPA sends
// response.metadata seconds before failing.
func TestOpenAIStreamingStatusEventsThenFailureFailOver(t *testing.T) {
	for _, status := range []string{
		"event: response.queued\n" + `data: {"type":"response.queued","response":{"id":"resp_q","status":"queued"}}` + "\n\n",
		"event: response.metadata\n" + `data: {"type":"response.metadata","response_id":"resp_m","metadata":{"openai_chatgpt_moderation_metadata":{}}}` + "\n\n",
	} {
		svc, c, rec, resp := newPlaceholderStreamTest(t, status+openAIBusyFailedEvent)

		_, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 115, Platform: PlatformOpenAI, Name: "relay"}, time.Now(), "model", "model")

		var failoverErr *UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr, status)
		require.Empty(t, rec.Body.String())
	}
}
