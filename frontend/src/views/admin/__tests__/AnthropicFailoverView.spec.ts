import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AnthropicFailoverView from '../AnthropicFailoverView.vue'

const { getOverview, updateConfig, openaiGetOverview, openaiUpdateConfig, getAll, showSuccess, showError } = vi.hoisted(() => ({
  getOverview: vi.fn(),
  updateConfig: vi.fn(),
  openaiGetOverview: vi.fn(),
  openaiUpdateConfig: vi.fn(),
  getAll: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    anthropicFailover: { getOverview, updateConfig },
    openAIFailover: { getOverview: openaiGetOverview, updateConfig: openaiUpdateConfig },
    groups: { getAll }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const overview = () => ({
  config: {
    enabled_group_ids: [18],
    max_rule_switches: 3,
    rules: [
      { id: 'shield', name: 'Shield', enabled: true, status_code: 400, keywords: ['prompt is too long'], action: 'return' },
      { id: 'no-channel', name: 'No channel', enabled: true, status_code: 0, keywords: ['暂无可用渠道', 'no channel'], action: 'failover' }
    ]
  },
  stats: { rules: [{ rule_id: 'no-channel', hits: 4, recovered: 3 }], stream_hits: 40, stream_recovered: 30, window_days: 7 },
  recent: [
    { time: '2026-11-11T06:00:00Z', group_id: 18, user_id: 148, api_key_id: 200, reason: 'stream_before_first_event', status_code: 429, switches: 1, accounts: [1177, 1156], recovered: true, canceled: false },
    { time: '2026-11-11T05:00:00Z', group_id: 18, user_id: 148, api_key_id: 200, reason: 'no-channel', status_code: 422, switches: 2, accounts: [1128], recovered: false, canceled: false }
  ]
})

function mountView() {
  return mount(AnthropicFailoverView, {
    global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, Icon: true } }
  })
}

describe('AnthropicFailoverView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getOverview.mockResolvedValue(overview())
    getAll.mockResolvedValue([{ id: 10, name: 'G10' }, { id: 18, name: 'G18' }])
    updateConfig.mockImplementation(async (saved) => saved)
    openaiGetOverview.mockResolvedValue({
      config: {
        enabled_group_ids: [45],
        max_rule_switches: 3,
        rules: [{ id: 'passback', name: 'Reasoning pass-back', enabled: true, status_code: 400, keywords: ['must be passed back to the API'], action: 'failover' }]
      },
      stats: { rules: [{ rule_id: 'passback', hits: 10, recovered: 9 }], window_days: 7 },
      recent: [{ time: '2026-11-12T09:00:00Z', group_id: 45, user_id: 148, api_key_id: 394, reason: 'passback', status_code: 400, switches: 1, accounts: [1184, 1165], recovered: true, canceled: false }]
    })
    openaiUpdateConfig.mockImplementation(async (saved) => saved)
  })

  it('lists only anthropic groups and shows rule and stream statistics', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(getAll).toHaveBeenCalledWith('anthropic')
    expect(wrapper.findAll('[data-test="failover-rule"]')).toHaveLength(2)
    expect(wrapper.text()).toContain('75.0%')
    expect(wrapper.find('[data-test="failover-stream-stats"]').text()).toContain('40')
    const recent = wrapper.find('[data-test="failover-recent"]').text()
    expect(recent).toContain('1177 → 1156')
    expect(recent).toContain('No channel')
    expect(recent).toContain('G18')
  })

  it('saves groups, order and keywords', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.find('[data-test="failover-group-10"]').setValue(true)
    await wrapper.find('[data-test="failover-max-switches"]').setValue(2)
    await wrapper.findAll('[data-test="failover-rule"]')[1].find('button').trigger('click')
    await wrapper.find('[data-test="failover-save"]').trigger('click')
    await flushPromises()

    const saved = updateConfig.mock.calls[0][0]
    expect(saved.enabled_group_ids).toEqual([18, 10])
    expect(saved.max_rule_switches).toBe(2)
    expect(saved.rules.map((rule: { id: string }) => rule.id)).toEqual(['no-channel', 'shield'])
    expect(saved.rules[0]).toMatchObject({ status_code: 0, keywords: ['暂无可用渠道', 'no channel'], action: 'failover' })
    expect(saved.rules[1]).toMatchObject({ status_code: 400, action: 'return' })
    expect(showSuccess).toHaveBeenCalled()
  })

  it('refuses to save a rule without a keyword', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.find('[data-test="failover-add-rule"]').trigger('click')
    expect(wrapper.findAll('[data-test="failover-rule"]')).toHaveLength(3)
    await wrapper.find('[data-test="failover-save"]').trigger('click')
    await flushPromises()

    expect(updateConfig).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('admin.anthropicFailover.rules.keywordRequired')

    await wrapper.findAll('[data-test="failover-delete-rule"]')[2].trigger('click')
    await wrapper.find('[data-test="failover-save"]').trigger('click')
    await flushPromises()
    expect(updateConfig).toHaveBeenCalledTimes(1)
  })

  it('shows the backend message when a save is rejected', async () => {
    updateConfig.mockRejectedValue({ response: { data: { message: 'rule "x": status 429 already switches accounts' } } })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.find('[data-test="failover-save"]').trigger('click')
    await flushPromises()
    expect(showError).toHaveBeenCalledWith('rule "x": status 429 already switches accounts')
  })

  it('switches to the OpenAI table with OpenAI groups and no stream statistics', async () => {
    getAll.mockImplementation(async (platform: string) =>
      platform === 'openai' ? [{ id: 45, name: '国模' }] : [{ id: 18, name: 'G18' }]
    )
    const wrapper = mountView()
    await flushPromises()

    await wrapper.find('[data-test="failover-platform-openai"]').trigger('click')
    await flushPromises()

    expect(getAll).toHaveBeenLastCalledWith('openai')
    expect(openaiGetOverview).toHaveBeenCalled()
    expect(wrapper.find('[data-test="failover-stream-stats"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="failover-group-45"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('90.0%')
    expect(wrapper.find('[data-test="failover-recent"]').text()).toContain('1184 → 1165')
  })

  it('saves an OpenAI status-only rule to the OpenAI API', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.find('[data-test="failover-platform-openai"]').trigger('click')
    await flushPromises()

    await wrapper.find('[data-test="failover-add-rule"]').trigger('click')
    const rows = wrapper.findAll('[data-test="failover-rule"]')
    await rows[1].find('input[type="number"]').setValue(408)
    await wrapper.find('[data-test="failover-save"]').trigger('click')
    await flushPromises()

    expect(updateConfig).not.toHaveBeenCalled()
    const saved = openaiUpdateConfig.mock.calls[0][0]
    expect(saved.rules[1]).toMatchObject({ status_code: 408, keywords: [], action: 'failover' })
  })

  it('refuses an OpenAI rule without status code and keyword', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.find('[data-test="failover-platform-openai"]').trigger('click')
    await flushPromises()

    await wrapper.find('[data-test="failover-add-rule"]').trigger('click')
    await wrapper.find('[data-test="failover-save"]').trigger('click')
    await flushPromises()

    expect(openaiUpdateConfig).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('admin.anthropicFailover.rules.statusOrKeywordRequired')
  })
})
