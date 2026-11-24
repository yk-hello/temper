// K 线图的指标设置和画线（12 的 V.1、V.2）。都存在这台设备的浏览器里：指标全局一份，画线按品种存。
import { useCallback, useMemo, useSyncExternalStore } from 'react'
import { bollinger, cvd, delta, ema, macd, rsi, sma, vwap, type OHLCV, type Series } from './indicators'

export interface IndCfg {
  ma1: { on: boolean; n: number }; ma2: { on: boolean; n: number }
  ema1: { on: boolean; n: number }; ema2: { on: boolean; n: number }
  vwap: { on: boolean }
  boll: { on: boolean; n: number; k: number }
  vol: { on: boolean }
  rsi: { on: boolean; n: number }
  macd: { on: boolean; f: number; s: number; g: number }
  cvd: { on: boolean }
  delta: { on: boolean }
  oi: { on: boolean }
  events: { on: boolean }
}

export const DEFAULT_IND: IndCfg = {
  ma1: { on: false, n: 20 }, ma2: { on: false, n: 60 }, ema1: { on: false, n: 12 }, ema2: { on: false, n: 26 },
  vwap: { on: false }, boll: { on: false, n: 20, k: 2 }, vol: { on: true }, rsi: { on: false, n: 14 },
  macd: { on: false, f: 12, s: 26, g: 9 }, cvd: { on: false }, delta: { on: false }, oi: { on: false }, events: { on: true },
}

const IND_KEY = 'qs.ind'
let cfg: IndCfg = (() => {
  try {
    const v = JSON.parse(localStorage.getItem(IND_KEY) ?? 'null')
    if (!v) return DEFAULT_IND
    const out = { ...DEFAULT_IND } as Record<string, object>
    for (const k of Object.keys(DEFAULT_IND)) out[k] = { ...(DEFAULT_IND as unknown as Record<string, object>)[k], ...(v[k] ?? {}) }
    return out as unknown as IndCfg
  } catch { return DEFAULT_IND }
})()
const cfgSubs = new Set<() => void>()
export function setIndCfg(f: (c: IndCfg) => IndCfg) {
  cfg = f(cfg)
  try { localStorage.setItem(IND_KEY, JSON.stringify(cfg)) } catch { /* 忽略 */ }
  cfgSubs.forEach((x) => x())
}
export function useIndCfg(): IndCfg {
  return useSyncExternalStore((f) => { cfgSubs.add(f); return () => cfgSubs.delete(f) }, () => cfg)
}

// ---------- 算好的指标 ----------

export interface MainLine { key: string; label: string; v: Series; color: string; dash?: number[] }
export interface MainBand { up: Series; lo: Series }
export interface Pane {
  key: string; label: string
  lines: { label: string; v: Series; color: string; dash?: number[] }[]
  bars?: Series
  /** 柱子的方向色：不给时按正负 */
  barColors?: ('up' | 'down' | null)[]
  /** 每根的一句话，读数时显示 */
  notes?: string[]
  levels?: number[] // 固定的参考线（RSI 的 30、70）
  range?: [number, number]
  fmt?: 'qty' | 'px' | 'pct'
}
export interface IndData { main: MainLine[]; band?: MainBand; panes: Pane[]; vol: boolean }

const W1 = '#ffffff', W2 = '#a8a8b0', W3 = '#6e6e76'

export function computeInd(bars: OHLCV[], c: IndCfg, oi?: Series): IndData {
  const close = bars.map((b) => b.c)
  const main: MainLine[] = []
  if (c.ma1.on) main.push({ key: 'ma1', label: `MA${c.ma1.n}`, v: sma(close, c.ma1.n), color: W1 })
  if (c.ma2.on) main.push({ key: 'ma2', label: `MA${c.ma2.n}`, v: sma(close, c.ma2.n), color: W2 })
  if (c.ema1.on) main.push({ key: 'ema1', label: `EMA${c.ema1.n}`, v: ema(close, c.ema1.n), color: W1, dash: [5, 3] })
  if (c.ema2.on) main.push({ key: 'ema2', label: `EMA${c.ema2.n}`, v: ema(close, c.ema2.n), color: W2, dash: [5, 3] })
  if (c.vwap.on) main.push({ key: 'vwap', label: 'VWAP', v: vwap(bars), color: W1, dash: [1, 3] })
  let band: MainBand | undefined
  if (c.boll.on) {
    const b = bollinger(close, c.boll.n, c.boll.k)
    band = { up: b.up, lo: b.lo }
    main.push({ key: 'boll', label: `BOLL${c.boll.n}`, v: b.mid, color: W3 })
  }
  const panes: Pane[] = []
  if (c.rsi.on) panes.push({ key: 'rsi', label: `RSI${c.rsi.n}`, lines: [{ label: '', v: rsi(close, c.rsi.n), color: W1 }], levels: [30, 70], range: [0, 100], fmt: 'pct' })
  if (c.macd.on) {
    const m = macd(close, c.macd.f, c.macd.s, c.macd.g)
    panes.push({ key: 'macd', label: `MACD ${c.macd.f},${c.macd.s},${c.macd.g}`, lines: [{ label: 'DIF', v: m.dif, color: W1 }, { label: 'DEA', v: m.dea, color: W2, dash: [4, 3] }], bars: m.hist, levels: [0], fmt: 'px' })
  }
  if (c.cvd.on) panes.push({ key: 'cvd', label: 'CVD', lines: [{ label: '', v: cvd(bars), color: W1 }], fmt: 'qty' })
  if (c.delta.on) panes.push({ key: 'delta', label: 'DELTA', lines: [], bars: delta(bars), levels: [0], fmt: 'qty' })
  if (c.oi.on && oi) panes.push({ key: 'oi', label: 'OI', lines: [{ label: '', v: oi, color: W1 }], fmt: 'qty' })
  return { main, band, panes, vol: c.vol.on }
}

// ---------- 画线 ----------

export type ToolKind = 'hline' | 'trend' | 'rect' | 'fib' | 'measure'
export interface ShapePt { t: number; p: number }
export interface Shape { id: string; kind: Exclude<ToolKind, 'measure'>; a: ShapePt; b: ShapePt }

const shapeKey = (inst: string) => `qs.shapes.${inst}`
const shapeCache = new Map<string, Shape[]>()
const shapeSubs = new Set<() => void>()
function readShapes(inst: string): Shape[] {
  let v = shapeCache.get(inst)
  if (!v) {
    try { v = JSON.parse(localStorage.getItem(shapeKey(inst)) ?? '[]') as Shape[] } catch { v = [] }
    shapeCache.set(inst, v)
  }
  return v
}
const EMPTY: Shape[] = []
export function useShapes(inst: string | undefined): [Shape[], (s: Shape[]) => void] {
  const list = useSyncExternalStore((f) => { shapeSubs.add(f); return () => shapeSubs.delete(f) }, () => (inst ? readShapes(inst) : EMPTY))
  const set = useCallback((s: Shape[]) => {
    if (!inst) return
    shapeCache.set(inst, s)
    try { localStorage.setItem(shapeKey(inst), JSON.stringify(s)) } catch { /* 忽略 */ }
    shapeSubs.forEach((x) => x())
  }, [inst])
  return [list, set]
}

export function useInd(bars: OHLCV[] | undefined, oi?: Series): IndData {
  const c = useIndCfg()
  return useMemo(() => computeInd(bars ?? [], c, oi), [bars, c, oi])
}

export const FIB_LEVELS = [0, 0.236, 0.382, 0.5, 0.618, 0.786, 1]
export const newShapeId = () => Math.random().toString(36).slice(2, 10)
