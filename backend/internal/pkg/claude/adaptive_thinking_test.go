package claude

import "testing"

func TestRequiresAdaptiveThinking(t *testing.T) {
	adaptiveOnly := []string{
		"claude-fable-5", "claude-fable-5-1", "claude-fable-5-2",
		"claude-mythos-5", "claude-mythos-5-1", "claude-mythos-5-2", "claude-mythos-preview",
		"claude-opus-5", "claude-opus-4-8", "claude-opus-4-7", "claude-sonnet-5",
		// snapshot dates, as written on the Claude API and on Vertex
		"claude-opus-4-7-20260115", "claude-fable-5-1-20261001", "claude-sonnet-5@20260301", "claude-fable-5-2@20261020",
		// provider / region / local decorations
		"Claude-Fable-5-2", " claude-opus-5 ", "models/claude-opus-5", "anthropic/claude-sonnet-5",
		"us.anthropic.claude-opus-4-8-v1:0", "global.anthropic.claude-fable-5-v1:0", "anthropic.claude-opus-4-7",
		"claude-opus-4-8-thinking",
	}
	for _, model := range adaptiveOnly {
		if !RequiresAdaptiveThinking(model) {
			t.Errorf("RequiresAdaptiveThinking(%q) = false, want true", model)
		}
	}

	keepsBudget := []string{
		// the 5.5 models have their own conversion branch
		"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5",
		"claude-opus-5-5-20260401", "claude-sonnet-5-5@20260401", "claude-opus-5.5", "us.anthropic.claude-sonnet-5-5-v1:0",
		// budget_tokens is still accepted, or required
		"claude-opus-4-6", "claude-sonnet-4-6", "claude-opus-4-6-20260205", "claude-sonnet-4-6@20260205",
		"claude-opus-4-5", "claude-opus-4-5-20251101", "claude-sonnet-4-5-20250929", "claude-haiku-4-5", "claude-haiku-4-5-20251001",
		"claude-opus-4-1-20250805", "claude-3-5-sonnet-20241022",
		// look-alikes and other providers
		"my-claude-fable-5x", "claude-fable-50", "claude-opus-50", "claude-sonnet-55", "claude-opus-4-70", "xclaude-opus-5",
		"claude-sonnet-5@2026", "claude-sonnet-5@abcdefgh", "claude-opus-5@20260301x",
		"gemini-3.1-pro", "gpt-6.1-sol", "kimi-k3.1-pro", "deepseek-reasoner", "glm-5", "",
	}
	for _, model := range keepsBudget {
		if RequiresAdaptiveThinking(model) {
			t.Errorf("RequiresAdaptiveThinking(%q) = true, want false", model)
		}
	}
}
