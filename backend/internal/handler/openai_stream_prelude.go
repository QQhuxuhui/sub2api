package handler

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/Wei-Shaw/sub2api/internal/pkg/firstbyte"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// The early first event of OpenAI streams (service/openai_stream_prelude.go
// explains why). The middleware puts a dormant writer outside the error
// identity scrubber and the ops error logger. The handler arms it once a
// streaming request in scope is known to be servable, with a random moment in
// the group's range after the request arrived.
//
// Armed, the response goes out as the handler writes it. Real output (a data
// event) or an error status before the moment disarms it for good: the
// request then looks exactly as without the feature. Response headers and SSE
// comments (slot-wait pings, keepalives) do not disarm it — upstreams commit
// 200 headers long before their first token.
//
// At the moment, the writer becomes active:
//
//   - the network gets a 200 text/event-stream response (unless the handler
//     already committed one) and the early event;
//   - everything inside sees only what the handler and the forwarding code
//     wrote: Size, Written, Status and Header leave the early event out, so
//     failover, error handling and the ops logger decide exactly as without it;
//   - until real output starts, the same neutral event is repeated as a
//     heartbeat; after that, output goes through unchanged (Chat Completions
//     chunk ids are rewritten to the early event's);
//   - an error response written afterwards (status >= 400) is turned into the
//     in-stream error of the protocol when the handler returns.
//
// The early event is the client's first byte (client_first_byte_ms), the same
// moment a sub2api downstream records as its first token; first_token_ms stays
// the upstream's real first token.

// openAIStreamPreludeHeartbeat is how often the neutral event is repeated
// while no real output has been written; tests shorten it.
var openAIStreamPreludeHeartbeat = 10 * time.Second

const (
	openAIStreamPreludeWriterKey     = "openai_stream_prelude_writer"
	openAIStreamPreludeErrorBodyMax  = 64 << 10
	openAIStreamPreludeLineBufferMax = 1 << 20
)

// OpenAIStreamPreludeMiddleware installs the dormant writer. It must be
// registered before OpenAIErrorIdentityMiddleware and OpsErrorLoggerMiddleware
// so that both see the handler's own status and body.
func OpenAIStreamPreludeMiddleware(router *service.IntentRouterService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if router == nil || c.Request == nil || c.Request.Method != http.MethodPost || !openAIStreamPreludePath(c.Request.URL.Path) {
			c.Next()
			return
		}
		original := c.Writer
		w := &openAIStreamPreludeWriter{ResponseWriter: original, c: c, router: router, fwdSize: -1}
		c.Writer = w
		c.Set(openAIStreamPreludeWriterKey, w)
		defer func() {
			w.finish()
			c.Writer = original
		}()
		c.Next()
	}
}

// openAIStreamPreludePath reports whether the path is a Chat Completions or
// Responses entry (not a Responses subpath such as compact).
func openAIStreamPreludePath(path string) bool {
	path = strings.TrimRight(path, "/")
	return strings.HasSuffix(path, "/chat/completions") || strings.HasSuffix(path, "/responses")
}

// openAIStreamPreludeRequest is what the handler knows when it may arm the
// early event.
type openAIStreamPreludeRequest struct {
	apiKey   *service.APIKey
	userID   int64
	model    string // as the client wrote it; echoed in the Chat chunks
	schedule string // the model accounts are selected for
	// platform and capability are what the request's own account selection
	// asks for.
	platform   string
	capability service.OpenAIEndpointCapability
	chat       bool // Chat Completions; otherwise Responses
}

// startOpenAIStreamPrelude arms the early event when the request is in the
// group's scope and the group could serve it now; otherwise it does nothing.
// Call it for streaming requests only, after the request has been validated
// and before waiting for slots or selecting accounts.
func startOpenAIStreamPrelude(c *gin.Context, req openAIStreamPreludeRequest) {
	value, ok := c.Get(openAIStreamPreludeWriterKey)
	if !ok {
		return
	}
	w, ok := value.(*openAIStreamPreludeWriter)
	if !ok || w == nil || req.apiKey == nil || req.apiKey.GroupID == nil {
		return
	}
	ctx := c.Request.Context()
	groupID := *req.apiKey.GroupID
	minDelay, maxDelay, ok := w.router.OpenAIStreamPreludeDelay(ctx, groupID, req.userID, req.apiKey.ID)
	if !ok {
		return
	}
	if !w.router.OpenAIStreamPreludeServableNow(ctx, groupID, req.platform, req.schedule, req.capability) {
		return
	}
	start := time.Now()
	if tracker := firstbyte.FromContext(ctx); tracker != nil {
		start = tracker.Start()
	}
	w.arm(req, groupID, start.Add(openAIStreamPreludeRandomDelay(minDelay, maxDelay)))
}

func openAIStreamPreludeRandomDelay(minDelay, maxDelay time.Duration) time.Duration {
	if maxDelay <= minDelay {
		return minDelay
	}
	return minDelay + time.Duration(mrand.Int64N(int64(maxDelay-minDelay)+1))
}

type openAIStreamPreludeState int

const (
	preludeDormant openAIStreamPreludeState = iota // never armed, or disarmed
	preludeArmed                                   // waiting for the moment
	preludeActive                                  // the early event went out
)

type openAIStreamPreludeMode int

const (
	preludeModeUndecided openAIStreamPreludeMode = iota
	preludeModePass                              // a stream: forwarded as it comes
	preludeModeError                             // an error response: converted at the end
)

type openAIStreamPreludeWriter struct {
	gin.ResponseWriter
	c      *gin.Context
	router *service.IntentRouterService

	mu     sync.Mutex
	state  openAIStreamPreludeState
	done   bool // finished: nothing more is written to the network
	req    openAIStreamPreludeRequest
	group  int64
	chatID string
	stop   chan struct{}
	// Captured when armed: the handler replaces c.Request while the timer
	// and the heartbeat run.
	tracker *firstbyte.Tracker
	reqDone <-chan struct{}

	// header is the handler's view of the response headers from arming on.
	// Only the handler's goroutine touches it (the handler mutates it outside
	// mu); the network's map is only touched under mu: copied from header
	// when the handler commits while armed, and by the early event.
	header http.Header
	// What the handler and the forwarding code wrote, as they see it (active).
	status  int
	fwdSize int
	mode    openAIStreamPreludeMode
	output  bool   // a real (non-comment) byte was written
	failed  bool   // the stream carried a failure event
	lineBuf []byte // Chat: an incomplete line waiting for its end
	scanBuf []byte // Responses: an incomplete line, for failure detection
	errBuf  []byte

	preludeAt  time.Time
	outputAt   time.Time
	heartbeats int
	writeErr   error
	outcome    string // recorded at finish
}

// arm schedules the early event at the given moment (at once when it has
// passed).
func (w *openAIStreamPreludeWriter) arm(req openAIStreamPreludeRequest, groupID int64, at time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state != preludeDormant || w.done || w.ResponseWriter.Size() > 0 || w.ResponseWriter.Status() >= http.StatusBadRequest {
		return
	}
	w.state = preludeArmed
	w.req = req
	w.group = groupID
	w.header = w.ResponseWriter.Header().Clone()
	w.stop = make(chan struct{})
	w.tracker = firstbyte.FromContext(w.c.Request.Context())
	w.reqDone = w.c.Request.Context().Done()
	if req.chat {
		w.chatID = "chatcmpl-" + openAIStreamPreludeRandomHex(12)
	}
	wait := time.Until(at)
	if wait <= 0 {
		w.fireLocked()
		return
	}
	go w.timer(wait)
}

func (w *openAIStreamPreludeWriter) timer(wait time.Duration) {
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-w.stop:
		return
	case <-w.reqDone:
		return
	case <-t.C:
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state == preludeArmed && !w.done {
		w.fireLocked()
	}
}

// disarmLocked gives up the early event: the writer is dormant from now on.
// Handler goroutine only (it reads header).
func (w *openAIStreamPreludeWriter) disarmLocked() {
	w.syncHeaderLocked()
	w.state = preludeDormant
	close(w.stop)
}

// syncHeaderLocked makes the network's headers the handler's view of them.
// Handler goroutine only.
func (w *openAIStreamPreludeWriter) syncHeaderLocked() {
	real := w.ResponseWriter.Header()
	for k := range real {
		delete(real, k)
	}
	for k, v := range w.header {
		real[k] = v
	}
}

// fireLocked sends the early event, unless what the handler committed in the
// meantime rules it out. It runs on the timer's goroutine, so it judges by the
// network's side only and never reads the handler's header map: headers the
// handler set but has not committed yet are not sent with the early event
// (once it is out they could not be sent anyway).
func (w *openAIStreamPreludeWriter) fireLocked() {
	committed := w.ResponseWriter.Written()
	if w.ResponseWriter.Status() >= http.StatusBadRequest {
		return // the handler's error commits (and disarms) on its own
	}
	if ct := w.ResponseWriter.Header().Get("Content-Type"); committed && ct != "" && !strings.HasPrefix(ct, "text/event-stream") {
		return // not a stream after all; stays armed and goes out as written
	}
	// The handler's view so far: headers or comments it already sent.
	w.fwdSize = w.ResponseWriter.Size()
	if committed {
		w.status = w.ResponseWriter.Status()
		w.mode = preludeModePass
	} else {
		header := w.ResponseWriter.Header()
		header.Set("Content-Type", "text/event-stream")
		header.Set("Cache-Control", "no-cache")
		header.Set("Connection", "keep-alive")
		header.Set("X-Accel-Buffering", "no")
		header.Del("Content-Length")
		w.ResponseWriter.WriteHeader(http.StatusOK)
	}
	w.state = preludeActive
	w.emit(w.neutralEvent(true))
	w.preludeAt = time.Now()
	go w.heartbeat(openAIStreamPreludeHeartbeat)
}

// emit writes gateway-made bytes to the network. The first one counts as the
// client's first byte (client_first_byte_ms), matching what a sub2api
// downstream records as its first token.
func (w *openAIStreamPreludeWriter) emit(b []byte) {
	if w.writeErr != nil {
		return
	}
	_, w.writeErr = w.ResponseWriter.Write(b)
	if w.writeErr == nil {
		w.ResponseWriter.Flush()
	}
}

func (w *openAIStreamPreludeWriter) heartbeat(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-w.reqDone:
			return
		case <-ticker.C:
		}
		w.mu.Lock()
		if w.done || w.output || w.mode == preludeModeError || w.writeErr != nil {
			w.mu.Unlock()
			return
		}
		w.emit(w.neutralEvent(false))
		w.heartbeats++
		w.mu.Unlock()
	}
}

// openAIStreamPreludeResponsesEvent is the early Responses event. A sub2api
// downstream (semantic TTFT) counts every event type as the first token except
// lifecycle metadata and transport heartbeats — since 0.2.6 that list is
// response.created/queued/in_progress/metadata and keepalive — so the early
// event must not be any of them. Clients ignore event types they do not know.
const openAIStreamPreludeResponsesEvent = "response.relay_ready"

// neutralEvent is an event a client ignores but a sub2api downstream counts
// as the first token: an empty assistant delta, or an event type Responses
// clients do not know. Repeats on Responses are plain keepalives, which only
// need to keep the connection busy.
func (w *openAIStreamPreludeWriter) neutralEvent(first bool) []byte {
	if !w.req.chat {
		if !first {
			return []byte("event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n")
		}
		return []byte("event: " + openAIStreamPreludeResponsesEvent + "\ndata: {\"type\":\"" + openAIStreamPreludeResponsesEvent + "\"}\n\n")
	}
	delta := map[string]any{"content": ""}
	if first {
		delta["role"] = "assistant"
	}
	chunk, _ := json.Marshal(map[string]any{
		"id":      w.chatID,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   w.req.model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}},
	})
	return append(append([]byte("data: "), chunk...), '\n', '\n')
}

// ---- gin.ResponseWriter, as seen from inside ----

func (w *openAIStreamPreludeWriter) Header() http.Header {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state == preludeDormant {
		return w.ResponseWriter.Header()
	}
	return w.header
}

func (w *openAIStreamPreludeWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch w.state {
	case preludeArmed:
		w.syncHeaderLocked()
		w.ResponseWriter.WriteHeader(code)
		if code >= http.StatusBadRequest {
			w.disarmLocked()
		}
	case preludeActive:
		if code > 0 && w.fwdSize < 0 {
			w.status = code
		}
	default:
		w.ResponseWriter.WriteHeader(code)
	}
}

func (w *openAIStreamPreludeWriter) WriteHeaderNow() {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch w.state {
	case preludeArmed:
		w.syncHeaderLocked()
		w.ResponseWriter.WriteHeaderNow()
	case preludeActive:
		if w.fwdSize < 0 {
			w.fwdSize = 0
		}
	default:
		w.ResponseWriter.WriteHeaderNow()
	}
}

func (w *openAIStreamPreludeWriter) Status() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state != preludeActive {
		return w.ResponseWriter.Status()
	}
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *openAIStreamPreludeWriter) Size() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state != preludeActive {
		return w.ResponseWriter.Size()
	}
	return w.fwdSize
}

func (w *openAIStreamPreludeWriter) Written() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state != preludeActive {
		return w.ResponseWriter.Written()
	}
	return w.fwdSize >= 0
}

func (w *openAIStreamPreludeWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

func (w *openAIStreamPreludeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch w.state {
	case preludeDormant:
		return w.ResponseWriter.Write(p)
	case preludeArmed:
		w.syncHeaderLocked()
		// Real output or an error body first: no early event for this request.
		if len(p) > 0 && (w.ResponseWriter.Status() >= http.StatusBadRequest || !firstbyte.IsSSECommentOnly(p)) {
			w.disarmLocked()
		}
		return w.ResponseWriter.Write(p)
	}
	if w.done {
		return 0, http.ErrHandlerTimeout
	}
	if len(p) == 0 {
		return 0, nil
	}
	if w.fwdSize < 0 {
		w.fwdSize = 0
	}
	w.fwdSize += len(p)
	if w.mode == preludeModeUndecided {
		w.mode = preludeModePass
		if w.status >= http.StatusBadRequest {
			w.mode = preludeModeError
		} else if ct := w.header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "text/event-stream") {
			slog.Warn("openai_stream_prelude.non_stream_output", "content_type", ct, "status", w.status)
		}
	}
	if w.mode == preludeModeError {
		if room := openAIStreamPreludeErrorBodyMax - len(w.errBuf); room > 0 {
			w.errBuf = append(w.errBuf, p[:min(room, len(p))]...)
		}
		return len(p), nil
	}
	if !w.output && !firstbyte.IsSSECommentOnly(p) {
		w.output = true
		w.outputAt = time.Now()
	}
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	if !w.req.chat {
		w.scanResponsesLines(p)
		_, w.writeErr = w.ResponseWriter.Write(p)
	} else {
		w.lineBuf = append(w.lineBuf, p...)
		w.passChatLines(false)
	}
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return len(p), nil
}

func (w *openAIStreamPreludeWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch w.state {
	case preludeArmed:
		w.syncHeaderLocked()
		w.ResponseWriter.Flush()
	case preludeActive:
		if w.mode == preludeModePass && !w.done && w.writeErr == nil {
			w.ResponseWriter.Flush()
		}
	default:
		w.ResponseWriter.Flush()
	}
}

// passChatLines writes the complete lines buffered so far (all of them when
// final), giving each chunk the early event's id so the stream has one id.
func (w *openAIStreamPreludeWriter) passChatLines(final bool) {
	out := make([]byte, 0, len(w.lineBuf)+32)
	rest := w.lineBuf
	for {
		idx := bytes.IndexByte(rest, '\n')
		if idx < 0 {
			break
		}
		w.observeLine(rest[:idx])
		out = append(out, w.rewriteChatLine(rest[:idx+1])...)
		rest = rest[idx+1:]
	}
	if final || len(rest) > openAIStreamPreludeLineBufferMax {
		w.observeLine(rest)
		out = append(out, w.rewriteChatLine(rest)...)
		rest = nil
	}
	w.lineBuf = append(w.lineBuf[:0], rest...)
	if len(out) > 0 && w.writeErr == nil {
		_, w.writeErr = w.ResponseWriter.Write(out)
	}
}

// scanResponsesLines watches the Responses output, which is passed on as it
// comes, for failure events across write boundaries.
func (w *openAIStreamPreludeWriter) scanResponsesLines(p []byte) {
	data := p
	if len(w.scanBuf) > 0 {
		data = append(w.scanBuf, p...)
	}
	for {
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			break
		}
		w.observeLine(data[:idx])
		data = data[idx+1:]
	}
	if len(data) > openAIStreamPreludeLineBufferMax {
		data = nil // a giant data line is content, not a failure event
	}
	w.scanBuf = append(w.scanBuf[:0], data...)
}

// observeLine notes a failure event of either protocol: an `event: error` or
// `event: response.failed` line, or a data line whose payload is one (a
// response.failed / error event, or a Chat error object).
func (w *openAIStreamPreludeWriter) observeLine(line []byte) {
	if w.failed {
		return
	}
	line = bytes.TrimRight(line, "\r")
	if event, ok := bytes.CutPrefix(line, []byte("event:")); ok {
		switch string(bytes.TrimSpace(event)) {
		case "error", "response.failed":
			w.failed = true
		}
		return
	}
	payload, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok || !bytes.Contains(payload, []byte(`"error`)) && !bytes.Contains(payload, []byte("response.failed")) {
		return
	}
	payload = bytes.TrimSpace(payload)
	switch gjson.GetBytes(payload, "type").String() {
	case "error", "response.failed":
		w.failed = true
		return
	}
	if gjson.GetBytes(payload, "error").IsObject() {
		w.failed = true
	}
}

func (w *openAIStreamPreludeWriter) rewriteChatLine(line []byte) []byte {
	body := bytes.TrimRight(line, "\r\n")
	payload, ok := bytes.CutPrefix(body, []byte("data:"))
	if !ok {
		return line
	}
	payload = bytes.TrimLeft(payload, " ")
	if len(payload) == 0 || payload[0] != '{' || gjson.GetBytes(payload, "object").String() != "chat.completion.chunk" {
		return line
	}
	id := gjson.GetBytes(payload, "id")
	if !id.Exists() || id.String() == w.chatID {
		return line
	}
	rewritten, err := sjson.SetBytes(payload, "id", w.chatID)
	if err != nil {
		return line
	}
	out := make([]byte, 0, len(rewritten)+8+len(line)-len(body))
	out = append(out, "data: "...)
	out = append(out, rewritten...)
	return append(out, line[len(body):]...)
}

// finish runs when the handler has returned: it stops the timer and the
// heartbeat, writes what is still held back, turns an error response into the
// protocol's in-stream error, and records the request.
func (w *openAIStreamPreludeWriter) finish() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return
	}
	switch w.state {
	case preludeArmed:
		w.disarmLocked()
		w.done = true
		return
	case preludeDormant:
		w.done = true
		return
	}
	close(w.stop)

	outcome := service.OpenAIStreamPreludeOutcomeOK
	switch {
	case w.mode == preludeModePass:
		if w.req.chat && len(w.lineBuf) > 0 {
			w.passChatLines(true)
		}
		if !w.req.chat && len(w.scanBuf) > 0 {
			w.observeLine(w.scanBuf)
		}
		if w.writeErr == nil {
			w.ResponseWriter.Flush()
		}
		_, markedStreamError := service.GetOpsStreamError(w.c)
		switch {
		case w.failed || markedStreamError:
			outcome = service.OpenAIStreamPreludeOutcomeFailed
		case !w.output:
			outcome = service.OpenAIStreamPreludeOutcomeClientGone
		}
	case w.mode == preludeModeError || w.status >= http.StatusBadRequest:
		outcome = service.OpenAIStreamPreludeOutcomeFailed
		w.emit(w.errorEvent())
	default:
		outcome = service.OpenAIStreamPreludeOutcomeClientGone
	}
	w.done = true
	w.outcome = outcome

	start := w.preludeAt
	if w.tracker != nil {
		start = w.tracker.Start()
	}
	rec := service.OpenAIStreamPreludeRecord{
		Time:       w.preludeAt,
		GroupID:    w.group,
		UserID:     w.req.userID,
		APIKeyID:   w.req.apiKey.ID,
		Endpoint:   w.c.Request.URL.Path,
		Model:      w.req.model,
		PreludeMs:  w.preludeAt.Sub(start).Milliseconds(),
		Heartbeats: w.heartbeats,
		Outcome:    outcome,
	}
	if w.output {
		ms := w.outputAt.Sub(start).Milliseconds()
		rec.OutputMs = &ms
	}
	w.router.RecordOpenAIStreamPrelude(rec)
}

// errorEvent is the in-stream form of the error response the handler wrote,
// in the shape handleStreamingAwareError uses once a stream has started.
func (w *openAIStreamPreludeWriter) errorEvent() []byte {
	errType := strings.TrimSpace(gjson.GetBytes(w.errBuf, "error.type").String())
	code := strings.TrimSpace(gjson.GetBytes(w.errBuf, "error.code").String())
	message := strings.TrimSpace(gjson.GetBytes(w.errBuf, "error.message").String())
	if message == "" {
		message = strings.TrimSpace(gjson.GetBytes(w.errBuf, "message").String())
	}
	if message == "" && len(w.errBuf) > 0 && !gjson.ValidBytes(w.errBuf) {
		message = strings.TrimSpace(string(w.errBuf[:min(len(w.errBuf), 500)]))
	}
	if errType == "" {
		errType = "upstream_error"
	}
	if message == "" {
		message = "Upstream request failed"
	}

	if !w.req.chat {
		payload, _ := json.Marshal(responsesFailedEvent{
			Type: "response.failed",
			Response: responsesFailedBody{
				ID:        synthesizeResponseID(w.c),
				Object:    "response",
				CreatedAt: time.Now().Unix(),
				Model:     w.req.model,
				Status:    "failed",
				Output:    []any{},
				Error:     responsesFailedError{Code: mapResponsesErrorCode(errType, code), Message: message},
			},
		})
		return []byte("event: response.failed\ndata: " + string(payload) + "\n\n")
	}
	errorObject := map[string]any{"type": errType, "message": message}
	if code != "" {
		errorObject["code"] = code
	}
	payload, _ := json.Marshal(map[string]any{"error": errorObject})
	return []byte("event: error\ndata: " + string(payload) + "\n\n")
}

func openAIStreamPreludeRandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
