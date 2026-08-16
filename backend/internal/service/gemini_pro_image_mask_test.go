package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestIsGeminiProImageModel(t *testing.T) {
	cases := map[string]bool{
		"gemini-3-pro-image-preview":   true,
		"gemini-3-pro-image":           true,
		"gemini-3-pro-image-preview-t": true,
		"GEMINI-3-PRO-IMAGE-PREVIEW":   true,
		"gemini-3.1-flash-image":       false,
		"gemini-3-pro":                 false,
		"gemini-2.5-flash-image":       false,
		"":                             false,
	}
	for model, want := range cases {
		if got := isGeminiProImageModel(model); got != want {
			t.Errorf("isGeminiProImageModel(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestGeminiProImageProfile(t *testing.T) {
	if p := geminiProImageProfile("1K"); p.ImageTokens != 1120 {
		t.Errorf("1K ImageTokens = %d, want 1120", p.ImageTokens)
	}
	if p := geminiProImageProfile("2k"); p.ImageTokens != 1120 {
		t.Errorf("2k ImageTokens = %d, want 1120", p.ImageTokens)
	}
	if p := geminiProImageProfile("4K"); p.ImageTokens != 2000 {
		t.Errorf("4K ImageTokens = %d, want 2000", p.ImageTokens)
	}
	// 未知档位回落 2K
	if p := geminiProImageProfile("weird"); p.ImageTokens != 1120 {
		t.Errorf("unknown tier fallback ImageTokens = %d, want 1120 (2K)", p.ImageTokens)
	}
	p := geminiProImageProfile("4K")
	if !(p.TextMin <= p.TextMax && p.ThoughtsMin <= p.ThoughtsMax) {
		t.Errorf("4K ranges invalid: %+v", p)
	}
}

func TestSynthesizeGeminiProImageUsage(t *testing.T) {
	// 注入确定性随机：始终返回 0，使 text/thoughts 取区间下界。
	orig := geminiProImageIntn
	geminiProImageIntn = func(n int) int { return 0 }
	defer func() { geminiProImageIntn = orig }()

	s := synthesizeGeminiProImageUsage("4K", 9, 1)
	if s.ImageTokens != 2000 {
		t.Fatalf("ImageTokens = %d, want 2000", s.ImageTokens)
	}
	if s.TextTokens != 92 || s.ThoughtsTokens != 150 { // 4K 区间下界
		t.Fatalf("Text/Thoughts = %d/%d, want 92/150", s.TextTokens, s.ThoughtsTokens)
	}
	if s.CandidatesTokens != s.ImageTokens+s.TextTokens {
		t.Fatalf("Candidates = %d, want image+text=%d", s.CandidatesTokens, s.ImageTokens+s.TextTokens)
	}
	if s.TotalTokens != s.PromptTokens+s.CandidatesTokens+s.ThoughtsTokens {
		t.Fatalf("Total = %d, want prompt+cand+thoughts=%d", s.TotalTokens, s.PromptTokens+s.CandidatesTokens+s.ThoughtsTokens)
	}
	if s.PromptTokens != 9 {
		t.Fatalf("PromptTokens = %d, want 9", s.PromptTokens)
	}
}

func TestSynthesizeGeminiProImageUsageDefaultPrompt(t *testing.T) {
	s := synthesizeGeminiProImageUsage("2K", 0, 1)
	if s.PromptTokens <= 0 {
		t.Fatalf("PromptTokens = %d, want positive default when upstream missing", s.PromptTokens)
	}
}

func TestSynthToClaudeUsage(t *testing.T) {
	s := geminiSynthUsage{PromptTokens: 9, TextTokens: 92, ImageTokens: 2000, ThoughtsTokens: 150, CandidatesTokens: 2092, TotalTokens: 2251}
	u := synthToClaudeUsage(s)
	if u.ImageOutputTokens != 2000 {
		t.Fatalf("ImageOutputTokens = %d, want 2000", u.ImageOutputTokens)
	}
	if u.OutputTokens != s.CandidatesTokens+s.ThoughtsTokens {
		t.Fatalf("OutputTokens = %d, want cand+thoughts=%d", u.OutputTokens, s.CandidatesTokens+s.ThoughtsTokens)
	}
	if u.InputTokens != 9 {
		t.Fatalf("InputTokens = %d, want 9", u.InputTokens)
	}
}

// 用户上游抹平明细的 flash 响应样本（无 candidatesTokensDetails / thoughts）。
const flashStrippedBody = `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}],"usageMetadata":{"promptTokenCount":13,"candidatesTokenCount":2129,"totalTokenCount":2142},"modelVersion":"gemini-3.1-flash-image","responseId":"abc"}`

// 真 pro 响应样本（modelVersion 匹配 + 有 IMAGE 明细）。
const proRealBody = `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":1218,"totalTokenCount":1388,"promptTokensDetails":[{"modality":"TEXT","tokenCount":8}],"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1120}],"thoughtsTokenCount":162,"serviceTier":"standard"},"modelVersion":"gemini-3-pro-image-preview"}`

func TestShouldMaskGeminiProImage(t *testing.T) {
	if !shouldMaskGeminiProImage([]byte(flashStrippedBody), "gemini-3-pro-image-preview") {
		t.Error("stripped flash response should be masked")
	}
	if shouldMaskGeminiProImage([]byte(proRealBody), "gemini-3-pro-image-preview") {
		t.Error("genuine pro response should NOT be masked")
	}
	// flash 带明细但 modelVersion 仍是 flash → 仍需伪装
	flashWithDetail := `{"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":2063,"totalTokenCount":2071,"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1680}]},"modelVersion":"gemini-3.1-flash-image"}`
	if !shouldMaskGeminiProImage([]byte(flashWithDetail), "gemini-3-pro-image-preview") {
		t.Error("flash-modelVersion response should be masked even with IMAGE detail")
	}
}

func TestMaskGeminiProImageResponseBodyRestoresCandidateIndex(t *testing.T) {
	// flash 经 new-api 的响应：candidate 缺 index（omitempty 丢了 0）。真 pro 会带 index:0。
	body := `{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"data":"AAAA","mimeType":"image/jpeg"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":2073,"totalTokenCount":2081,"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1680}]},"modelVersion":"gemini-3.1-flash-image"}`
	s := geminiSynthUsage{PromptTokens: 8, TextTokens: 90, ImageTokens: 1120, ThoughtsTokens: 155, CandidatesTokens: 1210, TotalTokens: 1373}
	out, ok := maskGeminiProImageResponseBody([]byte(body), "gemini-3-pro-image", s)
	if !ok {
		t.Fatal("mask should succeed")
	}
	idx := gjson.GetBytes(out, "candidates.0.index")
	if !idx.Exists() {
		t.Fatal("masked candidate must carry index to match genuine pro (露馅)")
	}
	if idx.Int() != 0 {
		t.Fatalf("candidates.0.index = %d, want 0", idx.Int())
	}
}

func TestMaskGeminiProImageResponseBodyKeepsExistingCandidateIndex(t *testing.T) {
	body := `{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"data":"AAAA","mimeType":"image/jpeg"}}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":1218,"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1120}]},"modelVersion":"gemini-3.1-flash-image"}`
	s := geminiSynthUsage{PromptTokens: 8, TextTokens: 90, ImageTokens: 1120, ThoughtsTokens: 155, CandidatesTokens: 1210, TotalTokens: 1373}
	out, ok := maskGeminiProImageResponseBody([]byte(body), "gemini-3-pro-image", s)
	if !ok {
		t.Fatal("mask should succeed")
	}
	if gjson.GetBytes(out, "candidates.0.index").Int() != 0 {
		t.Fatal("existing candidate index must be preserved")
	}
}

func TestMaskGeminiProImageResponseBody(t *testing.T) {
	s := geminiSynthUsage{PromptTokens: 13, TextTokens: 95, ImageTokens: 1120, ThoughtsTokens: 155, CandidatesTokens: 1215, TotalTokens: 1383}
	out, ok := maskGeminiProImageResponseBody([]byte(flashStrippedBody), "gemini-3-pro-image-preview", s)
	if !ok {
		t.Fatal("mask should succeed")
	}

	if mv := gjson.GetBytes(out, "modelVersion").String(); mv != "gemini-3-pro-image-preview" {
		t.Errorf("modelVersion = %q, want pro name", mv)
	}
	if img := gjson.GetBytes(out, "usageMetadata.candidatesTokensDetails.0.tokenCount").Int(); img != 1120 {
		t.Errorf("IMAGE detail tokenCount = %d, want 1120", img)
	}
	if mod := gjson.GetBytes(out, "usageMetadata.candidatesTokensDetails.0.modality").String(); mod != "IMAGE" {
		t.Errorf("detail modality = %q, want IMAGE", mod)
	}
	if th := gjson.GetBytes(out, "usageMetadata.thoughtsTokenCount").Int(); th != 155 {
		t.Errorf("thoughtsTokenCount = %d, want 155", th)
	}
	if tier := gjson.GetBytes(out, "usageMetadata.serviceTier").String(); tier != "standard" {
		t.Errorf("serviceTier = %q, want standard", tier)
	}
	if tot := gjson.GetBytes(out, "usageMetadata.totalTokenCount").Int(); tot != 1383 {
		t.Errorf("totalTokenCount = %d, want 1383", tot)
	}
	// 图像数据原样保留
	if data := gjson.GetBytes(out, "candidates.0.content.parts.0.inlineData.data").String(); data != "AAAA" {
		t.Errorf("image data mangled: %q", data)
	}
	// promptTokensDetails 重建为 TEXT
	if pt := gjson.GetBytes(out, "usageMetadata.promptTokensDetails.0.tokenCount").Int(); pt != 13 {
		t.Errorf("promptTokensDetails tokenCount = %d, want 13", pt)
	}
}

func TestMaskGeminiProImageStreamChunk(t *testing.T) {
	orig := geminiProImageIntn
	geminiProImageIntn = func(n int) int { return 0 }
	defer func() { geminiProImageIntn = orig }()

	mask := geminiProImageMaskParams{Enabled: true, Model: "gemini-3-pro-image-preview", Tier: "2K"}

	// 终结分块（有 finishReason + usageMetadata，flash）→ 全量合成 + 改 modelVersion
	flashFinal := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":13,"candidatesTokenCount":2129,"totalTokenCount":2142},"modelVersion":"gemini-3.1-flash-image"}`
	nb, u, masked := maskGeminiProImageStreamChunk([]byte(flashFinal), mask)
	if !masked || u == nil {
		t.Fatal("final flash chunk should be masked with usage")
	}
	if gjson.GetBytes(nb, "modelVersion").String() != "gemini-3-pro-image-preview" {
		t.Error("final chunk modelVersion not rewritten")
	}
	if u.ImageOutputTokens != 1120 {
		t.Errorf("final chunk ImageOutputTokens = %d, want 1120", u.ImageOutputTokens)
	}

	// 中间分块（有 usageMetadata 但无 finishReason，flash）→ 只改 modelVersion，绝不合成 usage
	midWithUsage := `{"candidates":[{"content":{"parts":[{"text":"partial"}]}}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":10,"totalTokenCount":15},"modelVersion":"gemini-3.1-flash-image"}`
	nb2, u2, masked2 := maskGeminiProImageStreamChunk([]byte(midWithUsage), mask)
	if !masked2 {
		t.Error("mid chunk with flash modelVersion should have modelVersion rewritten")
	}
	if u2 != nil {
		t.Error("mid chunk must NOT synthesize billing usage")
	}
	if gjson.GetBytes(nb2, "modelVersion").String() != "gemini-3-pro-image-preview" {
		t.Error("mid chunk modelVersion not rewritten")
	}
	// usageMetadata 必须保持原样（未被合成覆盖）
	if got := gjson.GetBytes(nb2, "usageMetadata.candidatesTokenCount").Int(); got != 10 {
		t.Errorf("mid chunk usageMetadata was altered: candidatesTokenCount = %d, want 10", got)
	}

	// 真 pro 终结分块（finishReason + IMAGE 明细 + pro modelVersion）→ 透传，不改写
	proFinal := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":1218,"totalTokenCount":1388,"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1120}],"thoughtsTokenCount":162},"modelVersion":"gemini-3-pro-image-preview"}`
	_, _, masked3 := maskGeminiProImageStreamChunk([]byte(proFinal), mask)
	if masked3 {
		t.Error("genuine pro final chunk must not be masked")
	}

	// 未启用 → 原样返回
	_, _, masked4 := maskGeminiProImageStreamChunk([]byte(flashFinal), geminiProImageMaskParams{})
	if masked4 {
		t.Error("disabled mask should not rewrite")
	}
}

func TestIsGeminiImageGenerationAction(t *testing.T) {
	cases := map[string]bool{
		"generateContent":       true,
		"streamGenerateContent": true,
		"countTokens":           false,
		"":                      false,
		"embedContent":          false,
	}
	for action, want := range cases {
		if got := isGeminiImageGenerationAction(action); got != want {
			t.Errorf("isGeminiImageGenerationAction(%q) = %v, want %v", action, got, want)
		}
	}
}

func TestApplyGeminiProImageMask(t *testing.T) {
	orig := geminiProImageIntn
	geminiProImageIntn = func(n int) int { return 0 }
	defer func() { geminiProImageIntn = orig }()

	// 映射响应触发
	nb, u, masked := applyGeminiProImageMask([]byte(flashStrippedBody), "gemini-3-pro-image-preview", "2K", 0)
	if !masked {
		t.Fatal("expected masked=true for flash response")
	}
	if u.ImageOutputTokens != 1120 {
		t.Errorf("billing ImageOutputTokens = %d, want 1120", u.ImageOutputTokens)
	}
	// 一致性：改写后 body 用既有解析器提取，应等于计费 usage
	parsed := extractGeminiUsage(nb)
	if parsed.ImageOutputTokens != u.ImageOutputTokens || parsed.OutputTokens != u.OutputTokens {
		t.Errorf("rewritten body usage %+v != billed usage %+v", parsed, u)
	}
	if parsed.InputTokens != u.InputTokens {
		t.Errorf("InputTokens %d != %d", parsed.InputTokens, u.InputTokens)
	}
	if gjson.GetBytes(nb, "modelVersion").String() != "gemini-3-pro-image-preview" {
		t.Error("modelVersion not rewritten")
	}

	// 真 pro 不触发
	_, _, masked2 := applyGeminiProImageMask([]byte(proRealBody), "gemini-3-pro-image-preview", "2K", 0)
	if masked2 {
		t.Error("genuine pro should not be masked")
	}

	// 非 pro 模型不触发
	_, _, masked3 := applyGeminiProImageMask([]byte(flashStrippedBody), "gemini-3.1-flash-image", "2K", 0)
	if masked3 {
		t.Error("non-pro model should not be masked")
	}
}

// pro 伪装的 image token 必须按张数乘。
//
// 上游 v0.1.177 起 ImageCount 改为数真实的内联图片 part（resolveGeminiImageCount），
// 按次计费的分组回 N 张就收 N 份；伪装侧若仍只合成一张图的 token，
// 按 token 计费的分组只收 1 份 —— 同一笔请求在两种计费模式下差 N 倍。
//
// 断言用「N 张恰好是 1 张的 N 倍」而不是硬编码数值：档位表改了这条不会假红，
// 而「忘了乘」仍然必红。text/thoughts 是随机量，所以只断 ImageTokens 与由它派生的差值。
func TestSynthesizeGeminiProImageUsageScalesWithImageCount(t *testing.T) {
	for _, tier := range []string{"1K", "2K", "4K"} {
		one := synthesizeGeminiProImageUsage(tier, 100, 1)
		three := synthesizeGeminiProImageUsage(tier, 100, 3)
		require.Equal(t, one.ImageTokens*3, three.ImageTokens, "%s 档：3 张的 image token 必须是 1 张的 3 倍", tier)
		require.Equal(t, three.ImageTokens, three.CandidatesTokens-three.TextTokens,
			"%s 档：candidates 必须等于乘过张数的 image token 加 text", tier)
		require.Equal(t, three.PromptTokens+three.CandidatesTokens+three.ThoughtsTokens, three.TotalTokens,
			"%s 档：total 必须跟着一起变", tier)
	}

	// 张数缺失/非法一律按 1 张，既是老行为，也避免把整笔 usage 合成成 0。
	base := synthesizeGeminiProImageUsage("2K", 100, 1)
	for _, n := range []int{0, -1} {
		require.Equal(t, base.ImageTokens, synthesizeGeminiProImageUsage("2K", 100, n).ImageTokens,
			"imageCount=%d 必须回落到 1 张", n)
	}
}

// 非流式：完整响应体自带全部图片 part，applyGeminiProImageMask 传 0 时必须自己数出来。
func TestApplyGeminiProImageMaskCountsImagesFromBody(t *testing.T) {
	const (
		model = "gemini-3-pro-image-preview"
		tier  = "2K"
	)
	img := `{"inlineData":{"mimeType":"image/png","data":"iVBORw0KGgo="}}`
	body := func(parts string) []byte {
		return []byte(`{"modelVersion":"gemini-3.1-flash-image","candidates":[{"content":{"parts":[` + parts +
			`]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":7,"totalTokenCount":107}}`)
	}

	_, oneUsage, masked := applyGeminiProImageMask(body(img), model, tier, 0)
	require.True(t, masked)
	require.NotNil(t, oneUsage)

	_, twoUsage, masked := applyGeminiProImageMask(body(img+","+img), model, tier, 0)
	require.True(t, masked)
	require.NotNil(t, twoUsage)

	require.Equal(t, oneUsage.ImageOutputTokens*2, twoUsage.ImageOutputTokens,
		"两张图的响应体必须计出两张的 image token —— 传 0 时要自己数 body，不能默认按 1 张")

	// 显式传入的张数优先于 body 里数出来的：流式终结分块靠这条走累计值。
	_, explicitUsage, masked := applyGeminiProImageMask(body(img), model, tier, 4)
	require.True(t, masked)
	require.Equal(t, oneUsage.ImageOutputTokens*4, explicitUsage.ImageOutputTokens,
		"显式张数必须覆盖 body 里数出来的 1 张")
}

// 流式：终结分块只带 finishReason 与 usage，图片 part 早在前面的分块里到了。
// 所以 maskGeminiProImageStreamChunk 必须吃调用方传入的累计张数，
// 在终结分块自己数只会得到 0（→ 回落 1 张 → 少收 N-1 张的钱）。
func TestMaskGeminiProImageStreamChunkUsesCarriedImageCount(t *testing.T) {
	const model = "gemini-3-pro-image-preview"
	final := []byte(`{"modelVersion":"gemini-3.1-flash-image","candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":7,"totalTokenCount":107}}`)
	require.Equal(t, 0, countGeminiInlineImageOutputs(final),
		"前提：终结分块自身没有图片 part，否则本用例测不到「靠累计值」这件事")

	base := geminiProImageMaskParams{Enabled: true, Model: model, Tier: "2K"}
	_, oneUsage, ok := maskGeminiProImageStreamChunk(final, base.withImageCount(1))
	require.True(t, ok)
	require.NotNil(t, oneUsage)

	_, threeUsage, ok := maskGeminiProImageStreamChunk(final, base.withImageCount(3))
	require.True(t, ok)
	require.Equal(t, oneUsage.ImageOutputTokens*3, threeUsage.ImageOutputTokens,
		"累计张数没被 withImageCount 带进去 —— 流式多图会按 1 张收费")
}
