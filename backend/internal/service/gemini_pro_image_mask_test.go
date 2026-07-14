package service

import "testing"

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
