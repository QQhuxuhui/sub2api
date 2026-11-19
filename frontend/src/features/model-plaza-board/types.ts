/** 模型广场健康接口（GET /model-plaza/health）的类型。结构是白名单，不含任何请求数。 */

export type PlazaHealthState = 'healthy' | 'warning' | 'critical' | 'unknown'

/** 一个有调用的小时：t = 小时起点（Unix 秒），e = 该小时失败占比（0~1）。没调用的小时不出现。 */
export interface PlazaHealthSample {
  t: number
  e: number
}

/** 一条线路（平台 + 分组 + 模型）最近若干个有调用小时的健康摘要。 */
export interface PlazaHealthLine {
  platform: string
  group_id: number
  model: string
  /** 按 samples 合计的成功率判定：≥90% healthy，≥70% warning，其余 critical。 */
  state: Exclude<PlazaHealthState, 'unknown'>
  /** samples 合计的失败占比（0~1）。 */
  error_rate: number
  /** samples 内的平均首字耗时；没有首字样本为 null。 */
  ttft_ms: number | null
  /** 近 24 小时热度名次（1 起）；近 24 小时无调用为 null。只有名次，没有数量。 */
  rank: number | null
  /** 最多 24 个，按时间升序；最远回看 7 天。 */
  samples: PlazaHealthSample[]
}

export interface PlazaHealthResponse {
  /** false：监控未启用或不是被动聚合模式，页面只展示价格。 */
  available: boolean
  lines: PlazaHealthLine[]
}
