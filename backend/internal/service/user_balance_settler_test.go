//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type settlerClock struct {
	mu  sync.Mutex
	now time.Time
}

func newSettlerClock() *settlerClock { return &settlerClock{now: time.Unix(1_000_000, 0)} }

func (c *settlerClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *settlerClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type settlerRepoFake struct {
	beforeList func(context.Context) error
	mu         sync.Mutex
	pending    map[int64]int // user -> rows
	order      []int64       // oldest first
	settle     func(userID int64) (*UserBalanceSettlement, error)
	excludes   [][]int64
	settled    []int64
	stats      UserBalancePendingStats
	allErr     error
}

func (r *settlerRepoFake) ListUsers(ctx context.Context, exclude []int64, limit int) ([]int64, error) {
	if r.beforeList != nil {
		if err := r.beforeList(ctx); err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.excludes = append(r.excludes, append([]int64{}, exclude...))
	skip := make(map[int64]bool, len(exclude))
	for _, id := range exclude {
		skip[id] = true
	}
	var out []int64
	for _, id := range r.order {
		if len(out) == limit {
			break
		}
		if !skip[id] && r.pending[id] > 0 {
			out = append(out, id)
		}
	}
	return out, nil
}

func (r *settlerRepoFake) ListAllUsers(context.Context) ([]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.allErr != nil {
		return nil, r.allErr
	}
	return append([]int64{}, r.order...), nil
}

func (r *settlerRepoFake) SettleUser(_ context.Context, userID int64, batchSize int) (*UserBalanceSettlement, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settled = append(r.settled, userID)
	if r.settle != nil {
		if settlement, err := r.settle(userID); settlement != nil || err != nil {
			return settlement, err
		}
	}
	rows := min(r.pending[userID], batchSize)
	r.pending[userID] -= rows
	return &UserBalanceSettlement{UserID: userID, Entries: rows, Amount: float64(rows), OldBalance: 100, NewBalance: 100 - float64(rows)}, nil
}

func (r *settlerRepoFake) Stats(context.Context) (UserBalancePendingStats, error) {
	return r.stats, nil
}

func (r *settlerRepoFake) takeSettled() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.settled
	r.settled = nil
	return out
}

func newSettlerRepoFake(users ...int64) *settlerRepoFake {
	repo := &settlerRepoFake{pending: map[int64]int{}, order: users}
	for _, id := range users {
		repo.pending[id] = 1
	}
	return repo
}

type settlerCacheFake struct {
	mu            sync.Mutex
	invalidated   []int64
	invalidateErr error
	purges        int
	purgeErr      error
}

func (c *settlerCacheFake) InvalidateUserBalance(_ context.Context, userID int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.invalidateErr != nil {
		return c.invalidateErr
	}
	c.invalidated = append(c.invalidated, userID)
	return nil
}

func (c *settlerCacheFake) PurgeUserBalances(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.purges++
	return c.purgeErr
}

func (c *settlerCacheFake) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.invalidated)
}

type settlerModeFake struct {
	mode string
	err  error
}

func (m *settlerModeFake) ShouldDefer(context.Context, int64) bool { return m.mode != "sync" }
func (m *settlerModeFake) Mode(context.Context) string             { return m.mode }
func (m *settlerModeFake) Refresh(context.Context) (string, error) { return m.mode, m.err }

type settlerUsersFake struct {
	err error
}

func (u *settlerUsersFake) GetByID(_ context.Context, id int64) (*User, error) {
	if u.err != nil {
		return nil, u.err
	}
	return &User{ID: id}, nil
}

type settlerNotifyFake struct {
	calls [][2]float64
	users []int64
}

func (n *settlerNotifyFake) CheckBalanceAfterDeduction(_ context.Context, user *User, oldBalance, cost float64) {
	n.users = append(n.users, user.ID)
	n.calls = append(n.calls, [2]float64{oldBalance, cost})
}

type settlerRig struct {
	settler *UserBalanceSettler
	repo    *settlerRepoFake
	cache   *settlerCacheFake
	notify  *settlerNotifyFake
	mode    *settlerModeFake
	clock   *settlerClock
}

func newSettlerRig(repo *settlerRepoFake) *settlerRig {
	rig := &settlerRig{
		repo:   repo,
		cache:  &settlerCacheFake{},
		notify: &settlerNotifyFake{},
		mode:   &settlerModeFake{mode: BillingBalanceWriteModeList},
		clock:  newSettlerClock(),
	}
	rig.settler = newUserBalanceSettler(repo, &settlerUsersFake{}, rig.cache, rig.notify, rig.mode, rig.clock.Now)
	return rig
}

func TestUserBalanceSettler_BackoffUsersAreExcludedFromTheListing(t *testing.T) {
	repo := newSettlerRepoFake(1, 2)
	repo.settle = func(userID int64) (*UserBalanceSettlement, error) {
		if userID == 1 {
			return nil, errors.New("settle failed")
		}
		return nil, nil
	}
	rig := newSettlerRig(repo)

	_, err := rig.settler.RunRound(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, repo.takeSettled())
	require.Empty(t, repo.excludes[0])

	repo.pending[3] = 1
	repo.order = append(repo.order, 3)
	rig.clock.Advance(userBalanceSettleInterval)
	_, err = rig.settler.RunRound(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int64{1}, repo.excludes[1], "a user backing off must not take a listing slot")
	require.Equal(t, []int64{3}, repo.takeSettled())
}

func TestUserBalanceSettler_ListingLimitAndBatchSize(t *testing.T) {
	var users []int64
	for id := int64(1); id <= userBalanceSettleUserLimit+5; id++ {
		users = append(users, id)
	}
	repo := newSettlerRepoFake(users...)
	rig := newSettlerRig(repo)

	result, err := rig.settler.RunRound(context.Background())
	require.NoError(t, err)
	require.Equal(t, userBalanceSettleUserLimit, result.Attempted)
	require.False(t, result.More)
}

func TestUserBalanceSettler_FullBatchRunsTheNextRoundAtOnce(t *testing.T) {
	repo := newSettlerRepoFake(1)
	repo.pending[1] = userBalanceSettleBatchSize + 1
	rig := newSettlerRig(repo)

	more := rig.settler.Tick(context.Background())
	require.True(t, more)
	require.Equal(t, 1, repo.pending[1])

	more = rig.settler.Tick(context.Background())
	require.False(t, more)
	require.Zero(t, repo.pending[1])
}

func TestUserBalanceSettler_FailureBackoffDoublesToThirtySeconds(t *testing.T) {
	repo := newSettlerRepoFake(1)
	repo.settle = func(int64) (*UserBalanceSettlement, error) { return nil, errors.New("settle failed") }
	rig := newSettlerRig(repo)

	for _, want := range []time.Duration{1, 2, 4, 8, 16, 30, 30, 30} {
		_, err := rig.settler.RunRound(context.Background())
		require.NoError(t, err)
		require.Equal(t, []int64{1}, repo.takeSettled())

		wait := want * time.Second
		rig.clock.Advance(wait - time.Millisecond)
		_, err = rig.settler.RunRound(context.Background())
		require.NoError(t, err)
		require.Empty(t, repo.takeSettled(), "still backing off before %s", wait)
		rig.clock.Advance(time.Millisecond)
	}
}

func TestUserBalanceSettler_BusyIsNotAFailure(t *testing.T) {
	repo := newSettlerRepoFake(1)
	failing := true
	repo.settle = func(int64) (*UserBalanceSettlement, error) {
		if failing {
			return nil, ErrUserBalanceSettleBusy
		}
		return nil, nil
	}
	rig := newSettlerRig(repo)

	for range 4 {
		_, err := rig.settler.RunRound(context.Background())
		require.NoError(t, err)
		require.Equal(t, []int64{1}, repo.takeSettled())
		rig.clock.Advance(userBalanceSettleBusyBackoff - time.Millisecond)
		_, err = rig.settler.RunRound(context.Background())
		require.NoError(t, err)
		require.Empty(t, repo.takeSettled())
		rig.clock.Advance(time.Millisecond)
	}

	failing = false
	_, err := rig.settler.RunRound(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int64{1}, repo.takeSettled())
	require.Empty(t, rig.settler.backoff)
}

func TestUserBalanceSettler_MissingUserBacksOffWithoutStoppingOthers(t *testing.T) {
	repo := newSettlerRepoFake(1, 2)
	repo.settle = func(userID int64) (*UserBalanceSettlement, error) {
		if userID == 1 {
			return nil, ErrUserNotFound
		}
		return nil, nil
	}
	rig := newSettlerRig(repo)

	result, err := rig.settler.RunRound(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, result.Attempted)
	require.Equal(t, 1, result.Settled)
	require.Contains(t, rig.settler.backoff, int64(1))
	require.Equal(t, 1, repo.pending[1], "the rows stay")
}

// Every settle of a slow user eats the budget. Without carrying the unprocessed
// users over, the same oldest users would fill every round.
func TestUserBalanceSettler_LeftoversGoBeforeRetriesAndNothingStarves(t *testing.T) {
	repo := newSettlerRepoFake(1, 2, 3, 4, 5, 6)
	rig := newSettlerRig(repo)
	repo.settle = func(userID int64) (*UserBalanceSettlement, error) {
		if userID <= 2 {
			rig.clock.Advance(time.Second)
			return nil, errors.New("slow failure")
		}
		return nil, nil
	}

	_, err := rig.settler.RunRound(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, repo.takeSettled(), "the budget is gone after two slow failures")
	require.Equal(t, []int64{3, 4, 5, 6}, rig.settler.carryover)

	// Both retries are due, but the users that never got a turn go first.
	rig.clock.Advance(2 * time.Second)
	_, err = rig.settler.RunRound(context.Background())
	require.NoError(t, err)
	settled := repo.takeSettled()
	require.Equal(t, []int64{3, 4, 5, 6}, settled[:4])
	require.ElementsMatch(t, []int64{1, 2}, settled[4:])
}

func TestUserBalanceSettler_NotifiesWithBalanceAroundTheBatch(t *testing.T) {
	repo := newSettlerRepoFake(1, 2)
	repo.settle = func(userID int64) (*UserBalanceSettlement, error) {
		if userID == 1 {
			return &UserBalanceSettlement{UserID: 1, Entries: 3, Amount: 2.5, OldBalance: 10, NewBalance: 7.5}, nil
		}
		return &UserBalanceSettlement{UserID: 2}, nil
	}
	rig := newSettlerRig(repo)

	_, err := rig.settler.RunRound(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int64{1}, rig.notify.users, "an empty batch moved no balance")
	require.Equal(t, [][2]float64{{10, 2.5}}, rig.notify.calls)
}

func TestUserBalanceSettler_NotifyFailureDoesNotAffectSettlement(t *testing.T) {
	repo := newSettlerRepoFake(1)
	rig := newSettlerRig(repo)
	rig.settler.users = &settlerUsersFake{err: ErrUserNotFound} // soft-deleted user

	result, err := rig.settler.RunRound(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Settled)
	require.Empty(t, rig.notify.calls)
}

func TestUserBalanceSettler_ReconcileRunsEveryFifteenSecondsHoweverManyBatches(t *testing.T) {
	repo := newSettlerRepoFake()
	rig := newSettlerRig(repo)
	ctx := context.Background()

	rig.settler.MarkBalanceDirty(1)
	rig.settler.Tick(ctx)
	require.Equal(t, 1, rig.cache.count())

	for range 50 {
		rig.settler.MarkBalanceDirty(1)
		rig.settler.Tick(ctx)
		rig.clock.Advance(userBalanceSettleInterval)
	}
	require.Equal(t, 1, rig.cache.count(), "10 seconds of batches stay inside one interval")

	rig.clock.Advance(5 * time.Second)
	rig.settler.Tick(ctx)
	require.Equal(t, 2, rig.cache.count())
}

func TestUserBalanceSettler_ReconcileStopsSixMinutesAfterTheLastActivity(t *testing.T) {
	repo := newSettlerRepoFake()
	rig := newSettlerRig(repo)
	ctx := context.Background()

	rig.settler.MarkBalanceDirty(1)
	rig.settler.Tick(ctx)
	rig.clock.Advance(userBalanceReconcileWindow)
	rig.settler.Tick(ctx)
	require.Equal(t, 2, rig.cache.count(), "still inside the window at 6 minutes")

	rig.clock.Advance(userBalanceReconcileInterval)
	rig.settler.Tick(ctx)
	require.Equal(t, 2, rig.cache.count())
	require.Empty(t, rig.settler.dirty)
}

func TestUserBalanceSettler_SettlementRefreshesTheReconcileWindow(t *testing.T) {
	repo := newSettlerRepoFake()
	rig := newSettlerRig(repo)
	ctx := context.Background()

	rig.settler.MarkBalanceDirty(1)
	rig.settler.Tick(ctx)
	rig.clock.Advance(5 * time.Minute)

	repo.pending[1] = 1
	repo.order = []int64{1}
	rig.settler.Tick(ctx)

	rig.clock.Advance(5 * time.Minute)
	rig.settler.Tick(ctx)
	require.Contains(t, rig.settler.dirty, int64(1))
}

func TestUserBalanceSettler_DirtyDoesNotDependOnSettlement(t *testing.T) {
	repo := newSettlerRepoFake(1)
	repo.settle = func(int64) (*UserBalanceSettlement, error) { return nil, errors.New("settle failed") }
	rig := newSettlerRig(repo)

	rig.settler.MarkBalanceDirty(1)
	rig.settler.Tick(context.Background())
	require.Equal(t, []int64{1}, rig.cache.invalidated)
}

func TestUserBalanceSettler_FailedInvalidationIsRetriedPastTheWindow(t *testing.T) {
	repo := newSettlerRepoFake()
	rig := newSettlerRig(repo)
	ctx := context.Background()

	rig.settler.MarkBalanceDirty(1)
	rig.settler.Tick(ctx)
	require.Equal(t, 1, rig.cache.count())

	rig.cache.invalidateErr = errors.New("redis down")
	rig.clock.Advance(userBalanceReconcileWindow)
	rig.settler.Tick(ctx)

	rig.clock.Advance(userBalanceReconcileInterval)
	rig.settler.Tick(ctx)
	require.Contains(t, rig.settler.dirty, int64(1), "the last invalidation failed, so the user stays")

	rig.cache.invalidateErr = nil
	rig.clock.Advance(userBalanceReconcileInterval)
	rig.settler.Tick(ctx)
	require.Equal(t, 2, rig.cache.count())
	require.Empty(t, rig.settler.dirty)
}

func TestUserBalanceSettler_StartupMarksBacklogAndPurgesCache(t *testing.T) {
	for _, tc := range []struct {
		mode       string
		wantPurges int
	}{
		{mode: BillingBalanceWriteModeSync},
		{mode: BillingBalanceWriteModeList, wantPurges: 1},
		{mode: BillingBalanceWriteModeAll, wantPurges: 1},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			rig := newSettlerRig(newSettlerRepoFake(1, 2))
			rig.mode.mode = tc.mode

			rig.settler.Startup(context.Background())
			require.Equal(t, tc.wantPurges, rig.cache.purges)
			require.Len(t, rig.settler.dirty, 2)

			rig.settler.Startup(context.Background())
			require.Equal(t, tc.wantPurges, rig.cache.purges, "only once")
		})
	}
}

func TestUserBalanceSettler_StartupRetriesWhatFailed(t *testing.T) {
	ctx := context.Background()
	rig := newSettlerRig(newSettlerRepoFake(1))
	rig.repo.allErr = errors.New("db down")
	rig.mode.mode = BillingBalanceWriteModeSync
	rig.mode.err = errors.New("db down")

	rig.settler.Startup(ctx)
	require.Empty(t, rig.settler.dirty)
	require.Zero(t, rig.cache.purges, "a failed read says nothing about the mode")

	rig.repo.allErr = nil
	rig.mode.mode = BillingBalanceWriteModeList
	rig.mode.err = nil
	rig.cache.purgeErr = errors.New("redis down")
	rig.settler.Startup(ctx)
	require.Contains(t, rig.settler.dirty, int64(1))
	require.Equal(t, 1, rig.cache.purges)

	rig.cache.purgeErr = nil
	rig.settler.Tick(ctx) // the reconcile timer retries
	require.Equal(t, 2, rig.cache.purges)

	rig.clock.Advance(userBalanceReconcileInterval)
	rig.settler.Tick(ctx)
	require.Equal(t, 2, rig.cache.purges)
}

func TestUserBalanceSettler_StopDrainsAndIsIdempotent(t *testing.T) {
	repo := newSettlerRepoFake(1, 2)
	repo.pending[1] = userBalanceSettleBatchSize*2 + 1
	rig := newSettlerRig(repo)
	rig.settler.nextStats = rig.clock.Now()

	rig.settler.Start()
	rig.settler.Stop()
	rig.settler.Stop()

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Zero(t, repo.pending[1])
	require.Zero(t, repo.pending[2])
}

func TestUserBalanceSettler_DrainGivesBackingOffUsersOneLastAttempt(t *testing.T) {
	repo := newSettlerRepoFake(1)
	busy := true
	repo.settle = func(int64) (*UserBalanceSettlement, error) {
		if busy {
			return nil, ErrUserBalanceSettleBusy
		}
		return nil, nil
	}
	rig := newSettlerRig(repo)
	_, err := rig.settler.RunRound(context.Background())
	require.NoError(t, err)
	repo.takeSettled()

	rig.settler.Drain(context.Background())
	require.Equal(t, []int64{1}, repo.takeSettled(), "one attempt, then stop instead of spinning")

	busy = false
	rig.settler.Drain(context.Background())
	require.Zero(t, repo.pending[1])
}

func TestUserBalanceSettler_DrainStopsAtTheDeadline(t *testing.T) {
	repo := newSettlerRepoFake(1)
	rig := newSettlerRig(repo)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rig.settler.Drain(ctx)
	require.Empty(t, repo.takeSettled())
	require.Equal(t, 1, repo.pending[1], "what is left waits for the next start")
}

func TestUserBalanceSettler_LogsBacklogAndAlertsOnOldRows(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	repo := newSettlerRepoFake(1)
	rig := newSettlerRig(repo)
	ctx := context.Background()
	rig.settler.Tick(ctx) // first tick only arms the minute timer
	require.NotContains(t, logs.String(), "billing.balance_settler_stats")

	repo.stats = UserBalancePendingStats{Entries: 3, OldestAgeSeconds: 60}
	rig.clock.Advance(userBalanceStatsInterval)
	rig.settler.Tick(ctx)
	require.Contains(t, logs.String(), "level=INFO msg=billing.balance_settler_stats settled_users=1 settled_entries=1 backlog_entries=3")

	logs.Reset()
	repo.stats = UserBalancePendingStats{Entries: 3, OldestAgeSeconds: 60.5}
	rig.clock.Advance(userBalanceStatsInterval)
	rig.settler.Tick(ctx)
	require.Contains(t, logs.String(), "level=ERROR msg=billing.balance_settler_backlog_old settled_users=0")
}

// A listing slower than the budget must not leave rounds with nothing done.
func TestUserBalanceSettler_SlowListingStillSettles(t *testing.T) {
	repo := newSettlerRepoFake(1, 2)
	rig := newSettlerRig(repo)
	repo.beforeList = func(context.Context) error {
		rig.clock.Advance(3 * time.Second)
		return nil
	}

	_, err := rig.settler.RunRound(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, repo.takeSettled())
}

func TestUserBalanceSettler_HungListingIsBoundedAndLeftoversStillRun(t *testing.T) {
	repo := newSettlerRepoFake(1, 2, 3)
	rig := newSettlerRig(repo)
	repo.settle = func(userID int64) (*UserBalanceSettlement, error) {
		rig.clock.Advance(3 * time.Second)
		return nil, nil
	}
	_, err := rig.settler.RunRound(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int64{1}, repo.takeSettled())
	require.Equal(t, []int64{2, 3}, rig.settler.carryover)

	var deadline bool
	repo.settle = nil
	repo.beforeList = func(ctx context.Context) error {
		_, deadline = ctx.Deadline()
		return context.DeadlineExceeded
	}
	_, err = rig.settler.RunRound(context.Background())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.True(t, deadline, "the listing runs under its own timeout")
	require.Equal(t, []int64{2, 3}, repo.takeSettled(), "a failed listing does not hold up users already owed a turn")
}
