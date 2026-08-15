package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/teamops"

	"github.com/gin-gonic/gin"
)

// registerTeamRoutes 注册团队消耗看板路由（挂在已认证的用户路由组下）。
//
// teamLimit 必须是一个**独立计数桶**的重查询限流器，不能直接传 PanelRateLimiter.Heavy()：
// Heavy 的计数键对全站重查询端点是同一个，本页与 /usage、/dashboard 混在一起之后，
// 用户在本页刷几下就会把另一个标签页里的既有功能打成 429。
func registerTeamRoutes(authenticated *gin.RouterGroup, h *teamops.Handler, teamLimit gin.HandlerFunc) {
	team := authenticated.Group("/user/team")
	team.Use(teamLimit)
	{
		team.GET("/summary", h.GetSummary)
		team.GET("/rows", h.ListRows)
	}
}
