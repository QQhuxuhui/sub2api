package service

import (
	"bytes"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// geminiNativeOutputMeter 在 ForwardNative 的三个成功出口上逐载荷观察上游原始响应
// （OAuth 已解包、mask/flash 改写前），用于两件事：
//
//  1. 判定「HTTP 200 但只有错误对象、没有任何有效输出」，以便在写出前换号；
//  2. 上游完全没给 usageMetadata 却交付了文本时，按累计字符数本地估算输出 token。
//
// 只保存定长计数和一段有上限的错误体，不保存输出文本，内存恒定。
// 所有方法对 nil 接收者安全：未启用时（非生成类 action、直接调用处理函数的测试）全部 no-op。
type geminiNativeOutputMeter struct {
	usageSeen   bool
	contentSeen bool
	errorSeen   bool
	firstError  []byte
	textRunes   int
	textASCII   int
}

const (
	geminiNativeOutputMeterContextKey = "gemini_native_output_meter"
	// geminiNativeOutputMeterMaxErrorBody 是换号时回传的首个错误载荷上限。
	geminiNativeOutputMeterMaxErrorBody = 4 * 1024
	// geminiNativeErrorHoldMaxBytes 是流式暂扣缓冲上限；超出即放行，按现状继续。
	geminiNativeErrorHoldMaxBytes = 64 * 1024
)

// geminiNativeOutputPartKeys 是视为「有效输出」的 part 字段（任一存在即可）。
// 非空 text 单独判断。
var geminiNativeOutputPartKeys = []string{
	"inlineData", "inline_data",
	"fileData", "file_data",
	"functionCall", "function_call",
	"executableCode", "executable_code",
	"codeExecutionResult", "code_execution_result",
}

func beginGeminiNativeOutputMeter(c *gin.Context) *geminiNativeOutputMeter {
	m := &geminiNativeOutputMeter{}
	if c != nil {
		c.Set(geminiNativeOutputMeterContextKey, m)
	}
	return m
}

// clearGeminiNativeOutputMeter 清掉上一次换号尝试留下的计量器。
func clearGeminiNativeOutputMeter(c *gin.Context) {
	if c == nil {
		return
	}
	if _, ok := c.Get(geminiNativeOutputMeterContextKey); ok {
		c.Set(geminiNativeOutputMeterContextKey, (*geminiNativeOutputMeter)(nil))
	}
}

func geminiNativeOutputMeterFromContext(c *gin.Context) *geminiNativeOutputMeter {
	if c == nil {
		return nil
	}
	v, ok := c.Get(geminiNativeOutputMeterContextKey)
	if !ok {
		return nil
	}
	m, _ := v.(*geminiNativeOutputMeter)
	return m
}

// observe 观察一个 JSON 载荷。raw 不被保留（错误体会被拷贝）。
func (m *geminiNativeOutputMeter) observe(raw []byte) {
	if m == nil {
		return
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return
	}
	if u := gjson.GetBytes(raw, "usageMetadata"); u.Exists() && u.IsObject() {
		m.usageSeen = true
	}
	if e := gjson.GetBytes(raw, "error"); e.Exists() && e.Type != gjson.Null {
		if !m.errorSeen {
			m.errorSeen = true
			n := len(raw)
			if n > geminiNativeOutputMeterMaxErrorBody {
				n = geminiNativeOutputMeterMaxErrorBody
			}
			m.firstError = append([]byte(nil), raw[:n]...)
		}
	}
	gjson.GetBytes(raw, "candidates").ForEach(func(_, cand gjson.Result) bool {
		cand.Get("content.parts").ForEach(func(_, part gjson.Result) bool {
			if !part.IsObject() {
				return true
			}
			if t := part.Get("text"); t.Type == gjson.String && t.Str != "" {
				m.contentSeen = true
				for _, r := range t.Str {
					m.textRunes++
					if r <= 0x7f {
						m.textASCII++
					}
				}
			}
			if !m.contentSeen {
				for _, k := range geminiNativeOutputPartKeys {
					if part.Get(k).Exists() {
						m.contentSeen = true
						break
					}
				}
			}
			return true
		})
		return true
	})
}

// errorOnly 报告目前为止是否「出现过错误对象且没有任何有效输出」。
func (m *geminiNativeOutputMeter) errorOnly() bool {
	return m != nil && m.errorSeen && !m.contentSeen
}

// failoverError 构造「200 但只有错误」时交给 handler 换号的错误。
func (m *geminiNativeOutputMeter) failoverError() *UpstreamFailoverError {
	var body []byte
	if m != nil {
		body = m.firstError
	}
	return &UpstreamFailoverError{
		StatusCode:             http.StatusBadGateway,
		ResponseBody:           body,
		RetryableOnSameAccount: false,
	}
}

// estimatedOutputTokens 按 estimateTokensForText 的规则（ASCII 占比 ≥0.8 时约 4 字符/token，
// 否则 1 字符/token）把累计计数换算成 token。
func (m *geminiNativeOutputMeter) estimatedOutputTokens() int {
	if m == nil || m.textRunes <= 0 {
		return 0
	}
	if float64(m.textASCII)/float64(m.textRunes) >= 0.8 {
		return (m.textRunes + 3) / 4
	}
	return m.textRunes
}

// shouldEstimate 报告是否应对最终 usage 做本地估算：上游从未给出 usageMetadata、
// 确有文本输出，且最终计费 usage 全为 0（图片伪装/flash 修复已合成 usage 的自然排除）。
func (m *geminiNativeOutputMeter) shouldEstimate(usage *ClaudeUsage) bool {
	if m == nil || m.usageSeen || m.textRunes <= 0 || usage == nil {
		return false
	}
	return usage.InputTokens == 0 && usage.OutputTokens == 0 &&
		usage.CacheCreationInputTokens == 0 && usage.CacheReadInputTokens == 0 &&
		usage.CacheCreation5mTokens == 0 && usage.CacheCreation1hTokens == 0 &&
		usage.ImageOutputTokens == 0
}
