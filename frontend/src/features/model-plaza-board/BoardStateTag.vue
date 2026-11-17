<template>
  <span :class="['inline-flex items-center gap-1.5 whitespace-nowrap text-xs font-semibold', textClass]">
    <span :class="['h-2 w-2 flex-none', shapeClass]" aria-hidden="true"></span>
    {{ t(`plazaBoard.state.${state}`) }}
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PlazaHealthState } from './types'

const props = defineProps<{ state: PlazaHealthState }>()
const { t } = useI18n()

// 状态不只靠颜色区分：正常为圆点、波动为菱形、异常为方块。
const shapeClass = computed(
  () =>
    ({
      healthy: 'rounded-full bg-emerald-500',
      warning: 'rotate-45 rounded-[1px] bg-amber-500',
      critical: 'rounded-[1px] bg-red-500',
      unknown: 'rounded-full bg-gray-300 dark:bg-dark-600'
    })[props.state]
)
const textClass = computed(
  () =>
    ({
      healthy: 'text-emerald-600 dark:text-emerald-400',
      warning: 'text-amber-600 dark:text-amber-400',
      critical: 'text-red-600 dark:text-red-400',
      unknown: 'text-gray-500 dark:text-dark-400'
    })[props.state]
)
</script>
