<template>
  <span :class="['inline-flex rounded-full px-2 py-0.5 text-xs font-medium', badgeClass]" :title="message || undefined">
    {{ text }}
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { IQProbeRunStatus } from '@/api/admin/iqProbe'

const props = defineProps<{
  status: IQProbeRunStatus | ''
  score: number | null
  message?: string
  /** Scores below this are shown as low. */
  threshold?: number
}>()

const { t } = useI18n()

const text = computed(() => {
  if (props.status === 'scored' && props.score !== null) return `${Math.round(props.score * 100)}%`
  if (props.status === '') return t('iqProbe.status.never')
  return t(`iqProbe.status.${props.status}`)
})

const badgeClass = computed(() => {
  if (props.status === 'scored' && props.score !== null) {
    const low = props.threshold !== undefined ? props.score < props.threshold : props.score < 0.6
    return low
      ? 'bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300'
      : 'bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-300'
  }
  if (props.status === 'invalid') return 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300'
  if (props.status === 'running') return 'bg-blue-100 text-blue-700 dark:bg-blue-900/40 dark:text-blue-300'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-400'
})
</script>
