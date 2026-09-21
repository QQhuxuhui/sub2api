//go:build unit

package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type memoryIntentRouteStore struct {
	mu       sync.Mutex
	sessions map[string]intentRouteSession
	ttls     map[string]time.Duration
	events   []IntentRouteEvent
	getErr   error
	touches  int
}

func newMemoryIntentRouteStore() *memoryIntentRouteStore {
	return &memoryIntentRouteStore{sessions: map[string]intentRouteSession{}, ttls: map[string]time.Duration{}}
}

func (m *memoryIntentRouteStore) GetSession(_ context.Context, groupID int64, key string) (*intentRouteSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	if s, ok := m.sessions[intentRouteSessionRedisKey(groupID, key)]; ok {
		return &s, nil
	}
	return nil, nil
}

// Same semantics as the Redis store: SETNX / EXPIRE / SET / DEL.
func (m *memoryIntentRouteStore) InitSession(_ context.Context, groupID int64, key string, s intentRouteSession, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := intentRouteSessionRedisKey(groupID, key)
	if _, exists := m.sessions[k]; !exists {
		m.sessions[k], m.ttls[k] = s, ttl
	}
	return nil
}

func (m *memoryIntentRouteStore) TouchSession(_ context.Context, groupID int64, key string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := intentRouteSessionRedisKey(groupID, key)
	if _, exists := m.sessions[k]; exists {
		m.ttls[k] = ttl
		m.touches++
	}
	return nil
}

func (m *memoryIntentRouteStore) PinSession(_ context.Context, groupID int64, key string, s intentRouteSession, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := intentRouteSessionRedisKey(groupID, key)
	m.sessions[k], m.ttls[k] = s, ttl
	return nil
}

func (m *memoryIntentRouteStore) DeleteSession(_ context.Context, groupID int64, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, intentRouteSessionRedisKey(groupID, key))
	return nil
}

func (m *memoryIntentRouteStore) ClearSessions(_ context.Context, groupID int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := fmt.Sprintf("%s%d:", intentRouteSessionKeyPrefix, groupID)
	var n int64
	for k := range m.sessions {
		if groupID == 0 || len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(m.sessions, k)
			n++
		}
	}
	return n, nil
}

func (m *memoryIntentRouteStore) AppendEvent(_ context.Context, e IntentRouteEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append([]IntentRouteEvent{e}, m.events...)
	return nil
}

func (m *memoryIntentRouteStore) ListEvents(context.Context, int) ([]IntentRouteEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]IntentRouteEvent(nil), m.events...), nil
}

func (m *memoryIntentRouteStore) kinds() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	kinds := make([]string, 0, len(m.events))
	for i := len(m.events) - 1; i >= 0; i-- {
		kinds = append(kinds, m.events[i].Kind)
	}
	return kinds
}

type scriptedIntentClassifier struct {
	mu      sync.Mutex
	answers []string
	err     error
	panics  bool
	calls   int
	texts   []string
}

func (c *scriptedIntentClassifier) Classify(_ context.Context, _ *intentRouterConfig, text string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.texts = append(c.texts, text)
	if c.panics {
		panic("classifier exploded")
	}
	if c.err != nil {
		return "", c.err
	}
	if len(c.answers) == 0 {
		return intentNoneLabel, nil
	}
	answer := c.answers[0]
	if len(c.answers) > 1 {
		c.answers = c.answers[1:]
	}
	return answer, nil
}

type intentRouteFixture struct {
	svc        *IntentRouterService
	client     *dbent.Client
	store      *memoryIntentRouteStore
	classifier *scriptedIntentClassifier
	groupID    int64
	now        time.Time
}

func newIntentRouteTestClient(t *testing.T) *dbent.Client {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:intent_route_%d?mode=memory&cache=shared&_fk=1", time.Now().UnixNano()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func newIntentRouteFixture(t *testing.T) *intentRouteFixture {
	t.Helper()
	ctx := context.Background()
	client := newIntentRouteTestClient(t)
	group, err := client.Group.Create().SetName("claude-main").SetPlatform(PlatformAnthropic).Save(ctx)
	require.NoError(t, err)
	for _, name := range []string{"pool-a", "coding-1", "coding-2", "openai-x"} {
		platform := PlatformAnthropic
		if name == "openai-x" {
			platform = PlatformOpenAI
		}
		_, err := client.Account.Create().SetName(name).SetPlatform(platform).SetType(AccountTypeAPIKey).
			SetCredentials(map[string]any{}).Save(ctx)
		require.NoError(t, err)
	}
	f := &intentRouteFixture{
		client: client, store: newMemoryIntentRouteStore(), classifier: &scriptedIntentClassifier{},
		groupID: group.ID, now: time.Unix(1_790_000_000, 0),
	}
	f.svc = &IntentRouterService{
		entClient: client, store: f.store, classifier: f.classifier,
		breakers: map[int64]*intentRouteBreaker{},
		now:      func() time.Time { return f.now },
		spawn:    func(work func()) { work() },
	}
	return f
}

func (f *intentRouteFixture) accountID(t *testing.T, name string) int64 {
	t.Helper()
	for _, row := range f.client.Account.Query().AllX(context.Background()) {
		if row.Name == name {
			return row.ID
		}
	}
	t.Fatalf("account %s not found", name)
	return 0
}

func (f *intentRouteFixture) saveRouter(t *testing.T, mutate func(*IntentRouterInput)) {
	t.Helper()
	in := IntentRouterInput{
		Enabled: true, ClassifierAPIKey: "sk-classifier", ClassifierModel: "gemini-flash-lite",
		Rules: []domain.IntentRule{
			{Name: "coding", Description: "writing or fixing code", Enabled: true, AccountIDs: []int64{f.accountID(t, "coding-1"), f.accountID(t, "coding-2")}},
			{Name: "chat", Description: "small talk", Enabled: true, AccountIDs: []int64{f.accountID(t, "pool-a")}},
		},
	}
	if mutate != nil {
		mutate(&in)
	}
	_, err := f.svc.SaveRouter(context.Background(), f.groupID, in)
	require.NoError(t, err)
}

func (f *intentRouteFixture) request(body string) IntentRouteInput {
	return IntentRouteInput{GroupID: f.groupID, APIKeyID: 11, Header: http.Header{}, Body: []byte(body)}
}

const (
	intentTurn1 = `{"messages":[{"role":"user","content":"please fix my go compile error"}]}`
	intentTurn2 = `{"messages":[{"role":"user","content":"please fix my go compile error"},{"role":"assistant","content":"sure"},{"role":"user","content":"thanks, tell me a joke"}]}`
)

func TestIntentRouter_GroupWithoutRouterCostsNothing(t *testing.T) {
	f := newIntentRouteFixture(t)
	require.False(t, f.svc.Enabled(context.Background(), f.groupID))
	require.Nil(t, f.svc.Decide(context.Background(), f.request(intentTurn1)))
	require.Zero(t, f.classifier.calls)

	f.saveRouter(t, func(in *IntentRouterInput) { in.Enabled = false })
	require.False(t, f.svc.Enabled(context.Background(), f.groupID), "saved but switched off")
	require.Nil(t, f.svc.Decide(context.Background(), f.request(intentTurn1)))
	require.Zero(t, f.classifier.calls)
}

func TestIntentRouter_ClassifiesOncePerConversationAndPinsTheAccount(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	f.saveRouter(t, nil)
	f.classifier.answers = []string{"coding", "chat"}
	coding1, coding2 := f.accountID(t, "coding-1"), f.accountID(t, "coding-2")

	first := f.svc.Decide(ctx, f.request(intentTurn1))
	require.NotNil(t, first)
	require.Equal(t, "coding", first.Intent)
	require.Equal(t, []int64{coding1, coding2}, first.AccountIDs)
	require.Zero(t, first.PinnedAccountID)
	require.Equal(t, []string{"please fix my go compile error"}, f.classifier.texts, "only the user's words reach the classifier")

	first.markSelected(coding2) // selection settled on the second target

	// Second turn: the topic changed, but the conversation stays where it started.
	second := f.svc.Decide(ctx, f.request(intentTurn2))
	require.NotNil(t, second)
	require.Equal(t, "coding", second.Intent)
	require.Equal(t, coding2, second.PinnedAccountID)
	require.Equal(t, []int64{coding2, coding1}, second.orderedAccountIDs())
	require.Equal(t, 1, f.classifier.calls, "no second classification")
	require.Equal(t, []string{"classified", "routed", "cached"}, f.store.kinds())
	for _, ttl := range f.store.ttls {
		require.Equal(t, 2*time.Hour, ttl, "default cache lifetime")
	}
}

func TestIntentRouter_UnmatchedConversationIsRememberedToo(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	f.saveRouter(t, nil)
	f.classifier.answers = []string{"NONE"}

	require.Nil(t, f.svc.Decide(ctx, f.request(intentTurn1)))
	require.Nil(t, f.svc.Decide(ctx, f.request(intentTurn2)))
	require.Equal(t, 1, f.classifier.calls, "an unmatched conversation is not re-classified every turn")
}

func TestIntentRouter_ClearedCacheClassifiesAgain(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	f.saveRouter(t, nil)
	f.classifier.answers = []string{"coding", "chat"}

	require.Equal(t, "coding", f.svc.Decide(ctx, f.request(intentTurn1)).Intent)
	removed, err := f.svc.ClearCache(ctx, f.groupID)
	require.NoError(t, err)
	require.EqualValues(t, 1, removed)
	require.Equal(t, "chat", f.svc.Decide(ctx, f.request(intentTurn2)).Intent)
	require.Equal(t, 2, f.classifier.calls)
}

func TestIntentRouter_RuleChangesReleaseRememberedConversations(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	f.saveRouter(t, nil)
	f.classifier.answers = []string{"coding", "chat"}
	require.Equal(t, "coding", f.svc.Decide(ctx, f.request(intentTurn1)).Intent)

	f.saveRouter(t, func(in *IntentRouterInput) { in.Rules[0].Enabled = false })
	again := f.svc.Decide(ctx, f.request(intentTurn2))
	require.NotNil(t, again)
	require.Equal(t, "chat", again.Intent, "a disabled rule no longer binds the conversations it caught")
}

func TestIntentRouter_EveryFailureMeansOrdinaryScheduling(t *testing.T) {
	ctx := context.Background()

	t.Run("classifier unreachable, then cooling down", func(t *testing.T) {
		f := newIntentRouteFixture(t)
		f.saveRouter(t, nil)
		f.classifier.err = errors.New("dial tcp 127.0.0.1:8080: connection refused")
		for i := 0; i < intentRouteBreakerThreshold; i++ {
			body := fmt.Sprintf(`{"messages":[{"role":"user","content":"request %d"}]}`, i)
			require.Nil(t, f.svc.Decide(ctx, f.request(body)))
		}
		require.Equal(t, intentRouteBreakerThreshold, f.classifier.calls)

		// Broken classifier: stop paying its timeout on every request.
		require.Nil(t, f.svc.Decide(ctx, f.request(`{"messages":[{"role":"user","content":"one more"}]}`)))
		require.Equal(t, intentRouteBreakerThreshold, f.classifier.calls, "skipped while cooling down")

		f.now = f.now.Add(intentRouteBreakerCooldown + time.Second)
		f.classifier.err = nil
		f.classifier.answers = []string{"coding"}
		require.NotNil(t, f.svc.Decide(ctx, f.request(`{"messages":[{"role":"user","content":"back again"}]}`)))
		require.Len(t, f.store.sessions, 1, "only the successful classification was remembered, none of the failures")
	})

	t.Run("answer that names no rule", func(t *testing.T) {
		f := newIntentRouteFixture(t)
		f.saveRouter(t, nil)
		f.classifier.answers = []string{"Sure! Here is how to fix your compile error: ..."}
		require.Nil(t, f.svc.Decide(ctx, f.request(intentTurn1)))
		require.Empty(t, f.store.sessions, "a misunderstanding is not cached; the next turn may try again")
	})

	t.Run("classifier panics", func(t *testing.T) {
		f := newIntentRouteFixture(t)
		f.saveRouter(t, nil)
		f.classifier.panics = true
		require.NotPanics(t, func() { require.Nil(t, f.svc.Decide(ctx, f.request(intentTurn1))) })
	})

	t.Run("cache outage still classifies", func(t *testing.T) {
		f := newIntentRouteFixture(t)
		f.saveRouter(t, nil)
		f.store.getErr = errors.New("redis: connection pool timeout")
		f.classifier.answers = []string{"coding"}
		decision := f.svc.Decide(ctx, f.request(intentTurn1))
		require.NotNil(t, decision)
		require.Equal(t, "coding", decision.Intent)
	})

	t.Run("nothing to classify", func(t *testing.T) {
		f := newIntentRouteFixture(t)
		f.saveRouter(t, nil)
		require.Nil(t, f.svc.Decide(ctx, f.request(`{"messages":[{"role":"user","content":[{"type":"image","source":{}}]}]}`)))
		require.Zero(t, f.classifier.calls)
	})

	t.Run("nil service", func(t *testing.T) {
		var svc *IntentRouterService
		require.Nil(t, svc.Decide(ctx, IntentRouteInput{}))
	})
}

func TestIntentRouter_SaveValidation(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	pool := f.accountID(t, "pool-a")
	base := func() IntentRouterInput {
		return IntentRouterInput{Enabled: true, ClassifierAPIKey: "k", ClassifierModel: "m",
			Rules: []domain.IntentRule{{Name: "coding", Description: "code", Enabled: true, AccountIDs: []int64{pool}}}}
	}
	cases := map[string]struct {
		mutate func(*IntentRouterInput)
		reason string
	}{
		"enabled without classifier": {func(in *IntentRouterInput) { in.ClassifierModel = "" }, "INTENT_CLASSIFIER_INCOMPLETE"},
		"bad base url":               {func(in *IntentRouterInput) { in.ClassifierBaseURL = "ftp://x" }, "INTENT_INVALID_BASE_URL"},
		"unknown protocol":           {func(in *IntentRouterInput) { in.ClassifierProtocol = "grpc" }, "INTENT_INVALID_PROTOCOL"},
		"timeout out of range":       {func(in *IntentRouterInput) { in.ClassifierTimeoutMS = 60000 }, "INTENT_INVALID_TIMEOUT"},
		"cache ttl out of range":     {func(in *IntentRouterInput) { in.CacheTTLSeconds = 5 }, "INTENT_INVALID_CACHE_TTL"},
		"rule without name":          {func(in *IntentRouterInput) { in.Rules[0].Name = " " }, "INTENT_RULE_NAME_REQUIRED"},
		"reserved rule name":         {func(in *IntentRouterInput) { in.Rules[0].Name = "none" }, "INTENT_RULE_NAME_RESERVED"},
		"rule without description":   {func(in *IntentRouterInput) { in.Rules[0].Description = "" }, "INTENT_RULE_DESCRIPTION_REQUIRED"},
		"rule without accounts":      {func(in *IntentRouterInput) { in.Rules[0].AccountIDs = nil }, "INTENT_RULE_ACCOUNTS_REQUIRED"},
		"duplicate rule names": {func(in *IntentRouterInput) {
			in.Rules = append(in.Rules, domain.IntentRule{Name: "Coding", Description: "x", AccountIDs: []int64{pool}})
		}, "INTENT_RULE_NAME_DUPLICATE"},
		"one rule name inside another": {func(in *IntentRouterInput) {
			in.Rules = append(in.Rules, domain.IntentRule{Name: "coding review", Description: "x", AccountIDs: []int64{pool}})
		}, "INTENT_RULE_NAME_OVERLAP"},
		"account that does not exist": {func(in *IntentRouterInput) { in.Rules[0].AccountIDs = []int64{987654} }, "INTENT_ACCOUNT_NOT_FOUND"},
		"account of another protocol": {func(in *IntentRouterInput) { in.Rules[0].AccountIDs = []int64{f.accountID(t, "openai-x")} }, "INTENT_ACCOUNT_PLATFORM_MISMATCH"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := base()
			tc.mutate(&in)
			_, err := f.svc.SaveRouter(ctx, f.groupID, in)
			require.Error(t, err)
			require.Equal(t, tc.reason, infraerrors.Reason(err), err.Error())
		})
	}

	_, err := f.svc.SaveRouter(ctx, f.groupID+999, base())
	require.Equal(t, "GROUP_NOT_FOUND", infraerrors.Reason(err))

	// The secret is write-only: never returned, kept when left blank.
	view, err := f.svc.SaveRouter(ctx, f.groupID, base())
	require.NoError(t, err)
	require.True(t, view.ClassifierAPIKeyConfigured)
	blank := base()
	blank.ClassifierAPIKey = ""
	view, err = f.svc.SaveRouter(ctx, f.groupID, blank)
	require.NoError(t, err)
	require.True(t, view.ClassifierAPIKeyConfigured)
	require.Equal(t, "k", f.client.IntentRouter.Query().OnlyX(ctx).ClassifierAPIKey)
	require.Equal(t, 7200, view.CacheTTLSeconds)
	require.Equal(t, domain.IntentClassifierProtocolOpenAIChat, view.ClassifierProtocol)
}

func TestIntentRouter_TestClassifyReportsWhatDecideHides(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	f.saveRouter(t, func(in *IntentRouterInput) { in.Enabled = false })

	f.classifier.answers = []string{"coding"}
	result, err := f.svc.TestClassify(ctx, f.groupID, "  fix my code  ")
	require.NoError(t, err, "a router can be tried out before it is switched on")
	require.True(t, result.Understood)
	require.Equal(t, "coding", result.Intent)
	require.Len(t, result.AccountIDs, 2)
	require.Empty(t, f.store.sessions, "trying a text out leaves no trace in the cache")

	f.classifier.err = errors.New("401 invalid api key")
	_, err = f.svc.TestClassify(ctx, f.groupID, "fix my code")
	require.Equal(t, "INTENT_CLASSIFIER_FAILED", infraerrors.Reason(err))
	require.ErrorContains(t, err, "401 invalid api key")
}

func TestIntentRouter_DeleteForgetsEverything(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	f.saveRouter(t, nil)
	f.classifier.answers = []string{"coding"}
	require.NotNil(t, f.svc.Decide(ctx, f.request(intentTurn1)))

	require.NoError(t, f.svc.DeleteRouter(ctx, f.groupID))
	require.False(t, f.svc.Enabled(ctx, f.groupID))
	require.Empty(t, f.store.sessions)
	_, err := f.svc.GetRouter(ctx, f.groupID)
	require.Equal(t, "INTENT_ROUTER_NOT_FOUND", infraerrors.Reason(err))
}

// Session writes are detached from the request and may land in any order. The
// classification result arriving AFTER the pin must not wipe the pin out.
func TestIntentRouter_LateClassificationWriteCannotEraseThePin(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	f.saveRouter(t, nil)
	f.classifier.answers = []string{"coding"}
	coding2 := f.accountID(t, "coding-2")

	var queued []func()
	f.svc.spawn = func(work func()) { queued = append(queued, work) }

	decision := f.svc.Decide(ctx, f.request(intentTurn1))
	require.NotNil(t, decision)
	decision.markSelected(coding2)
	for i := len(queued) - 1; i >= 0; i-- { // worst case: exact reverse order
		queued[i]()
	}
	queued = nil
	f.svc.spawn = func(work func()) { work() }

	next := f.svc.Decide(ctx, f.request(intentTurn2))
	require.NotNil(t, next)
	require.Equal(t, coding2, next.PinnedAccountID, "the account that served the first turn is still remembered")
	require.Equal(t, 1, f.classifier.calls)
}

// A turn served from the cache only extends the lifetime; it must not rewrite
// the value, or it could undo a pin made by a parallel request.
func TestIntentRouter_CacheHitOnlyTouchesTheSession(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	f.saveRouter(t, nil)
	f.classifier.answers = []string{"coding"}
	coding1, coding2 := f.accountID(t, "coding-1"), f.accountID(t, "coding-2")
	f.svc.Decide(ctx, f.request(intentTurn1)).markSelected(coding1)

	var queued []func()
	f.svc.spawn = func(work func()) { queued = append(queued, work) }
	stale := f.svc.Decide(ctx, f.request(intentTurn2)) // read the session while it still says coding-1
	require.Equal(t, coding1, stale.PinnedAccountID)
	f.svc.spawn = func(work func()) { work() }
	// Meanwhile a parallel request of the same conversation failed over to coding-2.
	f.svc.Decide(ctx, f.request(intentTurn2)).markSelected(coding2)
	for _, work := range queued { // the first request's delayed writes land last
		work()
	}
	require.Equal(t, coding2, f.svc.Decide(ctx, f.request(intentTurn2)).PinnedAccountID)
	require.Positive(t, f.store.touches)
}
