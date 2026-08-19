// 本页刻意不用 utils/format.ts 的 formatCurrency：它在中文 locale 下会输出 "US$"，
// 且小额时自动切到 6 位小数，与看板「统一两位小数 + $ 前缀」的口径不一致。

/** 金额。零值渲染成「—」而不是 $0.00 —— $0.00 看起来像一个算出来的结果，「—」明确表示本期没有活动。 */
export function formatTeamMoney(v: number | null | undefined, opts?: { zeroAsDash?: boolean }): string {
  if (v === null || v === undefined || Number.isNaN(v)) return '—'
  if (opts?.zeroAsDash !== false && v === 0) return '—'
  return '$' + v.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
}

/**
 * 环比。null 一律渲染「—」：后端在「上期展示值取整后 <= 0」或「上期不可比」时返回 null，
 * 那是刻意认输 —— 界面上的分母就是 0.00，此时百分比无法被用户验证。
 * 前端**不要**用 current/prev 自己算来"补上"，那会算出 +24900% 这种没法解释的数。
 */
export function formatTeamDelta(pct: number | null | undefined): string {
  if (pct === null || pct === undefined || Number.isNaN(pct)) return '—'
  const s = pct > 0 ? '+' : ''
  return `${s}${pct.toFixed(1)}%`
}

export function formatTeamCount(v: number | null | undefined): string {
  if (v === null || v === undefined) return '—'
  return v.toLocaleString('en-US')
}

/**
 * 占比条宽度。top_row_cost 取的是 roundCents(原值)，而行金额走最大余数法分配，
 * 第一名被摊掉一分时算出来会是 100.25% —— 必须 clamp，不要去后端改口径。
 */
export function shareBarPercent(cost: number, topCost: number): number {
  if (!topCost || topCost <= 0) return 0
  return Math.min(100, Math.max(0, (cost / topCost) * 100))
}

/**
 * 平均单次消耗。这个数天生比行金额小两三个数量级（$0.0037 这种），
 * 用两位小数会把一整列压成 $0.00，所以单独用 4 位小数。
 * 0 视为「本期没有请求」渲染「—」；正数但不足 4 位小数能表达的，退化成 "<$0.0001"
 * 而不是 "$0.0000" —— 后者看起来像是算出来的零。
 */
export function formatTeamAvgCost(v: number | null | undefined): string {
  if (v === null || v === undefined || Number.isNaN(v) || v === 0) return '—'
  if (v > 0 && v < 0.0001) return '<$0.0001'
  return '$' + v.toLocaleString('en-US', { minimumFractionDigits: 4, maximumFractionDigits: 4 })
}

/**
 * 缓存节省额。后端刻意不 clamp 负值（缓存读单价高于输入单价的畸形定价会算出负数），
 * 前端照实显示，负号放在 $ 前面：`-$0.42` 而不是 `$-0.42`。不足一分时保留
 * 4 位小数，避免把真实的正负小额渲染成 `$0.00` / `-$0.00`。
 */
export function formatTeamSavedCost(v: number | null | undefined): string {
  if (v === null || v === undefined || Number.isNaN(v) || v === 0) return '—'
  const digits = Math.abs(v) < 0.01 ? 4 : 2
  const body = Math.abs(v).toLocaleString('en-US', { minimumFractionDigits: digits, maximumFractionDigits: digits })
  return (v < 0 ? '-$' : '$') + body
}

/**
 * 百分比（缓存命中率 / 主力模型占比）。
 * null 一律「—」：后端在分母为 0 时返回 null，那表示「没有输入 token」，
 * 与「命中率 0%」是两回事，0 要照样渲染成 0.0%。
 */
export function formatTeamPercent(v: number | null | undefined, digits = 1): string {
  if (v === null || v === undefined || Number.isNaN(v)) return '—'
  return v.toFixed(digits) + '%'
}

/**
 * 后端直接给的百分比（top_model_share）转占比条宽度。
 * 仍然要 clamp：后端的分母与展示金额有取整口径差，理论上能算出 100.x%。
 */
export function percentBarWidth(pct: number | null | undefined): number {
  if (pct === null || pct === undefined || Number.isNaN(pct) || pct <= 0) return 0
  return Math.min(100, pct)
}

/**
 * 全局平均单次 = total_cost_raw / requests。展示总额已经按分位取整，不能拿来做派生指标。
 * requests 为 0 时返回 null 而不是 0 —— 0 会让「标黄」判定拿 0 当基准，整列全标黄。
 */
export function globalAvgCost(totalCost: number | null | undefined, requests: number | null | undefined): number | null {
  if (!requests || requests <= 0) return null
  if (totalCost === null || totalCost === undefined || Number.isNaN(totalCost)) return null
  return totalCost / requests
}

/** 标黄阈值：全局平均的 1.5 倍。恰好等于不算异常（严格大于），避免边界抖动误报。 */
export const AVG_COST_HOT_RATIO = 1.5

/**
 * 平均单次显著高于全局 → 标黄，这是「在烧长上下文」的信号。
 *
 * `global <= 0` 那条判定不是冗余保护：免费模型会出现 requests > 0、
 * total_cost_raw === 0。少了这条判定就是 `rowAvg > 0 * 1.5`，对任何正数恒成立，
 * 会让整列全部标黄并挂上错误的长上下文提示。
 */
export function isAvgCostHot(rowAvg: number | null | undefined, global: number | null | undefined): boolean {
  if (rowAvg === null || rowAvg === undefined || Number.isNaN(rowAvg) || rowAvg <= 0) return false
  if (global === null || global === undefined || Number.isNaN(global) || global <= 0) return false
  return rowAvg > global * AVG_COST_HOT_RATIO
}

/**
 * 本期自然日数（含首尾），日均请求数的分母。
 * 用 UTC 解析纯日期串，避开本地时区把 '2026-08-01' 解析成前一天 16:00 的坑。
 */
export function periodDayCount(start: string | null | undefined, end: string | null | undefined): number {
  if (!start || !end) return 0
  const s = Date.parse(start + 'T00:00:00Z')
  const e = Date.parse(end + 'T00:00:00Z')
  if (Number.isNaN(s) || Number.isNaN(e) || e < s) return 0
  return Math.round((e - s) / 86400000) + 1
}

/** 日均请求数。天数为 0（区间非法）时返回 null，让调用方渲染「—」而不是 Infinity。 */
export function requestsPerDay(requests: number | null | undefined, days: number): number | null {
  if (!days || days <= 0) return null
  if (requests === null || requests === undefined || Number.isNaN(requests)) return null
  return requests / days
}

/** 日均值展示：小于 100 时保留一位小数（0.6 次/天 取整会变成 0），大于等于 100 时取整。 */
export function formatTeamRate(v: number | null | undefined): string {
  if (v === null || v === undefined || Number.isNaN(v)) return '—'
  if (v >= 100) return Math.round(v).toLocaleString('en-US')
  return v.toFixed(1)
}

/**
 * 最后使用时间（本地 YYYY-MM-DD HH:mm）。
 * 返回 null 表示「从未使用」，由调用方渲染 t('team.neverUsed') —— 这里不掺 i18n。
 */
export function formatTeamLastUsed(iso: string | null | undefined): string | null {
  if (!iso) return null
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return null
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

/** 本地日期（YYYY-MM-DD），用于默认区间。用本地而非 UTC，与用户看到的「今天」一致。 */
export function localDate(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

/** 默认区间：本月 1 日至今（与 KeysView 的滚动 30 天天生不同，这不是 bug）。 */
export function defaultTeamRange(now = new Date()): { start_date: string; end_date: string } {
  return {
    start_date: localDate(new Date(now.getFullYear(), now.getMonth(), 1)),
    end_date: localDate(now),
  }
}
