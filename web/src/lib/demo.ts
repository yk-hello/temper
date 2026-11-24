// 示例数据（组件展厅用）。确定性生成，每次打开都一样，方便截图对比。
// 真实页面一律用真实行情，不用这里的数据。
import { gauss, rng } from './gfx'

export interface Trade { t: number; p: number; q: number; buy: boolean }
export interface Candle { t: number; o: number; h: number; l: number; c: number; vb: number; vs: number }

export interface Book {
  t0: number
  dt: number            // 每列的时间（毫秒）
  cols: number
  p0: number            // 最低一行的价格
  tick: number          // 每行的价格间隔
  rows: number
  cells: Uint8Array     // cols × rows，按行优先：cells[row * cols + col]，0–255 = 挂单量（对数刻度）
  mid: Float64Array     // 每列的中间价
  trades: Trade[]
  walls: Wall[]
}

export interface Wall { p: number; a: number; b: number; size: number; kind: 'wall' | 'spoof' | 'absorb'; bid: boolean }

/** 挂单量的亮度刻度（对数）：qLo 以下 = 0，qHi = 255。单位 BTC。 */
export const Q_LO = 0.1
export const Q_HI = 150
export function qOf(cell: number): number {
  return Math.exp(Math.log(Q_LO) + (cell / 255) * (Math.log(Q_HI) - Math.log(Q_LO)))
}

/**
 * 生成一段盘口历史（单位 BTC）。模拟真实盘口的结构：
 * - 做市商的阶梯挂单：离价格越远越厚，每一档有自己的"性格"（长期偏厚或偏薄），缓慢变化 → 连续的暗色光带
 * - 中等大单：随机出现在价格上下，持续几十秒到几分钟，然后撤掉或被吃掉 → 亮的横线段
 * - 整数关口的长期大单、虚假挂单（价格靠近就撤）、被慢慢吃掉的大单（吸收）
 * - 价格扫过的档位被吃空，随后慢慢补回 → 价格走过的地方留下一条暗的通道
 */
export function demoBook(opts: { seed?: number; cols?: number; rows?: number; t1?: number; p?: number; tick?: number; dt?: number } = {}): Book {
  const r = rng(opts.seed ?? 11)
  const cols = opts.cols ?? 1800
  const rows = opts.rows ?? 640
  const dt = opts.dt ?? 1000
  const tick = opts.tick ?? 1
  const t1 = opts.t1 ?? Date.UTC(2026, 10, 3, 12, 30, 0)
  const t0 = t1 - cols * dt
  const center = opts.p ?? 77420
  const p0 = Math.round(center / tick - rows / 2) * tick
  const lv = (p: number) => Math.round((p - p0) / tick)

  const walls: Wall[] = []
  // 整数关口：每 50 美元一张长期大单，100 的整数位更厚
  for (let p = Math.ceil(p0 / 50) * 50; p < p0 + rows * tick; p += 50) {
    if (r() < 0.25) continue
    const a = r() < 0.6 ? 0 : Math.floor(r() * cols * 0.6)
    walls.push({ p, a, b: r() < 0.7 ? cols : Math.min(cols, a + 400 + Math.floor(r() * 900)), size: (p % 100 === 0 ? 45 : 22) * (0.6 + r() * 0.8), kind: 'wall', bid: false })
  }
  // 虚假挂单：很大，价格靠近就撤
  for (let i = 0; i < 5; i++) {
    const a = Math.floor(cols * (0.08 + i * 0.18 + r() * 0.06))
    walls.push({ p: 0, a, b: Math.min(cols, a + 50 + Math.floor(r() * 70)), size: 60 + r() * 30, kind: 'spoof', bid: r() < 0.5 })
  }

  const mid = new Float64Array(cols)
  const cells = new Uint8Array(cols * rows)
  const trades: Trade[] = []
  let m = center, vol = 0.8, drift = 0
  const LO = Math.log(Q_LO), HI = Math.log(Q_HI)

  // 每一档的"性格"：长期偏厚或偏薄（对数正态），缓慢漂移
  // 相邻档位的性格相关（做市商成片挂单），所以先生成独立随机数再做一次平滑
  const raw = new Float32Array(rows)
  for (let y = 0; y < rows; y++) raw[y] = gauss(r)
  const bias = new Float32Array(rows)
  for (let y = 0; y < rows; y++) {
    let s = 0, wsum = 0
    for (let j = -3; j <= 3; j++) { const yy = y + j; if (yy < 0 || yy >= rows) continue; const wj = 4 - Math.abs(j); s += raw[yy] * wj; wsum += wj }
    bias[y] = (s / wsum) * 0.55 + raw[y] * 0.18
  }
  // 做市商挂单（平滑跟随目标值）
  const mm = new Float32Array(rows)
  const target = (y: number) => {
    const d = Math.abs(p0 + y * tick - m) / tick
    const shape = 0.25 + 2.6 * (1 - Math.exp(-d / 45)) // 近处薄，远处厚
    const near = d < 2 ? 0.25 : d < 6 ? 0.6 : 1
    return shape * near * Math.exp(bias[y])
  }
  for (let y = 0; y < rows; y++) mm[y] = target(y)
  const blocks: { y: number; until: number; size: number }[] = []
  const add = new Float32Array(rows)

  for (let c = 0; c < cols; c++) {
    if (c % 150 === 0) { drift = gauss(r) * 0.1; vol = 0.5 + r() * 0.8 }
    // 吸收：在价格前方放一张大单，价格反复撞上去，大单被慢慢吃掉，最后被击穿
    if (c === 560 || c === 1300) {
      const up = drift >= 0
      walls.push({ p: Math.round((m + (up ? 4 : -4) * tick) / tick) * tick, a: c, b: c + 200, size: 70, kind: 'absorb', bid: !up })
      drift = (up ? 1 : -1) * 0.3
    }
    // 虚假挂单出现时放在价格前方 8–20 档
    for (const w of walls) if (w.kind === 'spoof' && c === w.a) {
      const d = 8 + Math.floor(r() * 12)
      w.p = Math.round(m / tick) * tick + (w.bid ? -d : d) * tick
    }
    // 中间价：随机游走；碰到大单有一定概率被挡回
    let step = drift + gauss(r) * vol
    if (r() < 0.003) step += gauss(r) * 8
    const next = m + step * tick
    for (const w of walls) {
      if (c < w.a || c >= w.b || w.kind === 'spoof') continue
      const crossing = (m < w.p && next >= w.p) || (m > w.p && next <= w.p)
      if (crossing && r() < Math.min(0.92, w.size / 60)) { step = -step * 0.5; break }
    }
    const prev = m
    m += step * tick
    // 价格不能走出生成的价格范围（离边缘 60 档时反弹），否则画面会是一片空白
    const lo = p0 + 60 * tick, hi = p0 + (rows - 60) * tick
    if (m < lo) { m = lo + (lo - m); drift = Math.abs(drift) }
    if (m > hi) { m = hi - (m - hi); drift = -Math.abs(drift) }
    mid[c] = m
    for (const w of walls) {
      if (w.kind === 'spoof' && c > w.a && c < w.b && Math.abs(w.p - m) < 5 * tick) w.b = c
    }

    // 做市商挂单：每秒向目标值靠拢 4%，每档的性格缓慢漂移
    for (let y = 0; y < rows; y++) {
      mm[y] += (target(y) - mm[y]) * 0.04
      if (r() < 0.02) bias[y] += gauss(r) * 0.08
    }
    // 价格扫过的档位被吃空
    const ya = Math.max(0, Math.floor((Math.min(prev, m) - p0) / tick) - 1)
    const yb = Math.min(rows - 1, Math.ceil((Math.max(prev, m) - p0) / tick) + 1)
    for (let y = ya; y <= yb; y++) {
      mm[y] *= 0.15
      for (const bk of blocks) if (bk.y === y) bk.until = c
    }
    // 中等大单
    if (r() < 0.045) {
      const d = 6 + Math.floor(Math.pow(r(), 1.6) * 220)
      const y = Math.round((m - p0) / tick + (r() < 0.5 ? -d : d))
      if (y >= 0 && y < rows) blocks.push({ y, until: c + 40 + Math.floor(Math.pow(r(), 0.7) * 420), size: 6 + Math.exp(gauss(r) * 0.55) * 9 })
    }

    add.fill(0)
    for (const bk of blocks) if (c < bk.until) add[bk.y] += bk.size
    for (const w of walls) {
      if (c < w.a || c >= w.b) continue
      const wy = lv(w.p)
      if (wy < 0 || wy >= rows) continue
      const sz = w.kind === 'absorb' ? w.size * Math.max(0.04, 1 - (c - w.a) / (w.b - w.a)) : w.size
      add[wy] += sz
      // 大单通常分散在相邻几档
      if (wy > 0) add[wy - 1] += sz * 0.12
      if (wy + 1 < rows) add[wy + 1] += sz * 0.12
    }
    for (let y = 0; y < rows; y++) {
      const v = mm[y] + add[y]
      const l = (Math.log(Math.max(v, 1e-6)) - LO) / (HI - LO)
      cells[y * cols + c] = Math.max(0, Math.min(255, Math.round(l * 255)))
    }
    if (c % 60 === 0) for (let i = blocks.length - 1; i >= 0; i--) if (blocks[i].until <= c) blocks.splice(i, 1)

    // 成交：每秒若干笔，大小长尾分布（单位 BTC）
    const n = Math.floor(3 + r() * 7 + Math.abs(step) * 4)
    for (let i = 0; i < n; i++) {
      const buy = r() < 0.5 + Math.sign(step) * 0.22
      const qq = Math.exp(gauss(r) * 1.1 - 2.3) * (r() < 0.015 ? 30 : 1)
      trades.push({ t: t0 + c * dt + r() * dt, p: Math.round((m + (buy ? 0.5 : -0.5) * tick) / tick) * tick, q: qq, buy })
    }
  }
  trades.sort((a, b) => a.t - b.t)
  return { t0, dt, cols, p0, tick, rows, cells, mid, trades, walls }
}

/** 把成交聚合成 K 线 */
export function toCandles(trades: Trade[], ms: number): Candle[] {
  const out: Candle[] = []
  let cur: Candle | null = null
  for (const tr of trades) {
    const t = Math.floor(tr.t / ms) * ms
    if (!cur || cur.t !== t) {
      if (cur) out.push(cur)
      const o: number = cur ? cur.c : tr.p
      cur = { t, o, h: Math.max(o, tr.p), l: Math.min(o, tr.p), c: tr.p, vb: 0, vs: 0 }
    }
    cur.h = Math.max(cur.h, tr.p); cur.l = Math.min(cur.l, tr.p); cur.c = tr.p
    if (tr.buy) cur.vb += tr.q; else cur.vs += tr.q
  }
  if (cur) out.push(cur)
  return out
}
