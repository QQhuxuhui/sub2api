//go:build unit

package adminaccess

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 本仓决策 U6：二开新增的 /api/v1/admin/* 接口不进受限管理员密钥的权限目录。
// 即使受限密钥拿到目录里的全部权限，也不能访问这些接口（只有完整管理员 / 登录管理员可以）。
// 以后若有人把这些路由登记进 definitions，本用例会失败，提醒先回到用户决策。
func TestScopedAdminFullPolicyStillDeniesDevOnlyRoutes(t *testing.T) {
	full := Policy{}
	var scoped []string
	for _, p := range PermissionCatalog() {
		if p.Scope == "global" {
			full.GlobalPermissions = append(full.GlobalPermissions, p.ID)
		} else {
			scoped = append(scoped, p.ID)
		}
	}
	full.Rules = []Rule{{Permissions: scoped, AllGroups: true, IncludeUngrouped: true}}

	devRoutes := []string{
		"GET /api/v1/admin/users/:id/empty-response-billing-rules",
		"PUT /api/v1/admin/users/:id/empty-response-billing-rules",
		"GET /api/v1/admin/settings/model-mapping-template/:platform",
		"PUT /api/v1/admin/settings/model-mapping-template/:platform",
		"GET /api/v1/admin/settings/request-timeout",
		"PUT /api/v1/admin/settings/request-timeout",
		"GET /api/v1/admin/ops/notify-channels/config",
		"PUT /api/v1/admin/ops/notify-channels/config",
		"POST /api/v1/admin/ops/notify-channels/test",
		"POST /api/v1/admin/payment/orders/:id/settle-by-tx",
		"GET /api/v1/admin/anthropic-failover",
		"PUT /api/v1/admin/anthropic-failover",
	}
	for _, route := range devRoutes {
		method, path, _ := cut(route)
		require.False(t, RouteAllowed(full, method, path), "%s 不应对受限管理员密钥开放", route)
	}
	// 反向对照：同一份策略能访问目录里的上游路由，防止用例因策略构造错误而假通过。
	allowedAny := false
	for _, d := range definitions {
		for _, r := range d.Routes {
			method, rest, _ := cut(r)
			if RouteAllowed(full, method, "/api/v1/admin"+rest) {
				allowedAny = true
			}
		}
	}
	require.True(t, allowedAny)
}

func cut(s string) (string, string, bool) {
	for i, ch := range s {
		if ch == ' ' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}
