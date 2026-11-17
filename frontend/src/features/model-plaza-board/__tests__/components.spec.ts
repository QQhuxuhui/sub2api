import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import type { ModelPlazaResponse } from '@/api/modelPlaza'
import BoardHealthStrip from '../BoardHealthStrip.vue'
import BoardModelCard from '../BoardModelCard.vue'
import ModelPlazaBoard from '../ModelPlazaBoard.vue'
import { buildBoardModels } from '../plazaBoard'
import type { PlazaHealthResponse } from '../types'
import { buckets, group, health, healthLine, requestModel, tokenModel } from './fixtures'

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
  healthLine('openai', 16, 'gpt-a', 'healthy', { rank: 1, buckets: buckets(['healthy', 'critical']) }),
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
  it('渲染全部统计格，悬停提示不含请求数', () => {
    const wrapper = mount(BoardHealthStrip, {
      props: { buckets: buckets(['healthy', 'critical']), window: healthData.window }
    })
    const cells = wrapper.findAll('i')
    expect(cells).toHaveLength(72)
    expect(cells[0].classes()).toContain('bg-emerald-500')
    expect(cells[1].classes()).toContain('bg-red-500')
    expect(cells[1].attributes('title')).toContain('plazaBoard.bucketTitle|')
    expect(cells[1].attributes('title')).toContain('50.0%')
    expect(cells[2].attributes('title')).toContain('plazaBoard.bucketTitleUnknown')
    expect(wrapper.html()).not.toMatch(/request|count/i)
  })
})

describe('BoardModelCard', () => {
  const model = buildBoardModels(response.groups, healthData).find((m) => m.name === 'gpt-a')!

  it('展示推荐线路的价格、分组、状态与指标', () => {
    const wrapper = mount(BoardModelCard, { props: { model, showHealth: true, window: healthData.window } })
    const text = wrapper.text()
    expect(text).toContain('gpt-a')
    expect(text).toContain('ChatGPT-Pro') // 状态正常的线路里最便宜的一条
    expect(text).toContain('+1')
    expect(text).toContain('$1.50') // 3e-6 × 0.5 × 1M
    expect(text).toContain('plazaBoard.state.healthy')
    expect(text).toContain('99.0%')
    expect(wrapper.findAll('.plaza-strip i')).toHaveLength(72)
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
})
