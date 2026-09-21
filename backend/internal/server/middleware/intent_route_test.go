//go:build unit

package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestIsIntentRoutableRequest(t *testing.T) {
	newReq := func(method, path, contentType string) *http.Request {
		r := httptest.NewRequest(method, path, strings.NewReader("{}"))
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		return r
	}
	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses", "/v1/responses/",
		"/v1beta/models/gemini-2.5-pro:generateContent", "/v1beta/models/gemini-2.5-pro:streamGenerateContent"} {
		require.True(t, isIntentRoutableRequest(newReq(http.MethodPost, path, "application/json; charset=utf-8")), path)
	}
	for _, path := range []string{"/v1/messages/count_tokens", "/v1/images/generations", "/v1/embeddings", "/v1/models",
		"/v1beta/models/gemini-2.5-pro:countTokens", "/v1/audio/speech"} {
		require.False(t, isIntentRoutableRequest(newReq(http.MethodPost, path, "application/json")), path)
	}
	require.False(t, isIntentRoutableRequest(newReq(http.MethodGet, "/v1/messages", "application/json")))
	require.False(t, isIntentRoutableRequest(newReq(http.MethodPost, "/v1/chat/completions", "multipart/form-data; boundary=x")))

	own := newReq(http.MethodPost, "/v1/chat/completions", "application/json")
	own.Header.Set(service.IntentRouteInternalHeader, "1")
	require.False(t, isIntentRoutableRequest(own), "the classifier's own call is never classified")
}

// Without a router service (or for groups that do not use the feature) the
// middleware must be a transparent pass-through.
func TestIntentRoute_PassesThroughUntouched(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"model":"claude","messages":[{"role":"user","content":"hello"}]}`

	for name, install := range map[string]func(*gin.Context){
		"no service":    func(*gin.Context) {},
		"no api key":    func(*gin.Context) {},
		"key w/o group": func(c *gin.Context) { c.Set(string(ContextKeyAPIKey), &service.APIKey{ID: 1}) },
	} {
		t.Run(name, func(t *testing.T) {
			var router *service.IntentRouterService
			if name != "no service" {
				router = &service.IntentRouterService{}
			}
			w := httptest.NewRecorder()
			c, engine := gin.CreateTestContext(w)
			var seen []byte
			engine.POST("/v1/messages", func(c *gin.Context) { install(c); c.Next() }, IntentRoute(router), func(c *gin.Context) {
				seen, _ = io.ReadAll(c.Request.Body)
				require.Nil(t, service.IntentRouteDecisionFromContext(c.Request.Context()))
				c.Status(http.StatusNoContent)
			})
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			engine.HandleContext(c)
			require.Equal(t, http.StatusNoContent, w.Code)
			require.Equal(t, body, string(seen))
		})
	}
}

// The handler must read exactly the bytes the client sent, and still hit the
// original reader's error (e.g. the body size limit) where it would have.
func TestReplayReadCloser(t *testing.T) {
	failing := &erroringReader{data: []byte("-tail"), err: io.ErrUnexpectedEOF}
	head := []byte("head")
	rc := &replayReadCloser{Reader: io.MultiReader(bytes.NewReader(head), failing), closer: failing}

	got, err := io.ReadAll(rc)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Equal(t, "head-tail", string(got))
	require.NoError(t, rc.Close())
	require.True(t, failing.closed, "closing reaches the original body")
}

type erroringReader struct {
	data   []byte
	err    error
	closed bool
}

func (r *erroringReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func (r *erroringReader) Close() error { r.closed = true; return nil }

// Root aliases wrap their handler instead of joining a route group; the wrapper
// must hand the request on untouched and run the handler exactly once.
func TestWithIntentRoute_RunsTheHandlerOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"model":"gpt","input":"hello"}`
	calls := 0
	var seen []byte
	handler := WithIntentRoute(nil, func(c *gin.Context) {
		calls++
		seen, _ = io.ReadAll(c.Request.Body)
		c.Status(http.StatusNoContent)
	})

	w := httptest.NewRecorder()
	c, engine := gin.CreateTestContext(w)
	engine.POST("/responses", handler)
	c.Request = httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	engine.HandleContext(c)

	require.Equal(t, 1, calls)
	require.Equal(t, http.StatusNoContent, w.Code)
	require.Equal(t, body, string(seen))
}
