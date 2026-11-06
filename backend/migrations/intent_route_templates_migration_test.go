package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIntentRouteTemplatesMigration(t *testing.T) {
	content, err := FS.ReadFile("259_intent_route_templates.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")

	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS intent_route_templates")
	require.Contains(t, sql, "template_key CHAR(64) PRIMARY KEY")
	require.Contains(t, sql, "status VARCHAR(16) NOT NULL DEFAULT 'pending'")
	require.Contains(t, sql, "CHECK (status IN ('pending', 'probe', 'health', 'normal'))")
	require.Contains(t, sql, "revision BIGINT NOT NULL DEFAULT 0")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_intent_route_templates_status_seen ON intent_route_templates (status, last_seen_at DESC)")
}
