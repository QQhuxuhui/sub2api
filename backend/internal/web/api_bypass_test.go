//go:build unit

package web

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 网关在根级（无 /v1 前缀）注册的每一条路由，都必须被 shouldBypassEmbeddedFrontend 放行。
//
// 承载该函数的 FrontendServer.Middleware 是 router.go:86 用 r.Use() 注册的全局中间件，
// 排在 RegisterGatewayRoutes（router.go:131）之前。全局中间件先于一切路由运行，
// 所以清单漏一条，那条路由就被 SPA 遮蔽 —— 返回的不是 404 而是**首页 HTML 200**，
// 请求走不到鉴权，客户端拿到一坨 HTML。这种失败不报错、不进错误日志，
// 只有把 base_url 配成不带 /v1 的用户会遇到，最难排查。
//
// 上游 v0.1.177 的清单漏了 15 条，本仓生产实测复现。这条用例存在的意义是：
// 上游下次再加根级路由时，让它在 CI 里红，而不是在客户那里红。
func TestEveryRootGatewayRouteBypassesFrontend(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "server", "routes", "gateway.go"))
	require.NoError(t, err)

	// 只取根级注册（接收者是 r 的那些）；gateway/gemini/codexDirect 等是 /v1 子组，
	// 已被 "/v1/" 前缀整体放行。
	rootRoute := regexp.MustCompile(`(?m)^\s*r\.(?:GET|POST|PUT|PATCH|DELETE)\("([^"]+)"`)
	matches := rootRoute.FindAllStringSubmatch(string(source), -1)
	require.NotEmpty(t, matches, "没从 gateway.go 里解析出任何根级路由 —— 正则或文件结构变了，此时「零遗漏」是假绿灯")

	seen := map[string]struct{}{}
	var offenders []string
	for _, m := range matches {
		pattern := m[1]
		if _, dup := seen[pattern]; dup {
			continue
		}
		seen[pattern] = struct{}{}
		if !shouldBypassEmbeddedFrontend(concreteRequestPath(pattern)) {
			offenders = append(offenders, pattern)
		}
	}

	// 阈值按实测校准：gateway.go 里以 r. 直接注册的根级路由约 33 条
	// （/antigravity/* 那批走的是 r.Group 子组，已被 "/antigravity/" 前缀整体放行，
	// 不在本正则的匹配范围内）。留出余量，只用于拦「正则失配导致空转」。
	require.GreaterOrEqual(t, len(seen), 30,
		"解析出的根级路由只有 %d 条，明显偏少 —— 正则大概率失配了，此时「零遗漏」是假绿灯", len(seen))

	sort.Strings(offenders)
	require.Empty(t, offenders,
		"这些根级网关路由没被 shouldBypassEmbeddedFrontend 放行，会被前端 SPA 吞掉、\n"+
			"返回首页 HTML 200 而不是走鉴权：\n  %s\n\n"+
			"请把它们加进 api_bypass.go 的 bypassExactPaths / bypassPathPrefixes。\n"+
			"注意前缀与全等要成对：只加 \"/x/\" 前缀而漏了 \"/x\" 全等，POST /x 仍会被吞。",
		strings.Join(offenders, "\n  "))
}

// 反向守卫：放行清单不能宽到把前端路由也放走，否则用户直接访问这些页面会拿到 404 JSON。
func TestFrontendRoutesAreNotBypassed(t *testing.T) {
	for _, p := range []string{
		"/", "/login", "/dashboard", "/keys", "/usage", "/team",
		"/admin/settings", "/assets/index-abc123.js", "/favicon.ico",
	} {
		require.False(t, shouldBypassEmbeddedFrontend(p),
			"%s 是前端路由/静态资源，不该被放行给后端", p)
	}
}

// 命名空间前缀是整条 API 的命门：删掉 "/v1/" 会让**全部** /v1/* 请求返回首页 HTML。
// 上面的扫描只覆盖根级路由（r.METHOD 注册的那些），够不到子组，所以这里单独钉住。
// 变异实测：不写这条时，把 "/v1/" 从清单里删掉三条守卫全绿。
func TestAPINamespacePrefixesAreBypassed(t *testing.T) {
	for _, p := range []string{
		"/v1/chat/completions",
		"/v1/messages",
		"/v1/responses/compact",
		"/v1beta/models/gemini-3-pro:generateContent",
		"/api/v1/user/team/summary",
		"/api/v1/admin/accounts",
		"/backend-api/codex/responses",
		"/antigravity/v1/messages",
		"/setup/status",
		"/health",
	} {
		require.True(t, shouldBypassEmbeddedFrontend(p),
			"%s 必须放行给后端 —— 漏了会让整片 API 返回首页 HTML", p)
	}
}

// 事故回归清单：上游 v0.1.177 漏放行、本仓生产实测返回首页 HTML 的那 15 条。
//
// 与上面的自动扫描互补：扫描保证「新增路由不漏」，这条保证「已知踩过的坑不复发」，
// 且即使将来 gateway.go 的注册形态变了导致正则失配，这条仍然钉得住。
func TestKnownSwallowedRootRoutesAreBypassed(t *testing.T) {
	for _, p := range []string{
		"/chat/completions",
		"/embeddings",
		"/messages/count_tokens",
		"/tts",
		"/stt",
		"/web_search",
		"/x_search",
		"/videos",
		"/realtime",
		"/custom-voices",
		"/custom-voices/v_123",
		"/custom-voices/v_123/audio",
	} {
		require.True(t, shouldBypassEmbeddedFrontend(p),
			"%s 曾被前端 SPA 吞掉（返回首页 HTML 200 而非走鉴权），不能再漏", p)
	}
}

// concreteRequestPath 把 gin 路由模板变成一个具体请求路径，供前缀匹配判定。
func concreteRequestPath(pattern string) string {
	segments := strings.Split(pattern, "/")
	for i, seg := range segments {
		if strings.HasPrefix(seg, ":") || strings.HasPrefix(seg, "*") {
			segments[i] = "probe"
		}
	}
	return strings.Join(segments, "/")
}
