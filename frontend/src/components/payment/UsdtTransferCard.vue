<template>
  <div data-test="usdt-transfer-card" class="card border-2 border-amber-400 bg-amber-50 p-5 dark:border-amber-500/70 dark:bg-amber-950/30">
    <p class="text-sm text-amber-900 dark:text-amber-100">{{ autoSettle ? t('payment.usdtQuote.withdraw') : t('payment.usdtQuote.mustArrive') }}</p>
    <div class="mt-1 flex flex-wrap items-center gap-3">
      <p data-test="usdt-transfer-amount" class="text-3xl font-bold tabular-nums text-gray-900 dark:text-white">
        {{ amountText }}
        <span v-if="networkLabel" class="ml-1 align-middle text-base font-semibold text-amber-700 dark:text-amber-300">{{ networkLabel }}</span>
      </p>
      <button data-test="usdt-transfer-copy" class="btn btn-secondary px-3 py-1 text-sm" @click="copyAmount">
        {{ copied ? t('payment.usdtQuote.copied') : t('payment.usdtQuote.copy') }}
      </button>
    </div>
    <p data-test="usdt-transfer-rule" class="mt-2 text-sm text-amber-900 dark:text-amber-100">{{ autoSettle ? t('payment.usdtQuote.feeDeducted') : t('payment.usdtQuote.exactOnly') }}</p>
    <p class="mt-1 text-xs text-amber-800 dark:text-amber-200">{{ t('payment.usdtQuote.help') }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatUsdtAmount, usdtNetworkLabel } from './usdtQuote'

const props = defineProps<{
  amount: number
  network?: string
  autoSettle?: boolean
}>()

const { t } = useI18n()
const copied = ref(false)
const amountText = computed(() => formatUsdtAmount(props.amount))
const networkLabel = computed(() => usdtNetworkLabel(props.network))

async function copyAmount() {
  try {
    await navigator.clipboard.writeText(String(props.amount))
    copied.value = true
    setTimeout(() => { copied.value = false }, 2000)
  } catch {
    copied.value = false
  }
}
</script>
