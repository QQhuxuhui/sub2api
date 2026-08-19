// Package teamops 实现团队消耗看板：按「归属」聚合该账号的 API 消耗。
// 分组规则：令牌设了归属就按归属名合并（同名即同一人），没设归属的按令牌自身单独成行。
// 本阶段（阶段 0）**只读**：没有归属写入端点，路由只有 GET /user/team/summary 与
// GET /user/team/rows。team_key_owners 上线后恒为空表，于是 groupKeyExpr 恒走 'k:' 分支、
// by_owner 恒 false、owned_key_count 恒 0 —— 页面实际只按令牌名分组。
// 写归属的 PUT /user/team/owners 在后续 PR，落地后「按人合并」才生效。
// 归属分组链路目前只被集成测试的手工 INSERT 覆盖，生产覆盖为零。
//
// 查询分两条，刻意不合并（usage_logs 已 1300 万行 / 13GB）：
//   - ListRows / Summary 是**主查询**，按 (api_key_id) 分组，缓存 token 与缓存节省
//     跟着同一次扫描顺带算出来；
//   - EnrichRows 是**当页补充查询**，只针对当页那几十把令牌，按 (分组, 模型) 与
//     (分组, 日期) 再聚一次，给出主力模型与趋势线。日期按请求 Period 的用户时区
//     分桶，与 Daily 数组的自然日下标一一对应。它失败时服务层降级、不拖垮主功能。
//
// 缓存节省是**近似值**（逐行按同一行的输入单价估价，再应用该行用户计费倍率），
// 口径见 Summary.CacheSavedCost；对外文案必须写明「近似」，不能说成精确节省额。
//
// 保留窗口的 90 天是**近似值**：本包按「now − N 天」的精确时刻判越界，而 usage_logs
// 若为分区部署，实际按整月 DROP TABLE usage_logs_YYYYMM，真实地板是月初，
// 最多会白藏 30 天本来查得到的数据。方向保守（少给环比、不给错数），不修。
//
// 设计文档 docs/superpowers/specs/2026-08-15-team-usage-console-design.md
package teamops
