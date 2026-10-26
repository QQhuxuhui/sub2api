<template>
  <svg :width="width" :height="height" :viewBox="`0 0 ${width} ${height}`" class="overflow-visible" role="img" :aria-label="label">
    <line
      v-if="threshold !== undefined"
      :x1="0"
      :x2="width"
      :y1="y(threshold)"
      :y2="y(threshold)"
      class="stroke-amber-400/70"
      stroke-dasharray="2 2"
      stroke-width="1"
    />
    <polyline v-if="linePoints" :points="linePoints" fill="none" class="stroke-primary-500" stroke-width="1.5" stroke-linejoin="round" />
    <template v-for="(p, i) in placed" :key="i">
      <circle v-if="p.score !== null" :cx="p.x" :cy="y(p.score)" r="1.8" class="fill-primary-500" />
      <line v-else :x1="p.x" :x2="p.x" :y1="height - 4" :y2="height" class="stroke-red-400" stroke-width="1.5" />
    </template>
  </svg>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { IQProbeScorePoint } from '@/api/admin/iqProbe'

const props = withDefaults(
  defineProps<{
    points: IQProbeScorePoint[]
    threshold?: number
    width?: number
    height?: number
    label?: string
  }>(),
  { width: 120, height: 28, label: '' }
)

const pad = 2
const y = (score: number) => props.height - pad - score * (props.height - pad * 2)

const placed = computed(() => {
  const n = props.points.length
  return props.points.map((p, i) => ({
    x: n <= 1 ? props.width / 2 : pad + (i * (props.width - pad * 2)) / (n - 1),
    score: p.score,
  }))
})

// Invalid rounds (no score) break the line and show as red ticks.
const linePoints = computed(() => {
  const scored = placed.value.filter((p) => p.score !== null)
  if (scored.length < 2) return ''
  return scored.map((p) => `${p.x.toFixed(1)},${y(p.score as number).toFixed(1)}`).join(' ')
})
</script>
