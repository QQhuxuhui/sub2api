//go:build unit

package middleware

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var iqShowcaseTestProtocols = []string{
	service.IQShowcaseProtocolOpenAIResponses,
	service.IQShowcaseProtocolOpenAIChat,
	service.IQShowcaseProtocolAnthropicMessages,
	service.IQShowcaseProtocolGemini,
}

func iqShowcaseTestRequest(t *testing.T, protocol, prompt string) *http.Request {
	t.Helper()
	req, err := service.BuildIQShowcaseRequest(context.Background(), "http://127.0.0.1:8080", "sk-test",
		&service.IQShowcaseItem{Protocol: protocol, Model: "test-model", Prompt: prompt})
	require.NoError(t, err)
	return req
}

// Every showcase request must look routable to the intent middleware;
// otherwise the probe would silently skip intent routing.
func TestIQShowcaseRequest_IsIntentRoutable(t *testing.T) {
	for _, protocol := range iqShowcaseTestProtocols {
		require.True(t, isIntentRoutableRequest(iqShowcaseTestRequest(t, protocol, "draw")), protocol)
	}
}

// The showcase request goes through the real intent middleware and service:
// a keyword rule on the probe prompt routes it to the rule's accounts.
func TestIQShowcaseRequest_IsClassifiedByIntentRouting(t *testing.T) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:iq_showcase_intent_%d?mode=memory&cache=shared", time.Now().UnixNano()))
	require.NoError(t, err)
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	group, err := client.Group.Create().SetName("showcase-group").SetPlatform("openai").Save(ctx)
	require.NoError(t, err)
	routerRow, err := client.IntentRouter.Create().SetGroupID(group.ID).SetEnabled(true).
		SetClassifierAPIKey("test-key").SetClassifierModel("test-model").
		SetRules([]domain.IntentRule{
			{Name: "pelican", Keywords: []string{"鹈鹕"}, Enabled: true, AccountIDs: []int64{42}},
			{Name: "other", Keywords: []string{"zzz-never"}, Enabled: true, AccountIDs: []int64{7}},
		}).Save(ctx)
	require.NoError(t, err)
	router := service.NewIntentRouterService(client, nil, nil)

	serve := func(req *http.Request) *service.IntentRouteDecision {
		var got *service.IntentRouteDecision
		engine := gin.New()
		engine.POST("/*path", func(c *gin.Context) {
			c.Set(string(ContextKeyAPIKey), &service.APIKey{ID: 1, GroupID: &group.ID})
			c.Next()
		}, IntentRoute(router), func(c *gin.Context) {
			_, _ = io.ReadAll(c.Request.Body)
			got = service.IntentRouteDecisionFromContext(c.Request.Context())
		})
		engine.ServeHTTP(httptest.NewRecorder(), req)
		return got
	}

	for _, protocol := range iqShowcaseTestProtocols {
		t.Run(protocol, func(t *testing.T) {
			d := serve(iqShowcaseTestRequest(t, protocol, "用 SVG 画一只骑自行车的鹈鹕"))
			require.NotNil(t, d)
			require.Equal(t, "pelican", d.Intent, "the keyword rule matched the probe prompt")
			require.Equal(t, []int64{42}, d.AccountIDs)
		})
	}

	// Control: with the router switched off only the scope is kept and no rule
	// is chosen, so a non-empty decision alone proves nothing.
	_, err = client.IntentRouter.UpdateOneID(routerRow.ID).SetEnabled(false).Save(ctx)
	require.NoError(t, err)
	router = service.NewIntentRouterService(client, nil, nil)
	d := serve(iqShowcaseTestRequest(t, service.IQShowcaseProtocolOpenAIResponses, "用 SVG 画一只骑自行车的鹈鹕"))
	if d != nil {
		require.Empty(t, d.AccountIDs)
		require.Empty(t, d.Intent)
	}
}
