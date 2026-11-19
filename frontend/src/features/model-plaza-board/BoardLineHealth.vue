<template>
  <div class="flex min-w-0 items-center gap-2.5">
    <template v-if="health">
      <BoardHealthStrip class="flex-1" :samples="health.samples" :size="size" />
      <span v-if="agoText" class="flex-none whitespace-nowrap text-[11.5px] text-gray-400 dark:text-dark-500" data-testid="plaza-last-call">{{ agoText }}</span>
      <b :class="['flex-none text-[13px] font-semibold tabular-nums', RATE_CLASS[sampleLevel(health.error_rate)]]" data-testid="plaza-success-rate">
        {{ formatSuccessRate(health.error_rate) }}
      </b>
    </template>
    <span v-else class="flex items-center gap-1.5 text-xs text-gray-400 dark:text-dark-500" data-testid="plaza-idle">
      <span class="w-3.5 border-t-2 border-dashed border-gray-300 dark:border-dark-600" aria-hidden="true"></span>
      {{ t('plazaBoard.idle') }}
    </span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import BoardHealthStrip from './BoardHealthStrip.vue'
import { formatSuccessRate, hoursSinceLastSample, sampleLevel, type SampleLevel } from './plazaBoard'
import type { PlazaHealthLine } from './types'

/** 一条线路的状态条 + 成功率；最近一次调用较早时标出多久以前，近期无调用时给占位文案。 */
const props = withDefaults(defineProps<{ health: PlazaHealthLine | null; size?: 'sm' | 'md' }>(), { size: 'md' })
const { t } = useI18n()

const RATE_CLASS: Record<SampleLevel, string> = {
  excellent: 'text-emerald-600 dark:text-emerald-400',
  good: 'text-emerald-600 dark:text-emerald-400',
  warning: 'text-amber-600 dark:text-amber-400',
  critical: 'text-red-600 dark:text-red-400'
}

const agoText = computed(() => {
  const hours = hoursSinceLastSample(props.health)
  if (hours == null || hours < 2) return ''
  return hours < 24 ? t('plazaBoard.hoursAgo', { n: hours }) : t('plazaBoard.daysAgo', { n: Math.floor(hours / 24) })
})
</script>
