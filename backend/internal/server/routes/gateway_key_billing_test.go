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
}

func (r *keyBillingRouteRateRepo) GetByUserAndGroup(context.Context, int64, int64) (*float64, error) {
	r.lookupCalls++
	return nil, nil
}

func (r *keyBillingRouteRateRepo) GetRPMOverrideByUserAndGroup(context.Context, int64, int64) (*int, error) {
	return nil, nil
}

func newKeyBillingRouteTestRouter(runMode string) (*gin.Engine, *keyBillingRouteRateRepo, string) {
	gin.SetMode(gin.TestMode)
	group := &service.Group{
		ID:               42,
		Status:           service.StatusActive,
		Hydrated:         true,
		Platform:         service.PlatformOpenAI,
		SubscriptionType: service.SubscriptionTypeStandard,
		RateMultiplier:   0.75,
	}
	user := &service.User{ID: 7, Role: service.RoleUser, Status: service.StatusActive, Balance: 10}
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
	cfg := &config.Config{RunMode: runMode}
	rateRepo := &keyBillingRouteRateRepo{}
	apiKeyService := service.NewAPIKeyService(
		&keyBillingRouteAPIKeyRepo{apiKey: apiKey}, nil, nil, nil, rateRepo, nil, cfg,
	)
	gatewayService := service.NewGatewayService(
		nil, nil, nil, nil, nil, nil, rateRepo, nil, cfg, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	openAIGatewayService := service.NewOpenAIGatewayService(
		nil, nil, nil, nil, nil, rateRepo, nil, cfg, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
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
		router, rateRepo, _ := newKeyBillingRouteTestRouter(config.RunModeStandard)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/sub2api/billing", nil))

		require.Equal(t, http.StatusUnauthorized, w.Code)
		require.Contains(t, w.Header().Get("Content-Type"), "application/json")
		require.NotContains(t, strings.ToLower(w.Body.String()), "<!doctype html>")
		require.Zero(t, rateRepo.lookupCalls)
	})

	t.Run("standard mode", func(t *testing.T) {
		router, rateRepo, key := newKeyBillingRouteTestRouter(config.RunModeStandard)
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
		require.Equal(t, 0.75, body["effective_rate_multiplier"])
		require.Equal(t, 1, rateRepo.lookupCalls)
	})

	t.Run("simple mode", func(t *testing.T) {
		router, rateRepo, key := newKeyBillingRouteTestRouter(config.RunModeSimple)
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

// TestGatewayRoutesKeyBillingUserTimeoutOverride 验证 /v1/sub2api/billing 路由确实
// 经过用户级超时中间件（该路由需跳过 requireGroup 而单独挂载 UserOverride/Finalizer）。
// 复刻 billing 路由的中间件链：全局 RequestTimeout → 鉴权(注入带用户超时的 apiKey) →
// UserOverride → Finalizer → handler，断言 handler 收到的 deadline 由用户级超时决定。
func TestGatewayRoutesKeyBillingUserTimeoutOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newRouter := func(userTimeoutSeconds int, deadline *time.Time, hasDeadline *bool) *gin.Engine {
		router := gin.New()
		router.Use(servermiddleware.RequestTimeout(60, nil)) // 全局 60s
		router.Use(func(c *gin.Context) {                    // 模拟鉴权：注入带用户级超时的 apiKey
			c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
				User: &service.User{RequestTimeoutSeconds: userTimeoutSeconds},
			})
			c.Next()
		})
		router.GET("/v1/sub2api/billing",
			servermiddleware.RequestTimeoutUserOverride(),
			servermiddleware.RequestTimeoutFinalizer(),
			func(c *gin.Context) {
				*deadline, *hasDeadline = c.Request.Context().Deadline()
				c.Status(http.StatusOK)
			})
		return router
	}

	t.Run("positive user timeout overrides global deadline", func(t *testing.T) {
		var deadline time.Time
		var hasDeadline bool
		router := newRouter(3600, &deadline, &hasDeadline) // 用户 1 小时，远大于全局 60s
		start := time.Now()
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sub2api/billing", nil))

		require.Equal(t, http.StatusOK, rec.Code)
		require.True(t, hasDeadline, "billing 路由应经 UserOverride 设置用户级 deadline")
		// deadline 应≈ start+3600s（用户级），而非 start+60s（全局），证明 UserOverride 对本路由生效。
		require.WithinDuration(t, start.Add(3600*time.Second), deadline, 30*time.Second)
	})

	t.Run("minus one cancels global deadline", func(t *testing.T) {
		var deadline time.Time
		var hasDeadline bool
		router := newRouter(-1, &deadline, &hasDeadline) // -1 = 不限制
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sub2api/billing", nil))

		require.Equal(t, http.StatusOK, rec.Code)
		require.False(t, hasDeadline, "-1 应撤销全局 deadline，billing 路由 handler 不应带 deadline")
	})
}
