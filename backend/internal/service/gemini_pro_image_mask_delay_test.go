package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stagedGeminiStreamBody struct {
	first     []byte
	rest      []byte
	firstRead chan struct{}
	release   chan struct{}
	stage     int
}

func (b *stagedGeminiStreamBody) Read(p []byte) (int, error) {
	switch b.stage {
	case 0:
		b.stage++
		n := copy(p, b.first)
		close(b.firstRead)
		return n, nil
	case 1:
		b.stage++
		<-b.release
		n := copy(p, b.rest)
		return n, nil
	default:
		return 0, io.EOF
	}
}

func (b *stagedGeminiStreamBody) Close() error { return nil }

type notifyingGeminiResponseWriter struct {
	*httptest.ResponseRecorder
	wrote chan struct{}
	once  sync.Once
}

func (w *notifyingGeminiResponseWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(p)
	w.once.Do(func() { close(w.wrote) })
	return n, err
}

func (w *notifyingGeminiResponseWriter) WriteString(s string) (int, error) {
	n, err := w.ResponseRecorder.WriteString(s)
	w.once.Do(func() { close(w.wrote) })
	return n, err
}

func (w *notifyingGeminiResponseWriter) Flush() {
	w.ResponseRecorder.Flush()
}

// 把可注入的睡眠替换成记录器：记下每次调用的时长，不真等。返回恢复函数。
func stubGeminiProImageMaskSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	orig := geminiProImageMaskSleep
	calls := &[]time.Duration{}
	geminiProImageMaskSleep = func(_ context.Context, d time.Duration) { *calls = append(*calls, d) }
	t.Cleanup(func() { geminiProImageMaskSleep = orig })
	return calls
}

func TestGeminiProImageMaskDelayDurationRange(t *testing.T) {
	orig := geminiProImageIntn
	defer func() { geminiProImageIntn = orig }()

	lo := geminiProImageMaskDelayBase - geminiProImageMaskDelayJitter
	hi := geminiProImageMaskDelayBase + geminiProImageMaskDelayJitter

	// 随机源取最小值 → 下界；取最大值 → 上界。
	geminiProImageIntn = func(n int) int { return 0 }
	require.Equal(t, lo, geminiProImageMaskDelayDuration())
	geminiProImageIntn = func(n int) int { return n - 1 }
	require.Equal(t, hi, geminiProImageMaskDelayDuration())

	// 真随机源多抽几次，全部落在闭区间内。
	geminiProImageIntn = orig
	for i := 0; i < 200; i++ {
		d := geminiProImageMaskDelayDuration()
		require.GreaterOrEqual(t, d, lo)
		require.LessOrEqual(t, d, hi)
	}
	// 用户要求「10 秒左右」：中心落在 10s。
	require.Equal(t, 10*time.Second, (lo+hi)/2)
}

// 客户端断开后不应继续占着 goroutine 等满 10s。
func TestGeminiProImageMaskSleepReturnsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	geminiProImageMaskSleep(ctx, 10*time.Second)
	require.Less(t, time.Since(start), time.Second)
}

// 非流式挂载点：确实伪装时睡一次、时长在区间内；真 pro 与非 pro 模型一次都不睡。
func TestHandleNativeNonStreamingResponseDelaysOnlyWhenProMasked(t *testing.T) {
	gin.SetMode(gin.TestMode)
	lo := geminiProImageMaskDelayBase - geminiProImageMaskDelayJitter
	hi := geminiProImageMaskDelayBase + geminiProImageMaskDelayJitter

	run := func(t *testing.T, body, model string) []time.Duration {
		t.Helper()
		calls := stubGeminiProImageMaskSleep(t)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/"+model+":generateContent", nil)
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}
		params := newGeminiImageUsageParams(model, "generateContent", "2K", []byte(`{"contents":[{"parts":[{"text":"draw"}]}]}`))
		svc := &GeminiMessagesCompatService{}
		_, err := svc.handleNativeNonStreamingResponse(c, resp, false, params)
		require.NoError(t, err)
		return *calls
	}

	t.Run("flash response masked as pro sleeps once", func(t *testing.T) {
		calls := run(t, flashStrippedBody, "gemini-3-pro-image-preview")
		require.Len(t, calls, 1)
		require.GreaterOrEqual(t, calls[0], lo)
		require.LessOrEqual(t, calls[0], hi)
	})
	t.Run("genuine pro does not sleep", func(t *testing.T) {
		require.Empty(t, run(t, proRealBody, "gemini-3-pro-image-preview"))
	})
	t.Run("flash model itself does not sleep", func(t *testing.T) {
		require.Empty(t, run(t, flashStrippedBody, "gemini-3.1-flash-image"))
	})
}

// countTokens 不出图，既不伪装也不该延迟。
func TestHandleNativeNonStreamingResponseNoDelayForCountTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	calls := stubGeminiProImageMaskSleep(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-image-preview:countTokens", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"totalTokens":12}`)),
	}
	params := newGeminiImageUsageParams("gemini-3-pro-image-preview", "countTokens", "2K", nil)
	svc := &GeminiMessagesCompatService{}
	_, err := svc.handleNativeNonStreamingResponse(c, resp, false, params)
	require.NoError(t, err)
	require.Empty(t, *calls)
}

// 流式挂载点：多块都被伪装（首块纠 modelVersion、终结块合成 usage），但只睡一次。
func TestHandleNativeStreamingResponseDelaysOnceWhenProMasked(t *testing.T) {
	gin.SetMode(gin.TestMode)
	calls := stubGeminiProImageMaskSleep(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-image-preview:streamGenerateContent", nil)
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}],"modelVersion":"gemini-3.1-flash-image"}`,
		"",
		`data: {"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":1200,"totalTokenCount":1220},"modelVersion":"gemini-3.1-flash-image"}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(stream)),
	}
	params := newGeminiImageUsageParams("gemini-3-pro-image-preview", "streamGenerateContent", "2K", []byte(`{"contents":[{"parts":[{"text":"draw"}]}]}`))
	svc := &GeminiMessagesCompatService{}
	result, err := svc.handleNativeStreamingResponse(c, resp, time.Now(), false, params)
	require.NoError(t, err)
	require.Equal(t, 1120, result.usage.ImageOutputTokens, "伪装本身仍要生效")
	require.NotContains(t, w.Body.String(), `"modelVersion":"gemini-3.1-flash-image"`)
	require.Len(t, *calls, 1)
}

func TestHandleNativeStreamingResponseDelaysBeforeBufferedImageChunk(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	calls := []time.Duration{}
	origSleep := geminiProImageMaskSleep
	geminiProImageMaskSleep = func(_ context.Context, d time.Duration) {
		if w.Body.Len() != 0 {
			t.Fatalf("masked stream wrote data before delay: %q", w.Body.String())
		}
		calls = append(calls, d)
	}
	t.Cleanup(func() { geminiProImageMaskSleep = origSleep })

	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-image-preview:streamGenerateContent", nil)
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}]}`,
		"",
		`data: {"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":1200,"totalTokenCount":1220},"modelVersion":"gemini-3.1-flash-image"}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(stream)),
	}
	params := newGeminiImageUsageParams("gemini-3-pro-image-preview", "streamGenerateContent", "2K", []byte(`{"contents":[{"parts":[{"text":"draw"}]}]}`))
	svc := &GeminiMessagesCompatService{}
	_, err := svc.handleNativeStreamingResponse(c, resp, time.Now(), false, params)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	require.Contains(t, w.Body.String(), `"modelVersion":"gemini-3-pro-image-preview"`)
}

func TestHandleNativeStreamingResponseFirstTokenIncludesDeferredMaskDelay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-image-preview:streamGenerateContent", nil)
	origSleep := geminiProImageMaskSleep
	geminiProImageMaskSleep = func(_ context.Context, _ time.Duration) { time.Sleep(25 * time.Millisecond) }
	t.Cleanup(func() { geminiProImageMaskSleep = origSleep })
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}]}`,
		"",
		`data: {"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":1200,"totalTokenCount":1220},"modelVersion":"gemini-3.1-flash-image"}`,
		"",
	}, "\n")
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}
	params := newGeminiImageUsageParams("gemini-3-pro-image-preview", "streamGenerateContent", "2K", []byte(`{"contents":[{"parts":[{"text":"draw"}]}]}`))
	svc := &GeminiMessagesCompatService{}
	result, err := svc.handleNativeStreamingResponse(c, resp, time.Now(), false, params)
	require.NoError(t, err)
	require.NotNil(t, result.firstTokenMs)
	require.GreaterOrEqual(t, *result.firstTokenMs, 20)
}

func TestHandleNativeStreamingResponseMasksSplitFinishAndUsageChunks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	calls := stubGeminiProImageMaskSleep(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-image-preview:streamGenerateContent", nil)
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}]}`,
		"",
		`data: {"candidates":[{"finishReason":"STOP"}],"modelVersion":"gemini-3.1-flash-image"}`,
		"",
		`data: {"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":1200,"totalTokenCount":1220}}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}
	params := newGeminiImageUsageParams("gemini-3-pro-image-preview", "streamGenerateContent", "2K", []byte(`{"contents":[{"parts":[{"text":"draw"}]}]}`))
	svc := &GeminiMessagesCompatService{}
	result, err := svc.handleNativeStreamingResponse(c, resp, time.Now(), false, params)
	require.NoError(t, err)
	require.Len(t, *calls, 1)
	require.Equal(t, 1120, result.usage.ImageOutputTokens)
	require.Contains(t, w.Body.String(), `"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1120}]`)
}

func TestHandleNativeStreamingResponseDoesNotDelayGenuineProSplitTerminalChunks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	calls := stubGeminiProImageMaskSleep(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-image-preview:streamGenerateContent", nil)
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}],"modelVersion":"gemini-3-pro-image-preview"}`,
		"",
		`data: {"candidates":[{"finishReason":"STOP"}]}`,
		"",
		`data: {"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":1200,"totalTokenCount":1220,"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1120}]}}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}
	params := newGeminiImageUsageParams("gemini-3-pro-image-preview", "streamGenerateContent", "2K", []byte(`{"contents":[{"parts":[{"text":"draw"}]}]}`))
	svc := &GeminiMessagesCompatService{}
	result, err := svc.handleNativeStreamingResponse(c, resp, time.Now(), false, params)
	require.NoError(t, err)
	require.Empty(t, *calls)
	require.Equal(t, 1120, result.usage.ImageOutputTokens)
	require.Contains(t, w.Body.String(), `"modelVersion":"gemini-3-pro-image-preview"`)
}

type trackingGeminiResponseBody struct {
	io.Reader
	closed bool
}

func (b *trackingGeminiResponseBody) Close() error {
	b.closed = true
	return nil
}

func TestHandleNativeNonStreamingResponseClosesBodyBeforeMaskDelay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-image-preview:generateContent", nil)
	body := &trackingGeminiResponseBody{Reader: strings.NewReader(flashStrippedBody)}
	origSleep := geminiProImageMaskSleep
	geminiProImageMaskSleep = func(_ context.Context, _ time.Duration) {
		if !body.closed {
			t.Fatal("response body must be closed before mask delay")
		}
	}
	t.Cleanup(func() { geminiProImageMaskSleep = origSleep })

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       body,
	}
	params := newGeminiImageUsageParams("gemini-3-pro-image-preview", "generateContent", "2K", []byte(`{"contents":[{"parts":[{"text":"draw"}]}]}`))
	svc := &GeminiMessagesCompatService{}
	_, err := svc.handleNativeNonStreamingResponse(c, resp, false, params)
	require.NoError(t, err)
	require.True(t, body.closed)
	require.Equal(t, http.NoBody, resp.Body)
}

func TestHandleNativeStreamingResponseFlushesPendingChunksOnEOF(t *testing.T) {
	gin.SetMode(gin.TestMode)
	calls := stubGeminiProImageMaskSleep(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-image-preview:streamGenerateContent", nil)
	stream := `data: {"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}]}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(stream)),
	}
	params := newGeminiImageUsageParams("gemini-3-pro-image-preview", "streamGenerateContent", "2K", []byte(`{"contents":[{"parts":[{"text":"draw"}]}]}`))
	svc := &GeminiMessagesCompatService{}
	_, err := svc.handleNativeStreamingResponse(c, resp, time.Now(), false, params)
	require.NoError(t, err)
	require.Equal(t, stream, w.Body.String())
	require.Empty(t, *calls, "an unresolved truncated stream must not invent a mask delay")
}

func TestHandleNativeStreamingResponseFlushesPendingChunksBeforeReadError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-image-preview:streamGenerateContent", nil)
	first := `data: {"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}]}` + "\n"
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(io.MultiReader(strings.NewReader(first), errorReader{err: errors.New("upstream read failed")})),
	}
	params := newGeminiImageUsageParams("gemini-3-pro-image-preview", "streamGenerateContent", "2K", []byte(`{"contents":[{"parts":[{"text":"draw"}]}]}`))
	svc := &GeminiMessagesCompatService{}
	_, err := svc.handleNativeStreamingResponse(c, resp, time.Now(), false, params)
	require.EqualError(t, err, "upstream read failed")
	require.Equal(t, first, w.Body.String())
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

// 流式真 pro：分块 modelVersion 已是 pro 且终结块带 IMAGE 明细，不伪装也不延迟。
func TestHandleNativeStreamingResponseNoDelayForGenuinePro(t *testing.T) {
	gin.SetMode(gin.TestMode)
	calls := stubGeminiProImageMaskSleep(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-image-preview:streamGenerateContent", nil)
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}],"modelVersion":"gemini-3-pro-image-preview"}`,
		"",
		`data: {"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":1200,"totalTokenCount":1220,"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1120}]},"modelVersion":"gemini-3-pro-image-preview"}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(stream)),
	}
	params := newGeminiImageUsageParams("gemini-3-pro-image-preview", "streamGenerateContent", "2K", []byte(`{"contents":[{"parts":[{"text":"draw"}]}]}`))
	svc := &GeminiMessagesCompatService{}
	_, err := svc.handleNativeStreamingResponse(c, resp, time.Now(), false, params)
	require.NoError(t, err)
	require.Empty(t, *calls)
}

func TestHandleNativeStreamingResponseGenuineProWritesBeforeTerminalChunk(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := &stagedGeminiStreamBody{
		first:     []byte("data: " + strings.TrimSpace(proRealBody) + "\n"),
		rest:      []byte("\ndata: [DONE]\n\n"),
		firstRead: make(chan struct{}),
		release:   make(chan struct{}),
	}
	w := &notifyingGeminiResponseWriter{ResponseRecorder: httptest.NewRecorder(), wrote: make(chan struct{})}
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-image-preview:streamGenerateContent", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}
	params := newGeminiImageUsageParams("gemini-3-pro-image-preview", "streamGenerateContent", "2K", []byte(`{"contents":[{"parts":[{"text":"draw"}]}]}`))
	svc := &GeminiMessagesCompatService{}
	done := make(chan error, 1)
	go func() {
		_, err := svc.handleNativeStreamingResponse(c, resp, time.Now(), false, params)
		done <- err
	}()
	<-body.firstRead
	select {
	case <-w.wrote:
		// Genuine pro data remains streaming and is not held for the terminal chunk.
	case <-time.After(time.Second):
		close(body.release)
		t.Fatal("genuine pro first chunk was buffered until terminal response")
	}
	close(body.release)
	require.NoError(t, <-done)
}

func TestHandleNativeStreamingResponseGenuineProDecisionIsSticky(t *testing.T) {
	gin.SetMode(gin.TestMode)
	calls := stubGeminiProImageMaskSleep(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-image-preview:streamGenerateContent", nil)
	stream := strings.Join([]string{
		"data: " + strings.TrimSpace(proRealBody),
		"",
		`data: {"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":1200,"totalTokenCount":1220}}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}
	params := newGeminiImageUsageParams("gemini-3-pro-image-preview", "streamGenerateContent", "2K", []byte(`{"contents":[{"parts":[{"text":"draw"}]}]}`))
	svc := &GeminiMessagesCompatService{}
	result, err := svc.handleNativeStreamingResponse(c, resp, time.Now(), false, params)
	require.NoError(t, err)
	require.Empty(t, *calls, "a conclusive genuine-pro decision must not be reversed by later sparse usage")
	require.Equal(t, 1120, result.usage.ImageOutputTokens)
}
