import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import UsdtWithdrawCalculator from '../UsdtWithdrawCalculator.vue'

const getCryptoQuote = vi.hoisted(() => vi.fn())
const copyToClipboard = vi.hoisted(() => vi.fn())

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
    }),
  }
})

vi.mock('@/api/payment', () => ({ paymentAPI: { getCryptoQuote } }))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copied: { value: false }, copyToClipboard }),
}))

const quote = (amount: string, network = 'tron') => ({ data: { network, token: 'USDT', amount } })

describe('UsdtWithdrawCalculator', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    getCryptoQuote.mockReset()
    copyToClipboard.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('adds the exchange fee on top of the amount that must arrive', async () => {
    getCryptoQuote.mockResolvedValue(quote('4.48'))
    const wrapper = mount(UsdtWithdrawCalculator, { props: { orderId: 395 } })
    await flushPromises()

    expect(getCryptoQuote).toHaveBeenCalledWith(395)
    expect((wrapper.get('[data-test="usdt-calc-arrive"]').element as HTMLInputElement).value).toBe('4.48')
    // No fee yet: nothing to copy, only a nudge.
    expect(wrapper.find('[data-test="usdt-calc-result"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="usdt-calc-need-fee"]').exists()).toBe(true)

    await wrapper.get('[data-test="usdt-calc-fee"]').setValue('1')
    expect(wrapper.get('[data-test="usdt-calc-result"]').text()).toContain('5.48')

    await wrapper.get('[data-test="usdt-calc-copy"]').trigger('click')
    expect(copyToClipboard).toHaveBeenCalledWith('5.48')
    wrapper.unmount()
  })

  it('does exact decimal arithmetic', async () => {
    getCryptoQuote.mockResolvedValue(quote('0.1'))
    const wrapper = mount(UsdtWithdrawCalculator, { props: { orderId: 1 } })
    await flushPromises()

    await wrapper.get('[data-test="usdt-calc-fee"]').setValue('0.2')
    const text = wrapper.get('[data-test="usdt-calc-result"]').text()
    expect(text).toContain('0.3')
    expect(text).not.toContain('0.30000')

    await wrapper.get('[data-test="usdt-calc-arrive"]').setValue('21.59')
    await wrapper.get('[data-test="usdt-calc-fee"]').setValue('0.015')
    expect(wrapper.get('[data-test="usdt-calc-result"]').text()).toContain('21.605')
    wrapper.unmount()
  })

  it('sends the plain amount from a personal wallet', async () => {
    getCryptoQuote.mockResolvedValue(quote('4.48', 'binance'))
    const wrapper = mount(UsdtWithdrawCalculator, { props: { orderId: 1 } })
    await flushPromises()

    await wrapper.get('[data-test="usdt-calc-source-wallet"]').trigger('click')
    expect(wrapper.find('[data-test="usdt-calc-fee"]').exists()).toBe(false)
    const result = wrapper.get('[data-test="usdt-calc-result"]').text()
    expect(result).toContain('4.48')
    expect(result).toContain('payment.usdtCalc.noteWallet')
    wrapper.unmount()
  })

  it('waits for the payer to pick a network, then fills the amount in', async () => {
    getCryptoQuote.mockResolvedValueOnce(quote('', '')).mockResolvedValue(quote('4.48'))
    const wrapper = mount(UsdtWithdrawCalculator, { props: { orderId: 1 } })
    await flushPromises()
    const arrive = () => (wrapper.get('[data-test="usdt-calc-arrive"]').element as HTMLInputElement).value
    expect(arrive()).toBe('')
    expect(wrapper.text()).toContain('payment.usdtCalc.arriveWaiting')

    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    expect(arrive()).toBe('4.48')
    wrapper.unmount()
  })

  it('never overwrites an amount the payer typed, and survives a failing gateway', async () => {
    getCryptoQuote.mockRejectedValueOnce(new Error('gateway down')).mockResolvedValue(quote('4.48'))
    const wrapper = mount(UsdtWithdrawCalculator, { props: { orderId: 1 } })
    await flushPromises()

    await wrapper.get('[data-test="usdt-calc-arrive"]').setValue('4.49')
    await wrapper.get('[data-test="usdt-calc-fee"]').setValue('1')
    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()

    expect((wrapper.get('[data-test="usdt-calc-arrive"]').element as HTMLInputElement).value).toBe('4.49')
    expect(wrapper.get('[data-test="usdt-calc-result"]').text()).toContain('5.49')
    wrapper.unmount()
  })

  it('shows no result for malformed input and stops polling once unmounted', async () => {
    getCryptoQuote.mockResolvedValue(quote('4.48'))
    const wrapper = mount(UsdtWithdrawCalculator, { props: { orderId: 1 } })
    await flushPromises()

    for (const bad of ['abc', '-1', '1.2.3', '1.1234567']) {
      await wrapper.get('[data-test="usdt-calc-fee"]').setValue(bad)
      expect(wrapper.find('[data-test="usdt-calc-result"]').exists()).toBe(false)
    }
    // A comma decimal separator is accepted.
    await wrapper.get('[data-test="usdt-calc-fee"]').setValue('0,5')
    expect(wrapper.get('[data-test="usdt-calc-result"]').text()).toContain('4.98')

    wrapper.unmount()
    const calls = getCryptoQuote.mock.calls.length
    await vi.advanceTimersByTimeAsync(60000)
    expect(getCryptoQuote.mock.calls.length).toBe(calls)
  })
})
