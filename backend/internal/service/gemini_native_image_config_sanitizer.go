package service

import (
	"bytes"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// geminiImageConfigUnsupportedKeys 列出 Google 的 generateContent 不认、但客户端常带的
// imageConfig 字段。
//
// outputMimeType 来自 Imagen / google-genai SDK 的 GenerateImagesConfig，那边确实有
// output_mime_type；但 generateContent 的 image_config 只有 aspectRatio / imageSize，
// 带上去 Google 直接 400：
//
//	Invalid JSON payload received. Unknown name "outputMimeType" at
//	'request.generation_config.image_config': Cannot find field.
//
// 整个请求被拒，图一张也出不来。这里按黑名单剔除——只删已知不被接受的键，其余原样
// 透传，Google 以后新增字段不会被误伤。
var geminiImageConfigUnsupportedKeys = []string{"outputMimeType", "output_mime_type"}

// proto JSON 两种命名都收，客户端用哪种都可能。
var (
	geminiGenerationConfigKeys = []string{"generationConfig", "generation_config"}
	geminiImageConfigKeys      = []string{"imageConfig", "image_config"}
)

// SanitizeGeminiNativeImageConfig 从 Gemini 原生请求体里剔除 imageConfig 下 Google 不接受的
// 字段，返回处理后的 body 和被删掉的字段路径（供日志观测；无改动时返回原 body 与 nil）。
//
// 用 sjson 做定点删除而不是整体反序列化再序列化：请求体在这条链路上还要参与会话哈希与
// 安全审计，保持其余字节原样能避免无谓的形态变化。
func SanitizeGeminiNativeImageConfig(body []byte) ([]byte, []string) {
	if len(body) == 0 {
		return body, nil
	}
	// 绝大多数请求不带这些字段，先做一次廉价的字节扫描短路掉。
	hit := false
	for _, key := range geminiImageConfigUnsupportedKeys {
		if bytes.Contains(body, []byte(`"`+key+`"`)) {
			hit = true
			break
		}
	}
	// Escaped JSON keys (for example "output\u004dimeType") do not match the
	// literal scan. Only parse the uncommon escaped-body case before returning.
	if !hit && !bytes.Contains(body, []byte(`\u`)) {
		return body, nil
	}

	result := body
	var removed []string
	for _, genKey := range geminiGenerationConfigKeys {
		for _, imgKey := range geminiImageConfigKeys {
			for _, badKey := range geminiImageConfigUnsupportedKeys {
				path := genKey + "." + imgKey + "." + badKey
				value := gjson.GetBytes(result, path)
				// Exists is false for an explicit JSON null, but null is still an
				// unsupported field and must be removed.
				if value.Raw == "" {
					continue
				}
				next, err := sjson.DeleteBytes(result, path)
				if err != nil {
					// 删不掉就保持原样：宁可让上游报原来的错，也不要把 body 改坏。
					continue
				}
				result = next
				removed = append(removed, path)
			}
		}
	}
	if len(removed) == 0 {
		return body, nil
	}
	return result, removed
}
