// 订单流的纯计算：足迹合并、失衡、持仓量摘要。手算样例在 tools/test-flow.mjs。
export interface FPLevel { p: number; b: number; s: number }
export interface FPBar { t: number; l: FPLevel[] }
export interface DispLevel { p: number; b: number; s: number; imb: -1 | 0 | 1 }
export interface DispBar { levels: DispLevel[]; poc: number; maxSide: number; b: number; s: number }

export const IMBALANCE = 3 // 一侧至少是对角的多少倍算失衡

/** 不小于 raw 的 1、2、5 × 10^n：档位只能比算出来的宽，这样一定放得下数字 */
export function niceUp125(raw: number): number {
  if (!(raw > 0)) return 0
  const p = 10 ** Math.floor(Math.log10(raw))
  const f = raw / p
  return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10) * p
}

/**
 * 把细档位合并到屏幕放得下的档位，算成交最多的价位和失衡。
 * 失衡看对角：这一档的主动买 ≥ 下一档主动卖的 3 倍，或者这一档的主动卖 ≥ 上一档主动买的 3 倍。
 * 买方吃的是上面的卖单、卖方砸的是下面的买单，所以比较的是斜对角。
 * 对角是空的时，用这根 K 线每侧平均量的十分之一代替，免得零星一两笔就算失衡。
 */
export function mergeBar(bar: FPBar, step: number): DispBar {
  const m = new Map<number, { b: number; s: number }>()
  for (const x of bar.l) {
    const k = Math.floor(x.p / step + 1e-9)
    const o = m.get(k) ?? { b: 0, s: 0 }
    o.b += x.b; o.s += x.s
    m.set(k, o)
  }
  const keys = [...m.keys()].sort((a, c) => a - c)
  let tb = 0, ts = 0
  for (const k of keys) { tb += m.get(k)!.b; ts += m.get(k)!.s }
  const noise = keys.length ? ((tb + ts) / (2 * keys.length)) * 0.1 : 0
  let poc = 0, best = -1, maxSide = 0
  const levels: DispLevel[] = keys.map((k) => {
    const o = m.get(k)!
    const under = m.get(k - 1)?.s ?? 0
    const above = m.get(k + 1)?.b ?? 0
    const buyImb = o.b > 0 && o.b >= IMBALANCE * Math.max(under, noise)
    const sellImb = o.s > 0 && o.s >= IMBALANCE * Math.max(above, noise)
    const p = k * step
    if (o.b + o.s > best) { best = o.b + o.s; poc = p }
    maxSide = Math.max(maxSide, o.b, o.s)
    const imb: -1 | 0 | 1 = buyImb && !sellImb ? 1 : sellImb && !buyImb ? -1 : 0
    return { p, b: o.b, s: o.s, imb }
  })
  return { levels, poc, maxSide, b: tb, s: ts }
}

const mergeCache = new WeakMap<FPBar, Map<number, DispBar>>()
export function mergedBar(bar: FPBar, step: number): DispBar {
  let m = mergeCache.get(bar)
  if (!m) { m = new Map(); mergeCache.set(bar, m) }
  let d = m.get(step)
  if (!d) { d = mergeBar(bar, step); m.set(step, d) }
  return d
}

// ---------- 持仓量变化拆解 ----------

export type OIKind = 'new_long' | 'new_short' | 'short_cover' | 'long_close'
export interface OIPoint { t: number; usd: number; delta: number; dp: number; kind: OIKind }

/** 最近一段时间的摘要：持仓量变化百分比、主要是哪一种 */
export function oiSummary(pts: OIPoint[], now: number, windowMs: number): { pct: number; usd: number; main?: OIKind } | null {
  if (!pts.length) return null
  const last = pts[pts.length - 1]
  const inWin = pts.filter((p) => p.t >= now - windowMs)
  if (!inWin.length) return { pct: 0, usd: last.usd }
  const sum: Partial<Record<OIKind, number>> = {}
  let net = 0
  for (const p of inWin) {
    net += p.delta
    if (p.kind) sum[p.kind] = (sum[p.kind] ?? 0) + Math.abs(p.delta)
  }
  let main: OIKind | undefined
  let best = 0
  for (const k of Object.keys(sum) as OIKind[]) if ((sum[k] ?? 0) > best) { best = sum[k]!; main = k }
  const base = last.usd - net
  return { pct: base > 0 ? (net / base) * 100 : 0, usd: last.usd, main }
}
