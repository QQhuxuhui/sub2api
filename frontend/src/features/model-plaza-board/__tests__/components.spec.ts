import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import type { ModelPlazaResponse } from '@/api/modelPlaza'
import BoardHealthStrip from '../BoardHealthStrip.vue'
import BoardLineHealth from '../BoardLineHealth.vue'
import BoardModelCard from '../BoardModelCard.vue'
import ModelPlazaBoard from '../ModelPlazaBoard.vue'
import { buildBoardModels } from '../plazaBoard'
import type { PlazaHealthResponse } from '../types'
import { SAMPLE_START, group, health, healthLine, requestModel, samples, tokenModel } from './fixtures'

const state = vi.hoisted(() => ({
  authenticated: true,
  v2: true,
  query: {} as Record<string, string>,
  replace: vi.fn(),
  getPlazaHealth: vi.fn()
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => (params ? `${key}|${JSON.stringify(params)}` : key)
    })
  }
})
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: state.query }),
  useRouter: () => ({ replace: state.replace })
}))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    get isAuthenticated() {
      return state.authenticated
    }
  })
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ cachedPublicSettings: null }) }))
vi.mock('@/utils/featureFlags', () => ({ isChannelMonitorV2Mode: () => state.v2 }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: vi.fn() }) }))
vi.mock('../api', () => ({ getPlazaHealth: (...args: unknown[]) => state.getPlazaHealth(...args) }))

const response: ModelPlazaResponse = {
  description: '',
  groups: [
    group(3, 'ChatGPT', 'openai', [tokenModel('gpt-a', 'openai'), requestModel('gpt-image', 'openai', 'image', 0.02)], { rate_multiplier: 0.25 }),
    group(16, 'ChatGPT-Pro', 'openai', [tokenModel('gpt-a', 'openai')], { rate_multiplier: 0.5 }),
    group(18, 'Claude', 'anthropic', [tokenModel('claude-b', 'anthropic')], { rate_multiplier: 1.5 })
  ]
}

const healthData: PlazaHealthResponse = health([
  healthLine('openai', 3, 'gpt-a', 'critical', { rank: 1 }),
  healthLine('openai', 16, 'gpt-a', 'healthy', { rank: 1, samples: samples([0, 0.5]) }),
  healthLine('anthropic', 18, 'claude-b', 'warning', { rank: 2 })
])

async function mountBoard() {
  const wrapper = mount(ModelPlazaBoard, {
    props: { response, loading: false },
    global: { stubs: { Teleport: true, PlazaGroupSection: { template: '<div class="group-section-stub" />' } } }
  })
  await flushPromises()
  await nextTick()
  return wrapper
}

beforeEach(() => {
  state.authenticated = true
  state.v2 = true
  state.query = {}
  state.replace.mockReset()
  state.getPlazaHealth.mockReset()
  state.getPlazaHealth.mockResolvedValue(healthData)
})

describe('BoardHealthStrip', () => {
  it('只画有调用的小时，按成功率分四档，悬停提示不含请求数', () => {
    const wrapper = mount(BoardHealthStrip, { props: { samples: samples([0, 0.05, 0.25, 0.5]) } })
    const cells = wrapper.findAll('i')
    expect(cells).toHaveLength(4)
    expect(cells[0].classes()).toContain('bg-emerald-500')
    expect(cells[1].classes()).toContain('bg-emerald-300')
    expect(cells[2].classes()).toContain('bg-amber-500')
    expect(cells[3].classes()).toContain('bg-red-500')
    expect(cells[3].attributes('title')).toContain('plazaBoard.sampleTitle|')
    expect(cells[3].attributes('title')).toContain('50.00%')
    expect(wrapper.html()).not.toMatch(/request|count|bg-gray/i)
  })
})

describe('BoardLineHealth', () => {
  afterEach(() => vi.useRealTimers())

  it('近期无调用时给占位文案，不画状态条', () => {
    const wrapper = mount(BoardLineHealth, { props: { health: null } })
    expect(wrapper.find('[data-testid="plaza-idle"]').text()).toContain('plazaBoard.idle')
    expect(wrapper.find('.plaza-strip').exists()).toBe(false)
  })

  it('展示成功率；最近一次调用较早时标出多久以前', () => {
    vi.useFakeTimers()
    const line = healthLine('openai', 16, 'gpt-a', 'healthy', { samples: samples([0, 0.5]) })
    const lastEnd = (SAMPLE_START + 2 * 3600) * 1000

    vi.setSystemTime(lastEnd + 30 * 60_000)
    let wrapper = mount(BoardLineHealth, { props: { health: line } })
    expect(wrapper.find('[data-testid="plaza-success-rate"]').text()).toBe('99.00%')
    expect(wrapper.find('[data-testid="plaza-last-call"]').exists()).toBe(false)

    vi.setSystemTime(lastEnd + 5 * 3_600_000)
    wrapper = mount(BoardLineHealth, { props: { health: line } })
    expect(wrapper.find('[data-testid="plaza-last-call"]').text()).toContain('plazaBoard.hoursAgo')

    vi.setSystemTime(lastEnd + 3 * 24 * 3_600_000)
    wrapper = mount(BoardLineHealth, { props: { health: line } })
    expect(wrapper.find('[data-testid="plaza-last-call"]').text()).toContain('plazaBoard.daysAgo')
  })
})

describe('BoardModelCard', () => {
  const model = buildBoardModels(response.groups, healthData).find((m) => m.name === 'gpt-a')!

  it('展示推荐线路的价格、分组、状态与指标', () => {
    const wrapper = mount(BoardModelCard, { props: { model, showHealth: true } })
    const text = wrapper.text()
    expect(text).toContain('gpt-a')
    expect(text).toContain('ChatGPT-Pro') // 状态正常的线路里最便宜的一条
    expect(text).toContain('+1')
    expect(text).toContain('$1.50') // 3e-6 × 0.5 × 1M
    expect(text).toContain('plazaBoard.state.healthy')
    expect(text).toContain('99.00%')
    expect(wrapper.findAll('.plaza-strip i')).toHaveLength(2)
  })

  it('没有健康数据时只留价格和详情', () => {
    const plain = buildBoardModels(response.groups, null).find((m) => m.name === 'gpt-a')!
    const wrapper = mount(BoardModelCard, { props: { model: plain, showHealth: false } })
    expect(wrapper.find('.plaza-strip').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('plazaBoard.availability')
    expect(wrapper.text()).toContain('$0.75') // 最便宜的线路：3e-6 × 0.25 × 1M
    expect(wrapper.text()).toContain('plazaBoard.details')
  })

  it('点击详情发出 open 事件', async () => {
    const wrapper = mount(BoardModelCard, { props: { model, showHealth: true } })
    await wrapper.findAll('button').at(-1)!.trigger('click')
    expect(wrapper.emitted('open')).toEqual([['gpt-a']])
  })
})

describe('ModelPlazaBoard', () => {
  it('登录且监控为 V2：请求健康数据，展示状态总览并默认按热度排序', async () => {
    const wrapper = await mountBoard()
    expect(state.getPlazaHealth).toHaveBeenCalledTimes(1)
    expect(wrapper.find('[data-testid="plaza-state-all"]').text()).toContain('3')
    expect(wrapper.find('[data-testid="plaza-state-healthy"]').text()).toContain('1')
    expect(wrapper.find('[data-testid="plaza-state-warning"]').text()).toContain('1')
    const names = wrapper.findAll('article h3').map((h) => h.text())
    expect(names.slice(0, 2)).toEqual(['gpt-a', 'claude-b'])
    expect((wrapper.find('#plaza-board-sort').element as HTMLSelectElement).value).toBe('rank')
  })

  it('点击状态块只看对应状态的模型', async () => {
    const wrapper = await mountBoard()
    await wrapper.find('[data-testid="plaza-state-warning"]').trigger('click')
    expect(wrapper.findAll('article h3').map((h) => h.text())).toEqual(['claude-b'])
  })

  it('未登录：不请求健康数据，不展示任何状态界面', async () => {
    state.authenticated = false
    const wrapper = await mountBoard()
    expect(state.getPlazaHealth).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="plaza-state-all"]').exists()).toBe(false)
    expect(wrapper.find('.plaza-strip').exists()).toBe(false)
    const options = wrapper.findAll('#plaza-board-sort option').map((o) => o.attributes('value'))
    expect(options).toEqual(['name', 'price'])
    expect(wrapper.findAll('article')).toHaveLength(3)
  })

  it('监控不是 V2：不请求健康数据', async () => {
    state.v2 = false
    const wrapper = await mountBoard()
    expect(state.getPlazaHealth).not.toHaveBeenCalled()
    expect(wrapper.find('.plaza-strip').exists()).toBe(false)
  })

  it('健康接口失败或返回不可用：静默降级，价格照常', async () => {
    state.getPlazaHealth.mockRejectedValue(new Error('rate limited'))
    let wrapper = await mountBoard()
    expect(wrapper.findAll('article')).toHaveLength(3)
    expect(wrapper.find('.plaza-strip').exists()).toBe(false)

    state.getPlazaHealth.mockResolvedValue({ available: false, lines: [] })
    wrapper = await mountBoard()
    expect(wrapper.find('[data-testid="plaza-state-all"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('$0.75')
  })

  it('view=list 时渲染列表；切换视图更新 query 并保留其他参数', async () => {
    state.query = { embedded: '1', view: 'list' }
    const wrapper = await mountBoard()
    expect(wrapper.find('table').exists()).toBe(true)
    expect(wrapper.find('article').exists()).toBe(false)
    await wrapper.find('[data-testid="plaza-view-card"]').trigger('click')
    expect(state.replace).toHaveBeenCalledWith({ query: { embedded: '1' } })
  })

  it('打开抽屉列出该模型的全部线路，并带完整计费明细', async () => {
    const wrapper = await mountBoard()
    await wrapper.findAll('article')[0].findAll('button').at(-1)!.trigger('click')
    const dialog = wrapper.find('[role="dialog"]')
    expect(dialog.exists()).toBe(true)
    expect(dialog.findAll('section')).toHaveLength(2)
    expect(dialog.text()).toContain('plazaBoard.drawer.cheapest')
    expect(dialog.text()).toContain('plazaBoard.drawer.steadiest')
    expect(dialog.findAll('.group-section-stub')).toHaveLength(2)
    await wrapper.find('[data-testid="plaza-drawer-scrim"]').trigger('click')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('搜索不到时给出空结果提示', async () => {
    const wrapper = await mountBoard()
    await wrapper.find('#plaza-board-search').setValue('no-such-model')
    expect(wrapper.text()).toContain('plazaBoard.emptyFiltered')
  })

  it('分组筛选：选中后只看该分组的模型与线路，价格与统计随之变化', async () => {
    const wrapper = await mountBoard()
    const options = wrapper.findAll('[data-testid^="plaza-group-"]')
    expect(options.map((o) => o.attributes('data-testid'))).toEqual([
      'plaza-group-all',
      'plaza-group-18', // 按平台归类：Claude 在前，同平台内保持原顺序
      'plaza-group-3',
      'plaza-group-16'
    ])
    expect(options[0].text()).toContain('"count":3')
    expect(options[2].text()).toContain('ChatGPT')
    expect(options[2].text()).toContain('"count":2')
    expect(options[2].text()).toContain('×0.25')
    // 分组名原样显示，不拆分不改写
    expect(wrapper.findAll('[data-testid="plaza-rail-name"]').map((n) => n.text())).toEqual(['Claude', 'ChatGPT', 'ChatGPT-Pro'])

    await wrapper.find('[data-testid="plaza-group-3"]').trigger('click')
    expect(wrapper.find('[data-testid="plaza-group-3"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.findAll('article h3').map((h) => h.text()).sort()).toEqual(['gpt-a', 'gpt-image'])
    const card = wrapper.findAll('article').find((a) => a.find('h3').text() === 'gpt-a')!
    expect(card.text()).toContain('$0.75') // 组 3 的线路价，而不是全部分组时推荐的组 16（$1.50）
    expect(card.text()).not.toContain('+1')
    expect(card.text()).toContain('plazaBoard.state.critical')
    expect(wrapper.find('[data-testid="plaza-state-all"]').text()).toContain('2')
    expect(wrapper.find('[data-testid="plaza-state-healthy"]').text()).toContain('0')
    expect(wrapper.text()).toContain('plazaBoard.count|{"models":2,"lines":2}')

    await wrapper.find('[data-testid="plaza-group-all"]').trigger('click')
    expect(wrapper.findAll('article')).toHaveLength(3)
    expect(wrapper.text()).toContain('plazaBoard.count|{"models":3,"lines":4}')
  })

  it('分组筛选：选中的分组在刷新后消失时回到全部；选中分组不会误入整体空态', async () => {
    const wrapper = await mountBoard()
    await wrapper.find('[data-testid="plaza-group-18"]').trigger('click')
    await wrapper.find('#plaza-board-search').setValue('gpt')
    expect(wrapper.text()).toContain('plazaBoard.emptyFiltered')
    expect(wrapper.text()).not.toContain('modelPlaza.empty')
    await wrapper.find('#plaza-board-search').setValue('')

    await wrapper.setProps({ response: { ...response, groups: response.groups.filter((g) => g.id !== 18) } })
    await nextTick()
    expect(wrapper.find('[data-testid="plaza-group-18"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="plaza-group-all"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.findAll('article h3').map((h) => h.text()).sort()).toEqual(['gpt-a', 'gpt-image'])
  })
})
