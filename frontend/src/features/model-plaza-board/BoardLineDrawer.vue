<template>
  <Teleport to="body">
    <div v-if="model" class="fixed inset-0 z-50">
      <div class="absolute inset-0 bg-gray-900/45" data-testid="plaza-drawer-scrim" @click="emit('close')"></div>
      <aside
        ref="panel"
        class="absolute inset-y-0 right-0 flex w-full max-w-[680px] flex-col bg-gray-50 shadow-2xl dark:bg-dark-900"
        role="dialog"
        aria-modal="true"
        :aria-label="model.name"
        @keydown.esc.stop="emit('close')"
      >
        <header class="flex items-center gap-3 border-b border-gray-200 bg-white px-5 py-4 dark:border-dark-700 dark:bg-dark-800">
          <span class="flex h-10 w-10 flex-none items-center justify-center rounded-lg bg-gray-100 dark:bg-dark-700" aria-hidden="true">
            <ModelIcon :model="model.name" size="22px" />
          </span>
          <div class="min-w-0 flex-1">
            <h3 class="m-0 font-mono text-[17px] font-semibold text-gray-900 [overflow-wrap:anywhere] dark:text-white">{{ model.name }}</h3>
            <p class="m-0 text-xs text-gray-500 dark:text-dark-400">
              {{ vendorLabel }} · {{ t('plazaBoard.drawer.lines', { count: model.lines.length }) }}
            </p>
          </div>
          <button type="button" class="plaza-drawer-btn" @click="copyToClipboard(model.name)">
            <Icon name="copy" size="sm" class="h-3.5 w-3.5" />
            <span class="hidden sm:inline">{{ t('plazaBoard.copy') }}</span>
          </button>
          <button ref="closeButton" type="button" class="plaza-drawer-btn" :aria-label="t('plazaBoard.drawer.close')" @click="emit('close')">
            <Icon name="x" size="sm" class="h-3.5 w-3.5" />
          </button>
        </header>

        <div class="flex flex-col gap-3 overflow-y-auto px-5 pb-7 pt-4">
          <section
            v-for="line in model.lines"
            :key="line.key"
            :class="[
              'rounded-xl border bg-white p-3.5 dark:bg-dark-800',
              line.key === model.recommended.key && model.lines.length > 1
                ? 'border-primary-500'
                : 'border-gray-200 dark:border-dark-700'
            ]"
          >
            <div class="flex items-start gap-4">
              <div class="min-w-0 flex-1">
                <div class="font-semibold text-gray-900 [overflow-wrap:anywhere] dark:text-white">
                  {{ line.group.name }}
                  <span v-if="mixedPlatforms" class="ml-1 text-xs font-normal text-gray-500 dark:text-dark-400">{{ line.model.platform }}</span>
                </div>
                <div class="mt-1 flex flex-wrap gap-1.5">
                  <span v-if="badges.get(line.key)?.cheapest" class="plaza-tag plaza-tag-hi">{{ t('plazaBoard.drawer.cheapest') }}</span>
                  <span v-if="showHealth && badges.get(line.key)?.steadiest" class="plaza-tag plaza-tag-hi">{{ t('plazaBoard.drawer.steadiest') }}</span>
                  <span v-if="showHealth && badges.get(line.key)?.fastest" class="plaza-tag plaza-tag-hi">{{ t('plazaBoard.drawer.fastest') }}</span>
                  <span class="plaza-tag tabular-nums">{{ t('plazaBoard.drawer.rate', { rate: formatRate(line.rate) }) }}</span>
                </div>
              </div>
              <div class="flex-none text-right">
                <div class="whitespace-nowrap font-mono text-sm font-semibold tabular-nums text-gray-900 dark:text-white">{{ priceText(line) }}</div>
                <div v-if="officialText(line)" class="whitespace-nowrap text-[11.5px] tabular-nums text-gray-500 dark:text-dark-400">{{ officialText(line) }}</div>
              </div>
            </div>

            <div v-if="showHealth" class="mt-3 flex flex-wrap items-center gap-x-3 gap-y-2 text-xs text-gray-500 dark:text-dark-400">
              <BoardStateTag :state="line.state" />
              <BoardLineHealth class="min-w-[160px] flex-1" size="sm" :health="line.health" />
              <span v-if="line.health" class="whitespace-nowrap">
                {{ t('plazaBoard.ttft') }}
                <b class="font-semibold tabular-nums text-gray-900 dark:text-white">{{ formatTtft(line.health?.ttft_ms) }}</b>
              </span>
            </div>

            <details class="mt-3 border-t border-gray-100 pt-2 dark:border-dark-700">
              <summary class="cursor-pointer select-none text-xs text-gray-600 hover:text-gray-900 dark:text-dark-300 dark:hover:text-white">
                {{ t('plazaBoard.drawer.fullPricing') }}
              </summary>
              <div class="mt-2 overflow-x-auto">
                <PlazaGroupSection :group="{ ...line.group, models: [line.model] }" />
              </div>
            </details>
          </section>
        </div>
      </aside>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import ModelIcon from '@/components/common/ModelIcon.vue'
import PlazaGroupSection from '@/components/modelPlaza/PlazaGroupSection.vue'
import { useClipboard } from '@/composables/useClipboard'
import { BILLING_MODE_IMAGE } from '@/constants/channel'
import BoardLineHealth from './BoardLineHealth.vue'
import BoardStateTag from './BoardStateTag.vue'
import {
  OTHER_VENDOR_KEY,
  formatRate,
  formatTtft,
  lineBadges,
  officialPriceText,
  type BoardLine,
  type BoardModel
} from './plazaBoard'

const props = defineProps<{
  /** 为 null 时抽屉关闭。 */
  model: BoardModel | null
  showHealth: boolean
}>()
const emit = defineEmits<{ (e: 'close'): void }>()

const { t } = useI18n()
const { copyToClipboard } = useClipboard()
const closeButton = ref<HTMLButtonElement | null>(null)
const panel = ref<HTMLElement | null>(null)
let returnFocusTo: HTMLElement | null = null

const badges = computed(() => lineBadges(props.model?.lines ?? []))
const mixedPlatforms = computed(() => new Set((props.model?.lines ?? []).map((l) => l.model.platform)).size > 1)
const vendorLabel = computed(() =>
  !props.model || props.model.vendor.key === OTHER_VENDOR_KEY
    ? t('plazaBoard.filters.otherVendor')
    : props.model.vendor.label
)

// 打开时把焦点移到关闭按钮，关闭后还原到触发元素。
watch(
  () => props.model?.name ?? null,
  async (name, previous) => {
    if (name && !previous) {
      returnFocusTo = document.activeElement instanceof HTMLElement ? document.activeElement : null
      await nextTick()
      closeButton.value?.focus()
    } else if (!name && previous) {
      returnFocusTo?.focus?.()
      returnFocusTo = null
    }
  }
)

function priceText(line: BoardLine): string {
  const price = line.price
  if (price.billing === 'token') return `${price.input ?? '—'} / ${price.output ?? '—'}`
  if (price.billing === 'request') {
    const unit = price.billingMode === BILLING_MODE_IMAGE ? t('plazaBoard.price.perImage') : t('plazaBoard.price.perRequest')
    return `${price.request} ${unit}${price.from ? ` ${t('plazaBoard.price.from')}` : ''}`
  }
  return t('plazaBoard.price.unset')
}

function officialText(line: BoardLine): string {
  if (line.price.billing !== 'token') return ''
  const official = officialPriceText(line.model)
  return official ? t('plazaBoard.price.official', official) : ''
}
</script>

<style scoped>
.plaza-drawer-btn {
  @apply inline-flex h-8 flex-none items-center gap-1.5 rounded-lg border border-gray-200 bg-white px-2.5 text-xs text-gray-600 hover:border-primary-500 hover:text-gray-900 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 dark:border-dark-600 dark:bg-dark-800 dark:text-dark-300 dark:hover:text-white;
}
.plaza-tag {
  @apply rounded-md bg-gray-100 px-1.5 py-px text-[11.5px] text-gray-600 dark:bg-dark-700 dark:text-dark-300;
}
.plaza-tag-hi {
  @apply bg-primary-50 font-semibold text-primary-700 dark:bg-primary-900/30 dark:text-primary-300;
}
</style>
