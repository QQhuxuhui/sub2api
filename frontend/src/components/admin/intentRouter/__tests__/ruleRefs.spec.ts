import { describe, expect, it } from 'vitest'
import { newRuleRefState, resetRuleRefs, syncRuleRefs } from '../ruleRefs'
import type { NamedRule } from '../ruleRefs'

function setup(rules: NamedRule[], health_rule: string) {
  const jev = { rule: '', health_rule, timely_rule: '' }
  const state = newRuleRefState()
  resetRuleRefs(state, jev, rules)
  const step = (next: NamedRule[]) => {
    syncRuleRefs(state, jev, next)
    return next
  }
  return { jev, step }
}

describe('ruleRefs', () => {
  it('follows a rename, waiting out an emptied name', () => {
    const { jev, step } = setup([{ uid: 1, name: 'health' }], 'health')
    step([{ uid: 1, name: '' }])
    expect(jev.health_rule).toBe('health')
    step([{ uid: 1, name: 'c' }])
    step([{ uid: 1, name: 'check' }])
    expect(jev.health_rule).toBe('check')
  })

  it('gives a removed rule\'s reference to no one, however a new rule is typed', () => {
    for (const typing of [['h', 'health', 'health-eu'], ['health-eu']]) {
      const { jev, step } = setup([{ uid: 1, name: 'health' }], 'health')
      step([])
      for (const name of typing) step([{ uid: 2, name }])
      expect(jev.health_rule, typing.join('→')).toBe('health')
    }
  })

  it('keeps the admin\'s pick when the previously bound rule gets a name again', () => {
    const { jev, step } = setup([{ uid: 1, name: 'health' }, { uid: 2, name: 'coding' }], 'health')
    let rules = step([{ uid: 1, name: '' }, { uid: 2, name: 'coding' }])
    rules = step([{ uid: 1, name: '' }, { uid: 2, name: 'health' }])
    jev.health_rule = ''
    step(rules)
    jev.health_rule = 'health' // now rule 2
    step(rules)
    step([{ uid: 1, name: 'spare' }, { uid: 2, name: 'health' }])
    expect(jev.health_rule).toBe('health')
    step([{ uid: 1, name: 'spare' }, { uid: 2, name: 'health-2' }])
    expect(jev.health_rule).toBe('health-2')
  })

  it('leaves a cleared reference cleared', () => {
    const { jev, step } = setup([{ uid: 1, name: 'health' }], 'health')
    jev.health_rule = ''
    step([{ uid: 1, name: 'health' }])
    step([{ uid: 1, name: 'other' }])
    expect(jev.health_rule).toBe('')
  })

  it('tracks each role on its own', () => {
    const jev = { rule: 'coding', health_rule: 'health', timely_rule: 'health' }
    const state = newRuleRefState()
    resetRuleRefs(state, jev, [{ uid: 1, name: 'coding' }, { uid: 2, name: 'health' }])
    syncRuleRefs(state, jev, [{ uid: 1, name: 'code' }, { uid: 2, name: 'check' }])
    expect(jev).toEqual({ rule: 'code', health_rule: 'check', timely_rule: 'check' })
  })
})
