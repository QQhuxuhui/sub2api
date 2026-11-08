<template>
  <div data-test="intent-flow" class="rounded-xl bg-gray-50 px-4 py-3 dark:bg-dark-800/60">
    <ol class="flex flex-wrap items-start gap-x-2 gap-y-3 text-sm">
      <template v-for="(node, i) in nodes" :key="node.key">
        <li :data-test="`intent-flow-${node.key}`" class="min-w-0">
          <div class="flex items-center gap-1.5 font-medium text-gray-900 dark:text-white">
            <span v-if="i > 0" class="mr-0.5 font-normal text-gray-300 dark:text-dark-500" aria-hidden="true">→</span>
            <StatusDot v-if="node.state" :tone="node.tone" :label="node.state" />
            <span>{{ node.title }}</span>
            <span class="text-xs font-normal text-gray-500 dark:text-gray-400">{{ node.state }}</span>
          </div>
          <ul v-if="node.branches.length" :class="['mt-0.5 space-y-0.5 text-xs text-gray-500 dark:text-gray-400', i > 0 ? 'pl-7' : 'pl-3.5']">
            <li v-for="b in node.branches" :key="b.text" :class="b.muted ? 'text-gray-300 line-through dark:text-dark-500' : ''">{{ b.text }}</li>
          </ul>
        </li>
      </template>
    </ol>
    <p class="mt-2 border-t border-gray-200 pt-2 text-xs text-gray-500 dark:border-dark-700 dark:text-gray-400">
      {{ t('intentRouter.flow.unavailable', { status: form.unavailable_status || 503 }) }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import StatusDot from './StatusDot.vue'
import { useIntentRouter } from './context'
import { isActiveRule, parseKeywords } from './ruleUtils'
import type { RuleForm, Tone } from './ruleUtils'

interface FlowNode { key: string; title: string; state: string; tone: Tone; branches: { text: string; muted?: boolean }[] }

const { t } = useI18n()
const { form, savedRouter, jevStatus } = useIntentRouter()

function active(rule: RuleForm) {
  return isActiveRule({ enabled: rule.enabled, name: rule.name, account_ids: rule.target === 'accounts' ? rule.account_ids : [], api_key_id: rule.target === 'key' ? rule.api_key_id : 0 })
}
function activeRuleNamed(name: string) {
  const wanted = name.trim()
  return !!wanted && form.rules.some((r) => r.name.trim() === wanted && active(r))
}

const keywordRules = computed(() => form.rules.filter((r) => active(r) && parseKeywords(r.keywordsText).length > 0).length)
const probeTargetReady = computed(() => activeRuleNamed(form.jev.rule))

// Mirrors jevRule() on the server, with the live state on top.
const jevNode = computed((): Pick<FlowNode, 'tone' | 'state'> => {
  const savedOn = savedRouter.value?.jev?.enabled === true
  if (jevStatus.value?.state === 'auto_disabled' && !form.jev.enabled) return { tone: 'red', state: t('intentRouter.flow.jev.autoDisabled') }
  if (savedRouter.value && form.jev.enabled !== savedOn) return { tone: 'amber', state: t('intentRouter.flow.jev.pending') }
  if (!form.jev.enabled) return { tone: 'gray', state: t('intentRouter.flow.off') }
  const hasKey = savedRouter.value?.jev_api_key_configured === true || form.jev_api_key.trim() !== ''
  if (!hasKey || !probeTargetReady.value) return { tone: 'amber', state: t('intentRouter.flow.jev.incomplete') }
  if (jevStatus.value?.state === 'paused') return { tone: 'amber', state: t('intentRouter.flow.jev.paused') }
  return { tone: 'green', state: t('intentRouter.flow.on') }
})

// classifierRules() on the server: a model, a stored key, and an active rule with a description.
const classifierOn = computed(() => form.classifier_model.trim() !== '' && savedRouter.value?.classifier_api_key_configured === true &&
  form.rules.some((r) => active(r) && r.description.trim() !== ''))

const nodes = computed((): FlowNode[] => {
  const toTarget = t('intentRouter.flow.toTarget')
  const list: FlowNode[] = [
    {
      key: 'keyword', title: t('intentRouter.flow.keyword'),
      tone: keywordRules.value > 0 ? 'green' : 'gray',
      state: keywordRules.value > 0 ? t('intentRouter.flow.ruleCount', { n: keywordRules.value }) : t('intentRouter.flow.none'),
      branches: [{ text: toTarget }],
    },
    {
      key: 'template', title: t('intentRouter.flow.template'), tone: 'green', state: t('intentRouter.flow.on'),
      branches: [{ text: t('intentRouter.flow.probeToTarget'), muted: !probeTargetReady.value }, { text: t('intentRouter.flow.passToOrdinary') }],
    },
    {
      key: 'jev', title: 'Jev', ...jevNode.value,
      branches: form.jev.enabled ? [{ text: t('intentRouter.flow.probeToTarget'), muted: !probeTargetReady.value }, { text: t('intentRouter.flow.healthBranch') }] : [],
    },
  ]
  if (classifierOn.value) list.push({ key: 'classifier', title: t('intentRouter.flow.classifier'), tone: 'green', state: t('intentRouter.flow.on'), branches: [{ text: toTarget }] })
  list.push({ key: 'ordinary', title: t('intentRouter.flow.ordinary'), tone: 'gray', state: '', branches: [] })
  return list
})
</script>
