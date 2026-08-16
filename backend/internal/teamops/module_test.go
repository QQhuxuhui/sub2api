//go:build unit

package teamops

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"

	"github.com/stretchr/testify/require"
)

// 取值刻意不用 90：配置项 dashboard_aggregation.retention.usage_logs_days 的默认值
// 与 NewService 的兜底 defaultRetentionDays 恰好都是 90，用 90 做用例的话
// 「读到了配置」和「压根没读」在默认部署上输出逐字相同，这条测试什么都钉不住。
//
// 读丢的后果正是设计文档 §4.5 认输规则要防的那件事：把 usage_logs_days 显式配成 30 的
// 部署会静默按 90 跑，ApplyRetention 于是把一段已被物理清理的区间判成「可比」，
// 页面照常给出环比百分比，没有任何报错。
func TestProvideService_ReadsRetentionWindowFromConfig(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.DashboardAgg.Enabled = true
	cfg.DashboardAgg.Retention.UsageLogsDays = 45

	require.Equal(t, 45, ProvideService(nil, cfg).retentionDays)
}

// 聚合关着时 usage_logs 的保留清理不跑（dashboard_aggregation_service.go 在 !cfg.Enabled 时
// 直接返回），历史日志是完整的，**不该**有任何保留期限制。
//
// 这一条不是理论情况：config.go 里整个 retention 校验块都包在 `if c.DashboardAgg.Enabled` 里，
// 关闭时 usage_logs_days 既可能是 0（没人写过），也可能停在 viper 默认值 90 上。
// 两种取值都必须得出「无限制」，否则那种部署会被按 90 天砍：页面藏掉环比、
// 打出「数据不完整」的提示条，而实际上全部日志都在。
func TestProvideService_NoRetentionLimitWhenAggregationDisabled(t *testing.T) {
	t.Parallel()

	zero := &config.Config{}
	zero.DashboardAgg.Enabled = false
	require.Equal(t, 0, ProvideService(nil, zero).retentionDays, "没写过 usage_logs_days")

	viperDefault := &config.Config{}
	viperDefault.DashboardAgg.Enabled = false
	viperDefault.DashboardAgg.Retention.UsageLogsDays = 90
	require.Equal(t, 0, ProvideService(nil, viperDefault).retentionDays,
		"停在 viper 默认值 90 上的死数，同样不该当成保留边界")
}

// 聚合开着而窗口没读出来时回落到包内默认值，而不是把 0 原样带下去——
// 清理确实在删日志，此时 retentionDays=0 会让越界判定全线放行，
// 页面转而拿一段已被物理清理的区间算环比。
func TestProvideService_FallsBackToDefaultWhenEnabledButUnconfigured(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.DashboardAgg.Enabled = true

	require.Equal(t, defaultRetentionDays, ProvideService(nil, cfg).retentionDays)
}
