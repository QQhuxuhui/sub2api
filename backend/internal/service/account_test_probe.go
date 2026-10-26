package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/gin-gonic/gin"
)

// Successful probe streams can be much larger than connectivity-test replies.
// Exceeding this bound is a request error, never a partially scored answer.
const accountProbeMaxUpstreamBytes = 16 << 20

// AccountProbeRequest replaces the fixed "hi" connectivity prompt with a real
// question for the intelligence probe. Probe runs are observe-only: they never
// change account state (SetError / rate-limit reconcile / extra write-back /
// temp-unschedulable), because a hard question tripping a 400/429 must not
// affect live scheduling.
type AccountProbeRequest struct {
	Prompt          string
	System          string
	ReasoningEffort string // "" = upstream default; low|medium|high|xhigh
	MaxTokens       int    // 0 = path default
}

// AccountProbeTrace collects what the stream processors observed for one probe
// request. Applied stays false when the routed test path cannot carry a custom
// prompt (image / media / compact modes); the runner treats that as unsupported
// instead of scoring the canned "hi" reply.
type AccountProbeTrace struct {
	mu              sync.Mutex
	Applied         bool
	ResponseModel   string
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	firstContentAt  time.Time
}

type accountProbeContextKey struct{}

type accountProbeContext struct {
	req   AccountProbeRequest
	trace *AccountProbeTrace
}

func withAccountProbe(ctx context.Context, req AccountProbeRequest, trace *AccountProbeTrace) context.Context {
	return context.WithValue(ctx, accountProbeContextKey{}, &accountProbeContext{req: req, trace: trace})
}

func accountProbeFrom(ctx context.Context) *accountProbeContext {
	if ctx == nil {
		return nil
	}
	p, _ := ctx.Value(accountProbeContextKey{}).(*accountProbeContext)
	return p
}

// accountTestObserveOnly reports whether account-state side effects must be skipped.
func accountTestObserveOnly(ctx context.Context) bool {
	return accountProbeFrom(ctx) != nil
}

func (p *accountProbeContext) markApplied() {
	if p == nil || p.trace == nil {
		return
	}
	p.trace.mu.Lock()
	p.trace.Applied = true
	p.trace.mu.Unlock()
}

func (p *accountProbeContext) noteContent() {
	if p == nil || p.trace == nil {
		return
	}
	p.trace.mu.Lock()
	if p.trace.firstContentAt.IsZero() {
		p.trace.firstContentAt = time.Now()
	}
	p.trace.mu.Unlock()
}

// observe pulls model / usage out of any provider's streaming or JSON payload.
// Later values win, so a final usage block overrides partial ones.
func (p *accountProbeContext) observe(data map[string]any) {
	if p == nil || p.trace == nil || data == nil {
		return
	}
	t := p.trace
	t.mu.Lock()
	defer t.mu.Unlock()
	setModel := func(v any) {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			t.ResponseModel = s
		}
	}
	num := func(m map[string]any, key string) (int64, bool) {
		f, ok := m[key].(float64)
		return int64(f), ok
	}
	readUsage := func(u map[string]any) {
		if u == nil {
			return
		}
		// Anthropic: input + cache fields; OpenAI Responses: input_tokens.
		if v, ok := num(u, "input_tokens"); ok {
			cc, _ := num(u, "cache_creation_input_tokens")
			cr, _ := num(u, "cache_read_input_tokens")
			t.InputTokens = v + cc + cr
		}
		if v, ok := num(u, "prompt_tokens"); ok {
			t.InputTokens = v
		}
		if v, ok := num(u, "output_tokens"); ok && v > 0 {
			t.OutputTokens = v
		}
		if v, ok := num(u, "completion_tokens"); ok {
			t.OutputTokens = v
		}
		for _, key := range []string{"output_tokens_details", "completion_tokens_details"} {
			if d, ok := u[key].(map[string]any); ok {
				if v, ok := num(d, "reasoning_tokens"); ok {
					t.ReasoningTokens = v
				}
			}
		}
		// Gemini usageMetadata
		if v, ok := num(u, "promptTokenCount"); ok {
			t.InputTokens = v
		}
		candidates, hasCandidates := num(u, "candidatesTokenCount")
		thoughts, hasThoughts := num(u, "thoughtsTokenCount")
		if hasCandidates || hasThoughts {
			if !hasCandidates {
				candidates = t.OutputTokens - t.ReasoningTokens
			}
			if hasThoughts {
				t.ReasoningTokens = thoughts
			}
			// Gemini bills thoughts as output; snapshots are cumulative.
			t.OutputTokens = candidates + t.ReasoningTokens
		}
	}

	if inner, ok := data["response"].(map[string]any); ok {
		// OpenAI Responses (response.completed) or Gemini CLI envelope.
		setModel(inner["model"])
		setModel(inner["modelVersion"])
		if u, ok := inner["usage"].(map[string]any); ok {
			readUsage(u)
		}
		if u, ok := inner["usageMetadata"].(map[string]any); ok {
			readUsage(u)
		}
	}
	if msg, ok := data["message"].(map[string]any); ok {
		// Anthropic message_start
		setModel(msg["model"])
		if u, ok := msg["usage"].(map[string]any); ok {
			readUsage(u)
		}
	}
	setModel(data["model"])
	setModel(data["modelVersion"])
	if u, ok := data["usage"].(map[string]any); ok {
		readUsage(u)
	}
	if u, ok := data["usageMetadata"].(map[string]any); ok {
		readUsage(u)
	}
}

// observeProbeJSON feeds a raw SSE/JSON body (non-streaming paths) to the trace.
func (p *accountProbeContext) observeBody(body []byte) {
	if p == nil {
		return
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(sseDataPrefix.ReplaceAllString(strings.TrimSpace(line), ""))
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var data map[string]any
		if json.Unmarshal([]byte(line), &data) == nil {
			p.observe(data)
		}
	}
}

func (s *AccountTestService) observeProbe(c *gin.Context, data map[string]any) {
	if c == nil || c.Request == nil {
		return
	}
	accountProbeFrom(c.Request.Context()).observe(data)
}

func (p *accountProbeContext) maxTokens(def int) int {
	if p.req.MaxTokens > 0 {
		return p.req.MaxTokens
	}
	return def
}

// applyToAnthropicPayload rewrites a createTestPayload / Bedrock map in place.
// The Claude Code system block (OAuth requirement) is kept; the probe's own
// system prompt is appended after it.
func (p *accountProbeContext) applyToAnthropicPayload(payload map[string]any) {
	if p == nil {
		return
	}
	payload["messages"] = []map[string]any{{
		"role":    "user",
		"content": []map[string]any{{"type": "text", "text": p.req.Prompt}},
	}}
	if sys := strings.TrimSpace(p.req.System); sys != "" {
		blocks, _ := payload["system"].([]map[string]any)
		payload["system"] = append(blocks, map[string]any{"type": "text", "text": sys})
	}
	payload["max_tokens"] = p.maxTokens(8192)
	if effort := strings.TrimSpace(p.req.ReasoningEffort); effort != "" {
		payload["thinking"] = map[string]any{"type": "adaptive"}
		payload["output_config"] = map[string]any{"effort": effort}
	}
	p.markApplied()
}

// applyToResponsesPayload rewrites a createOpenAITestPayload map in place.
// Codex OAuth rejects max_output_tokens, so it is only sent to API-key upstreams.
func (p *accountProbeContext) applyToResponsesPayload(payload map[string]any, isOAuth bool) {
	if p == nil {
		return
	}
	input := make([]map[string]any, 0, 2)
	if sys := strings.TrimSpace(p.req.System); sys != "" {
		input = append(input, map[string]any{
			"role":    "developer",
			"content": []map[string]any{{"type": "input_text", "text": sys}},
		})
	}
	input = append(input, map[string]any{
		"role":    "user",
		"content": []map[string]any{{"type": "input_text", "text": p.req.Prompt}},
	})
	payload["input"] = input
	if effort := strings.TrimSpace(p.req.ReasoningEffort); effort != "" {
		payload["reasoning"] = map[string]any{"effort": effort}
	}
	if !isOAuth && p.req.MaxTokens > 0 {
		payload["max_output_tokens"] = p.req.MaxTokens
	}
	p.markApplied()
}

func (p *accountProbeContext) applyToChatPayload(payload map[string]any) {
	if p == nil {
		return
	}
	messages := make([]map[string]any, 0, 2)
	if sys := strings.TrimSpace(p.req.System); sys != "" {
		messages = append(messages, map[string]any{"role": "system", "content": sys})
	}
	messages = append(messages, map[string]any{"role": "user", "content": p.req.Prompt})
	payload["messages"] = messages
	payload["stream_options"] = map[string]any{"include_usage": true}
	if effort := strings.TrimSpace(p.req.ReasoningEffort); effort != "" {
		payload["reasoning_effort"] = effort
	}
	if p.req.MaxTokens > 0 {
		payload["max_tokens"] = p.req.MaxTokens
	}
	p.markApplied()
}

// geminiThinkingLevel maps OpenAI-style effort to Gemini 3 thinkingLevel.
func geminiThinkingLevel(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal", "low":
		return "low"
	case "":
		return ""
	default:
		return "high"
	}
}

func (p *accountProbeContext) applyToGeminiPayload(payload map[string]any, identity string) {
	if p == nil {
		return
	}
	payload["contents"] = []map[string]any{{
		"role":  "user",
		"parts": []map[string]any{{"text": p.req.Prompt}},
	}}
	parts := make([]map[string]any, 0, 2)
	if identity != "" {
		parts = append(parts, map[string]any{"text": identity})
	}
	if sys := strings.TrimSpace(p.req.System); sys != "" {
		parts = append(parts, map[string]any{"text": sys})
	}
	if len(parts) > 0 {
		payload["systemInstruction"] = map[string]any{"parts": parts}
	} else {
		delete(payload, "systemInstruction")
	}
	gen := map[string]any{}
	if p.req.MaxTokens > 0 {
		gen["maxOutputTokens"] = p.req.MaxTokens
	}
	if level := geminiThinkingLevel(p.req.ReasoningEffort); level != "" {
		gen["thinkingConfig"] = map[string]any{"thinkingLevel": level}
	}
	if len(gen) > 0 {
		payload["generationConfig"] = gen
	} else {
		delete(payload, "generationConfig")
	}
	p.markApplied()
}

// rewriteJSONPayload decodes, applies fn and re-encodes a JSON body.
func rewriteJSONPayload(body []byte, fn func(map[string]any)) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	fn(payload)
	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}

// buildAntigravityProbeClaudeRequest builds the Antigravity OAuth Claude-format probe.
func (p *accountProbeContext) buildAntigravityClaudeRequest(mappedModel string) *antigravity.ClaudeRequest {
	prompt, _ := json.Marshal(p.req.Prompt)
	req := &antigravity.ClaudeRequest{
		Model:     mappedModel,
		Messages:  []antigravity.ClaudeMessage{{Role: "user", Content: prompt}},
		MaxTokens: p.maxTokens(8192),
	}
	if sys := strings.TrimSpace(p.req.System); sys != "" {
		req.System, _ = json.Marshal(sys)
	}
	// Antigravity Claude exposes a token budget instead of output_config.effort.
	// Keep the budgets aligned with the Responses-to-Anthropic bridge.
	budget := 0
	switch strings.ToLower(strings.TrimSpace(p.req.ReasoningEffort)) {
	case "minimal", "low":
		budget = 1024
	case "medium":
		budget = 4096
	case "high":
		budget = 10240
	case "xhigh":
		budget = 32768
	}
	if budget > 0 {
		req.Thinking = &antigravity.ThinkingConfig{Type: "enabled", BudgetTokens: budget}
	}
	p.markApplied()
	return req
}

// AccountProbeResult is the outcome of one probe question against one account.
type AccountProbeResult struct {
	Success         bool
	ResponseText    string
	ErrorMessage    string
	Unsupported     bool
	RequestedModel  string
	UpstreamModel   string
	ResponseModel   string
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	LatencyMs       int64
	FirstTokenMs    int64
	StartedAt       time.Time
	FinishedAt      time.Time
}

// ErrAccountProbeUnsupported is returned when the selected model routes to a
// test path that cannot carry a custom prompt (image / media models).
var ErrAccountProbeUnsupported = errors.New("该账号/模型的测试路径不支持自定义题目")

// RunProbe sends one probe question to the account through the admin test
// path in observe-only mode and returns the full answer plus usage.
func (s *AccountTestService) RunProbe(ctx context.Context, accountID int64, modelID string, req AccountProbeRequest) *AccountProbeResult {
	trace := &AccountProbeTrace{}
	startedAt := time.Now()

	w := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(w)
	ginCtx.Request = (&http.Request{}).WithContext(withAccountProbe(ctx, req, trace))

	var testErr error
	if isOpenAIImageModel(modelID) || isImageGenerationModel(modelID) || isGrokVideoGenerationModel(modelID) {
		testErr = ErrAccountProbeUnsupported
	} else {
		testErr = s.TestAccountConnection(ginCtx, accountID, modelID, req.Prompt, AccountTestModeDefault)
	}

	finishedAt := time.Now()
	responseText, errMsg, upstreamModel := parseProbeSSEOutput(w.Body.String())

	trace.mu.Lock()
	defer trace.mu.Unlock()
	result := &AccountProbeResult{
		ResponseText:    responseText,
		RequestedModel:  modelID,
		UpstreamModel:   upstreamModel,
		ResponseModel:   trace.ResponseModel,
		InputTokens:     trace.InputTokens,
		OutputTokens:    trace.OutputTokens,
		ReasoningTokens: trace.ReasoningTokens,
		LatencyMs:       finishedAt.Sub(startedAt).Milliseconds(),
		StartedAt:       startedAt,
		FinishedAt:      finishedAt,
	}
	if !trace.firstContentAt.IsZero() {
		result.FirstTokenMs = trace.firstContentAt.Sub(startedAt).Milliseconds()
	}
	switch {
	case errors.Is(testErr, ErrAccountProbeUnsupported):
		result.Unsupported = true
		result.ErrorMessage = testErr.Error()
	case testErr != nil || errMsg != "":
		if errMsg == "" {
			errMsg = testErr.Error()
		}
		result.ErrorMessage = errMsg
	case !trace.Applied:
		result.Unsupported = true
		result.ErrorMessage = ErrAccountProbeUnsupported.Error()
	default:
		result.Success = true
	}
	return result
}

// parseProbeSSEOutput is parseTestSSEOutput plus the model announced in test_start
// (the post-mapping model actually sent upstream).
func parseProbeSSEOutput(body string) (responseText, errMsg, model string) {
	responseText, errMsg = parseTestSSEOutput(body)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event TestEvent
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) == nil && event.Type == "test_start" && event.Model != "" {
			model = event.Model
		}
	}
	return
}
