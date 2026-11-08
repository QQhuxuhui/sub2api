import type { IntentRouter, IntentRouterInput } from '@/api/admin/intentRouters'

export interface GroupOption { id: number; name: string; platform: string }
export interface AccountOption { id: number; name: string; platform: string }
export type RuleTarget = 'accounts' | 'key'
export interface RuleForm {
  uid: number
  name: string
  description: string
  keywordsText: string
  originalKeywords: string[]
  target: RuleTarget
  account_ids: number[]
  api_key_id: number
  enabled: boolean
  search: string
  keySearch: string
}
export interface JevUserForm { id: number; email: string }
export interface JevKeyForm { id: number; name: string; user_id: number }
export type FormState = Omit<IntentRouterInput, 'rules' | 'jev_set_balance_usd' | 'excluded_models'> & {
  excludedModelsText: string
  rules: RuleForm[]
  jevUsers: JevUserForm[]
  jevKeys: JevKeyForm[]
  jevBalanceText: string
}

export type Tone = 'green' | 'amber' | 'red' | 'gray'

// Keywords are typed one per line or comma-separated (ASCII or Chinese commas).
export function parseKeywords(text: string): string[] {
  const result: string[] = []
  let current = ''
  let quoted = false
  const finish = () => {
    const keyword = current.trim()
    if (keyword) result.push(keyword)
    current = ''
  }
  for (let i = 0; i < text.length; i++) {
    const char = text[i]
    if (quoted) {
      if (char === '"' && text[i + 1] === '"') {
        current += '"'
        i++
      } else if (char === '"') {
        quoted = false
      } else {
        current += char
      }
    } else if (char === '"' && current.trim() === '') {
      quoted = true
    } else if (/[\n,，、]/.test(char)) {
      finish()
    } else {
      current += char
    }
  }
  finish()
  return result
}

export function formatKeywords(keywords: string[]): string {
  return keywords.map((keyword) => /[\r\n,，、"]/.test(keyword)
    ? `"${keyword.replace(/\r/g, '\\r').replace(/"/g, '""')}"`
    : keyword).join(', ')
}

export function restoreOriginalControls(parsed: string[], originals: string[]): string[] {
  const controls = new Map(originals.filter((keyword) => keyword.includes('\r'))
    .map((keyword) => [keyword.replace(/\r/g, '\\r'), keyword]))
  return parsed.map((keyword) => controls.get(keyword) ?? keyword)
}

// Mirrors the server's rule: same platform, or Antigravity for Claude/Gemini groups.
export function platformCompatible(groupPlatform: string, accountPlatform: string) {
  if (groupPlatform === 'anthropic' || groupPlatform === 'gemini') return accountPlatform === groupPlatform || accountPlatform === 'antigravity'
  if (groupPlatform === 'openai' || groupPlatform === 'antigravity') return accountPlatform === groupPlatform
  return true
}

/** A key's group must serve the router group's platform; composite groups take anything. */
export function keyPlatformCompatible(groupPlatform: string, keyGroupPlatform: string) {
  return !groupPlatform || groupPlatform === 'composite' || keyGroupPlatform === groupPlatform
}

/** The server's activeRules(): enabled, named, and with a target. */
export function isActiveRule(rule: { enabled: boolean; name: string; account_ids?: number[]; api_key_id?: number }) {
  return rule.enabled && rule.name.trim() !== '' && ((rule.api_key_id ?? 0) > 0 || (rule.account_ids ?? []).length > 0)
}

/**
 * Whether an enabled router has an active rule whose target cannot serve:
 * a key with a problem or of another platform, an account that is gone or of
 * another platform.
 */
export function routerHasTargetProblem(router: IntentRouter, groupPlatform: string, accounts: Map<number, AccountOption>) {
  if (!router.enabled) return false
  const keys = new Map((router.rule_keys ?? []).map((k) => [k.id, k]))
  return (router.rules ?? []).some((rule) => {
    if (!isActiveRule(rule)) return false
    if (rule.api_key_id) {
      const key = keys.get(rule.api_key_id)
      return !key || key.problem !== '' || !keyPlatformCompatible(groupPlatform, key.group_platform)
    }
    return (rule.account_ids ?? []).some((id) => {
      const account = accounts.get(id)
      return !account || !platformCompatible(groupPlatform, account.platform)
    })
  })
}
