<template>
  <div class="space-y-5" data-test="intent-jev">
    <!-- On/off and live state -->
    <div class="flex flex-wrap items-center justify-between gap-3">
      <label class="flex items-center gap-3">
        <input v-model="form.jev.enabled" data-test="intent-jev-enabled" type="checkbox" class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
        <span class="text-base font-semibold text-gray-900 dark:text-white">{{ t('intentRouter.jev.title') }}</span>
        <InfoTip :text="t('intentRouter.jev.intro')" :label="t('intentRouter.jev.title')" />
      </label>
    </div>

    <div v-if="jevStatus" data-test="intent-jev-status" class="space-y-4 rounded-xl border border-gray-200 p-4 dark:border-dark-600">
      <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
        <StatusDot :tone="stateTone" :label="t(`intentRouter.jev.status.${jevStatus.state}`)" />
        <span class="font-medium text-gray-900 dark:text-white">{{ t(`intentRouter.jev.status.${jevStatus.state}`) }}</span>
        <span v-if="jevStatus.state === 'normal' && jevStatus.last_success_at" class="text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.jev.status.lastSuccess', { time: formatTime(jevStatus.last_success_at) }) }}</span>
        <span v-if="jevStatus.state === 'paused'" class="text-xs text-amber-700 dark:text-amber-300">{{ t('intentRouter.jev.status.pausedUntil', { time: formatTime(jevStatus.paused_until ?? ''), reason: jevStatus.reason ?? '' }) }}</span>
        <button type="button" class="ml-auto text-xs text-primary-600 hover:underline dark:text-primary-400" @click="loadJevStatus">{{ t('intentRouter.jev.status.refresh') }}</button>
      </div>
      <p v-if="jevStatus.state === 'auto_disabled'" data-test="intent-jev-auto-disabled" class="rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">
        {{ t('intentRouter.jev.status.autoDisabledAt', { time: jevStatus.auto_disabled_at ? formatTime(jevStatus.auto_disabled_at) : '—', status: jevStatus.auto_disabled_status ?? '—' }) }}
        {{ autoDisabledHint(jevStatus.auto_disabled_status) }}
        <span class="block text-xs opacity-80">{{ jevStatus.auto_disabled_detail || jevStatus.reason }}</span>
      </p>

      <div class="grid gap-4 sm:grid-cols-2">
        <!-- Balance -->
        <div>
          <p class="mb-1.5 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.jev.status.balanceTitle') }}</p>
          <template v-if="jevStatus.balance">
            <div data-test="intent-jev-balance-estimate" :title="t('intentRouter.jev.status.balance', { estimated: formatUSD(jevStatus.balance.estimated_usd), set: formatUSD(jevStatus.balance.set_usd), at: formatTime(jevStatus.balance.set_at), spent: formatUSD(jevStatus.balance.spent_usd) })">
              <div class="flex items-baseline justify-between gap-2">
                <span :class="['text-lg font-semibold tabular-nums', jevStatus.balance.low ? 'text-red-600 dark:text-red-400' : 'text-gray-900 dark:text-white']">{{ formatUSD(jevStatus.balance.estimated_usd) }}</span>
                <span class="text-xs tabular-nums text-gray-500 dark:text-gray-400">/ {{ formatUSD(jevStatus.balance.set_usd) }}</span>
              </div>
              <div class="mt-1 h-1.5 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700">
                <div :class="['h-full rounded-full', jevStatus.balance.low ? 'bg-red-500' : 'bg-green-500']" :style="{ width: `${balancePercent}%` }"></div>
              </div>
              <p v-if="jevStatus.balance.low" class="mt-1 text-xs text-red-600 dark:text-red-400">{{ t('intentRouter.jev.status.lowBalance') }}</p>
            </div>
          </template>
          <p v-else class="text-sm text-gray-400">{{ t('intentRouter.jev.status.noBalance') }}</p>
        </div>
        <!-- Calls per day -->
        <div>
          <p class="mb-1.5 flex items-center justify-between text-xs text-gray-500 dark:text-gray-400">
            <span>{{ t('intentRouter.jev.status.callsChart') }}</span>
            <span class="tabular-nums">{{ t('intentRouter.jev.status.today', { n: jevStatus.days[0]?.calls ?? 0 }) }}</span>
          </p>
          <MiniBars :bars="bars" :label="t('intentRouter.jev.status.callsChart')" />
        </div>
      </div>

      <details class="group text-xs">
        <summary class="cursor-pointer select-none text-gray-500 hover:text-gray-700 dark:text-gray-400">{{ t('intentRouter.details') }}</summary>
        <div class="mt-2 overflow-x-auto">
          <table class="min-w-full">
            <thead class="text-left text-gray-500 dark:text-gray-400">
              <tr>
                <th class="py-1 pr-4">{{ t('intentRouter.jev.status.day') }}</th>
                <th class="py-1 pr-4">{{ t('intentRouter.jev.status.calls') }}</th>
                <th class="py-1 pr-4">{{ t('intentRouter.jev.status.skipped') }}</th>
                <th class="py-1 pr-4">{{ t('intentRouter.jev.status.probes') }}</th>
                <th class="py-1 pr-4">{{ t('intentRouter.jev.status.health') }}</th>
                <th class="py-1 pr-4" :title="t('intentRouter.jev.status.timelyTitle')">{{ t('intentRouter.jev.status.timely') }}</th>
                <th class="py-1 pr-4">{{ t('intentRouter.jev.status.errors') }}</th>
                <th class="py-1 pr-4">{{ t('intentRouter.jev.status.tokens') }}</th>
                <th class="py-1">{{ t('intentRouter.jev.status.cost') }}</th>
              </tr>
            </thead>
            <tbody class="text-gray-700 dark:text-gray-300">
              <tr v-for="day in jevStatus.days" :key="day.date" :data-test="`intent-jev-day-${day.date}`">
                <td class="py-1 pr-4">{{ formatDay(day.date) }}</td>
                <td class="py-1 pr-4">{{ day.calls }}</td>
                <td class="py-1 pr-4">{{ day.skipped }}</td>
                <td class="py-1 pr-4">{{ day.probes }}</td>
                <td class="py-1 pr-4">{{ day.health ?? 0 }}</td>
                <td class="py-1 pr-4">{{ day.timely_routed ?? 0 }} / {{ day.timely_hits ?? 0 }} / {{ day.timely_answered ?? 0 }}</td>
                <td class="py-1 pr-4">{{ day.errors }}</td>
                <td class="py-1 pr-4">{{ day.tokens }}</td>
                <td class="py-1">{{ formatUSD(day.cost_usd) }} / {{ formatCNY(day.cost_usd) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </details>
    </div>

    <!-- Where probes go, and the key -->
    <div class="grid gap-4 md:grid-cols-2">
      <div>
        <label class="input-label">{{ t('intentRouter.jev.rule') }}</label>
        <select v-model="form.jev.rule" data-test="intent-jev-rule" class="input">
          <option value="">{{ t('intentRouter.jev.rulePlaceholder') }}</option>
          <option v-for="name in ruleNames" :key="name" :value="name">{{ name }}</option>
        </select>
        <p v-if="ruleTargetText" data-test="intent-jev-rule-accounts" class="mt-1 truncate text-xs text-gray-600 dark:text-gray-300" :title="ruleTargetText">→ {{ ruleTargetText }}</p>
      </div>
      <div>
        <label class="input-label">{{ t('intentRouter.jev.apiKey') }}</label>
        <input
          v-model.trim="form.jev_api_key"
          data-test="intent-jev-api-key"
          type="password"
          autocomplete="new-password"
          class="input"
          :placeholder="keyConfigured ? t('intentRouter.classifier.apiKeyKeep') : t('intentRouter.jev.apiKeyPlaceholder')"
        />
      </div>
    </div>

    <!-- Scope -->
    <div>
      <label class="input-label">{{ t('intentRouter.jev.scope') }}</label>
      <div class="flex flex-wrap gap-4 text-sm text-gray-700 dark:text-gray-300">
        <label class="flex items-center gap-2"><input v-model="form.jev.scope" data-test="intent-jev-scope-all" type="radio" value="all" />{{ t('intentRouter.jev.scopeAll') }}</label>
        <label class="flex items-center gap-2"><input v-model="form.jev.scope" data-test="intent-jev-scope-listed" type="radio" value="listed" />{{ t('intentRouter.jev.scopeListed') }}</label>
      </div>
      <ScopePicker v-if="form.jev.scope === 'listed'" v-model:users="form.jevUsers" v-model:keys="form.jevKeys" test-prefix="intent-jev" class="mt-3" />
    </div>

    <!-- Extra questions in the same call -->
    <div class="grid gap-3 lg:grid-cols-2">
      <div class="space-y-3 rounded-xl border border-gray-200 p-3 dark:border-dark-600" data-test="intent-jev-health">
        <label class="flex items-center gap-2.5">
          <input v-model="form.jev.health_enabled" data-test="intent-jev-health-enabled" type="checkbox" class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
          <span class="text-sm font-medium text-gray-900 dark:text-white">{{ t('intentRouter.jev.health.enabled') }}</span>
          <InfoTip :text="t('intentRouter.jev.health.enabledHint')" :label="t('intentRouter.jev.health.enabled')" />
        </label>
        <div v-if="form.jev.health_enabled" class="grid grid-cols-[7rem_minmax(0,1fr)] gap-3">
          <div>
            <label class="input-label flex items-center gap-1">{{ t('intentRouter.jev.health.threshold') }} <InfoTip :text="t('intentRouter.jev.health.thresholdHint')" :label="t('intentRouter.jev.health.threshold')" /></label>
            <input v-model.number="form.jev.health_threshold" data-test="intent-jev-health-threshold" type="number" min="0.01" max="1" step="0.05" class="input" />
          </div>
          <div>
            <label class="input-label">{{ t('intentRouter.jev.health.rule') }}</label>
            <select v-model="form.jev.health_rule" data-test="intent-jev-health-rule" class="input">
              <option value="">{{ t('intentRouter.jev.health.ruleNone') }}</option>
              <option v-for="name in ruleNames" :key="name" :value="name">{{ name }}</option>
            </select>
          </div>
        </div>
      </div>
      <div class="space-y-3 rounded-xl border border-gray-200 p-3 dark:border-dark-600" data-test="intent-jev-timely">
        <label class="flex items-center gap-2.5">
          <input v-model="form.jev.timely_enabled" data-test="intent-jev-timely-enabled" type="checkbox" class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
          <span class="text-sm font-medium text-gray-900 dark:text-white">{{ t('intentRouter.jev.timely.enabled') }}</span>
          <InfoTip :text="t('intentRouter.jev.timely.enabledHint')" :label="t('intentRouter.jev.timely.enabled')" />
        </label>
        <div v-if="form.jev.timely_enabled" class="grid grid-cols-[7rem_minmax(0,1fr)] gap-3">
          <div>
            <label class="input-label flex items-center gap-1">{{ t('intentRouter.jev.timely.threshold') }} <InfoTip :text="t('intentRouter.jev.timely.thresholdHint')" :label="t('intentRouter.jev.timely.threshold')" /></label>
            <input v-model.number="form.jev.timely_threshold" data-test="intent-jev-timely-threshold" type="number" min="0.01" max="1" step="0.05" class="input" />
          </div>
          <div>
            <label class="input-label">{{ t('intentRouter.jev.timely.rule') }}</label>
            <select v-model="form.jev.timely_rule" data-test="intent-jev-timely-rule" class="input">
              <option value="">{{ t('intentRouter.jev.timely.ruleNone') }}</option>
              <option v-for="name in ruleNames" :key="name" :value="name">{{ name }}</option>
            </select>
          </div>
        </div>
      </div>
    </div>

    <!-- Balance bookkeeping -->
    <div class="grid gap-4 sm:grid-cols-2">
      <div>
        <label class="input-label flex items-center gap-1">{{ t('intentRouter.jev.setBalance') }} <InfoTip :text="t('intentRouter.jev.setBalanceHint')" :label="t('intentRouter.jev.setBalance')" /></label>
        <input v-model="form.jevBalanceText" data-test="intent-jev-balance" type="number" min="0" step="0.01" class="input" :placeholder="t('intentRouter.jev.setBalancePlaceholder')" />
        <a href="https://console.typesafe.ai" target="_blank" rel="noopener noreferrer" class="mt-1 inline-block text-xs text-primary-600 hover:underline dark:text-primary-400">console.typesafe.ai ↗</a>
      </div>
      <div>
        <label class="input-label flex items-center gap-1">{{ t('intentRouter.jev.lowBalance') }} <InfoTip :text="t('intentRouter.jev.lowBalanceHint')" :label="t('intentRouter.jev.lowBalance')" /></label>
        <input v-model.number="form.jev.low_balance_usd" data-test="intent-jev-low-balance" type="number" min="0" step="0.5" class="input" />
      </div>
    </div>

    <!-- Rarely changed -->
    <details data-test="intent-jev-advanced" class="rounded-xl border border-gray-200 dark:border-dark-600">
      <summary class="cursor-pointer select-none px-4 py-2.5 text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('intentRouter.jev.advanced') }}</summary>
      <div class="grid gap-4 border-t border-gray-100 p-4 sm:grid-cols-2 lg:grid-cols-3 dark:border-dark-700">
        <div>
          <label class="input-label flex items-center gap-1">{{ t('intentRouter.jev.threshold') }} <InfoTip :text="t('intentRouter.jev.thresholdHint')" :label="t('intentRouter.jev.threshold')" /></label>
          <input v-model.number="form.jev.threshold" data-test="intent-jev-threshold" type="number" min="0.05" max="1" step="0.05" class="input" />
        </div>
        <div>
          <label class="input-label flex items-center gap-1">{{ t('intentRouter.jev.maxTurns') }} <InfoTip :text="t('intentRouter.jev.maxTurnsHint')" :label="t('intentRouter.jev.maxTurns')" /></label>
          <input v-model.number="form.jev.max_turns" type="number" min="1" max="50" class="input" />
        </div>
        <div>
          <label class="input-label">{{ t('intentRouter.jev.timeout') }}</label>
          <input v-model.number="form.jev.timeout_ms" type="number" min="200" max="5000" step="100" class="input" />
        </div>
        <div>
          <label class="input-label flex items-center gap-1">{{ t('intentRouter.jev.samplePercent') }} <InfoTip :text="t('intentRouter.jev.samplePercentHint')" :label="t('intentRouter.jev.samplePercent')" /></label>
          <input v-model.number="form.jev.sample_percent" data-test="intent-jev-sample" type="number" min="1" max="100" class="input" />
        </div>
        <div>
          <label class="input-label">{{ t('intentRouter.jev.model') }}</label>
          <input v-model.trim="form.jev.model" type="text" class="input" placeholder="jev-latest" />
        </div>
      </div>
    </details>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { IntentJevStatus } from '@/api/admin/intentRouters'
import InfoTip from './InfoTip.vue'
import MiniBars from './MiniBars.vue'
import ScopePicker from './ScopePicker.vue'
import StatusDot from './StatusDot.vue'
import { useIntentRouter } from './context'
import type { Tone } from './ruleUtils'

const { t } = useI18n()
const { form, savedRouter, jevStatus, accountLabel, keyLabel, loadJevStatus } = useIntentRouter()

const keyConfigured = computed(() => savedRouter.value?.jev_api_key_configured === true)
const ruleNames = computed(() => form.rules.map((rule) => rule.name.trim()).filter(Boolean))
const ruleTargetText = computed(() => {
  const rule = form.rules.find((r) => r.name.trim() === form.jev.rule)
  if (!rule) return ''
  if (rule.target === 'key') return rule.api_key_id ? keyLabel(rule.api_key_id) : ''
  return rule.account_ids.map(accountLabel).join('、')
})

const stateTone = computed((): Tone => {
  const state: IntentJevStatus['state'] | undefined = jevStatus.value?.state
  if (state === 'normal') return 'green'
  if (state === 'paused') return 'amber'
  if (state === 'auto_disabled') return 'red'
  return 'gray'
})
const balancePercent = computed(() => {
  const b = jevStatus.value?.balance
  if (!b || b.set_usd <= 0) return 0
  return Math.max(0, Math.min(100, (b.estimated_usd / b.set_usd) * 100))
})
// Oldest on the left; the server sends the newest first.
const bars = computed(() => (jevStatus.value?.days ?? []).slice(0, 7).reverse().map((day) => ({
  key: day.date,
  value: day.calls,
  title: t('intentRouter.jev.status.barTitle', { day: formatDay(day.date), calls: day.calls, probes: day.probes, errors: day.errors, cost: formatUSD(day.cost_usd) }),
})))

function formatUSD(value: number) { return `$${value.toFixed(value !== 0 && Math.abs(value) < 0.01 ? 4 : 2)}` }
function formatCNY(usd: number) { return `¥${(usd * (jevStatus.value?.cny_per_usd ?? 7)).toFixed(usd !== 0 && Math.abs(usd) < 0.01 ? 4 : 2)}` }
function formatDay(day: string) { return `${day.slice(4, 6)}-${day.slice(6, 8)}` }
function formatTime(value: string) {
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString()
}
function autoDisabledHint(status?: number) {
  switch (status) {
    case 401: return t('intentRouter.jev.status.why401')
    case 402: return t('intentRouter.jev.status.why402')
    case 403: return t('intentRouter.jev.status.why403')
    case 451: return t('intentRouter.jev.status.why451')
    default: return ''
  }
}
</script>
