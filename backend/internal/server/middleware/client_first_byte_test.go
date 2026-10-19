package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/firstbyte"
	"github.com/gin-gonic/gin"
)

func TestClientFirstByteIgnoresKeepaliveUntilRealContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ClientFirstByte())

	var fromCtx, fromGin *firstbyte.Tracker
	r.GET("/stream", func(c *gin.Context) {
		fromGin = ClientFirstByteTracker(c)
		fromCtx = firstbyte.FromContext(c.Request.Context())
		c.Header("Content-Type", "text/event-stream")
		c.Writer.WriteHeader(http.StatusOK)
		_, _ = c.Writer.WriteString(": keepalive\n\n")
		c.Writer.Flush()
		if _, ok := fromGin.ClientFirstByteMs(); ok {
			t.Errorf("keepalive must not mark first byte")
		}
		time.Sleep(15 * time.Millisecond)
		_, _ = c.Writer.Write([]byte("event: message_start\ndata: {}\n\n"))
		c.Writer.Flush()
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	r.ServeHTTP(w, req)

	if fromGin == nil || fromCtx == nil || fromGin != fromCtx {
		t.Fatalf("tracker must be reachable from both gin context and request context")
	}
	ms, ok := fromGin.ClientFirstByteMs()
	if !ok {
		t.Fatalf("first byte not recorded")
	}
	if ms < 15 {
		t.Fatalf("first byte latency %dms should include the wait before real content", ms)
	}
	if w.Body.String() == "" {
		t.Fatalf("response body must pass through")
	}
}

func TestClientFirstByteNonStreamJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ClientFirstByte())
	var tracker *firstbyte.Tracker
	r.GET("/json", func(c *gin.Context) {
		tracker = ClientFirstByteTracker(c)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/json", nil))
	if tracker == nil {
		t.Fatalf("tracker missing")
	}
	if _, ok := tracker.ClientFirstByteMs(); !ok {
		t.Fatalf("json response must record first byte")
	}
}
