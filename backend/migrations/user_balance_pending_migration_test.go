package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserBalancePendingMigration(t *testing.T) {
	content, err := FS.ReadFile("256_user_balance_pending.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS user_balance_pending (")
	require.Contains(t, sql, "id BIGSERIAL PRIMARY KEY")
	require.Contains(t, sql, "amount NUMERIC(20,8) NOT NULL CHECK (amount > 0)")
	require.Contains(t, sql, "UNIQUE (request_id, api_key_id)")
	require.Contains(t, sql,
		"CREATE INDEX IF NOT EXISTS idx_user_balance_pending_user ON user_balance_pending (user_id, id)")
	require.NotContains(t, sql, "REFERENCES", "the insert must stay off the users row lock")
}
