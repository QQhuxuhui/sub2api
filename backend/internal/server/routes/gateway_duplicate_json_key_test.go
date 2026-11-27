package routes

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Every gateway route that takes a body must reject a duplicate model key
// before its handler runs: the gateway bills the first value while upstreams
// use the last one. The handlers in this router have no dependencies, so a
// request that reached one would panic instead of answering 400.
func TestGatewayRoutesRejectDuplicateJSONKeysOnEveryBodyRoute(t *testing.T) {
	router := newGatewayRoutesTestRouterWithGroup(&service.Group{Platform: service.PlatformOpenAI})

	var checked []string
	for _, r := range router.Routes() {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
		default:
			continue
		}
		// The Google API key groups authenticate through services this router
		// does not wire; their mounting is asserted from source below.
		if strings.HasPrefix(r.Path, "/v1beta") || strings.HasPrefix(r.Path, "/antigravity/v1beta") {
			continue
		}
		path := concreteRoutePath(r.Path)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(r.Method, path, strings.NewReader(`{"model":"gpt-4o-mini","model":"gpt-5.5-pro"}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		require.Equalf(t, http.StatusBadRequest, rec.Code, "%s %s: %s", r.Method, r.Path, rec.Body.String())
		requireDuplicateFieldRejected(t, rec.Body.String(), "%s %s", r.Method, r.Path)
		checked = append(checked, r.Method+" "+r.Path)
	}
	require.NotEmpty(t, checked)
	for _, want := range []string{
		"POST /v1/responses", "POST /responses", "POST /v1/chat/completions", "POST /v1/messages",
		"POST /v1/embeddings", "POST /embeddings", "POST /backend-api/codex/responses",
		"POST /antigravity/v1/messages", "POST /contents/generations/tasks", "POST /v1/images/generations",
	} {
		require.Contains(t, checked, want)
	}
}

func TestGatewayRoutesDuplicateJSONKeyGuardMountedOnEveryChain(t *testing.T) {
	routeSource, err := os.ReadFile("gateway.go")
	require.NoError(t, err)
	source := string(routeSource)

	require.Regexp(t, regexp.MustCompile(regexp.QuoteMeta(`requestTimeoutFinalizer := middleware.WithDuplicateJSONKeyGuard(middleware.RequestTimeoutFinalizer(),`)), source)

	for _, group := range []string{"gateway", "gemini", "antigravityV1", "antigravityV1Beta"} {
		require.Containsf(t, source, group+".Use(requestTimeoutFinalizer)", "%s must mount the guarded finalizer", group)
	}
	require.Regexp(t, regexp.MustCompile(`codexDirect\.Use\([^\n]*\brequestTimeoutFinalizer\)`), source)
	require.Regexp(t, regexp.MustCompile(`r\.Handle\(method, path, [^\n]*\brequestTimeoutFinalizer, handler\)`), source)

	// Text-only routes get the text body limit at the group level (textBodyRoutes,
	// upstream v0.2.25); the guard pre-reads with the same per-route limit.
	limitTable := regexp.MustCompile(`(?s)WithDuplicateJSONKeyGuard\(middleware\.RequestTimeoutFinalizer\(\), map\[string\]int64\{(.*?)\n\t\}\)`).FindStringSubmatch(source)
	require.Len(t, limitTable, 2)
	textTable := regexp.MustCompile(`(?s)textBodyRoutes := map\[string\]bool\{(.*?)\n\t\}`).FindStringSubmatch(source)
	require.Len(t, textTable, 2)
	textRoutes := regexp.MustCompile(`"([^"]+)":\s*true`).FindAllStringSubmatch(textTable[1], -1)
	require.NotEmpty(t, textRoutes)
	for _, m := range textRoutes {
		require.Containsf(t, limitTable[1], `"`+m[1]+`"`, "%s must be in the guard's route limit table", m[1])
	}
}

// 合并上游 v0.2.25 后，同一类请求有两道检查：上游 RejectConflictingRequestFields
// 排在链上更前（GHSA-p3r4-jcm3-2hph），本仓重复键守卫在 Finalizer 里兜底（另拦嵌套过深、
// 键数过多）。重复 model 由先运行的上游检查拒绝，两种文案都表示请求被拦在 handler 之前。
func requireDuplicateFieldRejected(t *testing.T, body string, msgAndArgs ...any) {
	t.Helper()
	if strings.Contains(body, "duplicate JSON key: /model") || strings.Contains(body, `declares field \"model\" more than once`) {
		return
	}
	require.Failf(t, "duplicate model key was not rejected by either guard", "body=%s %v", body, msgAndArgs)
}

// A model the group's allowlist forbids is refused by the allowlist (404),
// which runs first; a duplicate of allowed models reaches the guard (400).
// Neither reaches a handler.
func TestGatewayRoutesDuplicateJSONKeyWithModelAllowlist(t *testing.T) {
	router := newGatewayRoutesTestRouterWithGroup(allowlistGroup(service.PlatformOpenAI, true, "gpt-5.4", "gpt-4.1"))

	send := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		return rec
	}
	rec := send(`{"model":"gpt-5.4","model":"gpt-9-blocked"}`)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	rec = send(`{"model":"gpt-5.4","model":"gpt-4.1"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	requireDuplicateFieldRejected(t, rec.Body.String(), "allowlisted duplicate")

	rec = send(`xx{"model":"gpt-5.4"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Failed to parse request body")
}
