// 复盘页的图：回撤水下图、MAE / MFE 散点。纯黑底、细线，和别的图一套画法。
import { useRef } from 'react'
import { C, cursorTag, dirColors, fmtNum, valueScale, type Box } from './scale'
import { niceStep, useSurface, type G } from '../lib/gfx'
import { tr, useLang } from '../lib/i18n'
import type { Excursion } from '../lib/journal'
import { dashedH, dashedV } from './Spectrum'
import { locDec } from '../lib/tz'

const pad = { l: 8, r: 56, t: 8, b: 20 }

/** 回撤水下图：平掉每一笔之后离权益最高点多远。横轴是第几笔，不按时间，免得空仓时一大段平线 */
export function UnderwaterChart({ points: raw }: { points: { t: number; dd: number }[] }) {
  const lang = useLang()
  // 前面补一个起点（第一笔之前没有回撤），纵轴按最深那一点定（11-17 走查：只有 1 笔时只在最右边画一个点，
  // 纵轴又至少拉到 −0.1%，标题写"最深 −0.01%"，图上什么都看不见）
  const points = raw.length ? [{ t: raw[0].t, dd: 0 }, ...raw] : raw
  const mouse = useRef<number | null>(null)
  const { ref, redraw } = useSurface((g: G) => {
    g.rect(0, 0, g.w, g.h, C.void)
    const P: Box = { x0: pad.l, y0: pad.t, x1: g.w - pad.r, y1: g.h - pad.b }
    if (!points.length) return
    const min = Math.min(-0.0001, ...points.map((p) => p.dd)) * 1.1
    const yOf = (v: number) => P.y0 + (v / min) * (P.y1 - P.y0)
    const xOf = (i: number) => P.x0 + (points.length > 1 ? i / (points.length - 1) : 1) * (P.x1 - P.x0)
    valueScale(g, { x0: P.x0, y0: P.y0, x1: P.x1, y1: P.y1 }, min, 0, (v) => `${locDec((v * 100).toFixed(Math.abs(min) < 0.001 ? 3 : Math.abs(min) < 0.01 ? 2 : 1))}%`, { grid: true, avoid: [] })
    const c = g.ctx
    c.beginPath()
    c.moveTo(g.X(xOf(0)), g.Y(yOf(0)))
    points.forEach((p, i) => { c.lineTo(g.X(xOf(i)), g.Y(yOf(p.dd))) })
    c.lineTo(g.X(xOf(points.length - 1)), g.Y(yOf(0)))
    c.closePath()
    c.fillStyle = 'rgba(255,255,255,0.1)'
    c.fill()
    c.beginPath()
    points.forEach((p, i) => { const X = g.X(xOf(i)), Y = g.Y(yOf(p.dd)); if (i) c.lineTo(X, Y); else c.moveTo(X, Y) })
    c.strokeStyle = C.white
    c.lineWidth = Math.max(1, Math.round(g.kx))
    c.stroke()
    g.text(tr('rv.nTrades', { n: raw.length }), P.x1, g.h - 7, { size: 11, color: C.t3, align: 'right', baseline: 'middle' })
    const m = mouse.current
    if (m != null && m >= P.x0 && m <= P.x1) {
      const i = Math.round(((m - P.x0) / Math.max(1, P.x1 - P.x0)) * (points.length - 1))
      const p = points[i]
      if (p && i > 0) {
        dashedV(g, xOf(i), P.y0, P.y1)
        cursorTag(g, P.x1 + 4, yOf(p.dd), `${locDec((p.dd * 100).toFixed(2))}%`, { color: C.plate, ink: C.t1 })
        g.text(tr('rv.nth', { n: i }), P.x0 + 2, g.h - 7, { size: 11, color: C.t2, baseline: 'middle' })
      }
    }
  }, [raw, lang])
  return (
    <div className="surface">
      <canvas ref={ref} className="interactive" onPointerMove={(e) => { mouse.current = e.clientX - e.currentTarget.getBoundingClientRect().left; redraw() }} onPointerLeave={() => { mouse.current = null; redraw() }} />
    </div>
  )
}

/**
 * MAE / MFE 散点：横轴最多浮亏，纵轴最多浮盈。绿点是赚钱的单，红点是亏钱的单。
 * 单位是 R 时，x = 1 那条线是初始止损；线右边的点是碰过止损线的。
 */
export function ExcursionChart({ items, unit, onPick, hidden }: { items: Excursion[]; unit: 'r' | 'usd'; onPick?: (id: string) => void; hidden?: boolean }) {
  const mouse = useRef<{ x: number; y: number } | null>(null)
  const hits = useRef<{ x: number; y: number; e: Excursion }[]>([])
  const lang = useLang()
  const { ref, redraw } = useSurface((g: G) => {
    g.rect(0, 0, g.w, g.h, C.void)
    const P: Box = { x0: pad.l, y0: pad.t, x1: g.w - pad.r, y1: g.h - pad.b - 8 }
    const val = (e: Excursion) => (unit === 'r' ? { a: e.mae_r, b: e.mfe_r } : { a: e.mae_usd, b: e.mfe_usd })
    const pts = items.filter((e) => val(e).a != null)
    const xm = Math.max(unit === 'r' ? 2 : 1, ...pts.map((e) => val(e).a!)) * 1.08
    const ym = Math.max(unit === 'r' ? 2 : 1, ...pts.map((e) => val(e).b!)) * 1.08
    const xOf = (v: number) => P.x0 + (v / xm) * (P.x1 - P.x0)
    const yOf = (v: number) => P.y1 - (v / ym) * (P.y1 - P.y0)
    const fmt = (v: number) => (hidden && unit === 'usd' ? '••' : unit === 'r' ? `${locDec(String(Number(v.toFixed(2))))}R` : fmtNum(v, v >= 100 ? 0 : 1))
    valueScale(g, { x0: P.x0, y0: P.y0, x1: P.x1, y1: P.y1 }, 0, ym, fmt, { grid: true, avoid: [] })
    g.hline(P.y1, P.x0, P.x1, C.rule2)
    const xStep = unit === 'r' ? (xm <= 3 ? 0.5 : xm <= 8 ? 1 : 2) : niceStep(xm / 5)
    for (let v = 0; v <= xm; v += xStep) {
      g.vline(xOf(v), P.y1, P.y1 + 4, C.rule3)
      g.text(fmt(v), xOf(v), P.y1 + 13, { size: 11, color: C.t3, align: v === 0 ? 'left' : 'center', baseline: 'middle' })
    }
    g.text(tr('rv.maeAxis'), P.x1 - 4, P.y1 - 10, { size: 11, color: C.t2, align: 'right', baseline: 'middle' })
    g.text(tr('rv.mfeAxis'), P.x0 + 4, P.y0 + 8, { size: 11, color: C.t2, baseline: 'middle' })
    if (unit === 'r') {
      for (let y = P.y0; y < P.y1; y += 6) g.vline(xOf(1), y, Math.min(y + 3, P.y1), 'rgba(255,255,255,0.45)')
      g.text(tr('rv.stopLine'), xOf(1) + 5, P.y1 - 10, { size: 11, color: C.t2, baseline: 'middle' })
    }
    const dc = dirColors()
    const c = g.ctx
    hits.current = []
    for (const e of pts) {
      const v = val(e)
      const x = xOf(v.a!), y = yOf(v.b!)
      hits.current.push({ x, y, e })
      c.beginPath()
      c.arc(g.X(x), g.Y(y), g.px(3.5), 0, Math.PI * 2)
      c.fillStyle = e.net >= 0 ? dc.up : dc.down
      c.fill()
      c.lineWidth = g.px(1)
      c.strokeStyle = '#000'
      c.stroke()
    }
    const m = mouse.current
    if (m && m.x > P.x0 && m.x < P.x1 && m.y > P.y0 && m.y < P.y1) {
      const hit = hits.current.find((h) => Math.hypot(h.x - m.x, h.y - m.y) <= 6)
      dashedV(g, m.x, P.y0, P.y1)
      dashedH(g, m.y, P.x0, P.x1)
      if (hit) {
        const v = val(hit.e)
        const lines = [tr('rv.dotMae', { v: fmt(v.a!) }), tr('rv.dotMfe', { v: fmt(v.b!) }), tr('rv.dotSrc', { s: tr(`rv.src.${hit.e.source}` as 'rv.src.ticks') })]
        const w = Math.max(...lines.map((s) => g.measure(s, { size: 12 }))) + 20
        const bx = Math.min(hit.x + 10, P.x1 - w), by = Math.max(P.y0, hit.y - 66)
        g.rect(bx, by, bx + w, by + 60, 'rgba(17,17,19,0.97)')
        g.hline(by, bx, bx + w, C.rule2); g.hline(by + 60, bx, bx + w, C.rule2)
        lines.forEach((s, i) => g.text(s, bx + 10, by + 13 + i * 17, { size: 12, color: i ? C.t2 : C.t1, baseline: 'middle' }))
      }
    }
  }, [items, unit, hidden, lang])
  return (
    <div className="surface">
      <canvas ref={ref} className="interactive"
        onPointerMove={(e) => { const r = e.currentTarget.getBoundingClientRect(); mouse.current = { x: e.clientX - r.left, y: e.clientY - r.top }; redraw() }}
        onPointerLeave={() => { mouse.current = null; redraw() }}
        onClick={(e) => {
          const r = e.currentTarget.getBoundingClientRect()
          const x = e.clientX - r.left, y = e.clientY - r.top
          const h = hits.current.find((q) => Math.hypot(q.x - x, q.y - y) <= 7)
          if (h) onPick?.(h.e.id)
        }} />
    </div>
  )
}
