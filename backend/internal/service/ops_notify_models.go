package service

import "time"

// Ops notify channel models. 配置存 settings 表单 key JSON blob;
// OpsNotifyMessage 是通道无关的告警消息(评估器窗口告警与严重错误即时通知两条路径都归一到它)。

const (
	OpsNotifyChannelTypeFeishu  = "feishu"
	OpsNotifyChannelTypeWebhook = "webhook"

	OpsNotifyKindAlertFiring   = "alert_firing"
	OpsNotifyKindAlertResolved = "alert_resolved"
	OpsNotifyKindCriticalError = "critical_error"
	OpsNotifyKindTest          = "test"
)

type OpsNotifyChannelSettings struct {
	Channels      []OpsNotifyChannelConfig     `json:"channels"`
	CriticalError OpsCriticalErrorNotifyConfig `json:"critical_error"`
}

type OpsNotifyChannelConfig struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"` // feishu | webhook
	Enabled bool   `json:"enabled"`

	WebhookURL string `json:"webhook_url"`
	// Secret: 飞书加签密钥 / webhook HMAC 密钥。GET 响应中一律清空,由 SecretConfigured 指示是否已配置;
	// PUT 时留空表示保留已存储的值(按 ID 匹配)。
	Secret           string `json:"secret,omitempty"`
	SecretConfigured bool   `json:"secret_configured"`

	// MinSeverity: ""(全部)/ critical / warning / info,与邮件通知配置同一词表。
	MinSeverity      string `json:"min_severity"`
	RateLimitPerHour int    `json:"rate_limit_per_hour"` // 0 = 不限
	NotifyResolved   bool   `json:"notify_resolved"`
	TimeoutSeconds   int    `json:"timeout_seconds"`
}

type OpsCriticalErrorNotifyConfig struct {
	Enabled bool `json:"enabled"`
	// StatusCodes 匹配 COALESCE(upstream_status_code, status_code)。
	StatusCodes     []int `json:"status_codes"`
	CooldownMinutes int   `json:"cooldown_minutes"` // 账号+状态码维度冷却
}

type OpsNotifyMessage struct {
	Kind        string
	Severity    string // P0/P1/P2(经 opsEmailSeverityForOps 映射后与通道 MinSeverity 比较)
	Title       string
	Description string
	Fields      []OpsNotifyField
	OccurredAt  time.Time
}

type OpsNotifyField struct {
	Label string `json:"label"`
	Value string `json:"value"`
}
