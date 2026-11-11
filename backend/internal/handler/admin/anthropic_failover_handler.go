package admin

import (
	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// AnthropicFailoverHandler 管理端「Anthropic 换号规则」页：规则表、分组开关与换号统计。
type AnthropicFailoverHandler struct {
	policyService *service.AnthropicFailoverPolicyService
}

// NewAnthropicFailoverHandler 构造。
func NewAnthropicFailoverHandler(policyService *service.AnthropicFailoverPolicyService) *AnthropicFailoverHandler {
	return &AnthropicFailoverHandler{policyService: policyService}
}

// Overview GET /admin/anthropic-failover
func (h *AnthropicFailoverHandler) Overview(c *gin.Context) {
	overview, err := h.policyService.Overview(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, overview)
}

// UpdateConfig PUT /admin/anthropic-failover
func (h *AnthropicFailoverHandler) UpdateConfig(c *gin.Context) {
	var cfg service.AnthropicFailoverConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	saved, err := h.policyService.UpdateConfig(c.Request.Context(), cfg)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, saved)
}
