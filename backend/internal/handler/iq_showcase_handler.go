package handler

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// IQShowcaseUserHandler serves the read-only IQ showcase page. Both routes
// return 404 IQ_SHOWCASE_DISABLED while the setting is off.
type IQShowcaseUserHandler struct {
	svc *service.IQShowcaseService
}

func NewIQShowcaseUserHandler(svc *service.IQShowcaseService) *IQShowcaseUserHandler {
	return &IQShowcaseUserHandler{svc: svc}
}

// View GET /api/v1/iq-showcase
func (h *IQShowcaseUserHandler) View(c *gin.Context) {
	view, err := h.svc.UserView(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

// Cards GET /api/v1/iq-showcase/cards?ids=1,2,3
func (h *IQShowcaseUserHandler) Cards(c *gin.Context) {
	var ids []int64
	for _, part := range strings.Split(c.Query("ids"), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "Invalid ids")
			return
		}
		ids = append(ids, id)
	}
	cards, err := h.svc.CardSVGs(c.Request.Context(), ids)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"cards": cards})
}
