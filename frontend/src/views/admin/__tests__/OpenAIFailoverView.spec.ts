import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OpenAIFailoverView from '../OpenAIFailoverView.vue'

const { getOverview, updateConfig, updateEncryptedArgs, checkEncryptedArgs, getAll } = vi.hoisted(() => ({
  getOverview: vi.fn(),
  updateConfig: vi.fn(),
  updateEncryptedArgs: vi.fn(),
  checkEncryptedArgs: vi.fn(),
  getAll: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    openaiFailover: { getOverview, updateConfig, updateEncryptedArgs, checkEncryptedArgs },
    groups: { getAll }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const config = () => ({
  enabled_group_ids: [4],
  slot_wait_ms: 3000,
  max_rule_switches: 3,
  max_slot_switches: 5,
  rules: [
    { id: 'a', name: 'A', enabled: true, status_code: 0, keywords: ['one', 'two'], action: 'failover' },
    { id: 'b', name: 'B', enabled: true, status_code: 408, keywords: [], action: 'failover' }
  ]
})

function mountView() {
  return mount(OpenAIFailoverView, { global: { stubs: { AppLayout: { template: '<div><slot /></div>' } } } })
}

describe('OpenAIFailoverView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getOverview.mockResolvedValue({
      config: config(),
      stats: { rules: [{ rule_id: 'a', hits: 4, recovered: 3 }], slot_switched: 1, slot_recovered: 1, rewritten_requests: 0, rewritten_items: 0 },
      recent: [],
      accounts: [{ id: 213, name: 'ly', type: 'apikey', status: 'active', schedulable: true, no_encrypted_args: false, can_check: true, default_check_model: 'gpt-6-astra' }]
    })
    getAll.mockResolvedValue([{ id: 3, name: 'G3' }, { id: 4, name: 'G4' }])
    updateConfig.mockImplementation(async (saved) => saved)
    updateEncryptedArgs.mockResolvedValue(undefined)
  })

  it('shows statistics and saves the table with group switches', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.findAll('[data-test="failover-rule"]')).toHaveLength(2)
    expect(wrapper.text()).toContain('75.0%')

    await wrapper.find('[data-test="failover-group-3"]').setValue(true)
    await wrapper.find('[data-test="failover-save"]').trigger('click')
    await flushPromises()

    const saved = updateConfig.mock.calls[0][0]
    expect(saved.enabled_group_ids).toEqual([4, 3])
    expect(saved.rules[0]).toMatchObject({ id: 'a', status_code: 0, keywords: ['one', 'two'] })
    expect(saved.rules[1]).toMatchObject({ id: 'b', status_code: 408, keywords: [] })
  })

  it('marks an account on its own endpoint', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.find('[data-test="failover-mark-213"]').setValue(true)
    await flushPromises()

    expect(updateEncryptedArgs).toHaveBeenCalledWith(213, true)
    expect(updateConfig).not.toHaveBeenCalled()
  })
})
