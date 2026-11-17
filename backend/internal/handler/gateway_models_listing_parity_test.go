//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 模型广场用 service.GatewayService.ListingModelIDsForGroup 枚举分组模型，它是
// GatewayHandler.Models（GET /v1/models）非固定清单、非 composite 路径的等价副本。
// 本测试直接对比两者的输出：上游改了 /v1/models 的枚举或回落规则而副本没跟上时，这里会失败，
// 届时请同步 internal/service/model_plaza_listing.go。
func TestModelPlazaListingMatchesGatewayModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const groupID int64 = 9101

	type scenario struct {
		name     string
		accounts func(platform string) []service.Account
		only     string // 非空时只对该平台生效
	}
	scenarios := []scenario{
		{name: "no accounts (nil listing)", accounts: func(string) []service.Account { return nil }},
		{name: "accounts without mapping", accounts: func(platform string) []service.Account {
			return []service.Account{{ID: 1, Platform: platform, Type: service.AccountTypeAPIKey}}
		}},
		{name: "mapped models", accounts: func(platform string) []service.Account {
			return []service.Account{{ID: 1, Platform: platform, Type: service.AccountTypeAPIKey, Credentials: map[string]any{
				"model_mapping": map[string]any{"custom-b": "up-b", "custom-a": "up-a"},
			}}}
		}},
		{name: "all mapped entries retired (non-nil empty listing)", only: service.PlatformOpenAI, accounts: func(platform string) []service.Account {
			return []service.Account{{ID: 1, Platform: platform, Type: service.AccountTypeOAuth, Credentials: map[string]any{
				"model_mapping": map[string]any{"gpt-5.5": "gpt-5.5"},
			}}}
		}},
	}
	allowlists := []struct {
		name      string
		allowlist service.GroupModelAllowlist
	}{
		{"allowlist off", service.GroupModelAllowlist{}},
		{"allowlist on", service.GroupModelAllowlist{Enabled: true, Models: []string{"custom-*", "gpt-*", "claude-*", "gemini-*", "grok-*", "jev-*"}}},
		{"allowlist on but empty", service.GroupModelAllowlist{Enabled: true}},
	}
	platforms := []string{
		service.PlatformOpenAI, service.PlatformGemini, service.PlatformAnthropic, service.PlatformAntigravity,
		service.PlatformOpenCodeGo, service.PlatformGrok, service.PlatformTypeSafe,
	}

	for _, platform := range platforms {
		for _, sc := range scenarios {
			if sc.only != "" && sc.only != platform {
				continue
			}
			for _, al := range allowlists {
				t.Run(platform+"/"+sc.name+"/"+al.name, func(t *testing.T) {
					newRepo := func() *gatewayModelsAccountRepoStub {
						return &gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{groupID: sc.accounts(platform)}}
					}
					group := &service.Group{ID: groupID, Platform: platform, ModelAllowlist: al.allowlist}

					// 两边各用全新的服务实例，避免模型清单缓存让第二次调用拿到不同形态的结果。
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
					c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{Group: group})
					newGatewayModelsHandlerForTest(newRepo()).Models(c)
					require.Equal(t, http.StatusOK, rec.Code)
					var listed struct {
						Data []struct {
							ID string `json:"id"`
						} `json:"data"`
					}
					require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed), rec.Body.String())
					want := make([]string, 0, len(listed.Data))
					for _, item := range listed.Data {
						want = append(want, item.ID)
					}

					got := newGatewayModelsHandlerForTest(newRepo()).gatewayService.ListingModelIDsForGroup(context.Background(), group)
					if got == nil {
						got = []string{}
					}
					require.Equal(t, want, got)
				})
			}
		}
	}
}
