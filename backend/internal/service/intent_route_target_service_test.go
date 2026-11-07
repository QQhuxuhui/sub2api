//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type intentKeyFixture struct {
	*intentRouteFixture
	targetGroup int64
	keys        map[string]int64
}

// Keys of every kind a rule may point at. The router lives on the fixture's
// Anthropic group; "good" points at another active Anthropic group.
func newIntentKeyFixture(t *testing.T) *intentKeyFixture {
	t.Helper()
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	c := f.client
	target := c.Group.Create().SetName("internal-coding").SetPlatform(PlatformAnthropic).SaveX(ctx)
	openai := c.Group.Create().SetName("openai-pool").SetPlatform(PlatformOpenAI).SaveX(ctx)
	inactive := c.Group.Create().SetName("retired").SetPlatform(PlatformAnthropic).SetStatus("disabled").SaveX(ctx)
	owner := c.User.Create().SetEmail("ops@example.com").SetPasswordHash("hash").SetUsername("ops").SaveX(ctx)
	past := f.now.Add(-time.Hour)
	mk := func(name string, set func(*dbent.APIKeyCreate)) int64 {
		create := c.APIKey.Create().SetUserID(owner.ID).SetKey("sk-" + name).SetName(name)
		if set != nil {
			set(create)
		}
		return create.SaveX(ctx).ID
	}
	keys := map[string]int64{
		"good":      mk("good", func(k *dbent.APIKeyCreate) { k.SetGroupID(target.ID) }),
		"exhausted": mk("exhausted", func(k *dbent.APIKeyCreate) { k.SetGroupID(target.ID).SetStatus(StatusAPIKeyQuotaExhausted) }),
		"disabled":  mk("disabled", func(k *dbent.APIKeyCreate) { k.SetGroupID(target.ID).SetStatus(StatusAPIKeyDisabled) }),
		"expired":   mk("expired", func(k *dbent.APIKeyCreate) { k.SetGroupID(target.ID).SetExpiresAt(past) }),
		"no-group":  mk("no-group", nil),
		"openai":    mk("openai", func(k *dbent.APIKeyCreate) { k.SetGroupID(openai.ID) }),
		"inactive":  mk("inactive", func(k *dbent.APIKeyCreate) { k.SetGroupID(inactive.ID) }),
	}
	return &intentKeyFixture{intentRouteFixture: f, targetGroup: target.ID, keys: keys}
}

func keywordKeyRouter(keyID int64) IntentRouterInput {
	return IntentRouterInput{Enabled: true, Rules: []domain.IntentRule{
		{Name: "draw", Keywords: []string{"pelican"}, Enabled: true, APIKeyID: keyID},
	}}
}

func TestIntentRouter_KeyRuleSaveValidation(t *testing.T) {
	ctx := context.Background()
	f := newIntentKeyFixture(t)
	cases := map[string]struct {
		mutate func(*IntentRouterInput)
		reason string
	}{
		"accounts and key together": {func(in *IntentRouterInput) { in.Rules[0].AccountIDs = []int64{f.accountID(t, "pool-a")} }, "INTENT_RULE_TARGET_CONFLICT"},
		"key that does not exist":   {func(in *IntentRouterInput) { in.Rules[0].APIKeyID = 987654 }, "INTENT_API_KEY_NOT_FOUND"},
		"disabled key":              {func(in *IntentRouterInput) { in.Rules[0].APIKeyID = f.keys["disabled"] }, "INTENT_API_KEY_INACTIVE"},
		"expired key":               {func(in *IntentRouterInput) { in.Rules[0].APIKeyID = f.keys["expired"] }, "INTENT_API_KEY_INACTIVE"},
		"key without group":         {func(in *IntentRouterInput) { in.Rules[0].APIKeyID = f.keys["no-group"] }, "INTENT_API_KEY_NO_GROUP"},
		"key of an inactive group":  {func(in *IntentRouterInput) { in.Rules[0].APIKeyID = f.keys["inactive"] }, "INTENT_API_KEY_GROUP_INACTIVE"},
		"key of another platform":   {func(in *IntentRouterInput) { in.Rules[0].APIKeyID = f.keys["openai"] }, "INTENT_API_KEY_PLATFORM_MISMATCH"},
		"neither accounts nor key":  {func(in *IntentRouterInput) { in.Rules[0].APIKeyID = 0 }, "INTENT_RULE_ACCOUNTS_REQUIRED"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := keywordKeyRouter(f.keys["good"])
			tc.mutate(&in)
			_, err := f.svc.SaveRouter(ctx, f.groupID, in)
			require.Error(t, err)
			require.Equal(t, tc.reason, infraerrors.Reason(err), err.Error())
		})
	}

	view, err := f.svc.SaveRouter(ctx, f.groupID, keywordKeyRouter(f.keys["exhausted"]))
	require.NoError(t, err, "a quota-exhausted key still points at its group: it is never billed")
	require.Len(t, view.RuleKeys, 1)
	label := view.RuleKeys[0]
	require.Equal(t, f.keys["exhausted"], label.ID)
	require.Equal(t, "ops@example.com", label.UserEmail)
	require.Equal(t, f.targetGroup, label.GroupID)
	require.Equal(t, "internal-coding", label.GroupName)
	require.Equal(t, PlatformAnthropic, label.GroupPlatform)
	require.Empty(t, label.Problem)
	require.Equal(t, []int64{}, view.Rules[0].AccountIDs)
}

func TestIntentRouter_KeyRuleDecisionFollowsTheKey(t *testing.T) {
	ctx := context.Background()
	f := newIntentKeyFixture(t)
	_, err := f.svc.SaveRouter(ctx, f.groupID, keywordKeyRouter(f.keys["good"]))
	require.NoError(t, err)

	d := f.svc.Decide(ctx, f.request(`{"messages":[{"role":"user","content":"draw a pelican"}]}`))
	require.NotNil(t, d)
	require.Equal(t, "draw", d.Intent)
	require.Empty(t, d.AccountIDs)
	require.Equal(t, f.keys["good"], d.TargetAPIKeyID)
	require.Equal(t, f.targetGroup, d.TargetGroupID)
	require.Equal(t, PlatformAnthropic, d.TargetPlatform)
	require.Equal(t, []int64{f.targetGroup}, d.ScopeGroupIDs)

	miss := f.svc.Decide(ctx, f.request(`{"messages":[{"role":"user","content":"hello"}]}`))
	require.NotNil(t, miss, "a router with only key rules still carries its scope")
	require.False(t, miss.routed())
	require.Equal(t, []int64{f.targetGroup}, miss.ScopeGroupIDs)

	// The key is disabled after the save: the rule still matches and binds,
	// but has nowhere to go.
	f.client.APIKey.UpdateOneID(f.keys["good"]).SetStatus(StatusAPIKeyDisabled).ExecX(ctx)
	f.svc.InvalidateConfig(ctx)
	d = f.svc.Decide(ctx, f.request(`{"messages":[{"role":"user","content":"draw a pelican"}]}`))
	require.NotNil(t, d)
	require.True(t, d.routed())
	require.Zero(t, d.TargetGroupID)
	require.Equal(t, intentKeyProblemDisabled, d.TargetProblem)
	require.Empty(t, d.ScopeGroupIDs)

	view, err := f.svc.GetRouter(ctx, f.groupID)
	require.NoError(t, err)
	require.Equal(t, intentKeyProblemDisabled, view.RuleKeys[0].Problem, "the page can flag the broken key")

	result, err := f.svc.TestClassify(ctx, f.groupID, "draw a pelican")
	require.NoError(t, err)
	require.Equal(t, f.keys["good"], result.APIKeyID)
	require.Zero(t, result.TargetGroupID)
}

func TestIntentRouter_SearchRuleTargetKeys(t *testing.T) {
	ctx := context.Background()
	f := newIntentKeyFixture(t)

	byName, err := f.svc.SearchRuleTargetKeys(ctx, "goo", "")
	require.NoError(t, err)
	require.Len(t, byName, 1)
	require.Equal(t, f.keys["good"], byName[0].ID)
	require.Equal(t, "internal-coding", byName[0].GroupName)

	byID, err := f.svc.SearchRuleTargetKeys(ctx, fmt.Sprintf("#%d", f.keys["openai"]), "")
	require.NoError(t, err)
	require.Len(t, byID, 1)
	require.Equal(t, PlatformOpenAI, byID[0].GroupPlatform)

	byEmail, err := f.svc.SearchRuleTargetKeys(ctx, "ops@", PlatformOpenAI)
	require.NoError(t, err)
	require.Len(t, byEmail, 1, "narrowed to groups of one platform")
	require.Equal(t, f.keys["openai"], byEmail[0].ID)

	all, err := f.svc.SearchRuleTargetKeys(ctx, "", "")
	require.NoError(t, err)
	problems := map[int64]string{}
	for _, k := range all {
		problems[k.ID] = k.Problem
	}
	require.Equal(t, intentKeyProblemExpired, problems[f.keys["expired"]])
	require.Equal(t, intentKeyProblemNoGroup, problems[f.keys["no-group"]])
	require.Equal(t, intentKeyProblemGroupInactive, problems[f.keys["inactive"]])
}

func TestIntentKeyGroupPlatformCompatible(t *testing.T) {
	require.True(t, intentKeyGroupPlatformCompatible(PlatformAnthropic, PlatformAnthropic))
	require.False(t, intentKeyGroupPlatformCompatible(PlatformAnthropic, PlatformAntigravity), "the target group is scheduled as is, no mixing")
	require.False(t, intentKeyGroupPlatformCompatible(PlatformOpenAI, PlatformGrok))
	require.True(t, intentKeyGroupPlatformCompatible(PlatformComposite, PlatformOpenAI), "per-request platforms are checked at request time")
}
