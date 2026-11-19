package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// Health 返回模型广场各线路的健康摘要（最近 24 个有调用的小时）与热度名次。
// GET /api/v1/model-plaza/health
//
// 广场扩展接口，独立于渠道监控 V2 的用户接口：
//   - 必须登录；数据范围 = 用户可用分组（与 V2 用户接口同一来源）；
//   - 监控未启用或不是 V2 模式时返回 available=false，前端据此只展示价格；
//   - 返回体是白名单结构，不含任何请求数。
func (h *ModelPlazaHandler) Health(c *gin.Context) {
	if h.settingService == nil || !h.settingService.GetModelPlazaRuntime(c.Request.Context()).Enabled {
		response.NotFound(c, "Model plaza is not enabled")
		return
	}
	subject, authed := middleware.GetAuthSubjectFromContext(c)
	if !authed || subject.UserID <= 0 {
		response.Unauthorized(c, "Authentication required")
		return
	}
	unavailable := service.PlazaHealth{Available: false, Lines: []service.PlazaHealthLine{}}
	if !h.settingService.GetChannelMonitorRuntime(c.Request.Context()).PassiveAggregationAllowed() {
		response.Success(c, unavailable)
		return
	}
	if h.apiKeyService == nil || h.plazaService == nil {
		response.Success(c, unavailable)
		return
	}
	groups, err := h.apiKeyService.GetAvailableGroups(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// 范围与主清单一致：登录账号可用的全部分组（含已授权的专属分组）。
	ids := make([]int64, 0, len(groups))
	for i := range groups {
		ids = append(ids, groups[i].ID)
	}
	health, err := h.plazaService.Health(c.Request.Context(), ids)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// 响应因用户而异，禁止中间缓存。
	c.Header("Cache-Control", "private, no-store")
	response.Success(c, health)
}
