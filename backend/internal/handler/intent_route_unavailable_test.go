//go:build unit

package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// A request that matched an intent rule must be answered with an ordinary
// rate limit when the rule's accounts cannot take it, never be handed to the
// group's own accounts, and never mention intent routing.
func TestOpenAIGatewayHandlerResponses_IntentRouteUnavailableReturns429(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := map[string]struct {
		targets []int64
		calls   []int64
	}{
		"target cannot be selected":      {targets: []int64{777}, calls: nil},
		"target failed and was excluded": {targets: []int64{2}, calls: []int64{2}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			upstream := &openAIResponsesFailoverCancelUpstream{}
			handler := newOpenAIResponsesFailoverTestHandler(t, upstream)
			ctx := service.WithIntentRouteDecision(context.Background(), &service.IntentRouteDecision{Intent: "probe", AccountIDs: tc.targets})
			c, rec := newOpenAIResponsesFailoverTestContext(t, ctx)

			handler.Responses(c)

			require.Equal(t, tc.calls, upstream.calls(), "the group's own accounts must not take over")
			require.Equal(t, http.StatusTooManyRequests, rec.Code)
			require.Equal(t, "rate_limit_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
			msg := gjson.GetBytes(rec.Body.Bytes(), "error.message").String()
			require.Equal(t, intentRouteUnavailableMessage, msg)
			require.NotContains(t, msg, "intent")
			require.NotContains(t, msg, "routed")
		})
	}
}
