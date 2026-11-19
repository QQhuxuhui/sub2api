import type { ModelPlazaGroup, PlazaModel } from '@/api/modelPlaza'
import type { PlazaHealthLine, PlazaHealthResponse, PlazaHealthSample } from '../types'

export function tokenModel(name: string, platform: string, overrides: Partial<PlazaModel> = {}): PlazaModel {
  return {
    name,
    platform,
    pricing: {
      billing_mode: 'token',
      input_price: 3e-6,
      output_price: 1.5e-5,
      cache_write_price: 3.75e-6,
      cache_read_price: 3e-7,
      image_input_price: null,
      image_output_price: null,
      per_request_price: null,
      intervals: []
    },
    official_pricing: {
      input_price: 3e-6,
      output_price: 1.5e-5,
      cache_write_price: 3.75e-6,
      cache_read_price: 3e-7
    },
    ...overrides
  }
}

export function requestModel(
  name: string,
  platform: string,
  mode: 'per_request' | 'image' | 'video',
  perRequest: number | null,
  tiers: Array<{ label: string; price: number | null }> = []
): PlazaModel {
  return {
    name,
    platform,
    pricing: {
      billing_mode: mode,
      input_price: null,
      output_price: null,
      cache_write_price: null,
      cache_read_price: null,
      image_input_price: null,
      image_output_price: null,
      per_request_price: perRequest,
      intervals: tiers.map((tier, idx) => ({
        min_tokens: idx,
        max_tokens: null,
        tier_label: tier.label,
        input_price: null,
        output_price: null,
        cache_write_price: null,
        cache_read_price: null,
        per_request_price: tier.price
      }))
    },
    official_pricing: null
  }
}

export function group(id: number, name: string, platform: string, models: PlazaModel[], overrides: Partial<ModelPlazaGroup> = {}): ModelPlazaGroup {
  return {
    id,
    name,
    description: '',
    platform,
    subscription_type: 'standard',
    rate_multiplier: 1,
    peak_rate_enabled: false,
    peak_start: '',
    peak_end: '',
    peak_rate_multiplier: 1,
    is_exclusive: false,
    image_rate_independent: false,
    image_rate_multiplier: 1,
    video_rate_independent: false,
    video_rate_multiplier: 1,
    long_context_pricing_enabled: true,
    models,
    ...overrides
  }
}

/** 第一个采样的小时起点（2026-11-17 00:00 UTC），之后每个采样往后一小时。 */
export const SAMPLE_START = 1794873600

/** 按给定的失败占比生成连续小时的采样。 */
export function samples(errorRates: number[] = [0]): PlazaHealthSample[] {
  return errorRates.map((e, idx) => ({ t: SAMPLE_START + idx * 3600, e }))
}

export function healthLine(
  platform: string,
  groupId: number,
  model: string,
  state: PlazaHealthLine['state'],
  overrides: Partial<PlazaHealthLine> = {}
): PlazaHealthLine {
  return {
    platform,
    group_id: groupId,
    model,
    state,
    error_rate: state === 'healthy' ? 0.01 : state === 'warning' ? 0.08 : 0.4,
    ttft_ms: 2000,
    rank: null,
    samples: samples(),
    ...overrides
  }
}

export function health(lines: PlazaHealthLine[]): PlazaHealthResponse {
  return { available: true, lines }
}
