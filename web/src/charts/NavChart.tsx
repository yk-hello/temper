// 策略净值图（3.22）：净值收益率叠 BTC 持有，下面是回撤水下图，共用一条时间轴。
// 鼠标移上去读数立刻出（不淡入），所有线对齐物理像素。
import { locDec, numLocale } from '../lib/tz'
import { useRef } from 'react'
import { useSurface } from '../lib/gfx'
import { fmtTime, tr } from '../lib/i18n'
import { C, dirColors, niceRange, timeScale, valueScale } from './scale'

export interface NavPoint { t: number; nav: number }
export interface PxPoint { t: number; c: number }

const pct = (v: number, d = 2) => (v > 0 ? '+' : v < 0 ? '−' : '') + locDec(Math.abs(v * 100).toFixed(d)) + '%'

export function NavChart({ curve, initial, btc, holdLabel }: { curve: NavPoint[]; initial: number; btc: PxPoint[]; holdLabel?: string }) {
  const hover = useRef<number | null>(null)
  const { ref, redraw } = useSurface((g) => {
    if (curve.length < 2) {
      g.text(tr('ai.curveWait'), g.w / 2, g.h / 2, { size: 13, color: C.t3, align: 'center', baseline: 'middle' })
      return
    }
    const t0 = curve[0].t, t1 = curve[curve.length - 1].t
    const wAxis = 58
    const split = Math.round(g.h * 0.72)
    const top = { x0: 0, y0: 10, x1: g.w - wAxis, y1: split - 10 }
    const bot = { x0: 0, y0: split + 2, x1: g.w - wAxis, y1: g.h - 22 }
    const xOf = (t: number) => top.x0 + ((t - t0) / Math.max(t1 - t0, 1)) * (top.x1 - top.x0)
    const ret = curve.map((p) => p.nav / initial - 1)
    // BTC 持有：从第一个净值点开始算
    const b0 = btc.find((x) => x.t >= t0) ?? btc[0]
    const bPts = b0 ? btc.filter((x) => x.t >= b0.t && x.t <= t1).map((x) => ({ t: x.t, r: x.c / b0.c - 1 })) : []
    let lo = Math.min(0, ...ret), hi = Math.max(0, ...ret)
    for (const p of bPts) { lo = Math.min(lo, p.r); hi = Math.max(hi, p.r) }
    const [yLo, yHi] = niceRange(lo, hi, 0.1, 0.0001)
    const fmtV = (v: number) => pct(v, hi - lo < 0.02 ? 2 : 1)
    const yOf = valueScale(g, top, Math.min(yLo, -0.001), Math.max(yHi, 0.001), fmtV)
    g.hline(yOf(0), top.x0, top.x1, C.rule3)
    const ctx = g.ctx
    const stroke = (xs: { x: number; y: number }[], color: string, w = 1) => {
      ctx.beginPath()
      xs.forEach((p, i) => { const X = g.X(p.x), Y = g.Y(p.y); if (i) ctx.lineTo(X, Y); else ctx.moveTo(X, Y) })
      ctx.lineWidth = g.px(w); ctx.strokeStyle = color; ctx.stroke()
    }
    if (bPts.length > 1) stroke(bPts.map((p) => ({ x: xOf(p.t), y: yOf(p.r) })), C.t3)
    // 净值和 0% 线之间铺一层淡光，缩小了也认得出走势
    const y0 = yOf(0)
    const grad = ctx.createLinearGradient(0, g.Y(top.y0), 0, g.Y(top.y1))
    grad.addColorStop(0, 'rgba(255,255,255,0.13)')
    grad.addColorStop(1, 'rgba(255,255,255,0.13)')
    ctx.beginPath()
    ctx.moveTo(g.X(xOf(curve[0].t)), g.Y(y0))
    curve.forEach((p, i) => ctx.lineTo(g.X(xOf(p.t)), g.Y(yOf(ret[i]))))
    ctx.lineTo(g.X(xOf(t1)), g.Y(y0))
    ctx.closePath()
    ctx.fillStyle = grad
    ctx.fill()
    stroke(curve.map((p, i) => ({ x: xOf(p.t), y: yOf(ret[i]) })), C.white, 2)

    // 回撤水下图
    const dd: number[] = []
    let peak = initial
    for (const p of curve) { peak = Math.max(peak, p.nav); dd.push(p.nav / peak - 1) }
    const ddLo = Math.min(-0.001, ...dd) * 1.15
    const yDD = valueScale(g, bot, ddLo, 0, (v) => pct(v, 1))
    const { down } = dirColors()
    ctx.beginPath()
    ctx.moveTo(g.X(xOf(curve[0].t)), g.Y(yDD(0)))
    curve.forEach((p, i) => ctx.lineTo(g.X(xOf(p.t)), g.Y(yDD(dd[i]))))
    ctx.lineTo(g.X(xOf(t1)), g.Y(yDD(0)))
    ctx.closePath()
    ctx.fillStyle = down + '38'
    ctx.fill()
    stroke(curve.map((p, i) => ({ x: xOf(p.t), y: yDD(dd[i]) })), down)
    timeScale(g, bot, t0, t1, { withSec: false })
    g.hline(split, 0, g.w, C.rule)

    // 悬停读数（立刻出）
    const hx = hover.current
    if (hx !== null && hx >= top.x0 && hx <= top.x1) {
      const t = t0 + ((hx - top.x0) / (top.x1 - top.x0)) * (t1 - t0)
      let i = 0
      while (i + 1 < curve.length && curve[i + 1].t <= t) i++
      const x = xOf(curve[i].t)
      g.vline(x, top.y0, bot.y1, C.rule3)
      let bTxt = ''
      if (bPts.length) {
        let j = 0
        while (j + 1 < bPts.length && bPts[j + 1].t <= t) j++
        bTxt = `  ${holdLabel ?? tr('ai.btcHold')} ${pct(bPts[j].r)}`
      }
      const text = `${fmtTime(curve[i].t)}  ${tr('ai.nav')} ${curve[i].nav.toLocaleString(numLocale(), { maximumFractionDigits: 2 })}  ${pct(ret[i])}${bTxt}  ${tr('ai.dd')} ${pct(dd[i])}`
      g.text(text, 8, top.y0 + 6, { size: 12, color: C.t1, baseline: 'middle', plate: { color: C.plate, padX: 6, padY: 4 } })
    }
  }, [curve, initial, btc])
  return (
    <div className="nav-chart">
      <canvas ref={ref}
        onPointerMove={(e) => { hover.current = e.nativeEvent.offsetX; redraw() }}
        onPointerLeave={() => { hover.current = null; redraw() }} />
    </div>
  )
}
