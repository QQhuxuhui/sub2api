import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import IQProbeView from '../IQProbeView.vue'

const api = vi.hoisted(() => ({
  listSets: vi.fn(), createSet: vi.fn(), updateSet: vi.fn(), removeSet: vi.fn(), exportSet: vi.fn(), importSet: vi.fn(),
  listQuestions: vi.fn(), createQuestion: vi.fn(), updateQuestion: vi.fn(), removeQuestion: vi.fn(),
  listMonitors: vi.fn(), createMonitors: vi.fn(), updateMonitor: vi.fn(), removeMonitor: vi.fn(), runMonitor: vi.fn(),
  matrix: vi.fn(), listRuns: vi.fn(), getAnswer: vi.fn(),
}))
const admin = vi.hoisted(() => ({ getAllIncludingInactive: vi.fn(), listAccounts: vi.fn(), getAvailableModels: vi.fn() }))
const toast = vi.hoisted(() => ({ showSuccess: vi.fn(), showError: vi.fn() }))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) }),
  }
})
vi.mock('@/api/admin/iqProbe', () => ({ iqProbeAPI: api, default: api }))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: { getAllIncludingInactive: admin.getAllIncludingInactive },
    accounts: { list: admin.listAccounts, getAvailableModels: admin.getAvailableModels },
  },
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => toast }))

const set = { id: 3, name: 'GPT 高难', description: '', question_count: 2, enabled_count: 2, monitor_count: 1, created_at: '', updated_at: '' }
const monitor = {
  id: 9, account_id: 42, group_id: 4, model: 'gpt-5.5', set_id: 3, interval_minutes: 60, reasoning_effort: '', max_tokens: 0,
  enabled: true, alert_enabled: true, alert_threshold: 0.7, alert_low_rounds: 2, alert_invalid_rounds: 3, alert_state: 'low',
  consecutive_low: 2, consecutive_invalid: 0, last_run_at: '2026-10-26T08:00:00Z', next_run_at: null, last_status: 'scored',
  last_score: 0.5, account_name: 'cpa-codex', account_platform: 'openai', account_type: 'apikey', account_status: 'active',
  group_name: '组4', set_name: 'GPT 高难',
  trend: [{ at: '2026-10-26T07:00:00Z', score: 0.9, status: 'scored' }, { at: '2026-10-26T08:00:00Z', score: 0.5, status: 'scored' }],
}

async function mountView() {
  const wrapper = mount(IQProbeView, {
    global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' } } },
  })
  await flushPromises()
  return wrapper
}

describe('IQProbeView', () => {
  beforeEach(() => {
    Object.values(api).forEach((fn) => fn.mockReset())
    Object.values(admin).forEach((fn) => fn.mockReset())
    Object.values(toast).forEach((fn) => fn.mockReset())
    admin.getAllIncludingInactive.mockResolvedValue([{ id: 4, name: '组4', platform: 'openai' }])
    admin.listAccounts.mockResolvedValue({ items: [
      { id: 42, name: 'cpa-codex', platform: 'openai', type: 'apikey', status: 'active' },
      { id: 48, name: 'plus-48', platform: 'openai', type: 'oauth', status: 'active' },
    ] })
    admin.getAvailableModels.mockResolvedValue([{ id: 'gpt-5.5' }, { id: 'gpt-5.4-mini' }])
    api.listSets.mockResolvedValue({ data: [set] })
    api.listMonitors.mockResolvedValue({ data: [monitor] })
    api.listQuestions.mockResolvedValue({ data: [] })
  })

  it('lists monitors with score, alert state and starts a manual round', async () => {
    api.runMonitor.mockResolvedValue({ data: { started: true } })
    const wrapper = await mountView()

    const row = wrapper.get('[data-test="iq-monitor-row-9"]')
    expect(row.text()).toContain('cpa-codex')
    expect(row.text()).toContain('gpt-5.5')
    expect(row.text()).toContain('50%')
    expect(row.text()).toContain('iqProbe.alertState.low')

    await wrapper.get('[data-test="iq-monitor-run-9"]').trigger('click')
    await flushPromises()
    expect(api.runMonitor).toHaveBeenCalledWith(9)
    expect(wrapper.get('[data-test="iq-monitor-run-9"]').attributes('disabled')).toBeDefined()
  })

  it('creates one monitor per picked account from a group', async () => {
    api.createMonitors.mockResolvedValue({ data: { created: [monitor], skipped_account_ids: [42] } })
    const wrapper = await mountView()

    await wrapper.get('[data-test="iq-monitor-add"]').trigger('click')
    await wrapper.get('[data-test="iq-monitor-group"]').setValue(4)
    await flushPromises()
    expect(admin.listAccounts).toHaveBeenCalledWith(1, 500, { group: '4', lite: '1' })

    await wrapper.get('[data-test="iq-monitor-account-42"]').setValue(true)
    await wrapper.get('[data-test="iq-monitor-account-48"]').setValue(true)
    await flushPromises()
    expect(admin.getAvailableModels).toHaveBeenCalledWith(42)

    await wrapper.get('[data-test="iq-monitor-model"]').setValue('gpt-5.5')
    await wrapper.get('[data-test="iq-monitor-save"]').trigger('click')
    await flushPromises()

    expect(api.createMonitors).toHaveBeenCalledWith(expect.objectContaining({
      account_ids: [42, 48], group_id: 4, model: 'gpt-5.5', set_id: 3, interval_minutes: 60, reasoning_effort: '',
    }))
    expect(toast.showSuccess).toHaveBeenCalledWith('iqProbe.monitorDialog.created:{"created":1,"skipped":1}')
  })

  it('adds a question to the selected set', async () => {
    api.createQuestion.mockResolvedValue({ data: {} })
    const wrapper = await mountView()
    await wrapper.get('[data-test="iq-tab-sets"]').trigger('click')
    await flushPromises()
    expect(api.listQuestions).toHaveBeenCalledWith(3)

    await wrapper.get('[data-test="iq-question-add"]').trigger('click')
    await wrapper.get('[data-test="iq-question-prompt"]').setValue('17*23=?')
    await wrapper.get('[data-test="iq-question-expected"]').setValue('391')
    await wrapper.get('[data-test="iq-question-save"]').trigger('click')
    await flushPromises()

    expect(api.createQuestion).toHaveBeenCalledWith(3, expect.objectContaining({ prompt: '17*23=?', expected_answer: '391', judge_type: 'exact', enabled: true }))
  })

  it('keeps the selected set questions when an older request finishes later', async () => {
    const question = (id: number, setId: number, title: string) => ({
      id, set_id: setId, title, prompt: title, expected_answer: '1', judge_type: 'exact', weight: 1, enabled: true,
    })
    let resolveOld!: (value: unknown) => void
    api.listSets.mockResolvedValue({ data: [set, { ...set, id: 4, name: '题组 B' }] })
    api.listQuestions.mockImplementation((id: number) => id === 3
      ? new Promise((resolve) => { resolveOld = resolve })
      : Promise.resolve({ data: [question(202, 4, 'B 的题目')] }))
    const wrapper = await mountView()
    await wrapper.get('[data-test="iq-tab-sets"]').trigger('click')
    await flushPromises()
    await wrapper.findAll('aside li button')[1].trigger('click')
    await flushPromises()
    expect(wrapper.get('tbody').text()).toContain('B 的题目')

    resolveOld({ data: [question(101, 3, 'A 的题目')] })
    await flushPromises()
    expect(wrapper.get('tbody').text()).toContain('B 的题目')
    expect(wrapper.get('tbody').text()).not.toContain('A 的题目')
  })

  it('removes the previous set questions immediately while the next set loads', async () => {
    api.listSets.mockResolvedValue({ data: [set, { ...set, id: 4, name: '题组 B' }] })
    api.listQuestions.mockResolvedValueOnce({ data: [{ id: 101, set_id: 3, title: 'A 的题目', prompt: 'A', enabled: true }] })
    const wrapper = await mountView()
    await wrapper.get('[data-test="iq-tab-sets"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('tbody').text()).toContain('A 的题目')

    api.listQuestions.mockReturnValue(new Promise(() => {}))
    await wrapper.findAll('aside li button')[1].trigger('click')
    expect(wrapper.get('tbody').text()).not.toContain('A 的题目')
  })

  it('reports zero created monitors when every selected account is a duplicate', async () => {
    api.createMonitors.mockResolvedValue({ data: { created: null, skipped_account_ids: [42] } })
    const wrapper = await mountView()
    await wrapper.get('[data-test="iq-monitor-add"]').trigger('click')
    await wrapper.get('[data-test="iq-monitor-group"]').setValue(4)
    await flushPromises()
    await wrapper.get('[data-test="iq-monitor-account-42"]').setValue(true)
    await flushPromises()
    await wrapper.get('[data-test="iq-monitor-model"]').setValue('gpt-5.5')
    await wrapper.get('[data-test="iq-monitor-save"]').trigger('click')
    await flushPromises()

    expect(toast.showSuccess).toHaveBeenCalledWith('iqProbe.monitorDialog.created:{"created":0,"skipped":1}')
    expect(toast.showError).not.toHaveBeenCalled()
    expect(wrapper.find('[data-test="iq-monitor-save"]').exists()).toBe(false)
  })
})
