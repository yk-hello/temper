// 复盘（模块 5）的数据类型和小工具。后端见 backend/internal/journal。
export interface Leg { t: number; side: 'buy' | 'sell'; qty: number; px: number; fee: number; ref?: number; source?: string; order_id: string }
export interface StopPoint { t: number; px: number; kind: 'set' | 'move' | 'cancel' }
export interface Note { tags: string[]; note: string; emotion: string; playbook: number; updated?: number }
export interface ShotMeta { id: number; mime: string; size: number; created: number }
export interface JTrade {
  id: string; instrument: string; side: 'long' | 'short'; open_time: number; close_time: number; hold_ms: number
  qty: number; entry: number; exit: number; gross: number; fees: number; funding: number; net: number
  slip: number; slip_known: boolean; origin: 'manual' | 'ai' | 'mixed' | 'bot' | 'signal'; closed_by?: string
  stop: number; risk: number; r: number | null; violations: string[]; stops: StopPoint[]; legs: Leg[]
  note?: Note; shots?: ShotMeta[]; playbook?: number
}
export interface Stat {
  key: string; n: number; wins: number; win_rate: number; net: number; expectancy: number; avg_win: number; avg_loss: number
  payoff: number | null; profit_factor: number | null; avg_r: number | null; r_count: number; small: boolean
}
export interface Waterfall { ideal: number; slip: number; gross: number; fees: number; funding: number; net: number; unknown_slip: number }
export interface JStats {
  all: Stat; groups: Record<string, Stat[]>; calendar: { day: string; net: number; n: number }[]
  underwater: { t: number; eq: number; dd: number }[]; r_dist: { lo: number; hi: number; n: number }[]; no_r: number
  waterfall: Waterfall; violations: Record<string, { n: number; net: number }>
}
export interface Excursion { id: string; mae: number; mfe: number; mae_usd: number; mfe_usd: number; mae_r: number | null; mfe_r: number | null; source: string; net: number; r: number | null }
export interface Finding { code: string; n: number; of: number; delta: number; args?: Record<string, number>; key?: string; small: boolean }
export interface Playbook { id: number; name: string; entry: string; stop: string; target: string }

export type Origin = 'manual' | 'ai' | 'bot' | 'signal' | 'all'

export const tzOffsetMin = () => -new Date().getTimezoneOffset()

export function holdText(ms: number, t: (k: 'rv.h.d' | 'rv.h.h' | 'rv.h.m' | 'rv.h.s', a: { n: number }) => string): string {
  if (ms >= 86400_000) return t('rv.h.d', { n: Math.round(ms / 8640_000) / 10 })
  if (ms >= 3600_000) return t('rv.h.h', { n: Math.round(ms / 360_000) / 10 })
  if (ms >= 60_000) return t('rv.h.m', { n: Math.round(ms / 60_000) })
  return t('rv.h.s', { n: Math.max(1, Math.round(ms / 1000)) })
}

export function median(v: number[]): number | null {
  if (!v.length) return null
  const s = [...v].sort((a, b) => a - b)
  return s[Math.floor(s.length / 2)]
}

/** 练习用的盈亏：手算样例在 tools/test-flow.mjs。费率按吃单 0.05% 算，开平各一次 */
export const PRACTICE_FEE = 0.0005
export function practicePnl(side: 'long' | 'short', entry: number, exit: number, notional: number): { gross: number; fee: number; net: number } {
  const qty = notional / entry
  const gross = (side === 'long' ? exit - entry : entry - exit) * qty
  const fee = (notional + qty * exit) * PRACTICE_FEE
  return { gross, fee, net: gross - fee }
}
