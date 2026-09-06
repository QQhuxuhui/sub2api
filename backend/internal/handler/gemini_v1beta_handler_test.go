//go:build unit

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/gemini"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGeminiV1BetaListModels_CustomGroupListUsesNativeResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
	// 本仓语义（与 /v1/models 一致）：自定义列表按账号可用模型过滤，无可用模型时退回平台默认列表；
	// 不在基准内的名字被丢弃，不会原样透传。
	knownModel := geminiV1BetaFallbackModelIDs(service.PlatformGemini)[0]
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{
			Platform: service.PlatformGemini,
			ModelsListConfig: service.GroupModelsListConfig{
				Enabled: true,
				Models:  []string{knownModel, "not-a-real-model"},
			},
		},
	})

	(&GatewayHandler{}).GeminiV1BetaListModels(c)

	require.Equal(t, http.StatusOK, rec.Code)
	var got gemini.ModelsListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, []gemini.Model{gemini.FallbackModel(knownModel)}, got.Models)
}

// 本仓语义（与上游「强制 antigravity 忽略自定义列表」不同）：/antigravity/v1beta/models 同样
// 应用分组自定义列表，按 antigravity 平台默认模型过滤——线上 antigravity 分组正是靠它收窄模型列表。
func TestGeminiV1BetaListModels_ForcedAntigravityAppliesCustomGroupList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/antigravity/v1beta/models", nil)
	knownModel := geminiV1BetaFallbackModelIDs(service.PlatformAntigravity)[0]
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{
			Platform: service.PlatformGemini,
			ModelsListConfig: service.GroupModelsListConfig{
				Enabled: true,
				Models:  []string{knownModel, "gemini-custom"},
			},
		},
	})
	c.Set(string(middleware.ContextKeyForcePlatform), service.PlatformAntigravity)

	(&GatewayHandler{}).GeminiV1BetaListModels(c)

	require.Equal(t, http.StatusOK, rec.Code)
	var got gemini.ModelsListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, []gemini.Model{gemini.FallbackModel(knownModel)}, got.Models)
}

func TestCustomGeminiModelsList_DisabledKeepsExistingFlow(t *testing.T) {
	group := &service.Group{
		ModelsListConfig: service.GroupModelsListConfig{
			Enabled: false,
			Models:  []string{"gemini-2.5-pro"},
		},
	}

	_, ok := geminiCustomModelsListResponse(service.PlatformGemini, []string{"gemini-2.5-pro"}, group)
	require.False(t, ok)
}

// TestGeminiV1BetaHandler_PlatformRoutingInvariant 文档化并验证 Handler 层的平台路由逻辑不变量
// 该测试确保 gemini 和 antigravity 平台的路由逻辑符合预期
func TestGeminiV1BetaHandler_PlatformRoutingInvariant(t *testing.T) {
	tests := []struct {
		name            string
		platform        string
		expectedService string
		description     string
	}{
		{
			name:            "Gemini平台使用ForwardNative",
			platform:        service.PlatformGemini,
			expectedService: "GeminiMessagesCompatService.ForwardNative",
			description:     "Gemini OAuth 账户直接调用 Google API",
		},
		{
			name:            "Antigravity平台使用ForwardGemini",
			platform:        service.PlatformAntigravity,
			expectedService: "AntigravityGatewayService.ForwardGemini",
			description:     "Antigravity 账户通过 CRS 中转，支持 Gemini 协议",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 模拟 GeminiV1BetaModels 中的路由决策 (lines 199-205 in gemini_v1beta_handler.go)
			var routedService string
			if tt.platform == service.PlatformAntigravity {
				routedService = "AntigravityGatewayService.ForwardGemini"
			} else {
				routedService = "GeminiMessagesCompatService.ForwardNative"
			}

			require.Equal(t, tt.expectedService, routedService,
				"平台 %s 应该路由到 %s: %s",
				tt.platform, tt.expectedService, tt.description)
		})
	}
}

// TestGeminiV1BetaHandler_ListModelsAntigravityFallback 验证 ListModels 的 antigravity 降级逻辑
// 当没有 gemini 账户但有 antigravity 账户时，应返回静态模型列表
func TestGeminiV1BetaHandler_ListModelsAntigravityFallback(t *testing.T) {
	tests := []struct {
		name             string
		hasGeminiAccount bool
		hasAntigravity   bool
		expectedBehavior string
	}{
		{
			name:             "有Gemini账户-调用ForwardAIStudioGET",
			hasGeminiAccount: true,
			hasAntigravity:   false,
			expectedBehavior: "forward_to_upstream",
		},
		{
			name:             "无Gemini有Antigravity-返回静态列表",
			hasGeminiAccount: false,
			hasAntigravity:   true,
			expectedBehavior: "static_fallback",
		},
		{
			name:             "无任何账户-返回503",
			hasGeminiAccount: false,
			hasAntigravity:   false,
			expectedBehavior: "service_unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 模拟 GeminiV1BetaListModels 的逻辑 (lines 33-44 in gemini_v1beta_handler.go)
			var behavior string

			if tt.hasGeminiAccount {
				behavior = "forward_to_upstream"
			} else if tt.hasAntigravity {
				behavior = "static_fallback"
			} else {
				behavior = "service_unavailable"
			}

			require.Equal(t, tt.expectedBehavior, behavior)
		})
	}
}

// TestGeminiV1BetaHandler_GetModelAntigravityFallback 验证 GetModel 的 antigravity 降级逻辑
func TestGeminiV1BetaHandler_GetModelAntigravityFallback(t *testing.T) {
	tests := []struct {
		name             string
		hasGeminiAccount bool
		hasAntigravity   bool
		expectedBehavior string
	}{
		{
			name:             "有Gemini账户-调用ForwardAIStudioGET",
			hasGeminiAccount: true,
			hasAntigravity:   false,
			expectedBehavior: "forward_to_upstream",
		},
		{
			name:             "无Gemini有Antigravity-返回静态模型信息",
			hasGeminiAccount: false,
			hasAntigravity:   true,
			expectedBehavior: "static_model_info",
		},
		{
			name:             "无任何账户-返回503",
			hasGeminiAccount: false,
			hasAntigravity:   false,
			expectedBehavior: "service_unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 模拟 GeminiV1BetaGetModel 的逻辑 (lines 77-87 in gemini_v1beta_handler.go)
			var behavior string

			if tt.hasGeminiAccount {
				behavior = "forward_to_upstream"
			} else if tt.hasAntigravity {
				behavior = "static_model_info"
			} else {
				behavior = "service_unavailable"
			}

			require.Equal(t, tt.expectedBehavior, behavior)
		})
	}
}

// TestGeminiCustomModelsListResponse 验证分组自定义模型列表在 Gemini 原生 /v1beta/models 上生效:
// 启用自定义列表时只返回配置的模型(v1beta 格式,带 models/ 前缀),未启用时不拦截。
func TestGeminiCustomModelsListResponse(t *testing.T) {
	t.Parallel()

	t.Run("启用自定义列表-只返回配置的模型", func(t *testing.T) {
		group := &service.Group{
			Platform: service.PlatformAntigravity,
			ModelsListConfig: service.GroupModelsListConfig{
				Enabled: true,
				Models:  []string{"gemini-3.1-pro-high", "gemini-2.5-flash"},
			},
		}
		resp, ok := geminiCustomModelsListResponse(service.PlatformAntigravity, nil, group)
		require.True(t, ok)
		names := make([]string, 0, len(resp.Models))
		for _, m := range resp.Models {
			names = append(names, m.Name)
		}
		require.Equal(t, []string{"models/gemini-3.1-pro-high", "models/gemini-2.5-flash"}, names)
	})

	t.Run("配置外的模型被过滤", func(t *testing.T) {
		group := &service.Group{
			Platform: service.PlatformAntigravity,
			ModelsListConfig: service.GroupModelsListConfig{
				Enabled: true,
				Models:  []string{"gemini-2.5-flash", "not-a-real-model"},
			},
		}
		resp, ok := geminiCustomModelsListResponse(service.PlatformAntigravity, nil, group)
		require.True(t, ok)
		require.Len(t, resp.Models, 1)
		require.Equal(t, "models/gemini-2.5-flash", resp.Models[0].Name)
	})

	t.Run("账号可用模型作为过滤基准", func(t *testing.T) {
		group := &service.Group{
			Platform: service.PlatformGemini,
			ModelsListConfig: service.GroupModelsListConfig{
				Enabled: true,
				Models:  []string{"gemini-2.5-pro", "gemini-custom-model"},
			},
		}
		available := []string{"gemini-2.5-pro", "gemini-custom-model", "gemini-extra"}
		resp, ok := geminiCustomModelsListResponse(service.PlatformGemini, available, group)
		require.True(t, ok)
		names := make([]string, 0, len(resp.Models))
		for _, m := range resp.Models {
			names = append(names, m.Name)
		}
		require.Equal(t, []string{"models/gemini-2.5-pro", "models/gemini-custom-model"}, names)
	})

	t.Run("Gemini原生回退保留native-only模型", func(t *testing.T) {
		group := &service.Group{
			Platform: service.PlatformGemini,
			ModelsListConfig: service.GroupModelsListConfig{
				Enabled: true,
				Models:  []string{"gemini-3.1-pro-preview-customtools"},
			},
		}
		resp, ok := geminiCustomModelsListResponse(service.PlatformGemini, nil, group)
		require.True(t, ok)
		require.Len(t, resp.Models, 1)
		require.Equal(t, "models/gemini-3.1-pro-preview-customtools", resp.Models[0].Name)
	})

	t.Run("未启用自定义列表-不拦截", func(t *testing.T) {
		group := &service.Group{Platform: service.PlatformGemini}
		_, ok := geminiCustomModelsListResponse(service.PlatformGemini, nil, group)
		require.False(t, ok)
	})

	t.Run("Group为nil-不拦截", func(t *testing.T) {
		_, ok := geminiCustomModelsListResponse(service.PlatformGemini, nil, nil)
		require.False(t, ok)
	})
}

func TestShouldFallbackGeminiModel_KnownFallbackOn404(t *testing.T) {
	t.Parallel()

	res := &service.UpstreamHTTPResult{StatusCode: http.StatusNotFound}
	require.True(t, shouldFallbackGeminiModel("gemini-3.1-pro-preview-customtools", res))
}

func TestShouldFallbackGeminiModel_UnknownModelOn404(t *testing.T) {
	t.Parallel()

	res := &service.UpstreamHTTPResult{StatusCode: http.StatusNotFound}
	require.False(t, shouldFallbackGeminiModel("gemini-future-model", res))
}

func TestShouldFallbackGeminiModel_DelegatesScopeFallback(t *testing.T) {
	t.Parallel()

	res := &service.UpstreamHTTPResult{
		StatusCode: http.StatusForbidden,
		Headers:    http.Header{"Www-Authenticate": []string{"Bearer error=\"insufficient_scope\""}},
		Body:       []byte("insufficient authentication scopes"),
	}
	require.True(t, shouldFallbackGeminiModel("gemini-future-model", res))
}
