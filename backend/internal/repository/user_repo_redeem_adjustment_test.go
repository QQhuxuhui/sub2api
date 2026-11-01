package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

func newRedeemAdjustmentRepoMock(t *testing.T) (*userRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })
	return newUserRepositoryWithSQL(client, db), mock
}

// expectLockEffectiveBalance pins the order: the row lock, then pending in its own
// statement.
func expectLockEffectiveBalance(mock sqlmock.Sqlmock, userID int64, balance, pending string) {
	mock.ExpectQuery(`SELECT balance FROM users WHERE id = \$1 AND deleted_at IS NULL FOR UPDATE`).
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(balance))
	mock.ExpectQuery(`SELECT COALESCE\(SUM\(amount\), 0\) FROM user_balance_pending WHERE user_id = \$1`).
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow(pending))
}

func TestApplyRedeemBalanceAdjustment_FloorsEffectiveBalanceUnderRowLock(t *testing.T) {
	for _, tc := range []struct {
		name      string
		balance   string
		pending   string
		delta     float64
		wantStore string
	}{
		{name: "above the floor", balance: "10", pending: "2", delta: -7, wantStore: "3"},
		{name: "floored keeps pending", balance: "10", pending: "2", delta: -9, wantStore: "2"},
		{name: "added", balance: "10", pending: "0", delta: 7, wantStore: "17"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, mock := newRedeemAdjustmentRepoMock(t)
			mock.ExpectBegin()
			expectLockEffectiveBalance(mock, 42, tc.balance, tc.pending)
			mock.ExpectQuery(`UPDATE users SET balance = \$1::numeric, updated_at = NOW\(\) WHERE id = \$2 RETURNING balance`).
				WithArgs(tc.wantStore, int64(42)).
				WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(tc.wantStore))
			mock.ExpectCommit()

			require.NoError(t, repo.ApplyRedeemBalanceAdjustment(context.Background(), 42, tc.delta))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestApplyRedeemConcurrencyAdjustment_UsesAtomicFloor(t *testing.T) {
	repo, mock := newRedeemAdjustmentRepoMock(t)
	mock.ExpectExec(`UPDATE users SET concurrency = GREATEST\(concurrency \+ \$1, 0\), updated_at = NOW\(\) WHERE id = \$2 AND deleted_at IS NULL`).
		WithArgs(-7, int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.ApplyRedeemConcurrencyAdjustment(context.Background(), 42, -7))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyRedeemAdjustment_MissingUser(t *testing.T) {
	repo, mock := newRedeemAdjustmentRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT balance FROM users WHERE id = \$1 AND deleted_at IS NULL FOR UPDATE`).
		WithArgs(int64(404)).
		WillReturnRows(sqlmock.NewRows([]string{"balance"}))
	mock.ExpectRollback()

	err := repo.ApplyRedeemBalanceAdjustment(context.Background(), 404, -1)
	require.ErrorIs(t, err, service.ErrUserNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}
