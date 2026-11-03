package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

const geminiEvidenceTestRequestBody = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`

func newGeminiEvidenceTestService(contentType, body string) *GeminiMessagesCompatService {
	return &GeminiMessagesCompatService{
		httpUpstream: &geminiCompatHTTPUpstreamStub{
			response: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{contentType}},
				Body:       io.NopCloser(strings.NewReader(body)),
			},
		},
		cfg: &config.Config{},
	}
}

func geminiEvidenceTestAccount() *Account {
	return &Account{
		ID:          1172,
		Platform:    PlatformGemini,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test-key"},
	}
}

// newGeminiEvidenceTestContext 返回带观测 logger 的 gin context（模拟中间件放入 request context 的 logger）。
func newGeminiEvidenceTestContext(t *testing.T, action string) (*gin.Context, *httptest.ResponseRecorder, *observer.ObservedLogs) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	core, logs := observer.New(zapcore.DebugLevel)
	l := zap.New(core).With(zap.String("client_request_id", "cid-1"))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.8-flash:"+action, strings.NewReader("{}"))
	c.Request = req.WithContext(logger.IntoContext(req.Context(), l))
	return c, rec, logs
}

func evidenceLogs(logs *observer.ObservedLogs) []observer.LoggedEntry {
	return logs.FilterMessage("gemini.usage_evidence").All()
}

func TestGeminiUsageEvidence_NonStreamMissingUsageLogsWarn(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"text":"secret answer"}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.8-flash","responseId":"r1"}`
	svc := newGeminiEvidenceTestService("application/json", body)
	c, rec, logs := newGeminiEvidenceTestContext(t, "generateContent")

	result, err := svc.ForwardNative(context.Background(), c, geminiEvidenceTestAccount(), "gemini-3.8-flash", "generateContent", false, []byte(geminiEvidenceTestRequestBody))
	require.NoError(t, err)
	require.Equal(t, ClaudeUsage{}, result.Usage, "计费用量保持原行为（0）")
	require.Equal(t, body, rec.Body.String(), "写给客户端的字节不变")

	entries := evidenceLogs(logs)
	require.Len(t, entries, 1)
	e := entries[0]
	require.Equal(t, zapcore.WarnLevel, e.Level)
	f := e.ContextMap()
	require.Equal(t, geminiUsageEvidenceReasonMissing, f["reason"])
	require.Equal(t, geminiUsageEvidenceExitNonStream, f["exit"])
	require.Equal(t, "cid-1", f["client_request_id"])
	require.Equal(t, int64(1172), f["account_id"])
	require.Equal(t, "STOP", f["finish_reason"])
	require.Equal(t, "r1", f["response_id"])
	require.Equal(t, int64(len(body)), f["bytes_read"])
	require.Equal(t, true, f["billed_usage_zero"])
	tail, ok := f["tail_samples"].([]geminiUsageEvidenceSampleOut)
	require.True(t, ok)
	require.Len(t, tail, 1)
	require.Contains(t, tail[0].Sample, "<text len=13>")
	require.NotContains(t, tail[0].Sample, "secret answer", "文本内容必须脱敏")
}

func TestGeminiUsageEvidence_NonStreamZeroUsageLogsZeroReason(t *testing.T) {
	body := `{"candidates":[{"finishReason":"SAFETY"}],"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":0}}`
	svc := newGeminiEvidenceTestService("application/json", body)
	c, _, logs := newGeminiEvidenceTestContext(t, "generateContent")

	_, err := svc.ForwardNative(context.Background(), c, geminiEvidenceTestAccount(), "gemini-3.8-flash", "generateContent", false, []byte(geminiEvidenceTestRequestBody))
	require.NoError(t, err)
	entries := evidenceLogs(logs)
	require.Len(t, entries, 1)
	require.Equal(t, geminiUsageEvidenceReasonZero, entries[0].ContextMap()["reason"])
	require.Equal(t, true, entries[0].ContextMap()["usage_seen"])
}

func TestGeminiUsageEvidence_NonStreamNormalUsageNoLog(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12}}`
	svc := newGeminiEvidenceTestService("application/json", body)
	c, _, logs := newGeminiEvidenceTestContext(t, "generateContent")

	result, err := svc.ForwardNative(context.Background(), c, geminiEvidenceTestAccount(), "gemini-3.8-flash", "generateContent", false, []byte(geminiEvidenceTestRequestBody))
	require.NoError(t, err)
	require.Equal(t, 10, result.Usage.InputTokens)
	require.Empty(t, evidenceLogs(logs))
}

// 非流式出口拿到 SSE 文本：无法当对象取 usage → missing，前缀样本能看出格式。
func TestGeminiUsageEvidence_NonStreamSSEBodyKeepsPrefixSample(t *testing.T) {
	body := "data: {\"candidates\":[]}\n\n"
	svc := newGeminiEvidenceTestService("text/event-stream", body)
	c, _, logs := newGeminiEvidenceTestContext(t, "generateContent")

	_, err := svc.ForwardNative(context.Background(), c, geminiEvidenceTestAccount(), "gemini-3.8-flash", "generateContent", false, []byte(geminiEvidenceTestRequestBody))
	require.NoError(t, err)
	entries := evidenceLogs(logs)
	require.Len(t, entries, 1)
	f := entries[0].ContextMap()
	require.Equal(t, int64(1), f["parse_failures"])
	require.Equal(t, "text/event-stream", f["content_type"])
	samples, _ := json.Marshal(f["tail_samples"])
	require.Contains(t, string(samples), "data: ")
}

func TestGeminiUsageEvidence_StreamMissingUsageCountsLines(t *testing.T) {
	stream := strings.Join([]string{
		`: keepalive`,
		`data: {"candidates":[{"content":{"parts":[{"text":"a"}]}}]}`,
		``,
		`{"error":{"code":500,"status":"INTERNAL"}}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"b"}]},"finishReason":"STOP"}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	svc := newGeminiEvidenceTestService("text/event-stream", stream)
	c, rec, logs := newGeminiEvidenceTestContext(t, "streamGenerateContent")

	result, err := svc.ForwardNative(context.Background(), c, geminiEvidenceTestAccount(), "gemini-3.8-flash", "streamGenerateContent", true, []byte(geminiEvidenceTestRequestBody))
	require.NoError(t, err)
	require.Equal(t, ClaudeUsage{}, result.Usage)
	require.Contains(t, rec.Body.String(), `{"error":{"code":500,"status":"INTERNAL"}}`, "非 data 行仍原样透传")

	entries := evidenceLogs(logs)
	require.Len(t, entries, 1)
	f := entries[0].ContextMap()
	require.Equal(t, geminiUsageEvidenceReasonMissing, f["reason"])
	require.Equal(t, geminiUsageEvidenceExitStream, f["exit"])
	require.Equal(t, int64(3), f["data_lines"])
	require.Equal(t, int64(2), f["json_payloads"])
	require.Equal(t, int64(1), f["comment_lines"])
	require.Equal(t, int64(1), f["done_markers"])
	require.Equal(t, int64(1), f["non_data_lines"])
	require.Equal(t, int64(len(stream)), f["bytes_read"])
	first, _ := json.Marshal(f["first_non_data_sample"])
	require.Contains(t, string(first), "INTERNAL")
}

func TestGeminiUsageEvidence_StreamUsageOnlyInFinalChunkNoLog(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"text":"a"}]}}]}`,
		``,
		`data: {"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":1,"totalTokenCount":6}}`,
		``,
	}, "\n")
	svc := newGeminiEvidenceTestService("text/event-stream", stream)
	c, _, logs := newGeminiEvidenceTestContext(t, "streamGenerateContent")

	result, err := svc.ForwardNative(context.Background(), c, geminiEvidenceTestAccount(), "gemini-3.8-flash", "streamGenerateContent", true, []byte(geminiEvidenceTestRequestBody))
	require.NoError(t, err)
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Empty(t, evidenceLogs(logs))
}

func TestGeminiUsageEvidence_CountTokensNotObserved(t *testing.T) {
	svc := newGeminiEvidenceTestService("application/json", `{"totalTokens":12}`)
	c, _, logs := newGeminiEvidenceTestContext(t, "countTokens")

	_, err := svc.ForwardNative(context.Background(), c, geminiEvidenceTestAccount(), "gemini-3.8-flash", "countTokens", false, []byte(geminiEvidenceTestRequestBody))
	require.NoError(t, err)
	require.Empty(t, evidenceLogs(logs))
	require.Nil(t, geminiUsageEvidenceFromContext(c))
}

// 换号重试：上一轮尝试留下的收集器不能串到 countTokens 等新一轮调用。
func TestGeminiUsageEvidence_ForwardNativeClearsPreviousAttempt(t *testing.T) {
	c, _, _ := newGeminiEvidenceTestContext(t, "countTokens")
	stale := beginGeminiUsageEvidence(c, geminiUsageEvidenceExitNonStream, "")
	require.NotNil(t, stale)
	svc := newGeminiEvidenceTestService("application/json", `{"totalTokens":12}`)
	_, err := svc.ForwardNative(context.Background(), c, geminiEvidenceTestAccount(), "gemini-3.8-flash", "countTokens", false, []byte(geminiEvidenceTestRequestBody))
	require.NoError(t, err)
	require.Nil(t, geminiUsageEvidenceFromContext(c))
}

func TestGeminiUsageEvidence_AggregateCollectorObservesWithoutChangingResult(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`,
		``,
		`data: {"candidates":[{"content":{"parts":[{"text":" world"}]},"finishReason":"STOP"}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	gotPlain, usagePlain, err := collectGeminiSSE(strings.NewReader(stream), false)
	require.NoError(t, err)

	ev := &geminiUsageEvidence{exit: geminiUsageEvidenceExitStreamAggregate}
	gotEv, usageEv, err := collectGeminiSSEWithEvidence(strings.NewReader(stream), false, ev)
	require.NoError(t, err)
	require.Equal(t, gotPlain, gotEv, "聚合结果不受取证影响")
	require.Equal(t, usagePlain, usageEv)
	require.Equal(t, geminiUsageEvidenceReasonMissing, ev.reason())
	require.Equal(t, 3, ev.dataLines)
	require.Equal(t, 2, ev.jsonPayloads)
	require.Equal(t, 1, ev.doneMarkers)
}

func TestGeminiUsageEvidence_OAuthUnwrapFailureCounted(t *testing.T) {
	ev := &geminiUsageEvidence{}
	_, _, err := collectGeminiSSEWithEvidence(strings.NewReader("data: not-json\n\n"), true, ev)
	require.NoError(t, err)
	require.Equal(t, 1, ev.parseFailures)
	first, tail, _ := ev.budgetedSamples()
	require.Nil(t, first)
	require.Len(t, tail, 1)
	require.Equal(t, "not-json", tail[0].Sample)
}

func TestGeminiUsageEvidence_RedactsInlineDataAndSignatures(t *testing.T) {
	raw := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAABBBB"}},{"functionCall":{"name":"f","args":{"q":1}},"thoughtSignature":"sig123"},{"text":"你好"}]}}]}`
	out := redactGeminiEvidenceSample([]byte(raw))
	require.Contains(t, out, `"<data len=8>"`)
	require.Contains(t, out, `"<signature len=6>"`)
	require.Contains(t, out, `"<text len=6>"`)
	require.Contains(t, out, `"mimeType":"image/png"`)
	require.Contains(t, out, `"name":"f"`)
	require.NotContains(t, out, "AAAABBBB")
	require.NotContains(t, out, "sig123")
	require.NotContains(t, out, "你好")
}

func TestGeminiUsageEvidence_SampleBudgetAndTailCapacity(t *testing.T) {
	ev := &geminiUsageEvidence{}
	ev.observeLine("<html>" + strings.Repeat("错", 1000) + "</html>\n") // 首个非 data 行约 3KB
	for i := 0; i < 5; i++ {
		ev.observePayload([]byte(`{"n":` + string(rune('0'+i)) + `}`))
	}
	require.Len(t, ev.tail, geminiUsageEvidenceTailSize)
	first, tail, truncated := ev.budgetedSamples()
	require.True(t, truncated)
	require.NotNil(t, first)
	require.True(t, first.Truncated)
	total := len(first.Sample)
	for _, s := range tail {
		total += len(s.Sample)
	}
	require.LessOrEqual(t, total, geminiUsageEvidenceSampleBudget)
	require.True(t, strings.HasPrefix(first.Sample, "<html>"))
	// UTF-8 边界：截断后仍是合法 UTF-8。
	require.True(t, isValidUTF8(first.Sample))
	// 尾部按时间序保留最后 3 条（预算已被首行用尽时为空串，但条数与顺序仍在）。
	require.Len(t, tail, geminiUsageEvidenceTailSize)
}

func TestGeminiUsageEvidence_TailOrderIsChronological(t *testing.T) {
	ev := &geminiUsageEvidence{}
	for _, n := range []string{"1", "2", "3", "4", "5"} {
		ev.observePayload([]byte(`{"n":` + n + `}`))
	}
	_, tail, truncated := ev.budgetedSamples()
	require.False(t, truncated)
	require.Equal(t, []string{`{"n":3}`, `{"n":4}`, `{"n":5}`}, []string{tail[0].Sample, tail[1].Sample, tail[2].Sample})
}

func TestGeminiUsageEvidence_NilSafe(t *testing.T) {
	var ev *geminiUsageEvidence
	ev.observeLine("data: x\n")
	ev.observePayload([]byte(`{}`))
	ev.observeParseFailure([]byte("x"))
	ev.addBytes(3)
	require.Equal(t, "", ev.reason())
	c, _, logs := newGeminiEvidenceTestContext(t, "generateContent")
	ev.emit(c, geminiUsageEvidenceMeta{})
	require.Empty(t, evidenceLogs(logs))
}

func isValidUTF8(s string) bool {
	return strings.ToValidUTF8(s, "�") == s
}

// 回归（code-review-1 #1）：超大载荷不能让样本持有完整内容，也不能泄露未脱敏前缀。
func TestGeminiUsageEvidence_OversizedPayloadsStayBounded(t *testing.T) {
	bigText := strings.Repeat("机密", 20000) // ~120KB
	bigJSON := `{"candidates":[{"content":{"parts":[{"text":"` + bigText + `"}]}}]}`

	ev := &geminiUsageEvidence{}
	ev.observePayload([]byte(bigJSON))
	require.Equal(t, 1, ev.jsonPayloads)
	require.Empty(t, ev.tail[0].source, "超大 JSON 不保留源内容")
	require.Contains(t, ev.tail[0].placeholder, "oversized json")

	// 首个非 data 行是超大裸 JSON：必须按 JSON 处理，不能留未脱敏前缀。
	ev.observeLine(bigJSON + "\n")
	require.NotNil(t, ev.firstNonData)
	require.Empty(t, ev.firstNonData.source)

	// 超大非 JSON：只留不超过预算的前缀。
	ev2 := &geminiUsageEvidence{}
	ev2.observeParseFailure([]byte("<html>" + bigText))
	require.True(t, ev2.tail[0].prefixOnly)
	require.LessOrEqual(t, len(ev2.tail[0].source), geminiUsageEvidenceSampleBudget)
	require.True(t, isValidUTF8(ev2.tail[0].source))

	first, tail, truncated := ev.budgetedSamples()
	require.True(t, truncated)
	require.NotContains(t, first.Sample, "机密")
	require.NotContains(t, tail[0].Sample, "机密")
	require.Equal(t, len(bigJSON), tail[0].OriginalLen)
}

// 正常大小的载荷延迟脱敏：入环时保存的是原文拷贝，输出时才替换 text。
func TestGeminiUsageEvidence_SmallPayloadRedactedAtRender(t *testing.T) {
	ev := &geminiUsageEvidence{}
	ev.observePayload([]byte(`{"candidates":[{"content":{"parts":[{"text":"abc"}]}}]}`))
	_, tail, _ := ev.budgetedSamples()
	require.Contains(t, tail[0].Sample, "<text len=3>")
	require.NotContains(t, tail[0].Sample, "abc")
}

// 回归（code-review-2 #1）：结构字段不能引用整块大载荷。
func TestGeminiUsageEvidence_StructureFieldsDoNotRetainPayload(t *testing.T) {
	big := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + strings.Repeat("A", 200000) +
		`"}}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.8-flash","responseId":"rid"}`
	ev := &geminiUsageEvidence{}
	ev.observePayload([]byte(big))
	for _, v := range []string{ev.modelVersion, ev.finishReason, ev.responseID} {
		require.NotEmpty(t, v)
		// 独立拷贝：容量等于自身长度，不可能覆盖 200KB 载荷。
		require.Less(t, len(v), 64)
	}
	require.Equal(t, "gemini-3.8-flash", ev.modelVersion)
	require.Equal(t, "STOP", ev.finishReason)

	long := `{"modelVersion":"` + strings.Repeat("m", 1000) + `"}`
	ev.observePayload([]byte(long))
	require.Len(t, ev.modelVersion, geminiEvidenceFieldMaxLen)
}
