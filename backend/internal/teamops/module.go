package teamops

import (
	"github.com/Wei-Shaw/sub2api/internal/config"

	"github.com/google/wire"
)

// ProviderSet 是本包对外的依赖注入入口。NewRepo 的 *sql.DB 由 repository.ProvideSQLDB 提供，
// 不需要在这里声明。
var ProviderSet = wire.NewSet(
	NewRepo,
	ProvideService,
	NewHandler,
)

// ProvideService 从配置取保留窗口构造服务层。
// 字段名是 DashboardAgg（mapstructure 标签才是 dashboard_aggregation）。
//
// Enabled 必须一起传：usage_logs 的保留清理是聚合作业的一部分
// （dashboard_aggregation_service.go 的 maybeCleanupRetention，作业本身在 !cfg.Enabled 时直接返回）。
// 关掉聚合的部署没人删日志，历史是完整的，此时 usage_logs_days 既不被 config 校验、
// 也不该被拿来当保留边界——它多半只是停在 viper 默认值 90 上的一个死数。
func ProvideService(repo *Repo, cfg *config.Config) *Service {
	return NewService(repo, cfg.DashboardAgg.Retention.UsageLogsDays, cfg.DashboardAgg.Enabled)
}
