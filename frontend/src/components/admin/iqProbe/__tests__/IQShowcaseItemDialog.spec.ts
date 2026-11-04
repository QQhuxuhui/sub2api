import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import IQShowcaseItemDialog from '../IQShowcaseItemDialog.vue'

const { keyOptions, createItem, updateItem } = vi.hoisted(() => ({
  keyOptions: vi.fn(),
  createItem: vi.fn(),
  updateItem: vi.fn(),
}))

vi.mock('@/api/admin/iqShowcase', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/admin/iqShowcase')>()
  return { ...actual, iqShowcaseAdminAPI: { keyOptions, createItem, updateItem } }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }),
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const groups = [
  { id: 4, name: 'speed', platform: 'openai' },
  { id: 7, name: 'claude', platform: 'anthropic' },
]

function mountDialog() {
  return mount(IQShowcaseItemDialog, {
    props: { show: true, editing: null, groups },
    global: {
      stubs: {
        BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        Toggle: true,
      },
    },
  })
}

describe('IQShowcaseItemDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    keyOptions.mockResolvedValue({ data: [{ id: 9, name: 'probe', masked: 'sk-…ABCD', status: 'active', user_email: 'a@b.c' }] })
    createItem.mockResolvedValue({ data: {} })
  })

  it('loads masked keys for the picked group and presets the protocol', async () => {
    const wrapper = mountDialog()
    await wrapper.find('[data-test="iq-showcase-group"]').setValue(7)
    await flushPromises()
    expect(keyOptions).toHaveBeenLastCalledWith(7)
    expect(wrapper.find('[data-test="iq-showcase-key"]').text()).toContain('sk-…ABCD')
    expect((wrapper.find('[data-test="iq-showcase-protocol"]').element as HTMLSelectElement).value).toBe('anthropic_messages')
  })

  it('saves the form and drops reasoning effort for non-OpenAI protocols', async () => {
    const wrapper = mountDialog()
    await wrapper.find('[data-test="iq-showcase-title"]').setValue('pelican')
    await wrapper.find('[data-test="iq-showcase-group"]').setValue(7)
    await flushPromises()
    await wrapper.find('[data-test="iq-showcase-key"]').setValue(9)
    await wrapper.find('[data-test="iq-showcase-model"]').setValue('claude-x')
    await wrapper.find('[data-test="iq-showcase-prompt"]').setValue('draw a pelican as SVG')
    await wrapper.find('[data-test="iq-showcase-save"]').trigger('click')
    await flushPromises()
    expect(createItem).toHaveBeenCalledTimes(1)
    const input = createItem.mock.calls[0][0]
    expect(input).toMatchObject({ title: 'pelican', group_id: 7, api_key_id: 9, protocol: 'anthropic_messages', model: 'claude-x', reasoning_effort: '' })
    expect(wrapper.emitted('saved')).toBeTruthy()
  })
})
