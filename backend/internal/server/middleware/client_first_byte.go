package middleware

import (
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/firstbyte"
	"github.com/gin-gonic/gin"
)

// ClientFirstByteTrackerKey 是 gin.Context 上存放 *firstbyte.Tracker 的键，
// 供 handler 层直接读取；request context 上同样挂了一份，供 service 层按 ctx 读取。
const ClientFirstByteTrackerKey = "client_first_byte_tracker"

type clientFirstByteWriter struct {
	gin.ResponseWriter
	tracker *firstbyte.Tracker
}

func (w *clientFirstByteWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *clientFirstByteWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	if n > 0 {
		w.tracker.MarkWrite(data[:n])
	}
	return n, err
}

func (w *clientFirstByteWriter) WriteString(data string) (int, error) {
	n, err := w.ResponseWriter.WriteString(data)
	if n > 0 {
		w.tracker.MarkWrite([]byte(data[:n]))
	}
	return n, err
}

// ClientFirstByte 记录「客户端实际首字节时间」：从请求进入网关到第一段响应体
// 写回客户端的耗时，包含鉴权、选号、账号重试与换号等全部等待。
// 落到 usage_logs.client_first_byte_ms，与只统计最后一次成功转发的 first_token_ms 互补。
func ClientFirstByte() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil {
			c.Next()
			return
		}
		tracker := firstbyte.New(time.Now())
		c.Set(ClientFirstByteTrackerKey, tracker)
		c.Request = c.Request.WithContext(firstbyte.WithTracker(c.Request.Context(), tracker))
		c.Writer = &clientFirstByteWriter{ResponseWriter: c.Writer, tracker: tracker}
		c.Next()
	}
}

// ClientFirstByteTracker 从 gin.Context 取出 Tracker，没有时返回 nil。
func ClientFirstByteTracker(c *gin.Context) *firstbyte.Tracker {
	if c == nil {
		return nil
	}
	if v, ok := c.Get(ClientFirstByteTrackerKey); ok {
		if t, ok := v.(*firstbyte.Tracker); ok {
			return t
		}
	}
	return firstbyte.FromContext(c.Request.Context())
}
