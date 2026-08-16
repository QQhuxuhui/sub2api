package routes

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/teamops"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// newTeamRouteTestRouter 按生产路径注册用户路由。settingService 与 panelRateLimiter 传 nil：
// 两者的中间件都在 nil 时放行，注册期也不解引用。
func newTeamRouteTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	jwtAuth := servermiddleware.JWTAuthMiddleware(func(c *gin.Context) {
		if c.GetHeader("Authorization") == "" {
			servermiddleware.AbortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "Authorization required")
			return
		}
		c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 1})
		c.Next()
	})
	auditLog := servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	handlers := &handler.Handlers{TeamOps: teamops.NewHandler(teamops.NewService(nil, 90, true))}
	RegisterUserRoutes(router.Group("/api/v1"), handlers, jwtAuth, auditLog, nil, nil)
	return router
}

// 路径写错或忘了调用 registerTeamRoutes 时，页面上是 404，而后端测试可以全绿——
// 服务层与 handler 的单测都不经过路由表。
func TestTeamRoutesAreRegisteredAtExpectedPaths(t *testing.T) {
	registered := map[string]bool{}
	for _, r := range newTeamRouteTestRouter().Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	require.True(t, registered["GET /api/v1/user/team/summary"], "路由表：%v", registered)
	require.True(t, registered["GET /api/v1/user/team/rows"], "路由表：%v", registered)
}

// 看板下发的是该账号全部令牌的消耗明细与密钥尾号，必须挂在认证组下面。
// 挂错组（比如挂到 v1 上）没有任何编译或运行期报错。
//
// 只断言状态码是不够的：handler 自己也会在拿不到认证主体时回 401，
// 于是「路由挂到了未认证组」这个改动照样是 401，测试全绿。所以这里断言的是
// **谁**拒的——认证中间件的响应体里带 UNAUTHORIZED 这个 reason，handler 的兜底没有。
func TestTeamRoutesRejectUnauthenticatedRequestsAtTheAuthMiddleware(t *testing.T) {
	router := newTeamRouteTestRouter()
	for _, path := range []string{"/api/v1/user/team/summary", "/api/v1/user/team/rows"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equalf(t, http.StatusUnauthorized, recorder.Code, "%s 必须要求认证", path)
		require.Containsf(t, recorder.Body.String(), "UNAUTHORIZED",
			"%s 必须被认证中间件拦下，而不是靠 handler 自己兜底", path)
	}
}

// 本页必须用独立计数桶的限流器。Heavy() 的计数键对全站重查询端点是同一个，
// 换成它之后本页的请求会挤占 /usage、/dashboard 的额度——报 429 的是既有功能，
// 而本页看起来一切正常。这条差异在路由表和响应里都看不出来，只能盯注册处的源码。
func TestTeamRoutesUseDedicatedRateLimitScope(t *testing.T) {
	source, err := os.ReadFile("user.go")
	require.NoError(t, err)
	call := regexp.MustCompile(`registerTeamRoutes\([^)]*\)`).FindString(string(source))
	require.NotEmpty(t, call, "user.go 必须调用 registerTeamRoutes")
	require.Contains(t, call, `Scoped("team")`)
	require.NotContains(t, call, "Heavy()")
}
