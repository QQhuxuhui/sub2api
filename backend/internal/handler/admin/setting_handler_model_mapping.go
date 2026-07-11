package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// GetModelMappingTemplate returns a platform's write-time model mapping template.
func (h *SettingHandler) GetModelMappingTemplate(c *gin.Context) {
	platform := c.Param("platform")
	mapping, err := h.settingService.GetModelMappingTemplate(c.Request.Context(), platform)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, mapping)
}

type UpdateModelMappingTemplateRequest struct {
	Mapping map[string]string `json:"mapping"`
}

// UpdateModelMappingTemplate replaces a platform's write-time model mapping template.
func (h *SettingHandler) UpdateModelMappingTemplate(c *gin.Context) {
	platform := c.Param("platform")
	var req UpdateModelMappingTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	if err := h.settingService.SetModelMappingTemplate(c.Request.Context(), platform, req.Mapping); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	updated, err := h.settingService.GetModelMappingTemplate(c.Request.Context(), platform)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, updated)
}
