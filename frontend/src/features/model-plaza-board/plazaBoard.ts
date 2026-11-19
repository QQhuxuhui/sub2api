/**
 * 模型广场看板的纯逻辑：把「分组 → 模型」的价目数据转成「模型 → 线路」，
 * 合并健康数据，并提供摘要价、推荐线路、筛选与排序。
 *
 * 摘要价的倍率与取价规则是 components/modelPlaza/PlazaModelPricingTable.vue 的等价实现
 * （那个组件不改）；__tests__/plazaBoard.pricing.spec.ts 用同一批数据对比两者的输出。
 */
import type { ModelPlazaGroup, PlazaModel } from '@/api/modelPlaza'
import type { UserPricingInterval } from '@/api/channels'
import {
  BILLING_MODE_IMAGE,
  BILLING_MODE_TOKEN,
  BILLING_MODE_VIDEO,
  type BillingMode
} from '@/constants/channel'
import { formatScaled, resolveIntervalPrices } from '@/utils/pricing'
import type { PlazaHealthLine, PlazaHealthResponse, PlazaHealthState } from './types'

const PER_MILLION = 1_000_000
const MIN_DECIMALS = 2

export type BoardBilling = 'token' | 'request' | 'none'

export interface PriceSummary {
  billing: BoardBilling
  billingMode: BillingMode
  /** token 计费：每百万 token 的实付价文本。 */
  input: string | null
  output: string | null
  cacheRead: string | null
  /** 按次 / 按图 / 视频：单次实付价文本。 */
  request: string | null
  /** 多档按次价时为 true，摘要取其中最低档。 */
  from: boolean
  /** 摘要所取档位的标签（如 1K）。 */
  tierLabel: string | null
  /** 用于比较的数值：token 取输入价，按次取单次价；无价为 null。 */
  amount: number | null
}

export interface BoardLine {
  key: string
  group: ModelPlazaGroup
  model: PlazaModel
  price: PriceSummary
  /** 生效倍率（按次模型为请求倍率）。 */
  rate: number
  health: PlazaHealthLine | null
  state: PlazaHealthState
}

export interface BoardVendor {
  key: string
  label: string
}

export interface BoardModel {
  name: string
  vendor: BoardVendor
  lines: BoardLine[]
  recommended: BoardLine
  state: PlazaHealthState
  billing: BoardBilling
  rank: number | null
}

export type BoardSort = 'rank' | 'name' | 'price' | 'stable' | 'fast'

export interface BoardFilter {
  query: string
  /** 分组 ID；'all' 为全部分组。分组裁剪发生在建模之前（scopeGroups），不在 filterAndSortModels 里。 */
  group: 'all' | number
  vendor: string
  billing: 'all' | 'token' | 'request'
  state: 'all' | Exclude<PlazaHealthState, 'unknown'>
  sort: BoardSort
}

export function billingModeOf(model: PlazaModel): BillingMode {
  return (model.pricing?.billing_mode || BILLING_MODE_TOKEN) as BillingMode
}

/** 生效倍率 = 用户专属倍率 ?? 分组默认倍率。 */
export function effectiveRate(group: ModelPlazaGroup): number {
  return group.user_rate_multiplier ?? group.rate_multiplier
}

/** 非 token 模型的生效倍率：图片 / 视频可由各自的独立倍率覆盖。 */
export function requestRate(model: PlazaModel, group: ModelPlazaGroup): number {
  const mode = billingModeOf(model)
  if (mode === BILLING_MODE_VIDEO && group.video_rate_independent === true) {
    return Math.max(0, group.video_rate_multiplier ?? 1)
  }
  if (mode === BILLING_MODE_IMAGE && group.image_rate_independent === true) {
    return group.image_rate_multiplier ?? 1
  }
  return effectiveRate(group)
}

function tokenIntervals(model: PlazaModel): UserPricingInterval[] {
  const pricing = model.pricing
  if (!pricing) return []
  return (pricing.intervals ?? []).map((iv) => resolveIntervalPrices(iv, pricing))
}

function requestIntervals(model: PlazaModel): UserPricingInterval[] {
  return (model.pricing?.intervals ?? []).filter((iv) => iv.per_request_price != null)
}

const EMPTY_PRICE = { input: null, output: null, cacheRead: null, request: null, from: false, tierLabel: null }

/** 一条线路的摘要价。卡片、列表、排序、推荐线路、「最便宜」标签都用这一个结果。 */
export function summarizePrice(model: PlazaModel, group: ModelPlazaGroup): PriceSummary {
  const billingMode = billingModeOf(model)
  const pricing = model.pricing
  if (!pricing) {
    return { ...EMPTY_PRICE, billing: 'none', billingMode, amount: null }
  }

  if (billingMode === BILLING_MODE_TOKEN) {
    const rate = effectiveRate(group)
    const base = tokenIntervals(model)[0] ?? pricing
    const scaled = (value: number | null | undefined) => (value == null ? null : value * rate)
    const text = (value: number | null) => (value == null ? null : formatScaled(value, PER_MILLION, MIN_DECIMALS))
    const input = scaled(base.input_price)
    const output = scaled(base.output_price)
    const cacheRead = scaled(base.cache_read_price)
    if (input == null && output == null) {
      return { ...EMPTY_PRICE, billing: 'none', billingMode, amount: null }
    }
    return {
      ...EMPTY_PRICE,
      billing: 'token',
      billingMode,
      input: text(input),
      output: text(output),
      cacheRead: text(cacheRead),
      amount: input == null ? null : input * PER_MILLION
    }
  }

  const rate = requestRate(model, group)
  const tiers = requestIntervals(model)
  let price: number | null = null
  let tierLabel: string | null = null
  if (tiers.length) {
    const cheapest = tiers.reduce((best, iv) =>
      (iv.per_request_price as number) < (best.per_request_price as number) ? iv : best
    )
    price = cheapest.per_request_price
    tierLabel = cheapest.tier_label ?? null
  } else if (pricing.per_request_price != null) {
    price = pricing.per_request_price
  }
  if (price == null) {
    return { ...EMPTY_PRICE, billing: 'none', billingMode, amount: null }
  }
  const paid = price * rate
  return {
    ...EMPTY_PRICE,
    billing: 'request',
    billingMode,
    request: formatScaled(paid, 1, MIN_DECIMALS),
    from: tiers.length > 1,
    tierLabel,
    amount: paid
  }
}

/** 官方参考价（不乘倍率），仅 token 计费有。 */
export function officialPriceText(model: PlazaModel): { input: string; output: string } | null {
  const official = model.official_pricing
  if (!official || (official.input_price == null && official.output_price == null)) return null
  return {
    input: formatScaled(official.input_price, PER_MILLION, MIN_DECIMALS),
    output: formatScaled(official.output_price, PER_MILLION, MIN_DECIMALS)
  }
}

const VENDORS: Array<{ key: string; label: string; pattern: RegExp }> = [
  { key: 'openai', label: 'OpenAI', pattern: /^(gpt|chatgpt|o\d|codex|dall-e|text-embedding|whisper|tts-|sora)/i },
  { key: 'anthropic', label: 'Anthropic', pattern: /^claude/i },
  { key: 'google', label: 'Google', pattern: /^(gemini|imagen|veo|gemma)/i },
  { key: 'xai', label: 'xAI', pattern: /^grok/i },
  { key: 'deepseek', label: 'DeepSeek', pattern: /^deepseek/i },
  { key: 'zhipu', label: 'Zhipu', pattern: /^glm/i },
  { key: 'moonshot', label: 'Moonshot', pattern: /^(kimi|moonshot)/i },
  { key: 'minimax', label: 'MiniMax', pattern: /^(minimax|abab)/i },
  { key: 'qwen', label: 'Qwen', pattern: /^(qwen|qwq)/i }
]

export const OTHER_VENDOR_KEY = 'other'

/** 按模型名前缀归厂商，只用于筛选分组；认不出的归「其他」（label 为空，由界面给文案）。 */
export function vendorOf(modelName: string): BoardVendor {
  const hit = VENDORS.find((v) => v.pattern.test(modelName.trim()))
  return hit ? { key: hit.key, label: hit.label } : { key: OTHER_VENDOR_KEY, label: '' }
}

function healthKey(platform: string, groupId: number, model: string): string {
  return `${platform}\u0000${groupId}\u0000${model}`
}

/** 健康数据索引：平台 + 分组 + 模型三者精确匹配，不做任何兜底。 */
export function buildHealthIndex(health: PlazaHealthResponse | null | undefined): Map<string, PlazaHealthLine> {
  const index = new Map<string, PlazaHealthLine>()
  if (!health?.available) return index
  for (const line of health.lines ?? []) {
    index.set(healthKey(line.platform, line.group_id, line.model), line)
  }
  return index
}

const STATE_ORDER: Record<PlazaHealthState, number> = { healthy: 0, warning: 1, critical: 2, unknown: 3 }
const BILLING_ORDER: Record<BoardBilling, number> = { token: 0, request: 1, none: 2 }

/** 摘要价比较：先按计费方式（按量 < 按次 < 待配置），同方式按金额升序，无价排后。 */
export function comparePrice(a: PriceSummary, b: PriceSummary): number {
  if (a.billing !== b.billing) return BILLING_ORDER[a.billing] - BILLING_ORDER[b.billing]
  if (a.amount == null || b.amount == null) return (a.amount == null ? 1 : 0) - (b.amount == null ? 1 : 0)
  return a.amount - b.amount
}

/**
 * 推荐线路：状态正常的线路里摘要价最低者；没有正常线路（或没有健康数据）时，
 * 取状态最好、其次摘要价最低者。
 */
export function pickRecommendedLine(lines: BoardLine[]): BoardLine {
  return [...lines].sort(
    (a, b) => STATE_ORDER[a.state] - STATE_ORDER[b.state] || comparePrice(a.price, b.price)
  )[0]
}

/** 按分组筛选裁剪广场数据：选中某个分组后，看板的线路、价格、状态与统计都只来自它。 */
export function scopeGroups(
  groups: ModelPlazaGroup[] | null | undefined,
  group: BoardFilter['group']
): ModelPlazaGroup[] {
  const list = groups ?? []
  return group === 'all' ? list : list.filter((g) => g.id === group)
}

/** 把广场分组数据转成以模型为中心的看板数据。 */
export function buildBoardModels(
  groups: ModelPlazaGroup[] | null | undefined,
  health: PlazaHealthResponse | null | undefined
): BoardModel[] {
  const index = buildHealthIndex(health)
  const byName = new Map<string, BoardLine[]>()
  for (const group of groups ?? []) {
    for (const model of group.models ?? []) {
      const lineHealth = index.get(healthKey(model.platform, group.id, model.name)) ?? null
      const line: BoardLine = {
        key: `${group.id}:${model.platform}:${model.name}`,
        group,
        model,
        price: summarizePrice(model, group),
        rate: billingModeOf(model) === BILLING_MODE_TOKEN ? effectiveRate(group) : requestRate(model, group),
        health: lineHealth,
        state: lineHealth?.state ?? 'unknown'
      }
      const list = byName.get(model.name)
      if (list) list.push(line)
      else byName.set(model.name, [line])
    }
  }
  const models: BoardModel[] = []
  for (const [name, lines] of byName) {
    const recommended = pickRecommendedLine(lines)
    const ranks = lines.map((l) => l.health?.rank).filter((r): r is number => r != null)
    models.push({
      name,
      vendor: vendorOf(name),
      lines: [...lines].sort(
        (a, b) => STATE_ORDER[a.state] - STATE_ORDER[b.state] || comparePrice(a.price, b.price)
      ),
      recommended,
      state: recommended.state,
      billing: recommended.price.billing,
      rank: ranks.length ? Math.min(...ranks) : null
    })
  }
  return models
}

/** 是否有任何一条线路带健康数据（决定是否展示状态相关的界面）。 */
export function hasHealthData(models: BoardModel[]): boolean {
  return models.some((m) => m.lines.some((l) => l.health != null))
}

/** 是否有热度名次（决定「最多人用」排序是否可选）。 */
export function hasRank(models: BoardModel[]): boolean {
  return models.some((m) => m.rank != null)
}

function nullsLast(a: number | null, b: number | null): number {
  if (a == null || b == null) return (a == null ? 1 : 0) - (b == null ? 1 : 0)
  return a - b
}

/** 可比较的错误率：没有健康数据（近期无调用）的线路不参与比较。 */
function comparableErrorRate(line: BoardLine): number | null {
  return line.health?.error_rate ?? null
}

export function filterAndSortModels(models: BoardModel[], filter: BoardFilter): BoardModel[] {
  const query = filter.query.trim().toLowerCase()
  const list = models.filter(
    (m) =>
      (!query || m.name.toLowerCase().includes(query)) &&
      (filter.vendor === 'all' || m.vendor.key === filter.vendor) &&
      (filter.billing === 'all' || m.billing === filter.billing) &&
      (filter.state === 'all' || m.state === filter.state)
  )
  const byName = (a: BoardModel, b: BoardModel) => a.name.localeCompare(b.name)
  const comparators: Record<BoardSort, (a: BoardModel, b: BoardModel) => number> = {
    name: byName,
    rank: (a, b) => nullsLast(a.rank, b.rank) || byName(a, b),
    price: (a, b) => comparePrice(a.recommended.price, b.recommended.price) || byName(a, b),
    stable: (a, b) =>
      nullsLast(comparableErrorRate(a.recommended), comparableErrorRate(b.recommended)) || byName(a, b),
    fast: (a, b) =>
      nullsLast(a.recommended.health?.ttft_ms ?? null, b.recommended.health?.ttft_ms ?? null) ||
      byName(a, b)
  }
  return list.sort(comparators[filter.sort])
}

export interface LineBadges {
  cheapest: boolean
  steadiest: boolean
  fastest: boolean
}

/** 抽屉里的线路标签。只有多条线路时才颁发；「最稳」「最快」只在有健康数据的线路中评选。 */
export function lineBadges(lines: BoardLine[]): Map<string, LineBadges> {
  const badges = new Map<string, LineBadges>()
  for (const line of lines) badges.set(line.key, { cheapest: false, steadiest: false, fastest: false })
  if (lines.length < 2) return badges

  const priced = lines.filter((l) => l.price.amount != null)
  if (priced.length) {
    const cheapest = [...priced].sort((a, b) => comparePrice(a.price, b.price))[0]
    badges.get(cheapest.key)!.cheapest = true
  }
  const known = lines.filter((l) => l.health != null)
  if (known.length) {
    const steadiest = [...known].sort((a, b) => a.health!.error_rate - b.health!.error_rate)[0]
    badges.get(steadiest.key)!.steadiest = true
  }
  const timed = known.filter((l) => l.health?.ttft_ms != null)
  if (timed.length) {
    const fastest = [...timed].sort((a, b) => a.health!.ttft_ms! - b.health!.ttft_ms!)[0]
    badges.get(fastest.key)!.fastest = true
  }
  return badges
}

/** 成功率文本：全部成功为 100%，否则保留两位小数且不进位成 100；null 为「—」。 */
export function formatSuccessRate(errorRate: number | null | undefined): string {
  if (errorRate == null) return '—'
  if (errorRate <= 0) return '100%'
  return `${Math.min(99.99, Math.max(0, (1 - errorRate) * 100)).toFixed(2)}%`
}

/** 线路的可用率文本；没有健康数据（近期无调用）为「—」。 */
export function formatAvailability(line: BoardLine | null | undefined): string {
  return formatSuccessRate(line?.health?.error_rate)
}

/** 首字耗时文本：毫秒换算为秒，一位小数；无数据为「—」。 */
export function formatTtft(ms: number | null | undefined): string {
  return ms == null ? '—' : `${(ms / 1000).toFixed(1)}s`
}

export type SampleLevel = 'excellent' | 'good' | 'warning' | 'critical'

/** 一个采样的成功率档位：100% / ≥90% / ≥70% / 其余。 */
export function sampleLevel(errorRate: number): SampleLevel {
  if (errorRate <= 0) return 'excellent'
  if (errorRate <= 0.1) return 'good'
  if (errorRate <= 0.3) return 'warning'
  return 'critical'
}

const pad2 = (value: number) => String(value).padStart(2, '0')

/** 采样的小时文本（本地时区）：当天只写「HH:00」，其它日期带「M/D」。 */
export function formatSampleHour(unixSeconds: number, now: number = Date.now()): string {
  const at = new Date(unixSeconds * 1000)
  const today = new Date(now)
  const sameDay =
    at.getFullYear() === today.getFullYear() && at.getMonth() === today.getMonth() && at.getDate() === today.getDate()
  const hour = `${pad2(at.getHours())}:00`
  return sameDay ? hour : `${at.getMonth() + 1}/${at.getDate()} ${hour}`
}

const HOUR_MS = 3_600_000

/** 距最近一次有调用的小时结束已过去多少整小时；该小时仍在进行或刚结束为 0，没有采样为 null。 */
export function hoursSinceLastSample(health: PlazaHealthLine | null | undefined, now: number = Date.now()): number | null {
  const last = health?.samples?.[health.samples.length - 1]
  if (!last) return null
  return Math.max(0, Math.floor((now - (last.t * 1000 + HOUR_MS)) / HOUR_MS))
}

/**
 * 各分组的整体状态（分组栏的指示灯）：有健康数据的线路全部正常为 healthy，
 * 半数及以上异常为 critical，其余为 warning；没有任何健康数据为 unknown。
 */
export function groupStates(
  groups: ModelPlazaGroup[] | null | undefined,
  health: PlazaHealthResponse | null | undefined
): Map<number, PlazaHealthState> {
  const index = buildHealthIndex(health)
  const states = new Map<number, PlazaHealthState>()
  for (const group of groups ?? []) {
    let known = 0
    let healthy = 0
    let critical = 0
    for (const model of group.models ?? []) {
      const line = index.get(healthKey(model.platform, group.id, model.name))
      if (!line) continue
      known += 1
      if (line.state === 'healthy') healthy += 1
      else if (line.state === 'critical') critical += 1
    }
    states.set(
      group.id,
      known === 0 ? 'unknown' : healthy === known ? 'healthy' : critical * 2 >= known ? 'critical' : 'warning'
    )
  }
  return states
}
