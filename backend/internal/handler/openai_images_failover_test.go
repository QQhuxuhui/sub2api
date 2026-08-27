//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type openAIImagesFailoverAccountRepo struct {
	service.AccountRepository
	accounts []service.Account
}

func (r openAIImagesFailoverAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			account := r.accounts[i]
			return &account, nil
		}
	}
	return nil, service.ErrNoAvailableAccounts
}

func (r openAIImagesFailoverAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, _ int64, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

func (r openAIImagesFailoverAccountRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

func (r openAIImagesFailoverAccountRepo) ListSchedulableUngroupedByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

func (r openAIImagesFailoverAccountRepo) accountsForPlatform(platform string) []service.Account {
	out := make([]service.Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		if account.Platform == platform {
			out = append(out, account)
		}
	}
	return out
}

type openAIImagesFailoverHTTPUpstream struct {
	service.HTTPUpstream
	mu         sync.Mutex
	accountIDs []int64
}

type openAIImagesMappedOptionsHTTPUpstream struct {
	service.HTTPUpstream
	mu         sync.Mutex
	accountIDs []int64
}

func (u *openAIImagesMappedOptionsHTTPUpstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.accountIDs = append(u.accountIDs, accountID)
	u.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(bytes.NewBufferString(
			"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{},\"tool_usage\":{\"image_gen\":{\"images\":1}},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aGVsbG8=\"}]}}\n\ndata: [DONE]\n\n",
		)),
	}, nil
}

func (u *openAIImagesMappedOptionsHTTPUpstream) calls() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.accountIDs...)
}

func (u *openAIImagesFailoverHTTPUpstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.accountIDs = append(u.accountIDs, accountID)
	u.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"X-Request-Id": []string{"req_img_failover"},
		},
		Body: io.NopCloser(bytes.NewBufferString(
			"data: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"code\":\"server_error\",\"message\":\"image backend unavailable\"}}\n\n",
		)),
	}, nil
}

func (u *openAIImagesFailoverHTTPUpstream) calls() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.accountIDs...)
}

func TestOpenAIGatewayHandlerImages_ServerErrorFailsOverAndReturnsClearErrorWhenExhausted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(3130)
	accounts := []service.Account{
		{
			ID:          1,
			Name:        "image-account-1",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeOAuth,
			Status:      service.StatusActive,
			Schedulable: true,
			Concurrency: 0,
			Priority:    0,
			Credentials: map[string]any{"access_token": "token-1", "openai_images_highres": true},
		},
		{
			ID:          2,
			Name:        "image-account-2",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeOAuth,
			Status:      service.StatusActive,
			Schedulable: true,
			Concurrency: 0,
			Priority:    1,
			Credentials: map[string]any{"access_token": "token-2", "openai_images_highres": true},
		},
	}
	accountRepo := openAIImagesFailoverAccountRepo{accounts: accounts}
	upstream := &openAIImagesFailoverHTTPUpstream{}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	gatewayService := service.NewOpenAIGatewayService(
		accountRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
		nil,
		nil,
		nil,
		nil,
		nil,
		upstream,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil, // emptyResponseBillingRepo
	)
	billingService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingService.Stop)
	concurrencyService := service.NewConcurrencyService(nil)
	handler := NewOpenAIGatewayHandler(
		gatewayService,
		concurrencyService,
		billingService,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil,
		nil,
		nil,
		nil,
		cfg,
	)
	handler.maxAccountSwitches = 10

	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","quality":"high","size":"1536x1024"}`)
	core, observedLogs := observer.New(zap.DebugLevel)
	requestCtx := logger.IntoContext(context.Background(), zap.New(core))
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body)).WithContext(requestCtx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      99,
		GroupID: &groupID,
		Group: &service.Group{
			ID:                   groupID,
			AllowImageGeneration: true,
		},
		User: &service.User{ID: 100},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100, Concurrency: 0})

	handler.Images(c)

	accountSelectingLogs := observedLogs.FilterMessage("openai.images.account_selecting").All()
	require.NotEmpty(t, accountSelectingLogs)
	loggedFields := make(map[string]string)
	for _, field := range accountSelectingLogs[0].Context {
		loggedFields[field.Key] = field.String
	}
	require.Equal(t, "high", loggedFields["img_quality"])
	require.Equal(t, "1536x1024", loggedFields["img_size"])
	require.NotContains(t, loggedFields, "prompt")

	require.Equal(t, []int64{1, 2}, upstream.calls())
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, "upstream_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Equal(t, "Upstream service temporarily unavailable", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())

	rawEvents, ok := c.Get(service.OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*service.OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 2)
	require.Equal(t, "failover", events[0].Kind)
	require.Equal(t, "failover", events[1].Kind)
}

func runOpenAIImagesAccountMappingCompatibilityTest(
	t *testing.T,
	incompatibleTarget string,
	body []byte,
	compatibleTarget ...string,
) (*httptest.ResponseRecorder, *openAIImagesMappedOptionsHTTPUpstream) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	groupID := int64(3131)
	requestModel := gjson.GetBytes(body, "model").String()
	if requestModel == "" {
		requestModel = "grok-imagine"
	}
	secondTarget := requestModel
	if len(compatibleTarget) > 0 {
		secondTarget = compatibleTarget[0]
	}
	accounts := []service.Account{
		{
			ID: 11, Name: "incompatible-image-account", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 0,
			Credentials: map[string]any{
				"access_token":  "token-1",
				"model_mapping": map[string]any{requestModel: incompatibleTarget},
			},
		},
		{
			ID: 12, Name: "compatible-image-account", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 1,
			Credentials: map[string]any{
				"access_token":  "token-2",
				"model_mapping": map[string]any{requestModel: secondTarget},
			},
		},
	}
	accountRepo := openAIImagesFailoverAccountRepo{accounts: accounts}
	upstream := &openAIImagesMappedOptionsHTTPUpstream{}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	gatewayService := service.NewOpenAIGatewayService(
		accountRepo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil,
		upstream, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, // emptyResponseBillingRepo
	)
	billingService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingService.Stop)
	handler := NewOpenAIGatewayHandler(
		gatewayService, service.NewConcurrencyService(nil), billingService,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil, nil, nil, nil, cfg,
	)
	handler.maxAccountSwitches = 10

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID: 100, GroupID: &groupID,
		Group: &service.Group{ID: groupID, AllowImageGeneration: true},
		User:  &service.User{ID: 101},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 101})

	handler.Images(c)
	return rec, upstream
}

func TestOpenAIGatewayHandlerImages_AccountMappingOptionMismatchFailsOver(t *testing.T) {
	body := []byte(`{"model":"grok-imagine","prompt":"draw a cat","quality":"ultra"}`)
	rec, upstream := runOpenAIImagesAccountMappingCompatibilityTest(t, "gpt-image-2", body)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []int64{12}, upstream.calls())
	require.Equal(t, "aGVsbG8=", gjson.GetBytes(rec.Body.Bytes(), "data.0.b64_json").String())
}

func TestOpenAIGatewayHandlerImages_AccountMappingNonImageModelFailsOver(t *testing.T) {
	body := []byte(`{"model":"grok-imagine","prompt":"draw a cat"}`)
	rec, upstream := runOpenAIImagesAccountMappingCompatibilityTest(t, "gpt-5.4", body)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []int64{12}, upstream.calls())
	require.Equal(t, "aGVsbG8=", gjson.GetBytes(rec.Body.Bytes(), "data.0.b64_json").String())
}

func TestOpenAIGatewayHandlerImages_ReverseModelMappingPreservesClientStream(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw","stream":true}`)
	rec, upstream := runOpenAIImagesAccountMappingCompatibilityTest(t, "gpt-image-1.5", body, "gpt-image-2")

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []int64{11}, upstream.calls())
	require.Contains(t, rec.Body.String(), "event: image_generation.completed")
}

func TestOpenAIGatewayHandlerImages_ChannelMappingNonImageModelReturnsBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat"}`)
	requestCtx := service.WithCompositeRouteDecision(context.Background(), service.CompositeRouteDecision{
		Matched:        true,
		TargetPlatform: service.PlatformOpenAI,
		UpstreamModel:  "gpt-5.4",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body)).WithContext(requestCtx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	groupID := int64(3132)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID: 102, GroupID: &groupID,
		Group: &service.Group{ID: groupID, AllowImageGeneration: true},
		User:  &service.User{ID: 103},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 103})

	h := &OpenAIGatewayHandler{
		gatewayService:      &service.OpenAIGatewayService{},
		billingCacheService: &service.BillingCacheService{},
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   &ConcurrencyHelper{concurrencyService: &service.ConcurrencyService{}},
	}
	h.Images(c)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Contains(t, gjson.GetBytes(rec.Body.Bytes(), "error.message").String(), "image model")
}
