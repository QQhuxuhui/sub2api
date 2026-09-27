//go:build unit

package service

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func withKeywords(coding, chat []string) func(*IntentRouterInput) {
	return func(in *IntentRouterInput) {
		in.Rules[0].Keywords = coding
		in.Rules[1].Keywords = chat
	}
}

func keywordOnly(coding, chat []string) func(*IntentRouterInput) {
	return func(in *IntentRouterInput) {
		withKeywords(coding, chat)(in)
		in.ClassifierModel, in.ClassifierAPIKey = "", ""
	}
}

func userTurns(texts ...string) string {
	var b strings.Builder
	_, _ = b.WriteString(`{"messages":[`)
	for i, text := range texts {
		if i > 0 {
			role := "assistant"
			_, _ = b.WriteString(`{"role":"` + role + `","content":"ok"},`)
		}
		_, _ = b.WriteString(`{"role":"user","content":` + intentJSONString(text) + `}`)
		if i < len(texts)-1 {
			_ = b.WriteByte(',')
		}
	}
	_, _ = b.WriteString(`]}`)
	return b.String()
}

func intentJSONString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

func TestIntentRouter_KeywordHitSkipsTheClassifier(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	f.saveRouter(t, withKeywords([]string{"Traceback", "编译"}, []string{"讲个笑话"}))
	coding1, coding2 := f.accountID(t, "coding-1"), f.accountID(t, "coding-2")

	d := f.svc.Decide(ctx, f.request(userTurns("帮我看下这个 traceback 是什么意思")))
	require.NotNil(t, d)
	require.Equal(t, "coding", d.Intent, "matching ignores case")
	require.Equal(t, []int64{coding1, coding2}, d.AccountIDs)
	require.Zero(t, f.classifier.calls)
	require.Equal(t, []string{"keyword"}, f.store.kinds())
	require.Equal(t, "Traceback", f.store.events[0].Detail, "the log names the keyword that matched")

	// No keyword: the classifier decides, as before.
	f.classifier.answers = []string{"chat"}
	other := f.svc.Decide(ctx, IntentRouteInput{GroupID: f.groupID, APIKeyID: 12, Body: []byte(userTurns("今天天气怎么样"))})
	require.Equal(t, "chat", other.Intent)
	require.Equal(t, 1, f.classifier.calls)
}

func TestIntentRouter_KeywordRulesAreTriedInConfiguredOrder(t *testing.T) {
	f := newIntentRouteFixture(t)
	f.saveRouter(t, withKeywords([]string{"代码"}, []string{"笑话"}))
	d := f.svc.Decide(context.Background(), f.request(userTurns("讲个关于写代码的笑话")))
	require.Equal(t, "coding", d.Intent, "both rules match; the first one wins")
}

func TestIntentRouter_KeywordsLookOnlyAtTheLatestUserMessage(t *testing.T) {
	f := newIntentRouteFixture(t)
	f.saveRouter(t, withKeywords([]string{"traceback"}, nil))
	f.classifier.answers = []string{"chat"}
	d := f.svc.Decide(context.Background(), f.request(userTurns("here is a traceback", "never mind, tell me a joke")))
	require.Equal(t, "chat", d.Intent, "a keyword in an earlier turn does not count")
	require.Equal(t, 1, f.classifier.calls)
}

func TestIntentRouter_KeywordsSeeTheWholeMessage(t *testing.T) {
	f := newIntentRouteFixture(t)
	f.saveRouter(t, func(in *IntentRouterInput) {
		withKeywords([]string{"traceback"}, nil)(in)
		in.MaxInputChars = 100
	})
	long := strings.Repeat("x", 500) + " traceback"
	d := f.svc.Decide(context.Background(), f.request(userTurns(long)))
	require.Equal(t, "coding", d.Intent, "the classifier's input limit does not cut what keywords see")
	require.Zero(t, f.classifier.calls)
}

func TestIntentRouter_KeywordHitOverridesTheConversation(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	f.saveRouter(t, withKeywords(nil, []string{"笑话"}))
	pool, coding2 := f.accountID(t, "pool-a"), f.accountID(t, "coding-2")
	f.classifier.answers = []string{"coding"}

	first := f.svc.Decide(ctx, f.request(userTurns("fix my go build")))
	require.Equal(t, "coding", first.Intent)
	first.markSelected(coding2)

	// The latest message carries a keyword of another rule: re-route at once.
	second := f.svc.Decide(ctx, f.request(userTurns("fix my go build", "讲个笑话")))
	require.Equal(t, "chat", second.Intent)
	require.Equal(t, []int64{pool}, second.AccountIDs)
	require.Zero(t, second.PinnedAccountID, "the old pin belongs to the old intent")
	second.markSelected(pool)

	// No keyword this turn: the conversation keeps its new route, unclassified.
	third := f.svc.Decide(ctx, f.request(userTurns("fix my go build", "讲个笑话", "再来一个")))
	require.Equal(t, "chat", third.Intent)
	require.Equal(t, pool, third.PinnedAccountID)
	require.Equal(t, 1, f.classifier.calls)
	require.Equal(t, []string{"classified", "routed", "keyword", "routed", "cached"}, f.store.kinds())
}

func TestIntentRouter_KeywordOverrideStandsEvenWithoutASelection(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	f.saveRouter(t, withKeywords(nil, []string{"笑话"}))
	f.classifier.answers = []string{"coding"}
	f.svc.Decide(ctx, f.request(userTurns("fix my go build"))).markSelected(f.accountID(t, "coding-1"))

	// Every chat account is busy, so this turn falls back to the group ...
	f.svc.Decide(ctx, f.request(userTurns("fix my go build", "讲个笑话")))
	// ... but the conversation is still re-routed for the next turn.
	next := f.svc.Decide(ctx, f.request(userTurns("fix my go build", "讲个笑话", "继续")))
	require.Equal(t, "chat", next.Intent)
	require.Zero(t, next.PinnedAccountID)
}

func TestIntentRouter_KeywordOfTheSameIntentKeepsThePin(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	f.saveRouter(t, withKeywords([]string{"traceback"}, nil))
	coding2 := f.accountID(t, "coding-2")

	f.svc.Decide(ctx, f.request(userTurns("traceback one"))).markSelected(coding2)
	again := f.svc.Decide(ctx, f.request(userTurns("traceback one", "another traceback")))
	require.Equal(t, "coding", again.Intent)
	require.Equal(t, coding2, again.PinnedAccountID, "same intent: the conversation stays on its account")
	require.Equal(t, 1, f.store.touches)
}

func TestIntentRouter_KeywordOverridesAnUnmatchedConversation(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	f.saveRouter(t, withKeywords([]string{"traceback"}, nil))
	f.classifier.answers = []string{"NONE"}
	requireNoPreference(t, f.svc.Decide(ctx, f.request(userTurns("hello"))))

	d := f.svc.Decide(ctx, f.request(userTurns("hello", "what does this traceback mean")))
	require.Equal(t, "coding", d.Intent)
	require.Equal(t, 1, f.classifier.calls)
}

func TestIntentRouter_LateWriteOfAnEarlierRequestCannotUndoAKeywordOverride(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	f.saveRouter(t, withKeywords(nil, []string{"笑话"}))
	f.classifier.answers = []string{"coding"}

	earlier := f.svc.Decide(ctx, f.request(userTurns("fix my go build")))
	later := f.svc.Decide(ctx, f.request(userTurns("fix my go build", "讲个笑话")))
	require.Equal(t, "chat", later.Intent)

	earlier.markSelected(f.accountID(t, "coding-1")) // the earlier request's pin lands last
	next := f.svc.Decide(ctx, f.request(userTurns("fix my go build", "讲个笑话", "继续")))
	require.Equal(t, "chat", next.Intent)
}

type waitingIntentClassifier struct {
	entered chan struct{}
	release chan struct{}
}

func (c *waitingIntentClassifier) Classify(ctx context.Context, _ *intentRouterConfig, _ string) (string, error) {
	close(c.entered)
	select {
	case <-c.release:
		return "coding", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestIntentRouter_SlowEarlierClassificationCannotUndoLaterKeyword(t *testing.T) {
	f := newIntentRouteFixture(t)
	f.saveRouter(t, withKeywords(nil, []string{"笑话"}))
	classifier := &waitingIntentClassifier{entered: make(chan struct{}), release: make(chan struct{})}
	f.svc.classifier = classifier
	ctx := context.Background()
	earlier := make(chan *IntentRouteDecision, 1)
	go func() { earlier <- f.svc.Decide(ctx, f.request(userTurns("fix my go build"))) }()
	select {
	case <-classifier.entered:
	case <-time.After(time.Second):
		t.Fatal("earlier request did not reach the classifier")
	}
	later := f.svc.Decide(ctx, f.request(userTurns("fix my go build", "讲个笑话")))
	require.Equal(t, "chat", later.Intent)
	later.markSelected(f.accountID(t, "pool-a"))
	close(classifier.release)
	old := <-earlier
	require.Equal(t, "coding", old.Intent)
	old.markSelected(f.accountID(t, "coding-1"))
	next := f.svc.Decide(ctx, f.request(userTurns("fix my go build", "讲个笑话", "继续")))
	require.Equal(t, "chat", next.Intent, "an older classification must not replace a newer keyword decision")
}

type delayedReadIntentStore struct {
	*memoryIntentRouteStore
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *delayedReadIntentStore) GetSession(ctx context.Context, groupID int64, key string) (*intentRouteSession, error) {
	session, err := s.memoryIntentRouteStore.GetSession(ctx, groupID, key)
	delay := false
	s.once.Do(func() { delay = true; close(s.entered) })
	if delay {
		<-s.release
	}
	return session, err
}

func TestIntentRouter_SlowEarlierSessionReadCannotUndoLaterKeyword(t *testing.T) {
	f := newIntentRouteFixture(t)
	f.saveRouter(t, withKeywords(nil, []string{"笑话"}))
	f.classifier.answers = []string{"coding"}
	ctx := context.Background()
	f.svc.Decide(ctx, f.request(userTurns("fix my go build"))).markSelected(f.accountID(t, "coding-1"))
	store := &delayedReadIntentStore{memoryIntentRouteStore: f.store, entered: make(chan struct{}), release: make(chan struct{})}
	f.svc.store = store
	earlier := make(chan *IntentRouteDecision, 1)
	go func() { earlier <- f.svc.Decide(ctx, f.request(userTurns("fix my go build", "continue"))) }()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("earlier request did not reach the session read")
	}
	later := f.svc.Decide(ctx, f.request(userTurns("fix my go build", "讲个笑话")))
	require.Equal(t, "chat", later.Intent)
	later.markSelected(f.accountID(t, "pool-a"))
	close(store.release)
	old := <-earlier
	require.Equal(t, "coding", old.Intent)
	old.markSelected(f.accountID(t, "coding-2"))
	next := f.svc.Decide(ctx, f.request(userTurns("fix my go build", "讲个笑话", "继续")))
	require.Equal(t, "chat", next.Intent, "an old cache read must not replace a newer keyword decision")
}

type delayedDeleteIntentStore struct {
	*memoryIntentRouteStore
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *delayedDeleteIntentStore) DeleteSession(ctx context.Context, groupID int64, key string, observed intentRouteSession) error {
	delay := false
	s.once.Do(func() { delay = true; close(s.entered) })
	if delay {
		<-s.release
	}
	return s.memoryIntentRouteStore.DeleteSession(ctx, groupID, key, observed)
}

func TestIntentRouter_OldStaleDeleteCannotEraseLaterKeyword(t *testing.T) {
	f := newIntentRouteFixture(t)
	f.saveRouter(t, withKeywords(nil, []string{"笑话"}))
	ctx := context.Background()
	first := userTurns("hello")
	key := intentSessionKey(11, nil, intentRequestView{body: []byte(first)})
	require.NoError(t, f.store.InitSession(ctx, f.groupID, key,
		intentRouteSession{Intent: "removed", Version: 1, Sequence: 1}, time.Hour))
	store := &delayedDeleteIntentStore{memoryIntentRouteStore: f.store, entered: make(chan struct{}), release: make(chan struct{})}
	f.svc.store = store
	f.classifier.answers = []string{"coding"}
	earlier := make(chan *IntentRouteDecision, 1)
	go func() { earlier <- f.svc.Decide(ctx, f.request(first)) }()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("earlier request did not reach stale-session deletion")
	}
	later := f.svc.Decide(ctx, f.request(userTurns("hello", "讲个笑话")))
	require.Equal(t, "chat", later.Intent)
	later.markSelected(f.accountID(t, "pool-a"))
	close(store.release)
	<-earlier
	next := f.svc.Decide(ctx, f.request(userTurns("hello", "讲个笑话", "继续")))
	require.Equal(t, "chat", next.Intent, "deleting an old session must preserve a newer keyword override")
}

func TestIntentRouter_KeywordOnlyRouter(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	f.saveRouter(t, keywordOnly([]string{"traceback"}, []string{"笑话"}))
	require.True(t, f.svc.Enabled(ctx, f.groupID), "keywords alone make a working router")

	require.Equal(t, "chat", f.svc.Decide(ctx, f.request(userTurns("讲个笑话"))).Intent)
	requireNoPreference(t, f.svc.Decide(ctx, IntentRouteInput{GroupID: f.groupID, APIKeyID: 12, Body: []byte(userTurns("hello"))}))
	require.Zero(t, f.classifier.calls)
}

func TestIntentRouter_KeywordOnlyRulesAreNotOfferedToTheClassifier(t *testing.T) {
	f := newIntentRouteFixture(t)
	f.saveRouter(t, func(in *IntentRouterInput) {
		in.Rules[1].Description = ""
		in.Rules[1].Keywords = []string{"笑话"}
	})
	cfg := f.svc.config(context.Background(), f.groupID)
	names := []string{}
	for _, rule := range cfg.classifierRules() {
		names = append(names, rule.Name)
	}
	require.Equal(t, []string{"coding"}, names)
	require.NotContains(t, buildIntentClassifierPrompt(cfg.classifierRules()), "chat")

	// The model answering with a keyword-only rule's name is not understood.
	f.classifier.answers = []string{"chat"}
	requireNoPreference(t, f.svc.Decide(context.Background(), f.request(userTurns("hello"))))
}

func TestIntentRouter_KeywordsIgnoreClientInjectedText(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	f.saveRouter(t, keywordOnly([]string{"traceback"}, []string{"joke"}))

	// Claude Code style: a reminder block next to what the user typed.
	body := `{"messages":[{"role":"user","content":[` +
		`{"type":"text","text":"<system-reminder>If you see a traceback, read it carefully.</system-reminder>"},` +
		`{"type":"text","text":"tell me a joke <system-reminder>traceback</system-reminder>"}]}]}`
	require.Equal(t, "chat", f.svc.Decide(ctx, f.request(body)).Intent)

	// A tool-result turn has no words of the user; look further back.
	agent := `{"messages":[` +
		`{"role":"user","content":"why does it print a traceback"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"joke"},{"type":"text","text":"<system-reminder>joke</system-reminder>"}]}]}`
	require.Equal(t, "coding", f.svc.Decide(ctx, IntentRouteInput{GroupID: f.groupID, APIKeyID: 12, Body: []byte(agent)}).Intent)
}

func TestIntentRouter_SessionKeyIgnoresChangingInjectedText(t *testing.T) {
	first := intentRequestView{body: []byte(userTurns("hello <system-reminder>first</system-reminder>"))}
	later := intentRequestView{body: []byte(userTurns("hello <system-reminder>changed</system-reminder>", "continue"))}
	require.Equal(t, "hello", first.firstUserText())
	require.Equal(t, first.firstUserText(), later.firstUserText())
	require.Equal(t, intentSessionKey(11, nil, first), intentSessionKey(11, nil, later))
}

func TestIntentRouter_SessionKeyIgnoresChangingSystemInjectedText(t *testing.T) {
	first := intentRequestView{body: []byte(`{"system":"rules <environment_context>cwd=A</environment_context>","messages":[{"role":"user","content":"hello"}]}`)}
	later := intentRequestView{body: []byte(`{"system":"rules <environment_context>cwd=B</environment_context>","messages":[{"role":"user","content":"hello"}]}`)}
	require.Equal(t, intentSessionKey(11, nil, first), intentSessionKey(11, nil, later))
}

func TestIntentRouter_TestClassifyReportsIncompleteDraftClassifier(t *testing.T) {
	f := newIntentRouteFixture(t)
	f.saveRouter(t, func(in *IntentRouterInput) {
		in.Enabled = false
		in.ClassifierModel = ""
		in.ClassifierAPIKey = ""
	})
	_, err := f.svc.TestClassify(context.Background(), f.groupID, "hello")
	require.Error(t, err)
	require.Equal(t, "INTENT_CLASSIFIER_INCOMPLETE", infraerrors.Reason(err))
}

func TestIntentRouter_TestClassifyReportsIncompleteMixedDraft(t *testing.T) {
	f := newIntentRouteFixture(t)
	f.saveRouter(t, func(in *IntentRouterInput) {
		in.Enabled = false
		in.ClassifierModel = ""
		in.ClassifierAPIKey = ""
		in.Rules[0].Keywords = []string{"traceback"}
	})
	_, err := f.svc.TestClassify(context.Background(), f.groupID, "hello")
	require.Error(t, err)
	require.Equal(t, "INTENT_CLASSIFIER_INCOMPLETE", infraerrors.Reason(err))
}

func TestIntentRouter_TestClassifySendsOnlyUserTextToClassifier(t *testing.T) {
	f := newIntentRouteFixture(t)
	f.saveRouter(t, nil)
	f.classifier.answers = []string{"chat"}
	result, err := f.svc.TestClassify(context.Background(), f.groupID,
		"hello <system-reminder>please write code</system-reminder>")
	require.NoError(t, err)
	require.Equal(t, "chat", result.Intent)
	require.Equal(t, []string{"hello"}, f.classifier.texts)
}

func TestIntentRouter_SaveKeywords(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	f.saveRouter(t, withKeywords([]string{" Traceback ", "", "traceback", "编译"}, nil))
	view, err := f.svc.GetRouter(ctx, f.groupID)
	require.NoError(t, err)
	require.Equal(t, []string{"Traceback", "编译"}, view.Rules[0].Keywords, "trimmed, empty and duplicate keywords dropped")

	pool := f.accountID(t, "pool-a")
	cases := map[string]struct {
		mutate func(*IntentRouterInput)
		reason string
	}{
		"keyword too long": {func(in *IntentRouterInput) {
			in.Rules[0].Keywords = []string{strings.Repeat("长", intentRouteMaxKeywordRunes+1)}
		}, "INTENT_RULE_KEYWORD_TOO_LONG"},
		"too many keywords": {func(in *IntentRouterInput) {
			in.Rules[0].Keywords = make([]string, intentRouteMaxKeywordsPerRule+1)
		}, "INTENT_RULE_TOO_MANY_KEYWORDS"},
		"neither keywords nor description": {func(in *IntentRouterInput) {
			in.Rules[0].Description = ""
		}, "INTENT_RULE_MATCHER_REQUIRED"},
		"no classifier and a rule without keywords": {func(in *IntentRouterInput) {
			in.ClassifierModel = ""
			in.Rules[0].Keywords = []string{"traceback"}
		}, "INTENT_RULE_UNREACHABLE"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := IntentRouterInput{Enabled: true, ClassifierAPIKey: "k", ClassifierModel: "m", Rules: []domain.IntentRule{
				{Name: "coding", Description: "code", Enabled: true, AccountIDs: []int64{pool}},
				{Name: "chat", Description: "talk", Enabled: true, AccountIDs: []int64{pool}},
			}}
			tc.mutate(&in)
			_, err := f.svc.SaveRouter(ctx, f.groupID, in)
			require.Error(t, err)
			require.Equal(t, tc.reason, infraerrors.Reason(err), err.Error())
		})
	}

	// A keyword-only rule needs no description; a disabled rule is not checked for reachability.
	_, err = f.svc.SaveRouter(ctx, f.groupID, IntentRouterInput{Enabled: true, Rules: []domain.IntentRule{
		{Name: "coding", Keywords: []string{"traceback"}, Enabled: true, AccountIDs: []int64{pool}},
		{Name: "chat", Description: "talk", Enabled: false, AccountIDs: []int64{pool}},
	}})
	require.NoError(t, err)
}

func TestIntentRouter_TestClassifyTriesKeywordsFirst(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	f.saveRouter(t, withKeywords([]string{"traceback"}, nil))

	result, err := f.svc.TestClassify(ctx, f.groupID, "what is this Traceback")
	require.NoError(t, err)
	require.Equal(t, "keyword", result.MatchedBy)
	require.Equal(t, "traceback", result.Keyword)
	require.Equal(t, "coding", result.Intent)
	require.Zero(t, f.classifier.calls)

	f.classifier.answers = []string{"chat"}
	result, err = f.svc.TestClassify(ctx, f.groupID, "hello")
	require.NoError(t, err)
	require.Equal(t, "classifier", result.MatchedBy)
	require.Equal(t, "chat", result.Intent)

	keywordsOnly := newIntentRouteFixture(t)
	keywordsOnly.saveRouter(t, keywordOnly([]string{"traceback"}, []string{"笑话"}))
	result, err = keywordsOnly.svc.TestClassify(ctx, keywordsOnly.groupID, "hello")
	require.NoError(t, err, "a keyword-only router can be tried without a classifier")
	require.Empty(t, result.MatchedBy)
	require.Empty(t, result.Intent)
}
