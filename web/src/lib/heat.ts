// 流动性频谱的数据接口（12 的 V.5–V.10）。后端见 backend/internal/heat。
export interface HeatEvent {
  t: number
  kind: 'spoof' | 'absorb' | 'iceberg' | 'sweep' | 'large'
  p: number
  bid: boolean
  usd: number
  ev: Record<string, number>
}

export interface HeatHistory {
  instrument: string
  t0: number
  dt: number
  cols: number
  rows: number
  p0: number
  step: number
  cells: string
  mids: number[]
  buy: number[]
  sell: number[]
  trades: [number, number, number, number][] // 时间、价格、美元、1=主动买
  events: HeatEvent[]
  gaps: [number, number][]
  since: number
  q_lo: number
  q_hi: number
  shift?: number[] // 开屏拼接用：每列的价格平移，真实价格 = p0 + 行 × step + shift[列]
}

export interface HeatWall {
  p: number
  usd: number
  bid: boolean
  age: number
  touch: number
  dist: number
}

export interface HeatFrame {
  t: number
  w: number
  b: number
  c?: string
  m: number
  tr?: { t: number; p: number; q: number; b?: boolean }[]
  ev?: HeatEvent[]
  sync?: boolean
  cov?: [number, number]
  walls?: HeatWall[]
  flow?: [number, number]
}

export interface HeatStatus {
  instrument: string
  ready: boolean
  syncing: boolean
  width: number
  mid: number
  full: [number, number]
  levels: number
  since: number
  last: number
}

export function decodeB64(s: string): Uint8Array {
  const bin = atob(s)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

// 和后端 FromCell 同一个公式：100 美元以下是 0，5000 万美元是 255
const Q_LO = 100, Q_HI = 50_000_000
export function cellUSD(c: number): number {
  return c === 0 ? 0 : Q_LO * Math.exp((c / 255) * Math.log(Q_HI / Q_LO))
}

export function usdCell(u: number): number {
  if (u <= Q_LO) return 0
  return Math.min(255, Math.max(1, Math.round((Math.log(u / Q_LO) / Math.log(Q_HI / Q_LO)) * 255)))
}

/** 和 usdCell 一样，不取整（画柱子高度用，动起来不一格一格地跳） */
export function usdCellF(u: number): number {
  if (u <= Q_LO) return 0
  return Math.min(255, (Math.log(u / Q_LO) / Math.log(Q_HI / Q_LO)) * 255)
}

/** 频谱配色：青（默认，D85 第 11 条）或灰（D82）。两种都是越亮金额越大 */
export type HeatRamp = 'gray' | 'teal'
let ramp: HeatRamp = (() => { try { return localStorage.getItem('qs.heatRamp') === 'gray' ? 'gray' : 'teal' } catch { return 'teal' } })()
export const getHeatRamp = () => ramp
export function setHeatRamp(v: HeatRamp) { ramp = v; try { localStorage.setItem('qs.heatRamp', v) } catch { /* 忽略 */ } }

/** 金额下限：低于它的挂单不亮 */
export const HEAT_FLOORS = [0, 100_000, 500_000, 1_000_000, 5_000_000]

export const HEAT_SPANS = [60_000, 5 * 60_000, 30 * 60_000, 2 * 3600_000, 24 * 3600_000]
