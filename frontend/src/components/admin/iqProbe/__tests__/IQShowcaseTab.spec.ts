import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import IQShowcaseTab from '../IQShowcaseTab.vue'

const { listItems, setEnabled, updateItem } = vi.hoisted(() => ({
  listItems: vi.fn(),
  setEnabled: vi.fn(),
  updateItem: vi.fn(),
}))

vi.mock('@/api/admin/iqShowcase', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/admin/iqShowcase')>()
  return { ...actual, iqShowcaseAdminAPI: { listItems, setEnabled, updateItem } }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }),
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const brokenItem = {
  id: 3, title: 'pelican', group_id: 4, api_key_id: null, protocol: 'openai_responses', model: 'gpt-x',
  prompt: 'p', reasoning_effort: '', max_output_tokens: 0, interval_minutes: 60, max_cards: 12, enabled: true,
  sort_order: 0, next_run_at: '', lease_until: null, config_rev: 1, created_at: '', updated_at: '',
  group_name: 'speed', group_platform: 'openai', api_key_name: '', api_key_masked: '',
  last_result_at: null, last_success: null, success_cards: 2, recent_failures: 5,
}

describe('IQShowcaseTab', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    listItems.mockResolvedValue({ data: [brokenItem] })
    setEnabled.mockResolvedValue({ data: { ...brokenItem, enabled: false } })
  })

  it('switches an item off through the switch-only endpoint, even when its key is gone', async () => {
    const wrapper = mount(IQShowcaseTab, {
      props: { groups: [] },
      global: {
        stubs: {
          IQShowcaseItemDialog: true,
          IQShowcaseResultsDialog: true,
          Toggle: { props: ['modelValue'], emits: ['update:modelValue'], template: '<button data-test="toggle" @click="$emit(\'update:modelValue\', !modelValue)" />' },
        },
      },
    })
    await flushPromises()
    expect(wrapper.text()).toContain('iqShowcase.admin.keyMissing')
    await wrapper.find('[data-test="toggle"]').trigger('click')
    await flushPromises()
    expect(setEnabled).toHaveBeenCalledWith(3, false)
    expect(updateItem).not.toHaveBeenCalled()
  })
})
