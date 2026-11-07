//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// groupAwareAccountRepo lists only the accounts bound to the asked group, so
// a test can tell the request's own group from a rule's target group.
type groupAwareAccountRepo struct {
	*mockAccountRepoForPlatform
}

func (r *groupAwareAccountRepo) inGroup(groupID int64, accounts []Account) []Account {
	var out []Account
	for _, acc := range accounts {
		if containsInt64(acc.GroupIDs, groupID) {
			out = append(out, acc)
		}
	}
	return out
}

func (r *groupAwareAccountRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]Account, error) {
	all, err := r.ListSchedulableByPlatform(ctx, platform)
	return r.inGroup(groupID, all), err
}

func (r *groupAwareAccountRepo) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]Account, error) {
	all, err := r.ListSchedulableByPlatforms(ctx, platforms)
	return r.inGroup(groupID, all), err
}

const (
	intentTestOwnGroup    = int64(10)
	intentTestTargetGroup = int64(20)
)

// Group 10 is the request's own group (accounts 1, 2); group 20 is the
// target group of an API-key rule (accounts 21, 22; 22 has the better
// priority).
func newIntentRouteTargetGatewayFixture(t *testing.T, mutate func(groups map[int64]*Group)) *GatewayService {
	t.Helper()
	acc := func(id int64, priority int, group int64) Account {
		return Account{ID: id, Platform: PlatformAnthropic, Priority: priority, Status: StatusActive, Schedulable: true, Concurrency: 5, GroupIDs: []int64{group}}
	}
	base := &mockAccountRepoForPlatform{
		accounts:     []Account{acc(1, 1, intentTestOwnGroup), acc(2, 2, intentTestOwnGroup), acc(21, 2, intentTestTargetGroup), acc(22, 1, intentTestTargetGroup)},
		accountsByID: map[int64]*Account{},
	}
	for i := range base.accounts {
		base.accountsByID[base.accounts[i].ID] = &base.accounts[i]
	}
	groups := map[int64]*Group{
		intentTestOwnGroup:    {ID: intentTestOwnGroup, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true},
		intentTestTargetGroup: {ID: intentTestTargetGroup, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true},
	}
	if mutate != nil {
		mutate(groups)
	}
	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	return &GatewayService{
		accountRepo: &groupAwareAccountRepo{base},
		groupRepo:   &mockGroupRepoForGateway{groups: groups},
		cache:       &mockGatewayCacheForPlatform{},
		cfg:         cfg,
	}
}

func keyRuleDecision(targetGroup int64, platform string) *IntentRouteDecision {
	return &IntentRouteDecision{GroupID: intentTestOwnGroup, Intent: "coding", TargetAPIKeyID: 5, TargetGroupID: targetGroup, TargetPlatform: platform, UnavailableStatus: 429}
}

func TestGatewayIntentRouteKey_SchedulesInTheKeysGroup(t *testing.T) {
	svc := newIntentRouteTargetGatewayFixture(t, nil)
	var routed []int64
	decision := keyRuleDecision(intentTestTargetGroup, PlatformAnthropic)
	decision.onSelect = func(id int64) { routed = append(routed, id) }
	ctx := WithIntentRouteDecision(context.Background(), decision)
	own := intentTestOwnGroup

	result, err := svc.SelectAccountWithLoadAwareness(ctx, &own, "", "claude-3-5-sonnet-20241022", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(22), result.Account.ID, "the target group's own priority decides")
	require.Equal(t, int64(22), decision.SelectedAccountID())
	require.Equal(t, []int64{22}, routed, "the routed event is still recorded")
	group, viaGroup := IntentRouteStickyGroup(ctx, 22)
	require.True(t, viaGroup)
	require.Equal(t, intentTestTargetGroup, group, "the sticky session belongs to the target group")
	_, viaGroup = IntentRouteStickyGroup(ctx, 1)
	require.False(t, viaGroup)

	// Failover inside the request: the excluded account is skipped, the
	// group's next account serves.
	result, err = svc.SelectAccountWithLoadAwareness(ctx, &own, "", "claude-3-5-sonnet-20241022", map[int64]struct{}{22: {}}, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(21), result.Account.ID)
}

func TestGatewayIntentRouteKey_UnavailableNeverFallsBack(t *testing.T) {
	cases := map[string]struct {
		decision *IntentRouteDecision
		excluded map[int64]struct{}
		mutate   func(map[int64]*Group)
	}{
		"all target accounts excluded": {decision: keyRuleDecision(intentTestTargetGroup, PlatformAnthropic), excluded: map[int64]struct{}{21: {}, 22: {}}},
		"key no longer resolves":       {decision: func() *IntentRouteDecision { d := keyRuleDecision(0, ""); d.TargetProblem = intentKeyProblemDisabled; return d }()},
		"target group of another platform": {decision: keyRuleDecision(intentTestTargetGroup, PlatformOpenAI)},
		"target group sends non Claude Code clients elsewhere": {
			decision: keyRuleDecision(intentTestTargetGroup, PlatformAnthropic),
			mutate: func(groups map[int64]*Group) {
				fallback := intentTestOwnGroup
				groups[intentTestTargetGroup].ClaudeCodeOnly = true
				groups[intentTestTargetGroup].FallbackGroupID = &fallback
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc := newIntentRouteTargetGatewayFixture(t, tc.mutate)
			ctx := WithIntentRouteDecision(context.Background(), tc.decision)
			own := intentTestOwnGroup
			result, err := svc.SelectAccountWithLoadAwareness(ctx, &own, "", "claude-3-5-sonnet-20241022", tc.excluded, "", 0)
			require.ErrorIs(t, err, ErrIntentRouteUnavailable)
			require.Equal(t, 429, IntentRouteUnavailableStatus(err))
			require.Nil(t, result, "the request's own group must not take over")
		})
	}
}

// A rule may target the router's own group: the nested selection does not
// route again, it just schedules the group normally.
func TestGatewayIntentRouteKey_TargetingOwnGroupDoesNotRecurse(t *testing.T) {
	svc := newIntentRouteTargetGatewayFixture(t, nil)
	ctx := WithIntentRouteDecision(context.Background(), keyRuleDecision(intentTestOwnGroup, PlatformAnthropic))
	own := intentTestOwnGroup
	result, err := svc.SelectAccountWithLoadAwareness(ctx, &own, "", "claude-3-5-sonnet-20241022", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), result.Account.ID)
}

func TestIntentRouteChannelGroup_NestedUsesTheBillingGroup(t *testing.T) {
	target := intentTestTargetGroup
	require.Equal(t, &target, intentRouteChannelGroup(context.Background(), &target))
	nested := withIntentRouteNested(context.Background(), intentTestOwnGroup)
	require.Equal(t, intentTestOwnGroup, *intentRouteChannelGroup(nested, &target))
	require.Equal(t, intentTestOwnGroup, intentRouteChannelGroupID(nested, target))
}

func TestIntentRouteDecision_KeyRuleIsAttachedAndRouted(t *testing.T) {
	d := &IntentRouteDecision{Intent: "x", TargetAPIKeyID: 5}
	require.True(t, d.routed(), "an unresolvable key rule still binds")
	require.NotNil(t, IntentRouteDecisionFromContext(WithIntentRouteDecision(context.Background(), d)))
	scope := &IntentRouteDecision{ScopeGroupIDs: []int64{20}}
	require.NotNil(t, IntentRouteDecisionFromContext(WithIntentRouteDecision(context.Background(), scope)))
	require.False(t, scope.routed())
}
