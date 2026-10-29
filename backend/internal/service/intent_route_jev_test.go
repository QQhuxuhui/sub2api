//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type scriptedProbeJudge struct {
	mu     sync.Mutex
	p      float64
	health *float64 // answered only when asked
	asked  []bool
	tokens int64
	err    error
	calls  int
	texts  []string
	keys   []string
}

func (j *scriptedProbeJudge) ProbeProbability(_ context.Context, apiKey, _ string, text string, askHealth bool) (intentJevAnswer, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.calls++
	j.texts = append(j.texts, text)
	j.keys = append(j.keys, apiKey)
	j.asked = append(j.asked, askHealth)
	answer := intentJevAnswer{P: j.p, Tokens: j.tokens}
	if askHealth {
		h := 0.0
		if j.health != nil {
			h = *j.health
		}
		answer.Health = &h
	}
	return answer, j.err
}

type memoryIntentJevStats struct {
	mu    sync.Mutex
	days  map[string]map[string]int64
	spent map[int64]int64
}

func newMemoryIntentJevStats() *memoryIntentJevStats {
	return &memoryIntentJevStats{days: map[string]map[string]int64{}, spent: map[int64]int64{}}
}

func (m *memoryIntentJevStats) Add(_ context.Context, groupID int64, day, field string, n int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := intentJevStatsKey(groupID, day)
	if m.days[key] == nil {
		m.days[key] = map[string]int64{}
	}
	m.days[key][field] += n
	return nil
}

func (m *memoryIntentJevStats) AddSpent(_ context.Context, groupID int64, tokens int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spent[groupID] += tokens
	return nil
}

func (m *memoryIntentJevStats) ResetSpent(_ context.Context, groupID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.spent, groupID)
	return nil
}

func (m *memoryIntentJevStats) Day(_ context.Context, groupID int64, day string) (map[string]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int64{}
	for k, v := range m.days[intentJevStatsKey(groupID, day)] {
		out[k] = v
	}
	return out, nil
}

func (m *memoryIntentJevStats) Spent(_ context.Context, groupID int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spent[groupID], nil
}

// jevOn turns a keyword-only router into one where Jev routes probes to the
// "coding" rule.
func jevOn(mutate func(*domain.IntentJevConfig)) func(*IntentRouterInput) {
	return func(in *IntentRouterInput) {
		keywordOnly([]string{"鹈鹕"}, []string{"笑话"})(in)
		in.JevAPIKey = "jev-secret"
		in.Jev = domain.IntentJevConfig{Enabled: true, Rule: "coding"}
		if mutate != nil {
			mutate(&in.Jev)
		}
	}
}

func newJevFixture(t *testing.T, p float64, mutate func(*domain.IntentJevConfig)) (*intentRouteFixture, *scriptedProbeJudge, *recordingHitSink) {
	t.Helper()
	f := newIntentRouteFixture(t)
	judge := &scriptedProbeJudge{p: p}
	sink := &recordingHitSink{}
	f.svc.judge, f.svc.hits, f.svc.jevStats = judge, sink, newMemoryIntentJevStats()
	f.saveRouter(t, jevOn(mutate))
	return f, judge, sink
}

func TestIntentJev_ProbeRoutedLikeAKeywordHit(t *testing.T) {
	f, judge, sink := newJevFixture(t, 0.91, nil)
	in := f.request(userTurns("黒い袋の中に3種類の味のキャンディーが入っており"))
	in.UserID, in.ClientRequestID = 14, "r1"

	d := f.svc.Decide(context.Background(), in)
	require.Equal(t, "coding", d.Intent)
	require.Equal(t, []int64{f.accountID(t, "coding-1"), f.accountID(t, "coding-2")}, d.AccountIDs)
	require.Equal(t, 1, judge.calls)
	require.Equal(t, "jev-secret", judge.keys[0])
	require.Equal(t, []string{"jev"}, f.store.kinds(), "the event log names jev")
	require.Len(t, sink.hits, 1)
	require.Equal(t, "jev:0.91", sink.hits[0].Keyword)
	require.Equal(t, "client:r1", sink.hits[0].RequestID)
	require.Contains(t, sink.hits[0].Excerpt, "キャンディー")
	require.Zero(t, f.classifier.calls)
}

func TestIntentJev_BelowThresholdIsNoPreference(t *testing.T) {
	f, judge, sink := newJevFixture(t, 0.49, nil)
	requireNoPreference(t, f.svc.Decide(context.Background(), f.request(userTurns("帮我改一下这个函数的命名"))))
	require.Equal(t, 1, judge.calls)
	require.Empty(t, sink.hits)
}

func TestIntentJev_KeywordHitNeverAsksJev(t *testing.T) {
	f, judge, _ := newJevFixture(t, 0.99, nil)
	require.Equal(t, "coding", f.svc.Decide(context.Background(), f.request(userTurns("画一只鹈鹕"))).Intent)
	require.Zero(t, judge.calls)
}

func TestIntentJev_LongConversationsAreNotSent(t *testing.T) {
	f, judge, _ := newJevFixture(t, 0.99, func(j *domain.IntentJevConfig) { j.MaxTurns = 3 })
	// userTurns(a, b) is user, assistant, user: three items, still short.
	require.Equal(t, "coding", f.svc.Decide(context.Background(), f.request(userTurns("第一句", "Reply OK"))).Intent)
	require.Equal(t, 1, judge.calls)
	// Five items: a working session, never judged.
	requireNoPreference(t, f.svc.Decide(context.Background(), f.request(userTurns("一", "二", "Reply OK"))))
	require.Equal(t, 1, judge.calls)
}

// jevUserWithKeys creates a user with API keys and returns their IDs.
func jevUserWithKeys(t *testing.T, f *intentRouteFixture, email string, keys int) (int64, []int64) {
	t.Helper()
	ctx := context.Background()
	u, err := f.client.User.Create().SetEmail(email).SetPasswordHash("hash").SetUsername(email).Save(ctx)
	require.NoError(t, err)
	var ids []int64
	for i := 0; i < keys; i++ {
		k, err := f.client.APIKey.Create().SetUserID(u.ID).SetKey(fmt.Sprintf("sk-%s-%d", email, i)).SetName(fmt.Sprintf("%s-key-%d", email, i)).Save(ctx)
		require.NoError(t, err)
		ids = append(ids, k.ID)
	}
	return u.ID, ids
}

func TestIntentJev_ListedScope(t *testing.T) {
	f := newIntentRouteFixture(t)
	judge := &scriptedProbeJudge{p: 0.99}
	f.svc.judge, f.svc.jevStats = judge, newMemoryIntentJevStats()
	alice, aliceKeys := jevUserWithKeys(t, f, "alice@x.com", 3)
	bob, bobKeys := jevUserWithKeys(t, f, "bob@x.com", 2)
	carol, carolKeys := jevUserWithKeys(t, f, "carol@x.com", 1)
	// Alice narrowed to one key; Bob with no key chosen: all of his.
	view := f.saveRouterView(t, jevOn(func(j *domain.IntentJevConfig) {
		j.Scope, j.UserIDs = domain.IntentJevScopeListed, []int64{alice, bob}
		j.APIKeys = []domain.IntentJevAPIKey{{ID: aliceKeys[1]}}
	}))
	require.Equal(t, []domain.IntentJevAPIKey{{ID: aliceKeys[1], UserID: alice}}, view.Jev.APIKeys, "the owner is filled in by the server")
	require.Equal(t, []IntentJevUserLabel{{ID: alice, Email: "alice@x.com", Username: "alice@x.com"}, {ID: bob, Email: "bob@x.com", Username: "bob@x.com"}}, view.JevScope.Users)
	require.Equal(t, []IntentJevAPIKeyLabel{{ID: aliceKeys[1], Name: "alice@x.com-key-1", UserID: alice}}, view.JevScope.APIKeys)

	ctx := context.Background()
	body := []byte(userTurns("Reply OK"))
	judged := func(userID, keyID int64) bool {
		before := judge.calls
		f.svc.Decide(ctx, IntentRouteInput{GroupID: f.groupID, UserID: userID, APIKeyID: keyID, Body: body})
		return judge.calls > before
	}
	require.True(t, judged(alice, aliceKeys[1]), "alice's chosen key")
	require.False(t, judged(alice, aliceKeys[0]), "alice's other keys are not covered")
	require.True(t, judged(bob, bobKeys[0]), "bob: no key chosen, all of his")
	require.True(t, judged(bob, bobKeys[1]))
	require.False(t, judged(carol, carolKeys[0]), "carol is not listed")
	require.False(t, judged(carol, aliceKeys[1]), "a listed key does not cover another user")
}

func TestIntentJev_ScopeValidation(t *testing.T) {
	f := newIntentRouteFixture(t)
	alice, aliceKeys := jevUserWithKeys(t, f, "alice@x.com", 1)
	_, bobKeys := jevUserWithKeys(t, f, "bob@x.com", 1)
	save := func(mutate func(*domain.IntentJevConfig)) error {
		in := IntentRouterInput{Enabled: true, Rules: []domain.IntentRule{
			{Name: "coding", Enabled: true, AccountIDs: []int64{f.accountID(t, "coding-1")}},
			{Name: "chat", Enabled: true, AccountIDs: []int64{f.accountID(t, "pool-a")}},
		}}
		jevOn(mutate)(&in)
		_, err := f.svc.SaveRouter(context.Background(), f.groupID, in)
		return err
	}
	require.Equal(t, "INTENT_JEV_SCOPE_EMPTY", infraerrors.Reason(save(func(j *domain.IntentJevConfig) {
		j.Scope, j.APIKeys = domain.IntentJevScopeListed, []domain.IntentJevAPIKey{{ID: aliceKeys[0]}}
	})), "keys alone are not a scope: choose their user")
	require.Equal(t, "INTENT_JEV_API_KEY_USER_NOT_LISTED", infraerrors.Reason(save(func(j *domain.IntentJevConfig) {
		j.Scope, j.UserIDs, j.APIKeys = domain.IntentJevScopeListed, []int64{alice}, []domain.IntentJevAPIKey{{ID: bobKeys[0]}}
	})))
	require.Equal(t, "INTENT_JEV_API_KEY_NOT_FOUND", infraerrors.Reason(save(func(j *domain.IntentJevConfig) {
		j.Scope, j.UserIDs, j.APIKeys = domain.IntentJevScopeListed, []int64{alice}, []domain.IntentJevAPIKey{{ID: 999999}}
	})))
	require.NoError(t, save(func(j *domain.IntentJevConfig) {
		j.Scope, j.UserIDs, j.APIKeys = domain.IntentJevScopeListed, []int64{alice}, []domain.IntentJevAPIKey{{ID: aliceKeys[0]}, {ID: aliceKeys[0]}}
	}))
}

func TestIntentJev_TemporaryFailuresPauseWithBackoff(t *testing.T) {
	f, judge, _ := newJevFixture(t, 0, nil)
	judge.err = &intentJevStatusError{Status: 529, Body: "overloaded"}
	ctx := context.Background()
	ask := func() { requireNoPreference(t, f.svc.Decide(ctx, f.request(userTurns("Reply OK")))) }

	ask()
	ask()
	require.Equal(t, 2, judge.calls, "two failures in a row pause Jev")
	ask()
	require.Equal(t, 2, judge.calls, "paused: not asked, no waiting")
	st, err := f.svc.JevStatus(ctx, f.groupID)
	require.NoError(t, err)
	require.Equal(t, "paused", st.State)
	require.Equal(t, f.now.Add(time.Minute), *st.PausedUntil)
	require.Contains(t, st.Reason, "529")
	require.True(t, f.svc.breakerAllows(f.groupID), "the classifier's breaker of the group is untouched")

	// The first failure after a pause pauses again, twice as long, capped.
	for _, want := range []time.Duration{2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 10 * time.Minute, 10 * time.Minute} {
		f.now = f.now.Add(time.Hour)
		ask()
		st, _ = f.svc.JevStatus(ctx, f.groupID)
		require.Equal(t, f.now.Add(want), *st.PausedUntil)
	}

	// One success resets everything.
	f.now = f.now.Add(time.Hour)
	judge.err, judge.p = nil, 0.9
	require.Equal(t, "coding", f.svc.Decide(ctx, f.request(userTurns("Reply OK"))).Intent)
	st, _ = f.svc.JevStatus(ctx, f.groupID)
	require.Equal(t, "normal", st.State)
	require.Equal(t, f.now, *st.LastSuccessAt)
	judge.err = errors.New("dial tcp: timeout")
	ask()
	st, _ = f.svc.JevStatus(ctx, f.groupID)
	require.Equal(t, "normal", st.State, "a single failure after a success does not pause")
}

func TestIntentJev_AccountProblemsSwitchJevOff(t *testing.T) {
	for _, status := range []int{401, 402, 403, 451} {
		f, judge, _ := newJevFixture(t, 0, nil)
		judge.err = &intentJevStatusError{Status: status, Body: "insufficient credits"}
		ctx := context.Background()
		requireNoPreference(t, f.svc.Decide(ctx, f.request(userTurns("Reply OK"))))
		requireNoPreference(t, f.svc.Decide(ctx, f.request(userTurns("Reply OK"))))
		require.Equal(t, 1, judge.calls, "status %d: switched off at once, never asked again", status)

		view, err := f.svc.GetRouter(ctx, f.groupID)
		require.NoError(t, err)
		require.False(t, view.Jev.Enabled, "the switch-off is saved")
		require.Equal(t, status, view.Jev.AutoDisabledStatus)
		require.Contains(t, view.Jev.AutoDisabledDetail, "insufficient credits")
		require.True(t, f.now.Equal(*view.Jev.AutoDisabledAt))
		st, err := f.svc.JevStatus(ctx, f.groupID)
		require.NoError(t, err)
		require.Equal(t, "auto_disabled", st.State)

		// Saving without turning Jev on keeps the record; turning it on clears it.
		f.saveRouter(t, jevOn(func(j *domain.IntentJevConfig) { j.Enabled = false }))
		view, _ = f.svc.GetRouter(ctx, f.groupID)
		require.Equal(t, status, view.Jev.AutoDisabledStatus)
		judge.err, judge.p = nil, 0.9
		f.saveRouter(t, jevOn(nil))
		view, _ = f.svc.GetRouter(ctx, f.groupID)
		require.True(t, view.Jev.Enabled)
		require.Zero(t, view.Jev.AutoDisabledStatus)
		require.Nil(t, view.Jev.AutoDisabledAt)
		require.Equal(t, "coding", f.svc.Decide(ctx, f.request(userTurns("Reply OK"))).Intent)
	}
}

func TestIntentJev_SamplingSkipsWithoutAsking(t *testing.T) {
	f, judge, _ := newJevFixture(t, 0.9, func(j *domain.IntentJevConfig) { j.SamplePercent = 80 })
	ctx := context.Background()
	f.svc.roll = func() int { return 80 } // the 21st..100th percentile: not sampled
	requireNoPreference(t, f.svc.Decide(ctx, f.request(userTurns("Reply OK"))))
	require.Zero(t, judge.calls)
	f.svc.roll = func() int { return 79 }
	require.Equal(t, "coding", f.svc.Decide(ctx, f.request(userTurns("Reply OK"))).Intent)
	require.Equal(t, 1, judge.calls)

	// Requests that are never eligible are not counted as skipped.
	f.svc.roll = func() int { return 99 }
	requireNoPreference(t, f.svc.Decide(ctx, f.request(userTurns("一", "二", "三"))))
	st, err := f.svc.JevStatus(ctx, f.groupID)
	require.NoError(t, err)
	require.Equal(t, int64(1), st.Days[0].Skipped)
	require.Equal(t, int64(1), st.Days[0].Calls)
	require.Equal(t, int64(1), st.Days[0].Probes)
}

func TestIntentJev_StatsAndEstimatedBalance(t *testing.T) {
	f, judge, _ := newJevFixture(t, 0.3, func(j *domain.IntentJevConfig) { j.LowBalanceUSD = jevFloat(0.5) })
	ctx := context.Background()
	judge.tokens = 500_000
	requireNoPreference(t, f.svc.Decide(ctx, f.request(userTurns("帮我改个函数名"))))
	st, err := f.svc.JevStatus(ctx, f.groupID)
	require.NoError(t, err)
	require.Len(t, st.Days, intentJevStatsDays)
	require.Equal(t, f.now.In(intentJevDayZone).Format("20060102"), st.Days[0].Date)
	require.Equal(t, int64(500_000), st.Days[0].Tokens)
	require.InDelta(t, 0.021, st.Days[0].CostUSD, 1e-9)
	require.Nil(t, st.Balance, "no balance entered yet")

	// Entering the balance restarts the estimate from it.
	balance := 0.55
	f.saveRouter(t, func(in *IntentRouterInput) {
		jevOn(func(j *domain.IntentJevConfig) { j.LowBalanceUSD = jevFloat(0.5) })(in)
		in.JevSetBalanceUSD = &balance
	})
	judge.tokens = 2_000_000 // $0.084
	requireNoPreference(t, f.svc.Decide(ctx, f.request(userTurns("帮我改个函数名"))))
	st, err = f.svc.JevStatus(ctx, f.groupID)
	require.NoError(t, err)
	require.NotNil(t, st.Balance)
	require.Equal(t, 0.55, st.Balance.SetUSD)
	require.True(t, f.now.Equal(st.Balance.SetAt))
	require.Equal(t, int64(2_000_000), st.Balance.SpentTokens, "spending before the balance was entered does not count")
	require.InDelta(t, 0.466, st.Balance.EstimatedUSD, 1e-9)
	require.True(t, st.Balance.Low)
	require.Equal(t, int64(2_500_000), st.Days[0].Tokens, "daily usage keeps counting")

	// A plain save keeps the baseline; the browser cannot overwrite it.
	f.saveRouter(t, jevOn(func(j *domain.IntentJevConfig) { j.BalanceUSD = 999 }))
	view, _ := f.svc.GetRouter(ctx, f.groupID)
	require.Equal(t, 0.55, view.Jev.BalanceUSD)
}

func TestIntentJev_SaveValidationAndKeyHandling(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	save := func(mutate func(*IntentRouterInput)) (*IntentRouterView, error) {
		in := IntentRouterInput{Enabled: true, Rules: []domain.IntentRule{
			{Name: "coding", Enabled: true, AccountIDs: []int64{f.accountID(t, "coding-1")}},
			{Name: "chat", Enabled: true, AccountIDs: []int64{f.accountID(t, "pool-a")}},
		}}
		jevOn(nil)(&in)
		if mutate != nil {
			mutate(&in)
		}
		return f.svc.SaveRouter(ctx, f.groupID, in)
	}
	reason := func(err error) string { return infraerrors.Reason(err) }

	_, err := save(func(in *IntentRouterInput) { in.JevAPIKey = "" })
	require.Equal(t, "INTENT_JEV_KEY_REQUIRED", reason(err))
	_, err = save(func(in *IntentRouterInput) { in.Jev.Rule = "nope" })
	require.Equal(t, "INTENT_JEV_RULE_REQUIRED", reason(err))
	_, err = save(func(in *IntentRouterInput) { in.Jev.Scope = domain.IntentJevScopeListed })
	require.Equal(t, "INTENT_JEV_SCOPE_EMPTY", reason(err))
	_, err = save(func(in *IntentRouterInput) { in.Jev.Threshold = 1.5 })
	require.Equal(t, "INTENT_JEV_INVALID_THRESHOLD", reason(err))

	// A rule with neither keywords nor a description is fine as Jev's target.
	view, err := save(func(in *IntentRouterInput) { in.Rules[0].Keywords, in.Rules[0].Description = nil, "" })
	require.NoError(t, err)
	require.True(t, view.JevAPIKeyConfigured)
	require.Equal(t, 0.5, view.Jev.Threshold)
	require.Equal(t, 3, view.Jev.MaxTurns)
	require.Equal(t, domain.IntentJevScopeAll, view.Jev.Scope)
	raw, _ := json.Marshal(view)
	require.NotContains(t, string(raw), "jev-secret", "the key never leaves the server")

	// Saving with an empty key keeps the stored one.
	_, err = save(func(in *IntentRouterInput) {
		in.JevAPIKey = ""
		in.Rules[0].Keywords, in.Rules[0].Description = nil, ""
	})
	require.NoError(t, err)
	judge := &scriptedProbeJudge{p: 0.8}
	f.svc.judge = judge
	require.Equal(t, "coding", f.svc.Decide(ctx, f.request(userTurns("Reply OK"))).Intent)
	require.Equal(t, "jev-secret", judge.keys[0])

	// Disabled Jev may be saved without a key or rule.
	_, err = f.svc.SaveRouter(ctx, f.groupID, IntentRouterInput{Enabled: true, Rules: []domain.IntentRule{
		{Name: "coding", Keywords: []string{"x"}, Enabled: true, AccountIDs: []int64{f.accountID(t, "coding-1")}},
	}, Jev: domain.IntentJevConfig{Rule: "gone"}})
	require.NoError(t, err)
}

func TestIntentJev_TestClassifyReportsProbability(t *testing.T) {
	f, judge, _ := newJevFixture(t, 0.87, func(j *domain.IntentJevConfig) {
		j.Scope, j.UserIDs = domain.IntentJevScopeListed, []int64{14}
	})
	res, err := f.svc.TestClassify(context.Background(), f.groupID, "Reply only OK.")
	require.NoError(t, err)
	require.Equal(t, "jev", res.MatchedBy)
	require.Equal(t, "coding", res.Intent)
	require.InDelta(t, 0.87, *res.ProbeProbability, 1e-9)
	require.Equal(t, 1, judge.calls, "the admin test ignores the user scope")

	judge.p = 0.2
	res, err = f.svc.TestClassify(context.Background(), f.groupID, "帮我重构这个模块")
	require.NoError(t, err)
	require.Equal(t, "jev", res.MatchedBy)
	require.Empty(t, res.Intent)
}

func jevFloat(v float64) *float64 { return &v }

// Review fix: a switch-off on one instance must not outlive a re-enable saved
// through another instance.
func TestIntentJev_ReenabledElsewhereIsAskedAgain(t *testing.T) {
	f, judge, _ := newJevFixture(t, 0.9, nil)
	ctx := context.Background()
	judge.err = &intentJevStatusError{Status: 402, Body: "payment required"}
	requireNoPreference(t, f.svc.Decide(ctx, f.request(userTurns("Reply OK"))))

	// Another instance: same database, its own memory.
	other := &IntentRouterService{entClient: f.client, store: newMemoryIntentRouteStore(), judge: judge, jevStats: newMemoryIntentJevStats(),
		breakers: map[int64]*intentRouteBreaker{}, now: func() time.Time { return f.now }, spawn: func(work func()) { work() }}
	in := IntentRouterInput{Enabled: true, Rules: f.svc.mustRouter(t, f.groupID).Rules}
	jevOn(nil)(&in)
	_, err := other.SaveRouter(ctx, f.groupID, in)
	require.NoError(t, err)

	judge.err = nil
	f.svc.InvalidateConfig(ctx) // this instance picks up the new version
	require.Equal(t, "coding", f.svc.Decide(ctx, f.request(userTurns("Reply OK"))).Intent)
	require.Equal(t, 2, judge.calls)
}

// Review fix: a switch-off triggered under an old configuration must not
// turn off one an admin saved since (new key, Jev re-enabled).
func TestIntentJev_StaleSwitchOffLeavesNewerConfigAlone(t *testing.T) {
	f, _, _ := newJevFixture(t, 0.9, nil)
	ctx := context.Background()
	old := f.svc.config(ctx, f.groupID)
	require.True(t, old.Jev.Enabled)

	for _, change := range []func(*IntentRouterInput){
		func(in *IntentRouterInput) { in.JevAPIKey = "jev-new-key" },
		func(in *IntentRouterInput) {}, // same key, saved again
	} {
		in := IntentRouterInput{Enabled: true, Rules: f.svc.mustRouter(t, f.groupID).Rules}
		jevOn(nil)(&in)
		change(&in)
		_, err := f.svc.SaveRouter(ctx, f.groupID, in)
		require.NoError(t, err)

		f.svc.jevSwitchOff(old, 402, "payment required") // the late failure of an old request
		view, err := f.svc.GetRouter(ctx, f.groupID)
		require.NoError(t, err)
		require.True(t, view.Jev.Enabled, "the newer configuration stays on")
		require.Zero(t, view.Jev.AutoDisabledStatus)
		require.True(t, f.svc.jevAvailable(f.groupID, f.svc.config(ctx, f.groupID).Jev.Revision), "and is asked")
	}
}

// Review fix (round 2): a router deleted and created again must not inherit a
// switch-off another instance still remembers.
func TestIntentJev_RecreatedRouterIsNotSeenAsSwitchedOff(t *testing.T) {
	f, judge, _ := newJevFixture(t, 0.9, nil)
	ctx := context.Background()
	rules := f.svc.mustRouter(t, f.groupID).Rules
	judge.err = &intentJevStatusError{Status: 402, Body: "payment required"}
	requireNoPreference(t, f.svc.Decide(ctx, f.request(userTurns("Reply OK"))))

	other := &IntentRouterService{entClient: f.client, store: newMemoryIntentRouteStore(), judge: judge, jevStats: newMemoryIntentJevStats(),
		breakers: map[int64]*intentRouteBreaker{}, now: func() time.Time { return f.now }, spawn: func(work func()) { work() }}
	require.NoError(t, other.DeleteRouter(ctx, f.groupID))
	in := IntentRouterInput{Enabled: true, Rules: rules}
	jevOn(nil)(&in)
	in.JevAPIKey = "jev-topped-up"
	_, err := other.SaveRouter(ctx, f.groupID, in)
	require.NoError(t, err)

	judge.err = nil
	f.svc.InvalidateConfig(ctx)
	require.Equal(t, "coding", f.svc.Decide(ctx, f.request(userTurns("Reply OK"))).Intent)
	require.Equal(t, "jev-topped-up", judge.keys[len(judge.keys)-1])
}

// Review fix (round 2): two saves never share a revision, so a late
// switch-off of the first cannot pass for the second.
func TestIntentJev_EverySaveDrawsANewRevision(t *testing.T) {
	f, _, _ := newJevFixture(t, 0.9, nil)
	ctx := context.Background()
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		in := IntentRouterInput{Enabled: true, Rules: f.svc.mustRouter(t, f.groupID).Rules}
		jevOn(nil)(&in)
		view, err := f.svc.SaveRouter(ctx, f.groupID, in)
		require.NoError(t, err)
		require.Len(t, view.Jev.Revision, 32)
		require.False(t, seen[view.Jev.Revision], "revision reused")
		seen[view.Jev.Revision] = true
	}
	// Both racing saves read the same stored revision; each still gets its own.
	stored := f.svc.mustRouter(t, f.groupID).Jev
	a, b := domain.IntentJevConfig{}, domain.IntentJevConfig{}
	require.NoError(t, normalizeIntentJev(&a, stored, nil, "", nil, f.now))
	require.NoError(t, normalizeIntentJev(&b, stored, nil, "", nil, f.now))
	require.NotEqual(t, a.Revision, b.Revision)
}

// Review fix: a rule kept only as Jev's target must stay savable once Jev is
// switched off, by an admin or by the server.
func TestIntentJev_TargetRuleWithoutMatcherSavesWithJevOff(t *testing.T) {
	f := newIntentRouteFixture(t)
	ctx := context.Background()
	coding := domain.IntentRule{Name: "coding", Enabled: true, AccountIDs: []int64{f.accountID(t, "coding-1")}}
	for _, routerOn := range []bool{true, false} {
		_, err := f.svc.SaveRouter(ctx, f.groupID, IntentRouterInput{Enabled: routerOn, Rules: []domain.IntentRule{coding},
			JevAPIKey: "jev-secret", Jev: domain.IntentJevConfig{Enabled: false, Rule: "coding"}})
		require.NoError(t, err, "router enabled=%v", routerOn)
	}
	// Without being Jev's target the rule is still rejected.
	_, err := f.svc.SaveRouter(ctx, f.groupID, IntentRouterInput{Enabled: true, Rules: []domain.IntentRule{coding}})
	require.Equal(t, "INTENT_RULE_MATCHER_REQUIRED", infraerrors.Reason(err))
}

// Review fix: an alert threshold of 0 is a setting, not "unset".
func TestIntentJev_LowBalanceZeroIsKept(t *testing.T) {
	f, _, _ := newJevFixture(t, 0.3, func(j *domain.IntentJevConfig) { j.LowBalanceUSD = jevFloat(0) })
	view, err := f.svc.GetRouter(context.Background(), f.groupID)
	require.NoError(t, err)
	require.Equal(t, 0.0, *view.Jev.LowBalanceUSD)

	legacy, _, _ := newJevFixture(t, 0.3, nil)
	view, _ = legacy.svc.GetRouter(context.Background(), legacy.groupID)
	require.Equal(t, 1.0, *view.Jev.LowBalanceUSD, "never set: the default")
}

func TestIntentJev_TestClassifyAsksEvenWhenSwitchedOff(t *testing.T) {
	f, judge, _ := newJevFixture(t, 0.95, func(j *domain.IntentJevConfig) { j.Enabled = false })
	res, err := f.svc.TestClassify(context.Background(), f.groupID, "Reply OK")
	require.NoError(t, err)
	require.Equal(t, "jev", res.MatchedBy)
	require.Equal(t, "coding", res.Intent)

	judge.err = &intentJevStatusError{Status: 402, Body: "payment required"}
	_, err = f.svc.TestClassify(context.Background(), f.groupID, "Reply OK")
	require.Equal(t, "INTENT_JEV_FAILED", infraerrors.Reason(err))
	require.Equal(t, "402", infraerrors.FromError(err).Metadata["status"])
	require.True(t, f.svc.jevAvailable(f.groupID, f.svc.config(context.Background(), f.groupID).Jev.Revision), "a failed try-it neither pauses nor switches off")
}

func TestHTTPIntentProbeJudge_ParsesNoul(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer k1", r.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		if strings.Contains(got["state"].(string), "fail") {
			http.Error(w, `{"title":"Typesafe is not available in your region."}`, http.StatusUnavailableForLegalReasons)
			return
		}
		_, _ = w.Write([]byte(`{"model":"jev-1.14.0","answers":{"is_probe":{"type":"noul","noul":0.67}},"usage":{"input_tokens":306}}`))
	}))
	defer srv.Close()
	j := &httpIntentProbeJudge{client: srv.Client(), endpoint: srv.URL}

	answer, err := j.ProbeProbability(context.Background(), "k1", "jev-latest", strings.Repeat("长", intentJevMaxTextRunes+10), false)
	require.NoError(t, err)
	require.Equal(t, 0.67, answer.P)
	require.Equal(t, int64(306), answer.Tokens)
	require.Equal(t, "jev-latest", got["model"])
	require.Len(t, []rune(got["state"].(string)), intentJevMaxTextRunes, "the state is cut to the rune limit")
	q := got["questions"].(map[string]any)["is_probe"].(map[string]any)
	require.Equal(t, "noul", q["type"])

	_, err = j.ProbeProbability(context.Background(), "k1", "jev-latest", "fail", false)
	status, account := intentJevAccountProblem(err)
	require.True(t, account)
	require.Equal(t, 451, status)
}

// TestHTTPIntentProbeJudge_Live calls the real Jev API; it runs only with
// INTENT_JEV_LIVE_KEY set (TypeSafe answers 451 from some regions).
func TestHTTPIntentProbeJudge_Live(t *testing.T) {
	key := os.Getenv("INTENT_JEV_LIVE_KEY")
	if key == "" {
		t.Skip("INTENT_JEV_LIVE_KEY not set")
	}
	j := newHTTPIntentProbeJudge()
	for _, c := range []struct {
		text  string
		probe bool
	}{
		{"黒い袋の中に3種類の味のキャンディーが入っており、それぞれの味のキャンディーには2種類の異なる形（丸形と星形）がある。少なくとも何個取り出さなければならないか？", true},
		{"Reply with exactly OK.", true},
		{"帮我看下这个 Go 服务启动时报 dial tcp 127.0.0.1:5432: connect: connection refused 是什么原因，docker compose 里 postgres 已经起来了", false},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		answer, err := j.ProbeProbability(ctx, key, intentJevDefaultModel, c.text, false)
		cancel()
		require.NoError(t, err)
		t.Logf("p=%.2f tokens=%d probe=%v %s", answer.P, answer.Tokens, c.probe, truncateRunes(c.text, 24))
		require.Equal(t, c.probe, answer.P >= intentJevDefaultThreshold, c.text)
		require.Positive(t, answer.Tokens)
	}
}

func (f *intentRouteFixture) saveRouterView(t *testing.T, mutate func(*IntentRouterInput)) *IntentRouterView {
	t.Helper()
	f.saveRouter(t, mutate)
	return f.svc.mustRouter(t, f.groupID)
}

func (s *IntentRouterService) mustRouter(t *testing.T, groupID int64) *IntentRouterView {
	t.Helper()
	view, err := s.GetRouter(context.Background(), groupID)
	require.NoError(t, err)
	return view
}
