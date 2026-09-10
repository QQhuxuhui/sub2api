package service

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestGeminiThinkingConfigForEffort(t *testing.T) {
	cases := []struct {
		effort, model string
		wantKey       string
		want          any
	}{
		// 2.x: budget based
		{"none", "gemini-2.5-flash", "thinkingBudget", 0},
		{"none", "gemini-2.5-flash-lite", "thinkingBudget", 0},
		{"none", "gemini-2.5-pro", "thinkingBudget", 128},
		{"minimal", "gemini-2.5-flash", "thinkingBudget", 512},
		{"low", "gemini-2.5-flash", "thinkingBudget", 1024},
		{"medium", "gemini-2.5-flash", "thinkingBudget", 8192},
		{"high", "gemini-2.5-flash", "thinkingBudget", -1},
		{"xhigh", "gemini-2.5-pro", "thinkingBudget", -1},
		// 3.x: level based
		{"none", "gemini-3-flash", "thinkingLevel", "minimal"},
		{"none", "gemini-3-flash-preview", "thinkingLevel", "minimal"},
		{"none", "gemini-3.5-flash", "thinkingLevel", "minimal"},
		{"none", "gemini-3.6-flash", "thinkingLevel", "minimal"},
		{"none", "gemini-3.7-flash", "thinkingLevel", "low"},
		{"none", "gemini-3.8-flash", "thinkingLevel", "low"},
		{"none", "gemini-3-pro-preview", "thinkingLevel", "low"},
		{"none", "gemini-3.1-pro", "thinkingLevel", "low"},
		{"minimal", "gemini-3.1-pro", "thinkingLevel", "low"},
		{"low", "gemini-3.6-flash", "thinkingLevel", "low"},
		{"medium", "gemini-3-pro-preview", "thinkingLevel", "high"},
		{"medium", "gemini-3.1-pro-preview", "thinkingLevel", "medium"},
		{"medium", "gemini-3.6-flash", "thinkingLevel", "medium"},
		{"high", "gemini-3-flash", "thinkingLevel", "high"},
		{"max", "gemini-3-flash", "thinkingLevel", "high"},
		// prefixes / spellings
		{"NONE", "models/gemini-3-flash", "thinkingLevel", "minimal"},
		{"x-high", "vertex/gemini-2.5-flash", "thinkingBudget", -1},
	}
	for _, tc := range cases {
		got := geminiThinkingConfigForEffort(tc.effort, tc.model)
		if got == nil {
			t.Fatalf("%s/%s: got nil", tc.effort, tc.model)
		}
		if len(got) != 1 {
			t.Fatalf("%s/%s: expected single key, got %v", tc.effort, tc.model, got)
		}
		if got[tc.wantKey] != tc.want {
			t.Fatalf("%s/%s: want %s=%v, got %v", tc.effort, tc.model, tc.wantKey, tc.want, got)
		}
	}
}

func TestGeminiThinkingConfigForEffort_EmptyOrUnknown(t *testing.T) {
	for _, effort := range []string{"", "  ", "bogus", "auto"} {
		if got := geminiThinkingConfigForEffort(effort, "gemini-3-flash"); got != nil {
			t.Fatalf("effort %q: expected nil, got %v", effort, got)
		}
	}
}

func TestGeminiThinkingConfigForEffort_UnsupportedModelsUntouched(t *testing.T) {
	for _, model := range []string{"gemini-2.0-flash", "gemini-pro-latest", "relay-model", "gemini-3rd-party", "gemini-2.5relay", "gemini-3.-flash"} {
		if got := geminiThinkingConfigForEffort("high", model); got != nil {
			t.Fatalf("model %q: expected nil for unsupported or ambiguous model, got %v", model, got)
		}
	}
	if got := geminiThinkingConfigForEffort("none", "gemini-2.5-flash-proxy"); got == nil || got["thinkingBudget"] != 0 {
		t.Fatalf("flash family suffix must not be misclassified as pro, got %v", got)
	}

	geminiReq := []byte(`{"contents":[]}`)
	for _, model := range []string{"gemini-2.0-flash", "relay-model"} {
		got := applyGeminiThinkingConfigFromOpenAIBody(geminiReq, []byte(`{"reasoning_effort":"none"}`), model)
		if string(got) != string(geminiReq) {
			t.Fatalf("model %q: request must remain untouched, got %s", model, got)
		}
	}
}

func TestApplyGeminiThinkingConfigFromOpenAIBody(t *testing.T) {
	geminiReq := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":10}}`)

	// flat reasoning_effort
	out := applyGeminiThinkingConfigFromOpenAIBody(geminiReq, []byte(`{"model":"gemini-3-flash","reasoning_effort":"none"}`), "gemini-3-flash")
	if got := gjson.GetBytes(out, "generationConfig.thinkingConfig.thinkingLevel").String(); got != "minimal" {
		t.Fatalf("expected thinkingLevel=minimal, got %s", string(out))
	}
	if got := gjson.GetBytes(out, "generationConfig.maxOutputTokens").Int(); got != 10 {
		t.Fatalf("existing generationConfig must be preserved, got %s", string(out))
	}

	// nested reasoning.effort wins over flat
	out = applyGeminiThinkingConfigFromOpenAIBody(geminiReq, []byte(`{"reasoning":{"effort":"high"},"reasoning_effort":"none"}`), "gemini-2.5-flash")
	if got := gjson.GetBytes(out, "generationConfig.thinkingConfig.thinkingBudget").Int(); got != -1 {
		t.Fatalf("expected thinkingBudget=-1, got %s", string(out))
	}

	// no generationConfig at all
	out = applyGeminiThinkingConfigFromOpenAIBody([]byte(`{"contents":[]}`), []byte(`{"reasoning_effort":"none"}`), "gemini-2.5-flash")
	if got := gjson.GetBytes(out, "generationConfig.thinkingConfig.thinkingBudget"); !got.Exists() || got.Int() != 0 {
		t.Fatalf("expected thinkingBudget=0, got %s", string(out))
	}

	// no effort: untouched
	out = applyGeminiThinkingConfigFromOpenAIBody(geminiReq, []byte(`{"model":"gemini-3-flash"}`), "gemini-3-flash")
	if string(out) != string(geminiReq) {
		t.Fatalf("expected request untouched, got %s", string(out))
	}
}
