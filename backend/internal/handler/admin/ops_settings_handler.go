package admin

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// GetEmailNotificationConfig returns Ops email notification config (DB-backed).
// GET /api/v1/admin/ops/email-notification/config
func (h *OpsHandler) GetEmailNotificationConfig(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	cfg, err := h.opsService.GetEmailNotificationConfig(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to get email notification config")
		return
	}
	response.Success(c, cfg)
}

// UpdateEmailNotificationConfig updates Ops email notification config (DB-backed).
// PUT /api/v1/admin/ops/email-notification/config
func (h *OpsHandler) UpdateEmailNotificationConfig(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	var req service.OpsEmailNotificationConfigUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}

	updated, err := h.opsService.UpdateEmailNotificationConfig(c.Request.Context(), &req)
	if err != nil {
		// Most failures here are validation errors from request payload; treat as 400.
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, updated)
}

// GetAlertRuntimeSettings returns Ops alert evaluator runtime settings (DB-backed).
// GET /api/v1/admin/ops/runtime/alert
func (h *OpsHandler) GetAlertRuntimeSettings(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	cfg, err := h.opsService.GetOpsAlertRuntimeSettings(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to get alert runtime settings")
		return
	}
	response.Success(c, cfg)
}

// UpdateAlertRuntimeSettings updates Ops alert evaluator runtime settings (DB-backed).
// PUT /api/v1/admin/ops/runtime/alert
func (h *OpsHandler) UpdateAlertRuntimeSettings(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	var req service.OpsAlertRuntimeSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}

	updated, err := h.opsService.UpdateOpsAlertRuntimeSettings(c.Request.Context(), &req)
	if err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, updated)
}

// GetRuntimeLogConfig returns runtime log config (DB-backed).
// GET /api/v1/admin/ops/runtime/logging
func (h *OpsHandler) GetRuntimeLogConfig(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	cfg, err := h.opsService.GetRuntimeLogConfig(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to get runtime log config")
		return
	}
	response.Success(c, cfg)
}

// UpdateRuntimeLogConfig updates runtime log config and applies changes immediately.
// PUT /api/v1/admin/ops/runtime/logging
func (h *OpsHandler) UpdateRuntimeLogConfig(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	var req service.OpsRuntimeLogConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}

	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Error(c, http.StatusUnauthorized, "Unauthorized")
		return
	}

	updated, err := h.opsService.UpdateRuntimeLogConfig(c.Request.Context(), &req, subject.UserID)
	if err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, updated)
}

// ResetRuntimeLogConfig removes runtime override and falls back to env/yaml baseline.
// POST /api/v1/admin/ops/runtime/logging/reset
func (h *OpsHandler) ResetRuntimeLogConfig(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Error(c, http.StatusUnauthorized, "Unauthorized")
		return
	}

	updated, err := h.opsService.ResetRuntimeLogConfig(c.Request.Context(), subject.UserID)
	if err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, updated)
}

// GetAdvancedSettings returns Ops advanced settings (DB-backed).
// GET /api/v1/admin/ops/advanced-settings
func (h *OpsHandler) GetAdvancedSettings(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	cfg, err := h.opsService.GetOpsAdvancedSettings(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to get advanced settings")
		return
	}
	response.Success(c, cfg)
}

// UpdateAdvancedSettings updates Ops advanced settings (DB-backed).
// PUT /api/v1/admin/ops/advanced-settings
func (h *OpsHandler) UpdateAdvancedSettings(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	var req service.OpsAdvancedSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}

	updated, err := h.opsService.UpdateOpsAdvancedSettings(c.Request.Context(), &req)
	if err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, updated)
}

// GetMetricThresholds returns Ops metric thresholds (DB-backed).
// GET /api/v1/admin/ops/settings/metric-thresholds
func (h *OpsHandler) GetMetricThresholds(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	cfg, err := h.opsService.GetMetricThresholds(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to get metric thresholds")
		return
	}
	response.Success(c, cfg)
}

// UpdateMetricThresholds updates Ops metric thresholds (DB-backed).
// PUT /api/v1/admin/ops/settings/metric-thresholds
func (h *OpsHandler) UpdateMetricThresholds(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	var req service.OpsMetricThresholds
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}

	updated, err := h.opsService.UpdateMetricThresholds(c.Request.Context(), &req)
	if err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, updated)
}

// GetNotifyChannelConfig returns ops notify channel config (secrets redacted).
// GET /api/v1/admin/ops/notify-channels/config
func (h *OpsHandler) GetNotifyChannelConfig(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	cfg, err := h.opsService.GetNotifyChannelSettingsRedacted(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to get notify channel config")
		return
	}
	response.Success(c, cfg)
}

// UpdateNotifyChannelConfig updates ops notify channel config.
// PUT /api/v1/admin/ops/notify-channels/config
func (h *OpsHandler) UpdateNotifyChannelConfig(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	var req service.OpsNotifyChannelSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}

	updated, err := h.opsService.UpdateNotifyChannelSettings(c.Request.Context(), &req)
	if err != nil {
		// 服务层错误多为配置校验失败,按 400 返回。
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if h.notifyDispatcher != nil {
		h.notifyDispatcher.InvalidateConfigCache()
	}
	response.Success(c, updated)
}

type opsNotifyChannelTestRequest struct {
	ChannelID string                          `json:"channel_id"`
	Channel   *service.OpsNotifyChannelConfig `json:"channel"`
}

// TestNotifyChannel sends a test message to one channel (by id, or an inline channel object).
// POST /api/v1/admin/ops/notify-channels/test
func (h *OpsHandler) TestNotifyChannel(c *gin.Context) {
	if h.opsService == nil || h.notifyDispatcher == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	var req opsNotifyChannelTestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}

	// 行内 channel 对象优先;channel_id 仅在未提供行内对象时用于查找已存通道。
	ch := req.Channel
	channelID := strings.TrimSpace(req.ChannelID)
	needLookup := (ch == nil && channelID != "") ||
		(ch != nil && strings.TrimSpace(ch.Secret) == "" && strings.TrimSpace(ch.ID) != "")
	if needLookup {
		stored, err := h.opsService.GetNotifyChannelSettings(c.Request.Context())
		if err != nil {
			response.Error(c, http.StatusInternalServerError, "Failed to load notify channel config")
			return
		}
		found := false
		for i := range stored.Channels {
			s := &stored.Channels[i]
			if ch == nil {
				if s.ID == channelID {
					ch = s
					found = true
					break
				}
				continue
			}
			if s.ID == strings.TrimSpace(ch.ID) {
				// 行内通道对象 secret 为空 → 用已存 secret 补全(前端脱敏回传场景)
				if strings.TrimSpace(ch.Secret) == "" {
					ch.Secret = s.Secret
				}
				found = true
				break
			}
		}
		if !found {
			response.NotFound(c, "Notify channel not found")
			return
		}
	}
	if ch == nil {
		response.BadRequest(c, "channel or channel_id is required")
		return
	}
	// 行内通道对象与保存路径走同一套归一化+校验(URL 格式、飞书前缀等),
	// 避免"测试发送成功但保存配置失败"的体验偏差。channel_id 查到的已存通道保存时已校验,跳过。
	if req.Channel != nil {
		if err := service.ValidateOpsNotifyChannel(ch); err != nil {
			response.BadRequest(c, err.Error())
			return
		}
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	if err := h.notifyDispatcher.SendTest(ctx, ch); err != nil {
		response.Error(c, http.StatusBadGateway, "Test send failed: "+err.Error())
		return
	}
	response.Success(c, gin.H{"ok": true})
}
