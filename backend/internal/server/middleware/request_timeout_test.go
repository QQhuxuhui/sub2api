package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
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

// newUserOverrideTimeoutRouter 模拟线上顺序：RequestTimeout（全局 deadline）在鉴权之前，
// UserOverride 在鉴权之后按用户值替换 deadline。
func newUserOverrideTimeoutRouter(cfgSeconds, userSeconds int, handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	fakeAuth := func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), &service.APIKey{
			User: &service.User{RequestTimeoutSeconds: userSeconds},
		})
	}
	r.Any("/v1/test", RequestTimeout(cfgSeconds, nil), fakeAuth, RequestTimeoutUserOverride(), RequestTimeoutFinalizer(), handler)
	return r
}

func TestRequestTimeoutUserOverrideShortensGlobal(t *testing.T) {
	// 全局 60s，用户 1s：以用户值为准，超时返回 504
	r := newUserOverrideTimeoutRouter(60, 1, func(c *gin.Context) {
		deadline, ok := c.Request.Context().Deadline()
		require.True(t, ok)
		require.WithinDuration(t, time.Now().Add(time.Second), deadline, 500*time.Millisecond)
		<-c.Request.Context().Done()
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
	require.Equal(t, http.StatusGatewayTimeout, w.Code)
	require.Contains(t, w.Body.String(), "timeout_error")
}

func TestRequestTimeoutUserOverrideExtendsGlobal(t *testing.T) {
	// 全局 1s，用户 600s：用户值可放宽全局限制
	var deadline time.Time
	var hasDeadline bool
	r := newUserOverrideTimeoutRouter(1, 600, func(c *gin.Context) {
		deadline, hasDeadline = c.Request.Context().Deadline()
		c.Status(http.StatusOK)
	})
	start := time.Now()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, hasDeadline)
	require.WithinDuration(t, start.Add(600*time.Second), deadline, 5*time.Second)
}

func TestRequestTimeoutUserOverrideUnlimited(t *testing.T) {
	// 用户 -1：豁免全局超时，不设置 deadline
	r := newUserOverrideTimeoutRouter(1, -1, func(c *gin.Context) {
		_, hasDeadline := c.Request.Context().Deadline()
		require.False(t, hasDeadline)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
	require.Equal(t, http.StatusOK, w.Code)
}

func TestRequestTimeoutUserZeroInheritsGlobal(t *testing.T) {
	// 用户 0：继承全局配置
	var deadline time.Time
	var hasDeadline bool
	r := newUserOverrideTimeoutRouter(240, 0, func(c *gin.Context) {
		deadline, hasDeadline = c.Request.Context().Deadline()
		c.Status(http.StatusOK)
	})
	start := time.Now()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
	require.True(t, hasDeadline)
	require.WithinDuration(t, start.Add(240*time.Second), deadline, 5*time.Second)
}

func TestRequestTimeoutGlobalCoversAuthPhaseBeforeOverride(t *testing.T) {
	// 全局 deadline 必须在鉴权之前建立（鉴权含 DB/缓存访问，不能无限等待），
	// 用户级覆盖只在鉴权之后替换 deadline。
	gin.SetMode(gin.TestMode)
	r := gin.New()
	var authHasDeadline bool
	var authDeadline, handlerDeadline time.Time
	fakeAuth := func(c *gin.Context) {
		authDeadline, authHasDeadline = c.Request.Context().Deadline()
		c.Set(string(ContextKeyAPIKey), &service.APIKey{
			User: &service.User{RequestTimeoutSeconds: 600},
		})
	}
	r.Any("/v1/test", RequestTimeout(60, nil), fakeAuth, RequestTimeoutUserOverride(), RequestTimeoutFinalizer(), func(c *gin.Context) {
		handlerDeadline, _ = c.Request.Context().Deadline()
		c.Status(http.StatusOK)
	})
	start := time.Now()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, authHasDeadline, "auth phase must run under the global deadline")
	require.WithinDuration(t, start.Add(60*time.Second), authDeadline, 5*time.Second)
	require.WithinDuration(t, start.Add(600*time.Second), handlerDeadline, 5*time.Second)
}

func TestRequestTimeoutUserOverridePreservesAuthContextValues(t *testing.T) {
	// 替换 deadline 不能丢失鉴权阶段写入的 context 值（UserID/Group/ForcePlatform），
	// 否则 Antigravity 平台调度、计费归属会错乱。
	for name, userSeconds := range map[string]int{"unlimited -1": -1, "extend 600": 600, "shorten 30": 30} {
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			r := gin.New()
			group := &service.Group{ID: 7, Platform: service.PlatformAntigravity, Status: service.StatusActive}
			fakeAuth := func(c *gin.Context) {
				ctx := c.Request.Context()
				ctx = context.WithValue(ctx, ctxkey.ForcePlatform, service.PlatformAntigravity)
				ctx = context.WithValue(ctx, ctxkey.UserID, int64(42))
				ctx = context.WithValue(ctx, ctxkey.Group, group)
				c.Request = c.Request.WithContext(ctx)
				c.Set(string(ContextKeyAPIKey), &service.APIKey{
					User: &service.User{ID: 42, RequestTimeoutSeconds: userSeconds},
				})
			}
			r.Any("/v1/test", RequestTimeout(60, nil), fakeAuth, RequestTimeoutUserOverride(), RequestTimeoutFinalizer(), func(c *gin.Context) {
				ctx := c.Request.Context()
				require.Equal(t, int64(42), ctx.Value(ctxkey.UserID), "UserID 必须在 deadline 替换后保留")
				require.Same(t, group, ctx.Value(ctxkey.Group), "Group 必须在 deadline 替换后保留")
				require.Equal(t, service.PlatformAntigravity, ctx.Value(ctxkey.ForcePlatform), "ForcePlatform 必须在 deadline 替换后保留")
				c.Status(http.StatusOK)
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
			require.Equal(t, http.StatusOK, w.Code)
		})
	}
}

func TestRequestTimeoutUserOverrideCannotReviveExpiredRequest(t *testing.T) {
	// 鉴权耗尽全局时限仍返回成功时，用户级 -1/更长超时不得"复活"请求：
	// 链路中止、handler 不执行（避免副作用），按 504 收尾。
	for name, userSeconds := range map[string]int{"unlimited -1": -1, "extend 600": 600} {
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			r := gin.New()
			fakeSlowAuth := func(c *gin.Context) {
				<-c.Request.Context().Done() // 鉴权超过全局时限后才"成功"
				c.Set(string(ContextKeyAPIKey), &service.APIKey{
					User: &service.User{ID: 42, RequestTimeoutSeconds: userSeconds},
				})
			}
			handlerRan := false
			r.Any("/v1/test", RequestTimeout(1, nil), fakeSlowAuth, RequestTimeoutUserOverride(), RequestTimeoutFinalizer(), func(c *gin.Context) {
				handlerRan = true
				c.JSON(http.StatusOK, gin.H{"ok": true})
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
			require.False(t, handlerRan, "已超时请求不得继续执行 handler")
			require.Equal(t, http.StatusGatewayTimeout, w.Code)
			require.Contains(t, w.Body.String(), "timeout_error")
			require.NotContains(t, w.Body.String(), `"ok":true`)
		})
	}
}

func TestRequestTimeoutUserOverrideAbortsWhenUserDeadlineAlreadyExpired(t *testing.T) {
	// 用户专属超时从请求进入时刻起算：鉴权耗时已超过用户时限时，
	// handler 不执行，直接按用户秒数 504 收尾。
	gin.SetMode(gin.TestMode)
	r := gin.New()
	fakeSlowAuth := func(c *gin.Context) {
		time.Sleep(1100 * time.Millisecond) // 超过用户 1s 时限，但在全局 60s 之内
		c.Set(string(ContextKeyAPIKey), &service.APIKey{
			User: &service.User{ID: 42, RequestTimeoutSeconds: 1},
		})
	}
	handlerRan := false
	r.Any("/v1/test", RequestTimeout(60, nil), fakeSlowAuth, RequestTimeoutUserOverride(), RequestTimeoutFinalizer(), func(c *gin.Context) {
		handlerRan = true
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
	require.False(t, handlerRan, "用户时限已耗尽的请求不得执行 handler")
	require.Equal(t, http.StatusGatewayTimeout, w.Code)
	require.Contains(t, w.Body.String(), "timeout_error")
	require.Contains(t, w.Body.String(), "within 1 seconds")
}

func TestRequestTimeoutUserOverrideKeepsCauseConsistentWithErr(t *testing.T) {
	// 替换 deadline 后，用户超时到期时 Err 与 Cause 必须一致为 DeadlineExceeded；
	// 若 Cause 命中被取消的旧全局链会得到 Canceled，HTTP transport 会把真实网关
	// 超时误判成客户端断开。
	var errAtTimeout, causeAtTimeout error
	r := newUserOverrideTimeoutRouter(60, 1, func(c *gin.Context) {
		ctx := c.Request.Context()
		<-ctx.Done()
		errAtTimeout = ctx.Err()
		causeAtTimeout = context.Cause(ctx)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
	require.Equal(t, http.StatusGatewayTimeout, w.Code)
	require.ErrorIs(t, errAtTimeout, context.DeadlineExceeded)
	require.ErrorIs(t, causeAtTimeout, context.DeadlineExceeded,
		"Cause 不得命中已取消的旧全局 deadline 链（Canceled）")
}

func TestRequestTimeoutUserOverrideRejectsOversizedValue(t *testing.T) {
	// 契约上限 86400；缓存快照里的超大脏值不生效，维持全局 deadline。
	var deadline time.Time
	var hasDeadline bool
	r := newUserOverrideTimeoutRouter(60, 100000, func(c *gin.Context) {
		deadline, hasDeadline = c.Request.Context().Deadline()
		c.Status(http.StatusOK)
	})
	start := time.Now()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, hasDeadline)
	require.WithinDuration(t, start.Add(60*time.Second), deadline, 5*time.Second)
}

func TestRequestTimeoutUserOverrideRejectsInvalidNegative(t *testing.T) {
	// 契约只定义 -1；其余负值（非法数据）不生效，维持全局 deadline。
	var deadline time.Time
	var hasDeadline bool
	r := newUserOverrideTimeoutRouter(60, -5, func(c *gin.Context) {
		deadline, hasDeadline = c.Request.Context().Deadline()
		c.Status(http.StatusOK)
	})
	start := time.Now()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, hasDeadline)
	require.WithinDuration(t, start.Add(60*time.Second), deadline, 5*time.Second)
}

// staleDeadlineCtx 模拟"deadline 时刻已到但定时器回调尚未发布 Err"的竞态窗口。
type staleDeadlineCtx struct {
	context.Context
	deadline time.Time
}

func (c staleDeadlineCtx) Deadline() (time.Time, bool) { return c.deadline, true }
func (c staleDeadlineCtx) Err() error                  { return nil }

// maskedErrCtx 模拟"初检读取 Err 时取消尚不可见"的竞态窗口。
type maskedErrCtx struct{ context.Context }

func (c maskedErrCtx) Err() error                  { return nil }
func (c maskedErrCtx) Deadline() (time.Time, bool) { return time.Time{}, false }

// installFakeTimeoutController 按 RequestTimeout 的方式手工装配 writer 与
// controller，以便向 override 注入构造出的竞态 context。
func installFakeTimeoutController(reqCtx context.Context, baseCtx context.Context, startedAt time.Time, seconds int) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request = c.Request.WithContext(reqCtx)
		original := c.Writer
		tw := &timeoutOverrideWriter{
			ResponseWriter: original,
			path:           c.Request.URL.Path,
			deadlineCtx:    reqCtx,
			seconds:        seconds,
		}
		c.Writer = tw
		c.Set(requestTimeoutControllerKey, &requestTimeoutController{
			baseCtx:   baseCtx,
			startedAt: startedAt,
			writer:    tw,
			cancel:    func() {},
		})
		defer func() { c.Writer = original }()
		c.Next()
	}
}

func TestRequestTimeoutUserOverrideDetectsExpiryBeforeErrPublished(t *testing.T) {
	// 全局 deadline 时刻已过但 Err() 尚未发布（定时器回调滞后）：
	// 仅靠 Err 判定会放行并让 oldCancel 把状态固定为 Canceled，复检失效。
	// 时钟判定必须兜住：不执行 handler，直接按全局秒数 504。
	for name, userSeconds := range map[string]int{"unlimited -1": -1, "extend 600": 600} {
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			r := gin.New()
			handlerRan := false
			install := func(c *gin.Context) {
				baseCtx := c.Request.Context()
				stale := staleDeadlineCtx{Context: baseCtx, deadline: time.Now().Add(-10 * time.Millisecond)}
				installFakeTimeoutController(stale, baseCtx, time.Now().Add(-61*time.Second), 60)(c)
			}
			fakeAuth := func(c *gin.Context) {
				c.Set(string(ContextKeyAPIKey), &service.APIKey{
					User: &service.User{ID: 42, RequestTimeoutSeconds: userSeconds},
				})
			}
			r.Any("/v1/test", install, fakeAuth, RequestTimeoutUserOverride(), RequestTimeoutFinalizer(), func(c *gin.Context) {
				handlerRan = true
				c.JSON(http.StatusOK, gin.H{"ok": true})
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
			require.False(t, handlerRan, "逻辑上已超时的请求不得执行 handler")
			require.Equal(t, http.StatusGatewayTimeout, w.Code)
			require.Contains(t, w.Body.String(), "timeout_error")
			require.Contains(t, w.Body.String(), "within 60 seconds")
		})
	}
}

func TestRequestTimeoutUserOverrideUnlimitedAbortsFreshDisconnect(t *testing.T) {
	// 客户端在初检之后、unlimited context 提交之前断开：
	// baseCtx 已取消（Canceled），提交后的 ctx.Err() 复检必须中止，不得进入 handler。
	gin.SetMode(gin.TestMode)
	r := gin.New()
	handlerRan := false
	install := func(c *gin.Context) {
		baseCtx, cancelBase := context.WithCancel(c.Request.Context())
		cancelBase()                             // 断连已发生
		masked := maskedErrCtx{Context: baseCtx} // 但初检视角 Err 仍不可见
		installFakeTimeoutController(masked, baseCtx, time.Now(), 60)(c)
	}
	fakeAuth := func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), &service.APIKey{
			User: &service.User{ID: 42, RequestTimeoutSeconds: -1},
		})
	}
	r.Any("/v1/test", install, fakeAuth, RequestTimeoutUserOverride(), RequestTimeoutFinalizer(), func(c *gin.Context) {
		handlerRan = true
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/test", nil))
	require.False(t, handlerRan, "已断连的请求不得进入 handler")
	require.NotContains(t, w.Body.String(), `"ok":true`)
}
