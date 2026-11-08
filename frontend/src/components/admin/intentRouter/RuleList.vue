<template>
  <div class="space-y-3">
    <div class="flex items-center justify-between gap-3">
      <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('intentRouter.rules.title') }}</h2>
      <button type="button" data-test="intent-add-rule" class="btn btn-secondary btn-sm" @click="addRule">+ {{ t('intentRouter.rules.add') }}</button>
    </div>

    <div v-if="form.rules.length === 0" class="rounded-xl border border-dashed border-gray-300 px-4 py-8 text-center text-sm text-gray-500 dark:border-dark-600 dark:text-gray-400">
      {{ t('intentRouter.rules.empty') }}
    </div>

    <ul v-else class="divide-y divide-gray-100 rounded-xl border border-gray-200 dark:divide-dark-700 dark:border-dark-600">
      <li v-for="(rule, index) in form.rules" :key="rule.uid" :data-test="`intent-rule-${index}`" :class="['first:rounded-t-xl last:rounded-b-xl', expanded.has(rule.uid) ? 'bg-gray-50/60 dark:bg-dark-800/40' : '']">
        <!-- Summary row -->
        <div class="flex items-start gap-3 px-3 py-2.5 sm:items-center">
          <button
            type="button"
            :data-test="`intent-rule-toggle-${index}`"
            class="flex min-w-0 flex-1 flex-col gap-1 text-left sm:flex-row sm:items-center sm:gap-3"
            :aria-expanded="expanded.has(rule.uid)"
            @click="toggle(rule.uid)"
          >
            <span class="flex min-w-0 items-center gap-2 sm:w-44 sm:flex-shrink-0">
              <StatusDot :tone="ruleTone(rule)" :label="ruleStateLabel(rule)" />
              <span :class="['truncate font-medium', rule.name.trim() ? 'text-gray-900 dark:text-white' : 'italic text-gray-400']">{{ rule.name.trim() || t('intentRouter.rules.unnamed') }}</span>
            </span>
            <span class="flex w-full min-w-0 flex-1 flex-col gap-1 overflow-hidden pl-4 sm:pl-0">
              <span class="flex min-w-0 flex-wrap items-center gap-1.5">
                <span v-for="role in roles(rule)" :key="role" class="whitespace-nowrap rounded bg-primary-50 px-1.5 py-0.5 text-xs text-primary-700 dark:bg-primary-900/30 dark:text-primary-300">{{ role }}</span>
                <template v-if="keywordsOf(rule).length">
                  <span v-for="kw in keywordsOf(rule).slice(0, 3)" :key="kw" class="max-w-[8rem] truncate rounded bg-gray-100 px-1.5 py-0.5 font-mono text-xs text-gray-700 dark:bg-dark-700 dark:text-gray-300">{{ kw }}</span>
                  <span v-if="keywordsOf(rule).length > 3" class="text-xs text-gray-400">+{{ keywordsOf(rule).length - 3 }}</span>
                </template>
                <span v-else class="rounded border border-dashed border-gray-300 px-1.5 py-0.5 text-xs text-gray-400 dark:border-dark-500">{{ t('intentRouter.rules.noKeywords') }}</span>
              </span>
              <span :class="['min-w-0 truncate text-xs', ruleTargetBroken(rule) ? 'text-red-600 dark:text-red-400' : 'text-gray-500 dark:text-gray-400']" :title="targetSummary(rule)">→ {{ targetSummary(rule) }}</span>
            </span>
          </button>
          <div class="flex flex-shrink-0 items-center gap-1">
            <Toggle v-model="rule.enabled" :data-test="`intent-rule-enabled-${index}`" :aria-label="t('intentRouter.rules.ruleEnabled')" />
            <div class="hidden items-center sm:flex">
              <button type="button" :data-test="`intent-rule-up-${index}`" class="icon-btn" :disabled="index === 0" :aria-label="t('intentRouter.rules.moveUp')" :title="t('intentRouter.rules.moveUp')" @click="move(index, -1)">↑</button>
              <button type="button" :data-test="`intent-rule-down-${index}`" class="icon-btn" :disabled="index === form.rules.length - 1" :aria-label="t('intentRouter.rules.moveDown')" :title="t('intentRouter.rules.moveDown')" @click="move(index, 1)">↓</button>
              <button type="button" class="icon-btn hover:text-red-600" :aria-label="t('intentRouter.rules.remove')" :title="t('intentRouter.rules.remove')" @click="remove(index)">✕</button>
            </div>
            <div class="relative sm:hidden">
              <button type="button" :data-test="`intent-rule-more-${index}`" class="icon-btn" :aria-label="t('intentRouter.rules.more')" @click.stop="menuFor = menuFor === rule.uid ? 0 : rule.uid">⋯</button>
              <div v-if="menuFor === rule.uid" :data-test="`intent-rule-menu-${index}`" :class="['absolute right-0 z-20 w-32 rounded-lg', index === form.rules.length - 1 && index > 0 ? 'bottom-full mb-1' : 'top-full mt-1']" class="border border-gray-200 bg-white py-1 text-sm shadow-lg dark:border-dark-600 dark:bg-dark-800">
                <button type="button" class="menu-item" :disabled="index === 0" @click="move(index, -1)">{{ t('intentRouter.rules.moveUp') }}</button>
                <button type="button" class="menu-item" :disabled="index === form.rules.length - 1" @click="move(index, 1)">{{ t('intentRouter.rules.moveDown') }}</button>
                <button type="button" class="menu-item text-red-600 dark:text-red-400" @click="remove(index)">{{ t('intentRouter.rules.remove') }}</button>
              </div>
            </div>
          </div>
        </div>

        <!-- Editor -->
        <div v-show="expanded.has(rule.uid)" class="space-y-4 border-t border-gray-100 px-3 pb-4 pt-3 dark:border-dark-700 sm:pl-7">
          <div class="max-w-xs">
            <label class="input-label">{{ t('intentRouter.rules.name') }}</label>
            <input v-model.trim="rule.name" data-test="intent-rule-name" type="text" maxlength="40" class="input" :placeholder="t('intentRouter.rules.namePlaceholder')" />
          </div>

          <div>
            <label class="input-label flex items-center gap-1">{{ t('intentRouter.rules.keywords') }} <InfoTip :text="t('intentRouter.rules.keywordsHint')" :label="t('intentRouter.rules.keywords')" /></label>
            <textarea v-model="rule.keywordsText" data-test="intent-rule-keywords" rows="2" class="input" :placeholder="t('intentRouter.rules.keywordsPlaceholder')"></textarea>
          </div>

          <div>
            <div class="mb-2 inline-flex rounded-lg bg-gray-100 p-0.5 text-sm dark:bg-dark-700" role="radiogroup" :aria-label="t('intentRouter.rules.target')">
              <label :class="segment(rule.target === 'accounts')">
                <input :checked="rule.target === 'accounts'" data-test="intent-rule-target-accounts" type="radio" class="sr-only" :name="`intent-rule-target-${rule.uid}`" @change="setRuleTarget(rule, 'accounts')" />{{ t('intentRouter.rules.targetAccounts') }}
              </label>
              <label :class="segment(rule.target === 'key')">
                <input :checked="rule.target === 'key'" data-test="intent-rule-target-key" type="radio" class="sr-only" :name="`intent-rule-target-${rule.uid}`" @change="setRuleTarget(rule, 'key')" />{{ t('intentRouter.rules.targetKey') }}
              </label>
              <InfoTip class="ml-1 self-center pr-1" :text="rule.target === 'key' ? t('intentRouter.rules.keyHint') : t('intentRouter.rules.accountsHint')" :label="t('intentRouter.rules.target')" />
            </div>

            <div v-if="rule.target === 'key'">
              <div v-if="rule.api_key_id" class="flex flex-wrap gap-2">
                <span data-test="intent-rule-key-chip" :class="chip(!!keyProblem(rule.api_key_id))">
                  {{ keyLabel(rule.api_key_id) }}
                  <span v-if="keyProblem(rule.api_key_id)">· {{ keyProblem(rule.api_key_id) }}</span>
                  <button type="button" class="ml-0.5 text-gray-400 hover:text-red-500" :aria-label="t('common.delete')" @click="rule.api_key_id = 0">×</button>
                </span>
              </div>
              <input v-model.trim="rule.keySearch" data-test="intent-key-search" type="search" class="input mt-2" :placeholder="t('intentRouter.rules.searchKeys')" @input="searchRuleKeys(rule)" />
              <ul v-if="rule.keySearch" class="mt-1 max-h-48 overflow-y-auto rounded-lg border border-gray-200 bg-white dark:border-dark-600 dark:bg-dark-800">
                <li v-if="(keyResults[rule.uid] ?? []).length === 0" class="px-3 py-2 text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.rules.noKeys') }}</li>
                <li v-for="key in keyResults[rule.uid] ?? []" :key="key.id">
                  <button
                    type="button"
                    :data-test="`intent-key-option-${key.id}`"
                    class="flex w-full items-center justify-between gap-3 px-3 py-2 text-left text-sm hover:bg-gray-50 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:bg-dark-700"
                    :disabled="key.problem !== ''"
                    @click="chooseRuleKey(rule, key)"
                  >
                    <span class="min-w-0 truncate text-gray-900 dark:text-white">#{{ key.id }} {{ key.name }} <span class="text-gray-500 dark:text-gray-400">· {{ key.user_email || `#${key.user_id}` }}</span></span>
                    <span class="flex-shrink-0 text-xs text-gray-500 dark:text-gray-400">{{ key.problem ? keyProblemText(key.problem) : `${key.group_name} · ${key.group_platform}` }}</span>
                  </button>
                </li>
              </ul>
            </div>

            <div v-else>
              <div class="flex flex-wrap gap-2">
                <span v-for="id in rule.account_ids" :key="id" :class="chip(!!accountProblem(id) || !accounts.some((a) => a.id === id))">
                  {{ accountLabel(id) }}
                  <span v-if="accountProblem(id)">· {{ accountProblem(id) }}</span>
                  <button type="button" class="ml-0.5 text-gray-400 hover:text-red-500" :aria-label="t('common.delete')" @click="rule.account_ids = rule.account_ids.filter((x) => x !== id)">×</button>
                </span>
              </div>
              <input v-model.trim="rule.search" data-test="intent-account-search" type="search" class="input mt-2" :placeholder="t('intentRouter.rules.searchAccounts')" />
              <ul v-if="rule.search" class="mt-1 max-h-48 overflow-y-auto rounded-lg border border-gray-200 bg-white dark:border-dark-600 dark:bg-dark-800">
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
            </div>
          </div>
        </div>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import { intentRoutersAPI } from '@/api/admin/intentRouters'
import type { IntentRuleKeyLabel } from '@/api/admin/intentRouters'
import InfoTip from './InfoTip.vue'
import StatusDot from './StatusDot.vue'
import { useIntentRouter } from './context'
import { parseKeywords, platformCompatible } from './ruleUtils'
import type { RuleForm, RuleTarget, Tone } from './ruleUtils'

const { t } = useI18n()
const { form, accounts, selectedGroup, formGeneration, accountLabel, accountProblem, keyLabel, keyProblem, rememberKey, ruleTargetBroken, newRule } = useIntentRouter()

const expanded = reactive(new Set<number>())
const menuFor = ref(0)
// A fresh form (another group, a save, a discard) starts collapsed.
watch(formGeneration, () => {
  expanded.clear()
  menuFor.value = 0
})

function toggle(uid: number) {
  if (expanded.has(uid)) expanded.delete(uid)
  else expanded.add(uid)
}
function addRule() {
  const rule = newRule()
  form.rules.push(rule)
  expanded.add(rule.uid)
}
function move(index: number, delta: number) {
  const to = index + delta
  if (to < 0 || to >= form.rules.length) return
  const [rule] = form.rules.splice(index, 1)
  form.rules.splice(to, 0, rule)
  menuFor.value = 0
}
function remove(index: number) {
  const [rule] = form.rules.splice(index, 1)
  if (rule) expanded.delete(rule.uid)
  menuFor.value = 0
}

function keywordsOf(rule: RuleForm) {
  return parseKeywords(rule.keywordsText)
}
/** Which Jev outcomes this rule receives, by name, as the server resolves them. */
function roles(rule: RuleForm) {
  const name = rule.name.trim()
  if (!name) return []
  const out: string[] = []
  if (form.jev.rule === name) out.push(t('intentRouter.rules.roleProbe'))
  if (form.jev.health_enabled && form.jev.health_rule === name) out.push(t('intentRouter.rules.roleHealth'))
  if (form.jev.timely_enabled && form.jev.timely_rule === name) out.push(t('intentRouter.rules.roleTimely'))
  return out
}
function targetSummary(rule: RuleForm) {
  if (rule.target === 'key') return rule.api_key_id ? keyLabel(rule.api_key_id) : t('intentRouter.rules.noTarget')
  return rule.account_ids.length ? rule.account_ids.map(accountLabel).join('、') : t('intentRouter.rules.noTarget')
}
function ruleTone(rule: RuleForm): Tone {
  if (!rule.enabled) return 'gray'
  return ruleTargetBroken(rule) ? 'red' : 'green'
}
function ruleStateLabel(rule: RuleForm) {
  if (!rule.enabled) return t('intentRouter.rules.stateOff')
  return ruleTargetBroken(rule) ? t('intentRouter.rules.stateBroken') : t('intentRouter.rules.stateOn')
}
function segment(active: boolean) {
  return ['cursor-pointer rounded-md px-3 py-1 transition-colors', active ? 'bg-white font-medium text-gray-900 shadow-sm dark:bg-dark-600 dark:text-white' : 'text-gray-500 hover:text-gray-700 dark:text-gray-400']
}
function chip(problem: boolean) {
  return ['inline-flex items-center gap-1 rounded-full px-2.5 py-1 text-xs', problem
    ? 'bg-red-50 text-red-700 dark:bg-red-900/30 dark:text-red-300'
    : 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-gray-200']
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

// API-key targets: the rule is served by the key's group, scheduled as that
// group schedules.
const keyResults = ref<Record<number, IntentRuleKeyLabel[]>>({})
const keySearchTimers = new Map<number, ReturnType<typeof setTimeout>>()
const keySearchSeq = new Map<number, number>()
function setRuleTarget(rule: RuleForm, target: RuleTarget) {
  rule.target = target
  if (target === 'key') {
    rule.account_ids = []
    rule.search = ''
  } else {
    rule.api_key_id = 0
    rule.keySearch = ''
  }
}
function keyProblemText(problem: string) {
  return problem ? t(`intentRouter.rules.keyProblems.${problem}`) : ''
}
function searchRuleKeys(rule: RuleForm) {
  const uid = rule.uid
  const prev = keySearchTimers.get(uid)
  if (prev) clearTimeout(prev)
  const seq = (keySearchSeq.get(uid) ?? 0) + 1
  keySearchSeq.set(uid, seq)
  const query = rule.keySearch.trim()
  if (!query) {
    keyResults.value = { ...keyResults.value, [uid]: [] }
    return
  }
  const groupPlatform = selectedGroup.value?.platform ?? ''
  keySearchTimers.set(uid, setTimeout(async () => {
    let found: IntentRuleKeyLabel[] = []
    try {
      found = (await intentRoutersAPI.targetKeys(query, groupPlatform && groupPlatform !== 'composite' ? groupPlatform : undefined)).data ?? []
    } catch {
      found = []
    }
    if (keySearchSeq.get(uid) !== seq) return
    keyResults.value = { ...keyResults.value, [uid]: found }
  }, 250))
}
function chooseRuleKey(rule: RuleForm, key: IntentRuleKeyLabel) {
  if (key.problem) return
  rememberKey(key)
  rule.api_key_id = key.id
  rule.keySearch = ''
  keyResults.value = { ...keyResults.value, [rule.uid]: [] }
}

function closeMenu() { menuFor.value = 0 }
onMounted(() => document.addEventListener('click', closeMenu))
onBeforeUnmount(() => {
  document.removeEventListener('click', closeMenu)
  for (const timer of keySearchTimers.values()) clearTimeout(timer)
})
</script>

<style scoped>
.icon-btn {
  @apply inline-flex h-8 w-8 items-center justify-center rounded-lg text-sm text-gray-400 hover:bg-gray-100 hover:text-gray-700 disabled:cursor-not-allowed disabled:opacity-30 disabled:hover:bg-transparent dark:hover:bg-dark-700 dark:hover:text-gray-200;
}
.menu-item {
  @apply block w-full px-3 py-1.5 text-left hover:bg-gray-50 disabled:opacity-40 dark:hover:bg-dark-700;
}
</style>
