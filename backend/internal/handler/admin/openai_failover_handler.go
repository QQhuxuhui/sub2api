package admin

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// OpenAIFailoverHandler 管理端「换号规则」页：规则表、分组开关、命中统计与
// 「不支持加密参数」账号标记。
type OpenAIFailoverHandler struct {
	policyService *service.OpenAIFailoverPolicyService
	adminService  service.AdminService
}

// NewOpenAIFailoverHandler 构造。
func NewOpenAIFailoverHandler(policyService *service.OpenAIFailoverPolicyService, adminService service.AdminService) *OpenAIFailoverHandler {
	return &OpenAIFailoverHandler{policyService: policyService, adminService: adminService}
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

func (h *OpenAIFailoverHandler) openAIAccountID(c *gin.Context) (int64, bool) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return 0, false
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return 0, false
	}
	if account.Platform != service.PlatformOpenAI {
		response.BadRequest(c, "only available for OpenAI accounts")
		return 0, false
	}
	return accountID, true
}

// UpdateEncryptedArgsRequest 标记账号不支持加密参数。
type UpdateEncryptedArgsRequest struct {
	NoEncryptedArgs bool `json:"no_encrypted_args"`
}

// UpdateEncryptedArgs PUT /admin/openai-failover/accounts/:id/encrypted-args
// 只对 Extra 做 key 级合并，不影响账号其他配置。
func (h *OpenAIFailoverHandler) UpdateEncryptedArgs(c *gin.Context) {
	accountID, ok := h.openAIAccountID(c)
	if !ok {
		return
	}
	var req UpdateEncryptedArgsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	updates := map[string]any{service.OpenAINoEncryptedArgsExtraKey: req.NoEncryptedArgs}
	if err := h.adminService.UpdateAccountExtra(c.Request.Context(), accountID, updates); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"no_encrypted_args": req.NoEncryptedArgs})
}

// CheckEncryptedArgsRequest 检测使用的模型，留空按账号映射自动选。
type CheckEncryptedArgsRequest struct {
	Model string `json:"model"`
}

// CheckEncryptedArgs POST /admin/openai-failover/accounts/:id/encrypted-args/check
// 会真实调用上游 3 次（极少量 token）。只给出结果，不会自动改标记。
func (h *OpenAIFailoverHandler) CheckEncryptedArgs(c *gin.Context) {
	accountID, ok := h.openAIAccountID(c)
	if !ok {
		return
	}
	var req CheckEncryptedArgsRequest
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			response.BadRequest(c, "Invalid request: "+err.Error())
			return
		}
	}
	result, err := h.policyService.CheckEncryptedArgs(c.Request.Context(), accountID, req.Model)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
