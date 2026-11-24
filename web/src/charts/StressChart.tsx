// 压力测试图（`10` 的 G，4.24）：横轴 BTC 涨跌，纵轴账户权益。竖线是当前拖到的位置，方块标出每个仓位被强平的点。
// 在图上按住拖动和拖滑块一样，1:1 跟手。
import { useRef } from 'react'
import { niceStep, useSurface } from '../lib/gfx'
import { tr } from '../lib/i18n'
import { C } from './scale'

export interface LiqMark { x: number; label: string }

const signedX = (x: number) => (x > 0 ? '+' : x < 0 ? '−' : '') + Math.abs(x) + '%'

export function StressChart({ moves, equity, value, marks, onChange, hidden }: { moves: number[]; equity: number[]; value: number; marks: LiqMark[]; onChange: (x: number) => void; hidden?: boolean }) {
  const box = useRef({ x0: 0, x1: 0 })
  const { ref } = useSurface((g) => {
    if (moves.length < 2) return
    const x0 = 58, x1 = g.w - 24, y0 = 10, y1 = g.h - 22
    box.current = { x0, x1 }
    const lo = moves[0], hi = moves[moves.length - 1]
    const X = (m: number) => x0 + ((m - lo) / (hi - lo)) * (x1 - x0)
    const mid = equity[Math.floor(equity.length / 2)]
    const top = Math.max(...equity, mid, 1) * 1.04
    const Y = (v: number) => y1 - (Math.max(0, v) / top) * (y1 - y0)
    // 纵轴
    const step = niceStep(top / 3)
    for (let v = 0; v <= top; v += step) {
      g.hline(Y(v), x0, x1, v === 0 ? C.rule3 : C.grid)
      g.hline(Y(v), x0 - 4, x0, C.rule3)
      if (!hidden) g.text(v >= 10000 ? `${(v / 1000).toFixed(0)}K` : v.toFixed(0), x0 - 8, Y(v), { size: 11, color: C.t3, align: 'right', baseline: 'middle' })
    }
    // 横轴：每 10% 一个主刻度，每 5% 一个副刻度
    for (let m = lo; m <= hi; m += 5) {
      const major = m % 10 === 0
      g.vline(X(m), y1, y1 + (major ? 5 : 3), major ? C.rule3 : C.rule2)
      if (major) {
        g.vline(X(m), y0, y1, m === 0 ? C.rule3 : C.grid)
        if (m % 25 === 0 || g.w > 420) g.text(signedX(m), X(m), y1 + 15, { size: 11, color: m === 0 ? C.t1 : C.t3, align: 'center', baseline: 'middle' })
      }
    }
    // 现在的权益
    if (mid > 0) {
      const ctx = g.ctx
      ctx.fillStyle = C.rule2
      for (let x = x0; x < x1; x += 6) ctx.fillRect(g.X(x), g.Y(Y(mid)), g.px(3), 1)
    }
    // 曲线
    const ctx = g.ctx
    ctx.strokeStyle = C.white
    ctx.lineWidth = g.px(1)
    ctx.beginPath()
    moves.forEach((m, i) => { const a = g.X(X(m)) + 0.5, b = g.Y(Y(equity[i])) + 0.5; if (i) ctx.lineTo(a, b); else ctx.moveTo(a, b) })
    ctx.stroke()
    // 强平点
    for (const k of marks) {
      const i = moves.indexOf(k.x)
      if (i < 0) continue
      const px = X(k.x), py = Y(equity[i])
      g.rect(px - 3, py - 3, px + 3, py + 3, C.down)
      g.text(k.label, px, py - 9, { size: 11, color: C.t1, align: 'center', baseline: 'middle' })
    }
    // 当前位置
    const cur = X(value)
    g.vline(cur, y0, y1, C.white)
    const i = moves.indexOf(value)
    if (i >= 0) g.rect(cur - 3, Y(equity[i]) - 3, cur + 3, Y(equity[i]) + 3, C.white)
    g.text(signedX(value), cur, y1 + 15, { size: 11, color: '#000000', align: 'center', baseline: 'middle', plate: { color: '#ffffff', padX: 4, padY: 2 } })
  }, [moves, equity, value, marks, hidden])
  const pick = (e: React.PointerEvent<HTMLCanvasElement>) => {
    const r = e.currentTarget.getBoundingClientRect()
    const { x0, x1 } = box.current
    if (x1 <= x0 || !moves.length) return
    const lo = moves[0], hi = moves[moves.length - 1]
    const m = Math.round(lo + ((e.clientX - r.left - x0) / (x1 - x0)) * (hi - lo))
    onChange(Math.max(lo, Math.min(hi, m)))
  }
  return (
    <canvas ref={ref} className="rk-chart" role="img" aria-label={tr('rk.g.title')}
      onPointerDown={(e) => { e.currentTarget.setPointerCapture(e.pointerId); pick(e) }}
      onPointerMove={(e) => { if (e.buttons) pick(e) }} />
  )
}
