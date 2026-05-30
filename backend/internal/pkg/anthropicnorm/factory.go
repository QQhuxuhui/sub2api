package anthropicnorm

// AccountView 是工厂判定渠道类型所需的最小视图，避免反向依赖 service 包。
type AccountView interface {
	NormPlatform() string
	NormType() string
}

// For 按渠道类型与分组开关决定是否启用规范化。
// 命中条件：enabled 且 account 为 Vertex（platform=anthropic & type=service_account）。
// 不命中返回 nil，调用方据此跳过（零接触原有路径）。
func For(acct AccountView, enabled bool) *Normalizer {
	if !enabled || acct == nil {
		return nil
	}
	if acct.NormPlatform() == "anthropic" && acct.NormType() == "service_account" {
		return newNormalizer()
	}
	return nil
}
