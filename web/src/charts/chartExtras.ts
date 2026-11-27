// K 线图上的指标线、副图、画线（12 的 V.1、V.2）。Scope 每帧调用，只负责画和命中判断。
import type { G } from '../lib/gfx'
import { tr } from '../lib/i18n'
import { fmtCompactL } from '../lib/i18n'
import type { DispBar } from '../lib/flow'
import { FIB_LEVELS, type IndData, type MainLine, type Pane, type Shape, type ShapePt } from '../lib/chartPrefs'
import { C, dirColors, fmtNum, type Box } from './scale'
import { locDec } from '../lib/tz'

type Bar = { t: number }

/** 主图指标线和布林带 */
export function drawMainInd(g: G, ind: IndData, bars: Bar[], interval: number, ta: number, tb: number, xOf: (t: number) => number, yOf: (v: number) => number, box: Box) {
  const ctx = g.ctx
  const i0 = firstVisible(bars, ta - interval)
  ctx.save()
  ctx.beginPath()
  ctx.rect(g.X(box.x0), g.Y(box.y0), g.X(box.x1) - g.X(box.x0), g.Y(box.y1) - g.Y(box.y0))
  ctx.clip()
  if (ind.band) {
    ctx.beginPath()
    let first = true
    let end = i0
    for (let i = i0; i < bars.length && bars[i].t <= tb; i++) {
      const v = ind.band.up[i]
      if (v == null) continue
      const x = g.X(xOf(bars[i].t + interval / 2)), y = g.Y(yOf(v))
      if (first) { ctx.moveTo(x, y); first = false } else ctx.lineTo(x, y)
      end = i
    }
    for (let i = end; i >= i0; i--) {
      const v = ind.band.lo[i]
      if (v == null) continue
      ctx.lineTo(g.X(xOf(bars[i].t + interval / 2)), g.Y(yOf(v)))
    }
    ctx.closePath()
    ctx.fillStyle = 'rgba(255,255,255,0.04)'
    ctx.fill()
    strokeSeries(g, ind.band.up, bars, i0, interval, tb, xOf, yOf, C.t3)
    strokeSeries(g, ind.band.lo, bars, i0, interval, tb, xOf, yOf, C.t3)
  }
  for (const l of ind.main) strokeSeries(g, l.v, bars, i0, interval, tb, xOf, yOf, l.color, l.dash)
  ctx.restore()
}

function firstVisible(bars: Bar[], t: number): number {
  let lo = 0, hi = bars.length
  while (lo < hi) {
    const m = (lo + hi) >> 1
    if (bars[m].t < t) lo = m + 1; else hi = m
  }
  return Math.max(0, lo - 1)
}

function strokeSeries(g: G, v: (number | null)[], bars: Bar[], i0: number, interval: number, tb: number, xOf: (t: number) => number, yOf: (v: number) => number, color: string, dash?: number[]) {
  const ctx = g.ctx
  ctx.beginPath()
  let pen = false
  for (let i = i0; i < bars.length && bars[i].t <= tb; i++) {
    const y = v[i]
    if (y == null) { pen = false; continue }
    const X = g.X(xOf(bars[i].t + interval / 2)) + 0.5, Y = g.Y(yOf(y)) + 0.5
    if (pen) ctx.lineTo(X, Y); else ctx.moveTo(X, Y)
    pen = true
  }
  ctx.strokeStyle = color
  ctx.lineWidth = g.px(1)
  ctx.setLineDash(dash ? dash.map((d) => d * g.kx) : [])
  ctx.stroke()
  ctx.setLineDash([])
}

const compact = (v: number) => {
  const a = Math.abs(v)
  // 1000 以下按大小留位数（11-28 抽查：订单流的副图里"−452.07"和旁边的"3.5K"写法不一）
  const s = a >= 1e9 ? `${locDec((a / 1e9).toFixed(2))}B` : a >= 1e6 ? `${locDec((a / 1e6).toFixed(2))}M` : a >= 1e3 ? `${locDec((a / 1e3).toFixed(1))}K`
    : a >= 100 ? locDec(a.toFixed(0)) : a >= 10 ? locDec(a.toFixed(1)) : a >= 1 ? locDec(a.toFixed(2)) : locDec(a.toFixed(4))
  return v < 0 ? `−${s}` : s
}

function fmtPane(p: Pane, v: number, dec: number): string {
  if (p.fmt === 'pct') return locDec(v.toFixed(1))
  if (p.fmt === 'px') return locDec(v.toFixed(Math.min(dec + 1, 6)))
  return compact(v)
}

/** 副图：每个一条，在主图和时间轴之间 */
export function drawPane(g: G, p: Pane, box: Box, bars: Bar[], interval: number, ta: number, tb: number, xOf: (t: number) => number, idx: number, dec: number) {
  const ctx = g.ctx
  g.hline(box.y0, box.x0, box.x1 + 68, C.rule2)
  const i0 = firstVisible(bars, ta - interval)
  let lo = Infinity, hi = -Infinity
  if (p.range) { [lo, hi] = p.range } else {
    for (let i = i0; i < bars.length && bars[i].t <= tb; i++) {
      for (const l of p.lines) { const v = l.v[i]; if (v != null) { lo = Math.min(lo, v); hi = Math.max(hi, v) } }
      const v = p.bars?.[i]
      if (v != null) { lo = Math.min(lo, v, 0); hi = Math.max(hi, v, 0) }
    }
    if (!(hi > lo)) { lo = (Number.isFinite(lo) ? lo : 0) - 1; hi = lo + 2 }
    const pad = (hi - lo) * 0.08
    lo -= pad; hi += pad
  }
  const h = box.y1 - box.y0 - 2
  const yOf = (v: number) => box.y0 + 2 + (1 - (v - lo) / (hi - lo)) * (h - 2)
  // 右侧刻度：上下两个值
  g.vline(box.x1, box.y0, box.y1, C.rule2)
  if (!p.range) {
    g.text(fmtPane(p, hi, dec), box.x1 + 8, box.y0 + 9, { size: 11, color: C.t3, baseline: 'middle' })
    g.text(fmtPane(p, lo, dec), box.x1 + 8, box.y1 - 7, { size: 11, color: C.t3, baseline: 'middle' })
  }
  for (const lv of p.levels ?? []) {
    const y = yOf(lv)
    for (let x = box.x0; x < box.x1; x += 6) g.hline(y, x, Math.min(x + 3, box.x1), C.rule2)
    if (p.range) g.text(String(lv), box.x1 + 8, y, { size: 11, color: C.t3, baseline: 'middle' })
  }
  ctx.save()
  ctx.beginPath()
  ctx.rect(g.X(box.x0), g.Y(box.y0), g.X(box.x1) - g.X(box.x0), g.Y(box.y1) - g.Y(box.y0))
  ctx.clip()
  if (p.bars) {
    const dc = dirColors()
    const zero = yOf(0)
    const slot = (((box.x1 - box.x0) / (tb - ta)) * interval) * g.kx
    let bw = Math.max(1, Math.floor(Math.min(slot * 0.68, 18 * g.kx)))
    if (bw % 2 === 0) bw -= 1
    for (let i = i0; i < bars.length && bars[i].t <= tb; i++) {
      const v = p.bars[i]
      if (v == null) continue
      const cx = Math.round(g.X(xOf(bars[i].t + interval / 2)))
      const y = g.Y(yOf(v)), z = g.Y(zero)
      const col = p.barColors?.[i]
      ctx.fillStyle = col ? (col === 'up' ? dc.up : dc.down) : v >= 0 ? dc.up : dc.down
      ctx.fillRect(cx - (bw - 1) / 2, Math.min(y, z), bw, Math.max(1, Math.abs(y - z)))
    }
  }
  for (const l of p.lines) strokeSeries(g, l.v, bars, i0, interval, tb, xOf, yOf, l.color, l.dash)
  ctx.restore()
  // 名称和当前值
  const parts: string[] = [p.label]
  const at = Math.max(0, Math.min(idx, bars.length - 1))
  for (const l of p.lines) { const v = l.v[at]; if (v != null) parts.push(`${l.label ? `${l.label} ` : ''}${fmtPane(p, v, dec)}`) }
  if (p.bars) { const v = p.bars[at]; if (v != null) parts.push(fmtPane(p, v, dec)) }
  if (p.notes?.[at]) parts.push(p.notes[at])
  g.text(parts.join('  '), box.x0 + 8, box.y0 + 10, { size: 11, color: C.t2, baseline: 'middle', plate: { color: 'rgba(0,0,0,0.6)', padX: 3, padY: 1 } })
}

/** 主图指标的读数：一行写不下就换行 */
export function drawLegend(g: G, main: MainLine[], idx: number, x: number, y: number, xMax: number, dec: number): void {
  let cx = x
  const cy = y
  for (const l of main) {
    const v = l.v[Math.max(0, Math.min(idx, l.v.length - 1))]
    const s = `${l.label} ${v == null ? '—' : fmtNum(v, dec)}`
    const w = g.measure(s, { size: 11 }) + 20
    if (cx + w > xMax) return
    const ctx = g.ctx
    ctx.strokeStyle = l.color
    ctx.lineWidth = g.px(1)
    ctx.setLineDash(l.dash ? l.dash.map((d) => d * g.kx) : [])
    ctx.beginPath()
    ctx.moveTo(g.X(cx), g.Y(cy) + 0.5)
    ctx.lineTo(g.X(cx + 12), g.Y(cy) + 0.5)
    ctx.stroke()
    ctx.setLineDash([])
    g.text(s, cx + 16, cy, { size: 11, color: C.t2, baseline: 'middle', plate: { color: 'rgba(0,0,0,0.55)', padX: 2, padY: 1 } })
    cx += w + 8
  }
}

// ---------- 画线 ----------

const fmtDur = (ms: number) => {
  const m = Math.round(Math.abs(ms) / 60000)
  if (m < 60) return tr('ct.m.min', { n: m })
  if (m < 1440) return tr('ct.m.hour', { h: Math.floor(m / 60), m: m % 60 })
  return tr('ct.m.day', { d: Math.floor(m / 1440), h: Math.floor((m % 1440) / 60) })
}

export interface Proj { xOf: (t: number) => number; yOf: (p: number) => number; box: Box; dec: number }

export function drawShape(g: G, s: Shape, pj: Proj, selected: boolean, hot: boolean) {
  const ctx = g.ctx
  const { xOf, yOf, box, dec } = pj
  const ax = xOf(s.a.t), ay = yOf(s.a.p), bx = xOf(s.b.t), by = yOf(s.b.p)
  const ink = selected || hot ? C.white : 'rgba(255,255,255,0.72)'
  ctx.save()
  ctx.beginPath()
  ctx.rect(g.X(box.x0), g.Y(box.y0), g.X(box.x1) - g.X(box.x0), g.Y(box.y1) - g.Y(box.y0))
  ctx.clip()
  ctx.strokeStyle = ink
  ctx.lineWidth = g.px(1)
  if (s.kind === 'hline') {
    g.hline(ay, box.x0, box.x1, ink)
  } else if (s.kind === 'trend') {
    ctx.beginPath()
    ctx.moveTo(g.X(ax) + 0.5, g.Y(ay) + 0.5)
    ctx.lineTo(g.X(bx) + 0.5, g.Y(by) + 0.5)
    ctx.stroke()
  } else if (s.kind === 'rect') {
    const x0 = Math.min(ax, bx), x1 = Math.max(ax, bx), y0 = Math.min(ay, by), y1 = Math.max(ay, by)
    g.rect(x0, y0, x1, y1, 'rgba(255,255,255,0.05)')
    g.hline(y0, x0, x1, ink); g.hline(y1, x0, x1, ink); g.vline(x0, y0, y1, ink); g.vline(x1, y0, y1, ink)
  } else if (s.kind === 'fib') {
    const x0 = Math.min(ax, bx), x1 = Math.max(ax, bx)
    let lastY = -Infinity
    for (const lv of FIB_LEVELS) {
      const p = s.b.p + (s.a.p - s.b.p) * lv
      const y = yOf(p)
      g.hline(y, x0, x1, lv === 0.5 || lv === 0.618 ? ink : 'rgba(255,255,255,0.4)')
      if (Math.abs(y - lastY) >= 13) { // 挤在一起时少写几个，不叠字
        g.text(`${lv} · ${fmtNum(p, dec)}`, x0 + 4, y - 7, { size: 11, color: C.t1, baseline: 'middle', plate: { color: 'rgba(0,0,0,0.72)', padX: 3, padY: 1 } })
        lastY = y
      }
    }
  }
  ctx.restore()
  if (s.kind === 'hline') {
    g.text(fmtNum(s.a.p, dec), box.x1 - 6, ay - 8, { size: 11, color: ink, align: 'right', baseline: 'middle', plate: { color: 'rgba(0,0,0,0.6)', padX: 3, padY: 1 } })
  }
  if (selected) {
    const dot = (x: number, y: number) => g.rect(x - 3, y - 3, x + 3, y + 3, C.white)
    if (s.kind === 'hline') dot((box.x0 + box.x1) / 2, ay)
    else { dot(ax, ay); dot(bx, by) }
    const d = delPos(s, pj)
    g.rect(d.x - 8, d.y - 8, d.x + 8, d.y + 8, C.plate)
    g.text('×', d.x, d.y, { size: 13, color: C.t1, align: 'center', baseline: 'middle' })
  }
}

/** 选中时的删除按钮位置：手机上没有 Delete 键 */
function delPos(s: Shape, pj: Proj) {
  const { xOf, yOf, box } = pj
  if (s.kind === 'hline') return { x: (box.x0 + box.x1) / 2 + 18, y: yOf(s.a.p) }
  const x = Math.min(box.x1 - 10, Math.max(xOf(s.a.t), xOf(s.b.t)) + 16)
  return { x, y: Math.max(box.y0 + 10, yOf(s.b.p)) }
}

export type ShapeHit = { id: string; part: 'a' | 'b' | 'all' | 'del' }

export function shapeAt(list: Shape[], x: number, y: number, pj: Proj, selected: string | null): ShapeHit | null {
  const { xOf, yOf } = pj
  for (let i = list.length - 1; i >= 0; i--) {
    const s = list[i]
    if (selected === s.id) {
      const d = delPos(s, pj)
      if (Math.abs(x - d.x) <= 9 && Math.abs(y - d.y) <= 9) return { id: s.id, part: 'del' }
    }
    const ax = xOf(s.a.t), ay = yOf(s.a.p), bx = xOf(s.b.t), by = yOf(s.b.p)
    if (s.kind === 'hline') {
      if (Math.abs(y - ay) <= 5 && x >= pj.box.x0 && x <= pj.box.x1) return { id: s.id, part: 'all' }
      continue
    }
    if (Math.hypot(x - ax, y - ay) <= 7) return { id: s.id, part: 'a' }
    if (Math.hypot(x - bx, y - by) <= 7) return { id: s.id, part: 'b' }
    if (s.kind === 'trend') {
      const dx = bx - ax, dy = by - ay, L = dx * dx + dy * dy
      const k = L > 0 ? Math.max(0, Math.min(1, ((x - ax) * dx + (y - ay) * dy) / L)) : 0
      if (Math.hypot(x - (ax + k * dx), y - (ay + k * dy)) <= 5) return { id: s.id, part: 'all' }
    } else {
      const x0 = Math.min(ax, bx), x1 = Math.max(ax, bx), y0 = Math.min(ay, by), y1 = Math.max(ay, by)
      if (s.kind === 'rect' && x >= x0 && x <= x1 && y >= y0 && y <= y1) return { id: s.id, part: 'all' }
      if (s.kind === 'fib' && x >= x0 && x <= x1) {
        for (const lv of FIB_LEVELS) if (Math.abs(y - yOf(s.b.p + (s.a.p - s.b.p) * lv)) <= 5) return { id: s.id, part: 'all' }
      }
    }
  }
  return null
}

/** 测量：从 a 到 b 的涨跌、价差、根数、时长 */
export function drawMeasure(g: G, a: ShapePt, b: ShapePt, interval: number, pj: Proj) {
  const { xOf, yOf, dec } = pj
  const ax = xOf(a.t), ay = yOf(a.p), bx = xOf(b.t), by = yOf(b.p)
  const up = b.p >= a.p
  const dc = dirColors()
  const col = up ? dc.up : dc.down
  g.rect(Math.min(ax, bx), Math.min(ay, by), Math.max(ax, bx), Math.max(ay, by), up ? 'rgba(76,185,138,0.12)' : 'rgba(226,98,92,0.12)')
  g.vline(bx, Math.min(ay, by), Math.max(ay, by), col)
  g.hline(by, Math.min(ax, bx), Math.max(ax, bx), col)
  const pct = a.p ? ((b.p - a.p) / a.p) * 100 : 0
  const bars = Math.round(Math.abs(b.t - a.t) / interval)
  const s = `${pct >= 0 ? '+' : '−'}${locDec(Math.abs(pct).toFixed(2))}% · ${fmtNum(Math.abs(b.p - a.p), dec)} · ${tr('ct.m.bars', { n: bars })} · ${fmtDur(b.t - a.t)}`
  g.text(s, (ax + bx) / 2, by + (up ? -14 : 14), { size: 12, color: '#000000', align: 'center', baseline: 'middle', plate: { color: C.t1, padX: 6, padY: 3 } })
}

// ---------- 足迹图 ----------

function fmtFp(v: number): string {
  if (v >= 1000) return fmtCompactL(v)
  if (v >= 100) return v.toFixed(0)
  if (v >= 10) return locDec(v.toFixed(1))
  return locDec(v.toFixed(2))
}

/** 一根 K 线的足迹：左边一条细的开高低收，右边每个价位左卖右买。放得下才写数字，放不下只留深浅 */
export function drawFootprintBar(g: G, c: { o: number; h: number; l: number; c: number }, d: DispBar, step: number, xl: number, xr: number, yOf: (p: number) => number, maxSide: number, up: string, down: string) {
  const ctx = g.ctx
  const isUp = c.c >= c.o
  const col = isUp ? up : down
  const wick = xl + 2
  g.vline(wick, yOf(c.h), yOf(c.l), col)
  const by0 = Math.min(yOf(c.o), yOf(c.c)), by1 = Math.max(yOf(c.o), yOf(c.c))
  g.rect(wick - 1, by0, wick + 2, Math.max(by1, by0 + 1), col)
  const x0 = xl + 6
  const mid = (x0 + xr) / 2
  const rowH = Math.abs(yOf(0) - yOf(step))
  const numbers = xr - x0 >= 58 && rowH >= 11
  for (const lv of d.levels) {
    const y0 = yOf(lv.p + step), y1 = yOf(lv.p)
    const pad = rowH >= 5 ? 1 / g.ky : 0
    const fill = (a: number, b: number, v: number, color: string) => {
      if (v <= 0) return
      ctx.globalAlpha = 0.08 + 0.62 * Math.sqrt(v / Math.max(maxSide, 1e-9))
      g.rect(a, y0 + pad, b, y1, color)
      ctx.globalAlpha = 1
    }
    fill(x0, mid, lv.s, down)
    fill(mid, xr, lv.b, up)
    if (lv.imb !== 0) {
      // 失衡：贴着格子外缘一条亮边
      if (lv.imb > 0) g.rect(xr - 2, y0 + pad, xr, y1, up)
      else g.rect(x0, y0 + pad, x0 + 2, y1, down)
    }
    if (lv.p === d.poc) {
      g.hline(y0, x0, xr, 'rgba(255,255,255,0.75)')
      g.hline(y1, x0, xr, 'rgba(255,255,255,0.75)')
    }
    if (numbers) {
      const ym = (y0 + y1) / 2
      if (lv.s > 0) g.text(fmtFp(lv.s), mid - 3, ym, { size: 10.5, color: lv.imb < 0 ? C.t1 : C.t2, weight: lv.imb < 0 ? 600 : 450, align: 'right', baseline: 'middle' })
      if (lv.b > 0) g.text(fmtFp(lv.b), mid + 3, ym, { size: 10.5, color: lv.imb > 0 ? C.t1 : C.t2, weight: lv.imb > 0 ? 600 : 450, baseline: 'middle' })
    }
  }
}
