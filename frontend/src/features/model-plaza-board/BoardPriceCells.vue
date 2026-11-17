<template>
  <div class="grid grid-cols-[repeat(auto-fit,minmax(84px,1fr))] gap-x-3 gap-y-2">
    <template v-if="price.billing === 'token'">
      <div v-for="cell in tokenCells" :key="cell.key" class="flex min-w-0 flex-col gap-0.5">
        <span class="text-xs text-gray-500 dark:text-dark-400">{{ cell.label }}</span>
        <span class="flex flex-wrap items-baseline gap-x-1 font-mono text-sm font-semibold tabular-nums text-gray-900 dark:text-white">
          <span>{{ cell.value }}</span>
          <span class="whitespace-nowrap font-sans text-xs font-normal text-gray-500 dark:text-dark-400">{{ t('plazaBoard.price.perMillion') }}</span>
        </span>
      </div>
    </template>
    <div v-else-if="price.billing === 'request'" class="col-span-full flex min-w-0 flex-col gap-0.5">
      <span class="text-xs text-gray-500 dark:text-dark-400">
        {{ t('plazaBoard.price.single') }}<template v-if="price.tierLabel"> · {{ price.tierLabel }}</template>
      </span>
      <span class="whitespace-nowrap font-mono text-sm font-semibold tabular-nums text-gray-900 dark:text-white">
        {{ price.request }}
        <span class="font-sans text-xs font-normal text-gray-500 dark:text-dark-400">
          {{ unitLabel }}<template v-if="price.from"> {{ t('plazaBoard.price.from') }}</template>
        </span>
      </span>
    </div>
    <span v-else class="col-span-full text-sm text-gray-400 dark:text-dark-500">{{ t('plazaBoard.price.unset') }}</span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { BILLING_MODE_IMAGE } from '@/constants/channel'
import type { PriceSummary } from './plazaBoard'

const props = defineProps<{ price: PriceSummary }>()
const { t } = useI18n()

const tokenCells = computed(() =>
  [
    { key: 'input', label: t('plazaBoard.price.input'), value: props.price.input },
    { key: 'output', label: t('plazaBoard.price.output'), value: props.price.output },
    { key: 'cacheRead', label: t('plazaBoard.price.cacheRead'), value: props.price.cacheRead }
  ].filter((cell) => cell.value != null)
)

const unitLabel = computed(() =>
  props.price.billingMode === BILLING_MODE_IMAGE ? t('plazaBoard.price.perImage') : t('plazaBoard.price.perRequest')
)
</script>
