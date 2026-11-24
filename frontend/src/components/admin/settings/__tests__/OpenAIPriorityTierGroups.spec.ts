import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OpenAIPriorityTierGroups from '../OpenAIPriorityTierGroups.vue'
import zh from '@/i18n/locales/zh/openaiPriorityTier'
import en from '@/i18n/locales/en/openaiPriorityTier'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => {
      const message = zh[key.replace('openaiPriorityTier.', '') as keyof typeof zh]
      if (!message) throw new Error(`缺少翻译：${key}`)
      return message
    },
  }),
}))

const mocks = vi.hoisted(() => ({
  getAll: vi.fn(),
  get: vi.fn(),
  update: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))
vi.mock('@/api/admin/groups', () => ({ getAll: mocks.getAll }))
vi.mock('@/api/admin/openaiPriorityTier', () => ({ get: mocks.get, update: mocks.update }))
vi.mock('@/stores', () => ({
  useAppStore: () => ({ showSuccess: mocks.showSuccess, showError: mocks.showError }),
}))

const groups = [
  { id: 3, name: 'GPT 常规' },
  { id: 4, name: 'GPT-Pro【临期pro速刷】' },
]

beforeEach(() => {
  vi.clearAllMocks()
  mocks.getAll.mockResolvedValue(groups)
  mocks.get.mockResolvedValue({ group_ids: [4] })
})

describe('OpenAI 优先级严格分档分组选择', () => {
  it('中英文文案键一致', () => {
    expect(Object.keys(en).sort()).toEqual(Object.keys(zh).sort())
  })

  it('只拉 OpenAI 分组并按服务端状态标记选中', async () => {
    const wrapper = mount(OpenAIPriorityTierGroups)
    await flushPromises()
    expect(mocks.getAll).toHaveBeenCalledWith('openai')
    expect(wrapper.get('[data-test="tier-group-4"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.get('[data-test="tier-group-3"]').attributes('aria-pressed')).toBe('false')
    expect(wrapper.text()).toContain('GPT-Pro【临期pro速刷】')
  })

  it('点选即保存完整列表，并以服务端返回为准', async () => {
    mocks.update.mockResolvedValue({ group_ids: [3, 4] })
    const wrapper = mount(OpenAIPriorityTierGroups)
    await flushPromises()
    await wrapper.get('[data-test="tier-group-3"]').trigger('click')
    await flushPromises()
    expect(mocks.update).toHaveBeenCalledWith([4, 3])
    expect(wrapper.get('[data-test="tier-group-3"]').attributes('aria-pressed')).toBe('true')
    expect(mocks.showSuccess).toHaveBeenCalled()
  })

  it('保存中禁用点选，连续点击只发一次请求', async () => {
    let resolveUpdate: (v: { group_ids: number[] }) => void = () => {}
    mocks.update.mockImplementation(() => new Promise((resolve) => { resolveUpdate = resolve }))
    const wrapper = mount(OpenAIPriorityTierGroups)
    await flushPromises()
    await wrapper.get('[data-test="tier-group-3"]').trigger('click')
    expect(wrapper.get('[data-test="tier-group-4"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="tier-group-4"]').trigger('click')
    expect(mocks.update).toHaveBeenCalledTimes(1)
    resolveUpdate({ group_ids: [3, 4] })
    await flushPromises()
    expect(wrapper.get('[data-test="tier-group-4"]').attributes('disabled')).toBeUndefined()
  })

  it('保存失败恢复为最后确认的状态', async () => {
    mocks.update.mockRejectedValue(new Error('boom'))
    const wrapper = mount(OpenAIPriorityTierGroups)
    await flushPromises()
    await wrapper.get('[data-test="tier-group-4"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="tier-group-4"]').attributes('aria-pressed')).toBe('true')
    expect(mocks.showError).toHaveBeenCalled()
  })

  it('加载失败显示重试，重试成功后可用', async () => {
    mocks.get.mockRejectedValueOnce(new Error('down'))
    const wrapper = mount(OpenAIPriorityTierGroups)
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    await wrapper.get('[data-test="retry"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="tier-group-4"]').exists()).toBe(true)
  })
})
