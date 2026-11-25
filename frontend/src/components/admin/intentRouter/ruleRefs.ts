import { watch } from 'vue'
import type { IntentJevConfig } from '@/api/admin/intentRouters'
import type { FormState } from './ruleUtils'

// Jev's target rules are stored by name. Renaming a rule in the editor must
// carry those references along — otherwise the form submits the old name and
// the server drops it (while Jev is off) or rejects the save (while it is on).

export const ruleRefRoles = ['rule', 'health_rule', 'timely_rule'] as const
export type RuleRefRole = (typeof ruleRefRoles)[number]

export interface NamedRule {
  uid: number
  name: string
}

// Which rule (by uid) each reference points at, and the name it was last seen
// with: a reference the admin re-picks no longer matches it and is re-bound.
export type RuleRefBindings = Map<RuleRefRole, NamedRule>

// followRuleRenames updates jev's references for the rule-name change from
// before to now. A rule whose name is emptied mid-edit keeps the reference
// until it gets a name again.
export function followRuleRenames(jev: Pick<IntentJevConfig, RuleRefRole>, before: NamedRule[], now: NamedRule[], bound: RuleRefBindings): void {
  for (const role of ruleRefRoles) {
    const ref = jev[role]
    let binding = bound.get(role)
    if (binding && binding.name === ref && !now.some((r) => r.uid === binding!.uid)) {
      // The bound rule is gone from the list. A rule that now carries the
      // name takes the reference (another group was loaded); otherwise keep
      // the binding: a move splices the rule out and back in.
      const named = ref ? now.find((r) => r.name === ref) : undefined
      if (named) bound.set(role, { uid: named.uid, name: ref })
      continue
    }
    if (!binding || binding.name !== ref) {
      const source = ref ? (before.find((r) => r.name === ref) ?? now.find((r) => r.name === ref)) : undefined
      if (!source) {
        bound.delete(role)
        continue
      }
      binding = { uid: source.uid, name: ref }
      bound.set(role, binding)
    }
    const current = now.find((r) => r.uid === binding!.uid)
    if (current && current.name !== '' && current.name !== ref) {
      jev[role] = current.name
      binding.name = current.name
    }
  }
}

export function useRuleRefsFollowRenames(form: FormState): void {
  const bound: RuleRefBindings = new Map()
  watch(
    () => form.rules.map((r) => ({ uid: r.uid, name: r.name.trim() })),
    (now, before) => followRuleRenames(form.jev, before ?? [], now, bound),
    { flush: 'sync' },
  )
}
