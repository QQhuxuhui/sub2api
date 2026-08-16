import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'

function flattenKeys(obj: Record<string, any>, prefix = ''): string[] {
  const keys: string[] = []
  for (const [k, v] of Object.entries(obj)) {
    const fullKey = prefix ? `${prefix}.${k}` : k
    if (typeof v === 'object' && v !== null && !Array.isArray(v)) {
      keys.push(...flattenKeys(v, fullKey))
    } else {
      keys.push(fullKey)
    }
  }
  return keys
}

// 本仓现有的 8 个 i18n spec 里**没有任何 zh/en 键树对齐测试**（只有
// localesNoKeyCollision 与 localesMessageCompile）。漏了 en 侧不会被 CI 抓到，
// 只会让英文界面把 key 本身渲染出来（"team.totalCost" 直接显示在页面上）。
describe('team locale keys', () => {
  const zhKeys = flattenKeys((zh as Record<string, any>).team ?? {}, 'team')
  const enKeys = flattenKeys((en as Record<string, any>).team ?? {}, 'team')

  it('zh 与 en 的 team 键树完全一致', () => {
    expect(zhKeys.length).toBeGreaterThan(20)
    expect([...enKeys].sort()).toEqual([...zhKeys].sort())
  })

  it('nav.team 两侧都在（菜单标签缺了会渲染成裸 key）', () => {
    expect((zh as any).nav?.team).toBeTruthy()
    expect((en as any).nav?.team).toBeTruthy()
  })

  it('没有空文案', () => {
    for (const [loc, tree] of [['zh', zh], ['en', en]] as const) {
      for (const key of flattenKeys((tree as Record<string, any>).team ?? {}, 'team')) {
        const value = key.split('.').reduce<any>((acc, k) => acc?.[k], tree)
        expect(typeof value === 'string' && value.trim().length > 0, `${loc} 的 ${key} 是空文案`).toBe(true)
      }
    }
  })

  // 带占位符的文案两侧必须用同一组变量名 —— 少一个 vue-i18n 不会报错，
  // 只会把那段插值渲染成空白，页面上出现「 /  已设」这种断句。
  it('两侧的插值变量名一致', () => {
    const vars = (s: string) => (s.match(/\{(\w+)\}/g) ?? []).sort().join(',')
    for (const key of zhKeys) {
      const z = key.split('.').reduce<any>((acc, k) => acc?.[k], zh)
      const e = key.split('.').reduce<any>((acc, k) => acc?.[k], en)
      if (typeof z !== 'string' || typeof e !== 'string') continue
      expect(vars(e), `${key} 的插值变量与中文不一致`).toBe(vars(z))
    }
  })
})
