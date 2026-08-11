package service

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 分组级「响应模型名归一化」开关（groups.normalize_response_model）。
//
// 默认关闭时，网关只在「上游返回值 == 我方转发的模型」时才把响应体 model 改回客户端
// 请求的模型；上游偷换模型（如自建 new-api 把 gemini-3.1-pro 换成 gemini-3.1-pro-low）
// 会原样透传给下游，下游若同为 sub2api 会标记 upstream model mismatch。
//
// 开启后，响应体里的模型名一律改写为客户端请求的模型，对下游完全一致。
// 注意：上游响应模型审计（ObserveXxx）在所有路径上都发生在改写之前，因此
// usage_logs.upstream_response_model / upstream_model_mismatch 仍忠实记录上游真实
// 模型——本地监控能力不受影响，只是不再外泄给下游。
//
// 开关直接从 gin.Context 里的 API Key 读取，而不是另设 context key：鉴权中间件在所有
// 网关路径上都会写入 API Key，这样新增协议路径时无需再记得"设一次 ctx key"，
// 避免某个平台静默不生效。
//
// 覆盖范围（截至引入时）：
//   - Anthropic 原生 /v1/messages 流式(message_start)与非流式
//   - OpenAI Chat Completions / Responses 的流式(SSE，含 response.model 嵌套)、
//     非流式、compact 聚合与 WS v2 终态响应
//   - Gemini 原生 /v1beta（modelVersion）：逐块流式、collected-SSE、非流式
//
// 未覆盖：antigravity 网关的 Gemini 转发出口（handleGeminiStreamingResponse /
// handleGeminiStreamToNonStreaming 三个 handler 均未持有客户端请求模型，需改签名透传；
// 且该平台目前无上游模型偷换流量）。若将来 antigravity 上游开始偷换模型，需补此路径。
func shouldNormalizeResponseModel(c interface{ Get(string) (any, bool) }) bool {
	if c == nil {
		return false
	}
	apiKey := getAPIKeyFromContext(c)
	return apiKey != nil && apiKey.Group != nil && apiKey.Group.NormalizeResponseModel
}

// forceSetJSONStringField 无条件把 JSON 里的某个字符串字段改写为 value（字段存在时）。
// 用于「响应模型名归一化」：与只在精确匹配时改写的默认路径不同，这里不比较原值。
// 字段不存在时保持原样（不凭空注入），避免给不含该字段的事件（如 SSE 增量块）加字段。
func forceSetJSONStringField(body []byte, path, value string) []byte {
	if len(body) == 0 || path == "" || value == "" {
		return body
	}
	m := gjson.GetBytes(body, path)
	if !m.Exists() || m.Type != gjson.String || m.Str == value {
		return body
	}
	newBody, err := sjson.SetBytes(body, path, value)
	if err != nil {
		return body
	}
	return newBody
}

// forceSetJSONStringFieldString 是 forceSetJSONStringField 的字符串版本（SSE data 行用）。
func forceSetJSONStringFieldString(data, path, value string) string {
	if data == "" || path == "" || value == "" {
		return data
	}
	m := gjson.Get(data, path)
	if !m.Exists() || m.Type != gjson.String || m.Str == value {
		return data
	}
	newData, err := sjson.Set(data, path, value)
	if err != nil {
		return data
	}
	return newData
}
