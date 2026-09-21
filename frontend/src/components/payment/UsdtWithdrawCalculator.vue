<template>
  <div data-test="usdt-calculator" class="card space-y-4 p-5">
    <div>
      <p class="text-base font-semibold text-gray-900 dark:text-white">{{ t('payment.usdtCalc.title') }}</p>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('payment.usdtCalc.intro') }}</p>
    </div>

    <!-- Where the money comes from decides whether a fee eats into the amount. -->
    <div class="grid grid-cols-2 gap-2">
      <button
        v-for="option in SOURCES"
        :key="option"
        type="button"
        :data-test="`usdt-calc-source-${option}`"
        :class="[
          'rounded-lg border px-3 py-2 text-sm font-medium transition-colors',
          source === option
            ? 'border-[#26A17B] bg-emerald-50 text-gray-900 dark:border-[#50AF95] dark:bg-emerald-950 dark:text-gray-100'
            : 'border-gray-200 text-gray-600 hover:bg-gray-50 dark:border-dark-600 dark:text-gray-300 dark:hover:bg-dark-700',
        ]"
        @click="source = option"
      >
        {{ t(`payment.usdtCalc.source_${option}`) }}
      </button>
    </div>

    <div class="grid gap-3 sm:grid-cols-2">
      <div>
        <label class="input-label" for="usdt-calc-arrive">{{ t('payment.usdtCalc.arrive', { token: tokenLabel }) }}</label>
        <input
          id="usdt-calc-arrive"
          v-model="arriveInput"
          data-test="usdt-calc-arrive"
          type="text"
          inputmode="decimal"
          class="input font-mono"
          :placeholder="t('payment.usdtCalc.arrivePlaceholder')"
          @input="arriveEdited = true"
        />
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
          {{ quoteAmount ? t('payment.usdtCalc.arriveFromGateway', { network: networkLabel }) : t('payment.usdtCalc.arriveWaiting') }}
        </p>
      </div>
      <div v-if="source === 'exchange'">
        <label class="input-label" for="usdt-calc-fee">{{ t('payment.usdtCalc.fee', { token: tokenLabel }) }}</label>
        <input
          id="usdt-calc-fee"
          v-model="feeInput"
          data-test="usdt-calc-fee"
          type="text"
          inputmode="decimal"
          class="input font-mono"
          :placeholder="t('payment.usdtCalc.feePlaceholder')"
        />
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('payment.usdtCalc.feeHint') }}</p>
      </div>
    </div>

    <div
      v-if="result"
      data-test="usdt-calc-result"
      class="rounded-xl border-2 border-[#26A17B] bg-emerald-50 p-4 dark:border-[#50AF95] dark:bg-emerald-950/40"
    >
      <p class="text-sm text-gray-600 dark:text-gray-300">
        {{ source === 'exchange' ? t('payment.usdtCalc.resultExchange') : t('payment.usdtCalc.resultWallet') }}
      </p>
      <div class="mt-1 flex flex-wrap items-center gap-3">
        <span class="font-mono text-2xl font-bold text-gray-900 dark:text-white">{{ result }}</span>
        <span class="text-sm font-medium text-gray-600 dark:text-gray-300">{{ tokenLabel }}</span>
        <button type="button" data-test="usdt-calc-copy" class="btn btn-secondary px-3 py-1 text-sm" @click="copyToClipboard(result)">
          {{ copied ? t('payment.usdtCalc.copied') : t('payment.usdtCalc.copy') }}
        </button>
      </div>
      <p class="mt-2 text-xs leading-5 text-gray-600 dark:text-gray-300">
        {{ source === 'exchange' ? t('payment.usdtCalc.noteExchange', { arrive: arriveText, fee: feeText, token: tokenLabel }) : t('payment.usdtCalc.noteWallet') }}
      </p>
    </div>
    <p v-else-if="source === 'exchange' && arriveUnits !== null" data-test="usdt-calc-need-fee" class="text-sm text-amber-700 dark:text-amber-300">
      {{ t('payment.usdtCalc.needFee') }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { paymentAPI } from '@/api/payment'
import { useClipboard } from '@/composables/useClipboard'

const SOURCES = ['exchange', 'wallet'] as const
type Source = (typeof SOURCES)[number]

// Stablecoin amounts never need more than 6 decimals; working in integer
// micro-units keeps 4.48 + 1 from turning into 5.4800000000000004.
const UNIT = 1_000_000
const POLL_UNTIL_QUOTED_MS = 5000
// Keep refreshing afterwards: the payer may still change network in the cashier.
const POLL_AFTER_QUOTED_MS = 20000

const NETWORK_LABELS: Record<string, string> = {
  tron: 'TRON (TRC20)',
  binance: 'BSC (BEP20)',
  polygon: 'Polygon',
  ethereum: 'Ethereum (ERC20)',
}

const props = defineProps<{ orderId: number }>()

const { t } = useI18n()
const { copied, copyToClipboard } = useClipboard()

const source = ref<Source>('exchange')
const arriveInput = ref('')
const arriveEdited = ref(false)
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
  const units = toUnits(arriveInput.value)
  return units !== null && units > 0 ? units : null
})
const feeUnits = computed(() => toUnits(feeInput.value))
const arriveText = computed(() => (arriveUnits.value === null ? '' : fromUnits(arriveUnits.value)))
const feeText = computed(() => (feeUnits.value === null ? '' : fromUnits(feeUnits.value)))

const result = computed(() => {
  if (arriveUnits.value === null) return ''
  if (source.value === 'wallet') return fromUnits(arriveUnits.value)
  if (feeUnits.value === null) return ''
  return fromUnits(arriveUnits.value + feeUnits.value)
})

async function refreshQuote() {
  let delay = POLL_UNTIL_QUOTED_MS
  try {
    const res = await paymentAPI.getCryptoQuote(props.orderId)
    if (stopped) return
    quoteToken.value = res.data.token || ''
    quoteNetwork.value = res.data.network || ''
    quoteAmount.value = res.data.amount || ''
    // Never overwrite a figure the payer typed in from the cashier.
    if (quoteAmount.value && !arriveEdited.value) arriveInput.value = quoteAmount.value
    if (quoteAmount.value) delay = POLL_AFTER_QUOTED_MS
  } catch {
    // The calculator still works with a hand-typed amount; keep trying quietly.
  }
  if (!stopped) timer = setTimeout(refreshQuote, delay)
}

onMounted(refreshQuote)
onUnmounted(() => {
  stopped = true
  if (timer) clearTimeout(timer)
})
</script>
