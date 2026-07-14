package service

import (
	"math/rand"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// isGeminiProImageModel 判断下游请求的模型是否为 Gemini pro 生图模型。
func isGeminiProImageModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gemini-3-pro-image")
}

// proImageProfile 描述某档位下 pro 生图 usageMetadata 的特征。
// ImageTokens 为确定值；其余为每请求随机取值的闭区间。
type proImageProfile struct {
	ImageTokens int
	TextMin     int
	TextMax     int
	ThoughtsMin int
	ThoughtsMax int
}

// geminiProImageProfile 返回归一化档位（1K/2K/4K，大小写不敏感）的画像；
// 未知档位回落到 2K（与 NormalizeImageBillingTierOrDefault 的默认一致）。
func geminiProImageProfile(tier string) proImageProfile {
	switch strings.ToUpper(strings.TrimSpace(tier)) {
	case "1K":
		return proImageProfile{ImageTokens: 1120, TextMin: 78, TextMax: 92, ThoughtsMin: 115, ThoughtsMax: 140}
	case "4K":
		return proImageProfile{ImageTokens: 2000, TextMin: 92, TextMax: 112, ThoughtsMin: 150, ThoughtsMax: 170}
	default: // 2K 及未知
		return proImageProfile{ImageTokens: 1120, TextMin: 80, TextMax: 100, ThoughtsMin: 145, ThoughtsMax: 165}
	}
}

// geminiProImageIntn 是可注入的随机源，返回 [0,n) 内的整数；测试可替换为确定序列。
var geminiProImageIntn = rand.Intn

// geminiSynthUsage 是一次合成的 pro 生图 usage 结果，供响应体改写与计费共用。
type geminiSynthUsage struct {
	PromptTokens     int
	TextTokens       int
	ImageTokens      int
	ThoughtsTokens   int
	CandidatesTokens int
	TotalTokens      int
}

const geminiProImageDefaultPromptTokens = 8

func randInRange(min, max int) int {
	if max <= min {
		return min
	}
	return min + geminiProImageIntn(max-min+1)
}

// synthesizeGeminiProImageUsage 按档位画像合成一份 pro 规范的 usage。
// promptTokens<=0 时使用小默认值，避免 total 明显异常。
func synthesizeGeminiProImageUsage(tier string, promptTokens int) geminiSynthUsage {
	p := geminiProImageProfile(tier)
	if promptTokens <= 0 {
		promptTokens = geminiProImageDefaultPromptTokens
	}
	text := randInRange(p.TextMin, p.TextMax)
	thoughts := randInRange(p.ThoughtsMin, p.ThoughtsMax)
	candidates := p.ImageTokens + text
	return geminiSynthUsage{
		PromptTokens:     promptTokens,
		TextTokens:       text,
		ImageTokens:      p.ImageTokens,
		ThoughtsTokens:   thoughts,
		CandidatesTokens: candidates,
		TotalTokens:      promptTokens + candidates + thoughts,
	}
}

// synthToClaudeUsage 把合成 usage 映射为计费用的 ClaudeUsage（与响应体同源）。
func synthToClaudeUsage(s geminiSynthUsage) *ClaudeUsage {
	return &ClaudeUsage{
		InputTokens:       s.PromptTokens,
		OutputTokens:      s.CandidatesTokens + s.ThoughtsTokens,
		ImageOutputTokens: s.ImageTokens,
	}
}

// shouldMaskGeminiProImage 判断该响应是否需要伪装成 pro。
// 前置由调用方保证已是 pro 生图请求；这里判断响应是否为「真 pro」。
func shouldMaskGeminiProImage(respBody []byte, model string) bool {
	mv := strings.ToLower(strings.TrimSpace(gjson.GetBytes(respBody, "modelVersion").String()))
	modelLower := strings.ToLower(strings.TrimSpace(model))
	modelVersionMatches := mv != "" && strings.HasPrefix(mv, modelLower)

	hasImageDetail := false
	gjson.GetBytes(respBody, "usageMetadata.candidatesTokensDetails").ForEach(func(_, detail gjson.Result) bool {
		if detail.Get("modality").String() == "IMAGE" {
			hasImageDetail = true
			return false
		}
		return true
	})

	// 真 pro：modelVersion 匹配 且 有 IMAGE 明细。任一不满足即伪装。
	return !(modelVersionMatches && hasImageDetail)
}

// maskGeminiProImageResponseBody 用合成 usage 改写响应体的 modelVersion 与 usageMetadata。
// 其余字段（含 candidates 图像数据）保持不变。
func maskGeminiProImageResponseBody(body []byte, model string, s geminiSynthUsage) []byte {
	out := body
	set := func(path string, val any) {
		if nb, err := sjson.SetBytes(out, path, val); err == nil {
			out = nb
		}
	}
	del := func(path string) {
		if nb, err := sjson.DeleteBytes(out, path); err == nil {
			out = nb
		}
	}

	set("modelVersion", model)
	del("usageMetadata")
	set("usageMetadata.promptTokenCount", s.PromptTokens)
	set("usageMetadata.candidatesTokenCount", s.CandidatesTokens)
	set("usageMetadata.totalTokenCount", s.TotalTokens)
	set("usageMetadata.thoughtsTokenCount", s.ThoughtsTokens)
	set("usageMetadata.serviceTier", "standard")
	set("usageMetadata.promptTokensDetails.0.modality", "TEXT")
	set("usageMetadata.promptTokensDetails.0.tokenCount", s.PromptTokens)
	set("usageMetadata.candidatesTokensDetails.0.modality", "IMAGE")
	set("usageMetadata.candidatesTokensDetails.0.tokenCount", s.ImageTokens)
	return out
}

// applyGeminiProImageMask 是响应处理器的统一入口：
// 若为 pro 生图请求且响应非真 pro，则合成一次并改写 body、产出同源计费 usage。
// 否则返回原 body、masked=false。
func applyGeminiProImageMask(respBody []byte, model, tier string) (newBody []byte, usage *ClaudeUsage, masked bool) {
	if !isGeminiProImageModel(model) {
		return respBody, nil, false
	}
	if !shouldMaskGeminiProImage(respBody, model) {
		return respBody, nil, false
	}
	promptTokens := int(gjson.GetBytes(respBody, "usageMetadata.promptTokenCount").Int())
	synth := synthesizeGeminiProImageUsage(tier, promptTokens)
	nb := maskGeminiProImageResponseBody(respBody, model, synth)
	return nb, synthToClaudeUsage(synth), true
}
