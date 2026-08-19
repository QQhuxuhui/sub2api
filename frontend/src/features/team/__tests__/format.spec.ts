import { describe, expect, it } from 'vitest'
import {
  AVG_COST_HOT_RATIO,
  formatTeamAvgCost,
  formatTeamLastUsed,
  formatTeamPercent,
  formatTeamRate,
  formatTeamSavedCost,
  globalAvgCost,
  isAvgCostHot,
  percentBarWidth,
  periodDayCount,
  requestsPerDay,
} from '../format'

describe('formatTeamAvgCost', () => {
  it('用 4 位小数，两位小数会把整列压成 $0.00', () => {
    // 0.0372 在两位小数下是 $0.04、在三位下是 $0.037，只有 4 位能还原
    expect(formatTeamAvgCost(0.037234)).toBe('$0.0372')
  })

  it('0 渲染「—」而不是 $0.0000', () => {
    expect(formatTeamAvgCost(0)).toBe('—')
  })

  it('正数但不足 4 位小数时退化成 <$0.0001，不冒充算出来的零', () => {
    expect(formatTeamAvgCost(0.00002)).toBe('<$0.0001')
  })

  it('null / undefined 渲染「—」', () => {
    expect(formatTeamAvgCost(null)).toBe('—')
    expect(formatTeamAvgCost(undefined)).toBe('—')
  })
})

describe('formatTeamSavedCost', () => {
  it('负值照实显示，负号在 $ 前面（缓存读价高于输入价的畸形定价）', () => {
    // 若实现把负值 clamp 到 0 会得到「—」，若忘了挪负号会得到 "$-0.42"
    expect(formatTeamSavedCost(-0.42)).toBe('-$0.42')
  })

  it('正值带千分位与两位小数', () => {
    expect(formatTeamSavedCost(1234.5)).toBe('$1,234.50')
  })

  it('不足一分时保留四位小数，不显示成正负 $0.00', () => {
    expect(formatTeamSavedCost(0.004)).toBe('$0.0040')
    expect(formatTeamSavedCost(-0.004)).toBe('-$0.0040')
  })

  it('0 与 null 渲染「—」', () => {
    expect(formatTeamSavedCost(0)).toBe('—')
    expect(formatTeamSavedCost(null)).toBe('—')
  })
})

describe('formatTeamPercent', () => {
  it('null 是「没有输入 token」，渲染「—」', () => {
    expect(formatTeamPercent(null)).toBe('—')
  })

  it('0 是真实的 0% 命中，照样渲染成 0.0% —— 与 null 必须区分开', () => {
    expect(formatTeamPercent(0)).toBe('0.0%')
  })

  it('保留一位小数', () => {
    expect(formatTeamPercent(62.44)).toBe('62.4%')
  })
})

describe('percentBarWidth', () => {
  it('超过 100 要 clamp（后端分母与展示金额有取整口径差）', () => {
    expect(percentBarWidth(100.25)).toBe(100)
  })

  it('正常值原样透出', () => {
    expect(percentBarWidth(62.5)).toBe(62.5)
  })

  it('null / 负数当 0 处理', () => {
    expect(percentBarWidth(null)).toBe(0)
    expect(percentBarWidth(-3)).toBe(0)
  })
})

describe('globalAvgCost', () => {
  it('总消耗 / 总请求数', () => {
    expect(globalAvgCost(4.8, 150)).toBeCloseTo(0.032, 10)
  })

  it('请求数为 0 时是 null，不是 0（0 当基准会让整列标黄）', () => {
    expect(globalAvgCost(4.8, 0)).toBeNull()
  })
})

describe('isAvgCostHot', () => {
  const global = 0.02

  it('明显高于全局（1.6 倍）标黄', () => {
    expect(isAvgCostHot(0.032, global)).toBe(true)
  })

  it('略高于全局（1.4 倍）不标黄', () => {
    expect(isAvgCostHot(0.028, global)).toBe(false)
  })

  it('阈值就是 1.5 倍，且恰好等于不算异常', () => {
    expect(AVG_COST_HOT_RATIO).toBe(1.5)
    expect(isAvgCostHot(global * AVG_COST_HOT_RATIO, global)).toBe(false)
  })

  it('全局为 null（本期零请求）时任何行都不标黄', () => {
    expect(isAvgCostHot(0.9, null)).toBe(false)
  })
})

describe('periodDayCount', () => {
  it('含首尾计数：08-01 到 08-10 是 10 天', () => {
    // 忘了 +1 会得到 9，日均因此偏大
    expect(periodDayCount('2026-08-01', '2026-08-10')).toBe(10)
  })

  it('跨夏令时切换的月份也按自然日算（UTC 解析，不受本地时区影响）', () => {
    expect(periodDayCount('2026-03-01', '2026-03-31')).toBe(31)
  })

  it('终点早于起点 / 缺参数时为 0', () => {
    expect(periodDayCount('2026-08-10', '2026-08-01')).toBe(0)
    expect(periodDayCount(null, '2026-08-01')).toBe(0)
  })
})

describe('requestsPerDay + formatTeamRate', () => {
  it('小于 100 保留一位小数（0.6 次/天 取整会变成 0）', () => {
    expect(formatTeamRate(requestsPerDay(337, 10))).toBe('33.7')
    expect(formatTeamRate(requestsPerDay(6, 10))).toBe('0.6')
  })

  it('大于等于 100 取整并带千分位', () => {
    expect(formatTeamRate(requestsPerDay(12345, 10))).toBe('1,235')
  })

  it('天数为 0 时是 null → 渲染「—」而不是 Infinity', () => {
    expect(requestsPerDay(100, 0)).toBeNull()
    expect(formatTeamRate(null)).toBe('—')
  })
})

describe('formatTeamLastUsed', () => {
  it('输出本地 YYYY-MM-DD HH:mm，不是原始 ISO 串', () => {
    const s = formatTeamLastUsed('2026-08-14T09:35:00Z')
    expect(s).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/)
    expect(s).not.toContain('T')
    expect(s).not.toContain('Z')
  })

  it('分钟位真的来自时间戳（相差 25 分钟的两个时刻不能格式化成同一串）', () => {
    const a = formatTeamLastUsed('2026-08-14T09:35:00Z')
    const b = formatTeamLastUsed('2026-08-14T10:00:00Z')
    expect(a).not.toBe(b)
  })

  it('null / 非法串返回 null，交给调用方渲染「从未使用」', () => {
    expect(formatTeamLastUsed(null)).toBeNull()
    expect(formatTeamLastUsed('not-a-date')).toBeNull()
  })
})

// isAvgCostHot 的 `global <= 0` 分支在生产可达：globalAvgCost 只在 requests<=0 时返回 null，
// 而「requests>0 但 total_cost_raw 为 0」在免费模型上是真实形态。
// 此时 global === 0，少了那条判定就是 rowAvg > 0*1.5，对任何正数恒成立 —— 整列误标黄。
//
// 复核实测：把 `global <= 0` 改成 `global < 0` 时，其余既有断言不会变红。
describe('isAvgCostHot 的零基准分支', () => {
  it('全局均价为 0 时一律不标黄（免费模型有请求但无费用）', () => {
    // 断多个量级：只断一个值时，把判定改成 `global < 0` 后依然可能因为选例恰好而不红
    for (const rowAvg of [0.0001, 0.032, 12.5, 1e6]) {
      expect(isAvgCostHot(rowAvg, 0)).toBe(false)
    }
  })

  it('全局均价为 null（requests<=0）时也不标黄', () => {
    expect(isAvgCostHot(0.032, null)).toBe(false)
  })

  it('对照组：全局均价为正时，阈值仍然照常生效', () => {
    // 没有这一组的话，把 isAvgCostHot 整个改成 `return false` 上面两条也全绿
    expect(isAvgCostHot(0.046, 0.03)).toBe(true) // 0.046 > 0.03*1.5=0.045
    expect(isAvgCostHot(0.045, 0.03)).toBe(false) // 恰好等于不算异常（严格大于）
  })

  it('globalAvgCost 在 requests>0、total_cost_raw=0 时返回 0 而不是 null', () => {
    // 这条把上面那个「可达性」前提本身钉住：若它改成返回 null，
    // 上面第一条就退化成走 null 分支，测不到 <= 0 那条判定了
    expect(globalAvgCost(0, 337)).toBe(0)
    expect(globalAvgCost(0, 0)).toBeNull()
  })
})
