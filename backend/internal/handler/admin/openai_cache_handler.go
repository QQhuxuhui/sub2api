package admin

import (
	"net/http"
	"strconv"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// OpenAICacheHandler 管理端「上游缓存」页：OpenAI 账号的真实/展示缓存率与缓存模拟让利。
type OpenAICacheHandler struct {
	statsService *service.OpenAICacheStatsService
	adminService service.AdminService
}

// NewOpenAICacheHandler 构造。
func NewOpenAICacheHandler(statsService *service.OpenAICacheStatsService, adminService service.AdminService) *OpenAICacheHandler {
	return &OpenAICacheHandler{statsService: statsService, adminService: adminService}
}

// openAICacheMaxRangeDays 自定义范围上限，防止大表全扫。
const openAICacheMaxRangeDays = 90

func (h *OpenAICacheHandler) timeRange(c *gin.Context) (time.Time, time.Time, bool) {
	startTime, endTime := parseTimeRange(c)
	// Include the end calendar date even when a DST transition makes it
	// shorter or longer than 24 hours.
	if endDate := c.Query("end_date"); endDate != "" {
		if end, err := timezone.ParseInUserLocation("2006-01-02", endDate, c.Query("timezone")); err == nil {
			endTime = end.AddDate(0, 0, 1)
		}
	}
	if !endTime.After(startTime) {
		response.BadRequest(c, "end_date must be after start_date")
		return startTime, endTime, false
	}
	if endTime.Sub(startTime) > (openAICacheMaxRangeDays+1)*24*time.Hour {
		response.BadRequest(c, "date range must not exceed 90 days")
		return startTime, endTime, false
	}
	return startTime, endTime, true
}

// ListAccounts GET /admin/openai-cache/accounts?start_date&end_date&timezone
func (h *OpenAICacheHandler) ListAccounts(c *gin.Context) {
	startTime, endTime, ok := h.timeRange(c)
	if !ok {
		return
	}
	result, err := h.statsService.ListAccounts(c.Request.Context(), startTime, endTime, startTime.Location())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// ListDaily GET /admin/openai-cache/daily?start_date&end_date&timezone&account_id&granularity=day|hour|5min|minute
// 分钟级只在范围不超过 2 天时生效，超出自动降为 5 分钟；返回里带实际生效的 granularity。
func (h *OpenAICacheHandler) ListDaily(c *gin.Context) {
	startTime, endTime, ok := h.timeRange(c)
	if !ok {
		return
	}
	var accountID int64
	if raw := c.Query("account_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id < 0 {
			response.BadRequest(c, "Invalid account_id")
			return
		}
		accountID = id
	}
	granularity := service.NormalizeOpenAICacheGranularity(c.Query("granularity"), endTime.Sub(startTime))
	rows, err := h.statsService.ListTrend(c.Request.Context(), startTime, endTime, startTime.Location(), accountID, granularity)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"days": rows, "granularity": granularity})
}

// UpdateSimulationRequest 页面内快捷修改缓存模拟配置的请求体。
type UpdateSimulationRequest struct {
	Enabled         bool    `json:"enabled"`
	Lo              float64 `json:"lo"`
	Hi              float64 `json:"hi"`
	HalfLifeMinutes float64 `json:"half_life_minutes"`
	Cap             float64 `json:"cap"`
}

// UpdateSimulation PUT /admin/openai-cache/accounts/:id/simulation
// 只对 Extra 做 key 级合并（UpdateAccountExtra），不会像通用账号更新那样整体替换 Extra，
// 所以本页面可以安全地只提交 openai_cache_simulation 一项。影子账号在创建时继承父账号配置，不单独改。
func (h *OpenAICacheHandler) UpdateSimulation(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	var req UpdateSimulationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	ctx := c.Request.Context()
	account, err := h.adminService.GetAccount(ctx, accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if account.Platform != service.PlatformOpenAI {
		response.BadRequest(c, "cache simulation is only available for OpenAI accounts")
		return
	}
	if account.IsShadow() {
		response.ErrorFrom(c, infraerrors.New(http.StatusBadRequest, "OPENAI_CACHE_SIMULATION_SHADOW",
			"spark shadow accounts inherit cache simulation from the parent account"))
		return
	}
	updates := map[string]any{
		service.OpenAICacheSimulationExtraKey: map[string]any{
			"enabled":           req.Enabled,
			"lo":                req.Lo,
			"hi":                req.Hi,
			"half_life_minutes": req.HalfLifeMinutes,
			"cap":               req.Cap,
		},
	}
	if err := service.ValidateOpenAICacheSimulationExtra(account.Platform, updates); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if err := h.adminService.UpdateAccountExtra(ctx, accountID, updates); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	updated, err := h.adminService.GetAccount(ctx, accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, service.NewOpenAICacheSimConfigView(updated.OpenAICacheSimulation()))
}
