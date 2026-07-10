//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeNotifySender struct {
	mu    sync.Mutex
	calls []string // channel ID 列表
	err   error
}

func (f *fakeNotifySender) Send(ctx context.Context, ch *OpsNotifyChannelConfig, msg *OpsNotifyMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, ch.ID)
	return f.err
}

func (f *fakeNotifySender) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// newDispatcherForTest 构造带内存配置的 dispatcher,并把两种 sender 都换成 fake。
func newDispatcherForTest(t *testing.T, cfg *OpsNotifyChannelSettings) (*OpsNotifyDispatcher, *fakeNotifySender) {
	t.Helper()
	repo := newOpsNotifySettingRepoStub()
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo.values[SettingKeyOpsNotifyChannelConfig] = string(raw)

	d := NewOpsNotifyDispatcher(&OpsService{settingRepo: repo})
	fake := &fakeNotifySender{}
	d.senders[OpsNotifyChannelTypeFeishu] = fake
	d.senders[OpsNotifyChannelTypeWebhook] = fake
	return d, fake
}

func firingMsg(severity string) *OpsNotifyMessage {
	return &OpsNotifyMessage{Kind: OpsNotifyKindAlertFiring, Severity: severity, Title: "t", OccurredAt: time.Now()}
}

func TestDispatchFiltersDisabledAndMinSeverity(t *testing.T) {
	t.Parallel()
	d, fake := newDispatcherForTest(t, &OpsNotifyChannelSettings{
		Channels: []OpsNotifyChannelConfig{
			{ID: "on-all", Name: "a", Type: "feishu", Enabled: true, WebhookURL: "https://x/1", TimeoutSeconds: 5},
			{ID: "disabled", Name: "b", Type: "feishu", Enabled: false, WebhookURL: "https://x/2", TimeoutSeconds: 5},
			{ID: "critical-only", Name: "c", Type: "webhook", Enabled: true, WebhookURL: "https://x/3", MinSeverity: "critical", TimeoutSeconds: 5},
		},
	})

	// P1 → warning:命中 on-all,不命中 critical-only,disabled 永不命中
	d.dispatch(firingMsg("P1"))
	require.Equal(t, []string{"on-all"}, fake.calls)

	// P0 → critical:两个启用通道都命中
	d.dispatch(firingMsg("P0"))
	require.Equal(t, 3, fake.callCount())
}

func TestDispatchResolvedGate(t *testing.T) {
	t.Parallel()
	d, fake := newDispatcherForTest(t, &OpsNotifyChannelSettings{
		Channels: []OpsNotifyChannelConfig{
			{ID: "with-resolved", Name: "a", Type: "feishu", Enabled: true, WebhookURL: "https://x/1", NotifyResolved: true, TimeoutSeconds: 5},
			{ID: "no-resolved", Name: "b", Type: "feishu", Enabled: true, WebhookURL: "https://x/2", NotifyResolved: false, TimeoutSeconds: 5},
		},
	})
	d.dispatch(&OpsNotifyMessage{Kind: OpsNotifyKindAlertResolved, Severity: "P1", Title: "t", OccurredAt: time.Now()})
	require.Equal(t, []string{"with-resolved"}, fake.calls)
}

func TestDispatchRateLimitPerChannel(t *testing.T) {
	t.Parallel()
	d, fake := newDispatcherForTest(t, &OpsNotifyChannelSettings{
		Channels: []OpsNotifyChannelConfig{
			{ID: "limited", Name: "a", Type: "feishu", Enabled: true, WebhookURL: "https://x/1", RateLimitPerHour: 2, TimeoutSeconds: 5},
		},
	})
	for i := 0; i < 5; i++ {
		d.dispatch(firingMsg("P0"))
	}
	require.Equal(t, 2, fake.callCount())
}

func TestDispatchOneChannelFailureDoesNotAffectOthers(t *testing.T) {
	t.Parallel()
	d, fake := newDispatcherForTest(t, &OpsNotifyChannelSettings{
		Channels: []OpsNotifyChannelConfig{
			{ID: "c1", Name: "a", Type: "feishu", Enabled: true, WebhookURL: "https://x/1", TimeoutSeconds: 5},
			{ID: "c2", Name: "b", Type: "webhook", Enabled: true, WebhookURL: "https://x/2", TimeoutSeconds: 5},
		},
	})
	fake.err = errors.New("boom")
	d.dispatch(firingMsg("P0"))
	// 两个通道都被尝试(失败只记日志)
	require.Equal(t, 2, fake.callCount())
}

func TestBuildOpsNotifyMessageFromAlertEvent(t *testing.T) {
	t.Parallel()
	value := 12.3
	threshold := 5.0
	rule := &OpsAlertRule{ID: 7, Name: "错误率过高", Severity: "P1", MetricType: "error_rate", Operator: ">", Threshold: 5, WindowMinutes: 5}
	firedAt := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	event := &OpsAlertEvent{ID: 42, RuleID: 7, Severity: "P1", Status: OpsAlertStatusFiring,
		Title: "P1: 错误率过高", Description: "desc", MetricValue: &value, ThresholdValue: &threshold,
		Dimensions: map[string]any{"platform": "anthropic"}, FiredAt: firedAt}

	msg := buildOpsNotifyMessageFromAlertEvent(rule, event, OpsNotifyKindAlertFiring)
	require.Equal(t, OpsNotifyKindAlertFiring, msg.Kind)
	require.Equal(t, "P1", msg.Severity)
	require.Equal(t, "P1: 错误率过高", msg.Title)
	require.Equal(t, firedAt, msg.OccurredAt)
	labels := map[string]string{}
	for _, f := range msg.Fields {
		labels[f.Label] = f.Value
	}
	require.Equal(t, "错误率过高", labels["规则"])
	require.Contains(t, labels["指标"], "error_rate > 5.00")
	require.Contains(t, labels["指标"], "12.30")
	require.Equal(t, "anthropic", labels["平台"])
	require.Equal(t, "42", labels["事件ID"])

	resolvedAt := firedAt.Add(10 * time.Minute)
	event.Status = OpsAlertStatusResolved
	event.ResolvedAt = &resolvedAt
	msg2 := buildOpsNotifyMessageFromAlertEvent(rule, event, OpsNotifyKindAlertResolved)
	require.Equal(t, "[已恢复] P1: 错误率过高", msg2.Title)
	require.Equal(t, resolvedAt, msg2.OccurredAt)
}

func TestSendTestUsesChannelType(t *testing.T) {
	t.Parallel()
	d, fake := newDispatcherForTest(t, &OpsNotifyChannelSettings{})
	// 未启用、限速为 0 的通道也能测试(SendTest 绕过过滤)
	err := d.SendTest(context.Background(), &OpsNotifyChannelConfig{
		ID: "t1", Name: "x", Type: OpsNotifyChannelTypeFeishu, Enabled: false, WebhookURL: "https://x/1", TimeoutSeconds: 5,
	})
	require.NoError(t, err)
	require.Equal(t, 1, fake.callCount())

	err = d.SendTest(context.Background(), &OpsNotifyChannelConfig{Type: "unknown"})
	require.Error(t, err)
}
