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
// Enabled 必须一起传：usage_logs 的按天保留清理挂在聚合作业里
// （dashboard_aggregation_service.go 的 maybeCleanupRetention，定时入口 Start() 在
// !cfg.Enabled 时直接返回）。关掉聚合后 usage_logs_days 既不被 config 校验
// （config.go 把整段 retention 校验包在 if c.DashboardAgg.Enabled 里），
// 也不该被拿来当保留边界——它多半只是停在 viper 默认值 90 上的一个死数。
//
// 注意这不等于「关掉聚合就绝对没人删 usage_logs」，已知两条例外：
//   - TriggerBackfill 不判 Enabled（只判 BackfillEnabled，默认 false），
//     而 backfillRange 尾部无条件调 maybeCleanupRetention；
//   - usage_cleanup 是管理员手工指定时间范围的独立删除通道，默认开启，
//     它删掉的区间本来就无法用「保留 N 天」建模。
//
// 两条都不在本包的判定范围内：前者默认不可达，后者是任务式删除。
// 这里的语义是「本部署有没有开启按天自动保留清理」，不是「日志有没有可能被删」。
func ProvideService(repo *Repo, cfg *config.Config) *Service {
	return NewService(repo, cfg.DashboardAgg.Retention.UsageLogsDays, cfg.DashboardAgg.Enabled)
}
