import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import IQShowcaseView from '../IQShowcaseView.vue'

const { view, cards, showError } = vi.hoisted(() => ({
  view: vi.fn(),
  cards: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/iqShowcase', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/iqShowcase')>()
  return { ...actual, iqShowcaseAPI: { view, cards } }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess: vi.fn() }),
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const evilSVG = '<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script><circle r="4"/></svg>'

function groups(cardIds: number[]) {
  return {
    data: {
      groups: [
        {
          group_id: 5,
          group_name: 'speed',
          items: [{ id: 1, title: 'pelican', cards: cardIds.map((id) => ({ id, model: 'gpt-x', created_at: '2026-11-04T08:00:00Z' })) }],
        },
      ],
    },
  }
}

function mountView() {
  return mount(IQShowcaseView, {
    global: {
      stubs: {
        AppLayout: { template: '<main><slot /></main>' },
        BaseDialog: { template: '<div><slot /></div>' },
        Icon: true,
        TransitionGroup: { template: '<div><slot /></div>' },
      },
    },
  })
}

describe('IQShowcaseView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.useFakeTimers()
    cards.mockImplementation(async (ids: number[]) => ({ data: { cards: ids.map((id) => ({ id, svg: evilSVG })) } }))
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('renders SVGs only as images, never as live markup', async () => {
    view.mockResolvedValue(groups([11, 12]))
    const wrapper = mountView()
    await flushPromises()

    const imgs = wrapper.findAll('[data-test="iq-showcase-card-11"] img')
    expect(imgs).toHaveLength(1)
    expect(imgs[0].attributes('src')).toMatch(/^data:image\/svg\+xml;base64,/)
    expect(wrapper.find('script').exists()).toBe(false)
    expect(wrapper.find('circle').exists()).toBe(false)
    expect(wrapper.text()).toContain('gpt-x')
    expect(wrapper.text()).toContain('speed')
    expect(wrapper.text()).not.toContain('draw')
  })

  it('polls for new cards, fetches only unseen SVGs and drops pruned ones', async () => {
    view.mockResolvedValueOnce(groups([11, 12]))
    const wrapper = mountView()
    await flushPromises()
    expect(cards).toHaveBeenCalledTimes(1)
    expect(cards).toHaveBeenLastCalledWith([11, 12])

    view.mockResolvedValueOnce(groups([13, 11]))
    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()
    expect(cards).toHaveBeenCalledTimes(2)
    expect(cards).toHaveBeenLastCalledWith([13])
    expect(wrapper.find('[data-test="iq-showcase-card-13"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="iq-showcase-card-12"]').exists()).toBe(false)
  })

  it('shows the closed state when the feature is off', async () => {
    view.mockRejectedValue({ status: 404, reason: 'IQ_SHOWCASE_DISABLED', message: 'iq showcase is not enabled' })
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-test="iq-showcase-disabled"]').exists()).toBe(true)
    expect(showError).not.toHaveBeenCalled()
    expect(cards).not.toHaveBeenCalled()
  })
})
