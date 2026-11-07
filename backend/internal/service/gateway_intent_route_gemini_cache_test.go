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

	selection, err := (&GatewayService{}).selectIntentRoutedAccount(ctx, nil, nil, "", "gemini-3-pro", nil, "", 0)
	require.NoError(t, err)
	require.Nil(t, selection, "显式缓存绑定优先，走普通绑定调度")
}

func TestSelectIntentRoutedAccount_SkipsDecisionForAnotherGroup(t *testing.T) {
	ctx := WithIntentRouteDecision(context.Background(), &IntentRouteDecision{GroupID: 1, AccountIDs: []int64{7}})
	fallback := int64(2)

	selection, err := (&GatewayService{}).selectIntentRoutedAccount(ctx, &fallback, nil, "", "claude-sonnet-5-5", nil, "", 0)
	require.NoError(t, err)
	require.Nil(t, selection, "降级到别的分组后不再使用原分组的意图规则")
}
