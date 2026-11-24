// 订单流（12 的 V.11–V.14）：足迹图的合并和失衡、持仓量变化拆解、爆仓流的数据。
import { useEffect, useMemo, useRef, useState } from 'react'
import { tr, type Key } from './i18n'
import type { IndData, Pane } from './chartPrefs'
import { cvd, delta, type OHLCV } from './indicators'
import type { FPBar, OIPoint } from './flowCalc'

export * from './flowCalc'

/** 订单流模式的副图：持仓量变化、主动买卖差、累计成交量差 */
export function flowInd(bars: OHLCV[], pts: OIPoint[]): IndData {
  const byT = new Map(pts.map((p) => [p.t, p]))
  const oiBars = bars.map((b) => byT.get(b.t)?.delta ?? null)
  const colors = bars.map((b) => { const p = byT.get(b.t); return p ? (p.dp >= 0 ? 'up' as const : 'down' as const) : null })
  const notes = bars.map((b) => { const p = byT.get(b.t); return p?.kind ? tr(`fl.kind.${p.kind}` as Key) : '' })
  const oi: Pane = { key: 'oiflow', label: tr('fl.oiPane'), lines: [], bars: oiBars, barColors: colors, notes, levels: [0], fmt: 'qty' }
  const dl: Pane = { key: 'delta', label: tr('fl.deltaPane'), lines: [], bars: delta(bars), levels: [0], fmt: 'qty' }
  const cv: Pane = { key: 'cvd', label: 'CVD', lines: [{ label: '', v: cvd(bars), color: '#ffffff' }], fmt: 'qty' }
  // 1 分钟线上持仓量 5 分钟才一根，大多数时候是空的：窗口小只放得下一个副图时先放主动买卖差
  const fine = bars.length > 1 && bars[1].t - bars[0].t < 300_000
  const panes = fine ? [dl, oi, cv] : [oi, dl, cv]
  return { main: [], panes, vol: false }
}

// ---------- 接口 ----------

export interface Liq { t: number; instrument: string; side: 'long' | 'short'; venue?: 'binance' | 'bybit' | 'okx'; p: number; q: number; usd: number }
export interface LiqData { items: Liq[]; m5: { long: number; short: number }; h1: { long: number; short: number }; since: number }
export interface Ratio { ratio: number; long: number; short: number; t: number }
export interface Ratios { account: Ratio; top: Ratio }

function usePolled<T>(url: string | null, ms: number): T | null {
  const [v, set] = useState<T | null>(null)
  useEffect(() => {
    set(null)
    if (!url) return
    let alive = true
    const go = async () => {
      if (document.hidden) return
      try {
        const r = await fetch(url, { cache: 'no-store' })
        if (r.ok && alive) set((await r.json()) as T)
      } catch { /* 下次再试 */ }
    }
    void go()
    const t = window.setInterval(go, ms)
    return () => { alive = false; clearInterval(t) }
  }, [url, ms])
  return v
}

export const useOIFlow = (inst: string | null, ms: number) => usePolled<{ period_ms: number; points: OIPoint[] }>(inst ? `/api/flow/oi?instrument=${inst}&interval=${ms}` : null, 60_000)
export const useRatios = (inst: string | null) => usePolled<Ratios>(inst ? `/api/flow/ratio?instrument=${inst}` : null, 60_000)
export const useLiqs = (inst: string | null) => usePolled<LiqData>(inst ? `/api/flow/liquidations?instrument=${inst}&limit=60` : null, 3000)

export interface Footprint { bars: Map<number, FPBar>; step: number; since: number; version: number; recorded: boolean }

/** 足迹数据：先取最近 400 根，之后每 2 秒只取最新的两根 */
export function useFootprint(inst: string | null, ms: number): Footprint {
  const store = useRef<Map<number, FPBar>>(new Map())
  const [state, set] = useState({ step: 0, since: 0, version: 0, recorded: true })
  useEffect(() => {
    store.current = new Map()
    set({ step: 0, since: 0, version: 0, recorded: true })
    if (!inst || ms < 60_000) return
    let alive = true
    let first = true
    const go = async () => {
      if (document.hidden) return
      const now = Date.now()
      const from = first ? now - 400 * ms : now - 2 * ms
      try {
        const r = await fetch(`/api/flow/footprint?instrument=${inst}&interval=${ms}&from=${from}&to=${now}`, { cache: 'no-store' })
        if (!alive) return
        if (r.status === 404) { set((s) => ({ ...s, recorded: false })); return }
        if (!r.ok) return
        const d = (await r.json()) as { step: number; bars: FPBar[]; since: number }
        // 细档位随振幅变，换了就整张重取
        if (!first && d.step !== store.current.get(-1)?.t) { first = true; void go(); return }
        for (const b of d.bars) store.current.set(b.t, b)
        store.current.set(-1, { t: d.step, l: [] })
        first = false
        set((s) => ({ step: d.step, since: d.since, version: s.version + 1, recorded: true }))
      } catch { /* 下次再试 */ }
    }
    void go()
    const t = window.setInterval(go, 2000)
    return () => { alive = false; clearInterval(t) }
  }, [inst, ms])
  return useMemo(() => ({ bars: store.current, ...state }), [state])
}
