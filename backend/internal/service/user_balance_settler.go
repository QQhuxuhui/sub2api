package service

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"
)

const (
	userBalanceSettleInterval    = 200 * time.Millisecond
	userBalanceSettleUserLimit   = 200
	userBalanceSettleBatchSize   = 500
	userBalanceSettleRoundBudget = 2 * time.Second
	userBalanceSettleUserTimeout = 3 * time.Second
	userBalanceSettleListTimeout = 2 * time.Second
	userBalanceSettleBusyBackoff = time.Second
	userBalanceSettleMinBackoff  = time.Second
	userBalanceSettleMaxBackoff  = 30 * time.Second
	userBalanceSettleDrainBudget = 5 * time.Second

	// The window has to outlast the balance cache TTL of about 5 minutes: a key
	// that went wrong just before the last deduction can live that long.
	userBalanceReconcileInterval = 15 * time.Second
	userBalanceReconcileWindow   = 6 * time.Minute
	userBalanceReconcileTimeout  = 5 * time.Second

	userBalanceStatsInterval  = time.Minute
	userBalanceStatsAlertAge  = 60 * time.Second
	userBalanceStatsTimeout   = 3 * time.Second
	userBalanceNotifyTimeout  = 3 * time.Second
	userBalanceStartupTimeout = 10 * time.Second
)

// ErrUserBalanceSettleBusy reports that the user row was locked. It is not a
// failure: the row's holder finishes soon and the user is retried.
var ErrUserBalanceSettleBusy = errors.New("user balance row is locked")

// UserBalanceSettlement describes one committed batch. OldBalance and NewBalance
// are users.balance around the batch.
type UserBalanceSettlement struct {
	UserID     int64
	Entries    int
	Amount     float64
	OldBalance float64
	NewBalance float64
}

// UserBalancePendingStats describes the backlog.
type UserBalancePendingStats struct {
	Entries          int64
	OldestAgeSeconds float64
}

// UserBalancePendingRepository stores deferred balance deductions.
type UserBalancePendingRepository interface {
	// ListUsers returns users with pending rows, oldest row first, leaving out
	// excludeUserIDs so they do not take up the limit.
	ListUsers(ctx context.Context, excludeUserIDs []int64, limit int) ([]int64, error)
	ListAllUsers(ctx context.Context) ([]int64, error)
	// SettleUser moves up to batchSize of the user's oldest rows into
	// users.balance in one transaction. It returns ErrUserBalanceSettleBusy when
	// the user row is locked and ErrUserNotFound when it does not exist.
	SettleUser(ctx context.Context, userID int64, batchSize int) (*UserBalanceSettlement, error)
	Stats(ctx context.Context) (UserBalancePendingStats, error)
}

// UserBalanceDeferral is what usage billing needs from the settler.
type UserBalanceDeferral interface {
	ShouldDeferBalance(ctx context.Context, userID int64) bool
	MarkBalanceDirty(userID int64)
}

type userBalanceUserLoader interface {
	GetByID(ctx context.Context, id int64) (*User, error)
}

type userBalanceCache interface {
	InvalidateUserBalance(ctx context.Context, userID int64) error
	PurgeUserBalances(ctx context.Context) error
}

type userBalanceLowChecker interface {
	CheckBalanceAfterDeduction(ctx context.Context, user *User, oldBalance, cost float64)
}

type userBalanceWriteMode interface {
	ShouldDefer(ctx context.Context, userID int64) bool
	Mode(ctx context.Context) string
	Refresh(ctx context.Context) (string, error)
}

type userBalanceBackoff struct {
	until    time.Time
	failures int
}

type userBalanceDirtyEntry struct {
	lastActivity time.Time
	// retry keeps a user past the window while its last invalidation failed.
	retry bool
}

// UserBalanceRoundResult reports one settlement round.
type UserBalanceRoundResult struct {
	Attempted int
	Settled   int
	// More means a batch came back full, so the next round should start at once.
	More bool
}

// UserBalanceSettler aggregates user_balance_pending rows into users.balance. No
// leader is needed: the user row lock serializes settlers and rows are deleted in
// the settling transaction, so two processes during a rolling release can not
// settle a row twice.
//
// Cache correction is deliberately independent of whether pending is empty or the
// settlement worked: a deferred request marks the user dirty, and every 15 seconds
// each user active in the last 6 minutes loses its cached balance, so the next
// check reloads the effective balance. That repairs a lost QueueDeductBalance, a
// deduction queued before a reload and applied after it, and a failed Redis
// write, also when no further deduction ever arrives.
type UserBalanceSettler struct {
	repo     UserBalancePendingRepository
	users    userBalanceUserLoader
	cache    userBalanceCache
	notifier userBalanceLowChecker
	mode     userBalanceWriteMode
	now      func() time.Time

	dirtyMu sync.Mutex
	dirty   map[int64]*userBalanceDirtyEntry

	// Owned by whoever runs rounds: the loop, then Stop after the loop ended.
	backoff       map[int64]*userBalanceBackoff
	carryover     []int64
	nextReconcile time.Time
	nextStats     time.Time
	backlogMarked bool
	cachesPurged  bool
	settledUsers  int64
	settledRows   int64

	lifecycleMu sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
	stopped     bool
}

// NewUserBalanceSettler builds a settler. Start runs it.
func NewUserBalanceSettler(
	repo UserBalancePendingRepository,
	users UserRepository,
	cache *BillingCacheService,
	notifier *BalanceNotifyService,
	mode *BillingBalanceWriteModeService,
) *UserBalanceSettler {
	return NewUserBalanceSettlerWithClock(repo, users, cache, notifier, mode, time.Now)
}

// NewUserBalanceSettlerWithClock is NewUserBalanceSettler with a controllable
// clock, for tests that drive rounds through Startup and Tick.
func NewUserBalanceSettlerWithClock(
	repo UserBalancePendingRepository,
	users UserRepository,
	cache *BillingCacheService,
	notifier *BalanceNotifyService,
	mode *BillingBalanceWriteModeService,
	now func() time.Time,
) *UserBalanceSettler {
	var checker userBalanceLowChecker
	if notifier != nil {
		checker = notifier
	}
	return newUserBalanceSettler(repo, users, cache, checker, mode, now)
}

func newUserBalanceSettler(
	repo UserBalancePendingRepository,
	users userBalanceUserLoader,
	cache userBalanceCache,
	notifier userBalanceLowChecker,
	mode userBalanceWriteMode,
	now func() time.Time,
) *UserBalanceSettler {
	return &UserBalanceSettler{
		repo:     repo,
		users:    users,
		cache:    cache,
		notifier: notifier,
		mode:     mode,
		now:      now,
		dirty:    make(map[int64]*userBalanceDirtyEntry),
		backoff:  make(map[int64]*userBalanceBackoff),
	}
}

// ShouldDeferBalance implements UserBalanceDeferral.
func (s *UserBalanceSettler) ShouldDeferBalance(ctx context.Context, userID int64) bool {
	return s.mode.ShouldDefer(ctx, userID)
}

// MarkBalanceDirty records activity. It does not wait for settlement, so a user
// whose settlement keeps failing is still corrected.
func (s *UserBalanceSettler) MarkBalanceDirty(userID int64) {
	now := s.now()
	s.dirtyMu.Lock()
	defer s.dirtyMu.Unlock()
	entry := s.dirty[userID]
	if entry == nil {
		entry = &userBalanceDirtyEntry{}
		s.dirty[userID] = entry
	}
	entry.lastActivity = now
}

// Start runs the settlement loop until Stop.
func (s *UserBalanceSettler) Start() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.cancel != nil || s.stopped {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	go s.run(ctx, s.done)
}

// Stop ends the loop and drains for at most 5 seconds. What is left waits for the
// next start: committed rows are not lost.
func (s *UserBalanceSettler) Stop() {
	s.lifecycleMu.Lock()
	cancel, done, stopped := s.cancel, s.done, s.stopped
	s.stopped = true
	s.lifecycleMu.Unlock()
	if cancel == nil || stopped {
		return
	}
	cancel()

	ctx, drainCancel := context.WithTimeout(context.Background(), userBalanceSettleDrainBudget)
	defer drainCancel()
	select {
	case <-done:
	case <-ctx.Done():
		slog.Error("billing.balance_settler_stop_timeout")
		return
	}
	s.Drain(ctx)
}

// Drain runs rounds until no user can be settled or ctx ends. Backoffs are
// dropped first, so every user gets a last attempt.
func (s *UserBalanceSettler) Drain(ctx context.Context) {
	s.backoff = make(map[int64]*userBalanceBackoff)
	for ctx.Err() == nil {
		result, err := s.RunRound(ctx)
		if err != nil {
			slog.Warn("billing.balance_settler_drain_failed", "error", err)
			return
		}
		if result.Attempted == 0 {
			return
		}
	}
}

func (s *UserBalanceSettler) run(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	s.guard("startup", func() { s.Startup(ctx) })

	ticker := time.NewTicker(userBalanceSettleInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		more := false
		s.guard("tick", func() { more = s.Tick(ctx) })
		if more {
			continue
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

// guard keeps a panic from taking the process down with the settler.
func (s *UserBalanceSettler) guard(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("billing.balance_settler_panic", "step", name, "recover", r)
		}
	}()
	fn()
}

// Startup marks the backlog dirty and, when not in sync mode, clears every cached
// balance: the dirty set did not survive the restart, and neither did queued
// cache deductions. Whatever fails here is retried by the reconcile timer.
func (s *UserBalanceSettler) Startup(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, userBalanceStartupTimeout)
	defer cancel()

	if !s.backlogMarked {
		users, err := s.repo.ListAllUsers(ctx)
		if err != nil {
			slog.Error("billing.balance_settler_backlog_load_failed", "error", err)
		} else {
			for _, userID := range users {
				s.MarkBalanceDirty(userID)
			}
			s.backlogMarked = true
		}
	}
	if !s.cachesPurged {
		// The mode has to come from a successful read: the sync fallback after a
		// failed read says nothing about what the previous process was doing.
		mode, err := s.mode.Refresh(ctx)
		if err != nil {
			slog.Error("billing.balance_settler_mode_load_failed", "error", err)
			return
		}
		if mode != BillingBalanceWriteModeSync {
			if err := s.cache.PurgeUserBalances(ctx); err != nil {
				slog.Error("billing.balance_settler_cache_purge_failed", "error", err)
				return
			}
		}
		s.cachesPurged = true
	}
}

// Tick runs one round and whatever timer is due. It reports whether the next
// round should start immediately.
func (s *UserBalanceSettler) Tick(ctx context.Context) bool {
	result, err := s.RunRound(ctx)
	if err != nil && ctx.Err() == nil {
		slog.Error("billing.balance_settler_round_failed", "error", err)
	}

	now := s.now()
	if !now.Before(s.nextReconcile) {
		s.nextReconcile = now.Add(userBalanceReconcileInterval)
		s.Startup(ctx)
		// Keeps the snapshot, and the mode change log, fresh without traffic.
		s.mode.Mode(ctx)
		s.reconcileDirtyUsers(ctx)
	}
	if !now.Before(s.nextStats) {
		first := s.nextStats.IsZero()
		s.nextStats = now.Add(userBalanceStatsInterval)
		if !first {
			s.logStats(ctx)
		}
	}
	return result.More
}

// RunRound settles one round: users left over from the last round, then users
// whose retry is due, then the oldest users outside backoff. Leftovers go first
// so that users failing slowly can not use up every round's budget and starve
// the users behind them.
func (s *UserBalanceSettler) RunRound(ctx context.Context) (UserBalanceRoundResult, error) {
	var result UserBalanceRoundResult
	worklist, exclude := s.planRound(s.now())
	listCtx, cancel := context.WithTimeout(ctx, userBalanceSettleListTimeout)
	listed, listErr := s.repo.ListUsers(listCtx, exclude, userBalanceSettleUserLimit)
	cancel()
	worklist = append(worklist, listed...)

	// The budget starts after the listing: when the database is slow, a listing
	// that eats the budget would otherwise leave every round with nothing done.
	deadline := s.now().Add(userBalanceSettleRoundBudget)
	s.carryover = nil
	for i, userID := range worklist {
		if ctx.Err() != nil || !s.now().Before(deadline) {
			s.carryover = append(s.carryover, worklist[i:]...)
			break
		}
		result.Attempted++
		if s.settleUser(ctx, userID, &result) {
			result.Settled++
		}
	}
	return result, listErr
}

// planRound returns the users owed a turn and the users ListUsers must leave out:
// those users, so they are not counted twice, and everyone still backing off.
func (s *UserBalanceSettler) planRound(now time.Time) (worklist, exclude []int64) {
	seen := make(map[int64]struct{}, len(s.carryover)+len(s.backoff))
	exclude = make([]int64, 0, len(s.carryover)+len(s.backoff))
	for _, userID := range s.carryover {
		if _, dup := seen[userID]; dup {
			continue
		}
		seen[userID] = struct{}{}
		worklist = append(worklist, userID)
		exclude = append(exclude, userID)
	}

	due := make([]int64, 0, len(s.backoff))
	for userID, backoff := range s.backoff {
		if _, dup := seen[userID]; dup {
			continue
		}
		seen[userID] = struct{}{}
		exclude = append(exclude, userID)
		if !now.Before(backoff.until) {
			due = append(due, userID)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		a, b := s.backoff[due[i]], s.backoff[due[j]]
		if !a.until.Equal(b.until) {
			return a.until.Before(b.until)
		}
		return due[i] < due[j]
	})
	return append(worklist, due...), exclude
}

func (s *UserBalanceSettler) settleUser(ctx context.Context, userID int64, result *UserBalanceRoundResult) bool {
	settleCtx, cancel := context.WithTimeout(ctx, userBalanceSettleUserTimeout)
	settlement, err := s.repo.SettleUser(settleCtx, userID, userBalanceSettleBatchSize)
	cancel()

	switch {
	case err == nil:
		delete(s.backoff, userID)
	case errors.Is(err, ErrUserBalanceSettleBusy):
		s.backOffBusy(userID)
		return false
	case errors.Is(err, ErrUserNotFound):
		slog.Error("billing.balance_settle_user_missing", "user_id", userID)
		s.backOffFailure(userID)
		return false
	case ctx.Err() != nil:
		return false
	default:
		slog.Error("billing.balance_settle_failed", "user_id", userID, "error", err)
		s.backOffFailure(userID)
		return false
	}

	if settlement == nil || settlement.Entries == 0 {
		return false
	}
	s.settledUsers++
	s.settledRows += int64(settlement.Entries)
	if settlement.Entries >= userBalanceSettleBatchSize {
		result.More = true
	}
	s.MarkBalanceDirty(userID)
	s.notifyAfterSettlement(ctx, settlement)
	return true
}

func (s *UserBalanceSettler) backOffBusy(userID int64) {
	entry := s.backoff[userID]
	if entry == nil {
		entry = &userBalanceBackoff{}
		s.backoff[userID] = entry
	}
	entry.until = s.now().Add(userBalanceSettleBusyBackoff)
}

func (s *UserBalanceSettler) backOffFailure(userID int64) {
	entry := s.backoff[userID]
	if entry == nil {
		entry = &userBalanceBackoff{}
		s.backoff[userID] = entry
	}
	delay := userBalanceSettleMaxBackoff
	if entry.failures < 5 { // 1s << 5 is past the 30s cap.
		delay = userBalanceSettleMinBackoff << entry.failures
	}
	entry.failures++
	entry.until = s.now().Add(delay)
}

// notifyAfterSettlement checks the low balance threshold with users.balance around
// the batch, so several small deductions that cross it together still notify. The
// balance only falls here, which keeps it to one notification per crossing.
func (s *UserBalanceSettler) notifyAfterSettlement(ctx context.Context, settlement *UserBalanceSettlement) {
	if s.notifier == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in balance settlement notification", "recover", r)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, userBalanceNotifyTimeout)
	defer cancel()
	user, err := s.users.GetByID(ctx, settlement.UserID)
	if err != nil {
		slog.Debug("billing.balance_settle_notify_skipped", "user_id", settlement.UserID, "error", err)
		return
	}
	s.notifier.CheckBalanceAfterDeduction(ctx, user, settlement.OldBalance, settlement.Amount)
}

func (s *UserBalanceSettler) reconcileDirtyUsers(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, userBalanceReconcileTimeout)
	defer cancel()

	now := s.now()
	type target struct {
		userID       int64
		lastActivity time.Time
		expired      bool
	}
	s.dirtyMu.Lock()
	targets := make([]target, 0, len(s.dirty))
	for userID, entry := range s.dirty {
		expired := now.Sub(entry.lastActivity) > userBalanceReconcileWindow
		if expired && !entry.retry {
			delete(s.dirty, userID)
			continue
		}
		targets = append(targets, target{userID, entry.lastActivity, expired})
	}
	s.dirtyMu.Unlock()

	for _, t := range targets {
		err := s.cache.InvalidateUserBalance(ctx, t.userID)
		if err != nil {
			slog.Warn("billing.balance_reconcile_failed", "user_id", t.userID, "error", err)
		}
		s.dirtyMu.Lock()
		if entry := s.dirty[t.userID]; entry != nil {
			entry.retry = err != nil
			if err == nil && t.expired && entry.lastActivity.Equal(t.lastActivity) {
				delete(s.dirty, t.userID)
			}
		}
		s.dirtyMu.Unlock()
	}
}

func (s *UserBalanceSettler) logStats(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, userBalanceStatsTimeout)
	defer cancel()
	stats, err := s.repo.Stats(ctx)
	if err != nil {
		slog.Warn("billing.balance_settler_stats_failed", "error", err)
		return
	}
	attrs := []any{
		"settled_users", s.settledUsers,
		"settled_entries", s.settledRows,
		"backlog_entries", stats.Entries,
		"oldest_age_seconds", stats.OldestAgeSeconds,
	}
	s.settledUsers, s.settledRows = 0, 0
	if stats.OldestAgeSeconds > userBalanceStatsAlertAge.Seconds() {
		slog.Error("billing.balance_settler_backlog_old", attrs...)
		return
	}
	slog.Info("billing.balance_settler_stats", attrs...)
}
