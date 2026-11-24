// 流动性频谱（D33 第一批 / D35 第 1 项）。
// 横轴时间、纵轴价格。每一档挂单是一条横向光带，越厚越亮；价格是一条白色阶梯线；
// 只有大额成交才画成圆点并标出数量，小额成交汇总到底部的成交量条里，画面不乱。
// 挂单突然撤掉（疑似虚假挂单）、大单被慢慢吃掉（吸收）自动标出来。
// 显卡画格子（HeatGL），2D 画布叠加刻度、成交、标注和读数。
import { useEffect, useRef } from 'react'
import { HeatGL, autoTone } from './heatgl'
import { C, cursorTag, dirColors, fmtNum, timeScale, valueScale, type Box } from './scale'
import { clock, makeG, type G } from '../lib/gfx'
import { qOf, type Book } from '../lib/demo'
import { approachAll, glide } from '../lib/motion'

interface Props {
  book: Book
  /** 初始可见时长（毫秒） */
  span?: number
  /** 右侧当前盘口剖面 */
  profile?: boolean
}

const AXIS = 68
const PROFILE = 72
const BOTTOM = 26
const VOL = 44   // 底部成交量条高度

type Print = { t: number; p: number; q: number; buy: boolean; x: number; y: number; r: number }

export function Spectrum({ book, span, profile = true }: Props) {
  const glc = useRef<HTMLCanvasElement>(null)
  const ovc = useRef<HTMLCanvasElement>(null)

  useEffect(() => {
    const gc = glc.current, oc = ovc.current
    if (!gc || !oc) return
    let gl: HeatGL | null = null
    try { gl = location.search.includes('nogl=1') ? null : new HeatGL(gc); gl?.setData(book.cols, book.rows, book.cells) } catch { gl = null }
    const ctx = oc.getContext('2d')!
    const agg = aggregateTrades(book)
    const T0 = book.t0, T1 = book.t0 + book.cols * book.dt
    const pLo = book.p0 - book.tick / 2, pHi = book.p0 + (book.rows - 0.5) * book.tick
    const lastMid = book.mid[book.cols - 1]
    const v = {
      ta: T1 - (span ?? T1 - T0), tb: T1,
      pa: lastMid - book.rows * book.tick * 0.2, pb: lastMid + book.rows * book.tick * 0.2,
    }
    const home = { ...v }
    const vt = { ...v }   // 目标视图：滚轮、复位、惯性改的是它，画面每帧平滑地追上去
    let lastFrame = performance.now()
    let trail: { t: number; x: number; y: number }[] = []
    let w = 0, h = 0, W = 0, H = 0, dirty = true, raf = 0
    let mouse: { x: number; y: number } | null = null
    let drag: { x: number; y: number; v: typeof v } | null = null
    let tone = { black: -1, white: 1 }, toneT = { black: 0.3, white: 0.95 }, toneKey = ''
    let prints: Print[] = []

    const plotOf = (): Box => ({ x0: 0, y0: 0, x1: w - AXIS - (profile ? PROFILE : 0), y1: h - BOTTOM - VOL })
    const xOf = (t: number, P: Box) => P.x0 + ((t - v.ta) / (v.tb - v.ta)) * (P.x1 - P.x0)
    const yOf = (p: number, P: Box) => P.y0 + (1 - (p - v.pa) / (v.pb - v.pa)) * (P.y1 - P.y0)

    const paint = () => {
      if (!w || !h) return
      const t0 = performance.now()
      paintInner()
      perf('spectrum', performance.now() - t0)
    }
    const paintInner = () => {
      for (const c of [gc, oc]) if (c.width !== W || c.height !== H) { c.width = W; c.height = H }
      const g = makeG(ctx, w, h, W, H, Date.now())
      const P = plotOf()
      // 自动对比度：视图变了才重算
      const key = [v.ta, v.tb, v.pa, v.pb].map((x) => x.toFixed(1)).join()
      if (key !== toneKey) {
        toneKey = key
        toneT = autoTone(book.cells, book.cols, book.rows,
          (v.ta - T0) / book.dt, (v.tb - T0) / book.dt, (v.pa - pLo) / book.tick, (v.pb - pLo) / book.tick)
        if (tone.black < 0) tone = { ...toneT }
      }
      // 对比度也平滑过渡，缩放时亮度不会一跳一跳
      if (approachAll(tone, toneT, 16, 8, 2e-3)) dirty = true
      if (gl) {
        const x = g.X(P.x0), y = g.Y(P.y0)
        gl.render(H, x, y, g.X(P.x1) - x, g.Y(P.y1) - y, {
          u0: (v.ta - T0) / (T1 - T0), u1: (v.tb - T0) / (T1 - T0),
          v0: (v.pa - pLo) / (pHi - pLo), v1: (v.pb - pLo) / (pHi - pLo),
        }, { ...tone, gamma: 1.9, tint: 0.7 })
      }
      ctx.setTransform(1, 0, 0, 1, 0, 0)
      ctx.clearRect(0, 0, W, H)
      if (!gl) g.rect(P.x0, P.y0, P.x1, P.y1, C.void)
      g.rect(P.x1, 0, w, h, C.void)
      g.rect(0, P.y1, w, h, C.void)
      overlay(g, P)
    }

    const overlay = (g: G, P: Box) => {
      const dc = dirColors()
      const ctx = g.ctx
      const k = g.kx
      const labels: { x0: number; y0: number; x1: number; y1: number }[] = []
      const free = (x0: number, y0: number, x1: number, y1: number) => {
        if (x0 < P.x0 + 2 || x1 > P.x1 - 2 || y0 < P.y0 + 2 || y1 > P.y1 - 2) return false
        return !labels.some((b) => x0 < b.x1 + 4 && x1 > b.x0 - 4 && y0 < b.y1 + 2 && y1 > b.y0 - 2)
      }
      const label = (s: string, x: number, y: number, color: string, ink = C.t1) => {
        const tw = g.measure(s, { size: 12, weight: 500 })
        // 依次尝试：右上、右下、左上、左下，找一个不和别的标注重叠的位置
        const tries: [number, number][] = [[x + 8, y - 20], [x + 8, y + 4], [x - 8 - tw - 10, y - 20], [x - 8 - tw - 10, y + 4]]
        for (const [lx, ly] of tries) {
          const box = { x0: lx, y0: ly, x1: lx + tw + 10, y1: ly + 18 }
          if (!free(box.x0, box.y0, box.x1, box.y1)) continue
          labels.push(box)
          g.rect(box.x0, box.y0, box.x1, box.y1, 'rgba(0,0,0,0.82)')
          g.rect(box.x0, box.y0, box.x0 + 2 / k, box.y1, color)
          g.text(s, box.x0 + 6, box.y0 + 9.5, { size: 12, weight: 500, color: ink, baseline: 'middle' })
          return true
        }
        return false
      }

      ctx.save()
      ctx.beginPath()
      ctx.rect(g.X(P.x0), g.Y(P.y0), g.X(P.x1) - g.X(P.x0), g.Y(P.y1) - g.Y(P.y0))
      ctx.clip()

      // 1. 价格：白色阶梯线（价格保持到下一次变化），1 个物理像素
      const c0 = Math.max(0, Math.floor((v.ta - T0) / book.dt) - 1)
      const c1 = Math.min(book.cols - 1, Math.ceil((v.tb - T0) / book.dt) + 1)
      ctx.beginPath()
      let started = false, py = 0
      for (let c = c0; c <= c1; c++) {
        const x = g.X(xOf(T0 + c * book.dt, P)) + 0.5
        const y = g.Y(yOf(book.mid[c], P)) + 0.5
        if (!started) { ctx.moveTo(x, y); started = true } else { ctx.lineTo(x, py); ctx.lineTo(x, y) }
        py = y
      }
      ctx.strokeStyle = C.white
      ctx.lineWidth = 1
      ctx.stroke()

      // 2. 大额成交：同一时段、同一价位的成交合并（用预先按秒汇总好的数据，重画时不再逐笔遍历）
      const bucketMs = Math.max(book.dt, Math.ceil(((v.tb - v.ta) / (P.x1 - P.x0)) * 6 / book.dt) * book.dt)
      const perBucket = bucketMs / book.dt
      const cA = Math.max(0, Math.floor((v.ta - T0) / book.dt)), cB = Math.min(book.cols - 1, Math.ceil((v.tb - T0) / book.dt))
      const keyIdx = new Map<number, number>()
      const aT: number[] = [], aP: number[] = [], aB: number[] = [], aS: number[] = []
      for (let c = cA; c <= cB; c++) {
        const bk = Math.floor(c / perBucket)
        for (let j = agg.start[c]; j < agg.start[c + 1]; j++) {
          const key = bk * book.rows + agg.row[j]
          let idx = keyIdx.get(key)
          if (idx === undefined) {
            idx = aT.length
            keyIdx.set(key, idx)
            aT.push(T0 + (bk + 0.5) * bucketMs); aP.push(book.p0 + agg.row[j] * book.tick); aB.push(0); aS.push(0)
          }
          aB[idx] += agg.b[j]; aS[idx] += agg.s[j]
        }
      }
      const all = aT.map((t, i) => ({ t, p: aP[i], b: aB[i], s: aS[i], q: aB[i] + aS[i] }))
      const sizes = all.map((a) => a.q).sort((a, b) => a - b)
      const thr = Math.max(2, sizes[Math.floor(sizes.length * 0.985)] ?? Infinity)
      const top = sizes[sizes.length - 1] ?? 1
      prints = all.filter((a) => a.q >= thr).sort((a, b) => a.q - b.q).map((a) => {
        const buy = a.b >= a.s
        const x = xOf(a.t, P), y = yOf(a.p, P)
        const r = 3 + 6 * Math.sqrt((a.q - thr) / Math.max(top - thr, 1e-9))
        return { t: a.t, p: a.p, q: a.q, buy, x, y, r }
      })
      for (const pr of prints) {
        const X = g.X(pr.x), Y = g.Y(pr.y), R = pr.r * k
        ctx.beginPath()
        ctx.arc(X, Y, R, 0, Math.PI * 2)
        ctx.fillStyle = pr.buy ? dc.up : dc.down
        ctx.globalAlpha = 0.9
        ctx.fill()
        ctx.globalAlpha = 1
        ctx.lineWidth = Math.max(1, Math.round(k))
        ctx.strokeStyle = '#000'
        ctx.stroke()
      }
      // 最大的几笔标上数量
      const biggest = [...prints].sort((a, b) => b.q - a.q).slice(0, 6)
      for (const pr of biggest) label(`${pr.buy ? '买' : '卖'} ${fmtQty(pr.q)}`, pr.x + pr.r - 4, pr.y, pr.buy ? dc.up : dc.down)

      // 3. 事件：撤单（虚假挂单）、吸收
      for (const wl of book.walls) {
        if (wl.kind === 'wall') continue
        const ta = T0 + wl.a * book.dt, tb = T0 + wl.b * book.dt
        if (tb < v.ta || ta > v.tb) continue
        const y = yOf(wl.p, P)
        if (wl.kind === 'spoof') {
          const x = xOf(tb, P)
          cross(g, x, y, C.amber)
          label(`撤单 ${fmtQty(wl.size)}`, x, y, C.amber)
        } else {
          const xa = xOf(ta, P), xb = xOf(tb, P)
          // 在大单那一档的外侧画一道琥珀色细线，两端各一个短刻度，标出被吃掉的时间段
          const yy = wl.bid ? y + 4 : y - 4
          g.hline(yy, xa, xb, C.amber)
          g.vline(xa, Math.min(y, yy) + (wl.bid ? 1 : 0), Math.max(y, yy), C.amber); g.vline(xb, Math.min(y, yy), Math.max(y, yy), C.amber)
          label(`吸收 ${fmtQty(wl.size)}，最后被击穿`, xb, yy, C.amber)
        }
      }
      ctx.restore()

      // 4. 底部成交量条：每个时间段的主动买入（向上）和主动卖出（向下）
      const V: Box = { x0: P.x0, y0: P.y1, x1: P.x1, y1: P.y1 + VOL }
      g.hline(V.y0, V.x0, w - AXIS, C.rule2)
      const nb = Math.max(1, Math.floor((P.x1 - P.x0) / 3))
      const bw = (v.tb - v.ta) / nb
      const vb = new Float32Array(nb), vs = new Float32Array(nb)
      for (let c = cA; c <= cB; c++) {
        const j = Math.floor((T0 + (c + 0.5) * book.dt - v.ta) / bw)
        if (j < 0 || j >= nb) continue
        vb[j] += agg.colB[c]; vs[j] += agg.colS[c]
      }
      let vmax = 0
      for (let j = 0; j < nb; j++) vmax = Math.max(vmax, vb[j], vs[j])
      const vm = (V.y0 + V.y1) / 2
      g.hline(vm, V.x0, V.x1, C.rule)
      const cw = Math.max(1, g.X(V.x0 + (V.x1 - V.x0) / nb) - g.X(V.x0) - 1)
      for (let j = 0; j < nb; j++) {
        const x = g.X(V.x0 + (j / nb) * (V.x1 - V.x0))
        const hb = Math.round((vb[j] / (vmax || 1)) * (VOL / 2 - 4) * g.ky)
        const hs = Math.round((vs[j] / (vmax || 1)) * (VOL / 2 - 4) * g.ky)
        ctx.fillStyle = dc.up; ctx.globalAlpha = 0.75
        if (hb > 0) ctx.fillRect(x, g.Y(vm) - hb, cw, hb)
        ctx.fillStyle = dc.down
        if (hs > 0) ctx.fillRect(x, g.Y(vm) + 1, cw, hs)
        ctx.globalAlpha = 1
      }
      g.text('主动买', w - AXIS + 8, V.y0 + 11, { size: 11, color: C.t3, baseline: 'middle' })
      g.text('主动卖', w - AXIS + 8, V.y1 - 10, { size: 11, color: C.t3, baseline: 'middle' })

      // 5. 刻度尺
      const dec = book.tick >= 1 ? 0 : 1
      const lastC = Math.min(book.cols - 1, Math.max(0, Math.floor((Math.min(v.tb, T1) - T0) / book.dt) - 1))
      const curY = yOf(book.mid[lastC], P)
      valueScale(g, { x0: P.x0, y0: P.y0, x1: w - AXIS, y1: P.y1 }, v.pa, v.pb, (x) => fmtNum(x, dec), { grid: false, avoid: [curY] })
      timeScale(g, { ...P, y1: V.y1 }, v.ta, v.tb, { grid: false, avoidX: mouse && mouse.x >= P.x0 && mouse.x <= P.x1 ? mouse.x : undefined })

      // 6. 右侧当前盘口：横条长度 = 挂单量（和左边同一套亮度）
      if (profile) {
        const x0 = P.x1, x1 = w - AXIS
        g.vline(x0, P.y0, P.y1, C.rule2)
        const rA = Math.max(0, Math.floor((v.pa - pLo) / book.tick)), rB = Math.min(book.rows - 1, Math.ceil((v.pb - pLo) / book.tick))
        const rowPx = (P.y1 - P.y0) / ((v.pb - v.pa) / book.tick)
        for (let r = rA; r <= rB; r++) {
          const val = book.cells[r * book.cols + lastC] / 255
          const l = Math.pow(Math.max(0, Math.min(1, (val - tone.black) / (tone.white - tone.black))), 1.9)
          const len = l * (x1 - x0 - 10)
          if (len < 0.5) continue
          const y = yOf(book.p0 + r * book.tick, P)
          const hh = Math.max(rowPx - (rowPx * g.ky >= 5 ? 1 / g.ky : 0), 1 / g.ky)
          const a = 0.25 + 0.75 * l
          g.rect(x0 + 5, y - hh / 2, x0 + 5 + len, y + hh / 2, `rgba(255,255,255,${a.toFixed(3)})`)
        }
        g.text('当前挂单', x0 + 6, P.y1 + 11, { size: 11, color: C.t3, baseline: 'middle' })
      }
      g.hline(curY, P.x1, w - AXIS + 6, C.white)
      cursorTag(g, w - AXIS + 6, curY, fmtNum(book.mid[lastC], dec))

      // 7. 十字准线读数：指到大额成交上显示成交详情，否则显示这一格的挂单量
      const m = mouse
      if (m && m.x > P.x0 && m.x < P.x1 && m.y > P.y0 && m.y < P.y1 && !drag) {
        const t = v.ta + ((m.x - P.x0) / (P.x1 - P.x0)) * (v.tb - v.ta)
        const p = v.pa + (1 - (m.y - P.y0) / (P.y1 - P.y0)) * (v.pb - v.pa)
        const row = Math.round((p - book.p0) / book.tick)
        const pr = book.p0 + row * book.tick
        const col = Math.floor((t - T0) / book.dt)
        dashedV(g, m.x, P.y0, V.y1)
        dashedH(g, yOf(pr, P), P.x0, P.x1)
        cursorTag(g, w - AXIS + 6, yOf(pr, P), fmtNum(pr, dec), { color: C.plate, ink: C.t1 })
        cursorTag(g, m.x, V.y1 + 13, clock(t), { color: C.plate, ink: C.t1 })
        const hit = prints.find((q) => Math.hypot(q.x - m.x, q.y - m.y) <= q.r + 3)
        let lines: string[]
        if (hit) {
          lines = [`${hit.buy ? '主动买入' : '主动卖出'} ${fmtQty(hit.q)} BTC`, `价格 ${fmtNum(hit.p, dec)}　${clock(hit.t)}`]
        } else if (col >= 0 && col < book.cols && row >= 0 && row < book.rows) {
          const q = qOf(book.cells[row * book.cols + col])
          lines = [`挂单 ${fmtQty(q)} BTC`, `${p > book.mid[col] ? '卖单（价格上方）' : '买单（价格下方）'}`]
        } else lines = []
        if (lines.length) tooltip(g, m.x, m.y, lines, P)
      }
    }

    // 性能测试钩子（?perf=1）：连续重画 n 次，每次等显卡画完（gl.finish），返回平均耗时。
    // 用来在用户的真实显卡上测，不依赖页面动画帧（浏览器窗格在后台时动画帧会暂停）。
    if (PERF_ON) {
      (window as unknown as Record<string, unknown>).__qsBench = (n = 60) => {
        const glctx = gc.getContext('webgl2')
        const t0 = performance.now()
        for (let i = 0; i < n; i++) {
          v.ta += (v.tb - v.ta) * 0.002; v.tb += (v.tb - v.ta) * 0.002
          paint()
          glctx?.finish()
        }
        const ms = (performance.now() - t0) / n
        Object.assign(v, home)
        dirty = true
        return { avgMsPerFrame: +ms.toFixed(2), canvasDevicePx: [W, H], gl: !!gl }
      }
    }
    const ro = new ResizeObserver((es) => {
      const e = es[0]
      w = e.contentRect.width; h = e.contentRect.height
      const d = e.devicePixelContentBoxSize?.[0]
      W = d ? d.inlineSize : Math.round(w * devicePixelRatio)
      H = d ? d.blockSize : Math.round(h * devicePixelRatio)
      paint(); dirty = false
    })
    try { ro.observe(oc, { box: 'device-pixel-content-box' }) } catch { ro.observe(oc) }
    const loop = () => {
      raf = requestAnimationFrame(loop)
      const now = performance.now(), dt = Math.min(64, now - lastFrame)
      lastFrame = now
      if (!drag && approachAll(v, vt, dt, 14, { ta: (vt.tb - vt.ta) * 2e-4, tb: (vt.tb - vt.ta) * 2e-4, pa: (vt.pb - vt.pa) * 2e-4, pb: (vt.pb - vt.pa) * 2e-4 })) dirty = true
      if (dirty) { paint(); dirty = false }
    }
    raf = requestAnimationFrame(loop)

    // 交互：滚轮缩放时间（以鼠标为中心），按住 Shift 或在价格尺上滚动缩放价格；拖动平移；双击复位
    const pos = (e: MouseEvent) => { const r = oc.getBoundingClientRect(); return { x: e.clientX - r.left, y: e.clientY - r.top } }
    const onWheel = (e: WheelEvent) => {
      e.preventDefault()
      const m = pos(e), P = plotOf()
      // 触控板的 deltaY 很小、很密；鼠标滚轮一格大约 100。统一换算成缩放倍数
      const f = Math.exp(Math.max(-1, Math.min(1, e.deltaY * (e.deltaMode === 1 ? 0.05 : 0.0018))))
      if (e.shiftKey || m.x > P.x1) {
        const p = vt.pa + (1 - (m.y - P.y0) / (P.y1 - P.y0)) * (vt.pb - vt.pa)
        const sp = Math.min(Math.max((vt.pb - vt.pa) * f, book.tick * 12), pHi - pLo)
        const a = (p - vt.pa) / (vt.pb - vt.pa)
        vt.pa = p - a * sp; vt.pb = vt.pa + sp
      } else {
        const t = vt.ta + ((m.x - P.x0) / (P.x1 - P.x0)) * (vt.tb - vt.ta)
        const sp = Math.min(Math.max((vt.tb - vt.ta) * f, book.dt * 30), (T1 - T0) * 1.05)
        const a = (t - vt.ta) / (vt.tb - vt.ta)
        vt.ta = t - a * sp; vt.tb = vt.ta + sp
      }
    }
    const onDown = (e: PointerEvent) => {
      Object.assign(vt, v)
      drag = { ...pos(e), v: { ...v } }
      trail = [{ t: performance.now(), ...pos(e) }]
      oc.setPointerCapture(e.pointerId)
    }
    const onMove = (e: PointerEvent) => {
      mouse = pos(e)
      if (drag) {
        const P = plotOf()
        const dx = ((mouse.x - drag.x) / (P.x1 - P.x0)) * (drag.v.tb - drag.v.ta)
        const dy = ((mouse.y - drag.y) / (P.y1 - P.y0)) * (drag.v.pb - drag.v.pa)
        v.ta = drag.v.ta - dx; v.tb = drag.v.tb - dx
        v.pa = drag.v.pa + dy; v.pb = drag.v.pb + dy
        Object.assign(vt, v)
        const now = performance.now()
        trail.push({ t: now, x: mouse.x, y: mouse.y })
        trail = trail.filter((q) => now - q.t < 90)
      }
      dirty = true
    }
    const onUp = () => {
      // 松手后按最后 90 毫秒的速度继续滑一小段（惯性）
      if (drag && trail.length > 1) {
        const a = trail[0], b = trail[trail.length - 1]
        const dtm = Math.max(1, b.t - a.t)
        const P = plotOf()
        const gx = glide((b.x - a.x) / dtm), gy = glide((b.y - a.y) / dtm)
        const dx = (gx / (P.x1 - P.x0)) * (v.tb - v.ta), dy = (gy / (P.y1 - P.y0)) * (v.pb - v.pa)
        vt.ta = v.ta - dx; vt.tb = v.tb - dx; vt.pa = v.pa + dy; vt.pb = v.pb + dy
      }
      drag = null; dirty = true
    }
    const onLeave = () => { mouse = null; dirty = true }
    const onDbl = () => { Object.assign(vt, home) }
    oc.addEventListener('wheel', onWheel, { passive: false })
    oc.addEventListener('pointerdown', onDown)
    oc.addEventListener('pointermove', onMove)
    oc.addEventListener('pointerup', onUp)
    oc.addEventListener('pointerleave', onLeave)
    oc.addEventListener('dblclick', onDbl)
    return () => {
      cancelAnimationFrame(raf); ro.disconnect()
      oc.removeEventListener('wheel', onWheel)
      oc.removeEventListener('pointerdown', onDown)
      oc.removeEventListener('pointermove', onMove)
      oc.removeEventListener('pointerup', onUp)
      oc.removeEventListener('pointerleave', onLeave)
      oc.removeEventListener('dblclick', onDbl)
    }
  }, [book, span, profile])

  return (
    <div className="surface">
      <canvas ref={glc} />
      <canvas ref={ovc} className="interactive" />
    </div>
  )
}

/** 频谱的图例：放在显示屏标题栏里，让第一次看的人知道每样东西是什么意思 */
/** 性能记录（?perf=1 时开启）：每次重画的耗时，放在 window.__qsPerf 里给自动化测试读 */
const PERF_ON = typeof location !== 'undefined' && location.search.includes('perf=1')
export function perf(name: string, ms: number) {
  if (!PERF_ON) return
  const w = window as unknown as { __qsPerf?: Record<string, number[]> }
  const m = (w.__qsPerf ??= {})
  const a = (m[name] ??= [])
  a.push(ms)
  if (a.length > 2000) a.shift()
}

export function SpectrumLegend() {
  return (
    <span className="legend">
      <span><i className="lg-line" />价格</span>
      <span><i className="lg-band" />挂单，越亮越厚</span>
      <span><i className="lg-dot up" />大额主动买入</span>
      <span><i className="lg-dot down" />大额主动卖出</span>
      <span><i className="lg-x" />撤单 / 吸收</span>
    </span>
  )
}

/** 成交按"每列（时间）× 每档（价格）"预先汇总，存成紧凑数组：start[c]..start[c+1] 是第 c 列的各档 */
function aggregateTrades(book: Book) {
  const per: Map<number, { b: number; s: number }>[] = Array.from({ length: book.cols }, () => new Map())
  const colB = new Float64Array(book.cols), colS = new Float64Array(book.cols)
  for (const t of book.trades) {
    const c = Math.floor((t.t - book.t0) / book.dt)
    if (c < 0 || c >= book.cols) continue
    const r = Math.round((t.p - book.p0) / book.tick)
    let a = per[c].get(r)
    if (!a) per[c].set(r, (a = { b: 0, s: 0 }))
    if (t.buy) { a.b += t.q; colB[c] += t.q } else { a.s += t.q; colS[c] += t.q }
  }
  const start = new Int32Array(book.cols + 1)
  let n = 0
  for (let c = 0; c < book.cols; c++) { start[c] = n; n += per[c].size }
  start[book.cols] = n
  const row = new Int32Array(n), b = new Float64Array(n), s = new Float64Array(n)
  let k = 0
  for (let c = 0; c < book.cols; c++) for (const [r, a] of per[c]) { row[k] = r; b[k] = a.b; s[k] = a.s; k++ }
  return { start, row, b, s, colB, colS }
}
export function fmtQty(v: number): string {
  if (v >= 100) return v.toFixed(0)
  if (v >= 10) return v.toFixed(1)
  return v.toFixed(2)
}
function cross(g: G, x: number, y: number, color: string) {
  const ctx = g.ctx, X = g.X(x) + 0.5, Y = g.Y(y) + 0.5, s = g.px(4)
  ctx.strokeStyle = color
  ctx.lineWidth = g.px(1.25)
  ctx.beginPath()
  ctx.moveTo(X - s, Y - s); ctx.lineTo(X + s, Y + s)
  ctx.moveTo(X + s, Y - s); ctx.lineTo(X - s, Y + s)
  ctx.stroke()
}
function tooltip(g: G, x: number, y: number, lines: string[], P: Box) {
  const wMax = Math.max(...lines.map((s) => g.measure(s, { size: 12, weight: 500 })))
  const bw = wMax + 20, bh = lines.length * 18 + 12
  let bx = x + 16, by = y - bh - 10
  if (bx + bw > P.x1 - 4) bx = x - 16 - bw
  if (by < P.y0 + 4) by = y + 16
  g.rect(bx, by, bx + bw, by + bh, 'rgba(17,17,19,0.94)')
  g.hline(by, bx, bx + bw, C.rule2); g.hline(by + bh - 1 / g.ky, bx, bx + bw, C.rule2)
  g.vline(bx, by, by + bh, C.rule2); g.vline(bx + bw - 1 / g.kx, by, by + bh, C.rule2)
  lines.forEach((s, i) => g.text(s, bx + 10, by + 15 + i * 18, { size: 12, weight: i ? 420 : 500, color: i ? C.t2 : C.t1, baseline: 'middle' }))
}
export function dashedV(g: G, x: number, y0: number, y1: number, color = 'rgba(255,255,255,0.4)') {
  for (let y = y0; y < y1; y += 4) g.vline(x, y, Math.min(y + 2, y1), color)
}
export function dashedH(g: G, y: number, x0: number, x1: number) {
  for (let x = x0; x < x1; x += 4) g.hline(y, x, Math.min(x + 2, x1), 'rgba(255,255,255,0.4)')
}
