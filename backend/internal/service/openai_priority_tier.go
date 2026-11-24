package service

// OpenAI 分组级「优先级严格分档」调度（gptniubi 二开）。
//
// 原高级调度器里 accounts.priority 只是负载均衡打分中的一项（按候选 min~max
// 归一化后再 top-K 加权随机），优先级最高的账号只能拿到部分流量。开启本开关的
// 分组改为：先只在优先级数值最小的一档里选，这一档满了或不可用才轮到下一档；
// 档内仍完整复用原有的打分、top-K、订阅优先、负载刷新与排队逻辑。
//
// 与原调度解耦：本文件是唯一实现；openai_account_scheduler.go 只有两处挂点
// （selectByLoadBalance 与 Select 的会话粘连分支），开关关闭时两处都不进入。
// 开关存于 settings 表单键（分组 ID 列表），不改 groups 表。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// SettingKeyOpenAIPriorityTierGroupIDs 开启严格分档的分组 ID 列表（JSON 整数数组）。
const SettingKeyOpenAIPriorityTierGroupIDs = "openai_priority_tier_group_ids"

const (
	openAIPriorityTierCacheTTL      = 10 * time.Second
	openAIPriorityTierErrorCacheTTL = 2 * time.Second
	openAIPriorityTierDBTimeout     = 2 * time.Second
)

type openAIPriorityTierCacheEntry struct {
	groups    map[int64]struct{}
	expiresAt int64
}

var (
	openAIPriorityTierCache atomic.Pointer[openAIPriorityTierCacheEntry]
	// openAIPriorityTierGen 每次管理端保存 +1。后台刷新在读库前记下代次，发布时
	// 代次已变则丢弃结果，防止“先读到旧值的刷新”覆盖刚保存的新值。
	openAIPriorityTierGen     atomic.Uint64
	openAIPriorityTierSF      singleflight.Group
	openAIPriorityTierPublish sync.Mutex
	// openAIPriorityTierSaveMu 串行化同进程的“写库 + 发布缓存”，保证两次保存按同一顺序落库与生效。
	openAIPriorityTierSaveMu sync.Mutex
)

func resetOpenAIPriorityTierCacheForTest() {
	openAIPriorityTierCache.Store(nil)
	openAIPriorityTierGen.Add(1)
	openAIPriorityTierSF = singleflight.Group{}
}

// normalizeOpenAIPriorityTierGroupIDs 去重、去掉非正数、升序。
func normalizeOpenAIPriorityTierGroupIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// parseOpenAIPriorityTierGroupIDs 空值 → 空列表；解析失败 → 空列表 + 错误。
func parseOpenAIPriorityTierGroupIDs(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []int64{}, nil
	}
	var ids []int64
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return []int64{}, err
	}
	return normalizeOpenAIPriorityTierGroupIDs(ids), nil
}

func openAIPriorityTierGroupSet(ids []int64) map[int64]struct{} {
	set := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// publishOpenAIPriorityTierCache 仅当代次未变时发布（gen 为读库前记下的代次）。
func publishOpenAIPriorityTierCache(gen uint64, groups map[int64]struct{}, ttl time.Duration) {
	openAIPriorityTierPublish.Lock()
	defer openAIPriorityTierPublish.Unlock()
	if openAIPriorityTierGen.Load() != gen {
		return
	}
	openAIPriorityTierCache.Store(&openAIPriorityTierCacheEntry{
		groups:    groups,
		expiresAt: time.Now().Add(ttl).UnixNano(),
	})
}

// storeOpenAIPriorityTierCacheAfterSave 管理端保存成功后：代次 +1 并发布新值。
func storeOpenAIPriorityTierCacheAfterSave(ids []int64) {
	openAIPriorityTierPublish.Lock()
	defer openAIPriorityTierPublish.Unlock()
	openAIPriorityTierGen.Add(1)
	openAIPriorityTierCache.Store(&openAIPriorityTierCacheEntry{
		groups:    openAIPriorityTierGroupSet(ids),
		expiresAt: time.Now().Add(openAIPriorityTierCacheTTL).UnixNano(),
	})
}

// GetOpenAIPriorityTierGroupIDs 直读 DB（管理端读写路径使用）。
func (s *SettingService) GetOpenAIPriorityTierGroupIDs(ctx context.Context) ([]int64, error) {
	if s == nil || s.settingRepo == nil {
		return []int64{}, nil
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAIPriorityTierGroupIDs)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return []int64{}, nil
		}
		return nil, fmt.Errorf("get openai priority tier groups: %w", err)
	}
	ids, parseErr := parseOpenAIPriorityTierGroupIDs(value)
	if parseErr != nil {
		slog.Warn("openai_priority_tier_groups_parse_failed", "error", parseErr)
	}
	return ids, nil
}

// SetOpenAIPriorityTierGroupIDs 规范化后保存，并立即刷新本进程缓存。
func (s *SettingService) SetOpenAIPriorityTierGroupIDs(ctx context.Context, ids []int64) ([]int64, error) {
	if s == nil || s.settingRepo == nil {
		return nil, fmt.Errorf("setting repository unavailable")
	}
	normalized := normalizeOpenAIPriorityTierGroupIDs(ids)
	data, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("marshal openai priority tier groups: %w", err)
	}
	openAIPriorityTierSaveMu.Lock()
	defer openAIPriorityTierSaveMu.Unlock()
	if err := s.settingRepo.Set(ctx, SettingKeyOpenAIPriorityTierGroupIDs, string(data)); err != nil {
		return nil, err
	}
	storeOpenAIPriorityTierCacheAfterSave(normalized)
	return normalized, nil
}

// openAIPriorityTierGroups 调度热路径读取（进程内缓存，TTL 10s）。
// 读库失败沿用上次已知值，无则视为全部关闭，短 TTL 后重试。
func (s *OpenAIGatewayService) openAIPriorityTierGroups(ctx context.Context) map[int64]struct{} {
	cached := openAIPriorityTierCache.Load()
	if cached != nil && time.Now().UnixNano() < cached.expiresAt {
		return cached.groups
	}
	repo := s.openAIAdvancedSchedulerSettingRepo()
	if repo == nil {
		if cached != nil {
			return cached.groups
		}
		return nil
	}
	result, _, _ := openAIPriorityTierSF.Do("groups", func() (any, error) {
		gen := openAIPriorityTierGen.Load()
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIPriorityTierDBTimeout)
		defer cancel()
		value, err := repo.GetValue(dbCtx, SettingKeyOpenAIPriorityTierGroupIDs)
		if err != nil && !errors.Is(err, ErrSettingNotFound) {
			slog.Warn("openai_priority_tier_groups_load_failed", "error", err)
			var groups map[int64]struct{}
			if previous := openAIPriorityTierCache.Load(); previous != nil {
				groups = previous.groups
			}
			publishOpenAIPriorityTierCache(gen, groups, openAIPriorityTierErrorCacheTTL)
			return groups, nil
		}
		ids, parseErr := parseOpenAIPriorityTierGroupIDs(value)
		if parseErr != nil {
			slog.Warn("openai_priority_tier_groups_parse_failed", "error", parseErr)
		}
		groups := openAIPriorityTierGroupSet(ids)
		publishOpenAIPriorityTierCache(gen, groups, openAIPriorityTierCacheTTL)
		if latest := openAIPriorityTierCache.Load(); latest != nil && openAIPriorityTierGen.Load() != gen {
			// 刷新期间有人保存：以保存后的值为准。
			return latest.groups, nil
		}
		return groups, nil
	})
	groups, _ := result.(map[int64]struct{})
	return groups
}

// openAIPriorityTierActive 两处挂点共用的开关判断：仅 OpenAI 平台且分组在列表中。
func (s *OpenAIGatewayService) openAIPriorityTierActive(ctx context.Context, req OpenAIAccountScheduleRequest) bool {
	if s == nil || req.GroupID == nil {
		return false
	}
	if NormalizeOpenAICompatiblePlatform(req.Platform) != PlatformOpenAI {
		return false
	}
	groups := s.openAIPriorityTierGroups(ctx)
	if len(groups) == 0 {
		return false
	}
	_, ok := groups[*req.GroupID]
	return ok
}

// ---------------------------------------------------------------------------
// 负载均衡分档选号
// ---------------------------------------------------------------------------

type openAIPriorityTier struct {
	priority int
	accounts []*Account
}

func splitOpenAIPriorityTiers(accounts []*Account, skip map[int64]struct{}) []openAIPriorityTier {
	byPriority := make(map[int][]*Account)
	for _, account := range accounts {
		if account == nil {
			continue
		}
		if _, skipped := skip[account.ID]; skipped {
			continue
		}
		p := openAIAccountSchedulingPriority(account)
		byPriority[p] = append(byPriority[p], account)
	}
	tiers := make([]openAIPriorityTier, 0, len(byPriority))
	for p, list := range byPriority {
		tiers = append(tiers, openAIPriorityTier{priority: p, accounts: list})
	}
	sort.Slice(tiers, func(i, j int) bool { return tiers[i].priority < tiers[j].priority })
	return tiers
}

func openAIPriorityTierWithout(accounts []*Account, skip map[int64]struct{}) []*Account {
	if len(skip) == 0 {
		return accounts
	}
	out := make([]*Account, 0, len(accounts))
	for _, account := range accounts {
		if account == nil {
			continue
		}
		if _, skipped := skip[account.ID]; skipped {
			continue
		}
		out = append(out, account)
	}
	return out
}

func openAIPriorityTierPoolContains(pool []*Account, accountID int64) bool {
	if accountID <= 0 {
		return false
	}
	for _, account := range pool {
		if account != nil && account.ID == accountID {
			return true
		}
	}
	return false
}

// openAIPriorityTierInnerRequest 内层调用用的请求副本：选号中不写绑定（采用后再写），
// 不在本池内的加权粘连 ID 置 0，防止加权粘连跨档。
func openAIPriorityTierInnerRequest(req OpenAIAccountScheduleRequest, pool []*Account) OpenAIAccountScheduleRequest {
	inner := req
	inner.PreserveStickyBinding = true
	if !openAIPriorityTierPoolContains(pool, inner.StickyAccountID) {
		inner.StickyAccountID = 0
	}
	if !openAIPriorityTierPoolContains(pool, inner.StickyPreviousAccountID) {
		inner.StickyPreviousAccountID = 0
	}
	return inner
}

func releaseOpenAIPriorityTierSelection(selection *AccountSelectionResult) {
	if selection != nil && selection.Acquired && selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

// openAIPriorityTierSelectionState 一次分档选号的过程状态。
type openAIPriorityTierSelectionState struct {
	demoted map[int64]struct{}
}

// acceptTierSelection 档位复核：结果账号（已做 DB 复核的最新对象）priority 不得劣于本档。
// 不通过时释放已取得的槽位，并把账号记入本请求的局部排除集合。
func (st *openAIPriorityTierSelectionState) acceptTierSelection(selection *AccountSelectionResult, tierPriority int) bool {
	if selection == nil || selection.Account == nil {
		return false
	}
	if openAIAccountSchedulingPriority(selection.Account) <= tierPriority {
		return true
	}
	releaseOpenAIPriorityTierSelection(selection)
	if st.demoted == nil {
		st.demoted = make(map[int64]struct{})
	}
	st.demoted[selection.Account.ID] = struct{}{}
	return false
}

// openAIPriorityTierPoolOutcome 单个池的抢槽结果。
type openAIPriorityTierPoolOutcome struct {
	result              *AccountSelectionResult
	attempt             *openAIAccountLoadSelectionAttempt
	queueAttempt        *openAIAccountLoadSelectionAttempt
	noCompactCandidates bool
	err                 error
}

// mergeOpenAIPriorityTierQueueOrder 排队顺序：首选 attempt 的原顺序在前（负载数据换成
// 新鲜值，去掉被降档的账号），再追加补试顺序里尚未包含的账号。
func mergeOpenAIPriorityTierQueueOrder(
	primary []openAIAccountCandidateScore,
	retry []openAIAccountCandidateScore,
	demoted map[int64]struct{},
	requireCompact bool,
) []openAIAccountCandidateScore {
	freshByID := make(map[int64]openAIAccountCandidateScore, len(retry))
	for _, candidate := range retry {
		if candidate.account != nil {
			freshByID[candidate.account.ID] = candidate
		}
	}
	out := make([]openAIAccountCandidateScore, 0, len(primary)+len(retry))
	included := make(map[int64]struct{}, len(primary)+len(retry))
	appendCandidate := func(candidate openAIAccountCandidateScore) {
		if candidate.account == nil {
			return
		}
		id := candidate.account.ID
		if _, skipped := demoted[id]; skipped {
			return
		}
		if _, ok := included[id]; ok {
			return
		}
		included[id] = struct{}{}
		out = append(out, candidate)
	}
	for _, candidate := range primary {
		if candidate.account == nil {
			continue
		}
		if fresh, ok := freshByID[candidate.account.ID]; ok {
			candidate.loadInfo = fresh.loadInfo
			candidate.loadKnown = fresh.loadKnown
		}
		appendCandidate(candidate)
	}
	for _, candidate := range retry {
		appendCandidate(candidate)
	}
	if requireCompact {
		// compact 能力分层：明确支持 → 未知 → 其余（旧快照复核），层内保持上面的顺序。
		sort.SliceStable(out, func(i, j int) bool {
			return openAICompactSupportTier(out[i].account) > openAICompactSupportTier(out[j].account)
		})
	}
	return out
}

// acquireFromPriorityTierPool 在一个池内抢槽：先走原 top-K 首选，再对整池做一次
// 新鲜负载读取后补试，确认整池都抢不到才返回（附带供排队阶段用的 attempt）。
func (s *defaultOpenAIAccountScheduler) acquireFromPriorityTierPool(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	pool []*Account,
	tierPriority int,
	loadMap map[int64]*AccountLoadInfo,
	budget *openAISelectionProbeBudget,
	st *openAIPriorityTierSelectionState,
) openAIPriorityTierPoolOutcome {
	pool = openAIPriorityTierWithout(pool, st.demoted)
	if len(pool) == 0 {
		return openAIPriorityTierPoolOutcome{}
	}
	inner := openAIPriorityTierInnerRequest(req, pool)

	// 1) 首选：原 top-K 逻辑（含内部负载刷新重试）。
	attempt := s.trySelectByLoadBalancePool(ctx, inner, pool, loadMap, budget)
	if attempt.err != nil {
		if attempt.noCompactCandidates {
			return openAIPriorityTierPoolOutcome{noCompactCandidates: true}
		}
		return openAIPriorityTierPoolOutcome{err: attempt.err}
	}
	if attempt.result != nil && st.acceptTierSelection(attempt.result, tierPriority) {
		return openAIPriorityTierPoolOutcome{result: attempt.result, attempt: &attempt}
	}

	// 2) 补试：整池只读一次新鲜负载、生成一次顺序，然后逐个候选抢槽；
	//    档位复核在候选循环内做，被降档的账号释放后继续下一个，不重刷负载。
	var retryOrder []openAIAccountCandidateScore
	compactBlocked := attempt.compactBlocked
	if rest := openAIPriorityTierWithout(pool, st.demoted); len(rest) > 0 && !budget.acquireExhausted() {
		freshMap := map[int64]*AccountLoadInfo{}
		if s.service.concurrencyService != nil {
			if fresh, err := s.service.concurrencyService.GetAccountsLoadBatchFresh(ctx, buildOpenAIAccountLoadRequest(rest)); err == nil && fresh != nil {
				freshMap = fresh
			}
		}
		plan := s.buildOpenAIAccountLoadPlan(ctx, inner, rest, freshMap)
		if len(plan.allCandidates) > 0 {
			plan.topK = len(plan.allCandidates)
			plan.selectionOrder = s.buildOpenAISelectionOrder(inner, plan)
		}
		retryOrder = plan.selectionOrder
		for i := range retryOrder {
			candidate := retryOrder[i]
			if candidate.account == nil {
				continue
			}
			if _, demoted := st.demoted[candidate.account.ID]; demoted {
				continue
			}
			result, blocked, err := s.tryAcquireOpenAISelectionOrderWithBudget(ctx, inner, retryOrder[i:i+1], budget)
			compactBlocked = compactBlocked || blocked
			if err != nil {
				return openAIPriorityTierPoolOutcome{err: err}
			}
			if result != nil {
				if st.acceptTierSelection(result, tierPriority) {
					return openAIPriorityTierPoolOutcome{result: result, attempt: &attempt}
				}
				continue
			}
			if budget.acquireExhausted() {
				break
			}
		}
	}

	// 3) 供排队阶段使用的全池 attempt。
	queue := attempt
	queue.result = nil
	queue.err = nil
	queue.compactBlocked = compactBlocked
	queue.selectionOrder = mergeOpenAIPriorityTierQueueOrder(attempt.selectionOrder, retryOrder, st.demoted, req.RequireCompact)
	return openAIPriorityTierPoolOutcome{queueAttempt: &queue}
}

type openAIPriorityTierQueueEntry struct {
	tierPriority int
	pool         []*Account
	regular      bool
	attempt      *openAIAccountLoadSelectionAttempt
}

// selectByPriorityTiers 分档选号入口（挂点：selectByLoadBalance，开关开启时）。
func (s *defaultOpenAIAccountScheduler) selectByPriorityTiers(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	filtered []*Account,
	loadMap map[int64]*AccountLoadInfo,
	budget *openAISelectionProbeBudget,
	filterStats openAISelectionFilterStats,
) (*AccountSelectionResult, int, int, float64, error) {
	// 0) 探测上限按整组口径判定，所有档/池共用同一个 budget。
	if wholePlan := s.buildOpenAIAccountLoadPlan(ctx, req, filtered, loadMap); openAICostOverflowExpanded(req, wholePlan) {
		budget.enableLimit()
	}

	st := &openAIPriorityTierSelectionState{}
	accept := func(selection *AccountSelectionResult, attempt *openAIAccountLoadSelectionAttempt) (*AccountSelectionResult, int, int, float64, error) {
		if selection.Acquired && req.SessionHash != "" && !req.PreserveStickyBinding {
			_ = s.service.bindOpenAIStickySessionDuringSelection(ctx, req.GroupID, req.SessionHash, selection.Account.ID)
		}
		if attempt == nil {
			return selection, len(filtered), 0, 0, nil
		}
		return selection, attempt.candidateCount, attempt.topK, attempt.loadSkew, nil
	}

	tiers := splitOpenAIPriorityTiers(filtered, nil)
	queue := make([]openAIPriorityTierQueueEntry, 0, len(tiers)*2)
	sawNoCompact := false
	exhausted := false

	// 4) 抢槽阶段：逐档、档内订阅池→普通池。
	for _, tier := range tiers {
		if exhausted {
			break
		}
		type tierPool struct {
			accounts []*Account
			regular  bool
		}
		pools := []tierPool{{accounts: tier.accounts, regular: true}}
		if req.SubscriptionPriority {
			subscription, regular := partitionOpenAIChatGPTSubscriptionAccounts(tier.accounts)
			pools = pools[:0]
			if len(subscription) > 0 {
				pools = append(pools, tierPool{accounts: subscription})
			}
			if len(regular) > 0 {
				pools = append(pools, tierPool{accounts: regular, regular: true})
			}
		}
		tierQueue := make([]openAIPriorityTierQueueEntry, 0, len(pools))
		for _, pool := range pools {
			outcome := s.acquireFromPriorityTierPool(ctx, req, pool.accounts, tier.priority, loadMap, budget, st)
			if outcome.err != nil {
				return nil, 0, 0, 0, outcome.err
			}
			if outcome.result != nil {
				return accept(outcome.result, outcome.attempt)
			}
			if outcome.noCompactCandidates {
				sawNoCompact = true
			}
			if outcome.queueAttempt != nil {
				tierQueue = append(tierQueue, openAIPriorityTierQueueEntry{tierPriority: tier.priority, pool: pool.accounts, regular: pool.regular, attempt: outcome.queueAttempt})
			}
			if budget.acquireExhausted() {
				exhausted = true
				break
			}
		}
		// 档内排队顺序与原逻辑一致：普通池先、订阅池后。
		sort.SliceStable(tierQueue, func(i, j int) bool {
			return tierQueue[i].regular && !tierQueue[j].regular
		})
		queue = append(queue, tierQueue...)
	}

	// 5) 排队阶段：按档从优到劣，取第一个能给出结果的 attempt。
	var lastErr error
	var lastAttempt *openAIAccountLoadSelectionAttempt
	var queueFullBackup *AccountSelectionResult
	var queueFullBackupAttempt *openAIAccountLoadSelectionAttempt
	for _, entry := range queue {
		pool := openAIPriorityTierWithout(entry.pool, st.demoted)
		if len(pool) == 0 {
			continue
		}
		inner := openAIPriorityTierInnerRequest(req, pool)
		attempt := *entry.attempt
		attempt.selectionOrder = mergeOpenAIPriorityTierQueueOrder(attempt.selectionOrder, nil, st.demoted, false)
		lastAttempt = entry.attempt
		selection, queueFull, err := s.finishPriorityTierFallback(ctx, inner, attempt, budget, filterStats, entry.tierPriority, st)
		if err != nil {
			if errors.Is(err, ErrNoAvailableAccounts) || errors.Is(err, ErrNoAvailableCompactAccounts) {
				lastErr = err
				continue
			}
			return nil, attempt.candidateCount, attempt.topK, attempt.loadSkew, err
		}
		if selection == nil || selection.Account == nil {
			continue
		}
		if queueFull {
			// 本档唯一能排队的号队列已满：先看低档有没有能入队的，没有再回来用它。
			if queueFullBackup == nil {
				queueFullBackup, queueFullBackupAttempt = selection, entry.attempt
			}
			continue
		}
		return accept(selection, entry.attempt)
	}
	if queueFullBackup != nil {
		return accept(queueFullBackup, queueFullBackupAttempt)
	}

	// 6) 都没有。
	if lastAttempt == nil {
		if sawNoCompact {
			return nil, len(filtered), 0, 0, ErrNoAvailableCompactAccounts
		}
		return nil, len(filtered), 0, 0, noAvailableOpenAISelectionError(req.RequestedModel, false, filterStats.summary("selection_order_empty"))
	}
	if lastErr == nil {
		lastErr = noAvailableOpenAISelectionError(req.RequestedModel, lastAttempt.compactBlocked, filterStats.summary("selection_order_exhausted"))
	}
	return nil, lastAttempt.candidateCount, lastAttempt.topK, lastAttempt.loadSkew, lastErr
}

// ---------------------------------------------------------------------------
// 会话粘连层：更优档抢占
// ---------------------------------------------------------------------------

// applyOpenAIPriorityTierToSticky 挂点：Select 的会话粘连分支拿到结果之后。
// 返回 replaced=false：保持原粘连结果；replaced=true 且 better=nil：丢弃粘连，
// 继续走（分档）负载均衡；replaced=true 且 better!=nil：改用更优档的结果。
func (s *defaultOpenAIAccountScheduler) applyOpenAIPriorityTierToSticky(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	selection *AccountSelectionResult,
) (bool, *AccountSelectionResult) {
	if s == nil || s.service == nil || selection == nil || selection.Account == nil {
		return false, nil
	}
	if !s.service.openAIPriorityTierActive(ctx, req) {
		return false, nil
	}
	// 粘连号已满（WaitPlan）：不占槽，直接交给分档负载均衡全量抢槽/排队。
	if !selection.Acquired {
		return true, nil
	}
	stickyPriority := openAIAccountSchedulingPriority(selection.Account)
	accounts, err := s.service.listSchedulableAccounts(ctx, req.GroupID, req.Platform)
	if err != nil {
		return false, nil
	}
	worseOrEqual := make(map[int64]struct{}, len(accounts))
	hasBetter := false
	for i := range accounts {
		if openAIAccountSchedulingPriority(&accounts[i]) < stickyPriority {
			hasBetter = true
			continue
		}
		worseOrEqual[accounts[i].ID] = struct{}{}
	}
	if !hasBetter {
		return false, nil
	}

	inner := req
	inner.PreserveStickyBinding = true
	inner.ExcludedIDs = cloneExcludedAccountIDs(req.ExcludedIDs)
	if inner.ExcludedIDs == nil {
		inner.ExcludedIDs = make(map[int64]struct{}, len(worseOrEqual))
	}
	for id := range worseOrEqual {
		inner.ExcludedIDs[id] = struct{}{}
	}
	inner.ExcludedIDs[selection.Account.ID] = struct{}{}

	better, _, _, _, lbErr := s.selectByLoadBalance(ctx, inner)
	if lbErr != nil || better == nil || better.Account == nil || !better.Acquired {
		// 更优档满或不可用：保留原粘连（WaitPlan 不占槽，无需释放）。
		return false, nil
	}
	if openAIAccountSchedulingPriority(better.Account) >= stickyPriority {
		// 两次读取之间优先级被调整：不换。
		releaseOpenAIPriorityTierSelection(better)
		return false, nil
	}
	releaseOpenAIPriorityTierSelection(selection)
	if req.SessionHash != "" && !req.PreserveStickyBinding {
		_ = s.service.bindOpenAIStickySessionDuringSelection(ctx, req.GroupID, req.SessionHash, better.Account.ID)
	}
	return true, better
}

// finishPriorityTierFallback 分档版排队计划：与原 finishLoadBalanceSelectionFallback
// 逐行等价（加权粘连重抢、预算分轮、新鲜复核、DB 复核预算、compact 判定、WaitPlan 字段），
// 只多两处：DB 复核后的账号优先级劣于本档 → 记为降档并继续下一个候选；负载已知
// 等待队列已满 → 记为备选并继续，单次遍历结束仍无更好的才返回备选（queueFull=true）。
// 一致性由 TestPriorityTier_FallbackMatchesOriginal 保证。
func (s *defaultOpenAIAccountScheduler) finishPriorityTierFallback(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	attempt openAIAccountLoadSelectionAttempt,
	budget *openAISelectionProbeBudget,
	filterStats openAISelectionFilterStats,
	tierPriority int,
	st *openAIPriorityTierSelectionState,
) (*AccountSelectionResult, bool, error) {
	if len(attempt.selectionOrder) == 0 {
		return nil, false, noAvailableOpenAISelectionError(req.RequestedModel, attempt.compactBlocked, filterStats.summary("selection_order_empty"))
	}

	cfg := s.service.schedulingConfig()
	compactBlocked := attempt.compactBlocked
	var backup *AccountSelectionResult

	if stickyFallback, stickyErr := s.tryFallbackToWeightedSticky(ctx, req); stickyErr != nil {
		return nil, false, stickyErr
	} else if stickyFallback != nil {
		if st.acceptTierSelection(stickyFallback, tierPriority) {
			if stickyFallback.Acquired || !openAIPriorityTierWaitQueueKnownFull(stickyFallback, attempt.selectionOrder) {
				return stickyFallback, false, nil
			}
			// 粘连号的等待队列已知满：留作备选，继续找能入队的候选。
			backup = stickyFallback
		}
	}

	passes := 1
	if budget != nil && budget.limited {
		passes = 4
	}
	for pass := 0; pass < passes; pass++ {
		wantAttempted := pass == 1 || pass == 3
		wantKnownFull := pass >= 2
		for _, candidate := range attempt.selectionOrder {
			if candidate.account == nil {
				continue
			}
			if _, demoted := st.demoted[candidate.account.ID]; demoted {
				continue
			}
			if budget != nil && budget.limited {
				knownFull := candidate.loadKnown && candidate.account.Concurrency > 0 &&
					candidate.loadInfo.CurrentConcurrency >= candidate.account.Concurrency
				if budget.wasAttempted(candidate.account.ID) != wantAttempted || knownFull != wantKnownFull {
					continue
				}
			}
			fresh := s.service.resolveFreshSchedulableOpenAIAccount(ctx, candidate.account, req.Platform, req.RequestedModel, false, req.RequiredCapability)
			if fresh == nil || !s.isAccountTransportCompatible(fresh, req.RequiredTransport) || !s.isAccountRequestCompatible(ctx, fresh, req) {
				continue
			}
			if !s.consumeOpenAISelectionDBRecheck(budget) {
				if backup != nil {
					return backup, true, nil
				}
				return nil, false, noAvailableOpenAISelectionError(req.RequestedModel, compactBlocked, filterStats.summary("selection_order_exhausted"))
			}
			fresh = s.service.recheckSelectedOpenAIAccountFromDB(ctx, fresh, req.GroupID, req.Platform, req.RequestedModel, false, req.RequiredCapability)
			if fresh == nil || !s.isAccountTransportCompatible(fresh, req.RequiredTransport) || !s.isAccountRequestCompatible(ctx, fresh, req) {
				continue
			}
			if req.RequireCompact && openAICompactSupportTier(fresh) == 0 {
				compactBlocked = true
				continue
			}
			if openAIAccountSchedulingPriority(fresh) > tierPriority {
				if st.demoted == nil {
					st.demoted = make(map[int64]struct{})
				}
				st.demoted[fresh.ID] = struct{}{}
				continue
			}
			plan := attachSelectionProfitGate(ctx, &AccountSelectionResult{
				Account: fresh,
				WaitPlan: &AccountWaitPlan{
					AccountID:      fresh.ID,
					MaxConcurrency: fresh.Concurrency,
					Timeout:        cfg.FallbackWaitTimeout,
					MaxWaiting:     cfg.FallbackMaxWaiting,
					Kind:           AccountWaitKindFallback,
				},
			})
			if cfg.FallbackMaxWaiting > 0 && candidate.loadKnown && candidate.loadInfo != nil &&
				candidate.loadInfo.WaitingCount >= cfg.FallbackMaxWaiting {
				if backup == nil {
					backup = plan
				}
				continue
			}
			return plan, false, nil
		}
	}
	if backup != nil {
		return backup, true, nil
	}
	return nil, false, noAvailableOpenAISelectionError(req.RequestedModel, compactBlocked, filterStats.summary("selection_order_exhausted"))
}

// openAIPriorityTierWaitQueueKnownFull 依据候选里已有的负载数据判断 WaitPlan 对应账号的
// 等待队列是否已满；负载未知时返回 false（按原逻辑直接使用该计划）。
func openAIPriorityTierWaitQueueKnownFull(selection *AccountSelectionResult, order []openAIAccountCandidateScore) bool {
	if selection == nil || selection.WaitPlan == nil || selection.WaitPlan.MaxWaiting <= 0 {
		return false
	}
	for _, candidate := range order {
		if candidate.account == nil || candidate.account.ID != selection.WaitPlan.AccountID {
			continue
		}
		return candidate.loadKnown && candidate.loadInfo != nil && candidate.loadInfo.WaitingCount >= selection.WaitPlan.MaxWaiting
	}
	return false
}
