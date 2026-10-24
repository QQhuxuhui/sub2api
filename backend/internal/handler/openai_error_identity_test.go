package handler

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/erroridentity"
	"github.com/gin-gonic/gin"
)

func TestOpenAIErrorIdentityBinaryDownload(t *testing.T) {
	for _, contentType := range []string{"image/png", "application/zip", "application/octet-stream"} {
		t.Run(contentType, func(t *testing.T) {
			r := gin.New()
			w := httptest.NewRecorder()
			r.Use(OpenAIErrorIdentityMiddleware(func() *erroridentity.Scrubber { return nil }, func(*gin.Context) bool { return true }))
			payload := []byte{137, 80, 78, 71}
			r.GET("/", func(c *gin.Context) {
				c.Header("Content-Type", contentType)
				c.Header("Content-Length", strconv.Itoa(len(payload)))
				_, _ = c.Writer.Write(payload)
				c.Writer.Flush()
				if !bytes.Equal(w.Body.Bytes(), payload) {
					t.Error("download was buffered despite Flush")
				}
			})
			r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
			if w.Header().Get("Content-Length") != "4" {
				t.Error("download lost Content-Length")
			}
		})
	}
}

func TestOpenAIErrorIdentitySSESizeLimitIsPerEvent(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n"} {
		for _, chunked := range []bool{false, true} {
			t.Run(strconv.Quote(ending)+"/chunked="+strconv.FormatBool(chunked), func(t *testing.T) {
				large := `data: {"type":"response.image_generation_call.partial_image","partial_image_b64":"` + strings.Repeat("A", erroridentity.MaxPayload) + `"}` + ending + ending
				rawError := "event: error" + ending + `data: {"error":{"message":"hidden codex2api","code":"rate_limit"}}` + ending + ending
				r := gin.New()
				r.Use(OpenAIErrorIdentityMiddleware(func() *erroridentity.Scrubber { return erroridentity.New([]string{"hidden"}) }, func(*gin.Context) bool { return true }))
				var audit *identityAuditWriter
				r.Use(func(c *gin.Context) {
					audit = &identityAuditWriter{ResponseWriter: c.Writer}
					c.Writer = audit
					c.Next()
				})
				r.GET("/", func(c *gin.Context) {
					setOpsRequestContext(c, "public", true)
					c.Header("Content-Type", "text/event-stream")
					if chunked {
						// Cross the size limit before splitting both line endings byte by byte.
						for _, p := range []string{large[:erroridentity.MaxPayload], large[erroridentity.MaxPayload : len(large)-2*len(ending)]} {
							_, _ = c.Writer.WriteString(p)
						}
						for _, b := range []byte(ending + ending) {
							_, _ = c.Writer.Write([]byte{b})
						}
						_, _ = c.Writer.WriteString(rawError)
					} else {
						_, _ = c.Writer.WriteString(large + rawError)
					}
					c.Writer.Flush()
				})
				w := httptest.NewRecorder()
				before := erroridentity.CoverageStats().SizeLimit
				r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
				if !bytes.HasPrefix(w.Body.Bytes(), []byte(large)) {
					t.Fatal("oversized event bytes changed")
				}
				if got := string(w.Body.Bytes()[len(large):]); strings.Contains(got, "hidden") || !strings.Contains(got, "public upstream") || !strings.Contains(got, "rate_limit") {
					t.Fatalf("following error was not scrubbed: %s", got)
				}
				if audit.raw.String() != large+rawError {
					t.Fatal("audit bytes changed")
				}
				if erroridentity.CoverageStats().SizeLimit != before+1 {
					t.Fatal("expected one oversized event")
				}
			})
		}
	}
}

func TestOpenAIErrorIdentitySSELargeWriteWithSmallEvents(t *testing.T) {
	// A single network write may contain many individually bounded events.
	normal := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + strings.Repeat("x", 1024) + "\"}\n\n"
	payload := strings.Repeat(normal, erroridentity.MaxPayload/len(normal)+1)
	rawError := "event: error\ndata: {\"message\":\"hidden codex2api\"}\n\n"
	r := gin.New()
	r.Use(OpenAIErrorIdentityMiddleware(func() *erroridentity.Scrubber { return erroridentity.New([]string{"hidden"}) }, func(*gin.Context) bool { return true }))
	r.GET("/", func(c *gin.Context) {
		setOpsRequestContext(c, "public", true)
		c.Header("Content-Type", "text/event-stream")
		_, _ = c.Writer.WriteString(payload + rawError)
	})
	w := httptest.NewRecorder()
	before := erroridentity.CoverageStats().SizeLimit
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if !bytes.HasPrefix(w.Body.Bytes(), []byte(payload)) || !strings.Contains(string(w.Body.Bytes()[len(payload):]), "public upstream") {
		t.Fatal("batched SSE events were not processed independently")
	}
	if erroridentity.CoverageStats().SizeLimit != before {
		t.Fatal("aggregate write size counted as an oversized event")
	}
}

func TestOpenAIErrorIdentityErrorContentTypes(t *testing.T) {
	for _, contentType := range []string{"", "text/plain", "application/octet-stream", "application/problem+json"} {
		t.Run(contentType, func(t *testing.T) {
			r := gin.New()
			r.Use(OpenAIErrorIdentityMiddleware(func() *erroridentity.Scrubber { return erroridentity.New([]string{"hidden"}) }, func(*gin.Context) bool { return true }))
			r.GET("/", func(c *gin.Context) {
				setOpsRequestContext(c, "public", false)
				c.Header("Content-Type", contentType)
				c.Status(http.StatusBadRequest)
				_, _ = c.Writer.WriteString(`{"detail":"hidden codex2api"}`)
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
			if w.Code != http.StatusBadRequest || w.Body.String() != `{"detail":"public upstream"}` {
				t.Fatalf("error was bypassed: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

type identityAuditWriter struct {
	gin.ResponseWriter
	raw strings.Builder
}

func (w *identityAuditWriter) Write(b []byte) (int, error) {
	w.raw.Write(b)
	return w.ResponseWriter.Write(b)
}
func (w *identityAuditWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func TestOpenAIErrorIdentityHTTP(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			r := gin.New()
			scrub := erroridentity.New([]string{"hidden"})
			var audit *identityAuditWriter
			r.Use(OpenAIErrorIdentityMiddleware(func() *erroridentity.Scrubber { return scrub }, func(*gin.Context) bool { return true }))
			r.Use(func(c *gin.Context) {
				audit = &identityAuditWriter{ResponseWriter: c.Writer}
				c.Writer = audit
				c.Next()
			})
			r.GET("/", func(c *gin.Context) {
				setOpsRequestContext(c, "public", stream)
				if stream {
					c.Header("Content-Type", "text/event-stream")
					for _, part := range []string{"event: error\r\ndata: {\"error\":\r\n", "data: {\"message\":\"hidden\"}}\r\n", "\r\n"} {
						_, _ = io.WriteString(c.Writer, part)
						c.Writer.Flush()
					}
				} else {
					c.Header("Content-Type", "application/json")
					c.Header("Content-Length", "30")
					c.Status(429)
					_, _ = c.Writer.Write([]byte(`{"error":{"message":`))
					c.Writer.Flush()
					_, _ = c.Writer.WriteString(`"hidden","code":"rate_limit"}}`)
				}
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
			if strings.Contains(w.Body.String(), "hidden") || !strings.Contains(w.Body.String(), "public") {
				t.Fatal(w.Body.String())
			}
			if !strings.Contains(audit.raw.String(), "hidden") {
				t.Fatal("audit lost original")
			}
			want := http.StatusTooManyRequests
			if stream {
				want = 200
			}
			if w.Code != want {
				t.Fatal(w.Code)
			}
			if w.Header().Get("Content-Length") != "" {
				t.Fatal("stale length")
			}
		})
	}
}

func TestOpenAIErrorIdentityJSONKeepalive(t *testing.T) {
	r := gin.New()
	r.Use(OpenAIErrorIdentityMiddleware(func() *erroridentity.Scrubber { return erroridentity.New([]string{"hidden"}) }, func(*gin.Context) bool { return true }))
	w := httptest.NewRecorder()
	r.GET("/", func(c *gin.Context) {
		setOpsRequestContext(c, "public", false)
		c.Header("Content-Type", "application/json")
		_, _ = c.Writer.WriteString(" \n")
		c.Writer.Flush()
		if w.Body.String() != " \n" {
			t.Error("JSON keepalive was buffered")
		}
		_, _ = c.Writer.WriteString(`{"error":{"message":"hidden"}}`)
	})
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), "public") || strings.Contains(w.Body.String(), "hidden") {
		t.Fatal(w.Body.String())
	}
}

func TestOpenAIErrorIdentityOtherPlatform(t *testing.T) {
	r := gin.New()
	r.Use(OpenAIErrorIdentityMiddleware(func() *erroridentity.Scrubber { return erroridentity.New([]string{"hidden"}) }, func(*gin.Context) bool { return false }))
	r.GET("/", func(c *gin.Context) { c.JSON(400, gin.H{"error": gin.H{"message": "hidden codex2api"}}) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), "hidden codex2api") {
		t.Fatal(w.Body.String())
	}
}
