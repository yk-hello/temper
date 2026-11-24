// K 线指标（12 的 V.1）。纯函数，手算样例在 tools/test-indicators.mjs。
// 数组和 K 线一一对应，数据不够的位置是 null。

export type Series = (number | null)[]
export interface OHLCV { t: number; h: number; l: number; c: number; vb: number; vs: number }

/** 简单移动平均 */
export function sma(v: number[], n: number): Series {
  const out: Series = new Array(v.length).fill(null)
  let sum = 0
  for (let i = 0; i < v.length; i++) {
    sum += v[i]
    if (i >= n) sum -= v[i - n]
    if (i >= n - 1) out[i] = sum / n
  }
  return out
}

/** 指数移动平均：第一个值是前 n 个的简单平均，之后 k = 2 / (n + 1) */
export function ema(v: number[], n: number): Series {
  const out: Series = new Array(v.length).fill(null)
  if (v.length < n) return out
  let e = 0
  for (let i = 0; i < n; i++) e += v[i]
  e /= n
  out[n - 1] = e
  const k = 2 / (n + 1)
  for (let i = n; i < v.length; i++) {
    e = v[i] * k + e * (1 - k)
    out[i] = e
  }
  return out
}

/** 布林带：中轨 n 根均值，上下轨 ± k 倍总体标准差 */
export function bollinger(v: number[], n = 20, k = 2): { mid: Series; up: Series; lo: Series } {
  const mid = sma(v, n)
  const up: Series = new Array(v.length).fill(null)
  const lo: Series = new Array(v.length).fill(null)
  for (let i = n - 1; i < v.length; i++) {
    const m = mid[i]!
    let s = 0
    for (let j = i - n + 1; j <= i; j++) s += (v[j] - m) * (v[j] - m)
    const d = Math.sqrt(s / n)
    up[i] = m + k * d
    lo[i] = m - k * d
  }
  return { mid, up, lo }
}

/** RSI（Wilder 平滑）：前 n 次涨跌取平均，之后 (旧 × (n−1) + 新) ÷ n */
export function rsi(v: number[], n = 14): Series {
  const out: Series = new Array(v.length).fill(null)
  if (v.length <= n) return out
  let gain = 0, loss = 0
  for (let i = 1; i <= n; i++) {
    const d = v[i] - v[i - 1]
    if (d > 0) gain += d; else loss -= d
  }
  gain /= n
  loss /= n
  const val = () => (loss === 0 ? (gain === 0 ? 50 : 100) : 100 - 100 / (1 + gain / loss))
  out[n] = val()
  for (let i = n + 1; i < v.length; i++) {
    const d = v[i] - v[i - 1]
    gain = (gain * (n - 1) + Math.max(d, 0)) / n
    loss = (loss * (n - 1) + Math.max(-d, 0)) / n
    out[i] = val()
  }
  return out
}

/** MACD：快线 EMA − 慢线 EMA = DIF；DIF 的 EMA = DEA；柱 = DIF − DEA */
export function macd(v: number[], fast = 12, slow = 26, signal = 9): { dif: Series; dea: Series; hist: Series } {
  const f = ema(v, fast), s = ema(v, slow)
  const dif: Series = v.map((_, i) => (f[i] != null && s[i] != null ? f[i]! - s[i]! : null))
  const start = dif.findIndex((x) => x != null)
  const dea: Series = new Array(v.length).fill(null)
  const hist: Series = new Array(v.length).fill(null)
  if (start < 0) return { dif, dea, hist }
  const sub = ema(dif.slice(start) as number[], signal)
  sub.forEach((x, i) => {
    dea[start + i] = x
    if (x != null) hist[start + i] = dif[start + i]! - x
  })
  return { dif, dea, hist }
}

/** 成交量加权均价：每天 UTC 0 点重新算，价格用 (高 + 低 + 收) ÷ 3 */
export function vwap(bars: OHLCV[]): Series {
  const out: Series = new Array(bars.length).fill(null)
  let day = NaN, pv = 0, vol = 0
  for (let i = 0; i < bars.length; i++) {
    const b = bars[i]
    const d = Math.floor(b.t / 86400000)
    if (d !== day) { day = d; pv = 0; vol = 0 }
    const v = b.vb + b.vs
    pv += ((b.h + b.l + b.c) / 3) * v
    vol += v
    out[i] = vol > 0 ? pv / vol : null
  }
  return out
}

/** 主动买卖差：主动买入量 − 主动卖出量 */
export const delta = (bars: OHLCV[]): number[] => bars.map((b) => b.vb - b.vs)

/** 累计成交量差：主动买卖差从第一根累加 */
export function cvd(bars: OHLCV[]): number[] {
  let s = 0
  return bars.map((b) => (s += b.vb - b.vs))
}
