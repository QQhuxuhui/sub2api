//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// The handler installs the request group's gate; scheduling in another group
// without profit control clears it. The handler's replay must clear it too,
// or its post-acquire check would veto what the scheduled group allowed.
func TestSelectionProfitGate_ClearedGateIsReplayed(t *testing.T) {
	entry := &openAIProfitControlGate{groupID: 10, threshold: 0.5}
	handlerCtx := context.WithValue(context.Background(), openAIProfitControlGateCtxKey{}, entry)
	schedCtx := context.WithValue(handlerCtx, openAIProfitControlGateCtxKey{}, (*openAIProfitControlGate)(nil))

	sel := attachSelectionProfitGate(schedCtx, &AccountSelectionResult{})
	require.False(t, sel.ProfitGateActive())
	require.True(t, sel.profitGateCleared)

	replayed := ContextWithSelectionProfitGate(handlerCtx, sel)
	gate, ok := replayed.Value(openAIProfitControlGateCtxKey{}).(*openAIProfitControlGate)
	require.True(t, ok)
	require.Nil(t, gate, "the entry gate is overridden by a typed nil")

	// Without a clearing the handler context is left as it is.
	plain := attachSelectionProfitGate(handlerCtx, &AccountSelectionResult{})
	require.Same(t, entry, plain.profitGate)
	require.Equal(t, handlerCtx, ContextWithSelectionProfitGate(handlerCtx, &AccountSelectionResult{}))
}
