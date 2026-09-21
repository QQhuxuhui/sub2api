import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import UsdtWithdrawCalculator from '../UsdtWithdrawCalculator.vue'

const getCryptoQuote = vi.hoisted(() => vi.fn())
const copyToClipboard = vi.hoisted(() => vi.fn())

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})
vi.mock('@/api/payment', () => ({ paymentAPI: { getCryptoQuote } }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard }) }))

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

  it('shows one thing by default: the amount that must arrive, ready to copy', async () => {
    getCryptoQuote.mockResolvedValue(quote('4.480'))
    const wrapper = mount(UsdtWithdrawCalculator, { props: { orderId: 395 } })
    await flushPromises()

    expect(getCryptoQuote).toHaveBeenCalledWith(395)
    expect(wrapper.get('[data-test="usdt-calc-arrive"]').text()).toBe('4.48')
    expect(wrapper.text()).toContain('TRC20')
    // Nothing to fill in, nothing to choose.
    expect(wrapper.findAll('input')).toHaveLength(0)
    expect(wrapper.find('[data-test="usdt-calc-result"]').exists()).toBe(false)

    await wrapper.get('[data-test="usdt-calc-copy-arrive"]').trigger('click')
    expect(copyToClipboard).toHaveBeenCalledWith('4.48')
    wrapper.unmount()
  })

  it('works out the exchange withdrawal amount from a single input', async () => {
    getCryptoQuote.mockResolvedValue(quote('4.48'))
    const wrapper = mount(UsdtWithdrawCalculator, { props: { orderId: 1 } })
    await flushPromises()

    await wrapper.get('[data-test="usdt-calc-open"]').trigger('click')
    expect(wrapper.findAll('input')).toHaveLength(1)
    expect(wrapper.find('[data-test="usdt-calc-result"]').exists()).toBe(false)

    await wrapper.get('[data-test="usdt-calc-fee"]').setValue('1')
    expect(wrapper.get('[data-test="usdt-calc-result"]').text()).toBe('5.48')
    await wrapper.get('[data-test="usdt-calc-copy"]').trigger('click')
    expect(copyToClipboard).toHaveBeenLastCalledWith('5.48')
    wrapper.unmount()
  })

  it('does exact decimal arithmetic and ignores malformed fees', async () => {
    getCryptoQuote.mockResolvedValue(quote('0.1'))
    const wrapper = mount(UsdtWithdrawCalculator, { props: { orderId: 1 } })
    await flushPromises()
    await wrapper.get('[data-test="usdt-calc-open"]').trigger('click')
    const fee = wrapper.get('[data-test="usdt-calc-fee"]')

    await fee.setValue('0.2')
    expect(wrapper.get('[data-test="usdt-calc-result"]').text()).toBe('0.3')
    await fee.setValue('0,015') // comma decimal separator
    expect(wrapper.get('[data-test="usdt-calc-result"]').text()).toBe('0.115')
    for (const bad of ['abc', '-1', '1.2.3', '1.1234567']) {
      await fee.setValue(bad)
      expect(wrapper.find('[data-test="usdt-calc-result"]').exists()).toBe(false)
    }
    wrapper.unmount()
  })

  it('waits for the payer to pick a network, then shows the amount', async () => {
    getCryptoQuote.mockResolvedValueOnce(quote('', '')).mockResolvedValue(quote('4.48', 'binance'))
    const wrapper = mount(UsdtWithdrawCalculator, { props: { orderId: 1 } })
    await flushPromises()
    expect(wrapper.find('[data-test="usdt-calc-waiting"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="usdt-calc-open"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('payment.usdtCalc.rule') // the rule is stated even before the amount is known

    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    expect(wrapper.get('[data-test="usdt-calc-arrive"]').text()).toBe('4.48')
    expect(wrapper.text()).toContain('BSC')
    wrapper.unmount()
  })

  it('survives a failing gateway and stops polling once unmounted', async () => {
    getCryptoQuote.mockRejectedValueOnce(new Error('gateway down')).mockResolvedValue(quote('4.48'))
    const wrapper = mount(UsdtWithdrawCalculator, { props: { orderId: 1 } })
    await flushPromises()
    expect(wrapper.find('[data-test="usdt-calc-waiting"]').exists()).toBe(true)
    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    expect(wrapper.get('[data-test="usdt-calc-arrive"]').text()).toBe('4.48')

    wrapper.unmount()
    const calls = getCryptoQuote.mock.calls.length
    await vi.advanceTimersByTimeAsync(60000)
    expect(getCryptoQuote.mock.calls.length).toBe(calls)
  })
})
