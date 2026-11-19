<template>
  <div class="plaza-strip flex min-w-0 items-stretch gap-0.5" :class="size === 'sm' ? 'h-3' : 'h-4'" role="img" :aria-label="t('plazaBoard.strip')">
    <i
      v-for="sample in samples"
      :key="sample.t"
      :class="['min-w-[3px] max-w-[7px] flex-1 rounded-sm', LEVEL_CLASS[sampleLevel(sample.e)]]"
      :title="t('plazaBoard.sampleTitle', { time: formatSampleHour(sample.t), rate: formatSuccessRate(sample.e) })"
    ></i>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { formatSampleHour, formatSuccessRate, sampleLevel, type SampleLevel } from './plazaBoard'
import type { PlazaHealthSample } from './types'

// 一根 = 一个有调用的小时；没调用的小时不画，也不留灰色占位。
withDefaults(defineProps<{ samples: PlazaHealthSample[]; size?: 'sm' | 'md' }>(), { size: 'md' })
const { t } = useI18n()

const LEVEL_CLASS: Record<SampleLevel, string> = {
  excellent: 'bg-emerald-500',
  good: 'bg-emerald-300 dark:bg-emerald-400',
  warning: 'bg-amber-500',
  critical: 'bg-red-500'
}
</script>
