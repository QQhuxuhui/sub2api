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

// openAIForcedResponseModelHeaderPaths 是载荷里内嵌响应头的位置：Responses 流事件
// 的 response.headers，以及 WS response.metadata 事件的顶层 headers。Codex 正是
// 靠其中的 openai-model / x-openai-model 判断服务端模型并提示「模型被替换」，
// 它忽略载荷里的 model 字段（codex-rs/codex-api/src/sse/responses.rs）。
var openAIForcedResponseModelHeaderPaths = []string{"response.headers", "headers"}

func isOpenAIModelHeaderName(name string) bool {
	return strings.EqualFold(name, "openai-model") || strings.EqualFold(name, "x-openai-model")
}

// containsModelHeaderKeyHint 判断是否可能含 "...-model" 形式的 JSON 键（大小写不敏感），
// 热路径上逐行调用，不做内存分配。
func containsModelHeaderKeyHint[T string | []byte](data T) bool {
	const suffix = `-model"`
	for i := 0; i+len(suffix) <= len(data); i++ {
		if data[i] != '-' {
			continue
		}
		matched := true
		for j := 1; j < len(suffix); j++ {
			ch := data[i+j]
			if 'A' <= ch && ch <= 'Z' {
				ch += 'a' - 'A'
			}
			if ch != suffix[j] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// openAIResponseModelRewrite 描述如何把上游响应里的 model 还原成客户端可见的名字。
//
// OpenAI 平台账号：只要能取到客户端请求的模型名就 force=true，响应 model 字段
// 无论上游返回什么都一律覆盖成客户端请求体里原本的名字。不要求网关这一层改过名
// （渠道映射、/v1/messages 默认映射、账号 model_mapping、上游模型归一化）：没配任何
// 映射、上游自己偷换模型（请求 gpt-6-astra 回 gpt-6.1-sol）同样不外泄。字段值已等于
// 客户端模型时原样返回，正常响应不多一个字节。
// 上游原值由 upstreamResponseModelObserver 在改写之前记录，管理端日志可见。
//
// 其他平台保持旧语义：仅当字段值恰好等于上游模型名时才替换。
type openAIResponseModelRewrite struct {
	from  string
	to    string
	force bool
	// stripCacheWrite 为 true 时，把发给客户端的 usage 里缓存写入相关字段删掉
	// （账号开关 StripsOpenAICacheCreation）。与 model 改写共用同一批改写点。
	stripCacheWrite bool
}

// openAICacheWriteUsagePaths 是上游 usage 里可能出现的缓存写入字段，与
// openAICacheCreationTokensFromUsage 识别的位置一一对应；非流式体 / Chat
// Completions 在 usage.*，Responses 事件在 response.usage.*。
var openAICacheWriteUsageFields = []string{
	"input_tokens_details.cache_write_tokens",
	"input_tokens_details.cache_creation_tokens",
	"prompt_tokens_details.cache_write_tokens",
	"prompt_tokens_details.cache_creation_tokens",
	"cache_write_tokens",
	"cache_creation_input_tokens",
	"cache_write_input_tokens",
	"cache_creation_tokens",
}

var openAICacheWriteUsagePaths = func() []string {
	paths := make([]string, 0, len(openAICacheWriteUsageFields)*3)
	for _, prefix := range []string{"usage.", "response.usage.", "message.usage."} {
		for _, field := range openAICacheWriteUsageFields {
			paths = append(paths, prefix+field)
		}
	}
	return paths
}()

// containsCacheWriteHint 是热路径上的字符串预检：没有这两个子串的事件不可能带缓存写入字段。
func containsCacheWriteHint[T string | []byte](data T) bool {
	s := string(data)
	return strings.Contains(s, "cache_write") || strings.Contains(s, "cache_creation")
}

// stripOpenAICacheWriteUsage 删除 usage 里的缓存写入字段；没有这些字段时原样返回同一切片。
func stripOpenAICacheWriteUsage(data []byte) []byte {
	return stripOpenAICacheWritePaths(data, openAICacheWriteUsagePaths)
}

func stripOpenAICacheWritePaths(data []byte, paths []string) []byte {
	if !containsCacheWriteHint(data) {
		return data
	}
	updated := data
	for _, path := range paths {
		if !gjson.GetBytes(updated, path).Exists() {
			continue
		}
		if next, err := sjson.DeleteBytes(updated, path); err == nil {
			updated = next
		}
	}
	return updated
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
		strip := account.StripsOpenAICacheCreation()
		if clientModel := openAIClientVisibleModel(c, account, originalModel); clientModel != "" {
			return openAIResponseModelRewrite{to: clientModel, force: true, stripCacheWrite: strip}
		}
		return openAIResponseModelRewrite{stripCacheWrite: strip}
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
	return openAIResponseModelRewrite{stripCacheWrite: r.stripCacheWrite}
}

// rewritesModel 报告是否要改写 model 字段（强制或等值替换）。
func (r openAIResponseModelRewrite) rewritesModel() bool {
	return r.to != "" && (r.force || r.from != "")
}

func (r openAIResponseModelRewrite) active() bool {
	return r.rewritesModel() || r.stripCacheWrite
}

// mayApply 是快速预判，避免对绝大多数不含 model 的事件做 JSON 解析。
func (r openAIResponseModelRewrite) mayApply(data string) bool {
	if r.stripCacheWrite && containsCacheWriteHint(data) {
		return true
	}
	if !r.rewritesModel() {
		return false
	}
	if r.force {
		return strings.Contains(data, `"model"`) || containsModelHeaderKeyHint(data)
	}
	return strings.Contains(data, r.from)
}

func (r openAIResponseModelRewrite) mayApplyBytes(data []byte) bool {
	if r.stripCacheWrite && containsCacheWriteHint(data) {
		return true
	}
	if !r.rewritesModel() {
		return false
	}
	if r.force {
		return bytes.Contains(data, []byte(`"model"`)) || containsModelHeaderKeyHint(data)
	}
	return bytes.Contains(data, []byte(r.from))
}

// appliesToWSEvent 决定某个 WS 事件是否需要改写。强制模式不按事件类型过滤，
// 交给 mayApply 的字符串预检，避免 response.metadata / response.queued 或上游
// 新增的事件类型漏改。
func (r openAIResponseModelRewrite) appliesToWSEvent(eventType string) bool {
	if !r.active() {
		return false
	}
	return r.force || r.stripCacheWrite || openAIWSEventMayContainModel(eventType)
}

// applyJSON 改写一个 JSON 文档（非流式响应体、SSE data 负载或 WS 帧）。
func (r openAIResponseModelRewrite) applyJSON(data []byte) []byte {
	if !r.mayApplyBytes(data) {
		return data
	}
	updated := data
	switch {
	case !r.rewritesModel():
	case !r.force:
		updated = replaceOpenAIWSMessageModel(updated, r.from, r.to)
	default:
		values := gjson.GetManyBytes(updated, openAIForcedResponseModelPaths...)
		for i, value := range values {
			if value.Type != gjson.String || value.Str == r.to {
				continue
			}
			if next, err := sjson.SetBytes(updated, openAIForcedResponseModelPaths[i], r.to); err == nil {
				updated = next
			}
		}
		for _, headersPath := range openAIForcedResponseModelHeaderPaths {
			updated = r.rewriteEmbeddedModelHeaders(updated, headersPath)
		}
	}
	if r.stripCacheWrite {
		updated = stripOpenAICacheWriteUsage(updated)
	}
	return updated
}

// rewriteEmbeddedModelHeaders 把 headersPath 对象里的 openai-model / x-openai-model
// 改成客户端模型；值可以是字符串或字符串数组，保持原有形状。
func (r openAIResponseModelRewrite) rewriteEmbeddedModelHeaders(data []byte, headersPath string) []byte {
	headers := gjson.GetBytes(data, headersPath)
	if !headers.IsObject() {
		return data
	}
	updated := data
	headers.ForEach(func(key, value gjson.Result) bool {
		if !isOpenAIModelHeaderName(key.Str) {
			return true
		}
		// 键名只会是 openai-model / x-openai-model 的大小写变体，无需路径转义。
		path := headersPath + "." + key.Str
		switch {
		case value.Type == gjson.String:
			if value.Str != r.to {
				if next, err := sjson.SetBytes(updated, path, r.to); err == nil {
					updated = next
				}
			}
		case value.IsArray():
			value.ForEach(func(index, item gjson.Result) bool {
				if item.Type == gjson.String && item.Str != r.to {
					if next, err := sjson.SetBytes(updated, path+"."+index.String(), r.to); err == nil {
						updated = next
					}
				}
				return true
			})
		}
		return true
	})
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
	if !r.force && !r.stripCacheWrite {
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

// rewriteOpenAIImagesRawSSELine 改写原样透传的一行 SSE 字节（保留行尾的 \r\n / \n）。
func rewriteOpenAIImagesRawSSELine(line []byte, r openAIResponseModelRewrite) []byte {
	if !r.mayApplyBytes(line) {
		return line
	}
	content := bytes.TrimRight(line, "\r\n")
	updated := r.applySSELine(string(content))
	if updated == string(content) {
		return line
	}
	out := make([]byte, 0, len(updated)+len(line)-len(content))
	out = append(out, updated...)
	return append(out, line[len(content):]...)
}

// applyImageMeta 在拼装生图输出前覆盖元数据里的模型名，避免对 MB 级 b64 载荷再做一遍 JSON 改写。
func (r openAIResponseModelRewrite) applyImageMeta(meta openAIResponsesImageResult) openAIResponsesImageResult {
	if r.force && r.to != "" && meta.Model != "" {
		meta.Model = r.to
	}
	return meta
}
