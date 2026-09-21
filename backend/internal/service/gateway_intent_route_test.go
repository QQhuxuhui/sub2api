//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// The pool (repo.accounts) stands for the accounts of the request's own group;
// account 9 exists only by ID, i.e. it belongs to some other group.
func newIntentRouteGatewayFixture(t *testing.T, foreign ...Account) (*GatewayService, *mockAccountRepoForPlatform) {
	t.Helper()
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{ID: 1, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5},
			{ID: 2, Platform: PlatformAnthropic, Priority: 2, Status: StatusActive, Schedulable: true, Concurrency: 5},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	for i := range foreign {
		repo.accountsByID[foreign[i].ID] = &foreign[i]
	}
	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	return &GatewayService{accountRepo: repo, cache: &mockGatewayCacheForPlatform{}, cfg: cfg}, repo
}

func foreignAnthropicAccount(id int64) Account {
	return Account{ID: id, Platform: PlatformAnthropic, Priority: 5, Status: StatusActive, Schedulable: true, Concurrency: 5}
}

func TestGatewayIntentRoute_SendsRequestToAccountOutsideTheGroup(t *testing.T) {
	svc, _ := newIntentRouteGatewayFixture(t, foreignAnthropicAccount(9))
	var pinned []int64
	decision := &IntentRouteDecision{Intent: "coding", AccountIDs: []int64{9}, onSelect: func(id int64) { pinned = append(pinned, id) }}
	ctx := WithIntentRouteDecision(context.Background(), decision)

	result, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", "claude-3-5-sonnet-20241022", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(9), result.Account.ID)
	require.True(t, result.Acquired)
	require.Equal(t, int64(9), decision.SelectedAccountID())
	require.Equal(t, []int64{9}, pinned, "the conversation is pinned to the account it was first served by")

	// A retry of the same request selects again; the pin is not re-announced.
	_, err = svc.SelectAccountWithLoadAwareness(ctx, nil, "", "claude-3-5-sonnet-20241022", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, []int64{9}, pinned)
}

func TestGatewayIntentRoute_WithoutDecisionNothingChanges(t *testing.T) {
	svc, _ := newIntentRouteGatewayFixture(t, foreignAnthropicAccount(9))
	result, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "claude-3-5-sonnet-20241022", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), result.Account.ID, "ordinary priority selection")
}

func TestGatewayIntentRoute_FallsBackToOrdinarySelection(t *testing.T) {
	cases := map[string]struct {
		foreign  Account
		excluded map[int64]struct{}
		model    string
	}{
		"target failed earlier in this request": {foreign: foreignAnthropicAccount(9), excluded: map[int64]struct{}{9: {}}},
		"target paused":                         {foreign: Account{ID: 9, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: false, Concurrency: 5}},
		"target disabled":                       {foreign: Account{ID: 9, Platform: PlatformAnthropic, Status: StatusDisabled, Schedulable: true, Concurrency: 5}},
		"target speaks another protocol":        {foreign: Account{ID: 9, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, Concurrency: 5}},
		"target does not serve the model": {foreign: Account{ID: 9, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true, Concurrency: 5,
			Credentials: map[string]any{"model_mapping": map[string]any{"claude-only-this": "claude-only-this"}}}},
		"target no longer exists": {foreign: Account{ID: 77, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc, _ := newIntentRouteGatewayFixture(t, tc.foreign)
			decision := &IntentRouteDecision{Intent: "coding", AccountIDs: []int64{9}}
			ctx := WithIntentRouteDecision(context.Background(), decision)

			result, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", "claude-3-5-sonnet-20241022", tc.excluded, "", 0)
			require.NoError(t, err, "routing trouble must never surface as a request error")
			require.Equal(t, int64(1), result.Account.ID, "the group's own accounts take over")
			require.Zero(t, decision.SelectedAccountID())
		})
	}
}

func TestGatewayIntentRoute_PrefersPinnedThenNextTarget(t *testing.T) {
	svc, _ := newIntentRouteGatewayFixture(t, foreignAnthropicAccount(8), foreignAnthropicAccount(9))
	select9 := func(excluded map[int64]struct{}, pinned int64) int64 {
		decision := &IntentRouteDecision{Intent: "coding", AccountIDs: []int64{8, 9}, PinnedAccountID: pinned}
		result, err := svc.SelectAccountWithLoadAwareness(WithIntentRouteDecision(context.Background(), decision), nil, "", "claude-3-5-sonnet-20241022", excluded, "", 0)
		require.NoError(t, err)
		return result.Account.ID
	}
	require.Equal(t, int64(8), select9(nil, 0), "rule order when nothing is pinned")
	require.Equal(t, int64(9), select9(nil, 9), "the pinned account goes first")
	require.Equal(t, int64(8), select9(map[int64]struct{}{9: {}}, 9), "a failed pin moves on to the rule's next account")
	require.Equal(t, int64(8), select9(nil, 1), "a pin that is no longer a target of the rule is ignored")
}

func TestGatewayIntentRoute_ScopeOnlyDecisionChangesNothing(t *testing.T) {
	svc, _ := newIntentRouteGatewayFixture(t, foreignAnthropicAccount(9))
	ctx := WithIntentRouteDecision(context.Background(), &IntentRouteDecision{ScopeAccountIDs: []int64{9}})
	result, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", "claude-3-5-sonnet-20241022", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), result.Account.ID, "being in scope is not a preference")
}

func TestIntentRouteDecision_EmptyDecisionIsNotAttached(t *testing.T) {
	ctx := WithIntentRouteDecision(context.Background(), &IntentRouteDecision{Intent: "coding"})
	require.Nil(t, IntentRouteDecisionFromContext(ctx))
	require.Nil(t, IntentRouteDecisionFromContext(WithIntentRouteDecision(context.Background(), nil)))
}
