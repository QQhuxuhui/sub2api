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

  function twoGroups(firstCards: number[], opts: { dropFirst?: boolean } = {}) {
    const card = (id: number, model = 'gpt-x') => ({ id, model, created_at: '2026-11-04T08:00:00Z' })
    const first = {
      group_id: 5,
      group_name: 'speed',
      items: [
        { id: 1, title: 'pelican', cards: firstCards.map((id) => card(id)) },
        { id: 2, title: 'clock', cards: [] },
      ],
    }
    const second = { group_id: 7, group_name: 'claude', items: [{ id: 3, title: 'owl', cards: [card(31, 'claude-x')] }] }
    return { data: { groups: opts.dropFirst ? [second] : [first, second] } }
  }

  it('shows one group at a time and falls back when the selected group goes away', async () => {
    view.mockResolvedValueOnce(twoGroups([11]))
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-test="iq-showcase-group-5"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="iq-showcase-group-7"]').exists()).toBe(false)

    await wrapper.find('[data-test="iq-showcase-tab-7"]').trigger('click')
    expect(wrapper.find('[data-test="iq-showcase-group-7"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="iq-showcase-tab-7"]').attributes('aria-selected')).toBe('true')

    // The poll keeps the chosen group while it exists...
    view.mockResolvedValueOnce(twoGroups([11]))
    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()
    expect(wrapper.find('[data-test="iq-showcase-group-7"]').exists()).toBe(true)

    // ...and a failed poll changes nothing.
    view.mockRejectedValueOnce(new Error('network'))
    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()
    expect(wrapper.find('[data-test="iq-showcase-group-7"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="iq-showcase-card-31"]').exists()).toBe(true)

    // Selecting a group that disappears falls back to the first one left.
    await wrapper.find('[data-test="iq-showcase-tab-5"]').trigger('click')
    view.mockResolvedValueOnce(twoGroups([11], { dropFirst: true }))
    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()
    expect(wrapper.find('[data-test="iq-showcase-group-7"]').exists()).toBe(true)
  })

  it('puts the newest card large, keeps history in order and shows the latest model once', async () => {
    view.mockResolvedValue(twoGroups([13, 12, 11]))
    const wrapper = mountView()
    await flushPromises()
    const item = wrapper.find('[data-test="iq-showcase-item-1"]')
    expect(item.find('[data-role="latest"]').attributes('data-test')).toBe('iq-showcase-card-13')
    const history = item.findAll('[data-role="history"] button').map((b) => b.attributes('data-test'))
    expect(history).toEqual(['iq-showcase-card-12', 'iq-showcase-card-11'])
    expect(item.find('[data-test="iq-showcase-item-model"]').text()).toBe('gpt-x')
    const empty = wrapper.find('[data-test="iq-showcase-item-2"]')
    expect(empty.find('[data-test="iq-showcase-item-model"]').exists()).toBe(false)
    expect(empty.text()).toContain('iqShowcase.waiting')
  })

  it('animates only cards that arrive after the first load', async () => {
    view.mockResolvedValueOnce(twoGroups([12, 11]))
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.findAll('.iq-card-enter')).toHaveLength(0)

    view.mockResolvedValueOnce(twoGroups([13, 12, 11]))
    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()
    expect(wrapper.find('[data-test="iq-showcase-card-13"]').classes()).toContain('iq-card-enter')
    expect(wrapper.find('[data-test="iq-showcase-card-12"]').classes()).not.toContain('iq-card-enter')

    // Switching groups does not replay anything.
    await wrapper.find('[data-test="iq-showcase-card-13"]').trigger('animationend')
    await wrapper.find('[data-test="iq-showcase-tab-7"]').trigger('click')
    await wrapper.find('[data-test="iq-showcase-tab-5"]').trigger('click')
    expect(wrapper.findAll('.iq-card-enter')).toHaveLength(0)
  })

  it('keeps the new listing when an SVG batch fails and fetches it on the next poll', async () => {
    view.mockResolvedValue(twoGroups([12, 11]))
    cards.mockRejectedValueOnce(new Error('svg down'))
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-test="iq-showcase-card-12"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="iq-showcase-card-12"] img').exists()).toBe(false)

    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()
    expect(cards).toHaveBeenLastCalledWith([12, 11, 31])
    expect(wrapper.find('[data-test="iq-showcase-card-12"] img').exists()).toBe(true)
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
