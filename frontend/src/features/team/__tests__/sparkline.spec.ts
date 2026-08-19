import { describe, expect, it } from 'vitest'
import { buildSparkline } from '../sparkline'

// 统一用一组好算的尺寸，断言具体坐标：
// width=100 / height=20 / padding=2 → x 从 2 到 98，y 从 2（顶）到 18（底）
const OPTS = { width: 100, height: 20, padding: 2 }

describe('buildSparkline', () => {
  it('空数组返回 null —— 不画线，「没数据」不能长得像「一直是 0」', () => {
    expect(buildSparkline([], OPTS)).toBeNull()
  })

  it('null / undefined（后端 EnrichRows 降级）同样不画线', () => {
    expect(buildSparkline(null, OPTS)).toBeNull()
    expect(buildSparkline(undefined, OPTS)).toBeNull()
  })

  it('逐日消耗按峰值归一化到 viewBox：值越大 y 越小', () => {
    const line = buildSparkline([1, 2, 4], OPTS)!
    // x: 2 / 50 / 98；y: 18-(1/4)*16=14、18-(2/4)*16=10、18-(4/4)*16=2
    expect(line.points).toBe('2,14 50,10 98,2')
    expect(line.max).toBe(4)
    expect(line.allZero).toBe(false)
  })

  it('峰值出现在中间时也按同一比例落点（不是简单地首尾连线）', () => {
    const line = buildSparkline([0, 5, 1], OPTS)!
    // y: 18、2、18-(1/5)*16=14.8
    expect(line.points).toBe('2,18 50,2 98,14.8')
  })

  it('非空但全 0 是真实的「这几天没花钱」，画一条贴底的平线', () => {
    const line = buildSparkline([0, 0, 0], OPTS)!
    expect(line.points).toBe('2,18 50,18 98,18')
    expect(line.allZero).toBe(true)
  })

  it('只有一天时画中线，贴顶会被读成「冲到峰值」', () => {
    const line = buildSparkline([7], OPTS)!
    expect(line.points).toBe('2,10 98,10')
    expect(line.max).toBe(7)
  })

  it('负值（不该出现的脏数据）按 0 处理，不把基线拽到 viewBox 外', () => {
    const line = buildSparkline([-3, 4], OPTS)!
    expect(line.points).toBe('2,18 98,2')
  })

  it('默认尺寸下顶点仍留出 padding，stroke 不会被 viewBox 切掉', () => {
    const line = buildSparkline([1, 3])!
    expect(line.width).toBe(96)
    expect(line.height).toBe(24)
    // bottom=22 / top=2 / span=20，max=3 → y(1)=22-(1/3)*20=15.33、y(3)=2
    expect(line.points).toBe('2,15.33 94,2')
  })
})
