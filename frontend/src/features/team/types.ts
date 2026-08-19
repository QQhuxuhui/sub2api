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
  /** 未取整原值；不直接渲染，但平均单次等派生指标必须用它，避免分位取整放大误差 */
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
  /** 本期请求数（仓储层一直在算，加厚版才下发） */
  requests: number
  /** 上期请求数 */
  prev_requests: number
  /** current_cost > 0 的分组数 —— 概览条「活跃成员 6/8」的分子，分母是 row_count */
  active_row_count: number
  /**
   * 缓存命中率，token 口径 0~100：100 * cache_read / (input + cache_creation + cache_read)。
   * 分母为 0 时后端给 null（不是 0）——「没有输入 token」与「命中率 0%」是两回事，
   * 前端必须把 null 渲染成「—」。
   */
  cache_hit_rate: number | null
  /**
   * 缓存节省金额，**近似值**：按同一行的输入单价折算缓存读 token，再应用用户计费倍率。
   * 可能为负（缓存读价高于输入价的畸形定价），后端不 clamp，前端照实显示 ——
   * 抹平负值等于把定价配错的分组伪装成正常。
   */
  cache_saved_cost: number
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
  /** current_cost / requests；requests == 0 时后端给 0（不是 null） */
  avg_cost: number
  /** 本期该分组花费最高的模型；无消耗时 null —— 渲染「—」，不要渲染 "null" */
  top_model: string | null
  /** 该模型占本行花费的百分比 0~100；top_model 为 null 时也为 null */
  top_model_share: number | null
  /** 分组级缓存命中率，口径同 TeamSummary.cache_hit_rate，null 渲染「—」 */
  cache_hit_rate: number | null
  /**
   * 本期逐自然日消耗，长度 == 本期自然日数、缺的日子补 0（靠下标对齐日期）。
   * EnrichRows 降级时是空数组 —— 空数组表示「没这份数据」，必须不画线、留空；
   * 画一条平的假线会让「没数据」看起来像「一直是 0」。
   */
  daily: number[]
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
