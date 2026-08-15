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
func ProvideService(repo *Repo, cfg *config.Config) *Service {
	return NewService(repo, cfg.DashboardAgg.Retention.UsageLogsDays)
}
