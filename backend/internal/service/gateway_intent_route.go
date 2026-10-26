package service

import (
	"context"
	"log/slog"
)

// selectIntentRoutedAccount serves a request from the accounts its intent was
// routed to. Those accounts may sit in other groups, so they are looked up by
// ID rather than filtered out of the group's candidate pool; everything else
// (schedulability, model support, quotas, concurrency slots, session limits)
// goes through the same checks ordinary selection uses.
//
// A nil result with a nil error means the turn has no preference and the caller
// must carry on with ordinary selection. When the turn matched a rule but none
// of its accounts can take it, the error is ErrIntentRouteUnavailable and the
// request must fail rather than fall back.
func (s *GatewayService) selectIntentRoutedAccount(
	ctx context.Context,
	groupID *int64,
	group *Group,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
) (*AccountSelectionResult, error) {
	decision := IntentRouteDecisionFromContext(ctx)
	if decision == nil {
		return nil, nil
	}
	decision.clearSelected()
	if len(decision.AccountIDs) == 0 {
		// Scope only: this turn has no preference, and these protocols bind no
		// server-side state to an account that would have to be honored.
		return nil, nil
	}

	platform, hasForcePlatform, err := s.resolvePlatform(ctx, groupID, group, requestedModel)
	if err != nil {
		return nil, nil
	}
	useMixed := (platform == PlatformAnthropic || platform == PlatformGemini) && !hasForcePlatform
	// Channel pricing of the key's own group still governs which upstream
	// models may be billed, wherever the account lives.
	channelRestricted := func(account *Account) bool {
		return groupID != nil && s.needsUpstreamChannelRestrictionCheck(ctx, groupID) &&
			s.isUpstreamModelRestrictedByChannel(ctx, *groupID, account, requestedModel)
	}

	for _, accountID := range decision.orderedAccountIDs() {
		if _, excluded := excludedIDs[accountID]; excluded {
			continue
		}
		account, err := s.getSchedulableAccount(ctx, accountID)
		if err != nil || account == nil {
			continue
		}
		if channelRestricted(account) || !s.isIntentRoutedAccountUsable(ctx, account, platform, useMixed, requestedModel) {
			continue
		}
		acquired, err := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
		if err != nil || acquired == nil || !acquired.Acquired {
			// Busy: try the next target rather than queueing on this one.
			continue
		}
		if !s.checkAndRegisterSession(ctx, account, sessionHash) {
			acquired.ReleaseFunc()
			continue
		}
		result, err := s.newSelectionResult(ctx, account, true, acquired.ReleaseFunc, nil)
		if err != nil || result == nil {
			acquired.ReleaseFunc()
			continue
		}
		decision.markSelected(account.ID)
		slog.Debug("intent_route.selected",
			"group_id", derefGroupID(groupID), "intent", decision.Intent, "account_id", account.ID, "model", requestedModel)
		return result, nil
	}
	slog.Info("intent_route.unavailable",
		"group_id", derefGroupID(groupID), "intent", decision.Intent, "model", requestedModel, "excluded", len(excludedIDs))
	return nil, ErrIntentRouteUnavailable
}

func (s *GatewayService) isIntentRoutedAccountUsable(ctx context.Context, account *Account, platform string, useMixed bool, requestedModel string) bool {
	if !s.isAccountSchedulableForSelection(account) {
		return false
	}
	if !s.isGatewayAccountProfitEligible(ctx, account) {
		return false
	}
	if !s.isAccountAllowedForPlatform(account, platform, useMixed) {
		return false
	}
	if requestedModel != "" && !s.isModelSupportedByAccountWithContext(ctx, account, requestedModel) {
		return false
	}
	if !s.isAccountSchedulableForModelSelection(ctx, account, requestedModel) {
		return false
	}
	if !s.isAccountSchedulableForQuota(account) {
		return false
	}
	if !s.isAccountSchedulableForWindowCost(ctx, account, false) {
		return false
	}
	return s.isAccountSchedulableForRPM(ctx, account, false)
}
