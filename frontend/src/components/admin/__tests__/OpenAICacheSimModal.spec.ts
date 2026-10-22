import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import OpenAICacheSimModal from '../OpenAICacheSimModal.vue'
import type { OpenAICacheAccountRow } from '@/api/admin/openaiCache'

const showError = vi.fn()
const showSuccess = vi.fn()
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess })
}))

const updateSimulation = vi.fn()
vi.mock('@/api/admin/openaiCache', () => ({
  openaiCacheAPI: { updateSimulation: (...args: unknown[]) => updateSimulation(...args) }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const rate = { requests: 0, zero_cache_requests: 0, input_tokens: 0, cache_read_tokens: 0, cache_creation_tokens: 0, raw_input_tokens: 0, raw_cache_read_tokens: 0, billed_cost: 0, cost_delta: 0, raw_rate: null, display_rate: null }
const makeRow = (overrides: Partial<OpenAICacheAccountRow> = {}): OpenAICacheAccountRow => ({
  account_id: 112,
  name: 'qimux',
  type: 'apikey',
  status: 'active',
  is_shadow: false,
  schedulable: true,
  simulation: { enabled: false, lo: 82, hi: 88, half_life_minutes: 60, cap: 95 },
  all: rate,
  text: rate,
  requests: 0,
  zero_cache_requests: 0,
  zero_cache_pct: 0,
  billed_cost: 0,
  expected_cost: 0,
  cost_delta: 0,
  ...overrides
})

const mountModal = (row: OpenAICacheAccountRow) =>
  mount(OpenAICacheSimModal, {
    props: { show: true, account: row },
    global: { stubs: { Teleport: true, Transition: false } },
    attachTo: document.body
  })

describe('OpenAICacheSimModal', () => {
  beforeEach(() => {
    updateSimulation.mockReset()
    showError.mockReset()
    showSuccess.mockReset()
  })

  it('从关着的账号点开时默认开启开关，保存只提交模拟配置并回传结果', async () => {
    updateSimulation.mockResolvedValue({ enabled: true, lo: 80, hi: 88, half_life_minutes: 60, cap: 90 })
    const wrapper = mountModal(makeRow())
    await flushPromises()

    const toggle = wrapper.find('[data-testid="openai-cache-sim-modal-toggle"]')
    expect(toggle.attributes('aria-checked')).toBe('true')

    await wrapper.find('[data-testid="openai-cache-sim-modal-lo"]').setValue('80')
    await wrapper.find('[data-testid="openai-cache-sim-modal-cap"]').setValue('90')
    await wrapper.find('[data-testid="openai-cache-sim-modal-save"]').trigger('click')
    await flushPromises()

    expect(updateSimulation).toHaveBeenCalledWith(112, { enabled: true, lo: 80, hi: 88, half_life_minutes: 60, cap: 90 })
    expect(showSuccess).toHaveBeenCalled()
    const saved = wrapper.emitted('saved')
    expect(saved).toBeTruthy()
    expect(saved![0]).toEqual([112, { enabled: true, lo: 80, hi: 88, half_life_minutes: 60, cap: 90 }])
    wrapper.unmount()
  })

  it('下限大于上限时不提交', async () => {
    const wrapper = mountModal(makeRow({ simulation: { enabled: true, lo: 80, hi: 88, half_life_minutes: 60, cap: 95 } }))
    await flushPromises()
    await wrapper.find('[data-testid="openai-cache-sim-modal-lo"]').setValue('90')
    await wrapper.find('[data-testid="openai-cache-sim-modal-save"]').trigger('click')
    await flushPromises()
    expect(updateSimulation).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('admin.accounts.openai.cacheSimulationRangeInvalid')
    wrapper.unmount()
  })

  it('已开启的账号可以在弹窗里关闭', async () => {
    updateSimulation.mockResolvedValue({ enabled: false, lo: 82, hi: 88, half_life_minutes: 60, cap: 95 })
    const wrapper = mountModal(makeRow({ simulation: { enabled: true, lo: 82, hi: 88, half_life_minutes: 60, cap: 95 } }))
    await flushPromises()
    await wrapper.find('[data-testid="openai-cache-sim-modal-toggle"]').trigger('click')
    await wrapper.find('[data-testid="openai-cache-sim-modal-save"]').trigger('click')
    await flushPromises()
    expect(updateSimulation).toHaveBeenCalledWith(112, { enabled: false, lo: 82, hi: 88, half_life_minutes: 60, cap: 95 })
    wrapper.unmount()
  })
})
