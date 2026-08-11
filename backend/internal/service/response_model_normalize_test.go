//go:build unit

package service

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// ctxWithGroupNormalize 构造一个带 API Key 的 gin.Context，分组开关按参数设置。
func ctxWithGroupNormalize(enabled bool) *gin.Context {
	c, _ := gin.CreateTestContext(nil)
	c.Set("api_key", &APIKey{Group: &Group{NormalizeResponseModel: enabled}})
	return c
}

func TestShouldNormalizeResponseModel(t *testing.T) {
	require.True(t, shouldNormalizeResponseModel(ctxWithGroupNormalize(true)))
	require.False(t, shouldNormalizeResponseModel(ctxWithGroupNormalize(false)), "默认关闭")

	// 无 API Key / 无分组时必须回落到关闭，不能 panic。
	noKey, _ := gin.CreateTestContext(nil)
	require.False(t, shouldNormalizeResponseModel(noKey))

	noGroup, _ := gin.CreateTestContext(nil)
	noGroup.Set("api_key", &APIKey{})
	require.False(t, shouldNormalizeResponseModel(noGroup))
}

func TestForceSetJSONStringField(t *testing.T) {
	// 上游偷换的模型名被归一化（这正是默认路径做不到的）
	body := []byte(`{"model":"gemini-3.1-pro-low","x":1}`)
	got := forceSetJSONStringField(body, "model", "gemini-3.1-pro")
	require.Equal(t, "gemini-3.1-pro", gjson.GetBytes(got, "model").Str)
	require.EqualValues(t, 1, gjson.GetBytes(got, "x").Int(), "其余字段不受影响")

	// 字段不存在时不凭空注入（避免给增量块加字段）
	noField := []byte(`{"delta":"hi"}`)
	require.JSONEq(t, string(noField), string(forceSetJSONStringField(noField, "model", "m")))

	// 非字符串字段不动
	notStr := []byte(`{"model":123}`)
	require.JSONEq(t, string(notStr), string(forceSetJSONStringField(notStr, "model", "m")))

	// 空值保护
	require.Nil(t, forceSetJSONStringField(nil, "model", "m"))
	require.JSONEq(t, string(body), string(forceSetJSONStringField(body, "model", "")))
}

func TestReplaceModelInResponseBody_ForceNormalizesSubstitutedModel(t *testing.T) {
	gw := &GatewayService{}
	// 上游把 claude-opus-4-6 换成了 claude-opus-4-6-thinking
	body := []byte(`{"model":"claude-opus-4-6-thinking","type":"message"}`)

	// 默认（关）：不等于我方转发模型 → 原样透传，下游能看见偷换
	kept := gw.replaceModelInResponseBody(body, "claude-opus-4-6", "claude-opus-4-6", false)
	require.Equal(t, "claude-opus-4-6-thinking", gjson.GetBytes(kept, "model").Str)

	// 开启：无条件归一化为客户端请求的模型
	normalized := gw.replaceModelInResponseBody(body, "claude-opus-4-6", "claude-opus-4-6", true)
	require.Equal(t, "claude-opus-4-6", gjson.GetBytes(normalized, "model").Str)
}

func TestOpenAIReplaceModelInSSELine_ForceRewritesBothFields(t *testing.T) {
	svc := &OpenAIGatewayService{}
	// Responses 协议：顶层 model 与嵌套 response.model 同时存在时两个都要改，
	// 默认实现命中第一个就 return，归一化不能只改一半。
	line := `data: {"model":"grok-4.5-build","response":{"model":"grok-4.5-build"}}`

	got := svc.replaceModelInSSELine(line, "grok-4.5", "grok-4.5", true)
	data := got[len("data: "):]
	require.Equal(t, "grok-4.5", gjson.Get(data, "model").Str)
	require.Equal(t, "grok-4.5", gjson.Get(data, "response.model").Str, "嵌套 response.model 也必须归一化")

	// 关闭时原样透传
	require.Equal(t, line, svc.replaceModelInSSELine(line, "grok-4.5", "grok-4.5", false))

	// [DONE] 与非 data 行不受影响
	require.Equal(t, "data: [DONE]", svc.replaceModelInSSELine("data: [DONE]", "a", "b", true))
	require.Equal(t, "event: ping", svc.replaceModelInSSELine("event: ping", "a", "b", true))
}

func TestGeminiModelVersionNormalization(t *testing.T) {
	// Gemini 用 modelVersion 字段（占本站上游偷换流量的绝大多数）
	body := []byte(`{"modelVersion":"gemini-3.1-pro-low","candidates":[]}`)
	got := forceSetJSONStringField(body, "modelVersion", "gemini-3.1-pro")
	require.Equal(t, "gemini-3.1-pro", gjson.GetBytes(got, "modelVersion").Str)
	require.True(t, gjson.GetBytes(got, "candidates").Exists(), "其余结构保持")
}
