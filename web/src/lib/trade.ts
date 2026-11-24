// 手动交易（模块 2）的前端数据层：当前模拟账户、账户全貌轮询、品种规格、下单动作、下单区共享状态。
import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import { api, ApiError, useSession, type PaperAccount } from './session'
import { acctName, errText, fmtTime, reasonText, tr, type Key } from './i18n'
import { toastNotice } from './privacy'
import { clearLive, refreshLive, useLive } from './live'

export interface Spec {
  instrument: string; symbol: string; tick: string; step: string; min_qty: string; min_notional: string
  price_up: string; price_down: string; brackets: { floor: string; mmr: string; max_leverage: number }[]; source: string; bracket_source?: string
  /** 条件单价格保护的阈值（币安 triggerProtect，BTC 0.05 = 5%） */
  trigger_protect?: string
}
export interface Order {
  id: string; instrument: string; side: 'buy' | 'sell'; type: 'market' | 'limit' | 'stop_market' | 'take_profit_market' | 'cond_market' | 'cond_limit'; tif?: string
  trigger_by?: 'mark' | 'last' | 'index'; trigger_dir?: 'up' | 'down'
  /** 币安的另外几条规则（D103）：GTD 到期时刻、条件单价格保护、按盘口定的价 */
  good_till?: number; price_protect?: boolean; price_match?: string
  price: string; trigger_px: string; qty: string; filled: string; avg_px: string; fee: string; reduce_only: boolean; close_position: boolean
  tp: string; sl: string; status: string; reason?: string; source: string; parent?: string; created: number; updated: number
  queue_ahead?: string // 限价挂单前面还排着多少
}
export interface PosView {
  instrument: string; side: 'long' | 'short'; qty: string; entry: string; mark: string; notional: string; upnl: string; roe: string
  margin: string; maint: string; liq_px: string; mode: 'cross' | 'isolated'; leverage: number; tp: string; sl: string; realized: string; opened: number
  /** 自动减仓排队 0–4（越大越先被减）；adl_est = 模拟盘按收益率和杠杆估的 */
  adl?: number; adl_est?: boolean
}
export interface Notice { loss?: string; seq: number; time: number; kind: 'liquidation' | 'tp' | 'sl' | 'canceled' | 'cond' | 'cond_failed' | 'takeover' | 'takeover_end' | 'discipline_lock' | 'discipline_unlock' | 'gap_replay' | 'live_stream_down'; instrument: string; qty: string; px: string; amount: string; reason?: string; at?: number }
export interface AccountView {
  id: number; archived: boolean; initial: string; wallet: string; equity: string; upnl: string; realized: string; fees: string; funding: string; liq_fees: string; covered?: string; transferable?: string
  available: string; position_margin: string; order_margin: string; maint_margin: string; margin_ratio: string
  positions: PosView[]; orders: Order[]; settings: Record<string, { leverage: number; mode: 'cross' | 'isolated' }>; notices: Notice[]; seq: number
}
export interface Preview {
  qty: string; est_px: string; notional: string; fee: string; maker: boolean; margin: string; available: string; liq_px: string; max_qty: string
  problem?: { code: string; params?: Record<string, string> }
}
export interface Fill { seq: number; time: number; order_id: string; instrument: string; side: 'buy' | 'sell'; qty: string; px: string; fee: string; maker: boolean; realized: string; liquidation?: boolean }
export interface FundingRec { seq: number; time: number; instrument: string; rate: string; mark: string; qty: string; amount: string }

/** 拒单提示（带参数，例如"最小 100 USDT"） */
export function rejectText(e: unknown): string {
  if (e instanceof ApiError) return errText(e.code, e.params)
  return errText('INTERNAL_ERROR')
}

// ---------- 品种规格（只取一次） ----------

let specCache: Record<string, Spec> | null = null
let specPromise: Promise<void> | null = null
const specSubs = new Set<() => void>()
function loadSpecs() {
  if (specPromise) return
  specPromise = api<{ specs: Spec[] }>('GET', '/api/trade/specs').then((r) => {
    specCache = Object.fromEntries(r.specs.map((s) => [s.instrument, s]))
    specSubs.forEach((f) => f())
  }).catch(() => { specPromise = null })
}
/** 再取一次（新点开的品种） */
export function reloadSpecs() {
  api<{ specs: Spec[] }>('GET', '/api/trade/specs').then((r) => {
    if (specCache && r.specs.length === Object.keys(specCache).length) return
    specCache = Object.fromEntries(r.specs.map((s) => [s.instrument, s]))
    specSubs.forEach((f) => f())
  }).catch(() => {})
}
export function useSpecs(): Record<string, Spec> | null {
  loadSpecs()
  return useSyncExternalStore((f) => { specSubs.add(f); return () => specSubs.delete(f) }, () => specCache)
}
export function decimalsOf(step: string): number {
  const i = step.indexOf('.')
  return i < 0 ? 0 : step.replace(/0+$/, '').length - i - 1
}

// ---------- 当前模拟账户 ----------

const ACC_KEY = 'qs.paper'
let selected: number | null = (() => { try { const v = Number(localStorage.getItem(ACC_KEY)); return v > 0 ? v : null } catch { return null } })()
const accSubs = new Set<() => void>()
export function selectAccount(id: number) {
  selected = id
  try { localStorage.setItem(ACC_KEY, String(id)) } catch { /* 忽略 */ }
  accSubs.forEach((f) => f())
}

/** 账户变了（比如开启跟随时自动建了一个）：所有地方重新取列表 */
let accVersion = 0
export function refreshPaperAccounts() {
  accVersion++
  accSubs.forEach((f) => f())
}

/** 交易所账户的环境标记：实盘 / 测试网 / 测试交易所 */
export function acctEnv(a: PaperAccount): string {
  return tr(a.env === 'testnet' ? 'live.envTag.testnet' : a.env === 'fake' ? 'live.envTag.fake' : 'live.envTag.main')
}

/** 下拉框里的账户名：交易所账户后面写明环境（复盘这类没有标记的地方用） */
export function acctLabel(a: PaperAccount): string {
  return a.live ? `${a.name} · ${acctEnv(a)}` : acctName(a.name)
}

/** 登录用户的模拟账户列表和当前选中的那个。withLive = true 时连实盘账户一起（合约交易页、复盘）；
 *  不带实盘的地方（现货、机器人）选中的是实盘账户时，用第一个模拟账户 */
let liveOwner = 0
let accReq: { key: string; p: Promise<PaperAccount[]> } | null = null
export function usePaperAccounts(withLive = false): { list: PaperAccount[] | null; current: PaperAccount | null } {
  const s = useSession()
  const sel = useSyncExternalStore((f) => { accSubs.add(f); return () => accSubs.delete(f) }, () => selected)
  const [list, setList] = useState<PaperAccount[] | null>(null)
  const uid = s.state === 'in' ? s.user.id : 0
  const ver = useSyncExternalStore((f) => { accSubs.add(f); return () => accSubs.delete(f) }, () => accVersion)
  const live = useLive(withLive && !!uid)
  useEffect(() => {
    if (uid && withLive && liveOwner !== uid) { liveOwner = uid; clearLive(); void refreshLive() } // 换了人：不要留着上一个人的
  }, [uid, withLive])
  useEffect(() => {
    if (!uid) { setList(null); return }
    let alive = true
    // 同一时刻好几处要这个列表（顶栏、下单区、复盘……）：只请求一次，大家共用
    const key = `${uid}:${ver}`
    if (accReq?.key !== key) {
      const p = api<{ accounts: PaperAccount[] }>('GET', '/api/paper-accounts').then((r) => r.accounts)
      accReq = { key, p }
      p.catch(() => { if (accReq?.key === key) accReq = null }) // 失败了下次重新请求
    }
    accReq.p.then((l) => { if (alive) setList(l) }).catch(() => {})
    return () => { alive = false }
  }, [uid, ver])
  const all = useMemo(() => {
    if (!list || !withLive || !live || liveOwner !== uid) return list
    return [...list, ...live.accounts.map((a): PaperAccount => ({ id: a.id, name: a.name, initial: '0', created_at: new Date(a.created).toISOString(), live: true, env: a.env, armed: a.armed, confirm: a.confirm }))]
  }, [list, live, withLive, uid])
  const current = all?.find((a) => a.id === sel) ?? all?.find((a) => !a.live) ?? null
  return { list: all, current }
}

// ---------- 账户全貌：服务器实时推送（有变化才发，最快每秒 4 次）；推送断了就每秒轮询；动作之后立即刷新 ----------

const refreshers = new Set<() => void>()
export function refreshTrade() { refreshers.forEach((f) => f()) }

export function useTradeAccount(acct: number | null): { view: AccountView | null; error: string | null; live: boolean } {
  const [view, setView] = useState<AccountView | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [live, setLive] = useState(false)
  const seen = useRef(0)
  const take = useCallback((v: AccountView) => {
    setView(v)
    setError(null)
    // 新的强平、止盈止损、条件单触发：提醒（第一次加载时只记下位置，不提醒旧的）
    const last = v.notices.length ? v.notices[v.notices.length - 1].seq : 0
    if (seen.current && last > seen.current) for (const n of v.notices.filter((x) => x.seq > seen.current && x.kind !== 'liquidation')) toastNotice(noticeText(n)) // 爆仓有常驻提示（3.28）
    seen.current = Math.max(seen.current, last, 1)
  }, [])
  const load = useCallback(async () => {
    if (!acct) return
    try { take(await api<AccountView>('GET', `/api/trade/${acct}`)) } catch (e) { setError(rejectText(e)) }
  }, [acct, take])
  useEffect(() => {
    setView(null)
    setLive(false)
    seen.current = 0
    if (!acct) return
    void load()
    refreshers.add(load)
    let poll = 0
    const startPoll = () => { if (!poll) poll = window.setInterval(load, 1000) }
    const stopPoll = () => { if (poll) { window.clearInterval(poll); poll = 0 } }
    let es: EventSource | null = null
    if (typeof EventSource !== 'undefined') {
      es = new EventSource(`/api/trade/${acct}/stream`)
      es.onmessage = (m) => { try { take(JSON.parse(m.data)); setLive(true); stopPoll() } catch { /* 忽略坏帧 */ } }
      es.onerror = () => { setLive(false); startPoll() } // EventSource 自己会重连；连上之前先轮询
    } else startPoll()
    const fallback = window.setTimeout(() => { if (!es || es.readyState !== 1) startPoll() }, 3000)
    return () => { es?.close(); stopPoll(); window.clearTimeout(fallback); refreshers.delete(load) }
  }, [acct, load, take])
  return { view, error, live }
}

export function noticeText(n: Notice): string {
  const base = n.instrument.replace(/-PERP/g, '')
  if (n.kind === 'liquidation') return n.reason === 'partial' ? tr('notice.liqPartial', { inst: base, q: n.qty }) : tr('notice.liquidation', { inst: base })
  if (n.kind === 'tp') return tr('notice.tp', { inst: base, px: n.px })
  if (n.kind === 'sl') return tr('notice.sl', { inst: base, px: n.px })
  if (n.kind === 'cond') return tr('notice.cond', { inst: base, px: n.px })
  if (n.kind === 'takeover') return tr('notice.takeover', { inst: base })
  if (n.kind === 'takeover_end') return tr('notice.takeoverEnd', { inst: base })
  if (n.kind === 'discipline_lock') return tr('notice.discLock', { rule: tr(`disc.rule.${n.reason}` as Key) })
  if (n.kind === 'discipline_unlock') return tr('notice.discUnlock')
  if (n.kind === 'cond_failed') return tr('notice.condFailed', { inst: base, px: n.px, why: reasonText(n.reason) })
  // 断流补算：行情断了一段，按这段的 K 线补查出来的止损、止盈、强平
  if (n.kind === 'gap_replay') return tr(n.reason === 'liquidation' ? 'notice.gapLiq' : n.reason === 'tp' ? 'notice.gapTp' : 'notice.gapSl', { inst: base, px: n.px, t: n.at ? fmtTime(n.at) : '' })
  return tr('notice.canceled', { inst: base })
}

/** 交易动作：成功后刷新账户；失败抛出带说明的错误 */
export async function act<T>(method: string, path: string, body?: unknown): Promise<T> {
  try {
    return await api<T>(method, path, body)
  } finally {
    refreshTrade()
  }
}

// ---------- 下单区共享状态（点盘口价格填进下单价；B / S 快捷键切换方向） ----------

/** kind：图上右键选的是限价单还是条件单（18 第 6.5 条）；不给时按下单区现在的类型 */
export interface Ticket { price?: string; side?: 'buy' | 'sell'; kind?: 'limit' | 'cond'; n: number }
let ticket: Ticket = { n: 0 }
const ticketSubs = new Set<() => void>()
export function setTicket(t: Omit<Ticket, 'n'>) {
  ticket = { ...t, n: ticket.n + 1 }
  ticketSubs.forEach((f) => f())
}
export function useTicket(): Ticket {
  return useSyncExternalStore((f) => { ticketSubs.add(f); return () => ticketSubs.delete(f) }, () => ticket)
}

/** 按价格单位截断（下单价必须是价格单位的整数倍） */
export function toTick(p: string | number, tick: string): string {
  if (!Number.isFinite(Number(p))) return String(p) // 0,5 这种看不准的原样留着，不变成 NaN（numIn）
  const d = decimalsOf(tick)
  const t = Number(tick)
  const v = Math.round(Number(p) / t) * t
  return v.toFixed(d)
}
