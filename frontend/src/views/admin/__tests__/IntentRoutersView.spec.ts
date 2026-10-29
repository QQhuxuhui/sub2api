import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import IntentRoutersView from '../IntentRoutersView.vue'

const api = vi.hoisted(() => ({
  list: vi.fn(), save: vi.fn(), remove: vi.fn(), test: vi.fn(), clearCache: vi.fn(), events: vi.fn(),
  hits: vi.fn(), hitStats: vi.fn(), jevStatus: vi.fn(), monitorAnswer: vi.fn(), saveMonitorAnswer: vi.fn(), monitorAnswerStatus: vi.fn(),
}))
const admin = vi.hoisted(() => ({ getAllIncludingInactive: vi.fn(), listAccounts: vi.fn(), searchUsers: vi.fn(), searchApiKeys: vi.fn() }))
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
  adminAPI: {
    groups: { getAllIncludingInactive: admin.getAllIncludingInactive }, accounts: { list: admin.listAccounts },
    usage: { searchUsers: admin.searchUsers, searchApiKeys: admin.searchApiKeys },
  },
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

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

function keywordStat(keyword: string) {
  return { keyword, hits: 1, users: 1, api_keys: 1, avg_chars: 10, avg_input_tokens: 100, total_cost: 0, last_hit: '2026-10-23T09:00:00Z' }
}

function hitRow(excerpt: string) {
  return { id: 1, time: '2026-10-23T09:00:00Z', group_id: 1, user_id: 1, keyword: 'coding', excerpt, user_turn: 0, message_chars: 10 }
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
    api.hitStats.mockResolvedValue({ data: [] })
    api.hits.mockResolvedValue({ data: { items: [], total: 0 } })
    api.jevStatus.mockResolvedValue({ data: { state: 'off', days: [], usd_per_mtok: 0.042, cny_per_usd: 7 } })
    api.monitorAnswer.mockResolvedValue({ data: { group_id: 1, config: { enabled: false, scope: 'all', user_ids: [], api_keys: [] }, scope_labels: { users: [], api_keys: [] } } })
    api.monitorAnswerStatus.mockResolvedValue({ data: { days: [], recent: [] } })
  })

  it('keeps statistics and details on the newest group when requests finish out of order', async () => {
    api.list.mockResolvedValue({ data: [savedRouter, { ...savedRouter, group_id: 2 }] })
    const wrapper = await mountView()
    const old = deferred<{ data: ReturnType<typeof keywordStat>[] }>()
    const current = deferred<{ data: ReturnType<typeof keywordStat>[] }>()
    api.hitStats.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    api.hits.mockImplementation(async (q: { group_id: number }) => ({ data: { items: [hitRow(`detail-${q.group_id}`)], total: 1 } }))
    const group = wrapper.get('[data-test="intent-hits"]').findAll('select')[0]
    await group.setValue(1)
    await group.setValue(2)
    current.resolve({ data: [keywordStat('group-two')] })
    await flushPromises()
    old.resolve({ data: [keywordStat('group-one')] })
    await flushPromises()
    const section = wrapper.get('[data-test="intent-hits"]')
    expect(section.text()).toContain('group-two')
    expect(section.text()).toContain('detail-2')
    expect(section.text()).not.toContain('group-one')
    wrapper.unmount()
  })

  it('ignores stale detail responses after a newer keyword is selected', async () => {
    api.hitStats.mockResolvedValue({ data: [keywordStat('one'), keywordStat('two')] })
    const wrapper = await mountView()
    const old = deferred<{ data: { items: ReturnType<typeof hitRow>[]; total: number } }>()
    const current = deferred<{ data: { items: ReturnType<typeof hitRow>[]; total: number } }>()
    api.hits.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const rows = wrapper.get('[data-test="intent-hits"]').findAll('tbody')[0].findAll('tr')
    await rows[0].get('button').trigger('click')
    await rows[1].get('button').trigger('click')
    current.resolve({ data: { items: [hitRow('newest-details')], total: 1 } })
    await flushPromises()
    old.resolve({ data: { items: [hitRow('stale-details')], total: 2 } })
    await flushPromises()
    expect(wrapper.text()).toContain('newest-details')
    expect(wrapper.text()).not.toContain('stale-details')
    wrapper.unmount()
  })

  it('does not show errors from an obsolete filter or an unmounted view', async () => {
    const wrapper = await mountView()
    const old = deferred<never>()
    api.hitStats.mockReturnValueOnce(old.promise).mockResolvedValueOnce({ data: [] })
    const hours = wrapper.get('[data-test="intent-hits"]').findAll('select')[1]
    await hours.setValue(1)
    await hours.setValue(168)
    await flushPromises()
    old.reject(new Error('obsolete error'))
    await flushPromises()
    expect(toast.showError).not.toHaveBeenCalled()
    const gone = deferred<never>()
    api.hitStats.mockReturnValueOnce(gone.promise)
    await hours.setValue(720)
    wrapper.unmount()
    gone.reject(new Error('unmounted'))
    await flushPromises()
    expect(toast.showError).not.toHaveBeenCalled()
  })

  it('reviews keyword hits: flags long-context keywords and drills into one keyword', async () => {
    api.hitStats.mockResolvedValue({ data: [
      { keyword: '动画', hits: 900, users: 2, api_keys: 2, avg_chars: 3000, avg_input_tokens: 95000, total_cost: 812.5, last_hit: '2026-10-23T09:00:00Z' },
      { keyword: 'reply with ok.\n<<<', hits: 12, users: 1, api_keys: 1, avg_chars: 40, avg_input_tokens: 60, total_cost: 0.01, last_hit: '2026-10-23T09:00:00Z' },
    ] })
    api.hits.mockResolvedValue({ data: { items: [
      { id: 7, time: '2026-10-23T09:00:00Z', group_id: 1, user_id: 14, api_key_id: 3, request_id: 'client:x', endpoint: '/v1/responses', model: 'gpt-6-astra',
        rule: 'coding', keyword: '动画', user_turn: 1, message_chars: 3000, excerpt: '…给按钮加个动画效果…', account_id: 21, input_tokens: 95000, total_cost: 0.9 },
    ], total: 1 } })
    const wrapper = await mountView()

    expect(api.hitStats).toHaveBeenCalledWith({ group_id: undefined, hours: 24 })
    const section = wrapper.get('[data-test="intent-hits"]')
    const rows = section.findAll('tbody')[0].findAll('tr')
    expect(rows[0].text()).toContain('intentRouter.hits.suspect')
    expect(rows[1].text()).not.toContain('intentRouter.hits.suspect')
    expect(rows[1].text()).toContain('reply with ok.\\n<<<') // the line break is shown, not rendered
    expect(section.get('mark').text()).toBe('动画')
    expect(section.text()).toContain('intentRouter.hits.turnPrevious')
    expect(section.text()).toContain('#21 claude-coding')

    await rows[0].get('button').trigger('click')
    await flushPromises()
    expect(api.hits).toHaveBeenLastCalledWith({ group_id: undefined, hours: 24, keyword: '动画', page: 1, page_size: 50 })
  })

  it('opens the configured group, shows its rules and flags accounts that vanished', async () => {
    const wrapper = await mountView()

    expect(wrapper.get('[data-test="intent-group-1"]').text()).toContain('intentRouter.status.on')
    expect(wrapper.get('[data-test="intent-group-2"]').text()).toContain('intentRouter.status.none')
    expect((wrapper.get('[data-test="intent-rule-name"]').element as HTMLInputElement).value).toBe('coding')
    const rule = wrapper.get('[data-test="intent-rule-0"]').text()
    expect(rule).toContain('#21 claude-coding')
    expect(rule).toContain('intentRouter.rules.missingAccount')
    // The model-classifier settings are no longer shown (Jev replaced them).
    expect(wrapper.find('[data-test="intent-model"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="intent-api-key"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="intent-rule-description"]').exists()).toBe(false)
  })

  it('only offers accounts that can serve the group, and saves the edited document', async () => {
    api.save.mockImplementation(async (_id: number, input: Record<string, unknown>) => ({ data: { ...savedRouter, ...input, classifier_api_key_configured: true } }))
    const wrapper = await mountView()

    await wrapper.get('[data-test="intent-account-search"]').setValue('only')
    expect(wrapper.find('[data-test="intent-account-option-31"]').exists()).toBe(false) // an OpenAI account, a Claude group
    await wrapper.get('[data-test="intent-account-search"]').setValue('chat')
    await wrapper.get('[data-test="intent-account-option-22"]').trigger('click')
    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()

    expect(api.save).toHaveBeenCalledTimes(1)
    const [groupId, input] = api.save.mock.calls[0]
    expect(groupId).toBe(1)
    expect(input.rules).toEqual([{ name: 'coding', description: 'code', keywords: [], account_ids: [21, 99, 22], enabled: true }])
    // Hidden classifier settings and rule descriptions are saved back unchanged.
    expect(input).toMatchObject({ classifier_model: 'flash-lite', classifier_timeout_ms: 3000, cache_ttl_seconds: 7200, max_input_chars: 2000 })
    expect(input.classifier_api_key).toBe('') // blank keeps the stored key
    expect(toast.showSuccess).toHaveBeenCalledWith('intentRouter.actions.saved')
  })

  it('tries a text against the saved router, but not while edits are unsaved', async () => {
    api.test.mockResolvedValue({ data: { matched_by: 'classifier', answer: 'coding', intent: 'coding', understood: true, account_ids: [21], latency_ms: 420 } })
    const wrapper = await mountView()

    await wrapper.get('[data-test="intent-test-text"]').setValue('fix my go build')
    await wrapper.get('[data-test="intent-test-run"]').trigger('click')
    await flushPromises()
    expect(api.test).toHaveBeenCalledWith(1, 'fix my go build')
    expect(wrapper.get('[data-test="intent-test-result"]').text()).toContain('intentRouter.test.resultIntent')
    expect(wrapper.get('[data-test="intent-test-result"]').text()).toContain('#21 claude-coding')

    await wrapper.get('[data-test="intent-jev-threshold"]').setValue('0.6')
    expect(wrapper.get('[data-test="intent-test-run"]').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('intentRouter.test.saveFirst')
  })

  it('edits keywords as one text field and saves them as a list', async () => {
    api.list.mockResolvedValue({ data: [{ ...savedRouter, rules: [{ ...savedRouter.rules[0], keywords: ['Traceback', '编译'] }] }] })
    api.save.mockImplementation(async (_id: number, input: Record<string, unknown>) => ({ data: { ...savedRouter, ...input } }))
    const wrapper = await mountView()

    const field = wrapper.get('[data-test="intent-rule-keywords"]')
    expect((field.element as HTMLTextAreaElement).value).toBe('Traceback, 编译')
    expect(wrapper.text()).not.toContain('intentRouter.test.saveFirst') // loading keywords does not count as an edit

    await field.setValue('Traceback\n编译，报错、 build failed ,,')
    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()
    expect(api.save.mock.calls[0][1].rules[0].keywords).toEqual(['Traceback', '编译', '报错', 'build failed'])
  })

  it('preserves saved keywords containing delimiters when other settings change', async () => {
    const keywords = ['foo, bar', 'say "hello"', '编译']
    api.list.mockResolvedValue({ data: [{ ...savedRouter, rules: [{ ...savedRouter.rules[0], keywords }] }] })
    api.save.mockImplementation(async (_id: number, input: Record<string, unknown>) => ({ data: { ...savedRouter, ...input } }))
    const wrapper = await mountView()

    const field = wrapper.get('[data-test="intent-rule-keywords"]')
    expect((field.element as HTMLTextAreaElement).value).toBe('"foo, bar", "say ""hello""", 编译')
    await wrapper.get('[data-test="intent-jev-threshold"]').setValue('0.6')
    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()
    expect(api.save.mock.calls[0][1].rules[0].keywords).toEqual(keywords)
  })

  it('preserves a saved keyword with a carriage return when another field changes', async () => {
    const keywords = ['foo\rbar']
    api.list.mockResolvedValue({ data: [{ ...savedRouter, rules: [{ ...savedRouter.rules[0], keywords }] }] })
    api.save.mockImplementation(async (_id: number, input: Record<string, unknown>) => ({ data: { ...savedRouter, ...input } }))
    const wrapper = await mountView()
    await wrapper.get('[data-test="intent-jev-threshold"]').setValue('0.6')
    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()
    expect(api.save.mock.calls[0][1].rules[0].keywords).toEqual(keywords)
  })

  it('keeps an existing carriage return keyword when the keywords field is edited', async () => {
    const keywords = ['foo\rbar']
    api.list.mockResolvedValue({ data: [{ ...savedRouter, rules: [{ ...savedRouter.rules[0], keywords }] }] })
    api.save.mockImplementation(async (_id: number, input: Record<string, unknown>) => ({ data: { ...savedRouter, ...input } }))
    const wrapper = await mountView()
    const field = wrapper.get('[data-test="intent-rule-keywords"]')
    await field.setValue(`${(field.element as HTMLTextAreaElement).value}, extra`)
    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()
    expect(api.save.mock.calls[0][1].rules[0].keywords).toEqual(['foo\rbar', 'extra'])
  })

  it('keeps a manually entered Windows path literal inside a quoted keyword', async () => {
    api.save.mockImplementation(async (_id: number, input: Record<string, unknown>) => ({ data: { ...savedRouter, ...input } }))
    const wrapper = await mountView()
    await wrapper.get('[data-test="intent-rule-keywords"]').setValue('"C:\\repo, test"')
    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()
    expect(api.save.mock.calls[0][1].rules[0].keywords).toEqual(['C:\\repo, test'])
  })

  it('reads a router saved before Jev existed with Jev off and the defaults filled in', async () => {
    api.save.mockImplementation(async (_id: number, input: Record<string, unknown>) => ({ data: { ...savedRouter, ...input } }))
    const wrapper = await mountView()
    expect((wrapper.get('[data-test="intent-jev-enabled"]').element as HTMLInputElement).checked).toBe(false)
    expect((wrapper.get('[data-test="intent-jev-threshold"]').element as HTMLInputElement).value).toBe('0.5')
    expect(wrapper.get('[data-test="intent-jev-api-key"]').attributes('placeholder')).toBe('intentRouter.jev.apiKeyPlaceholder')
    expect(wrapper.find('[data-test="intent-jev-user-search"]').exists()).toBe(false)

    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()
    const [, input] = api.save.mock.calls[0]
    expect(input.jev).toEqual({ enabled: false, rule: '', threshold: 0.5, max_turns: 3, timeout_ms: 1000, model: 'jev-latest', scope: 'all', user_ids: [], api_keys: [], sample_percent: 100, health_enabled: false, health_threshold: 0.7, health_rule: '', low_balance_usd: 1, balance_usd: 0 })
    expect(input.jev_api_key).toBe('')
    expect(input).not.toHaveProperty('jev_set_balance_usd') // an empty balance field changes nothing
  })

  it('turns Jev on for users picked by search, narrowing one of them to a chosen key', async () => {
    vi.useFakeTimers()
    api.save.mockImplementation(async (_id: number, input: Record<string, unknown>) => ({ data: { ...savedRouter, ...input, jev_api_key_configured: true } }))
    admin.searchUsers.mockResolvedValue([{ id: 14, email: 'buyer@qq.com', deleted: false }, { id: 3, email: 'gone@x.com', deleted: true }])
    // Newest 30 when nothing is typed; older keys only by name.
    const newest = Array.from({ length: 30 }, (_, i) => ({ id: 100 + i, name: `k${i}`, user_id: 14 }))
    admin.searchApiKeys.mockImplementation(async (userId: number, q?: string) => (userId !== 14 ? []
      : q ? [{ id: 22, name: '好蓝', user_id: 14 }] : newest))
    const wrapper = await mountView()
    await wrapper.get('[data-test="intent-jev-enabled"]').setValue(true)
    const rule = wrapper.get('[data-test="intent-jev-rule"]')
    expect(rule.findAll('option').map((o) => o.text())).toEqual(['intentRouter.jev.rulePlaceholder', 'coding'])
    expect(wrapper.find('[data-test="intent-jev-rule-accounts"]').exists()).toBe(false)
    await rule.setValue('coding')
    // The chosen rule is only a destination: its accounts are shown right there.
    expect(wrapper.get('[data-test="intent-jev-rule-accounts"]').text()).toContain('#21 claude-coding')
    await wrapper.get('[data-test="intent-jev-api-key"]').setValue('jev-secret')
    await wrapper.get('[data-test="intent-jev-threshold"]').setValue('0.7')
    await wrapper.get('[data-test="intent-jev-scope-listed"]').setValue(true)
    expect((wrapper.get('[data-test="intent-jev-key-search"]').element as HTMLInputElement).disabled).toBe(true)

    await wrapper.get('[data-test="intent-jev-user-search"]').setValue('qq')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
    expect(admin.searchUsers).toHaveBeenCalledWith('qq')
    expect(wrapper.find('[data-test="intent-jev-user-option-3"]').exists()).toBe(false) // deleted users are not offered
    await wrapper.get('[data-test="intent-jev-user-option-14"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="intent-jev-user-14"]').text()).toContain('buyer@qq.com')

    const keySearch = wrapper.get('[data-test="intent-jev-key-search"]')
    await keySearch.trigger('focus')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
    expect(admin.searchApiKeys).toHaveBeenLastCalledWith(14, undefined)
    const results = wrapper.get('[data-test="intent-jev-key-results"]')
    expect(results.findAll('[data-test^="intent-jev-key-option-"]')).toHaveLength(30)
    expect(results.text()).toContain('intentRouter.jev.scopeKeyMore')
    expect(wrapper.find('[data-test="intent-jev-key-option-22"]').exists()).toBe(false) // older than the newest 30…

    await keySearch.setValue('好蓝') // …so it is found by name
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
    expect(admin.searchApiKeys).toHaveBeenLastCalledWith(14, '好蓝')
    await wrapper.get('[data-test="intent-jev-key-option-22"]').trigger('click')
    expect(wrapper.get('[data-test="intent-jev-key-22"]').text()).toContain('buyer@qq.com')
    expect(wrapper.find('[data-test="intent-jev-key-option-22"]').exists()).toBe(false) // chosen keys are not offered again

    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()
    const [, input] = api.save.mock.calls[0]
    expect(input.jev_api_key).toBe('jev-secret')
    expect(input.jev).toMatchObject({ enabled: true, rule: 'coding', threshold: 0.7, scope: 'listed', user_ids: [14], api_keys: [{ id: 22 }] })
    expect((wrapper.get('[data-test="intent-jev-api-key"]').element as HTMLInputElement).value).toBe('')
    expect(wrapper.get('[data-test="intent-jev-api-key"]').attributes('placeholder')).toBe('intentRouter.classifier.apiKeyKeep')
    vi.useRealTimers()
  })

  it('shows saved scope users and keys by name, and drops a removed user\'s keys', async () => {
    const router = { ...savedRouter, jev_api_key_configured: true,
      jev: { enabled: true, rule: 'coding', threshold: 0.5, max_turns: 3, timeout_ms: 1000, model: 'jev-latest', scope: 'listed', user_ids: [14, 8], api_keys: [{ id: 22, user_id: 14 }], sample_percent: 100, low_balance_usd: 1, balance_usd: 0 },
      jev_scope: { users: [{ id: 14, email: 'buyer@qq.com', username: '' }], api_keys: [{ id: 22, name: '好蓝', user_id: 14 }] } }
    api.list.mockResolvedValue({ data: [router] })
    admin.searchApiKeys.mockResolvedValue([])
    api.save.mockImplementation(async (_id: number, input: Record<string, unknown>) => ({ data: { ...router, ...input } }))
    const wrapper = await mountView()
    expect(wrapper.get('[data-test="intent-jev-user-14"]').text()).toContain('buyer@qq.com')
    expect(wrapper.get('[data-test="intent-jev-user-8"]').text()).toContain('intentRouter.jev.scopeDeleted')
    expect(wrapper.get('[data-test="intent-jev-key-22"]').text()).toContain('好蓝')

    await wrapper.get('[data-test="intent-jev-user-14"]').get('button').trigger('click')
    expect(wrapper.find('[data-test="intent-jev-key-22"]').exists()).toBe(false)
    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()
    expect(api.save.mock.calls[0][1].jev).toMatchObject({ user_ids: [8], api_keys: [] })
  })

  it('shows Jev status, daily usage and a low estimated balance', async () => {
    api.jevStatus.mockResolvedValue({ data: {
      state: 'paused', paused_until: '2026-10-29T06:01:00Z', reason: 'jev returned 529: overloaded', usd_per_mtok: 0.042, cny_per_usd: 7,
      days: [{ date: '20261029', calls: 120, skipped: 30, probes: 40, errors: 2, tokens: 70000, cost_usd: 0.00294 }],
      balance: { set_usd: 1.2, set_at: '2026-10-28T06:00:00Z', spent_tokens: 10_000_000, spent_usd: 0.42, estimated_usd: 0.78, low_balance_usd: 1, low: true },
    } })
    const wrapper = await mountView()
    const status = wrapper.get('[data-test="intent-jev-status"]')
    expect(status.text()).toContain('intentRouter.jev.status.paused')
    expect(status.text()).toContain('jev returned 529')
    const day = status.get('[data-test="intent-jev-day-20261029"]').text()
    expect(day).toContain('10-29')
    expect(day).toContain('$0.0029 / ¥0.0206')
    const balance = status.get('[data-test="intent-jev-balance-estimate"]')
    expect(balance.text()).toContain('"estimated":"$0.78"')
    expect(balance.text()).toContain('intentRouter.jev.status.lowBalance')
    expect(balance.classes()).toContain('text-red-700')
  })

  it('sends a newly entered balance once, and follows a switch-off made by the server', async () => {
    const enabledRouter = { ...savedRouter, jev_api_key_configured: true, jev: { enabled: true, rule: 'coding', threshold: 0.5, max_turns: 3, timeout_ms: 1000, model: 'jev-latest', scope: 'all', user_ids: [], api_keys: [], sample_percent: 80, low_balance_usd: 1, balance_usd: 0 } }
    api.list.mockResolvedValue({ data: [enabledRouter] })
    api.jevStatus.mockResolvedValue({ data: { state: 'auto_disabled', auto_disabled_status: 402, auto_disabled_detail: 'payment required', auto_disabled_at: '2026-10-29T06:00:00Z', days: [], usd_per_mtok: 0.042, cny_per_usd: 7 } })
    api.save.mockImplementation(async (_id: number, input: Record<string, unknown>) => ({ data: { ...enabledRouter, ...input } }))
    const wrapper = await mountView()

    expect(wrapper.get('[data-test="intent-jev-auto-disabled"]').text()).toContain('intentRouter.jev.status.why402')
    expect((wrapper.get('[data-test="intent-jev-enabled"]').element as HTMLInputElement).checked).toBe(false)
    expect((wrapper.get('[data-test="intent-jev-sample"]').element as HTMLInputElement).value).toBe('80')

    await wrapper.get('[data-test="intent-jev-balance"]').setValue('12.5')
    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()
    const [, input] = api.save.mock.calls[0]
    expect(input.jev_set_balance_usd).toBe(12.5)
    expect(input.jev.enabled).toBe(false) // the stale "on" is not saved back
    expect((wrapper.get('[data-test="intent-jev-balance"]').element as HTMLInputElement).value).toBe('')
  })

  it('ignores a switched-off status that was requested before the admin re-enabled and saved', async () => {
    const jev = { enabled: false, rule: 'coding', threshold: 0.5, max_turns: 3, timeout_ms: 1000, model: 'jev-latest', scope: 'all', user_ids: [], api_keys: [], sample_percent: 100, low_balance_usd: 1, balance_usd: 0, auto_disabled_status: 402, auto_disabled_at: '2026-10-29T06:00:00Z' }
    const router = { ...savedRouter, jev_api_key_configured: true, jev }
    api.list.mockResolvedValue({ data: [router] })
    const stale = deferred<{ data: unknown }>()
    api.jevStatus.mockReturnValueOnce(stale.promise).mockResolvedValue({ data: { state: 'normal', days: [], usd_per_mtok: 0.042, cny_per_usd: 7 } })
    api.save.mockImplementation(async (_id: number, input: { jev: object }) => ({ data: { ...router, ...input, jev: { ...jev, ...input.jev, enabled: true, auto_disabled_status: undefined, auto_disabled_at: undefined } } }))
    const wrapper = await mountView()

    await wrapper.get('[data-test="intent-jev-enabled"]').setValue(true)
    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()
    // The status asked for before the save now arrives, still saying "switched off".
    stale.resolve({ data: { state: 'auto_disabled', auto_disabled_status: 402, auto_disabled_at: '2026-10-29T06:00:00Z', days: [], usd_per_mtok: 0.042, cny_per_usd: 7 } })
    await flushPromises()

    expect((wrapper.get('[data-test="intent-jev-enabled"]').element as HTMLInputElement).checked).toBe(true)
    expect(wrapper.find('[data-test="intent-jev-auto-disabled"]').exists()).toBe(false)
    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()
    expect(api.save.mock.calls[1][1].jev.enabled).toBe(true)
  })

  it('edits the models that skip intent routing as one text field', async () => {
    api.list.mockResolvedValue({ data: [{ ...savedRouter, excluded_models: ['codex-auto-review'] }] })
    api.save.mockImplementation(async (_id: number, input: Record<string, unknown>) => ({ data: { ...savedRouter, ...input } }))
    const wrapper = await mountView()
    const field = wrapper.get('[data-test="intent-excluded-models"]')
    expect((field.element as HTMLTextAreaElement).value).toBe('codex-auto-review')
    await field.setValue('codex-auto-review\n gemini-* ，, \n')
    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()
    expect(api.save.mock.calls[0][1].excluded_models).toEqual(['codex-auto-review', 'gemini-*'])
  })

  it('loads and saves the keyword length limit', async () => {
    api.list.mockResolvedValue({ data: [{ ...savedRouter, keyword_max_chars: 500 }] })
    api.save.mockImplementation(async (_id: number, input: Record<string, unknown>) => ({ data: { ...savedRouter, ...input } }))
    const wrapper = await mountView()
    const field = wrapper.get('[data-test="intent-keyword-max-chars"]')
    expect((field.element as HTMLInputElement).value).toBe('500')
    await field.setValue('800')
    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()
    expect(api.save.mock.calls[0][1].keyword_max_chars).toBe(800)
  })

  it('treats a missing or cleared keyword length limit as no limit', async () => {
    api.save.mockImplementation(async (_id: number, input: Record<string, unknown>) => ({ data: { ...savedRouter, ...input } }))
    const wrapper = await mountView()
    const field = wrapper.get('[data-test="intent-keyword-max-chars"]')
    expect((field.element as HTMLInputElement).value).toBe('0')
    await field.setValue('')
    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()
    expect(api.save.mock.calls[0][1].keyword_max_chars).toBe(0)
  })

  it('picks the status sent when target accounts are unavailable, 503 by default', async () => {
    api.save.mockImplementation(async (_id: number, input: Record<string, unknown>) => ({ data: { ...savedRouter, ...input } }))
    const wrapper = await mountView()
    const field = wrapper.get('[data-test="intent-unavailable-status"]')
    expect((field.element as HTMLSelectElement).value).toBe('503')
    await field.setValue('429')
    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()
    expect(api.save.mock.calls[0][1].unavailable_status).toBe(429)
  })

  it('edits the Jev health-check settings', async () => {
    api.save.mockImplementation(async (_id: number, input: Record<string, unknown>) => ({ data: { ...savedRouter, ...input } }))
    const wrapper = await mountView()
    expect(wrapper.find('[data-test="intent-jev-health-threshold"]').exists()).toBe(false)
    await wrapper.get('[data-test="intent-jev-health-enabled"]').setValue(true)
    expect((wrapper.get('[data-test="intent-jev-health-threshold"]').element as HTMLInputElement).value).toBe('0.7')
    await wrapper.get('[data-test="intent-jev-health-threshold"]').setValue('0.8')
    await wrapper.get('[data-test="intent-jev-health-rule"]').setValue('coding')
    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()
    expect(api.save.mock.calls[0][1].jev).toMatchObject({ health_enabled: true, health_threshold: 0.8, health_rule: 'coding' })
  })

  it('leaves a stale health-check rule for the server to resolve, names matched as it does', async () => {
    // The server matches rule names case-insensitively and drops references to
    // rules that are gone while health checks are off; the page sends as is.
    const router = { ...savedRouter, rules: [...savedRouter.rules, { name: 'health', description: '', account_ids: [21], enabled: true }],
      jev: { enabled: false, rule: 'coding', threshold: 0.5, max_turns: 3, timeout_ms: 1000, model: 'jev-latest', scope: 'all', user_ids: [], api_keys: [], sample_percent: 100,
        health_enabled: true, health_threshold: 0.7, health_rule: 'health', low_balance_usd: 1, balance_usd: 0 } }
    api.list.mockResolvedValue({ data: [router] })
    api.save.mockImplementation(async (_id: number, input: Record<string, unknown>) => ({ data: { ...router, ...input } }))
    const wrapper = await mountView()
    await wrapper.get('[data-test="intent-jev-health-enabled"]').setValue(false)
    await wrapper.findAll('[data-test="intent-rule-name"]')[1].setValue('Health')
    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()
    expect(api.save.mock.calls[0][1].jev).toMatchObject({ health_enabled: false, health_rule: 'health' })
  })

  it('shows the health-check answer of a try-it result', async () => {
    api.test.mockResolvedValue({ data: { matched_by: 'jev', answer: 'p=0.95', intent: '', understood: true, account_ids: [], latency_ms: 80, probe_probability: 0.95, health_probability: 0.973, health_check: true } })
    const wrapper = await mountView()
    await wrapper.get('[data-test="intent-test-text"]').setValue('Reply OK')
    await wrapper.get('[data-test="intent-test-run"]').trigger('click')
    await flushPromises()
    const text = wrapper.get('[data-test="intent-test-result"]').text()
    expect(text).toContain('intentRouter.test.byJevHealthHit:{"p":"0.97"}')
    expect(text).toContain('intentRouter.test.resultNone')
  })

  it('shows the Jev probability of a try-it result', async () => {
    api.test.mockResolvedValue({ data: { matched_by: 'jev', answer: 'p=0.91', intent: 'coding', understood: true, account_ids: [21], latency_ms: 80, probe_probability: 0.912 } })
    const wrapper = await mountView()
    await wrapper.get('[data-test="intent-test-text"]').setValue('Reply OK')
    await wrapper.get('[data-test="intent-test-run"]').trigger('click')
    await flushPromises()
    const text = wrapper.get('[data-test="intent-test-result"]').text()
    expect(text).toContain('intentRouter.test.byJev:{"p":"0.91"}')
    expect(text).toContain('intentRouter.test.latency:{"ms":80}')
  })

  it('says when a keyword decided the try-it result', async () => {
    api.test.mockResolvedValue({ data: { matched_by: 'keyword', keyword: 'traceback', answer: '', intent: 'coding', understood: true, account_ids: [21], latency_ms: 0 } })
    const wrapper = await mountView()
    await wrapper.get('[data-test="intent-test-text"]').setValue('a traceback')
    await wrapper.get('[data-test="intent-test-run"]').trigger('click')
    await flushPromises()
    const text = wrapper.get('[data-test="intent-test-result"]').text()
    expect(text).toContain('intentRouter.test.byKeyword:{"keyword":"traceback"}')
    expect(text).not.toContain('intentRouter.test.answer')
  })

  it('reports a rejected save with the server reason translated', async () => {
    api.save.mockImplementation(() => Promise.reject(Object.assign(new Error('rule names must be unique: coding'),
      { reason: 'INTENT_RULE_NAME_DUPLICATE', metadata: { name: 'coding' } })))
    const wrapper = await mountView()
    await wrapper.get('[data-test="intent-save"]').trigger('click')
    await flushPromises()
    expect(toast.showError).toHaveBeenCalledWith('intentRouter.errors.INTENT_RULE_NAME_DUPLICATE:{"name":"coding"}')
  })

  it('never lets a late answer for one group land in the form of another', async () => {
    let finishSave: (value: unknown) => void = () => {}
    api.save.mockImplementation(() => new Promise((resolve) => { finishSave = resolve }))
    const wrapper = await mountView()

    await wrapper.get('[data-test="intent-jev-threshold"]').setValue('0.8')
    await wrapper.get('[data-test="intent-save"]').trigger('click') // save of group 1 is in flight…
    await wrapper.get('[data-test="intent-group-2"]').trigger('click') // …and the admin moves on to group 2
    expect((wrapper.get('[data-test="intent-jev-threshold"]').element as HTMLInputElement).value).toBe('0.5')

    finishSave({ data: { ...savedRouter, jev: { enabled: false, rule: '', threshold: 0.8, max_turns: 3, timeout_ms: 1000, model: 'jev-latest', scope: 'all', user_ids: [], api_keys: [], sample_percent: 100, low_balance_usd: 1, balance_usd: 0 } } })
    await flushPromises()

    // Group 2's blank form is untouched, so saving now cannot write group 1's rules into it.
    expect((wrapper.get('[data-test="intent-jev-threshold"]').element as HTMLInputElement).value).toBe('0.5')
    expect(wrapper.find('[data-test="intent-rule-0"]').exists()).toBe(false)
    // Group 1 did get its result.
    await wrapper.get('[data-test="intent-group-1"]').trigger('click')
    expect((wrapper.get('[data-test="intent-jev-threshold"]').element as HTMLInputElement).value).toBe('0.8')
  })

  it('drops a try-it result that arrives after switching groups', async () => {
    let finishTest: (value: unknown) => void = () => {}
    api.test.mockImplementation(() => new Promise((resolve) => { finishTest = resolve }))
    api.list.mockResolvedValue({ data: [savedRouter, { ...savedRouter, group_id: 2, rules: [] }] })
    const wrapper = await mountView()
    await wrapper.get('[data-test="intent-test-text"]').setValue('fix my go build')
    await wrapper.get('[data-test="intent-test-run"]').trigger('click')
    await wrapper.get('[data-test="intent-group-2"]').trigger('click')
    finishTest({ data: { answer: 'coding', intent: 'coding', understood: true, account_ids: [21], latency_ms: 5 } })
    await flushPromises()
    expect(wrapper.find('[data-test="intent-test-result"]').exists()).toBe(false)
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
