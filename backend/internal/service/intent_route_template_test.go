//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// ---- fakes ----

type memoryIntentTemplateStore struct {
	mu      sync.Mutex
	labels  map[string]string
	loadErr error
	loads   int
	upserts [][]intentTemplateAgg
	pruned  int
}

func (m *memoryIntentTemplateStore) upsert(_ context.Context, aggs []intentTemplateAgg) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upserts = append(m.upserts, aggs)
	return nil
}

func (m *memoryIntentTemplateStore) decided(context.Context) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loads++
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	out := make(map[string]string, len(m.labels))
	for k, v := range m.labels {
		out[k] = v
	}
	return out, nil
}

func (m *memoryIntentTemplateStore) list(context.Context, IntentRouteTemplateQuery) ([]IntentRouteTemplate, int64, error) {
	return nil, 0, nil
}

func (m *memoryIntentTemplateStore) decide(_ context.Context, key, status string, revision, _ int64, _ time.Time) (*IntentRouteTemplate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if revision != 0 {
		return nil, errIntentTemplateConflict
	}
	if m.labels == nil {
		m.labels = map[string]string{}
	}
	m.labels[key] = status
	return &IntentRouteTemplate{Key: key, Status: status, Revision: 1}, nil
}

func (m *memoryIntentTemplateStore) revoke(_ context.Context, key string, _ int64) (*IntentRouteTemplate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.labels, key)
	return &IntentRouteTemplate{Key: key, Status: IntentTemplatePending}, nil
}

func (m *memoryIntentTemplateStore) prune(context.Context, time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruned++
	return nil
}

type recordingTemplateSink struct {
	obs  []intentTemplateObservation
	full bool
}

func (r *recordingTemplateSink) add(o intentTemplateObservation) bool {
	if r.full {
		return false
	}
	r.obs = append(r.obs, o)
	return true
}

func keyOf(text string) string {
	key, _ := intentTemplateOf(truncateRunes(text, intentJevMaxTextRunes))
	return key
}

// newTemplateFixture: Jev on (threshold 0.8, health checks at 0.7 to ordinary
// scheduling), judged templates from a memory store, doubtful verdicts recorded.
func newTemplateFixture(t *testing.T, p float64, labels map[string]string, mutate func(*domain.IntentJevConfig)) (*intentRouteFixture, *scriptedProbeJudge, *recordingHitSink, *recordingTemplateSink, *memoryIntentTemplateStore) {
	t.Helper()
	f, judge, hits := newJevFixture(t, p, func(j *domain.IntentJevConfig) {
		j.Threshold, j.HealthEnabled, j.HealthThreshold = 0.8, true, 0.7
		if mutate != nil {
			mutate(j)
		}
	})
	store := &memoryIntentTemplateStore{labels: labels}
	sink := &recordingTemplateSink{}
	f.svc.templateStore, f.svc.templateSink, f.svc.templateFlips = store, sink, newIntentTemplateFlips(100)
	require.NoError(t, f.svc.reloadConfigs(context.Background()))
	return f, judge, hits, sink, store
}

func fptr(v float64) *float64 { return &v }

const fpProbe = "Reply with exactly this JSON and nothing else, no markdown code fence, no explanation:\n{\"fp\":\"7Q2K\",\"calc\":391}"

// ---- normalization ----

func TestIntentTemplate_Normalization(t *testing.T) {
	_, tpl := intentTemplateOf(fpProbe)
	require.Equal(t, `reply with exactly this json and nothing else, no markdown code fence, no explanation: {"fp":"<rand>","calc":<num>}`, tpl)

	a, _ := intentTemplateOf("Please reply with this short API health-check label and no additional words: maxx-probe-31de95b4f3a79d7f")
	b, _ := intentTemplateOf("please reply with this  short API health-check label and no additional words:\nmaxx-probe-0574c04052c61f7c")
	require.Equal(t, a, b, "nonce, case and whitespace do not split a template")

	c1, _ := intentTemplateOf("Calculate: 87 + 77 = ? Reply ONLY: RP_ANSWER=164")
	c2, _ := intentTemplateOf("Calculate: 3 + 5 = ? Reply ONLY: RP_ANSWER=8")
	require.Equal(t, c1, c2, "numbers are placeholders")

	d1, _ := intentTemplateOf("只回答 123，不要输出其他任何字符。")
	d2, _ := intentTemplateOf("只回答 456，不要输出其他任何字符！")
	require.NotEqual(t, d1, d2, "other characters stay as they are")

	_, short := intentTemplateOf(`say "ab12" now and "abc1"`)
	require.Equal(t, `say "<rand>" now and "abc<num>"`, short, "a short quoted code needs two letters and two digits")

	_, word := intentTemplateOf("deadbeef and abcdefgh")
	require.Equal(t, "<rand> and abcdefgh", word, "long hex is random, a long plain word is not")

	k, _ := intentTemplateOf("x")
	require.Len(t, k, 64)
	require.True(t, validIntentTemplateKey(k))
}

func TestIntentTemplate_KeyFollowsWhatJevSees(t *testing.T) {
	head := make([]rune, intentJevMaxTextRunes)
	for i := range head {
		head[i] = '字'
	}
	require.Equal(t, keyOf(string(head)+" tail one"), keyOf(string(head)+" another tail"), "only the first 4000 runes count, as for Jev")
}

// ---- flips ----

func TestIntentTemplateFlips(t *testing.T) {
	f := newIntentTemplateFlips(2)
	now := time.Unix(1_800_000_000, 0)
	require.False(t, f.see("k", intentJevConclHealth, now), "one conclusion is no flip")
	require.False(t, f.see("k", intentJevConclHealth, now))
	require.True(t, f.see("k", intentJevConclProbe, now), "a second conclusion is a flip")
	f.sent("k", true, now)
	require.False(t, f.see("k", intentJevConclHealth, now.Add(time.Minute)), "reported recently")
	require.True(t, f.see("k", intentJevConclLow, now.Add(2*time.Minute)), "a third conclusion is new")
	f.sent("k", false, now.Add(2*time.Minute))
	require.True(t, f.see("k", intentJevConclLow, now.Add(3*time.Minute)), "a lost observation is retried at once")
	f.sent("k", true, now.Add(3*time.Minute))
	require.False(t, f.see("k", intentJevConclProbe, now.Add(12*time.Minute)))
	require.True(t, f.see("k", intentJevConclProbe, now.Add(13*time.Minute)), "a flipping template keeps being reported every 10 minutes")

	f.see("a", intentJevConclHealth, now)
	f.see("b", intentJevConclHealth, now)
	require.False(t, f.see("k", intentJevConclHealth, now), "k was evicted: least recently used")
}

func TestIntentTemplate_MergeFoldsOneKey(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	aggs := mergeIntentTemplateObservations([]intentTemplateObservation{
		{Key: "k", Template: "t", Sample: "s1", Time: t0.Add(time.Second), GroupID: 4, UserID: 7, ReasonA: true, Final: intentTemplateFinalHealth, Probe: 0.92, Health: fptr(0.72)},
		{Key: "k", Template: "t", Sample: "s2", Time: t0, GroupID: 3, UserID: 8, ReasonB: true, Final: intentTemplateFinalRoute, Probe: 0.96, Health: fptr(0.65)},
		{Key: "j", Template: "u", Time: t0, Final: intentTemplateFinalNormal, Probe: 0.7, ReasonC: true},
	})
	require.Len(t, aggs, 2)
	k := aggs[0]
	require.Equal(t, int64(2), k.Seen)
	require.Equal(t, [3]int64{1, 1, 0}, [3]int64{k.A, k.B, k.C})
	require.Equal(t, [3]int64{1, 1, 0}, [3]int64{k.Route, k.Health, k.Normal})
	require.Equal(t, t0, k.First)
	require.Equal(t, t0.Add(time.Second), k.Last)
	require.InDelta(t, 0.92, k.ProbeMin, 1e-9)
	require.InDelta(t, 0.96, k.ProbeMax, 1e-9)
	require.InDelta(t, 0.65, *k.HealthMin, 1e-9)
	require.InDelta(t, 0.72, *k.HealthMax, 1e-9)
	require.Equal(t, []int64{8, 7}, k.UserIDs, "newest first")
	require.Equal(t, []int64{3, 4}, k.GroupIDs)
	require.Nil(t, aggs[1].HealthMin)
}

func TestIntentTemplate_RecentIDsBounded(t *testing.T) {
	var ids []int64
	for i := int64(1); i <= 15; i++ {
		ids = pushRecentID(ids, i)
	}
	ids = pushRecentID(ids, 10)
	require.Len(t, ids, intentTemplateMaxIDs)
	require.Equal(t, int64(10), ids[0])
	require.Equal(t, int64(15), ids[1])
}

// ---- judged templates on the request path ----

func TestIntentTemplate_ProbeJudgementRoutesWithoutJev(t *testing.T) {
	f, judge, hits, sink, _ := newTemplateFixture(t, 0.1, map[string]string{keyOf(fpProbe): IntentTemplateProbe}, nil)
	in := f.request(userTurns(fpProbe))
	in.ClientRequestID = "r1"
	d := f.svc.Decide(context.Background(), in)
	require.Equal(t, "coding", d.Intent)
	require.Zero(t, judge.calls, "a judged template is not sent to Jev")
	require.Len(t, hits.hits, 1)
	require.Equal(t, "manual:probe", hits.hits[0].Keyword)
	require.Equal(t, "coding", hits.hits[0].Rule)
	require.Empty(t, sink.obs, "a judged template is not queued again")
	require.Contains(t, f.store.kinds(), "manual")
}

func TestIntentTemplate_ProbeJudgementCoversNonceVariants(t *testing.T) {
	probe := "Please reply with this short API health-check label and no additional words: maxx-probe-31de95b4f3a79d7f"
	f, judge, _, _, _ := newTemplateFixture(t, 0.1, map[string]string{keyOf(probe): IntentTemplateProbe}, nil)
	d := f.svc.Decide(context.Background(), f.request(userTurns("Please reply with this short API health-check label and no additional words: maxx-probe-0574c04052c61f7c")))
	require.Equal(t, "coding", d.Intent)
	require.Zero(t, judge.calls)
}

func TestIntentTemplate_HealthAndNormalJudgementsLeaveToOrdinaryScheduling(t *testing.T) {
	for _, label := range []string{IntentTemplateHealth, IntentTemplateNormal} {
		f, judge, hits, sink, _ := newTemplateFixture(t, 0.99, map[string]string{keyOf("hi"): label}, nil)
		requireNoPreference(t, f.svc.Decide(context.Background(), f.request(userTurns("hi"))))
		require.Zero(t, judge.calls, label)
		require.Len(t, hits.hits, 1)
		require.Equal(t, "manual:"+label, hits.hits[0].Keyword)
		require.Empty(t, hits.hits[0].Rule)
		require.Empty(t, sink.obs)
	}
}

func TestIntentTemplate_JudgementsOnlyForShortConversations(t *testing.T) {
	f, judge, hits, _, _ := newTemplateFixture(t, 0.99, map[string]string{keyOf("ok"): IntentTemplateProbe}, nil)
	requireNoPreference(t, f.svc.Decide(context.Background(), f.request(userTurns("fix the build", "and the tests", "ok"))))
	require.Zero(t, judge.calls, "beyond max_turns neither the judgement nor Jev applies")
	require.Empty(t, hits.hits)
}

func TestIntentTemplate_KeywordsComeFirst(t *testing.T) {
	f, judge, hits, _, _ := newTemplateFixture(t, 0.99, map[string]string{keyOf("讲个笑话"): IntentTemplateProbe}, nil)
	require.Equal(t, "chat", f.svc.Decide(context.Background(), f.request(userTurns("讲个笑话"))).Intent)
	require.Zero(t, judge.calls)
	require.Equal(t, "笑话", hits.hits[0].Keyword)
}

func TestIntentTemplate_ExcludedModelSkipsJudgements(t *testing.T) {
	body := `{"model":"codex-auto-review","messages":[{"role":"user","content":"hi"}]}`
	f, judge, hits, _, _ := newTemplateFixture(t, 0.99, map[string]string{keyOf("hi"): IntentTemplateProbe}, nil)
	f.saveRouter(t, func(in *IntentRouterInput) {
		jevOn(func(j *domain.IntentJevConfig) { j.Threshold = 0.8 })(in)
		in.ExcludedModels = []string{"codex-auto-review"}
	})
	requireNoPreference(t, f.svc.Decide(context.Background(), f.request(body)))
	require.Zero(t, judge.calls)
	require.Empty(t, hits.hits)
}

func TestIntentTemplate_ProbeJudgementWorksWithJevOff(t *testing.T) {
	f, judge, _, _, _ := newTemplateFixture(t, 0.99, map[string]string{keyOf(fpProbe): IntentTemplateProbe}, func(j *domain.IntentJevConfig) { j.Enabled = false })
	require.Equal(t, "coding", f.svc.Decide(context.Background(), f.request(userTurns(fpProbe))).Intent)
	require.Zero(t, judge.calls)
}

func TestIntentTemplate_ProbeJudgementWithoutTargetFallsThrough(t *testing.T) {
	f, judge, _, sink, _ := newTemplateFixture(t, 0.99, map[string]string{keyOf(fpProbe): IntentTemplateProbe}, nil)
	f.saveRouter(t, func(in *IntentRouterInput) {
		jevOn(func(j *domain.IntentJevConfig) { j.Threshold, j.HealthEnabled = 0.8, true })(in)
		in.Rules[0].Enabled = false // the Jev target
	})
	requireNoPreference(t, f.svc.Decide(context.Background(), f.request(userTurns(fpProbe))))
	require.Contains(t, f.store.kinds(), "manual_target_missing")
	require.Empty(t, sink.obs, "not queued again")
	_ = judge
}

func TestIntentTemplate_SnapshotFollowsDecisions(t *testing.T) {
	f, judge, _, _, store := newTemplateFixture(t, 0.1, nil, nil)
	ctx := context.Background()
	_, err := f.svc.DecideTemplate(ctx, keyOf(fpProbe), IntentTemplateProbe, 0, 1)
	require.NoError(t, err)
	require.Equal(t, "coding", f.svc.Decide(ctx, f.request(userTurns(fpProbe))).Intent, "applies at once on this instance")
	_, err = f.svc.DecideTemplate(ctx, keyOf(fpProbe), IntentTemplateProbe, 5, 1)
	require.ErrorIs(t, err, errIntentTemplateConflict)
	_, err = f.svc.DecideTemplate(ctx, "NOT-A-KEY", IntentTemplateProbe, 0, 1)
	require.Error(t, err)
	_, err = f.svc.DecideTemplate(ctx, keyOf(fpProbe), "pending", 0, 1)
	require.Error(t, err)
	_, err = f.svc.RevokeTemplate(ctx, keyOf(fpProbe), 1)
	require.NoError(t, err)
	requireNoPreference(t, f.svc.Decide(ctx, f.request(userTurns(fpProbe))))
	require.Equal(t, 1, judge.calls, "revoked: Jev decides again")

	store.loadErr = errors.New("db down")
	store.labels[keyOf("x")] = IntentTemplateProbe
	require.NoError(t, f.svc.reloadConfigs(ctx))
	require.NotContains(t, f.svc.templateLabels(), keyOf("x"), "a failed load keeps the previous snapshot")
}

// ---- doubtful verdicts ----

func TestIntentTemplate_ReasonAHealthCheckWithHighProbeScore(t *testing.T) {
	f, judge, _, sink, _ := newTemplateFixture(t, 0.92, nil, nil)
	judge.health = fptr(0.72)
	requireNoPreference(t, f.svc.Decide(context.Background(), f.request(userTurns(fpProbe))))
	require.Len(t, sink.obs, 1)
	o := sink.obs[0]
	require.True(t, o.ReasonA)
	require.False(t, o.ReasonB)
	require.False(t, o.ReasonC)
	require.Equal(t, intentTemplateFinalHealth, o.Final)
	require.Equal(t, keyOf(fpProbe), o.Key)
	require.Equal(t, f.groupID, o.GroupID)

	judge.health = fptr(0.85) // margin 0.07 < 0.1
	sink.obs = nil
	f.svc.Decide(context.Background(), f.request(userTurns("Reply with exactly OK.")))
	require.Empty(t, sink.obs, "a health check scoring high on both is not doubtful")
}

func TestIntentTemplate_ReasonAMarginExactlyAtBoundary(t *testing.T) {
	f, judge, _, sink, _ := newTemplateFixture(t, 0.9, nil, nil)
	judge.health = fptr(0.8) // 0.9-0.8 is 0.0999... in float64
	f.svc.Decide(context.Background(), f.request(userTurns(fpProbe)))
	require.Len(t, sink.obs, 1)
	require.True(t, sink.obs[0].ReasonA, "a margin of exactly 0.1 counts")
}

func TestIntentTemplate_ReasonCNearMiss(t *testing.T) {
	f, judge, _, sink, _ := newTemplateFixture(t, 0.7, nil, nil)
	judge.health = fptr(0.2)
	requireNoPreference(t, f.svc.Decide(context.Background(), f.request(userTurns("a"))))
	require.Len(t, sink.obs, 1)
	require.True(t, sink.obs[0].ReasonC)
	require.Equal(t, intentTemplateFinalNormal, sink.obs[0].Final)

	judge.p, sink.obs = 0.5, nil
	f.svc.Decide(context.Background(), f.request(userTurns("b")))
	require.Empty(t, sink.obs, "below 0.6 is not doubtful")
}

func TestIntentTemplate_ReasonCNotWhenRoutedByTimely(t *testing.T) {
	f, judge, _, sink, _ := newTemplateFixture(t, 0.7, nil, func(j *domain.IntentJevConfig) {
		j.TimelyEnabled, j.TimelyThreshold, j.TimelyRule = true, 0.85, "chat"
	})
	judge.health, judge.timely = fptr(0.2), fptr(0.95)
	require.Equal(t, "chat", f.svc.Decide(context.Background(), f.request(userTurns("who won yesterday"))).Intent)
	require.Empty(t, sink.obs, "routed by another rule: not a near miss")
}

func TestIntentTemplate_ReasonBFlipBetweenHealthAndProbe(t *testing.T) {
	f, judge, _, sink, _ := newTemplateFixture(t, 0.85, nil, healthOn("chat"))
	ctx := context.Background()
	judge.health = fptr(0.75)
	require.Equal(t, "chat", f.svc.Decide(ctx, f.request(userTurns(fpProbe))).Intent, "health rule")
	require.Empty(t, sink.obs)
	judge.health = fptr(0.69)
	require.Equal(t, "coding", f.svc.Decide(ctx, f.request(userTurns(fpProbe))).Intent, "probe rule")
	require.Len(t, sink.obs, 1, "health-rule vs probe-rule routing is a flip")
	require.True(t, sink.obs[0].ReasonB)
	require.Equal(t, intentTemplateFinalRoute, sink.obs[0].Final)
}

func TestIntentTemplate_FlipRetriedWhenQueueFull(t *testing.T) {
	f, judge, _, sink, _ := newTemplateFixture(t, 0.85, nil, nil)
	ctx := context.Background()
	judge.health = fptr(0.75)
	f.svc.Decide(ctx, f.request(userTurns(fpProbe)))
	judge.health = fptr(0.1)
	sink.full = true
	f.svc.Decide(ctx, f.request(userTurns(fpProbe)))
	sink.full = false
	f.svc.Decide(ctx, f.request(userTurns(fpProbe)))
	require.Len(t, sink.obs, 1)
	require.True(t, sink.obs[0].ReasonB, "the lost flip is reported on the next verdict")
}

func TestIntentTemplate_NothingFiledWhenJevFails(t *testing.T) {
	f, judge, _, sink, _ := newTemplateFixture(t, 0.7, nil, nil)
	judge.err = errors.New("timeout")
	f.svc.Decide(context.Background(), f.request(userTurns("a")))
	require.Empty(t, sink.obs)
}

// ---- admin test ----

func TestIntentTemplate_TestClassifyShowsJudgement(t *testing.T) {
	f, judge, _, _, store := newTemplateFixture(t, 0.1, nil, nil)
	ctx := context.Background()
	f.svc.templates.Store(nil) // as after a restart
	store.labels = map[string]string{keyOf(fpProbe): IntentTemplateProbe, keyOf("hi"): IntentTemplateHealth}

	got, err := f.svc.TestClassify(ctx, f.groupID, fpProbe)
	require.NoError(t, err)
	require.Equal(t, "manual", got.MatchedBy)
	require.Equal(t, IntentTemplateProbe, got.ManualLabel)
	require.Equal(t, "coding", got.Intent)
	require.Equal(t, keyOf(fpProbe), got.TemplateKey)

	got, err = f.svc.TestClassify(ctx, f.groupID, "hi")
	require.NoError(t, err)
	require.Equal(t, "manual", got.MatchedBy)
	require.Equal(t, IntentTemplateHealth, got.ManualLabel)
	require.Empty(t, got.Intent)
	require.Zero(t, judge.calls)

	// Another instance judges a template; a stale snapshot is refreshed.
	store.labels[keyOf("ping")] = IntentTemplateNormal
	f.now = f.now.Add(intentRouteConfigTTL + time.Second)
	got, err = f.svc.TestClassify(ctx, f.groupID, "ping")
	require.NoError(t, err)
	require.Equal(t, IntentTemplateNormal, got.ManualLabel)

	f.svc.templates.Store(nil)
	store.loadErr = errors.New("db down")
	_, err = f.svc.TestClassify(ctx, f.groupID, "ping")
	require.Error(t, err, "never loaded and cannot load: the admin is told")
}

func TestIntentTemplate_TestClassifyProbeWithoutTarget(t *testing.T) {
	f, judge, _, _, _ := newTemplateFixture(t, 0.95, map[string]string{keyOf(fpProbe): IntentTemplateProbe}, nil)
	f.saveRouter(t, func(in *IntentRouterInput) {
		jevOn(func(j *domain.IntentJevConfig) { j.Threshold = 0.8 })(in)
		in.Rules[0].Enabled = false
	})
	got, err := f.svc.TestClassify(context.Background(), f.groupID, fpProbe)
	require.NoError(t, err)
	require.True(t, got.ManualTargetMissing)
	require.Equal(t, IntentTemplateProbe, got.ManualLabel)
	require.Equal(t, "jev", got.MatchedBy)
	require.Equal(t, 1, judge.calls)
}

// ---- write queue ----

func TestIntentTemplateLog_DrainsOnStopAndMerges(t *testing.T) {
	store := &memoryIntentTemplateStore{}
	l := newIntentTemplateLog(store)
	l.Start()
	now := time.Unix(1_800_000_000, 0)
	require.True(t, l.add(intentTemplateObservation{Key: "k", Time: now, ReasonA: true, Final: intentTemplateFinalHealth}))
	require.True(t, l.add(intentTemplateObservation{Key: "k", Time: now, ReasonB: true, Final: intentTemplateFinalRoute}))
	require.NoError(t, l.Stop(context.Background()))
	require.False(t, l.add(intentTemplateObservation{Key: "late"}), "closed after Stop")
	var total int
	for _, batch := range store.upserts {
		keys := map[string]bool{}
		for _, a := range batch {
			require.False(t, keys[a.Key], "one row per key per batch")
			keys[a.Key] = true
			total += int(a.Seen)
		}
	}
	require.Equal(t, 2, total)
	require.GreaterOrEqual(t, store.pruned, 1)
}
