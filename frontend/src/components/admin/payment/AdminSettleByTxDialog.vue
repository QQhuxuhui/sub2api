<template>
  <BaseDialog :show="show" :title="t('payment.admin.settleByTx.title')" width="normal" @close="emit('cancel')">
    <form id="settle-by-tx-form" class="space-y-4" @submit.prevent="verify">
      <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('payment.admin.settleByTx.intro') }}</p>

      <div class="rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-700">
        <div class="flex justify-between">
          <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.orderId') }}</span>
          <span class="font-mono text-gray-900 dark:text-white">#{{ order?.id }}</span>
        </div>
        <div class="mt-1 flex justify-between">
          <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.creditedAmount') }}</span>
          <span class="font-medium text-gray-900 dark:text-white">{{ creditedAmountSymbol }}{{ order?.amount?.toFixed(2) }}</span>
        </div>
        <div class="mt-1 flex justify-between">
          <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.payAmount') }}</span>
          <span class="font-medium text-gray-900 dark:text-white">{{ paymentAmountSymbol }}{{ order?.pay_amount?.toFixed(2) }}</span>
        </div>
      </div>

      <div>
        <label class="input-label" for="settle-tx-hash">{{ t('payment.admin.settleByTx.txHash') }}</label>
        <input
          id="settle-tx-hash"
          v-model.trim="form.txHash"
          data-test="settle-tx-hash"
          type="text"
          class="input font-mono text-xs"
          :placeholder="t('payment.admin.settleByTx.txHashPlaceholder')"
          autocomplete="off"
          spellcheck="false"
          required
        />
      </div>

      <div>
        <label class="input-label" for="settle-network">{{ t('payment.admin.settleByTx.network') }}</label>
        <select id="settle-network" v-model="form.network" class="input">
          <option value="">{{ t('payment.admin.settleByTx.networkAuto') }}</option>
          <option v-for="n in NETWORKS" :key="n.value" :value="n.value">{{ n.label }}</option>
        </select>
      </div>

      <div
        v-if="errorMessage"
        data-test="settle-error"
        class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300"
      >
        <p class="font-medium">{{ errorMessage }}</p>
        <p v-if="errorDetail" class="mt-1 break-all text-xs opacity-80">{{ errorDetail }}</p>
      </div>

      <div
        v-if="preview"
        data-test="settle-preview"
        class="space-y-1 rounded-lg border p-3 text-sm"
        :class="hasShortfall
          ? 'border-amber-300 bg-amber-50 dark:border-amber-600/60 dark:bg-amber-950/30'
          : 'border-green-300 bg-green-50 dark:border-green-700/60 dark:bg-green-950/30'"
      >
        <p class="font-semibold text-gray-900 dark:text-white">{{ t('payment.admin.settleByTx.verified') }}</p>
        <div v-for="row in previewRows" :key="row.label" class="flex justify-between gap-4">
          <span class="flex-shrink-0 text-gray-500 dark:text-gray-400">{{ row.label }}</span>
          <span class="break-all text-right font-mono text-xs text-gray-900 dark:text-white">{{ row.value }}</span>
        </div>
        <p class="pt-1 text-xs leading-5 text-gray-600 dark:text-gray-300">{{ t('payment.admin.settleByTx.fraudWarning') }}</p>
        <p v-if="hasShortfall" class="pt-1 font-medium text-amber-800 dark:text-amber-200">
          {{ t('payment.admin.settleByTx.shortfallWarning', { shortfall: preview.shortfall, token: preview.token, amount: `${creditedAmountSymbol}${order?.amount?.toFixed(2)}` }) }}
        </p>
      </div>
    </form>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="emit('cancel')">{{ t('common.cancel') }}</button>
        <button
          v-if="!preview"
          type="submit"
          form="settle-by-tx-form"
          data-test="settle-verify"
          class="btn btn-primary"
          :disabled="busy || !form.txHash"
        >
          {{ busy ? t('common.processing') : t('payment.admin.settleByTx.verify') }}
        </button>
        <button
          v-else
          type="button"
          data-test="settle-confirm"
          class="btn btn-primary"
          :disabled="busy"
          @click="settle"
        >
          {{ busy ? t('common.processing') : t('payment.admin.settleByTx.confirm') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { reactive, ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { adminPaymentAPI } from '@/api/admin/payment'
import type { SettleByTxResult } from '@/api/admin/payment'
import { extractApiErrorMessage, extractI18nErrorMessage } from '@/utils/apiError'
import { formatOrderDateTime } from '@/components/payment/orderUtils'
import { currencySymbol } from '@/components/payment/currency'
import type { PaymentOrder } from '@/types/payment'

const NETWORKS = [
  { value: 'tron', label: 'TRON (TRC20)' },
  { value: 'binance', label: 'BSC (BEP20)' },
  { value: 'polygon', label: 'Polygon' },
  { value: 'ethereum', label: 'Ethereum (ERC20)' },
]

const props = defineProps<{ show: boolean; order: PaymentOrder | null }>()
const emit = defineEmits<{ (e: 'cancel'): void; (e: 'settled', result: SettleByTxResult): void }>()

const { t } = useI18n()

const form = reactive({ txHash: '', network: '' })
const busy = ref(false)
const preview = ref<SettleByTxResult | null>(null)
const errorMessage = ref('')
const errorDetail = ref('')

type RequestSnapshot = {
  orderId: number
  txHash: string
  network: string
  generation: number
}

let generation = 0
let verifiedInput: RequestSnapshot | null = null

const creditedAmountSymbol = computed(() => currencySymbol('USD'))
const paymentAmountSymbol = computed(() => currencySymbol(props.order?.currency))
const hasShortfall = computed(() => !!preview.value && Number(preview.value.shortfall) > 0)

const previewRows = computed(() => {
  const p = preview.value
  if (!p) return []
  return [
    { label: t('payment.admin.settleByTx.network'), value: p.network },
    { label: t('payment.admin.settleByTx.received'), value: `${p.received_amount} ${p.token}` },
    { label: t('payment.admin.settleByTx.expected'), value: `${p.expected_amount} ${p.expected_token} (${p.expected_network})` },
    { label: t('payment.admin.settleByTx.from'), value: p.from },
    { label: t('payment.admin.settleByTx.to'), value: p.to },
    { label: t('payment.admin.settleByTx.blockTime'), value: formatOrderDateTime(p.block_time) },
    { label: t('payment.admin.settleByTx.confirmations'), value: String(p.confirmations) },
  ]
})

function invalidateRequests() {
  generation++
  verifiedInput = null
  preview.value = null
  busy.value = false
  errorMessage.value = ''
  errorDetail.value = ''
}

// Invalidate synchronously, including edits restored before the next render.
watch(() => [form.txHash, form.network], invalidateRequests, { flush: 'sync' })
watch([() => props.show, () => props.order?.id], () => {
  invalidateRequests()
  form.txHash = ''
  form.network = ''
}, { flush: 'sync' })

function isCurrent(snapshot: RequestSnapshot) {
  return props.show && snapshot.generation === generation &&
    snapshot.orderId === props.order?.id &&
    snapshot.txHash === form.txHash && snapshot.network === form.network
}

function showError(err: unknown) {
  const raw = extractApiErrorMessage(err, '')
  errorMessage.value = extractI18nErrorMessage(err, t, 'payment.errors', t('common.error'))
  errorDetail.value = raw && raw !== errorMessage.value ? raw : ''
}

async function run(dryRun: boolean, input: Omit<RequestSnapshot, 'generation'>) {
  const snapshot: RequestSnapshot = { ...input, generation: ++generation }
  busy.value = true
  errorMessage.value = ''
  errorDetail.value = ''
  if (dryRun) {
    verifiedInput = null
    preview.value = null
  }
  try {
    const res = await adminPaymentAPI.settleByTx(snapshot.orderId, {
      tx_hash: snapshot.txHash,
      network: snapshot.network || undefined,
      dry_run: dryRun,
    })
    if (!isCurrent(snapshot)) return
    if (dryRun) {
      verifiedInput = snapshot
      preview.value = res.data
    } else {
      emit('settled', res.data)
    }
  } catch (err) {
    if (!isCurrent(snapshot)) return
    verifiedInput = null
    preview.value = null
    showError(err)
  } finally {
    if (isCurrent(snapshot)) busy.value = false
  }
}

function verify() {
  if (!props.show || !props.order || busy.value || !form.txHash) return
  return run(true, { orderId: props.order.id, txHash: form.txHash, network: form.network })
}

function settle() {
  if (busy.value || !preview.value || !verifiedInput || !isCurrent(verifiedInput)) return
  return run(false, verifiedInput)
}
</script>
