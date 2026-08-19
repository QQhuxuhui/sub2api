import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import zh from '@/i18n/locales/zh'
import type { TeamRow, TeamSummary } from '../types'

const { getTeamSummary, listTeamRows } = vi.hoisted(() => ({
  getTeamSummary: vi.fn(),
  listTeamRows: vi.fn(),
}))

vi.mock('@/features/team/api', () => ({ getTeamSummary, listTeamRows }))

vi.mock('@/components/layout/AppLayout.vue', () => ({
  default: defineComponent({ name: 'AppLayout', setup: (_, { slots }) => () => h('div', slots.default?.()) }),
}))
vi.mock('@/components/common/Pagination.vue', () => ({
  default: defineComponent({ name: 'Pagination', setup: () => () => h('div') }),
}))
vi.mock('@/components/common/DateRangePicker.vue', () => ({
  default: defineComponent({ name: 'DateRangePicker', setup: () => () => h('div') }),
}))

// 用真实的中文词条 + 最小插值实现：这样漏加 i18n 键会让断言拿到裸 key 而失败，
// 而不是被一个返回 key 的假 t() 蒙混过去。
const teamMessages = (zh as unknown as Record<string, Record<string, string>>).team
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        const raw = key.startsWith('team.') ? teamMessages[key.slice('team.'.length)] : undefined
        if (typeof raw !== 'string') return key
        return raw.replace(/\{(\w+)\}/g, (_m, k: string) => String(params?.[k] ?? ''))
      },
    }),
  }
})

import TeamView from '@/views/user/TeamView.vue'

const summary: TeamSummary = {
  period: { start_date: '2026-08-01', end_date: '2026-08-10', timezone: 'Asia/Shanghai' },
  compare: { start_date: '2026-07-22', end_date: '2026-07-31', comparable: true, reason: null },
  total_cost: 4.8,
  total_cost_raw: 4.804,
  prev_cost: 4,
  delta_pct: 20,
  delta_abs: 0.8,
  row_count: 4,
  key_count: 6,
  deleted_key_count: 0,
  owned_key_count: 3,
  top_row_cost: 3.2,
  requests: 337,
  prev_requests: 300,
  active_row_count: 3,
  cache_hit_rate: 41.6,
  cache_saved_cost: -0.42,
  retention_warning: null,
  conclusion: null,
}

// 全局平均单次 = 4.8 / 337 = 0.014243…
const rowHot: TeamRow = {
  group_key: 'o:alice',
  display_name: 'alice',
  by_owner: true,
  key_count: 2,
  key_count_all: 2,
  all_deleted: false,
  masked_key: null,
  last_used_at: '2026-08-14T09:35:00Z',
  current_cost: 3.2,
  prev_cost: 2,
  delta_pct: 60,
  delta_abs: 1.2,
  requests: 100,
  prev_requests: 80,
  is_anomaly: true,
  avg_cost: 0.032, // 全局的 2.2 倍 → 标黄
  top_model: 'claude-sonnet-4-5',
  top_model_share: 62.5,
  cache_hit_rate: 0, // 真实的 0% 命中，不是缺数据
  daily: [1, 2, 4],
}

const rowBare: TeamRow = {
  group_key: 'k:9',
  display_name: 'billing-bot',
  by_owner: false,
  key_count: 1,
  key_count_all: 1,
  all_deleted: false,
  masked_key: 'sk-…f00d',
  last_used_at: null,
  current_cost: 1.6,
  prev_cost: 1.6,
  delta_pct: null, // 上期不可比 → 「—」
  delta_abs: null,
  requests: 100,
  prev_requests: 100,
  is_anomaly: false,
  avg_cost: 0.016, // 全局的 1.12 倍 → 不标黄
  top_model: null,
  top_model_share: null,
  cache_hit_rate: null, // 没有输入 token
  daily: [], // EnrichRows 降级 → 不画线
}

async function mountView(rows: TeamRow[] = [rowHot, rowBare], s: TeamSummary = summary) {
  getTeamSummary.mockResolvedValue(s)
  listTeamRows.mockResolvedValue({ items: rows, total: rows.length, page: 1, page_size: 20, pages: 1 })
  const wrapper = mount(TeamView, { global: { stubs: { RouterLink: true } } })
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  getTeamSummary.mockReset()
  listTeamRows.mockReset()
})

describe('TeamView 加厚版骨架', () => {
  it('概览条是 6 格', async () => {
    const w = await mountView()
    const cells = w.get('[data-testid="overview-grid"]').element.children
    expect(cells.length).toBe(6)
  })

  it('表格是 9 列，且列序与契约一致', async () => {
    const w = await mountView()
    const heads = w.findAll('thead th').map((th) => th.text())
    expect(heads.length).toBe(9)
    const labels = [
      teamMessages.colName,
      teamMessages.colCost,
      teamMessages.colDelta,
      teamMessages.colTrend,
      teamMessages.colRequests,
      teamMessages.colAvgCost,
      teamMessages.colTopModel,
      teamMessages.colCacheHit,
      teamMessages.colLastUsed,
    ]
    labels.forEach((label, i) => expect(heads[i]).toContain(label))
    expect(w.findAll('tbody tr')[0].findAll('td').length).toBe(9)
  })

  it('骨架/空态的 colspan 跟着列数走（漏改会让空态只横跨半张表）', async () => {
    getTeamSummary.mockReturnValue(new Promise(() => {}))
    listTeamRows.mockReturnValue(new Promise(() => {}))
    const w = mount(TeamView, { global: { stubs: { RouterLink: true } } })
    await flushPromises()
    expect(w.find('tbody td').attributes('colspan')).toBe(String(w.findAll('thead th').length))
  })
})

describe('TeamView 概览指标', () => {
  it('请求数带日均，分母是本期自然日数（08-01~08-10 共 10 天 → 337/10）', async () => {
    const text = (await mountView()).get('[data-testid="overview-grid"]').text()
    expect(text).toContain('337')
    expect(text).toContain('日均 33.7 次')
  })

  it('本期部分超出保留期时不展示混入残缺自然日的日均', async () => {
    const text = (await mountView([], {
      ...summary,
      retention_warning: { kind: 'period_partial', cut_date: '2026-08-05' },
    })).get('[data-testid="overview-grid"]').text()

    // 请求数可能含切点当天残留的部分数据，前端没有逐日请求数可剔除它，必须认输。
    expect(text).toContain('日均 — 次')
    expect(text).not.toContain('日均 33.7 次')
  })

  it('本期整段早于保留边界时不展示虚假的 0.0 日均', async () => {
    const text = (await mountView([], {
      ...summary,
      requests: 0,
      retention_warning: { kind: 'period_partial', cut_date: '2026-08-11' },
    })).get('[data-testid="overview-grid"]').text()

    expect(text).toContain('日均 — 次')
    expect(text).not.toContain('日均 0.0 次')
  })

  it('平均单次取「未取整总消耗 ÷ 总请求数」并用 4 位小数', async () => {
    const text = (await mountView()).get('[data-testid="overview-grid"]').text()
    expect(text).toContain('$0.0143')
  })

  it('全局平均与行平均都使用未取整金额，亚分总额不会误触发标黄', async () => {
    const w = await mountView(
      [{ ...rowHot, avg_cost: 0.008 }, { ...rowBare, avg_cost: 0.006 }],
      { ...summary, total_cost: 0.01, total_cost_raw: 0.014, requests: 2 },
    )

    expect(w.get('[data-testid="overview-grid"]').text()).toContain('$0.0070')
    expect(w.findAll('tbody tr')[0].findAll('td')[5].classes()).not.toContain('text-amber-600')
  })

  it('缓存节省为负时照实显示，不抹平成 0（那会掩盖配错的定价）', async () => {
    const text = (await mountView()).get('[data-testid="overview-grid"]').text()
    expect(text).toContain('-$0.42')
    expect(text).toContain('命中率 41.6%')
  })

  it('缓存节省那格挂着「近似」的 tooltip', async () => {
    const w = await mountView()
    const cell = w.get('[data-testid="overview-grid"]').element.children[3] as HTMLElement
    expect(cell.getAttribute('title')).toBe(teamMessages.statCacheApprox)
  })

  it('活跃成员显示 active_row_count / row_count', async () => {
    const text = (await mountView()).get('[data-testid="overview-grid"]').text()
    expect(text).toContain('3 / 4 有消耗')
  })

  it('summary.cache_hit_rate 为 null 时渲染「—」而不是 0.0%', async () => {
    const w = await mountView([rowHot], { ...summary, cache_hit_rate: null })
    expect(w.get('[data-testid="overview-grid"]').text()).toContain('命中率 —')
  })
})

describe('TeamView 行内维度', () => {
  it('daily 有数据 → 画 polyline；daily 为空数组 → 整格留空', async () => {
    const w = await mountView()
    const rows = w.findAll('tbody tr')
    expect(rows[0].find('[data-testid="trend-cell"] polyline').exists()).toBe(true)
    expect(rows[1].find('[data-testid="trend-cell"] svg').exists()).toBe(false)
    expect(rows[1].get('[data-testid="trend-cell"]').text()).toBe('')
  })

  it('趋势线的顶点来自这一行的 daily，不是所有行共用一条', async () => {
    const w = await mountView([rowHot, { ...rowBare, daily: [4, 2, 1] }])
    const rows = w.findAll('tbody tr')
    const a = rows[0].get('[data-testid="trend-cell"] polyline').attributes('points')
    const b = rows[1].get('[data-testid="trend-cell"] polyline').attributes('points')
    expect(a).not.toBe(b)
    // 默认 96x24：x = 2/48/94，y = 22-(v/4)*20 → 17/12/2
    expect(a).toBe('2,17 48,12 94,2')
  })

  it('top_model 为 null 渲染「—」，绝不能渲染成 "null"', async () => {
    const w = await mountView()
    const cell = w.findAll('tbody tr')[1].findAll('td')[6]
    expect(cell.text()).toBe('—')
    expect(cell.text()).not.toContain('null')
  })

  it('主力模型带占比条，占比文案与条宽都来自 top_model_share', async () => {
    const w = await mountView()
    const cell = w.findAll('tbody tr')[0].findAll('td')[6]
    expect(cell.text()).toContain('claude-sonnet-4-5')
    expect(cell.text()).toContain('62.5%')
    expect(cell.get('.bg-primary-500').attributes('style')).toContain('width: 62.5%')
  })

  it('占比条百分比 clamp 到 100（后端口径差可能给出 100 以上）', async () => {
    const w = await mountView([{ ...rowHot, top_model_share: 100.4 }])
    const cell = w.findAll('tbody tr')[0].findAll('td')[6]
    expect(cell.get('.bg-primary-500').attributes('style')).toContain('width: 100%')
  })

  it('行级 cache_hit_rate：0 是 0.0%，null 是「—」', async () => {
    const w = await mountView()
    const rows = w.findAll('tbody tr')
    expect(rows[0].findAll('td')[7].text()).toBe('0.0%')
    expect(rows[1].findAll('td')[7].text()).toBe('—')
  })

  it('delta_pct 为 null 渲染「—」，不拿 current/prev 自己算', async () => {
    const w = await mountView()
    expect(w.findAll('tbody tr')[1].findAll('td')[2].text()).toBe('—')
  })

  it('平均单次高于全局 1.5 倍标黄并给出倍数说明，1.12 倍的那行不标', async () => {
    const w = await mountView()
    const rows = w.findAll('tbody tr')
    const hot = rows[0].findAll('td')[5]
    const cold = rows[1].findAll('td')[5]
    expect(hot.classes()).toContain('text-amber-600')
    expect(hot.attributes('title')).toContain('2.2')
    expect(cold.classes()).not.toContain('text-amber-600')
    expect(cold.attributes('title') ?? '').toBe('')
  })

  it('本期零请求时全局平均为 null，任何行都不标黄', async () => {
    const w = await mountView([rowHot], { ...summary, requests: 0, total_cost: 0 })
    expect(w.findAll('tbody tr')[0].findAll('td')[5].classes()).not.toContain('text-amber-600')
  })

  it('最后使用：有值给本地时间串，null 给「从未使用」', async () => {
    const w = await mountView()
    const rows = w.findAll('tbody tr')
    expect(rows[0].findAll('td')[8].text()).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/)
    expect(rows[1].findAll('td')[8].text()).toBe(teamMessages.neverUsed)
  })

  // 加厚字段是前后端并行上线的：后端还没发版时这些键整个不存在（不是 null）。
  // 那种情况下必须整页降级成「—」，而不是抛错把表格打成「加载失败」。
  it('后端还没下发加厚字段时逐格降级成「—」，不炸页', async () => {
    const legacy = {
      group_key: 'k:1',
      display_name: 'legacy',
      by_owner: false,
      key_count: 1,
      key_count_all: 1,
      all_deleted: false,
      masked_key: null,
      last_used_at: null,
      current_cost: 1.5,
      prev_cost: 1,
      delta_pct: 50,
      delta_abs: 0.5,
      requests: 10,
      prev_requests: 8,
      is_anomaly: false,
    } as unknown as TeamRow
    const legacySummary = { ...summary } as Record<string, unknown>
    delete legacySummary.requests
    delete legacySummary.active_row_count
    delete legacySummary.cache_hit_rate
    delete legacySummary.cache_saved_cost

    const w = await mountView([legacy], legacySummary as unknown as TeamSummary)
    const tds = w.findAll('tbody tr')[0].findAll('td')
    expect(tds.length).toBe(9)
    expect(tds[3].find('svg').exists()).toBe(false) // 没有 daily → 不画线
    expect(tds[5].text()).toBe('—') // avg_cost
    expect(tds[6].text()).toBe('—') // top_model
    expect(tds[7].text()).toBe('—') // cache_hit_rate
    const overview = w.get('[data-testid="overview-grid"]').text()
    expect(overview).toContain('命中率 —')
    expect(overview).toContain('— / 4 有消耗')
    expect(overview).not.toContain('0 / 4 有消耗')
  })

  it('「全新账号」看 key_count/row_count，不是 rows.length', async () => {
    const w = await mountView([], { ...summary, key_count: 0, row_count: 0 })
    expect(w.text()).toContain(teamMessages.empty)

    const w2 = await mountView([], summary) // 有令牌但本期没消耗
    expect(w2.text()).not.toContain(teamMessages.empty)
    expect(w2.text()).toContain(teamMessages.zeroCost)
  })
})
