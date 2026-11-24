// 纪律锁（模块 4，D45、D80）的前端数据层。规则和锁都在服务器判断，这里只负责显示和提交。
import { useCallback, useEffect, useState } from 'react'
import { api, useSession } from './session'
import { tr, type Key } from './i18n'

export interface Rules {
  daily_loss: string; per_trade: string; max_drawdown: string; peak_r: string; stop_r: string; loss_streak: number; cooldown_min: number
  /** 连亏只数亏得比（权益 × 这个）多的单；比它小的盈亏当没发生（D107）。0 = 每笔都算 */
  min_loss?: string
}
export interface DLock { rule: string; since: number; until?: number; unlock_at?: number; value: string; limit: string }
export interface AcctDisc { account: number; day_open: string; day_peak: string; peak: string; streak: number; lock?: DLock; unlock_used: boolean; next_day: number }
export interface DiscView { enabled: boolean; rules: Rules; defaults: Rules; tz: number; accounts: AcctDisc[] }

export const ruleName = (rule: string) => tr(`disc.rule.${rule}` as Key)

/** 剩余时间写成 01:23:45 */
export function hms(ms: number): string {
  const s = Math.max(0, Math.ceil(ms / 1000))
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(Math.floor(s / 3600))}:${p(Math.floor(s / 60) % 60)}:${p(s % 60)}`
}

const url = () => `/api/discipline?tz=${-new Date().getTimezoneOffset()}`

/** 纪律状态。reload：改完马上刷新；set：用接口返回的新状态 */
export function useDiscipline(ms = 3000): { view: DiscView | null; set: (v: DiscView) => void; reload: () => void } {
  const s = useSession()
  const [view, set] = useState<DiscView | null>(null)
  const reload = useCallback(() => {
    if (s.state !== 'in' || document.hidden) return
    api<DiscView>('GET', url()).then(set).catch(() => {})
  }, [s.state])
  useEffect(() => {
    reload()
    const id = window.setInterval(reload, ms)
    return () => window.clearInterval(id)
  }, [reload, ms])
  return { view, set, reload }
}

/** 一秒走一次的时钟：倒计时用 */
export function useTick(on: boolean): number {
  const [now, setNow] = useState(Date.now())
  useEffect(() => {
    if (!on) return
    const id = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(id)
  }, [on])
  return now
}

/** 还有多久解开。到时间不会自动解开的（最大回撤）返回 null */
export function lockLeft(l: DLock, now: number): number | null {
  const at = l.unlock_at || l.until || 0
  return at ? at - now : null
}
