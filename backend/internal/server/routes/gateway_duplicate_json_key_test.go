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
		require.Containsf(t, rec.Body.String(), "duplicate JSON key: /model", "%s %s", r.Method, r.Path)
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

	// Route-level text limits run after the guard, so the guard must know them.
	limitTable := regexp.MustCompile(`(?s)WithDuplicateJSONKeyGuard\(middleware\.RequestTimeoutFinalizer\(\), map\[string\]int64\{(.*?)\n\t\}\)`).FindStringSubmatch(source)
	require.Len(t, limitTable, 2)
	textRoutes := regexp.MustCompile(`(gateway|codexDirect)\.(POST|PUT|PATCH)\("([^"]+)", textBodyLimit`).FindAllStringSubmatch(source, -1)
	require.NotEmpty(t, textRoutes)
	for _, m := range textRoutes {
		prefix := "/v1"
		if m[1] == "codexDirect" {
			prefix = "/backend-api/codex"
		}
		require.Containsf(t, limitTable[1], `"`+prefix+m[3]+`"`, "%s%s must be in the guard's route limit table", prefix, m[3])
	}
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
	require.Contains(t, rec.Body.String(), "duplicate JSON key: /model")

	rec = send(`xx{"model":"gpt-5.4"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Failed to parse request body")
}
