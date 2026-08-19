// 趋势线的几何计算。刻意与渲染分开：这样「空数组不画线」这条规则可以被单测钉住，
// 而不是靠肉眼看一列 SVG。不引入任何图表库 —— 一条 polyline 不值得再拖一个 chart.js。

export interface TeamSparkline {
  /** polyline 的 points 属性，形如 "2,14 50,10 98,2" */
  points: string
  width: number
  height: number
  /** 本期峰值，用于 tooltip；全 0 时是 0 */
  max: number
  /** 有数据但全是 0（真实的「这几天没花钱」），与「没有数据」不是一回事 */
  allZero: boolean
}

export interface SparklineOptions {
  width?: number
  height?: number
  /** 内边距，给 stroke 的一半线宽留位置，否则顶点会被 viewBox 切掉 */
  padding?: number
}

const round2 = (n: number) => Math.round(n * 100) / 100

/**
 * 把逐日消耗算成 polyline 的顶点。
 *
 * 返回 null 表示**不要画线**：daily 为空数组意味着后端 EnrichRows 降级、这行没有逐日数据。
 * 此时画一条贴底的平线会把「没数据」伪装成「一直是 0」—— 两者在这个页面上的含义
 * 完全相反（前者要去查后端，后者是正常的没消耗）。全 0 的**非空**数组才画平线。
 *
 * 单点（本期只有一天）画在中线而不是顶端：只有一个样本时它既是最大值也是最小值，
 * 贴顶会被读成「冲到峰值」。
 */
export function buildSparkline(
  daily: readonly number[] | null | undefined,
  opts: SparklineOptions = {},
): TeamSparkline | null {
  if (!daily || daily.length === 0) return null

  const width = opts.width ?? 96
  const height = opts.height ?? 24
  const pad = opts.padding ?? 2

  // 负数不该出现在消耗里；真出现了按 0 处理，免得把整条线的基线拽下去
  const vals = daily.map((v) => (typeof v === 'number' && Number.isFinite(v) && v > 0 ? v : 0))
  const max = Math.max(...vals)
  const top = pad
  const bottom = height - pad
  const span = bottom - top

  const yOf = (v: number) => (max <= 0 ? bottom : bottom - (v / max) * span)

  if (vals.length === 1) {
    const y = round2(max <= 0 ? bottom : top + span / 2)
    return {
      points: `${round2(pad)},${y} ${round2(width - pad)},${y}`,
      width,
      height,
      max,
      allZero: max <= 0,
    }
  }

  const step = (width - pad * 2) / (vals.length - 1)
  const points = vals.map((v, i) => `${round2(pad + i * step)},${round2(yOf(v))}`).join(' ')
  return { points, width, height, max, allZero: max <= 0 }
}
