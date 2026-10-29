import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import MonitorAnswerCard from '../MonitorAnswerCard.vue'

const api = vi.hoisted(() => ({ monitorAnswer: vi.fn(), saveMonitorAnswer: vi.fn(), monitorAnswerStatus: vi.fn() }))
const admin = vi.hoisted(() => ({ searchUsers: vi.fn(), searchApiKeys: vi.fn() }))
const toast = vi.hoisted(() => ({ showSuccess: vi.fn(), showError: vi.fn() }))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) }),
  }
})
vi.mock('@/api/admin/intentRouters', () => ({ intentRoutersAPI: api, default: api }))
vi.mock('@/api/admin', () => ({ adminAPI: { usage: { searchUsers: admin.searchUsers, searchApiKeys: admin.searchApiKeys } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => toast }))

const listedView = {
  group_id: 4,
  config: { enabled: true, scope: 'listed', user_ids: [14], api_keys: [{ id: 22, user_id: 14 }] },
  scope_labels: { users: [{ id: 14, email: 'a@qq.com', username: '' }], api_keys: [{ id: 22, name: '好蓝', user_id: 14 }] },
}

describe('MonitorAnswerCard', () => {
  beforeEach(() => {
    Object.values(api).forEach((fn) => fn.mockReset())
    Object.values(admin).forEach((fn) => fn.mockReset())
    Object.values(toast).forEach((fn) => fn.mockReset())
    api.monitorAnswer.mockResolvedValue({ data: listedView })
    api.monitorAnswerStatus.mockResolvedValue({ data: {
      days: [{ date: '20261029', answers: 12 }, { date: '20261028', answers: 3 }],
      recent: [{ time: '2026-10-29T10:00:00+08:00', group_id: 4, user_id: 14, api_key_id: 22, endpoint: '/v1/responses', model: 'gpt-6.1-sol', question: '37 + 64', answer: '101' }],
    } })
  })

  it('shows the saved scope with names, today\'s count and the latest answers', async () => {
    const wrapper = mount(MonitorAnswerCard, { props: { groupId: 4 } })
    await flushPromises()
    expect(api.monitorAnswer).toHaveBeenCalledWith(4)
    expect((wrapper.get('[data-test="monitor-answer-enabled"]').element as HTMLInputElement).checked).toBe(true)
    expect(wrapper.get('[data-test="monitor-answer-user-14"]').text()).toContain('a@qq.com')
    expect(wrapper.get('[data-test="monitor-answer-key-22"]').text()).toContain('好蓝')
    expect(wrapper.get('[data-test="monitor-answer-status"]').text()).toContain('intentRouter.monitorAnswer.today:{"n":12}')
    expect(wrapper.get('[data-test="monitor-answer-record"]').text()).toContain('37 + 64 = 101')
    expect(wrapper.get('[data-test="monitor-answer-save"]').attributes('disabled')).toBeDefined()
  })

  it('saves its own setting, separately from the router', async () => {
    api.saveMonitorAnswer.mockImplementation(async (_id: number, config: object) => ({ data: { ...listedView, config } }))
    const wrapper = mount(MonitorAnswerCard, { props: { groupId: 4 } })
    await flushPromises()
    await wrapper.get('[data-test="monitor-answer-key-22"] button').trigger('click')
    await wrapper.get('[data-test="monitor-answer-save"]').trigger('click')
    await flushPromises()
    expect(api.saveMonitorAnswer).toHaveBeenCalledWith(4, { enabled: true, scope: 'listed', user_ids: [14], api_keys: [] })
    expect(toast.showSuccess).toHaveBeenCalledWith('intentRouter.monitorAnswer.saved')
  })

  it('shows the server\'s reason when a save is refused', async () => {
    api.saveMonitorAnswer.mockRejectedValue({ response: { data: { code: 400, reason: 'MONITOR_ANSWER_SCOPE_EMPTY', message: 'choose at least one user' } } })
    const wrapper = mount(MonitorAnswerCard, { props: { groupId: 4 } })
    await flushPromises()
    await wrapper.get('[data-test="monitor-answer-user-14"] button').trigger('click')
    await wrapper.get('[data-test="monitor-answer-save"]').trigger('click')
    await flushPromises()
    expect(toast.showError).toHaveBeenCalled()
  })

  it('reloads for another group and ignores the earlier group\'s late answer', async () => {
    let resolveFirst: (v: unknown) => void = () => {}
    api.monitorAnswer.mockImplementationOnce(() => new Promise((r) => { resolveFirst = r }))
    const wrapper = mount(MonitorAnswerCard, { props: { groupId: 3 } })
    await wrapper.setProps({ groupId: 4 })
    await flushPromises()
    resolveFirst({ data: { ...listedView, group_id: 3, config: { enabled: false, scope: 'all', user_ids: [], api_keys: [] } } })
    await flushPromises()
    expect(api.monitorAnswer).toHaveBeenLastCalledWith(4)
    expect((wrapper.get('[data-test="monitor-answer-enabled"]').element as HTMLInputElement).checked).toBe(true)
  })

  it('clears the form and blocks saving while another group loads', async () => {
    const wrapper = mount(MonitorAnswerCard, { props: { groupId: 4 } })
    await flushPromises()
    await wrapper.get('[data-test="monitor-answer-scope-all"]').setValue(true)
    expect(wrapper.get('[data-test="monitor-answer-save"]').attributes('disabled')).toBeUndefined()

    let resolveB: (v: unknown) => void = () => {}
    api.monitorAnswer.mockImplementationOnce(() => new Promise((r) => { resolveB = r }))
    await wrapper.setProps({ groupId: 5 })
    await flushPromises()
    expect((wrapper.get('[data-test="monitor-answer-enabled"]').element as HTMLInputElement).checked).toBe(false)
    expect(wrapper.find('[data-test="monitor-answer-user-14"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="monitor-answer-save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="monitor-answer-enabled"]').setValue(true)
    expect(wrapper.get('[data-test="monitor-answer-save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="monitor-answer-save"]').trigger('click')
    expect(api.saveMonitorAnswer).not.toHaveBeenCalled()

    resolveB({ data: { group_id: 5, config: { enabled: false, scope: 'all', user_ids: [], api_keys: [] }, scope_labels: { users: [], api_keys: [] } } })
    await flushPromises()
    expect((wrapper.get('[data-test="monitor-answer-enabled"]').element as HTMLInputElement).checked).toBe(false)
  })

  it('does not let a save of the previous group overwrite the current form', async () => {
    let resolveSave: (v: unknown) => void = () => {}
    api.saveMonitorAnswer.mockImplementationOnce(() => new Promise((r) => { resolveSave = r }))
    const wrapper = mount(MonitorAnswerCard, { props: { groupId: 4 } })
    await flushPromises()
    await wrapper.get('[data-test="monitor-answer-scope-all"]').setValue(true)
    await wrapper.get('[data-test="monitor-answer-save"]').trigger('click')

    api.monitorAnswer.mockResolvedValueOnce({ data: { group_id: 5, config: { enabled: false, scope: 'all', user_ids: [], api_keys: [] }, scope_labels: { users: [], api_keys: [] } } })
    await wrapper.setProps({ groupId: 5 })
    await flushPromises()
    resolveSave({ data: { ...listedView, config: { ...listedView.config, scope: 'all' } } })
    await flushPromises()

    expect(api.saveMonitorAnswer).toHaveBeenCalledWith(4, expect.objectContaining({ scope: 'all' }))
    expect((wrapper.get('[data-test="monitor-answer-enabled"]').element as HTMLInputElement).checked).toBe(false)
    expect(wrapper.get('[data-test="monitor-answer-save"]').attributes('disabled')).toBeDefined()
  })
})
