import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import PlazaModelPricingTable from '@/components/modelPlaza/PlazaModelPricingTable.vue'
import type { ModelPlazaGroup, PlazaModel } from '@/api/modelPlaza'
import { summarizePrice } from '../plazaBoard'
import { group, requestModel, tokenModel } from './fixtures'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

/**
 * 看板的摘要价是现有定价表（PlazaModelPricingTable.vue）取价与倍率规则的等价实现。
 * 这里用同一批数据渲染真实的定价表，断言摘要价文本就出现在表格里；
 * 上游改了定价表的倍率或格式规则而摘要没跟上时，这个测试会失败。
 */
function tableText(model: PlazaModel, g: ModelPlazaGroup): string {
  return mount(PlazaModelPricingTable, {
    props: {
      models: [model],
      platform: g.platform,
      rateMultiplier: g.rate_multiplier,
      userRateMultiplier: g.user_rate_multiplier ?? null,
      imageRateIndependent: g.image_rate_independent,
      imageRateMultiplier: g.image_rate_multiplier,
      videoRateIndependent: g.video_rate_independent,
      videoRateMultiplier: g.video_rate_multiplier
    }
  }).text()
}

describe('摘要价与现有定价表一致', () => {
  const cases: Array<{ name: string; model: PlazaModel; group: ModelPlazaGroup }> = [
    { name: '按量 · 分组倍率', model: tokenModel('claude-a', 'anthropic'), group: group(1, 'g', 'anthropic', [], { rate_multiplier: 0.45 }) },
    {
      name: '按量 · 用户专属倍率',
      model: tokenModel('claude-b', 'anthropic'),
      group: group(1, 'g', 'anthropic', [], { rate_multiplier: 1.5, user_rate_multiplier: 0.37 })
    },
    {
      name: '按量 · 阶梯',
      model: (() => {
        const m = tokenModel('gpt-ladder', 'openai')
        m.pricing!.intervals = [
          { min_tokens: 0, max_tokens: 272000, input_price: 2.5e-6, output_price: 1e-5, cache_write_price: null, cache_read_price: 2.5e-7, per_request_price: null },
          { min_tokens: 272000, max_tokens: null, input_price: 5e-6, output_price: 2.25e-5, cache_write_price: null, cache_read_price: 5e-7, per_request_price: null }
        ]
        return m
      })(),
      group: group(1, 'g', 'openai', [], { rate_multiplier: 0.25 })
    },
    { name: '按次', model: requestModel('per-call', 'anthropic', 'per_request', 1), group: group(1, 'g', 'anthropic', [], { rate_multiplier: 0.2 }) },
    {
      name: '图片 · 档位 + 独立倍率',
      model: requestModel('gpt-image', 'openai', 'image', 0.2, [
        { label: '1K', price: 0.02 },
        { label: '2K', price: 0.04 },
        { label: '4K', price: 0.08 }
      ]),
      group: group(1, 'g', 'openai', [], { rate_multiplier: 0.5, image_rate_independent: true, image_rate_multiplier: 1.5 })
    },
    {
      name: '视频 · 独立倍率',
      model: requestModel('video-gen', 'openai', 'video', 0.3),
      group: group(1, 'g', 'openai', [], { rate_multiplier: 0.5, video_rate_independent: true, video_rate_multiplier: 2 })
    }
  ]

  for (const c of cases) {
    it(c.name, () => {
      const summary = summarizePrice(c.model, c.group)
      const text = tableText(c.model, c.group)
      const shown = [summary.input, summary.output, summary.cacheRead, summary.request].filter((v): v is string => v != null)
      expect(shown.length).toBeGreaterThan(0)
      for (const value of shown) {
        expect(text, `表格里应出现 ${value}`).toContain(value)
      }
    })
  }
})
