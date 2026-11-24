// 链上强平地图（12 的 V.15）：纵轴价格，和 K 线、频谱一样。横条是这个价位上会被强平的金额，白线是累计。
// 现价下面是多单（跌到那里爆），上面是空单（涨到那里爆）。最大的几个价位列在价格尺上。
import { useRef } from 'react'
import { C, cursorTag, dirColors, fmtNum, valueScale } from './scale'
import { useSurface, type G } from '../lib/gfx'
import { fmtCompactL, tr } from '../lib/i18n'
import { dashedH } from './Spectrum'
import { locDec } from '../lib/tz'

export interface LiqBin { lo: number; hi: number; long_value: number; short_value: number; long_px?: number; short_px?: number }

/** 累计：从现价往外把强平金额加起来。下面只算多单，上面只算空单 */
export function cumulate(bins: LiqBin[], mid: number): { up: { p: number; cum: number }[]; dn: { p: number; cum: number }[] } {
  const above = bins.filter((b) => (b.lo + b.hi) / 2 >= mid).sort((a, b) => a.lo - b.lo)
  const below = bins.filter((b) => (b.lo + b.hi) / 2 < mid).sort((a, b) => b.lo - a.lo)
  let s = 0
  const up = above.map((b) => { s += b.short_value; return { p: (b.lo + b.hi) / 2, cum: s } })
  s = 0
  const dn = below.map((b) => { s += b.long_value; return { p: (b.lo + b.hi) / 2, cum: s } })
  return { up, dn }
}

export function LiqMapChart({ bins, mid, dec, pctRange = 0.2 }: { bins: LiqBin[]; mid: number; dec: number; pctRange?: number }) {
  const mouse = useRef<{ x: number; y: number } | null>(null)
  const draw = (g: G) => {
    const AX = g.w >= 460 ? 148 : 72
    const P = { x0: 0, y0: 6, x1: g.w - AX, y1: g.h - 6 }
    g.rect(0, 0, g.w, g.h, C.void)
    if (!(mid > 0) || !bins.length) return
    const a = mid * (1 - pctRange), b = mid * (1 + pctRange)
    const yOf = (p: number) => P.y0 + (1 - (p - a) / (b - a)) * (P.y1 - P.y0)
    const dc = dirColors()
    const vmax = Math.max(1, ...bins.map((x) => Math.max(x.long_value, x.short_value)))
    const { up, dn } = cumulate(bins, mid)
    const cmax = Math.max(1, up[up.length - 1]?.cum ?? 0, dn[dn.length - 1]?.cum ?? 0)
    const W = P.x1 - P.x0
    // 每格至少 1 个物理像素高；长度按平方根，小的也看得见，准确数字指上去看
    for (const x of bins) {
      const m = (x.lo + x.hi) / 2
      if (m < a || m > b) continue
      const v = m >= mid ? x.short_value : x.long_value
      if (v <= 0) continue
      const y0 = yOf(x.hi), y1 = yOf(x.lo)
      const len = Math.sqrt(v / vmax) * W * 0.92
      g.rect(P.x1 - len, y0, P.x1, Math.max(y1 - 1 / g.ky, y0 + 1 / g.ky), m >= mid ? dc.up : dc.down)
    }
    // 累计：白线
    const line = (pts: { p: number; cum: number }[]) => {
      const c = g.ctx
      c.beginPath()
      c.moveTo(g.X(P.x1), g.Y(yOf(mid)))
      for (const q of pts) if (q.p >= a && q.p <= b) c.lineTo(g.X(P.x1 - (q.cum / cmax) * W * 0.92), g.Y(yOf(q.p)))
      c.strokeStyle = C.white
      c.lineWidth = Math.max(1, Math.round(g.kx))
      c.stroke()
    }
    line(up); line(dn)
    // 价格尺：最大的几个价位亮
    const candidates = bins.map((x) => {
      const m = (x.lo + x.hi) / 2
      const above = m >= mid
      return { p: (above ? x.short_px : x.long_px) || m, v: above ? x.short_value : x.long_value, above }
    }).filter((x) => x.v > 0 && x.p >= a && x.p <= b).sort((p, q) => q.v - p.v)
    const curY = yOf(mid)
    const tags: { y: number; p: number; v: number }[] = []
    for (const x of candidates) {
      const y = yOf(x.p)
      if (Math.abs(y - curY) < 16 || tags.some((t) => Math.abs(t.y - y) < 15)) continue
      tags.push({ y, p: x.p, v: x.v })
      if (tags.length >= Math.floor((P.y1 - P.y0) / 44)) break
    }
    valueScale(g, { x0: P.x0, y0: P.y0, x1: P.x1, y1: P.y1 }, a, b, (x) => fmtNum(x, dec >= 1 ? 0 : dec), { grid: false, avoid: [curY, ...tags.map((t) => t.y)] })
    for (const t of tags) {
      g.hline(t.y, P.x1 - 6, P.x1 + 5, 'rgba(255,255,255,0.7)')
      g.text(fmtNum(t.p, dec >= 1 ? 0 : dec), P.x1 + 8, t.y, { size: 11, color: C.t1, weight: 500, baseline: 'middle' })
      if (AX >= 148) g.text(fmtCompactL(t.v), g.w - 8, t.y, { size: 11, color: C.t2, align: 'right', baseline: 'middle' })
    }
    g.hline(curY, P.x0, P.x1 + 6, C.white)
    cursorTag(g, P.x1 + 6, curY, fmtNum(mid, dec))
    // 十字线和读数
    const m = mouse.current
    if (m && m.x < P.x1 && m.y > P.y0 && m.y < P.y1) {
      const p = a + (1 - (m.y - P.y0) / (P.y1 - P.y0)) * (b - a)
      const hit = bins.find((x) => p >= x.lo && p < x.hi)
      dashedH(g, m.y, P.x0, P.x1)
      cursorTag(g, P.x1 + 6, m.y, fmtNum(p, dec), { color: C.plate, ink: C.t1 })
      const above = p >= mid
      const v = hit ? (above ? hit.short_value : hit.long_value) : 0
      const cum = (above ? up : dn).filter((q) => (above ? q.p <= p : q.p >= p)).pop()?.cum ?? 0
      const pct = ((p / mid - 1) * 100)
      const l1 = tr(above ? 'cn.tipUp' : 'cn.tipDn', { px: fmtNum(p, dec >= 1 ? 0 : dec), pct: `${pct > 0 ? '+' : ''}${locDec(pct.toFixed(1))}%` })
      const l2 = tr('cn.tipVal', { v: fmtCompactL(v), cum: fmtCompactL(cum) })
      const w = Math.max(g.measure(l1, { size: 12, weight: 500 }), g.measure(l2, { size: 12 })) + 20
      const bx = Math.max(4, Math.min(m.x + 14, P.x1 - w - 4)), by = Math.max(P.y0 + 2, m.y - 58)
      g.rect(bx, by, bx + w, by + 46, 'rgba(17,17,19,0.97)')
      g.hline(by, bx, bx + w, C.rule2); g.hline(by + 46, bx, bx + w, C.rule2)
      g.text(l1, bx + 10, by + 15, { size: 12, weight: 500, color: C.t1, baseline: 'middle' })
      g.text(l2, bx + 10, by + 32, { size: 12, color: C.t2, baseline: 'middle' })
    }
  }
  const { ref, redraw } = useSurface(draw, [bins, mid, dec, pctRange])
  return (
    <div className="surface">
      <canvas ref={ref} className="interactive"
        onPointerMove={(e) => { const r = e.currentTarget.getBoundingClientRect(); mouse.current = { x: e.clientX - r.left, y: e.clientY - r.top }; redraw() }}
        onPointerLeave={() => { mouse.current = null; redraw() }} />
    </div>
  )
}
