package middleware

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// IntentRoute classifies chat-style requests of groups that opted into intent
// routing and attaches the resulting decision to the request context, where
// account selection picks it up. It must run after API key authentication.
//
// It is a pure add-on: for groups without a usable router it costs one map
// lookup, and nothing it does can fail a request — the body is always handed on
// exactly as it arrived, and every classification problem just means the
// request is scheduled normally.
func IntentRoute(router *service.IntentRouterService) gin.HandlerFunc {
	return func(c *gin.Context) {
		applyIntentRoute(c, router)
		c.Next()
	}
}

// WithIntentRoute is IntentRoute for routes registered with an explicit handler
// chain instead of a route group: it classifies, then runs the handler.
func WithIntentRoute(router *service.IntentRouterService, handler gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		applyIntentRoute(c, router)
		handler(c)
	}
}

func applyIntentRoute(c *gin.Context, router *service.IntentRouterService) {
	{
		if router == nil || !isIntentRoutableRequest(c.Request) {
			return
		}
		apiKey, ok := GetAPIKeyFromContext(c)
		if !ok || apiKey == nil || apiKey.GroupID == nil {
			return
		}
		ctx := c.Request.Context()
		if !router.Enabled(ctx, *apiKey.GroupID) {
			// Not classifying, but a router that was on earlier may have sent
			// this conversation to an account outside the group.
			if scope := router.ScopeOnly(ctx, *apiKey.GroupID); scope != nil {
				c.Request = c.Request.WithContext(service.WithIntentRouteDecision(ctx, scope))
			}
			return
		}
		if c.Request.ContentLength > router.MaxBodyBytes() || c.Request.Body == nil {
			return
		}

		original := c.Request.Body
		body, err := io.ReadAll(io.LimitReader(original, router.MaxBodyBytes()+1))
		// Whatever was read goes back in front of whatever is left, so the
		// handler sees the untouched stream — including its own read errors
		// (such as the body size limit) at the point they would have occurred.
		c.Request.Body = &replayReadCloser{Reader: io.MultiReader(bytes.NewReader(body), original), closer: original}
		if err != nil || int64(len(body)) > router.MaxBodyBytes() {
			return
		}

		decision := router.Decide(ctx, service.IntentRouteInput{
			GroupID:  *apiKey.GroupID,
			APIKeyID: apiKey.ID,
			Header:   c.Request.Header,
			Body:     body,
		})
		if decision != nil {
			c.Request = c.Request.WithContext(service.WithIntentRouteDecision(ctx, decision))
		}
	}
}

type replayReadCloser struct {
	io.Reader
	closer io.Closer
}

func (r *replayReadCloser) Close() error { return r.closer.Close() }

// isIntentRoutableRequest limits classification to endpoints that carry a
// conversation. Token counting, images, audio, embeddings, model listings and
// the classifier's own calls are left alone.
func isIntentRoutableRequest(r *http.Request) bool {
	if r == nil || r.Method != http.MethodPost || r.Header.Get(service.IntentRouteInternalHeader) != "" {
		return false
	}
	if !strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "json") {
		return false
	}
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case strings.HasSuffix(path, "/messages"),
		strings.HasSuffix(path, "/chat/completions"),
		strings.HasSuffix(path, "/responses"):
		return true
	case strings.HasSuffix(path, ":generateContent"), strings.HasSuffix(path, ":streamGenerateContent"):
		return true
	}
	return false
}
