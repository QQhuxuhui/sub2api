<template>
  <span ref="root" class="relative inline-flex align-middle" @mouseenter="open = true" @mouseleave="open = false">
    <button
      type="button"
      class="inline-flex h-4 w-4 items-center justify-center rounded-full text-gray-400 hover:text-gray-600 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:text-gray-500 dark:hover:text-gray-300"
      :aria-label="label"
      :aria-expanded="open"
      @click.stop="open = true"
      @focus="open = true"
      @blur="open = false"
      @keydown.esc="open = false"
    >
      <svg viewBox="0 0 20 20" fill="currentColor" class="h-4 w-4" aria-hidden="true">
        <path fill-rule="evenodd" d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7-4a1 1 0 11-2 0 1 1 0 012 0zM9 9a1 1 0 000 2v3a1 1 0 001 1h1a1 1 0 100-2v-3a1 1 0 00-1-1H9z" clip-rule="evenodd" />
      </svg>
    </button>
    <span
      v-if="open"
      role="tooltip"
      :class="[
        'absolute top-full z-30 mt-1.5 w-64 max-w-[calc(100vw-2rem)] whitespace-normal rounded-lg bg-gray-900 px-3 py-2 text-xs font-normal leading-5 text-white shadow-lg dark:bg-dark-600',
        alignRight ? 'right-0' : 'left-0',
      ]"
    >{{ text }}</span>
  </span>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'

defineProps<{ text: string; label: string }>()

const open = ref(false)
const root = ref<HTMLElement | null>(null)
// Keep the bubble on screen: open it leftwards near the right edge.
const alignRight = ref(false)
watch(open, (now) => {
  if (!now || !root.value || typeof window === 'undefined') return
  alignRight.value = root.value.getBoundingClientRect().left > window.innerWidth - 272
})

function onDocumentClick(event: MouseEvent) {
  if (open.value && root.value && !root.value.contains(event.target as Node)) open.value = false
}
onMounted(() => document.addEventListener('click', onDocumentClick))
onBeforeUnmount(() => document.removeEventListener('click', onDocumentClick))
</script>
