//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type anthropicFailoverSettingRepo struct{ data map[string]string }

func (r *anthropicFailoverSettingRepo) Get(context.Context, string) (*service.Setting, error) {
	return nil, service.ErrSettingNotFound
}
func (r *anthropicFailoverSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := r.data[key]; ok {
		return v, nil
	}
	return "", service.ErrSettingNotFound
}
func (r *anthropicFailoverSettingRepo) Set(_ context.Context, key, value string) error {
	r.data[key] = value
	return nil
}
func (r *anthropicFailoverSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *anthropicFailoverSettingRepo) SetMultiple(context.Context, map[string]string) error {
	return nil
}
func (r *anthropicFailoverSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *anthropicFailoverSettingRepo) Delete(context.Context, string) error { return nil }

func TestAnthropicFailoverHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &anthropicFailoverSettingRepo{data: map[string]string{}}
	policy := service.NewAnthropicFailoverPolicyService(repo, nil)
	h := NewAnthropicFailoverHandler(policy)
	router := gin.New()
	router.GET("/admin/anthropic-failover", h.Overview)
	router.PUT("/admin/anthropic-failover", h.UpdateConfig)

	do := func(method, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/admin/anthropic-failover", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		return rec
	}

	rec := do(http.MethodGet, "")
	require.Equal(t, http.StatusOK, rec.Code)
	var overview struct {
		Data service.AnthropicFailoverOverview `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &overview))
	require.Empty(t, overview.Data.Config.EnabledGroupIDs)
	require.Empty(t, overview.Data.Config.Rules)
	require.Equal(t, 3, overview.Data.Config.MaxRuleSwitches)
	require.Contains(t, rec.Body.String(), `"rules":[]`, "empty lists are arrays, not null")

	require.Equal(t, http.StatusBadRequest, do(http.MethodPut, `{`).Code)
	rec = do(http.MethodPut, `{"enabled_group_ids":[18],"max_rule_switches":3,"rules":[{"id":"x","enabled":true,"status_code":429,"keywords":["k"],"action":"failover"}]}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "ANTHROPIC_FAILOVER_CONFIG_INVALID")
	gid := int64(18)
	require.Nil(t, policy.PolicyForGroup(context.Background(), &gid))

	rec = do(http.MethodPut, `{"enabled_group_ids":[18],"max_rule_switches":2,"rules":[{"id":"no-channel","name":"n","enabled":true,"status_code":422,"keywords":["暂无可用渠道"],"action":"failover"}]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, policy.PolicyForGroup(context.Background(), &gid), "a save applies at once")
	require.Contains(t, do(http.MethodGet, "").Body.String(), "no-channel")
}
