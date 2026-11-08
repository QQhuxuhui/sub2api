<template>
  <div class="flex h-12 items-end gap-0.5" role="img" :aria-label="label">
    <div
      v-for="bar in bars"
      :key="bar.key"
      class="group relative flex h-full min-w-[0.75rem] flex-1 flex-col items-center justify-end"
      :title="bar.title"
    >
      <div
        class="w-full rounded-t bg-primary-500/80 group-hover:bg-primary-600 dark:bg-primary-400/80 dark:group-hover:bg-primary-300"
        :style="{ height: `${height(bar.value)}%` }"
      ></div>
      <div v-if="bar.value === 0" class="h-px w-full bg-gray-200 dark:bg-dark-600"></div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

export interface MiniBar { key: string; value: number; title: string }

const props = defineProps<{ bars: MiniBar[]; label: string }>()
const max = computed(() => Math.max(1, ...props.bars.map((b) => b.value)))
function height(value: number) {
  return value > 0 ? Math.max(4, (value / max.value) * 100) : 0
}
</script>
