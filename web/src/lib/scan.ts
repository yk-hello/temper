// 筛选器、异动、今日（盘前准备和市场总览）的数据。所有人共用后端算好的结果。
import { recall, remember } from './cache'
import { useEffect, useState } from 'react'

export interface ScanRow {
  s: string; i: string; sec?: string; px: number; chg5m: number | null; chg1h: number | null; chg24h: number; qv: number; rv?: number
  funding?: number; oi?: number; oi1h?: number; oi24h?: number; f?: boolean
}
export type ScanKind = 'up' | 'down' | 'volume' | 'funding_hi' | 'funding_lo' | 'oi_up' | 'oi_down'
export interface ScanEvent { t: number; s: string; i: string; kind: ScanKind; v: number; px: number }
export interface ScanData { t: number; warm: boolean; rows: ScanRow[]; events: ScanEvent[] }

export interface Level { s: string; i: string; px: number; prev_high: number; prev_low: number; prev_close: number; day_open: number; week_open: number; week_high: number; week_low: number }
export interface TodayData {
  t: number
  fear?: { value: number; label: string; prev: number; week: number; updated: number; source: string }
  global?: { mcap: number; mcap_chg: number; btc: number; eth: number; source: string }
  lsr?: { ratio: number; long: number; prev: number; t: number }
  breadth: { up: number; down: number; flat: number; median: number; qv: number; funding_w?: number }
  levels: Level[]; movers: ScanRow[]; events: ScanEvent[]; missing?: string[]
  // 场外和宏观（18 第 10.2 条）
  premium?: { now: number; avg24: number; hours: number[] }
  iv?: { btc: number; btc_prev: number; eth: number; eth_prev: number }
  stable?: { total: number; day: number; week: number }
  macro?: { id: 'dxy' | 'us10y' | 'spx'; value: number; prev: number; date: string }[]
  macro_off?: boolean
}

/** 进页面先显示上一次的（lib/cache.ts），后台取到新的再换。 */
function usePoll<T>(url: string, every: number): T | null {
  const [v, setV] = useState<T | null>(() => recall<T>(url))
  useEffect(() => {
    let alive = true
    const load = () => fetch(url).then((r) => (r.ok ? r.json() : null)).then((d) => { if (d) remember(url, d); if (alive && d) setV(d as T) }).catch(() => {})
    load()
    const id = window.setInterval(load, every)
    return () => { alive = false; window.clearInterval(id) }
  }, [url, every])
  return v
}

export const useScan = () => usePoll<ScanData>('/api/scan', 15_000)
export const useToday = () => usePoll<TodayData>('/api/today', 60_000)

// ---------- 自选（只存在这台设备的浏览器里） ----------
const STAR = 'qs.stars'
export function loadStars(): Set<string> {
  try { return new Set(JSON.parse(localStorage.getItem(STAR) || '[]') as string[]) } catch { return new Set() }
}
export function saveStars(s: Set<string>) {
  try { localStorage.setItem(STAR, JSON.stringify([...s])) } catch { /* 隐私模式：只在这次打开里有效 */ }
}
