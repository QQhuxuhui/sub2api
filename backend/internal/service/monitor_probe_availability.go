package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// monitorProbeAvailability answers "could this group serve the model right
// now?" by asking the same account selection a real request goes through,
// without reserving a slot: the platform the request resolves to (forced by
// the route, or a composite group's target), the pool of that platform, and
// every candidate filter — schedulability, model support and cooldowns,
// quotas, window cost, RPM, channel pricing restrictions.
type monitorProbeAvailability struct {
	gateway *GatewayService
	openai  *OpenAIGatewayService
}

// NewMonitorProbeAvailability builds the check from the two gateways; either
// may be nil, and a request its gateway is missing for is never answered.
func NewMonitorProbeAvailability(gateway *GatewayService, openai *OpenAIGatewayService) MonitorProbeAvailability {
	return &monitorProbeAvailability{gateway: gateway, openai: openai}
}

func (a *monitorProbeAvailability) HasServableAccountNow(ctx context.Context, groupID int64, groupPlatform, model string) bool {
	platform := groupPlatform
	if target, ok := ResolvedTargetPlatformFromContext(ctx); ok && target != "" {
		platform = target
	}
	if force, ok := ctx.Value(ctxkey.ForcePlatform).(string); ok && force != "" {
		platform = force
	}
	if monitorProbeUsesOpenAIGateway(platform) {
		if a.openai == nil {
			return false
		}
		account, err := a.openai.selectAccountForModelWithExclusions(a.openai.withOpenAIQuotaAutoPauseContext(ctx), &groupID, platform, "", model, nil, false, 0, "", false)
		return err == nil && account != nil
	}
	if a.gateway == nil {
		return false
	}
	// Resolves forced platforms, composite targets, Claude Code fallbacks and
	// mixed scheduling itself.
	account, err := a.gateway.SelectAccountForModelWithExclusions(ctx, &groupID, "", model, nil)
	return err == nil && account != nil
}

// monitorProbeUsesOpenAIGateway mirrors the gateway routes: these platforms
// are served by the OpenAI gateway whatever the endpoint.
func monitorProbeUsesOpenAIGateway(platform string) bool {
	switch platform {
	case PlatformOpenAI, PlatformGrok, PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax, PlatformOpenCodeGo:
		return true
	}
	return false
}
