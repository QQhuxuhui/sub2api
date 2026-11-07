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
// ok=false with a nil error means ordinary scheduling must take over. When the
// turn matched a rule but none of its accounts can take it, the error is
// ErrIntentRouteUnavailable and the request must fail rather than fall back.
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
	sessionHash string,
	previousResponseCanMove bool,
	useUpstreamTokenCost bool,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, bool, error) {
	none := OpenAIAccountScheduleDecision{}
	route := IntentRouteDecisionFromContext(ctx)
	if route == nil || s == nil {
		return nil, none, false, nil
	}
	// Scheduling inside an API-key rule's target group: intent routing has
	// already decided this request.
	if _, nested := intentRouteNestedFromContext(ctx); nested {
		return nil, none, false, nil
	}
	route.clearSelected()

	// A Responses follow-up names the response it continues, and only the
	// account that produced that response knows it. That binding outranks any
	// routing preference: serve the turn from the bound account when it is one
	// of the rule's targets, and otherwise step aside so ordinary scheduling
	// applies its own previous_response_id handling. An unknown binding (expired,
	// or a first turn) leaves the preference free to apply.
	//
	// "One of the rule's targets" is not the test, though: the bound account may
	// have been routed to by an earlier turn under a different intent — or this
	// turn may have no intent at all — and ordinary scheduling cannot reach it,
	// because it does not belong to the group. So any account the router could
	// have sent this conversation to (its scope) is served from here; only an
	// account outside the scope is the group's own and is left to the ordinary path.
	//
	// An API-key rule's group counts as scope too: an account of that group
	// may have served an earlier turn. Such an account is checked under its
	// group's policies (privacy, profit control), as its first turn was, while
	// channel pricing stays the request's own group's.
	candidates := route.orderedAccountIDs()
	boundAccountID := int64(0)
	policyGroupID := groupID
	if prev := strings.TrimSpace(previousResponseID); prev != "" {
		if store := s.getOpenAIWSStateStore(); store != nil {
			if bound, err := store.GetResponseAccount(ctx, derefGroupID(groupID), prev); err == nil && bound > 0 {
				if !route.inScope(bound) {
					scopeGroup := s.intentRouteScopeGroupOf(ctx, route, bound)
					if scopeGroup <= 0 {
						return nil, none, false, nil
					}
					policyGroupID = &scopeGroup
					ctx = withIntentRouteNested(ctx, derefGroupID(groupID))
				}
				boundAccountID = bound
				candidates = []int64{bound}
			}
		}
	}
	if boundAccountID == 0 && route.viaKey() {
		return s.selectIntentRoutedGroupAccount(ctx, route, groupID, sessionHash, requestedModel, excludedIDs, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, platform, previousResponseCanMove, useUpstreamTokenCost)
	}
	if len(candidates) == 0 {
		return nil, none, false, nil
	}

	// Same request-scoped context the ordinary path installs before filtering.
	ctx = s.withOpenAIQuotaAutoPauseContext(ctx)
	ctx = s.withOpenAIGroupPrivacyRequirement(ctx, policyGroupID)
	if requiredImageCapability == "" {
		ctx = s.withOpenAIProfitControlGate(ctx, policyGroupID)
	}
	if s.checkChannelPricingRestriction(ctx, groupID, requestedModel) {
		return nil, none, false, nil
	}
	platform = NormalizeOpenAICompatiblePlatform(platform)
	checker := &defaultOpenAIAccountScheduler{service: s, stats: s.openaiAccountStats}
	if checker.stats == nil {
		checker.stats = newOpenAIAccountRuntimeStats()
	}
	req := OpenAIAccountScheduleRequest{
		GroupID:                 policyGroupID,
		Platform:                platform,
		RequestedModel:          requestedModel,
		RequiredTransport:       requiredTransport,
		RequiredCapability:      requiredCapability,
		RequiredImageCapability: requiredImageCapability,
		RequireCompact:          requireCompact,
		ExcludedIDs:             excludedIDs,
		RequirePrivacySet:       s.openAIGroupRequiresPrivacySet(ctx, policyGroupID),
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
		if isGrokTeamModelRateLimited(account, upstreamModel, now) || isGrokAccountModelQuotaBlocked(account, upstreamModel, now) {
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
		if boundAccountID == 0 || route.prefers(account.ID) {
			route.markSelected(account.ID)
		} else {
			// Served for the sake of the binding; the conversation's pin is not
			// rewritten with an intent this account is not a target of.
			route.noteSelected(account.ID)
		}
		slog.Debug("intent_route.selected",
			"group_id", derefGroupID(groupID), "intent", route.Intent, "account_id", account.ID, "model", requestedModel, "bound", boundAccountID > 0)
		return result, OpenAIAccountScheduleDecision{
			Layer:               openAIAccountScheduleLayerIntentRoute,
			SelectedAccountID:   account.ID,
			SelectedAccountType: account.Type,
		}, true, nil
	}
	if route.routed() {
		slog.Info("intent_route.unavailable",
			"group_id", derefGroupID(groupID), "intent", route.Intent, "model", requestedModel, "excluded", len(excludedIDs))
		return nil, none, false, route.unavailable()
	}
	slog.Debug("intent_route.fallback",
		"group_id", derefGroupID(groupID), "intent", route.Intent, "model", requestedModel)
	return nil, none, false, nil
}

// intentRouteScopeGroupOf is the first group (in rule order) of the router's
// API-key rules that the account belongs to, or 0.
func (s *OpenAIGatewayService) intentRouteScopeGroupOf(ctx context.Context, route *IntentRouteDecision, accountID int64) int64 {
	if route == nil || len(route.ScopeGroupIDs) == 0 {
		return 0
	}
	account, err := s.getSchedulableAccount(ctx, accountID)
	if err != nil || account == nil {
		return 0
	}
	for _, groupID := range route.ScopeGroupIDs {
		if containsInt64(account.GroupIDs, groupID) {
			return groupID
		}
	}
	return 0
}

// selectIntentRoutedGroupAccount serves an API-key rule: the request is
// scheduled in the key's group by the full ordinary scheduler (including its
// proxy-quarantine fail-open pass). Any failure is the rule's unavailability;
// the request never falls back to its own group.
func (s *OpenAIGatewayService) selectIntentRoutedGroupAccount(
	ctx context.Context,
	route *IntentRouteDecision,
	groupID *int64,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability,
	requiredImageCapability OpenAIImagesCapability,
	requireCompact bool,
	platform string,
	previousResponseCanMove bool,
	useUpstreamTokenCost bool,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, bool, error) {
	none := OpenAIAccountScheduleDecision{}
	unavailable := func(reason string) (*AccountSelectionResult, OpenAIAccountScheduleDecision, bool, error) {
		slog.Info("intent_route.unavailable",
			"group_id", derefGroupID(groupID), "intent", route.Intent, "model", requestedModel,
			"api_key_id", route.TargetAPIKeyID, "target_group_id", route.TargetGroupID, "reason", reason, "excluded", len(excludedIDs))
		return nil, none, false, route.unavailable()
	}
	if route.TargetGroupID <= 0 {
		return unavailable(route.TargetProblem)
	}
	normalized := NormalizeOpenAICompatiblePlatform(platform)
	if route.TargetPlatform != normalized {
		return unavailable("platform_mismatch:" + route.TargetPlatform + "!=" + normalized)
	}
	targetGroupID := route.TargetGroupID
	selection, decision, err := s.selectAccountWithScheduler(withIntentRouteNested(ctx, derefGroupID(groupID)), &targetGroupID, "", sessionHash, requestedModel, excludedIDs, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, platform, previousResponseCanMove, useUpstreamTokenCost)
	if err != nil || selection == nil || selection.Account == nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, none, false, ctxErr
		}
		reason := "no_account"
		if err != nil {
			reason = err.Error()
		}
		return unavailable(reason)
	}
	route.markSelectedViaGroup(selection.Account.ID)
	decision.Layer = openAIAccountScheduleLayerIntentRoute
	decision.SelectedAccountID = selection.Account.ID
	decision.SelectedAccountType = selection.Account.Type
	slog.Debug("intent_route.selected",
		"group_id", derefGroupID(groupID), "intent", route.Intent, "account_id", selection.Account.ID,
		"target_group_id", targetGroupID, "model", requestedModel, "wait", selection.WaitPlan != nil)
	return selection, decision, true, nil
}

// openAIAccountScheduleLayerIntentRoute labels selections made by intent routing
// in scheduler decisions and metrics.
const openAIAccountScheduleLayerIntentRoute = "intent_route"

// hasIntentRoutedAccountNow is the no-reservation counterpart of
// selectIntentRoutedAccount for checks made before a request is forwarded:
// decided=false when the request carries no routing rule and ordinary
// scheduling applies; otherwise ok reports whether one of the rule's targets
// could take the request now, concurrency aside.
func (s *OpenAIGatewayService) hasIntentRoutedAccountNow(ctx context.Context, groupID *int64, requestedModel, platform string, capability OpenAIEndpointCapability) (decided, ok bool) {
	route := IntentRouteDecisionFromContext(ctx)
	if route == nil || s == nil || !route.routed() {
		return false, false
	}
	if route.viaKey() {
		return true, s.hasIntentRoutedGroupAccountNow(ctx, route, groupID, requestedModel, platform, capability)
	}
	ctx = s.withOpenAIQuotaAutoPauseContext(ctx)
	ctx = s.withOpenAIGroupPrivacyRequirement(ctx, groupID)
	ctx = s.withOpenAIProfitControlGate(ctx, groupID)
	platform = NormalizeOpenAICompatiblePlatform(platform)
	checker := &defaultOpenAIAccountScheduler{service: s, stats: s.openaiAccountStats}
	if checker.stats == nil {
		checker.stats = newOpenAIAccountRuntimeStats()
	}
	req := OpenAIAccountScheduleRequest{
		GroupID:            groupID,
		Platform:           platform,
		RequestedModel:     requestedModel,
		RequiredCapability: capability,
		RequirePrivacySet:  s.openAIGroupRequiresPrivacySet(ctx, groupID),
	}
	now := time.Now()
	for _, accountID := range route.orderedAccountIDs() {
		account, err := s.getSchedulableAccount(ctx, accountID)
		if err != nil || account == nil {
			continue
		}
		if account.Platform != platform || !account.IsOpenAICompatible() || !account.IsSchedulable() ||
			shouldClearStickySession(account, requestedModel) {
			continue
		}
		if !checker.isAccountRequestCompatible(ctx, account, req) {
			continue
		}
		upstreamModel := canonicalOpenAIAccountSchedulingModel(account, requestedModel)
		if isGrokTeamModelRateLimited(account, upstreamModel, now) || isGrokAccountModelQuotaBlocked(account, upstreamModel, now) {
			continue
		}
		return true, true
	}
	return true, false
}

// hasIntentRoutedGroupAccountNow is hasIntentRoutedAccountNow for an API-key
// rule: whether the target group's ordinary scheduling could serve the
// request now, under the target group's policies and with the same
// proxy-quarantine fail-open the real selection has.
func (s *OpenAIGatewayService) hasIntentRoutedGroupAccountNow(ctx context.Context, route *IntentRouteDecision, groupID *int64, requestedModel, platform string, capability OpenAIEndpointCapability) bool {
	if route.TargetGroupID <= 0 {
		return false
	}
	normalized := NormalizeOpenAICompatiblePlatform(platform)
	if route.TargetPlatform != normalized {
		return false
	}
	targetGroupID := route.TargetGroupID
	ctx = withIntentRouteNested(ctx, derefGroupID(groupID))
	ctx = s.withOpenAIQuotaAutoPauseContext(ctx)
	ctx = s.withOpenAIGroupPrivacyRequirement(ctx, &targetGroupID)
	ctx = s.withOpenAIProfitControlGate(ctx, &targetGroupID)
	account, err := s.selectAccountForModelWithExclusions(ctx, &targetGroupID, normalized, "", requestedModel, nil, false, 0, capability, false)
	if err == nil && account != nil {
		return true
	}
	if normalized != PlatformOpenAI || openAIProxyStreamQuarantineBypassed(ctx) {
		return false
	}
	if s.getOpenAIProxyStreamCircuit().activeBlockCount(time.Now()) == 0 {
		return false
	}
	account, err = s.selectAccountForModelWithExclusions(withOpenAIProxyStreamQuarantineBypass(ctx), &targetGroupID, normalized, "", requestedModel, nil, false, 0, capability, false)
	return err == nil && account != nil
}
