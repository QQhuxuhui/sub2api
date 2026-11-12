package apicompat

import "github.com/Wei-Shaw/sub2api/internal/pkg/claude"

// UseAdaptiveThinkingWhereBudgetRemoved rewrites a converted request's manual
// thinking budget into adaptive thinking for models that reject budget_tokens.
// Requests for other models, and requests without manual thinking, are left
// untouched. Call it only on paths whose upstream speaks the Anthropic Messages
// protocol: other consumers of the converted request read the budget.
func UseAdaptiveThinkingWhereBudgetRemoved(req *AnthropicRequest) {
	if req == nil || req.Thinking == nil || req.Thinking.Type != "enabled" {
		return
	}
	if !claude.RequiresAdaptiveThinking(req.Model) {
		return
	}
	req.Thinking = &AnthropicThinking{Type: "adaptive"}
}
