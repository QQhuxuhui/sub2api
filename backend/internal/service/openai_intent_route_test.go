//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// Accounts 41001/41002 belong to the request's group (10); 49001 belongs to
// another group (99) and is only reachable through intent routing.
func newIntentRouteOpenAIFixture(t *testing.T, foreign Account) (*OpenAIGatewayService, *int64) {
	t.Helper()
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	groupID := int64(10)
	inGroup := func(id int64, priority int) Account {
		return Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Concurrency: 2, Priority: priority, GroupIDs: []int64{groupID}, AccountGroups: []AccountGroup{{AccountID: id, GroupID: groupID}}}
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: []Account{inGroup(41001, 0), inGroup(41002, 5), foreign}}},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	return svc, &groupID
}

func foreignOpenAIAccount() Account {
	return Account{ID: 49001, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Concurrency: 2, Priority: 9, GroupIDs: []int64{99}, AccountGroups: []AccountGroup{{AccountID: 49001, GroupID: 99}}}
}

func selectOpenAIForIntentTest(t *testing.T, svc *OpenAIGatewayService, ctx context.Context, groupID *int64, excluded map[int64]struct{}) (*AccountSelectionResult, OpenAIAccountScheduleDecision) {
	t.Helper()
	selection, decision, err := svc.SelectAccountWithScheduler(ctx, groupID, "", "", "gpt-5.1", excluded, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err, "routing trouble must never surface as a request error")
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	return selection, decision
}

func TestOpenAIIntentRoute_SendsRequestToAccountOutsideTheGroup(t *testing.T) {
	svc, groupID := newIntentRouteOpenAIFixture(t, foreignOpenAIAccount())

	plain, _ := selectOpenAIForIntentTest(t, svc, context.Background(), groupID, nil)
	require.Equal(t, int64(41001), plain.Account.ID, "without a decision the group's own accounts are used, as before")
	if plain.ReleaseFunc != nil {
		plain.ReleaseFunc()
	}

	route := &IntentRouteDecision{Intent: "coding", AccountIDs: []int64{49001}}
	routed, decision := selectOpenAIForIntentTest(t, svc, WithIntentRouteDecision(context.Background(), route), groupID, nil)
	require.Equal(t, int64(49001), routed.Account.ID)
	require.True(t, routed.Acquired)
	require.Equal(t, openAIAccountScheduleLayerIntentRoute, decision.Layer)
	require.Equal(t, int64(49001), decision.SelectedAccountID)
	require.Equal(t, int64(49001), route.SelectedAccountID())
}

func TestOpenAIIntentRoute_FallsBackToOrdinarySelection(t *testing.T) {
	cases := map[string]struct {
		foreign  Account
		excluded map[int64]struct{}
	}{
		"target failed earlier in this request": {foreign: foreignOpenAIAccount(), excluded: map[int64]struct{}{49001: {}}},
		"target paused":                         {foreign: func() Account { a := foreignOpenAIAccount(); a.Schedulable = false; return a }()},
		"target speaks another protocol":        {foreign: func() Account { a := foreignOpenAIAccount(); a.Platform = PlatformAnthropic; return a }()},
		"target does not serve the model": {foreign: func() Account {
			a := foreignOpenAIAccount()
			a.Credentials = map[string]any{"model_mapping": map[string]any{"only-this-model": "only-this-model"}}
			return a
		}()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc, groupID := newIntentRouteOpenAIFixture(t, tc.foreign)
			route := &IntentRouteDecision{Intent: "coding", AccountIDs: []int64{49001}}
			selection, decision := selectOpenAIForIntentTest(t, svc, WithIntentRouteDecision(context.Background(), route), groupID, tc.excluded)
			require.Equal(t, int64(41001), selection.Account.ID, "the group's own accounts take over")
			require.NotEqual(t, openAIAccountScheduleLayerIntentRoute, decision.Layer)
			require.Zero(t, route.SelectedAccountID())
		})
	}
}

// A Responses follow-up carries previous_response_id, and only the account
// that produced that response can continue it. Routing must not move the turn.
func TestOpenAIIntentRoute_RespectsPreviousResponseBinding(t *testing.T) {
	ctx := context.Background()
	selectWithPrevious := func(t *testing.T, svc *OpenAIGatewayService, groupID *int64, route *IntentRouteDecision, responseID string) int64 {
		t.Helper()
		selection, _, err := svc.SelectAccountWithScheduler(WithIntentRouteDecision(ctx, route), groupID, responseID, "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
		require.NoError(t, err)
		require.NotNil(t, selection)
		return selection.Account.ID
	}

	t.Run("bound to an account outside the rule: routing steps aside", func(t *testing.T) {
		svc, groupID := newIntentRouteOpenAIFixture(t, foreignOpenAIAccount())
		// previous_response_id pinning belongs to the advanced scheduler; with it
		// off the legacy path ignores bindings altogether.
		svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true")
		svc.cfg.Gateway.OpenAIWS.Enabled = true
		svc.cfg.Gateway.OpenAIWS.OAuthEnabled = true
		svc.cfg.Gateway.OpenAIWS.APIKeyEnabled = true
		require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, *groupID, "resp_in_group", 41002, time.Hour))
		route := &IntentRouteDecision{Intent: "coding", AccountIDs: []int64{49001}}
		require.Equal(t, int64(41002), selectWithPrevious(t, svc, groupID, route, "resp_in_group"), "the turn stays with the account that owns the response")
		require.Zero(t, route.SelectedAccountID())
	})

	t.Run("bound to one of the rule's accounts: that one, whatever the order", func(t *testing.T) {
		second := foreignOpenAIAccount()
		second.ID = 49002
		svc, groupID := newIntentRouteOpenAIFixture(t, foreignOpenAIAccount())
		repo := svc.accountRepo.(schedulerGroupAwareOpenAIAccountRepo)
		repo.accounts = append(repo.accounts, second)
		svc.accountRepo = repo
		require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, *groupID, "resp_routed", 49002, time.Hour))
		route := &IntentRouteDecision{Intent: "coding", AccountIDs: []int64{49001, 49002}}
		require.Equal(t, int64(49002), selectWithPrevious(t, svc, groupID, route, "resp_routed"))
	})

	t.Run("unknown response id: the preference applies", func(t *testing.T) {
		svc, groupID := newIntentRouteOpenAIFixture(t, foreignOpenAIAccount())
		route := &IntentRouteDecision{Intent: "coding", AccountIDs: []int64{49001}}
		require.Equal(t, int64(49001), selectWithPrevious(t, svc, groupID, route, "resp_never_seen"))
	})
}
