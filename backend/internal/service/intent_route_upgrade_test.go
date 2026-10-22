//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIntentRouter_LegacyKeywordSessionIsReclassified(t *testing.T) {
	f := newIntentRouteFixture(t)
	f.saveRouter(t, func(in *IntentRouterInput) {
		in.Rules[1].Description = ""
		in.Rules[1].Keywords = []string{"笑话"}
	})
	ctx := context.Background()
	req := f.request(userTurns("讲个笑话", "please fix my Go code"))
	cfg := f.svc.config(ctx, f.groupID)
	require.Len(t, cfg.classifierRules(), 1)
	_, accepted := cfg.rule("chat")
	require.True(t, accepted)
	key := intentSessionKey(req.APIKeyID, req.Header, intentRequestView{body: req.Body})
	// Serialized schema emitted by the pre-upgrade keyword override path.
	var legacy intentRouteSession
	require.NoError(t, json.Unmarshal([]byte(`{"i":"chat","a":1,"v":10,"q":2}`), &legacy))
	legacy.AccountID = f.accountID(t, "pool-a")
	f.store.sessions[fmt.Sprintf("intent_route:s:%d:%s", f.groupID, key)] = legacy
	f.classifier.answers = []string{"coding"}
	for i := 0; i < 3; i++ {
		decision := f.svc.Decide(ctx, req)
		require.Equal(t, "coding", decision.Intent)
		require.NotEqual(t, legacy.AccountID, decision.PinnedAccountID)
	}
	require.Equal(t, 1, f.classifier.calls)
	require.Equal(t, 2, f.store.touches)
	require.Contains(t, f.store.sessions, fmt.Sprintf("intent_route:s:%d:%s", f.groupID, key), "old keys are left to expire")
}
