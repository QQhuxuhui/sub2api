package admin

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// IQShowcaseHandler manages the user-facing IQ showcase items.
type IQShowcaseHandler struct {
	svc *service.IQShowcaseService
}

func NewIQShowcaseHandler(svc *service.IQShowcaseService) *IQShowcaseHandler {
	return &IQShowcaseHandler{svc: svc}
}

type iqShowcaseItemRequest struct {
	Title           string `json:"title"`
	GroupID         int64  `json:"group_id"`
	APIKeyID        *int64 `json:"api_key_id"`
	Protocol        string `json:"protocol"`
	Model           string `json:"model"`
	Prompt          string `json:"prompt"`
	ReasoningEffort string `json:"reasoning_effort"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	IntervalMinutes int    `json:"interval_minutes"`
	MaxCards        int    `json:"max_cards"`
	Enabled         bool   `json:"enabled"`
	SortOrder       int    `json:"sort_order"`
}

func (r *iqShowcaseItemRequest) toItem(id int64) *service.IQShowcaseItem {
	return &service.IQShowcaseItem{
		ID: id, Title: r.Title, GroupID: r.GroupID, APIKeyID: r.APIKeyID, Protocol: r.Protocol,
		Model: r.Model, Prompt: r.Prompt, ReasoningEffort: r.ReasoningEffort,
		MaxOutputTokens: r.MaxOutputTokens, IntervalMinutes: r.IntervalMinutes, MaxCards: r.MaxCards,
		Enabled: r.Enabled, SortOrder: r.SortOrder,
	}
}

// ListItems GET /api/v1/admin/iq-showcase/items
func (h *IQShowcaseHandler) ListItems(c *gin.Context) {
	items, err := h.svc.ListItems(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

// CreateItem POST /api/v1/admin/iq-showcase/items
func (h *IQShowcaseHandler) CreateItem(c *gin.Context) {
	var req iqShowcaseItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	item, err := h.svc.CreateItem(c.Request.Context(), req.toItem(0))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// UpdateItem PUT /api/v1/admin/iq-showcase/items/:id
func (h *IQShowcaseHandler) UpdateItem(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req iqShowcaseItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	item, err := h.svc.UpdateItem(c.Request.Context(), req.toItem(id))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// SetEnabled PUT /api/v1/admin/iq-showcase/items/:id/enabled {"enabled": bool}
// Switches an item without validating the rest, so a broken one can be stopped.
func (h *IQShowcaseHandler) SetEnabled(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		response.BadRequest(c, "Invalid request: enabled is required")
		return
	}
	item, err := h.svc.SetEnabled(c.Request.Context(), id, *req.Enabled)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// DeleteItem DELETE /api/v1/admin/iq-showcase/items/:id
func (h *IQShowcaseHandler) DeleteItem(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.svc.DeleteItem(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// RunItem POST /api/v1/admin/iq-showcase/items/:id/run
func (h *IQShowcaseHandler) RunItem(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.svc.RunNow(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"started": true})
}

// ListResults GET /api/v1/admin/iq-showcase/items/:id/results?success=&limit=
func (h *IQShowcaseHandler) ListResults(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var success *bool
	if raw := c.Query("success"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			response.BadRequest(c, "Invalid success")
			return
		}
		success = &v
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	results, err := h.svc.ListResults(c.Request.Context(), id, success, limit)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, results)
}

// ListKeyOptions GET /api/v1/admin/iq-showcase/key-options?group_id=
// Only masked keys: the existing group key list returns plaintext keys.
func (h *IQShowcaseHandler) ListKeyOptions(c *gin.Context) {
	groupID, _ := strconv.ParseInt(c.Query("group_id"), 10, 64)
	opts, err := h.svc.ListKeyOptions(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, opts)
}
