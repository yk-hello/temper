// 通知（模块 7，D85）：站内通知、开关、到价提醒、Telegram 绑定。
import { useEffect, useState } from 'react'
import { evLong } from './calendar'
import { acctName, tr, type Key } from './i18n'
import { fmtPrice } from './format'
import { locDec, numLocale } from './tz'
import { toastNotice } from './privacy'
import { api } from './session'

export interface NoticeItem { id: number; kind: string; params: Record<string, string> | null; t: number; read: boolean }
export interface PrefRow { kind: string; site: boolean; tg: boolean; wh: boolean; mail: boolean; tg_ok: boolean }
/** 邮件（18 第 8.2 条）：这个部署配了发信邮箱才有。to 是验证过的邮箱，空 = 还没验证 */
export interface MailState { to: string; email: string }
/** 对外 Webhook（18 第 6.3 条）：地址和最后一次发送的结果 */
export interface WebhookState { url: string; last_at: number; last_status: number; last_err?: string }
export interface TgState { enabled: boolean; linked: boolean; bot: string }
export interface PriceAlert { id: number; instrument: string; px: number; up: boolean; created: number }

/** 这些事关用户的钱，在列表里标出来，站内弹提示 */
export const isSafety = (k: string) => k === 'liq_warn' || k === 'margin_high' || k === 'discipline_lock' || k === 'login_new' || k === 'live_recon'

const baseOf = (inst?: string) => (inst ?? '').replace(/-(PERP|SPOT)$/, '')

function botWhy(code: string): string {
  const k = `bt.r.${code}` as Key
  const v = tr(k)
  return v === k ? tr('nt.botOther') : v
}

/** 一条通知的文字。字段由服务器给，语言在这里决定，切语言不用重新发 */
export function noticeText(n: NoticeItem): string {
  const p = n.params ?? {}
  const coin = baseOf(p.inst)
  const acct = acctName(p.acct ?? '')
  switch (n.kind) {
    case 'liq_warn':
      if (p.done === '1' && !p.px) return tr('nt.liqCross', { acct, loss: p.loss })
      return p.done === '1'
        ? tr('nt.liqDone', { coin, px: px(p.px), loss: p.loss })
        : tr('nt.liqWarn', { coin, px: px(p.px), dist: p.dist })
    case 'margin_high': return tr('nt.margin', { acct, ratio: p.ratio })
    case 'discipline_lock': return tr('nt.lock', { acct })
    case 'login_new': return p.event === 'tp_reset' ? tr('nt.tpReset') : tr('nt.login', { device: p.device ?? '', ip: p.ip ? tr('nt.loginIp', { ip: p.ip }) : '' }) // 二级密码被重置也是这一类（D107）
    case 'live_recon': return reconText(p, coin)
    case 'strategy_signal': return stratText(p)
    case 'daily_recap': return tr('nt.recap', { acct, day: p.day ?? '', closed: p.closed ?? '0', wins: p.wins ?? '0', losses: p.losses ?? '0', realized: signed(p.realized), fees: amt(p.fees ?? '0'), funding: signed(p.funding), net: signed(p.net), fills: p.fills ?? '0' })
    case 'bot_error': return tr('nt.bot', { coin, why: botWhy(p.reason) })
    case 'price_alert': return tr(p.dir === 'up' ? 'nt.priceUp' : 'nt.priceDown', { coin, px: px(p.px) })
    case 'rule_alert': return ruleNotice(p, coin)
    case 'stop_hit': return tr(p.what === 'tp' ? 'nt.tp' : 'nt.sl', { coin, px: px(p.px) })
    case 'fill': return tr(p.side === 'buy' ? 'nt.bought' : 'nt.sold', { coin, qty: p.qty, px: px(p.px) })
    case 'follow_stop': return tr('nt.follow')
    case 'whale': return tr(`nt.whale.${p.what}` as Key, { coin: p.coin, value: p.value })
    case 'econ': return tr('nt.econ', { name: evLong(p.key) })
    case 'highlight': return tr(p.side === 'short' ? 'nt.hlShort' : 'nt.hlLong', { coin, roe: p.roe, lev: p.lev })
    default: return n.kind
  }
}

// 服务器给的是原始数字串：价格、金额按界面的格式写（千位分隔、正经的负号），和别处一样（11-17 走查："离强平价 64103.6""已实现 -0.25"）
const px = (v?: string) => (v && Number.isFinite(Number(v)) ? fmtPrice(v) : v ?? '')
const amt = (v: string) => { const n = Number(v); return Number.isFinite(n) ? Math.abs(n).toLocaleString(numLocale(), { minimumFractionDigits: 2, maximumFractionDigits: 2 }) : v }
const signed = (v?: string) => { const n = Number(v); if (!v || !Number.isFinite(n)) return v ?? ''; const s = amt(v); return (Number(s.replace(/[^\d]/g, '')) === 0 ? '' : n > 0 ? '+' : '−') + s }

/** 交易所账户对账发现对不上（18 第 6.4 条，后端 live/recon.go） */
function reconText(p: Record<string, string>, coin: string): string {
  const account = p.account ?? ''
  const trade = tr(p.side === 'buy' ? 'nt.bought' : 'nt.sold', { coin, qty: p.qty, px: px(p.px) })
  switch (p.what) {
    case 'external': return tr('nt.rc.external', { account, trade })
    case 'liq': return tr('nt.rc.liq', { account, coin, trade })
    case 'adl': return tr('nt.rc.adl', { account, coin, trade })
    case 'money': {
      const parts = (['transfer', 'insurance', 'rebate'] as const).filter((k) => p[k]).map((k) => tr(`nt.rc.m.${k}`, { v: signed(p[k]) }))
      const why = parts.length
        ? tr('nt.rc.why', { list: parts.join(tr('nt.rc.sep')) }) + (p.rest ? tr('nt.rc.rest', { v: signed(p.rest) }) : '') + tr('nt.rc.end')
        : tr('nt.rc.noWhy')
      return tr('nt.rc.money', { account, amount: signed(p.amount), why })
    }
    case 'fail': return tr('nt.rc.fail', { account, err: p.err ?? '' })
    case 'ok': return tr('nt.rc.ok', { account })
    default: return tr('ntk.live_recon')
  }
}

/** 自己的策略发了新的目标仓位（18 第 8.4 条）：平台只转达。targets = "品种:权重:信心,…" */
function stratText(p: Record<string, string>): string {
  const parts = (p.targets ?? '').split(',').filter(Boolean).map((x) => {
    const [inst, w, conf] = x.split(':')
    const n = Number(w)
    const coin = baseOf(inst)
    const pct = String(+(Math.abs(n) * 100).toFixed(4))
    const s = n === 0 ? tr('nt.st.flat', { coin }) : tr(n > 0 ? 'nt.st.long' : 'nt.st.short', { coin, pct })
    return conf && conf !== '0' ? s + tr('nt.st.conf', { c: conf }) : s
  })
  const more = Number(p.more) > 0 ? tr('nt.st.more', { n: p.more }) : ''
  const why = (tr('nt.st.lang') === 'en' ? p.reason_en || p.reason : p.reason) ?? ''
  return tr('nt.st', { strategy: p.strategy ?? '', list: parts.join(tr('nt.st.sep')) + more, why: why ? tr('nt.st.why', { why }) : '' })
}

let lastId = 0 // 换页时顶栏会重新挂载，记在外面，不重复提示

/** 顶栏用：每 10 秒看一次，页面在后台时不查 */
export function useNotices(on: boolean) {
  const [d, setD] = useState<{ items: NoticeItem[]; unread: number }>({ items: [], unread: 0 })
  const load = async () => {
    try {
      const r = await api<{ items: NoticeItem[]; unread: number }>('GET', '/api/notify')
      setD(r)
      const top = r.items[0]?.id ?? 0
      if (lastId) {
        for (const n of [...r.items].reverse()) {
          if (n.id > lastId && (isSafety(n.kind) || n.kind === 'price_alert' || n.kind === 'rule_alert')) toastNotice(noticeText(n))
          if (n.id > lastId && n.kind === 'highlight') window.dispatchEvent(new CustomEvent('qs:highlight', { detail: n.params ?? {} }))
        }
      }
      lastId = Math.max(lastId, top)
      if (!lastId) lastId = 1 // 还没有通知：下一条就是新的
      return r
    } catch { return null /* 下一轮再取 */ }
  }
  useEffect(() => {
    if (!on) return
    void load()
    const id = window.setInterval(() => { if (!document.hidden) void load() }, 10_000)
    return () => window.clearInterval(id)
  }, [on]) // eslint-disable-line react-hooks/exhaustive-deps
  return { ...d, reload: load, markRead: async () => {
    setD((x) => ({ ...x, unread: 0, items: x.items.map((n) => ({ ...n, read: true })) }))
    try { await api('POST', '/api/notify/read') } catch { /* 下一轮会对上 */ }
  } }
}

export const loadPrefs = () => api<{ kinds: PrefRow[]; tg: TgState; webhook: WebhookState | null; mail: MailState | null }>('GET', '/api/notify/prefs')
export const savePref = (r: Pick<PrefRow, 'kind' | 'site' | 'tg' | 'wh' | 'mail'>) => api('POST', '/api/notify/prefs', { kind: r.kind, site: r.site, tg: r.tg, wh: r.wh, mail: r.mail })
export const setWebhook = (url: string, rotate = false) => api<{ secret: string }>('POST', '/api/notify/webhook', { url, rotate })
export const delWebhook = () => api('DELETE', '/api/notify/webhook')
export const testWebhook = () => api<{ status: number; error: string }>('POST', '/api/notify/webhook/test')
export const linkTelegram = () => api<{ url: string }>('POST', '/api/notify/telegram/link')
export const unlinkTelegram = () => api('POST', '/api/notify/telegram/unlink')
// ---------- 自定义提醒条件（18 第 10.4 条，后端 notify/rules.go） ----------

export type RuleField = 'px' | 'chg5m' | 'chg1h' | 'chg24h' | 'funding' | 'oi1h' | 'oi24h' | 'rv'
export const RULE_FIELDS: RuleField[] = ['px', 'chg5m', 'chg1h', 'chg24h', 'funding', 'oi1h', 'oi24h', 'rv']
export interface RuleCond { field: RuleField; op: '>' | '<'; value: number }
export interface AlertRule { id: number; name: string; instrument: string; join: 'all' | 'any'; conds: RuleCond[]; once: boolean; cooldown: number; created: number; last_hit: number; off: boolean; now?: boolean }
/** 字段的单位：价格是 USDT，倍数是 ×，其余是 % */
export const ruleUnit = (f: RuleField) => (f === 'px' ? 'USDT' : f === 'rv' ? '×' : '%')
export const loadRules = () => api<{ rules: AlertRule[]; max: number }>('GET', '/api/notify/rules')
export const addRule = (r: Omit<AlertRule, 'id' | 'created' | 'last_hit' | 'off' | 'now'>) => api<AlertRule>('POST', '/api/notify/rules', r)
export const delRule = (id: number) => api('DELETE', `/api/notify/rules/${id}`)
export const setRuleOff = (id: number, off: boolean) => api('POST', `/api/notify/rules/${id}/off`, { off })
/** 一条规则写成一句话：资金费 > 0.05% 且 24 小时涨跌 > 5% */
export function ruleSummary(r: Pick<AlertRule, 'conds' | 'join'>): string {
  return r.conds.map((c) => `${tr(`rf.${c.field}` as Key)} ${c.op === '>' ? '>' : '<'} ${locDec(String(c.value))}${c.field === 'px' ? '' : ruleUnit(c.field)}`).join(tr(r.join === 'any' ? 'rl.or' : 'rl.and'))
}

/** 条件提醒的通知：后端给的是 "funding>0.05=0.0612;chg24h>5=6.3"（字段、方向、线、此刻的值） */
function ruleNotice(p: Record<string, string>, coin: string): string {
  const parts = (p.conds ?? '').split(';').map((c) => {
    const [lhs, now] = c.split('=')
    const op = lhs?.includes('<') ? '<' : '>'
    const [field, line] = (lhs ?? '').split(op) as [RuleField, string]
    if (!field || now == null) return ''
    const v = Number(now)
    const cur = field === 'px' ? String(v) : field === 'rv' ? v.toFixed(1) : field === 'funding' ? v.toFixed(4) : v.toFixed(2)
    const unit = field === 'px' ? '' : ruleUnit(field)
    return tr(op === '>' ? 'rl.hitAbove' : 'rl.hitBelow', { f: tr(`rf.${field}` as Key), v: locDec(cur) + unit, line: locDec(line ?? '') + unit })
  }).filter(Boolean)
  return tr('rl.notice', { name: p.name || tr('ntk.rule_alert'), coin, what: parts.join(tr('rl.sep')) })
}

export const loadAlerts = () => api<{ alerts: PriceAlert[] }>('GET', '/api/notify/alerts').then((r) => r.alerts ?? [])
export const addAlert = (instrument: string, px: number) => api<PriceAlert>('POST', '/api/notify/alerts', { instrument, px })
export const delAlert = (id: number) => api('DELETE', `/api/notify/alerts/${id}`)
