package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 上游（CLIProxyAPI 等）只要设了 thinkingConfig 就会回 thought:true 的 part。
// 以前转换一律当 text，思考摘要和正文被拼进同一个 content；
// 现在必须分成 thinking / text 两个块，Chat Completions 侧落成 reasoning_content。

func geminiThoughtResp() (map[string]any, []byte) {
	resp := map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"role": "model", "parts": []any{
				map[string]any{"text": "**Thinking it through**\n\nOkay, 17*23...", "thought": true},
				map[string]any{"text": "The answer is **391**."},
			}},
			"finishReason": "STOP",
		}},
		"usageMetadata": map[string]any{"promptTokenCount": 19, "candidatesTokenCount": 10, "thoughtsTokenCount": 30, "totalTokenCount": 59},
	}
	raw, _ := json.Marshal(resp)
	return resp, raw
}

func TestConvertGeminiToClaudeMessage_ThoughtPartBecomesThinkingBlock(t *testing.T) {
	resp, raw := geminiThoughtResp()
	claude, _ := convertGeminiToClaudeMessage(resp, "gemini-3.7-flash", raw, false)
	blocks := claude["content"].([]any)
	require.Len(t, blocks, 2)
	require.Equal(t, "thinking", blocks[0].(map[string]any)["type"])
	require.Equal(t, "**Thinking it through**\n\nOkay, 17*23...", blocks[0].(map[string]any)["thinking"])
	require.Equal(t, "text", blocks[1].(map[string]any)["type"])
	require.Equal(t, "The answer is **391**.", blocks[1].(map[string]any)["text"])
}

func TestGeminiResponseToChatCompletions_ThoughtGoesToReasoningContent(t *testing.T) {
	resp, raw := geminiThoughtResp()
	got, _, err := geminiResponseToChatCompletions(resp, "gemini-3.7-flash", raw, nil)
	require.NoError(t, err)
	require.Len(t, got.Choices, 1)
	msg := got.Choices[0].Message
	var content string
	require.NoError(t, json.Unmarshal(msg.Content, &content))
	require.Equal(t, "The answer is **391**.", content, "正文里不得再混入思考")
	require.Equal(t, "**Thinking it through**\n\nOkay, 17*23...", msg.ReasoningContent)
}

// 没有 thought 标记的普通响应行为不变：没有 reasoning_content，content 原样。
func TestGeminiResponseToChatCompletions_NoThoughtUnchanged(t *testing.T) {
	resp := map[string]any{"candidates": []any{map[string]any{
		"content":      map[string]any{"parts": []any{map[string]any{"text": "plain"}}},
		"finishReason": "STOP",
	}}}
	raw, _ := json.Marshal(resp)
	got, _, err := geminiResponseToChatCompletions(resp, "m", raw, nil)
	require.NoError(t, err)
	var content string
	require.NoError(t, json.Unmarshal(got.Choices[0].Message.Content, &content))
	require.Equal(t, "plain", content)
	require.Empty(t, got.Choices[0].Message.ReasoningContent)
}

func geminiThoughtSSE() string {
	return strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"text":"**Thinking**\n\nOkay, ","thought":true}],"role":"model"}}],"modelVersion":"gemini-3.7-flash"}`,
		"",
		`data: {"candidates":[{"content":{"parts":[{"text":"17*23 is easy.","thought":true}],"role":"model"}}],"modelVersion":"gemini-3.7-flash"}`,
		"",
		`data: {"candidates":[{"content":{"parts":[{"text":"The answer "}],"role":"model"}}],"modelVersion":"gemini-3.7-flash"}`,
		"",
		`data: {"candidates":[{"content":{"parts":[{"text":"is **391**."}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":19,"candidatesTokenCount":10,"thoughtsTokenCount":30,"totalTokenCount":59},"modelVersion":"gemini-3.7-flash"}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
}

func TestHandleChatCompletionsStreamingResponseFromGemini_ThoughtDeltasGoToReasoningContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(geminiThoughtSSE())),
	}
	svc := &GeminiMessagesCompatService{}
	_, err := svc.handleChatCompletionsStreamingResponseFromGemini(c, resp, time.Now(), "gemini-3.7-flash", false, false)
	require.NoError(t, err)

	var reasoning, content string
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data:") || strings.Contains(line, "[DONE]") {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          *string `json:"content"`
					ReasoningContent *string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &chunk), line)
		for _, ch := range chunk.Choices {
			if ch.Delta.ReasoningContent != nil {
				reasoning += *ch.Delta.ReasoningContent
			}
			if ch.Delta.Content != nil {
				content += *ch.Delta.Content
			}
		}
	}
	require.Equal(t, "**Thinking**\n\nOkay, 17*23 is easy.", reasoning)
	require.Equal(t, "The answer is **391**.", content)
}

// Claude 协议流式：thought part 落成 thinking 块 + thinking_delta，正文仍是 text 块。
func TestHandleStreamingResponse_ThoughtPartsBecomeThinkingBlocks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(geminiThoughtSSE())),
	}
	svc := &GeminiMessagesCompatService{}
	_, err := svc.handleStreamingResponse(c, resp, time.Now(), "gemini-3.7-flash")
	require.NoError(t, err)
	out := w.Body.String()

	var thinking, text string
	var starts []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var evt struct {
			Type         string `json:"type"`
			ContentBlock *struct {
				Type string `json:"type"`
			} `json:"content_block"`
			Delta *struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
			} `json:"delta"`
		}
		require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &evt), line)
		switch evt.Type {
		case "content_block_start":
			starts = append(starts, evt.ContentBlock.Type)
		case "content_block_delta":
			switch evt.Delta.Type {
			case "thinking_delta":
				thinking += evt.Delta.Thinking
			case "text_delta":
				text += evt.Delta.Text
			}
		}
	}
	require.Equal(t, []string{"thinking", "text"}, starts)
	require.Equal(t, "**Thinking**\n\nOkay, 17*23 is easy.", thinking)
	require.Equal(t, "The answer is **391**.", text)
}
