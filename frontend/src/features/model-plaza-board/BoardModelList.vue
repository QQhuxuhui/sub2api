<template>
  <div class="overflow-x-auto rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
    <table class="w-full min-w-[720px] border-collapse text-sm">
      <thead>
        <tr class="border-b border-gray-200 text-left text-xs font-semibold text-gray-500 dark:border-dark-700 dark:text-dark-400">
          <th class="whitespace-nowrap px-4 py-2.5">{{ t('plazaBoard.list.model') }}</th>
          <th v-if="showHealth" class="whitespace-nowrap px-4 py-2.5">{{ t('plazaBoard.list.status') }}</th>
          <th class="whitespace-nowrap px-4 py-2.5 text-right">{{ t('plazaBoard.price.input') }}</th>
          <th class="whitespace-nowrap px-4 py-2.5 text-right">{{ t('plazaBoard.price.output') }}</th>
          <template v-if="showHealth">
            <th class="whitespace-nowrap px-4 py-2.5 text-right">{{ t('plazaBoard.availability') }}</th>
            <th class="whitespace-nowrap px-4 py-2.5 text-right">{{ t('plazaBoard.ttft') }}</th>
            <th class="whitespace-nowrap px-4 py-2.5">{{ t('plazaBoard.list.strip') }}</th>
          </template>
        </tr>
      </thead>
      <tbody>
        <tr
          v-for="model in models"
          :key="model.name"
          class="cursor-pointer border-b border-gray-100 last:border-b-0 hover:bg-gray-50 focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-primary-500 dark:border-dark-700 dark:hover:bg-dark-700/50"
          tabindex="0"
          @click="emit('open', model.name)"
          @keydown.enter="emit('open', model.name)"
        >
          <td class="px-4 py-2.5">
            <div class="flex min-w-0 items-center gap-2.5">
              <ModelIcon :model="model.name" size="18px" />
              <span class="font-mono text-[13.5px] font-semibold text-gray-900 [overflow-wrap:anywhere] dark:text-white">{{ model.name }}</span>
            </div>
          </td>
          <td v-if="showHealth" class="px-4 py-2.5"><BoardStateTag :state="model.state" /></td>
          <td class="whitespace-nowrap px-4 py-2.5 text-right font-mono tabular-nums text-gray-900 dark:text-white">{{ primaryPrice(model) }}</td>
          <td class="whitespace-nowrap px-4 py-2.5 text-right font-mono tabular-nums text-gray-900 dark:text-white">{{ model.recommended.price.output ?? '—' }}</td>
          <template v-if="showHealth">
            <td class="whitespace-nowrap px-4 py-2.5 text-right tabular-nums text-gray-900 dark:text-white">{{ formatAvailability(model.recommended) }}</td>
            <td class="whitespace-nowrap px-4 py-2.5 text-right tabular-nums text-gray-900 dark:text-white">{{ formatTtft(model.recommended.health?.ttft_p50_ms) }}</td>
            <td class="px-4 py-2.5">
              <BoardHealthStrip
                v-if="model.recommended.health"
                class="min-w-[180px]"
                size="sm"
                :buckets="model.recommended.health.buckets"
                :window="window"
              />
              <span v-else class="text-gray-400 dark:text-dark-500">—</span>
            </td>
          </template>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import ModelIcon from '@/components/common/ModelIcon.vue'
import { BILLING_MODE_IMAGE } from '@/constants/channel'
import BoardHealthStrip from './BoardHealthStrip.vue'
import BoardStateTag from './BoardStateTag.vue'
import { formatAvailability, formatTtft, type BoardModel } from './plazaBoard'
import type { PlazaHealthWindow } from './types'

defineProps<{
  models: BoardModel[]
  showHealth: boolean
  window?: PlazaHealthWindow | null
}>()
const emit = defineEmits<{ (e: 'open', name: string): void }>()
const { t } = useI18n()

/** 第一列价格：按量显示输入价；按次显示单次价并带单位；无价显示待配置。 */
function primaryPrice(model: BoardModel): string {
  const price = model.recommended.price
  if (price.billing === 'token') return price.input ?? '—'
  if (price.billing === 'request') {
    const unit = price.billingMode === BILLING_MODE_IMAGE ? t('plazaBoard.price.perImage') : t('plazaBoard.price.perRequest')
    return `${price.request} ${unit}${price.from ? ` ${t('plazaBoard.price.from')}` : ''}`
  }
  return t('plazaBoard.price.unset')
}
</script>
