package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/web"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type keyBillingRouteAPIKeyRepo struct {
	service.APIKeyRepository
	apiKey *service.APIKey
}

func (r *keyBillingRouteAPIKeyRepo) GetByKeyForAuth(_ context.Context, key string) (*service.APIKey, error) {
	if r.apiKey == nil || key != r.apiKey.Key {
		return nil, service.ErrAPIKeyNotFound
	}
	clone := *r.apiKey
	return &clone, nil
}

type keyBillingRouteRateRepo struct {
	service.UserGroupRateRepository
	lookupCalls int
	// handler 经 c.Request.Context() 把请求 context 一路传到这里，因此这是观测
	// billing 路由真实中间件链（全局超时 → 鉴权 → 用户级覆盖）结果的唯一可靠位置。
	ctxDeadline    time.Time
	ctxHasDeadline bool
}

type keyBillingRouteGroupRepo struct {
	service.GroupRepository
	group *service.Group
}

func (r *keyBillingRouteGroupRepo) GetByIDLite(_ context.Context, id int64) (*service.Group, error) {
	if r.group == nil || id != r.group.ID {
		return nil, service.ErrGroupNotFound
	}
	return r.group, nil
}

func (r *keyBillingRouteRateRepo) GetByUserAndGroup(ctx context.Context, _ int64, _ int64) (*float64, error) {
	r.lookupCalls++
	r.ctxDeadline, r.ctxHasDeadline = ctx.Deadline()
	return nil, nil
}

func (r *keyBillingRouteRateRepo) GetRPMOverrideByUserAndGroup(context.Context, int64, int64) (*int, error) {
	return nil, nil
}

func newKeyBillingRouteTestRouter(runMode string, groups service.GroupRepository) (*gin.Engine, *keyBillingRouteRateRepo, string) {
	return newKeyBillingRouteTestRouterWithTimeouts(runMode, groups, 0, 0)
}

// newKeyBillingRouteTestRouterWithTimeouts 走与生产一致的 RegisterGatewayRoutes 注册，
// 可指定全局网关超时与用户级 request_timeout_seconds，用于验证 billing 路由是否真的
// 挂上了用户级超时中间件（该路由需跳过 requireGroup，故在 Group 之前单独注册）。
func newKeyBillingRouteTestRouterWithTimeouts(runMode string, groups service.GroupRepository, globalTimeoutSeconds, userTimeoutSeconds int) (*gin.Engine, *keyBillingRouteRateRepo, string) {
	gin.SetMode(gin.TestMode)
	group := &service.Group{
		ID:               42,
		Name:             "标准分组",
		Description:      "共享账号",
		I18n:             service.GroupI18n{"en": {Name: "Standard"}},
		Status:           service.StatusActive,
		Hydrated:         true,
		Platform:         service.PlatformOpenAI,
		SubscriptionType: service.SubscriptionTypeStandard,
		RateMultiplier:   0.75,
	}
	user := &service.User{
		ID: 7, Role: service.RoleUser, Status: service.StatusActive, Balance: 10,
		RequestTimeoutSeconds: userTimeoutSeconds,
	}
	var groupID *int64
	var apiKeyGroup *service.Group
	if runMode != config.RunModeSimple {
		groupID = &group.ID
		apiKeyGroup = group
	}
	apiKey := &service.APIKey{
		ID:      100,
		UserID:  user.ID,
		Key:     "billing-route-test-key",
		Status:  service.StatusActive,
		User:    user,
		GroupID: groupID,
		Group:   apiKeyGroup,
	}
	cfg := &config.Config{
		RunMode: runMode,
		Gateway: config.GatewayConfig{RequestTimeoutSeconds: globalTimeoutSeconds},
	}
	rateRepo := &keyBillingRouteRateRepo{}
	if groups == nil {
		groups = &keyBillingRouteGroupRepo{group: group}
	}
	apiKeyService := service.NewAPIKeyService(
		&keyBillingRouteAPIKeyRepo{apiKey: apiKey}, nil, groups, nil, rateRepo, nil, cfg,
	)
	gatewayService := service.NewGatewayService(
		nil, nil, nil, nil, nil, nil, rateRepo, nil, cfg, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, // emptyResponseBillingRepo
	)
	openAIGatewayService := service.NewOpenAIGatewayService(
		nil, nil, nil, nil, nil, rateRepo, nil, cfg, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, // emptyResponseBillingRepo
	)
	gatewayHandler := handler.NewGatewayHandler(
		gatewayService, openAIGatewayService, nil, nil, nil, nil, nil, nil,
		apiKeyService, nil, nil, nil, nil, cfg, nil,
	)

	router := gin.New()
	if web.HasEmbeddedFrontend() {
		router.Use(web.ServeEmbeddedFrontend())
	}
	RegisterGatewayRoutes(
		router,
		&handler.Handlers{Gateway: gatewayHandler, OpenAIGateway: &handler.OpenAIGatewayHandler{}},
		servermiddleware.NewAPIKeyAuthMiddleware(apiKeyService, nil, cfg),
		apiKeyService,
		nil,
		nil,
		nil,
		nil,
		cfg,
	)
	return router, rateRepo, apiKey.Key
}

func TestGatewayRoutesKeyBillingInfoPathIsRegistered(t *testing.T) {
	router := newGatewayRoutesTestRouter()

	for _, route := range router.Routes() {
		if route.Method == http.MethodGet && route.Path == "/v1/sub2api/billing" {
			return
		}
	}

	t.Fatal("GET /v1/sub2api/billing should be registered")
}

func TestGatewayRoutesKeyBillingInfoEndToEnd(t *testing.T) {
	t.Run("missing credentials", func(t *testing.T) {
		router, rateRepo, _ := newKeyBillingRouteTestRouter(config.RunModeStandard, nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/sub2api/billing", nil))

		require.Equal(t, http.StatusUnauthorized, w.Code)
		require.Contains(t, w.Header().Get("Content-Type"), "application/json")
		require.NotContains(t, strings.ToLower(w.Body.String()), "<!doctype html>")
		require.Zero(t, rateRepo.lookupCalls)
	})

	t.Run("standard mode", func(t *testing.T) {
		router, rateRepo, key := newKeyBillingRouteTestRouter(config.RunModeStandard, nil)
		req := httptest.NewRequest(http.MethodGet, "/v1/sub2api/billing", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		require.Contains(t, w.Header().Get("Content-Type"), "application/json")
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		require.NotContains(t, strings.ToLower(w.Body.String()), "<!doctype html>")
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, "sub2api.key_billing", body["object"])
		require.NotContains(t, body, "group_name")
		require.NotContains(t, body, "group_description")
		require.Equal(t, 0.75, body["effective_rate_multiplier"])
		require.Equal(t, 1, rateRepo.lookupCalls)
	})

	t.Run("requested group belongs to the calling key", func(t *testing.T) {
		router, _, key := newKeyBillingRouteTestRouter(config.RunModeStandard, nil)
		req := httptest.NewRequest(http.MethodGet, "/v1/sub2api/billing?include=group&group_id=99", nil)
		req.Header.Set("x-api-key", key)
		req.Header.Set("Accept-Language", "en")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, "Standard", body["group_name"])
		require.Equal(t, "共享账号", body["group_description"])
		require.Equal(t, 0.75, body["group_rate_multiplier"])
		require.NotContains(t, body, "group_id")
		require.NotContains(t, w.Body.String(), key)
	})

	t.Run("include accepts a list of sections", func(t *testing.T) {
		for query, wantGroup := range map[string]bool{
			"include=group,model_pricing":         true,
			"include=model_pricing,%20group":      true,
			"include=model_pricing&include=group": true,
			"include=group&include=model_pricing": true,
			"include=model_pricing":               false,
			"include=groups":                      false,
			"include=":                            false,
		} {
			router, _, key := newKeyBillingRouteTestRouter(config.RunModeStandard, nil)
			req := httptest.NewRequest(http.MethodGet, "/v1/sub2api/billing?"+query, nil)
			req.Header.Set("x-api-key", key)
			req.Header.Set("Accept-Language", "en")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code, query)
			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), query)
			require.Equal(t, 0.75, body["effective_rate_multiplier"], query)
			if wantGroup {
				require.Equal(t, "Standard", body["group_name"], query)
			} else {
				require.NotContains(t, body, "group_name", query)
			}
		}
	})

	t.Run("optional group lookup failure preserves billing rates", func(t *testing.T) {
		router, _, key := newKeyBillingRouteTestRouter(config.RunModeStandard, &keyBillingRouteGroupRepo{})
		req := httptest.NewRequest(http.MethodGet, "/v1/sub2api/billing?include=group", nil)
		req.Header.Set("x-api-key", key)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, 0.75, body["effective_rate_multiplier"])
		require.NotContains(t, body, "group_name")
	})

	t.Run("simple mode", func(t *testing.T) {
		router, rateRepo, key := newKeyBillingRouteTestRouter(config.RunModeSimple, nil)
		req := httptest.NewRequest(http.MethodGet, "/v1/sub2api/billing", nil)
		req.Header.Set("x-api-key", key)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusNotFound, w.Code)
		require.Contains(t, w.Header().Get("Content-Type"), "application/json")
		require.NotContains(t, strings.ToLower(w.Body.String()), "<!doctype html>")
		require.JSONEq(t, `{
			"type": "error",
			"error": {
				"type": "not_found_error",
				"message": "Billing information is not supported in simple mode"
			}
		}`, w.Body.String())
		require.Zero(t, rateRepo.lookupCalls)
	})
}

// TestGatewayRoutesKeyBillingUserTimeoutOverride 验证 /v1/sub2api/billing 经真实
// RegisterGatewayRoutes 注册后确实挂上了用户级超时中间件。该路由需跳过 requireGroup
// 而在 Group 之前注册，Gin 注册时即固定中间件链，若漏挂 UserOverride/Finalizer，
// 用户的 request_timeout_seconds（含 -1 豁免）对它就不生效。
//
// 观测点是 rate repo 收到的 context —— handler 经 c.Request.Context() 一路传下来，
// 因此断言的是真实链路结果，而非测试自建的中间件栈。
func TestGatewayRoutesKeyBillingUserTimeoutOverride(t *testing.T) {
	const globalTimeout = 60

	doRequest := func(t *testing.T, router *gin.Engine, key string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/v1/sub2api/billing", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	// 反向对照：不设用户级超时时应落在全局 deadline 上。这条锁住"链路里确实有全局
	// 超时"，否则下面 -1 的用例可能因为压根没挂超时而假通过。
	t.Run("no user override keeps global deadline", func(t *testing.T) {
		router, rateRepo, key := newKeyBillingRouteTestRouterWithTimeouts(config.RunModeStandard, nil, globalTimeout, 0)
		start := time.Now()

		w := doRequest(t, router, key)

		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, 1, rateRepo.lookupCalls)
		require.True(t, rateRepo.ctxHasDeadline, "全局超时应给 billing 路由建立 deadline")
		require.WithinDuration(t, start.Add(globalTimeout*time.Second), rateRepo.ctxDeadline, 30*time.Second)
	})

	t.Run("positive user timeout overrides global deadline", func(t *testing.T) {
		router, rateRepo, key := newKeyBillingRouteTestRouterWithTimeouts(config.RunModeStandard, nil, globalTimeout, 3600)
		start := time.Now()

		w := doRequest(t, router, key)

		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, 1, rateRepo.lookupCalls)
		require.True(t, rateRepo.ctxHasDeadline)
		// deadline 必须≈ start+3600s（用户级），而非 start+60s（全局）：
		// 若 billing 路由漏挂 UserOverride，这里会是全局 60s 而判定失败。
		require.WithinDuration(t, start.Add(3600*time.Second), rateRepo.ctxDeadline, 30*time.Second)
		require.Greater(t, time.Until(rateRepo.ctxDeadline), 10*time.Minute,
			"用户级超时未生效（deadline 仍是全局 60s）")
	})

	t.Run("minus one cancels global deadline", func(t *testing.T) {
		router, rateRepo, key := newKeyBillingRouteTestRouterWithTimeouts(config.RunModeStandard, nil, globalTimeout, -1)

		w := doRequest(t, router, key)

		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, 1, rateRepo.lookupCalls)
		require.False(t, rateRepo.ctxHasDeadline,
			"-1 应撤销全局 deadline；仍有 deadline 说明 billing 路由漏挂 UserOverride")
	})
}
