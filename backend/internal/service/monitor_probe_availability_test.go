//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

type fixedRPMCache struct{ rpm int }

func (c fixedRPMCache) IncrementRPM(context.Context, int64) (int, error) { return c.rpm + 1, nil }
func (c fixedRPMCache) GetRPM(context.Context, int64) (int, error)       { return c.rpm, nil }
func (c fixedRPMCache) GetRPMBatch(_ context.Context, ids []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(ids))
	for _, id := range ids {
		out[id] = c.rpm
	}
	return out, nil
}

// The check asks real account selection, so whatever keeps a real request
// from an account keeps the probe from being answered.
func TestMonitorProbeAvailability_FollowsRealSelection(t *testing.T) {
	const groupID, model = int64(30), "claude-3-5-sonnet-20241022"
	newSvc := func() (*GatewayService, *mockAccountRepoForPlatform) {
		repo := &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{}}
		for _, id := range []int64{1, 2} {
			repo.accounts = append(repo.accounts, Account{
				ID: id, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Priority: int(id), Status: StatusActive, Schedulable: true, Concurrency: 5,
				AccountGroups: []AccountGroup{{GroupID: groupID}},
			})
		}
		for i := range repo.accounts {
			repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
		}
		group := &Group{ID: groupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}
		return &GatewayService{accountRepo: repo, groupRepo: &mockGroupRepoForGateway{groups: map[int64]*Group{groupID: group}}, cache: &mockGatewayCacheForPlatform{}, cfg: testConfig()}, repo
	}
	ctx := context.Background()

	svc, repo := newSvc()
	check := NewMonitorProbeAvailability(svc, nil)
	require.True(t, check.HasServableAccountNow(ctx, groupID, PlatformAnthropic, model))

	future := time.Now().Add(time.Hour)
	repo.accounts[0].RateLimitResetAt = &future
	repo.accounts[1].TempUnschedulableUntil = &future
	require.False(t, check.HasServableAccountNow(ctx, groupID, PlatformAnthropic, model), "all cooling down")

	// RPM: both accounts at their base RPM — real selection skips them.
	svc, repo = newSvc()
	for i := range repo.accounts {
		repo.accounts[i].Extra = map[string]any{"base_rpm": 5}
	}
	svc.rpmCache = fixedRPMCache{rpm: 4}
	check = NewMonitorProbeAvailability(svc, nil)
	require.True(t, check.HasServableAccountNow(ctx, groupID, PlatformAnthropic, model), "under the RPM limit")
	svc.rpmCache = fixedRPMCache{rpm: 50}
	require.False(t, check.HasServableAccountNow(ctx, groupID, PlatformAnthropic, model), "over the RPM limit")

	// A forced platform (the /antigravity routes) is honoured: this group has
	// no antigravity account, whatever its anthropic accounts can do.
	svc, _ = newSvc()
	check = NewMonitorProbeAvailability(svc, nil)
	forced := context.WithValue(ctx, ctxkey.ForcePlatform, PlatformAntigravity)
	require.False(t, check.HasServableAccountNow(forced, groupID, PlatformAnthropic, model))

	// An OpenAI-family group is checked by the OpenAI gateway; without it,
	// nothing is answered.
	require.False(t, check.HasServableAccountNow(ctx, groupID, PlatformOpenAI, "gpt-6.1-sol"))
	// A composite group's resolved target decides the gateway too.
	composite := WithResolvedTargetPlatform(ctx, PlatformOpenAI)
	require.False(t, check.HasServableAccountNow(composite, groupID, PlatformComposite, "gpt-6.1-sol"))
}

func TestMonitorProbeAvailability_OpenAIGateway(t *testing.T) {
	svc, groupID := newIntentRouteOpenAIFixture(t, foreignOpenAIAccount())
	check := NewMonitorProbeAvailability(nil, svc)
	ctx := context.Background()
	require.True(t, check.HasServableAccountNow(ctx, *groupID, PlatformOpenAI, "gpt-5.1"))
	require.False(t, check.HasServableAccountNow(ctx, 99999, PlatformOpenAI, "gpt-5.1"), "a group without accounts")
}
