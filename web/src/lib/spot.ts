// 现货账户（D44、D83）：余额按币种算，没有杠杆。
import { useEffect, useState } from 'react'
import { api } from './session'

export const SPOT_COINS = ['BTC', 'ETH', 'SOL', 'BNB', 'XRP', 'DOGE']
export const isSpot = (inst: string | undefined) => !!inst && inst.endsWith('-SPOT')

export interface SpotSpec { instrument: string; base: string; tick: string; step: string; min_notional: string; fee: string }
export interface SpotBalance { coin: string; free: string; locked: string; cost: string; px: string; value: string; unreal: string }
export interface SpotOrder {
  id: string; instrument: string; side: 'buy' | 'sell'; type: 'market' | 'limit' | 'cond_market'; tif?: string
  price: string; trigger_px: string; trigger_dir?: string; qty: string; filled: string; avg_px: string
  status: string; reason?: string; source: string; created: number; updated: number
  queue_ahead?: string // 限价挂单前面还排着多少
}
export interface SpotFill { seq: number; time: number; order_id: string; instrument: string; side: 'buy' | 'sell'; qty: string; px: string; fee: string; fee_coin: string; fee_usdt: string; maker: boolean; realized: string; source: string }
export interface SpotView { account: number; equity: string; balances: SpotBalance[]; orders: SpotOrder[]; done: SpotOrder[]; fills: SpotFill[] }

const listeners = new Set<() => void>()
/** 下单、划转以后马上刷新，不等下一次轮询 */
export const refreshSpot = () => listeners.forEach((f) => f())

export function useSpot(acct: number | null | undefined): SpotView | null {
  const [v, setV] = useState<SpotView | null>(null)
  useEffect(() => {
    if (!acct) { setV(null); return }
    let off = false
    const load = async () => {
      try {
        const r = await api<SpotView>('GET', `/api/spot/${acct}`)
        if (!off) setV(r)
      } catch { /* 下次再试 */ }
    }
    void load()
    const id = window.setInterval(load, 1000)
    listeners.add(load)
    return () => { off = true; window.clearInterval(id); listeners.delete(load) }
  }, [acct])
  return v
}

let specCache: Record<string, SpotSpec> | null = null
export function useSpotSpecs(): Record<string, SpotSpec> | null {
  const [s, setS] = useState(specCache)
  useEffect(() => {
    if (specCache) return
    void api<SpotSpec[]>('GET', '/api/spot/specs').then((l) => {
      specCache = Object.fromEntries(l.map((x) => [x.instrument, x]))
      setS(specCache)
    }).catch(() => undefined)
  }, [])
  return s
}
