package admin

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// IntentRouterHandler manages per-group intent routing.
type IntentRouterHandler struct {
	router *service.IntentRouterService
}

func NewIntentRouterHandler(router *service.IntentRouterService) *IntentRouterHandler {
	return &IntentRouterHandler{router: router}
}

// RouterService exposes the service to the gateway route setup, which installs
// the classification middleware.
func (h *IntentRouterHandler) RouterService() *service.IntentRouterService {
	if h == nil {
		return nil
	}
	return h.router
}

// List returns every configured router.
// GET /api/v1/admin/intent-routers
func (h *IntentRouterHandler) List(c *gin.Context) {
	routers, err := h.router.ListRouters(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, routers)
}

// Get returns the router of one group.
// GET /api/v1/admin/intent-routers/:group_id
func (h *IntentRouterHandler) Get(c *gin.Context) {
	groupID, ok := parseIDParam(c, "group_id")
	if !ok {
		return
	}
	router, err := h.router.GetRouter(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, router)
}

// Save creates or replaces the router of one group.
// PUT /api/v1/admin/intent-routers/:group_id
func (h *IntentRouterHandler) Save(c *gin.Context) {
	groupID, ok := parseIDParam(c, "group_id")
	if !ok {
		return
	}
	var req service.IntentRouterInput
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	router, err := h.router.SaveRouter(c.Request.Context(), groupID, req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, router)
}

// Delete removes the router of one group and forgets its conversations.
// DELETE /api/v1/admin/intent-routers/:group_id
func (h *IntentRouterHandler) Delete(c *gin.Context) {
	groupID, ok := parseIDParam(c, "group_id")
	if !ok {
		return
	}
	if err := h.router.DeleteRouter(c.Request.Context(), groupID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "deleted"})
}

type intentClassifyTestRequest struct {
	Text string `json:"text" binding:"required"`
}

// Test classifies a sample message with the group's saved router.
// POST /api/v1/admin/intent-routers/:group_id/test
func (h *IntentRouterHandler) Test(c *gin.Context) {
	groupID, ok := parseIDParam(c, "group_id")
	if !ok {
		return
	}
	var req intentClassifyTestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.router.TestClassify(c.Request.Context(), groupID, req.Text)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// ClearCache forgets remembered conversations. group_id=0 clears every group.
// POST /api/v1/admin/intent-routers/:group_id/clear-cache
func (h *IntentRouterHandler) ClearCache(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("group_id"), 10, 64)
	if err != nil || groupID < 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}
	removed, err := h.router.ClearCache(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"removed": removed})
}

// Events returns the recent routing log, newest first.
// GET /api/v1/admin/intent-routers/events
func (h *IntentRouterHandler) Events(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	events, err := h.router.RecentEvents(c.Request.Context(), limit)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, events)
}
