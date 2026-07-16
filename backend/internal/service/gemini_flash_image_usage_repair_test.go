package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestIsGemini31FlashImageModel(t *testing.T) {
	tests := map[string]bool{
		"gemini-3.1-flash-image":                true,
		"models/gemini-3.1-flash-image":         true,
		"gemini-3.1-flash-image-preview":        true,
		"gemini-3.1-flash-image-preview-202607": true,
		"gemini-3.1-flash-lite-image":           false,
		"gemini-2.5-flash-image":                false,
		"gemini-3-pro-image":                    false,
		"custom-gemini-3.1-flash-image-wrapper": false,
	}
	for model, want := range tests {
		t.Run(model, func(t *testing.T) {
			require.Equal(t, want, isGemini31FlashImageModel(model))
		})
	}
}

func TestGemini31FlashImageTokens(t *testing.T) {
	tests := []struct {
		size string
		want int
		ok   bool
	}{
		{size: "", want: 1120, ok: true},
		{size: "0.5K", want: 747, ok: true},
		{size: "512px", want: 747, ok: true},
		{size: "1K", want: 1120, ok: true},
		{size: "1k", want: 1120, ok: true},
		{size: "2K", want: 1680, ok: true},
		{size: "4K", want: 2520, ok: true},
		{size: "auto", want: 0, ok: false},
		{size: "8K", want: 0, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.size, func(t *testing.T) {
			got, ok := gemini31FlashImageTokens(tt.size)
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestGeminiPromptIsTextOnly(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "text", body: `{"contents":[{"role":"user","parts":[{"text":"draw a mug"}]}]}`, want: true},
		{name: "text with system instruction", body: `{"systemInstruction":{"parts":[{"text":"be concise"}]},"contents":[{"parts":[{"text":"draw"}]}]}`, want: true},
		{name: "inline image", body: `{"contents":[{"parts":[{"text":"edit"},{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}]}`, want: false},
		{name: "file image", body: `{"contents":[{"parts":[{"text":"edit"},{"fileData":{"mimeType":"image/png","fileUri":"gs://image"}}]}]}`, want: false},
		{name: "cached content unknown modality", body: `{"cachedContent":"cachedContents/abc","contents":[{"parts":[{"text":"draw"}]}]}`, want: false},
		{name: "function response", body: `{"contents":[{"parts":[{"functionResponse":{"name":"tool","response":{}}}]}]}`, want: false},
		{name: "empty", body: `{}`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, geminiPromptIsTextOnly([]byte(tt.body)))
		})
	}
}

const flashUsageMissingImageDetail = `{
  "candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]},"finishReason":"STOP"}],
  "usageMetadata":{
    "promptTokenCount":20,
    "candidatesTokenCount":2091,
    "totalTokenCount":2111,
    "trafficType":"ON_DEMAND",
    "promptTokensDetails":[{"modality":"TEXT","tokenCount":20}],
    "candidatesTokensDetails":[{"modality":"TEXT","tokenCount":411}]
  },
  "modelVersion":"gemini-3.1-flash-image",
  "responseId":"resp_flash"
}`

const flashUsageMissingAllDetails = `{
  "candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]},"finishReason":"STOP"}],
  "usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":2091,"totalTokenCount":2111},
  "modelVersion":"gemini-3.1-flash-image"
}`

func TestRepairGemini31FlashImageUsageAddsTextPromptDetailsAndDefaultServiceTier(t *testing.T) {
	out, usage, repaired := repairGemini31FlashImageUsage(
		[]byte(flashUsageMissingAllDetails),
		"gemini-3.1-flash-image",
		"2K",
		geminiFlashUsageRepairOptions{PromptTextOnly: true, RequestedServiceTier: "standard"},
	)
	require.True(t, repaired)
	require.Equal(t, int64(20), gjson.GetBytes(out, "usageMetadata.promptTokensDetails.0.tokenCount").Int())
	require.Equal(t, "TEXT", gjson.GetBytes(out, "usageMetadata.promptTokensDetails.0.modality").String())
	require.Equal(t, "standard", gjson.GetBytes(out, "usageMetadata.serviceTier").String())
	require.Equal(t, 2091, usage.OutputTokens)
	require.Equal(t, 1680, usage.ImageOutputTokens)
}

func TestRepairGemini31FlashImageUsageDoesNotInventPromptDetailsForImageInput(t *testing.T) {
	out, _, repaired := repairGemini31FlashImageUsage(
		[]byte(flashUsageMissingAllDetails),
		"gemini-3.1-flash-image",
		"2K",
		geminiFlashUsageRepairOptions{PromptTextOnly: false, RequestedServiceTier: "standard"},
	)
	require.True(t, repaired)
	require.False(t, gjson.GetBytes(out, "usageMetadata.promptTokensDetails").Exists())
	require.Equal(t, "standard", gjson.GetBytes(out, "usageMetadata.serviceTier").String())
}

func TestRepairGemini31FlashImageUsagePreservesTrafficTypeWithoutAddingServiceTier(t *testing.T) {
	body := strings.Replace(flashUsageMissingAllDetails, `"totalTokenCount":2111`, `"totalTokenCount":2111,"trafficType":"PROVISIONED_THROUGHPUT"`, 1)
	out, _, repaired := repairGemini31FlashImageUsage(
		[]byte(body),
		"gemini-3.1-flash-image",
		"2K",
		geminiFlashUsageRepairOptions{PromptTextOnly: true, RequestedServiceTier: "standard"},
	)
	require.True(t, repaired)
	require.Equal(t, "PROVISIONED_THROUGHPUT", gjson.GetBytes(out, "usageMetadata.trafficType").String())
	require.False(t, gjson.GetBytes(out, "usageMetadata.serviceTier").Exists())
}

func TestRepairGemini31FlashImageUsagePreservesExistingServiceTier(t *testing.T) {
	body := strings.Replace(flashUsageMissingAllDetails, `"totalTokenCount":2111`, `"totalTokenCount":2111,"serviceTier":"flex"`, 1)
	out, _, repaired := repairGemini31FlashImageUsage(
		[]byte(body),
		"gemini-3.1-flash-image",
		"2K",
		geminiFlashUsageRepairOptions{PromptTextOnly: true, RequestedServiceTier: "standard"},
	)
	require.True(t, repaired)
	require.Equal(t, "flex", gjson.GetBytes(out, "usageMetadata.serviceTier").String())
}

func TestRepairGemini31FlashImageUsagePreservesTotalsAndAvoidsOverlap(t *testing.T) {
	out, usage, repaired := repairGemini31FlashImageUsage(
		[]byte(flashUsageMissingImageDetail),
		"gemini-3.1-flash-image",
		"2K",
		geminiFlashUsageRepairOptions{},
	)
	require.True(t, repaired)
	require.NotNil(t, usage)

	require.Equal(t, int64(20), gjson.GetBytes(out, "usageMetadata.promptTokenCount").Int())
	require.Equal(t, int64(2091), gjson.GetBytes(out, "usageMetadata.candidatesTokenCount").Int())
	require.Equal(t, int64(2111), gjson.GetBytes(out, "usageMetadata.totalTokenCount").Int())
	require.Equal(t, "ON_DEMAND", gjson.GetBytes(out, "usageMetadata.trafficType").String())
	require.False(t, gjson.GetBytes(out, "usageMetadata.serviceTier").Exists())
	require.False(t, gjson.GetBytes(out, "usageMetadata.thoughtsTokenCount").Exists())
	require.Equal(t, "gemini-3.1-flash-image", gjson.GetBytes(out, "modelVersion").String())
	require.False(t, gjson.GetBytes(out, "candidates.0.index").Exists())

	details := gjson.GetBytes(out, "usageMetadata.candidatesTokensDetails").Array()
	require.Len(t, details, 2)
	require.Equal(t, "TEXT", details[0].Get("modality").String())
	require.Equal(t, int64(411), details[0].Get("tokenCount").Int())
	require.Equal(t, "IMAGE", details[1].Get("modality").String())
	require.Equal(t, int64(1680), details[1].Get("tokenCount").Int())

	require.Equal(t, 20, usage.InputTokens)
	require.Equal(t, 2091, usage.OutputTokens)
	require.Equal(t, 1680, usage.ImageOutputTokens)
	require.Equal(t, 411, usage.OutputTokens-usage.ImageOutputTokens)
}

func TestRepairGemini31FlashImageUsagePreservesThoughtsWithoutAddingToCandidates(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1200,"thoughtsTokenCount":37,"totalTokenCount":1247},"modelVersion":"gemini-3.1-flash-image"}`
	out, usage, repaired := repairGemini31FlashImageUsage([]byte(body), "gemini-3.1-flash-image", "1K", geminiFlashUsageRepairOptions{})
	require.True(t, repaired)
	require.Equal(t, int64(1200), gjson.GetBytes(out, "usageMetadata.candidatesTokenCount").Int())
	require.Equal(t, int64(37), gjson.GetBytes(out, "usageMetadata.thoughtsTokenCount").Int())
	require.Equal(t, int64(1247), gjson.GetBytes(out, "usageMetadata.totalTokenCount").Int())
	require.Equal(t, 1237, usage.OutputTokens)
	require.Equal(t, 1120, usage.ImageOutputTokens)
	require.Equal(t, 117, usage.OutputTokens-usage.ImageOutputTokens)
}

func TestRepairGemini31FlashImageUsageCountsMultipleImages(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}},{"inlineData":{"mimeType":"image/jpeg","data":"BBBB"}}]}}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":4000,"totalTokenCount":4008},"modelVersion":"gemini-3.1-flash-image"}`
	out, usage, repaired := repairGemini31FlashImageUsage([]byte(body), "gemini-3.1-flash-image", "2K", geminiFlashUsageRepairOptions{})
	require.True(t, repaired)
	require.Equal(t, int64(3360), gjson.GetBytes(out, "usageMetadata.candidatesTokensDetails.0.tokenCount").Int())
	require.Equal(t, 3360, usage.ImageOutputTokens)
	require.Equal(t, 640, usage.OutputTokens-usage.ImageOutputTokens)
}

func TestRepairGemini31FlashImageUsageDoesNotOverwriteExistingImageDetail(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":1680,"totalTokenCount":1688,"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1680}]},"modelVersion":"gemini-3.1-flash-image"}`
	out, usage, repaired := repairGemini31FlashImageUsage([]byte(body), "gemini-3.1-flash-image", "2K", geminiFlashUsageRepairOptions{})
	require.False(t, repaired)
	require.Nil(t, usage)
	require.JSONEq(t, body, string(out))
}

func TestRepairGemini31FlashImageUsageReplacesZeroImageDetail(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":1680,"totalTokenCount":1688,"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":0}]},"modelVersion":"gemini-3.1-flash-image"}`
	out, usage, repaired := repairGemini31FlashImageUsage([]byte(body), "gemini-3.1-flash-image", "2K", geminiFlashUsageRepairOptions{})
	require.True(t, repaired)
	require.NotNil(t, usage)
	require.Len(t, gjson.GetBytes(out, "usageMetadata.candidatesTokensDetails").Array(), 1)
	require.Equal(t, int64(1680), gjson.GetBytes(out, "usageMetadata.candidatesTokensDetails.0.tokenCount").Int())
	require.Equal(t, 1680, usage.ImageOutputTokens)
}

func TestRepairGemini31FlashImageUsageRequiresActualImage(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"text":"I cannot generate that image."}]},"finishReason":"SAFETY"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":12,"totalTokenCount":20},"modelVersion":"gemini-3.1-flash-image"}`
	_, usage, repaired := repairGemini31FlashImageUsage([]byte(body), "gemini-3.1-flash-image", "2K", geminiFlashUsageRepairOptions{})
	require.False(t, repaired)
	require.Nil(t, usage)
}

func TestRepairGemini31FlashImageUsageRejectsInconsistentCandidateCount(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":1000,"totalTokenCount":1008},"modelVersion":"gemini-3.1-flash-image"}`
	_, usage, repaired := repairGemini31FlashImageUsage([]byte(body), "gemini-3.1-flash-image", "2K", geminiFlashUsageRepairOptions{})
	require.False(t, repaired)
	require.Nil(t, usage)
}

func TestRepairGemini31FlashImageUsageRejectsNonTargetAndUnknownSize(t *testing.T) {
	for _, tc := range []struct {
		model string
		size  string
	}{
		{model: "gemini-3-pro-image", size: "2K"},
		{model: "gemini-2.5-flash-image", size: "2K"},
		{model: "gemini-3.1-flash-image", size: "auto"},
	} {
		_, usage, repaired := repairGemini31FlashImageUsage([]byte(flashUsageMissingImageDetail), tc.model, tc.size, geminiFlashUsageRepairOptions{})
		require.False(t, repaired)
		require.Nil(t, usage)
	}
}

func TestGeminiFlashImageStreamStateRepairsUsageAfterSeparateImageChunk(t *testing.T) {
	state := &geminiFlashImageStreamState{}
	imageChunk := []byte(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}],"modelVersion":"gemini-3.1-flash-image"}`)
	usageChunk := []byte(`{"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":2091,"totalTokenCount":2111},"modelVersion":"gemini-3.1-flash-image"}`)

	out1, usage1, repaired1 := state.process(imageChunk, "gemini-3.1-flash-image", "2K", true, geminiFlashUsageRepairOptions{})
	require.False(t, repaired1)
	require.Nil(t, usage1)
	require.Equal(t, imageChunk, out1)

	out2, usage2, repaired2 := state.process(usageChunk, "gemini-3.1-flash-image", "2K", true, geminiFlashUsageRepairOptions{})
	require.True(t, repaired2)
	require.NotNil(t, usage2)
	require.Equal(t, int64(1680), gjson.GetBytes(out2, "usageMetadata.candidatesTokensDetails.0.tokenCount").Int())
	require.Equal(t, 2091, usage2.OutputTokens)
	require.Equal(t, 1680, usage2.ImageOutputTokens)
	require.Equal(t, 411, usage2.OutputTokens-usage2.ImageOutputTokens)
}

func TestGeminiFlashImageStreamStateDoesNotRepairUsageBeforeImage(t *testing.T) {
	state := &geminiFlashImageStreamState{}
	usageChunk := []byte(`{"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":2091,"totalTokenCount":2111},"modelVersion":"gemini-3.1-flash-image"}`)
	out, usage, repaired := state.process(usageChunk, "gemini-3.1-flash-image", "2K", true, geminiFlashUsageRepairOptions{})
	require.False(t, repaired)
	require.Nil(t, usage)
	require.Equal(t, usageChunk, out)
}

func TestCollectGeminiSSEPreservesUsageFromMetadataOnlyFinalChunk(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}],"modelVersion":"gemini-3.1-flash-image"}`,
		`data: {"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":2091,"totalTokenCount":2111},"modelVersion":"gemini-3.1-flash-image","responseId":"resp-final"}`,
		`data: [DONE]`,
		"",
	}, "\n")

	collected, usage, err := collectGeminiSSE(strings.NewReader(stream), false)
	require.NoError(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 2091, usage.OutputTokens)

	body, err := json.Marshal(collected)
	require.NoError(t, err)
	require.Equal(t, int64(2091), gjson.GetBytes(body, "usageMetadata.candidatesTokenCount").Int())
	require.Equal(t, "resp-final", gjson.GetBytes(body, "responseId").String())
	require.Equal(t, "AAAA", gjson.GetBytes(body, "candidates.0.content.parts.0.inlineData.data").String())

	repairedBody, repairedUsage, repaired := repairGemini31FlashImageUsage(body, "gemini-3.1-flash-image", "2K", geminiFlashUsageRepairOptions{})
	require.True(t, repaired)
	require.Equal(t, int64(1680), gjson.GetBytes(repairedBody, "usageMetadata.candidatesTokensDetails.0.tokenCount").Int())
	require.Equal(t, 1680, repairedUsage.ImageOutputTokens)
}

func TestNewGeminiImageUsageParamsUsesRawFlashSizeAndGatesActions(t *testing.T) {
	textRequest := []byte(`{"contents":[{"parts":[{"text":"draw"}]}]}`)
	params := newGeminiImageUsageParams("gemini-3.1-flash-image", "generateContent", "", textRequest)
	require.True(t, params.FlashRepairEnabled)
	require.False(t, params.ProMaskEnabled)
	require.Empty(t, params.FlashImageSize)
	require.True(t, params.FlashPromptTextOnly)
	require.Equal(t, "standard", params.RequestedServiceTier)

	flexRequest := []byte(`{"serviceTier":"flex","contents":[{"parts":[{"text":"draw"}]}]}`)
	flex := newGeminiImageUsageParams("gemini-3.1-flash-image", "generateContent", "2K", flexRequest)
	require.Equal(t, "flex", flex.RequestedServiceTier)

	imageRequest := []byte(`{"contents":[{"parts":[{"text":"edit"},{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}]}`)
	image := newGeminiImageUsageParams("gemini-3.1-flash-image", "generateContent", "2K", imageRequest)
	require.False(t, image.FlashPromptTextOnly)

	countTokens := newGeminiImageUsageParams("gemini-3.1-flash-image", "countTokens", "2K", nil)
	require.False(t, countTokens.FlashRepairEnabled)
	require.False(t, countTokens.ProMaskEnabled)

	pro := newGeminiImageUsageParams("gemini-3-pro-image", "generateContent", "4K", nil)
	require.True(t, pro.ProMaskEnabled)
	require.False(t, pro.FlashRepairEnabled)
	require.Equal(t, "4K", pro.ProTier)
}

func TestHandleNativeNonStreamingResponseRepairsFlashUsageWithoutOverlap(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &GeminiMessagesCompatService{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.1-flash-image:generateContent", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(flashUsageMissingImageDetail)),
	}
	params := newGeminiImageUsageParams("gemini-3.1-flash-image", "generateContent", "2K", []byte(`{"contents":[{"parts":[{"text":"draw"}]}]}`))

	usage, err := svc.handleNativeNonStreamingResponse(c, resp, false, params)
	require.NoError(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 2091, usage.OutputTokens)
	require.Equal(t, 1680, usage.ImageOutputTokens)
	require.Equal(t, 411, usage.OutputTokens-usage.ImageOutputTokens)
	require.Equal(t, int64(1680), gjson.GetBytes(w.Body.Bytes(), "usageMetadata.candidatesTokensDetails.1.tokenCount").Int())
	require.Equal(t, int64(2091), gjson.GetBytes(w.Body.Bytes(), "usageMetadata.candidatesTokenCount").Int())
	require.Equal(t, int64(2111), gjson.GetBytes(w.Body.Bytes(), "usageMetadata.totalTokenCount").Int())
}

func TestHandleNativeStreamingResponseRepairsFlashUsageFromSeparateChunk(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &GeminiMessagesCompatService{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.1-flash-image:streamGenerateContent", nil)
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}],"modelVersion":"gemini-3.1-flash-image"}`,
		"",
		`data: {"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":2091,"totalTokenCount":2111},"modelVersion":"gemini-3.1-flash-image"}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(stream)),
	}
	params := newGeminiImageUsageParams("gemini-3.1-flash-image", "streamGenerateContent", "2K", []byte(`{"contents":[{"parts":[{"text":"draw"}]}]}`))

	result, err := svc.handleNativeStreamingResponse(c, resp, time.Now(), false, params)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 2091, result.usage.OutputTokens)
	require.Equal(t, 1680, result.usage.ImageOutputTokens)
	require.Contains(t, w.Body.String(), `"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1680}]`)
	require.Contains(t, w.Body.String(), `"promptTokensDetails":[{"modality":"TEXT","tokenCount":20}]`)
	require.Contains(t, w.Body.String(), `"serviceTier":"standard"`)
}

func TestRepairedGemini31FlashImageUsageBillingDoesNotDoubleCountImageTokens(t *testing.T) {
	_, usage, repaired := repairGemini31FlashImageUsage(
		[]byte(flashUsageMissingImageDetail),
		"gemini-3.1-flash-image",
		"2K",
		geminiFlashUsageRepairOptions{},
	)
	require.True(t, repaired)

	svc := &BillingService{}
	pricing := &ModelPricing{
		OutputPricePerToken:      3e-6,
		ImageOutputPricePerToken: 60e-6,
		ImageOutputPriceExplicit: true,
	}
	breakdown := svc.computeTokenBreakdown(pricing, UsageTokens{
		InputTokens:       usage.InputTokens,
		OutputTokens:      usage.OutputTokens,
		ImageOutputTokens: usage.ImageOutputTokens,
	}, 1, "", false)

	require.InDelta(t, 411*3e-6, breakdown.OutputCost, 1e-12)
	require.InDelta(t, 1680*60e-6, breakdown.ImageOutputCost, 1e-12)
	require.InDelta(t, breakdown.OutputCost+breakdown.ImageOutputCost, breakdown.TotalCost, 1e-12)
}
