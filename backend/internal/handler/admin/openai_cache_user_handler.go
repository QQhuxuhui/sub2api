package admin

import (
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// By-user cache simulation endpoints of the 「上游缓存」page. Date ranges are
// always read in Asia/Shanghai (the browser time zone is ignored) so the list
// and the trend agree with the dashboard acceptance query.

func (h *OpenAICacheHandler) userTimeRange(c *gin.Context) (time.Time, time.Time, bool) {
	loc := service.OpenAICacheSimShanghai()
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	start, end := today, today.AddDate(0, 0, 1)
	if v := c.Query("start_date"); v != "" {
		t, err := time.ParseInLocation("2006-01-02", v, loc)
		if err != nil {
			response.BadRequest(c, "invalid start_date")
			return start, end, false
		}
		start = t
	}
	if v := c.Query("end_date"); v != "" {
		t, err := time.ParseInLocation("2006-01-02", v, loc)
		if err != nil {
			response.BadRequest(c, "invalid end_date")
			return start, end, false
		}
		end = t.AddDate(0, 0, 1)
	}
	if !end.After(start) {
		response.BadRequest(c, "end_date must be after start_date")
		return start, end, false
	}
	if end.Sub(start) > (openAICacheMaxRangeDays+1)*24*time.Hour {
		response.BadRequest(c, "date range must not exceed 90 days")
		return start, end, false
	}
	return start, end, true
}

// ListUsers GET /admin/openai-cache/users?start_date&end_date
func (h *OpenAICacheHandler) ListUsers(c *gin.Context) {
	start, end, ok := h.userTimeRange(c)
	if !ok {
		return
	}
	result, err := h.statsService.ListUsers(c.Request.Context(), h.settingService, start, end)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// ListUserTrend GET /admin/openai-cache/user-trend?user_id&start_date&end_date&granularity
func (h *OpenAICacheHandler) ListUserTrend(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Query("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		response.BadRequest(c, "Invalid user ID")
		return
	}
	start, end, ok := h.userTimeRange(c)
	if !ok {
		return
	}
	granularity := service.NormalizeOpenAICacheGranularity(c.Query("granularity"), end.Sub(start))
	rows, err := h.statsService.ListUserTrend(c.Request.Context(), h.settingService, start, end, userID, granularity)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"rows": rows, "granularity": granularity, "timezone": service.OpenAICacheSimShanghai().String()})
}

// GetGlobalSimulation GET /admin/openai-cache/global
func (h *OpenAICacheHandler) GetGlobalSimulation(c *gin.Context) {
	cfg, err := h.settingService.GetOpenAICacheSimGlobal(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	overrides, err := h.settingService.GetOpenAICacheSimOverrides(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"config": service.NewOpenAICacheSimConfigView(cfg), "overrides": len(overrides)})
}

func (req UpdateSimulationRequest) toConfig() (service.OpenAICacheSimConfig, error) {
	return service.ParseOpenAICacheSimConfigMap(map[string]any{
		"enabled":           req.Enabled,
		"lo":                req.Lo,
		"hi":                req.Hi,
		"half_life_minutes": req.HalfLifeMinutes,
		"cap":               req.Cap,
	})
}

// UpdateGlobalSimulation PUT /admin/openai-cache/global
func (h *OpenAICacheHandler) UpdateGlobalSimulation(c *gin.Context) {
	var req UpdateSimulationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	cfg, err := req.toConfig()
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	saved, err := h.settingService.SetOpenAICacheSimGlobal(c.Request.Context(), cfg)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, service.NewOpenAICacheSimConfigView(saved))
}

// UpdateUserSimulationRequest is a user's override; Inherit removes it.
type UpdateUserSimulationRequest struct {
	UpdateSimulationRequest
	Inherit bool `json:"inherit"`
}

// UpdateUserSimulation PUT /admin/openai-cache/users/:id/simulation
func (h *OpenAICacheHandler) UpdateUserSimulation(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || userID <= 0 {
		response.BadRequest(c, "Invalid user ID")
		return
	}
	var req UpdateUserSimulationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if req.Inherit {
		if err := h.settingService.SetOpenAICacheSimUser(c.Request.Context(), userID, nil); err != nil {
			response.ErrorFrom(c, err)
			return
		}
		response.Success(c, gin.H{"inherit": true})
		return
	}
	cfg, err := req.toConfig()
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := h.settingService.SetOpenAICacheSimUser(c.Request.Context(), userID, &cfg); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, service.NewOpenAICacheSimConfigView(cfg))
}

// ListUserOverrides GET /admin/openai-cache/user-overrides
func (h *OpenAICacheHandler) ListUserOverrides(c *gin.Context) {
	overrides, err := h.settingService.GetOpenAICacheSimOverrides(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out := make([]gin.H, 0, len(overrides))
	for id, cfg := range overrides {
		out = append(out, gin.H{"user_id": id, "config": service.NewOpenAICacheSimConfigView(cfg)})
	}
	response.Success(c, gin.H{"overrides": out})
}
