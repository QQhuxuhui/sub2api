package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type delayedRequestTimeoutSettingRepo struct {
	delay     time.Duration
	value     string
	calls     atomic.Int32
	completed atomic.Bool
}

func (r *delayedRequestTimeoutSettingRepo) Get(context.Context, string) (*service.Setting, error) {
	return nil, service.ErrSettingNotFound
}

func (r *delayedRequestTimeoutSettingRepo) GetValue(ctx context.Context, _ string) (string, error) {
	r.calls.Add(1)
	timer := time.NewTimer(r.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		r.completed.Store(true)
		return r.value, nil
	}
}

func (r *delayedRequestTimeoutSettingRepo) Set(context.Context, string, string) error {
	return nil
}

func (r *delayedRequestTimeoutSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return nil, nil
}

func (r *delayedRequestTimeoutSettingRepo) SetMultiple(context.Context, map[string]string) error {
	return nil
}

func (r *delayedRequestTimeoutSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return nil, nil
}

func (r *delayedRequestTimeoutSettingRepo) Delete(context.Context, string) error {
	return nil
}

// newTimeoutTestRouter 按线上顺序装配：RequestTimeout 在外层，Finalizer 在 handler 之前。
// extra 位于两者之间，模拟 ops 等中间层。
func newTimeoutTestRouter(seconds int, handler gin.HandlerFunc, extra ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	chain := []gin.HandlerFunc{RequestTimeout(seconds, nil, "/v1/responses")}
	chain = append(chain, extra...)
	chain = append(chain, RequestTimeoutFinalizer(), handler)
	r.Any("/v1/test", chain...)
	r.Any("/v1/messages", chain...)
	r.Any("/v1/responses", chain...)
	r.Any("/v1beta/models/gemini:generateContent", chain...)
	return r
}

func wsUpgradeRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "keep-alive, Upgrade")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Sec-WebSocket-Version", "13")
	return req
}

func TestRequestTimeoutDisabledByZero(t *testing.T) {
	r := newTimeoutTestRouter(0, func(c *gin.Context) {
		_, hasDeadline := c.Request.Context().Deadline()
		require.False(t, hasDeadline)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
	require.Equal(t, http.StatusOK, w.Code)
}

func TestRequestTimeoutWrites504WhenHandlerWroteNothing(t *testing.T) {
	// 模拟转发路径把 DeadlineExceeded 当客户端断连、静默返回的情况
	r := newTimeoutTestRouter(1, func(c *gin.Context) {
		<-c.Request.Context().Done()
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
	require.Equal(t, http.StatusGatewayTimeout, w.Code)
	require.Contains(t, w.Body.String(), "timeout_error")
}

func TestRequestTimeoutKeepsHandlerResponse(t *testing.T) {
	r := newTimeoutTestRouter(60, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"ok":true`)
}

func TestRequestTimeoutOverridesAnyFirstWriteAfterDeadline(t *testing.T) {
	// 严格契约：deadline 后的第一次写出，无论 5xx/4xx/200，一律 504
	cases := map[string]gin.HandlerFunc{
		"late 502": func(c *gin.Context) {
			<-c.Request.Context().Done()
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"type": "upstream_error"}})
		},
		"late 500": func(c *gin.Context) {
			<-c.Request.Context().Done()
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "api_error"}})
		},
		"late 400": func(c *gin.Context) {
			<-c.Request.Context().Done()
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error"}})
		},
		"late 200": func(c *gin.Context) {
			<-c.Request.Context().Done()
			c.JSON(http.StatusOK, gin.H{"ok": true})
		},
		"late implicit 200 body write": func(c *gin.Context) {
			<-c.Request.Context().Done()
			_, _ = c.Writer.WriteString("late body")
		},
	}
	for name, handler := range cases {
		r := newTimeoutTestRouter(1, handler)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
		require.Equal(t, http.StatusGatewayTimeout, w.Code, name)
		require.Contains(t, w.Body.String(), "timeout_error", name)
		require.NotContains(t, w.Body.String(), "upstream_error", name)
		require.NotContains(t, w.Body.String(), "late body", name)
	}
}

func TestRequestTimeoutKeepsStreamStartedBeforeDeadline(t *testing.T) {
	// 已开始输出的响应不能改写状态码，超时表现为断流；不得追加错误体
	r := newTimeoutTestRouter(1, func(c *gin.Context) {
		c.Writer.WriteHeader(http.StatusOK)
		_, _ = c.Writer.WriteString("data: chunk\n\n")
		c.Writer.Flush()
		<-c.Request.Context().Done()
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "data: chunk")
	require.NotContains(t, w.Body.String(), "timeout_error")
}

func TestRequestTimeoutFlushCommittedStreamNotAppended(t *testing.T) {
	// Flush 提交过隐式 200 后，超时后的 WriteHeader/Write 必须被丢弃。
	r := newTimeoutTestRouter(1, func(c *gin.Context) {
		c.Writer.Flush() // 提交隐式 200 头
		<-c.Request.Context().Done()
		c.Writer.WriteHeader(http.StatusInternalServerError)
		_, _ = c.Writer.WriteString("late error body")
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.NotContains(t, w.Body.String(), "timeout_error")
	require.NotContains(t, w.Body.String(), "late error body")
}

func TestRequestTimeoutClearsStaleRepresentationHeaders(t *testing.T) {
	r := newTimeoutTestRouter(1, func(c *gin.Context) {
		c.Header("Content-Encoding", "gzip")
		c.Header("Content-Disposition", `attachment; filename="result.zip"`)
		c.Header("ETag", `"upstream-body"`)
		<-c.Request.Context().Done()
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))

	require.Equal(t, http.StatusGatewayTimeout, w.Code)
	require.Empty(t, w.Header().Get("Content-Encoding"))
	require.Empty(t, w.Header().Get("Content-Disposition"))
	require.Empty(t, w.Header().Get("ETag"))
	require.Contains(t, w.Body.String(), "timeout_error")
}

func TestRequestTimeoutRestoresWriter(t *testing.T) {
	// 外层中间件（如 Ops）池化复用 writer 并校验归属，返回前必须还原 c.Writer
	var writerBefore, writerAfterNext gin.ResponseWriter
	outer := func(c *gin.Context) {
		writerBefore = c.Writer
		c.Next()
		writerAfterNext = c.Writer
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/test", outer, RequestTimeout(1, nil), RequestTimeoutFinalizer(), func(c *gin.Context) {
		<-c.Request.Context().Done()
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
	require.Equal(t, http.StatusGatewayTimeout, w.Code)
	require.Same(t, writerBefore, writerAfterNext)
}

func TestRequestTimeoutFinalizerWritesBeforeOuterMiddleware(t *testing.T) {
	// Ops 场景：外层中间件读状态码时，静默超时的 504 必须已经写出
	var statusSeenByOuter int
	outer := func(c *gin.Context) {
		c.Next()
		statusSeenByOuter = c.Writer.Status()
	}
	r := newTimeoutTestRouter(1, func(c *gin.Context) {
		<-c.Request.Context().Done()
	}, outer)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
	require.Equal(t, http.StatusGatewayTimeout, w.Code)
	require.Equal(t, http.StatusGatewayTimeout, statusSeenByOuter)
}

func TestRequestTimeoutForgedUpgradeHeaderDoesNotBypass(t *testing.T) {
	// 任意请求带 Upgrade: websocket 头不能绕过超时
	r := newTimeoutTestRouter(1, func(c *gin.Context) {
		<-c.Request.Context().Done()
	})
	req := wsUpgradeRequest(http.MethodPost, "/v1/test")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusGatewayTimeout, w.Code)
}

func TestRequestTimeoutUpgradeOnNonExemptPathDoesNotBypass(t *testing.T) {
	// 完整握手但路径不是 WebSocket 路由，同样不豁免
	r := newTimeoutTestRouter(1, func(c *gin.Context) {
		<-c.Request.Context().Done()
	})
	req := wsUpgradeRequest(http.MethodGet, "/v1/test")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusGatewayTimeout, w.Code)
}

func TestRequestTimeoutExemptsRealWebSocketRoute(t *testing.T) {
	r := newTimeoutTestRouter(1, func(c *gin.Context) {
		_, hasDeadline := c.Request.Context().Deadline()
		require.False(t, hasDeadline)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	req := wsUpgradeRequest(http.MethodGet, "/v1/responses")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestIsWebSocketUpgradeRequiresRFC6455Handshake(t *testing.T) {
	valid := wsUpgradeRequest(http.MethodGet, "/v1/responses")
	require.True(t, isWebSocketUpgrade(valid))

	missingVersion := valid.Clone(valid.Context())
	missingVersion.Header = valid.Header.Clone()
	missingVersion.Header.Del("Sec-WebSocket-Version")
	require.False(t, isWebSocketUpgrade(missingVersion))

	invalidKey := valid.Clone(valid.Context())
	invalidKey.Header = valid.Header.Clone()
	invalidKey.Header.Set("Sec-WebSocket-Key", "not-base64")
	require.False(t, isWebSocketUpgrade(invalidKey))
}

func TestRequestTimeoutErrorFormatFollowsEntryProtocol(t *testing.T) {
	// 协议由路径决定，与 API Key 分组无关
	silent := func(c *gin.Context) { <-c.Request.Context().Done() }

	// /v1/messages → Anthropic 格式（顶层 type:error）
	r := newTimeoutTestRouter(1, silent)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	require.Equal(t, http.StatusGatewayTimeout, w.Code)
	require.Contains(t, w.Body.String(), `"type":"error"`)

	// /v1/test（chat/completions 等 OpenAI 形态入口的代表）→ OpenAI 格式
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
	require.Equal(t, http.StatusGatewayTimeout, w.Code)
	require.NotContains(t, w.Body.String(), `"type":"error"`)
	require.Contains(t, w.Body.String(), `"timeout_error"`)

	// /v1beta → Google 格式
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini:generateContent", nil))
	require.Equal(t, http.StatusGatewayTimeout, w.Code)
	require.Contains(t, w.Body.String(), `"code":504`)
	require.Contains(t, w.Body.String(), `"DEADLINE_EXCEEDED"`)
}

func TestRequestTimeoutProtocolDoesNotUseMessageSubstring(t *testing.T) {
	openAI := timeoutErrorBody("/v1/responses/foo/messages", 1)
	require.NotEqual(t, "error", openAI["type"])

	anthropic := timeoutErrorBody("/v1/usage", 1)
	require.Equal(t, "error", anthropic["type"])
}

func TestRequestTimeoutLoadsRuntimeSettingsBeforeFirstRequestWithoutBlockingRequests(t *testing.T) {
	repo := &delayedRequestTimeoutSettingRepo{
		delay: 250 * time.Millisecond,
		value: `{"enabled":true,"timeout_seconds":1}`,
	}
	settingService := service.NewSettingService(repo, &config.Config{})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	var deadlines []time.Time
	r.POST("/v1/test", RequestTimeout(60, settingService), func(c *gin.Context) {
		deadline, _ := c.Request.Context().Deadline()
		deadlines = append(deadlines, deadline)
		c.Status(http.StatusNoContent)
	})

	startedAt := time.Now()
	for range 5 {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
		require.Equal(t, http.StatusNoContent, w.Code)
	}
	require.Less(t, time.Since(startedAt), 150*time.Millisecond)
	require.Eventually(t, func() bool { return repo.calls.Load() == 1 }, 100*time.Millisecond, time.Millisecond,
		"repeated requests must reuse the loaded snapshot")
	require.WithinDuration(t, startedAt.Add(time.Second), deadlines[0], 100*time.Millisecond)

	require.Eventually(t, repo.completed.Load, time.Second, 10*time.Millisecond)
	runtimeStartedAt := time.Now()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
	require.Equal(t, http.StatusNoContent, w.Code)
	require.WithinDuration(t, runtimeStartedAt.Add(time.Second), deadlines[len(deadlines)-1], 100*time.Millisecond)
}

func TestRequestTimeoutSetsDeadlineOnContext(t *testing.T) {
	var deadline time.Time
	var hasDeadline bool
	r := newTimeoutTestRouter(240, func(c *gin.Context) {
		deadline, hasDeadline = c.Request.Context().Deadline()
		c.Status(http.StatusOK)
	})
	start := time.Now()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
	require.True(t, hasDeadline)
	require.WithinDuration(t, start.Add(240*time.Second), deadline, 5*time.Second)
}
