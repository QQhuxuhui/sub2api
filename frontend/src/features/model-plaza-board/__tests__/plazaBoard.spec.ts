import { describe, expect, it } from 'vitest'
import {
  buildBoardModels,
  filterAndSortModels,
  formatAvailability,
  formatTtft,
  hasHealthData,
  hasRank,
  lineBadges,
  summarizePrice,
  vendorOf,
  scopeGroups,
  type BoardFilter
} from '../plazaBoard'
import { group, health, healthLine, requestModel, tokenModel } from './fixtures'

const baseFilter: BoardFilter = { query: '', group: 'all', vendor: 'all', billing: 'all', state: 'all', sort: 'name' }

describe('summarizePrice', () => {
  it('token 计费：基础价乘生效倍率，换算为每百万 token', () => {
    const g = group(1, 'g', 'anthropic', [], { rate_multiplier: 0.5 })
    const price = summarizePrice(tokenModel('claude-sonnet', 'anthropic'), g)
    expect(price).toMatchObject({ billing: 'token', input: '$1.50', output: '$7.50', cacheRead: '$0.15' })
    expect(price.amount).toBeCloseTo(1.5)
  })

  it('用户专属倍率覆盖分组倍率', () => {
    const g = group(1, 'g', 'anthropic', [], { rate_multiplier: 1, user_rate_multiplier: 0.2 })
    expect(summarizePrice(tokenModel('m', 'anthropic'), g).input).toBe('$0.60')
  })

  it('有阶梯时取最低档', () => {
    const m = tokenModel('gpt-ladder', 'openai')
    m.pricing!.intervals = [
      { min_tokens: 0, max_tokens: 272000, input_price: 2.5e-6, output_price: 1e-5, cache_write_price: null, cache_read_price: null, per_request_price: null },
      { min_tokens: 272000, max_tokens: null, input_price: 5e-6, output_price: 2e-5, cache_write_price: null, cache_read_price: null, per_request_price: null }
    ]
    expect(summarizePrice(m, group(1, 'g', 'openai', []))).toMatchObject({ input: '$2.50', output: '$10.00' })
  })

  it('图片档位价优先于基础按次价，多档时标「起」', () => {
    const m = requestModel('gpt-image', 'openai', 'image', 0.2, [
      { label: '1K', price: 0.02 },
      { label: '2K', price: 0.04 }
    ])
    const price = summarizePrice(m, group(1, 'g', 'openai', []))
    expect(price).toMatchObject({ billing: 'request', request: '$0.02', from: true, tierLabel: '1K' })
    expect(price.amount).toBeCloseTo(0.02)
  })

  it('图片独立倍率覆盖分组倍率；视频独立倍率不为负', () => {
    const image = requestModel('img', 'openai', 'image', 0.1)
    const g = group(1, 'g', 'openai', [], { rate_multiplier: 0.5, image_rate_independent: true, image_rate_multiplier: 2 })
    expect(summarizePrice(image, g).request).toBe('$0.20')

    const video = requestModel('vid', 'openai', 'video', 0.1)
    const vg = group(1, 'g', 'openai', [], { video_rate_independent: true, video_rate_multiplier: -3 })
    expect(summarizePrice(video, vg).request).toBe('$0.00')
  })

  it('没有任何价格时为待配置', () => {
    const g = group(1, 'g', 'openai', [])
    expect(summarizePrice({ name: 'x', platform: 'openai', pricing: null, official_pricing: null }, g).billing).toBe('none')
    expect(summarizePrice(requestModel('y', 'openai', 'per_request', null), g).billing).toBe('none')
  })
})

describe('buildBoardModels', () => {
  const groups = [
    group(3, 'cheap-unstable', 'anthropic', [tokenModel('claude-x', 'anthropic')], { rate_multiplier: 0.2 }),
    group(18, 'stable', 'anthropic', [tokenModel('claude-x', 'anthropic'), tokenModel('claude-only', 'anthropic')], { rate_multiplier: 1.5 }),
    group(20, 'mid', 'anthropic', [tokenModel('claude-x', 'anthropic')], { rate_multiplier: 0.8 })
  ]

  it('同名模型合并为一张卡片，线路数等于分组数', () => {
    const models = buildBoardModels(groups, null)
    const x = models.find((m) => m.name === 'claude-x')!
    expect(x.lines).toHaveLength(3)
    expect(models.find((m) => m.name === 'claude-only')!.lines).toHaveLength(1)
  })

  it('没有健康数据时推荐最便宜的线路，状态为未知', () => {
    const x = buildBoardModels(groups, null).find((m) => m.name === 'claude-x')!
    expect(x.recommended.group.id).toBe(3)
    expect(x.state).toBe('unknown')
    expect(hasHealthData([x])).toBe(false)
  })

  it('推荐线路取状态正常的线路里最便宜的一条', () => {
    const h = health([
      healthLine('anthropic', 3, 'claude-x', 'critical'),
      healthLine('anthropic', 18, 'claude-x', 'healthy'),
      healthLine('anthropic', 20, 'claude-x', 'healthy')
    ])
    const x = buildBoardModels(groups, h).find((m) => m.name === 'claude-x')!
    expect(x.recommended.group.id).toBe(20)
    expect(x.state).toBe('healthy')
  })

  it('没有正常线路时取状态最好的一条', () => {
    const h = health([
      healthLine('anthropic', 3, 'claude-x', 'critical'),
      healthLine('anthropic', 18, 'claude-x', 'warning')
    ])
    const x = buildBoardModels(groups, h).find((m) => m.name === 'claude-x')!
    expect(x.recommended.group.id).toBe(18)
    expect(x.state).toBe('warning')
  })

  it('健康数据按平台+分组+模型精确匹配，不串到别的线路', () => {
    const h = health([
      healthLine('openai', 3, 'claude-x', 'healthy'), // 平台不同
      healthLine('anthropic', 99, 'claude-x', 'healthy'), // 分组不同
      healthLine('anthropic', 3, 'claude-other', 'healthy') // 模型不同
    ])
    const x = buildBoardModels(groups, h).find((m) => m.name === 'claude-x')!
    expect(x.lines.every((l) => l.health === null && l.state === 'unknown')).toBe(true)
  })

  it('available 为 false 的健康响应视为没有健康数据', () => {
    const h = { ...health([healthLine('anthropic', 3, 'claude-x', 'healthy')]), available: false }
    expect(hasHealthData(buildBoardModels(groups, h))).toBe(false)
  })

  it('同名但平台不同的模型是不同线路', () => {
    const mixed = [
      group(7, 'mix', 'composite', [tokenModel('shared', 'openai'), tokenModel('shared', 'gemini')])
    ]
    const h = health([healthLine('gemini', 7, 'shared', 'healthy')])
    const shared = buildBoardModels(mixed, h)[0]
    expect(shared.lines).toHaveLength(2)
    expect(shared.lines.filter((l) => l.health).map((l) => l.model.platform)).toEqual(['gemini'])
  })

  it('卡片名次取各线路里的名次', () => {
    const h = health([healthLine('anthropic', 18, 'claude-x', 'healthy', { rank: 4 })])
    const models = buildBoardModels(groups, h)
    expect(models.find((m) => m.name === 'claude-x')!.rank).toBe(4)
    expect(models.find((m) => m.name === 'claude-only')!.rank).toBeNull()
    expect(hasRank(models)).toBe(true)
  })
})

describe('filterAndSortModels', () => {
  const groups = [
    group(1, 'g', 'openai', [
      tokenModel('gpt-busy', 'openai'),
      tokenModel('gpt-quiet', 'openai', { pricing: { ...tokenModel('a', 'b').pricing!, input_price: 1e-6 } }),
      tokenModel('claude-flaky', 'openai'),
      requestModel('gpt-image', 'openai', 'image', 0.02),
      { name: 'gpt-unpriced', platform: 'openai', pricing: null, official_pricing: null }
    ])
  ]
  const h = health([
    healthLine('openai', 1, 'gpt-busy', 'healthy', { rank: 1, error_rate: 0.02, ttft_p50_ms: 900 }),
    healthLine('openai', 1, 'claude-flaky', 'critical', { rank: 2, error_rate: 0.4, ttft_p50_ms: 300 }),
    // 样本不足：接口给的 error_rate 为 null，状态 unknown
    healthLine('openai', 1, 'gpt-quiet', 'unknown', { rank: null, ttft_p50_ms: null })
  ])
  const models = buildBoardModels(groups, h)
  const names = (filter: Partial<BoardFilter>) => filterAndSortModels(models, { ...baseFilter, ...filter }).map((m) => m.name)

  it('按名称、厂商、计费方式、状态筛选', () => {
    expect(names({ query: 'IMAGE' })).toEqual(['gpt-image'])
    expect(names({ vendor: 'anthropic' })).toEqual(['claude-flaky'])
    expect(names({ billing: 'request' })).toEqual(['gpt-image'])
    expect(names({ state: 'critical' })).toEqual(['claude-flaky'])
  })

  it('最多人用：无名次的排在后面', () => {
    expect(names({ sort: 'rank' }).slice(0, 2)).toEqual(['gpt-busy', 'claude-flaky'])
  })

  it('价格：按量在前、按次其次、待配置最后', () => {
    expect(names({ sort: 'price' })).toEqual(['gpt-quiet', 'claude-flaky', 'gpt-busy', 'gpt-image', 'gpt-unpriced'])
  })

  it('最稳定：状态未知或没有健康数据的不参与比较，排在最后', () => {
    const order = names({ sort: 'stable' })
    expect(order.slice(0, 2)).toEqual(['gpt-busy', 'claude-flaky'])
    expect(order.indexOf('gpt-quiet')).toBeGreaterThan(1)
  })

  it('响应最快：没有首字数据的排在后面', () => {
    expect(names({ sort: 'fast' }).slice(0, 2)).toEqual(['claude-flaky', 'gpt-busy'])
  })
})

describe('lineBadges', () => {
  const groups = [
    group(1, 'cheap', 'openai', [tokenModel('m', 'openai')], { rate_multiplier: 0.1 }),
    group(2, 'steady', 'openai', [tokenModel('m', 'openai')], { rate_multiplier: 1 }),
    group(3, 'no-data', 'openai', [tokenModel('m', 'openai')], { rate_multiplier: 2 })
  ]

  it('最便宜、最稳、最快分别颁给对应线路', () => {
    const h = health([
      healthLine('openai', 1, 'm', 'warning', { error_rate: 0.08, ttft_p50_ms: 500 }),
      healthLine('openai', 2, 'm', 'healthy', { error_rate: 0.01, ttft_p50_ms: 3000 })
    ])
    const model = buildBoardModels(groups, h)[0]
    const badges = lineBadges(model.lines)
    const of = (id: number) => badges.get(model.lines.find((l) => l.group.id === id)!.key)!
    expect(of(1)).toEqual({ cheapest: true, steadiest: false, fastest: true })
    expect(of(2)).toEqual({ cheapest: false, steadiest: true, fastest: false })
    expect(of(3)).toEqual({ cheapest: false, steadiest: false, fastest: false })
  })

  it('全部状态未知时不颁发最稳和最快', () => {
    const model = buildBoardModels(groups, health([healthLine('openai', 1, 'm', 'unknown')]))[0]
    const all = [...lineBadges(model.lines).values()]
    expect(all.some((b) => b.steadiest || b.fastest)).toBe(false)
    expect(all.filter((b) => b.cheapest)).toHaveLength(1)
  })

  it('只有一条线路时不颁发任何标签', () => {
    const model = buildBoardModels([groups[0]], null)[0]
    expect([...lineBadges(model.lines).values()]).toEqual([{ cheapest: false, steadiest: false, fastest: false }])
  })
})

describe('展示格式', () => {
  it('可用率与首字', () => {
    const groups = [group(1, 'g', 'openai', [tokenModel('a', 'openai'), tokenModel('b', 'openai')])]
    const h = health([
      healthLine('openai', 1, 'a', 'healthy', { error_rate: 0.0123, ttft_p50_ms: 8817 }),
      healthLine('openai', 1, 'b', 'unknown')
    ])
    const [a, b] = buildBoardModels(groups, h)
    expect(formatAvailability(a.recommended)).toBe('98.8%')
    expect(formatTtft(a.recommended.health?.ttft_p50_ms)).toBe('8.8s')
    expect(formatAvailability(b.recommended)).toBe('—')
    expect(formatTtft(null)).toBe('—')
  })

  it('厂商归类', () => {
    expect(vendorOf('gpt-6.1-sol').key).toBe('openai')
    expect(vendorOf('claude-opus-5-5').key).toBe('anthropic')
    expect(vendorOf('gemini-3-flash').key).toBe('google')
    expect(vendorOf('MiniMax-M3').key).toBe('minimax')
    expect(vendorOf('step-5-preview').key).toBe('other')
  })
})

describe('scopeGroups', () => {
  const groups = [
    group(3, 'A', 'openai', [tokenModel('gpt-a', 'openai')], { rate_multiplier: 0.25 }),
    group(16, 'B', 'openai', [tokenModel('gpt-a', 'openai'), tokenModel('gpt-b', 'openai')], { rate_multiplier: 0.5 })
  ]

  it('全部分组时原样返回，空输入得空数组', () => {
    expect(scopeGroups(groups, 'all')).toBe(groups)
    expect(scopeGroups(null, 'all')).toEqual([])
  })

  it('选中分组后只留该分组，模型的线路与推荐价都来自它', () => {
    const scoped = scopeGroups(groups, 16)
    expect(scoped.map((g) => g.id)).toEqual([16])
    const models = buildBoardModels(scoped, null)
    expect(models.map((m) => m.name).sort()).toEqual(['gpt-a', 'gpt-b'])
    const a = models.find((m) => m.name === 'gpt-a')!
    expect(a.lines).toHaveLength(1)
    expect(a.recommended.group.id).toBe(16)
    expect(a.recommended.price.input).toBe('$1.50') // 3e-6 × 0.5 × 1M，而不是全部分组时更便宜的 0.75
  })

  it('分组不存在时得空数组', () => {
    expect(scopeGroups(groups, 999)).toEqual([])
  })
})
