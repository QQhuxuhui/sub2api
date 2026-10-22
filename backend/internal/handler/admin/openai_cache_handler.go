package admin

import (
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// OpenAICacheHandler 管理端「上游缓存」页：OpenAI 账号的真实/展示缓存率与缓存模拟让利。
type OpenAICacheHandler struct {
	statsService *service.OpenAICacheStatsService
}

// NewOpenAICacheHandler 构造。
func NewOpenAICacheHandler(statsService *service.OpenAICacheStatsService) *OpenAICacheHandler {
	return &OpenAICacheHandler{statsService: statsService}
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
