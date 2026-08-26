package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Google 的 generateContent 对 image_config 下的未知字段是硬 400（整个请求被拒），
// 所以 outputMimeType 必须在转发前剔除。
func TestSanitizeGeminiNativeImageConfig_StripsOutputMimeType(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"draw a cat"}]}],` +
		`"generationConfig":{"imageConfig":{"aspectRatio":"9:16","imageSize":"4K","outputMimeType":"image/png"}}}`)

	sanitized, removed := SanitizeGeminiNativeImageConfig(body)
	require.Equal(t, []string{"generationConfig.imageConfig.outputMimeType"}, removed)
	require.False(t, gjson.GetBytes(sanitized, "generationConfig.imageConfig.outputMimeType").Exists())
	// 兄弟字段与请求其余部分必须原样保留。
	require.Equal(t, "9:16", gjson.GetBytes(sanitized, "generationConfig.imageConfig.aspectRatio").String())
	require.Equal(t, "4K", gjson.GetBytes(sanitized, "generationConfig.imageConfig.imageSize").String())
	require.Equal(t, "draw a cat", gjson.GetBytes(sanitized, "contents.0.parts.0.text").String())
}

// proto JSON 两种命名都合法，客户端用哪种都要认。
func TestSanitizeGeminiNativeImageConfig_SnakeCaseVariants(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "snake generation and image config",
			body: `{"generation_config":{"image_config":{"imageSize":"2K","output_mime_type":"image/jpeg"}}}`,
			want: "generation_config.image_config.output_mime_type",
		},
		{
			name: "camel container with snake field",
			body: `{"generationConfig":{"imageConfig":{"output_mime_type":"image/jpeg"}}}`,
			want: "generationConfig.imageConfig.output_mime_type",
		},
		{
			name: "snake container with camel field",
			body: `{"generation_config":{"image_config":{"outputMimeType":"image/webp"}}}`,
			want: "generation_config.image_config.outputMimeType",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sanitized, removed := SanitizeGeminiNativeImageConfig([]byte(tt.body))
			require.Equal(t, []string{tt.want}, removed)
			require.False(t, gjson.GetBytes(sanitized, tt.want).Exists())
		})
	}
}

// 不带该字段的请求（也就是现存的绝大多数）必须原样返回，字节都不能动。
func TestSanitizeGeminiNativeImageConfig_LeavesOtherRequestsUntouched(t *testing.T) {
	for _, body := range []string{
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
		`{"generationConfig":{"imageConfig":{"aspectRatio":"1:1","imageSize":"1K"}}}`,
		`{"generationConfig":{"temperature":0.7,"thinkingConfig":{"includeThoughts":true}}}`,
		``,
		`not json`,
	} {
		sanitized, removed := SanitizeGeminiNativeImageConfig([]byte(body))
		require.Nil(t, removed, "body %q must not be reported as modified", body)
		require.Equal(t, body, string(sanitized), "body %q must be returned byte-identical", body)
	}
}

// 只在 imageConfig 下剔除：同名字段出现在别处（例如客户端自定义的顶层配置）不该被误删。
func TestSanitizeGeminiNativeImageConfig_OnlyTouchesImageConfig(t *testing.T) {
	body := []byte(`{"generationConfig":{"outputMimeType":"image/png","imageConfig":{"imageSize":"2K"}},"outputMimeType":"image/png"}`)

	sanitized, removed := SanitizeGeminiNativeImageConfig(body)
	require.Nil(t, removed)
	require.Equal(t, string(body), string(sanitized))
}

func TestSanitizeGeminiNativeImageConfig_RemovesEscapedAndNullKeys(t *testing.T) {
	t.Run("escaped key", func(t *testing.T) {
		body := []byte(`{"generationConfig":{"imageConfig":{"output\u004dimeType":"image/png","aspectRatio":"1:1"}}}`)
		sanitized, removed := SanitizeGeminiNativeImageConfig(body)
		require.Equal(t, []string{"generationConfig.imageConfig.outputMimeType"}, removed)
		require.False(t, gjson.GetBytes(sanitized, "generationConfig.imageConfig.outputMimeType").Exists())
		require.Equal(t, "1:1", gjson.GetBytes(sanitized, "generationConfig.imageConfig.aspectRatio").String())
	})

	t.Run("null value", func(t *testing.T) {
		body := []byte(`{"generationConfig":{"imageConfig":{"output_mime_type":null,"aspectRatio":"1:1"}}}`)
		sanitized, removed := SanitizeGeminiNativeImageConfig(body)
		require.Equal(t, []string{"generationConfig.imageConfig.output_mime_type"}, removed)
		require.NotContains(t, string(sanitized), `"output_mime_type"`)
		require.Equal(t, "1:1", gjson.GetBytes(sanitized, "generationConfig.imageConfig.aspectRatio").String())
	})

	t.Run("escaped container keys", func(t *testing.T) {
		body := []byte(`{"generation\u0043onfig":{"image\u0043onfig":{"output\u004dimeType":"image/png"}}}`)
		sanitized, removed := SanitizeGeminiNativeImageConfig(body)
		require.Equal(t, []string{"generationConfig.imageConfig.outputMimeType"}, removed)
		require.NotContains(t, string(sanitized), `output\u004dimeType`)
	})
}
