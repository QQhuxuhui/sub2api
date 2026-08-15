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
	cfg.DashboardAgg.Retention.UsageLogsDays = 45

	require.Equal(t, 45, ProvideService(nil, cfg).retentionDays)
}

// 配置缺省（零值）时回落到包内默认值，而不是把 0 原样带下去——
// retentionDays <= 0 会让 ApplyRetention 无从判定越界、全线判为可比。
func TestProvideService_FallsBackToDefaultWhenConfigIsZero(t *testing.T) {
	t.Parallel()
	require.Equal(t, defaultRetentionDays, ProvideService(nil, &config.Config{}).retentionDays)
}
