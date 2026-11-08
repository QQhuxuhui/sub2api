<template>
  <span class="inline-flex flex-shrink-0 items-center" :title="label">
    <span :class="['inline-block h-2 w-2 rounded-full', toneClass]" aria-hidden="true"></span>
    <span class="sr-only">{{ label }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { Tone } from './ruleUtils'

const props = defineProps<{ tone: Tone; label: string }>()

const toneClass = computed(() => {
  switch (props.tone) {
    case 'green': return 'bg-green-500'
    case 'amber': return 'bg-amber-500'
    case 'red': return 'bg-red-500'
    default: return 'bg-gray-300 dark:bg-dark-500'
  }
})
</script>
