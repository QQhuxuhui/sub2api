package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func priorityTierTestAccount(id int64, priority int, groupID int64) Account {
	return Account{
		ID:          id,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 10,
		Priority:    priority,
		GroupIDs:    []int64{groupID},
	}
}

type priorityTierTestOptions struct {
	tierGroups           string // settings 值；空串表示未配置
	subscriptionPriority bool
	topK                 int
	disableStickyEscape  bool
	noConcurrencyService bool
}

func newPriorityTierTestService(
	accounts []Account,
	cc ConcurrencyCache,
	gatewayCache *schedulerTestGatewayCache,
	opts priorityTierTestOptions,
) *OpenAIGatewayService {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	resetOpenAIPriorityTierCacheForTest()
	repo := &openAIAdvancedSchedulerSettingRepoStub{values: map[string]string{
		openAIAdvancedSchedulerSettingKey: "true",
	}}
	if opts.subscriptionPriority {
		repo.values[SettingKeyOpenAIAdvancedSchedulerSubscriptionPriorityEnabled] = "true"
	}
	if opts.tierGroups != "" {
		repo.values[SettingKeyOpenAIPriorityTierGroupIDs] = opts.tierGroups
	}
	cfg := newSchedulerTestSubscriptionPriorityConfig()
	cfg.Gateway.OpenAIWS.LBTopK = 7
	if opts.topK > 0 {
		cfg.Gateway.OpenAIWS.LBTopK = opts.topK
	}
	if opts.disableStickyEscape {
		cfg.Gateway.OpenAIScheduler.StickyEscapeEnabled = false
		cfg.Gateway.OpenAIScheduler.StickyEscapeTTFTMs = 30000
		cfg.Gateway.OpenAIScheduler.StickyEscapeErrorRate = 0.5
	}
	if gatewayCache == nil {
		gatewayCache = &schedulerTestGatewayCache{}
	}
	svc := &OpenAIGatewayService{
		accountRepo:      schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}},
		cache:            gatewayCache,
		cfg:              cfg,
		rateLimitService: &RateLimitService{settingService: NewSettingService(repo, &config.Config{})},
	}
	if !opts.noConcurrencyService {
		svc.concurrencyService = NewConcurrencyService(cc)
	}
	return svc
}

func releaseSelection(selection *AccountSelectionResult) {
	if selection != nil && selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

// 组 4 形态：42 优先级 1，若干优先级 10，若干优先级 100。
func group4LikeAccounts(groupID int64) []Account {
	return []Account{
		priorityTierTestAccount(42, 1, groupID),
		priorityTierTestAccount(96, 10, groupID),
		priorityTierTestAccount(97, 10, groupID),
		priorityTierTestAccount(99, 10, groupID),
		priorityTierTestAccount(268, 100, groupID),
		priorityTierTestAccount(277, 100, groupID),
	}
}

// ---------------------------------------------------------------------------
// 负载均衡分档
// ---------------------------------------------------------------------------

func TestPriorityTier_TopTierAlwaysChosenWhenFree(t *testing.T) {
	ctx := context.Background()
	groupID := int64(4)
	svc := newPriorityTierTestService(group4LikeAccounts(groupID), schedulerTestConcurrencyCache{}, nil, priorityTierTestOptions{tierGroups: "[4]"})
	for i := 0; i < 40; i++ {
		selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", fmt.Sprintf("tier_free_%d", i), "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.Equal(t, int64(42), selection.Account.ID, "seed %d", i)
		require.True(t, selection.Acquired)
		require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
		releaseSelection(selection)
	}
}

func TestPriorityTier_DisabledKeepsWeightedSpread(t *testing.T) {
	ctx := context.Background()
	groupID := int64(4)
	// 分组 4 不在列表中（只开了 5）：原调度的加权随机仍会把部分请求给优先级 10 的号。
	svc := newPriorityTierTestService(group4LikeAccounts(groupID), schedulerTestConcurrencyCache{}, nil, priorityTierTestOptions{tierGroups: "[5]"})
	nonTop := 0
	for i := 0; i < 60; i++ {
		selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", fmt.Sprintf("tier_off_%d", i), "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
		require.NoError(t, err)
		if selection.Account.ID != 42 {
			nonTop++
		}
		releaseSelection(selection)
	}
	require.Greater(t, nonTop, 0, "开关关闭时应保持原有加权随机行为")
}

func TestPriorityTier_DisabledMatchesOriginalSelections(t *testing.T) {
	ctx := context.Background()
	groupID := int64(4)
	run := func(tierGroups string) []int64 {
		svc := newPriorityTierTestService(group4LikeAccounts(groupID), schedulerTestConcurrencyCache{}, nil, priorityTierTestOptions{tierGroups: tierGroups})
		ids := make([]int64, 0, 30)
		for i := 0; i < 30; i++ {
			selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", fmt.Sprintf("tier_same_%d", i), "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			ids = append(ids, selection.Account.ID)
			releaseSelection(selection)
		}
		return ids
	}
	require.Equal(t, run(""), run("[7]"))
}

func TestPriorityTier_SpillsToNextTierWhenTopFull(t *testing.T) {
	ctx := context.Background()
	groupID := int64(4)
	cc := schedulerTestConcurrencyCache{acquireResults: map[int64]bool{42: false}}
	svc := newPriorityTierTestService(group4LikeAccounts(groupID), cc, nil, priorityTierTestOptions{tierGroups: "[4]"})
	for i := 0; i < 20; i++ {
		selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", fmt.Sprintf("tier_spill_%d", i), "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
		require.NoError(t, err)
		require.True(t, selection.Acquired)
		require.Contains(t, []int64{96, 97, 99}, selection.Account.ID, "应溢出到优先级 10，而不是 100")
		releaseSelection(selection)
	}
}

func TestPriorityTier_ExcludedTopTierFallsToNextTier(t *testing.T) {
	ctx := context.Background()
	groupID := int64(4)
	svc := newPriorityTierTestService(group4LikeAccounts(groupID), schedulerTestConcurrencyCache{}, nil, priorityTierTestOptions{tierGroups: "[4]"})
	excluded := map[int64]struct{}{42: {}}
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "tier_excluded", "gpt-5.1", excluded, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Contains(t, []int64{96, 97, 99}, selection.Account.ID)
	releaseSelection(selection)
}

func TestPriorityTier_IncompatibleTopTierFallsToNextTier(t *testing.T) {
	ctx := context.Background()
	groupID := int64(4)
	accounts := group4LikeAccounts(groupID)
	// 42 只映射 gpt-5.1，请求 gpt-6-astra 时被过滤。
	accounts[0].Credentials = map[string]any{"model_mapping": map[string]any{"gpt-5.1": "gpt-5.1"}}
	for i := 1; i < len(accounts); i++ {
		accounts[i].Credentials = map[string]any{"model_mapping": map[string]any{"gpt-6-astra": "gpt-6-astra"}}
	}
	svc := newPriorityTierTestService(accounts, schedulerTestConcurrencyCache{}, nil, priorityTierTestOptions{tierGroups: "[4]"})
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "tier_incompatible", "gpt-6-astra", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Contains(t, []int64{96, 97, 99}, selection.Account.ID)
	releaseSelection(selection)
}

func TestPriorityTier_AllFullQueuesOnTopTier(t *testing.T) {
	ctx := context.Background()
	groupID := int64(4)
	cc := schedulerTestConcurrencyCache{acquireResults: map[int64]bool{42: false, 96: false, 97: false, 99: false, 268: false, 277: false}}
	svc := newPriorityTierTestService(group4LikeAccounts(groupID), cc, nil, priorityTierTestOptions{tierGroups: "[4]"})
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "tier_all_full", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.False(t, selection.Acquired)
	require.NotNil(t, selection.WaitPlan)
	require.Equal(t, int64(42), selection.WaitPlan.AccountID)
}

func TestPriorityTier_TriesBeyondTopKBeforeDescending(t *testing.T) {
	ctx := context.Background()
	groupID := int64(8)
	accounts := []Account{
		priorityTierTestAccount(1, 1, groupID),
		priorityTierTestAccount(2, 1, groupID),
		priorityTierTestAccount(3, 1, groupID),
		priorityTierTestAccount(9, 2, groupID),
	}
	// top-K=1：1 号得分最高（负载最低）但抢不到；2 号在 top-K 之外有空位。
	cc := schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{1: false, 3: false},
		loadMap: map[int64]*AccountLoadInfo{
			1: {AccountID: 1, LoadRate: 0},
			2: {AccountID: 2, LoadRate: 50},
			3: {AccountID: 3, LoadRate: 60},
		},
	}
	svc := newPriorityTierTestService(accounts, cc, nil, priorityTierTestOptions{tierGroups: "[8]", topK: 1})
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "tier_beyond_topk", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.True(t, selection.Acquired)
	require.Equal(t, int64(2), selection.Account.ID)
	releaseSelection(selection)
}

// staleThenErrorConcurrencyCache 首次批量负载读取返回给定快照，之后全部报错。
type staleThenErrorConcurrencyCache struct {
	schedulerTestConcurrencyCache
	mu    *sync.Mutex
	calls *int
	first map[int64]*AccountLoadInfo
}

func (c staleThenErrorConcurrencyCache) GetAccountsLoadBatch(ctx context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	c.mu.Lock()
	*c.calls++
	n := *c.calls
	c.mu.Unlock()
	if n > 1 {
		return nil, errors.New("redis unavailable")
	}
	out := make(map[int64]*AccountLoadInfo, len(accounts))
	for _, acc := range accounts {
		if load, ok := c.first[acc.ID]; ok {
			out[acc.ID] = load
			continue
		}
		out[acc.ID] = &AccountLoadInfo{AccountID: acc.ID}
	}
	return out, nil
}

func TestPriorityTier_StaleFullSnapshotWithFailedRefreshStillTriesTopTier(t *testing.T) {
	ctx := context.Background()
	groupID := int64(4)
	accounts := group4LikeAccounts(groupID)
	cc := staleThenErrorConcurrencyCache{
		mu:    &sync.Mutex{},
		calls: new(int),
		first: map[int64]*AccountLoadInfo{
			// 旧快照把 42 标满，实际可抢。
			42: {AccountID: 42, CurrentConcurrency: 10, LoadRate: 100},
		},
	}
	svc := newPriorityTierTestService(accounts, cc, nil, priorityTierTestOptions{tierGroups: "[4]"})
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "tier_stale", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.True(t, selection.Acquired)
	require.Equal(t, int64(42), selection.Account.ID)
	releaseSelection(selection)
}

func TestPriorityTier_SubscriptionPoolQueueOrderMatchesOriginal(t *testing.T) {
	ctx := context.Background()
	groupID := int64(12)
	accounts := []Account{
		{
			ID: 501, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
			Concurrency: 1, Priority: 1, GroupIDs: []int64{groupID}, Credentials: map[string]any{"plan_type": "team"},
		},
		priorityTierTestAccount(502, 1, groupID),
	}
	accounts[1].Concurrency = 1
	cc := schedulerTestConcurrencyCache{acquireResults: map[int64]bool{501: false, 502: false}}
	waitAccount := func(tierGroups string) int64 {
		svc := newPriorityTierTestService(accounts, cc, nil, priorityTierTestOptions{tierGroups: tierGroups, subscriptionPriority: true, topK: 1})
		selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "tier_sub_queue", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
		require.NoError(t, err)
		require.NotNil(t, selection.WaitPlan)
		return selection.WaitPlan.AccountID
	}
	original := waitAccount("")
	require.Equal(t, int64(502), original, "原逻辑：普通池先给 WaitPlan")
	require.Equal(t, original, waitAccount("[12]"))
}

func TestPriorityTier_NilConcurrencyServiceDoesNotPanic(t *testing.T) {
	ctx := context.Background()
	groupID := int64(4)
	svc := newPriorityTierTestService(group4LikeAccounts(groupID), nil, nil, priorityTierTestOptions{tierGroups: "[4]", noConcurrencyService: true})
	require.NotPanics(t, func() {
		selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "tier_nil_cc", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
		require.NoError(t, err)
		require.Equal(t, int64(42), selection.Account.ID)
		releaseSelection(selection)
	})
}

func TestPriorityTier_NonOpenAIPlatformNotActive(t *testing.T) {
	ctx := context.Background()
	groupID := int64(4)
	svc := newPriorityTierTestService(group4LikeAccounts(groupID), schedulerTestConcurrencyCache{}, nil, priorityTierTestOptions{tierGroups: "[4]"})
	require.True(t, svc.openAIPriorityTierActive(ctx, OpenAIAccountScheduleRequest{GroupID: &groupID, Platform: PlatformOpenAI}))
	require.False(t, svc.openAIPriorityTierActive(ctx, OpenAIAccountScheduleRequest{GroupID: &groupID, Platform: PlatformGrok}))
	require.False(t, svc.openAIPriorityTierActive(ctx, OpenAIAccountScheduleRequest{GroupID: nil, Platform: PlatformOpenAI}))
	other := int64(5)
	require.False(t, svc.openAIPriorityTierActive(ctx, OpenAIAccountScheduleRequest{GroupID: &other, Platform: PlatformOpenAI}))
}

func TestPriorityTier_AcceptTierSelectionRejectsDemotedAccount(t *testing.T) {
	st := &openAIPriorityTierSelectionState{}
	released := 0
	demoted := &AccountSelectionResult{
		Account:     &Account{ID: 42, Priority: 9},
		Acquired:    true,
		ReleaseFunc: func() { released++ },
	}
	require.False(t, st.acceptTierSelection(demoted, 1))
	require.Equal(t, 1, released)
	_, recorded := st.demoted[42]
	require.True(t, recorded)

	ok := &AccountSelectionResult{Account: &Account{ID: 7, Priority: 1}, Acquired: true}
	require.True(t, st.acceptTierSelection(ok, 1))
	promoted := &AccountSelectionResult{Account: &Account{ID: 8, Priority: 0}}
	require.True(t, st.acceptTierSelection(promoted, 1))
}

func TestPriorityTier_MergeQueueOrderKeepsPrimaryOrder(t *testing.T) {
	a := &Account{ID: 1}
	b := &Account{ID: 2}
	c := &Account{ID: 3}
	primary := []openAIAccountCandidateScore{{account: b, loadInfo: &AccountLoadInfo{AccountID: 2}}, {account: a, loadInfo: &AccountLoadInfo{AccountID: 1}}}
	retry := []openAIAccountCandidateScore{
		{account: a, loadKnown: true, loadInfo: &AccountLoadInfo{AccountID: 1, CurrentConcurrency: 3}},
		{account: c, loadKnown: true, loadInfo: &AccountLoadInfo{AccountID: 3}},
		{account: b, loadKnown: true, loadInfo: &AccountLoadInfo{AccountID: 2, CurrentConcurrency: 5}},
	}
	merged := mergeOpenAIPriorityTierQueueOrder(primary, retry, map[int64]struct{}{}, false)
	require.Len(t, merged, 3)
	require.Equal(t, []int64{2, 1, 3}, []int64{merged[0].account.ID, merged[1].account.ID, merged[2].account.ID})
	require.Equal(t, 5, merged[0].loadInfo.CurrentConcurrency, "负载数据应换成新鲜值")
	require.True(t, merged[0].loadKnown)

	withoutDemoted := mergeOpenAIPriorityTierQueueOrder(primary, retry, map[int64]struct{}{2: {}}, false)
	require.Equal(t, []int64{1, 3}, []int64{withoutDemoted[0].account.ID, withoutDemoted[1].account.ID})
}

// ---------------------------------------------------------------------------
// 会话粘连抢占
// ---------------------------------------------------------------------------

func TestPriorityTier_StickyOnLowerTierPreemptedByFreeTopTier(t *testing.T) {
	ctx := context.Background()
	groupID := int64(4)
	var released []int64
	gw := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:sess_low": 97}}
	cc := schedulerTestConcurrencyCache{releasedIDs: &released}
	svc := newPriorityTierTestService(group4LikeAccounts(groupID), cc, gw, priorityTierTestOptions{tierGroups: "[4]"})
	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "sess_low", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.True(t, selection.Acquired)
	require.Equal(t, int64(42), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.Contains(t, released, int64(97), "原粘连号的槽位应释放")
	require.Equal(t, int64(42), gw.sessionBindings["openai:sess_low"], "绑定应指向新账号")
	releaseSelection(selection)
}

func TestPriorityTier_StickyKeptWhenTopTierFull(t *testing.T) {
	ctx := context.Background()
	groupID := int64(4)
	gw := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:sess_keep": 97}}
	cc := schedulerTestConcurrencyCache{acquireResults: map[int64]bool{42: false}}
	svc := newPriorityTierTestService(group4LikeAccounts(groupID), cc, gw, priorityTierTestOptions{tierGroups: "[4]"})
	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "sess_keep", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.True(t, selection.Acquired)
	require.Equal(t, int64(97), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
	require.Equal(t, int64(97), gw.sessionBindings["openai:sess_keep"])
	releaseSelection(selection)
}

func TestPriorityTier_StickyOnTopTierNoExtraSelection(t *testing.T) {
	ctx := context.Background()
	groupID := int64(4)
	var acquired []int64
	gw := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:sess_top": 42}}
	cc := schedulerTestConcurrencyCache{acquiredIDs: &acquired}
	svc := newPriorityTierTestService(group4LikeAccounts(groupID), cc, gw, priorityTierTestOptions{tierGroups: "[4]"})
	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "sess_top", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, int64(42), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
	require.Equal(t, []int64{42}, acquired, "粘连号已在最优档时不应再做一次选号")
	releaseSelection(selection)
}

func TestPriorityTier_StickyWaitPlanDiscardedForTieredSelection(t *testing.T) {
	ctx := context.Background()
	groupID := int64(4)
	gw := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:sess_wait": 97}}
	cc := schedulerTestConcurrencyCache{acquireResults: map[int64]bool{97: false}}
	svc := newPriorityTierTestService(group4LikeAccounts(groupID), cc, gw, priorityTierTestOptions{tierGroups: "[4]", disableStickyEscape: true})
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "sess_wait", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.True(t, selection.Acquired)
	require.Equal(t, int64(42), selection.Account.ID)
	releaseSelection(selection)
}

func TestPriorityTier_StickyUntouchedWhenDisabled(t *testing.T) {
	ctx := context.Background()
	groupID := int64(4)
	gw := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:sess_off": 97}}
	svc := newPriorityTierTestService(group4LikeAccounts(groupID), schedulerTestConcurrencyCache{}, gw, priorityTierTestOptions{})
	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "sess_off", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, int64(97), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
	releaseSelection(selection)
}

// ---------------------------------------------------------------------------
// 开关存取
// ---------------------------------------------------------------------------

type priorityTierSettingRepo struct {
	openAIAdvancedSchedulerSettingRepoStub
	mu sync.Mutex
}

func (r *priorityTierSettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[key] = value
	return nil
}

func TestPriorityTier_SettingsNormalizeAndRefreshCache(t *testing.T) {
	ctx := context.Background()
	resetOpenAIPriorityTierCacheForTest()
	repo := &priorityTierSettingRepo{}
	settingSvc := NewSettingService(repo, &config.Config{})

	ids, err := settingSvc.GetOpenAIPriorityTierGroupIDs(ctx)
	require.NoError(t, err)
	require.Empty(t, ids)

	saved, err := settingSvc.SetOpenAIPriorityTierGroupIDs(ctx, []int64{7, 4, 4, 0, -3})
	require.NoError(t, err)
	require.Equal(t, []int64{4, 7}, saved)
	require.Equal(t, "[4,7]", repo.values[SettingKeyOpenAIPriorityTierGroupIDs])

	ids, err = settingSvc.GetOpenAIPriorityTierGroupIDs(ctx)
	require.NoError(t, err)
	require.Equal(t, []int64{4, 7}, ids)

	// 保存后本进程缓存立即生效。
	gw := &OpenAIGatewayService{rateLimitService: &RateLimitService{settingService: settingSvc}}
	groupID := int64(7)
	require.True(t, gw.openAIPriorityTierActive(ctx, OpenAIAccountScheduleRequest{GroupID: &groupID, Platform: PlatformOpenAI}))

	repo.values[SettingKeyOpenAIPriorityTierGroupIDs] = "not-json"
	ids, err = settingSvc.GetOpenAIPriorityTierGroupIDs(ctx)
	require.NoError(t, err)
	require.Empty(t, ids)
}

func TestPriorityTier_StaleRefreshDoesNotOverwriteSavedValue(t *testing.T) {
	resetOpenAIPriorityTierCacheForTest()
	staleGen := openAIPriorityTierGen.Load() // 刷新开始时记下的代次（读到旧值：空）
	storeOpenAIPriorityTierCacheAfterSave([]int64{4})
	publishOpenAIPriorityTierCache(staleGen, map[int64]struct{}{}, openAIPriorityTierCacheTTL)
	cached := openAIPriorityTierCache.Load()
	require.NotNil(t, cached)
	_, ok := cached.groups[4]
	require.True(t, ok, "旧刷新不应覆盖刚保存的值")
}

// ---------------------------------------------------------------------------
// 快照与 DB 优先级不一致（代码审查第 1 轮）
// ---------------------------------------------------------------------------

type countingLoadConcurrencyCache struct {
	schedulerTestConcurrencyCache
	loadCalls *int
	mu        *sync.Mutex
}

func (c countingLoadConcurrencyCache) GetAccountsLoadBatch(ctx context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	c.mu.Lock()
	*c.loadCalls++
	c.mu.Unlock()
	return c.schedulerTestConcurrencyCache.GetAccountsLoadBatch(ctx, accounts)
}

func newPriorityTierSnapshotService(snapshot []*Account, db []Account, cc ConcurrencyCache, tierGroups string) *OpenAIGatewayService {
	svc := newPriorityTierTestService(db, cc, nil, priorityTierTestOptions{tierGroups: tierGroups})
	byID := make(map[int64]*Account, len(snapshot))
	for _, account := range snapshot {
		byID[account.ID] = account
	}
	svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: db}
	svc.cfg.RunMode = config.RunModeStandard
	svc.schedulerSnapshot = &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{snapshotAccounts: snapshot, accountsByID: byID}}
	return svc
}

func TestPriorityTier_QueueSkipsDemotedHeadAndKeepsPoolCandidates(t *testing.T) {
	ctx := context.Background()
	groupID := int64(4)
	a := priorityTierTestAccount(1, 1, groupID)
	b := priorityTierTestAccount(2, 1, groupID)
	c := priorityTierTestAccount(3, 2, groupID)
	// A 负载更低排在前面；DB 中 A 已被调到 9。
	cc := schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{1: false, 2: false, 3: false},
		loadMap: map[int64]*AccountLoadInfo{
			1: {AccountID: 1, LoadRate: 0},
			2: {AccountID: 2, LoadRate: 50},
			3: {AccountID: 3, LoadRate: 0},
		},
	}
	dbA := a
	dbA.Priority = 9
	svc := newPriorityTierSnapshotService([]*Account{&a, &b, &c}, []Account{dbA, b, c}, cc, "[4]")
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "tier_demoted_queue", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection.WaitPlan)
	require.Equal(t, int64(2), selection.WaitPlan.AccountID, "队首被降档后应继续同池的 B，而不是去低档")
}

func TestPriorityTier_BulkDemotionDoesNotRefreshLoadPerCandidate(t *testing.T) {
	ctx := context.Background()
	groupID := int64(4)
	snapshot := make([]*Account, 0, 8)
	db := make([]Account, 0, 8)
	for id := int64(1); id <= 7; id++ {
		acc := priorityTierTestAccount(id, 1, groupID)
		snapshot = append(snapshot, &acc)
		dbAcc := acc
		if id != 7 {
			dbAcc.Priority = 9 // 只有 7 号仍在最优档
		}
		db = append(db, dbAcc)
	}
	low := priorityTierTestAccount(20, 5, groupID)
	snapshot = append(snapshot, &low)
	db = append(db, low)
	calls := 0
	var released []int64
	cc := countingLoadConcurrencyCache{
		schedulerTestConcurrencyCache: schedulerTestConcurrencyCache{releasedIDs: &released},
		loadCalls:                     &calls,
		mu:                            &sync.Mutex{},
	}
	svc := newPriorityTierSnapshotService(snapshot, db, cc, "[4]")
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "tier_bulk_demote", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.True(t, selection.Acquired)
	require.Equal(t, int64(7), selection.Account.ID)
	require.LessOrEqual(t, calls, 3, "负载读取次数应与池大小无关（初始 + 原函数刷新 + 补试刷新）")
	require.NotEmpty(t, released, "被降档且已取得槽位的账号应释放")
	releaseSelection(selection)
}

func TestPriorityTier_MergeQueueOrderCompactTiers(t *testing.T) {
	supported := map[string]any{"openai_compact_supported": true}
	a := &Account{ID: 1, Platform: PlatformOpenAI, Extra: supported}
	u := &Account{ID: 2, Platform: PlatformOpenAI}
	b := &Account{ID: 3, Platform: PlatformOpenAI, Extra: supported}
	require.Equal(t, 2, openAICompactSupportTier(a))
	primary := []openAIAccountCandidateScore{{account: a}, {account: u}}
	retry := []openAIAccountCandidateScore{{account: a}, {account: b}, {account: u}}
	merged := mergeOpenAIPriorityTierQueueOrder(primary, retry, nil, true)
	require.Equal(t, []int64{1, 3, 2}, []int64{merged[0].account.ID, merged[1].account.ID, merged[2].account.ID})
}

func TestPriorityTier_ConcurrentSavesKeepDBAndCacheConsistent(t *testing.T) {
	ctx := context.Background()
	resetOpenAIPriorityTierCacheForTest()
	repo := &priorityTierSettingRepo{}
	settingSvc := NewSettingService(repo, &config.Config{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = settingSvc.SetOpenAIPriorityTierGroupIDs(ctx, []int64{int64(i + 1)})
		}(i)
	}
	wg.Wait()
	ids, err := parseOpenAIPriorityTierGroupIDs(repo.values[SettingKeyOpenAIPriorityTierGroupIDs])
	require.NoError(t, err)
	cached := openAIPriorityTierCache.Load()
	require.NotNil(t, cached)
	require.Len(t, ids, 1)
	_, ok := cached.groups[ids[0]]
	require.True(t, ok)
	require.Len(t, cached.groups, 1)
}

// ---------------------------------------------------------------------------
// 分档排队（代码审查第 2 轮）
// ---------------------------------------------------------------------------

type countingGetByIDRepo struct {
	schedulerTestOpenAIAccountRepo
	mu    *sync.Mutex
	calls map[int64]int
}

func (r countingGetByIDRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	r.calls[id]++
	r.mu.Unlock()
	return r.schedulerTestOpenAIAccountRepo.GetByID(ctx, id)
}

func TestPriorityTier_FallbackMatchesOriginal(t *testing.T) {
	ctx := context.Background()
	groupID := int64(4)
	accounts := []Account{
		priorityTierTestAccount(1, 1, groupID),
		priorityTierTestAccount(2, 1, groupID),
		priorityTierTestAccount(3, 1, groupID),
	}
	accounts[1].Status = StatusDisabled
	svc := newPriorityTierTestService(accounts, schedulerTestConcurrencyCache{}, nil, priorityTierTestOptions{tierGroups: "[4]"})
	scheduler := &defaultOpenAIAccountScheduler{service: svc}
	req := OpenAIAccountScheduleRequest{GroupID: &groupID, Platform: PlatformOpenAI, RequestedModel: "gpt-5.1"}
	orders := [][]int{{0, 1, 2}, {1, 2, 0}, {2, 0, 1}, {1}}
	for _, limited := range []bool{false, true} {
		for _, idx := range orders {
			order := make([]openAIAccountCandidateScore, 0, len(idx))
			for _, i := range idx {
				acc := accounts[i]
				order = append(order, openAIAccountCandidateScore{account: &acc, loadKnown: true, loadInfo: &AccountLoadInfo{AccountID: acc.ID, CurrentConcurrency: acc.Concurrency}})
			}
			attempt := openAIAccountLoadSelectionAttempt{selectionOrder: order, candidateCount: len(order), topK: 1}
			b1, b2 := newOpenAISelectionProbeBudget(), newOpenAISelectionProbeBudget()
			if limited {
				b1.enableLimit()
				b2.enableLimit()
			}
			want, _, _, _, wantErr := scheduler.finishLoadBalanceSelectionFallback(ctx, req, attempt, b1, openAISelectionFilterStats{})
			got, queueFull, gotErr := scheduler.finishPriorityTierFallback(ctx, req, attempt, b2, openAISelectionFilterStats{}, 1, &openAIPriorityTierSelectionState{})
			require.False(t, queueFull)
			require.Equal(t, wantErr == nil, gotErr == nil, "order %v limited %v", idx, limited)
			if wantErr != nil {
				require.Equal(t, wantErr.Error(), gotErr.Error())
				continue
			}
			require.Equal(t, want.Account.ID, got.Account.ID, "order %v limited %v", idx, limited)
			require.Equal(t, *want.WaitPlan, *got.WaitPlan)
			require.Equal(t, b1.rechecks, b2.rechecks)
		}
	}
}

func TestPriorityTier_QueueFullTopTierLetsLowerTierQueue(t *testing.T) {
	ctx := context.Background()
	groupID := int64(4)
	accounts := []Account{priorityTierTestAccount(1, 1, groupID), priorityTierTestAccount(2, 2, groupID)}
	cc := schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{1: false, 2: false},
		loadMap: map[int64]*AccountLoadInfo{
			1: {AccountID: 1, WaitingCount: 5},
			2: {AccountID: 2, WaitingCount: 0},
		},
	}
	svc := newPriorityTierTestService(accounts, cc, nil, priorityTierTestOptions{tierGroups: "[4]"})
	svc.cfg.Gateway.Scheduling.FallbackMaxWaiting = 5
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "tier_queue_full", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection.WaitPlan)
	require.Equal(t, int64(2), selection.WaitPlan.AccountID, "最优档等待队列已满时应让低档排队")

	// 两档都满队：仍回到最优档（与原行为一致）。
	cc.loadMap[2] = &AccountLoadInfo{AccountID: 2, WaitingCount: 9}
	svc = newPriorityTierTestService(accounts, cc, nil, priorityTierTestOptions{tierGroups: "[4]"})
	svc.cfg.Gateway.Scheduling.FallbackMaxWaiting = 5
	selection, _, err = svc.SelectAccountWithScheduler(ctx, &groupID, "", "tier_queue_full_all", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, int64(1), selection.WaitPlan.AccountID)
}

func TestPriorityTier_QueueWalkRechecksEachCandidateOnce(t *testing.T) {
	ctx := context.Background()
	groupID := int64(4)
	snapshot := make([]*Account, 0, 6)
	db := make([]Account, 0, 6)
	acquire := map[int64]bool{}
	for id := int64(1); id <= 5; id++ {
		acc := priorityTierTestAccount(id, 1, groupID)
		snapshot = append(snapshot, &acc)
		dbAcc := acc
		switch {
		case id <= 3:
			dbAcc.Status = StatusDisabled // DB 中已停用
		case id == 4:
			dbAcc.Priority = 9 // DB 中已降档
		}
		db = append(db, dbAcc)
		acquire[id] = false
	}
	cc := schedulerTestConcurrencyCache{acquireResults: acquire}
	svc := newPriorityTierSnapshotService(snapshot, db, cc, "[4]")
	repo := countingGetByIDRepo{schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: db}, mu: &sync.Mutex{}, calls: map[int64]int{}}
	svc.accountRepo = repo
	scheduler := &defaultOpenAIAccountScheduler{service: svc}
	order := make([]openAIAccountCandidateScore, 0, len(snapshot))
	for _, acc := range snapshot {
		order = append(order, openAIAccountCandidateScore{account: acc, loadInfo: &AccountLoadInfo{AccountID: acc.ID}})
	}
	req := OpenAIAccountScheduleRequest{GroupID: &groupID, Platform: PlatformOpenAI, RequestedModel: "gpt-5.1"}
	st := &openAIPriorityTierSelectionState{}
	selection, queueFull, err := scheduler.finishPriorityTierFallback(ctx, req, openAIAccountLoadSelectionAttempt{selectionOrder: order}, newOpenAISelectionProbeBudget(), openAISelectionFilterStats{}, 1, st)
	require.NoError(t, err)
	require.False(t, queueFull)
	require.Equal(t, int64(5), selection.WaitPlan.AccountID)
	for id, n := range repo.calls {
		require.LessOrEqual(t, n, 1, "账号 %d 被重复查库", id)
	}
	_, demoted := st.demoted[4]
	require.True(t, demoted)
}
