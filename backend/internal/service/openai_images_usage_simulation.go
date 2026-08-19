package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	_ "golang.org/x/image/webp"
)

// isSimulatableOpenAIImagesModel only admits model versions covered by the token table.
func isSimulatableOpenAIImagesModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "gpt-image-2", "gpt-image-2-2026-04-21":
		return true
	default:
		return false
	}
}

// openAIImagesRequestSimulatable rejects request shapes not covered by reference measurements.
func openAIImagesRequestSimulatable(parsed *OpenAIImagesRequest) bool {
	if parsed == nil || parsed.Stream || parsed.PartialImages != nil || parsed.N != 1 || parsed.HasMask {
		return false
	}
	if background := strings.ToLower(strings.TrimSpace(parsed.Background)); background != "" && background != "opaque" {
		return false
	}
	if format := strings.ToLower(strings.TrimSpace(parsed.OutputFormat)); format != "" && format != "png" {
		return false
	}
	if parsed.OutputCompression != nil || strings.TrimSpace(parsed.InputFidelity) != "" {
		return false
	}
	if format := strings.ToLower(strings.TrimSpace(parsed.ResponseFormat)); format != "" && format != "b64_json" {
		return false
	}
	return true
}

const (
	openAIImageInputPatchLimit    = 1536
	openAIImageInputUpscaleTarget = 1024
)

// openAIImageInputTokens calculates the official 32-pixel patch count for one input image.
func openAIImageInputTokens(width, height int) int {
	if width <= 0 || height <= 0 {
		return 0
	}
	fWidth, fHeight := float64(width), float64(height)
	if longest := math.Max(fWidth, fHeight); longest < openAIImageInputUpscaleTarget {
		factor := math.Min(2, openAIImageInputUpscaleTarget/longest)
		fWidth *= factor
		fHeight *= factor
	}
	patches := openAIImagePatchCount(fWidth, fHeight)
	if patches > openAIImageInputPatchLimit {
		factor := math.Sqrt(float64(openAIImageInputPatchLimit) / float64(patches))
		fWidth *= factor
		fHeight *= factor
		for openAIImagePatchCount(fWidth, fHeight) > openAIImageInputPatchLimit {
			fWidth *= 0.99
			fHeight *= 0.99
		}
		patches = openAIImagePatchCount(fWidth, fHeight)
	}
	return patches
}

func openAIImagePatchCount(width, height float64) int {
	return int(math.Ceil(width/32)) * int(math.Ceil(height/32))
}

type openAIImageTokenCell struct {
	Size   string
	Low    int
	Medium int
	High   int
}

// Values are measured for the 18 landscape/square cells documented in GPT_IMAGE_2_TOKEN_REFERENCE.md.
var openAIImageTokenTable = map[string]map[string]openAIImageTokenCell{
	"1:1": {
		ImageBillingSize1K: {Size: "1024x1024", Low: 196, Medium: 1756, High: 7024},
		ImageBillingSize2K: {Size: "2048x2048", Low: 397, Medium: 3568, High: 14272},
		ImageBillingSize4K: {Size: "2880x2880", Low: 659, Medium: 5930, High: 23719},
	},
	"5:4": {
		ImageBillingSize1K: {Size: "1120x896", Low: 157, Medium: 1370, High: 5551},
		ImageBillingSize2K: {Size: "2240x1792", Low: 313, Medium: 2743, High: 11115},
		ImageBillingSize4K: {Size: "3200x2560", Low: 530, Medium: 4648, High: 18835},
	},
	"4:3": {
		ImageBillingSize1K: {Size: "1152x864", Low: 144, Medium: 1294, High: 5176},
		ImageBillingSize2K: {Size: "2304x1728", Low: 288, Medium: 2584, High: 10336},
		ImageBillingSize4K: {Size: "3264x2448", Low: 480, Medium: 4316, High: 17264},
	},
	"3:2": {
		ImageBillingSize1K: {Size: "1248x832", Low: 134, Medium: 1167, High: 4667},
		ImageBillingSize2K: {Size: "2496x1664", Low: 271, Medium: 2363, High: 9452},
		ImageBillingSize4K: {Size: "3504x2336", Low: 449, Medium: 3912, High: 15645},
	},
	"16:9": {
		ImageBillingSize1K: {Size: "1280x720", Low: 106, Medium: 947, High: 3787},
		ImageBillingSize2K: {Size: "2560x1440", Low: 205, Medium: 1843, High: 7370},
		ImageBillingSize4K: {Size: "3840x2160", Low: 371, Medium: 3336, High: 13342},
	},
	"21:9": {
		ImageBillingSize1K: {Size: "1456x624", Low: 82, Medium: 733, High: 2863},
		ImageBillingSize2K: {Size: "3024x1296", Low: 166, Medium: 1492, High: 5825},
		ImageBillingSize4K: {Size: "3696x1584", Low: 220, Medium: 1980, High: 7729},
	},
}

type openAIImageGeometry struct {
	Width  int
	Height int
	Ratio  string
	Tier   string
}

var openAIImageSizeIndex = buildOpenAIImageSizeIndex()

func buildOpenAIImageSizeIndex() map[string]openAIImageGeometry {
	transposable := map[string]bool{"5:4": true, "4:3": true, "3:2": true, "16:9": true}
	index := make(map[string]openAIImageGeometry, 30)
	for ratio, tiers := range openAIImageTokenTable {
		for tier, cell := range tiers {
			width, height, ok := parseOpenAIImageWidthHeight(cell.Size)
			if !ok {
				continue
			}
			index[openAIImageDimensionsKey(width, height)] = openAIImageGeometry{
				Width: width, Height: height, Ratio: ratio, Tier: tier,
			}
			if width != height && transposable[ratio] {
				index[openAIImageDimensionsKey(height, width)] = openAIImageGeometry{
					Width: height, Height: width, Ratio: ratio, Tier: tier,
				}
			}
		}
	}
	return index
}

func lookupOpenAIImageSize(width, height int) (openAIImageGeometry, bool) {
	if width <= 0 || height <= 0 {
		return openAIImageGeometry{}, false
	}
	geometry, ok := openAIImageSizeIndex[openAIImageDimensionsKey(width, height)]
	return geometry, ok
}

func openAIImageDimensionsKey(width, height int) string {
	return fmt.Sprintf("%dx%d", width, height)
}

func parseOpenAIImageWidthHeight(value string) (int, int, bool) {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(value)), "x", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	width, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || width <= 0 {
		return 0, 0, false
	}
	height, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

func normalizeOpenAIImageQuality(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto", "low":
		return "low", true
	case "medium":
		return "medium", true
	case "high":
		return "high", true
	default:
		return "", false
	}
}

// officialOpenAIImageOutputTokens reproduces the official gpt-image-2 output-token
// formula. It matches all 54 measured cells in GPT_IMAGE_2_TOKEN_REFERENCE.md §4
// byte-for-byte (verified in TestOfficialOpenAIImageOutputTokens*) and extends
// safely to arbitrary WxH, so it replaces the fixed reference table for token
// counting — off-table sizes no longer collapse to zero output tokens. The
// one-cell lower bound only affects non-official extreme aspect ratios.
//
//	base   = {low:16, medium:48, high:96}
//	other  = max(1, round(base * min(w,h) / max(w,h)))
//	tokens = ceil(base * other * (2e6 + w*h) / 4e6)
func officialOpenAIImageOutputTokens(width, height int, quality string) (int, bool) {
	var base float64
	switch quality {
	case "low":
		base = 16
	case "medium":
		base = 48
	case "high":
		base = 96
	default:
		return 0, false
	}
	if width <= 0 || height <= 0 {
		return 0, false
	}
	shorter := math.Min(float64(width), float64(height))
	longer := math.Max(float64(width), float64(height))
	// A decoded image always consumes at least one token cell on the short axis.
	// Without the lower bound, sufficiently extreme but valid dimensions such as
	// 1000x1 round to zero and silently produce zero output tokens.
	other := math.Max(1, math.Round(base*shorter/longer))
	pixels := float64(width) * float64(height)
	tokens := math.Ceil(base * other * (2e6 + pixels) / 4e6)
	return int(tokens), true
}

// decodeImageMeta streams base64 into DecodeConfig so large images do not require a second raw buffer.
func decodeImageMeta(encoded string) (int, int, string, bool) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return 0, 0, "", false
	}
	// 只比较前 5 字节判断 data: URL，避免对整串（4K 图 b64 可达数 MB）做 ToLower 拷贝与逗号全扫描。
	if len(encoded) >= 5 && strings.EqualFold(encoded[:5], "data:") {
		comma := strings.IndexByte(encoded, ',')
		if comma < 0 {
			// 无逗号的 data: 串不是合法 base64 载荷，旧路径也必然解码失败。
			return 0, 0, "", false
		}
		encoded = encoded[comma+1:]
	}
	decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded))
	config, format, err := image.DecodeConfig(decoder)
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return 0, 0, "", false
	}
	return config.Width, config.Height, format, true
}

func parseOpenAIImagesSimulationResponse(body []byte) (map[string]any, bool) {
	root := make(map[string]any)
	if err := json.Unmarshal(body, &root); err != nil || root == nil {
		return nil, false
	}
	return root, true
}

func decodedJSONInt(root map[string]any, path ...string) (int, bool) {
	var current any = root
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return 0, false
		}
		current, ok = object[segment]
		if !ok {
			return 0, false
		}
	}
	switch value := current.(type) {
	case float64:
		return int(value), true
	case json.Number:
		parsed, err := strconv.ParseInt(value.String(), 10, 64)
		return int(parsed), err == nil
	default:
		return 0, false
	}
}

func openAIUsageFromDecodedImagesResponse(root map[string]any) (OpenAIUsage, bool) {
	usageValue, exists := root["usage"]
	if !exists {
		return OpenAIUsage{}, false
	}
	usageBody, err := json.Marshal(usageValue)
	if err != nil {
		return OpenAIUsage{}, false
	}
	return openAIUsageFromGJSON(gjson.ParseBytes(usageBody))
}

// resolveOpenAIImageGeometry trusts the actual first image; a declared size is only a consistency check.
func resolveOpenAIImageGeometryFromDecoded(root map[string]any) (openAIImageGeometry, string, bool) {
	data, ok := root["data"].([]any)
	if !ok || len(data) == 0 {
		return openAIImageGeometry{}, "", false
	}
	first, ok := data[0].(map[string]any)
	if !ok {
		return openAIImageGeometry{}, "", false
	}
	encoded, ok := first["b64_json"].(string)
	if !ok {
		return openAIImageGeometry{}, "", false
	}
	width, height, format, ok := decodeImageMeta(encoded)
	if !ok {
		return openAIImageGeometry{}, "", false
	}
	// Token counting now uses officialOpenAIImageOutputTokens, which safely extends to
	// any WxH, so the geometry is no longer gated on a reference-table hit. Gating
	// here was what collapsed off-table sizes (e.g. 1536x1024, 3456x2304 from the
	// web-reverse / upscale path) to zero output tokens. Ratio/Tier stay empty;
	// only Width/Height feed the formula. The declared size, when present, is still
	// a consistency check against the decoded pixels.
	if declaredValue, exists := root["size"]; exists {
		declared, validString := declaredValue.(string)
		declaredWidth, declaredHeight, valid := parseOpenAIImageWidthHeight(declared)
		if !validString || !valid || declaredWidth != width || declaredHeight != height {
			return openAIImageGeometry{}, "", false
		}
	}
	return openAIImageGeometry{Width: width, Height: height}, format, true
}

func resolveOpenAIImageGeometry(body []byte) (openAIImageGeometry, string, bool) {
	root, ok := parseOpenAIImagesSimulationResponse(body)
	if !ok {
		return openAIImageGeometry{}, "", false
	}
	return resolveOpenAIImageGeometryFromDecoded(root)
}

// resolveOpenAIImagesInputTokens sums locally readable images and falls back to upstream aggregate usage.
func resolveOpenAIImagesInputTokensFromDecoded(root map[string]any, parsed *OpenAIImagesRequest) (int, bool) {
	if parsed == nil {
		return 0, false
	}
	total := 0
	unknown := false
	for _, imageURL := range parsed.InputImageURLs {
		width, height, _, ok := decodeImageMeta(imageURL)
		if !ok {
			unknown = true
			continue
		}
		total += openAIImageInputTokens(width, height)
	}
	for _, upload := range parsed.Uploads {
		width, height := upload.Width, upload.Height
		if width <= 0 || height <= 0 {
			config, _, err := image.DecodeConfig(bytes.NewReader(upload.Data))
			if err != nil || config.Width <= 0 || config.Height <= 0 {
				unknown = true
				continue
			}
			width, height = config.Width, config.Height
		}
		total += openAIImageInputTokens(width, height)
	}
	if !unknown {
		return total, true
	}
	upstreamUsage, ok := openAIUsageFromDecodedImagesResponse(root)
	if !ok || upstreamUsage.ImageInputTokens <= 0 {
		return 0, false
	}
	return upstreamUsage.ImageInputTokens, true
}

func resolveOpenAIImagesInputTokens(body []byte, parsed *OpenAIImagesRequest) (int, bool) {
	root, ok := parseOpenAIImagesSimulationResponse(body)
	if !ok {
		return 0, false
	}
	return resolveOpenAIImagesInputTokensFromDecoded(root, parsed)
}

type openAIImagesSynthUsage struct {
	TextInputTokens   int
	ImageInputTokens  int
	ImageOutputTokens int
	InputTokens       int
	OutputTokens      int
	TotalTokens       int
}

func synthesizeOpenAIImagesUsage(
	geometry openAIImageGeometry,
	quality string,
	textInputTokens int,
	imageInputTokens int,
) (openAIImagesSynthUsage, bool) {
	imageOutputTokens, ok := officialOpenAIImageOutputTokens(geometry.Width, geometry.Height, quality)
	if !ok {
		return openAIImagesSynthUsage{}, false
	}
	if textInputTokens < 1 {
		textInputTokens = 1
	}
	if imageInputTokens < 0 {
		imageInputTokens = 0
	}
	usage := openAIImagesSynthUsage{
		TextInputTokens:   textInputTokens,
		ImageInputTokens:  imageInputTokens,
		ImageOutputTokens: imageOutputTokens,
	}
	usage.InputTokens = usage.TextInputTokens + usage.ImageInputTokens
	usage.OutputTokens = usage.ImageOutputTokens
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	return usage, true
}

func (usage openAIImagesSynthUsage) toOpenAIUsage() OpenAIUsage {
	return OpenAIUsage{
		InputTokens:       usage.InputTokens,
		ImageInputTokens:  usage.ImageInputTokens,
		OutputTokens:      usage.OutputTokens,
		ImageOutputTokens: usage.ImageOutputTokens,
	}
}

// estimateOpenAIImagePromptTokens is a last-resort approximation when upstream text usage is absent.
func estimateOpenAIImagePromptTokens(prompt string) int {
	cjk, other := 0, 0
	for _, r := range prompt {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF:
			cjk++
		case r >= 0x3040 && r <= 0x30FF:
			cjk++
		case r >= 0xAC00 && r <= 0xD7AF:
			cjk++
		default:
			other++
		}
	}
	if tokens := cjk + other/4; tokens >= 1 {
		return tokens
	}
	return 1
}

func rewriteOpenAIImagesResponseBody(
	body []byte,
	quality string,
	format string,
	usage openAIImagesSynthUsage,
	geometry openAIImageGeometry,
) ([]byte, bool) {
	root, ok := parseOpenAIImagesSimulationResponse(body)
	if !ok {
		return body, false
	}
	return rewriteDecodedOpenAIImagesResponseBody(body, root, quality, format, usage, geometry)
}

func rewriteDecodedOpenAIImagesResponseBody(
	body []byte,
	root map[string]any,
	quality string,
	format string,
	usage openAIImagesSynthUsage,
	geometry openAIImageGeometry,
) ([]byte, bool) {
	if root == nil {
		return body, false
	}

	root["usage"] = map[string]any{
		"input_tokens": usage.InputTokens,
		"input_tokens_details": map[string]any{
			"image_tokens": usage.ImageInputTokens,
			"text_tokens":  usage.TextInputTokens,
		},
		"output_tokens": usage.OutputTokens,
		"output_tokens_details": map[string]any{
			"image_tokens": usage.ImageOutputTokens,
			"text_tokens":  0,
		},
		"total_tokens": usage.TotalTokens,
	}
	if _, exists := root["background"]; !exists {
		root["background"] = "opaque"
	}
	if _, exists := root["output_format"]; !exists {
		resolvedFormat := strings.ToLower(strings.TrimSpace(format))
		if resolvedFormat == "" {
			resolvedFormat = "png"
		}
		root["output_format"] = resolvedFormat
	}
	root["quality"] = quality
	root["size"] = openAIImageDimensionsKey(geometry.Width, geometry.Height)
	// 官方 images 响应不返回 model 字段（已与官方直连逐字段比对确认），
	// 上游若回显了它（adobe 的 gpt-image-2-codex 之类）一并删掉，保持结构同构。
	delete(root, "model")
	if data, ok := root["data"].([]any); ok {
		for _, item := range data {
			if imageData, ok := item.(map[string]any); ok {
				delete(imageData, "revised_prompt")
			}
		}
	}
	out, err := json.Marshal(root)
	if err != nil {
		return body, false
	}
	return out, true
}

func openAIImagesResponseSimulatableFromDecoded(root map[string]any, actualFormat, expectedQuality string) bool {
	if !strings.EqualFold(strings.TrimSpace(actualFormat), "png") {
		return false
	}
	if backgroundValue, exists := root["background"]; exists {
		background, ok := backgroundValue.(string)
		if !ok || !strings.EqualFold(strings.TrimSpace(background), "opaque") {
			return false
		}
	}
	if outputFormatValue, exists := root["output_format"]; exists {
		outputFormat, ok := outputFormatValue.(string)
		if !ok || !strings.EqualFold(strings.TrimSpace(outputFormat), "png") {
			return false
		}
	}
	if qualityValue, exists := root["quality"]; exists {
		quality, ok := qualityValue.(string)
		if !ok || !strings.EqualFold(strings.TrimSpace(quality), expectedQuality) {
			return false
		}
	}
	if modelValue, exists := root["model"]; exists {
		model, ok := modelValue.(string)
		if !ok || !isSimulatableOpenAIImagesModel(model) {
			return false
		}
	}
	if usage, ok := openAIUsageFromDecodedImagesResponse(root); ok &&
		(usage.CacheReadInputTokens > 0 || usage.CacheCreationInputTokens > 0) {
		return false
	}
	return true
}

func openAIImagesResponseSimulatable(body []byte, actualFormat, expectedQuality string) bool {
	root, ok := parseOpenAIImagesSimulationResponse(body)
	return ok && openAIImagesResponseSimulatableFromDecoded(root, actualFormat, expectedQuality)
}

// applyOpenAIImagesUsageSimulation 返回改写后的响应体、合成 usage、真实出图尺寸（"WxH"）与是否生效。
// 出图尺寸取自解码得到的真实像素，供计费档位归类与用量日志使用（响应体本身保持官方字段结构）。
func applyOpenAIImagesUsageSimulation(
	body []byte,
	parsed *OpenAIImagesRequest,
) ([]byte, OpenAIUsage, string, bool) {
	if len(body) == 0 || !openAIImagesRequestSimulatable(parsed) {
		return body, OpenAIUsage{}, "", false
	}
	quality, ok := normalizeOpenAIImageQuality(parsed.Quality)
	if !ok {
		return body, OpenAIUsage{}, "", false
	}
	root, ok := parseOpenAIImagesSimulationResponse(body)
	if !ok {
		return body, OpenAIUsage{}, "", false
	}
	geometry, format, ok := resolveOpenAIImageGeometryFromDecoded(root)
	if !ok || !openAIImagesResponseSimulatableFromDecoded(root, format, quality) {
		return body, OpenAIUsage{}, "", false
	}
	data, ok := root["data"].([]any)
	if !ok || len(data) != 1 {
		return body, OpenAIUsage{}, "", false
	}
	imageInputTokens, ok := resolveOpenAIImagesInputTokensFromDecoded(root, parsed)
	if !ok {
		return body, OpenAIUsage{}, "", false
	}

	textInputTokens, exists := decodedJSONInt(root, "usage", "input_tokens_details", "text_tokens")
	if !exists {
		textInputTokens, _ = decodedJSONInt(root, "usage", "prompt_tokens_details", "text_tokens")
	}
	textInputTokens = max(textInputTokens, 0)
	if textInputTokens == 0 {
		// 二级兜底只做同口径运算：纯生图时上游聚合值即全文本；带输入图时仅当上游
		// 自带 image_tokens 明细，才用「上游聚合 − 上游图片明细」。绝不与本地 patch
		// 求和值相减——上游聚合里可能内嵌伪造的图片 token（如 adobe2api 写死的 300），
		// 混合口径会把假数据当成文本 token 计入。
		if upstreamUsage, found := openAIUsageFromDecodedImagesResponse(root); found {
			hasInputImages := len(parsed.InputImageURLs) > 0 || len(parsed.Uploads) > 0
			switch {
			case !hasInputImages && upstreamUsage.InputTokens > 0:
				textInputTokens = upstreamUsage.InputTokens
			case hasInputImages && upstreamUsage.ImageInputTokens > 0 && upstreamUsage.InputTokens > upstreamUsage.ImageInputTokens:
				textInputTokens = upstreamUsage.InputTokens - upstreamUsage.ImageInputTokens
			}
		}
	}
	if textInputTokens == 0 {
		textInputTokens = estimateOpenAIImagePromptTokens(parsed.Prompt)
	}

	synthesized, ok := synthesizeOpenAIImagesUsage(geometry, quality, textInputTokens, imageInputTokens)
	if !ok {
		return body, OpenAIUsage{}, "", false
	}
	rewritten, ok := rewriteDecodedOpenAIImagesResponseBody(body, root, quality, format, synthesized, geometry)
	if !ok {
		return body, OpenAIUsage{}, "", false
	}
	return rewritten, synthesized.toOpenAIUsage(), openAIImageDimensionsKey(geometry.Width, geometry.Height), true
}

func maybeSimulateOpenAIImagesUsage(
	body []byte,
	account *Account,
	parsed *OpenAIImagesRequest,
	effectiveUpstreamModel string,
) ([]byte, OpenAIUsage, string, bool) {
	if !account.SupportsOpenAIImagesUsageSimulation() || parsed == nil ||
		!isSimulatableOpenAIImagesModel(parsed.Model) || !isSimulatableOpenAIImagesModel(effectiveUpstreamModel) {
		return body, OpenAIUsage{}, "", false
	}
	return applyOpenAIImagesUsageSimulation(body, parsed)
}
