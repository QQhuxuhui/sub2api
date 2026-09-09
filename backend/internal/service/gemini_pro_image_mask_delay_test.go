package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

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
