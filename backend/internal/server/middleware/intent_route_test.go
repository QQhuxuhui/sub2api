//go:build unit

package middleware

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	_ "modernc.org/sqlite"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/domain"
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

func TestIntentRoute_OversizedRequestKeepsScope(t *testing.T) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:intent_middleware_%d?mode=memory&cache=shared", time.Now().UnixNano()))
	require.NoError(t, err)
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	group, err := client.Group.Create().SetName("intent-group").SetPlatform("openai").Save(ctx)
	require.NoError(t, err)
	_, err = client.IntentRouter.Create().SetGroupID(group.ID).SetEnabled(true).SetClassifierAPIKey("test-key").SetClassifierModel("test-model").SetRules([]domain.IntentRule{{Name: "coding", Enabled: true, AccountIDs: []int64{42}}}).Save(ctx)
	require.NoError(t, err)
	router := service.NewIntentRouterService(client, nil, nil)
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprintf("chunked=%v", chunked), func(t *testing.T) {
			body := `{"previous_response_id":"resp_old","input":"` + strings.Repeat("x", int(router.MaxBodyBytes())) + `"}`
			engine := gin.New()
			engine.POST("/responses", func(c *gin.Context) {
				c.Set(string(ContextKeyAPIKey), &service.APIKey{ID: 1, GroupID: &group.ID})
				c.Next()
			}, IntentRoute(router), func(c *gin.Context) {
				got, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				require.Equal(t, body, string(got))
				d := service.IntentRouteDecisionFromContext(c.Request.Context())
				require.NotNil(t, d, "classification bypass must retain response ownership scope")
				require.Empty(t, d.AccountIDs)
				require.Equal(t, []int64{42}, d.ScopeAccountIDs)
			})
			req := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if chunked {
				req.ContentLength = -1
			}
			engine.ServeHTTP(httptest.NewRecorder(), req)
		})
	}
}

type stubProbeAvailability struct{ ok bool }

func (s stubProbeAvailability) HasServableAccountNow(context.Context, int64, string, string) bool {
	return s.ok
}

// A group that answers channel-monitor challenges replies itself — the handler
// never runs — after a short delay, without an intent router, and only for the
// challenge, in scope, when the group could serve the model.
func TestIntentRoute_AnswersMonitorChallenge(t *testing.T) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:intent_monitor_%d?mode=memory&cache=shared", time.Now().UnixNano()))
	require.NoError(t, err)
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	group, err := client.Group.Create().SetName("monitor-group").SetPlatform("anthropic").Save(ctx)
	require.NoError(t, err)
	router := service.NewIntentRouterService(client, nil, nil)
	_, err = router.SaveMonitorProbeAnswer(ctx, group.ID, domain.MonitorProbeAnswerConfig{Enabled: true})
	require.NoError(t, err)

	challenge := `{"model":"gpt-6.1-sol","instructions":"You are a channel health-check endpoint. Answer the arithmetic challenge exactly and briefly.","input":"Calculate and respond with ONLY the number, nothing else.\n\nQ: 3 + 5 = ?\nA: 8\n\nQ: 12 - 7 = ?\nA: 5\n\nQ: 40 - 15 = ?\nA:","max_output_tokens":50,"stream":false}`
	serve := func(body string) (*httptest.ResponseRecorder, bool, time.Duration) {
		handled := false
		engine := gin.New()
		engine.POST("/v1/responses", func(c *gin.Context) {
			c.Set(string(ContextKeyAPIKey), &service.APIKey{ID: 1, UserID: 3, GroupID: &group.ID, Group: &service.Group{ID: group.ID, Platform: "anthropic"}})
			c.Next()
		}, IntentRoute(router), func(c *gin.Context) {
			handled = true
			got, err := io.ReadAll(c.Request.Body)
			require.NoError(t, err)
			require.Equal(t, body, string(got), "the handler sees the body untouched")
			c.Status(http.StatusTeapot)
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		started := time.Now()
		engine.ServeHTTP(rec, req)
		return rec, handled, time.Since(started)
	}

	router.SetMonitorProbeAvailability(stubProbeAvailability{ok: true})
	rec, handled, took := serve(challenge)
	require.False(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "25", gjson.Get(rec.Body.String(), "output.0.content.0.text").String())
	require.GreaterOrEqual(t, took, 300*time.Millisecond, "answered after a model-like delay")

	_, handled, _ = serve(`{"model":"gpt-6.1-sol","input":"hi","stream":false}`)
	require.True(t, handled, "anything else goes to the handler")

	router.SetMonitorProbeAvailability(stubProbeAvailability{ok: false})
	_, handled, _ = serve(challenge)
	require.True(t, handled, "a group that cannot serve the model fails the normal way")

	router.SetMonitorProbeAvailability(stubProbeAvailability{ok: true})
	_, err = router.SaveMonitorProbeAnswer(ctx, group.ID, domain.MonitorProbeAnswerConfig{Enabled: false})
	require.NoError(t, err)
	_, handled, _ = serve(challenge)
	require.True(t, handled, "switched off")
}
