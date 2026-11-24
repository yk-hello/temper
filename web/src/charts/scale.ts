// 刻度语言：所有坐标轴共用。主刻度带数字，主刻度之间有副刻度，像精密量具。
import { clock, niceStep, niceTimeStep, type G } from '../lib/gfx'
import { numLocale, tzOffsetMs, wall } from '../lib/tz'

export const C = {
  void: '#000000',
  chassis: '#111113',
  plate: '#17171a',
  t1: '#ededef',
  t2: '#a3a3aa',
  t3: '#6e6e76',
  t4: '#48484e',
  white: '#ffffff',
  rule: 'rgba(255,255,255,0.085)',
  rule2: 'rgba(255,255,255,0.15)',
  rule3: 'rgba(255,255,255,0.3)',
  grid: 'rgba(255,255,255,0.045)',
  up: '#4cb98a',
  down: '#e2625c',
  amber: '#d4a44c',
}

/** 读取当前涨跌配色（红涨绿跌 / 绿涨红跌） */
export function dirColors(): { up: string; down: string } {
  const redUp = document.documentElement.dataset.colorMode === 'red-up'
  return redUp ? { up: C.down, down: C.up } : { up: C.up, down: C.down }
}

export interface Box { x0: number; y0: number; x1: number; y1: number }

/** 底部时间刻度尺。plot 是绘图区（CSS 坐标），刻度画在 plot.y1 下方。 */
// avoidX：十字线时间标签所在的横坐标，挨着它的刻度数字不画（11-17 走查：频谱底部"13:23:32"标签旁边露出半截"13:23:3"）
export function timeScale(g: G, plot: Box, t0: number, t1: number, opts: { labels?: boolean; grid?: boolean; withSec?: boolean; avoidX?: number } = {}) {
  const span = t1 - t0
  if (span <= 0) return
  const widthPx = plot.x1 - plot.x0
  const major = niceTimeStep(span, Math.max(2, Math.floor(widthPx / 96)))
  const minor = major / (major % 5000 === 0 && major / 5 >= 1000 ? 5 : 4)
  const xOf = (t: number) => plot.x0 + ((t - t0) / span) * widthPx
  const tick = g.px(1)
  // 副刻度（和主刻度一样按本地时间对齐）
  const tzm = major >= 3600e3 ? tzOffsetMs(t0) : 0
  for (let t = Math.ceil((t0 + tzm) / minor) * minor - tzm; t <= t1; t += minor) {
    const x = xOf(t)
    g.vline(x, plot.y1, plot.y1 + 3, C.rule2, tick)
  }
  // 主刻度 + 网格 + 数字。一小时以上的刻度按本地时间对齐（日刻度落在本地零点），零点处显示日期
  const withSec = opts.withSec ?? major < 60e3
  const tz = major >= 3600e3 ? tzOffsetMs(t0) : 0
  for (let t = Math.ceil((t0 + tz) / major) * major - tz; t <= t1; t += major) {
    const x = xOf(t)
    if (opts.grid !== false) g.vline(x, plot.y0, plot.y1, C.grid, tick)
    g.vline(x, plot.y1, plot.y1 + 6, C.rule3, tick)
    if (opts.labels !== false && x > plot.x0 + 28 && x < plot.x1 - 28 && (opts.avoidX == null || Math.abs(x - opts.avoidX) > 58)) {
      const d = wall(t)
      const midnight = major >= 3600e3 && d.h === 0 && d.mi === 0
      const label = midnight ? `${d.mo}/${d.d}` : clock(t, withSec)
      g.text(label, x, plot.y1 + 18, { size: 11, color: midnight ? C.t2 : C.t3, align: 'center', weight: 450 })
    }
  }
  g.hline(plot.y1, plot.x0, plot.x1, C.rule2)
}

/** 右侧数值刻度尺。返回 yOf。 */
export function valueScale(
  g: G, plot: Box, lo: number, hi: number,
  fmt: (v: number) => string,
  opts: { grid?: boolean; avoid?: number[]; side?: 'right' | 'left' } = {},
) {
  const h = plot.y1 - plot.y0
  const yOf = (v: number) => plot.y0 + (1 - (v - lo) / (hi - lo)) * h
  const major = niceStep((hi - lo) / Math.max(2, Math.floor(h / 56)))
  const minor = major / 5
  const tick = g.px(1)
  const right = opts.side !== 'left'
  const ax = right ? plot.x1 : plot.x0
  const dir = right ? 1 : -1
  for (let v = Math.ceil(lo / minor) * minor; v <= hi; v += minor) {
    const y = yOf(v)
    if (y < plot.y0 || y > plot.y1) continue
    g.hline(y, right ? ax : ax - 3, right ? ax + 3 : ax, C.rule2, tick)
  }
  for (let v = Math.ceil(lo / major) * major; v <= hi + major * 1e-9; v += major) {
    const y = yOf(v)
    if (y < plot.y0 + 2 || y > plot.y1 - 2) continue
    if (opts.grid !== false) g.hline(y, plot.x0, plot.x1, C.grid, tick)
    g.hline(y, right ? ax : ax - 6, right ? ax + 6 : ax, C.rule3, tick)
    if (opts.avoid?.some((a) => Math.abs(a - y) < 13)) continue
    g.text(fmt(v), ax + dir * 10, y, { size: 11, color: C.t3, align: right ? 'left' : 'right', baseline: 'middle', weight: 450 })
  }
  g.vline(ax, plot.y0, plot.y1, C.rule2)
  return yOf
}

/** 当前值标签：白底黑字的实心标签，像量具上的游标。 */
export function cursorTag(g: G, x: number, y: number, s: string, opts: { color?: string; ink?: string; align?: 'left' | 'right' } = {}) {
  g.text(s, x, y, {
    size: 11, weight: 550, color: opts.ink ?? '#000', align: opts.align ?? 'left', baseline: 'middle',
    plate: { color: opts.color ?? C.white, padX: 6, padY: 4 },
  })
}

export function niceRange(lo: number, hi: number, padFrac = 0.08, minSpanFrac = 0.0004): [number, number] {
  let a = lo, b = hi
  const mid = (a + b) / 2
  const minSpan = Math.abs(mid) * minSpanFrac || 1
  if (b - a < minSpan) { a = mid - minSpan / 2; b = mid + minSpan / 2 }
  const p = (b - a) * padFrac
  return [a - p, b + p]
}

export function fmtNum(v: number, dec: number): string {
  return v.toLocaleString(numLocale(), { minimumFractionDigits: dec, maximumFractionDigits: dec })
}
