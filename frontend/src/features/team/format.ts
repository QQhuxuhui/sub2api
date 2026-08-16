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
