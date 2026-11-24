import { locDec, numLocale } from './tz'
// 显示用的数字格式化。注意：只用于展示，账本和下单从不用浮点数（D09）。

export function priceDecimals(v: number): number {
  const a = Math.abs(v)
  if (a >= 10000) return 1
  if (a >= 100) return 2
  if (a >= 1) return 3
  return 5
}

export function fmtPrice(s: string | undefined, dec?: number): string {
  const v = Number(s)
  if (!s || !Number.isFinite(v)) return '—'
  const d = dec ?? priceDecimals(v)
  return v.toLocaleString(numLocale(), { minimumFractionDigits: d, maximumFractionDigits: d })
}

/** 输入框里的数统一过一遍（11-17 走查：数量框打 0,5 或中文输入法的 0。5，标点被删掉变成 5，大十倍）。
 *  - 全角数字、全角句号、全角逗号当成半角
 *  - 数字格式是 1.234,56 的：逗号就是小数点
 *  - 其他格式：整串是 1,234 / 65,078.5 这种千位分隔（比如从盘口复制的价格）就去掉逗号；
 *    看不准的（0,5、64,5）原样留着——这时数字不成立、下不了单，不会被悄悄读成大十倍的数。
 *    不能一个键一个键地猜：打 1,000 的中途是 1,0，看着像小数
 *  - 只留第一个小数点 */
export function numIn(s: string): string {
  let x = s.replace(/[０-９]/g, (c) => String.fromCharCode(c.charCodeAt(0) - 0xfee0)).replace(/[。．]/g, '.').replace(/，/g, ',')
  x = x.replace(/[^\d.,]/g, '')
  if (locDec('1.5') !== '1.5') x = x.replace(/,/g, '.')
  else if (/^\d{1,3}(,\d{3})+(\.\d*)?$/.test(x)) x = x.replace(/,/g, '')
  const i = x.indexOf('.')
  return i < 0 ? x : x.slice(0, i + 1) + x.slice(i + 1).replace(/\./g, '')
}

export function fmtPct(s: string | undefined, digits = 2): string {
  const v = Number(s)
  if (!s || !Number.isFinite(v)) return '—'
  const a = Math.abs(v).toFixed(digits)
  const sign = Number(a) === 0 ? '' : v > 0 ? '+' : '−' // 四舍五入后是 0 不带正负号（11-17 走查："−0.00%"）
  return `${sign}${locDec(a)}%`
}

export function fmtFunding(s: string | undefined): string {
  const v = Number(s)
  if (!s || !Number.isFinite(v)) return '—'
  return fmtPct(String(v * 100), 4)
}


export function fmtCountdown(targetMs: number, now: number): string {
  if (!targetMs) return '—'
  let s = Math.max(0, Math.floor((targetMs - now) / 1000))
  const h = Math.floor(s / 3600)
  s -= h * 3600
  const m = Math.floor(s / 60)
  s -= m * 60
  return [h, m, s].map((x) => String(x).padStart(2, '0')).join(':')
}

export function spreadBps(bid: string, ask: string): string {
  const b = Number(bid), a = Number(ask)
  if (!b || !a) return '—'
  return locDec((((a - b) / ((a + b) / 2)) * 10000).toFixed(2)) + ' bp'
}

export function sign(s: string | undefined): 'up' | 'down' | '' {
  const v = Number(s)
  if (!s || !Number.isFinite(v) || v === 0) return ''
  return v > 0 ? 'up' : 'down'
}

export function baseOf(instrument: string): string {
  return instrument.replace(/-(PERP|SPOT)$/, '')
}
