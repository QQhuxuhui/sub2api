package admin

import (
	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// OpenAIFailoverHandler 管理端「换号规则」页 OpenAI 标签：规则表、分组开关与换号统计。
type OpenAIFailoverHandler struct {
	policyService *service.OpenAIFailoverPolicyService
}

// NewOpenAIFailoverHandler 构造。
func NewOpenAIFailoverHandler(policyService *service.OpenAIFailoverPolicyService) *OpenAIFailoverHandler {
	return &OpenAIFailoverHandler{policyService: policyService}
}

// Overview GET /admin/openai-failover
func (h *OpenAIFailoverHandler) Overview(c *gin.Context) {
	overview, err := h.policyService.Overview(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, overview)
}

// UpdateConfig PUT /admin/openai-failover
func (h *OpenAIFailoverHandler) UpdateConfig(c *gin.Context) {
	var cfg service.OpenAIFailoverConfig
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
