package service

import (
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
)

// cacheWriteResponseRewrite applies only the account's usage policy (cache-write
// strip + cache simulation). Protocol bridges already restore model names while
// building their client responses.
func cacheWriteResponseRewrite(c *gin.Context, account *Account) openAIResponseModelRewrite {
	return openAIResponseModelRewrite{
		stripCacheWrite: account.StripsOpenAICacheCreation(),
		cacheSim:        openAICacheSimForRequest(c, account),
	}
}

// anthropicUsageResponseRewrite 与 cacheWriteResponseRewrite 相同，但标记输出载荷里的
// usage 是 Anthropic 口径（/v1/messages 转换路径）。
func anthropicUsageResponseRewrite(c *gin.Context, account *Account) openAIResponseModelRewrite {
	r := cacheWriteResponseRewrite(c, account)
	r.anthropicShape = true
	return r
}

// Copy before normalizing: client protocol conversion must not change the
// upstream usage snapshot retained for accounting. Anthropic conversion must
// see zero creation tokens before splitting the total input into exclusive bins.
func (r openAIResponseModelRewrite) applyResponsesUsage(usage *apicompat.ResponsesUsage) *apicompat.ResponsesUsage {
	// Callers pass a complete response's usage.
	return r.applyResponsesUsageFinal(usage, true)
}

// terminal reports the usage is the response's final one (see
// openAICacheSimRequest.applyResponsesUsage).
func (r openAIResponseModelRewrite) applyResponsesUsageFinal(usage *apicompat.ResponsesUsage, terminal bool) *apicompat.ResponsesUsage {
	if usage == nil || (!r.stripCacheWrite && r.cacheSim == nil) {
		return usage
	}
	out := usage
	if r.stripCacheWrite {
		result := *usage
		result.CacheCreationInputTokens = 0
		if usage.InputTokensDetails != nil {
			details := *usage.InputTokensDetails
			details.CacheCreationTokens = 0
			details.CacheWriteTokens = 0
			result.InputTokensDetails = &details
		}
		out = &result
	}
	if r.cacheSim != nil {
		out = r.cacheSim.applyResponsesUsage(out, terminal)
	}
	return out
}

func (r openAIResponseModelRewrite) applyResponsesEventUsage(event *apicompat.ResponsesStreamEvent) {
	if event == nil || (!r.stripCacheWrite && r.cacheSim == nil) {
		return
	}
	terminal := openAIResponsesTerminalEventType(event.Type)
	// When the event also carries the response's usage, that one is what
	// conversions output: only it may record the smoothing baseline.
	topFinal := terminal && (event.Response == nil || event.Response.Usage == nil)
	event.Usage = r.applyResponsesUsageFinal(event.Usage, topFinal)
	if event.Response != nil {
		response := *event.Response
		response.Usage = r.applyResponsesUsageFinal(response.Usage, terminal)
		event.Response = &response
	}
}

func (r openAIResponseModelRewrite) applyChatUsage(usage *apicompat.ChatUsage) *apicompat.ChatUsage {
	if usage == nil || (!r.stripCacheWrite && r.cacheSim == nil) {
		return usage
	}
	// Without details there is nothing to strip, and only cache smoothing
	// may still fill them in.
	if usage.PromptTokensDetails == nil && (r.cacheSim == nil || r.cacheSim.smooth == nil) {
		return usage
	}
	out := usage
	if r.stripCacheWrite && usage.PromptTokensDetails != nil {
		result := *usage
		details := *usage.PromptTokensDetails
		details.CacheCreationTokens = 0
		details.CacheWriteTokens = 0
		result.PromptTokensDetails = &details
		out = &result
	}
	if r.cacheSim != nil {
		out = r.cacheSim.applyChatUsage(out)
	}
	return out
}

// applyUsageJSON handles image tool usage before it is embedded in a potentially
// large image response, avoiding another parse/copy of the base64 image data.
func (r openAIResponseModelRewrite) applyUsageJSON(usage []byte) []byte {
	if !r.stripCacheWrite {
		return usage
	}
	return stripOpenAICacheWritePaths(usage, openAICacheWriteUsageFields)
}

// Anthropic's typed response always serializes cache_creation_input_tokens,
// including zero. Remove that field at the wire boundary as well.
func (r openAIResponseModelRewrite) writeJSON(c *gin.Context, status int, value any) error {
	if !r.stripCacheWrite && r.cacheSim == nil {
		c.JSON(status, value)
		return nil
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.Data(status, "application/json; charset=utf-8", r.applyJSON(body))
	return nil
}

// openAIResponsesTerminalEventType reports a Responses stream event that
// carries the response's final usage.
func openAIResponsesTerminalEventType(eventType string) bool {
	switch eventType {
	case "response.completed", "response.incomplete", "response.failed", "response.done":
		return true
	}
	return false
}
