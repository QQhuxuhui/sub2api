package admin

import (
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// IQProbeHandler manages the intelligence probe: question sets, questions,
// monitors and their rounds.
type IQProbeHandler struct {
	svc *service.IQProbeService
}

func NewIQProbeHandler(svc *service.IQProbeService) *IQProbeHandler {
	return &IQProbeHandler{svc: svc}
}

// ---- question sets ----

type iqProbeSetRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ListSets GET /api/v1/admin/iq-probe/sets
func (h *IQProbeHandler) ListSets(c *gin.Context) {
	sets, err := h.svc.ListSets(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, sets)
}

// CreateSet POST /api/v1/admin/iq-probe/sets
func (h *IQProbeHandler) CreateSet(c *gin.Context) {
	var req iqProbeSetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	set, err := h.svc.CreateSet(c.Request.Context(), &service.IQProbeQuestionSet{Name: req.Name, Description: req.Description})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, set)
}

// UpdateSet PUT /api/v1/admin/iq-probe/sets/:id
func (h *IQProbeHandler) UpdateSet(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req iqProbeSetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	set, err := h.svc.UpdateSet(c.Request.Context(), &service.IQProbeQuestionSet{ID: id, Name: req.Name, Description: req.Description})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, set)
}

// DeleteSet DELETE /api/v1/admin/iq-probe/sets/:id
func (h *IQProbeHandler) DeleteSet(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.svc.DeleteSet(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// ExportSet GET /api/v1/admin/iq-probe/sets/:id/export
func (h *IQProbeHandler) ExportSet(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	export, err := h.svc.ExportSet(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, export)
}

// ImportSet POST /api/v1/admin/iq-probe/sets/import
func (h *IQProbeHandler) ImportSet(c *gin.Context) {
	var req service.IQProbeSetExport
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	set, err := h.svc.ImportSet(c.Request.Context(), &req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, set)
}

// ---- questions ----

type iqProbeQuestionRequest struct {
	Title            string  `json:"title"`
	Prompt           string  `json:"prompt"`
	SystemPrompt     string  `json:"system_prompt"`
	ExpectedAnswer   string  `json:"expected_answer"`
	JudgeType        string  `json:"judge_type"`
	NumericTolerance float64 `json:"numeric_tolerance"`
	Weight           float64 `json:"weight"`
	Enabled          *bool   `json:"enabled"`
	Note             string  `json:"note"`
	SortOrder        int     `json:"sort_order"`
}

func (r *iqProbeQuestionRequest) toQuestion() *service.IQProbeQuestion {
	enabled := true
	if r.Enabled != nil {
		enabled = *r.Enabled
	}
	return &service.IQProbeQuestion{
		Title: r.Title, Prompt: r.Prompt, SystemPrompt: r.SystemPrompt, ExpectedAnswer: r.ExpectedAnswer,
		JudgeType: r.JudgeType, NumericTolerance: r.NumericTolerance, Weight: r.Weight, Enabled: enabled,
		Note: r.Note, SortOrder: r.SortOrder,
	}
}

// ListQuestions GET /api/v1/admin/iq-probe/sets/:id/questions
func (h *IQProbeHandler) ListQuestions(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	questions, err := h.svc.ListQuestions(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, questions)
}

// CreateQuestion POST /api/v1/admin/iq-probe/sets/:id/questions
func (h *IQProbeHandler) CreateQuestion(c *gin.Context) {
	setID, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req iqProbeQuestionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	q := req.toQuestion()
	q.SetID = setID
	created, err := h.svc.CreateQuestion(c.Request.Context(), q)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, created)
}

// UpdateQuestion PUT /api/v1/admin/iq-probe/questions/:id
func (h *IQProbeHandler) UpdateQuestion(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req iqProbeQuestionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	q := req.toQuestion()
	q.ID = id
	updated, err := h.svc.UpdateQuestion(c.Request.Context(), q)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, updated)
}

// DeleteQuestion DELETE /api/v1/admin/iq-probe/questions/:id
func (h *IQProbeHandler) DeleteQuestion(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.svc.DeleteQuestion(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// ---- monitors ----

type iqProbeMonitorRequest struct {
	AccountIDs         []int64 `json:"account_ids"`
	GroupID            *int64  `json:"group_id"`
	Model              string  `json:"model"`
	SetID              int64   `json:"set_id"`
	IntervalMinutes    int     `json:"interval_minutes"`
	ReasoningEffort    string  `json:"reasoning_effort"`
	MaxTokens          int     `json:"max_tokens"`
	Enabled            *bool   `json:"enabled"`
	AlertEnabled       bool    `json:"alert_enabled"`
	AlertThreshold     float64 `json:"alert_threshold"`
	AlertLowRounds     int     `json:"alert_low_rounds"`
	AlertInvalidRounds int     `json:"alert_invalid_rounds"`
}

func (r *iqProbeMonitorRequest) toMonitor() *service.IQProbeMonitor {
	enabled := true
	if r.Enabled != nil {
		enabled = *r.Enabled
	}
	return &service.IQProbeMonitor{
		GroupID: r.GroupID, Model: r.Model, SetID: r.SetID, IntervalMinutes: r.IntervalMinutes,
		ReasoningEffort: r.ReasoningEffort, MaxTokens: r.MaxTokens, Enabled: enabled,
		AlertEnabled: r.AlertEnabled, AlertThreshold: r.AlertThreshold,
		AlertLowRounds: r.AlertLowRounds, AlertInvalidRounds: r.AlertInvalidRounds,
	}
}

// ListMonitors GET /api/v1/admin/iq-probe/monitors
func (h *IQProbeHandler) ListMonitors(c *gin.Context) {
	monitors, err := h.svc.ListMonitors(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, monitors)
}

// CreateMonitors POST /api/v1/admin/iq-probe/monitors (one per account_ids entry)
func (h *IQProbeHandler) CreateMonitors(c *gin.Context) {
	var req iqProbeMonitorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	created, skipped, err := h.svc.CreateMonitors(c.Request.Context(), req.toMonitor(), req.AccountIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if created == nil {
		created = []*service.IQProbeMonitor{}
	}
	if skipped == nil {
		skipped = []int64{}
	}
	response.Success(c, gin.H{"created": created, "skipped_account_ids": skipped})
}

// UpdateMonitor PUT /api/v1/admin/iq-probe/monitors/:id
func (h *IQProbeHandler) UpdateMonitor(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req iqProbeMonitorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	m := req.toMonitor()
	m.ID = id
	updated, err := h.svc.UpdateMonitor(c.Request.Context(), m)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, updated)
}

// DeleteMonitor DELETE /api/v1/admin/iq-probe/monitors/:id
func (h *IQProbeHandler) DeleteMonitor(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.svc.DeleteMonitor(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// RunMonitor POST /api/v1/admin/iq-probe/monitors/:id/run
func (h *IQProbeHandler) RunMonitor(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	runID, err := h.svc.RunNow(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"started": true, "run_id": runID})
}

// GetMatrix GET /api/v1/admin/iq-probe/monitors/:id/matrix?limit=30
func (h *IQProbeHandler) GetMatrix(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	matrix, err := h.svc.GetMatrix(c.Request.Context(), id, limit)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, matrix)
}

// ---- runs ----

// GetRun GET /api/v1/admin/iq-probe/runs/:id
func (h *IQProbeHandler) GetRun(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	run, err := h.svc.GetRun(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, run)
}

// ListRuns GET /api/v1/admin/iq-probe/runs?monitor_id=&status=&page=&page_size=
func (h *IQProbeHandler) ListRuns(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	filter := service.IQProbeRunFilter{Status: c.Query("status"), Page: page, PageSize: pageSize}
	if raw := c.Query("monitor_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			response.BadRequest(c, fmt.Sprintf("invalid monitor_id: %s", raw))
			return
		}
		filter.MonitorID = id
	}
	runs, total, err := h.svc.ListRuns(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, runs, total, filter.Page, filter.PageSize)
}

// ListRunAnswers GET /api/v1/admin/iq-probe/runs/:id/answers (includes full replies)
func (h *IQProbeHandler) ListRunAnswers(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	answers, err := h.svc.ListRunAnswers(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, answers)
}

// GetAnswer GET /api/v1/admin/iq-probe/answers/:id (includes the full reply)
func (h *IQProbeHandler) GetAnswer(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	answer, err := h.svc.GetAnswer(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, answer)
}
