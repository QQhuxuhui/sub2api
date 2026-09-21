package service

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// selectIntentRoutedAccount is the OpenAI-family counterpart of
// GatewayService.selectIntentRoutedAccount. It mirrors the scheduler's
// "try this specific account" path (selectBySessionHash) minus the two checks
// that tie an account to the request's group, because a routed account may
// deliberately live in another group. Privacy, capability, transport, model,
// quota and profit rules of the request still apply.
//
// ok=false means ordinary scheduling must take over.
func (s *OpenAIGatewayService) selectIntentRoutedAccount(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability,
	requiredImageCapability OpenAIImagesCapability,
	requireCompact bool,
	platform string,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, bool) {
	none := OpenAIAccountScheduleDecision{}
	route := IntentRouteDecisionFromContext(ctx)
	if route == nil || s == nil {
		return nil, none, false
	}
	route.clearSelected()

	// A Responses follow-up names the response it continues, and only the
	// account that produced that response knows it. That binding outranks any
	// routing preference: serve the turn from the bound account when it is one
	// of the rule's targets, and otherwise step aside so ordinary scheduling
	// applies its own previous_response_id handling. An unknown binding (expired,
	// or a first turn) leaves the preference free to apply.
	candidates := route.orderedAccountIDs()
	if prev := strings.TrimSpace(previousResponseID); prev != "" {
		if store := s.getOpenAIWSStateStore(); store != nil {
			if bound, err := store.GetResponseAccount(ctx, derefGroupID(groupID), prev); err == nil && bound > 0 {
				if !containsInt64(candidates, bound) {
					return nil, none, false
				}
				candidates = []int64{bound}
			}
		}
	}

	// Same request-scoped context the ordinary path installs before filtering.
	ctx = s.withOpenAIQuotaAutoPauseContext(ctx)
	ctx = s.withOpenAIGroupPrivacyRequirement(ctx, groupID)
	if requiredImageCapability == "" {
		ctx = s.withOpenAIProfitControlGate(ctx, groupID)
	}
	if s.checkChannelPricingRestriction(ctx, groupID, requestedModel) {
		return nil, none, false
	}
	platform = NormalizeOpenAICompatiblePlatform(platform)
	checker := &defaultOpenAIAccountScheduler{service: s, stats: s.openaiAccountStats}
	if checker.stats == nil {
		checker.stats = newOpenAIAccountRuntimeStats()
	}
	req := OpenAIAccountScheduleRequest{
		GroupID:                 groupID,
		Platform:                platform,
		RequestedModel:          requestedModel,
		RequiredTransport:       requiredTransport,
		RequiredCapability:      requiredCapability,
		RequiredImageCapability: requiredImageCapability,
		RequireCompact:          requireCompact,
		ExcludedIDs:             excludedIDs,
		RequirePrivacySet:       s.openAIGroupRequiresPrivacySet(ctx, groupID),
	}

	for _, accountID := range candidates {
		if _, excluded := excludedIDs[accountID]; excluded {
			continue
		}
		account, err := s.getSchedulableAccount(ctx, accountID)
		if err != nil || account == nil {
			continue
		}
		if account.Platform != platform || !account.IsOpenAICompatible() || !account.IsSchedulable() ||
			shouldClearStickySession(account, requestedModel) {
			continue
		}
		if !checker.isAccountRequestCompatible(ctx, account, req) || !checker.isAccountTransportCompatible(account, requiredTransport) {
			continue
		}
		if len(checker.filterGrokFreeQuotaAccounts(ctx, []Account{*account})) == 0 {
			continue
		}
		now := time.Now()
		upstreamModel := canonicalOpenAIAccountSchedulingModel(account, requestedModel)
		if isGrokTeamModelRateLimited(account, upstreamModel, now) || isGrokModelQuotaBlocked(account.ID, upstreamModel, now) {
			continue
		}
		acquired, err := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
		if err != nil || acquired == nil || !acquired.Acquired {
			// Busy: try the next target rather than queueing on this one.
			continue
		}
		result, err := s.newSelectionResult(ctx, account, true, acquired.ReleaseFunc, nil)
		if err != nil || result == nil {
			acquired.ReleaseFunc()
			continue
		}
		route.markSelected(account.ID)
		slog.Debug("intent_route.selected",
			"group_id", derefGroupID(groupID), "intent", route.Intent, "account_id", account.ID, "model", requestedModel)
		return result, OpenAIAccountScheduleDecision{
			Layer:               openAIAccountScheduleLayerIntentRoute,
			SelectedAccountID:   account.ID,
			SelectedAccountType: account.Type,
		}, true
	}
	slog.Debug("intent_route.fallback",
		"group_id", derefGroupID(groupID), "intent", route.Intent, "model", requestedModel)
	return nil, none, false
}

// openAIAccountScheduleLayerIntentRoute labels selections made by intent routing
// in scheduler decisions and metrics.
const openAIAccountScheduleLayerIntentRoute = "intent_route"
