//go:build unit

package routes

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Equivalent entry points must not differ in whether intent routing applies:
// the same Responses request sent to /v1/responses, /responses or
// /backend-api/codex/responses has to be treated the same. Each of these
// chains is assembled by hand in gateway.go, so the wiring is checked here.
func TestEveryConversationEntryPointRunsIntentRouting(t *testing.T) {
	raw, err := os.ReadFile("gateway.go")
	require.NoError(t, err)
	src := string(raw)

	for _, group := range []string{"gateway", "gemini", "antigravityV1", "antigravityV1Beta"} {
		require.Regexp(t, regexp.MustCompile(`(?m)^\t`+group+`\.Use\(intentRoute\)$`), src, "route group %s", group)
	}
	require.Regexp(t, regexp.MustCompile(`(?m)^\tcodexDirect\.Use\(intentRoute\)$`), src, "codex direct (/backend-api/codex)")

	// Root aliases are registered through the rootRoute helper, whose chain is
	// asserted verbatim elsewhere; their handlers are wrapped instead, which
	// still runs classification after authentication.
	for _, alias := range []string{"/responses", "/chat/completions"} {
		require.Contains(t, src, `rootRoute(http.MethodPost, "`+alias+`", bodyLimit, middleware.WithIntentRoute(intentRouterService, `, "root alias POST %s", alias)
	}
	require.Equal(t, 2, strings.Count(src, "middleware.WithIntentRoute("), "a new root conversation alias must be added to this test")
}
