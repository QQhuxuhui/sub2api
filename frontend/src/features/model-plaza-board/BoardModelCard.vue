<template>
  <article
    class="flex min-w-0 flex-col gap-3 rounded-xl border border-gray-200 bg-white px-4 pb-2.5 pt-4 transition-colors hover:border-gray-400 dark:border-dark-700 dark:bg-dark-800 dark:hover:border-dark-500"
  >
    <header class="flex min-w-0 items-start gap-3">
      <span
        class="flex h-10 w-10 flex-none items-center justify-center rounded-lg bg-gray-100 dark:bg-dark-700"
        aria-hidden="true"
      >
        <ModelIcon :model="model.name" size="22px" />
      </span>
      <div class="min-w-0 flex-1">
        <h3 class="break-words font-mono text-[15px] font-semibold leading-snug text-gray-900 [overflow-wrap:anywhere] dark:text-white">
          {{ model.name }}
        </h3>
        <p class="mt-0.5 truncate text-xs text-gray-500 dark:text-dark-400">{{ vendorLabel }}</p>
      </div>
      <button
        type="button"
        class="flex h-7 w-7 flex-none items-center justify-center rounded-md text-gray-400 hover:bg-gray-100 hover:text-gray-700 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 dark:hover:bg-dark-700 dark:hover:text-gray-200"
        :aria-label="t('plazaBoard.copy')"
        :title="t('plazaBoard.copy')"
        @click="copyToClipboard(model.name)"
      >
        <Icon name="copy" size="sm" class="h-3.5 w-3.5" />
      </button>
    </header>

    <div class="flex min-w-0 flex-col gap-1.5">
      <p v-if="line.price.billing !== 'none'" class="flex items-center gap-1.5 text-xs text-gray-500 dark:text-dark-400">
        <span class="h-1.5 w-1.5 flex-none rounded-full bg-primary-500" aria-hidden="true"></span>
        {{ billingLabel }}<template v-if="model.lines.length > 1"> · {{ t('plazaBoard.recommended') }}</template>
      </p>
      <BoardPriceCells :price="line.price" />
    </div>

    <dl class="m-0 flex min-w-0 items-baseline gap-1.5 text-xs">
      <dt class="flex-none text-gray-500 dark:text-dark-400">{{ t('plazaBoard.group') }}</dt>
      <dd class="m-0 min-w-0 truncate text-gray-800 dark:text-gray-200" :title="allGroupNames">{{ line.group.name }}</dd>
      <span v-if="model.lines.length > 1" class="flex-none text-gray-500 dark:text-dark-400">+{{ model.lines.length - 1 }}</span>
    </dl>

    <BoardLineHealth v-if="showHealth" :health="line.health" />

    <footer class="mt-auto flex items-center gap-3 border-t border-gray-100 pt-2 text-xs text-gray-500 dark:border-dark-700 dark:text-dark-400">
      <template v-if="showHealth">
        <BoardStateTag :state="line.state" />
        <span v-if="line.health" class="whitespace-nowrap">
          {{ t('plazaBoard.ttft') }}
          <b class="font-semibold tabular-nums text-gray-900 dark:text-white">{{ formatTtft(line.health?.ttft_ms) }}</b>
        </span>
      </template>
      <span class="flex-1"></span>
      <button
        type="button"
        class="inline-flex flex-none items-center gap-1 rounded-md px-2 py-1 text-[13px] text-gray-600 hover:bg-gray-100 hover:text-gray-900 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 dark:text-dark-300 dark:hover:bg-dark-700 dark:hover:text-white"
        @click="emit('open', model.name)"
      >
        {{ t('plazaBoard.details') }}
        <Icon name="chevronRight" size="xs" class="h-3 w-3" />
      </button>
    </footer>
  </article>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import ModelIcon from '@/components/common/ModelIcon.vue'
import { useClipboard } from '@/composables/useClipboard'
import { BILLING_MODE_IMAGE, BILLING_MODE_VIDEO } from '@/constants/channel'
import BoardLineHealth from './BoardLineHealth.vue'
import BoardPriceCells from './BoardPriceCells.vue'
import BoardStateTag from './BoardStateTag.vue'
import { OTHER_VENDOR_KEY, formatTtft, type BoardModel } from './plazaBoard'

const props = defineProps<{
  model: BoardModel
  /** 是否展示健康相关内容（未登录、监控未启用时为 false）。 */
  showHealth: boolean
}>()
const emit = defineEmits<{ (e: 'open', name: string): void }>()

const { t } = useI18n()
const { copyToClipboard } = useClipboard()

const line = computed(() => props.model.recommended)
const vendorLabel = computed(() =>
  props.model.vendor.key === OTHER_VENDOR_KEY ? t('plazaBoard.filters.otherVendor') : props.model.vendor.label
)
const allGroupNames = computed(() => props.model.lines.map((l) => l.group.name).join('、'))
const billingLabel = computed(() => {
  const price = line.value.price
  if (price.billing === 'token') return t('plazaBoard.billing.token')
  if (price.billingMode === BILLING_MODE_IMAGE) return t('plazaBoard.billing.image')
  if (price.billingMode === BILLING_MODE_VIDEO) return t('plazaBoard.billing.video')
  return price.billing === 'request' ? t('plazaBoard.billing.request') : t('plazaBoard.price.unset')
})
</script>
