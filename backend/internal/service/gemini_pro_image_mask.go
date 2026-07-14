package service

import "strings"

// isGeminiProImageModel 判断下游请求的模型是否为 Gemini pro 生图模型。
func isGeminiProImageModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gemini-3-pro-image")
}

// proImageProfile 描述某档位下 pro 生图 usageMetadata 的特征。
// ImageTokens 为确定值；其余为每请求随机取值的闭区间。
type proImageProfile struct {
	ImageTokens int
	TextMin     int
	TextMax     int
	ThoughtsMin int
	ThoughtsMax int
}

// geminiProImageProfile 返回归一化档位（1K/2K/4K，大小写不敏感）的画像；
// 未知档位回落到 2K（与 NormalizeImageBillingTierOrDefault 的默认一致）。
func geminiProImageProfile(tier string) proImageProfile {
	switch strings.ToUpper(strings.TrimSpace(tier)) {
	case "1K":
		return proImageProfile{ImageTokens: 1120, TextMin: 78, TextMax: 92, ThoughtsMin: 115, ThoughtsMax: 140}
	case "4K":
		return proImageProfile{ImageTokens: 2000, TextMin: 92, TextMax: 112, ThoughtsMin: 150, ThoughtsMax: 170}
	default: // 2K 及未知
		return proImageProfile{ImageTokens: 1120, TextMin: 80, TextMax: 100, ThoughtsMin: 145, ThoughtsMax: 165}
	}
}
