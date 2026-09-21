<template>
  <AppLayout>
    <div class="space-y-6">
      <div>
        <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">{{ t('intentRouter.title') }}</h1>
        <p class="mt-1 max-w-4xl text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.description') }}</p>
        <p class="mt-2 max-w-4xl rounded-lg bg-gray-50 p-3 text-sm leading-6 text-gray-600 dark:bg-dark-800 dark:text-gray-300">
          {{ t('intentRouter.howItWorks') }}
        </p>
      </div>

      <div v-if="loading" class="flex items-center justify-center py-16">
        <div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600"></div>
      </div>

      <div v-else class="grid gap-6 lg:grid-cols-[18rem_minmax(0,1fr)]">
        <!-- Groups -->
        <aside class="card h-fit p-3">
          <p class="px-2 pb-2 text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('intentRouter.groups') }}</p>
          <input v-model.trim="groupSearch" type="search" class="input mb-2" :placeholder="t('intentRouter.searchGroups')" />
          <p v-if="filteredGroups.length === 0" class="px-2 py-4 text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.noGroups') }}</p>
          <ul class="max-h-[32rem] space-y-1 overflow-y-auto">
            <li v-for="group in filteredGroups" :key="group.id">
              <button
                type="button"
                :data-test="`intent-group-${group.id}`"
                :class="[
                  'flex w-full items-center justify-between gap-2 rounded-lg px-2 py-2 text-left text-sm transition-colors',
                  selectedGroupId === group.id
                    ? 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-200'
                    : 'text-gray-700 hover:bg-gray-50 dark:text-gray-300 dark:hover:bg-dark-700',
                ]"
                @click="selectGroup(group.id)"
              >
                <span class="min-w-0">
                  <span class="block truncate font-medium">{{ group.name }}</span>
                  <span class="block text-xs text-gray-500 dark:text-gray-400">{{ group.platform }}</span>
                </span>
                <span :class="['flex-shrink-0 rounded-full px-2 py-0.5 text-xs', statusClass(group.id)]">
                  {{ t(`intentRouter.status.${routerStatus(group.id)}`) }}
                </span>
              </button>
            </li>
          </ul>
          <button type="button" class="btn btn-secondary mt-3 w-full text-sm" :disabled="busy" @click="clearCache(0)">
            {{ t('intentRouter.actions.clearCacheAll') }}
          </button>
        </aside>

        <!-- Editor -->
        <section v-if="selectedGroup" class="min-w-0 space-y-6">
          <div class="card space-y-5 p-5">
            <label class="flex items-start gap-3">
              <input v-model="form.enabled" data-test="intent-enabled" type="checkbox" class="mt-1 h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
              <span>
                <span class="block text-sm font-medium text-gray-900 dark:text-white">{{ t('intentRouter.enabled') }} — {{ selectedGroup.name }}</span>
                <span class="block text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.enabledHint') }}</span>
              </span>
            </label>

            <div>
              <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('intentRouter.classifier.title') }}</h2>
              <p class="mt-1 text-sm leading-6 text-gray-500 dark:text-gray-400">{{ t('intentRouter.classifier.intro') }}</p>
              <div class="mt-3 grid gap-4 md:grid-cols-2">
                <div>
                  <label class="input-label">{{ t('intentRouter.classifier.baseUrl') }}</label>
                  <input v-model.trim="form.classifier_base_url" type="url" class="input" :placeholder="t('intentRouter.classifier.baseUrlPlaceholder')" />
                  <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.classifier.baseUrlHint') }}</p>
                </div>
                <div>
                  <label class="input-label">{{ t('intentRouter.classifier.protocol') }}</label>
                  <select v-model="form.classifier_protocol" class="input">
                    <option value="openai_chat">{{ t('intentRouter.classifier.protocolOpenAI') }}</option>
                    <option value="gemini">{{ t('intentRouter.classifier.protocolGemini') }}</option>
                  </select>
                </div>
                <div>
                  <label class="input-label">{{ t('intentRouter.classifier.model') }}</label>
                  <input v-model.trim="form.classifier_model" data-test="intent-model" type="text" class="input" :placeholder="t('intentRouter.classifier.modelPlaceholder')" />
                </div>
                <div>
                  <label class="input-label">{{ t('intentRouter.classifier.apiKey') }}</label>
                  <input
                    v-model.trim="form.classifier_api_key"
                    data-test="intent-api-key"
                    type="password"
                    autocomplete="new-password"
                    class="input"
                    :placeholder="keyConfigured ? t('intentRouter.classifier.apiKeyKeep') : t('intentRouter.classifier.apiKeyPlaceholder')"
                  />
                </div>
                <div>
                  <label class="input-label">{{ t('intentRouter.classifier.timeout') }}</label>
                  <input v-model.number="form.classifier_timeout_ms" type="number" min="300" max="15000" step="100" class="input" />
                  <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.classifier.timeoutHint') }}</p>
                </div>
                <div>
                  <label class="input-label">{{ t('intentRouter.classifier.cacheTtl') }}</label>
                  <input v-model.number="cacheTtlMinutes" data-test="intent-cache-minutes" type="number" min="1" max="10080" class="input" />
                  <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.classifier.cacheTtlHint') }}</p>
                </div>
                <div>
                  <label class="input-label">{{ t('intentRouter.classifier.maxInput') }}</label>
                  <input v-model.number="form.max_input_chars" type="number" min="100" max="20000" step="100" class="input" />
                  <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.classifier.maxInputHint') }}</p>
                </div>
              </div>
            </div>
          </div>

          <!-- Rules -->
          <div class="card space-y-4 p-5">
            <div class="flex flex-wrap items-start justify-between gap-3">
              <div>
                <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('intentRouter.rules.title') }}</h2>
                <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.rules.intro') }}</p>
              </div>
              <button type="button" data-test="intent-add-rule" class="btn btn-secondary text-sm" @click="addRule">{{ t('intentRouter.rules.add') }}</button>
            </div>
            <p v-if="form.rules.length === 0" class="rounded-lg border border-dashed border-gray-300 p-4 text-sm text-gray-500 dark:border-dark-600 dark:text-gray-400">
              {{ t('intentRouter.rules.empty') }}
            </p>

            <div v-for="(rule, index) in form.rules" :key="rule.uid" :data-test="`intent-rule-${index}`" class="space-y-3 rounded-xl border border-gray-200 p-4 dark:border-dark-600">
              <div class="grid gap-3 md:grid-cols-[14rem_minmax(0,1fr)]">
                <div>
                  <label class="input-label">{{ t('intentRouter.rules.name') }}</label>
                  <input v-model.trim="rule.name" data-test="intent-rule-name" type="text" maxlength="40" class="input" :placeholder="t('intentRouter.rules.namePlaceholder')" />
                </div>
                <div>
                  <label class="input-label">{{ t('intentRouter.rules.description') }}</label>
                  <textarea v-model.trim="rule.description" data-test="intent-rule-description" rows="2" maxlength="500" class="input" :placeholder="t('intentRouter.rules.descriptionPlaceholder')"></textarea>
                </div>
              </div>

              <div>
                <label class="input-label">{{ t('intentRouter.rules.accounts') }}</label>
                <div class="flex flex-wrap gap-2">
                  <span
                    v-for="id in rule.account_ids"
                    :key="id"
                    :class="[
                      'inline-flex items-center gap-1 rounded-full px-2.5 py-1 text-xs',
                      accountProblem(id)
                        ? 'bg-red-50 text-red-700 dark:bg-red-900/30 dark:text-red-300'
                        : 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-gray-200',
                    ]"
                  >
                    {{ accountLabel(id) }}
                    <span v-if="accountProblem(id)">· {{ accountProblem(id) }}</span>
                    <button type="button" class="ml-0.5 text-gray-400 hover:text-red-500" :aria-label="t('common.delete')" @click="removeAccount(rule, id)">×</button>
                  </span>
                </div>
                <input
                  v-model.trim="rule.search"
                  data-test="intent-account-search"
                  type="search"
                  class="input mt-2"
                  :placeholder="t('intentRouter.rules.searchAccounts')"
                />
                <ul v-if="rule.search" class="mt-1 max-h-48 overflow-y-auto rounded-lg border border-gray-200 dark:border-dark-600">
                  <li v-if="accountMatches(rule).length === 0" class="px-3 py-2 text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.rules.noAccounts') }}</li>
                  <li v-for="account in accountMatches(rule)" :key="account.id">
                    <button
                      type="button"
                      :data-test="`intent-account-option-${account.id}`"
                      class="flex w-full items-center justify-between gap-3 px-3 py-2 text-left text-sm hover:bg-gray-50 dark:hover:bg-dark-700"
                      @click="addAccount(rule, account.id)"
                    >
                      <span class="min-w-0 truncate text-gray-900 dark:text-white">#{{ account.id }} {{ account.name }}</span>
                      <span class="flex-shrink-0 text-xs text-gray-500 dark:text-gray-400">{{ account.platform }}</span>
                    </button>
                  </li>
                </ul>
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.rules.accountsHint') }}</p>
              </div>

              <div class="flex items-center justify-between">
                <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                  <input v-model="rule.enabled" type="checkbox" class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
                  {{ t('intentRouter.rules.ruleEnabled') }}
                </label>
                <button type="button" class="text-sm text-red-600 hover:underline dark:text-red-400" @click="form.rules.splice(index, 1)">{{ t('intentRouter.rules.remove') }}</button>
              </div>
            </div>
          </div>

          <div class="flex flex-wrap items-center gap-3">
            <button type="button" data-test="intent-save" class="btn btn-primary" :disabled="busy" @click="save">
              {{ saving ? t('intentRouter.actions.saving') : t('intentRouter.actions.save') }}
            </button>
            <button v-if="savedRouter" type="button" class="btn btn-secondary" :disabled="busy" @click="clearCache(selectedGroup.id)">{{ t('intentRouter.actions.clearCache') }}</button>
            <button v-if="savedRouter" type="button" class="ml-auto text-sm text-red-600 hover:underline dark:text-red-400" :disabled="busy" @click="removeRouter">
              {{ t('intentRouter.actions.delete') }}
            </button>
          </div>

          <!-- Try it -->
          <div v-if="savedRouter" class="card space-y-3 p-5">
            <div>
              <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('intentRouter.test.title') }}</h2>
              <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.test.intro') }}</p>
            </div>
            <textarea v-model.trim="testText" data-test="intent-test-text" rows="3" class="input" :placeholder="t('intentRouter.test.placeholder')"></textarea>
            <p v-if="dirty" class="text-sm text-amber-700 dark:text-amber-300">{{ t('intentRouter.test.saveFirst') }}</p>
            <button type="button" data-test="intent-test-run" class="btn btn-secondary" :disabled="testing || dirty || !testText" @click="runTest">
              {{ testing ? t('intentRouter.test.running') : t('intentRouter.test.run') }}
            </button>
            <div v-if="testResult" data-test="intent-test-result" class="rounded-lg bg-gray-50 p-3 text-sm leading-6 dark:bg-dark-800">
              <p class="font-medium text-gray-900 dark:text-white">{{ testSummary }}</p>
              <p class="text-gray-500 dark:text-gray-400">
                {{ t('intentRouter.test.answer', { answer: testResult.answer || '—' }) }} · {{ t('intentRouter.test.latency', { ms: testResult.latency_ms }) }}
              </p>
            </div>
            <p v-if="testError" data-test="intent-test-error" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ testError }}</p>
          </div>
        </section>
        <section v-else class="card p-8 text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.selectGroup') }}</section>
      </div>

      <!-- Recent routing -->
      <div v-if="!loading" class="card p-5">
        <div class="flex items-center justify-between">
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('intentRouter.events.title') }}</h2>
          <button type="button" class="btn btn-secondary text-sm" :disabled="eventsLoading" @click="loadEvents">{{ t('intentRouter.events.refresh') }}</button>
        </div>
        <p v-if="events.length === 0" class="mt-3 text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.events.empty') }}</p>
        <div v-else class="mt-3 overflow-x-auto">
          <table class="min-w-full text-left text-sm">
            <thead class="text-xs text-gray-500 dark:text-gray-400">
              <tr>
                <th class="py-2 pr-4 font-medium">{{ t('intentRouter.events.time') }}</th>
                <th class="py-2 pr-4 font-medium">{{ t('intentRouter.events.group') }}</th>
                <th class="py-2 pr-4 font-medium">{{ t('intentRouter.events.kind') }}</th>
                <th class="py-2 pr-4 font-medium">{{ t('intentRouter.events.intent') }}</th>
                <th class="py-2 pr-4 font-medium">{{ t('intentRouter.events.account') }}</th>
                <th class="py-2 pr-4 font-medium">{{ t('intentRouter.events.latency') }}</th>
                <th class="py-2 font-medium">{{ t('intentRouter.events.detail') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="(event, i) in events" :key="i" class="text-gray-700 dark:text-gray-300">
                <td class="whitespace-nowrap py-2 pr-4 tabular-nums">{{ formatTime(event.time) }}</td>
                <td class="whitespace-nowrap py-2 pr-4">{{ groupName(event.group_id) }}</td>
                <td class="whitespace-nowrap py-2 pr-4">
                  <span :class="['rounded-full px-2 py-0.5 text-xs', eventClass(event.kind)]">{{ t(`intentRouter.events.kinds.${event.kind}`) }}</span>
                </td>
                <td class="whitespace-nowrap py-2 pr-4">{{ event.kind === 'classified' && !event.intent ? t('intentRouter.events.noIntent') : event.intent || '—' }}</td>
                <td class="whitespace-nowrap py-2 pr-4">{{ event.account_id ? accountLabel(event.account_id) : '—' }}</td>
                <td class="whitespace-nowrap py-2 pr-4 tabular-nums">{{ event.latency_ms ? `${event.latency_ms} ms` : '—' }}</td>
                <td class="max-w-md break-words py-2 text-xs text-gray-500 dark:text-gray-400">{{ event.detail || '' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import { intentRoutersAPI } from '@/api/admin/intentRouters'
import type { IntentClassifyTestResult, IntentRouteEvent, IntentRouter, IntentRouterInput } from '@/api/admin/intentRouters'
import { extractI18nErrorMessage } from '@/utils/apiError'

interface GroupOption { id: number; name: string; platform: string }
interface AccountOption { id: number; name: string; platform: string }
interface RuleForm { uid: number; name: string; description: string; account_ids: number[]; enabled: boolean; search: string }
type FormState = Omit<IntentRouterInput, 'rules'> & { rules: RuleForm[] }

const ACCOUNT_PAGE_SIZE = 1000
const ACCOUNT_MAX_PAGES = 10

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(true)
const saving = ref(false)
const working = ref(false)
const testing = ref(false)
const eventsLoading = ref(false)
const busy = computed(() => saving.value || working.value)

const groups = ref<GroupOption[]>([])
const accounts = ref<AccountOption[]>([])
const routers = ref<Record<number, IntentRouter>>({})
const events = ref<IntentRouteEvent[]>([])
const groupSearch = ref('')
const selectedGroupId = ref<number | null>(null)
const testText = ref('')
const testResult = ref<IntentClassifyTestResult | null>(null)
const testError = ref('')

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
  rules: [],
})
const form = reactive<FormState>(blankForm())
const baseline = ref('')

const accountsById = computed(() => new Map(accounts.value.map((a) => [a.id, a])))
const selectedGroup = computed(() => groups.value.find((g) => g.id === selectedGroupId.value) ?? null)
const savedRouter = computed(() => (selectedGroupId.value ? routers.value[selectedGroupId.value] ?? null : null))
const keyConfigured = computed(() => savedRouter.value?.classifier_api_key_configured === true)
const filteredGroups = computed(() => {
  const q = groupSearch.value.toLowerCase()
  return q ? groups.value.filter((g) => g.name.toLowerCase().includes(q) || String(g.id) === q) : groups.value
})
const cacheTtlMinutes = computed({
  get: () => Math.round(form.cache_ttl_seconds / 60),
  set: (minutes: number) => { form.cache_ttl_seconds = Math.round((Number(minutes) || 0) * 60) },
})
const dirty = computed(() => snapshot() !== baseline.value)

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
    rules: form.rules.map(({ name, description, account_ids, enabled }) => ({ name, description, account_ids: [...account_ids], enabled })),
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
    form.rules = (router.rules ?? []).map((rule) => ({ ...rule, account_ids: [...(rule.account_ids ?? [])], uid: nextUid++, search: '' }))
  }
  baseline.value = snapshot()
  testResult.value = null
  testError.value = ''
}

function selectGroup(id: number) {
  selectedGroupId.value = id
  fillForm(routers.value[id] ?? null)
}

function routerStatus(groupId: number): 'on' | 'off' | 'none' {
  const router = routers.value[groupId]
  if (!router) return 'none'
  return router.enabled ? 'on' : 'off'
}
function statusClass(groupId: number) {
  switch (routerStatus(groupId)) {
    case 'on': return 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
    case 'off': return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
    default: return 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-400'
  }
}

// Mirrors the server's rule: same platform, or Antigravity for Claude/Gemini groups.
function platformCompatible(groupPlatform: string, accountPlatform: string) {
  if (groupPlatform === 'anthropic' || groupPlatform === 'gemini') return accountPlatform === groupPlatform || accountPlatform === 'antigravity'
  if (groupPlatform === 'openai' || groupPlatform === 'antigravity') return accountPlatform === groupPlatform
  return true
}
function accountLabel(id: number) {
  const account = accountsById.value.get(id)
  return account ? `#${id} ${account.name}` : t('intentRouter.rules.missingAccount', { id })
}
function accountProblem(id: number) {
  const account = accountsById.value.get(id)
  if (!account || !selectedGroup.value) return ''
  return platformCompatible(selectedGroup.value.platform, account.platform) ? '' : t('intentRouter.rules.incompatible')
}
function accountMatches(rule: RuleForm) {
  const q = rule.search.toLowerCase()
  const groupPlatform = selectedGroup.value?.platform ?? ''
  return accounts.value
    .filter((a) => !rule.account_ids.includes(a.id) && platformCompatible(groupPlatform, a.platform))
    .filter((a) => a.name.toLowerCase().includes(q) || String(a.id) === q)
    .slice(0, 30)
}
function addAccount(rule: RuleForm, id: number) {
  if (!rule.account_ids.includes(id)) rule.account_ids.push(id)
  rule.search = ''
}
function removeAccount(rule: RuleForm, id: number) {
  rule.account_ids = rule.account_ids.filter((x) => x !== id)
}
function addRule() {
  form.rules.push({ uid: nextUid++, name: '', description: '', account_ids: [], enabled: true, search: '' })
}

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
  try {
    const res = await intentRoutersAPI.save(groupId, toInput())
    routers.value = { ...routers.value, [groupId]: res.data }
    if (selectedGroupId.value === groupId) fillForm(res.data)
    appStore.showSuccess(t('intentRouter.actions.saved'))
  } catch (err) {
    appStore.showError(errorText(err))
  } finally {
    saving.value = false
  }
}

async function removeRouter() {
  const group = selectedGroup.value
  if (!group || busy.value || !window.confirm(t('intentRouter.actions.deleteConfirm', { group: group.name }))) return
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

const testSummary = computed(() => {
  const result = testResult.value
  if (!result) return ''
  if (!result.understood) return t('intentRouter.test.resultUnclear')
  if (!result.intent) return t('intentRouter.test.resultNone')
  return t('intentRouter.test.resultIntent', { intent: result.intent, accounts: result.account_ids.map(accountLabel).join('、') })
})

async function runTest() {
  const groupId = selectedGroupId.value
  if (!groupId || testing.value || dirty.value || !testText.value) return
  testing.value = true
  testResult.value = null
  testError.value = ''
  try {
    const result = (await intentRoutersAPI.test(groupId, testText.value)).data
    if (selectedGroupId.value === groupId) testResult.value = result
  } catch (err) {
    if (selectedGroupId.value === groupId) testError.value = errorText(err)
  } finally {
    testing.value = false
  }
}

function groupName(id: number) { return groups.value.find((g) => g.id === id)?.name ?? `#${id}` }
function formatTime(value: string) {
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString()
}
function eventClass(kind: IntentRouteEvent['kind']) {
  if (kind === 'error') return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  if (kind === 'skipped') return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
  if (kind === 'routed') return 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
}

async function loadEvents() {
  eventsLoading.value = true
  try {
    events.value = (await intentRoutersAPI.events(100)).data ?? []
  } catch {
    // The log is a convenience; the page works without it.
  } finally {
    eventsLoading.value = false
  }
}

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
    const first = groups.value.find((g) => routers.value[g.id]) ?? groups.value[0]
    if (first) selectGroup(first.id)
  } catch (err) {
    appStore.showError(errorText(err))
  } finally {
    loading.value = false
  }
  void loadEvents()
})
</script>
