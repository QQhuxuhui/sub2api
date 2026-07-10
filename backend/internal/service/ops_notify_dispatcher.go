package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	// 配置进程内缓存 TTL。保存配置后最多 30s 生效(测试发送走请求体内的通道对象,不受影响)。
	opsNotifySettingsCacheTTL = 30 * time.Second
	// 单次分发(含全部通道)总超时。
	opsNotifyDispatchTimeout = 15 * time.Second
)

type opsNotifySender interface {
	Send(ctx context.Context, ch *OpsNotifyChannelConfig, msg *OpsNotifyMessage) error
}

type opsNotifySettingsSnapshot struct {
	cfg      *OpsNotifyChannelSettings
	loadedAt time.Time
}

// OpsNotifyDispatcher 把 OpsNotifyMessage 分发到所有满足条件的通知通道。
// 发送是 fire-and-forget:任何失败只记日志,绝不影响调用方。
type OpsNotifyDispatcher struct {
	opsService *OpsService
	senders    map[string]opsNotifySender

	mu       sync.Mutex
	limiters map[string]*slidingWindowLimiter

	cache atomic.Value // *opsNotifySettingsSnapshot
}

func NewOpsNotifyDispatcher(opsService *OpsService) *OpsNotifyDispatcher {
	return &OpsNotifyDispatcher{
		opsService: opsService,
		senders: map[string]opsNotifySender{
			OpsNotifyChannelTypeFeishu:  feishuNotifySender{},
			OpsNotifyChannelTypeWebhook: webhookNotifySender{},
		},
		limiters: map[string]*slidingWindowLimiter{},
	}
}

func (d *OpsNotifyDispatcher) getSettings(ctx context.Context) *OpsNotifyChannelSettings {
	if v := d.cache.Load(); v != nil {
		if snap, ok := v.(*opsNotifySettingsSnapshot); ok && time.Since(snap.loadedAt) < opsNotifySettingsCacheTTL {
			return snap.cfg
		}
	}
	cfg, err := d.opsService.GetNotifyChannelSettings(ctx)
	if err != nil || cfg == nil {
		// 读配置失败按"无通道"降级,不缓存错误结果。
		return &OpsNotifyChannelSettings{}
	}
	d.cache.Store(&opsNotifySettingsSnapshot{cfg: cfg, loadedAt: time.Now()})
	return cfg
}

func (d *OpsNotifyDispatcher) limiterFor(channelID string) *slidingWindowLimiter {
	d.mu.Lock()
	defer d.mu.Unlock()
	l, ok := d.limiters[channelID]
	if !ok {
		l = newSlidingWindowLimiter(0, time.Hour)
		d.limiters[channelID] = l
	}
	return l
}

func (d *OpsNotifyDispatcher) channelAllows(ch *OpsNotifyChannelConfig, msg *OpsNotifyMessage, now time.Time) bool {
	if ch == nil || msg == nil || !ch.Enabled {
		return false
	}
	if _, ok := d.senders[ch.Type]; !ok {
		return false
	}
	if strings.TrimSpace(ch.WebhookURL) == "" {
		return false
	}
	if msg.Kind == OpsNotifyKindAlertResolved && !ch.NotifyResolved {
		return false
	}
	// 复用邮件路径的级别比较:P0→critical / P1→warning / 其他→info。
	if !shouldSendOpsAlertEmailByMinSeverity(ch.MinSeverity, msg.Severity) {
		return false
	}
	limiter := d.limiterFor(ch.ID)
	limiter.SetLimit(ch.RateLimitPerHour)
	return limiter.Allow(now)
}

// Dispatch 异步分发,立即返回。
func (d *OpsNotifyDispatcher) Dispatch(msg *OpsNotifyMessage) {
	if d == nil || msg == nil {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.LegacyPrintf("service.ops_notify", "[OpsNotify] dispatch panic recovered: %v", r)
			}
		}()
		d.dispatch(msg)
	}()
}

// dispatch 同步执行一次分发(测试直接调它)。
func (d *OpsNotifyDispatcher) dispatch(msg *OpsNotifyMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), opsNotifyDispatchTimeout)
	defer cancel()

	cfg := d.getSettings(ctx)
	now := time.Now().UTC()

	var wg sync.WaitGroup
	for i := range cfg.Channels {
		ch := cfg.Channels[i]
		if !d.channelAllows(&ch, msg, now) {
			continue
		}
		wg.Add(1)
		go func(ch OpsNotifyChannelConfig) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					logger.LegacyPrintf("service.ops_notify",
						"[OpsNotify] channel send panic recovered (channel=%s type=%s): %v", ch.Name, ch.Type, r)
				}
			}()
			sender := d.senders[ch.Type]
			if err := sender.Send(ctx, &ch, msg); err != nil {
				logger.LegacyPrintf("service.ops_notify",
					"[OpsNotify] send failed (channel=%s type=%s kind=%s): %v", ch.Name, ch.Type, msg.Kind, err)
			}
		}(ch)
	}
	wg.Wait()
}

// DispatchAlertEvent 把告警规则事件转成通知消息并分发(kind: alert_firing / alert_resolved)。
func (d *OpsNotifyDispatcher) DispatchAlertEvent(rule *OpsAlertRule, event *OpsAlertEvent, kind string) {
	if d == nil || rule == nil || event == nil {
		return
	}
	d.Dispatch(buildOpsNotifyMessageFromAlertEvent(rule, event, kind))
}

func buildOpsNotifyMessageFromAlertEvent(rule *OpsAlertRule, event *OpsAlertEvent, kind string) *OpsNotifyMessage {
	value := "-"
	if event.MetricValue != nil {
		value = fmt.Sprintf("%.2f", *event.MetricValue)
	}
	statusLabel := "触发"
	if kind == OpsNotifyKindAlertResolved {
		statusLabel = "已恢复"
	}
	fields := []OpsNotifyField{
		{Label: "规则", Value: strings.TrimSpace(rule.Name)},
		{Label: "级别", Value: strings.TrimSpace(rule.Severity)},
		{Label: "状态", Value: statusLabel},
		{Label: "指标", Value: fmt.Sprintf("%s %s %.2f (当前 %s)",
			strings.TrimSpace(rule.MetricType), strings.TrimSpace(rule.Operator), rule.Threshold, value)},
		{Label: "窗口", Value: fmt.Sprintf("%d 分钟", rule.WindowMinutes)},
	}
	if platform, ok := event.Dimensions["platform"].(string); ok && strings.TrimSpace(platform) != "" {
		fields = append(fields, OpsNotifyField{Label: "平台", Value: strings.TrimSpace(platform)})
	}
	if groupID, ok := event.Dimensions["group_id"]; ok {
		fields = append(fields, OpsNotifyField{Label: "分组", Value: fmt.Sprintf("%v", groupID)})
	}
	fields = append(fields, OpsNotifyField{Label: "事件ID", Value: fmt.Sprintf("%d", event.ID)})

	title := strings.TrimSpace(event.Title)
	occurredAt := event.FiredAt
	if kind == OpsNotifyKindAlertResolved {
		title = "[已恢复] " + title
		if event.ResolvedAt != nil {
			occurredAt = *event.ResolvedAt
		}
	}
	return &OpsNotifyMessage{
		Kind:        kind,
		Severity:    strings.TrimSpace(rule.Severity),
		Title:       title,
		Description: strings.TrimSpace(event.Description),
		Fields:      fields,
		OccurredAt:  occurredAt,
	}
}

// SendTest 同步向单个通道发送测试消息,绕过 enabled/级别/限速过滤(用于连通性验证)。
func (d *OpsNotifyDispatcher) SendTest(ctx context.Context, ch *OpsNotifyChannelConfig) error {
	if d == nil || ch == nil {
		return fmt.Errorf("nil dispatcher or channel")
	}
	sender, ok := d.senders[strings.ToLower(strings.TrimSpace(ch.Type))]
	if !ok {
		return fmt.Errorf("unknown channel type: %s", ch.Type)
	}
	msg := &OpsNotifyMessage{
		Kind:        OpsNotifyKindTest,
		Severity:    "P2",
		Title:       "sub2api 通知通道测试",
		Description: "这是一条测试消息,收到即表示通道配置有效。",
		OccurredAt:  time.Now().UTC(),
	}
	return sender.Send(ctx, ch, msg)
}
