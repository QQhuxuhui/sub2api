package middleware

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/googleapi"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// requestTimeoutCacheTTL 运行时设置快照的本地缓存时长。
// 首次读取发生在路由构建阶段；过期后请求继续使用最近快照并触发单次后台刷新。
const requestTimeoutCacheTTL = 30 * time.Second

// requestTimeoutSettingsQueryTimeout 运行时设置后台刷新查询的上限。
const requestTimeoutSettingsQueryTimeout = 2 * time.Second

// requestTimeoutRefreshRetry 读取失败后沿用上一份快照，并以较短间隔重试。
const requestTimeoutRefreshRetry = 5 * time.Second

// requestTimeoutSecondsKey 存放本请求生效的超时秒数，供 Finalizer 构造错误消息。
const requestTimeoutSecondsKey = "gw_request_timeout_seconds"

type requestTimeoutSnapshot struct {
	seconds   int
	expiresAt time.Time
}

// timeoutErrorBody 按稳定入口路径构造 504 错误响应体。协议由端点决定，
// 与 API Key 分组无关：OpenAI 分组也可能走 /v1/messages 的 Anthropic 协议。
func timeoutErrorBody(path string, seconds int) gin.H {
	message := fmt.Sprintf(
		"Request did not complete within %d seconds and was aborted by the gateway",
		seconds,
	)
	switch {
	case strings.HasPrefix(path, "/v1beta/") || strings.HasPrefix(path, "/antigravity/v1beta/"):
		return gin.H{
			"error": gin.H{
				"code":    http.StatusGatewayTimeout,
				"message": message,
				"status":  googleapi.HTTPStatusToGoogleStatus(http.StatusGatewayTimeout),
			},
		}
	case path == "/v1/messages",
		strings.HasPrefix(path, "/v1/messages/"),
		path == "/v1/usage",
		path == "/antigravity/models",
		strings.HasPrefix(path, "/antigravity/v1/"):
		return gin.H{
			"type": "error",
			"error": gin.H{
				"type":    "timeout_error",
				"message": message,
			},
		}
	default:
		return gin.H{
			"error": gin.H{
				"type":    "timeout_error",
				"message": message,
			},
		}
	}
}

// timeoutOverrideWriter 包装响应写入器：网关超时已触发、且响应尚未提交时，
// handler 的**第一次写出**（无论状态码，含隐式 200 的 body 写、Flush 提交）
// 都会被替换成协议正确的 504，保证"超时=504"的对外契约。
// 已提交的响应（含流式）不能再改写状态码；deadline 后的后续写入直接丢弃，
// 由请求 context 取消驱动 handler 结束并关闭响应流。
// 提交状态一律以底层 writer 的 Written() 为准，避免 Flush/WriteHeaderNow
// 绕过本层记账后再补写导致向已提交的流追加错误体。
type timeoutOverrideWriter struct {
	gin.ResponseWriter
	path        string
	deadlineCtx context.Context
	seconds     int
	overrode    bool
}

func (w *timeoutOverrideWriter) shouldOverride() bool {
	return !w.overrode &&
		!w.ResponseWriter.Written() &&
		w.deadlineCtx.Err() == context.DeadlineExceeded
}

func (w *timeoutOverrideWriter) writeTimeoutResponse() {
	w.overrode = true
	body, err := json.Marshal(timeoutErrorBody(w.path, w.seconds))
	if err != nil {
		body = []byte(`{"error":{"type":"timeout_error","message":"gateway request timeout"}}`)
	}
	header := w.ResponseWriter.Header()
	header.Set("Content-Type", "application/json; charset=utf-8")
	for _, name := range []string{
		"Content-Encoding",
		"Content-Disposition",
		"Content-Language",
		"Content-Length",
		"Content-Range",
		"ETag",
		"Last-Modified",
	} {
		header.Del(name)
	}
	w.ResponseWriter.WriteHeader(http.StatusGatewayTimeout)
	_, _ = w.ResponseWriter.Write(body)
}

func (w *timeoutOverrideWriter) deadlineExceeded() bool {
	return w.deadlineCtx.Err() == context.DeadlineExceeded
}

func (w *timeoutOverrideWriter) WriteHeader(code int) {
	if w.overrode {
		return
	}
	if w.shouldOverride() {
		w.writeTimeoutResponse()
		return
	}
	if w.deadlineExceeded() {
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *timeoutOverrideWriter) Write(b []byte) (int, error) {
	if w.overrode {
		return len(b), nil
	}
	if w.shouldOverride() {
		w.writeTimeoutResponse()
		return len(b), nil
	}
	if w.deadlineExceeded() {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

func (w *timeoutOverrideWriter) WriteString(s string) (int, error) {
	if w.overrode {
		return len(s), nil
	}
	if w.shouldOverride() {
		w.writeTimeoutResponse()
		return len(s), nil
	}
	if w.deadlineExceeded() {
		return len(s), nil
	}
	return w.ResponseWriter.WriteString(s)
}

func (w *timeoutOverrideWriter) WriteHeaderNow() {
	if w.overrode {
		return
	}
	if w.shouldOverride() {
		w.writeTimeoutResponse()
		return
	}
	if w.deadlineExceeded() {
		return
	}
	w.ResponseWriter.WriteHeaderNow()
}

func (w *timeoutOverrideWriter) Flush() {
	if w.overrode {
		return
	}
	if w.shouldOverride() {
		// 第一次提交发生在超时后：写 504 而不是把隐式 200 头 flush 出去
		w.writeTimeoutResponse()
		return
	}
	if w.deadlineExceeded() {
		return
	}
	w.ResponseWriter.Flush()
}

// isWebSocketUpgrade 校验完整的 WebSocket 升级握手，而不是单看 Upgrade 头。
func isWebSocketUpgrade(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	if strings.TrimSpace(r.Header.Get("Sec-WebSocket-Version")) != "13" {
		return false
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key")))
	if err != nil || len(key) != 16 {
		return false
	}
	for _, token := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
			return true
		}
	}
	return false
}

// RequestTimeout 给网关请求设置协作式截止时间。注册在 Ops 日志内层、鉴权之前，
// 计时覆盖鉴权、分组校验和 handler 全程；到点后取消请求上下文，
// 让所有基于 c.Request.Context() 的上游调用中断。
//
// 超时后的响应统一为 504：deadline 之后的第一次写出由 timeoutOverrideWriter
// 改写；handler 静默返回时由 RequestTimeoutFinalizer（链路最内层）补写，
// 保证外层 Ops 错误日志能看到 504。鉴权阶段静默超时也由本层兜底补写，
// 响应会经过外层 Ops writer 并正常入账。
//
// 局限：handler 既无视 context 取消又不写响应时，连接仍要等
// 它返回才结束（现有转发路径都以 request context 发起上游调用，实际会及时返回）。
//
// wsExemptPaths 列出真正的 WebSocket 路由；只有这些路径上的完整升级握手
// 请求才豁免超时，防止任意请求伪造 Upgrade 头绕过限制。
//
// 生效值优先取管理台运行时设置（settings.request_timeout_settings，启用时），
// 否则回退到配置文件/环境变量 gateway.request_timeout_seconds；0 表示不限制。
func RequestTimeout(
	cfgSeconds int,
	settingService *service.SettingService,
	wsExemptPaths ...string,
) gin.HandlerFunc {
	var cached atomic.Pointer[requestTimeoutSnapshot]
	var refreshInFlight atomic.Bool
	exemptPaths := make(map[string]struct{}, len(wsExemptPaths))
	for _, p := range wsExemptPaths {
		exemptPaths[p] = struct{}{}
	}

	loadSeconds := func() (int, bool) {
		seconds := cfgSeconds
		queryCtx, cancel := context.WithTimeout(context.Background(), requestTimeoutSettingsQueryTimeout)
		defer cancel()
		settings, err := settingService.GetRequestTimeoutSettings(queryCtx)
		if err != nil {
			return 0, false
		}
		if settings.Enabled {
			seconds = settings.TimeoutSeconds
		}
		return seconds, true
	}
	initialSeconds := cfgSeconds
	initialTTL := requestTimeoutCacheTTL
	if settingService != nil {
		if seconds, ok := loadSeconds(); ok {
			initialSeconds = seconds
		} else {
			initialTTL = requestTimeoutRefreshRetry
		}
	}
	cached.Store(&requestTimeoutSnapshot{
		seconds:   initialSeconds,
		expiresAt: time.Now().Add(initialTTL),
	})

	resolveSeconds := func() int {
		if settingService == nil {
			return cfgSeconds
		}
		now := time.Now()
		snap := cached.Load()
		if now.Before(snap.expiresAt) {
			return snap.seconds
		}

		// 热路径永远使用最近快照立即继续。只有一个请求负责后台刷新，
		// 避免慢设置存储延长请求时限或在缓存过期时形成并发查库。
		if refreshInFlight.CompareAndSwap(false, true) {
			previous := snap
			go func() {
				defer refreshInFlight.Store(false)
				seconds, ok := loadSeconds()
				ttl := requestTimeoutCacheTTL
				if !ok {
					seconds = previous.seconds
					ttl = requestTimeoutRefreshRetry
				}
				cached.Store(&requestTimeoutSnapshot{
					seconds:   seconds,
					expiresAt: time.Now().Add(ttl),
				})
			}()
		}
		return snap.seconds
	}

	return func(c *gin.Context) {
		if _, exempt := exemptPaths[c.Request.URL.Path]; exempt && isWebSocketUpgrade(c.Request) {
			c.Next()
			return
		}
		startedAt := time.Now()
		seconds := resolveSeconds()
		if seconds <= 0 {
			c.Next()
			return
		}
		ctx, cancel := context.WithDeadline(
			c.Request.Context(),
			startedAt.Add(time.Duration(seconds)*time.Second),
		)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Set(requestTimeoutSecondsKey, seconds)

		originalWriter := c.Writer
		timeoutWriter := &timeoutOverrideWriter{
			ResponseWriter: originalWriter,
			path:           c.Request.URL.Path,
			deadlineCtx:    ctx,
			seconds:        seconds,
		}
		c.Writer = timeoutWriter
		// 内层中间件（如 Ops 日志）会池化复用自己的 writer 并在退出时校验
		// c.Writer 归属，必须在返回前把 writer 还原，panic 时也不例外。
		defer func() { c.Writer = originalWriter }()

		c.Next()

		// 鉴权/分组校验阶段静默超时的兜底（正常 handler 路径由 Finalizer 处理）
		if ctx.Err() == context.DeadlineExceeded && !timeoutWriter.Written() {
			timeoutWriter.writeTimeoutResponse()
		}
	}
}

// RequestTimeoutFinalizer 注册在链路最内层（handler 之前）：handler 超时后
// 静默返回（多数转发路径把 DeadlineExceeded 当客户端断连处理）时补写 504。
// 放在内层是为了让外层的 Ops 错误日志读到 504 状态码。
func RequestTimeoutFinalizer() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		seconds, ok := c.Get(requestTimeoutSecondsKey)
		if !ok {
			return
		}
		if c.Request.Context().Err() == context.DeadlineExceeded && !c.Writer.Written() {
			secondsInt, _ := seconds.(int)
			c.JSON(http.StatusGatewayTimeout, timeoutErrorBody(c.Request.URL.Path, secondsInt))
		}
	}
}
