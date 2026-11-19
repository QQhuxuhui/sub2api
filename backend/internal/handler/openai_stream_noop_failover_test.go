//go:build unit

package handler

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Account A sends the fake first token seen on group 4 on 2026-11-19, then a
// failure only the relay-transient rule switches. Account B must take over and
// nothing of A may reach the client.
func TestOpenAIFailoverPolicy_FakeFirstTokenThenRuleFailureSwitchesAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const marker = "fake-first-token-from-a"
	accountA := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_a\",\"status\":\"in_progress\"}}\n\n" +
				"event: response.audio.done\ndata: {\"sequence_number\":1,\"type\":\"response.audio.done\",\"item_id\":\"" + marker + "\"}\n\n" +
				"event: codex.rate_limits\ndata: {\"type\":\"codex.rate_limits\",\"plan_type\":\"" + marker + "\"}\n\n" +
				"event: response.reasoning_summary_part.done\ndata: {\"type\":\"response.reasoning_summary_part.done\",\"item_id\":\"" + marker + "\",\"part\":{\"type\":\"summary_text\",\"text\":\"\\u200b\"}}\n\n" +
				"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"" + marker + "\",\"summary\":[],\"content\":[],\"encrypted_content\":\"gAAAAAB\"}}\n\n" +
				"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_a\",\"status\":\"failed\",\"error\":{\"type\":\"invalid_request_error\",\"code\":\"upstream_error\",\"message\":\"Upstream service temporarily unavailable\"}}}\n\n")),
	}
	upstream := newAstraProCapturedUpstream(accountA, preludeUpstreamStream())
	h := newOpenAIResponsesFailoverTestHandler(t, upstream)
	enableFailoverPolicy(t, h, []int64{failoverTestGroupID}, func(cfg *service.OpenAIFailoverConfig) {
		cfg.Rules = append(cfg.Rules, service.OpenAIFailoverRule{
			ID: "relay-transient", Name: "中转临时故障", Enabled: true,
			Keywords: []string{"temporarily unavailable"}, Action: service.OpenAIFailoverActionFailover,
		})
	})
	c, rec := newAstraProFailoverContext(t, preludeStreamRequestBody)

	h.Responses(c)

	_, accountIDs, _ := upstream.snapshot()
	require.Len(t, accountIDs, 2)
	require.NotEqual(t, accountIDs[0], accountIDs[1], "the second attempt goes to another account")
	body := rec.Body.String()
	require.Contains(t, body, `"delta":"ok"`)
	require.Contains(t, body, "event: response.completed")
	require.NotContains(t, body, marker, "nothing staged from account A reaches the client")
	require.NotContains(t, body, "temporarily unavailable")
}
