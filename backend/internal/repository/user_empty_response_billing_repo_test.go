package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestReplaceUserEmptyResponseBillingRulesSerializesPerUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &userEmptyResponseBillingRepository{db: db}

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs(advisoryLockHash("user-empty-response-billing:42")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM user_empty_response_billing_rules").
		WithArgs(int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	require.NoError(t, repo.ReplaceByUserID(context.Background(), 42, nil))
	require.NoError(t, mock.ExpectationsWereMet())
}
