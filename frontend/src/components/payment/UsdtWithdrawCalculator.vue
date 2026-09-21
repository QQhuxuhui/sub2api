<template>
  <div
    data-test="usdt-calculator"
    class="rounded-xl border-2 border-amber-400 bg-amber-50 p-4 dark:border-amber-500/70 dark:bg-amber-950/30"
  >
    <!-- The one thing every payer must know. -->
    <p class="text-sm text-amber-900 dark:text-amber-100">{{ t('payment.usdtCalc.mustArrive') }}</p>
    <div v-if="arriveUnits !== null" class="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1">
      <span data-test="usdt-calc-arrive" class="font-mono text-3xl font-bold text-gray-900 dark:text-white">{{ arriveText }}</span>
      <span class="text-sm font-medium text-gray-600 dark:text-gray-300">{{ tokenLabel }}<template v-if="networkLabel"> · {{ networkLabel }}</template></span>
      <button type="button" data-test="usdt-calc-copy-arrive" class="btn btn-secondary px-3 py-1 text-sm" @click="copyToClipboard(arriveText)">
        {{ t('payment.usdtCalc.copy') }}
      </button>
    </div>
    <p v-else data-test="usdt-calc-waiting" class="mt-1 text-base font-semibold text-gray-900 dark:text-white">{{ t('payment.usdtCalc.waiting') }}</p>
    <p class="mt-1 text-xs text-amber-800 dark:text-amber-200">{{ t('payment.usdtCalc.rule') }}</p>

    <!-- Only exchange withdrawals need arithmetic, so it stays folded away. -->
    <template v-if="arriveUnits !== null">
      <button
        v-if="!open"
        type="button"
        data-test="usdt-calc-open"
        class="mt-3 text-sm font-medium text-amber-900 underline underline-offset-2 dark:text-amber-100"
        @click="open = true"
      >
        {{ t('payment.usdtCalc.fromExchange') }}
      </button>
      <div v-else class="mt-3 flex flex-wrap items-center gap-x-2 gap-y-2 border-t border-amber-300/70 pt-3 text-sm text-gray-800 dark:border-amber-500/40 dark:text-gray-100">
        <label for="usdt-calc-fee">{{ t('payment.usdtCalc.fee') }}</label>
        <input
          id="usdt-calc-fee"
          v-model="feeInput"
          data-test="usdt-calc-fee"
          type="text"
          inputmode="decimal"
          class="input w-24 px-2 py-1 font-mono"
          placeholder="0"
        />
        <div v-if="result" class="flex basis-full flex-wrap items-center gap-x-3 gap-y-1">
          <span>{{ t('payment.usdtCalc.enter') }}</span>
          <span data-test="usdt-calc-result" class="font-mono text-2xl font-bold text-gray-900 dark:text-white">{{ result }}</span>
          <button type="button" data-test="usdt-calc-copy" class="btn btn-secondary px-3 py-1 text-sm" @click="copyToClipboard(result)">
            {{ t('payment.usdtCalc.copy') }}
          </button>
        </div>
        <p class="basis-full text-xs text-gray-500 dark:text-gray-400">{{ t('payment.usdtCalc.feeHint') }}</p>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { paymentAPI } from '@/api/payment'
import { useClipboard } from '@/composables/useClipboard'

// Stablecoin amounts never need more than 6 decimals; working in integer
// micro-units keeps 4.48 + 1 from turning into 5.4800000000000004.
const UNIT = 1_000_000
const POLL_UNTIL_QUOTED_MS = 5000
// Keep refreshing afterwards: the payer may still change network in the cashier.
const POLL_AFTER_QUOTED_MS = 20000

const NETWORK_LABELS: Record<string, string> = {
  tron: 'TRC20',
  binance: 'BSC',
  polygon: 'Polygon',
  ethereum: 'ERC20',
}

const props = defineProps<{ orderId: number }>()

const { t } = useI18n()
const { copyToClipboard } = useClipboard()

const open = ref(false)
const feeInput = ref('')
const quoteAmount = ref('')
const quoteToken = ref('')
const quoteNetwork = ref('')

let timer: ReturnType<typeof setTimeout> | null = null
let stopped = false

function toUnits(raw: string): number | null {
  const text = raw.trim().replace(',', '.')
  if (!/^\d+(\.\d{1,6})?$/.test(text)) return null
  const units = Math.round(Number(text) * UNIT)
  return Number.isSafeInteger(units) ? units : null
}

function fromUnits(units: number): string {
  const whole = Math.floor(units / UNIT)
  const fraction = String(units % UNIT).padStart(6, '0').replace(/0+$/, '')
  return fraction ? `${whole}.${fraction}` : String(whole)
}

const tokenLabel = computed(() => quoteToken.value || 'USDT')
const networkLabel = computed(() => NETWORK_LABELS[quoteNetwork.value] || quoteNetwork.value)
const arriveUnits = computed(() => {
  const units = toUnits(quoteAmount.value)
  return units !== null && units > 0 ? units : null
})
const arriveText = computed(() => (arriveUnits.value === null ? '' : fromUnits(arriveUnits.value)))

const result = computed(() => {
  const fee = toUnits(feeInput.value)
  if (arriveUnits.value === null || fee === null) return ''
  return fromUnits(arriveUnits.value + fee)
})

async function refreshQuote() {
  let delay = POLL_UNTIL_QUOTED_MS
  try {
    const res = await paymentAPI.getCryptoQuote(props.orderId)
    if (stopped) return
    quoteToken.value = res.data.token || ''
    quoteNetwork.value = res.data.network || ''
    quoteAmount.value = res.data.amount || ''
    if (quoteAmount.value) delay = POLL_AFTER_QUOTED_MS
  } catch {
    // Without a quote the card still states the rule; keep trying quietly.
  }
  if (!stopped) timer = setTimeout(refreshQuote, delay)
}

onMounted(refreshQuote)
onUnmounted(() => {
  stopped = true
  if (timer) clearTimeout(timer)
})
</script>
