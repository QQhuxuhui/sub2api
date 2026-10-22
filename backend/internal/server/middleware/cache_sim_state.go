package middleware

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/cachesim"
	"github.com/gin-gonic/gin"
)

// CacheSimState 为每个请求挂一份「OpenAI 缓存模拟」决策状态到 request context。
// 响应改写层与用量落库共用这一份状态，保证发给客户端的 usage 与 usage_logs 一致。
// 状态本身极轻（几个 int），不按路由过滤；未开模拟的账号不会触碰它。
func CacheSimState() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil {
			c.Next()
			return
		}
		c.Request = c.Request.WithContext(cachesim.WithState(c.Request.Context(), cachesim.New()))
		c.Next()
	}
}
