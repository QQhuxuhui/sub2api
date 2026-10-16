package service

import "strings"

// openAIAccountMappedRequestBillingModel 在 OpenAI 平台账号的 model_mapping 把请求
// 改了名时，返回改名前的模型（即客户端请求、经渠道映射后的名字），让计费按客户
// 请求的模型走，而不是按账号映射到的上游模型（例如 gpt-5.6-sol → gpt-5.6-luna
// 时按 sol 计价）。与响应 model 强制覆盖成客户端请求名是同一口径。
//
// 只在 result.BillingModel 恰好是账号映射结果时生效：生图（tools 里的图片模型）、
// /v1/messages 默认映射等由其他逻辑决定的计费模型保持不变。返回空串表示不干预。
// 改名前的模型没有定价时，计费候选列表里仍保留映射后的模型作为兜底。
func openAIAccountMappedRequestBillingModel(account *Account, result *OpenAIForwardResult) string {
	if account == nil || account.Platform != PlatformOpenAI || result == nil {
		return ""
	}
	requested := strings.TrimSpace(result.Model)
	billing := strings.TrimSpace(result.BillingModel)
	if requested == "" || billing == "" || requested == billing {
		return ""
	}
	mapped, matched := account.ResolveMappedModel(requested)
	if !matched || strings.TrimSpace(mapped) != billing {
		return ""
	}
	return requested
}
