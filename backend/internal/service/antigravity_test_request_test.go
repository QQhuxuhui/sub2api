//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestBuildGeminiTestRequest_OmitsMaxOutputTokens 验证 antigravity 连接测试的 Gemini 请求
// 不再设置 maxOutputTokens。thinking 模型（如 gemini-3.1-pro-high）需要为思考保留 output
// 预算，maxOutputTokens=1 会被上游判 INVALID_ARGUMENT。改为不发该字段、交由上游用默认值。
func TestBuildGeminiTestRequest_OmitsMaxOutputTokens(t *testing.T) {
	svc := &AntigravityGatewayService{}

	raw, err := svc.buildGeminiTestRequest("proj-1", "gemini-3.1-pro-high")
	require.NoError(t, err)

	j := string(raw)
	require.Equal(t, "gemini-3.1-pro-high", gjson.Get(j, "model").String())
	// 仍保留最小内容与身份提示词（上游要求必须有 systemInstruction）
	require.True(t, gjson.Get(j, "request.contents.0.parts.0.text").Exists(),
		"test request must still carry minimal user content")
	require.True(t, gjson.Get(j, "request.systemInstruction.parts.0.text").Exists(),
		"test request must still carry identity systemInstruction")
	// 关键：不再发 maxOutputTokens
	require.False(t, gjson.Get(j, "request.generationConfig.maxOutputTokens").Exists(),
		"gemini test request must not set maxOutputTokens (breaks thinking models like gemini-3.1-pro-high)")
}
