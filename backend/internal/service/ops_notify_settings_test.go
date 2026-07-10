//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// opsNotifySettingRepoStub 内存版 SettingRepository。
type opsNotifySettingRepoStub struct {
	values map[string]string
}

func newOpsNotifySettingRepoStub() *opsNotifySettingRepoStub {
	return &opsNotifySettingRepoStub{values: map[string]string{}}
}

func (s *opsNotifySettingRepoStub) Get(ctx context.Context, key string) (*Setting, error) {
	v, ok := s.values[key]
	if !ok {
		return nil, ErrSettingNotFound
	}
	return &Setting{Key: key, Value: v}, nil
}

func (s *opsNotifySettingRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	v, ok := s.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return v, nil
}

func (s *opsNotifySettingRepoStub) Set(ctx context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

func (s *opsNotifySettingRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := s.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func (s *opsNotifySettingRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	for k, v := range settings {
		s.values[k] = v
	}
	return nil
}

func (s *opsNotifySettingRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	return s.values, nil
}

func (s *opsNotifySettingRepoStub) Delete(ctx context.Context, key string) error {
	delete(s.values, key)
	return nil
}

var _ SettingRepository = (*opsNotifySettingRepoStub)(nil)

func newOpsServiceForNotifyTest() (*OpsService, *opsNotifySettingRepoStub) {
	repo := newOpsNotifySettingRepoStub()
	return &OpsService{settingRepo: repo}, repo
}

func TestGetNotifyChannelSettingsDefaults(t *testing.T) {
	t.Parallel()
	svc, _ := newOpsServiceForNotifyTest()

	cfg, err := svc.GetNotifyChannelSettings(context.Background())
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Empty(t, cfg.Channels)
	require.False(t, cfg.CriticalError.Enabled)
	require.Equal(t, []int{401, 403, 529}, cfg.CriticalError.StatusCodes)
	require.Equal(t, 10, cfg.CriticalError.CooldownMinutes)
}

func TestUpdateNotifyChannelSettingsAssignsIDAndRedacts(t *testing.T) {
	t.Parallel()
	svc, _ := newOpsServiceForNotifyTest()

	updated, err := svc.UpdateNotifyChannelSettings(context.Background(), &OpsNotifyChannelSettings{
		Channels: []OpsNotifyChannelConfig{{
			Name:       "研发群",
			Type:       OpsNotifyChannelTypeFeishu,
			Enabled:    true,
			WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/abc",
			Secret:     "s3cret",
		}},
		CriticalError: OpsCriticalErrorNotifyConfig{Enabled: true, StatusCodes: []int{401}, CooldownMinutes: 5},
	})
	require.NoError(t, err)
	require.Len(t, updated.Channels, 1)
	require.NotEmpty(t, updated.Channels[0].ID)
	// 返回值必须脱敏
	require.Empty(t, updated.Channels[0].Secret)
	require.True(t, updated.Channels[0].SecretConfigured)
	// 默认值补齐
	require.Equal(t, 5, updated.Channels[0].TimeoutSeconds)

	// 内部读取拿得到明文 secret
	raw, err := svc.GetNotifyChannelSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, "s3cret", raw.Channels[0].Secret)
}

func TestUpdateNotifyChannelSettingsPreservesSecretWhenEmpty(t *testing.T) {
	t.Parallel()
	svc, _ := newOpsServiceForNotifyTest()

	first, err := svc.UpdateNotifyChannelSettings(context.Background(), &OpsNotifyChannelSettings{
		Channels: []OpsNotifyChannelConfig{{
			Name: "ch", Type: OpsNotifyChannelTypeWebhook, Enabled: true,
			WebhookURL: "https://alerts.example.com/hook", Secret: "old-secret",
		}},
	})
	require.NoError(t, err)
	id := first.Channels[0].ID

	// 空 secret + 相同 ID → 保留旧值
	_, err = svc.UpdateNotifyChannelSettings(context.Background(), &OpsNotifyChannelSettings{
		Channels: []OpsNotifyChannelConfig{{
			ID: id, Name: "ch", Type: OpsNotifyChannelTypeWebhook, Enabled: true,
			WebhookURL: "https://alerts.example.com/hook", Secret: "",
		}},
	})
	require.NoError(t, err)
	raw, err := svc.GetNotifyChannelSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, "old-secret", raw.Channels[0].Secret)

	// 提供新 secret → 覆盖
	_, err = svc.UpdateNotifyChannelSettings(context.Background(), &OpsNotifyChannelSettings{
		Channels: []OpsNotifyChannelConfig{{
			ID: id, Name: "ch", Type: OpsNotifyChannelTypeWebhook, Enabled: true,
			WebhookURL: "https://alerts.example.com/hook", Secret: "new-secret",
		}},
	})
	require.NoError(t, err)
	raw, err = svc.GetNotifyChannelSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, "new-secret", raw.Channels[0].Secret)
}

func TestUpdateNotifyChannelSettingsValidation(t *testing.T) {
	t.Parallel()
	svc, _ := newOpsServiceForNotifyTest()

	cases := []struct {
		name string
		ch   OpsNotifyChannelConfig
		want string
	}{
		{"missing name", OpsNotifyChannelConfig{Type: "feishu", WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/x"}, "name"},
		{"bad type", OpsNotifyChannelConfig{Name: "a", Type: "dingtalk", WebhookURL: "https://x.example.com/"}, "type"},
		{"bad url", OpsNotifyChannelConfig{Name: "a", Type: "webhook", WebhookURL: "not-a-url"}, "webhook_url"},
		{"feishu url prefix", OpsNotifyChannelConfig{Name: "a", Type: "feishu", WebhookURL: "https://evil.example.com/hook"}, "feishu"},
		{"bad min severity", OpsNotifyChannelConfig{Name: "a", Type: "webhook", WebhookURL: "https://x.example.com/", MinSeverity: "P0"}, "min_severity"},
		{"bad rate limit", OpsNotifyChannelConfig{Name: "a", Type: "webhook", WebhookURL: "https://x.example.com/", RateLimitPerHour: -1}, "rate_limit_per_hour"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.UpdateNotifyChannelSettings(context.Background(), &OpsNotifyChannelSettings{
				Channels: []OpsNotifyChannelConfig{tc.ch},
			})
			require.Error(t, err)
			require.True(t, strings.Contains(err.Error(), tc.want), "error %q should mention %q", err.Error(), tc.want)
		})
	}

	// 状态码越界
	_, err := svc.UpdateNotifyChannelSettings(context.Background(), &OpsNotifyChannelSettings{
		CriticalError: OpsCriticalErrorNotifyConfig{Enabled: true, StatusCodes: []int{99}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "status_codes")
}

func TestGetNotifyChannelSettingsCorruptedJSONFallsBack(t *testing.T) {
	t.Parallel()
	svc, repo := newOpsServiceForNotifyTest()
	repo.values[SettingKeyOpsNotifyChannelConfig] = "{not json"

	cfg, err := svc.GetNotifyChannelSettings(context.Background())
	require.NoError(t, err)
	require.Empty(t, cfg.Channels)
}
