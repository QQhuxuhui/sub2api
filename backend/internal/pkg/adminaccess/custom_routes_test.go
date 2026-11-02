//go:build unit

package adminaccess

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 意图路由、换号规则、缓存页、智力探测等是全站配置，受限管理密钥即使持有全部权限也不能访问。
func TestScopedAdminPolicyDeniesCustomRoutes(t *testing.T) {
	policy := Policy{}
	for _, permission := range PermissionCatalog() {
		policy.GlobalPermissions = append(policy.GlobalPermissions, permission.ID)
		policy.Rules = append(policy.Rules, Rule{Permissions: []string{permission.ID}, GroupIDs: []int64{1}})
	}
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/v1/admin/intent-routers"},
		{"GET", "/api/v1/admin/intent-routers/events"},
		{"PUT", "/api/v1/admin/intent-routers/:group_id"},
		{"POST", "/api/v1/admin/intent-routers/:group_id/clear-cache"},
		{"PUT", "/api/v1/admin/intent-routers/:group_id/stream-prelude"},
		{"GET", "/api/v1/admin/iq-probe/monitors"},
		{"POST", "/api/v1/admin/iq-probe/monitors/:id/run"},
		{"GET", "/api/v1/admin/openai-cache/accounts"},
		{"PUT", "/api/v1/admin/openai-cache/accounts/:id/simulation"},
		{"GET", "/api/v1/admin/openai-failover"},
		{"PUT", "/api/v1/admin/openai-failover/accounts/:id/encrypted-args"},
		{"GET", "/api/v1/admin/ops/error-identity/health"},
	} {
		require.False(t, RouteAllowed(policy, route.method, route.path), route.path)
	}
}
