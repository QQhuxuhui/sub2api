// Package teamops 实现团队消耗看板：按「归属」聚合该账号的 API 消耗。
// 分组规则：令牌设了归属就按归属名合并（同名即同一人），没设归属的按令牌自身单独成行。
// 设计文档 docs/superpowers/specs/2026-08-15-team-usage-console-design.md
package teamops
