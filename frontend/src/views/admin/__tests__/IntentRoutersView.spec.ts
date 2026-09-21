import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import IntentRoutersView from '../IntentRoutersView.vue'

const api = vi.hoisted(() => ({
  list: vi.fn(), save: vi.fn(), remove: vi.fn(), test: vi.fn(), clearCache: vi.fn(), events: vi.fn(),
}))
const admin = vi.hoisted(() => ({ getAllIncludingInactive: vi.fn(), listAccounts: vi.fn() }))
const toast = vi.hoisted(() => ({ showSuccess: vi.fn(), showError: vi.fn() }))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) }),
  }
})
vi.mock('@/api/admin/intentRouters', () => ({ intentRoutersAPI: api, default: api }))
vi.mock('@/api/admin', () => ({
  adminAPI: { groups: { getAllIncludingInactive: admin.getAllIncludingInactive }, accounts: { list: admin.listAccounts } },
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => toast }))

const savedRouter = {
  group_id: 1, enabled: true, classifier_base_url: '', classifier_api_key_configured: true,
  classifier_protocol: 'openai_chat', classifier_model: 'flash-lite', classifier_timeout_ms: 3000,
  cache_ttl_seconds: 7200, max_input_chars: 2000, updated_at: '2026-09-21T00:00:00Z',
  rules: [{ name: 'coding', description: 'code', account_ids: [21, 99], enabled: true }],
}

async function mountView() {
  const wrapper = mount(IntentRoutersView, {
    global: { stubs: { AppLayout: { template: '<div><slot /></div>' } } },
  })
  await flushPromises()
  return wrapper
}

describe('IntentRoutersView', () => {
  beforeEach(() => {
    Object.values(api).forEach((fn) => fn.mockReset())
    Object.values(admin).forEach((fn) => fn.mockReset())
    Object.values(toast).forEach((fn) => fn.mockReset())
    admin.getAllIncludingInactive.mockResolvedValue([
      { id: 1, name: 'claude-main', platform: 'anthropic' },
      { id: 2, name: 'gpt-main', platform: 'openai' },
    ])
    admin.listAccounts.mockResolvedValue({ items: [
      { id: 21, name: 'claude-coding', platform: 'anthropic' },
      { id: 22, name: 'claude-chat', platform: 'anthropic' },
      { id: 31, name: 'gpt-only', platform: 'openai' },
    ] })
    api.list.mockResolvedValue({ data: [savedRouter] })
    api.events.mockResolvedValue({ data: [] })
  })

  it('opens the configured group, shows its rules and flags accounts that vanished', async () => {
    const wrapper = await mountView()

    expect(wrapper.get('[data-test="intent-group-1"]').text()).toContain('intentRouter.status.on')
    expect(wrapper.get('[data-test="intent-group-2"]').text()).toContain('intentRouter.status.none')
    expect((wrapper.get('[data-test="intent-rule-name"]').element as HTMLInputElement).value).toBe('coding')
    const rule = wrapper.get('[data-test="intent-rule-0"]').text()
    expect(rule).toContain('#21 claude-coding')
    expect(rule).toContain('intentRouter.rules.missingAccount')
    // The stored key is never sent to the browser.
    expect((wrapper.get('[data-test="intent-api-key"]').element as HTMLInputElement).value).toBe('')
    expect(wrapper.get('[data-test="intent-api-key"]').attributes('placeholder')).toBe('intentRouter.classifier.apiKeyKeep')
    expect((wrapper.get('[data-test="intent-cache-minutes"]').element as HTMLInputElement).value).toBe('120')
  })

  it('only offers accounts that can serve the group, and saves the edited document', async () => {
    api.save.mockImplementation(async (_id: number, input: Record<string, unknown>) => ({ data: { ...savedRouter, ...input, classifier_api_key_configured: true } }))
    const wrapper = await mountView()

    await wrapper.get('[data-test="intent-account-search"]').setValue('only')
    expect(wrapper.find('[data-test="intent-account-option-31"]').exists()).toBe(false) // an OpenAI account, a Claude group
    await wrapper.get('[data-test="intent-account-search"]').setValue('chat')
    await wrapper.get('[data-test="intent-account-option-22"]').trigger('click')
    await wrapper.get('[data-test="intent-cache-minutes"]').setValue('30')
    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()

    expect(api.save).toHaveBeenCalledTimes(1)
    const [groupId, input] = api.save.mock.calls[0]
    expect(groupId).toBe(1)
    expect(input.rules).toEqual([{ name: 'coding', description: 'code', account_ids: [21, 99, 22], enabled: true }])
    expect(input.cache_ttl_seconds).toBe(1800)
    expect(input.classifier_api_key).toBe('') // blank keeps the stored key
    expect(toast.showSuccess).toHaveBeenCalledWith('intentRouter.actions.saved')
  })

  it('tries a text against the saved router, but not while edits are unsaved', async () => {
    api.test.mockResolvedValue({ data: { answer: 'coding', intent: 'coding', understood: true, account_ids: [21], latency_ms: 420 } })
    const wrapper = await mountView()

    await wrapper.get('[data-test="intent-test-text"]').setValue('fix my go build')
    await wrapper.get('[data-test="intent-test-run"]').trigger('click')
    await flushPromises()
    expect(api.test).toHaveBeenCalledWith(1, 'fix my go build')
    expect(wrapper.get('[data-test="intent-test-result"]').text()).toContain('intentRouter.test.resultIntent')
    expect(wrapper.get('[data-test="intent-test-result"]').text()).toContain('#21 claude-coding')

    await wrapper.get('[data-test="intent-model"]').setValue('another-model')
    expect(wrapper.get('[data-test="intent-test-run"]').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('intentRouter.test.saveFirst')
  })

  it('reports a rejected save with the server reason translated', async () => {
    api.save.mockImplementation(() => Promise.reject(Object.assign(new Error('rule names must be unique: coding'),
      { reason: 'INTENT_RULE_NAME_DUPLICATE', metadata: { name: 'coding' } })))
    const wrapper = await mountView()
    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()
    expect(toast.showError).toHaveBeenCalledWith('intentRouter.errors.INTENT_RULE_NAME_DUPLICATE:{"name":"coding"}')
  })

  it('starts a blank form for a group that has no router yet', async () => {
    const wrapper = await mountView()
    await wrapper.get('[data-test="intent-group-2"]').trigger('click')
    expect(wrapper.find('[data-test="intent-rule-0"]').exists()).toBe(false)
    expect((wrapper.get('[data-test="intent-enabled"]').element as HTMLInputElement).checked).toBe(false)
    expect(wrapper.find('[data-test="intent-test-text"]').exists()).toBe(false) // nothing saved to try yet
    await wrapper.get('[data-test="intent-add-rule"]').trigger('click')
    expect(wrapper.find('[data-test="intent-rule-0"]').exists()).toBe(true)
  })
})
