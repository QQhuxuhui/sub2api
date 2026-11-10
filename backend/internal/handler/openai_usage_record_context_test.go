package handler

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageRecordContext_CarriesOnlyCacheSmoothDecisions(t *testing.T) {
	smoothing := service.NewIntentCacheSmoothDecisionForTest(4)
	parent := service.WithIntentRouteDecision(context.Background(), smoothing)
	got := usageRecordContext(parent, context.Background())
	require.Same(t, smoothing, service.IntentRouteDecisionFromContext(got))
	require.NotNil(t, service.IntentCacheSmoothPlanFromContext(got))

	// A decision without a plan stays out of the record context, as before.
	plain := &service.IntentRouteDecision{GroupID: 4, AccountIDs: []int64{1}}
	got = usageRecordContext(service.WithIntentRouteDecision(context.Background(), plain), context.Background())
	require.Nil(t, service.IntentRouteDecisionFromContext(got))
}
