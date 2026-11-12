//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func openAIRuleFixture() OpenAIFailoverConfig {
	return OpenAIFailoverConfig{
		EnabledGroupIDs: []int64{45},
		MaxRuleSwitches: 3,
		Rules: []OpenAIFailoverRule{
			{ID: "reasoning-passback", Enabled: true, StatusCode: 400, Keywords: []string{"must be passed back to the API"}, Action: OpenAIFailoverActionFailover},
		},
	}
}

func TestNormalizeOpenAIFailoverConfig(t *testing.T) {
	t.Run("status-only rule is accepted", func(t *testing.T) {
		cfg, err := NormalizeOpenAIFailoverConfig(OpenAIFailoverConfig{MaxRuleSwitches: 3, Rules: []OpenAIFailoverRule{
			{ID: "http-408", Enabled: true, StatusCode: 408, Action: OpenAIFailoverActionFailover},
		}})
		require.NoError(t, err)
		require.Empty(t, cfg.Rules[0].Keywords)
	})
	t.Run("rule needs status or keyword", func(t *testing.T) {
		_, err := NormalizeOpenAIFailoverConfig(OpenAIFailoverConfig{MaxRuleSwitches: 3, Rules: []OpenAIFailoverRule{
			{ID: "empty", Enabled: true, Keywords: []string{"  "}, Action: OpenAIFailoverActionFailover},
		}})
		require.Error(t, err)
	})
	t.Run("statuses that already switch are rejected on edit", func(t *testing.T) {
		for _, status := range []int{401, 403, 429} {
			_, err := NormalizeOpenAIFailoverConfig(OpenAIFailoverConfig{MaxRuleSwitches: 3, Rules: []OpenAIFailoverRule{
				{ID: "x", Enabled: true, StatusCode: status, Keywords: []string{"k"}, Action: OpenAIFailoverActionFailover},
			}})
			require.Error(t, err, status)
		}
	})
	t.Run("bounds", func(t *testing.T) {
		_, err := NormalizeOpenAIFailoverConfig(OpenAIFailoverConfig{MaxRuleSwitches: 0})
		require.Error(t, err)
		_, err = NormalizeOpenAIFailoverConfig(OpenAIFailoverConfig{MaxRuleSwitches: 11})
		require.Error(t, err)
		_, err = NormalizeOpenAIFailoverConfig(OpenAIFailoverConfig{MaxRuleSwitches: 3, Rules: []OpenAIFailoverRule{
			{ID: "x", Enabled: true, StatusCode: 500, Keywords: []string{"k"}, Action: OpenAIFailoverActionFailover},
		}})
		require.Error(t, err)
	})
}

// A table saved by the gptniubi branch: slot-wait fields, a status-only rule
// and a disabled rule on a status that already switches accounts.
const gptniubiOpenAIFailoverJSON = `{"enabled_group_ids":[4],"slot_wait_ms":3000,"max_rule_switches":3,"max_slot_switches":5,
"rules":[{"id":"http-408","name":"timeout","enabled":true,"status_code":408,"keywords":[],"action":"failover","note":"kept"},
{"id":"old-429","name":"","enabled":false,"status_code":429,"keywords":["quota"],"action":"failover"},
{"id":"overloaded","name":"","enabled":true,"status_code":400,"keywords":["overloaded"],"action":"failover"}]}`

func TestOpenAIFailoverConfigCompatibleWithGptniubi(t *testing.T) {
	repo := newFailoverSettingRepo()
	repo.data[SettingKeyOpenAIFailoverPolicy] = gptniubiOpenAIFailoverJSON
	svc := NewOpenAIFailoverPolicyService(repo, nil)

	cfg, err := svc.Config(context.Background())
	require.NoError(t, err, "a gptniubi table must load as a whole")
	require.Len(t, cfg.Rules, 3)
	policy := svc.PolicyForGroup(context.Background(), groupID(4))
	require.NotNil(t, policy)
	require.Equal(t, "overloaded", policy.match(400, "server overloaded"))
	require.Equal(t, "http-408", policy.match(408, "anything"))
	require.Equal(t, "", policy.match(429, "quota"), "kept 429 rule never decides here")

	// Saving the unchanged table keeps the 429 rule and every unknown field.
	_, err = svc.UpdateConfig(context.Background(), cfg)
	require.NoError(t, err)
	var stored map[string]any
	require.NoError(t, json.Unmarshal([]byte(repo.data[SettingKeyOpenAIFailoverPolicy]), &stored))
	require.EqualValues(t, 3000, stored["slot_wait_ms"])
	require.EqualValues(t, 5, stored["max_slot_switches"])
	rules := stored["rules"].([]any)
	require.Len(t, rules, 3)
	require.Equal(t, "kept", rules[0].(map[string]any)["note"])

	// Editing the 429 rule is still refused.
	cfg.Rules[1].Keywords = []string{"quota", "credits"}
	_, err = svc.UpdateConfig(context.Background(), cfg)
	require.Error(t, err)
}

func TestOpenAIFailoverFirstSaveIsLoadableByGptniubi(t *testing.T) {
	repo := newFailoverSettingRepo()
	svc := NewOpenAIFailoverPolicyService(repo, nil)
	_, err := svc.UpdateConfig(context.Background(), openAIRuleFixture())
	require.NoError(t, err)

	// gptniubi rejects a table whose slot_wait_ms is outside 500..10000 or
	// max_slot_switches outside 0..10.
	var stored struct {
		SlotWaitMs      *int `json:"slot_wait_ms"`
		MaxSlotSwitches *int `json:"max_slot_switches"`
	}
	require.NoError(t, json.Unmarshal([]byte(repo.data[SettingKeyOpenAIFailoverPolicy]), &stored))
	require.NotNil(t, stored.SlotWaitMs)
	require.NotNil(t, stored.MaxSlotSwitches)
	require.GreaterOrEqual(t, *stored.SlotWaitMs, 500)
	require.LessOrEqual(t, *stored.SlotWaitMs, 10000)
	require.GreaterOrEqual(t, *stored.MaxSlotSwitches, 0)
	require.LessOrEqual(t, *stored.MaxSlotSwitches, 10)
}

func TestOpenAIFailoverPolicyServiceKeepsLastSnapshotOnLoadError(t *testing.T) {
	repo := newFailoverSettingRepo()
	svc := NewOpenAIFailoverPolicyService(repo, nil)
	_, err := svc.UpdateConfig(context.Background(), openAIRuleFixture())
	require.NoError(t, err)
	require.NotNil(t, svc.PolicyForGroup(context.Background(), groupID(45)))

	repo.getErr = errors.New("db down")
	svc.reload(context.Background())
	require.NotNil(t, svc.PolicyForGroup(context.Background(), groupID(45)), "a failed load keeps the previous rules")
	require.Nil(t, svc.PolicyForGroup(context.Background(), groupID(46)))
}

func TestOpenAIFailoverPolicyServiceNeverLoadedMeansNoPolicy(t *testing.T) {
	repo := newFailoverSettingRepo()
	repo.getErr = errors.New("db down")
	svc := NewOpenAIFailoverPolicyService(repo, nil)
	require.Nil(t, svc.PolicyForGroup(context.Background(), groupID(45)))
}

func openAIRuleContext(t *testing.T, cfg OpenAIFailoverConfig) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	normalized, err := NormalizeOpenAIFailoverConfig(cfg)
	require.NoError(t, err)
	c.Request = c.Request.WithContext(WithOpenAIFailoverPolicy(c.Request.Context(), newOpenAIFailoverPolicy(normalized)))
	return c
}

func TestMatchOpenAIFailoverRule(t *testing.T) {
	passback := []byte(`{"error":{"message":"The ` + "`reasoning_text`" + ` in the thinking mode must be passed back to the API. (request_id: x)","type":"invalid_request_error"}}`)

	t.Run("keyword in structured message", func(t *testing.T) {
		c := openAIRuleContext(t, openAIRuleFixture())
		require.Equal(t, "reasoning-passback", matchOpenAIFailoverRule(c, 400, "", passback))
	})
	t.Run("case insensitive", func(t *testing.T) {
		c := openAIRuleContext(t, openAIRuleFixture())
		require.Equal(t, "reasoning-passback", matchOpenAIFailoverRule(c, 400, "MUST BE PASSED BACK TO THE API", nil))
	})
	t.Run("status must match", func(t *testing.T) {
		c := openAIRuleContext(t, openAIRuleFixture())
		require.Equal(t, "", matchOpenAIFailoverRule(c, 422, "", passback))
	})
	t.Run("echoed request content does not match", func(t *testing.T) {
		c := openAIRuleContext(t, openAIRuleFixture())
		body := []byte(`{"error":{"message":"invalid tool"},"echo":"must be passed back to the API"}`)
		require.Equal(t, "", matchOpenAIFailoverRule(c, 400, "invalid tool", body))
	})
	t.Run("return rule shields a broader failover rule", func(t *testing.T) {
		cfg := openAIRuleFixture()
		cfg.Rules = append([]OpenAIFailoverRule{{ID: "keep", Enabled: true, StatusCode: 400, Keywords: []string{"thinking mode"}, Action: OpenAIFailoverActionReturn}}, cfg.Rules...)
		c := openAIRuleContext(t, cfg)
		require.Equal(t, "", matchOpenAIFailoverRule(c, 400, "", passback))
	})
	t.Run("context window error never switches", func(t *testing.T) {
		cfg := openAIRuleFixture()
		cfg.Rules = []OpenAIFailoverRule{{ID: "any400", Enabled: true, StatusCode: 400, Action: OpenAIFailoverActionFailover}}
		c := openAIRuleContext(t, cfg)
		body := []byte(`{"error":{"message":"Your input exceeds the context window of this model.","code":"context_length_exceeded"}}`)
		require.Equal(t, "", matchOpenAIFailoverRule(c, 400, "Your input exceeds the context window of this model.", body))
	})
	t.Run("no policy", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		require.Equal(t, "", matchOpenAIFailoverRule(c, 400, "", passback))
	})
	t.Run("disabled rule", func(t *testing.T) {
		cfg := openAIRuleFixture()
		cfg.Rules[0].Enabled = false
		c := openAIRuleContext(t, cfg)
		require.Equal(t, "", matchOpenAIFailoverRule(c, 400, "", passback))
	})
}

func TestTryOpenAIFailoverByRuleSwitchesWithoutSameAccountRetry(t *testing.T) {
	c := openAIRuleContext(t, openAIRuleFixture())
	body := []byte(`{"error":{"message":"must be passed back to the API"}}`)
	resp := &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}}
	replayed := 0
	failoverErr := tryOpenAIFailoverByRule(c, &Account{ID: 1184, Platform: PlatformOpenAI}, resp, body, "must be passed back to the API", false,
		func(c *gin.Context, r *http.Response) {
			replayed++
			require.True(t, isOpenAIRuleReplay(c.Request.Context()))
			require.Equal(t, 400, r.StatusCode)
		})
	require.NotNil(t, failoverErr)
	require.Equal(t, "reasoning-passback", failoverErr.FailoverRuleID)
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.False(t, failoverErr.RequestScopedTransient)
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.NotNil(t, failoverErr.RuleReplay)
	failoverErr.RuleReplay(c)
	require.Equal(t, 1, replayed)
	require.Equal(t, "", matchOpenAIFailoverRule(c, 400, "must be passed back to the API", body), "replay never switches again")
}
