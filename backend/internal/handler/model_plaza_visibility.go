package handler

import (
	"log/slog"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// 本文件是模型广场「按登录账号权限展示分组」的扩展，独立于上游的 model_plaza_handler.go：
// 上游 Get 里只有一个挂点（plazaViewFor 调用）。

// plazaViewFor 按请求身份裁剪广场分组，并给出登录用户的个人倍率。
//   - 匿名：只有非专属分组（上游规则），无个人倍率；
//   - 登录：账号可绑定的分组，即 APIKeyService.GetAvailableGroups（创建令牌时可选的分组，
//     健康接口同源）与广场清单的交集，含已授权的专属分组；权限拿不到时报错，不降级成
//     匿名视图。
//
// 同一 URL 靠 Authorization 区分匿名与登录，响应声明 Vary: Authorization；登录响应因人而异，
// 禁止中间缓存。
func (h *ModelPlazaHandler) plazaViewFor(
	c *gin.Context,
	groups []service.PlazaGroup,
	authed bool,
	userID int64,
) ([]service.PlazaGroup, map[int64]float64, error) {
	c.Writer.Header().Add("Vary", "Authorization")
	if !authed {
		return filterPlazaVisibleGroups(groups, nil, false), nil, nil
	}
	c.Header("Cache-Control", "private, no-store")
	if h.apiKeyService == nil {
		return []service.PlazaGroup{}, nil, nil
	}
	available, err := h.apiKeyService.GetAvailableGroups(c.Request.Context(), userID)
	if err != nil {
		return nil, nil, err
	}
	userRates, err := h.apiKeyService.GetUserGroupRates(c.Request.Context(), userID)
	if err != nil {
		// 专属倍率仅是展示增强，失败降级为分组默认倍率。
		slog.Warn("model_plaza_user_rates_failed", "error", err, "user_id", userID)
		userRates = nil
	}
	return intersectPlazaGroups(groups, available), userRates, nil
}

// intersectPlazaGroups 保留 groups 中落在 available 里的分组，顺序不变。
func intersectPlazaGroups(groups []service.PlazaGroup, available []service.Group) []service.PlazaGroup {
	allowed := make(map[int64]struct{}, len(available))
	for i := range available {
		allowed[available[i].ID] = struct{}{}
	}
	visible := make([]service.PlazaGroup, 0, len(groups))
	for _, g := range groups {
		if _, ok := allowed[g.ID]; ok {
			visible = append(visible, g)
		}
	}
	return visible
}
