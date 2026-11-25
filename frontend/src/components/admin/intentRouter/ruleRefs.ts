import { watch } from 'vue'
import type { Ref } from 'vue'
import type { IntentJevConfig } from '@/api/admin/intentRouters'
import type { FormState } from './ruleUtils'

// Jev's target rules are stored by name. Renaming a rule in the editor must
// carry those references along — otherwise the form submits the old name and
// the server drops it (while Jev is off) or rejects the save (while it is on).
//
// Each reference is bound to one rule by uid, never guessed from names:
// - the form is (re)filled, or the admin picks a rule: bind to the rule that
//   carries the picked name right then (none for '' or an unknown name);
// - the bound rule is renamed: the reference follows, except while the name
//   is empty mid-edit;
// - the bound rule is removed: unbind and leave the reference as it is.

export const ruleRefRoles = ['rule', 'health_rule', 'timely_rule'] as const
export type RuleRefRole = (typeof ruleRefRoles)[number]

export interface NamedRule {
  uid: number
  name: string
}

export interface RuleRefState {
  /** The rule each reference is bound to (absent: none). */
  bound: Map<RuleRefRole, number>
  /** The reference value last seen, to tell an admin's pick from a rename. */
  seen: Map<RuleRefRole, string>
}

export function newRuleRefState(): RuleRefState {
  return { bound: new Map(), seen: new Map() }
}

// A name two rules share mid-edit binds to neither: the picker passes names
// only, so which one was picked is unknown. The reference keeps the name.
function bindByName(state: RuleRefState, role: RuleRefRole, ref: string, rules: NamedRule[]): void {
  const named = ref ? rules.filter((r) => r.name === ref) : []
  const rule = named.length === 1 ? named[0] : undefined
  if (rule) state.bound.set(role, rule.uid)
  else state.bound.delete(role)
  state.seen.set(role, ref)
}

/** Binds every reference afresh, as after loading a group. */
export function resetRuleRefs(state: RuleRefState, jev: Pick<IntentJevConfig, RuleRefRole>, rules: NamedRule[]): void {
  for (const role of ruleRefRoles) bindByName(state, role, jev[role], rules)
}

/** Applies one change of the rule list or of the references. */
export function syncRuleRefs(state: RuleRefState, jev: Pick<IntentJevConfig, RuleRefRole>, rules: NamedRule[]): void {
  for (const role of ruleRefRoles) {
    const ref = jev[role]
    if (ref !== state.seen.get(role)) {
      bindByName(state, role, ref, rules)
      continue
    }
    const uid = state.bound.get(role)
    if (uid === undefined) continue
    const rule = rules.find((r) => r.uid === uid)
    if (!rule) {
      state.bound.delete(role)
      continue
    }
    if (rule.name !== '' && rule.name !== ref) {
      state.seen.set(role, rule.name)
      jev[role] = rule.name
    }
  }
}

export function useRuleRefsFollowRenames(form: FormState, formGeneration: Ref<number>): void {
  const state = newRuleRefState()
  const names = () => form.rules.map((r) => ({ uid: r.uid, name: r.name.trim() }))
  resetRuleRefs(state, form.jev, names())
  watch(formGeneration, () => resetRuleRefs(state, form.jev, names()), { flush: 'sync' })
  watch(
    () => [names(), form.jev.rule, form.jev.health_rule, form.jev.timely_rule] as const,
    ([rules]) => syncRuleRefs(state, form.jev, rules),
    { flush: 'sync' },
  )
}
