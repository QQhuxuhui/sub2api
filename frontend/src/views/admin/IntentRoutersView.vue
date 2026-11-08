<template>
  <AppLayout>
    <div class="space-y-5">
      <!-- The three places this page has (the layout header carries the title) -->
      <div class="border-b border-gray-200 dark:border-dark-700">
        <nav class="-mb-px flex overflow-x-auto" role="tablist">
          <button
            v-for="item in TABS"
            :key="item"
            type="button"
            role="tab"
            :data-test="`intent-tab-${item}`"
            :aria-selected="tab === item"
            :class="['ir-tab', tab === item ? 'ir-tab-active' : 'ir-tab-inactive']"
            @click="setTab(item)"
          >{{ t(`intentRouter.tabs.${item}`) }}</button>
        </nav>
      </div>

      <div v-if="loading" class="flex items-center justify-center py-16">
        <div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600"></div>
      </div>

      <template v-else>
        <!-- Routing configuration -->
        <div v-show="tab === 'config'" class="grid gap-5 lg:grid-cols-[17rem_minmax(0,1fr)]">
          <GroupList :groups="groups" :selected-id="selectedGroupId" :status-of="groupStatus" :busy="busy" @select="requestGroup" @clear-all="clearCache(0)" />

          <section v-if="selectedGroup" class="min-w-0 space-y-5">
            <div class="card px-5 pt-4">
              <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
                <div class="flex min-w-0 items-center gap-2">
                  <h2 class="truncate text-lg font-semibold text-gray-900 dark:text-white">{{ selectedGroup.name }}</h2>
                  <span class="rounded bg-gray-100 px-1.5 py-0.5 text-xs text-gray-500 dark:bg-dark-700 dark:text-gray-400">{{ selectedGroup.platform }}</span>
                </div>
                <label class="ml-auto flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                  <span>{{ form.enabled ? t('intentRouter.enabledOn') : t('intentRouter.enabledOff') }}</span>
                  <Toggle v-model="form.enabled" data-test="intent-enabled" :aria-label="t('intentRouter.enabled')" />
                </label>
              </div>
              <nav class="-mx-2 mt-2 flex overflow-x-auto" role="tablist">
                <button
                  v-for="item in SUBS"
                  :key="item"
                  type="button"
                  role="tab"
                  :data-test="`intent-sub-${item}`"
                  :aria-selected="sub === item"
                  :class="['ir-tab', sub === item ? 'ir-tab-active' : 'ir-tab-inactive']"
                  @click="setSub(item)"
                >
                  {{ t(`intentRouter.subs.${item}`) }}
                  <StatusDot v-if="item === 'jev' && form.jev.enabled" :tone="jevTone" :label="t('intentRouter.subs.jev')" />
                  <span v-if="item === 'extras' && extrasDirty" class="h-1.5 w-1.5 rounded-full bg-amber-500" :title="t('intentRouter.unsaved')"></span>
                </button>
              </nav>
            </div>

            <div v-show="sub === 'rules'" class="card space-y-6 p-5">
              <RouteFlow />
              <RuleList />
              <div v-if="savedRouter" class="border-t border-gray-100 pt-5 dark:border-dark-700">
                <TryItPanel />
              </div>
            </div>
            <div v-show="sub === 'jev'" class="card p-5">
              <JevPanel />
            </div>
            <div v-show="sub === 'extras'" class="grid gap-5 xl:grid-cols-2">
              <MonitorAnswerCard :group-id="selectedGroup.id" @dirty-change="extras.monitor = $event" />
              <StreamPreludeCard :group-id="selectedGroup.id" @dirty-change="extras.prelude = $event" />
            </div>
            <div v-show="sub === 'settings'" class="card p-5">
              <SettingsPanel :busy="busy" @clear-cache="clearCache(selectedGroup.id)" @delete="askDelete" />
            </div>

            <!-- Saving the routing configuration (the extras save on their own) -->
            <div v-show="sub !== 'extras'" data-test="intent-save-bar" class="sticky bottom-0 z-20 -mx-1 flex flex-wrap items-center gap-3 rounded-xl border border-gray-200 bg-white/95 px-4 py-3 shadow-lg backdrop-blur dark:border-dark-600 dark:bg-dark-800/95">
              <span v-if="dirty" class="flex items-center gap-1.5 text-sm text-amber-700 dark:text-amber-300"><span class="h-2 w-2 rounded-full bg-amber-500"></span>{{ t('intentRouter.saveBar.unsaved') }}</span>
              <span v-else class="text-sm text-gray-500 dark:text-gray-400">{{ savedRouter ? t('intentRouter.saveBar.saved') : t('intentRouter.saveBar.new') }}</span>
              <button v-if="extrasDirty" type="button" data-test="intent-extras-unsaved" class="rounded-full bg-amber-50 px-2 py-0.5 text-xs text-amber-700 hover:underline dark:bg-amber-900/30 dark:text-amber-300" @click="setSub('extras')">{{ t('intentRouter.saveBar.extrasUnsaved') }}</button>
              <div class="ml-auto flex items-center gap-2">
                <button type="button" data-test="intent-discard" class="btn btn-secondary btn-sm" :disabled="busy || !dirty" @click="fillForm(savedRouter)">{{ t('intentRouter.saveBar.discard') }}</button>
                <button type="button" data-test="intent-save" class="btn btn-primary btn-sm" :disabled="busy" @click="save">
                  {{ saving ? t('intentRouter.actions.saving') : t('intentRouter.actions.save') }}
                </button>
              </div>
            </div>
          </section>
          <section v-else class="card p-8 text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.noGroups') }}</section>
        </div>

        <!-- Looking back at what happened -->
        <HitsPanel v-if="visited.has('hits')" v-show="tab === 'hits'" :routed-group-ids="routedGroupIds" :group-name="groupName" :account-label="accountLabel" />

        <!-- Global template judgements -->
        <TemplateReviewCard v-if="visited.has('templates')" v-show="tab === 'templates'" :group-names="groupNameMap" />
      </template>
    </div>

    <ConfirmDialog
      :show="confirm.open"
      :title="confirm.kind === 'delete' ? t('intentRouter.actions.delete') : t('intentRouter.leave.title')"
      :message="confirm.kind === 'delete' ? t('intentRouter.actions.deleteConfirm', { group: selectedGroup?.name ?? '' }) : t('intentRouter.leave.message')"
      :confirm-text="confirm.kind === 'delete' ? t('common.delete') : t('intentRouter.leave.discard')"
      :cancel-text="confirm.kind === 'delete' ? t('common.cancel') : t('intentRouter.leave.stay')"
      danger
      @confirm="onConfirm"
      @cancel="onCancel"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Toggle from '@/components/common/Toggle.vue'
import GroupList from '@/components/admin/intentRouter/GroupList.vue'
import HitsPanel from '@/components/admin/intentRouter/HitsPanel.vue'
import JevPanel from '@/components/admin/intentRouter/JevPanel.vue'
import MonitorAnswerCard from '@/components/admin/intentRouter/MonitorAnswerCard.vue'
import RouteFlow from '@/components/admin/intentRouter/RouteFlow.vue'
import RuleList from '@/components/admin/intentRouter/RuleList.vue'
import SettingsPanel from '@/components/admin/intentRouter/SettingsPanel.vue'
import StatusDot from '@/components/admin/intentRouter/StatusDot.vue'
import StreamPreludeCard from '@/components/admin/intentRouter/StreamPreludeCard.vue'
import TemplateReviewCard from '@/components/admin/intentRouter/TemplateReviewCard.vue'
import TryItPanel from '@/components/admin/intentRouter/TryItPanel.vue'
import { provideIntentRouter } from '@/components/admin/intentRouter/context'
import { formatKeywords, keyPlatformCompatible, parseKeywords, platformCompatible, restoreOriginalControls, routerHasTargetProblem } from '@/components/admin/intentRouter/ruleUtils'
import type { AccountOption, FormState, GroupOption, RuleForm, RuleTarget, Tone } from '@/components/admin/intentRouter/ruleUtils'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import { intentRoutersAPI } from '@/api/admin/intentRouters'
import type { IntentJevConfig, IntentJevStatus, IntentRouter, IntentRouterInput, IntentRuleKeyLabel } from '@/api/admin/intentRouters'
import { extractI18nErrorMessage } from '@/utils/apiError'

const TABS = ['config', 'hits', 'templates'] as const
const SUBS = ['rules', 'jev', 'extras', 'settings'] as const
type Tab = typeof TABS[number]
type Sub = typeof SUBS[number]
const ACCOUNT_PAGE_SIZE = 1000
const ACCOUNT_MAX_PAGES = 10

const { t } = useI18n()
const appStore = useAppStore()
const route = useRoute()
const router = useRouter()

const loading = ref(true)
const saving = ref(false)
const working = ref(false)
const busy = computed(() => saving.value || working.value)

const groups = ref<GroupOption[]>([])
const accounts = ref<AccountOption[]>([])
const routers = ref<Record<number, IntentRouter>>({})
const routedGroupIds = computed(() => Object.keys(routers.value).map(Number))
const selectedGroupId = ref<number | null>(null)
const jevStatus = ref<IntentJevStatus | null>(null)
const tab = ref<Tab>('config')
const sub = ref<Sub>('rules')
const visited = reactive(new Set<Tab>(['config']))
const extras = reactive({ monitor: false, prelude: false })
const extrasDirty = computed(() => extras.monitor || extras.prelude)

let nextUid = 1
const blankForm = (): FormState => ({
  enabled: false,
  classifier_base_url: '',
  classifier_api_key: '',
  classifier_protocol: 'openai_chat',
  classifier_model: '',
  classifier_timeout_ms: 3000,
  cache_ttl_seconds: 7200,
  max_input_chars: 2000,
  keyword_max_chars: 0,
  unavailable_status: 503,
  rules: [],
  jev_api_key: '',
  jev: blankJev(),
  jevUsers: [],
  jevKeys: [],
  excludedModelsText: '',
  jevBalanceText: '',
})
function blankJev(): IntentJevConfig {
  return { enabled: false, rule: '', threshold: 0.5, max_turns: 3, timeout_ms: 1000, model: 'jev-latest', scope: 'all', user_ids: [], api_keys: [], sample_percent: 100, health_enabled: false, health_threshold: 0.7, health_rule: '', timely_enabled: false, timely_threshold: 0.9, timely_rule: '', low_balance_usd: 1, balance_usd: 0 }
}
const form = reactive<FormState>(blankForm())
// The blank form counts as saved until a group is loaded into it.
const baseline = ref(JSON.stringify(toInput()))
const formGeneration = ref(0)

const accountsById = computed(() => new Map(accounts.value.map((a) => [a.id, a])))
const selectedGroup = computed(() => groups.value.find((g) => g.id === selectedGroupId.value) ?? null)
const savedRouter = computed(() => (selectedGroupId.value ? routers.value[selectedGroupId.value] ?? null : null))
const dirty = computed(() => snapshot() !== baseline.value)
/** Anything on this group that leaving would lose; a save under way is not lost. */
// The save under way: which group, and exactly what it sent. Only that very
// form is exempt from the guard — another group, or edits made after
// pressing save, are still unsaved.
const inFlight = ref<{ groupId: number; snapshot: string } | null>(null)
/** Leaving the page or the route: nothing counts as saved until the save succeeded. */
const unsavedOnLeave = computed(() => !loading.value && (dirty.value || extrasDirty.value))
/** Switching groups: the form being saved may be left, its save carries on. */
const unsaved = computed(() => {
  if (loading.value) return false
  if (extrasDirty.value) return true
  if (!dirty.value) return false
  const pending = inFlight.value
  return !(pending && pending.groupId === selectedGroupId.value && pending.snapshot === snapshot())
})

function toInput(): IntentRouterInput {
  return {
    enabled: form.enabled,
    classifier_base_url: form.classifier_base_url,
    classifier_api_key: form.classifier_api_key,
    classifier_protocol: form.classifier_protocol,
    classifier_model: form.classifier_model,
    classifier_timeout_ms: Number(form.classifier_timeout_ms) || 0,
    cache_ttl_seconds: Number(form.cache_ttl_seconds) || 0,
    max_input_chars: Number(form.max_input_chars) || 0,
    keyword_max_chars: Math.max(0, Math.trunc(Number(form.keyword_max_chars) || 0)),
    unavailable_status: Number(form.unavailable_status) || 503,
    rules: form.rules.map(({ name, description, keywordsText, originalKeywords, target, account_ids, api_key_id, enabled }) => ({
      name, description, keywords: restoreOriginalControls(parseKeywords(keywordsText), originalKeywords),
      ...(target === 'key' ? { account_ids: [], api_key_id } : { account_ids: [...account_ids] }),
      enabled,
    })),
    excluded_models: form.excludedModelsText.split(/[\n,，、]+/).map((m) => m.trim()).filter(Boolean),
    jev_api_key: form.jev_api_key,
    jev: {
      ...form.jev,
      threshold: Number(form.jev.threshold) || 0,
      max_turns: Number(form.jev.max_turns) || 0,
      timeout_ms: Number(form.jev.timeout_ms) || 0,
      user_ids: form.jevUsers.map((u) => u.id),
      api_keys: form.jevKeys.map((k) => ({ id: k.id })),
      sample_percent: Number(form.jev.sample_percent) || 0,
      health_threshold: Number(form.jev.health_threshold) || 0,
      timely_threshold: Number(form.jev.timely_threshold) || 0,
      low_balance_usd: Number(form.jev.low_balance_usd) || 0,
    },
    ...(String(form.jevBalanceText).trim() !== '' ? { jev_set_balance_usd: Number(form.jevBalanceText) } : {}),
  }
}
function snapshot() { return JSON.stringify(toInput()) }

function fillForm(router: IntentRouter | null) {
  Object.assign(form, blankForm())
  if (router) {
    form.enabled = router.enabled
    form.classifier_base_url = router.classifier_base_url
    form.classifier_protocol = router.classifier_protocol
    form.classifier_model = router.classifier_model
    form.classifier_timeout_ms = router.classifier_timeout_ms
    form.cache_ttl_seconds = router.cache_ttl_seconds
    form.max_input_chars = router.max_input_chars
    form.keyword_max_chars = router.keyword_max_chars ?? 0
    form.unavailable_status = router.unavailable_status || 503
    form.excludedModelsText = (router.excluded_models ?? []).join('\n')
    form.jev = { ...blankJev(), ...(router.jev ?? {}) }
    const userLabels = new Map((router.jev_scope?.users ?? []).map((u) => [u.id, u.email]))
    const keyLabels = new Map((router.jev_scope?.api_keys ?? []).map((k) => [k.id, k.name]))
    form.jevUsers = (router.jev?.user_ids ?? []).map((id) => ({ id, email: userLabels.get(id) ?? '' }))
    form.jevKeys = (router.jev?.api_keys ?? []).map((k) => ({ id: k.id, user_id: k.user_id ?? 0, name: keyLabels.get(k.id) ?? '' }))
    for (const key of router.rule_keys ?? []) ruleKeyLabels.value.set(key.id, key)
    form.rules = (router.rules ?? []).map(({ keywords, api_key_id, ...rule }) => ({
      ...rule, keywordsText: formatKeywords(keywords ?? []), originalKeywords: [...(keywords ?? [])], account_ids: [...(rule.account_ids ?? [])],
      target: (api_key_id ? 'key' : 'accounts') as RuleTarget, api_key_id: api_key_id ?? 0, uid: nextUid++, search: '', keySearch: '',
    }))
  }
  baseline.value = snapshot()
  formGeneration.value++
}

function newRule(): RuleForm {
  return { uid: nextUid++, name: '', description: '', keywordsText: '', originalKeywords: [], target: 'accounts', account_ids: [], api_key_id: 0, enabled: true, search: '', keySearch: '' }
}

// ---- labels and target health ----

const ruleKeyLabels = ref(new Map<number, IntentRuleKeyLabel>())
function accountLabel(id: number) {
  const account = accountsById.value.get(id)
  return account ? `#${id} ${account.name}` : t('intentRouter.rules.missingAccount', { id })
}
function accountProblem(id: number) {
  const account = accountsById.value.get(id)
  if (!account || !selectedGroup.value) return ''
  return platformCompatible(selectedGroup.value.platform, account.platform) ? '' : t('intentRouter.rules.incompatible')
}
function keyLabel(id: number) {
  const key = ruleKeyLabels.value.get(id)
  if (!key || key.problem === 'key_not_found') return t('intentRouter.rules.missingKey', { id })
  return t('intentRouter.rules.keyChip', { id, name: key.name, user: key.user_email || `#${key.user_id}`, group: key.group_name || `#${key.group_id}`, platform: key.group_platform })
}
function keyProblem(id: number) {
  const key = ruleKeyLabels.value.get(id)
  if (!key) return ''
  if (key.problem) return t(`intentRouter.rules.keyProblems.${key.problem}`)
  return keyPlatformCompatible(selectedGroup.value?.platform ?? '', key.group_platform) ? '' : t('intentRouter.rules.incompatible')
}
function ruleTargetBroken(rule: RuleForm) {
  if (rule.target === 'key') return !rule.api_key_id || keyProblem(rule.api_key_id) !== ''
  if (rule.account_ids.length === 0) return true
  return rule.account_ids.some((id) => !accountsById.value.has(id) || accountProblem(id) !== '')
}

function groupStatus(groupId: number): { tone: Tone; label: string } {
  const saved = routers.value[groupId]
  if (!saved) return { tone: 'gray', label: t('intentRouter.status.none') }
  if (!saved.enabled) return { tone: 'gray', label: t('intentRouter.status.off') }
  const platform = groups.value.find((g) => g.id === groupId)?.platform ?? ''
  if (routerHasTargetProblem(saved, platform, accountsById.value)) return { tone: 'red', label: t('intentRouter.status.problem') }
  return { tone: 'green', label: t('intentRouter.status.on') }
}
const jevTone = computed((): Tone => {
  const state = jevStatus.value?.state
  if (state === 'auto_disabled') return 'red'
  if (state === 'paused') return 'amber'
  return 'green'
})

// ---- Jev status ----

// Every status request and every save takes a new sequence number; only the
// answer to the latest request may touch the page. An answer that left the
// server before a save (say "switched off") must not undo that save.
let jevStatusSeq = 0
async function loadJevStatus(groupId: number) {
  const seq = ++jevStatusSeq
  if (!routers.value[groupId]) {
    jevStatus.value = null
    return
  }
  try {
    const status = (await intentRoutersAPI.jevStatus(groupId)).data
    if (seq !== jevStatusSeq || selectedGroupId.value !== groupId) return
    jevStatus.value = status
    // Switched off by the server while this page still shows it on: follow,
    // or the next save would quietly turn it back on.
    const router = routers.value[groupId]
    if (status.state === 'auto_disabled' && router?.jev?.enabled) {
      const wasClean = !dirty.value
      routers.value = { ...routers.value, [groupId]: { ...router, jev: { ...router.jev, enabled: false, auto_disabled_status: status.auto_disabled_status, auto_disabled_detail: status.auto_disabled_detail, auto_disabled_at: status.auto_disabled_at } } }
      if (wasClean) fillForm(routers.value[groupId])
      else form.jev.enabled = false
      jevStatus.value = status
    }
  } catch {
    if (seq === jevStatusSeq && selectedGroupId.value === groupId) jevStatus.value = null
  }
}

provideIntentRouter({
  form, accounts, selectedGroup, savedRouter, jevStatus, dirty, formGeneration,
  accountLabel, accountProblem, keyLabel, keyProblem, ruleTargetBroken, newRule,
  rememberKey: (key) => { ruleKeyLabels.value.set(key.id, key) },
  loadJevStatus: () => { if (selectedGroupId.value) void loadJevStatus(selectedGroupId.value) },
})

// ---- navigation: tabs, the group, and the URL ----

function selectGroup(id: number) {
  selectedGroupId.value = id
  extras.monitor = false
  extras.prelude = false
  fillForm(routers.value[id] ?? null)
  void loadJevStatus(id)
}
function syncQuery() {
  const query = { ...route.query, tab: tab.value, sub: sub.value, group: selectedGroupId.value ? String(selectedGroupId.value) : undefined }
  if (query.tab === route.query.tab && query.sub === route.query.sub && query.group === route.query.group) return
  void router.replace({ query })
}
function setTab(next: Tab) {
  tab.value = next
  visited.add(next)
  syncQuery()
}
function setSub(next: Sub) {
  sub.value = next
  syncQuery()
}
/** Moves to another group, asking first when that would lose edits. */
function requestGroup(id: number) {
  if (id === selectedGroupId.value) return
  if (unsaved.value) {
    openConfirm('group', id)
    return
  }
  selectGroup(id)
  syncQuery()
}
// Back and forward change the query under us.
watch(() => route.query, (query) => {
  if (loading.value) return
  const nextTab = TABS.find((x) => x === query.tab)
  if (nextTab && nextTab !== tab.value) {
    tab.value = nextTab
    visited.add(nextTab)
  }
  const nextSub = SUBS.find((x) => x === query.sub)
  if (nextSub && nextSub !== sub.value) sub.value = nextSub
  const id = Number(query.group)
  if (id && id !== selectedGroupId.value && groups.value.some((g) => g.id === id)) requestGroup(id)
})

// ---- confirmation: leaving edits behind, deleting ----

const confirm = reactive<{ open: boolean; kind: 'group' | 'leave' | 'delete'; groupId: number; path: string }>({ open: false, kind: 'group', groupId: 0, path: '' })
let leaveApproved = false
function openConfirm(kind: 'group' | 'leave' | 'delete', groupId = 0, path = '') {
  Object.assign(confirm, { open: true, kind, groupId, path })
}
function onConfirm() {
  confirm.open = false
  if (confirm.kind === 'group') {
    selectGroup(confirm.groupId)
    syncQuery()
  } else if (confirm.kind === 'leave') {
    leaveApproved = true
    void router.push(confirm.path)
  } else {
    void removeRouter()
  }
}
function onCancel() {
  confirm.open = false
  // The URL may already name the group the admin decided not to open.
  if (confirm.kind === 'group') syncQuery()
}
onBeforeRouteLeave((to) => {
  if (leaveApproved || !unsavedOnLeave.value) return true
  openConfirm('leave', 0, to.fullPath)
  return false
})
function onBeforeUnload(event: BeforeUnloadEvent) {
  event.preventDefault()
  event.returnValue = ''
}
watch(unsavedOnLeave, (now) => {
  if (now) window.addEventListener('beforeunload', onBeforeUnload)
  else window.removeEventListener('beforeunload', onBeforeUnload)
})
onBeforeUnmount(() => window.removeEventListener('beforeunload', onBeforeUnload))

// ---- saving ----

function errorText(err: unknown) {
  return extractI18nErrorMessage(err, t, 'intentRouter.errors', t('common.error'))
}

// Every request below is tied to the group it was started for. The admin may
// pick another group while it is in flight; its answer then still updates that
// group's stored state, but must never touch the form, which by now shows a
// different group — refilling it would let the next save overwrite the wrong one.
async function save() {
  const groupId = selectedGroupId.value
  if (!groupId || busy.value) return
  saving.value = true
  jevStatusSeq++
  const input = toInput()
  inFlight.value = { groupId, snapshot: JSON.stringify(input) }
  try {
    const res = await intentRoutersAPI.save(groupId, input)
    routers.value = { ...routers.value, [groupId]: res.data }
    if (selectedGroupId.value === groupId) {
      fillForm(res.data)
      void loadJevStatus(groupId)
    }
    appStore.showSuccess(t('intentRouter.actions.saved'))
  } catch (err) {
    appStore.showError(errorText(err))
  } finally {
    saving.value = false
    inFlight.value = null
  }
}

function askDelete() {
  if (selectedGroup.value && !busy.value) openConfirm('delete', selectedGroup.value.id)
}
async function removeRouter() {
  const group = groups.value.find((g) => g.id === confirm.groupId)
  if (!group || busy.value) return
  working.value = true
  try {
    await intentRoutersAPI.remove(group.id)
    const next = { ...routers.value }
    delete next[group.id]
    routers.value = next
    if (selectedGroupId.value === group.id) fillForm(null)
    appStore.showSuccess(t('intentRouter.actions.deleted'))
  } catch (err) {
    appStore.showError(errorText(err))
  } finally {
    working.value = false
  }
}

async function clearCache(groupId: number) {
  if (busy.value) return
  working.value = true
  try {
    const res = await intentRoutersAPI.clearCache(groupId)
    appStore.showSuccess(t('intentRouter.actions.cleared', { count: res.data.removed }))
  } catch (err) {
    appStore.showError(errorText(err))
  } finally {
    working.value = false
  }
}

function groupName(id: number) { return groups.value.find((g) => g.id === id)?.name ?? `#${id}` }
const groupNameMap = computed(() => Object.fromEntries(groups.value.map((g) => [g.id, g.name])) as Record<number, string>)

async function loadAccounts() {
  const all: AccountOption[] = []
  for (let page = 1; page <= ACCOUNT_MAX_PAGES; page++) {
    const res = await adminAPI.accounts.list(page, ACCOUNT_PAGE_SIZE, { lite: '1' })
    const items = res.items ?? []
    all.push(...items.map((a) => ({ id: a.id, name: a.name, platform: a.platform })))
    if (items.length < ACCOUNT_PAGE_SIZE) break
  }
  accounts.value = all
}

onMounted(async () => {
  try {
    const [groupList, routerList] = await Promise.all([
      adminAPI.groups.getAllIncludingInactive(),
      intentRoutersAPI.list(),
      loadAccounts(),
    ])
    groups.value = groupList.map((g) => ({ id: g.id, name: g.name, platform: g.platform }))
    routers.value = Object.fromEntries((routerList.data ?? []).map((r) => [r.group_id, r]))
    // Open where the URL points, falling back to the first configured group.
    tab.value = TABS.find((x) => x === route.query.tab) ?? 'config'
    sub.value = SUBS.find((x) => x === route.query.sub) ?? 'rules'
    visited.add(tab.value)
    const wanted = groups.value.find((g) => g.id === Number(route.query.group))
    const first = wanted ?? groups.value.find((g) => routers.value[g.id]) ?? groups.value[0]
    if (first) selectGroup(first.id)
  } catch (err) {
    appStore.showError(errorText(err))
  } finally {
    loading.value = false
  }
  syncQuery()
})
</script>

<style scoped>
.ir-tab {
  @apply flex items-center gap-1.5 whitespace-nowrap border-b-2 px-3 py-2.5 text-sm font-medium transition-colors;
}
.ir-tab-active {
  @apply border-primary-600 text-primary-600 dark:border-primary-400 dark:text-primary-400;
}
.ir-tab-inactive {
  @apply border-transparent text-gray-500 hover:border-gray-300 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-300;
}
</style>
