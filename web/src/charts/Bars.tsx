// 柱状 + 累计线：订单流里的"主动买卖量差"和累计成交量差（CVD）。
import { useRef } from 'react'
import { useSurface, clock, type G } from '../lib/gfx'
import type { Candle } from '../lib/demo'
import { C, cursorTag, dirColors, fmtNum, niceRange, timeScale, valueScale, type Box } from './scale'
import { dashedV } from './Spectrum'

const AXIS = 68
const BOTTOM = 26

export function DeltaBars({ candles, interval, span, t1 }: { candles: Candle[]; interval: number; span: number; t1: number }) {
  const mouse = useRef<number | null>(null)
  const { ref, redraw } = useSurface((g) => draw(g), [candles, span, t1])

  function draw(g: G) {
    const tb = t1, ta = tb - span
    const P: Box = { x0: 0, y0: 10, x1: g.w - AXIS, y1: g.h - BOTTOM }
    g.rect(0, 0, g.w, g.h, C.void)
    const dc = dirColors()
    const vis = candles.filter((c) => c.t + interval >= ta && c.t <= tb)
    let cvd = 0
    const cv: number[] = []
    let lo = 0, hi = 0
    for (const c of vis) {
      const d = c.vb - c.vs
      cvd += d
      cv.push(cvd)
      lo = Math.min(lo, cvd, d); hi = Math.max(hi, cvd, d)
    }
    timeScale(g, P, ta, tb)
    if (!vis.length) return
    // 柱和累计线共用一把刻度尺，零线就是刻度上的 0
    const [a, b] = niceRange(lo, hi, 0.1)
    const yOf = (v: number) => P.y0 + (1 - (v - a) / (b - a)) * (P.y1 - P.y0)
    const yC = valueScale(g, P, a, b, (v) => fmtNum(v, 0), { avoid: [yOf(cvd)] })
    const y0 = g.Y(yOf(0))
    g.hline(yOf(0), P.x0, P.x1, C.rule2)
    const xOf = (t: number) => P.x0 + ((t - ta) / span) * (P.x1 - P.x0)
    const slot = ((P.x1 - P.x0) / span) * interval * g.kx
    let bw = Math.max(1, Math.floor(slot * 0.62)); if (bw % 2 === 0) bw -= 1
    const ctx = g.ctx
    ctx.globalAlpha = 0.72
    vis.forEach((c) => {
      const d = c.vb - c.vs
      const cx = Math.round(g.X(xOf(c.t + interval / 2)))
      const yd = g.Y(yOf(d))
      ctx.fillStyle = d >= 0 ? dc.up : dc.down
      if (d >= 0) ctx.fillRect(cx - (bw - 1) / 2, yd, bw, Math.max(1, y0 - yd))
      else ctx.fillRect(cx - (bw - 1) / 2, y0 + 1, bw, Math.max(1, yd - y0))
    })
    ctx.globalAlpha = 1
    // 累计线：阶梯
    ctx.beginPath()
    vis.forEach((c, i) => {
      const x = g.X(xOf(c.t)) + 0.5, x2 = g.X(xOf(c.t + interval)) + 0.5
      const y = g.Y(yC(cv[i])) + 0.5
      if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y)
      ctx.lineTo(x2, y)
    })
    ctx.strokeStyle = C.t1
    ctx.lineWidth = 1
    ctx.stroke()
    const lastY = yC(cv[cv.length - 1])
    cursorTag(g, P.x1 + 6, lastY, fmtNum(cv[cv.length - 1], 0))
    const m = mouse.current
    if (m !== null && m >= P.x0 && m <= P.x1) {
      dashedV(g, m, P.y0, P.y1)
      const t = ta + ((m - P.x0) / (P.x1 - P.x0)) * span
      cursorTag(g, m, P.y1 + 13, clock(t, false), { color: C.plate, ink: C.t1 })
    }
  }

  return (
    <div className="surface">
      <canvas ref={ref} className="interactive"
        onPointerMove={(e) => { mouse.current = e.clientX - e.currentTarget.getBoundingClientRect().left; redraw() }}
        onPointerLeave={() => { mouse.current = null; redraw() }} />
    </div>
  )
}
