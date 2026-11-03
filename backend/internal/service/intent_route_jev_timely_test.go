//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func timelyOn(rule string) func(*domain.IntentJevConfig) {
	return func(j *domain.IntentJevConfig) {
		j.TimelyEnabled, j.TimelyRule = true, rule
	}
}

func f64(v float64) *float64 { return &v }

// With the switch off the request to Jev must be byte-for-byte what it was
// before the time-sensitivity question existed, health checks on or off.
func TestHTTPIntentProbeJudge_TimelyOffKeepsTheRequestUnchanged(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"answers":{"is_probe":{"noul":0.2},"is_health":{"noul":0.1}},"usage":{"input_tokens":500}}`))
	}))
	defer srv.Close()
	j := &httpIntentProbeJudge{client: srv.Client(), endpoint: srv.URL}

	for _, health := range []bool{false, true} {
		legacy := map[string]any{"is_probe": map[string]any{"type": "noul", "instructions": intentJevProbeQuestion}}
		if health {
			legacy["is_health"] = map[string]any{"type": "noul", "instructions": intentJevHealthQuestion}
		}
		want, err := json.Marshal(map[string]any{"state": "日本的首相是谁", "model": "jev-latest", "questions": legacy})
		require.NoError(t, err)

		answer, err := j.ProbeProbability(context.Background(), "k", "jev-latest", "日本的首相是谁", intentJevQuestions{Health: health})
		require.NoError(t, err)
		require.Equal(t, string(want), string(raw), "health=%v", health)
		require.Nil(t, answer.Timely)
	}
}

func TestHTTPIntentProbeJudge_AsksTheTimelyQuestion(t *testing.T) {
	var got map[string]any
	answer := `{"answers":{"is_probe":{"noul":0.3},"is_health":{"noul":0.1},"is_timely":{"noul":0.93}},"usage":{"input_tokens":606}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()
	j := &httpIntentProbeJudge{client: srv.Client(), endpoint: srv.URL}

	res, err := j.ProbeProbability(context.Background(), "k", "jev-latest", "日本的首相是谁，不要使用联网搜索", intentJevQuestions{Health: true, Timely: true})
	require.NoError(t, err)
	require.Equal(t, 0.3, res.P)
	require.Equal(t, 0.1, *res.Health)
	require.Equal(t, 0.93, *res.Timely)
	q := got["questions"].(map[string]any)
	require.Len(t, q, 3)
	require.Equal(t, intentJevTimelyQuestion, q["is_timely"].(map[string]any)["instructions"])
	require.Equal(t, intentJevProbeQuestion, q["is_probe"].(map[string]any)["instructions"], "the probe question is untouched")
}

// A bad time-sensitivity answer only drops that answer: the probe and health
// verdicts stay usable and the call does not fail.
func TestHTTPIntentProbeJudge_BadTimelyAnswerDropsOnlyTimely(t *testing.T) {
	for name, timely := range map[string]string{
		"missing":      ``,
		"string":       `,"is_timely":{"noul":"0.9"}`,
		"object":       `,"is_timely":{"noul":{"v":0.9}}`,
		"null":         `,"is_timely":{"noul":null}`,
		"not object":   `,"is_timely":"high"`,
		"above one":    `,"is_timely":{"noul":1.5}`,
		"below zero":   `,"is_timely":{"noul":-0.1}`,
		"no noul":      `,"is_timely":{"type":"noul"}`,
		"array answer": `,"is_timely":[0.9]`,
	} {
		t.Run(name, func(t *testing.T) {
			body := `{"answers":{"is_probe":{"noul":0.85},"is_health":{"noul":0.2}` + timely + `},"usage":{"input_tokens":606}}`
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer srv.Close()
			j := &httpIntentProbeJudge{client: srv.Client(), endpoint: srv.URL}
			res, err := j.ProbeProbability(context.Background(), "k", "jev-latest", "x", intentJevQuestions{Health: true, Timely: true})
			require.NoError(t, err)
			require.Equal(t, 0.85, res.P)
			require.Equal(t, 0.2, *res.Health)
			require.Nil(t, res.Timely)
		})
	}
}

func TestIntentJevTimely_OffNeverAsks(t *testing.T) {
	f, judge, _ := newJevFixture(t, 0.1, nil)
	judge.timely = f64(0.99)
	requireNoPreference(t, f.svc.Decide(context.Background(), f.request(userTurns("日本的首相是谁"))))
	require.Equal(t, []bool{false}, judge.askedTimely)
	require.Zero(t, jevDayCount(t, f, intentJevStatTimelyAnswered))
}

func TestIntentJevTimely_RoutesToTheTimelyRule(t *testing.T) {
	f, judge, sink := newJevFixture(t, 0.1, timelyOn("CHAT"))
	require.Equal(t, "chat", f.svc.mustRouter(t, f.groupID).Jev.TimelyRule, "the rule name is canonicalised")
	judge.timely = f64(0.95)
	in := f.request(userTurns("日本的首相是谁，不要使用联网搜索"))
	in.ClientRequestID = "r1"

	d := f.svc.Decide(context.Background(), in)
	require.Equal(t, "chat", d.Intent)
	require.Equal(t, []int64{f.accountID(t, "pool-a")}, d.AccountIDs)
	require.Equal(t, []bool{true}, judge.askedTimely)
	require.Equal(t, 1, judge.calls, "one Jev call carries every question")
	require.Len(t, sink.hits, 1)
	require.Equal(t, "timely:0.95", sink.hits[0].Keyword)
	require.Equal(t, "chat", sink.hits[0].Rule)
	require.Equal(t, []string{"jev_timely"}, f.store.kinds())
	require.Equal(t, int64(1), jevDayCount(t, f, intentJevStatTimelyAnswered))
	require.Equal(t, int64(1), jevDayCount(t, f, intentJevStatTimelyHits))
	require.Equal(t, int64(1), jevDayCount(t, f, intentJevStatTimelyRouted))
	require.Equal(t, int64(1), jevDayCount(t, f, intentJevStatCalls))
	require.Zero(t, jevDayCount(t, f, intentJevStatProbes))
	require.Zero(t, f.classifier.calls)
}

func TestIntentJevTimely_BelowThresholdIsNoPreference(t *testing.T) {
	f, judge, sink := newJevFixture(t, 0.1, timelyOn("chat"))
	judge.timely = f64(0.89) // default threshold 0.9
	requireNoPreference(t, f.svc.Decide(context.Background(), f.request(userTurns("帮我写一个快速排序"))))
	require.Empty(t, sink.hits)
	require.Equal(t, int64(1), jevDayCount(t, f, intentJevStatTimelyAnswered))
	require.Zero(t, jevDayCount(t, f, intentJevStatTimelyHits))

	f2, judge2, _ := newJevFixture(t, 0.1, func(j *domain.IntentJevConfig) { timelyOn("chat")(j); j.TimelyThreshold = 0.85 })
	judge2.timely = f64(0.86)
	require.Equal(t, "chat", f2.svc.Decide(context.Background(), f2.request(userTurns("美国现任总统是谁"))).Intent, "the threshold is configurable")
}

func TestIntentJevTimely_ProbeAndHealthComeFirst(t *testing.T) {
	// A probe wins over time sensitivity.
	f, judge, _ := newJevFixture(t, 0.95, timelyOn("chat"))
	judge.timely = f64(0.99)
	require.Equal(t, "coding", f.svc.Decide(context.Background(), f.request(userTurns("黒い袋の中に3種類の味のキャンディー"))).Intent)
	require.Equal(t, int64(1), jevDayCount(t, f, intentJevStatProbes))
	require.Equal(t, int64(1), jevDayCount(t, f, intentJevStatTimelyHits), "counted as a hit even though the probe took it")
	require.Zero(t, jevDayCount(t, f, intentJevStatTimelyRouted))

	// A health check wins too, also when left to ordinary scheduling.
	f2, judge2, _ := newJevFixture(t, 0.1, func(j *domain.IntentJevConfig) { healthOn("")(j); timelyOn("chat")(j) })
	judge2.health, judge2.timely = f64(0.98), f64(0.99)
	requireNoPreference(t, f2.svc.Decide(context.Background(), f2.request(userTurns("ping"))))
	require.Equal(t, []bool{true}, judge2.asked)
	require.Equal(t, []bool{true}, judge2.askedTimely)
	require.Equal(t, int64(1), jevDayCount(t, f2, intentJevStatHealth))
	require.Zero(t, jevDayCount(t, f2, intentJevStatTimelyRouted))
}

// A target rule that is disabled at runtime is skipped: the request goes on
// as if time sensitivity were not asked.
func TestIntentJevTimely_DisabledTargetIsSkipped(t *testing.T) {
	f, judge, sink := newJevFixture(t, 0.1, nil)
	f.saveRouter(t, func(in *IntentRouterInput) {
		jevOn(timelyOn("chat"))(in)
		for i := range in.Rules {
			if in.Rules[i].Name == "chat" {
				in.Rules[i].Enabled = false
			}
		}
	})
	judge.timely = f64(0.97)
	requireNoPreference(t, f.svc.Decide(context.Background(), f.request(userTurns("今天比特币价格多少"))))
	require.Empty(t, sink.hits)
	require.Equal(t, int64(1), jevDayCount(t, f, intentJevStatTimelyHits))
	require.Zero(t, jevDayCount(t, f, intentJevStatTimelyRouted))
}

func TestIntentJevTimely_LongConversationsAreNotAsked(t *testing.T) {
	f, judge, _ := newJevFixture(t, 0.1, timelyOn("chat"))
	judge.timely = f64(0.99)
	requireNoPreference(t, f.svc.Decide(context.Background(), f.request(userTurns("a", "b", "c", "日本的首相是谁"))))
	require.Zero(t, judge.calls, "max_turns still gates Jev")
}

func TestIntentJevTimely_Validation(t *testing.T) {
	f, _, _ := newJevFixture(t, 0.5, nil)
	save := func(mutate func(*domain.IntentJevConfig)) error {
		in := IntentRouterInput{Enabled: true}
		in.Rules = []domain.IntentRule{
			{Name: "coding", Keywords: []string{"鹈鹕"}, Enabled: true, AccountIDs: []int64{f.accountID(t, "coding-1")}},
			// No keywords: reachable only as Jev's time-sensitivity rule.
			{Name: "fresh", Enabled: true, AccountIDs: []int64{f.accountID(t, "pool-a")}},
		}
		in.JevAPIKey = "jev-secret"
		in.Jev = domain.IntentJevConfig{Enabled: true, Rule: "coding", TimelyEnabled: true, TimelyRule: "fresh"}
		mutate(&in.Jev)
		_, err := f.svc.SaveRouter(context.Background(), f.groupID, in)
		return err
	}
	require.NoError(t, save(func(*domain.IntentJevConfig) {}), "the timely rule needs no matcher of its own")
	view := f.svc.mustRouter(t, f.groupID)
	require.Equal(t, intentJevDefaultTimelyThres, view.Jev.TimelyThreshold)
	require.Equal(t, intentJevDefaultThreshold, view.Jev.Threshold, "the probe threshold keeps its own default")
	require.Equal(t, intentJevDefaultHealthThres, view.Jev.HealthThreshold, "the health threshold keeps its own default")
	require.Equal(t, "INTENT_JEV_INVALID_TIMELY_THRESHOLD", infraerrors.Reason(save(func(j *domain.IntentJevConfig) { j.TimelyThreshold = 1.5 })))
	require.Equal(t, "INTENT_JEV_INVALID_TIMELY_THRESHOLD", infraerrors.Reason(save(func(j *domain.IntentJevConfig) { j.TimelyThreshold = -0.2 })))
	require.Equal(t, "INTENT_JEV_TIMELY_RULE_UNKNOWN", infraerrors.Reason(save(func(j *domain.IntentJevConfig) { j.TimelyRule = "nope" })))
	require.Equal(t, "INTENT_JEV_TIMELY_RULE_REQUIRED", infraerrors.Reason(save(func(j *domain.IntentJevConfig) { j.TimelyRule = "" })))

	// Switched off, a stale reference is dropped instead of blocking the save.
	f.saveRouter(t, jevOn(func(j *domain.IntentJevConfig) { j.TimelyEnabled, j.TimelyRule = false, "gone" }))
	require.Empty(t, f.svc.mustRouter(t, f.groupID).Jev.TimelyRule)
}

func TestIntentJevTimely_TestClassifyShowsTheTimelyAnswer(t *testing.T) {
	f, judge, _ := newJevFixture(t, 0.1, timelyOn("chat"))
	judge.timely = f64(0.94)
	res, err := f.svc.TestClassify(context.Background(), f.groupID, "日本的首相是谁，不要使用联网搜索")
	require.NoError(t, err)
	require.True(t, res.Timely)
	require.Equal(t, 0.94, *res.TimelyProbability)
	require.Equal(t, "chat", res.Intent)
	require.Equal(t, []int64{f.accountID(t, "pool-a")}, res.AccountIDs)
}

// When the classifier decides after Jev, the test result keeps Jev's answers.
func TestIntentJevTimely_TestClassifyKeepsJevAnswersForTheClassifier(t *testing.T) {
	f := newIntentRouteFixture(t)
	judge := &scriptedProbeJudge{p: 0.1, timely: f64(0.2)}
	f.svc.judge, f.svc.hits, f.svc.jevStats = judge, &recordingHitSink{}, newMemoryIntentJevStats()
	f.saveRouter(t, func(in *IntentRouterInput) {
		in.JevAPIKey = "jev-secret"
		in.Jev = domain.IntentJevConfig{Enabled: true, Rule: "coding", TimelyEnabled: true, TimelyRule: "chat"}
	})
	res, err := f.svc.TestClassify(context.Background(), f.groupID, "帮我重构这个函数")
	require.NoError(t, err)
	require.Equal(t, "classifier", res.MatchedBy)
	require.Equal(t, 0.1, *res.ProbeProbability)
	require.Equal(t, 0.2, *res.TimelyProbability)
	require.False(t, res.Timely)
}

// TestHTTPIntentProbeJudge_LiveTimely calls the real Jev API through the new
// code path; it runs only with INTENT_JEV_LIVE_KEY set.
func TestHTTPIntentProbeJudge_LiveTimely(t *testing.T) {
	key := os.Getenv("INTENT_JEV_LIVE_KEY")
	if key == "" {
		t.Skip("INTENT_JEV_LIVE_KEY not set")
	}
	j := newHTTPIntentProbeJudge()
	for _, c := range []struct {
		text   string
		timely bool
	}{
		{"日本的首相是谁，不要使用联网搜索", true},
		{"现在比特币价格是多少", true},
		{"Who is the current UK prime minister? Don't search the web.", true},
		{"帮我写一个 Python 快速排序", false},
		{"日本的首都是哪里", false},
		{"运行测试并修复失败的用例", false},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		answer, err := j.ProbeProbability(ctx, key, intentJevDefaultModel, c.text, intentJevQuestions{Health: true, Timely: true})
		cancel()
		require.NoError(t, err)
		require.NotNil(t, answer.Timely, c.text)
		t.Logf("timely=%.2f p=%.2f health=%.2f tokens=%d %s", *answer.Timely, answer.P, *answer.Health, answer.Tokens, c.text)
		require.Equal(t, c.timely, *answer.Timely >= intentJevDefaultTimelyThres, c.text)
	}
}
