//go:build unit

package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func opsNotifyIntPtr(v int) *int       { return &v }
func opsNotifyI64Ptr(v int64) *int64   { return &v }

func criticalCfg() *OpsCriticalErrorNotifyConfig {
	return &OpsCriticalErrorNotifyConfig{Enabled: true, StatusCodes: []int{401, 403, 529}, CooldownMinutes: 10}
}

func TestMatchOpsCriticalError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		entry    *OpsInsertErrorLogInput
		wantCode int
		wantOK   bool
	}{
		{"provider 401 via upstream code", &OpsInsertErrorLogInput{ErrorOwner: "provider", StatusCode: 502, UpstreamStatusCode: opsNotifyIntPtr(401)}, 401, true},
		{"provider 401 via status code", &OpsInsertErrorLogInput{ErrorOwner: "provider", StatusCode: 401}, 401, true},
		{"client owner excluded", &OpsInsertErrorLogInput{ErrorOwner: "client", StatusCode: 401}, 0, false},
		{"platform owner excluded", &OpsInsertErrorLogInput{ErrorOwner: "platform", StatusCode: 401}, 0, false},
		{"business limited excluded", &OpsInsertErrorLogInput{ErrorOwner: "provider", StatusCode: 401, IsBusinessLimited: true}, 0, false},
		{"code not in list", &OpsInsertErrorLogInput{ErrorOwner: "provider", StatusCode: 500}, 0, false},
		{"owner case insensitive", &OpsInsertErrorLogInput{ErrorOwner: "Provider", StatusCode: 529}, 529, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, ok := matchOpsCriticalError(criticalCfg(), tc.entry)
			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.wantCode, code)
		})
	}
}

func TestCriticalErrorCooldownLocalFallback(t *testing.T) {
	t.Parallel()
	// redisClient 为 nil → 走进程内冷却
	n := NewOpsCriticalErrorNotifier(nil, nil, nil, nil)

	require.True(t, n.allowCooldown(nil, "account:1:401", time.Minute))
	require.False(t, n.allowCooldown(nil, "account:1:401", time.Minute))
	// 不同 key 互不影响
	require.True(t, n.allowCooldown(nil, "account:2:401", time.Minute))
	// ttl<=0 → 不做冷却
	require.True(t, n.allowCooldown(nil, "account:3:401", 0))
	require.True(t, n.allowCooldown(nil, "account:3:401", 0))
}

func TestOpsCriticalErrorCooldownKey(t *testing.T) {
	t.Parallel()
	require.Equal(t, "account:7:401",
		opsCriticalErrorCooldownKey(&OpsInsertErrorLogInput{AccountID: opsNotifyI64Ptr(7), Platform: "anthropic"}, 401))
	require.Equal(t, "platform:anthropic:529",
		opsCriticalErrorCooldownKey(&OpsInsertErrorLogInput{Platform: "anthropic"}, 529))
}

func TestCriticalErrorProcessDispatchesOncePerCooldown(t *testing.T) {
	t.Parallel()
	repo := newOpsNotifySettingRepoStub()
	cfgJSON, err := json.Marshal(&OpsNotifyChannelSettings{
		Channels: []OpsNotifyChannelConfig{
			{ID: "c1", Name: "a", Type: "feishu", Enabled: true, WebhookURL: "https://x/1", TimeoutSeconds: 5},
		},
		CriticalError: *criticalCfg(),
	})
	require.NoError(t, err)
	repo.values[SettingKeyOpsNotifyChannelConfig] = string(cfgJSON)

	opsSvc := &OpsService{settingRepo: repo}
	d := NewOpsNotifyDispatcher(opsSvc)
	fake := &fakeNotifySender{}
	d.senders[OpsNotifyChannelTypeFeishu] = fake
	n := NewOpsCriticalErrorNotifier(opsSvc, d, nil, nil)

	entries := []*OpsInsertErrorLogInput{
		{ErrorOwner: "provider", StatusCode: 401, AccountID: opsNotifyI64Ptr(7), Platform: "anthropic",
			ErrorType: "authentication_error", ErrorMessage: "unauthorized", CreatedAt: time.Now()},
		// 同账号同状态码 → 冷却期内被吞
		{ErrorOwner: "provider", StatusCode: 401, AccountID: opsNotifyI64Ptr(7), Platform: "anthropic",
			ErrorType: "authentication_error", ErrorMessage: "unauthorized again", CreatedAt: time.Now()},
		// 客户端错误 → 不报
		{ErrorOwner: "client", StatusCode: 401, Platform: "anthropic", CreatedAt: time.Now()},
	}
	n.process(entries)
	require.Equal(t, 1, fake.callCount())
}

func TestCriticalErrorProcessDisabledConfigNoDispatch(t *testing.T) {
	t.Parallel()
	repo := newOpsNotifySettingRepoStub()
	cfgJSON, err := json.Marshal(&OpsNotifyChannelSettings{
		Channels: []OpsNotifyChannelConfig{
			{ID: "c1", Name: "a", Type: "feishu", Enabled: true, WebhookURL: "https://x/1", TimeoutSeconds: 5},
		},
		CriticalError: OpsCriticalErrorNotifyConfig{Enabled: false, StatusCodes: []int{401}},
	})
	require.NoError(t, err)
	repo.values[SettingKeyOpsNotifyChannelConfig] = string(cfgJSON)

	opsSvc := &OpsService{settingRepo: repo}
	d := NewOpsNotifyDispatcher(opsSvc)
	fake := &fakeNotifySender{}
	d.senders[OpsNotifyChannelTypeFeishu] = fake
	n := NewOpsCriticalErrorNotifier(opsSvc, d, nil, nil)

	n.process([]*OpsInsertErrorLogInput{
		{ErrorOwner: "provider", StatusCode: 401, Platform: "anthropic", CreatedAt: time.Now()},
	})
	require.Equal(t, 0, fake.callCount())
}
