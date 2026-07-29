package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestAccountSupportsOpenAIImagesUsageSimulation(t *testing.T) {
	var nilAccount *Account
	if nilAccount.SupportsOpenAIImagesUsageSimulation() {
		t.Fatal("nil account must not enable usage simulation")
	}

	tests := []struct {
		name        string
		credentials map[string]any
		want        bool
	}{
		{name: "no credentials", credentials: nil},
		{name: "flag absent", credentials: map[string]any{"api_key": "sk-test"}},
		{name: "bool true", credentials: map[string]any{"openai_images_usage_simulation": true}, want: true},
		{name: "bool false", credentials: map[string]any{"openai_images_usage_simulation": false}},
		{name: "string true", credentials: map[string]any{"openai_images_usage_simulation": "true"}, want: true},
		{name: "string enabled", credentials: map[string]any{"openai_images_usage_simulation": " enabled "}, want: true},
		{name: "string false", credentials: map[string]any{"openai_images_usage_simulation": "false"}},
		{name: "float one", credentials: map[string]any{"openai_images_usage_simulation": float64(1)}, want: true},
		{name: "float zero", credentials: map[string]any{"openai_images_usage_simulation": float64(0)}},
		{name: "int one", credentials: map[string]any{"openai_images_usage_simulation": 1}, want: true},
		{name: "int64 one", credentials: map[string]any{"openai_images_usage_simulation": int64(1)}, want: true},
		{name: "json number one", credentials: map[string]any{"openai_images_usage_simulation": json.Number("1")}, want: true},
		{name: "unsupported type", credentials: map[string]any{"openai_images_usage_simulation": []string{"true"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{Credentials: tt.credentials}
			if got := account.SupportsOpenAIImagesUsageSimulation(); got != tt.want {
				t.Fatalf("SupportsOpenAIImagesUsageSimulation() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsSimulatableOpenAIImagesModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{model: "gpt-image-2", want: true},
		{model: " GPT-IMAGE-2 ", want: true},
		{model: "gpt-image-2-2026-04-21", want: true},
		{model: "gpt-image-2-2027-01-01"},
		{model: "gpt-image-2-codex"},
		{model: "gpt-image-2-"},
		{model: "gpt-image-1"},
		{model: "gpt-image-3"},
		{model: "grok-imagine"},
		{model: "grok-imagine-edit"},
		{model: ""},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := isSimulatableOpenAIImagesModel(tt.model); got != tt.want {
				t.Fatalf("isSimulatableOpenAIImagesModel(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestOpenAIImagesRequestSimulatable(t *testing.T) {
	one := 1
	compression := 80
	clean := func() *OpenAIImagesRequest {
		return &OpenAIImagesRequest{Model: "gpt-image-2", N: 1}
	}

	if openAIImagesRequestSimulatable(nil) {
		t.Fatal("nil request must not be simulatable")
	}
	if !openAIImagesRequestSimulatable(clean()) {
		t.Fatal("clean request should be simulatable")
	}
	explicitB64 := clean()
	explicitB64.ResponseFormat = " B64_JSON "
	if !openAIImagesRequestSimulatable(explicitB64) {
		t.Fatal("explicit b64_json should be simulatable")
	}
	explicitDefaults := clean()
	explicitDefaults.Background = "opaque"
	explicitDefaults.OutputFormat = "png"
	explicitDefaults.ResponseFormat = "b64_json"
	if !openAIImagesRequestSimulatable(explicitDefaults) {
		t.Fatal("explicit opaque/png/b64_json should be simulatable")
	}

	tests := []struct {
		name   string
		mutate func(*OpenAIImagesRequest)
	}{
		{name: "stream", mutate: func(r *OpenAIImagesRequest) { r.Stream = true }},
		{name: "partial images", mutate: func(r *OpenAIImagesRequest) { r.PartialImages = &one }},
		{name: "zero images", mutate: func(r *OpenAIImagesRequest) { r.N = 0 }},
		{name: "multiple images", mutate: func(r *OpenAIImagesRequest) { r.N = 2 }},
		{name: "mask", mutate: func(r *OpenAIImagesRequest) { r.HasMask = true }},
		{name: "transparent", mutate: func(r *OpenAIImagesRequest) { r.Background = "transparent" }},
		{name: "jpeg", mutate: func(r *OpenAIImagesRequest) { r.OutputFormat = "jpeg" }},
		{name: "compression", mutate: func(r *OpenAIImagesRequest) { r.OutputCompression = &compression }},
		{name: "input fidelity", mutate: func(r *OpenAIImagesRequest) { r.InputFidelity = "high" }},
		{name: "url response", mutate: func(r *OpenAIImagesRequest) { r.ResponseFormat = "url" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := clean()
			tt.mutate(req)
			if openAIImagesRequestSimulatable(req) {
				t.Fatalf("%s request must not be simulatable", tt.name)
			}
		})
	}
}

func TestOpenAIImageInputTokensMeasuredSamples(t *testing.T) {
	tests := []struct {
		width  int
		height int
		want   int
	}{
		{width: 256, height: 256, want: 256},
		{width: 512, height: 512, want: 1024},
		{width: 512, height: 1024, want: 512},
		{width: 550, height: 368, want: 704},
		{width: 768, height: 768, want: 1024},
		{width: 1024, height: 1024, want: 1024},
		{width: 1280, height: 720, want: 920},
		{width: 1536, height: 1536, want: 1521},
		{width: 2048, height: 1152, want: 1508},
		{width: 2048, height: 2048, want: 1521},
		{width: 3840, height: 2160, want: 1508},
	}

	for _, tt := range tests {
		if got := openAIImageInputTokens(tt.width, tt.height); got != tt.want {
			t.Errorf("openAIImageInputTokens(%d, %d) = %d, want %d", tt.width, tt.height, got, tt.want)
		}
	}
	for _, dimensions := range [][2]int{{0, 100}, {100, 0}, {-1, -1}} {
		if got := openAIImageInputTokens(dimensions[0], dimensions[1]); got != 0 {
			t.Errorf("openAIImageInputTokens(%d, %d) = %d, want 0", dimensions[0], dimensions[1], got)
		}
	}
}

type measuredOpenAIImageTokenRow struct {
	ratio        string
	tier         string
	width        int
	height       int
	low          int
	medium       int
	high         int
	transposable bool
}

var measuredOpenAIImageTokenRows = []measuredOpenAIImageTokenRow{
	{ratio: "1:1", tier: ImageBillingSize1K, width: 1024, height: 1024, low: 196, medium: 1756, high: 7024},
	{ratio: "1:1", tier: ImageBillingSize2K, width: 2048, height: 2048, low: 397, medium: 3568, high: 14272},
	{ratio: "1:1", tier: ImageBillingSize4K, width: 2880, height: 2880, low: 659, medium: 5930, high: 23719},
	{ratio: "5:4", tier: ImageBillingSize1K, width: 1120, height: 896, low: 157, medium: 1370, high: 5551, transposable: true},
	{ratio: "5:4", tier: ImageBillingSize2K, width: 2240, height: 1792, low: 313, medium: 2743, high: 11115, transposable: true},
	{ratio: "5:4", tier: ImageBillingSize4K, width: 3200, height: 2560, low: 530, medium: 4648, high: 18835, transposable: true},
	{ratio: "4:3", tier: ImageBillingSize1K, width: 1152, height: 864, low: 144, medium: 1294, high: 5176, transposable: true},
	{ratio: "4:3", tier: ImageBillingSize2K, width: 2304, height: 1728, low: 288, medium: 2584, high: 10336, transposable: true},
	{ratio: "4:3", tier: ImageBillingSize4K, width: 3264, height: 2448, low: 480, medium: 4316, high: 17264, transposable: true},
	{ratio: "3:2", tier: ImageBillingSize1K, width: 1248, height: 832, low: 134, medium: 1167, high: 4667, transposable: true},
	{ratio: "3:2", tier: ImageBillingSize2K, width: 2496, height: 1664, low: 271, medium: 2363, high: 9452, transposable: true},
	{ratio: "3:2", tier: ImageBillingSize4K, width: 3504, height: 2336, low: 449, medium: 3912, high: 15645, transposable: true},
	{ratio: "16:9", tier: ImageBillingSize1K, width: 1280, height: 720, low: 106, medium: 947, high: 3787, transposable: true},
	{ratio: "16:9", tier: ImageBillingSize2K, width: 2560, height: 1440, low: 205, medium: 1843, high: 7370, transposable: true},
	{ratio: "16:9", tier: ImageBillingSize4K, width: 3840, height: 2160, low: 371, medium: 3336, high: 13342, transposable: true},
	{ratio: "21:9", tier: ImageBillingSize1K, width: 1456, height: 624, low: 82, medium: 733, high: 2863},
	{ratio: "21:9", tier: ImageBillingSize2K, width: 3024, height: 1296, low: 166, medium: 1492, high: 5825},
	{ratio: "21:9", tier: ImageBillingSize4K, width: 3696, height: 1584, low: 220, medium: 1980, high: 7729},
}

func TestLookupOpenAIImageSizeCoversKnownThirtyEntries(t *testing.T) {
	checked := 0
	for _, row := range measuredOpenAIImageTokenRows {
		assertGeometry := func(width, height int) {
			t.Helper()
			geometry, ok := lookupOpenAIImageSize(width, height)
			if !ok {
				t.Fatalf("lookupOpenAIImageSize(%d, %d) not found", width, height)
			}
			if geometry.Width != width || geometry.Height != height || geometry.Ratio != row.ratio || geometry.Tier != row.tier {
				t.Errorf("lookupOpenAIImageSize(%d, %d) = %+v, want ratio=%s tier=%s", width, height, geometry, row.ratio, row.tier)
			}
			checked++
		}
		assertGeometry(row.width, row.height)
		if row.transposable {
			assertGeometry(row.height, row.width)
		}
	}
	if checked != 30 {
		t.Fatalf("checked %d known output sizes, want 30", checked)
	}
	if got := len(openAIImageSizeIndex); got != 30 {
		t.Fatalf("size index has %d entries, want 30", got)
	}
}

func TestLookupOpenAIImageSizeRejectsUnknownDimensions(t *testing.T) {
	for _, dimensions := range [][2]int{
		{1254, 1254},
		{1672, 941},
		{1000, 100},
		{1023, 1023},
		{624, 1456},
		{0, 0},
		{-1, 10},
	} {
		if _, ok := lookupOpenAIImageSize(dimensions[0], dimensions[1]); ok {
			t.Errorf("lookupOpenAIImageSize(%d, %d) unexpectedly matched", dimensions[0], dimensions[1])
		}
	}
}

func TestNormalizeOpenAIImageQuality(t *testing.T) {
	tests := []struct {
		raw  string
		want string
		ok   bool
	}{
		{raw: "", want: "low", ok: true},
		{raw: "auto", want: "low", ok: true},
		{raw: " AUTO ", want: "low", ok: true},
		{raw: "low", want: "low", ok: true},
		{raw: "medium", want: "medium", ok: true},
		{raw: " High ", want: "high", ok: true},
		{raw: "hd"},
		{raw: "4k"},
		{raw: "ultra"},
	}
	for _, tt := range tests {
		got, ok := normalizeOpenAIImageQuality(tt.raw)
		if ok != tt.ok || got != tt.want {
			t.Errorf("normalizeOpenAIImageQuality(%q) = %q, %v, want %q, %v", tt.raw, got, ok, tt.want, tt.ok)
		}
	}
}

func TestOpenAIImageOutputTokensAllMeasuredCells(t *testing.T) {
	qualities := []struct {
		name string
		get  func(measuredOpenAIImageTokenRow) int
	}{
		{name: "low", get: func(row measuredOpenAIImageTokenRow) int { return row.low }},
		{name: "medium", get: func(row measuredOpenAIImageTokenRow) int { return row.medium }},
		{name: "high", get: func(row measuredOpenAIImageTokenRow) int { return row.high }},
	}
	checked := 0
	for _, row := range measuredOpenAIImageTokenRows {
		for _, quality := range qualities {
			got, ok := openAIImageOutputTokens(row.ratio, row.tier, quality.name)
			want := quality.get(row)
			if !ok || got != want {
				t.Errorf("openAIImageOutputTokens(%q, %q, %q) = %d, %v, want %d, true", row.ratio, row.tier, quality.name, got, ok, want)
			}
			checked++
		}
	}
	if checked != 54 {
		t.Fatalf("checked %d measured token cells, want 54", checked)
	}
}

func TestOpenAIImageOutputTokensRejectsUnknownCell(t *testing.T) {
	for _, input := range [][3]string{
		{"7:3", ImageBillingSize1K, "low"},
		{"1:1", "8K", "low"},
		{"1:1", ImageBillingSize1K, "ultra"},
	} {
		if _, ok := openAIImageOutputTokens(input[0], input[1], input[2]); ok {
			t.Errorf("openAIImageOutputTokens(%q, %q, %q) unexpectedly matched", input[0], input[1], input[2])
		}
	}
}

func testPNGBase64(t *testing.T, width, height int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buffer.Bytes())
}

func testJPEGBase64(t *testing.T, width, height int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buffer.Bytes())
}

func TestDecodeImageMeta(t *testing.T) {
	encoded := testPNGBase64(t, 1280, 720)
	width, height, format, ok := decodeImageMeta(encoded)
	if !ok || width != 1280 || height != 720 || format != "png" {
		t.Fatalf("decodeImageMeta() = %d, %d, %q, %v", width, height, format, ok)
	}
	dataURL := "data:image/png;base64," + encoded
	width, height, format, ok = decodeImageMeta(dataURL)
	if !ok || width != 1280 || height != 720 || format != "png" {
		t.Fatalf("decodeImageMeta(data URL) = %d, %d, %q, %v", width, height, format, ok)
	}
	// RFC 2397 scheme 大小写不敏感：大写前缀同样剥离
	width, height, format, ok = decodeImageMeta("DATA:image/png;base64," + encoded)
	if !ok || width != 1280 || height != 720 || format != "png" {
		t.Fatalf("decodeImageMeta(uppercase data URL) = %d, %d, %q, %v", width, height, format, ok)
	}
	for _, invalid := range []string{"", "not-base64!!!", base64.StdEncoding.EncodeToString([]byte("hello")), "data:image/png;base64", "data,xxx"} {
		if _, _, _, ok := decodeImageMeta(invalid); ok {
			t.Errorf("decodeImageMeta(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestResolveOpenAIImageGeometryUsesActualImage(t *testing.T) {
	body := []byte(`{"size":"2048x2048","data":[{"b64_json":"` + testPNGBase64(t, 2048, 2048) + `"}]}`)
	geometry, format, ok := resolveOpenAIImageGeometry(body)
	if !ok || geometry.Ratio != "1:1" || geometry.Tier != ImageBillingSize2K || format != "png" {
		t.Fatalf("resolveOpenAIImageGeometry() = %+v, %q, %v", geometry, format, ok)
	}

	body = []byte(`{"data":[{"b64_json":"` + testPNGBase64(t, 1280, 720) + `"}]}`)
	geometry, format, ok = resolveOpenAIImageGeometry(body)
	if !ok || geometry.Ratio != "16:9" || geometry.Tier != ImageBillingSize1K || format != "png" {
		t.Fatalf("resolveOpenAIImageGeometry() = %+v, %q, %v", geometry, format, ok)
	}
}

func TestResolveOpenAIImageGeometryRejectsUntrustedMetadata(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{
			name: "declared size mismatch",
			body: []byte(`{"size":"2048x2048","data":[{"b64_json":"` + testPNGBase64(t, 1024, 1024) + `"}]}`),
		},
		{name: "declared size with invalid image", body: []byte(`{"size":"2048x2048","data":[{"b64_json":"aGk="}]}`)},
		{name: "unknown actual size", body: []byte(`{"data":[{"b64_json":"` + testPNGBase64(t, 8, 8) + `"}]}`)},
		{name: "url only", body: []byte(`{"size":"1024x1024","data":[{"url":"https://example.com/a.png"}]}`)},
		{name: "empty data", body: []byte(`{"data":[]}`)},
		{name: "invalid json", body: []byte(`not json`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, ok := resolveOpenAIImageGeometry(tt.body); ok {
				t.Fatal("unsupported response must not resolve geometry")
			}
		})
	}
}

func TestResolveOpenAIImagesInputTokens(t *testing.T) {
	uploadBytes, err := base64.StdEncoding.DecodeString(testPNGBase64(t, 2048, 2048))
	if err != nil {
		t.Fatalf("decode upload fixture: %v", err)
	}

	tests := []struct {
		name   string
		body   []byte
		parsed *OpenAIImagesRequest
		want   int
		ok     bool
	}{
		{
			name:   "no input images",
			body:   []byte(`{"usage":{}}`),
			parsed: &OpenAIImagesRequest{N: 1},
			ok:     true,
		},
		{
			name: "local data URLs sum",
			body: []byte(`{"usage":{}}`),
			parsed: &OpenAIImagesRequest{N: 1, InputImageURLs: []string{
				"data:image/png;base64," + testPNGBase64(t, 1024, 1024),
				"data:image/png;base64," + testPNGBase64(t, 1280, 720),
			}},
			want: 1944,
			ok:   true,
		},
		{
			name:   "multipart upload",
			body:   []byte(`{"usage":{}}`),
			parsed: &OpenAIImagesRequest{N: 1, Uploads: []OpenAIImagesUpload{{Data: uploadBytes}}},
			want:   1521,
			ok:     true,
		},
		{
			name: "remote URL uses aggregate for every image",
			body: []byte(`{"usage":{"input_tokens_details":{"image_tokens":2212}}}`),
			parsed: &OpenAIImagesRequest{N: 1, InputImageURLs: []string{
				"https://example.com/a.png",
				"data:image/png;base64," + testPNGBase64(t, 1024, 1024),
			}},
			want: 2212,
			ok:   true,
		},
		{
			name:   "remote URL uses prompt alias",
			body:   []byte(`{"usage":{"prompt_tokens_details":{"image_tokens":1508}}}`),
			parsed: &OpenAIImagesRequest{N: 1, InputImageURLs: []string{"https://example.com/a.png"}},
			want:   1508,
			ok:     true,
		},
		{
			name:   "remote URL without aggregate",
			body:   []byte(`{"usage":{}}`),
			parsed: &OpenAIImagesRequest{N: 1, InputImageURLs: []string{"https://example.com/a.png"}},
		},
		{
			name:   "remote URL with zero aggregate",
			body:   []byte(`{"usage":{"input_tokens_details":{"image_tokens":0}}}`),
			parsed: &OpenAIImagesRequest{N: 1, InputImageURLs: []string{"https://example.com/a.png"}},
		},
		{name: "nil request", body: []byte(`{"usage":{}}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := resolveOpenAIImagesInputTokens(tt.body, tt.parsed)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("resolveOpenAIImagesInputTokens() = %d, %v, want %d, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestSynthesizeOpenAIImagesUsage(t *testing.T) {
	geometry := openAIImageGeometry{Width: 1024, Height: 1024, Ratio: "1:1", Tier: ImageBillingSize1K}

	usage, ok := synthesizeOpenAIImagesUsage(geometry, "low", 15, 0)
	if !ok {
		t.Fatal("expected low usage synthesis to succeed")
	}
	if usage.TextInputTokens != 15 || usage.ImageInputTokens != 0 || usage.InputTokens != 15 {
		t.Errorf("input usage = %+v", usage)
	}
	if usage.ImageOutputTokens != 196 || usage.OutputTokens != 196 || usage.TotalTokens != 211 {
		t.Errorf("output usage = %+v", usage)
	}

	medium, ok := synthesizeOpenAIImagesUsage(geometry, "medium", 10, 2212)
	if !ok || medium.ImageOutputTokens != 1756 || medium.InputTokens != 2222 {
		t.Errorf("medium usage = %+v, %v", medium, ok)
	}
	if medium.TotalTokens != medium.InputTokens+medium.OutputTokens {
		t.Errorf("total invariant broken: %+v", medium)
	}
	billable := medium.toOpenAIUsage()
	if billable.InputTokens != medium.InputTokens || billable.ImageInputTokens != 2212 ||
		billable.OutputTokens != medium.OutputTokens || billable.ImageOutputTokens != 1756 {
		t.Errorf("billable usage = %+v, synth = %+v", billable, medium)
	}

	floor, ok := synthesizeOpenAIImagesUsage(geometry, "low", 0, -1)
	if !ok || floor.TextInputTokens != 1 || floor.ImageInputTokens != 0 {
		t.Errorf("floored usage = %+v, %v", floor, ok)
	}

	unknownGeometry := openAIImageGeometry{Width: 10, Height: 10, Ratio: "7:3", Tier: ImageBillingSize1K}
	if _, ok := synthesizeOpenAIImagesUsage(unknownGeometry, "low", 10, 0); ok {
		t.Error("unknown geometry unexpectedly synthesized usage")
	}
	if _, ok := synthesizeOpenAIImagesUsage(geometry, "ultra", 10, 0); ok {
		t.Error("unknown quality unexpectedly synthesized usage")
	}
}

func TestEstimateOpenAIImagePromptTokens(t *testing.T) {
	tests := []struct {
		prompt string
		want   int
	}{
		{prompt: "变成黑夜", want: 4},
		{prompt: "make it night", want: 3},
		{prompt: "", want: 1},
		{prompt: "abc", want: 1},
	}
	for _, tt := range tests {
		if got := estimateOpenAIImagePromptTokens(tt.prompt); got != tt.want {
			t.Errorf("estimateOpenAIImagePromptTokens(%q) = %d, want %d", tt.prompt, got, tt.want)
		}
	}
}

func TestRewriteOpenAIImagesResponseBody(t *testing.T) {
	encoded := testPNGBase64(t, 8, 8)
	body := []byte(`{"created":1,"model":"gpt-image-2-codex","data":[{"b64_json":"` + encoded + `","revised_prompt":"expanded"}],"usage":{"input_tokens":304,"output_tokens":400,"total_tokens":704}}`)
	geometry := openAIImageGeometry{Width: 2048, Height: 2048, Ratio: "1:1", Tier: ImageBillingSize2K}
	usage, ok := synthesizeOpenAIImagesUsage(geometry, "low", 12, 0)
	if !ok {
		t.Fatal("synthesize fixture usage")
	}

	out, ok := rewriteOpenAIImagesResponseBody(body, "low", "png", usage, geometry)
	if !ok {
		t.Fatal("expected response rewrite to succeed")
	}
	fields := map[string]string{
		"background":    "opaque",
		"output_format": "png",
		"quality":       "low",
		"size":          "2048x2048",
	}
	for path, want := range fields {
		if got := gjson.GetBytes(out, path).String(); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	// 官方 images 响应不返回 model；上游回显的 gpt-image-2-codex 必须被删掉。
	if gjson.GetBytes(out, "model").Exists() {
		t.Errorf("model 字段应被删除，实际 = %q", gjson.GetBytes(out, "model").String())
	}
	numericFields := map[string]int64{
		"usage.input_tokens":                       12,
		"usage.input_tokens_details.image_tokens":  0,
		"usage.input_tokens_details.text_tokens":   12,
		"usage.output_tokens":                      397,
		"usage.output_tokens_details.image_tokens": 397,
		"usage.output_tokens_details.text_tokens":  0,
		"usage.total_tokens":                       409,
	}
	for path, want := range numericFields {
		if got := gjson.GetBytes(out, path).Int(); got != want {
			t.Errorf("%s = %d, want %d", path, got, want)
		}
	}
	if got := gjson.GetBytes(out, "data.0.b64_json").String(); got != encoded {
		t.Error("b64_json content changed")
	}
	if gjson.GetBytes(out, "data.0.revised_prompt").Exists() {
		t.Error("revised_prompt was not removed")
	}
}

func TestRewriteOpenAIImagesResponseBodyPreservesAllowedMetadata(t *testing.T) {
	body := []byte(`{"background":"opaque","output_format":"png","data":[{"b64_json":"abc"}],"usage":{}}`)
	geometry := openAIImageGeometry{Width: 1280, Height: 720, Ratio: "16:9", Tier: ImageBillingSize1K}
	usage, _ := synthesizeOpenAIImagesUsage(geometry, "medium", 20, 0)
	out, ok := rewriteOpenAIImagesResponseBody(body, "medium", "png", usage, geometry)
	if !ok {
		t.Fatal("expected response rewrite to succeed")
	}
	if got := gjson.GetBytes(out, "background").String(); got != "opaque" {
		t.Errorf("background = %q, want opaque", got)
	}
	if got := gjson.GetBytes(out, "output_format").String(); got != "png" {
		t.Errorf("output_format = %q, want png", got)
	}
	if got := gjson.GetBytes(out, "quality").String(); got != "medium" {
		t.Errorf("quality = %q, want medium", got)
	}
	if got := gjson.GetBytes(out, "data.0.b64_json").String(); got != "abc" {
		t.Errorf("b64_json = %q, want abc", got)
	}
}

func TestRewriteOpenAIImagesResponseBodyRejectsInvalidJSON(t *testing.T) {
	geometry := openAIImageGeometry{Width: 1024, Height: 1024, Ratio: "1:1", Tier: ImageBillingSize1K}
	usage, _ := synthesizeOpenAIImagesUsage(geometry, "low", 10, 0)
	for _, body := range [][]byte{[]byte("not json"), []byte(`[]`)} {
		out, ok := rewriteOpenAIImagesResponseBody(body, "low", "png", usage, geometry)
		if ok || !bytes.Equal(out, body) {
			t.Errorf("invalid body must be returned untouched: %q", body)
		}
	}
}

func BenchmarkRewriteOpenAIImagesResponseBody4MiB(b *testing.B) {
	body := []byte(`{"created":1,"data":[{"b64_json":"` + strings.Repeat("A", 4<<20) + `","revised_prompt":"expanded"}],"usage":{"input_tokens":304,"output_tokens":400}}`)
	geometry := openAIImageGeometry{Width: 2048, Height: 2048, Ratio: "1:1", Tier: ImageBillingSize2K}
	usage, ok := synthesizeOpenAIImagesUsage(geometry, "low", 12, 0)
	if !ok {
		b.Fatal("synthesize fixture usage")
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for range b.N {
		if _, ok := rewriteOpenAIImagesResponseBody(body, "low", "png", usage, geometry); !ok {
			b.Fatal("rewrite failed")
		}
	}
}

func TestOpenAIImagesResponseSimulatable(t *testing.T) {
	tests := []struct {
		name   string
		body   []byte
		format string
		want   bool
	}{
		{name: "missing metadata", body: []byte(`{"data":[]}`), format: "png", want: true},
		{name: "supported metadata", body: []byte(`{"background":" OPAQUE ","output_format":"PNG"}`), format: "png", want: true},
		{name: "transparent", body: []byte(`{"background":"transparent"}`), format: "png"},
		{name: "webp field", body: []byte(`{"output_format":"webp"}`), format: "png"},
		{name: "actual jpeg", body: []byte(`{}`), format: "jpeg"},
		{name: "unknown actual format", body: []byte(`{}`), format: ""},
		{name: "cached input", body: []byte(`{"usage":{"input_tokens_details":{"cached_tokens":100}}}`), format: "png"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := openAIImagesResponseSimulatable(tt.body, tt.format, "low"); got != tt.want {
				t.Fatalf("openAIImagesResponseSimulatable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyOpenAIImagesUsageSimulationConsistency(t *testing.T) {
	encoded := testPNGBase64(t, 2048, 2048)
	body := []byte(`{"created":1,"data":[{"b64_json":"` + encoded + `"}],"usage":{"input_tokens":304,"output_tokens":400,"total_tokens":704}}`)
	parsed := &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "fallback text", Size: "2048x2048", N: 1}

	out, usage, _, applied := applyOpenAIImagesUsageSimulation(body, parsed)
	if !applied {
		t.Fatal("expected simulation to apply")
	}
	if usage.ImageOutputTokens != 397 || usage.InputTokens != 304 {
		t.Errorf("usage = %+v, want image output 397 and upstream aggregate input 304", usage)
	}
	parsedUsage, ok := extractOpenAIUsageFromJSONBytes(out)
	if !ok || parsedUsage != usage {
		t.Errorf("response usage = %+v, %v; billed usage = %+v", parsedUsage, ok, usage)
	}
	if got := gjson.GetBytes(out, "usage.input_tokens_details.text_tokens").Int(); got != 304 {
		t.Errorf("text tokens = %d, want aggregate fallback 304", got)
	}
	if got := gjson.GetBytes(out, "data.0.b64_json").String(); got != encoded {
		t.Error("b64_json content changed")
	}
}

func TestApplyOpenAIImagesUsageSimulationUsesTextDetailAndQuality(t *testing.T) {
	body := []byte(`{"data":[{"b64_json":"` + testPNGBase64(t, 1024, 1024) + `"}],"usage":{"input_tokens":999,"input_tokens_details":{"text_tokens":12,"image_tokens":0}}}`)
	parsed := &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "ignored fallback", Quality: "medium", N: 1}
	out, usage, _, applied := applyOpenAIImagesUsageSimulation(body, parsed)
	if !applied || usage.InputTokens != 12 || usage.ImageOutputTokens != 1756 {
		t.Fatalf("usage = %+v, applied = %v", usage, applied)
	}
	if got := gjson.GetBytes(out, "quality").String(); got != "medium" {
		t.Errorf("quality = %q, want medium", got)
	}
}

func TestApplyOpenAIImagesUsageSimulationRejectsUnknownQuality(t *testing.T) {
	body := []byte(`{"data":[{"b64_json":"` + testPNGBase64(t, 1024, 1024) + `"}],"usage":{}}`)
	parsed := &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "x", Quality: "hd", N: 1}
	out, _, _, applied := applyOpenAIImagesUsageSimulation(body, parsed)
	if applied || !bytes.Equal(out, body) {
		t.Fatal("unknown quality must pass through byte-for-byte")
	}
}

func TestApplyOpenAIImagesUsageSimulationTextFallbackKeepsScalesConsistent(t *testing.T) {
	inputPNG := "data:image/png;base64," + testPNGBase64(t, 256, 256) // 本地 patch 公式 = 256 token
	outputPNG := testPNGBase64(t, 1024, 1024)

	t.Run("aggregate only with local images falls back to estimate", func(t *testing.T) {
		// 上游只给聚合 input_tokens（内嵌假图像 300），无任何明细：
		// 不得用「聚合 310 − 本地 256」推出 54 个假文本 token，应退回 prompt 估算。
		body := []byte(`{"data":[{"b64_json":"` + outputPNG + `"}],"usage":{"input_tokens":310}}`)
		parsed := &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "make it night", N: 1,
			InputImageURLs: []string{inputPNG}}
		out, usage, _, applied := applyOpenAIImagesUsageSimulation(body, parsed)
		if !applied {
			t.Fatal("expected simulation to apply")
		}
		if got := gjson.GetBytes(out, "usage.input_tokens_details.text_tokens").Int(); got != 3 {
			t.Errorf("text tokens = %d, want prompt estimate 3", got)
		}
		if usage.ImageInputTokens != 256 || usage.InputTokens != 259 {
			t.Errorf("usage = %+v, want image 256 + text 3", usage)
		}
	})

	t.Run("upstream image detail enables upstream-scale subtraction", func(t *testing.T) {
		// 上游给了 image_tokens 明细：文本 = 聚合 310 − 上游明细 300 = 10（同口径，
		// 假图像 token 被整体消去）；图片输入仍用本地 patch 值 256。
		body := []byte(`{"data":[{"b64_json":"` + outputPNG + `"}],"usage":{"input_tokens":310,"input_tokens_details":{"image_tokens":300}}}`)
		parsed := &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "make it night", N: 1,
			InputImageURLs: []string{inputPNG}}
		out, usage, _, applied := applyOpenAIImagesUsageSimulation(body, parsed)
		if !applied {
			t.Fatal("expected simulation to apply")
		}
		if got := gjson.GetBytes(out, "usage.input_tokens_details.text_tokens").Int(); got != 10 {
			t.Errorf("text tokens = %d, want upstream-scale 10", got)
		}
		if usage.ImageInputTokens != 256 || usage.InputTokens != 266 {
			t.Errorf("usage = %+v, want image 256 + text 10", usage)
		}
	})
}

func TestApplyOpenAIImagesUsageSimulationRejectsUnsupportedResponses(t *testing.T) {
	pngFixture := testPNGBase64(t, 1024, 1024)
	parsed := &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "x", N: 1}
	tests := []struct {
		name string
		body []byte
	}{
		{name: "transparent response", body: []byte(`{"background":"transparent","data":[{"b64_json":"` + pngFixture + `"}],"usage":{}}`)},
		{name: "webp response field", body: []byte(`{"output_format":"webp","data":[{"b64_json":"` + pngFixture + `"}],"usage":{}}`)},
		{name: "actual jpeg", body: []byte(`{"output_format":"png","data":[{"b64_json":"` + testJPEGBase64(t, 1024, 1024) + `"}],"usage":{}}`)},
		{name: "quality mismatch", body: []byte(`{"quality":"high","data":[{"b64_json":"` + pngFixture + `"}],"usage":{}}`)},
		{name: "response model mismatch", body: []byte(`{"model":"gpt-image-1","data":[{"b64_json":"` + pngFixture + `"}],"usage":{}}`)},
		{name: "declared size mismatch", body: []byte(`{"size":"2048x2048","data":[{"b64_json":"` + pngFixture + `"}],"usage":{}}`)},
		{name: "multiple response images", body: []byte(`{"data":[{"b64_json":"` + pngFixture + `"},{"b64_json":"` + pngFixture + `"}],"usage":{}}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, _, applied := applyOpenAIImagesUsageSimulation(tt.body, parsed)
			if applied || !bytes.Equal(out, tt.body) {
				t.Fatal("unsupported response must pass through byte-for-byte")
			}
		})
	}
}

func TestMaybeSimulateOpenAIImagesUsageGates(t *testing.T) {
	body := []byte(`{"data":[{"b64_json":"` + testPNGBase64(t, 1024, 1024) + `"}],"usage":{}}`)
	marked := &Account{Credentials: map[string]any{openAIImagesUsageSimulationCredentialKey: true}}
	clean := func() *OpenAIImagesRequest {
		return &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "x", N: 1}
	}
	if _, usage, _, applied := maybeSimulateOpenAIImagesUsage(body, marked, clean(), "gpt-image-2"); !applied || usage.ImageOutputTokens != 196 {
		t.Fatalf("marked supported request = %+v, %v", usage, applied)
	}

	tests := []struct {
		name    string
		account *Account
		parsed  *OpenAIImagesRequest
	}{
		{name: "nil account", parsed: clean()},
		{name: "unmarked account", account: &Account{Credentials: map[string]any{}}, parsed: clean()},
		{name: "unknown model", account: marked, parsed: &OpenAIImagesRequest{Model: "gpt-image-1", N: 1}},
		{name: "unsupported request", account: marked, parsed: &OpenAIImagesRequest{Model: "gpt-image-2", N: 2}},
		{name: "nil request", account: marked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, _, applied := maybeSimulateOpenAIImagesUsage(body, tt.account, tt.parsed, "gpt-image-2")
			if applied || !bytes.Equal(out, body) {
				t.Fatal("failed gate must pass through body byte-for-byte")
			}
		})
	}
}
