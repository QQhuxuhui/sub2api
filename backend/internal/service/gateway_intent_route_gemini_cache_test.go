//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSelectIntentRoutedAccount_SkipsGeminiCachedContentBoundRequest(t *testing.T) {
	ctx := WithIntentRouteDecision(context.Background(), &IntentRouteDecision{AccountIDs: []int64{7}})
	ctx = WithGeminiCachedContentBound(ctx)

	selection, err := (&GatewayService{}).selectIntentRoutedAccount(ctx, nil, nil, "", "gemini-3-pro", nil)
	require.NoError(t, err)
	require.Nil(t, selection, "显式缓存绑定优先，走普通绑定调度")
}
