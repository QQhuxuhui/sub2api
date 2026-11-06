//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type iqShowcaseFakeRepo struct {
	mu        sync.Mutex
	groups    map[int64]*IQShowcaseGroupInfo
	keys      map[int64]*IQShowcaseRunKey
	items     map[int64]*IQShowcaseItem
	due       []int64
	claims    int
	claimNil  bool
	owned     bool
	completed []*IQShowcaseResult
	done      chan struct{}
}

func newIQShowcaseFakeRepo() *iqShowcaseFakeRepo {
	g := int64(5)
	return &iqShowcaseFakeRepo{
		groups: map[int64]*IQShowcaseGroupInfo{5: {ID: 5, Name: "speed", Platform: "openai"}},
		keys:   map[int64]*IQShowcaseRunKey{9: {Key: "sk-secret-key-123456", GroupID: &g, Status: StatusAPIKeyActive}},
		items:  map[int64]*IQShowcaseItem{},
		owned:  true,
		done:   make(chan struct{}, 16),
	}
}

func (r *iqShowcaseFakeRepo) ListItems(context.Context) ([]*IQShowcaseItem, error) { return nil, nil }
func (r *iqShowcaseFakeRepo) GetItem(_ context.Context, id int64) (*IQShowcaseItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if it, ok := r.items[id]; ok {
		cp := *it
		return &cp, nil
	}
	return nil, ErrIQShowcaseItemNotFound
}
func (r *iqShowcaseFakeRepo) CreateItem(_ context.Context, it *IQShowcaseItem) (*IQShowcaseItem, error) {
	it.ID = int64(len(r.items) + 1)
	r.items[it.ID] = it
	return it, nil
}
func (r *iqShowcaseFakeRepo) UpdateItem(_ context.Context, it *IQShowcaseItem) (*IQShowcaseItem, error) {
	r.items[it.ID] = it
	return it, nil
}
func (r *iqShowcaseFakeRepo) SetEnabled(_ context.Context, id int64, enabled bool) (*IQShowcaseItem, error) {
	it, ok := r.items[id]
	if !ok {
		return nil, ErrIQShowcaseItemNotFound
	}
	it.Enabled = enabled
	return it, nil
}
func (r *iqShowcaseFakeRepo) DeleteItem(context.Context, int64) error { return nil }
func (r *iqShowcaseFakeRepo) GetGroup(_ context.Context, id int64) (*IQShowcaseGroupInfo, error) {
	return r.groups[id], nil
}
func (r *iqShowcaseFakeRepo) GetRunKey(_ context.Context, id int64) (*IQShowcaseRunKey, error) {
	return r.keys[id], nil
}
func (r *iqShowcaseFakeRepo) ListKeyOptions(context.Context, int64) ([]*IQShowcaseKeyOption, error) {
	return nil, nil
}
func (r *iqShowcaseFakeRepo) ListDueItemIDs(context.Context, int) ([]int64, error) { return r.due, nil }
func (r *iqShowcaseFakeRepo) ClaimItem(_ context.Context, id int64, _ string, _ time.Duration, _ bool) (*IQShowcaseItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claims++
	if r.claimNil {
		return nil, nil
	}
	cp := *r.items[id]
	return &cp, nil
}
func (r *iqShowcaseFakeRepo) CompleteRound(_ context.Context, _ int64, _ string, res *IQShowcaseResult) (bool, error) {
	r.mu.Lock()
	r.completed = append(r.completed, res)
	owned := r.owned
	r.mu.Unlock()
	r.done <- struct{}{}
	return owned, nil
}
func (r *iqShowcaseFakeRepo) ListResults(context.Context, int64, *bool, int) ([]*IQShowcaseResult, error) {
	return nil, nil
}
func (r *iqShowcaseFakeRepo) ListVisibleItems(context.Context) ([]*IQShowcaseVisibleItem, error) {
	return []*IQShowcaseVisibleItem{
		{ID: 1, Title: "pelican", GroupID: 5, GroupName: "speed", ConfigRev: 2, MaxCards: 3},
		{ID: 2, Title: "fox", GroupID: 5, GroupName: "speed", ConfigRev: 1, MaxCards: 3},
		{ID: 3, Title: "owl", GroupID: 6, GroupName: "pro", ConfigRev: 1, MaxCards: 3},
	}, nil
}
func (r *iqShowcaseFakeRepo) ListCardMeta(_ context.Context, itemID, groupID int64, limit int) ([]*IQShowcaseCardMeta, error) {
	if itemID == 1 {
		if groupID != 5 || limit != 3 {
			return nil, fmt.Errorf("cards asked for group %d limit %d", groupID, limit)
		}
		return []*IQShowcaseCardMeta{{ID: 11, Model: "m"}}, nil
	}
	return nil, nil
}
func (r *iqShowcaseFakeRepo) GetCardSVGs(context.Context, []int64) ([]*IQShowcaseCardSVG, error) {
	return nil, nil
}
func (r *iqShowcaseFakeRepo) Maintenance(context.Context, time.Duration) error { return nil }

type iqShowcaseSettingRepo struct {
	SettingRepository
	enabled string
}

func (r *iqShowcaseSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{SettingKeyIQShowcaseEnabled: r.enabled}, nil
}

func newIQShowcaseTestService(repo *iqShowcaseFakeRepo, gateway *httptest.Server, enabled bool) *IQShowcaseService {
	flag := "false"
	if enabled {
		flag = "true"
	}
	s := NewIQShowcaseService(repo, &SettingService{settingRepo: &iqShowcaseSettingRepo{enabled: flag}}, nil)
	if gateway != nil {
		s.baseURL = gateway.URL
	}
	return s
}

func iqShowcaseTestItem() *IQShowcaseItem {
	key := int64(9)
	return &IQShowcaseItem{ID: 1, Title: "pelican", GroupID: 5, APIKeyID: &key, Protocol: IQShowcaseProtocolOpenAIChat,
		Model: "gpt-x", Prompt: "draw a pelican", ConfigRev: 3, MaxCards: 12, IntervalMinutes: 60, Enabled: true}
}

func chatSSE(text string) string {
	quoted, _ := json.Marshal(text)
	return sse(`{"choices":[{"delta":{"content":`+string(quoted)+`}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)
}

func TestIQShowcaseProbe_SendsThroughGatewayAndKeepsSVG(t *testing.T) {
	var gotAuth, gotPath string
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(chatSSE("Sure:\n```svg\n" + iqTestSVG + "\n```")))
	}))
	defer gw.Close()
	repo := newIQShowcaseFakeRepo()
	s := newIQShowcaseTestService(repo, gw, true)

	res := s.probe(context.Background(), iqShowcaseTestItem())
	require.True(t, res.Success, res.Error)
	require.Equal(t, iqTestSVG, res.SVG)
	require.Equal(t, "Bearer sk-secret-key-123456", gotAuth)
	require.Equal(t, "/v1/chat/completions", gotPath)
	require.Equal(t, 3, res.ConfigRev, "the result keeps the claimed snapshot")
	require.Equal(t, int64(5), res.GroupID)
	require.Equal(t, "gpt-x", res.Model)
	require.Equal(t, http.StatusOK, res.HTTPStatus)
}

func TestIQShowcaseProbe_Failures(t *testing.T) {
	status, body := http.StatusOK, ""
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer gw.Close()

	t.Run("gateway error never leaks the key", func(t *testing.T) {
		repo := newIQShowcaseFakeRepo()
		s := newIQShowcaseTestService(repo, gw, true)
		status, body = http.StatusTooManyRequests, `{"error":"bad key sk-secret-key-123456"}`
		res := s.probe(context.Background(), iqShowcaseTestItem())
		require.False(t, res.Success)
		require.Equal(t, http.StatusTooManyRequests, res.HTTPStatus)
		require.Contains(t, res.Error, "429")
		require.NotContains(t, res.Error, "sk-secret-key-123456")
	})
	t.Run("answer without svg", func(t *testing.T) {
		s := newIQShowcaseTestService(newIQShowcaseFakeRepo(), gw, true)
		status, body = http.StatusOK, chatSSE("I would rather not.")
		res := s.probe(context.Background(), iqShowcaseTestItem())
		require.False(t, res.Success)
		require.Contains(t, res.Error, "no <svg>")
	})

	keyCases := map[string]func(r *iqShowcaseFakeRepo, it *IQShowcaseItem){
		"key deleted":    func(r *iqShowcaseFakeRepo, _ *IQShowcaseItem) { delete(r.keys, 9) },
		"key id cleared": func(_ *iqShowcaseFakeRepo, it *IQShowcaseItem) { it.APIKeyID = nil },
		"key moved":      func(r *iqShowcaseFakeRepo, _ *IQShowcaseItem) { g := int64(6); r.keys[9].GroupID = &g },
		"key disabled":   func(r *iqShowcaseFakeRepo, _ *IQShowcaseItem) { r.keys[9].Status = StatusAPIKeyDisabled },
		"key expired": func(r *iqShowcaseFakeRepo, _ *IQShowcaseItem) {
			past := time.Now().Add(-time.Hour)
			r.keys[9].ExpiresAt = &past
		},
		"group deleted": func(r *iqShowcaseFakeRepo, _ *IQShowcaseItem) { delete(r.groups, 5) },
	}
	for name, mutate := range keyCases {
		t.Run(name, func(t *testing.T) {
			hit := false
			never := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
			defer never.Close()
			repo := newIQShowcaseFakeRepo()
			it := iqShowcaseTestItem()
			mutate(repo, it)
			res := newIQShowcaseTestService(repo, never, true).probe(context.Background(), it)
			require.False(t, res.Success)
			require.NotEmpty(t, res.Error)
			require.False(t, hit, "no request is sent with an unusable key")
		})
	}
}

func TestIQShowcaseExecuteRound_RecordsTimeoutWithFreshContext(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer gw.Close()
	repo := newIQShowcaseFakeRepo()
	s := newIQShowcaseTestService(repo, gw, true)
	s.cancel() // the probe's context is already over, like a used-up deadline
	s.executeRound(iqShowcaseTestItem(), "tok")
	require.Len(t, repo.completed, 1, "the failure is still written with the separate finish context")
	require.False(t, repo.completed[0].Success)
}

func TestIQShowcaseRunDue_TakesSlotBeforeClaim(t *testing.T) {
	repo := newIQShowcaseFakeRepo()
	repo.items[1] = iqShowcaseTestItem()
	repo.due = []int64{1, 1, 1}
	s := newIQShowcaseTestService(repo, nil, true)
	for i := 0; i < iqShowcaseMaxConcurrent; i++ {
		require.True(t, s.tryAcquireSlot())
	}
	s.RunDue(context.Background())
	require.Zero(t, repo.claims, "no claim without a free slot")

	require.ErrorIs(t, s.RunNow(context.Background(), 1), ErrIQShowcaseNoSlot)
	require.Zero(t, repo.claims)

	s.releaseSlot()
	repo.claimNil = true
	require.ErrorIs(t, s.RunNow(context.Background(), 1), ErrIQShowcaseItemBusy)
	require.Len(t, s.sem, iqShowcaseMaxConcurrent-1, "a failed claim gives the slot back")
	require.ErrorIs(t, s.RunNow(context.Background(), 99), ErrIQShowcaseItemNotFound)
}

func TestIQShowcaseRunNow_RunsAndReleasesSlot(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(chatSSE(iqTestSVG)))
	}))
	defer gw.Close()
	repo := newIQShowcaseFakeRepo()
	repo.items[1] = iqShowcaseTestItem()
	repo.owned = false // a round that lost its lease only logs
	s := newIQShowcaseTestService(repo, gw, true)
	require.NoError(t, s.RunNow(context.Background(), 1))
	select {
	case <-repo.done:
	case <-time.After(5 * time.Second):
		t.Fatal("round did not finish")
	}
	s.Stop()
	require.Empty(t, s.sem)
	require.True(t, repo.completed[0].Success)
}

func TestIQShowcaseNormalizeItem(t *testing.T) {
	repo := newIQShowcaseFakeRepo()
	s := newIQShowcaseTestService(repo, nil, true)
	ok := func() *IQShowcaseItem {
		it := iqShowcaseTestItem()
		it.IntervalMinutes, it.MaxCards = 0, 0
		return it
	}
	it := ok()
	require.NoError(t, s.normalizeItem(context.Background(), it))
	require.Equal(t, iqShowcaseDefaultInterval, it.IntervalMinutes)
	require.Equal(t, iqShowcaseDefaultMaxCards, it.MaxCards)

	bad := map[string]func(*IQShowcaseItem){
		"IQ_SHOWCASE_TITLE_INVALID":      func(i *IQShowcaseItem) { i.Title = " " },
		"IQ_SHOWCASE_GROUP_NOT_FOUND":    func(i *IQShowcaseItem) { i.GroupID = 77 },
		"IQ_SHOWCASE_KEY_REQUIRED":       func(i *IQShowcaseItem) { i.APIKeyID = nil },
		"IQ_SHOWCASE_KEY_INVALID":        func(i *IQShowcaseItem) { k := int64(404); i.APIKeyID = &k },
		"IQ_SHOWCASE_PROTOCOL_INVALID":   func(i *IQShowcaseItem) { i.Protocol = "grpc" },
		"IQ_SHOWCASE_MODEL_INVALID":      func(i *IQShowcaseItem) { i.Model = "" },
		"IQ_SHOWCASE_PROMPT_INVALID":     func(i *IQShowcaseItem) { i.Prompt = strings.Repeat("a", iqShowcaseMaxPromptBytes+1) },
		"IQ_SHOWCASE_REASONING_INVALID":  func(i *IQShowcaseItem) { i.ReasoningEffort = "max" },
		"IQ_SHOWCASE_MAX_TOKENS_INVALID": func(i *IQShowcaseItem) { i.MaxOutputTokens = -1 },
		"IQ_SHOWCASE_INTERVAL_INVALID":   func(i *IQShowcaseItem) { i.IntervalMinutes = 1441 },
		"IQ_SHOWCASE_MAX_CARDS_INVALID":  func(i *IQShowcaseItem) { i.MaxCards = 101 },
	}
	for reason, mutate := range bad {
		it := ok()
		mutate(it)
		require.Equal(t, reason, infraerrors.Reason(s.normalizeItem(context.Background(), it)), reason)
	}
	// A key of another group is refused.
	g := int64(6)
	repo.keys[10] = &IQShowcaseRunKey{Key: "k", GroupID: &g, Status: StatusAPIKeyActive}
	it = ok()
	k := int64(10)
	it.APIKeyID = &k
	require.Equal(t, "IQ_SHOWCASE_KEY_INVALID", infraerrors.Reason(s.normalizeItem(context.Background(), it)))
}

// An item whose key or group is gone fails the full save validation, but it
// must still be possible to switch it off.
func TestIQShowcaseSetEnabled_WorksWhenReferencesAreGone(t *testing.T) {
	repo := newIQShowcaseFakeRepo()
	s := newIQShowcaseTestService(repo, nil, true)
	repo.items[1] = iqShowcaseTestItem()
	delete(repo.keys, 9)
	delete(repo.groups, 5)

	full := iqShowcaseTestItem()
	full.Enabled = false
	_, err := s.UpdateItem(context.Background(), full)
	require.Error(t, err, "a full save still validates")

	got, err := s.SetEnabled(context.Background(), 1, false)
	require.NoError(t, err)
	require.False(t, got.Enabled)
	_, err = s.SetEnabled(context.Background(), 99, false)
	require.ErrorIs(t, err, ErrIQShowcaseItemNotFound)
}

func TestIQShowcaseUserView(t *testing.T) {
	repo := newIQShowcaseFakeRepo()
	_, err := newIQShowcaseTestService(repo, nil, false).UserView(context.Background())
	require.ErrorIs(t, err, ErrIQShowcaseDisabled)
	_, err = newIQShowcaseTestService(repo, nil, false).CardSVGs(context.Background(), []int64{1})
	require.ErrorIs(t, err, ErrIQShowcaseDisabled)

	s := newIQShowcaseTestService(repo, nil, true)
	view, err := s.UserView(context.Background())
	require.NoError(t, err)
	require.Len(t, view.Groups, 2)
	require.Equal(t, "speed", view.Groups[0].GroupName)
	require.Len(t, view.Groups[0].Items, 2)
	require.Equal(t, []*IQShowcaseCardMeta{{ID: 11, Model: "m"}}, view.Groups[0].Items[0].Cards)
	require.NotNil(t, view.Groups[0].Items[1].Cards, "an item without cards is listed with an empty list")
	require.Equal(t, "pro", view.Groups[1].GroupName)

	ids := make([]int64, iqShowcaseMaxSVGBatch+1)
	_, err = s.CardSVGs(context.Background(), ids)
	require.Equal(t, "IQ_SHOWCASE_TOO_MANY_IDS", infraerrors.Reason(err))
}
