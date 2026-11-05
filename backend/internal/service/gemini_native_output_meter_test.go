package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 线上 1148（aijinmi）样本：HTTP 200，流里只有一个错误对象。
const geminiMeterErrorOnlyStream = "data: {\"error\":{\"code\":\"upstream_service_error\",\"message\":\"upstream service request failed\",\"param\":\"\",\"type\":\"new_api_error\"},\"type\":\"error\"}\n\n"

func TestGeminiNativeOutputMeter_ObserveCountsAndClassifies(t *testing.T) {
	m := &geminiNativeOutputMeter{}
	m.observe([]byte(`{"candidates":[{"content":{"parts":[{"text":"hello"},{"text":"你好","thought":true}]}}]}`))
	require.True(t, m.contentSeen)
	require.False(t, m.usageSeen)
	require.Equal(t, 7, m.textRunes)
	require.Equal(t, 5, m.textASCII)
	// 5/7 < 0.8 → 1 字符/token
	require.Equal(t, 7, m.estimatedOutputTokens())

	m2 := &geminiNativeOutputMeter{}
	m2.observe([]byte(`{"candidates":[{"content":{"parts":[{"text":"` + strings.Repeat("a", 322) + `"}]}}]}`))
	require.Equal(t, (322+3)/4, m2.estimatedOutputTokens())

	m3 := &geminiNativeOutputMeter{}
	m3.observe([]byte(`{"usageMetadata":{"promptTokenCount":0}}`))
	require.True(t, m3.usageSeen)
}

func TestGeminiNativeOutputMeter_EmptyCandidatesAreNotContent(t *testing.T) {
	for _, payload := range []string{
		`{"error":{"code":"x"},"candidates":[]}`,
		`{"error":{"code":"x"},"candidates":[{}]}`,
		`{"error":{"code":"x"},"candidates":[{"finishReason":"STOP"}]}`,
		`{"error":{"code":"x"},"candidates":[{"content":{"parts":[{"text":""}]}}]}`,
	} {
		m := &geminiNativeOutputMeter{}
		m.observe([]byte(payload))
		require.True(t, m.errorOnly(), payload)
	}
	m := &geminiNativeOutputMeter{}
	m.observe([]byte(`{"error":{"code":"x"}}`))
	m.observe([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"f"}}]}}]}`))
	require.False(t, m.errorOnly(), "functionCall 算有效输出")
	require.Equal(t, 0, m.textRunes)
}

func TestGeminiNativeOutputMeter_NilSafeAndBoundedError(t *testing.T) {
	var m *geminiNativeOutputMeter
	m.observe([]byte(`{"error":{}}`))
	require.False(t, m.errorOnly())
	require.Equal(t, 0, m.estimatedOutputTokens())
	require.False(t, m.shouldEstimate(&ClaudeUsage{}))

	big := &geminiNativeOutputMeter{}
	big.observe([]byte(`{"error":{"message":"` + strings.Repeat("x", 10000) + `"}}`))
	require.Len(t, big.firstError, geminiNativeOutputMeterMaxErrorBody)
	fe := big.failoverError()
	require.Equal(t, http.StatusBadGateway, fe.StatusCode)
	require.False(t, fe.RetryableOnSameAccount)
}

func TestGeminiNativeOutputMeter_ShouldEstimateOnlyWhenUsageMissingAndZero(t *testing.T) {
	m := &geminiNativeOutputMeter{}
	m.observe([]byte(`{"candidates":[{"content":{"parts":[{"text":"x"}]}}]}`))
	require.True(t, m.shouldEstimate(&ClaudeUsage{}))
	require.False(t, m.shouldEstimate(&ClaudeUsage{ImageOutputTokens: 1120}), "已合成 usage 不估算")
	m.observe([]byte(`{"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0}}`))
	require.False(t, m.shouldEstimate(&ClaudeUsage{}), "usageMetadata 存在（含全 0）不估算")
}

// ---- 非流式出口 ----

func TestGeminiNativeOutput_NonStreamErrorOnlyFailsOver(t *testing.T) {
	body := `{"error":{"code":"upstream_service_error","type":"new_api_error"},"candidates":[{}]}`
	svc := newGeminiEvidenceTestService("application/json", body)
	c, rec, logs := newGeminiEvidenceTestContext(t, "generateContent")

	_, err := svc.ForwardNative(context.Background(), c, geminiEvidenceTestAccount(), "gemini-3.8-flash", "generateContent", false, []byte(geminiEvidenceTestRequestBody))
	var fe *UpstreamFailoverError
	require.True(t, errors.As(err, &fe))
	require.Equal(t, http.StatusBadGateway, fe.StatusCode)
	require.False(t, fe.RetryableOnSameAccount)
	require.Equal(t, body, string(fe.ResponseBody))
	require.False(t, c.Writer.Written(), "换号前客户端不能收到任何字节")
	require.Empty(t, rec.Body.String())
	require.Empty(t, evidenceLogs(logs), "换号的尝试不写取证日志")
}

func TestGeminiNativeOutput_NonStreamErrorWithContentSucceeds(t *testing.T) {
	body := `{"error":{"code":"partial"},"candidates":[{"content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1}}`
	svc := newGeminiEvidenceTestService("application/json", body)
	c, rec, _ := newGeminiEvidenceTestContext(t, "generateContent")

	result, err := svc.ForwardNative(context.Background(), c, geminiEvidenceTestAccount(), "gemini-3.8-flash", "generateContent", false, []byte(geminiEvidenceTestRequestBody))
	require.NoError(t, err)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.False(t, result.UsageEstimated)
	require.Equal(t, body, rec.Body.String())
}

func TestGeminiNativeOutput_NonStreamMissingUsageEstimates(t *testing.T) {
	text := strings.Repeat("a", 322)
	body := `{"candidates":[{"content":{"parts":[{"text":"` + text + `"}],"role":"model"},"finishReason":"STOP","index":0}]}`
	svc := newGeminiEvidenceTestService("application/json", body)
	c, rec, logs := newGeminiEvidenceTestContext(t, "generateContent")

	result, err := svc.ForwardNative(context.Background(), c, geminiEvidenceTestAccount(), "gemini-3.8-flash", "generateContent", false, []byte(geminiEvidenceTestRequestBody))
	require.NoError(t, err)
	require.True(t, result.UsageEstimated)
	require.Equal(t, estimateGeminiCountTokens([]byte(geminiEvidenceTestRequestBody)), result.Usage.InputTokens)
	require.Equal(t, (322+3)/4, result.Usage.OutputTokens)
	require.Equal(t, body, rec.Body.String(), "客户端字节不变")

	entries := evidenceLogs(logs)
	require.Len(t, entries, 1)
	f := entries[0].ContextMap()
	require.Equal(t, geminiUsageEvidenceReasonMissing, f["reason"])
	require.Equal(t, true, f["usage_estimated"])
	require.Equal(t, false, f["billed_usage_zero"])
	require.Equal(t, int64((322+3)/4), f["estimated_output_tokens"])
}

func TestGeminiNativeOutput_ZeroUsageMetadataNotEstimated(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}],"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":0}}`
	svc := newGeminiEvidenceTestService("application/json", body)
	c, _, _ := newGeminiEvidenceTestContext(t, "generateContent")

	result, err := svc.ForwardNative(context.Background(), c, geminiEvidenceTestAccount(), "gemini-3.8-flash", "generateContent", false, []byte(geminiEvidenceTestRequestBody))
	require.NoError(t, err)
	require.False(t, result.UsageEstimated)
	require.Equal(t, ClaudeUsage{}, result.Usage)
}

func TestGeminiNativeOutput_ForwardNativeClearsStaleMeter(t *testing.T) {
	c, _, _ := newGeminiEvidenceTestContext(t, "countTokens")
	stale := beginGeminiNativeOutputMeter(c)
	stale.observe([]byte(`{"error":{}}`))
	svc := newGeminiEvidenceTestService("application/json", `{"totalTokens":12}`)
	_, err := svc.ForwardNative(context.Background(), c, geminiEvidenceTestAccount(), "gemini-3.8-flash", "countTokens", false, []byte(geminiEvidenceTestRequestBody))
	require.NoError(t, err)
	require.Nil(t, geminiNativeOutputMeterFromContext(c))
}

// ---- 流式出口 ----

func TestGeminiNativeOutput_StreamErrorOnlyFailsOverAndRestoresHeaders(t *testing.T) {
	svc := newGeminiEvidenceTestService("text/event-stream", geminiMeterErrorOnlyStream)
	svc.httpUpstream.(*geminiCompatHTTPUpstreamStub).response.Header.Set("X-Upstream-Trace", "t1")
	c, rec, logs := newGeminiEvidenceTestContext(t, "streamGenerateContent")
	c.Writer.Header().Set("X-Request-Id", "keep-me")

	_, err := svc.ForwardNative(context.Background(), c, geminiEvidenceTestAccount(), "gemini-3.8-flash", "streamGenerateContent", true, []byte(geminiEvidenceTestRequestBody))
	var fe *UpstreamFailoverError
	require.True(t, errors.As(err, &fe))
	require.Equal(t, http.StatusBadGateway, fe.StatusCode)
	require.Contains(t, string(fe.ResponseBody), "upstream_service_error")
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
	h := c.Writer.Header()
	require.Equal(t, "keep-me", h.Get("X-Request-Id"), "入口前已有的头保留")
	require.Empty(t, h.Get("Content-Type"), "流式头被撤回")
	require.Empty(t, h.Get("Cache-Control"))
	require.Empty(t, h.Get("X-Upstream-Trace"))
	require.Empty(t, evidenceLogs(logs))
}

func TestGeminiNativeOutput_StreamErrorThenContentSucceedsInOrder(t *testing.T) {
	stream := geminiMeterErrorOnlyStream +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"late\"}]}}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":1}}\n\n"
	svc := newGeminiEvidenceTestService("text/event-stream", stream)
	c, rec, _ := newGeminiEvidenceTestContext(t, "streamGenerateContent")

	result, err := svc.ForwardNative(context.Background(), c, geminiEvidenceTestAccount(), "gemini-3.8-flash", "streamGenerateContent", true, []byte(geminiEvidenceTestRequestBody))
	require.NoError(t, err)
	require.Equal(t, 2, result.Usage.InputTokens)
	require.Equal(t, stream, rec.Body.String(), "先扣下的错误行按原顺序放行")
}

func TestGeminiNativeOutput_StreamErrorAfterWrittenKeepsCurrentBehavior(t *testing.T) {
	stream := ": keepalive\n\n" + geminiMeterErrorOnlyStream
	svc := newGeminiEvidenceTestService("text/event-stream", stream)
	c, rec, _ := newGeminiEvidenceTestContext(t, "streamGenerateContent")

	result, err := svc.ForwardNative(context.Background(), c, geminiEvidenceTestAccount(), "gemini-3.8-flash", "streamGenerateContent", true, []byte(geminiEvidenceTestRequestBody))
	require.NoError(t, err)
	require.False(t, result.UsageEstimated)
	require.Equal(t, stream, rec.Body.String())
}

func TestGeminiNativeOutput_StreamMissingUsageEstimates(t *testing.T) {
	stream := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"" + strings.Repeat("b", 322) + "\"}]}}]}\n\n"
	svc := newGeminiEvidenceTestService("text/event-stream", stream)
	c, rec, _ := newGeminiEvidenceTestContext(t, "streamGenerateContent")

	result, err := svc.ForwardNative(context.Background(), c, geminiEvidenceTestAccount(), "gemini-3.8-flash", "streamGenerateContent", true, []byte(geminiEvidenceTestRequestBody))
	require.NoError(t, err)
	require.True(t, result.UsageEstimated)
	require.Equal(t, (322+3)/4, result.Usage.OutputTokens)
	require.Equal(t, stream, rec.Body.String())
}

type geminiMeterErrAfterReader struct {
	data []byte
	err  error
}

func (r *geminiMeterErrAfterReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestGeminiNativeOutput_StreamErrorOnlyThenReadErrorFailsOver(t *testing.T) {
	svc := newGeminiEvidenceTestService("text/event-stream", "")
	c, rec, _ := newGeminiEvidenceTestContext(t, "streamGenerateContent")
	beginGeminiNativeOutputMeter(c)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(&geminiMeterErrAfterReader{data: []byte(geminiMeterErrorOnlyStream), err: errors.New("connection reset")}),
	}
	_, err := svc.handleNativeStreamingResponse(c, resp, time.Now(), false, geminiImageUsageParams{}, &Account{}, "")
	var fe *UpstreamFailoverError
	require.True(t, errors.As(err, &fe))
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

// ProMaskEnabled 下纯错误流：pro 伪装的 pending 行与 Flush 都不能提前提交 200。
func TestGeminiNativeOutput_StreamErrorOnlyWithProMaskFailsOver(t *testing.T) {
	for name, reader := range map[string]io.Reader{
		"eof":        strings.NewReader(geminiMeterErrorOnlyStream),
		"read_error": &geminiMeterErrAfterReader{data: []byte(geminiMeterErrorOnlyStream), err: errors.New("boom")},
	} {
		t.Run(name, func(t *testing.T) {
			svc := newGeminiEvidenceTestService("text/event-stream", "")
			c, rec, _ := newGeminiEvidenceTestContext(t, "streamGenerateContent")
			beginGeminiNativeOutputMeter(c)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(reader),
			}
			params := geminiImageUsageParams{ProMaskEnabled: true, Model: "gemini-3-pro-image-preview"}
			_, err := svc.handleNativeStreamingResponse(c, resp, time.Now(), false, params, &Account{}, "")
			var fe *UpstreamFailoverError
			require.True(t, errors.As(err, &fe))
			require.False(t, c.Writer.Written())
			require.Empty(t, rec.Body.String())
		})
	}
}

// ---- 上游流式聚合出口（OAuth code_assist 非流式）----

type geminiMeterTokenCache struct{}

func (geminiMeterTokenCache) GetAccessToken(context.Context, string) (string, error) {
	return "tok", nil
}
func (geminiMeterTokenCache) SetAccessToken(context.Context, string, string, time.Duration) error {
	return nil
}
func (geminiMeterTokenCache) DeleteAccessToken(context.Context, string) error { return nil }
func (geminiMeterTokenCache) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (geminiMeterTokenCache) ReleaseRefreshLock(context.Context, string) error { return nil }

func geminiMeterCodeAssistAccount() *Account {
	return &Account{
		ID:       1121,
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"project_id": "proj-1",
			"expires_at": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		},
	}
}

func newGeminiMeterAggregateService(stream string) *GeminiMessagesCompatService {
	svc := newGeminiEvidenceTestService("text/event-stream", stream)
	svc.tokenProvider = &GeminiTokenProvider{tokenCache: geminiMeterTokenCache{}}
	return svc
}

func TestGeminiNativeOutput_AggregateErrorOnlyFailsOver(t *testing.T) {
	svc := newGeminiMeterAggregateService(geminiMeterErrorOnlyStream)
	c, rec, _ := newGeminiEvidenceTestContext(t, "generateContent")

	_, err := svc.ForwardNative(context.Background(), c, geminiMeterCodeAssistAccount(), "gemini-3.8-flash", "generateContent", false, []byte(geminiEvidenceTestRequestBody))
	var fe *UpstreamFailoverError
	require.True(t, errors.As(err, &fe), "err=%v", err)
	require.Equal(t, http.StatusBadGateway, fe.StatusCode)
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

func TestGeminiNativeOutput_AggregateMissingUsageEstimates(t *testing.T) {
	stream := "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"" + strings.Repeat("c", 40) + "\"}]}}]}}\n\n" +
		"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"\"}],\"role\":\"model\"},\"finishReason\":\"STOP\"}]}}\n\n"
	svc := newGeminiMeterAggregateService(stream)
	c, _, _ := newGeminiEvidenceTestContext(t, "generateContent")

	result, err := svc.ForwardNative(context.Background(), c, geminiMeterCodeAssistAccount(), "gemini-3.8-flash", "generateContent", false, []byte(geminiEvidenceTestRequestBody))
	require.NoError(t, err)
	require.True(t, result.UsageEstimated)
	require.Equal(t, 10, result.Usage.OutputTokens)
}
