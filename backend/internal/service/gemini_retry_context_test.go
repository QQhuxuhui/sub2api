package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type geminiRequestContextErrorUpstream struct {
	calls int
}

func (s *geminiRequestContextErrorUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	s.calls++
	return nil, req.Context().Err()
}

func (s *geminiRequestContextErrorUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxyURL, accountID, accountConcurrency)
}

type geminiTransientThenSuccessUpstream struct {
	calls int
}

func (s *geminiTransientThenSuccessUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	s.calls++
	if s.calls == 1 {
		return nil, errors.New("temporary upstream error")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"x-request-id": []string{"gemini-retry-success"},
		},
		Body: io.NopCloser(strings.NewReader(`{
			"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2},
			"modelVersion":"gemini-test"
		}`)),
	}, nil
}

func (s *geminiTransientThenSuccessUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxyURL, accountID, accountConcurrency)
}

func TestGeminiForwardNativeStopsAfterContextDeadline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)

	upstream := &geminiRequestContextErrorUpstream{}
	svc := &GeminiMessagesCompatService{
		httpUpstream: upstream,
		cfg:          &config.Config{},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-test:generateContent", nil)
	account := &Account{
		ID:       1,
		Name:     "gemini-test",
		Platform: PlatformGemini,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "test-key",
		},
	}

	type forwardResult struct {
		result *ForwardResult
		err    error
	}
	done := make(chan forwardResult, 1)
	go func() {
		result, err := svc.ForwardNative(
			ctx,
			c,
			account,
			"gemini-test",
			"generateContent",
			false,
			[]byte(`{"contents":[{"parts":[{"text":"hello"}]}]}`),
		)
		done <- forwardResult{result: result, err: err}
	}()

	select {
	case got := <-done:
		require.Nil(t, got.result)
		require.ErrorIs(t, got.err, context.DeadlineExceeded)
		require.Equal(t, 1, upstream.calls)
	case <-time.After(200 * time.Millisecond):
		require.Fail(t, "ForwardNative continued into Gemini retry backoff after its context deadline")
	}
}

func TestSleepGeminiBackoffReturnsOnCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	startedAt := time.Now()
	err := sleepGeminiBackoff(ctx, 1)

	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(startedAt), 100*time.Millisecond)
}

func TestGeminiForwardNativeRetriesTransientErrorWhileContextActive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &geminiTransientThenSuccessUpstream{}
	svc := &GeminiMessagesCompatService{
		httpUpstream: upstream,
		cfg:          &config.Config{},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-test:generateContent", nil)
	account := &Account{
		ID:       1,
		Name:     "gemini-test",
		Platform: PlatformGemini,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "test-key",
		},
	}

	startedAt := time.Now()
	result, err := svc.ForwardNative(
		context.Background(),
		c,
		account,
		"gemini-test",
		"generateContent",
		false,
		[]byte(`{"contents":[{"parts":[{"text":"hello"}]}]}`),
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 2, upstream.calls)
	require.GreaterOrEqual(t, time.Since(startedAt), 700*time.Millisecond)
}
