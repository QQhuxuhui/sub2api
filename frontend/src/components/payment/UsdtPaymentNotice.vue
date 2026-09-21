<template>
  <div
    data-test="usdt-payment-notice"
    role="alert"
    class="rounded-xl border-2 border-amber-400 bg-amber-50 p-4 dark:border-amber-500/70 dark:bg-amber-950/30"
  >
    <div class="flex items-center gap-2">
      <svg class="h-5 w-5 flex-shrink-0 text-amber-600 dark:text-amber-400" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
        <path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m0 3.75h.008M10.34 3.94 1.82 18a1.9 1.9 0 0 0 1.64 2.85h17.08A1.9 1.9 0 0 0 22.18 18L13.66 3.94a1.9 1.9 0 0 0-3.32 0Z" />
      </svg>
      <p class="text-base font-bold text-amber-800 dark:text-amber-200">{{ t('payment.usdtNotice.title') }}</p>
    </div>
    <ul class="mt-2 list-disc space-y-1 pl-5 text-sm leading-6 text-amber-900 dark:text-amber-100">
      <li><span class="font-semibold">{{ t('payment.usdtNotice.exactAmount') }}</span></li>
      <li>{{ t('payment.usdtNotice.exchangeFee') }}</li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'

const { t } = useI18n()
</script>
