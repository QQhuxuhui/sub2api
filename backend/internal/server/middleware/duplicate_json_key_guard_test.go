//go:build unit

package middleware

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type guardDownstream struct {
	called        bool
	body          []byte
	readErr       error
	contentLength int64
	header        http.Header
	bodyType      string
}

// newGuardRouter mounts the guard the way gateway.go does: wrapping a
// finalizer-like handler that continues the chain.
func newGuardRouter(t *testing.T, limits map[string]int64, before ...gin.HandlerFunc) (*gin.Engine, *guardDownstream, *observer.ObservedLogs) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	core, logs := observer.New(zapcore.WarnLevel)
	down := &guardDownstream{}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(logger.IntoContext(c.Request.Context(), zap.New(core)))
		groupID := int64(7)
		c.Set(string(ContextKeyAPIKey), &service.APIKey{ID: 42, UserID: 148, GroupID: &groupID})
		c.Next()
	})
	r.Use(before...)
	r.Use(WithDuplicateJSONKeyGuard(func(c *gin.Context) { c.Next() }, limits))
	handler := func(c *gin.Context) {
		down.called = true
		down.contentLength = c.Request.ContentLength
		down.header = c.Request.Header.Clone()
		switch c.Request.Body.(type) {
		case *httputil.PrereadBody:
			down.bodyType = "preread"
		default:
			down.bodyType = "other"
		}
		down.body, down.readErr = io.ReadAll(c.Request.Body)
		c.Status(http.StatusNoContent)
	}
	for _, p := range []string{
		"/v1/responses", "/v1/responses/*subpath", "/v1/messages", "/v1/systemone", "/v1/chat/completions",
		"/v1beta/models/*modelAction", "/v1/images/edits", "/v1/images/generations", "/v1/videos",
		"/custom-voices/:voice_id", "/v1/stt", "/contents/generations/tasks",
	} {
		r.POST(p, handler)
		r.PATCH(p, handler)
	}
	r.POST("/v1/embeddings", RequestBodyLimit(limits["/v1/embeddings"]), handler)
	r.GET("/v1/responses", handler)
	return r, down, logs
}

func doGuard(r *gin.Engine, method, path string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

const dupModel = `{"model":"gpt-4o-mini","input":"hi","model":"gpt-5.5-pro"}`

func TestDuplicateJSONKeyGuard_ErrorShapes(t *testing.T) {
	r, down, _ := newGuardRouter(t, nil)
	cases := []struct {
		path  string
		check func(t *testing.T, body string)
	}{
		{"/v1/responses", func(t *testing.T, body string) {
			require.Equal(t, "invalid_request_error", gjson.Get(body, "error.type").String())
			require.Equal(t, "duplicate_json_key", gjson.Get(body, "error.code").String())
			require.True(t, gjson.Get(body, "error.param").Exists())
			require.Equal(t, "Request body contains a duplicate JSON key: /model", gjson.Get(body, "error.message").String())
		}},
		{"/v1/messages", func(t *testing.T, body string) {
			require.Equal(t, "error", gjson.Get(body, "type").String())
			require.Equal(t, "invalid_request_error", gjson.Get(body, "error.type").String())
		}},
		{"/v1/systemone", func(t *testing.T, body string) {
			require.Equal(t, "error", gjson.Get(body, "type").String())
		}},
		{"/v1beta/models/gemini-3:generateContent", func(t *testing.T, body string) {
			require.Equal(t, int64(400), gjson.Get(body, "error.code").Int())
			require.Equal(t, "INVALID_ARGUMENT", gjson.Get(body, "error.status").String())
		}},
		// A dynamic segment named "messages" does not make it an Anthropic route.
		{"/custom-voices/messages", func(t *testing.T, body string) {
			require.False(t, gjson.Get(body, "type").Exists())
			require.Equal(t, "duplicate_json_key", gjson.Get(body, "error.code").String())
		}},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			down.called = false
			method := http.MethodPost
			if strings.HasPrefix(tc.path, "/custom-voices") {
				method = http.MethodPatch
			}
			rec := doGuard(r, method, tc.path, strings.NewReader(dupModel), nil)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.False(t, down.called)
			require.NotContains(t, rec.Body.String(), "gpt-5.5-pro", "values are never echoed")
			tc.check(t, rec.Body.String())
		})
	}
}

func TestDuplicateJSONKeyGuard_PassesBodyUnchanged(t *testing.T) {
	r, down, _ := newGuardRouter(t, nil)
	body := " {\"model\":\"gpt-5.4\",\"input\":\"line\nbreak\"}"
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", io.NopCloser(strings.NewReader(body)))
	req.ContentLength = -1 // chunked: no length known
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.True(t, down.called)
	require.Equal(t, body, string(down.body))
	require.Equal(t, int64(-1), down.contentLength, "length metadata unchanged")
	require.Empty(t, down.header.Get("Content-Length"))
	require.Equal(t, "preread", down.bodyType)
}

func TestDuplicateJSONKeyGuard_MediaTypeDoesNotExemptJSONRoutes(t *testing.T) {
	r, down, _ := newGuardRouter(t, nil)
	for _, ct := range []string{"multipart/form-data; boundary=x", "application/octet-stream", "text/plain"} {
		down.called = false
		rec := doGuard(r, http.MethodPost, "/v1/responses", strings.NewReader(dupModel), map[string]string{"Content-Type": ct})
		require.Equal(t, http.StatusBadRequest, rec.Code, ct)
		require.False(t, down.called)
	}
	// Broken JSON on a JSON-only route is refused even when labelled multipart.
	rec := doGuard(r, http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"m",,"reasoning":{"effort":"low","effort":"max"}}`), map[string]string{"Content-Type": "multipart/form-data; boundary=x"})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "Failed to parse request body")
}

func TestDuplicateJSONKeyGuard_ResponsesRejectsNonJSONPrefix(t *testing.T) {
	r, down, logs := newGuardRouter(t, nil)
	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		down.called = false
		rec := doGuard(r, http.MethodPost, path, strings.NewReader(`xx{"model":"m","reasoning":{"effort":"low","effort":"max"}}`), nil)
		require.Equal(t, http.StatusBadRequest, rec.Code, path)
		require.False(t, down.called)
	}
	require.Equal(t, "non_json_prefix", logs.All()[0].ContextMap()["reason"])

	// Other routes leave non-JSON bodies to their handler.
	rec := doGuard(r, http.MethodPost, "/v1/chat/completions", strings.NewReader(`xx`), nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, "xx", string(down.body))
}

type countingReader struct {
	r    io.Reader
	read int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += n
	return n, err
}

func multipartBody(preamble string) (string, string) {
	boundary := "XBOUNDARY"
	body := preamble + "--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"model\"\r\n\r\ngpt-image-2\r\n" +
		"--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"image\"; filename=\"a.png\"\r\n\r\n" + strings.Repeat("P", 200000) + "\r\n" +
		"--" + boundary + "--\r\n"
	return body, "multipart/form-data; boundary=" + boundary
}

func TestDuplicateJSONKeyGuard_MultipartIsNotReadUpFront(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body, ct := multipartBody("")
	counter := &countingReader{r: strings.NewReader(body)}
	var readByGuard int
	r := gin.New()
	r.Use(WithDuplicateJSONKeyGuard(func(c *gin.Context) {
		readByGuard = counter.read
		c.Next()
	}, nil))
	var got []byte
	r.POST("/v1/images/edits", func(c *gin.Context) {
		form, err := c.MultipartForm()
		require.NoError(t, err)
		require.Equal(t, []string{"gpt-image-2"}, form.Value["model"])
		got = []byte(form.Value["model"][0])
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", counter)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, "gpt-image-2", string(got))
	require.LessOrEqual(t, readByGuard, 512, "the guard reads only the first chunk of a non-JSON body")
}

func TestDuplicateJSONKeyGuard_MultipartPreambleAndCompleteJSON(t *testing.T) {
	r, down, _ := newGuardRouter(t, nil)
	for _, path := range []string{"/v1/images/edits", "/v1/images/generations", "/v1/videos", "/v1/stt"} {
		// A legal form whose preamble looks like JSON with duplicate keys.
		body, ct := multipartBody(`{"a":1,"a":2}` + "\r\n")
		down.called = false
		rec := doGuard(r, http.MethodPost, path, strings.NewReader(body), map[string]string{"Content-Type": ct})
		require.Equal(t, http.StatusNoContent, rec.Code, path)
		require.Equal(t, body, string(down.body))

		// A complete JSON body labelled multipart is parsed as JSON by some
		// endpoints (Grok media) and stays checked.
		down.called = false
		rec = doGuard(r, http.MethodPost, path, strings.NewReader(dupModel), map[string]string{"Content-Type": ct})
		require.Equal(t, http.StatusBadRequest, rec.Code, path)
		require.False(t, down.called)
	}
	deep := strings.Repeat("[", 10001) + strings.Repeat("]", 10001)
	rec := doGuard(r, http.MethodPost, "/v1/images/edits", strings.NewReader(deep), map[string]string{"Content-Type": "multipart/form-data; boundary=x"})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "nesting is too deep")
}

func TestDuplicateJSONKeyGuard_SkipsGETAndAttachesIdentity(t *testing.T) {
	r, down, _ := newGuardRouter(t, nil)
	rec := doGuard(r, http.MethodGet, "/v1/responses", strings.NewReader(dupModel), nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.True(t, down.called)
	require.Equal(t, dupModel, string(down.body))
}

type failingBody struct{ err error }

func (f failingBody) Read([]byte) (int, error) { return 0, f.err }
func (f failingBody) Close() error             { return nil }

func TestDuplicateJSONKeyGuard_ReadErrorReachesHandler(t *testing.T) {
	r, down, _ := newGuardRouter(t, nil)
	readErr := errors.New("connection reset")
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Body = failingBody{err: readErr}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.True(t, down.called)
	require.ErrorIs(t, down.readErr, readErr)
}

func gzipBytes(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write([]byte(s))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestDuplicateJSONKeyGuard_CompressedBodies(t *testing.T) {
	r, down, _ := newGuardRouter(t, nil)

	rec := doGuard(r, http.MethodPost, "/v1/responses", bytes.NewReader(gzipBytes(t, dupModel)), map[string]string{"Content-Encoding": "gzip"})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.False(t, down.called)

	clean := gzipBytes(t, `{"model":"gpt-5.4","input":"hi"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(clean))
	req.Header.Set("Content-Encoding", "gzip")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, clean, down.body, "downstream receives the original transfer bytes")
	require.Equal(t, "gzip", down.header.Get("Content-Encoding"))
	require.Equal(t, int64(len(clean)), down.contentLength)
	require.Equal(t, "gzip", req.Header.Get("Content-Encoding"), "the original header map is untouched")

	// Content-Encoding that does not match the bytes: endpoints that bind the
	// raw bytes would read the JSON, so the raw bytes are checked.
	down.called = false
	rec = doGuard(r, http.MethodPost, "/v1/chat/completions", strings.NewReader(dupModel), map[string]string{"Content-Encoding": "gzip"})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.False(t, down.called)
}

func TestDuplicateJSONKeyGuard_RouteBodyLimit(t *testing.T) {
	const limit = 64 * 1024
	limits := map[string]int64{"/v1/embeddings": limit}
	r, down, _ := newGuardRouter(t, limits)

	big := `{"model":"m","input":"` + strings.Repeat("a", 4*limit) + `"}`
	counter := &countingReader{r: strings.NewReader(big)}
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", counter)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.True(t, down.called)
	var maxErr *http.MaxBytesError
	require.ErrorAs(t, down.readErr, &maxErr, "the handler sees the route's limit error")
	require.LessOrEqual(t, counter.read, limit+4096, "the guard stops at the route limit")

	// Compressed below the limit, larger once decoded: still accepted, and the
	// route limit keeps counting transfer bytes.
	payload := `{"model":"m","input":"` + strings.Repeat("a", 4*limit) + `"}`
	compressed := gzipBytes(t, payload)
	require.Less(t, len(compressed), limit)
	rec = doGuard(r, http.MethodPost, "/v1/embeddings", bytes.NewReader(compressed), map[string]string{"Content-Encoding": "gzip"})
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.NoError(t, down.readErr)
	require.Equal(t, compressed, down.body)
}

func TestDuplicateJSONKeyGuard_PrereadBodyIsScannedInPlace(t *testing.T) {
	var original []byte
	preread := func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		original = body
		c.Request.Body = httputil.NewPrereadBody(body)
		c.Next()
	}
	r, down, _ := newGuardRouter(t, map[string]int64{"/v1/embeddings": 1 << 20}, preread)

	rec := doGuard(r, http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4"}`), nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, "preread", down.bodyType)
	require.Equal(t, `{"model":"gpt-5.4"}`, string(down.body))

	rec = doGuard(r, http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"m","input":"x"}`), nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, "preread", down.bodyType, "a preread body within the route limit stays unwrapped")
	require.NotNil(t, original)

	rec = doGuard(r, http.MethodPost, "/v1/responses", strings.NewReader(dupModel), nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	rec = doGuard(r, http.MethodPost, "/v1/responses/compact", strings.NewReader(`xx{}`), nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDuplicateJSONKeyGuard_SeedanceContentIsUserContent(t *testing.T) {
	r, _, _ := newGuardRouter(t, nil)
	rec := doGuard(r, http.MethodPost, "/contents/generations/tasks", strings.NewReader(`{"model":"m","content":[{"type":"text","type":"x"}]}`), nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	rec = doGuard(r, http.MethodPost, "/contents/generations/tasks", strings.NewReader(`{"model":"a","content":[],"model":"b"}`), nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDuplicateJSONKeyGuard_LogsEachReason(t *testing.T) {
	r, _, logs := newGuardRouter(t, nil)
	doGuard(r, http.MethodPost, "/v1/chat/completions", strings.NewReader(dupModel), nil)
	doGuard(r, http.MethodPost, "/v1/chat/completions", strings.NewReader(strings.Repeat("[", 10001)), nil)
	doGuard(r, http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"a":1,}`), nil)
	doGuard(r, http.MethodPost, "/v1/responses", strings.NewReader(`xx`), nil)

	entries := logs.FilterMessage("gateway.duplicate_json_key_rejected").All()
	require.Len(t, entries, 4)
	var reasons []string
	for _, e := range entries {
		fields := e.ContextMap()
		reasons = append(reasons, fields["reason"].(string))
		require.Equal(t, int64(148), fields["user_id"])
		require.Equal(t, int64(42), fields["api_key_id"])
		require.Equal(t, int64(7), fields["group_id"])
	}
	require.Equal(t, []string{"duplicate", "too_deep", "invalid_json", "non_json_prefix"}, reasons)
	require.Equal(t, "/model", entries[0].ContextMap()["json_path"])
	_, hasPath := entries[1].ContextMap()["json_path"]
	require.False(t, hasPath)
}

func TestRequestBodyLimit_KeepsPrereadBodyWithinLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	var bodyType string
	var readErr error
	r.POST("/x", func(c *gin.Context) {
		c.Request.Body = httputil.NewPrereadBody([]byte(c.GetHeader("X-Body")))
		c.Next()
	}, RequestBodyLimit(8), func(c *gin.Context) {
		_, ok := c.Request.Body.(*httputil.PrereadBody)
		bodyType = map[bool]string{true: "preread", false: "wrapped"}[ok]
		_, readErr = io.ReadAll(c.Request.Body)
	})
	for _, tc := range []struct {
		body, want string
		limited    bool
	}{{"12345678", "preread", false}, {"123456789", "wrapped", true}} {
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		req.Header.Set("X-Body", tc.body)
		r.ServeHTTP(httptest.NewRecorder(), req)
		require.Equal(t, tc.want, bodyType)
		if tc.limited {
			var maxErr *http.MaxBytesError
			require.ErrorAs(t, readErr, &maxErr)
		}
	}
}

// BenchmarkDuplicateJSONKeyGuardChain compares a handler reading the body the
// way gateway handlers do, with and without the guard, for plain and gzip
// bodies of growing size and a fixed key count.
func BenchmarkDuplicateJSONKeyGuardChain(b *testing.B) {
	gin.SetMode(gin.TestMode)
	newRouter := func(guard bool) *gin.Engine {
		r := gin.New()
		if guard {
			r.Use(WithDuplicateJSONKeyGuard(func(c *gin.Context) { c.Next() }, nil))
		}
		r.POST("/v1/responses", func(c *gin.Context) {
			body, err := httputil.ReadRequestBodyWithPrealloc(c.Request)
			if err != nil || len(body) == 0 {
				b.Fatal("read failed")
			}
			c.Status(http.StatusNoContent)
		})
		return r
	}
	for _, size := range []int{64 << 10, 1 << 20, 4 << 20} {
		payload := []byte(`{"model":"gpt-5.4","reasoning":{"effort":"high"},"input":[{"role":"user","content":"` + strings.Repeat("a", size) + `"}]}`)
		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		_, _ = zw.Write(payload)
		_ = zw.Close()
		for _, enc := range []string{"plain", "gzip"} {
			for _, guard := range []bool{false, true} {
				name := enc + "/" + map[bool]string{false: "no_guard", true: "guard"}[guard] + "/" + map[int]string{64 << 10: "64KB", 1 << 20: "1MB", 4 << 20: "4MB"}[size]
				r := newRouter(guard)
				b.Run(name, func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						var body io.Reader = bytes.NewReader(payload)
						if enc == "gzip" {
							body = bytes.NewReader(gz.Bytes())
						}
						req := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
						if enc == "gzip" {
							req.Header.Set("Content-Encoding", "gzip")
						}
						rec := httptest.NewRecorder()
						r.ServeHTTP(rec, req)
						if rec.Code != http.StatusNoContent {
							b.Fatal(rec.Code)
						}
					}
				})
			}
		}
	}
}
