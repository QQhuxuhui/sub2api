//go:build unit

package service

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
)

const compatThinkingUpstreamSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":3}}}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// compatThinkingOutboundBody sends one OpenAI-format request through an
// Anthropic-protocol upstream path and returns the body that left the gateway.
func compatThinkingOutboundBody(t *testing.T, path, model, effort string) []byte {
	t.Helper()
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(compatThinkingUpstreamSSE)),
	}}
	chat := strings.HasPrefix(path, "chat")
	var body string
	endpoint := "/v1/responses"
	if chat {
		endpoint = "/v1/chat/completions"
		body = `{"model":"` + model + `","stream":true,"messages":[{"role":"user","content":"hi"}]`
		if effort != "" {
			body += `,"reasoning_effort":"` + effort + `"`
		}
	} else {
		body = `{"model":"` + model + `","stream":true,"input":"hi"`
		if effort != "" {
			body += `,"reasoning":{"effort":"` + effort + `"}`
		}
	}
	body += "}"
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))

	var err error
	switch path {
	case "chat/anthropic":
		svc := &GatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
		account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-test"}}
		_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, []byte(body), nil)
	case "responses/anthropic":
		svc := &GatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
		account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-test"}}
		_, err = svc.ForwardAsResponses(context.Background(), c, account, []byte(body), nil)
	case "chat/native":
		svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
		_, err = svc.forwardChatCompletionsViaNativeAnthropic(context.Background(), c, nativeAnthropicTestAccount(), []byte(body), "")
	case "responses/native":
		svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
		_, err = svc.forwardResponsesViaNativeAnthropic(context.Background(), c, nativeAnthropicTestAccount(), []byte(body), "")
	default:
		t.Fatalf("unknown path %q", path)
	}
	require.NoError(t, err)
	require.NotEmpty(t, upstream.lastBody, "the request reached the upstream")
	return upstream.lastBody
}

var compatThinkingPaths = []string{"chat/anthropic", "responses/anthropic", "chat/native", "responses/native"}

// Every Anthropic-protocol upstream path sends adaptive thinking to models
// that reject budget_tokens.
func TestCompatPaths_BudgetRemovedModelsGetAdaptiveThinking(t *testing.T) {
	for _, path := range compatThinkingPaths {
		for _, model := range []string{"claude-fable-5-2", "claude-opus-4-8", "claude-sonnet-5"} {
			t.Run(path+"/"+model, func(t *testing.T) {
				out := compatThinkingOutboundBody(t, path, model, "high")
				require.Equal(t, model, gjson.GetBytes(out, "model").String())
				require.JSONEq(t, `{"type":"adaptive"}`, gjson.GetBytes(out, "thinking").Raw)
				require.Equal(t, "high", gjson.GetBytes(out, "output_config.effort").String())
				require.NotContains(t, string(out), "budget_tokens")

				// Low and absent effort never produced manual thinking: still none.
				require.False(t, gjson.GetBytes(compatThinkingOutboundBody(t, path, model, "low"), "thinking").Exists())
				require.False(t, gjson.GetBytes(compatThinkingOutboundBody(t, path, model, ""), "thinking").Exists())
			})
		}
	}
}

// Models that still take budget_tokens keep the request they had.
func TestCompatPaths_OlderModelsKeepThinkingBudget(t *testing.T) {
	for _, path := range compatThinkingPaths {
		for _, model := range []string{"claude-opus-4-6", "claude-sonnet-4-5-20250929", "kimi-k3.1-pro"} {
			t.Run(path+"/"+model, func(t *testing.T) {
				out := compatThinkingOutboundBody(t, path, model, "high")
				require.JSONEq(t, `{"type":"enabled","budget_tokens":10240}`, gjson.GetBytes(out, "thinking").Raw)
				require.Equal(t, "high", gjson.GetBytes(out, "output_config.effort").String())
			})
		}
	}
}

func TestCompatPaths_Opus55StillUsesItsOwnAdaptiveBranch(t *testing.T) {
	for _, path := range compatThinkingPaths {
		out := compatThinkingOutboundBody(t, path, "claude-opus-5-5", "high")
		require.JSONEq(t, `{"type":"adaptive"}`, gjson.GetBytes(out, "thinking").Raw, path)
		require.Equal(t, "high", gjson.GetBytes(out, "output_config.effort").String(), path)
	}
}
