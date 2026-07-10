//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func feishuTestMessage() *OpsNotifyMessage {
	return &OpsNotifyMessage{
		Kind:        OpsNotifyKindAlertFiring,
		Severity:    "P1",
		Title:       "P1: 错误率过高",
		Description: "error_rate > 5.00 (current 12.30) over last 5m (overall)",
		Fields:      []OpsNotifyField{{Label: "规则", Value: "错误率过高"}},
		OccurredAt:  time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC),
	}
}

func TestFeishuSignDeterministic(t *testing.T) {
	t.Parallel()
	s1, err := feishuSign("secret-a", 1751970000)
	require.NoError(t, err)
	s2, err := feishuSign("secret-a", 1751970000)
	require.NoError(t, err)
	require.Equal(t, s1, s2)

	// base64(HMAC-SHA256) → 解码后 32 字节
	rawSig, err := base64.StdEncoding.DecodeString(s1)
	require.NoError(t, err)
	require.Len(t, rawSig, 32)

	s3, err := feishuSign("secret-b", 1751970000)
	require.NoError(t, err)
	require.NotEqual(t, s1, s3)
	s4, err := feishuSign("secret-a", 1751970001)
	require.NoError(t, err)
	require.NotEqual(t, s1, s4)
}

func TestFeishuSendSuccessWithSign(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &captured))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer srv.Close()

	ch := &OpsNotifyChannelConfig{
		Type: OpsNotifyChannelTypeFeishu, WebhookURL: srv.URL,
		Secret: "s", TimeoutSeconds: 5,
	}
	err := feishuNotifySender{}.Send(context.Background(), ch, feishuTestMessage())
	require.NoError(t, err)

	require.Equal(t, "interactive", captured["msg_type"])
	require.NotEmpty(t, captured["timestamp"])
	require.NotEmpty(t, captured["sign"])
	card, ok := captured["card"].(map[string]any)
	require.True(t, ok)
	header := card["header"].(map[string]any)
	require.Equal(t, "orange", header["template"]) // P1 → orange
	title := header["title"].(map[string]any)
	require.Equal(t, "P1: 错误率过高", title["content"])
}

func TestFeishuSendNoSignWhenSecretEmpty(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &captured))
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()

	ch := &OpsNotifyChannelConfig{Type: OpsNotifyChannelTypeFeishu, WebhookURL: srv.URL, TimeoutSeconds: 5}
	require.NoError(t, feishuNotifySender{}.Send(context.Background(), ch, feishuTestMessage()))
	_, hasSign := captured["sign"]
	require.False(t, hasSign)
}

func TestFeishuSendErrorOnNonZeroCode(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":19021,"msg":"sign match fail"}`))
	}))
	defer srv.Close()

	ch := &OpsNotifyChannelConfig{Type: OpsNotifyChannelTypeFeishu, WebhookURL: srv.URL, TimeoutSeconds: 5}
	err := feishuNotifySender{}.Send(context.Background(), ch, feishuTestMessage())
	require.Error(t, err)
	require.Contains(t, err.Error(), "19021")
}

func TestFeishuSendErrorOnHTTPFailure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	ch := &OpsNotifyChannelConfig{Type: OpsNotifyChannelTypeFeishu, WebhookURL: srv.URL, TimeoutSeconds: 5}
	require.Error(t, feishuNotifySender{}.Send(context.Background(), ch, feishuTestMessage()))
}

func TestFeishuCardTemplateByKindAndSeverity(t *testing.T) {
	t.Parallel()
	require.Equal(t, "green", feishuCardTemplate(&OpsNotifyMessage{Kind: OpsNotifyKindAlertResolved, Severity: "P0"}))
	require.Equal(t, "red", feishuCardTemplate(&OpsNotifyMessage{Kind: OpsNotifyKindCriticalError, Severity: "P0"}))
	require.Equal(t, "blue", feishuCardTemplate(&OpsNotifyMessage{Kind: OpsNotifyKindTest}))
	require.Equal(t, "red", feishuCardTemplate(&OpsNotifyMessage{Kind: OpsNotifyKindAlertFiring, Severity: "P0"}))
	require.Equal(t, "orange", feishuCardTemplate(&OpsNotifyMessage{Kind: OpsNotifyKindAlertFiring, Severity: "P1"}))
	require.Equal(t, "yellow", feishuCardTemplate(&OpsNotifyMessage{Kind: OpsNotifyKindAlertFiring, Severity: "P2"}))
}
