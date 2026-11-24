package admin

import (
	"log/slog"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"

	"github.com/gin-gonic/gin"
)

// gptniubi 二开：OpenAI 分组优先级严格分档开关（见 service/openai_priority_tier.go）。

// OpenAIPriorityTierSettings 开启严格分档的分组 ID 列表。
type OpenAIPriorityTierSettings struct {
	GroupIDs []int64 `json:"group_ids"`
}

// GetOpenAIPriorityTierSettings
// GET /api/v1/admin/settings/openai-priority-tier
func (h *SettingHandler) GetOpenAIPriorityTierSettings(c *gin.Context) {
	ids, err := h.settingService.GetOpenAIPriorityTierGroupIDs(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, OpenAIPriorityTierSettings{GroupIDs: ids})
}

// UpdateOpenAIPriorityTierSettings
// PUT /api/v1/admin/settings/openai-priority-tier
func (h *SettingHandler) UpdateOpenAIPriorityTierSettings(c *gin.Context) {
	var req struct {
		GroupIDs *[]int64 `json:"group_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if req.GroupIDs == nil {
		response.BadRequest(c, "Invalid request: group_ids is required")
		return
	}
	ctx := c.Request.Context()
	previous, _ := h.settingService.GetOpenAIPriorityTierGroupIDs(ctx)
	saved, err := h.settingService.SetOpenAIPriorityTierGroupIDs(ctx, *req.GroupIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	slog.Info("openai_priority_tier_groups_updated", "before", previous, "after", saved)
	response.Success(c, OpenAIPriorityTierSettings{GroupIDs: saved})
}
