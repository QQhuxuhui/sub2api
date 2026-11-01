//go:build unit

package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// sqlmock rejects any statement it does not expect, so an UPDATE of users fails
// this test.
func TestUsageBillingRepositoryApply_DeferredTouchesOnlyClaimAndPending(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO usage_billing_dedup`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`FROM usage_billing_dedup_archive`).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO user_balance_pending`).
		WithArgs(int64(14), "req-1", int64(2), 1.25).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	repo := NewUsageBillingRepository(nil, db)
	result, err := repo.Apply(context.Background(), &service.UsageBillingCommand{
		RequestID:    "req-1",
		APIKeyID:     2,
		UserID:       14,
		BalanceCost:  1.25,
		DeferBalance: true,
	})
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.True(t, result.BalanceDeferred)
	require.Nil(t, result.NewBalance)
	require.NoError(t, mock.ExpectationsWereMet())
}
