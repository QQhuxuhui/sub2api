package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
)

const pgCodeLockNotAvailable = "55P03"

type userBalancePendingRepository struct {
	db *sql.DB
}

// NewUserBalancePendingRepository stores the deferred balance deductions.
func NewUserBalancePendingRepository(sqlDB *sql.DB) service.UserBalancePendingRepository {
	return &userBalancePendingRepository{db: sqlDB}
}

// lockEffectiveBalance locks the user row, then reads the pending sum in a second
// statement. The order and the split are what make the pair consistent under
// READ COMMITTED: the second statement takes a new snapshot after the lock is
// held, so it sees everything a settlement committed before releasing the row. A
// subquery inside one UPDATE would keep the snapshot from before the lock wait
// and count rows the settlement has already moved into balance.
//
// The caller decides on balance - pending and must stay in the same transaction
// until it has written.
func lockEffectiveBalance(ctx context.Context, q sqlQueryer, userID int64) (balance, pending decimal.Decimal, err error) {
	err = scanSingleRow(ctx, q,
		`SELECT balance FROM users WHERE id = $1 AND deleted_at IS NULL FOR NO KEY UPDATE`,
		[]any{userID}, &balance)
	if errors.Is(err, sql.ErrNoRows) {
		return decimal.Zero, decimal.Zero, service.ErrUserNotFound
	}
	if err != nil {
		return decimal.Zero, decimal.Zero, err
	}
	err = scanSingleRow(ctx, q,
		`SELECT COALESCE(SUM(amount), 0) FROM user_balance_pending WHERE user_id = $1`,
		[]any{userID}, &pending)
	if err != nil {
		return decimal.Zero, decimal.Zero, err
	}
	return balance, pending, nil
}

func (r *userBalancePendingRepository) ListUsers(ctx context.Context, excludeUserIDs []int64, limit int) (ids []int64, err error) {
	if excludeUserIDs == nil {
		// pq.Array(nil) is NULL, and `<> ALL(NULL)` matches nothing.
		excludeUserIDs = []int64{}
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT user_id, MIN(id)
		FROM user_balance_pending
		WHERE user_id <> ALL($1)
		GROUP BY user_id
		ORDER BY 2
		LIMIT $2
	`, pq.Array(excludeUserIDs), limit)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	for rows.Next() {
		var userID, oldest int64
		if err := rows.Scan(&userID, &oldest); err != nil {
			return nil, err
		}
		ids = append(ids, userID)
	}
	return ids, rows.Err()
}

func (r *userBalancePendingRepository) ListAllUsers(ctx context.Context) (ids []int64, err error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT user_id FROM user_balance_pending`)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		ids = append(ids, userID)
	}
	return ids, rows.Err()
}

// SettleUser moves the user's oldest batchSize rows into users.balance in one
// short transaction. The user row lock serializes settlers and conditional
// balance operations, and the amounts stay decimal end to end: the batch sum must
// leave balance exactly what the deleted rows added up to.
func (r *userBalancePendingRepository) SettleUser(ctx context.Context, userID int64, batchSize int) (*service.UserBalanceSettlement, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '500ms'`); err != nil {
		return nil, err
	}

	var before decimal.Decimal
	// Soft-deleted users settle too: their requests were served.
	err = tx.QueryRowContext(ctx, `SELECT balance FROM users WHERE id = $1 FOR NO KEY UPDATE NOWAIT`, userID).Scan(&before)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrUserNotFound
	}
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && string(pqErr.Code) == pgCodeLockNotAvailable {
			return nil, service.ErrUserBalanceSettleBusy
		}
		return nil, err
	}

	ids, sum, err := readPendingBatch(ctx, tx, userID, batchSize)
	if err != nil {
		return nil, err
	}
	settlement := &service.UserBalanceSettlement{UserID: userID, Entries: len(ids)}
	if len(ids) == 0 {
		return settlement, nil
	}

	var after decimal.Decimal
	err = tx.QueryRowContext(ctx,
		`UPDATE users SET balance = balance - $1::numeric, updated_at = NOW() WHERE id = $2 RETURNING balance`,
		sum.String(), userID).Scan(&after)
	if err != nil {
		return nil, err
	}

	res, err := tx.ExecContext(ctx, `DELETE FROM user_balance_pending WHERE id = ANY($1)`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if deleted != int64(len(ids)) {
		return nil, fmt.Errorf("settle user %d: deleted %d pending rows, batch had %d", userID, deleted, len(ids))
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	settlement.Amount = sum.InexactFloat64()
	settlement.OldBalance = before.InexactFloat64()
	settlement.NewBalance = after.InexactFloat64()
	return settlement, nil
}

// readPendingBatch reads ids and amounts in one statement, so the sum belongs to
// exactly the rows the caller deletes.
func readPendingBatch(ctx context.Context, tx *sql.Tx, userID int64, batchSize int) (ids []int64, sum decimal.Decimal, err error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, amount
		FROM user_balance_pending
		WHERE user_id = $1
		ORDER BY id
		LIMIT $2
	`, userID, batchSize)
	if err != nil {
		return nil, decimal.Zero, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	for rows.Next() {
		var id int64
		var amount decimal.Decimal
		if err := rows.Scan(&id, &amount); err != nil {
			return nil, decimal.Zero, err
		}
		ids = append(ids, id)
		sum = sum.Add(amount)
	}
	return ids, sum, rows.Err()
}

func (r *userBalancePendingRepository) Stats(ctx context.Context) (service.UserBalancePendingStats, error) {
	var stats service.UserBalancePendingStats
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(EXTRACT(EPOCH FROM NOW() - MIN(created_at)), 0)
		FROM user_balance_pending
	`).Scan(&stats.Entries, &stats.OldestAgeSeconds)
	return stats, err
}
