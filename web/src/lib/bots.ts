// 交易机器人（D41、D83）。
import { useEffect, useRef, useState } from 'react'
import { api } from './session'
import { toastNotice } from './privacy'
import { latinWord, tr, type Key } from './i18n'

export type BotKind = 'spot_grid' | 'perp_grid' | 'dca' | 'dip' | 'plan'

export interface Rung { px: string; usd: string; avg: string; tp: string; down_pc: string; sum: string }
export interface BotPlan {
  lines?: string[]; qty?: string; sell_qty?: string; below: number; above: number; init_side?: string; init_qty?: string
  net_lo?: string; net_hi?: string; net_lo_pct: string; net_hi_pct: string; fee_per_grid: string; need: string; worst_loss: string
  liq_px?: string; ladder?: Rung[]; warn: string[]
}
export interface BotRec { t: number; kind: string; side?: string; px: string; qty: string; net: string; note?: string }
export interface BotGrid { i: number; side: 'buy' | 'sell'; order?: string; entry: string; done: number; net: string }
export interface BotRung { px: string; usd: string; order?: string; filled: boolean }
export interface Bot {
  id: number; account: number; kind: BotKind; params: BotParams; status: 'running' | 'waiting' | 'paused' | 'stopped'; reason?: string
  created: number; stopped?: number; last_ok: number; plan?: BotPlan; grids?: BotGrid[]; rungs?: BotRung[]; tp_order?: string; round: number
  qty: string; cost: string; avg: string; next_at?: number; count: number; spent: string; bought: string
  realized: string; pairs: number; fills: number; recs: BotRec[]
  price: string; unreal: string; range?: 'below' | 'inside' | 'above'; grid_now: number
}
export interface BotParams {
  instrument: string; lower?: string; upper?: string; grids?: number; geo?: boolean; invest?: string; dir?: string; leverage?: number
  stop_loss?: string; take_profit?: string; side?: string; first?: string; add?: string; step_pct?: string; mult?: string; max_adds?: number
  tp_pct?: string; sl_pct?: string; repeat?: boolean; trigger_px?: string; drop_pct?: string; amount?: string; every?: string; times?: number
}

let cache: { bots: Bot[]; order: string[] } | null = null
const subs = new Set<() => void>()
let timer = 0
async function load() {
  try {
    const next = await api<{ bots: Bot[]; order: string[] }>('GET', '/api/bots')
    // 手动单成交、机器人被系统暂停时当场说一声（11-17 走查：只在机器人列表里写着"已暂停"，下单那一刻什么都没说）
    if (cache) {
      for (const b of next.bots) {
        const was = cache.bots.find((x) => x.id === b.id)
        if (was && was.status !== 'paused' && b.status === 'paused' && b.reason && b.reason !== 'USER') { // 自己点的暂停那边已经说过了
          const why = tr(`bt.r.${b.reason}` as Key)
          toastNotice(tr('bt.pausedNow', { coin: b.params.instrument.replace(/-(PERP|SPOT)$/, ''), kind: latinWord(tr(`bt.k.${b.kind}` as Key)).trimStart(), why: why.startsWith('bt.r.') ? b.reason : why }))
        }
      }
    }
    cache = next
    subs.forEach((f) => f())
  } catch { /* 没登录或者后端在重启 */ }
}
export const refreshBots = () => { void load() }

export function useBots(on: boolean): { bots: Bot[]; order: string[] } | null {
  const [, setN] = useState(0)
  useEffect(() => {
    if (!on) return
    const f = () => setN((n) => n + 1)
    subs.add(f)
    if (!timer) { void load(); timer = window.setInterval(load, 1500) }
    return () => { subs.delete(f); if (!subs.size) { window.clearInterval(timer); timer = 0 } }
  }, [on])
  return on ? cache : null
}

/** 参数变了就重新算（停顿 250 毫秒） */
export function usePlan(kind: BotKind | null, params: BotParams | null): { plan: BotPlan | null; err: { code: string; params?: Record<string, string> } | null } {
  const [out, setOut] = useState<{ plan: BotPlan | null; err: { code: string; params?: Record<string, string> } | null }>({ plan: null, err: null })
  const key = kind && params ? JSON.stringify([kind, params]) : ''
  const seq = useRef(0)
  useEffect(() => {
    if (!key) { setOut({ plan: null, err: null }); return }
    const s = ++seq.current
    const id = window.setTimeout(async () => {
      try {
        const plan = await api<BotPlan>('POST', '/api/bots/preview', { kind, params })
        if (s === seq.current) setOut({ plan, err: null })
      } catch (e) {
        const x = e as { code?: string; params?: Record<string, string> }
        if (s === seq.current) setOut({ plan: null, err: { code: x.code ?? 'INTERNAL_ERROR', params: x.params } })
      }
    }, 250)
    return () => window.clearTimeout(id)
  }, [key]) // eslint-disable-line react-hooks/exhaustive-deps
  return out
}

/** 没填的数字字段发 0，后端按没设处理 */
export function cleanParams(p: BotParams): BotParams {
  const out: Record<string, unknown> = { ...p }
  for (const k of ['lower', 'upper', 'invest', 'stop_loss', 'take_profit', 'first', 'add', 'step_pct', 'mult', 'tp_pct', 'sl_pct', 'trigger_px', 'drop_pct', 'amount']) {
    if (out[k] === '' || out[k] == null) out[k] = '0'
  }
  return out as unknown as BotParams
}
