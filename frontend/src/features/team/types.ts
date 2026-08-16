// 与后端 backend/internal/teamops 的 JSON 契约一一对应。
// 后端有一组键集合快照测试（json_contract_test.go）钉住这些字段名：
// 那边改名/增删键会让 CI 红，提示同步这里。改这个文件时也请回头看那条用例。

export interface TeamPeriod {
  start_date: string
  end_date: string
  timezone: string
}

export interface TeamCompare {
  start_date: string
  end_date: string
  /** 上期起点早于保留边界时为 false，此时环比一律显示「—」，不要自己算 */
  comparable: boolean
  reason: string | null
}

export interface TeamRetentionWarning {
  /** 目前只有 period_partial 一档 */
  kind: string
  cut_date: string
}

export interface TeamConclusion {
  kind: string
  group_key: string
  display_name: string
  text: string
  extra_count: number
}

export interface TeamSummary {
  period: TeamPeriod
  compare: TeamCompare
  /** 展示值（已做最大余数法分配，保证 Σ各行 == 本值） */
  total_cost: number
  /** 未取整原值，仅供对账/验收，不要拿它渲染 */
  total_cost_raw: number
  prev_cost: number
  /** 上期展示值取整后 <= 0 时为 null —— 认输，前端渲染「—」，不要退回自己算 */
  delta_pct: number | null
  delta_abs: number | null
  /** 「有账或有存续令牌」的分组数，不是「你有几把令牌」 */
  row_count: number
  /** 仅存续令牌 */
  key_count: number
  /** 成行分组里已软删的令牌数，对账条的「含 N 把已删除令牌」用它 */
  deleted_key_count: number
  owned_key_count: number
  /** 占比条分母。与行金额有最多 1 分的口径差，算出的百分比必须 clamp 到 100 */
  top_row_cost: number
  retention_warning: TeamRetentionWarning | null
  conclusion: TeamConclusion | null
}

export interface TeamRow {
  /** 'o:<归属名>' 或 'k:<令牌id>'，前端只当不透明标识用 */
  group_key: string
  display_name: string
  by_owner: boolean
  /** 存续令牌数 */
  key_count: number
  /** 含已软删的历史令牌数 */
  key_count_all: number
  all_deleted: boolean
  /** 仅当组内恰好一把存续令牌时非空。只读展示，明文密钥不经过本接口 */
  masked_key: string | null
  last_used_at: string | null
  /** 展示值（已分配），与 prev_cost 口径不同，两者不要相减 */
  current_cost: number
  /** 原值，未分配 */
  prev_cost: number
  delta_pct: number | null
  delta_abs: number | null
  requests: number
  prev_requests: number
  is_anomaly: boolean
}

export interface TeamRowsResponse {
  items: TeamRow[]
  total: number
  page: number
  page_size: number
  pages: number
}

export type TeamSort = 'cost' | 'delta' | 'name'
export type TeamOrder = 'asc' | 'desc'

export interface TeamQuery {
  start_date?: string
  end_date?: string
  timezone?: string
  sort?: TeamSort
  order?: TeamOrder
  page?: number
  page_size?: number
  q?: string
}
