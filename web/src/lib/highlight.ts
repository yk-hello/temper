// 高光（D91）：每笔平仓按保证金的回报够门槛，就进生涯档案。没有验真、没有邀请（D90）。
import { api } from './session'
import { locDec } from './tz'

export interface HlSnap {
  trade_id: string
  instrument: string
  side: 'long' | 'short'
  origin: string
  mode: 'sim' | 'live'
  leverage: number
  open_time: number
  close_time: number
  entry: number
  exit: number
  roe: number // 按保证金的净回报
  acct_ret: number // 占开仓时的权益
  mae: number // 最深浮亏，按保证金，≤ 0
  mfe: number // 最高浮盈，按保证金，≥ 0
  path_known: boolean
  net: number
  qty: number
}

export interface Highlight { id: number; account: number; snap: HlSnap; created: number }
export interface HlEvent { t: number; kind: 'econ' | 'whale' | 'liq'; key?: string; side?: string; usd?: number }
export interface HlDemo { instrument: string; side: 'long' | 'short'; from: number; to: number; entry: number; exit: number; move: number }
export interface HlList { items: Highlight[]; min_roe: number; default_min: number; floor: number; ceil: number }

export const loadHighlights = () => api<HlList>('GET', '/api/highlights')
export const setMinRoe = (min_roe: number) => api<{ min_roe: number }>('POST', '/api/highlights/prefs', { min_roe })
export const delHighlight = (id: number) => api('DELETE', `/api/highlights/${id}`)
export interface HlCandle { t: number; o: number; h: number; l: number; c: number }
/** 开仓到平仓的 K 线（服务器挑好周期，短单用逐笔成交合成）和持仓期间的事件 */
export interface HlScene { events: HlEvent[]; candles: HlCandle[]; iv: number }
export const highlightScene = (id: number) => api<HlScene>('GET', `/api/highlights/${id}/scene`)
export const loadDemo = () => api<HlScene & { demo: HlDemo }>('GET', '/api/highlights/demo')

/** 比例写成 +8.3%（负数用真正的减号） */
export const pct = (x: number, d = 1) => `${x >= 0 ? '+' : '−'}${locDec(Math.abs(x * 100).toFixed(d))}%`
export const coinOf = (inst: string) => inst.replace(/-(PERP|SPOT)$/, '')
export const isSpot = (inst: string) => inst.endsWith('-SPOT')
