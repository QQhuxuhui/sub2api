package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// writeOpenAIRuleStreamDisconnectExhausted answers a request whose rule-table
// switches all ended on an upstream stream disconnect. The fault is upstream,
// so the customer gets 502 server_error (or a terminal event carrying
// request_timeout once the stream is committed) instead of a client 400.
func (h *OpenAIGatewayHandler) writeOpenAIRuleStreamDisconnectExhausted(c *gin.Context, failoverErr *service.UpstreamFailoverError, streamStarted bool) {
	// The compact keepalive may already have committed a 200 SSE response.
	if service.StopOpenAICompactSSEKeepaliveCommitted(c) {
		streamStarted = true
	}
	status := http.StatusBadGateway
	// The rule error body already carries the sanitized upstream message.
	message := strings.TrimSpace(gjson.GetBytes(failoverErr.ResponseBody, "error.message").String())
	if message == "" {
		message = service.SanitizeUpstreamErrorMessage(service.ExtractUpstreamErrorMessage(failoverErr.ResponseBody))
	}
	if message == "" {
		message = http.StatusText(status)
	}
	service.SetOpsUpstreamError(c, status, message, "")
	if !streamStarted {
		c.Header("Content-Type", "application/json; charset=utf-8")
		service.WriteOpenAIUpstreamClientError(c, status, failoverErr.ResponseBody, message)
		return
	}
	h.handleStreamingAwareErrorWithCode(c, status,
		service.OpenAIStreamDisconnectClientErrorType,
		service.OpenAIStreamDisconnectClientCode,
		message, true, false)
}
