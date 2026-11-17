<template>
  <div
    class="plaza-strip grid gap-px"
    :class="size === 'sm' ? 'h-3' : 'h-2.5'"
    :style="{ gridTemplateColumns: `repeat(${buckets.length || 1}, minmax(0, 1fr))` }"
    role="img"
    :aria-label="t('plazaBoard.strip')"
  >
    <i
      v-for="(bucket, idx) in buckets"
      :key="idx"
      :class="['rounded-[1px]', cellClass(bucket.s)]"
      :title="cellTitle(bucket, idx)"
    ></i>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { formatErrorRate } from './plazaBoard'
import type { PlazaHealthBucket, PlazaHealthState, PlazaHealthWindow } from './types'

const props = withDefaults(
  defineProps<{
    buckets: PlazaHealthBucket[]
    window?: PlazaHealthWindow | null
    size?: 'sm' | 'md'
  }>(),
  { window: null, size: 'md' }
)
const { t } = useI18n()

function cellClass(state: PlazaHealthState): string {
  return {
    healthy: 'bg-emerald-500',
    warning: 'bg-amber-500',
    critical: 'bg-red-500',
    unknown: 'bg-gray-200 dark:bg-dark-700'
  }[state]
}

function bucketTime(idx: number): string {
  if (!props.window) return ''
  const start = new Date(props.window.start).getTime()
  if (Number.isNaN(start)) return ''
  const at = new Date(start + idx * props.window.bucket_seconds * 1000)
  return `${String(at.getHours()).padStart(2, '0')}:${String(at.getMinutes()).padStart(2, '0')}`
}

/** 悬停提示只含时间、状态、错误率，不含任何请求数。 */
function cellTitle(bucket: PlazaHealthBucket, idx: number): string {
  const time = bucketTime(idx)
  const state = t(`plazaBoard.state.${bucket.s}`)
  return bucket.e == null
    ? t('plazaBoard.bucketTitleUnknown', { time, state })
    : t('plazaBoard.bucketTitle', { time, state, rate: formatErrorRate(bucket.e) })
}
</script>
