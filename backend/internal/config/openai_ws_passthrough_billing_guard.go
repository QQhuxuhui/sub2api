package config

import "log/slog"

// openAIWSPassthroughBillingGapWarning explains why enabling the WebSocket
// mode router needs care: accounts resolved to passthrough mode settle a turn
// only when the upstream terminal event is seen, so a client that is rejected
// locally or disconnects mid-turn leaves that turn unbilled. ctx_pool and
// http_bridge read the upstream to the terminal event and are not affected.
// Design notes for the fix live in the dev-loop work directory
// ws-passthrough-midturn-billing-v3 (local only).
const openAIWSPassthroughBillingGapWarning = "gateway.openai_ws.mode_router_v2_enabled is on: accounts in passthrough mode do not bill a turn interrupted by a client disconnect or a locally rejected frame; keep accounts off passthrough until this is fixed"

// warnOpenAIWSPassthroughBillingGap logs a warning when the mode router is
// enabled, because only then can an account resolve to passthrough mode.
func warnOpenAIWSPassthroughBillingGap(c *Config, logger *slog.Logger) {
	if c == nil || !c.Gateway.OpenAIWS.ModeRouterV2Enabled {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn(openAIWSPassthroughBillingGapWarning,
		"ingress_mode_default", c.Gateway.OpenAIWS.IngressModeDefault)
}
