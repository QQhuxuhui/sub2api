package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// 单用户规则条数上限。规则语义是「分组 × 模型」的白名单，正常配置一只手数得过来；
// 上限只是挡住失控的自动化调用把整表灌爆。
const maxEmptyResponseBillingRulesPerUser = 50

// EmptyResponseBillingRuleView 是规则的 API 表现形式。
type EmptyResponseBillingRuleView struct {
	ID        int64  `json:"id"`
	GroupID   *int64 `json:"group_id"`
	Model     string `json:"model"`
	Enabled   bool   `json:"enabled"`
	Note      string `json:"note"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// EmptyResponseBillingRuleInput 是全量替换时的单条输入。
type EmptyResponseBillingRuleInput struct {
	GroupID *int64 `json:"group_id"`
	Model   string `json:"model"`
	Enabled *bool  `json:"enabled"`
	Note    string `json:"note"`
}

// UpdateEmptyResponseBillingRulesRequest PUT body：全量替换该用户的规则集。
type UpdateEmptyResponseBillingRulesRequest struct {
	Rules []EmptyResponseBillingRuleInput `json:"rules"`
}

func emptyResponseBillingRuleViews(rules []service.EmptyResponseBillingRule) []EmptyResponseBillingRuleView {
	views := make([]EmptyResponseBillingRuleView, 0, len(rules))
	for i := range rules {
		rule := &rules[i]
		views = append(views, EmptyResponseBillingRuleView{
			ID:        rule.ID,
			GroupID:   rule.GroupID,
			Model:     rule.Model,
			Enabled:   rule.Enabled,
			Note:      rule.Note,
			CreatedAt: rule.CreatedAt.Format(time.RFC3339),
			UpdatedAt: rule.UpdatedAt.Format(time.RFC3339),
		})
	}
	return views
}

// GetUserEmptyResponseBillingRules GET /admin/users/:id/empty-response-billing-rules
func (h *UserHandler) GetUserEmptyResponseBillingRules(c *gin.Context) {
	if h.emptyResponseBillingRepo == nil {
		response.Error(c, 503, "empty-response billing rules not available")
		return
	}
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}
	// 与 platform-quotas 同口径：用户不存在返回 404 而非空列表，避免管理页误判。
	if _, err := h.adminService.GetUser(c.Request.Context(), userID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	rules, err := h.emptyResponseBillingRepo.ListByUserID(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, map[string]any{"rules": emptyResponseBillingRuleViews(rules)})
}

// UpdateUserEmptyResponseBillingRules PUT /admin/users/:id/empty-response-billing-rules
// 全量替换该用户的「空返回不扣费」规则集。
func (h *UserHandler) UpdateUserEmptyResponseBillingRules(c *gin.Context) {
	if h.emptyResponseBillingRepo == nil {
		response.Error(c, 503, "empty-response billing rules not available")
		return
	}
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}
	var req UpdateEmptyResponseBillingRulesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if len(req.Rules) > maxEmptyResponseBillingRulesPerUser {
		response.BadRequest(c, "too many rules (max "+strconv.Itoa(maxEmptyResponseBillingRulesPerUser)+")")
		return
	}
	if _, err := h.adminService.GetUser(c.Request.Context(), userID); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	rules := make([]service.EmptyResponseBillingRule, 0, len(req.Rules))
	// 去重口径与 DB 唯一索引一致：(group_id 折叠 NULL, lower(model))。
	// 在这里拦下重复能给出可读的报错，而不是把 pq duplicate key 直接甩给前端。
	seen := make(map[string]struct{}, len(req.Rules))
	for i := range req.Rules {
		input := &req.Rules[i]
		model := strings.TrimSpace(input.Model)
		if len(model) > 200 {
			response.BadRequest(c, "model name too long (max 200)")
			return
		}
		if input.GroupID != nil && *input.GroupID <= 0 {
			response.BadRequest(c, "group_id must be a positive id or null")
			return
		}
		key := "0"
		if input.GroupID != nil {
			key = strconv.FormatInt(*input.GroupID, 10)
		}
		key += "|" + strings.ToLower(model)
		if _, dup := seen[key]; dup {
			response.BadRequest(c, "duplicate rule for the same group/model")
			return
		}
		seen[key] = struct{}{}

		enabled := true
		if input.Enabled != nil {
			enabled = *input.Enabled
		}
		rules = append(rules, service.EmptyResponseBillingRule{
			UserID:  userID,
			GroupID: input.GroupID,
			Model:   model,
			Enabled: enabled,
			Note:    strings.TrimSpace(input.Note),
		})
	}

	if err := h.emptyResponseBillingRepo.ReplaceByUserID(c.Request.Context(), userID, rules); err != nil {
		// group_id 指向不存在的分组会撞外键；转成 400 而不是 500。
		if strings.Contains(err.Error(), "violates foreign key constraint") {
			response.BadRequest(c, "group_id does not exist")
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	// 保留失效钩子调用，兼容未来可能引入的共享缓存实现。
	for _, invalidator := range h.emptyResponseBillingInvalidators {
		if invalidator != nil {
			invalidator.InvalidateEmptyResponseBillingRules(userID)
		}
	}

	saved, err := h.emptyResponseBillingRepo.ListByUserID(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, map[string]any{"rules": emptyResponseBillingRuleViews(saved)})
}
