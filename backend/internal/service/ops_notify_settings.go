package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

func defaultOpsNotifyChannelSettings() *OpsNotifyChannelSettings {
	return &OpsNotifyChannelSettings{
		Channels: []OpsNotifyChannelConfig{},
		CriticalError: OpsCriticalErrorNotifyConfig{
			Enabled:         false,
			StatusCodes:     []int{401, 403, 529},
			CooldownMinutes: 10,
		},
	}
}

func normalizeOpsNotifyChannelSettings(cfg *OpsNotifyChannelSettings) {
	if cfg == nil {
		return
	}
	if cfg.Channels == nil {
		cfg.Channels = []OpsNotifyChannelConfig{}
	}
	for i := range cfg.Channels {
		ch := &cfg.Channels[i]
		ch.ID = strings.TrimSpace(ch.ID)
		ch.Name = strings.TrimSpace(ch.Name)
		ch.Type = strings.ToLower(strings.TrimSpace(ch.Type))
		ch.WebhookURL = strings.TrimSpace(ch.WebhookURL)
		ch.MinSeverity = strings.ToLower(strings.TrimSpace(ch.MinSeverity))
		if ch.TimeoutSeconds <= 0 || ch.TimeoutSeconds > 30 {
			ch.TimeoutSeconds = 5
		}
		// SecretConfigured 是派生字段,存储时以 Secret 为准。
		ch.SecretConfigured = strings.TrimSpace(ch.Secret) != ""
	}
	if cfg.CriticalError.StatusCodes == nil {
		cfg.CriticalError.StatusCodes = []int{}
	}
	// 0 表示不冷却(与 allowCooldown 的 ttl<=0 语义一致),必须原样保留;
	// 仅把无意义的负数归 0。新配置的默认值 10 由 defaultOpsNotifyChannelSettings 提供。
	if cfg.CriticalError.CooldownMinutes < 0 {
		cfg.CriticalError.CooldownMinutes = 0
	}
}

func validateOpsNotifyChannelSettings(cfg *OpsNotifyChannelSettings) error {
	if cfg == nil {
		return errors.New("invalid config")
	}
	seen := map[string]struct{}{}
	for i := range cfg.Channels {
		ch := &cfg.Channels[i]
		if ch.Name == "" {
			return fmt.Errorf("channels[%d]: name is required", i)
		}
		if ch.Type != OpsNotifyChannelTypeFeishu && ch.Type != OpsNotifyChannelTypeWebhook {
			return fmt.Errorf("channels[%d]: type must be one of: feishu, webhook", i)
		}
		u, err := url.Parse(ch.WebhookURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("channels[%d]: webhook_url must be a valid http(s) URL", i)
		}
		if ch.Type == OpsNotifyChannelTypeFeishu {
			if !strings.HasPrefix(ch.WebhookURL, "https://open.feishu.cn/open-apis/bot/") &&
				!strings.HasPrefix(ch.WebhookURL, "https://open.larksuite.com/open-apis/bot/") {
				return fmt.Errorf("channels[%d]: feishu webhook_url must start with https://open.feishu.cn/open-apis/bot/ or https://open.larksuite.com/open-apis/bot/", i)
			}
		}
		switch ch.MinSeverity {
		case "", "critical", "warning", "info":
		default:
			return fmt.Errorf("channels[%d]: min_severity must be one of: critical, warning, info, or empty", i)
		}
		if ch.RateLimitPerHour < 0 || ch.RateLimitPerHour > 100000 {
			return fmt.Errorf("channels[%d]: rate_limit_per_hour must be between 0 and 100000", i)
		}
		if ch.ID != "" {
			if _, dup := seen[ch.ID]; dup {
				return fmt.Errorf("channels[%d]: duplicate channel id %s", i, ch.ID)
			}
			seen[ch.ID] = struct{}{}
		}
	}
	for _, code := range cfg.CriticalError.StatusCodes {
		if code < 100 || code > 599 {
			return fmt.Errorf("critical_error.status_codes must be valid HTTP status codes (100-599)")
		}
	}
	if cfg.CriticalError.CooldownMinutes < 0 || cfg.CriticalError.CooldownMinutes > 1440 {
		return errors.New("critical_error.cooldown_minutes must be between 0 and 1440")
	}
	return nil
}

func redactOpsNotifyChannelSettings(cfg *OpsNotifyChannelSettings) *OpsNotifyChannelSettings {
	if cfg == nil {
		return nil
	}
	out := &OpsNotifyChannelSettings{
		Channels:      make([]OpsNotifyChannelConfig, len(cfg.Channels)),
		CriticalError: cfg.CriticalError,
	}
	copy(out.Channels, cfg.Channels)
	for i := range out.Channels {
		out.Channels[i].SecretConfigured = strings.TrimSpace(out.Channels[i].Secret) != ""
		out.Channels[i].Secret = ""
	}
	return out
}

// GetNotifyChannelSettings 返回含明文 secret 的配置(仅限进程内使用,不得直接回传 API)。
func (s *OpsService) GetNotifyChannelSettings(ctx context.Context) (*OpsNotifyChannelSettings, error) {
	defaultCfg := defaultOpsNotifyChannelSettings()
	if s == nil || s.settingRepo == nil {
		return defaultCfg, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	raw, err := s.settingRepo.GetValue(ctx, SettingKeyOpsNotifyChannelConfig)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			if b, mErr := json.Marshal(defaultCfg); mErr == nil {
				_ = s.settingRepo.Set(ctx, SettingKeyOpsNotifyChannelConfig, string(b))
			}
			return defaultCfg, nil
		}
		return nil, err
	}

	cfg := &OpsNotifyChannelSettings{}
	if err := json.Unmarshal([]byte(raw), cfg); err != nil {
		return defaultCfg, nil
	}
	normalizeOpsNotifyChannelSettings(cfg)
	return cfg, nil
}

// GetNotifyChannelSettingsRedacted 返回脱敏配置(handler GET 用)。
func (s *OpsService) GetNotifyChannelSettingsRedacted(ctx context.Context) (*OpsNotifyChannelSettings, error) {
	cfg, err := s.GetNotifyChannelSettings(ctx)
	if err != nil {
		return nil, err
	}
	return redactOpsNotifyChannelSettings(cfg), nil
}

// UpdateNotifyChannelSettings 全量更新;新通道自动分配 ID,空 secret 按 ID 沿用已存值。
// 返回脱敏后的配置。
func (s *OpsService) UpdateNotifyChannelSettings(ctx context.Context, incoming *OpsNotifyChannelSettings) (*OpsNotifyChannelSettings, error) {
	if s == nil || s.settingRepo == nil {
		return nil, errors.New("setting repository not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if incoming == nil {
		return nil, errors.New("invalid config")
	}

	current, err := s.GetNotifyChannelSettings(ctx)
	if err != nil {
		return nil, err
	}
	existingSecrets := map[string]string{}
	for _, ch := range current.Channels {
		if strings.TrimSpace(ch.ID) != "" {
			existingSecrets[ch.ID] = ch.Secret
		}
	}

	normalizeOpsNotifyChannelSettings(incoming)
	for i := range incoming.Channels {
		ch := &incoming.Channels[i]
		if strings.TrimSpace(ch.Secret) == "" {
			if old, ok := existingSecrets[ch.ID]; ok {
				ch.Secret = old
			}
		}
		if ch.ID == "" {
			ch.ID = uuid.NewString()
		}
	}
	// secret 回填后重算派生字段
	normalizeOpsNotifyChannelSettings(incoming)

	if err := validateOpsNotifyChannelSettings(incoming); err != nil {
		return nil, err
	}

	raw, err := json.Marshal(incoming)
	if err != nil {
		return nil, err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyOpsNotifyChannelConfig, string(raw)); err != nil {
		return nil, err
	}
	return redactOpsNotifyChannelSettings(incoming), nil
}

// ValidateOpsNotifyChannel 归一化并校验单个通道,与保存路径同一套规则
// (测试发送等不落库路径复用,避免"测试发送成功但保存配置失败"的偏差)。
// 归一化结果(类型小写、超时默认值等)写回 ch。
func ValidateOpsNotifyChannel(ch *OpsNotifyChannelConfig) error {
	if ch == nil {
		return errors.New("invalid channel")
	}
	tmp := &OpsNotifyChannelSettings{Channels: []OpsNotifyChannelConfig{*ch}}
	normalizeOpsNotifyChannelSettings(tmp)
	if err := validateOpsNotifyChannelSettings(tmp); err != nil {
		return err
	}
	*ch = tmp.Channels[0]
	return nil
}
