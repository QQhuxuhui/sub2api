import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import OpenAICacheUserSimModal from '../OpenAICacheUserSimModal.vue'
import OpenAICacheGlobalCard from '../OpenAICacheGlobalCard.vue'
import type { OpenAICacheUserRow } from '@/api/admin/openaiCache'

const showError = vi.fn()
const showSuccess = vi.fn()
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess })
}))

const updateUserSimulation = vi.fn()
const getGlobalSimulation = vi.fn()
const updateGlobalSimulation = vi.fn()
vi.mock('@/api/admin/openaiCache', () => ({
  openaiCacheAPI: {
    updateUserSimulation: (...args: unknown[]) => updateUserSimulation(...args),
    getGlobalSimulation: (...args: unknown[]) => getGlobalSimulation(...args),
    updateGlobalSimulation: (...args: unknown[]) => updateGlobalSimulation(...args)
  }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const rate = { requests: 0, zero_cache_requests: 0, input_tokens: 0, cache_read_tokens: 0, cache_creation_tokens: 0, raw_input_tokens: 0, raw_cache_read_tokens: 0, billed_cost: 0, cost_delta: 0, raw_rate: null, display_rate: null }
const cfg = { enabled: true, lo: 82, hi: 88, half_life_minutes: 60, cap: 95 }
const makeUser = (overrides: Partial<OpenAICacheUserRow> = {}): OpenAICacheUserRow => ({
  user_id: 185,
  email: 'u@example.com',
  username: '',
  source: 'global',
  active: true,
  config: cfg,
  override: null,
  all: rate,
  text: rate,
  requests: 0,
  zero_cache_requests: 0,
  zero_cache_pct: 0,
  billed_cost: 0,
  expected_cost: 0,
  cost_delta: 0,
  reachable_rate: null,
  coverage_rate: null,
  verdict: '',
  ...overrides
})

const mountModal = (user: OpenAICacheUserRow) =>
  mount(OpenAICacheUserSimModal, {
    props: { show: true, user, global: cfg },
    global: { stubs: { Teleport: true, Transition: false } },
    attachTo: document.body
  })

describe('OpenAICacheUserSimModal', () => {
  beforeEach(() => {
    updateUserSimulation.mockReset()
    showError.mockReset()
    showSuccess.mockReset()
  })

  it('继承模式提交 inherit，删除单独配置', async () => {
    updateUserSimulation.mockResolvedValue({ inherit: true })
    const wrapper = mountModal(makeUser())
    await flushPromises()
    await wrapper.get('[data-testid="openai-cache-user-save"]').trigger('click')
    await flushPromises()
    expect(updateUserSimulation).toHaveBeenCalledWith(185, { inherit: true })
    expect(wrapper.emitted('saved')).toBeTruthy()
    wrapper.unmount()
  })

  it('已有单独关闭的用户默认选中关闭，保存提交 enabled=false', async () => {
    updateUserSimulation.mockResolvedValue({ ...cfg, enabled: false })
    const wrapper = mountModal(makeUser({ source: 'user', active: false, override: { ...cfg, enabled: false } }))
    await flushPromises()
    expect(wrapper.get('[data-testid="openai-cache-user-mode-off"]').attributes('aria-checked')).toBe('true')
    await wrapper.get('[data-testid="openai-cache-user-save"]').trigger('click')
    await flushPromises()
    expect(updateUserSimulation).toHaveBeenCalledWith(185, expect.objectContaining({ enabled: false, lo: 82, hi: 88 }))
    wrapper.unmount()
  })

  it('单独开启时区间非法不提交', async () => {
    const wrapper = mountModal(makeUser())
    await flushPromises()
    await wrapper.get('[data-testid="openai-cache-user-mode-on"]').trigger('click')
    await wrapper.get('[data-testid="openai-cache-user-lo"]').setValue(90)
    await wrapper.get('[data-testid="openai-cache-user-hi"]').setValue(80)
    await wrapper.get('[data-testid="openai-cache-user-save"]').trigger('click')
    await flushPromises()
    expect(updateUserSimulation).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalled()
    wrapper.unmount()
  })
})

describe('OpenAICacheGlobalCard', () => {
  beforeEach(() => {
    getGlobalSimulation.mockReset()
    updateGlobalSimulation.mockReset()
  })

  it('加载全局配置并显示单独配置数量，保存提交表单', async () => {
    getGlobalSimulation.mockResolvedValue({ config: { ...cfg, enabled: false }, overrides: 3 })
    updateGlobalSimulation.mockResolvedValue(cfg)
    const wrapper = mount(OpenAICacheGlobalCard)
    await flushPromises()
    expect(wrapper.get('[data-testid="openai-cache-global-overrides"]').exists()).toBe(true)
    await wrapper.get('[data-testid="openai-cache-global-toggle"]').trigger('click')
    await wrapper.get('[data-testid="openai-cache-global-save"]').trigger('click')
    await flushPromises()
    expect(updateGlobalSimulation).toHaveBeenCalledWith(expect.objectContaining({ enabled: true, lo: 82, hi: 88, cap: 95 }))
    expect(wrapper.emitted('saved')).toBeTruthy()
  })

  it('读取失败时不能保存，只能重新读取', async () => {
    getGlobalSimulation.mockRejectedValueOnce(new Error('boom'))
    const wrapper = mount(OpenAICacheGlobalCard)
    await flushPromises()
    expect(wrapper.find('[data-testid="openai-cache-global-save"]').exists()).toBe(false)
    getGlobalSimulation.mockResolvedValue({ config: { ...cfg, enabled: true }, overrides: 0 })
    await wrapper.get('[data-testid="openai-cache-global-reload"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="openai-cache-global-save"]').exists()).toBe(true)
    expect(updateGlobalSimulation).not.toHaveBeenCalled()
  })
})
