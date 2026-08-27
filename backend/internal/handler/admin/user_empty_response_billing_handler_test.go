package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stubEmptyResponseBillingAdminRepo struct {
	rules      []service.EmptyResponseBillingRule
	replaceErr error

	replacedUserID int64
	replacedRules  []service.EmptyResponseBillingRule
}

func (s *stubEmptyResponseBillingAdminRepo) ListEnabledByUserID(context.Context, int64) ([]service.EmptyResponseBillingRule, error) {
	return s.rules, nil
}

func (s *stubEmptyResponseBillingAdminRepo) ListByUserID(context.Context, int64) ([]service.EmptyResponseBillingRule, error) {
	return s.rules, nil
}

func (s *stubEmptyResponseBillingAdminRepo) ReplaceByUserID(_ context.Context, userID int64, rules []service.EmptyResponseBillingRule) error {
	if s.replaceErr != nil {
		return s.replaceErr
	}
	s.replacedUserID = userID
	s.replacedRules = rules
	s.rules = rules
	return nil
}

type stubEmptyResponseInvalidator struct{ invalidated []int64 }

func (s *stubEmptyResponseInvalidator) InvalidateEmptyResponseBillingRules(userID int64) {
	s.invalidated = append(s.invalidated, userID)
}

func setupEmptyResponseBillingRouter(repo service.UserEmptyResponseBillingAdminRepository, invalidators ...service.EmptyResponseBillingRuleInvalidator) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := NewUserHandler(newStubAdminService(), nil, nil, nil, nil, nil, nil, repo, invalidators)
	router.GET("/admin/users/:id/empty-response-billing-rules", h.GetUserEmptyResponseBillingRules)
	router.PUT("/admin/users/:id/empty-response-billing-rules", h.UpdateUserEmptyResponseBillingRules)
	return router
}

func TestGetUserEmptyResponseBillingRules(t *testing.T) {
	groupID := int64(12)
	repo := &stubEmptyResponseBillingAdminRepo{rules: []service.EmptyResponseBillingRule{
		{ID: 1, UserID: 67, GroupID: &groupID, Model: "gemini-3.1-flash-image", Enabled: true},
	}}
	router := setupEmptyResponseBillingRouter(repo)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/users/67/empty-response-billing-rules", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data struct {
			Rules []EmptyResponseBillingRuleView `json:"rules"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Data.Rules, 1)
	require.Equal(t, "gemini-3.1-flash-image", resp.Data.Rules[0].Model)
	require.NotNil(t, resp.Data.Rules[0].GroupID)
	require.Equal(t, int64(12), *resp.Data.Rules[0].GroupID)
}

func TestGetUserEmptyResponseBillingRules_RepoUnavailable(t *testing.T) {
	router := setupEmptyResponseBillingRouter(nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/users/67/empty-response-billing-rules", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func putRules(t *testing.T, router *gin.Engine, userID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPut, "/admin/users/"+userID+"/empty-response-billing-rules", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestUpdateUserEmptyResponseBillingRules_ReplacesAndInvalidates(t *testing.T) {
	repo := &stubEmptyResponseBillingAdminRepo{}
	inv1 := &stubEmptyResponseInvalidator{}
	inv2 := &stubEmptyResponseInvalidator{}
	router := setupEmptyResponseBillingRouter(repo, inv1, inv2)

	groupID := int64(12)
	rec := putRules(t, router, "67", UpdateEmptyResponseBillingRulesRequest{Rules: []EmptyResponseBillingRuleInput{
		{GroupID: &groupID, Model: "  Gemini-3.1-Flash-Image  ", Note: " 空返回免单 "},
		{Model: ""},
	}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	require.Equal(t, int64(67), repo.replacedUserID)
	require.Len(t, repo.replacedRules, 2)
	// 模型名与备注要去掉首尾空白；enabled 缺省为 true。
	require.Equal(t, "Gemini-3.1-Flash-Image", repo.replacedRules[0].Model)
	require.Equal(t, "空返回免单", repo.replacedRules[0].Note)
	require.True(t, repo.replacedRules[0].Enabled)
	require.Nil(t, repo.replacedRules[1].GroupID)

	// 两个网关服务的规则缓存都必须被失效，否则最长 60s 内新规则不生效。
	require.Equal(t, []int64{67}, inv1.invalidated)
	require.Equal(t, []int64{67}, inv2.invalidated)
}

func TestUpdateUserEmptyResponseBillingRules_Validation(t *testing.T) {
	repo := &stubEmptyResponseBillingAdminRepo{}
	router := setupEmptyResponseBillingRouter(repo)
	groupID := int64(12)

	t.Run("重复规则（大小写折叠后）拒绝", func(t *testing.T) {
		rec := putRules(t, router, "67", UpdateEmptyResponseBillingRulesRequest{Rules: []EmptyResponseBillingRuleInput{
			{GroupID: &groupID, Model: "gpt-image-2"},
			{GroupID: &groupID, Model: "GPT-IMAGE-2"},
		}})
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("非法 group_id", func(t *testing.T) {
		bad := int64(-1)
		rec := putRules(t, router, "67", UpdateEmptyResponseBillingRulesRequest{Rules: []EmptyResponseBillingRuleInput{
			{GroupID: &bad},
		}})
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("条数超上限", func(t *testing.T) {
		rules := make([]EmptyResponseBillingRuleInput, maxEmptyResponseBillingRulesPerUser+1)
		for i := range rules {
			rules[i] = EmptyResponseBillingRuleInput{Model: "m"}
		}
		rec := putRules(t, router, "67", UpdateEmptyResponseBillingRulesRequest{Rules: rules})
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("外键失败转 400", func(t *testing.T) {
		failing := &stubEmptyResponseBillingAdminRepo{replaceErr: errors.New(`pq: insert or update on table "user_empty_response_billing_rules" violates foreign key constraint "user_empty_response_billing_rules_group_id_fkey"`)}
		failRouter := setupEmptyResponseBillingRouter(failing)
		rec := putRules(t, failRouter, "67", UpdateEmptyResponseBillingRulesRequest{Rules: []EmptyResponseBillingRuleInput{
			{GroupID: &groupID, Model: "gpt-image-2"},
		}})
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("清空规则集是合法操作", func(t *testing.T) {
		rec := putRules(t, router, "67", UpdateEmptyResponseBillingRulesRequest{Rules: []EmptyResponseBillingRuleInput{}})
		require.Equal(t, http.StatusOK, rec.Code)
		require.Empty(t, repo.replacedRules)
	})
}
