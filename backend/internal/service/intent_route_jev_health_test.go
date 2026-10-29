//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func healthOn(rule string) func(*domain.IntentJevConfig) {
	return func(j *domain.IntentJevConfig) {
		j.HealthEnabled, j.HealthRule = true, rule
	}
}

func jevDayCount(t *testing.T, f *intentRouteFixture, field string) int64 {
	t.Helper()
	counts, err := f.svc.jevStats.Day(context.Background(), f.groupID, f.svc.jevDay(f.svc.now()))
	require.NoError(t, err)
	return counts[field]
}

func TestIntentJevHealth_OffAsksOneQuestion(t *testing.T) {
	f, judge, _ := newJevFixture(t, 0.95, nil)
	h := 0.99
	judge.health = &h
	require.Equal(t, "coding", f.svc.Decide(context.Background(), f.request(userTurns("Reply OK"))).Intent)
	require.Equal(t, []bool{false}, judge.asked, "the health question is only asked when switched on")
}

func TestIntentJevHealth_HealthCheckGoesToOrdinarySchedulingByDefault(t *testing.T) {
	f, judge, sink := newJevFixture(t, 0.95, healthOn(""))
	h := 0.98
	judge.health = &h
	in := f.request(userTurns("Calculate and respond with ONLY the number, nothing else.\nQ: 3 + 5 = ?\nA: 8\nQ: 40 + 2 = ?\nA:"))
	in.ClientRequestID = "r1"

	requireNoPreference(t, f.svc.Decide(context.Background(), in))
	require.Equal(t, []bool{true}, judge.asked)
	require.Zero(t, f.classifier.calls, "a settled health check is not classified further")
	require.Len(t, sink.hits, 1, "recorded for review, with no rule")
	require.Equal(t, "health:0.98", sink.hits[0].Keyword)
	require.Empty(t, sink.hits[0].Rule)
	require.Equal(t, int64(1), jevDayCount(t, f, intentJevStatHealth))
	require.Zero(t, jevDayCount(t, f, intentJevStatProbes))
	require.Equal(t, []string{"jev_health"}, f.store.kinds())
}

func TestIntentJevHealth_HealthRuleRoutesHealthChecks(t *testing.T) {
	f, judge, sink := newJevFixture(t, 0.95, healthOn("CHAT"))
	require.Equal(t, "chat", f.svc.mustRouter(t, f.groupID).Jev.HealthRule, "the rule name is canonicalised")
	h := 0.80
	judge.health = &h
	d := f.svc.Decide(context.Background(), f.request(userTurns("hi")))
	require.Equal(t, "chat", d.Intent)
	require.Equal(t, []int64{f.accountID(t, "pool-a")}, d.AccountIDs)
	require.Equal(t, "chat", sink.hits[0].Rule)
}

func TestIntentJevHealth_BelowHealthThresholdIsJudgedAsAProbe(t *testing.T) {
	f, judge, _ := newJevFixture(t, 0.95, healthOn(""))
	h := 0.69 // default threshold 0.7
	judge.health = &h
	require.Equal(t, "coding", f.svc.Decide(context.Background(), f.request(userTurns("黒い袋の中に3種類の味のキャンディー"))).Intent)
	require.Equal(t, int64(1), jevDayCount(t, f, intentJevStatProbes))
	require.Zero(t, jevDayCount(t, f, intentJevStatHealth))

	// A health check wins even when the probe answer alone would not route.
	judge.p, h = 0.10, 0.90
	requireNoPreference(t, f.svc.Decide(context.Background(), f.request(userTurns("ping"))))
	require.Equal(t, int64(1), jevDayCount(t, f, intentJevStatHealth))
}

func TestIntentJevHealth_Validation(t *testing.T) {
	f, _, _ := newJevFixture(t, 0.5, nil)
	save := func(mutate func(*domain.IntentJevConfig)) error {
		in := IntentRouterInput{Enabled: true}
		in.Rules = []domain.IntentRule{
			{Name: "coding", Keywords: []string{"鹈鹕"}, Enabled: true, AccountIDs: []int64{f.accountID(t, "coding-1")}},
			// No keywords: reachable only as Jev's health rule.
			{Name: "health", Enabled: true, AccountIDs: []int64{f.accountID(t, "pool-a")}},
		}
		in.JevAPIKey = "jev-secret"
		in.Jev = domain.IntentJevConfig{Enabled: true, Rule: "coding", HealthEnabled: true, HealthRule: "health"}
		mutate(&in.Jev)
		_, err := f.svc.SaveRouter(context.Background(), f.groupID, in)
		return err
	}
	require.NoError(t, save(func(*domain.IntentJevConfig) {}), "the health rule needs no matcher of its own")
	view := f.svc.mustRouter(t, f.groupID)
	require.Equal(t, intentJevDefaultHealthThres, view.Jev.HealthThreshold)
	require.Equal(t, "INTENT_JEV_INVALID_HEALTH_THRESHOLD", infraerrors.Reason(save(func(j *domain.IntentJevConfig) { j.HealthThreshold = 1.5 })))
	require.Equal(t, "INTENT_JEV_HEALTH_RULE_UNKNOWN", infraerrors.Reason(save(func(j *domain.IntentJevConfig) { j.HealthRule = "nope" })))
}

func TestIntentJevHealth_TestClassifyShowsTheHealthAnswer(t *testing.T) {
	f, judge, _ := newJevFixture(t, 0.95, healthOn(""))
	h := 0.97
	judge.health = &h
	res, err := f.svc.TestClassify(context.Background(), f.groupID, "Reply with exactly OK.")
	require.NoError(t, err)
	require.True(t, res.HealthCheck)
	require.Equal(t, 0.97, *res.HealthProbability)
	require.Empty(t, res.Intent, "left to ordinary scheduling")
	require.Empty(t, res.AccountIDs)
}

func TestHTTPIntentProbeJudge_AsksTheHealthQuestion(t *testing.T) {
	var got map[string]any
	answer := `{"answers":{"is_probe":{"noul":0.9},"is_health":{"noul":0.97}},"usage":{"input_tokens":400}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()
	j := &httpIntentProbeJudge{client: srv.Client(), endpoint: srv.URL}

	res, err := j.ProbeProbability(context.Background(), "k", "jev-latest", "hi", true)
	require.NoError(t, err)
	require.Equal(t, 0.9, res.P)
	require.Equal(t, 0.97, *res.Health)
	q := got["questions"].(map[string]any)
	require.Contains(t, q, "is_health")
	require.Equal(t, intentJevHealthQuestion, q["is_health"].(map[string]any)["instructions"])

	res, err = j.ProbeProbability(context.Background(), "k", "jev-latest", "hi", false)
	require.NoError(t, err)
	require.Nil(t, res.Health)
	require.NotContains(t, got["questions"].(map[string]any), "is_health")

	answer = `{"answers":{"is_probe":{"noul":0.9}},"usage":{"input_tokens":400}}`
	_, err = j.ProbeProbability(context.Background(), "k", "jev-latest", "hi", true)
	require.ErrorContains(t, err, "no health probability")
}

// A rule deleted (or renamed) while health checks — or Jev — are off must not
// block the save; the stale reference is dropped. While on, it is an error.
func TestIntentJevHealth_StaleRuleIsDroppedWhileOff(t *testing.T) {
	f, _, _ := newJevFixture(t, 0.5, healthOn("chat"))
	save := func(jevOn, healthOn bool) (*IntentRouterView, error) {
		in := IntentRouterInput{Enabled: true}
		in.Rules = []domain.IntentRule{{Name: "coding", Keywords: []string{"鹈鹕"}, Enabled: true, AccountIDs: []int64{f.accountID(t, "coding-1")}}}
		in.Jev = domain.IntentJevConfig{Enabled: jevOn, Rule: "coding", HealthEnabled: healthOn, HealthRule: "chat"} // "chat" was deleted
		return f.svc.SaveRouter(context.Background(), f.groupID, in)
	}
	_, err := save(true, true)
	require.Equal(t, "INTENT_JEV_HEALTH_RULE_UNKNOWN", infraerrors.Reason(err))
	for _, c := range []struct{ jev, health bool }{{true, false}, {false, true}, {false, false}} {
		view, err := save(c.jev, c.health)
		require.NoError(t, err, c)
		require.Empty(t, view.Jev.HealthRule, c)
	}
}

// A case-only rename keeps the reference — names match case-insensitively — and
// the renamed rule, with no matcher of its own, stays saveable as the health rule.
func TestIntentJevHealth_CaseOnlyRenameKeepsTheRule(t *testing.T) {
	f, _, _ := newJevFixture(t, 0.5, nil)
	in := IntentRouterInput{Enabled: true}
	in.Rules = []domain.IntentRule{
		{Name: "coding", Keywords: []string{"鹈鹕"}, Enabled: true, AccountIDs: []int64{f.accountID(t, "coding-1")}},
		{Name: "Health", Enabled: true, AccountIDs: []int64{f.accountID(t, "pool-a")}},
	}
	in.Jev = domain.IntentJevConfig{Enabled: false, Rule: "coding", HealthEnabled: false, HealthRule: "health"}
	view, err := f.svc.SaveRouter(context.Background(), f.groupID, in)
	require.NoError(t, err)
	require.Equal(t, "Health", view.Jev.HealthRule)
}
