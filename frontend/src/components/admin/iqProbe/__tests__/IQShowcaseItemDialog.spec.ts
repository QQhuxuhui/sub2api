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

function mountDialog(editing: Record<string, unknown> | null = null) {
  return mount(IQShowcaseItemDialog, {
    props: { show: true, editing: editing as never, groups },
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

  const existing = {
    id: 3, title: 'pelican', group_id: 4, api_key_id: 9, protocol: 'openai_responses', model: 'gpt-x', prompt: 'draw',
    reasoning_effort: '', max_output_tokens: 0, interval_minutes: 60, max_cards: 12, enabled: true, sort_order: 0,
  }

  it('folds the advanced fields for new items and opens them when an item uses one', async () => {
    const created = mountDialog()
    await flushPromises()
    expect((created.find('[data-test="iq-showcase-advanced"]').element as HTMLDetailsElement).open).toBe(false)

    const plain = mountDialog(existing)
    await flushPromises()
    expect((plain.find('[data-test="iq-showcase-advanced"]').element as HTMLDetailsElement).open).toBe(false)

    const tuned = mountDialog({ ...existing, max_output_tokens: 4000 })
    await flushPromises()
    expect((tuned.find('[data-test="iq-showcase-advanced"]').element as HTMLDetailsElement).open).toBe(true)
  })

  it('still saves the advanced fields while the section is folded', async () => {
    updateItem.mockResolvedValue({ data: {} })
    const wrapper = mountDialog({ ...existing, reasoning_effort: 'high', sort_order: 2 })
    await flushPromises()
    const details = wrapper.find('[data-test="iq-showcase-advanced"]')
    ;(details.element as HTMLDetailsElement).open = false
    await details.trigger('toggle')
    await wrapper.find('[data-test="iq-showcase-max-tokens"]').setValue(900)
    await wrapper.find('[data-test="iq-showcase-save"]').trigger('click')
    await flushPromises()
    expect(updateItem).toHaveBeenCalledWith(3, expect.objectContaining({ reasoning_effort: 'high', max_output_tokens: 900, sort_order: 2 }))
  })

  it('explains what 0 means for the output limit per protocol', async () => {
    const wrapper = mountDialog()
    const hint = () => wrapper.find('[data-test="iq-showcase-max-tokens-hint"]').text()
    expect(hint()).toBe('iqShowcase.admin.fields.maxTokensHint')
    await wrapper.find('[data-test="iq-showcase-protocol"]').setValue('anthropic_messages')
    expect(hint()).toBe('iqShowcase.admin.fields.maxTokensHintAnthropic')
  })
})
