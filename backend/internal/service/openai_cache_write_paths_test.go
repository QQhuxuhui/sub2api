//go:build unit

package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCacheStripRegressionOAuthImages(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "json"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			c, rec, rw := newImagesRewriteTestContext(t)
			raw := strings.Replace(swappedImagesResponsesSSE(), `"output":[`, `"tool_usage":{"image_gen":{"input_tokens":100,"cache_creation_input_tokens":20,"output_tokens":1}},"output":[`, 1)
			resp := newRewriteTestUpstreamResponse("text/event-stream", raw)
			require.True(t, rw.stripCacheWrite)
			if stream {
				_, _, _, _, err := newRewriteTestService().handleOpenAIImagesOAuthStreamingResponse(resp, c, time.Now(), "b64_json", "image_generation", rewriteTestImageUpstreamModel, rw)
				require.NoError(t, err)
			} else {
				_, _, _, err := newRewriteTestService().handleOpenAIImagesOAuthNonStreamingResponse(resp, c, "b64_json", rewriteTestImageUpstreamModel, rw)
				require.NoError(t, err)
			}
			require.NotContains(t, rec.Body.String(), "cache_creation_input_tokens")
		})
	}
}

func TestCacheStripRegressionCompat(t *testing.T) {
	for _, endpoint := range []string{"messages", "chat"} {
		for _, stream := range []bool{false, true} {
			for _, strip := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/strip=%t", endpoint, stream, strip), func(t *testing.T) {
					c, rec := newRewriteTestContext(t, "gpt-5.4")
					account := openAIRewriteTestAccount(AccountTypeAPIKey)
					account.Extra = map[string]any{openAIStripCacheCreationKey: strip}
					raw := `data: {"type":"response.completed","response":{"id":"resp_probe","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":10,"cache_write_tokens":20},"cache_creation_input_tokens":20,"output_tokens":1}}}` + "\n\n"
					resp := newRewriteTestUpstreamResponse("text/event-stream", raw)
					svc := newRewriteTestService()
					var result *OpenAIForwardResult
					var err error
					if endpoint == "messages" {
						if stream {
							result, err = svc.handleAnthropicStreamingResponse(resp, c, account, "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now())
						} else {
							result, err = svc.handleAnthropicBufferedStreamingResponse(resp, c, account, "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now())
						}
					} else {
						if stream {
							result, err = svc.handleChatStreamingResponse(resp, c, account, "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now(), 0)
						} else {
							result, err = svc.handleChatBufferedStreamingResponse(resp, c, account, "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now())
						}
					}
					require.NoError(t, err)
					require.Equal(t, 20, result.Usage.CacheCreationInputTokens, "preserve raw upstream usage")
					require.Equal(t, 100, result.Usage.InputTokens)
					assertCacheStripClientUsage(t, rec.Body.String(), stream, endpoint, strip)
				})
			}
		}
	}
}

func TestCacheStripRegressionEmbeddings(t *testing.T) {
	for _, strip := range []bool{false, true} {
		t.Run(fmt.Sprintf("strip=%t", strip), func(t *testing.T) {
			c, rec := newRewriteTestContext(t, "text-embedding-3-small")
			resp := newRewriteTestUpstreamResponse("application/json", `{"object":"list","data":[],"usage":{"prompt_tokens":100,"cache_creation_input_tokens":20}}`)
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: &httpUpstreamRecorder{resp: resp}}
			account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test", "base_url": "https://api.openai.com"}, Extra: map[string]any{openAIStripCacheCreationKey: strip}}
			result, err := svc.ForwardEmbeddings(context.Background(), c, account, []byte(`{"model":"text-embedding-3-small","input":"hi"}`), "")
			require.NoError(t, err)
			require.Equal(t, 20, result.Usage.CacheCreationInputTokens)
			require.Equal(t, int64(100), gjson.Get(rec.Body.String(), "usage.prompt_tokens").Int())
			require.Equal(t, !strip, gjson.Get(rec.Body.String(), "usage.cache_creation_input_tokens").Exists())
		})
	}
}

func TestCacheStripRegressionChatUpstreamBridges(t *testing.T) {
	for _, endpoint := range []string{"messages", "responses"} {
		for _, stream := range []bool{false, true} {
			for _, strip := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/strip=%t", endpoint, stream, strip), func(t *testing.T) {
					c, rec := newRewriteTestContext(t, "gpt-5.4")
					account := rawChatCompletionsTestAccount()
					account.Extra = map[string]any{openAIStripCacheCreationKey: strip}
					usage := `"usage":{"prompt_tokens":100,"completion_tokens":1,"total_tokens":101,"prompt_tokens_details":{"cached_tokens":10,"cache_write_tokens":20}}`
					raw := `{"id":"chat_probe","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` + usage + `}`
					contentType := "application/json"
					if stream {
						contentType = "text/event-stream"
						raw = "data: " + `{"id":"chat_probe","model":"gpt-5.4","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` + usage + `}` + "\n\ndata: [DONE]\n\n"
					}
					svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: &httpUpstreamRecorder{resp: newRewriteTestUpstreamResponse(contentType, raw)}}
					var result *OpenAIForwardResult
					var err error
					if endpoint == "messages" {
						body := []byte(fmt.Sprintf(`{"model":"gpt-5.4","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":%t}`, stream))
						result, err = svc.forwardAnthropicViaRawChatCompletions(context.Background(), c, account, body, "")
					} else {
						body := []byte(fmt.Sprintf(`{"model":"gpt-5.4","input":"hi","stream":%t}`, stream))
						result, err = svc.forwardResponsesViaRawChatCompletions(context.Background(), c, account, body)
					}
					require.NoError(t, err)
					require.Equal(t, 20, result.Usage.CacheCreationInputTokens, "retain upstream usage for accounting")
					assertCacheStripClientUsage(t, rec.Body.String(), stream, endpoint, strip)
				})
			}
		}
	}
}

// Inspect the final wire usage, including stream frames, so missing usage cannot
// make absence-of-cache-field assertions pass vacuously.
func assertCacheStripClientUsage(t *testing.T, body string, stream bool, endpoint string, strip bool) {
	t.Helper()
	var usage gjson.Result
	inspect := func(data []byte) {
		for _, path := range []string{"usage", "response.usage", "message.usage"} {
			if got := gjson.GetBytes(data, path); got.Exists() {
				usage = got
			}
		}
	}
	if stream {
		forEachOpenAISSEDataPayload(body, inspect)
	} else {
		inspect([]byte(body))
	}
	require.True(t, usage.Exists(), body)
	if strip {
		require.NotContains(t, body, "cache_creation")
		require.NotContains(t, body, "cache_write")
	} else {
		creation := usage.Get("cache_creation_input_tokens").Int()
		for _, path := range []string{"prompt_tokens_details.cache_creation_tokens", "prompt_tokens_details.cache_write_tokens"} {
			if value := usage.Get(path).Int(); value > creation {
				creation = value
			}
		}
		require.Equal(t, int64(20), creation, body)
	}
	switch endpoint {
	case "messages":
		want := int64(70)
		if strip {
			want = 90
		}
		require.Equal(t, want, usage.Get("input_tokens").Int())
		require.Equal(t, int64(10), usage.Get("cache_read_input_tokens").Int())
		require.Equal(t, int64(1), usage.Get("output_tokens").Int())
	case "responses":
		require.Equal(t, int64(100), usage.Get("input_tokens").Int())
		require.Equal(t, int64(10), usage.Get("input_tokens_details.cached_tokens").Int())
		require.Equal(t, int64(1), usage.Get("output_tokens").Int())
	case "chat":
		require.Equal(t, int64(100), usage.Get("prompt_tokens").Int())
		require.Equal(t, int64(10), usage.Get("prompt_tokens_details.cached_tokens").Int())
		require.Equal(t, int64(1), usage.Get("completion_tokens").Int())
	}
}
