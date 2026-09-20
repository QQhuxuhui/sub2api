import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import AdminSettleByTxDialog from '../AdminSettleByTxDialog.vue'
import type { SettleByTxResult } from '@/api/admin/payment'
import type { PaymentOrder } from '@/types/payment'

const settleByTx = vi.hoisted(() => vi.fn())

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
    }),
  }
})

vi.mock('@/api/admin/payment', () => ({
  adminPaymentAPI: { settleByTx },
}))

const HASH = '0x2a003b114b896199d75998e3712f8cc1f32118ed62ff38419d397282b183c404'
const OTHER_HASH = HASH.replace('404', '405')

const order = { id: 395, amount: 30, pay_amount: 30, currency: 'CNY', status: 'EXPIRED', payment_type: 'usdt' } as PaymentOrder

const verified = {
  order_id: 395, order_status: 'EXPIRED', settled: false, tx_hash: HASH, network: 'binance', token: 'USDT',
  from: '0xeb2d2f1b8c558a40207669291fda468e50c8a0bb', to: '0x4c1349a30c3a91d2cd69329c48dfb02c10d812c7',
  received_amount: '4.47', expected_amount: '4.48', expected_network: 'binance', expected_token: 'USDT',
  shortfall: '0.01', allowed_shortfall: '1.5', block_time: '2026-09-20T07:00:51Z', confirmations: 8667,
}

function deferredResponse() {
  let resolve!: (value: { data: SettleByTxResult }) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<{ data: SettleByTxResult }>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

async function mountOpen() {
  const wrapper = mount(AdminSettleByTxDialog, {
    props: { show: false, order },
    global: { stubs: { BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' } } },
  })
  await wrapper.setProps({ show: true })
  return wrapper
}

describe('AdminSettleByTxDialog', () => {
  // Braces matter: a function returned from beforeEach is run as teardown.
  beforeEach(() => {
    settleByTx.mockReset()
  })

  it('verifies as a dry run first, then settles the same input', async () => {
    settleByTx.mockResolvedValueOnce({ data: verified }).mockResolvedValueOnce({ data: { ...verified, settled: true, order_status: 'COMPLETED' } })
    const wrapper = await mountOpen()

    expect(wrapper.find('[data-test="settle-confirm"]').exists()).toBe(false)
    await wrapper.find('[data-test="settle-tx-hash"]').setValue(`  ${HASH} `)
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(settleByTx).toHaveBeenLastCalledWith(395, { tx_hash: HASH, network: undefined, dry_run: true })
    const preview = wrapper.find('[data-test="settle-preview"]')
    expect(preview.text()).toContain('4.47 USDT')
    expect(preview.text()).toContain('payment.admin.settleByTx.shortfallWarning')
    expect(wrapper.emitted('settled')).toBeUndefined()

    await wrapper.find('[data-test="settle-confirm"]').trigger('click')
    await flushPromises()
    expect(settleByTx).toHaveBeenLastCalledWith(395, { tx_hash: HASH, network: undefined, dry_run: false })
    expect(wrapper.emitted('settled')).toHaveLength(1)
  })

  it('drops the verified preview when the input changes', async () => {
    settleByTx.mockResolvedValue({ data: verified })
    const wrapper = await mountOpen()
    await wrapper.find('[data-test="settle-tx-hash"]').setValue(HASH)
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(wrapper.find('[data-test="settle-confirm"]').exists()).toBe(true)

    await wrapper.find('[data-test="settle-tx-hash"]').setValue(HASH.replace('404', '405'))
    expect(wrapper.find('[data-test="settle-confirm"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="settle-verify"]').exists()).toBe(true)
  })

  it('shows the translated reason with the server detail and never offers confirm', async () => {
    settleByTx.mockImplementation(() => Promise.reject(Object.assign(new Error('the transaction pays 0xdead, not this order'), { reason: 'ADDRESS_MISMATCH' })))
    const wrapper = await mountOpen()
    await wrapper.find('[data-test="settle-tx-hash"]').setValue(HASH)
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    const error = wrapper.find('[data-test="settle-error"]')
    expect(error.exists()).toBe(true)
    expect(error.text()).toContain('0xdead')
    expect(wrapper.find('[data-test="settle-confirm"]').exists()).toBe(false)
    expect(wrapper.emitted('settled')).toBeUndefined()
  })

  it.each(['hash', 'network'])('discards an outstanding verification after changing %s', async (field) => {
    const request = deferredResponse()
    settleByTx.mockReturnValueOnce(request.promise)
    const wrapper = await mountOpen()
    await wrapper.find('[data-test="settle-tx-hash"]').setValue(HASH)
    await wrapper.find('form').trigger('submit')

    if (field === 'hash') {
      await wrapper.find('[data-test="settle-tx-hash"]').setValue(OTHER_HASH)
    } else {
      await wrapper.find('#settle-network').setValue('ethereum')
    }
    request.resolve({ data: verified })
    await flushPromises()

    expect(wrapper.find('[data-test="settle-preview"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="settle-confirm"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="settle-verify"]').attributes('disabled')).toBeUndefined()
  })

  it('does not revive order A preview after closing and opening order B', async () => {
    const request = deferredResponse()
    settleByTx.mockReturnValueOnce(request.promise)
    const wrapper = await mountOpen()
    await wrapper.find('[data-test="settle-tx-hash"]').setValue(HASH)
    await wrapper.find('form').trigger('submit')
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true, order: { ...order, id: 396 } })
    await wrapper.find('[data-test="settle-tx-hash"]').setValue(OTHER_HASH)
    request.resolve({ data: verified })
    await flushPromises()

    expect(wrapper.find('[data-test="settle-confirm"]').exists()).toBe(false)
  })

  it.each(['edit and restore', 'close and reopen'])('discards old verification even if the same input returns after %s', async (change) => {
    const request = deferredResponse()
    settleByTx.mockReturnValueOnce(request.promise)
    const wrapper = await mountOpen()
    await wrapper.find('[data-test="settle-tx-hash"]').setValue(HASH)
    await wrapper.find('form').trigger('submit')

    if (change === 'edit and restore') {
      await wrapper.find('[data-test="settle-tx-hash"]').setValue(OTHER_HASH)
    } else {
      await wrapper.setProps({ show: false })
      await wrapper.setProps({ show: true })
    }
    await wrapper.find('[data-test="settle-tx-hash"]').setValue(HASH)
    request.resolve({ data: verified })
    await flushPromises()

    expect(wrapper.find('[data-test="settle-confirm"]').exists()).toBe(false)
  })

  it('resets a verified preview when the order changes while still open', async () => {
    settleByTx.mockResolvedValueOnce({ data: verified })
    const wrapper = await mountOpen()
    await wrapper.find('[data-test="settle-tx-hash"]').setValue(HASH)
    await wrapper.find('#settle-network').setValue('binance')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(wrapper.find('[data-test="settle-confirm"]').exists()).toBe(true)

    await wrapper.setProps({ order: { ...order, id: 396 } })

    expect(wrapper.find('[data-test="settle-confirm"]').exists()).toBe(false)
    expect(wrapper.get<HTMLInputElement>('[data-test="settle-tx-hash"]').element.value).toBe('')
    expect(wrapper.get<HTMLSelectElement>('#settle-network').element.value).toBe('')
    expect(settleByTx).toHaveBeenCalledTimes(1)
  })

  it('preserves verification when the parent refreshes the same order', async () => {
    settleByTx.mockResolvedValueOnce({ data: verified })
    const wrapper = await mountOpen()
    await wrapper.find('[data-test="settle-tx-hash"]').setValue(HASH)
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    await wrapper.setProps({ order: { ...order } })

    expect(wrapper.find('[data-test="settle-confirm"]').exists()).toBe(true)
    expect(wrapper.get<HTMLInputElement>('[data-test="settle-tx-hash"]').element.value).toBe(HASH)
  })

  it.each(['resolve', 'reject'] as const)('an old request that will %s cannot release the new request busy state', async (outcome) => {
    const oldRequest = deferredResponse()
    const newRequest = deferredResponse()
    settleByTx.mockReturnValueOnce(oldRequest.promise).mockReturnValueOnce(newRequest.promise)
    const wrapper = await mountOpen()
    await wrapper.find('[data-test="settle-tx-hash"]').setValue(HASH)
    await wrapper.find('form').trigger('submit')
    await wrapper.setProps({ order: { ...order, id: 396 } })
    await wrapper.find('[data-test="settle-tx-hash"]').setValue(OTHER_HASH)
    await wrapper.find('form').trigger('submit')
    expect(settleByTx).toHaveBeenCalledTimes(2)

    if (outcome === 'resolve') oldRequest.resolve({ data: verified })
    else oldRequest.reject(new Error('stale verification failure'))
    await flushPromises()

    expect(wrapper.find('[data-test="settle-error"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="settle-confirm"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="settle-verify"]').attributes('disabled')).toBeDefined()
    await wrapper.find('form').trigger('submit')
    expect(settleByTx).toHaveBeenCalledTimes(2)

    newRequest.resolve({ data: { ...verified, order_id: 396, tx_hash: OTHER_HASH } })
    await flushPromises()
    expect(wrapper.get('[data-test="settle-confirm"]').attributes('disabled')).toBeUndefined()
  })

  it.each(['resolve', 'reject'] as const)('an old request that will %s cannot replace or clear a new verified preview', async (outcome) => {
    const oldRequest = deferredResponse()
    settleByTx.mockReturnValueOnce(oldRequest.promise).mockResolvedValueOnce({
      data: { ...verified, tx_hash: OTHER_HASH, received_amount: '5.00' },
    })
    const wrapper = await mountOpen()
    await wrapper.find('[data-test="settle-tx-hash"]').setValue(HASH)
    await wrapper.find('form').trigger('submit')
    await wrapper.find('[data-test="settle-tx-hash"]').setValue(OTHER_HASH)
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[data-test="settle-preview"]').text()).toContain('5.00 USDT')

    if (outcome === 'resolve') oldRequest.resolve({ data: verified })
    else oldRequest.reject(new Error('stale verification failure'))
    await flushPromises()

    expect(wrapper.get('[data-test="settle-preview"]').text()).toContain('5.00 USDT')
    expect(wrapper.find('[data-test="settle-error"]').exists()).toBe(false)
  })

  it.each(['hash', 'network'])('ignores a confirm click immediately after changing %s before the next render', async (field) => {
    settleByTx.mockResolvedValue({ data: verified })
    const wrapper = await mountOpen()
    await wrapper.find('[data-test="settle-tx-hash"]').setValue(HASH)
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    const confirm = wrapper.get('[data-test="settle-confirm"]').element

    if (field === 'hash') {
      const input = wrapper.get<HTMLInputElement>('[data-test="settle-tx-hash"]').element
      input.value = OTHER_HASH
      input.dispatchEvent(new Event('input', { bubbles: true }))
    } else {
      const select = wrapper.get<HTMLSelectElement>('#settle-network').element
      select.value = 'ethereum'
      select.dispatchEvent(new Event('change', { bubbles: true }))
    }
    confirm.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flushPromises()

    expect(settleByTx).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('settled')).toBeUndefined()
  })

  it.each(['resolve', 'reject'] as const)('ignores a settlement that will %s after switching sessions', async (outcome) => {
    const request = deferredResponse()
    settleByTx.mockResolvedValueOnce({ data: verified }).mockReturnValueOnce(request.promise)
    const wrapper = await mountOpen()
    await wrapper.find('[data-test="settle-tx-hash"]').setValue(HASH)
    await wrapper.find('#settle-network').setValue('binance')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    await wrapper.find('[data-test="settle-confirm"]').trigger('click')
    expect(settleByTx).toHaveBeenLastCalledWith(395, { tx_hash: HASH, network: 'binance', dry_run: false })
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true, order: { ...order, id: 396 } })

    if (outcome === 'resolve') request.resolve({ data: { ...verified, settled: true, order_status: 'COMPLETED' } })
    else request.reject(new Error('stale settlement failure'))
    await flushPromises()

    expect(wrapper.emitted('settled')).toBeUndefined()
    expect(wrapper.find('[data-test="settle-error"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="settle-confirm"]').exists()).toBe(false)
  })

  it('ignores a verification response and new submissions while closed', async () => {
    const request = deferredResponse()
    settleByTx.mockReturnValueOnce(request.promise)
    const wrapper = await mountOpen()
    await wrapper.find('[data-test="settle-tx-hash"]').setValue(HASH)
    await wrapper.find('form').trigger('submit')
    await wrapper.setProps({ show: false })
    request.resolve({ data: verified })
    await flushPromises()

    expect(wrapper.find('[data-test="settle-confirm"]').exists()).toBe(false)
    await wrapper.find('form').trigger('submit')
    expect(settleByTx).toHaveBeenCalledTimes(1)
  })
})
