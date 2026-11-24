// 概率云（`10` 第 9 项）：用历史的每段收益，有放回地抽样，模拟几千条未来路径，叠成密度。
// 只是"过去的波动如果重演"的样子，不是预测。这个文件没有界面依赖，手算样例在 tools/test-cloud.mjs。

export interface CloudInput {
  /** 每一段的收益率（比如每 4 小时一个） */
  returns: number[]
  /** 往后模拟多少段 */
  steps: number
  paths: number
  /** 最大亏损（比例）：亏到这里自动平仓，这条路径之后就是一条平线 */
  maxLoss: number
  seed?: number
  /** 密度图的格数 */
  cols?: number
  rows?: number
}

export interface CloudResult {
  /** 纵轴范围（收益率） */
  lo: number
  hi: number
  cols: number
  rows: number
  /** 每格的路径数，按 cols × rows，第一行是最高收益 */
  density: Float32Array
  /** 每一列的分位数（收益率） */
  p5: Float32Array
  p50: Float32Array
  p95: Float32Array
  /** 碰到最大亏损线的路径比例 */
  stopped: number
  /** 所有路径里最差的结果（收益率）。一段跌得太猛时会比最大亏损更差 */
  worst: number
  best: number
  /** 最后一列的中位数 */
  median: number
}

/** 确定性的随机数（同样的输入画出同样的云，拖滑块时不会乱跳） */
function mulberry(seed: number) {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) >>> 0
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

export function simulate(inp: CloudInput): CloudResult {
  const { returns, steps, paths, maxLoss } = inp
  const cols = inp.cols ?? 60
  const rows = inp.rows ?? 48
  const rand = mulberry(inp.seed ?? 20261106)
  const n = returns.length
  const floor = 1 - maxLoss
  // values[c * paths + p]：第 c 列、第 p 条路径的净值（从 1 开始）
  const values = new Float32Array(cols * paths)
  let stopped = 0
  let worst = 0, best = 0
  for (let p = 0; p < paths; p++) {
    let v = 1
    let dead = false
    let col = 0
    for (let s = 1; s <= steps; s++) {
      if (!dead) {
        v *= 1 + returns[Math.floor(rand() * n)]
        if (v <= floor) { dead = true; stopped++ }
      }
      // 这一段覆盖到第几列（段数比列数少时一段铺几列，多时几段合一列）
      const c1 = Math.min(cols, Math.ceil((s * cols) / steps))
      for (; col < c1; col++) values[col * paths + p] = v
    }
    if (v - 1 < worst) worst = v - 1
    if (v - 1 > best) best = v - 1
  }
  // 纵轴：包住 1%–99% 的路径和最大亏损线，不让个别极端路径把图压扁
  const last = Array.from(values.subarray((cols - 1) * paths, cols * paths)).sort((a, b) => a - b)
  const hi = Math.max(last[Math.floor(paths * 0.99)] - 1, 0.02)
  const lo = Math.min(last[Math.floor(paths * 0.01)] - 1, -maxLoss) * 1.08
  const span = hi * 1.08 - lo
  const top = hi * 1.08
  const density = new Float32Array(cols * rows)
  const p5 = new Float32Array(cols), p50 = new Float32Array(cols), p95 = new Float32Array(cols)
  const buf = new Float32Array(paths)
  for (let c = 0; c < cols; c++) {
    for (let p = 0; p < paths; p++) {
      const x = values[c * paths + p] - 1
      buf[p] = x
      const r = Math.floor(((top - x) / span) * rows)
      if (r >= 0 && r < rows) density[r * cols + c]++
    }
    buf.sort()
    p5[c] = buf[Math.floor(paths * 0.05)]
    p50[c] = buf[Math.floor(paths * 0.5)]
    p95[c] = buf[Math.floor(paths * 0.95)]
  }
  return { lo, hi: top, cols, rows, density, p5, p50, p95, stopped: stopped / paths, worst, best, median: p50[cols - 1] }
}

/** 把净值或价格序列换算成固定时间段的收益率。pts 按时间排；每段 stepMs，中间没点的段跳过。 */
export function stepReturns(pts: { t: number; v: number }[], stepMs: number): number[] {
  if (pts.length < 2) return []
  const out: number[] = []
  let i = 0
  let prev = pts[0].v
  for (let t = pts[0].t + stepMs; t <= pts[pts.length - 1].t; t += stepMs) {
    while (i + 1 < pts.length && pts[i + 1].t <= t) i++
    const v = pts[i].v
    if (prev > 0) out.push(v / prev - 1)
    prev = v
  }
  return out
}
