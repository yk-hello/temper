// 全市场币种列表（品种搜索、命令面板用）：从点阵场的接口来，只取一次，几分钟刷新。
import { recall, remember } from './cache'
import { useEffect, useSyncExternalStore } from 'react'

export interface CoinRow { base: string; inst: string; px: number; chg: number; qv: number }

type MapResp = { nodes: { s: string; i: string; px: number; chg: number; qv: number }[] }
const toRows = (v: MapResp) => v.nodes.map((n) => ({ base: n.s, inst: n.i, px: n.px, chg: n.chg, qv: n.qv })).sort((a, b) => b.qv - a.qv)
let rows: CoinRow[] = (() => { const v = recall<MapResp>('/api/marketmap'); return v?.nodes?.length ? toRows(v) : [] })() // 搜索框一打开就有（lib/cache.ts）
let at = 0
let loading = false
const subs = new Set<() => void>()

async function load() {
  if (loading || Date.now() - at < 120_000) return
  loading = true
  try {
    const r = await fetch('/api/marketmap', { cache: 'no-store' })
    if (r.ok) {
      const v = await r.json() as MapResp
      if (v.nodes?.length) {
        remember('/api/marketmap', v)
        rows = toRows(v)
        at = Date.now()
        subs.forEach((f) => f())
      }
    }
  } catch { /* 下次打开再取 */ } finally { loading = false }
}

export function useMarketList(): CoinRow[] {
  useEffect(() => { void load() }, [])
  return useSyncExternalStore((f) => { subs.add(f); return () => subs.delete(f) }, () => rows)
}
