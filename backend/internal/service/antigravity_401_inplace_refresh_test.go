//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// TestAntigravityTokenProvider_ForceRefreshAccessToken 验证强制刷新：
// 即使本地 expires_at 未过期，也应走刷新流程并返回新 access_token，
// 用于上游 401（bearer token 失效/误判）后的原地重试。
func TestAntigravityTokenProvider_ForceRefreshAccessToken(t *testing.T) {
	account := &Account{
		ID:       100,
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "rt-100",
		},
	}
	repo := &refreshAPIAccountRepo{account: account}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  map[string]any{"access_token": "new-token"},
	}
	provider := &AntigravityTokenProvider{accountRepo: repo}
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, nil), executor)

	token, err := provider.ForceRefreshAccessToken(context.Background(), account)

	require.NoError(t, err)
	require.Equal(t, "new-token", token, "force refresh must return the freshly refreshed access_token")
	require.Equal(t, 1, executor.refreshCalls, "force refresh must trigger executor.Refresh exactly once")
}

func TestAntigravityTokenProvider_ForceRefreshAccessToken_Guards(t *testing.T) {
	provider := &AntigravityTokenProvider{}

	t.Run("nil account", func(t *testing.T) {
		_, err := provider.ForceRefreshAccessToken(context.Background(), nil)
		require.Error(t, err)
	})

	t.Run("non-antigravity", func(t *testing.T) {
		_, err := provider.ForceRefreshAccessToken(context.Background(), &Account{
			Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		})
		require.Error(t, err)
	})

	t.Run("refresh api not configured", func(t *testing.T) {
		_, err := provider.ForceRefreshAccessToken(context.Background(), &Account{
			Platform: PlatformAntigravity, Type: AccountTypeOAuth,
		})
		require.Error(t, err)
	})
}

// TestAntigravityRetryLoop_401_ForceRefreshAndRetryInPlace 验证：
// 上游返回 401（Invalid bearer token）时，循环应强制刷新 token 并用新 token 原地重试一次；
// 第二次成功则正常返回 200，而不是把 401 直接抛给客户端。
func TestAntigravityRetryLoop_401_ForceRefreshAndRetryInPlace(t *testing.T) {
	resp401 := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":{"message":"Invalid bearer token"}}`))),
	}
	resp200 := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"result":"ok"}`))),
	}
	upstream := &mockSmartRetryUpstream{
		responses: []*http.Response{resp401, resp200},
		errors:    []error{nil, nil},
	}

	account := &Account{
		ID:       100,
		Name:     "acc-401",
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "rt-100",
		},
		Concurrency: 1,
	}
	repo := &refreshAPIAccountRepo{account: account}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  map[string]any{"access_token": "new-token"},
	}
	provider := &AntigravityTokenProvider{accountRepo: repo}
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, nil), executor)

	svc := &AntigravityGatewayService{tokenProvider: provider}
	result, err := svc.antigravityRetryLoop(antigravityRetryLoopParams{
		ctx:          context.Background(),
		prefix:       "[test-401]",
		account:      account,
		accessToken:  "old-token",
		action:       "generateContent",
		body:         []byte(`{"input":"test"}`),
		httpUpstream: upstream,
		handleError: func(context.Context, string, *Account, int, http.Header, []byte, string, int64, string, bool) *handleModelRateLimitResult {
			return nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.resp)
	require.Equal(t, http.StatusOK, result.resp.StatusCode, "401 should be recovered by force-refresh + in-place retry")
	require.Equal(t, 2, len(upstream.calls), "should retry once after 401 (2 upstream calls)")
	require.Equal(t, 1, executor.refreshCalls, "401 should force exactly one token refresh")
}

func TestAntigravityRetryLoop_401_ForceRefreshHasIndependentRetryBudget(t *testing.T) {
	serverError := func() *http.Response {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     http.Header{},
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":{"message":"temporary failure"}}`))),
		}
	}
	upstream := &mockSmartRetryUpstream{
		responses: []*http.Response{
			serverError(),
			serverError(),
			{
				StatusCode: http.StatusUnauthorized,
				Header:     http.Header{},
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":{"message":"Invalid bearer token"}}`))),
			},
			{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"result":"ok"}`))),
			},
		},
		errors: []error{nil, nil, nil, nil},
	}

	account := &Account{
		ID:          101,
		Name:        "acc-401-budget",
		Platform:    PlatformAntigravity,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "rt-101",
		},
	}
	repo := &refreshAPIAccountRepo{account: account}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  map[string]any{"access_token": "new-token"},
	}
	provider := &AntigravityTokenProvider{accountRepo: repo}
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, nil), executor)

	service := &AntigravityGatewayService{tokenProvider: provider}
	result, err := service.antigravityRetryLoop(antigravityRetryLoopParams{
		ctx:          context.Background(),
		prefix:       "[test-401-budget]",
		account:      account,
		accessToken:  "old-token",
		action:       "generateContent",
		body:         []byte(`{"input":"test"}`),
		httpUpstream: upstream,
		handleError: func(context.Context, string, *Account, int, http.Header, []byte, string, int64, string, bool) *handleModelRateLimitResult {
			return nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.resp)
	require.Equal(t, http.StatusOK, result.resp.StatusCode)
	require.Len(t, upstream.calls, 4, "the refreshed token must get one request even when generic retries are exhausted")
	require.Equal(t, 1, executor.refreshCalls)
}

func TestAntigravityRetryLoop_401_ForceRefreshPrecedesTempUnschedulablePolicy(t *testing.T) {
	upstream := &mockSmartRetryUpstream{
		responses: []*http.Response{
			{
				StatusCode: http.StatusUnauthorized,
				Header:     http.Header{},
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":{"message":"Invalid bearer token"}}`))),
			},
			{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"result":"ok"}`))),
			},
		},
		errors: []error{nil, nil},
	}

	account := &Account{
		ID:          102,
		Name:        "acc-401-policy",
		Platform:    PlatformAntigravity,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":               "old-token",
			"refresh_token":              "rt-102",
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{
				map[string]any{
					"error_code":       float64(http.StatusUnauthorized),
					"keywords":         []any{"invalid bearer token"},
					"duration_minutes": float64(10),
				},
			},
		},
	}
	refreshRepo := &refreshAPIAccountRepo{account: account}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  map[string]any{"access_token": "new-token"},
	}
	provider := &AntigravityTokenProvider{accountRepo: refreshRepo}
	provider.SetRefreshAPI(NewOAuthRefreshAPI(refreshRepo, nil), executor)
	policyRepo := &rateLimitAccountRepoStub{}
	rateLimitService := NewRateLimitService(policyRepo, nil, &config.Config{}, nil, nil)

	service := &AntigravityGatewayService{
		tokenProvider:    provider,
		rateLimitService: rateLimitService,
	}
	result, err := service.antigravityRetryLoop(antigravityRetryLoopParams{
		ctx:          context.Background(),
		prefix:       "[test-401-policy]",
		account:      account,
		accessToken:  "old-token",
		action:       "generateContent",
		body:         []byte(`{"input":"test"}`),
		httpUpstream: upstream,
		handleError: func(context.Context, string, *Account, int, http.Header, []byte, string, int64, string, bool) *handleModelRateLimitResult {
			return nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.resp)
	require.Equal(t, http.StatusOK, result.resp.StatusCode)
	require.Equal(t, 0, policyRepo.tempCalls, "401 recovery must run before temp-unschedulable policy")
	require.Equal(t, 0, policyRepo.setErrorCalls)
	require.Equal(t, 1, executor.refreshCalls)
}
