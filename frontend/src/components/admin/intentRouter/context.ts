import { inject, provide } from 'vue'
import type { ComputedRef, InjectionKey, Ref } from 'vue'
import type { IntentJevStatus, IntentRouter, IntentRuleKeyLabel } from '@/api/admin/intentRouters'
import type { AccountOption, FormState, GroupOption, RuleForm } from './ruleUtils'

/**
 * What the intent-router page shares with its panels. The page owns the form
 * and everything that saves it; panels edit the form in place and read the
 * labels they need from here.
 */
export interface IntentRouterContext {
  form: FormState
  accounts: Ref<AccountOption[]>
  selectedGroup: ComputedRef<GroupOption | null>
  savedRouter: ComputedRef<IntentRouter | null>
  jevStatus: Ref<IntentJevStatus | null>
  /** The main form differs from what was saved. */
  dirty: ComputedRef<boolean>
  /** Bumped every time the form is (re)filled: a new group, a save, a discard. */
  formGeneration: Ref<number>
  accountLabel: (id: number) => string
  /** Why an account cannot serve this group ('' when it can, or it is unknown). */
  accountProblem: (id: number) => string
  keyLabel: (id: number) => string
  keyProblem: (id: number) => string
  rememberKey: (key: IntentRuleKeyLabel) => void
  /** A rule's target cannot serve right now. */
  ruleTargetBroken: (rule: RuleForm) => boolean
  newRule: () => RuleForm
  loadJevStatus: () => void
}

const key: InjectionKey<IntentRouterContext> = Symbol('intentRouter')

export function provideIntentRouter(ctx: IntentRouterContext) {
  provide(key, ctx)
}

export function useIntentRouter(): IntentRouterContext {
  const ctx = inject(key)
  if (!ctx) throw new Error('intent router context missing')
  return ctx
}
