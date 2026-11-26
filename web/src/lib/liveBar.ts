// 最后一根 K 线跟着实时价走（2026-11-26 用户："改成跟着动"）。
// K 线每 2 秒（1 秒线每 1 秒）向后端取一次，实时价每 0.1 秒推一次：中间这段用推来的最新成交价
// 更新最后一根的收盘、最高、最低；时间已经进了下一根就先临时开一根，下次取到真的 K 线就换掉。
// 不依赖 React，tools/test-livebar.mjs 直接测。
import type { Candle } from './demo'

export const IV_MS: Record<string, number> = { '1s': 1000, '1m': 60_000, '5m': 300_000, '15m': 900_000, '1h': 3_600_000, '4h': 14_400_000, '1d': 86_400_000 }

/** 这一根里实时推送见过的最高、最低：两次取 K 线之间冲高又回落，高点不能缩回去 */
export interface LiveExt { t: number; h: number; l: number; o: number }

/**
 * bars：取到的 K 线（不改它）；px：推来的最新成交价；at：这笔成交的交易所时间（毫秒，和后端拼 K 线用的是同一个钟）；
 * ext：跨推送记着这一根见过的高低（换品种、换周期时清空）。返回合并后的 K 线，没变化时原样返回同一个数组。
 */
export function withLive(bars: Candle[], px: number, at: number, ivMs: number, ext: { current: LiveExt | null }): Candle[] {
  const last = bars[bars.length - 1]
  if (!last || !(px > 0) || !(at > 0) || !(ivMs > 0)) return bars
  const t = Math.floor(at / ivMs) * ivMs
  // 推送比取到的 K 线还旧，或者中间隔了不止一根（断线、电脑睡醒）：不猜，等下次取
  if (t < last.t || t - last.t > ivMs) return bars
  let e = ext.current
  if (!e || e.t !== t) e = ext.current = { t, h: px, l: px, o: t === last.t ? last.o : last.c }
  else { e.h = Math.max(e.h, px); e.l = Math.min(e.l, px) }
  if (t === last.t) {
    const h = Math.max(last.h, e.h), l = Math.min(last.l, e.l)
    if (last.c === px && last.h === h && last.l === l) return bars
    return [...bars.slice(0, -1), { ...last, c: px, h, l }]
  }
  // 新的一根还没取到：开盘价先用上一根的收盘价，成交量先记 0（取到真的就换掉）
  return [...bars, { t, o: e.o, h: Math.max(e.o, e.h), l: Math.min(e.o, e.l), c: px, vb: 0, vs: 0 }]
}
