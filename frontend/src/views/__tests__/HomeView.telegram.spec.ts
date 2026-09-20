import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'

import HomeView from '../HomeView.vue'

const { appStore, authStore } = vi.hoisted(() => ({
  appStore: {
    cachedPublicSettings: {} as Record<string, unknown>,
    siteName: 'Fallback site',
    siteLogo: '',
    docUrl: '',
    telegramUrl: '',
    publicSettingsLoaded: true,
    fetchPublicSettings: vi.fn(),
  },
  authStore: {
    isAuthenticated: false,
    isAdmin: false,
    user: null as { email?: string } | null,
    checkAuth: vi.fn(),
  },
}))

vi.mock('@/stores', () => ({
  useAppStore: () => appStore,
  useAuthStore: () => authStore,
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => appStore,
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

const TG = 'https://t.me/example_support'

function mountHome(settings: Record<string, unknown> = {}) {
  appStore.cachedPublicSettings = { site_name: 'Test site', ...settings }
  return mount(HomeView, {
    global: {
      stubs: {
        RouterLink: RouterLinkStub,
        LocaleSwitcher: { template: '<div />' },
        Icon: { template: '<span />' },
      },
    },
  })
}

describe('HomeView Telegram contact link', () => {
  beforeEach(() => {
    appStore.telegramUrl = ''
    localStorage.clear()
    vi.spyOn(window, 'matchMedia').mockReturnValue({ matches: false } as MediaQueryList)
  })

  it('shows the link in the header and footer when configured', () => {
    const wrapper = mountHome({ telegram_url: TG })

    for (const selector of ['[data-test="home-telegram"]', '[data-test="home-telegram-footer"]']) {
      const link = wrapper.get(selector)
      expect(link.attributes('href')).toBe(TG)
      expect(link.attributes('target')).toBe('_blank')
      expect(link.attributes('rel')).toBe('noopener noreferrer')
    }
  })

  it('shows the link on the compact home page', () => {
    const wrapper = mountHome({ telegram_url: TG, compact_home_enabled: true })

    expect(wrapper.get('[data-test="home-telegram-compact"]').attributes('href')).toBe(TG)
  })

  it('falls back to the store value before public settings are cached', () => {
    appStore.telegramUrl = TG
    const wrapper = mountHome()

    expect(wrapper.get('[data-test="home-telegram"]').attributes('href')).toBe(TG)
  })

  it('renders nothing when unset or when the URL is not safe', () => {
    expect(mountHome().find('[data-test="home-telegram"]').exists()).toBe(false)

    const unsafe = mountHome({ telegram_url: 'javascript:alert(1)' })
    expect(unsafe.find('[data-test="home-telegram"]').exists()).toBe(false)
    expect(unsafe.find('[data-test="home-telegram-footer"]').exists()).toBe(false)
  })
})
