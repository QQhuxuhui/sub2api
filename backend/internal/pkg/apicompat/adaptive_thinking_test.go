package apicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func responsesRequestForThinkingTest(t *testing.T, model, effort string) *ResponsesRequest {
	t.Helper()
	maxTokens := 4096
	req := &ResponsesRequest{
		Model:           model,
		Instructions:    "be brief",
		Input:           json.RawMessage(`[{"role":"user","content":"hi"},{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"},{"type":"function_call_output","call_id":"call_1","output":"ok"}]`),
		MaxOutputTokens: &maxTokens,
		Tools:           []ResponsesTool{{Type: "function", Name: "lookup", Description: "look up", Parameters: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)}},
	}
	if effort != "" {
		req.Reasoning = &ResponsesReasoning{Effort: effort}
	}
	return req
}

func convertForThinkingTest(t *testing.T, model, effort string) (before, after []byte) {
	t.Helper()
	converted, err := ResponsesToAnthropicRequest(responsesRequestForThinkingTest(t, model, effort))
	require.NoError(t, err)
	before, err = json.Marshal(converted)
	require.NoError(t, err)
	UseAdaptiveThinkingWhereBudgetRemoved(converted)
	after, err = json.Marshal(converted)
	require.NoError(t, err)
	return before, after
}

var thinkingTestEfforts = []string{"", "low", "medium", "high", "xhigh", "max", "none", "minimal", "something-new"}

// Models that reject budget_tokens get adaptive thinking; nothing else in the
// request moves.
func TestUseAdaptiveThinkingWhereBudgetRemoved_TargetModels(t *testing.T) {
	for _, model := range []string{
		"claude-fable-5", "claude-fable-5-1", "claude-fable-5-2", "claude-mythos-5-2", "claude-mythos-preview",
		"claude-opus-5", "claude-opus-4-8", "claude-opus-4-7", "claude-sonnet-5",
		"claude-opus-4-7-20260115", "claude-sonnet-5@20260301", "us.anthropic.claude-opus-4-8-v1:0",
	} {
		for _, effort := range thinkingTestEfforts {
			t.Run(model+"/"+effort, func(t *testing.T) {
				before, after := convertForThinkingTest(t, model, effort)
				if effort == "" || effort == "low" {
					require.NotContains(t, string(before), `"thinking"`, "no manual thinking was generated")
					require.Equal(t, string(before), string(after), "low and absent effort stay byte for byte")
					return
				}
				require.Contains(t, string(before), `"thinking":{"type":"enabled","budget_tokens":`)
				require.Contains(t, string(after), `"thinking":{"type":"adaptive"}`)
				require.NotContains(t, string(after), "budget_tokens")

				var beforeMap, afterMap map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(before, &beforeMap))
				require.NoError(t, json.Unmarshal(after, &afterMap))
				delete(beforeMap, "thinking")
				delete(afterMap, "thinking")
				require.Equal(t, beforeMap, afterMap, "only thinking changes")

				wantEffort := effort
				if effort == "xhigh" {
					wantEffort = "max" // the existing mapping is kept
				}
				require.JSONEq(t, `{"effort":"`+wantEffort+`"}`, string(afterMap["output_config"]))
			})
		}
	}
}

// Every other model keeps its converted request byte for byte.
func TestUseAdaptiveThinkingWhereBudgetRemoved_OtherModelsUnchanged(t *testing.T) {
	for _, model := range []string{
		"claude-opus-4-6", "claude-sonnet-4-6", "claude-opus-4-5-20251101", "claude-sonnet-4-5-20250929", "claude-haiku-4-5-20251001",
		"claude-opus-4-6@20260205", "gemini-3.1-pro", "gpt-6.1-sol", "kimi-k3.1-pro", "my-claude-fable-5x", "claude-opus-4-70",
	} {
		for _, effort := range thinkingTestEfforts {
			before, after := convertForThinkingTest(t, model, effort)
			require.Equal(t, string(before), string(after), "%s effort=%q", model, effort)
			if effort != "" && effort != "low" {
				require.Contains(t, string(after), `"thinking":{"type":"enabled","budget_tokens":`, "%s effort=%q keeps the budget", model, effort)
			}
		}
	}
}

// The 5.5 models are already adaptive after conversion: nothing to rewrite.
func TestUseAdaptiveThinkingWhereBudgetRemoved_55ModelsUnchanged(t *testing.T) {
	for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"} {
		for _, effort := range []string{"", "low", "medium", "high", "xhigh", "max"} {
			before, after := convertForThinkingTest(t, model, effort)
			require.Equal(t, string(before), string(after), "%s effort=%q", model, effort)
			require.NotContains(t, string(after), "budget_tokens")
		}
	}
}

func TestUseAdaptiveThinkingWhereBudgetRemoved_LeavesOtherThinkingModesAlone(t *testing.T) {
	UseAdaptiveThinkingWhereBudgetRemoved(nil)

	for _, thinking := range []*AnthropicThinking{nil, {Type: "adaptive"}, {Type: "disabled"}, {Type: "between_tools"}} {
		req := &AnthropicRequest{Model: "claude-fable-5-2", Thinking: thinking}
		UseAdaptiveThinkingWhereBudgetRemoved(req)
		require.Same(t, thinking, req.Thinking)
	}

	original := &AnthropicThinking{Type: "enabled", BudgetTokens: 4096}
	req := &AnthropicRequest{Model: "claude-fable-5-2", Thinking: original}
	UseAdaptiveThinkingWhereBudgetRemoved(req)
	require.Equal(t, &AnthropicThinking{Type: "adaptive"}, req.Thinking)
	require.Equal(t, 4096, original.BudgetTokens, "the caller's object is not mutated")
}

// The shared converter is untouched: paths that do not call the rewrite
// (Antigravity, Gemini compatibility) still get the budget they derive tiers from.
func TestResponsesToAnthropicRequest_StillEmitsBudgetForTargetModels(t *testing.T) {
	for _, model := range []string{"claude-fable-5-2", "claude-opus-5", "claude-opus-4-8", "claude-sonnet-5"} {
		converted, err := ResponsesToAnthropicRequest(responsesRequestForThinkingTest(t, model, "high"))
		require.NoError(t, err)
		require.Equal(t, &AnthropicThinking{Type: "enabled", BudgetTokens: 10240}, converted.Thinking, model)
	}
}

// Chat Completions → Responses → Anthropic, the chain the Cursor group uses.
func TestChatCompletionsChain_FableHighEffortBecomesAdaptive(t *testing.T) {
	var ccReq ChatCompletionsRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"claude-fable-5-2","stream":true,"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`), &ccReq))
	responsesReq, err := ChatCompletionsToResponses(&ccReq)
	require.NoError(t, err)
	anthropicReq, err := ResponsesToAnthropicRequest(responsesReq)
	require.NoError(t, err)
	UseAdaptiveThinkingWhereBudgetRemoved(anthropicReq)
	body, err := json.Marshal(anthropicReq)
	require.NoError(t, err)
	require.Contains(t, string(body), `"thinking":{"type":"adaptive"}`)
	require.Contains(t, string(body), `"output_config":{"effort":"high"}`)
	require.False(t, strings.Contains(string(body), "budget_tokens"))
}
