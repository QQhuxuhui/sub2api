import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import AdminSettleByTxDialog from '../AdminSettleByTxDialog.vue'
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

const order = { id: 395, amount: 30, pay_amount: 30, currency: 'CNY', status: 'EXPIRED', payment_type: 'usdt' } as PaymentOrder

const verified = {
  order_id: 395, order_status: 'EXPIRED', settled: false, tx_hash: HASH, network: 'binance', token: 'USDT',
  from: '0xeb2d2f1b8c558a40207669291fda468e50c8a0bb', to: '0x4c1349a30c3a91d2cd69329c48dfb02c10d812c7',
  received_amount: '4.47', expected_amount: '4.48', expected_network: 'binance', expected_token: 'USDT',
  shortfall: '0.01', allowed_shortfall: '1.5', block_time: '2026-09-20T07:00:51Z', confirmations: 8667,
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
})
