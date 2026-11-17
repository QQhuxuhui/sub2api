package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// writeOpenAIStreamConfiguredRetryExhausted answers the customer when every
// retry the in-stream retry-code list triggered has failed: HTTP 400 with the
// upstream error, instead of the empty 200 the failed event used to become.
// It reports whether it wrote the response. Once bytes are on the wire the
// status can not change, so that case is left to the caller's stream logic.
func (h *OpenAIGatewayHandler) writeOpenAIStreamConfiguredRetryExhausted(c *gin.Context, failoverErr *service.UpstreamFailoverError, streamStarted bool, anthropicShape bool) bool {
	if failoverErr == nil || failoverErr.Reason != service.OpenAIStreamConfiguredRetryReason {
		return false
	}
	if streamStarted || c == nil || c.Writer == nil || c.Writer.Written() {
		return false
	}
	message := service.SanitizeUpstreamErrorMessage(service.ExtractUpstreamErrorMessage(failoverErr.ResponseBody))
	service.SetOpsUpstreamError(c, http.StatusBadRequest, message, "")
	// The stream reader may already have announced an event stream.
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	if anthropicShape {
		if message == "" {
			message = "Upstream rejected the request"
		}
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", message)
		return true
	}
	service.WriteOpenAIUpstreamClientError(c, http.StatusBadRequest, failoverErr.ResponseBody, message)
	return true
}
