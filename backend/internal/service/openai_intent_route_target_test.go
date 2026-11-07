//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Group 99 is the target group of an API-key rule: 49001 (priority 9) and
// 49002 (priority 1, preferred by the group's scheduling).
func newIntentRouteKeyOpenAIFixture(t *testing.T) (*OpenAIGatewayService, *int64) {
	t.Helper()
	second := foreignOpenAIAccount()
	second.ID = 49002
	second.Priority = 1
	second.AccountGroups = []AccountGroup{{AccountID: 49002, GroupID: 99}}
	svc, groupID := newIntentRouteOpenAIFixture(t, foreignOpenAIAccount())
	repo := svc.accountRepo.(schedulerGroupAwareOpenAIAccountRepo)
	repo.accounts = append(repo.accounts, second)
	svc.accountRepo = repo
	return svc, groupID
}

func openAIKeyRuleDecision(targetGroup int64, platform string) *IntentRouteDecision {
	return &IntentRouteDecision{GroupID: 10, Intent: "coding", TargetAPIKeyID: 7, TargetGroupID: targetGroup, TargetPlatform: platform, UnavailableStatus: 429, ScopeGroupIDs: []int64{99}}
}

func TestOpenAIIntentRouteKey_SchedulesInTheKeysGroup(t *testing.T) {
	svc, groupID := newIntentRouteKeyOpenAIFixture(t)
	route := openAIKeyRuleDecision(99, PlatformOpenAI)
	ctx := WithIntentRouteDecision(context.Background(), route)

	selection, decision := selectOpenAIForIntentTest(t, svc, ctx, groupID, nil)
	require.Equal(t, int64(49002), selection.Account.ID, "the target group's priority decides")
	require.Equal(t, openAIAccountScheduleLayerIntentRoute, decision.Layer)
	require.Equal(t, int64(49002), route.SelectedAccountID())
	stickyGroup, viaGroup := IntentRouteStickyGroup(ctx, 49002)
	require.True(t, viaGroup)
	require.Equal(t, int64(99), stickyGroup)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}

	selection, _ = selectOpenAIForIntentTest(t, svc, ctx, groupID, map[int64]struct{}{49002: {}})
	require.Equal(t, int64(49001), selection.Account.ID, "failover moves on inside the target group")
}

func TestOpenAIIntentRouteKey_UnavailableNeverFallsBack(t *testing.T) {
	cases := map[string]struct {
		route    *IntentRouteDecision
		excluded map[int64]struct{}
	}{
		"all target accounts excluded":     {route: openAIKeyRuleDecision(99, PlatformOpenAI), excluded: map[int64]struct{}{49001: {}, 49002: {}}},
		"key no longer resolves":           {route: openAIKeyRuleDecision(0, "")},
		"target group of another platform": {route: openAIKeyRuleDecision(99, PlatformGrok)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc, groupID := newIntentRouteKeyOpenAIFixture(t)
			selection, _, err := svc.SelectAccountWithScheduler(WithIntentRouteDecision(context.Background(), tc.route), groupID, "", "", "gpt-5.1", tc.excluded, OpenAIUpstreamTransportAny, false)
			require.ErrorIs(t, err, ErrIntentRouteUnavailable)
			require.Equal(t, 429, IntentRouteUnavailableStatus(err))
			require.Nil(t, selection, "the group's own accounts must not take over")
		})
	}
}

// The first turn was served by 49001 through the API-key rule; the follow-up
// matched no rule. 49001 is not in the request's group, but belongs to the
// rule's target group, so it stays reachable.
func TestOpenAIIntentRouteKey_PreviousResponseInTargetGroupStaysReachable(t *testing.T) {
	ctx := context.Background()
	svc, groupID := newIntentRouteKeyOpenAIFixture(t)
	svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true")
	svc.cfg.Gateway.OpenAIWS.Enabled = true
	svc.cfg.Gateway.OpenAIWS.OAuthEnabled = true
	svc.cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, *groupID, "resp_key", 49001, time.Hour))

	route := &IntentRouteDecision{GroupID: 10, ScopeGroupIDs: []int64{99}}
	selection, _, err := svc.SelectAccountWithScheduler(WithIntentRouteDecision(ctx, route), groupID, "resp_key", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, int64(49001), selection.Account.ID)
	_, viaGroup := IntentRouteStickyGroup(WithIntentRouteDecision(ctx, route), 49001)
	require.False(t, viaGroup, "served for the binding, not picked by group scheduling")
}

func TestOpenAIIntentRouteKey_ProbeChecksTheTargetGroup(t *testing.T) {
	svc, groupID := newIntentRouteKeyOpenAIFixture(t)
	decided, ok := svc.hasIntentRoutedAccountNow(WithIntentRouteDecision(context.Background(), openAIKeyRuleDecision(99, PlatformOpenAI)), groupID, "gpt-5.1", PlatformOpenAI, "")
	require.True(t, decided)
	require.True(t, ok)

	decided, ok = svc.hasIntentRoutedAccountNow(WithIntentRouteDecision(context.Background(), openAIKeyRuleDecision(0, "")), groupID, "gpt-5.1", PlatformOpenAI, "")
	require.True(t, decided)
	require.False(t, ok, "an unresolvable key is decided and unavailable")

	// Target group 98 has no accounts at all.
	decided, ok = svc.hasIntentRoutedAccountNow(WithIntentRouteDecision(context.Background(), openAIKeyRuleDecision(98, PlatformOpenAI)), groupID, "gpt-5.1", PlatformOpenAI, "")
	require.True(t, decided)
	require.False(t, ok)
}
