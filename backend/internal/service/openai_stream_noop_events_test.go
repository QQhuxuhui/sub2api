//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The frames that committed group 4 streams right before a failure on
// 2026-11-19, as logged by openai.stream_failed_after_commit.
var openAINoopCapturedFrames = []string{
	`{"sequence_number":2,"type":"response.audio.done"}`,
	`{"type":"codex.rate_limits","plan_type":"pro","rate_limits":{"allowed":true,"limit_reached":false,"primary":{"used_percent":95,"window_minutes":10080,"reset_after_seconds":599377,"reset_at":1795689325},"secondary":null},"code_review_rate_limits":null,"additional_rate_limits":null}`,
	`{"type":"codex.client.keepalive"}`,
	`{"type":"proxy.upstream_error","req_id":"cd4b74d1d9206f81f9b6e11af45cacec","status":502,"content_type":"application/json; charset=utf-8","message":"upstream response arrived after synthetic frame; original HTTP status is already committed","upstream_body":"{\"error\":{\"message\":\"Upstream service temporarily unavailable\"}}"}`,
	`{"type":"sub2api.gateway_ready","source":"gateway","model_output":false}`,
	`{"type":"response.reasoning_summary_part.done","sequence_number":4,"item_id":"rs_cb90","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":"​"}}`,
	`{"type":"response.reasoning_summary_part.added","sequence_number":3,"item_id":"rs_cb90","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`,
	`{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"type":"reasoning","id":"rs_0f98","summary":[],"content":[],"encrypted_content":"gAAAAABq_txcpRgVaIAO8Hgy"}}`,
	`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_0f98","encrypted_content":"gAAAAABq_txc"}}`,
}

func TestOpenAIStreamNoopFramesAreNotOutput(t *testing.T) {
	for _, data := range openAINoopCapturedFrames {
		for _, eventType := range []string{gjson.Get(data, "type").String(), ""} {
			require.True(t, openAIStreamEventCarriesNoOutput(data, gjson.Get(data, "type").String()), data)
			require.True(t, openAIStreamEventIsPlaceholder(data, eventType), data)
			require.False(t, openAIStreamDataStartsClientOutput(data, eventType), data)
			require.False(t, openAIStreamDataStartsVisibleOutput(data, eventType), data)
			require.False(t, openAIStreamDataStartsSemanticTTFT(data, eventType), data)
		}
	}
}

func TestOpenAIStreamNoopKeepsRealOutput(t *testing.T) {
	for _, tc := range []struct{ name, eventType, data string }{
		{"summary with text", "response.reasoning_summary_part.done", `{"part":{"type":"summary_text","text":"thinking"}}`},
		{"zero-width summary with transcript", "response.reasoning_summary_part.done", `{"part":{"type":"summary_text","text":"​","transcript":"spoken"}}`},
		{"reasoning with summary text", "response.output_item.added", `{"item":{"type":"reasoning","encrypted_content":"c","summary":[{"type":"summary_text","text":"t"}]}}`},
		{"reasoning with empty summary part", "response.output_item.added", `{"item":{"type":"reasoning","encrypted_content":"c","summary":[{"type":"summary_text","text":""}]}}`},
		{"reasoning with transcript", "response.output_item.added", `{"item":{"type":"reasoning","encrypted_content":"c","content":[{"type":"reasoning_text","text":"","transcript":"hi"}]}}`},
		{"reasoning with refusal", "response.output_item.added", `{"item":{"type":"reasoning","encrypted_content":"c","content":[{"type":"refusal","refusal":"已拒绝"}]}}`},
		{"reasoning with result", "response.output_item.added", `{"item":{"type":"reasoning","encrypted_content":"c","result":"r"}}`},
		{"ciphertext reasoning done", "response.output_item.done", `{"item":{"type":"reasoning","encrypted_content":"ciphertext"}}`},
		{"unlisted extension event", "vendor.note", `{"type":"vendor.note"}`},
		{"unknown event", "unknown_event", `{"type":"unknown_event"}`},
		{"text delta", "response.output_text.delta", `{"type":"response.output_text.delta","delta":"hi"}`},
		{"listed event name with [DONE]", "codex.client.keepalive", `[DONE]`},
		{"listed event name with plain text", "codex.rate_limits", `hello`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.False(t, openAIStreamEventCarriesNoOutput(tc.data, tc.eventType))
			require.True(t, openAIStreamDataStartsClientOutput(tc.data, tc.eventType))
		})
	}
	for _, tc := range []struct{ eventType, data string }{
		{"error", `{"type":"error","error":{"code":"server_is_overloaded","message":"overloaded"}}`},
		{"response.failed", `{"type":"response.failed","response":{"error":{"code":"server_error"}}}`},
		{"response.completed", `{"type":"response.completed","response":{"output":[]}}`},
	} {
		require.False(t, openAIStreamEventCarriesNoOutput(tc.data, tc.eventType), tc.eventType)
	}
}

const (
	openAINoopFakeFirstTokenFrames = "event: response.created\n" + `data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_n","status":"in_progress"}}` + "\n\n" +
		"event: response.audio.done\n" + `data: {"sequence_number":1,"type":"response.audio.done"}` + "\n\n" +
		"event: codex.rate_limits\n" + `data: {"type":"codex.rate_limits","plan_type":"pro","rate_limits":{"allowed":true}}` + "\n\n" +
		"event: response.reasoning_summary_part.done\n" + `data: {"type":"response.reasoning_summary_part.done","sequence_number":3,"part":{"type":"summary_text","text":"​"}}` + "\n\n" +
		"event: response.output_item.added\n" + `data: {"type":"response.output_item.added","sequence_number":4,"output_index":0,"item":{"type":"reasoning","id":"rs_n","summary":[],"content":[],"encrypted_content":"gAAAAAB"}}` + "\n\n"
	// The default judgement fails this over (no non-retryable marker).
	openAINoopUnavailableFailed = "event: response.failed\n" +
		`data: {"type":"response.failed","sequence_number":5,"response":{"id":"resp_n","status":"failed","error":{"code":"upstream_error","message":"Upstream service temporarily unavailable"}}}` + "\n\n"
	// Only a relay-transient rule fails this over: invalid_request blocks the
	// default judgement.
	openAINoopRuleOnlyFailedPayload = `{"type":"response.failed","sequence_number":5,"response":{"id":"resp_n","status":"failed","error":{"type":"invalid_request_error","code":"upstream_error","message":"Upstream service temporarily unavailable"}}}`
	openAINoopRuleOnlyFailed        = "event: response.failed\ndata: " + openAINoopRuleOnlyFailedPayload + "\n\n"
)

func openAINoopForwarders() map[string]func(*OpenAIGatewayService, *http.Response, *gin.Context) error {
	account := &Account{ID: 286, Platform: PlatformOpenAI, Name: "relay"}
	return map[string]func(*OpenAIGatewayService, *http.Response, *gin.Context) error{
		"native": func(svc *OpenAIGatewayService, resp *http.Response, c *gin.Context) error {
			_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
			return err
		},
		"passthrough": func(svc *OpenAIGatewayService, resp *http.Response, c *gin.Context) error {
			_, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
			return err
		},
	}
}

func TestOpenAIStreamingFakeFirstTokenThenFailureFailsOver(t *testing.T) {
	for name, forward := range openAINoopForwarders() {
		t.Run(name, func(t *testing.T) {
			svc, c, rec, resp := newPlaceholderStreamTest(t, openAINoopFakeFirstTokenFrames+openAINoopUnavailableFailed)

			err := forward(svc, resp, c)

			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Empty(t, rec.Body.String(), "the fake first token goes with the failed attempt")
		})
	}
}

func TestOpenAIStreamingFakeFirstTokenThenRuleOnlyFailure(t *testing.T) {
	payload := []byte(openAINoopRuleOnlyFailedPayload)
	require.False(t, openAIStreamFailedEventShouldFailover(payload, extractOpenAISSEErrorMessage(payload)), "only the rule can switch this failure")

	relayTransient := OpenAIFailoverRule{ID: "relay-transient", Name: "中转临时故障", Enabled: true, Keywords: []string{"temporarily unavailable"}, Action: OpenAIFailoverActionFailover}
	for name, forward := range openAINoopForwarders() {
		t.Run(name+"/rule on", func(t *testing.T) {
			svc, c, rec, resp := newPlaceholderStreamTest(t, openAINoopFakeFirstTokenFrames+openAINoopRuleOnlyFailed)
			c.Request = c.Request.WithContext(WithOpenAIFailoverPolicy(context.Background(), enabledFailoverPolicy(relayTransient)))

			err := forward(svc, resp, c)

			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, "relay-transient", failoverErr.FailoverRuleID)
			require.Empty(t, rec.Body.String())
		})
		t.Run(name+"/rule off", func(t *testing.T) {
			svc, c, rec, resp := newPlaceholderStreamTest(t, openAINoopFakeFirstTokenFrames+openAINoopRuleOnlyFailed)

			err := forward(svc, resp, c)

			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr), "without the rule the failure is not switched")
			require.Contains(t, rec.Body.String(), "temporarily unavailable")
		})
	}
}

func TestOpenAIStreamingFakeFirstTokenIsForwardedWithTheRealOutput(t *testing.T) {
	const answer = "event: response.output_text.delta\n" + `data: {"type":"response.output_text.delta","sequence_number":5,"delta":"hi"}` + "\n\n" +
		"event: response.completed\n" + `data: {"type":"response.completed","sequence_number":6,"response":{"id":"resp_n","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"
	for name, forward := range openAINoopForwarders() {
		t.Run(name, func(t *testing.T) {
			svc, c, rec, resp := newPlaceholderStreamTest(t, openAINoopFakeFirstTokenFrames+answer)

			err := forward(svc, resp, c)

			require.NoError(t, err)
			body := rec.Body.String()
			last := -1
			for _, marker := range []string{"response.created", "response.audio.done", "codex.rate_limits", "response.reasoning_summary_part.done", `"rs_n"`, `"delta":"hi"`, "response.completed"} {
				at := strings.Index(body, marker)
				require.Greater(t, at, last, "%s keeps its place", marker)
				last = at
			}
		})
	}
}
