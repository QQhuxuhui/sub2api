<template>
  <AppLayout>
    <div class="space-y-5">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">{{ t('iqShowcase.title') }}</h1>
          <p class="mt-0.5 text-sm text-gray-500 dark:text-gray-400">{{ t('iqShowcase.subtitle') }}</p>
        </div>
        <button
          type="button"
          class="btn btn-secondary h-10 w-10 !p-0"
          data-test="iq-showcase-refresh"
          :aria-label="t('common.refresh')"
          :disabled="loading"
          @click="refresh"
        >
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
        <div role="tablist" :aria-label="t('iqShowcase.groupsLabel')" class="flex flex-wrap gap-2">
          <button
            v-for="group in groups"
            :key="group.group_id"
            type="button"
            role="tab"
            :data-test="`iq-showcase-tab-${group.group_id}`"
            :aria-selected="group.group_id === activeGroupId"
            :class="[
              'inline-flex min-h-9 items-center gap-1.5 rounded-full border px-3.5 text-sm transition-colors',
              group.group_id === activeGroupId
                ? 'border-gray-900 bg-gray-900 font-semibold text-white dark:border-gray-100 dark:bg-gray-100 dark:text-gray-900'
                : 'border-gray-200 bg-white text-gray-600 hover:border-gray-300 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300',
            ]"
            @click="activeGroupId = group.group_id"
          >
            {{ group.group_name }}
            <span class="text-xs tabular-nums opacity-60">{{ group.items.length }}</span>
          </button>
        </div>

        <div
          v-if="activeGroup"
          role="tabpanel"
          class="space-y-4"
          :data-test="`iq-showcase-group-${activeGroup.group_id}`"
          :aria-label="activeGroup.group_name"
        >
          <article
            v-for="item in activeGroup.items"
            :key="item.id"
            :data-test="`iq-showcase-item-${item.id}`"
            class="grid grid-cols-[minmax(0,1fr)] gap-4 rounded-2xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900 sm:p-5 min-[760px]:grid-cols-[minmax(0,260px)_minmax(0,1fr)]"
          >
            <div class="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1 min-[760px]:col-span-2">
              <h2 class="text-base font-semibold text-gray-800 dark:text-gray-100">{{ item.title }}</h2>
              <span v-if="item.cards.length" class="min-w-0 max-w-full truncate font-mono text-xs text-gray-500 dark:text-gray-400" :title="item.cards[0].model" data-test="iq-showcase-item-model">{{ item.cards[0].model }}</span>
            </div>

            <p v-if="item.cards.length === 0" class="flex items-center gap-2 py-3 text-sm text-gray-500 dark:text-gray-400 min-[760px]:col-span-2">
              <span class="h-2 w-2 rounded-full bg-gray-300 dark:bg-dark-500" aria-hidden="true"></span>
              {{ t('iqShowcase.waiting') }}
            </p>

            <template v-else>
              <div class="flex min-w-0 flex-col gap-2">
                <button
                  type="button"
                  :data-test="`iq-showcase-card-${item.cards[0].id}`"
                  data-role="latest"
                  :aria-label="t('iqShowcase.zoom', { title: item.title, time: fullTime(item.cards[0].created_at) })"
                  :class="['flex aspect-square max-w-full cursor-zoom-in items-center justify-center overflow-hidden rounded-xl border border-gray-200 bg-white transition-colors hover:border-primary-400 dark:border-dark-600', enterClass(item.cards[0].id)]"
                  @click="openCard(item, item.cards[0])"
                  @animationend="settle(item.cards[0].id)"
                >
                  <img v-if="svgs[item.cards[0].id]" :src="svgs[item.cards[0].id]" alt="" class="h-[92%] w-[92%] object-contain" decoding="async" />
                  <span v-else class="h-[92%] w-[92%] animate-pulse rounded-lg bg-gray-100"></span>
                </button>
                <div class="flex items-center justify-between gap-2 text-xs text-gray-500 dark:text-gray-400">
                  <span class="rounded-full bg-primary-50 px-2 py-0.5 font-semibold text-primary-700 dark:bg-primary-900/40 dark:text-primary-300">{{ t('iqShowcase.latest') }}</span>
                  <span class="tabular-nums">{{ fullTime(item.cards[0].created_at) }}</span>
                </div>
              </div>

              <div v-if="item.cards.length > 1" class="grid min-w-0 content-start gap-2.5 [grid-template-columns:repeat(auto-fill,minmax(92px,1fr))]" data-role="history">
                <button
                  v-for="card in item.cards.slice(1)"
                  :key="card.id"
                  type="button"
                  :data-test="`iq-showcase-card-${card.id}`"
                  :aria-label="t('iqShowcase.zoom', { title: item.title, time: fullTime(card.created_at) })"
                  :class="['flex cursor-zoom-in flex-col overflow-hidden rounded-lg border border-gray-200 bg-white text-left transition-colors hover:border-primary-400 dark:border-dark-600 dark:bg-dark-800', enterClass(card.id)]"
                  @click="openCard(item, card)"
                  @animationend="settle(card.id)"
                >
                  <span class="flex aspect-square max-w-full items-center justify-center bg-white">
                    <img v-if="svgs[card.id]" :src="svgs[card.id]" alt="" class="h-[88%] w-[88%] object-contain" loading="lazy" decoding="async" />
                    <span v-else class="h-[88%] w-[88%] animate-pulse rounded bg-gray-100"></span>
                  </span>
                  <span class="border-t border-gray-100 px-2 py-1 text-[11px] tabular-nums text-gray-500 dark:border-dark-700 dark:text-gray-400">{{ shortTime(card.created_at) }}</span>
                </button>
              </div>
            </template>
          </article>
        </div>
      </template>
    </div>

    <BaseDialog :show="preview.show" :title="preview.title" width="wide" :close-on-click-outside="true" @close="preview.show = false">
      <div v-if="preview.card" class="space-y-3">
        <div class="flex items-center justify-center rounded-xl bg-white p-4">
          <img v-if="svgs[preview.card.id]" :src="svgs[preview.card.id]" :alt="preview.title" class="aspect-square max-h-[64vh] w-full max-w-md object-contain" />
        </div>
        <p class="text-center text-sm tabular-nums text-gray-500 dark:text-gray-400">
          {{ preview.card.model }} · {{ fullTime(preview.card.created_at) }}
        </p>
      </div>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import Icon from '@/components/icons/Icon.vue'
import { IQ_SHOWCASE_SVG_BATCH, iqShowcaseAPI, iqShowcaseSVGDataURL } from '@/api/iqShowcase'
import type { IQShowcaseCardMeta, IQShowcaseUserGroup, IQShowcaseUserItem } from '@/api/iqShowcase'
import { extractApiErrorCode, extractI18nErrorMessage } from '@/utils/apiError'
import { formatDateTime, formatDateTimeToMinute } from '@/utils/format'
import { useAppStore } from '@/stores/app'

const POLL_MS = 60_000

const { t } = useI18n()
const appStore = useAppStore()

const groups = ref<IQShowcaseUserGroup[]>([])
const activeGroupId = ref<number | null>(null)
/** Card id -> data URL. Cards never change, so this only grows with new ids. */
const svgs = reactive<Record<number, string>>({})
/** Ids already shown once; only ids that arrive later animate in. */
const seen = new Set<number>()
const fresh = reactive(new Set<number>())
const loading = ref(false)
const firstLoad = ref(true)
const disabled = ref(false)
const preview = reactive<{ show: boolean; title: string; card: IQShowcaseCardMeta | null }>({ show: false, title: '', card: null })
let timer: ReturnType<typeof setInterval> | null = null
let seq = 0
let loadedOnce = false

const activeGroup = computed(() => groups.value.find((g) => g.group_id === activeGroupId.value) ?? null)

function fullTime(at: string) {
  return formatDateTimeToMinute(at)
}

function shortTime(at: string) {
  return formatDateTime(at, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })
}

function enterClass(id: number) {
  return fresh.has(id) ? 'iq-card-enter' : ''
}

function settle(id: number) {
  fresh.delete(id)
}

function clearSVGs() {
  for (const id of Object.keys(svgs)) delete svgs[Number(id)]
}

async function loadSVGs(ids: number[]) {
  for (let i = 0; i < ids.length; i += IQ_SHOWCASE_SVG_BATCH) {
    const batch = ids.slice(i, i + IQ_SHOWCASE_SVG_BATCH)
    try {
      const { data } = await iqShowcaseAPI.cards(batch)
      for (const card of data.cards ?? []) {
        svgs[card.id] = iqShowcaseSVGDataURL(card.svg)
      }
    } catch {
      // Cards of this batch stay placeholders; the next poll asks for them again.
    }
  }
}

// Commits a successful listing at once: groups, the selected tab, and which
// cards are new. SVGs are fetched afterwards and never roll the listing back.
function applyListing(next: IQShowcaseUserGroup[]) {
  const live = new Set<number>()
  for (const g of next) for (const it of g.items) for (const c of it.cards) live.add(c.id)
  for (const id of Object.keys(svgs).map(Number)) if (!live.has(id)) delete svgs[id]
  fresh.clear()
  for (const id of live) {
    if (!seen.has(id)) {
      if (loadedOnce) fresh.add(id)
      seen.add(id)
    }
  }
  loadedOnce = true
  groups.value = next
  if (!next.some((g) => g.group_id === activeGroupId.value)) {
    activeGroupId.value = next.length ? next[0].group_id : null
  }
  return [...live].filter((id) => !(id in svgs))
}

async function refresh() {
  const mine = ++seq
  loading.value = true
  try {
    const { data } = await iqShowcaseAPI.view()
    if (mine !== seq) return
    disabled.value = false
    const missing = applyListing(data.groups ?? [])
    if (missing.length) await loadSVGs(missing)
  } catch (err) {
    if (mine !== seq) return
    if (extractApiErrorCode(err) === 'IQ_SHOWCASE_DISABLED') {
      disabled.value = true
      groups.value = []
      activeGroupId.value = null
      clearSVGs()
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
.iq-card-enter {
  animation: iq-card-in 0.55s cubic-bezier(0.2, 0.8, 0.2, 1) both;
}
@keyframes iq-card-in {
  from {
    opacity: 0;
    transform: scale(0.94);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
@media (prefers-reduced-motion: reduce) {
  .iq-card-enter {
    animation: none;
  }
}
</style>
