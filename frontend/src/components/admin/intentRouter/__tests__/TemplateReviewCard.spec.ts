import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import TemplateReviewCard from '../TemplateReviewCard.vue'

const api = vi.hoisted(() => ({ templates: vi.fn(), decideTemplate: vi.fn(), revokeTemplate: vi.fn() }))
const toast = vi.hoisted(() => ({ showSuccess: vi.fn(), showError: vi.fn() }))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) }),
  }
})
vi.mock('@/api/admin/intentRouters', () => ({ intentRoutersAPI: api, default: api }))
vi.mock('@/stores/app', () => ({ useAppStore: () => toast }))

const KEY = 'a'.repeat(64)
const short = KEY.slice(0, 12)
const pending = {
  key: KEY,
  template: 'reply with exactly this json: {"fp":"<rand>","calc":<num>}',
  sample: 'Reply with exactly this JSON: {"fp":"7Q2K","calc":391}',
  text_chars: 111,
  status: 'pending',
  first_seen_at: '2026-11-06T01:00:00Z',
  last_seen_at: '2026-11-06T02:00:00Z',
  seen_count: 505,
  reason_a: 366,
  reason_b: 3,
  reason_c: 0,
  verdict_route: 139,
  verdict_health: 366,
  verdict_normal: 0,
  probe_min: 0.92,
  probe_max: 0.97,
  health_min: 0.7,
  health_max: 0.76,
  user_ids: [107, 2],
  group_ids: [4],
  decided_at: null,
  decided_by: null,
  revision: 0,
}

describe('TemplateReviewCard', () => {
  beforeEach(() => {
    Object.values(api).forEach((fn) => fn.mockReset())
    Object.values(toast).forEach((fn) => fn.mockReset())
    api.templates.mockResolvedValue({ data: { items: [pending], total: 1 } })
  })

  it('lists pending templates with reasons, counts, scores and where they came from', async () => {
    const wrapper = mount(TemplateReviewCard, { props: { groupNames: { 4: '速刷组' } } })
    await flushPromises()
    expect(api.templates).toHaveBeenCalledWith({ status: 'pending', reason: '', q: '', page: 1, page_size: 20 })
    const row = wrapper.get(`[data-test="template-row-${short}"]`)
    expect(row.text()).toContain('{"fp":"<rand>","calc":<num>}')
    expect(row.text()).toContain('366')
    expect(row.text()).toContain('139 / 366 / 0')
    expect(row.text()).toContain('0.92~0.97 / 0.70~0.76')
    expect(row.text()).toContain('#107 #2')
    expect(row.text()).toContain('速刷组')
    expect(wrapper.find(`[data-test="template-sample-${short}"]`).exists()).toBe(false)
    await wrapper.get(`[data-test="template-sample-toggle-${short}"]`).trigger('click')
    expect(wrapper.get(`[data-test="template-sample-${short}"]`).text()).toContain('7Q2K')
  })

  it('filters by status, reason and search', async () => {
    const wrapper = mount(TemplateReviewCard)
    await flushPromises()
    await wrapper.get('[data-test="template-status"]').setValue('probe')
    await wrapper.get('[data-test="template-reason"]').setValue('b')
    await wrapper.get('[data-test="template-search"]').setValue(' fp ')
    await wrapper.get('[data-test="template-refresh"]').trigger('click')
    await flushPromises()
    expect(api.templates).toHaveBeenLastCalledWith({ status: 'probe', reason: 'b', q: 'fp', page: 1, page_size: 20 })
  })

  it('judges with the row revision and reloads', async () => {
    api.decideTemplate.mockResolvedValue({ data: { ...pending, status: 'probe', revision: 1 } })
    const wrapper = mount(TemplateReviewCard)
    await flushPromises()
    api.templates.mockResolvedValue({ data: { items: [], total: 0 } })
    await wrapper.get(`[data-test="template-decide-probe-${short}"]`).trigger('click')
    await flushPromises()
    expect(api.decideTemplate).toHaveBeenCalledWith(KEY, 'probe', 0)
    expect(toast.showSuccess).toHaveBeenCalledWith('intentRouter.templates.saved')
    expect(api.templates).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-test="template-empty"]').exists()).toBe(true)
  })

  it('tells about a conflicting change and reloads', async () => {
    api.decideTemplate.mockRejectedValue({ status: 409, reason: 'INTENT_TEMPLATE_CONFLICT', message: 'changed' })
    const wrapper = mount(TemplateReviewCard)
    await flushPromises()
    await wrapper.get(`[data-test="template-decide-health-${short}"]`).trigger('click')
    await flushPromises()
    expect(toast.showError).toHaveBeenCalledWith('intentRouter.templates.conflict')
    expect(api.templates).toHaveBeenCalledTimes(2)
  })

  it('revokes a judged template', async () => {
    api.templates.mockResolvedValue({ data: { items: [{ ...pending, status: 'health', revision: 3 }], total: 1 } })
    api.revokeTemplate.mockResolvedValue({ data: { ...pending, revision: 4 } })
    const wrapper = mount(TemplateReviewCard)
    await flushPromises()
    expect(wrapper.find(`[data-test="template-decide-probe-${short}"]`).exists()).toBe(false)
    expect(wrapper.get(`[data-test="template-row-${short}"]`).text()).toContain('intentRouter.templates.decidedAs')
    await wrapper.get(`[data-test="template-revoke-${short}"]`).trigger('click')
    await flushPromises()
    expect(api.revokeTemplate).toHaveBeenCalledWith(KEY, 3)
    expect(toast.showSuccess).toHaveBeenCalledWith('intentRouter.templates.revoked')
  })

  it('shows a load error', async () => {
    api.templates.mockRejectedValue({ status: 503, reason: 'INTENT_TEMPLATE_UNAVAILABLE', message: 'needs db' })
    const wrapper = mount(TemplateReviewCard)
    await flushPromises()
    expect(wrapper.find('[data-test="template-error"]').exists()).toBe(true)
  })
})
