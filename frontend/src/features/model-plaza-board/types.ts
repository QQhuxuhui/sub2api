/** 模型广场健康接口（GET /model-plaza/health）的类型。结构是白名单，不含任何请求数。 */

export type PlazaHealthState = 'healthy' | 'warning' | 'critical' | 'unknown'

/** 一个 20 分钟统计格：s = 状态（只按错误率判定），e = 错误率（状态未知时为 null）。 */
export interface PlazaHealthBucket {
  s: PlazaHealthState
  e: number | null
}

/** 一条线路（平台 + 分组 + 模型）近 24 小时的健康摘要。 */
export interface PlazaHealthLine {
  platform: string
  group_id: number
  model: string
  state: PlazaHealthState
  error_rate: number | null
  ttft_p50_ms: number | null
  /** 热度名次（1 起）；无流量为 null。只有名次，没有数量。 */
  rank: number | null
  buckets: PlazaHealthBucket[]
}

export interface PlazaHealthWindow {
  start: string
  end: string
  bucket_seconds: number
}

export interface PlazaHealthResponse {
  /** false：监控未启用或不是被动聚合模式，页面只展示价格。 */
  available: boolean
  window?: PlazaHealthWindow
  lines: PlazaHealthLine[]
}
