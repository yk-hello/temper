// 当前登录用户和账户接口（模块 1）。
// 所有改动类请求带 X-QS-Client 头（配合 SameSite=Strict 的会话 Cookie，防跨站请求伪造）。
import { forgetPrivate } from './cache'
import { useSyncExternalStore } from 'react'
import { getLang, langHooks, setLang, type Lang } from './i18n'

export interface User {
  id: number
  /** 账户名（D113）：登录用 */
  name?: string
  /** 所有者：这台第一个注册的账号，只有他能进"系统"页（D113） */
  owner?: boolean
  /** 绑定的邮箱：找回密码、收提醒（D113 起不用来登录） */
  email?: string
  email_verified: boolean
  wallet?: string
  lang: Lang
  has_trade_password: boolean
  has_password?: boolean
  /** 开了二次验证；防钓鱼码（18 第 6.2 条） */
  two_fa?: boolean
  anti_phish?: string
}

export interface PaperAccount {
  id: number
  name: string
  initial: string
  created_at: string
  /** 模拟账户现在的手续费率（小数，0.0005 = 0.05%）；custom_fee = 自己设过（D103） */
  taker_fee?: string
  maker_fee?: string
  custom_fee?: boolean
  /** 实盘账户（D89）：接的是用户自己的交易所账户，下单是真实下单 */
  live?: boolean
  env?: 'main' | 'testnet' | 'fake'
  /** 用户确认过"这是真钱"，才能下单 */
  armed?: boolean
  /** 实盘下单前弹确认（D96：默认要，关要二级密码） */
  confirm?: boolean
}

export class ApiError extends Error {
  constructor(public code: string, public status: number, public params?: Record<string, string>, public body?: unknown) { super(code) }
}

export async function api<T = unknown>(method: string, path: string, body?: unknown): Promise<T> {
  let res: Response
  try {
    res = await fetch(path, {
      method,
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', 'X-QS-Client': 'web' },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch {
    throw new ApiError('NETWORK', 0)
  }
  let data: any = null
  try { data = await res.json() } catch { /* 空响应 */ }
  if (!res.ok) throw new ApiError(data?.code ?? (res.status === 503 ? 'STARTING' : 'INTERNAL_ERROR'), res.status, data?.params, data)
  return data as T
}

// ---------- 会话状态 ----------

export type Session =
  | { state: 'loading' }
  | { state: 'out' }
  | { state: 'unavailable' }            // 账户服务还没就绪（数据库没连上）
  | { state: 'in'; user: User }

let cur: Session = { state: 'loading' }
const subs = new Set<() => void>()
function set(s: Session) {
  // 退出、换了一个人：个人数据的记忆清掉（lib/cache.ts），不让下一个人看到上一个人的
  const was = cur.state === 'in' ? cur.user.id : null
  const now = s.state === 'in' ? s.user.id : null
  if (was !== now) forgetPrivate()
  cur = s
  subs.forEach((f) => f())
}

export function useSession(): Session {
  return useSyncExternalStore((f) => { subs.add(f); return () => subs.delete(f) }, () => cur)
}

/** 登录、刷新后拿到用户：语言以账户里保存的为准 */
export function setUser(u: User) {
  set({ state: 'in', user: u })
  if (u.lang !== getLang()) setLang(u.lang, false)
}

let retry: number | undefined
export async function refreshSession() {
  clearTimeout(retry)
  try {
    const r = await api<{ user: User }>('GET', '/api/me')
    setUser(r.user)
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) set({ state: 'out' })
    else {
      set({ state: 'unavailable' })
      retry = window.setTimeout(refreshSession, 4000)
    }
  }
}

export async function logout() {
  try { await api('POST', '/api/auth/logout') } finally { set({ state: 'out' }) }
}

// 登录后切换语言 → 存到账户里（失败不打扰用户，下次切换再存）
langHooks.onChange = (l) => {
  if (cur.state === 'in') api<{ user: User }>('POST', '/api/me/lang', { lang: l }).then((r) => set({ state: 'in', user: r.user })).catch(() => {})
}

/** 地址缩写：0x1234…abcd */
export function shortAddr(a: string) {
  return a.length > 12 ? `${a.slice(0, 6)}…${a.slice(-4)}` : a
}

/** 顶栏里显示的身份 */
export function userLabel(u: User) {
  return u.name || u.email || (u.wallet ? shortAddr(u.wallet) : `#${u.id}`)
}
