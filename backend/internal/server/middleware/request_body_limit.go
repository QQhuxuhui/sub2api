package middleware

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"

	"github.com/gin-gonic/gin"
)

// RequestBodyLimit 使用 MaxBytesReader 限制请求体大小。
func RequestBodyLimit(maxBytes int64, budgets ...*httputil.BodyMemoryBudget) gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(budgets) > 0 {
			ctx, release := httputil.WithRequestBodyMemoryBudget(c.Request.Context(), budgets[0])
			defer release()
			c.Request = c.Request.WithContext(ctx)
		}
		// A body already read into memory within the limit stays unwrapped, so
		// handlers keep reading it without a copy (see PrereadBody).
		if preread, ok := c.Request.Body.(*httputil.PrereadBody); ok && int64(len(preread.Bytes())) <= maxBytes {
			c.Next()
			return
		}
		if transfer, ok := c.Request.Body.(*httputil.TransferBytesBody); ok && int64(len(transfer.Bytes())) <= maxBytes {
			c.Next()
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}
