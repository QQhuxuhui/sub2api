import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import StreamPreludeCard from '../StreamPreludeCard.vue'

const api = vi.hoisted(() => ({ streamPrelude: vi.fn(), saveStreamPrelude: vi.fn(), streamPreludeStatus: vi.fn() }))
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
  config: { enabled: true, delay_min_ms: 600, delay_max_ms: 1200, scope: 'listed', user_ids: [14], api_keys: [{ id: 22, user_id: 14 }] },
  scope_labels: { users: [{ id: 14, email: 'a@qq.com', username: '' }], api_keys: [{ id: 22, name: '好蓝', user_id: 14 }] },
}

describe('StreamPreludeCard', () => {
  beforeEach(() => {
    Object.values(api).forEach((fn) => fn.mockReset())
    Object.values(admin).forEach((fn) => fn.mockReset())
    Object.values(toast).forEach((fn) => fn.mockReset())
    api.streamPrelude.mockResolvedValue({ data: listedView })
    api.streamPreludeStatus.mockResolvedValue({ data: {
      days: [{ date: '20261029', sent: 40, failed: 2 }, { date: '20261028', sent: 0, failed: 0 }],
      recent: [
        { time: '2026-10-29T10:00:00+08:00', group_id: 4, user_id: 14, api_key_id: 22, endpoint: '/v1/chat/completions', model: 'gpt-6.1', prelude_ms: 120, output_ms: 14500, heartbeats: 1, outcome: 'ok' },
        { time: '2026-10-29T10:01:00+08:00', group_id: 4, user_id: 14, api_key_id: 22, endpoint: '/v1/responses', model: 'gpt-6.1', prelude_ms: 90, heartbeats: 0, outcome: 'failed' },
      ],
    } })
  })

  it('shows the saved scope, today\'s counts and the latest requests', async () => {
    const wrapper = mount(StreamPreludeCard, { props: { groupId: 4 } })
    await flushPromises()
    expect(api.streamPrelude).toHaveBeenCalledWith(4)
    expect((wrapper.get('[data-test="stream-prelude-enabled"]').element as HTMLInputElement).checked).toBe(true)
    expect(wrapper.get('[data-test="stream-prelude-user-14"]').text()).toContain('a@qq.com')
    expect(wrapper.get('[data-test="stream-prelude-status"]').text()).toContain('intentRouter.streamPrelude.today:{"n":40,"failed":2}')
    const records = wrapper.findAll('[data-test="stream-prelude-record"]')
    expect(records).toHaveLength(2)
    expect(records[0].text()).toContain('120ms')
    expect(records[0].text()).toContain('14.5s')
    expect(records[0].text()).toContain('intentRouter.streamPrelude.outcomes.ok')
    expect(records[1].text()).toContain('—')
    expect(records[1].text()).toContain('intentRouter.streamPrelude.outcomes.failed')
  })

  it('edits the delay range', async () => {
    api.saveStreamPrelude.mockImplementation(async (_id: number, config: object) => ({ data: { ...listedView, config } }))
    const wrapper = mount(StreamPreludeCard, { props: { groupId: 4 } })
    await flushPromises()
    expect((wrapper.get('[data-test="stream-prelude-delay-min"]').element as HTMLInputElement).value).toBe('600')
    expect(wrapper.get('[data-test="stream-prelude-save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="stream-prelude-delay-max"]').setValue('1500')
    await wrapper.get('[data-test="stream-prelude-save"]').trigger('click')
    await flushPromises()
    expect(api.saveStreamPrelude).toHaveBeenCalledWith(4, expect.objectContaining({ delay_min_ms: 600, delay_max_ms: 1500 }))
  })

  it('saves its own setting', async () => {
    api.saveStreamPrelude.mockImplementation(async (_id: number, config: object) => ({ data: { ...listedView, config } }))
    const wrapper = mount(StreamPreludeCard, { props: { groupId: 4 } })
    await flushPromises()
    await wrapper.get('[data-test="stream-prelude-key-22"] button').trigger('click')
    await wrapper.get('[data-test="stream-prelude-save"]').trigger('click')
    await flushPromises()
    expect(api.saveStreamPrelude).toHaveBeenCalledWith(4, { enabled: true, delay_min_ms: 600, delay_max_ms: 1200, scope: 'listed', user_ids: [14], api_keys: [] })
    expect(toast.showSuccess).toHaveBeenCalledWith('intentRouter.streamPrelude.saved')
  })
})
