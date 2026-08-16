package routes

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 用户级请求超时（migration 175，语义 0 / -1 / 1..86400）靠三个中间件协作：
//
//	requestTimeout              鉴权前建立全局 deadline
//	requestTimeoutUserOverride  鉴权后按用户的 request_timeout_seconds 替换 deadline
//	requestTimeoutFinalizer     链尾补写 handler 静默返回的 504
//
// 三者必须逐条挂到**每一条**网关路由上。漏挂不会报错，只会让那条路径静默退回全局超时
// 或干脆没有超时 —— 用户在管理台设的值对它毫无作用，上游挂死时连接一直吊着，
// 且不返 504、Ops 日志里也看不到超时记录，排障时表现为「请求消失」。
//
// 这类漏挂在同步上游时几乎必然复发：上游每加一批路由（本仓已经历过一次 —— v0.1.177
// 新增的 11 条根级语音/搜索路由全部漏挂），新路由都不会自带本仓的三件套。
// 所以这里用两条互补的守卫钉死它。

// 真实路由表跑一遍：每条路由都必须在鉴权时刻已经有 deadline。
//
// 只断 deadline 而不断另外两个中间件，是因为 requestTimeoutUserOverride 排在
// requireGroupAnthropic 之后、即探针（挂在 apiKeyAuth 位置）之后，探针时刻它还没跑。
// 另外两个由下面的源码扫描守卫覆盖。
func TestEveryGatewayRouteHasRequestDeadlineAtAuthTime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	var probedPath string
	var hasDeadline bool
	RegisterGatewayRoutes(
		router,
		&handler.Handlers{
			Gateway:       &handler.GatewayHandler{},
			OpenAIGateway: &handler.OpenAIGatewayHandler{},
			AsyncImage:    handler.NewAsyncImageHandler(nil, nil),
		},
		servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
			probedPath = c.FullPath()
			_, hasDeadline = c.Request.Context().Deadline()
			groupID := int64(1)
			c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
				GroupID: &groupID,
				Group:   &service.Group{Platform: service.PlatformOpenAI},
			})
			c.AbortWithStatus(http.StatusNoContent)
		}),
		nil, nil, nil, nil, nil,
		&config.Config{Gateway: config.GatewayConfig{RequestTimeoutSeconds: 60}},
	)

	// 不发 WebSocket 升级头：豁免名单只在「GET + 完整升级握手」时生效，
	// 所以这里连 WS 路由也必须拿到 deadline，无需任何例外清单。
	var missing []string
	var reached int
	for _, r := range router.Routes() {
		path := concreteRoutePath(r.Path)
		probedPath, hasDeadline = "", false

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(r.Method, path, strings.NewReader("{}")))

		if probedPath == "" {
			continue // 没走到鉴权（例如被 body limit / 平台守卫提前拦掉），不在本用例判定范围
		}
		reached++
		if !hasDeadline {
			missing = append(missing, r.Method+" "+r.Path)
		}
	}

	require.Greater(t, reached, 50,
		"走到鉴权的路由太少，说明探针没生效 —— 此时「零漏挂」是假绿灯")
	sort.Strings(missing)
	require.Empty(t, missing,
		"这些路由在鉴权时刻没有 deadline，说明 requestTimeout 没挂上：\n  %s",
		strings.Join(missing, "\n  "))
}

// 源码扫描：凡是挂了 requireGroupAnthropic 的路由行，必须同时带三件套。
//
// requireGroupAnthropic 是网关业务路由的共同标志，用它筛掉 billing 这类特殊只读路由。
func TestEveryGatewayRouteLineCarriesRequestTimeoutTrio(t *testing.T) {
	source, err := os.ReadFile("gateway.go")
	require.NoError(t, err)

	routeLine := regexp.MustCompile(`^\s*(?:gateway|gemini|r|codexDirect|antigravityV1|antigravityV1Beta)\.(?:GET|POST|PUT|PATCH|DELETE)\("([^"]+)"`)

	var offenders []string
	for i, line := range strings.Split(string(source), "\n") {
		m := routeLine.FindStringSubmatch(line)
		if m == nil || !strings.Contains(line, "requireGroupAnthropic") {
			continue
		}
		var lacks []string
		for _, mw := range []string{"requestTimeout,", "requestTimeoutUserOverride", "requestTimeoutFinalizer"} {
			if !strings.Contains(line, mw) {
				lacks = append(lacks, strings.TrimSuffix(mw, ","))
			}
		}
		if len(lacks) > 0 {
			offenders = append(offenders, "gateway.go:"+itoa(i+1)+" "+m[1]+" 缺 "+strings.Join(lacks, "/"))
		}
	}

	require.Empty(t, offenders,
		"这些路由行漏了用户级超时中间件（同步上游新增路由时最容易漏）：\n  %s",
		strings.Join(offenders, "\n  "))
}

// WebSocket 路由在**完整升级握手**时必须豁免全局 deadline，否则长连接会在超时点被
// context 取消强行掐断 —— 而响应早已被 WS 升级提交，Finalizer 写不进 504，
// 客户端只看到连接莫名关闭、Ops 日志里也没有超时痕迹（v0.1.177 新增的 /realtime
// 就漏进过这个坑）。豁免名单是 c.Request.URL.Path 的**精确匹配**，
// 所以带 /v1 前缀与根级镜像各要一条，这里逐条钉住。
//
// 每条路由都断言一对：带升级头 → 无 deadline（豁免生效）；不带 → 有 deadline。
// 只断前半句的话，把路由整条删掉也照样通过 —— "没有 deadline" 与 "没有这条路由"
// 在那种写法下产出同一个结果。
func TestWebSocketRoutesAreExemptFromRequestDeadline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	var reached, hasDeadline bool
	RegisterGatewayRoutes(
		router,
		&handler.Handlers{
			Gateway:       &handler.GatewayHandler{},
			OpenAIGateway: &handler.OpenAIGatewayHandler{},
			AsyncImage:    handler.NewAsyncImageHandler(nil, nil),
		},
		servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
			reached = true
			_, hasDeadline = c.Request.Context().Deadline()
			groupID := int64(1)
			c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
				GroupID: &groupID,
				Group:   &service.Group{Platform: service.PlatformOpenAI},
			})
			c.AbortWithStatus(http.StatusNoContent)
		}),
		nil, nil, nil, nil, nil,
		&config.Config{Gateway: config.GatewayConfig{RequestTimeoutSeconds: 60}},
	)

	probe := func(path string, upgrade bool) (bool, bool) {
		reached, hasDeadline = false, false
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if upgrade {
			req.Header.Set("Upgrade", "websocket")
			req.Header.Set("Connection", "keep-alive, Upgrade")
			req.Header.Set("Sec-WebSocket-Version", "13")
			req.Header.Set("Sec-WebSocket-Key", "AAAAAAAAAAAAAAAAAAAAAA==") // 16 字节 base64
		}
		router.ServeHTTP(httptest.NewRecorder(), req)
		return reached, hasDeadline
	}

	for _, path := range []string{
		"/v1/responses",
		"/responses",
		"/backend-api/codex/responses",
		"/v1/realtime",
		"/realtime",
	} {
		gotReached, gotDeadline := probe(path, true)
		require.True(t, gotReached, "%s 没走到鉴权 —— 路由可能被删了，此时豁免断言毫无意义", path)
		require.False(t, gotDeadline, "%s 的 WebSocket 升级握手必须豁免全局 deadline", path)

		gotReached, gotDeadline = probe(path, false)
		require.True(t, gotReached, "%s 不带升级头时没走到鉴权", path)
		require.True(t, gotDeadline,
			"%s 不带升级头就是普通请求，必须照常受全局超时约束（这条对照组防止上一句变成空断言）", path)
	}
}

// concreteRoutePath 把 gin 的路由模板变成可请求的具体路径。
func concreteRoutePath(pattern string) string {
	segments := strings.Split(pattern, "/")
	for i, seg := range segments {
		switch {
		case strings.HasPrefix(seg, "*"):
			segments[i] = "probe"
		case strings.HasPrefix(seg, ":"):
			segments[i] = "probe"
		}
	}
	return strings.Join(segments, "/")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
