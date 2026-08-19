import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import TrendSparkline from '../TrendSparkline.vue'

describe('TrendSparkline', () => {
  it('daily 为空数组时整个 svg 都不渲染 —— 留空，不画平的假线', () => {
    const w = mount(TrendSparkline, { props: { daily: [] } })
    expect(w.find('svg').exists()).toBe(false)
    expect(w.find('polyline').exists()).toBe(false)
  })

  it('daily 缺失（后端降级、字段没下发）同样留空', () => {
    const w = mount(TrendSparkline, { props: { daily: undefined } })
    expect(w.find('polyline').exists()).toBe(false)
  })

  it('有数据时画一条 polyline，顶点来自逐日值', () => {
    const w = mount(TrendSparkline, { props: { daily: [1, 2, 4], width: 100, height: 20 } })
    const line = w.find('polyline')
    expect(line.exists()).toBe(true)
    expect(line.attributes('points')).toBe('2,14 50,10 98,2')
    // 描边不能填充，否则会变成一块色斑
    expect(line.attributes('fill')).toBe('none')
  })

  it('全 0 的非空数组要画贴底平线：「这几天没花钱」是真实结论', () => {
    const w = mount(TrendSparkline, { props: { daily: [0, 0], width: 100, height: 20 } })
    expect(w.find('polyline').attributes('points')).toBe('2,18 98,18')
  })

  it('label 同时进 aria-label 与 <title>，屏幕阅读器与 hover 都能拿到行名', () => {
    const w = mount(TrendSparkline, { props: { daily: [1, 2], label: 'alice 本期逐日消耗' } })
    expect(w.find('svg').attributes('aria-label')).toBe('alice 本期逐日消耗')
    expect(w.find('title').text()).toBe('alice 本期逐日消耗')
  })

  it('viewBox 跟随传入尺寸，不写死', () => {
    const w = mount(TrendSparkline, { props: { daily: [1, 2], width: 120, height: 30 } })
    expect(w.find('svg').attributes('viewBox')).toBe('0 0 120 30')
  })
})
