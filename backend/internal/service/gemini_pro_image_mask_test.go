package service

import (
	"testing"

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

	s := synthesizeGeminiProImageUsage("4K", 9)
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
	s := synthesizeGeminiProImageUsage("2K", 0)
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

	// 含 usageMetadata 的末块 → 改写 usage + modelVersion
	nb, u, masked := maskGeminiProImageStreamChunk([]byte(flashStrippedBody), mask)
	if !masked || u == nil {
		t.Fatal("chunk with usageMetadata should be masked")
	}
	if gjson.GetBytes(nb, "modelVersion").String() != "gemini-3-pro-image-preview" {
		t.Error("stream chunk modelVersion not rewritten")
	}
	if u.ImageOutputTokens != 1120 {
		t.Errorf("stream billing ImageOutputTokens = %d, want 1120", u.ImageOutputTokens)
	}

	// 仅含 modelVersion、无 usageMetadata 的中间块 → 只改 modelVersion，usage 为 nil
	mid := `{"candidates":[{"content":{"parts":[{"text":"partial"}]}}],"modelVersion":"gemini-3.1-flash-image"}`
	nb2, u2, masked2 := maskGeminiProImageStreamChunk([]byte(mid), mask)
	if !masked2 {
		t.Error("mid chunk with flash modelVersion should be rewritten")
	}
	if u2 != nil {
		t.Error("mid chunk without usageMetadata should not yield billing usage")
	}
	if gjson.GetBytes(nb2, "modelVersion").String() != "gemini-3-pro-image-preview" {
		t.Error("mid chunk modelVersion not rewritten")
	}

	// 未启用 → 原样返回
	_, _, masked3 := maskGeminiProImageStreamChunk([]byte(flashStrippedBody), geminiProImageMaskParams{})
	if masked3 {
		t.Error("disabled mask should not rewrite")
	}
}

func TestApplyGeminiProImageMask(t *testing.T) {
	orig := geminiProImageIntn
	geminiProImageIntn = func(n int) int { return 0 }
	defer func() { geminiProImageIntn = orig }()

	// 映射响应触发
	nb, u, masked := applyGeminiProImageMask([]byte(flashStrippedBody), "gemini-3-pro-image-preview", "2K")
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
	_, _, masked2 := applyGeminiProImageMask([]byte(proRealBody), "gemini-3-pro-image-preview", "2K")
	if masked2 {
		t.Error("genuine pro should not be masked")
	}

	// 非 pro 模型不触发
	_, _, masked3 := applyGeminiProImageMask([]byte(flashStrippedBody), "gemini-3.1-flash-image", "2K")
	if masked3 {
		t.Error("non-pro model should not be masked")
	}
}
