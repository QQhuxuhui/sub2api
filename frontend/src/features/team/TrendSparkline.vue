<template>
  <!-- daily 为空 → 什么都不渲染。留空是刻意的：一条贴底的平线会让「后端没给这份数据」
       看起来像「这几天一直是 0」，而这两件事在本页的含义相反。 -->
  <svg
    v-if="line"
    :viewBox="`0 0 ${line.width} ${line.height}`"
    :width="line.width"
    :height="line.height"
    class="text-primary-500 dark:text-primary-400"
    role="img"
    :aria-label="label"
    focusable="false"
  >
    <title v-if="label">{{ label }}</title>
    <polyline
      :points="line.points"
      fill="none"
      stroke="currentColor"
      stroke-width="1.5"
      stroke-linecap="round"
      stroke-linejoin="round"
      vector-effect="non-scaling-stroke"
    />
  </svg>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { buildSparkline } from './sparkline'

const props = defineProps<{
  daily?: number[] | null
  width?: number
  height?: number
  /** 无障碍标签，由调用方带上行名做完 i18n 再传进来，组件本身不碰 i18n */
  label?: string
}>()

const line = computed(() =>
  buildSparkline(props.daily, { width: props.width ?? 96, height: props.height ?? 24 }),
)
</script>
