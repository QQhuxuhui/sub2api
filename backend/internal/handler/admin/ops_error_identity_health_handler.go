package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
	"net/http"
)

// GetErrorIdentityHealth reports cold-start and last-good-snapshot state without
// exposing account mappings or raw diagnostics.
func (h *OpsHandler) GetErrorIdentityHealth(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	response.Success(c, h.opsService.ErrorIdentityHealth())
}
