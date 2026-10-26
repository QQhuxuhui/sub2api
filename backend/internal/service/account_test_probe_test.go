//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func sseResponse(status int, body string) *http.Response {
	resp := newJSONResponse(status, "")
	resp.Body = io.NopCloser(strings.NewReader(body))
	return resp
}

func TestRunProbe_OpenAIOAuthSendsQuestionAndRecordsUsage(t *testing.T) {
	resp := sseResponse(http.StatusOK, `data: {"type":"response.output_text.delta","delta":"推理……\n答案："}

data: {"type":"response.output_text.delta","delta":"42"}

data: {"type":"response.completed","response":{"model":"gpt-5.4-mini","usage":{"input_tokens":120,"output_tokens":30,"output_tokens_details":{"reasoning_tokens":12}}}}

`)
	// Codex quota headers must not be written back in probe mode.
	resp.Header.Set("x-codex-primary-used-percent", "88")
	account := &Account{
		ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Concurrency: 1,
		Credentials: map[string]any{"access_token": "tok"},
	}
	repo := &openAIAccountTestRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{7: account}}}
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
	svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream}

	res := svc.RunProbe(context.Background(), 7, "gpt-5.4", AccountProbeRequest{
		Prompt: "6*7=?", System: "只输出数字", ReasoningEffort: "high", MaxTokens: 2048,
	})

	require.True(t, res.Success, res.ErrorMessage)
	require.Equal(t, "推理……\n答案：42", res.ResponseText)
	require.Equal(t, "gpt-5.4", res.UpstreamModel)
	require.Equal(t, "gpt-5.4-mini", res.ResponseModel)
	require.EqualValues(t, 120, res.InputTokens)
	require.EqualValues(t, 30, res.OutputTokens)
	require.EqualValues(t, 12, res.ReasoningTokens)
	require.Nil(t, repo.updatedExtra, "probe must not write codex snapshot")

	require.Len(t, upstream.requests, 1)
	body, _ := io.ReadAll(upstream.requests[0].Body)
	require.Equal(t, "developer", gjson.GetBytes(body, "input.0.role").String())
	require.Equal(t, "只输出数字", gjson.GetBytes(body, "input.0.content.0.text").String())
	require.Equal(t, "6*7=?", gjson.GetBytes(body, "input.1.content.0.text").String())
	require.Equal(t, "high", gjson.GetBytes(body, "reasoning.effort").String())
	require.False(t, gjson.GetBytes(body, "max_output_tokens").Exists(), "codex OAuth rejects max_output_tokens")
}

func TestRunProbe_OpenAIErrorsDoNotTouchAccountState(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests} {
		account := &Account{
			ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Concurrency: 1,
			Credentials: map[string]any{"access_token": "tok"},
		}
		repo := &openAIAccountTestRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{8: account}}}
		resp := newJSONResponse(status, `{"error":{"message":"nope"}}`)
		resp.Header.Set("x-codex-primary-used-percent", "100")
		resp.Header.Set("x-codex-primary-reset-after-seconds", "600")
		svc := &AccountTestService{accountRepo: repo, httpUpstream: &queuedHTTPUpstream{responses: []*http.Response{resp}}}

		res := svc.RunProbe(context.Background(), 8, "gpt-5.4", AccountProbeRequest{Prompt: "q"})

		require.False(t, res.Success)
		require.False(t, res.Unsupported)
		require.Contains(t, res.ErrorMessage, "nope")
		require.Zero(t, repo.setErrorID, "status %d must not SetError", status)
		require.Zero(t, repo.rateLimitedID, "status %d must not rate-limit", status)
		require.Nil(t, repo.updatedExtra)
	}
}

func TestRunProbe_ClaudeAPIKeyKeepsSystemAndAppliesEffort(t *testing.T) {
	account := &Account{
		ID: 9, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk", "base_url": "https://relay.example.com"},
	}
	repo := &openAIAccountTestRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{9: account}}}
	upstream := &queuedHTTPUpstream{responses: []*http.Response{sseResponse(http.StatusOK, `data: {"type":"message_start","message":{"model":"claude-x","usage":{"input_tokens":50,"cache_read_input_tokens":10,"output_tokens":1}}}

data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hmm"}}

data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"答案：B"}}

data: {"type":"message_delta","usage":{"output_tokens":77}}

data: {"type":"message_stop"}

`)}}
	svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream, cfg: &config.Config{}}

	res := svc.RunProbe(context.Background(), 9, "claude-sonnet-4-6", AccountProbeRequest{
		Prompt: "选哪个?", System: "你是考生", ReasoningEffort: "medium",
	})

	require.True(t, res.Success, res.ErrorMessage)
	require.Equal(t, "答案：B", res.ResponseText, "thinking deltas are not part of the answer")
	require.Equal(t, "claude-x", res.ResponseModel)
	require.EqualValues(t, 60, res.InputTokens)
	require.EqualValues(t, 77, res.OutputTokens)

	body, _ := io.ReadAll(upstream.requests[0].Body)
	require.Equal(t, "选哪个?", gjson.GetBytes(body, "messages.0.content.0.text").String())
	require.Equal(t, int64(2), gjson.GetBytes(body, "system.#").Int(), "Claude Code system block kept, probe system appended")
	require.Equal(t, "你是考生", gjson.GetBytes(body, "system.1.text").String())
	require.Equal(t, int64(8192), gjson.GetBytes(body, "max_tokens").Int())
	require.Equal(t, "adaptive", gjson.GetBytes(body, "thinking.type").String())
	require.Equal(t, "medium", gjson.GetBytes(body, "output_config.effort").String())
}

func TestRunProbe_ClaudeForbiddenDoesNotSetError(t *testing.T) {
	account := &Account{
		ID: 10, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk"},
	}
	repo := &openAIAccountTestRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{10: account}}}
	svc := &AccountTestService{
		accountRepo: repo, cfg: &config.Config{},
		httpUpstream: &queuedHTTPUpstream{responses: []*http.Response{newJSONResponse(http.StatusForbidden, "banned")}},
	}

	res := svc.RunProbe(context.Background(), 10, "claude-sonnet-4-6", AccountProbeRequest{Prompt: "q"})
	require.False(t, res.Success)
	require.Zero(t, repo.setErrorID)
}

func TestRunProbe_ImageModelIsUnsupported(t *testing.T) {
	svc := &AccountTestService{}
	res := svc.RunProbe(context.Background(), 1, "gpt-image-2", AccountProbeRequest{Prompt: "q"})
	require.True(t, res.Unsupported)
	require.False(t, res.Success)
}

func TestAccountTestConnection_DefaultPayloadUnchangedWithoutProbe(t *testing.T) {
	ctx, _ := newTestContext()
	upstream := &queuedHTTPUpstream{responses: []*http.Response{sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\"}\n\n")}}
	svc := &AccountTestService{httpUpstream: upstream}
	account := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{"access_token": "t"}}

	require.NoError(t, svc.testOpenAIAccountConnection(ctx, account, "gpt-5.4", "", ""))
	body, _ := io.ReadAll(upstream.requests[0].Body)
	require.Equal(t, "hi", gjson.GetBytes(body, "input.0.content.0.text").String())
	require.False(t, gjson.GetBytes(body, "reasoning").Exists())
}

func TestApplyProbeToChatAndGeminiPayload(t *testing.T) {
	p := &accountProbeContext{req: AccountProbeRequest{Prompt: "q", System: "s", ReasoningEffort: "low", MaxTokens: 100}, trace: &AccountProbeTrace{}}

	chat := createOpenAIChatCompletionsTestPayload("m", "q")
	p.applyToChatPayload(chat)
	require.Equal(t, "low", chat["reasoning_effort"])
	require.Equal(t, 100, chat["max_tokens"])
	require.Len(t, chat["messages"], 2)
	require.Equal(t, map[string]any{"include_usage": true}, chat["stream_options"])

	gem := rewriteJSONPayload(createGeminiTestPayload("gemini-3-pro", "q"), func(m map[string]any) { p.applyToGeminiPayload(m, "") })
	require.Equal(t, "q", gjson.GetBytes(gem, "contents.0.parts.0.text").String())
	require.Equal(t, "s", gjson.GetBytes(gem, "systemInstruction.parts.0.text").String())
	require.Equal(t, "low", gjson.GetBytes(gem, "generationConfig.thinkingConfig.thinkingLevel").String())
	require.Equal(t, int64(100), gjson.GetBytes(gem, "generationConfig.maxOutputTokens").Int())
	require.True(t, p.trace.Applied)

	usage := &accountProbeContext{trace: &AccountProbeTrace{}}
	usage.observe(map[string]any{"response": map[string]any{"modelVersion": "gemini-3-flash", "usageMetadata": map[string]any{
		"promptTokenCount": 9.0, "candidatesTokenCount": 4.0, "thoughtsTokenCount": 2.0,
	}}})
	require.Equal(t, "gemini-3-flash", usage.trace.ResponseModel)
	require.EqualValues(t, 9, usage.trace.InputTokens)
	require.EqualValues(t, 6, usage.trace.OutputTokens)
	require.EqualValues(t, 2, usage.trace.ReasoningTokens)
}

func TestAccountProbeGeminiUsage_StreamingTotals(t *testing.T) {
	p := &accountProbeContext{trace: &AccountProbeTrace{}}
	for _, usage := range []map[string]any{
		{"candidatesTokenCount": 20.0, "thoughtsTokenCount": 5000.0},
		{"candidatesTokenCount": 30.0},
		{"candidatesTokenCount": 40.0, "thoughtsTokenCount": 6000.0},
		{"thoughtsTokenCount": 7000.0},
	} {
		p.observe(map[string]any{"usageMetadata": usage})
	}
	require.EqualValues(t, 7040, p.trace.OutputTokens, "cumulative snapshots must replace, not double count, thoughts")
	require.EqualValues(t, 7000, p.trace.ReasoningTokens)
}

func TestAccountProbeAntigravityClaude_ReasoningEffort(t *testing.T) {
	for _, tc := range []struct {
		effort string
		budget int64
	}{{"", 0}, {"minimal", 1024}, {"low", 1024}, {"medium", 4096}, {"high", 10240}, {"xhigh", 32768}} {
		t.Run(tc.effort, func(t *testing.T) {
			p := &accountProbeContext{req: AccountProbeRequest{Prompt: "q", ReasoningEffort: tc.effort}, trace: &AccountProbeTrace{}}
			req := p.buildAntigravityClaudeRequest("claude-opus-4-6")
			body, err := antigravity.TransformClaudeToGemini(req, "test-project", req.Model)
			require.NoError(t, err)
			thinking := gjson.GetBytes(body, "request.generationConfig.thinkingConfig")
			if tc.effort == "" {
				require.False(t, thinking.Exists())
				return
			}
			require.True(t, thinking.Exists(), "configured effort must reach the wire")
			require.Equal(t, tc.budget, thinking.Get("thinkingBudget").Int())
			require.Greater(t, gjson.GetBytes(body, "request.generationConfig.maxOutputTokens").Int(), tc.budget)
		})
	}
}

func TestAccountProbeAntigravity_LongResponseKeepsFinalAnswer(t *testing.T) {
	// Many small SSE frames exceed the error-body limit before the final answer/usage.
	frame := "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"reasoning \"}]}}]}}\n\n"
	body := strings.Repeat(frame, int(gatewayUpstreamErrorBodyReadLimit)/len(frame)+1) +
		"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"\\n答案：42\"}]}}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":20,\"thoughtsTokenCount\":30}}}\n\n"
	upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{sseResponse(http.StatusOK, body)}}
	svc := &AntigravityGatewayService{tokenProvider: &AntigravityTokenProvider{}, httpUpstream: upstream}
	account := &Account{ID: 12, Platform: PlatformAntigravity, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "test-token", "project_id": "test-project"}}
	trace := &AccountProbeTrace{}
	ctx := withAccountProbe(context.Background(), AccountProbeRequest{Prompt: "q"}, trace)
	res, err := svc.TestConnection(ctx, account, "gemini-3-flash")
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(res.Text, "\n答案：42"))
	require.EqualValues(t, 10, trace.InputTokens)
	require.EqualValues(t, 50, trace.OutputTokens)
}

func TestAccountProbeAntigravity_OversizeResponseIsAnError(t *testing.T) {
	upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{
		sseResponse(http.StatusOK, strings.Repeat("x", accountProbeMaxUpstreamBytes+1)),
	}}
	svc := &AntigravityGatewayService{tokenProvider: &AntigravityTokenProvider{}, httpUpstream: upstream}
	account := &Account{ID: 12, Platform: PlatformAntigravity, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "test-token", "project_id": "test-project"}}
	ctx := withAccountProbe(context.Background(), AccountProbeRequest{Prompt: "q"}, &AccountProbeTrace{})
	res, err := svc.TestConnection(ctx, account, "gemini-3-flash")
	require.ErrorContains(t, err, "probe response exceeds")
	require.Nil(t, res, "a truncated answer must never reach the judge as a success")
}

func TestAccountProbeAntigravity_ThinkingUsesMatchingModel(t *testing.T) {
	upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{sseResponse(http.StatusOK, "")}}
	svc := &AntigravityGatewayService{tokenProvider: &AntigravityTokenProvider{}, httpUpstream: upstream}
	account := &Account{ID: 12, Platform: PlatformAntigravity, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "test-token", "project_id": "test-project",
			"model_mapping": map[string]any{"claude-sonnet-4-5": "claude-sonnet-4-5"}}}
	ctx := withAccountProbe(context.Background(), AccountProbeRequest{Prompt: "q", ReasoningEffort: "high"}, &AccountProbeTrace{})
	res, err := svc.TestConnection(ctx, account, "claude-sonnet-4-5")
	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4-5-thinking", res.MappedModel)
	require.Len(t, upstream.requestBodies, 1)
	require.Equal(t, res.MappedModel, gjson.GetBytes(upstream.requestBodies[0], "model").String())
	require.EqualValues(t, 10240, gjson.GetBytes(upstream.requestBodies[0], "request.generationConfig.thinkingConfig.thinkingBudget").Int())
}
