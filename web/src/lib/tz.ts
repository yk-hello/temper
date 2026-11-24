// 时区和数字格式（设置页，3.26）。图表每帧都要算时间，所以偏移量按小时缓存。
import { getPrefs } from './prefs'

export const TZ_OPTIONS = ['local', 'UTC', 'Asia/Taipei', 'Asia/Shanghai', 'Asia/Tokyo', 'Asia/Singapore', 'Europe/London', 'America/New_York'] as const
export const NUM_OPTIONS = ['en-US', 'de-DE', 'fr-FR'] as const

const cache = new Map<string, number>()

/** 现在选的时区在 t 时刻比 UTC 早多少毫秒 */
export function tzOffsetMs(t: number): number {
  const tz = getPrefs().tz
  if (tz === 'local') return -new Date(t).getTimezoneOffset() * 60_000
  const k = `${tz}|${Math.floor(t / 3_600_000)}`
  const hit = cache.get(k)
  if (hit !== undefined) return hit
  let off = 0
  try {
    const parts = new Intl.DateTimeFormat('en-US', { timeZone: tz, hourCycle: 'h23', year: 'numeric', month: 'numeric', day: 'numeric', hour: 'numeric', minute: 'numeric', second: 'numeric' }).formatToParts(t)
    const g = (n: string) => Number(parts.find((p) => p.type === n)?.value)
    off = Date.UTC(g('year'), g('month') - 1, g('day'), g('hour'), g('minute'), g('second')) - Math.floor(t / 1000) * 1000
  } catch { /* 时区名字不认识：当 UTC */ }
  if (cache.size > 500) cache.clear()
  cache.set(k, off)
  return off
}

export interface Wall { y: number; mo: number; d: number; h: number; mi: number; s: number; ms: number }

/** t 在现在选的时区里是几点 */
export function wall(t: number | string | Date): Wall {
  const ms = t instanceof Date ? t.getTime() : typeof t === 'string' ? new Date(t).getTime() : t
  const d = new Date(ms + tzOffsetMs(ms))
  return { y: d.getUTCFullYear(), mo: d.getUTCMonth() + 1, d: d.getUTCDate(), h: d.getUTCHours(), mi: d.getUTCMinutes(), s: d.getUTCSeconds(), ms: d.getUTCMilliseconds() }
}

/** 数字格式：1,234.56 / 1.234,56 / 1 234,56 */
export function numLocale(): string { return getPrefs().numFmt }
/** 小数点按数字格式写：1.5 / 1,5。用于自己拼出来的数（百分比、缩写），免得一页上两种写法（11-17 走查） */
export function locDec(s: string): string {
  const d = (1.5).toLocaleString(numLocale()).charAt(1)
  return d === '.' ? s : s.replace(/(\d)\.(\d)/g, `$1${d}$2`)
}
