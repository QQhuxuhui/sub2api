//go:build unit

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newPlazaHealthHandlerForTest(values map[string]string) *ModelPlazaHandler {
	settings := service.NewSettingService(&settingHandlerPublicRepoStub{values: values}, &config.Config{})
	return NewModelPlazaHandler(service.NewModelPlazaService(nil, nil, nil, nil, nil), nil, settings)
}

func callPlazaHealth(h *ModelPlazaHandler, userID int64) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/model-plaza/health?v=2", nil)
	if userID > 0 {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: userID})
	}
	h.Health(c)
	return rec
}

func TestModelPlazaHealth_NotFoundWhenPlazaDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := callPlazaHealth(newPlazaHealthHandlerForTest(map[string]string{}), 7)
	require.Equal(t, http.StatusNotFound, rec.Code)

	rec = callPlazaHealth(&ModelPlazaHandler{}, 7)
	require.Equal(t, http.StatusNotFound, rec.Code, "未接设置服务时按未启用处理")
}

func TestModelPlazaHealth_RequiresLogin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newPlazaHealthHandlerForTest(map[string]string{
		service.SettingKeyModelPlazaEnabled:     "true",
		service.SettingKeyChannelMonitorMode:    service.ChannelMonitorModeV2,
		service.SettingKeyChannelMonitorEnabled: "true",
	})
	rec := callPlazaHealth(h, 0)
	require.Equal(t, http.StatusUnauthorized, rec.Code, "未登录访客拿不到任何健康数据")
	require.NotContains(t, rec.Body.String(), "lines")
}

func TestModelPlazaHealth_UnavailableWhenMonitorNotPassive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, values := range map[string]map[string]string{
		"v1 mode": {
			service.SettingKeyModelPlazaEnabled:     "true",
			service.SettingKeyChannelMonitorEnabled: "true",
			service.SettingKeyChannelMonitorMode:    service.ChannelMonitorModeV1,
		},
		"monitor disabled": {
			service.SettingKeyModelPlazaEnabled:     "true",
			service.SettingKeyChannelMonitorEnabled: "false",
			service.SettingKeyChannelMonitorMode:    service.ChannelMonitorModeV2,
		},
	} {
		t.Run(name, func(t *testing.T) {
			rec := callPlazaHealth(newPlazaHealthHandlerForTest(values), 7)
			require.Equal(t, http.StatusOK, rec.Code)
			var body struct {
				Data struct {
					Available bool  `json:"available"`
					Lines     []any `json:"lines"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.False(t, body.Data.Available)
			require.Empty(t, body.Data.Lines)
		})
	}
}

func TestModelPlazaHealth_UnversionedRequestGetsUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	src := &plazaViewHealthSource{}
	h := newPlazaViewHandler(&service.User{ID: 7, Role: service.RoleUser}, nil, nil, src)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	// 发版前已打开的旧页面：不带格式版本参数。
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/model-plaza/health", nil)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7})
	h.Health(c)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"available":false`)
	require.Zero(t, src.calls, "旧格式请求不查询健康数据")
}
