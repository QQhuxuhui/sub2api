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

// A request that matched an intent rule must be answered in the router's
// configured status when the rule's accounts cannot take it, never be handed
// to the group's own accounts, and never mention intent routing.
func TestOpenAIGatewayHandlerResponses_IntentRouteUnavailableUsesConfiguredStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)

	targets := map[string]struct {
		targets []int64
		calls   []int64
	}{
		"target cannot be selected":      {targets: []int64{777}, calls: nil},
		"target failed and was excluded": {targets: []int64{2}, calls: []int64{2}},
	}
	statuses := map[string]struct {
		configured int
		status     int
		errType    string
		message    string
	}{
		"default":   {configured: 0, status: http.StatusServiceUnavailable, errType: "api_error", message: "Service temporarily unavailable. Please retry later."},
		"503":       {configured: 503, status: http.StatusServiceUnavailable, errType: "api_error", message: "Service temporarily unavailable. Please retry later."},
		"502":       {configured: 502, status: http.StatusBadGateway, errType: "upstream_error", message: "Upstream request failed"},
		"429 (old)": {configured: 429, status: http.StatusTooManyRequests, errType: "rate_limit_error", message: intentRouteUnavailableMessage},
	}
	for name, tc := range targets {
		for statusName, want := range statuses {
			t.Run(name+"/"+statusName, func(t *testing.T) {
				upstream := &openAIResponsesFailoverCancelUpstream{}
				handler := newOpenAIResponsesFailoverTestHandler(t, upstream)
				ctx := service.WithIntentRouteDecision(context.Background(), &service.IntentRouteDecision{Intent: "probe", AccountIDs: tc.targets, UnavailableStatus: want.configured})
				c, rec := newOpenAIResponsesFailoverTestContext(t, ctx)

				handler.Responses(c)

				require.Equal(t, tc.calls, upstream.calls(), "the group's own accounts must not take over")
				require.Equal(t, want.status, rec.Code)
				require.Equal(t, want.errType, gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
				msg := gjson.GetBytes(rec.Body.Bytes(), "error.message").String()
				require.Equal(t, want.message, msg)
				require.NotContains(t, msg, "intent")
				require.NotContains(t, msg, "routed")
			})
		}
	}
}
