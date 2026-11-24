// 策略跟随（模块 3）的前端数据层：策略、战绩、决策、我的跟随。
// 权限都在服务器判断（3.13）：没开启服务时决策类接口回 401 / 403，界面按状态码写一句话，不是空白。
import { recall, remember } from './cache'
import { useEffect, useState, useSyncExternalStore } from 'react'
import { api, useSession } from './session'
import { refreshTrade } from './trade'

// ---------- 轮询 ----------

export interface Polled<T> { data: T | null; status: number }

/** 定时取一个接口。status 是最近一次的 HTTP 状态（0 = 还没回来），data 只留最近一次成功的。
 *  进页面先显示上一次的结果（lib/cache.ts），取到新的再换，不从"加载中"开始。 */
export function usePoll<T>(url: string | null, ms: number): Polled<T> {
  const [st, setSt] = useState<Polled<T>>(() => { const d = recall<T>(url); return { data: d, status: d ? 200 : 0 } })
  useEffect(() => {
    const d = recall<T>(url)
    setSt({ data: d, status: d ? 200 : 0 })
    if (!url) return
    let alive = true
    const tick = async () => {
      if (document.hidden) return
      try {
        const r = await fetch(url, { cache: 'no-store' })
        if (!alive) return
        if (r.ok) {
          const data = await r.json() as T
          remember(url, data)
          if (alive) setSt({ data, status: r.status })
        } else setSt((s) => ({ data: r.status === 401 || r.status === 403 ? null : s.data, status: r.status }))
      } catch { /* 下次再试 */ }
    }
    void tick()
    const id = window.setInterval(tick, ms)
    return () => { alive = false; window.clearInterval(id) }
  }, [url, ms])
  return st
}

// ---------- 策略 ----------

export interface StrategyStatus {
  strategy_id: string; online: boolean; last_heartbeat: number; heartbeat_status: string; note?: string
  halted?: string; halt_reason?: string; circuit_breaker: boolean; sample: boolean
}

export interface ClosedTrade {
  instrument: string; side: 'long' | 'short'; open_time: number; close_time?: number; qty: string; entry: string; exit: string
  gross: string; fees: string; funding: string; net: string; closed_by?: string
}

export interface Performance {
  strategy_id: string; since: number; initial: string; nav: string; fees: string; funding: string; liq_fees: string
  return: number; max_drawdown: number; sharpe: number | null; trades: number; wins: number; win_rate: number | null; days: number
  followers: number; follow_capital: string
  curve: { t: number; nav: number }[]; closed: ClosedTrade[]
}

export interface PositionState {
  instrument: string; target_weight: string; actual_weight: string; qty: string; entry_price: string; mark_price: string
  unrealized_pnl: string; target_set_at?: string; horizon_expired: boolean; closed_by?: string
}
export interface StrategyState {
  locked: boolean; nav_history: { t: number; nav: number }[] | null
  state: { strategy_id: string; nav: string; cash: string; circuit_breaker: boolean; halted?: string; positions: PositionState[]; time: number }
}

export interface TargetMsg {
  instrument: string; weight: string; confidence: number; horizon_min: number; ref_price: string
  rationale?: {
    summary?: { zh?: string; en?: string }
    factors?: { key: string; label: string; score: number }[]
    evidence?: { kind?: string; title?: string; source?: string; url?: string; published_at?: string; excerpt?: string }[]
  }
}
export interface Execution {
  instrument: string; prev_weight: string; target_weight: string; achieved_weight: string; state: string
  avg_price?: string; slippage_bps?: string; fee: string; start?: number; end?: number
}
export interface Decision {
  id: string; strategy_id: string; type: string; status: string; code?: string; received_at: number
  message: { targets?: TargetMsg[]; action?: string; reason?: { zh?: string } }
  executions: Execution[]
}
export interface TradeLeg {
  time: number; side: 'buy' | 'sell'; qty: string; px: string; fee: string; maker: boolean; realized: string
  signal?: string; ref_px: string; slip_bps: string; closed_by?: string
}
export interface TradeDetail extends ClosedTrade {
  legs: TradeLeg[]
  fundings: { time: number; rate: string; mark: string; qty: string; amount: string }[]
  decisions: { id: string; weight: string; confidence: number; rationale?: TargetMsg['rationale'] }[]
  confidence: number | null
}
export interface Bucket { lo: number; hi: number; trades: number; wins: number; enough: boolean; win_rate: number | null; avg_net: number | null }

// ---------- 我的跟随：全站一份，有人在用时每 3 秒刷新 ----------

export interface FollowParams { alloc: string; max_loss: string; max_leverage: number; per_inst: string; blacklist: string[]; ratio?: string }
export interface ExecRec {
  id: number; msg?: string; instrument: string; time: number; end?: number; weight: string; applied: string; note?: string; state: string; reject?: string
  side?: 'buy' | 'sell'; qty: string; filled: string; avg_px: string; fee: string; model_avg: string; slip_bps: string
}
export interface Follow {
  manual?: Record<string, number> // 手动管理的品种（D80）
  id: number; account: number; strategy: string; params: FollowParams; status: 'active' | 'paused' | 'closing' | 'stopped'; reason?: string
  started: number; stopped?: number; nav: string; pnl: string; stop_at: string; records: ExecRec[]
}

export const isRunning = (f: Follow) => f.status === 'active' || f.status === 'paused'

let follows: Follow[] | null = null
let users = 0
let timer = 0
const subs = new Set<() => void>()
function emit() { subs.forEach((f) => f()) }

export async function refreshFollows() {
  try {
    const r = await api<{ follows: Follow[] }>('GET', '/api/follows')
    follows = r.follows
  } catch { /* 没登录或者后端还没好：下次再试 */ }
  emit()
}

/** 我的跟随。没登录时是空列表。 */
export function useFollows(): Follow[] | null {
  const s = useSession()
  const inn = s.state === 'in'
  useEffect(() => {
    if (!inn) { follows = null; emit(); return }
    users++
    if (users === 1) {
      void refreshFollows()
      timer = window.setInterval(() => { if (!document.hidden) void refreshFollows() }, 3000)
    }
    return () => {
      users--
      if (users === 0) window.clearInterval(timer)
    }
  }, [inn])
  const v = useSyncExternalStore((f) => { subs.add(f); return () => subs.delete(f) }, () => follows)
  return inn ? v : []
}

export interface StartReq { strategy: string; account?: number; alloc: string; max_loss: string; max_leverage: number; per_inst: string; blacklist: string[]; ratio?: string }

async function after<T>(p: Promise<T>): Promise<T> {
  try { return await p } finally { void refreshFollows(); refreshTrade() }
}
export const startFollow = (r: StartReq) => after(api<Follow>('POST', '/api/follows', { ...r, confirm: true }))
export const pauseFollow = (id: number) => after(api<Follow>('POST', `/api/follows/${id}/pause`, {}))
export const resumeFollow = (id: number) => after(api<Follow>('POST', `/api/follows/${id}/resume`, {}))
export const stopFollow = (id: number, mode: 'close' | 'keep') => after(api<Follow>('POST', `/api/follows/${id}/stop`, { mode }))
export interface PlanItem { instrument: string; side: 'buy' | 'sell'; qty: string; notional: string }
export const resumePlan = (id: number) => api<{ trades: PlanItem[] }>('GET', `/api/follows/${id}/resume-plan`).then((r) => r.trades)

// ---------- 向导的开关（注册后第一次进来出现，以后在帮助里再打开，3.18） ----------

let wizard = false
const wizSubs = new Set<() => void>()
const SEEN = 'qs.wizardSeen', PENDING = 'qs.wizardPending'
export function openWizard() { wizard = true; wizSubs.forEach((f) => f()) }
export function closeWizard() {
  wizard = false
  try { localStorage.setItem(SEEN, '1'); localStorage.removeItem(PENDING) } catch { /* 忽略 */ }
  wizSubs.forEach((f) => f())
}
export function useWizardOpen(): boolean {
  return useSyncExternalStore((f) => { wizSubs.add(f); return () => wizSubs.delete(f) }, () => wizard)
}
/** 刚注册：下一次进交易页或 AI 页时打开向导 */
export function markWizardPending() {
  try { if (!localStorage.getItem(SEEN)) localStorage.setItem(PENDING, '1') } catch { /* 忽略 */ }
}
export function wizardPending(): boolean {
  try { return localStorage.getItem(PENDING) === '1' } catch { return false }
}
