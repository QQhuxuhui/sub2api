//go:build unit

package service

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: ")
		b.WriteString(e)
		b.WriteString("\n\n")
	}
	return b.String()
}

func TestReadIQShowcaseStream(t *testing.T) {
	cases := []struct {
		name, protocol, body, want string
		fail                       bool
	}{
		{"responses deltas", IQShowcaseProtocolOpenAIResponses, sse(
			`{"type":"response.output_text.delta","delta":"<svg"}`,
			`{"type":"response.output_text.delta","delta":"/>"}`,
			`{"type":"response.completed","response":{}}`), "<svg/>", false},
		{"responses final text only", IQShowcaseProtocolOpenAIResponses, sse(
			`{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]}}`), "hi", false},
		{"responses failed", IQShowcaseProtocolOpenAIResponses, sse(
			`{"type":"response.output_text.delta","delta":"x"}`, `{"type":"response.failed"}`), "", true},
		{"responses error after completed", IQShowcaseProtocolOpenAIResponses, sse(
			`{"type":"response.output_text.delta","delta":"x"}`, `{"type":"response.completed"}`, `{"type":"error"}`), "", true},
		{"responses no end", IQShowcaseProtocolOpenAIResponses, sse(`{"type":"response.output_text.delta","delta":"x"}`), "", true},

		{"chat done", IQShowcaseProtocolOpenAIChat, sse(
			`{"choices":[{"delta":{"content":"a"}}]}`, `{"choices":[{"delta":{"content":"b"},"finish_reason":"stop"}]}`, `[DONE]`), "ab", false},
		{"chat stop then EOF without DONE", IQShowcaseProtocolOpenAIChat, sse(
			`{"choices":[{"delta":{"content":"a"},"finish_reason":null}]}`, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`), "a", false},
		{"chat DONE only", IQShowcaseProtocolOpenAIChat, sse(`{"choices":[{"delta":{"content":"a"}}]}`, `[DONE]`), "a", false},
		{"chat length", IQShowcaseProtocolOpenAIChat, sse(
			`{"choices":[{"delta":{"content":"a"},"finish_reason":"length"}]}`, `[DONE]`), "", true},
		{"chat content filter", IQShowcaseProtocolOpenAIChat, sse(
			`{"choices":[{"delta":{},"finish_reason":"content_filter"}]}`), "", true},
		{"chat cut mid-stream", IQShowcaseProtocolOpenAIChat, sse(`{"choices":[{"delta":{"content":"a"}}]}`), "", true},
		{"chat error frame", IQShowcaseProtocolOpenAIChat, sse(`{"choices":[{"delta":{"content":"a"}}]}`, `{"error":{"message":"boom"}}`, `[DONE]`), "", true},

		{"anthropic ok", IQShowcaseProtocolAnthropicMessages, "event: content_block_delta\n" + sse(
			`{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hmm"}}`,
			`{"type":"content_block_delta","delta":{"type":"text_delta","text":"x"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
			`{"type":"message_stop"}`), "x", false},
		{"anthropic max tokens", IQShowcaseProtocolAnthropicMessages, sse(
			`{"type":"content_block_delta","delta":{"type":"text_delta","text":"x"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`, `{"type":"message_stop"}`), "", true},
		{"anthropic error", IQShowcaseProtocolAnthropicMessages, sse(`{"type":"error","error":{"type":"overloaded_error"}}`), "", true},
		{"anthropic no stop", IQShowcaseProtocolAnthropicMessages, sse(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"x"}}`), "", true},

		{"gemini stop", IQShowcaseProtocolGemini, sse(
			`{"candidates":[{"content":{"parts":[{"text":"plan","thought":true},{"text":"a"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"b"}]},"finishReason":"STOP"}]}`), "ab", false},
		{"gemini no finish", IQShowcaseProtocolGemini, sse(`{"candidates":[{"content":{"parts":[{"text":"a"}]}}]}`), "", true},
		{"gemini safety", IQShowcaseProtocolGemini, sse(`{"candidates":[{"content":{"parts":[{"text":"a"}]},"finishReason":"SAFETY"}]}`), "", true},
		{"gemini blocked", IQShowcaseProtocolGemini, sse(`{"promptFeedback":{"blockReason":"OTHER"}}`), "", true},
		{"gemini error after text", IQShowcaseProtocolGemini, sse(
			`{"candidates":[{"content":{"parts":[{"text":"a"}]},"finishReason":"STOP"}]}`, `{"error":{"code":500}}`), "", true},

		{"non-json event", IQShowcaseProtocolOpenAIChat, sse(`oops`), "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := readIQShowcaseStream(c.protocol, strings.NewReader(c.body))
			if c.fail {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, c.want, got)
		})
	}
}

func TestReadIQShowcaseStream_CapsAnswer(t *testing.T) {
	chunk := strings.Repeat("a", 64<<10)
	r, w := io.Pipe()
	go func() {
		for i := 0; i < 20; i++ {
			if _, err := io.WriteString(w, sse(`{"choices":[{"delta":{"content":"`+chunk+`"}}]}`)); err != nil {
				return
			}
		}
		_ = w.Close()
	}()
	_, err := readIQShowcaseStream(IQShowcaseProtocolOpenAIChat, r)
	require.ErrorContains(t, err, "larger than")
	_ = r.Close()
}

func TestBuildIQShowcaseRequest(t *testing.T) {
	item := &IQShowcaseItem{Model: "m/1", Prompt: "draw", ReasoningEffort: "high", MaxOutputTokens: 900}
	cases := map[string]struct {
		path, authHeader, authValue string
		check                       func(t *testing.T, body string)
	}{
		IQShowcaseProtocolOpenAIResponses: {"/v1/responses", "Authorization", "Bearer sk-k", func(t *testing.T, b string) {
			require.Equal(t, "draw", gjson.Get(b, "input.0.content.0.text").String())
			require.Equal(t, "high", gjson.Get(b, "reasoning.effort").String())
			require.EqualValues(t, 900, gjson.Get(b, "max_output_tokens").Int())
			require.True(t, gjson.Get(b, "stream").Bool())
		}},
		IQShowcaseProtocolOpenAIChat: {"/v1/chat/completions", "Authorization", "Bearer sk-k", func(t *testing.T, b string) {
			require.Equal(t, "draw", gjson.Get(b, "messages.0.content").String())
			require.Equal(t, "high", gjson.Get(b, "reasoning_effort").String())
			require.EqualValues(t, 900, gjson.Get(b, "max_tokens").Int())
		}},
		IQShowcaseProtocolAnthropicMessages: {"/v1/messages", "x-api-key", "sk-k", func(t *testing.T, b string) {
			require.EqualValues(t, 900, gjson.Get(b, "max_tokens").Int())
			require.False(t, gjson.Get(b, "reasoning").Exists())
		}},
		IQShowcaseProtocolGemini: {"/v1beta/models/m%2F1:streamGenerateContent", "x-goog-api-key", "sk-k", func(t *testing.T, b string) {
			require.Equal(t, "draw", gjson.Get(b, "contents.0.parts.0.text").String())
			require.EqualValues(t, 900, gjson.Get(b, "generationConfig.maxOutputTokens").Int())
		}},
	}
	for protocol, c := range cases {
		t.Run(protocol, func(t *testing.T) {
			it := *item
			it.Protocol = protocol
			req, err := BuildIQShowcaseRequest(context.Background(), "http://127.0.0.1:9/", "sk-k", &it)
			require.NoError(t, err)
			require.Equal(t, "POST", req.Method)
			require.Equal(t, c.path, req.URL.EscapedPath())
			require.Equal(t, "application/json", req.Header.Get("Content-Type"))
			require.Equal(t, c.authValue, req.Header.Get(c.authHeader))
			require.Empty(t, req.Header.Get(IntentRouteInternalHeader), "must look like a client request")
			body, _ := io.ReadAll(req.Body)
			if protocol != IQShowcaseProtocolGemini {
				require.Equal(t, "m/1", gjson.GetBytes(body, "model").String())
			}
			c.check(t, string(body))
		})
	}

	anth := &IQShowcaseItem{Protocol: IQShowcaseProtocolAnthropicMessages, Model: "c", Prompt: "p"}
	req, err := BuildIQShowcaseRequest(context.Background(), "http://x", "k", anth)
	require.NoError(t, err)
	body, _ := io.ReadAll(req.Body)
	require.EqualValues(t, iqShowcaseAnthropicDefaultMaxTokens, gjson.GetBytes(body, "max_tokens").Int(), "anthropic requires max_tokens")

	_, err = BuildIQShowcaseRequest(context.Background(), "http://x", "k", &IQShowcaseItem{Protocol: "nope"})
	require.Error(t, err)
}
