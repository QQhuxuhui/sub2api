package service

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// gpt-image-2.5（flare/sunburst）纳入 usage 模拟：同一网格公式、五档 quality，
// 网格长边 16/24/48/64/96 由官方公开的 1024×1024 五档数值反推并逐个吻合。

func TestOpenAIImagesModelFamily(t *testing.T) {
	cases := map[string]string{
		"gpt-image-2":                       openAIImagesFamilyV2,
		"gpt-image-2-2026-04-21":            openAIImagesFamilyV2,
		"gpt-image-2.5":                     openAIImagesFamilyV25,
		" GPT-IMAGE-2.5-FLARE ":             openAIImagesFamilyV25,
		"gpt-image-2.5-sunburst":            openAIImagesFamilyV25,
		"gpt-image-2.5-flare-2026-09-08":    openAIImagesFamilyV25,
		"gpt-image-2.5-sunburst-2026-09-08": openAIImagesFamilyV25,
		"gpt-image-2.5-codex":               "",
		"gpt-image-2-codex":                 "",
		"gpt-image-3":                       "",
		"":                                  "",
	}
	for model, want := range cases {
		got, ok := openAIImagesModelFamily(model)
		if got != want || ok != (want != "") {
			t.Errorf("openAIImagesModelFamily(%q) = %q,%v want %q", model, got, ok, want)
		}
		if isSimulatableOpenAIImagesModel(model) != (want != "") || isGPTImage2Model(model) != (want != "") {
			t.Errorf("gates disagree with family for %q", model)
		}
	}
}

func TestNormalizeOpenAIImageQualityForFamily(t *testing.T) {
	type tc struct {
		family, raw, want string
		ok                bool
	}
	cases := []tc{
		{openAIImagesFamilyV2, "xhigh", "", false},
		{openAIImagesFamilyV2, "max", "", false},
		{openAIImagesFamilyV2, "high", "high", true},
		{openAIImagesFamilyV25, "", "low", true},
		{openAIImagesFamilyV25, "auto", "low", true},
		{openAIImagesFamilyV25, "medium", "medium", true},
		{openAIImagesFamilyV25, " XHIGH ", "xhigh", true},
		{openAIImagesFamilyV25, "max", "max", true},
		{openAIImagesFamilyV25, "hd", "", false},
	}
	for _, c := range cases {
		got, ok := normalizeOpenAIImageQualityForFamily(c.family, c.raw)
		if got != c.want || ok != c.ok {
			t.Errorf("family %s raw %q = %q,%v want %q,%v", c.family, c.raw, got, ok, c.want, c.ok)
		}
	}
}

// 官方公开的 2.5 @1024×1024 五档：low 196 / medium 439 / high 1756 / xhigh 3122 / max 7024。
func TestOfficialOpenAIImageOutputTokensV25PublishedLadder(t *testing.T) {
	want := map[string]int{"low": 196, "medium": 439, "high": 1756, "xhigh": 3122, "max": 7024}
	for quality, tokens := range want {
		got, ok := officialOpenAIImageOutputTokensForFamily(openAIImagesFamilyV25, 1024, 1024, quality)
		if !ok || got != tokens {
			t.Errorf("2.5 %s @1024² = %d,%v want %d", quality, got, ok, tokens)
		}
	}
	// 2 系不受影响：三档仍是 196/1756/7024，且不认 xhigh/max。
	for quality, tokens := range map[string]int{"low": 196, "medium": 1756, "high": 7024} {
		if got, ok := officialOpenAIImageOutputTokens(1024, 1024, quality); !ok || got != tokens {
			t.Errorf("2 %s @1024² = %d,%v want %d", quality, got, ok, tokens)
		}
	}
	if _, ok := officialOpenAIImageOutputTokensForFamily(openAIImagesFamilyV2, 1024, 1024, "xhigh"); ok {
		t.Error("gpt-image-2 must not accept xhigh")
	}
	// 2.5 的 high == 2 的 medium、2.5 的 max == 2 的 high（同网格长边），换个非方形尺寸也成立。
	for _, pair := range [][2]string{{"high", "medium"}, {"max", "high"}, {"low", "low"}} {
		a, _ := officialOpenAIImageOutputTokensForFamily(openAIImagesFamilyV25, 1536, 1024, pair[0])
		b, _ := officialOpenAIImageOutputTokens(1536, 1024, pair[1])
		if a != b {
			t.Errorf("2.5 %s (%d) should equal 2 %s (%d) at 1536x1024", pair[0], a, pair[1], b)
		}
	}
}

func TestOpenAIImagesForwardQuality(t *testing.T) {
	cases := []struct{ client, upstream, in, want string }{
		{"gpt-image-2.5-flare", "gpt-image-2", "xhigh", "high"},
		{"gpt-image-2.5-sunburst", "gpt-image-2", "max", "high"},
		{"gpt-image-2.5-flare", "gpt-image-2", "medium", "medium"},
		{"gpt-image-2.5-flare", "gpt-image-2.5-flare", "xhigh", "xhigh"},
		{"gpt-image-2", "gpt-image-2", "high", "high"},
		{"gpt-image-2.5-flare", "gpt-image-2-codex", "max", "max"},
	}
	for _, c := range cases {
		if got := openAIImagesForwardQuality(c.client, c.upstream, c.in); got != c.want {
			t.Errorf("forward(%s→%s, %s) = %s want %s", c.client, c.upstream, c.in, got, c.want)
		}
	}
}

func TestOpenAIImagesQualityEchoAcceptable(t *testing.T) {
	if !openAIImagesQualityEchoAcceptable("high", "xhigh") || !openAIImagesQualityEchoAcceptable("HIGH", "max") {
		t.Error("2 系上游回显 high 应视为与 xhigh/max 一致")
	}
	if openAIImagesQualityEchoAcceptable("medium", "xhigh") || openAIImagesQualityEchoAcceptable("high", "medium") || openAIImagesQualityEchoAcceptable("low", "high") {
		t.Error("其余档位必须严格相等")
	}
}

// 端到端：客户端请求 2.5 xhigh，账号把模型映射到 gpt-image-2 上游，上游回显 quality=high，
// 仍按 2.5 的 xhigh 计 3122 token，响应里的 quality 改回 xhigh。
func TestApplyOpenAIImagesUsageSimulationV25MappedToV2Upstream(t *testing.T) {
	body := []byte(`{"created":1,"quality":"high","data":[{"b64_json":"` + testPNGBase64(t, 1024, 1024) + `"}],"usage":{"input_tokens":20,"input_tokens_details":{"text_tokens":20,"image_tokens":0}}}`)
	parsed := &OpenAIImagesRequest{Model: "gpt-image-2.5-flare", Prompt: "x", Quality: "xhigh", N: 1}
	out, usage, _, applied := applyOpenAIImagesUsageSimulation(body, parsed)
	if !applied {
		t.Fatal("expected simulation to apply")
	}
	if usage.ImageOutputTokens != 3122 || usage.InputTokens != 20 {
		t.Fatalf("usage = %+v, want image output 3122 / input 20", usage)
	}
	if got := gjson.GetBytes(out, "quality").String(); got != "xhigh" {
		t.Errorf("echoed quality = %q, want xhigh", got)
	}
	if gjson.GetBytes(out, "model").Exists() {
		t.Error("model must be removed like the official response")
	}

	// 2.5 medium 走 24 网格：1024² = 439，而不是 2 系 medium 的 1756。
	parsed = &OpenAIImagesRequest{Model: "gpt-image-2.5-sunburst", Prompt: "x", Quality: "medium", N: 1}
	_, usage, _, applied = applyOpenAIImagesUsageSimulation(body[:0:0], parsed)
	if applied {
		t.Fatal("empty body must not apply")
	}
	body2 := []byte(`{"data":[{"b64_json":"` + testPNGBase64(t, 1024, 1024) + `"}],"usage":{"input_tokens_details":{"text_tokens":5}}}`)
	_, usage, _, applied = applyOpenAIImagesUsageSimulation(body2, parsed)
	if !applied || usage.ImageOutputTokens != 439 {
		t.Fatalf("2.5 medium usage = %+v applied=%v, want 439", usage, applied)
	}
}

// 请求校验：2.5 客户端模型可传 xhigh/max，即便将接收请求的上游模型是 gpt-image-2；
// 纯 2 系请求仍拒绝 xhigh/max。
func TestNormalizeOpenAIImagesOptionsV25Quality(t *testing.T) {
	req := &OpenAIImagesRequest{Model: "gpt-image-2.5-flare", Prompt: "x", Quality: "xhigh", HasQuality: true, N: 1}
	normalized, err := NormalizeOpenAIImagesRequestForModel(req, "gpt-image-2")
	if err != nil || normalized.Quality != "xhigh" {
		t.Fatalf("2.5 client → 2 upstream: err=%v quality=%q", err, normalized.Quality)
	}
	req = &OpenAIImagesRequest{Model: "gpt-image-2.5-sunburst", Prompt: "x", Quality: "max", HasQuality: true, N: 1}
	if normalized, err = NormalizeOpenAIImagesRequestForModel(req, "gpt-image-2.5-sunburst"); err != nil || normalized.Quality != "max" {
		t.Fatalf("2.5 → 2.5: err=%v quality=%q", err, normalized.Quality)
	}
	req = &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "x", Quality: "xhigh", HasQuality: true, N: 1}
	if _, err = NormalizeOpenAIImagesRequestForModel(req, "gpt-image-2"); err == nil || !strings.Contains(err.Error(), "invalid quality") {
		t.Fatalf("gpt-image-2 must reject xhigh, got %v", err)
	}
}

// 转发体：2.5 客户端 + 2 系上游，JSON 里的 quality 降成 high；模型名换成上游名。
func TestRewriteOpenAIImagesRequestDowngradesV25QualityForV2Upstream(t *testing.T) {
	parsed := &OpenAIImagesRequest{Model: "gpt-image-2.5-flare", Prompt: "x", Quality: "max", HasQuality: true, N: 1}
	body := []byte(`{"model":"gpt-image-2.5-flare","prompt":"x","quality":"max"}`)
	out, _, err := rewriteOpenAIImagesRequest(body, "application/json", "gpt-image-2", parsed)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(out, "quality").String() != "high" || gjson.GetBytes(out, "model").String() != "gpt-image-2" {
		t.Fatalf("rewritten = %s", out)
	}
	out, _, err = rewriteOpenAIImagesRequest(body, "application/json", "gpt-image-2.5-flare", parsed)
	if err != nil || gjson.GetBytes(out, "quality").String() != "max" {
		t.Fatalf("same-family upstream must keep max: %s err=%v", out, err)
	}
}
