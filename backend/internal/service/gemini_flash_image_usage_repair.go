package service

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const gemini31FlashImageModel = "gemini-3.1-flash-image"

type geminiImageUsageParams struct {
	Model                string
	ProTier              string
	FlashImageSize       string
	RequestedServiceTier string
	ProMaskEnabled       bool
	FlashRepairEnabled   bool
	FlashPromptTextOnly  bool
}

type geminiFlashUsageRepairOptions struct {
	PromptTextOnly       bool
	RequestedServiceTier string
}

func newGeminiImageUsageParams(model, action, imageSize string, requestBody []byte) geminiImageUsageParams {
	isGeneration := isGeminiImageGenerationAction(action)
	return geminiImageUsageParams{
		Model:                model,
		ProTier:              normalizeOpenAIImageSizeTier(imageSize),
		FlashImageSize:       strings.TrimSpace(imageSize),
		RequestedServiceTier: geminiRequestedServiceTier(requestBody),
		ProMaskEnabled:       isGeneration && isGeminiProImageModel(model),
		FlashRepairEnabled:   isGeneration && isGemini31FlashImageModel(model),
		FlashPromptTextOnly:  geminiPromptIsTextOnly(requestBody),
	}
}

func (p geminiImageUsageParams) proMaskParams() geminiProImageMaskParams {
	return geminiProImageMaskParams{Enabled: p.ProMaskEnabled, Model: p.Model, Tier: p.ProTier}
}

func applyGeminiImageUsageAdjustment(body []byte, params geminiImageUsageParams) ([]byte, *ClaudeUsage, bool) {
	if params.ProMaskEnabled {
		return applyGeminiProImageMask(body, params.Model, params.ProTier)
	}
	if params.FlashRepairEnabled {
		return repairGemini31FlashImageUsage(body, params.Model, params.FlashImageSize, geminiFlashUsageRepairOptions{
			PromptTextOnly:       params.FlashPromptTextOnly,
			RequestedServiceTier: params.RequestedServiceTier,
		})
	}
	return body, nil, false
}

func geminiRequestedServiceTier(body []byte) string {
	switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "serviceTier").String())) {
	case "", "unspecified":
		return "standard"
	case "standard":
		return "standard"
	case "flex":
		return "flex"
	case "priority":
		return "priority"
	default:
		return ""
	}
}

func geminiPromptIsTextOnly(body []byte) bool {
	if !gjson.ValidBytes(body) || gjson.GetBytes(body, "cachedContent").Exists() {
		return false
	}
	contents := gjson.GetBytes(body, "contents")
	if !contents.Exists() || !contents.IsArray() {
		return false
	}
	hasText := false
	textOnly := true
	inspectParts := func(parts gjson.Result) {
		if !parts.Exists() || !parts.IsArray() {
			textOnly = false
			return
		}
		parts.ForEach(func(_, part gjson.Result) bool {
			fields := part.Map()
			if _, ok := fields["text"]; !ok {
				textOnly = false
				return false
			}
			for name := range fields {
				if name != "text" && name != "thoughtSignature" {
					textOnly = false
					return false
				}
			}
			hasText = true
			return true
		})
	}
	contents.ForEach(func(_, content gjson.Result) bool {
		inspectParts(content.Get("parts"))
		return textOnly
	})
	if textOnly {
		systemInstruction := gjson.GetBytes(body, "systemInstruction")
		if systemInstruction.Exists() {
			inspectParts(systemInstruction.Get("parts"))
		}
	}
	return textOnly && hasText
}

func isGemini31FlashImageModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	normalized = strings.TrimPrefix(normalized, "models/")
	return normalized == gemini31FlashImageModel || strings.HasPrefix(normalized, gemini31FlashImageModel+"-")
}

func gemini31FlashImageTokens(size string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(size)) {
	case "":
		// Gemini 3 image models default to 1K when imageSize is omitted.
		return 1120, true
	case "0.5k", "512px":
		return 747, true
	case "1k":
		return 1120, true
	case "2k":
		return 1680, true
	case "4k":
		return 2520, true
	default:
		return 0, false
	}
}

func countGeminiInlineOutputImages(body []byte) int {
	count := 0
	gjson.GetBytes(body, "candidates").ForEach(func(_, candidate gjson.Result) bool {
		candidate.Get("content.parts").ForEach(func(_, part gjson.Result) bool {
			inlineData := part.Get("inlineData")
			mimeType := strings.ToLower(strings.TrimSpace(inlineData.Get("mimeType").String()))
			data := inlineData.Get("data")
			if strings.HasPrefix(mimeType, "image/") && data.Exists() && data.String() != "" {
				count++
			}
			return true
		})
		return true
	})
	return count
}

func findGeminiUsageImageDetail(body []byte) (index int, valid bool) {
	index = -1
	gjson.GetBytes(body, "usageMetadata.candidatesTokensDetails").ForEach(func(key, detail gjson.Result) bool {
		if strings.EqualFold(strings.TrimSpace(detail.Get("modality").String()), "IMAGE") {
			if index < 0 {
				index = int(key.Int())
			}
			if detail.Get("tokenCount").Int() > 0 {
				index = int(key.Int())
				valid = true
				return false
			}
		}
		return true
	})
	return index, valid
}

// repairGemini31FlashImageUsage repairs only the missing or invalid IMAGE usage detail.
// candidatesTokenCount already includes image tokens, so no aggregate count is changed.
func repairGemini31FlashImageUsage(body []byte, model, imageSize string, options geminiFlashUsageRepairOptions) ([]byte, *ClaudeUsage, bool) {
	imageCount := countGeminiInlineOutputImages(body)
	return repairGemini31FlashImageUsageWithCount(body, model, imageSize, imageCount, options)
}

func repairGemini31FlashImageUsageWithCount(body []byte, model, imageSize string, imageCount int, options geminiFlashUsageRepairOptions) ([]byte, *ClaudeUsage, bool) {
	if !isGemini31FlashImageModel(model) || !gjson.ValidBytes(body) {
		return body, nil, false
	}
	usageMetadata := gjson.GetBytes(body, "usageMetadata")
	imageDetailIndex, hasValidImageDetail := findGeminiUsageImageDetail(body)
	if !usageMetadata.Exists() || hasValidImageDetail {
		return body, nil, false
	}
	perImageTokens, ok := gemini31FlashImageTokens(imageSize)
	if !ok {
		return body, nil, false
	}
	if imageCount == 0 {
		return body, nil, false
	}
	imageTokens := perImageTokens * imageCount
	candidates := usageMetadata.Get("candidatesTokenCount")
	if !candidates.Exists() || candidates.Int() < int64(imageTokens) {
		return body, nil, false
	}

	out := body
	set := func(path string, value any) bool {
		updated, err := sjson.SetBytes(out, path, value)
		if err != nil {
			return false
		}
		out = updated
		return true
	}

	detail := map[string]any{"modality": "IMAGE", "tokenCount": imageTokens}
	details := usageMetadata.Get("candidatesTokensDetails")
	if details.Exists() {
		if !details.IsArray() {
			return body, nil, false
		}
		if imageDetailIndex >= 0 {
			if !set("usageMetadata.candidatesTokensDetails."+strconv.Itoa(imageDetailIndex)+".tokenCount", imageTokens) {
				return body, nil, false
			}
		} else {
			if !set("usageMetadata.candidatesTokensDetails.-1", detail) {
				return body, nil, false
			}
		}
	} else {
		if !set("usageMetadata.candidatesTokensDetails", []map[string]any{detail}) {
			return body, nil, false
		}
	}

	if options.PromptTextOnly && !usageMetadata.Get("promptTokensDetails").Exists() {
		promptTokens := usageMetadata.Get("promptTokenCount")
		if promptTokens.Exists() && !set("usageMetadata.promptTokensDetails", []map[string]any{{
			"modality": "TEXT", "tokenCount": promptTokens.Int(),
		}}) {
			return body, nil, false
		}
	}
	if !usageMetadata.Get("serviceTier").Exists() &&
		!usageMetadata.Get("trafficType").Exists() && options.RequestedServiceTier != "" {
		if !set("usageMetadata.serviceTier", options.RequestedServiceTier) {
			return body, nil, false
		}
	}
	parsed := extractGeminiUsage(out)
	if parsed == nil {
		return body, nil, false
	}
	return out, parsed, true
}

type geminiFlashImageStreamState struct {
	imageCount int
}

func (s *geminiFlashImageStreamState) process(payload []byte, model, imageSize string, enabled bool, options geminiFlashUsageRepairOptions) ([]byte, *ClaudeUsage, bool) {
	if s == nil || !enabled || !isGemini31FlashImageModel(model) {
		return payload, nil, false
	}
	s.imageCount += countGeminiInlineOutputImages(payload)
	if s.imageCount == 0 || !gjson.GetBytes(payload, "usageMetadata").Exists() {
		return payload, nil, false
	}
	return repairGemini31FlashImageUsageWithCount(payload, model, imageSize, s.imageCount, options)
}
