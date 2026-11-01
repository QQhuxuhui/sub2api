//go:build unit

package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func deferralTestParams() *postUsageBillingParams {
	return &postUsageBillingParams{
		Cost:    &CostBreakdown{TotalCost: 1.5, ActualCost: 1.5},
		User:    &User{ID: 14},
		APIKey:  &APIKey{ID: 2},
		Account: &Account{ID: 3, Type: AccountTypeAPIKey},
	}
}

type deferralStub struct {
	deferred bool
	dirty    []int64
}

func (d *deferralStub) ShouldDeferBalance(context.Context, int64) bool { return d.deferred }
func (d *deferralStub) MarkBalanceDirty(userID int64)                  { d.dirty = append(d.dirty, userID) }

type usageBillingRepoRecorder struct {
	UsageBillingRepository
	cmd    *UsageBillingCommand
	result *UsageBillingApplyResult
}

func (r *usageBillingRepoRecorder) Apply(_ context.Context, cmd *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	r.cmd = cmd
	return r.result, nil
}

func TestUsageBillingFingerprint_ExcludesDeferBalance(t *testing.T) {
	syncParams := deferralTestParams()
	deferredParams := deferralTestParams()
	deferredParams.DeferBalance = true

	syncCmd := buildUsageBillingCommand("req-1", nil, syncParams)
	deferredCmd := buildUsageBillingCommand("req-1", nil, deferredParams)

	require.False(t, syncCmd.DeferBalance)
	require.True(t, deferredCmd.DeferBalance)
	require.NotEmpty(t, syncCmd.RequestFingerprint)
	require.Equal(t, syncCmd.RequestFingerprint, deferredCmd.RequestFingerprint)
}

func TestBuildUsageBillingCommand_SubscriptionBillIsNeverDeferred(t *testing.T) {
	groupID := int64(9)
	p := deferralTestParams()
	p.APIKey.GroupID = &groupID
	p.Subscription = &UserSubscription{ID: 7}
	p.IsSubscriptionBill = true
	p.DeferBalance = true

	cmd := buildUsageBillingCommand("req-1", nil, p)
	require.Zero(t, cmd.BalanceCost)
	require.False(t, cmd.DeferBalance)
}

func TestApplyUsageBilling_FollowsWriteModeAndMarksDirty(t *testing.T) {
	for _, tc := range []struct {
		name         string
		deferral     *deferralStub
		subscription bool
		result       *UsageBillingApplyResult
		wantDefer    bool
		wantDirty    []int64
	}{
		{name: "no settler", result: &UsageBillingApplyResult{Applied: true}},
		{name: "sync", deferral: &deferralStub{}, result: &UsageBillingApplyResult{Applied: true}},
		{
			name:      "deferred",
			deferral:  &deferralStub{deferred: true},
			result:    &UsageBillingApplyResult{Applied: true, BalanceDeferred: true},
			wantDefer: true,
			wantDirty: []int64{14},
		},
		{
			name:      "replay writes nothing, so nothing is dirty",
			deferral:  &deferralStub{deferred: true},
			result:    &UsageBillingApplyResult{Applied: false},
			wantDefer: true,
		},
		{
			name:         "subscription",
			deferral:     &deferralStub{deferred: true},
			subscription: true,
			result:       &UsageBillingApplyResult{Applied: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := deferralTestParams()
			if tc.subscription {
				groupID := int64(9)
				p.APIKey.GroupID = &groupID
				p.Subscription = &UserSubscription{ID: 7}
				p.IsSubscriptionBill = true
			}
			repo := &usageBillingRepoRecorder{result: tc.result}
			cacheService := NewBillingCacheService(&balanceEligibilityCacheStub{}, nil, nil, nil, nil, nil, &config.Config{}, nil)
			t.Cleanup(cacheService.Stop)
			deps := &billingDeps{deferredService: &DeferredService{}, billingCacheService: cacheService}
			if tc.deferral != nil {
				deps.balanceDeferral = tc.deferral
			}

			_, err := applyUsageBilling(context.Background(), "req-1", nil, p, deps, repo)
			require.NoError(t, err)
			require.Equal(t, tc.wantDefer, repo.cmd.DeferBalance)
			if tc.deferral != nil {
				require.Equal(t, tc.wantDirty, tc.deferral.dirty)
			}
		})
	}
}

func TestSyncBalanceCacheAfterDeduction_DeferredQueuesCacheDeductionEvenWhenLow(t *testing.T) {
	cache := &balanceEligibilityCacheStub{balance: 0.001}
	cfg := &config.Config{}
	cfg.Billing.MinimumBalanceReserve = 1
	svc := NewBillingCacheService(cache, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(svc.Stop)

	syncBalanceCacheAfterDeduction(context.Background(), deferralTestParams(), &billingDeps{billingCacheService: svc},
		&UsageBillingApplyResult{Applied: true, BalanceDeferred: true})

	require.Eventually(t, func() bool { return cache.deductCalls.Load() == 1 }, 2*time.Second, time.Millisecond)
	require.Zero(t, cache.invalidateCalls.Load())
}

type balanceNotifySettingCounter struct {
	SettingRepository
	reads atomic.Int64
}

func (s *balanceNotifySettingCounter) GetMultiple(context.Context, []string) (map[string]string, error) {
	s.reads.Add(1)
	return map[string]string{}, nil
}

func TestNotifyBalanceLow_DeferredLeavesTheCheckToTheSettler(t *testing.T) {
	settings := &balanceNotifySettingCounter{}
	notify := NewBalanceNotifyService(&EmailService{}, settings, nil)
	deps := &billingDeps{balanceNotifyService: notify}
	p := deferralTestParams()
	p.User.BalanceNotifyEnabled = true

	notifyBalanceLow(p, deps, &UsageBillingApplyResult{Applied: true, BalanceDeferred: true})
	require.Zero(t, settings.reads.Load(), "a deferred deduction must not reach the threshold check")

	newBalance := 5.0
	notifyBalanceLow(p, deps, &UsageBillingApplyResult{Applied: true, NewBalance: &newBalance})
	require.Equal(t, int64(1), settings.reads.Load())
}
