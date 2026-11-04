<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">{{ t('iqShowcase.title') }}</h1>
          <p class="mt-1 max-w-3xl text-sm text-gray-500 dark:text-gray-400">{{ t('iqShowcase.description') }}</p>
        </div>
        <button type="button" class="btn btn-secondary" data-test="iq-showcase-refresh" :disabled="loading" @click="refresh">
          <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
        </button>
      </div>

      <div v-if="firstLoad" class="flex items-center justify-center py-16">
        <div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600"></div>
      </div>

      <EmptyState
        v-else-if="disabled"
        data-test="iq-showcase-disabled"
        :title="t('iqShowcase.disabledTitle')"
        :description="t('iqShowcase.disabledDescription')"
      />

      <EmptyState
        v-else-if="groups.length === 0"
        data-test="iq-showcase-empty"
        :title="t('iqShowcase.emptyTitle')"
        :description="t('iqShowcase.emptyDescription')"
      />

      <template v-else>
        <section v-for="group in groups" :key="group.group_id" class="space-y-4" :data-test="`iq-showcase-group-${group.group_id}`">
          <h2 class="flex items-center gap-2 text-lg font-semibold text-gray-900 dark:text-white">
            <span class="h-4 w-1 rounded bg-primary-500"></span>
            {{ group.group_name }}
          </h2>

          <div v-for="item in group.items" :key="item.id" class="card p-4">
            <div class="mb-3 flex items-baseline justify-between gap-3">
              <h3 class="font-medium text-gray-800 dark:text-gray-100">{{ item.title }}</h3>
              <span class="text-xs text-gray-400">{{ t('iqShowcase.cardCount', { n: item.cards.length }) }}</span>
            </div>
            <p v-if="item.cards.length === 0" class="py-6 text-center text-sm text-gray-400">{{ t('iqShowcase.noCards') }}</p>
            <TransitionGroup v-else tag="div" name="iq-card" class="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-6">
              <button
                v-for="card in item.cards"
                :key="card.id"
                type="button"
                :data-test="`iq-showcase-card-${card.id}`"
                class="group overflow-hidden rounded-xl border border-gray-200 bg-white text-left transition hover:border-primary-400 hover:shadow-md dark:border-dark-600"
                @click="openCard(item, card)"
              >
                <div class="flex aspect-square items-center justify-center bg-white p-2">
                  <img
                    v-if="svgs[card.id]"
                    :src="svgs[card.id]"
                    :alt="item.title"
                    class="max-h-full max-w-full object-contain"
                    loading="lazy"
                    decoding="async"
                  />
                  <div v-else class="h-full w-full animate-pulse rounded-lg bg-gray-100"></div>
                </div>
                <div class="border-t border-gray-100 bg-gray-50 px-2 py-1.5 dark:border-dark-700 dark:bg-dark-800">
                  <div class="truncate text-xs font-medium text-gray-700 dark:text-gray-200" :title="card.model">{{ card.model }}</div>
                  <div class="text-[11px] text-gray-400" :title="formatDateTime(card.created_at)">{{ formatDateTime(card.created_at) }}</div>
                </div>
              </button>
            </TransitionGroup>
          </div>
        </section>
      </template>
    </div>

    <BaseDialog :show="preview.show" :title="preview.title" width="wide" @close="preview.show = false">
      <div v-if="preview.card" class="space-y-3">
        <div class="flex items-center justify-center rounded-xl bg-white p-4">
          <img v-if="svgs[preview.card.id]" :src="svgs[preview.card.id]" :alt="preview.title" class="max-h-[70vh] max-w-full object-contain" />
        </div>
        <p class="text-center text-sm text-gray-500 dark:text-gray-400">
          {{ preview.card.model }} · {{ formatDateTime(preview.card.created_at) }}
        </p>
      </div>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import Icon from '@/components/icons/Icon.vue'
import { IQ_SHOWCASE_SVG_BATCH, iqShowcaseAPI, iqShowcaseSVGDataURL } from '@/api/iqShowcase'
import type { IQShowcaseCardMeta, IQShowcaseUserGroup, IQShowcaseUserItem } from '@/api/iqShowcase'
import { extractApiErrorCode, extractI18nErrorMessage } from '@/utils/apiError'
import { formatDateTime } from '@/utils/format'
import { useAppStore } from '@/stores/app'

const POLL_MS = 60_000

const { t } = useI18n()
const appStore = useAppStore()

const groups = ref<IQShowcaseUserGroup[]>([])
/** Card id -> data URL. Cards never change, so this only grows with new ids. */
const svgs = reactive<Record<number, string>>({})
const loading = ref(false)
const firstLoad = ref(true)
const disabled = ref(false)
const preview = reactive<{ show: boolean; title: string; card: IQShowcaseCardMeta | null }>({ show: false, title: '', card: null })
let timer: ReturnType<typeof setInterval> | null = null
let seq = 0

async function loadSVGs(ids: number[]) {
  for (let i = 0; i < ids.length; i += IQ_SHOWCASE_SVG_BATCH) {
    const batch = ids.slice(i, i + IQ_SHOWCASE_SVG_BATCH)
    const { data } = await iqShowcaseAPI.cards(batch)
    for (const card of data.cards ?? []) {
      svgs[card.id] = iqShowcaseSVGDataURL(card.svg)
    }
  }
}

async function refresh() {
  const mine = ++seq
  loading.value = true
  try {
    const { data } = await iqShowcaseAPI.view()
    if (mine !== seq) return
    disabled.value = false
    const next = data.groups ?? []
    const live = new Set<number>()
    for (const g of next) for (const it of g.items) for (const c of it.cards) live.add(c.id)
    // Forget cards that were pruned, disabled or deleted.
    for (const id of Object.keys(svgs).map(Number)) if (!live.has(id)) delete svgs[id]
    groups.value = next
    const missing = [...live].filter((id) => !(id in svgs))
    if (missing.length) await loadSVGs(missing)
  } catch (err) {
    if (mine !== seq) return
    if (extractApiErrorCode(err) === 'IQ_SHOWCASE_DISABLED') {
      disabled.value = true
      groups.value = []
      for (const id of Object.keys(svgs)) delete svgs[Number(id)]
    } else if (firstLoad.value || !document.hidden) {
      appStore.showError(extractI18nErrorMessage(err, t, 'iqShowcase.errors', t('common.error')))
    }
  } finally {
    if (mine === seq) {
      loading.value = false
      firstLoad.value = false
    }
  }
}

function openCard(item: IQShowcaseUserItem, card: IQShowcaseCardMeta) {
  preview.title = item.title
  preview.card = card
  preview.show = true
}

function tick() {
  if (!document.hidden) void refresh()
}

function onVisibility() {
  if (!document.hidden) void refresh()
}

onMounted(() => {
  void refresh()
  timer = setInterval(tick, POLL_MS)
  document.addEventListener('visibilitychange', onVisibility)
})

onBeforeUnmount(() => {
  if (timer) clearInterval(timer)
  timer = null
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<style scoped>
.iq-card-enter-active {
  transition:
    opacity 0.6s ease,
    transform 0.6s ease;
}
.iq-card-enter-from {
  opacity: 0;
  transform: translateY(-8px) scale(0.96);
}
.iq-card-move {
  transition: transform 0.5s ease;
}
.iq-card-leave-active {
  display: none;
}
</style>
