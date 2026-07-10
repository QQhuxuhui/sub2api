//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func webhookTestMessage() *OpsNotifyMessage {
	return &OpsNotifyMessage{
		Kind:        OpsNotifyKindCriticalError,
		Severity:    "P0",
		Title:       "上游账号严重错误: anthropic 401",
		Description: "",
		Fields:      []OpsNotifyField{{Label: "平台", Value: "anthropic"}, {Label: "状态码", Value: "401"}},
		OccurredAt:  time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC),
	}
}

func TestWebhookSendPayloadAndSignature(t *testing.T) {
	t.Parallel()
	var gotBody []byte
	var gotTimestamp, gotSignature string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotTimestamp = r.Header.Get("X-Sub2API-Timestamp")
		gotSignature = r.Header.Get("X-Sub2API-Signature")
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch := &OpsNotifyChannelConfig{Type: OpsNotifyChannelTypeWebhook, WebhookURL: srv.URL, Secret: "whsec", TimeoutSeconds: 5}
	require.NoError(t, webhookNotifySender{}.Send(context.Background(), ch, webhookTestMessage()))

	var payload map[string]any
	require.NoError(t, json.Unmarshal(gotBody, &payload))
	require.Equal(t, "critical_error", payload["kind"])
	require.Equal(t, "P0", payload["severity"])
	require.Equal(t, "上游账号严重错误: anthropic 401", payload["title"])
	require.Equal(t, "2026-07-09T10:00:00Z", payload["occurred_at"])
	fields := payload["fields"].([]any)
	require.Len(t, fields, 2)

	// 验签:接收方按同样算法应能复现签名
	require.NotEmpty(t, gotTimestamp)
	require.Equal(t, webhookSignature("whsec", gotTimestamp, gotBody), gotSignature)
}

func TestWebhookSendNoSignatureWhenSecretEmpty(t *testing.T) {
	t.Parallel()
	var gotSignature string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSignature = r.Header.Get("X-Sub2API-Signature")
		w.WriteHeader(http.StatusNoContent) // 204 也算 2xx 成功
	}))
	defer srv.Close()

	ch := &OpsNotifyChannelConfig{Type: OpsNotifyChannelTypeWebhook, WebhookURL: srv.URL, TimeoutSeconds: 5}
	require.NoError(t, webhookNotifySender{}.Send(context.Background(), ch, webhookTestMessage()))
	require.Empty(t, gotSignature)
}

func TestWebhookSendErrorOnNon2xx(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ch := &OpsNotifyChannelConfig{Type: OpsNotifyChannelTypeWebhook, WebhookURL: srv.URL, TimeoutSeconds: 5}
	err := webhookNotifySender{}.Send(context.Background(), ch, webhookTestMessage())
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
}
