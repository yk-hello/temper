// 模拟盘的下单延迟（D94）：跟随实测、选地区或关闭。设置存在服务器（引擎按它延迟成交）。
import { useEffect, useSyncExternalStore } from 'react'
import { api } from './session'

export interface LatRegion { key: string; rtt_ms: number; jitter_ms: number }
export interface LatInfo { mode: string; measured_ms: number | null; measured_jitter_ms: number; regions: LatRegion[] }

let cache: LatInfo | null = null
let loading = false
const subs = new Set<() => void>()
const emit = () => subs.forEach((f) => f())

function load() {
  if (loading) return
  loading = true
  api<LatInfo>('GET', '/api/trade/latency').then((r) => { cache = r; emit() }).catch(() => {}).finally(() => { loading = false })
}

/** 当前模式和各地区的参考值；实测值每分钟刷新一次 */
export function useLatency(on = true): LatInfo | null {
  const v = useSyncExternalStore((f) => { subs.add(f); return () => subs.delete(f) }, () => cache)
  useEffect(() => {
    if (!on) return
    load()
    const id = window.setInterval(load, 60000)
    return () => window.clearInterval(id)
  }, [on])
  return on ? v : null
}

export async function setLatency(mode: string) {
  cache = await api<LatInfo>('PUT', '/api/trade/latency', { mode })
  emit()
}

/** 现在这个模式下一次往返大约多少毫秒（null = 立即成交或测不到） */
export function rttOf(i: LatInfo | null): number | null {
  if (!i || i.mode === 'off') return null
  if (i.mode === 'measured') return i.measured_ms
  return i.regions.find((r) => r.key === i.mode)?.rtt_ms ?? null
}

/** 毫秒数：1 以下留一位小数，其余取整 */
export function fmtMs(v: number): string {
  return v < 1 ? v.toFixed(1) : String(Math.round(v))
}
