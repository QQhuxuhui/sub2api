package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/cachesim"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Exclusion must be observed before typed protocol conversion drops modality
// fields, and before any usage object in a payload is rewritten.
func containsCacheSimExclusionHint[T string | []byte](data T) bool {
	s := string(data)
	return strings.Contains(s, "image_tokens") || strings.Contains(s, "audio_tokens") ||
		strings.Contains(s, "image_generation_call") || strings.Contains(s, "tool_usage")
}

func observeOpenAICacheSimPayload(c *gin.Context, data []byte) {
	if c == nil || c.Request == nil {
		return
	}
	observeOpenAICacheSimState(cachesim.FromContext(c.Request.Context()), data)
}

// A non-streaming client can receive only the final JSON extracted from an
// upstream SSE body. Observe intermediate tool events before discarding them.
func observeOpenAICacheSimSSEBody(c *gin.Context, body string) {
	if !containsCacheSimExclusionHint(body) {
		return
	}
	forEachOpenAISSEFrame(body, func(eventType string, data []byte) {
		observeOpenAICacheSimPayload(c, []byte(openAICompatPayloadWithEventType(string(data), eventType)))
	})
}

func observeOpenAICacheSimState(state *cachesim.State, data []byte) {
	if state == nil || state.Excluded() || !containsCacheSimExclusionHint(data) {
		return
	}
	root := gjson.ParseBytes(data)
	if strings.HasPrefix(root.Get("type").String(), "response.image_generation_call.") {
		state.Exclude()
		return
	}
	for _, prefix := range []string{"", "response.", "message.", "data.", "data.response."} {
		usage := root.Get(prefix + "usage")
		for _, details := range []string{"input_tokens_details", "prompt_tokens_details", "output_tokens_details", "completion_tokens_details"} {
			if usage.Get(details+".image_tokens").Int() > 0 || usage.Get(details+".audio_tokens").Int() > 0 {
				state.Exclude()
				return
			}
		}
		// OpenAI 现在每个 Responses 响应都带全零的 tool_usage.image_gen 占位对象（含
		// image_tokens: 0），只有真的产生了图片 token 才算生图请求。
		if imageGen := root.Get(prefix + "tool_usage.image_gen"); imageGen.IsObject() {
			if imageGen.Get("total_tokens").Int() > 0 || imageGen.Get("input_tokens").Int() > 0 || imageGen.Get("output_tokens").Int() > 0 ||
				imageGen.Get("input_tokens_details.image_tokens").Int() > 0 || imageGen.Get("output_tokens_details.image_tokens").Int() > 0 {
				state.Exclude()
				return
			}
		}
		for _, item := range root.Get(prefix + "output").Array() {
			if openAICacheSimExcludedOutput(item.Get("type").String()) {
				state.Exclude()
				return
			}
		}
	}
	if openAICacheSimExcludedOutput(root.Get("item.type").String()) {
		state.Exclude()
	}
}

// 只排除真正的非文本模态（生图）。带 web_search_call 的 Responses 仍是普通文本对话，
// 提示缓存语义与计费不变；之前把它也排除掉，导致开着联网搜索的客户（如账号 113）
// 94% 的请求既不改写也不进窗口，模拟形同虚设。
func openAICacheSimExcludedOutput(kind string) bool {
	return kind == "image_generation_call"
}
