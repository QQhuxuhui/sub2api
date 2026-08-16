//go:build embed || unit

package web

import "strings"

// 本文件从 embed_on.go 拆出来，只为一件事：让 shouldBypassEmbeddedFrontend 能被
// 默认的 unit 测试跑到（embed_on.go 是 //go:build embed，CI 的 make test-unit 够不着）。
// 同包的 static_cache.go 用的就是这个 `embed || unit` 先例。

// shouldBypassEmbeddedFrontend 判断请求路径应当交给后端路由，而不是被前端 SPA 吞掉。
//
// ⚠️ 这是一份**硬编码清单**，而承载它的 FrontendServer.Middleware 是在 router.go:86 用
// r.Use() 注册的**全局中间件**，位置在 RegisterGatewayRoutes（router.go:131）**之前**。
// 全局中间件先于一切路由运行，所以清单漏掉哪条网关路由，那条路由就被 SPA 遮蔽 ——
// 表现不是 404，而是返回**首页 HTML 200**，请求压根走不到鉴权。
//
// 上游 v0.1.177 的这份清单漏了 15 条根级路由（生产实测：/chat/completions、/embeddings、
// /tts、/stt、/web_search、/x_search、/videos、/realtime、/messages/count_tokens
// 与 /custom-voices 全套 CRUD 全部返回首页 HTML）。受影响的是把 base_url 配成
// **不带 /v1** 的客户端 —— 而那批根级镜像路由存在的理由正是支持这种配置。
//
// 新增网关路由时不要手动来这里补：TestEveryRootGatewayRouteBypassesFrontend 会扫
// gateway.go 的根级路由表，漏一条就红。
func shouldBypassEmbeddedFrontend(path string) bool {
	trimmed := strings.TrimSpace(path)
	for _, exact := range bypassExactPaths {
		if trimmed == exact {
			return true
		}
	}
	for _, prefix := range bypassPathPrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// bypassPathPrefixes 是按前缀放行的路径。
var bypassPathPrefixes = []string{
	"/api/",
	"/v1/",
	"/v1beta/",
	"/backend-api/",
	"/antigravity/",
	"/setup/",
	"/responses/",
	"/images/",
	"/videos/",
	"/custom-voices/",
}

// bypassExactPaths 是按全等放行的路径。
//
// 前缀与全等必须成对维护：/videos/ 有前缀却漏了 /videos 全等，POST /videos 就被吞掉 ——
// 上游正是这么漏的。
var bypassExactPaths = []string{
	"/health",
	"/models",
	"/responses",
	"/alpha/search",
	"/chat/completions",
	"/embeddings",
	"/messages/count_tokens",
	"/videos",
	"/custom-voices",
	"/realtime",
	"/tts",
	"/stt",
	"/web_search",
	"/x_search",
}
