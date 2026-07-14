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
