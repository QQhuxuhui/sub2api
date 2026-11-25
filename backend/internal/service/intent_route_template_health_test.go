//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/ent/intentrouter"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// Templates an admin judged "health" go to the health-check rule while health
// checks are on — like a Jev health verdict — and are otherwise let through.

var healthTemplateLabels = map[string]string{keyOf("hi"): IntentTemplateHealth, keyOf("ping"): IntentTemplateNormal}

func withHealthRule(rule string) func(*domain.IntentJevConfig) {
	return func(j *domain.IntentJevConfig) { j.HealthRule = rule }
}

func TestIntentTemplateHealth_RoutesToTheHealthRule(t *testing.T) {
	f, judge, hits, _, _ := newTemplateFixture(t, 0.99, healthTemplateLabels, withHealthRule("chat"))
	d := f.svc.Decide(context.Background(), f.request(userTurns("hi")))
	require.Equal(t, "chat", d.Intent)
	require.Equal(t, []int64{f.accountID(t, "pool-a")}, d.AccountIDs)
	require.Zero(t, judge.calls, "the judgement is final: Jev is not asked")
	require.Len(t, hits.hits, 1)
	require.Equal(t, "chat", hits.hits[0].Rule)
	require.Equal(t, "manual:"+IntentTemplateHealth, hits.hits[0].Keyword)
}

func TestIntentTemplateHealth_WorksWithJevOff(t *testing.T) {
	f, judge, _, _, _ := newTemplateFixture(t, 0.99, healthTemplateLabels, func(j *domain.IntentJevConfig) {
		j.Enabled, j.HealthRule = false, "chat"
	})
	require.Equal(t, "chat", f.svc.Decide(context.Background(), f.request(userTurns("hi"))).Intent)
	require.Zero(t, judge.calls)
}

func TestIntentTemplateHealth_LetThroughWhileHealthChecksAreOff(t *testing.T) {
	f, judge, hits, _, _ := newTemplateFixture(t, 0.99, healthTemplateLabels, func(j *domain.IntentJevConfig) {
		j.HealthEnabled, j.HealthRule = false, "chat" // kept, but hidden in the UI
	})
	require.Equal(t, "chat", f.svc.mustRouter(t, f.groupID).Jev.HealthRule)
	requireNoPreference(t, f.svc.Decide(context.Background(), f.request(userTurns("hi"))))
	require.Zero(t, judge.calls)
	require.Len(t, hits.hits, 1)
	require.Empty(t, hits.hits[0].Rule)
	require.Equal(t, "manual:"+IntentTemplateHealth, hits.hits[0].Keyword)
}

func TestIntentTemplateHealth_LetThroughWhenTheRuleIsUnavailable(t *testing.T) {
	f, judge, hits, _, _ := newTemplateFixture(t, 0.99, healthTemplateLabels, nil)
	f.saveRouter(t, func(in *IntentRouterInput) {
		jevOn(func(j *domain.IntentJevConfig) {
			j.Threshold, j.HealthEnabled, j.HealthThreshold, j.HealthRule = 0.8, true, 0.7, "chat"
		})(in)
		in.Rules[1].Enabled = false // the health-check rule
	})
	requireNoPreference(t, f.svc.Decide(context.Background(), f.request(userTurns("hi"))))
	require.Zero(t, judge.calls, "not handed to Jev either")
	require.Len(t, hits.hits, 1)
	require.Empty(t, hits.hits[0].Rule)

	got, err := f.svc.TestClassify(context.Background(), f.groupID, "hi")
	require.NoError(t, err)
	require.Equal(t, IntentTemplateHealth, got.ManualLabel)
	require.Empty(t, got.Intent)
	require.Empty(t, got.AccountIDs)
}

func TestIntentTemplateHealth_NormalStaysLetThrough(t *testing.T) {
	f, judge, hits, _, _ := newTemplateFixture(t, 0.99, healthTemplateLabels, withHealthRule("chat"))
	requireNoPreference(t, f.svc.Decide(context.Background(), f.request(userTurns("ping"))))
	require.Zero(t, judge.calls)
	require.Len(t, hits.hits, 1)
	require.Empty(t, hits.hits[0].Rule)
	require.Equal(t, "manual:"+IntentTemplateNormal, hits.hits[0].Keyword)

	got, err := f.svc.TestClassify(context.Background(), f.groupID, "ping")
	require.NoError(t, err)
	require.Equal(t, IntentTemplateNormal, got.ManualLabel)
	require.Empty(t, got.Intent)
}

func TestIntentTemplateHealth_TestClassifyMatchesLiveTraffic(t *testing.T) {
	// Jev switched off but its key kept: the test may still ask Jev, yet a
	// judged template decides first.
	f, judge, _, _, _ := newTemplateFixture(t, 0.99, healthTemplateLabels, func(j *domain.IntentJevConfig) {
		j.Enabled, j.HealthRule = false, "chat"
	})
	got, err := f.svc.TestClassify(context.Background(), f.groupID, "hi")
	require.NoError(t, err)
	require.Equal(t, "manual", got.MatchedBy)
	require.Equal(t, IntentTemplateHealth, got.ManualLabel)
	require.Equal(t, keyOf("hi"), got.TemplateKey)
	require.Equal(t, "chat", got.Intent)
	require.Equal(t, []int64{f.accountID(t, "pool-a")}, got.AccountIDs)
	require.False(t, got.HealthCheck, "no Jev health score was involved")
	require.Zero(t, judge.calls)
}

// A router whose only route is the health-check rule: no keywords, no
// classifier, no Jev target.
func healthOnlyRouter(f *intentRouteFixture, t *testing.T, enabled, healthEnabled bool) IntentRouterInput {
	return IntentRouterInput{
		Enabled: enabled,
		Rules:   []domain.IntentRule{{Name: "chat", Enabled: true, AccountIDs: []int64{f.accountID(t, "pool-a")}}},
		Jev:     domain.IntentJevConfig{HealthEnabled: healthEnabled, HealthRule: "chat"},
	}
}

// newHealthOnlyFixture is a group that never stored a Jev key or classifier.
func newHealthOnlyFixture(t *testing.T) (*intentRouteFixture, *scriptedProbeJudge) {
	t.Helper()
	f := newIntentRouteFixture(t)
	judge := &scriptedProbeJudge{p: 0.99}
	f.svc.judge, f.svc.hits, f.svc.jevStats = judge, &recordingHitSink{}, newMemoryIntentJevStats()
	f.svc.templateStore = &memoryIntentTemplateStore{labels: healthTemplateLabels}
	f.svc.templateSink, f.svc.templateFlips = &recordingTemplateSink{}, newIntentTemplateFlips(100)
	require.NoError(t, f.svc.reloadConfigs(context.Background()))
	return f, judge
}

func TestIntentTemplateHealth_HealthRuleAloneIsARoute(t *testing.T) {
	f, judge := newHealthOnlyFixture(t)
	ctx := context.Background()
	_, err := f.svc.SaveRouter(ctx, f.groupID, healthOnlyRouter(f, t, true, true))
	require.NoError(t, err)

	d := f.svc.Decide(ctx, f.request(userTurns("hi")))
	require.Equal(t, "chat", d.Intent)
	requireNoPreference(t, f.svc.Decide(ctx, f.request(userTurns("帮我改一下这个函数"))))
	require.Zero(t, judge.calls)

	got, err := f.svc.TestClassify(ctx, f.groupID, "帮我改一下这个函数")
	require.NoError(t, err, "ordinary scheduling, as live traffic")
	require.Empty(t, got.Intent)
	got, err = f.svc.TestClassify(ctx, f.groupID, "hi")
	require.NoError(t, err)
	require.Equal(t, "chat", got.Intent)
	require.Zero(t, judge.calls)
}

func TestIntentTemplateHealth_HealthRuleAloneNeedsHealthChecksOn(t *testing.T) {
	f, _ := newHealthOnlyFixture(t)
	ctx := context.Background()
	_, err := f.svc.SaveRouter(ctx, f.groupID, healthOnlyRouter(f, t, true, false))
	require.Equal(t, "INTENT_CLASSIFIER_INCOMPLETE", infraerrors.Reason(err))

	// Saved as a disabled draft, the test still reports what is missing.
	_, err = f.svc.SaveRouter(ctx, f.groupID, healthOnlyRouter(f, t, false, false))
	require.NoError(t, err)
	_, err = f.svc.TestClassify(ctx, f.groupID, "帮我改一下这个函数")
	require.Equal(t, "INTENT_CLASSIFIER_INCOMPLETE", infraerrors.Reason(err))
}

func TestIntentTemplateHealth_KeyTargetRule(t *testing.T) {
	ctx := context.Background()
	f := newIntentKeyFixture(t)
	f.svc.templateStore = &memoryIntentTemplateStore{labels: healthTemplateLabels}
	f.svc.templateSink, f.svc.templateFlips = &recordingTemplateSink{}, newIntentTemplateFlips(100)
	_, err := f.svc.SaveRouter(ctx, f.groupID, IntentRouterInput{
		Enabled: true,
		Rules: []domain.IntentRule{
			{Name: "draw", Keywords: []string{"pelican"}, Enabled: true, APIKeyID: f.keys["good"]},
			{Name: "health", Enabled: true, APIKeyID: f.keys["good"]},
		},
		Jev: domain.IntentJevConfig{HealthEnabled: true, HealthRule: "health"},
	})
	require.NoError(t, err)
	require.NoError(t, f.svc.reloadConfigs(ctx))
	require.NotZero(t, f.targetGroup)

	d := f.svc.Decide(ctx, f.request(userTurns("hi")))
	require.NotNil(t, d)
	require.Equal(t, "health", d.Intent)
	require.Equal(t, f.keys["good"], d.TargetAPIKeyID)
	require.Equal(t, f.targetGroup, d.TargetGroupID)

	got, err := f.svc.TestClassify(ctx, f.groupID, "hi")
	require.NoError(t, err)
	require.Equal(t, "health", got.Intent)
	require.Equal(t, f.keys["good"], got.APIKeyID)
	require.Equal(t, f.targetGroup, got.TargetGroupID)
}

// The try-it target comes from the configuration the text was judged against,
// not from a cached copy: another instance may have just switched the rule
// between accounts and an API key.
func TestIntentRouter_TestClassifyTargetFollowsTheSavedRule(t *testing.T) {
	ctx := context.Background()
	f := newIntentKeyFixture(t)
	accountRule := []domain.IntentRule{{Name: "draw", Keywords: []string{"pelican"}, Enabled: true, AccountIDs: []int64{f.accountID(t, "coding-1")}}}
	keyRule := []domain.IntentRule{{Name: "draw", Keywords: []string{"pelican"}, Enabled: true, APIKeyID: f.keys["good"]}}
	saveElsewhere := func(rules []domain.IntentRule) {
		f.client.IntentRouter.Update().Where(intentrouter.GroupIDEQ(f.groupID)).SetRules(rules).ExecX(ctx)
	}

	_, err := f.svc.SaveRouter(ctx, f.groupID, IntentRouterInput{Enabled: true, Rules: accountRule})
	require.NoError(t, err)
	require.NoError(t, f.svc.reloadConfigs(ctx))
	saveElsewhere(keyRule)
	got, err := f.svc.TestClassify(ctx, f.groupID, "draw a pelican")
	require.NoError(t, err)
	require.Empty(t, got.AccountIDs)
	require.Equal(t, f.keys["good"], got.APIKeyID)
	require.Equal(t, f.targetGroup, got.TargetGroupID)

	require.NoError(t, f.svc.reloadConfigs(ctx))
	saveElsewhere(accountRule)
	got, err = f.svc.TestClassify(ctx, f.groupID, "draw a pelican")
	require.NoError(t, err)
	require.Equal(t, []int64{f.accountID(t, "coding-1")}, got.AccountIDs)
	require.Zero(t, got.APIKeyID)
	require.Zero(t, got.TargetGroupID)
}
