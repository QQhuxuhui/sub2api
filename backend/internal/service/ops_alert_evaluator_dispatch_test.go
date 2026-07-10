//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newDispatcherWithFeishuChannel 构造一个带单个已启用 feishu 通道的 dispatcher,sender 替换为 fake。
func newDispatcherWithFeishuChannel(t *testing.T) (*OpsNotifyDispatcher, *fakeNotifySender) {
	t.Helper()
	repo := newOpsNotifySettingRepoStub()
	cfg := &OpsNotifyChannelSettings{Channels: []OpsNotifyChannelConfig{
		{ID: "c1", Name: "a", Type: "feishu", Enabled: true, WebhookURL: "https://x/1", NotifyResolved: true, TimeoutSeconds: 5},
	}}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo.values[SettingKeyOpsNotifyChannelConfig] = string(raw)

	d := NewOpsNotifyDispatcher(&OpsService{settingRepo: repo})
	fake := &fakeNotifySender{}
	d.senders[OpsNotifyChannelTypeFeishu] = fake
	return d, fake
}

// newOpsServiceForEvaluatorTest 构造一个 OpsService,使 IsMonitoringEnabled 为 true 且
// GetOpsAlertRuntimeSettings 关闭分布式锁(避免走 Redis leader-lock 分支,redisClient 为 nil)。
func newOpsServiceForEvaluatorTest(t *testing.T, silencingEnabled bool) *OpsService {
	t.Helper()
	repo := newOpsNotifySettingRepoStub()
	repo.values[SettingKeyOpsMonitoringEnabled] = "true"

	runtimeCfg := defaultOpsAlertRuntimeSettings()
	runtimeCfg.DistributedLock.Enabled = false
	if silencingEnabled {
		runtimeCfg.Silencing.Enabled = true
		runtimeCfg.Silencing.GlobalUntilRFC3339 = time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	}
	raw, err := json.Marshal(runtimeCfg)
	require.NoError(t, err)
	repo.values[SettingKeyOpsAlertRuntimeSettings] = string(raw)

	return &OpsService{settingRepo: repo}
}

func newTestAlertRule() *OpsAlertRule {
	return &OpsAlertRule{
		ID:               1,
		Name:             "High CPU",
		Enabled:          true,
		Severity:         "P0",
		MetricType:       "cpu_usage_percent",
		Operator:         ">",
		Threshold:        50,
		WindowMinutes:    1,
		SustainedMinutes: 0,
		CooldownMinutes:  0,
	}
}

func newEvaluatorForTest(opsService *OpsService, opsRepo OpsRepository, dispatcher *OpsNotifyDispatcher) *OpsAlertEvaluatorService {
	return &OpsAlertEvaluatorService{
		opsService:   opsService,
		opsRepo:      opsRepo,
		dispatcher:   dispatcher,
		ruleStates:   map[int64]*opsAlertRuleState{},
		emailLimiter: newSlidingWindowLimiter(0, time.Hour),
	}
}

// TestEvaluateOnceDispatchesFiringAlert 驱动真实的 evaluateOnce:一条启用规则命中阈值、
// 无活跃事件 → 创建事件并异步分发 alert_firing 给 dispatcher。
func TestEvaluateOnceDispatchesFiringAlert(t *testing.T) {
	t.Parallel()
	dispatcher, fake := newDispatcherWithFeishuChannel(t)
	opsService := newOpsServiceForEvaluatorTest(t, false)

	repo := &opsRepoMock{
		ListAlertRulesFn: func(ctx context.Context) ([]*OpsAlertRule, error) {
			return []*OpsAlertRule{newTestAlertRule()}, nil
		},
		GetLatestSystemMetricsFn: func(ctx context.Context, windowMinutes int) (*OpsSystemMetricsSnapshot, error) {
			return &OpsSystemMetricsSnapshot{CPUUsagePercent: float64Ptr(90)}, nil
		},
		GetActiveAlertEventFn: func(ctx context.Context, ruleID int64) (*OpsAlertEvent, error) {
			return nil, nil
		},
		CreateAlertEventFn: func(ctx context.Context, event *OpsAlertEvent) (*OpsAlertEvent, error) {
			created := *event
			created.ID = 42
			return &created, nil
		},
	}

	s := newEvaluatorForTest(opsService, repo, dispatcher)
	s.evaluateOnce(time.Minute)

	require.Eventually(t, func() bool { return fake.callCount() == 1 }, 2*time.Second, 10*time.Millisecond)
	msg := fake.lastMessage()
	require.NotNil(t, msg)
	require.Equal(t, OpsNotifyKindAlertFiring, msg.Kind)
	require.Equal(t, "P0", msg.Severity)
}

// TestEvaluateOnceDispatchesResolvedAlert 驱动真实的 evaluateOnce:已有活跃事件,但本轮指标
// 不再命中阈值 → UpdateAlertEventStatus(resolved) 并异步分发 alert_resolved。
func TestEvaluateOnceDispatchesResolvedAlert(t *testing.T) {
	t.Parallel()
	dispatcher, fake := newDispatcherWithFeishuChannel(t)
	opsService := newOpsServiceForEvaluatorTest(t, false)

	firedAt := time.Now().UTC().Add(-10 * time.Minute)
	activeEvent := &OpsAlertEvent{
		ID: 100, RuleID: 1, Severity: "P0", Status: OpsAlertStatusFiring,
		Title: "P0: High CPU", FiredAt: firedAt,
	}

	var updateCalled bool
	repo := &opsRepoMock{
		ListAlertRulesFn: func(ctx context.Context) ([]*OpsAlertRule, error) {
			return []*OpsAlertRule{newTestAlertRule()}, nil
		},
		GetLatestSystemMetricsFn: func(ctx context.Context, windowMinutes int) (*OpsSystemMetricsSnapshot, error) {
			return &OpsSystemMetricsSnapshot{CPUUsagePercent: float64Ptr(10)}, nil
		},
		GetActiveAlertEventFn: func(ctx context.Context, ruleID int64) (*OpsAlertEvent, error) {
			return activeEvent, nil
		},
		UpdateAlertEventStatusFn: func(ctx context.Context, eventID int64, status string, resolvedAt *time.Time) error {
			updateCalled = true
			require.Equal(t, int64(100), eventID)
			require.Equal(t, OpsAlertStatusResolved, status)
			return nil
		},
	}

	s := newEvaluatorForTest(opsService, repo, dispatcher)
	s.evaluateOnce(time.Minute)

	require.Eventually(t, func() bool { return fake.callCount() == 1 }, 2*time.Second, 10*time.Millisecond)
	require.True(t, updateCalled)
	msg := fake.lastMessage()
	require.NotNil(t, msg)
	require.Equal(t, OpsNotifyKindAlertResolved, msg.Kind)
}

// TestEvaluateOnceSkipsDispatchWhenGloballySilenced 驱动真实的 evaluateOnce:运行时全局静默
// 生效期间,规则命中阈值仍会创建告警事件(供 UI 展示),但不应触发任何通知分发。
func TestEvaluateOnceSkipsDispatchWhenGloballySilenced(t *testing.T) {
	t.Parallel()
	dispatcher, fake := newDispatcherWithFeishuChannel(t)
	opsService := newOpsServiceForEvaluatorTest(t, true)

	repo := &opsRepoMock{
		ListAlertRulesFn: func(ctx context.Context) ([]*OpsAlertRule, error) {
			return []*OpsAlertRule{newTestAlertRule()}, nil
		},
		GetLatestSystemMetricsFn: func(ctx context.Context, windowMinutes int) (*OpsSystemMetricsSnapshot, error) {
			return &OpsSystemMetricsSnapshot{CPUUsagePercent: float64Ptr(90)}, nil
		},
		GetActiveAlertEventFn: func(ctx context.Context, ruleID int64) (*OpsAlertEvent, error) {
			return nil, nil
		},
		CreateAlertEventFn: func(ctx context.Context, event *OpsAlertEvent) (*OpsAlertEvent, error) {
			created := *event
			created.ID = 43
			return &created, nil
		},
	}

	s := newEvaluatorForTest(opsService, repo, dispatcher)
	s.evaluateOnce(time.Minute)

	// 给异步分发路径留出足够时间证明"确实不会发生",而不仅仅是还没发生。
	time.Sleep(300 * time.Millisecond)
	require.Equal(t, 0, fake.callCount())
}
