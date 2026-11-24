// 经济事件（可视化 A）：议息、PCE、GDP。后端只用官方日历（D56），每 5 分钟取一次就够。
import { recall, remember } from './cache'
import { useEffect, useState } from 'react'
import { tr, type Key } from './i18n'

export interface EconEvent { t: number; key: string; title: string; vol?: { n: number; mean: number } }

type CalResp = { events: EconEvent[]; auto?: boolean; until?: number }
const last = recall<CalResp>('/api/calendar') // 刷新浏览器也先显示上一次的（lib/cache.ts）
let cache: EconEvent[] = last?.events ?? []
/** 日程会不会自己更新（D102）：auto = 部署的人配了 FRED；until = 内置的美国劳工统计局日程到哪天 */
export let calMeta: { auto: boolean; until: number } = { auto: !!last?.auto, until: last?.until ?? 0 }
const subs = new Set<(v: EconEvent[]) => void>()
let timer = 0

async function load() {
  try {
    const r = await fetch('/api/calendar')
    if (!r.ok) return
    const d = (await r.json()) as CalResp
    remember('/api/calendar', d)
    cache = d.events ?? []
    calMeta = { auto: !!d.auto, until: d.until ?? 0 }
    subs.forEach((f) => f(cache))
  } catch { /* 下一轮再取 */ }
}

export function useEconEvents(on: boolean): EconEvent[] {
  const [v, setV] = useState(cache)
  useEffect(() => {
    if (!on) return
    subs.add(setV)
    if (!timer) { void load(); timer = window.setInterval(load, 300_000) }
    setV(cache)
    return () => {
      subs.delete(setV)
      if (!subs.size) { window.clearInterval(timer); timer = 0 }
    }
  }, [on])
  return on ? v : []
}

export const evShort = (k: string) => tr(`ev.${k}` as Key)
export const evLong = (k: string) => tr(`ev.${k}L` as Key)
/** 样本够 3 次才说平均 */
export const evVolOk = (e: EconEvent) => !!e.vol && e.vol.n >= 3

export function nextEvent(list: EconEvent[], now: number, withinMs: number): EconEvent | null {
  for (const e of list) if (e.t > now && e.t - now <= withinMs) return e
  return null
}
