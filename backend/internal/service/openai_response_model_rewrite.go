package service

import (
	"bytes"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// openAIForcedResponseModelPaths 是强制覆盖时改写的响应 model 字段。
// 只改结构化字段，不碰错误文本或正文里出现的模型名。
var openAIForcedResponseModelPaths = []string{"model", "response.model", "message.model"}

// openAIResponseModelRewrite 描述如何把上游响应里的 model 还原成客户端可见的名字。
//
// OpenAI 平台账号：请求在任一层被改过名（渠道映射、/v1/messages 默认映射、
// 账号 model_mapping、上游模型归一化）时 force=true，响应 model 字段无论上游
// 返回什么都一律覆盖成客户端请求体里原本的名字，避免上游偷换的模型名外泄。
// 上游原值由 upstreamResponseModelObserver 在改写之前记录，管理端日志可见。
//
// 其他平台保持旧语义：仅当字段值恰好等于上游模型名时才替换。
type openAIResponseModelRewrite struct {
	from  string
	to    string
	force bool
}

// openAIClientRequestedModel 返回客户端请求体里原本的模型名（handler 经
// setOpsRequestContext 写入，早于渠道映射和账号映射）。
func openAIClientRequestedModel(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	model, _ := c.Request.Context().Value(ctxkey.Model).(string)
	return strings.TrimSpace(model)
}

// openAIClientVisibleModel 返回响应里应展示给客户端的模型名：OpenAI 平台账号
// 优先取客户端原始请求模型，其余情况沿用 fallback（转发层看到的模型）。
func openAIClientVisibleModel(c *gin.Context, account *Account, fallback string) string {
	if account != nil && account.Platform == PlatformOpenAI {
		if clientModel := openAIClientRequestedModel(c); clientModel != "" {
			return clientModel
		}
	}
	return fallback
}

func newOpenAIResponseModelRewrite(c *gin.Context, account *Account, originalModel, upstreamModel string) openAIResponseModelRewrite {
	originalModel = strings.TrimSpace(originalModel)
	upstreamModel = strings.TrimSpace(upstreamModel)
	if account != nil && account.Platform == PlatformOpenAI {
		clientModel := openAIClientVisibleModel(c, account, originalModel)
		if clientModel != "" && upstreamModel != "" && clientModel != upstreamModel {
			return openAIResponseModelRewrite{to: clientModel, force: true}
		}
		return openAIResponseModelRewrite{}
	}
	if originalModel != "" && upstreamModel != "" && originalModel != upstreamModel {
		return openAIResponseModelRewrite{from: upstreamModel, to: originalModel}
	}
	return openAIResponseModelRewrite{}
}

// forcedOnly 丢弃旧的等值替换语义，供原本不改写响应 model 的路径（raw Chat
// Completions）只接入 OpenAI 平台的强制覆盖，不改变其他平台的透传行为。
func (r openAIResponseModelRewrite) forcedOnly() openAIResponseModelRewrite {
	if r.force {
		return r
	}
	return openAIResponseModelRewrite{}
}

func (r openAIResponseModelRewrite) active() bool {
	return r.to != "" && (r.force || r.from != "")
}

// mayApply 是快速预判，避免对绝大多数不含 model 的事件做 JSON 解析。
func (r openAIResponseModelRewrite) mayApply(data string) bool {
	if !r.active() {
		return false
	}
	if r.force {
		return strings.Contains(data, `"model"`)
	}
	return strings.Contains(data, r.from)
}

func (r openAIResponseModelRewrite) mayApplyBytes(data []byte) bool {
	if !r.active() {
		return false
	}
	if r.force {
		return bytes.Contains(data, []byte(`"model"`))
	}
	return bytes.Contains(data, []byte(r.from))
}

// applyJSON 改写一个 JSON 文档（非流式响应体、SSE data 负载或 WS 帧）。
func (r openAIResponseModelRewrite) applyJSON(data []byte) []byte {
	if !r.mayApplyBytes(data) {
		return data
	}
	if !r.force {
		return replaceOpenAIWSMessageModel(data, r.from, r.to)
	}
	values := gjson.GetManyBytes(data, openAIForcedResponseModelPaths...)
	updated := data
	for i, value := range values {
		if value.Type != gjson.String || value.Str == r.to {
			continue
		}
		if next, err := sjson.SetBytes(updated, openAIForcedResponseModelPaths[i], r.to); err == nil {
			updated = next
		}
	}
	return updated
}

// applySSELine 改写单行 SSE；非 data 行、[DONE] 和非 JSON 负载原样返回。
func (r openAIResponseModelRewrite) applySSELine(line string) string {
	if !r.mayApply(line) {
		return line
	}
	data, ok := extractOpenAISSEDataLine(line)
	if !ok || data == "" || data == "[DONE]" {
		return line
	}
	if !r.force {
		return replaceOpenAIModelInSSELine(line, r.from, r.to)
	}
	trimmed := strings.TrimSpace(data)
	if !strings.HasPrefix(trimmed, "{") {
		return line
	}
	updated := r.applyJSON([]byte(data))
	if bytes.Equal(updated, []byte(data)) {
		return line
	}
	return "data: " + string(updated)
}

// applySSEBody 逐行改写整段 SSE 文本。
func (r openAIResponseModelRewrite) applySSEBody(body string) string {
	if !r.mayApply(body) {
		return body
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		lines[i] = r.applySSELine(line)
	}
	return strings.Join(lines, "\n")
}
