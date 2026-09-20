//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUpdateSettingsContactLinksRoundTripOmissionAndClear(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	tg, qq := "https://t.me/example_support", "https://qm.qq.com/q/example"
	saved := doUpdateSettings(t, h, map[string]any{"telegram_url": tg, "qq_group_url": qq}, nil)
	require.Equal(t, http.StatusOK, saved.Code, saved.Body.String())
	require.Equal(t, tg, repo.values[service.SettingKeyTelegramURL])
	require.Equal(t, qq, repo.values[service.SettingKeyQQGroupURL])

	var envelope struct {
		Data struct {
			TelegramURL string `json:"telegram_url"`
			QQGroupURL  string `json:"qq_group_url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(saved.Body.Bytes(), &envelope))
	require.Equal(t, tg, envelope.Data.TelegramURL)
	require.Equal(t, qq, envelope.Data.QQGroupURL)

	assertPublic := func(wantTelegram, wantQQ string) {
		settings, err := h.settingService.GetPublicSettings(context.Background())
		require.NoError(t, err)
		require.Equal(t, wantTelegram, settings.TelegramURL)
		require.Equal(t, wantQQ, settings.QQGroupURL)
		injected, err := h.settingService.GetPublicSettingsForInjection(context.Background())
		require.NoError(t, err)
		payload := injected.(*service.PublicSettingsInjectionPayload)
		require.Equal(t, wantTelegram, payload.TelegramURL)
		require.Equal(t, wantQQ, payload.QQGroupURL)
	}
	assertPublic(tg, qq)

	// An older client does not know these two newly added fields.
	saved = doUpdateSettings(t, h, map[string]any{"site_name": "Changed name"}, nil)
	require.Equal(t, http.StatusOK, saved.Code, saved.Body.String())
	assertPublic(tg, qq)

	saved = doUpdateSettings(t, h, map[string]any{"telegram_url": ""}, nil)
	require.Equal(t, http.StatusOK, saved.Code, saved.Body.String())
	assertPublic("", qq)

	saved = doUpdateSettings(t, h, map[string]any{"qq_group_url": ""}, nil)
	require.Equal(t, http.StatusOK, saved.Code, saved.Body.String())
	assertPublic("", "")
}

func TestUpdateSettingsAuditIgnoresOmittedFields(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyTelegramURL: "https://t.me/example_support",
		service.SettingKeyQQGroupURL:  "https://qm.qq.com/q/example",
		service.SettingKeyDocURL:      "https://docs.example.com",
	})
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(original)
	rec := doUpdateSettings(t, h, map[string]any{"site_name": "Changed name"}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "https://t.me/example_support", repo.values[service.SettingKeyTelegramURL])
	require.Equal(t, "https://qm.qq.com/q/example", repo.values[service.SettingKeyQQGroupURL])
	var audit struct {
		Changed []string `json:"changed"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &audit))
	require.Contains(t, audit.Changed, "site_name", "the field that was actually sent must still be audited")
	require.NotContains(t, audit.Changed, "doc_url", "omitted fields keep their stored value and must not be marked as changed")
	require.NotContains(t, audit.Changed, "telegram_url", "unchanged contact URL must not be marked as changed")
	require.NotContains(t, audit.Changed, "qq_group_url", "unchanged contact URL must not be marked as changed")
}
