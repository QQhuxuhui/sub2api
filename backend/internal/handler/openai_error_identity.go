package handler

import (
	"bytes"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/erroridentity"
	"github.com/gin-gonic/gin"
)

// OpenAIErrorIdentityMiddleware must be registered BEFORE OpsErrorLoggerMiddleware:
// handler -> audit capture -> identity writer -> network. It never changes the
// bytes seen by account policies, retry logic or the audit capture writer.
func OpenAIErrorIdentityMiddleware(snapshot func() *erroridentity.Scrubber, enabled func(*gin.Context) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		original := c.Writer
		w := &identityResponseWriter{ResponseWriter: original, c: c, enabled: enabled, scrubber: snapshot(), size: -1}
		c.Writer = w
		defer func() { w.finish(); c.Writer = original }()
		c.Next()
	}
}

type identityResponseWriter struct {
	gin.ResponseWriter
	c                               *gin.Context
	enabled                         func(*gin.Context) bool
	scrubber                        *erroridentity.Scrubber
	pending                         []byte
	size                            int
	decided, active, stream, bypass bool
	sseLineBytes                    int
	sseLineCR                       bool
	err                             error
}

func (w *identityResponseWriter) model() string {
	m, _ := w.c.Request.Context().Value(ctxkey.Model).(string)
	return m
}
func (w *identityResponseWriter) decide() {
	if w.decided {
		return
	}
	w.decided = true
	contentType, _, _ := mime.ParseMediaType(w.Header().Get("Content-Type"))
	w.stream = contentType == "text/event-stream"
	// Successful downloads must retain their streaming and length semantics.
	// Unknown content types and error statuses still use the JSON fallback.
	jsonBody := contentType == "" || contentType == "application/json" || strings.HasSuffix(contentType, "+json")
	w.active = w.enabled(w.c) && w.Header().Get("Content-Encoding") == "" &&
		(w.stream || jsonBody || w.Status() >= http.StatusBadRequest)
	if w.active {
		w.Header().Del("Content-Length")
	}
}
func (w *identityResponseWriter) WriteHeaderNow() { w.decide(); w.ResponseWriter.WriteHeaderNow() }
func (w *identityResponseWriter) Written() bool   { return w.size >= 0 || w.ResponseWriter.Written() }
func (w *identityResponseWriter) Size() int {
	if w.size >= 0 {
		return w.size
	}
	return w.ResponseWriter.Size()
}
func (w *identityResponseWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *identityResponseWriter) emit(b []byte) {
	if w.err == nil && len(b) > 0 {
		_, w.err = w.ResponseWriter.Write(b)
	}
}
func (w *identityResponseWriter) Write(p []byte) (int, error) {
	w.decide()
	if w.err != nil {
		return 0, w.err
	}
	if w.size < 0 {
		w.size = 0
	}
	w.size += len(p)
	if !w.active || w.bypass && !w.stream {
		return w.ResponseWriter.Write(p)
	}
	if w.stream {
		w.writeSSE(p)
		return len(p), w.err
	}
	if len(p) > erroridentity.MaxPayload-len(w.pending) {
		erroridentity.RecordSizeLimit()
		slog.Warn("openai error identity payload exceeds buffer limit", "limit", erroridentity.MaxPayload)
		w.bypass = true
		w.emit(w.pending)
		w.pending = nil
		w.emit(p)
		return len(p), w.err
	}
	w.pending = append(w.pending, p...)
	return len(p), w.err
}

func (w *identityResponseWriter) writeSSE(p []byte) {
	for len(p) > 0 && w.err == nil {
		end := w.sseFrameEnd(p)
		complete := end >= 0
		if !complete {
			end = len(p)
		}
		part := p[:end]
		p = p[end:]
		if !w.bypass && len(part) > erroridentity.MaxPayload-len(w.pending) {
			erroridentity.RecordSizeLimit()
			slog.Warn("openai error identity SSE event exceeds buffer limit", "limit", erroridentity.MaxPayload)
			w.bypass = true
			w.emit(w.pending)
			w.pending = nil
		}
		if w.bypass {
			w.emit(part)
		} else {
			w.pending = append(w.pending, part...)
		}
		if complete {
			if !w.bypass {
				w.emit(w.scrubber.SSE(w.pending, w.model()))
			}
			w.pending = nil
			w.bypass = false
		}
	}
}

// Track just enough line state to recognize blank lines across writes, even
// while an oversized event is being passed through without buffering it.
func (w *identityResponseWriter) sseFrameEnd(b []byte) int {
	for i, ch := range b {
		if ch == '\n' {
			blank := w.sseLineBytes == 0 || w.sseLineBytes == 1 && w.sseLineCR
			w.sseLineBytes = 0
			w.sseLineCR = false
			if blank {
				return i + 1
			}
		} else {
			if w.sseLineBytes == 0 {
				w.sseLineCR = ch == '\r'
			}
			w.sseLineBytes = min(w.sseLineBytes+1, 2)
		}
	}
	return -1
}
func (w *identityResponseWriter) Flush() {
	w.decide()
	// Partial SSE events are held until a blank line or EOF. Flushing headers
	// and previously completed events still works, including keepalive comments.
	if !w.stream && w.active && len(w.pending) > 0 && len(bytes.TrimSpace(w.pending)) == 0 {
		// Image generation sends JSON whitespace heartbeats while waiting upstream.
		// Emit those immediately, but never release a partial JSON error on Flush.
		w.emit(w.pending)
		w.pending = nil
	}
	w.ResponseWriter.Flush()
}
func (w *identityResponseWriter) finish() {
	if len(w.pending) == 0 {
		return
	}
	if w.stream {
		w.emit(w.scrubber.SSE(w.pending, w.model()))
	} else {
		w.emit(w.scrubber.JSON(w.pending, w.model(), w.Status() >= 400))
	}
	w.pending = nil
}
