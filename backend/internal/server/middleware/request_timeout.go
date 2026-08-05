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
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
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

// requestTimeoutControllerKey 存放本请求的超时控制器，
// 供鉴权后的 RequestTimeoutUserOverride 调整 deadline。
const requestTimeoutControllerKey = "gw_request_timeout_controller"

type requestTimeoutSnapshot struct {
	seconds   int
	expiresAt time.Time
}

// requestTimeoutController 允许链路内层（鉴权之后）替换本请求的 deadline。
// context deadline 只能收紧不能放宽，因此保留建立全局 deadline 之前的
// baseCtx，用户级覆盖从 baseCtx 重新派生。
type requestTimeoutController struct {
	baseCtx   context.Context
	startedAt time.Time
	writer    *timeoutOverrideWriter
	cancel    context.CancelFunc // 当前生效 deadline 的 cancel；覆盖时替换
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
	// seconds <= 0 表示本请求未启用网关超时（含用户级 -1 撤销），writer 完全透明，
	// 即使 deadlineCtx 因客户端自带 deadline 而超时也不改写。
	return !w.overrode &&
		w.seconds > 0 &&
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
	return w.seconds > 0 && w.deadlineCtx.Err() == context.DeadlineExceeded
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
// 全局值（管理台运行时设置 > 配置文件 gateway.request_timeout_seconds）在此建立；
// 鉴权之后由 RequestTimeoutUserOverride 按用户级 request_timeout_seconds 替换
// deadline（可收紧或放宽）。为此本层总是安装 writer 与 controller——即使全局
// 为 0（不限制），用户级仍可能需要建立 deadline。
//
// 超时后的响应统一为 504：deadline 之后的第一次写出由 timeoutOverrideWriter
// 改写；handler 静默返回时由 RequestTimeoutFinalizer（链路最内层）补写，
// 保证外层 Ops 错误日志能看到 504。鉴权阶段静默超时也由本层兜底补写，
// 响应会经过外层 Ops writer 并正常入账。
//
// 局限：handler 既无视 context 取消又不写响应时，连接仍要等
// 它返回才结束（现有转发路径都以 request context 发起上游调用，实际会及时返回）。
// 已知边界：writer 与兜底以请求 context 的 DeadlineExceeded 判定超时，未区分
// 网关自建 deadline 与父 context 更早的 deadline；若未来有上游中间件注入更短的
// 父 deadline，504 消息会错误声称网关秒数（当前路由不存在这样的上游 deadline）。
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
		baseCtx := c.Request.Context()
		ctx := baseCtx
		cancel := context.CancelFunc(func() {})
		if seconds > 0 {
			ctx, cancel = context.WithDeadline(baseCtx, startedAt.Add(time.Duration(seconds)*time.Second))
			c.Request = c.Request.WithContext(ctx)
			c.Set(requestTimeoutSecondsKey, seconds)
		}

		originalWriter := c.Writer
		timeoutWriter := &timeoutOverrideWriter{
			ResponseWriter: originalWriter,
			path:           c.Request.URL.Path,
			deadlineCtx:    ctx,
			seconds:        seconds,
		}
		c.Writer = timeoutWriter
		ctrl := &requestTimeoutController{
			baseCtx:   baseCtx,
			startedAt: startedAt,
			writer:    timeoutWriter,
			cancel:    cancel,
		}
		c.Set(requestTimeoutControllerKey, ctrl)
		// 内层中间件（如 Ops 日志）会池化复用自己的 writer 并在退出时校验
		// c.Writer 归属，必须在返回前把 writer 还原，panic 时也不例外。
		// cancel 经由 ctrl 调用：用户级覆盖会把它替换成新 deadline 的 cancel。
		defer func() {
			c.Writer = originalWriter
			ctrl.cancel()
		}()

		c.Next()

		// 鉴权/分组校验阶段静默超时的兜底（正常 handler 路径由 Finalizer 处理）；
		// 以最终生效的请求 context 为准（可能已被用户级覆盖替换）。
		if c.Request.Context().Err() == context.DeadlineExceeded && !timeoutWriter.Written() && timeoutWriter.seconds > 0 {
			timeoutWriter.writeTimeoutResponse()
		}
	}
}

// unlimitedRequestTimeoutSeconds 用户级"不限制"的唯一合法哨兵值。
// 其余负值视为非法数据（DB 被绕过校验写入等），回退全局并告警。
const unlimitedRequestTimeoutSeconds = -1

// maxUserRequestTimeoutSeconds 用户级超时上限（1 天），与 DB CHECK 约束、管理 API
// 校验一致。中间件对越界值（含历史缓存快照里的脏数据）同样防御。
const maxUserRequestTimeoutSeconds = 86400

// deadlineSwappedContext 组合两条 context 链：Done/Deadline/Err 来自 lifecycle
// （从 baseCtx 重新派生的 deadline，或 baseCtx 本身），Value 优先走鉴权后的
// values 链。替换 deadline 时必须保留鉴权阶段写入的值（ctxkey.UserID、
// ctxkey.Group、ctxkey.ForcePlatform 等），否则调度平台、计费归属会错乱。
//
// values 必须经 context.WithoutCancel 包装：它会拦截标准库内部取消键的查找，
// 使 context.Cause 不会命中旧（已被取消的）deadline 链；未命中时回退 lifecycle，
// 保证 Err() 与 Cause() 一致（真实网关超时不会被 HTTP transport 当成客户端断开）。
type deadlineSwappedContext struct {
	context.Context // lifecycle：提供 Done/Deadline/Err
	values          context.Context
}

func (c deadlineSwappedContext) Value(key any) any {
	if v := c.values.Value(key); v != nil {
		return v
	}
	return c.Context.Value(key)
}

// revertRequestTimeoutOverride 撤销一次已提交的 deadline 替换，恢复替换前的
// （已到期的）context 与 writer 状态，让 504 兜底按原全局秒数收尾。
func revertRequestTimeoutOverride(c *gin.Context, ctrl *requestTimeoutController, oldCtx context.Context, oldSeconds int) {
	ctrl.cancel() // 取消刚建立的新 lifecycle（-1 分支为 no-op）
	ctrl.cancel = func() {}
	c.Request = c.Request.WithContext(oldCtx)
	ctrl.writer.deadlineCtx = oldCtx
	ctrl.writer.seconds = oldSeconds
	c.Set(requestTimeoutSecondsKey, oldSeconds)
}

// RequestTimeoutUserOverride 注册在鉴权之后：按用户级 request_timeout_seconds
// 调整本请求的 deadline。0 = 继承全局；-1 = 不限制（撤销全局 deadline）；
// 1..86400 = 以请求进入时刻起算的用户专属超时，可收紧也可放宽全局值。
// 依赖 RequestTimeout 存入的 controller；WS 豁免等未装 controller 的请求直接放行。
// 用户值来自鉴权缓存快照中的 apiKey.User，零额外查库。
//
// 已超时/已断连的请求一律 c.Abort()，不再执行 handler（避免副作用），
// 504 由外层 RequestTimeout 的兜底补写。deadline 替换提交后还会复检旧 context，
// 防止全局 deadline 恰在"检查→提交"窗口内自然到期而被静默放宽。
func RequestTimeoutUserOverride() gin.HandlerFunc {
	return func(c *gin.Context) {
		v, ok := c.Get(requestTimeoutControllerKey)
		if !ok {
			c.Next()
			return
		}
		ctrl, ok := v.(*requestTimeoutController)
		if !ok || ctrl == nil {
			c.Next()
			return
		}
		apiKey, ok := GetAPIKeyFromContext(c)
		if !ok || apiKey.User == nil {
			c.Next()
			return
		}
		oldCtx := c.Request.Context()
		oldDeadline, oldHasDeadline := oldCtx.Deadline()
		gatewayDeadlineActive := ctrl.writer.seconds > 0
		// globalDeadlineExpired 判断全局 deadline 是否在逻辑上已到期。
		// Err() 的发布依赖定时器回调，deadline 时刻到达后可能仍短暂为 nil；
		// 而我们主动 oldCancel() 又会把状态固定为 Canceled。因此除 Err 外
		// 必须直接与时钟比较，不能只依赖异步发布的 Err()。
		// 仅在网关自己建立了 deadline 时才做时钟判定（seconds>0），
		// 避免把测试/上游注入的父 context deadline 误认成网关超时。
		globalDeadlineExpired := func() bool {
			if !gatewayDeadlineActive {
				return false
			}
			if oldCtx.Err() == context.DeadlineExceeded {
				return true
			}
			return oldHasDeadline && !time.Now().Before(oldDeadline)
		}
		// 初检：已超时/已断开的请求不允许被用户级配置"复活"，且不执行 handler。
		// 超时场景直接补写 504——Err 可能尚未发布，外层兜底的 Err 判断靠不住。
		if globalDeadlineExpired() {
			ctrl.writer.writeTimeoutResponse()
			c.Abort()
			return
		}
		if oldCtx.Err() != nil { // 客户端断连（Canceled）：中止即可，无需写响应
			c.Abort()
			return
		}
		switch seconds := apiKey.User.RequestTimeoutSeconds; {
		case seconds == unlimitedRequestTimeoutSeconds:
			// 不限制：撤销全局 deadline。生命周期回到 baseCtx（保留客户端断连取消），
			// Value 走 WithoutCancel 包装的鉴权后 context。
			oldSeconds := ctrl.writer.seconds
			ctx := deadlineSwappedContext{Context: ctrl.baseCtx, values: context.WithoutCancel(oldCtx)}
			c.Request = c.Request.WithContext(ctx)
			ctrl.writer.deadlineCtx = ctx
			ctrl.writer.seconds = 0
			c.Set(requestTimeoutSecondsKey, 0)
			oldCancel := ctrl.cancel
			ctrl.cancel = func() {}
			oldCancel()
			// 提交后复检：全局 deadline 恰在初检与提交之间自然到期 → 撤销放宽并
			// 按原全局秒数补写 504（时钟判定对 oldCancel 固定的 Canceled 免疫）。
			if globalDeadlineExpired() {
				revertRequestTimeoutOverride(c, ctrl, oldCtx, oldSeconds)
				ctrl.writer.writeTimeoutResponse()
				c.Abort()
				return
			}
			// 客户端在初检后断开：unlimited 生命周期来自 baseCtx，同样中止。
			if ctx.Err() != nil {
				c.Abort()
				return
			}
		case seconds > 0 && seconds <= maxUserRequestTimeoutSeconds:
			oldSeconds := ctrl.writer.seconds
			lifecycle, cancel := context.WithDeadline(ctrl.baseCtx, ctrl.startedAt.Add(time.Duration(seconds)*time.Second))
			ctx := deadlineSwappedContext{Context: lifecycle, values: context.WithoutCancel(oldCtx)}
			c.Request = c.Request.WithContext(ctx)
			ctrl.writer.deadlineCtx = ctx
			ctrl.writer.seconds = seconds
			c.Set(requestTimeoutSecondsKey, seconds)
			oldCancel := ctrl.cancel
			ctrl.cancel = cancel
			oldCancel()
			// 提交后复检：全局 deadline 恰在初检与提交之间自然到期 → 撤销放宽并
			// 按原全局秒数补写 504（时钟判定对 oldCancel 固定的 Canceled 免疫）。
			if globalDeadlineExpired() {
				revertRequestTimeoutOverride(c, ctrl, oldCtx, oldSeconds)
				ctrl.writer.writeTimeoutResponse()
				c.Abort()
				return
			}
			// 用户 deadline 在鉴权完成时已经过期（WithDeadline 会同步发布
			// DeadlineExceeded，兜底可写 504），或客户端在初检后断开 → 中止。
			if lifecycle.Err() != nil {
				c.Abort()
				return
			}
		case seconds != 0:
			// 契约只定义 -1 与 1..86400；其余值（< -1 或 > 86400）不生效，
			// 维持全局 deadline 并告警。
			logger.FromContext(c.Request.Context()).Warn(
				"invalid user request_timeout_seconds, falling back to global timeout",
				zap.Int("request_timeout_seconds", seconds),
				zap.Int64("user_id", apiKey.User.ID),
			)
		}
		c.Next()
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
		secondsInt, _ := seconds.(int)
		// 用户级 -1（不限制）会把秒数键归零撤销超时；deadline 若来自客户端 context 也不补写。
		if secondsInt <= 0 {
			return
		}
		if c.Request.Context().Err() == context.DeadlineExceeded && !c.Writer.Written() {
			c.JSON(http.StatusGatewayTimeout, timeoutErrorBody(c.Request.URL.Path, secondsInt))
		}
	}
}
