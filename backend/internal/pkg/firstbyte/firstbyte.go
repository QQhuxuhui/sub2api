// Package firstbyte 记录「客户端实际首字节时间」：从请求进入网关到第一段响应体
// 真正写回客户端的耗时。它与 usage_logs.first_token_ms 的区别在于起点：
// first_token_ms 从最后一次成功的上游转发开始计时，账号重试、换号、排队都
// 不在内；本包从中间件收到请求那一刻起算，和客户端看到的体验一致。
package firstbyte

import (
	"bytes"
	"context"
	"sync/atomic"
	"time"
)

// Tracker 记录单个请求的到达时刻与首个响应体字节写出的时刻。
// 所有方法并发安全：流式响应里写出发生在转发 goroutine，用量落库发生在
// worker 池，两者可能同时读写。
type Tracker struct {
	start        time.Time
	firstByteAt  atomic.Int64 // UnixNano；0 表示尚未写出任何响应体
	markedFirst  atomic.Bool
	sseKeepalive atomic.Int32 // 被跳过的纯 SSE 注释写出次数（仅诊断用）
}

// New 以 start 作为请求到达时刻创建 Tracker。
func New(start time.Time) *Tracker {
	if start.IsZero() {
		start = time.Now()
	}
	return &Tracker{start: start}
}

// Start 返回请求到达时刻。
func (t *Tracker) Start() time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.start
}

// MarkWrite 在每次向客户端写响应体时调用。只有第一段「有内容」的写出会被记录：
// 空写出和网关自己为了保活发出的纯 SSE 注释（": keepalive\n\n"、": ping\n\n"）
// 不算首字节，否则保活会把真实等待掩盖掉。
func (t *Tracker) MarkWrite(data []byte) {
	if t == nil || t.markedFirst.Load() || len(data) == 0 {
		return
	}
	if isSSECommentOnly(data) {
		t.sseKeepalive.Add(1)
		return
	}
	t.MarkNow()
}

// MarkNow 把当前时刻记为首字节时刻（幂等，只有第一次生效）。
func (t *Tracker) MarkNow() {
	if t == nil {
		return
	}
	if t.markedFirst.CompareAndSwap(false, true) {
		t.firstByteAt.Store(time.Now().UnixNano())
	}
}

// FirstByteAt 返回首字节写出时刻；尚未写出时 ok=false。
func (t *Tracker) FirstByteAt() (time.Time, bool) {
	if t == nil {
		return time.Time{}, false
	}
	ns := t.firstByteAt.Load()
	if ns == 0 {
		return time.Time{}, false
	}
	return time.Unix(0, ns), true
}

// ClientFirstByteMs 返回「请求到达 → 首字节写出」的毫秒数；尚未写出时 ok=false。
func (t *Tracker) ClientFirstByteMs() (int, bool) {
	at, ok := t.FirstByteAt()
	if !ok {
		return 0, false
	}
	ms := at.Sub(t.start).Milliseconds()
	if ms < 0 {
		ms = 0
	}
	return int(ms), true
}

// SkippedKeepalives 返回被跳过的纯注释写出次数，仅用于测试与诊断。
func (t *Tracker) SkippedKeepalives() int {
	if t == nil {
		return 0
	}
	return int(t.sseKeepalive.Load())
}

// IsSSECommentOnly 是 isSSECommentOnly 的导出版本：只含 SSE 注释行与空行的写出
// 不携带上游内容。
func IsSSECommentOnly(data []byte) bool { return isSSECommentOnly(data) }

// isSSECommentOnly 判断一段写出是否只包含 SSE 注释行（以 ':' 开头）与空行。
// 这类帧是网关自身的保活，不携带上游内容。
func isSSECommentOnly(data []byte) bool {
	rest := data
	sawComment := false
	for len(rest) > 0 {
		line := rest
		if idx := bytes.IndexByte(rest, '\n'); idx >= 0 {
			line = rest[:idx]
			rest = rest[idx+1:]
		} else {
			rest = nil
		}
		line = bytes.TrimRight(line, "\r")
		if len(line) == 0 {
			continue
		}
		if line[0] != ':' {
			return false
		}
		sawComment = true
	}
	return sawComment
}

type ctxKey struct{}

// WithTracker 把 Tracker 挂到 context 上。
func WithTracker(ctx context.Context, t *Tracker) context.Context {
	if ctx == nil || t == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, t)
}

// FromContext 取出 context 上的 Tracker，没有时返回 nil。
func FromContext(ctx context.Context) *Tracker {
	if ctx == nil {
		return nil
	}
	t, _ := ctx.Value(ctxKey{}).(*Tracker)
	return t
}

// ClientFirstByteMsFromContext 是 FromContext + ClientFirstByteMs 的组合，
// 供用量落库直接取值：没有 Tracker 或尚未写出首字节时返回 nil。
func ClientFirstByteMsFromContext(ctx context.Context) *int {
	t := FromContext(ctx)
	if t == nil {
		return nil
	}
	ms, ok := t.ClientFirstByteMs()
	if !ok {
		return nil
	}
	return &ms
}
