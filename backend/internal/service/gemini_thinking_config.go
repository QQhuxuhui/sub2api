package service

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// applyGeminiThinkingConfigFromOpenAIBody maps the OpenAI Chat Completions
// reasoning_effort (flat) / reasoning.effort (nested) field onto Gemini
// generationConfig.thinkingConfig. Without this the effort is silently dropped
// on the Chat Completions → Claude → Gemini conversion chain, so clients that
// send reasoning_effort:"none" still pay for Gemini's default dynamic thinking.
//
// When the client does not send an effort the request is returned untouched so
// upstream defaults are preserved.
func applyGeminiThinkingConfigFromOpenAIBody(geminiReq []byte, openAIBody []byte, mappedModel string) []byte {
	raw := strings.TrimSpace(gjson.GetBytes(openAIBody, "reasoning.effort").String())
	if raw == "" {
		raw = strings.TrimSpace(gjson.GetBytes(openAIBody, "reasoning_effort").String())
	}
	cfg := geminiThinkingConfigForEffort(raw, mappedModel)
	if cfg == nil {
		return geminiReq
	}
	out, err := sjson.SetBytes(geminiReq, "generationConfig.thinkingConfig", cfg)
	if err != nil {
		return geminiReq
	}
	return out
}

// geminiThinkingConfigForEffort converts an OpenAI reasoning effort level into
// a Gemini thinkingConfig object for the given upstream model. It returns nil
// for empty / unrecognized efforts.
//
// Gemini 2.x models only understand thinkingBudget (0 disables on flash /
// flash-lite; pro cannot go below 128; -1 means dynamic). Gemini 3.x models use
// thinkingLevel and cannot be fully disabled: the floor is "minimal" where
// supported (3.0 / 3.5 / 3.6 flash families) and "low" elsewhere (all pro
// models, 3.7+ flash). Sending an unsupported level yields an upstream 400, so
// the fallbacks below stay on the documented set for each family.
func geminiThinkingConfigForEffort(effort, model string) map[string]any {
	level := normalizeGeminiReasoningEffort(effort)
	if level == "" {
		return nil
	}

	name := geminiModelBaseName(model)
	major, minor, versioned := geminiModelVersion(name)
	isPro := strings.Contains(name, "pro")

	if !versioned || major < 3 {
		return map[string]any{"thinkingBudget": geminiThinkingBudgetForEffort(level, isPro)}
	}

	supportsMinimal := !isPro && major == 3 && minor < 7
	supportsMedium := !(isPro && major == 3 && minor == 0)

	var thinkingLevel string
	switch level {
	case "none", "minimal":
		if supportsMinimal {
			thinkingLevel = "minimal"
		} else {
			thinkingLevel = "low"
		}
	case "low":
		thinkingLevel = "low"
	case "medium":
		if supportsMedium {
			thinkingLevel = "medium"
		} else {
			thinkingLevel = "high"
		}
	default: // high
		thinkingLevel = "high"
	}
	return map[string]any{"thinkingLevel": thinkingLevel}
}

func geminiThinkingBudgetForEffort(level string, isPro bool) int {
	switch level {
	case "none":
		if isPro {
			return 128 // gemini-2.5-pro floor; thinking cannot be disabled
		}
		return 0
	case "minimal":
		if isPro {
			return 128
		}
		return 512 // flash-lite floor is 512; flash accepts anything >= 0
	case "low":
		return 1024
	case "medium":
		return 8192
	default: // high
		return -1 // dynamic
	}
}

// normalizeGeminiReasoningEffort collapses OpenAI effort spellings into
// none | minimal | low | medium | high. Unlike normalizeOpenAIReasoningEffort
// (used for billing labels) it keeps none/minimal because they are the whole
// point for Gemini thinking control.
func normalizeGeminiReasoningEffort(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	value = strings.NewReplacer("-", "", "_", "", " ", "").Replace(value)
	switch value {
	case "none", "minimal", "low", "medium", "high":
		return value
	case "xhigh", "extrahigh", "max":
		return "high"
	default:
		return ""
	}
}

// geminiModelBaseName strips any "models/" or vendor prefix and lowercases.
func geminiModelBaseName(model string) string {
	name := strings.ToLower(strings.TrimSpace(model))
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	return name
}

// geminiModelVersion parses "gemini-<major>[.<minor>]-..." into numbers.
// ok is false when the name does not start with a gemini version.
func geminiModelVersion(name string) (major, minor int, ok bool) {
	const prefix = "gemini-"
	if !strings.HasPrefix(name, prefix) {
		return 0, 0, false
	}
	rest := name[len(prefix):]
	end := 0
	for end < len(rest) && (rest[end] >= '0' && rest[end] <= '9' || rest[end] == '.') {
		end++
	}
	ver := rest[:end]
	if ver == "" {
		return 0, 0, false
	}
	parts := strings.SplitN(ver, ".", 2)
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	if len(parts) == 2 && parts[1] != "" {
		minor, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, false
		}
	}
	return major, minor, true
}
