// 频谱分析仪（介绍页，D85）：真实盘口画成一排光柱。横轴价格，纵轴挂单金额（对数，像仪器的 dB 刻度）。
// 光柱涨得快、落得慢，顶上有悬停的峰值条。只用 2D 画布，弱设备上自动关发光、降低刷新。
// 没有假数据：断线就写断线。
import { useEffect, useRef } from 'react'
import { clock, makeG, niceStep, type G } from '../lib/gfx'
import { cellUSD, decodeB64, usdCellF, type HeatFrame, type HeatWall } from '../lib/heat'
import { fmtCompactL, tr } from '../lib/i18n'
import { C, fmtNum } from './scale'

export interface AnalyzerRead { mid: number; flow?: [number, number]; walls?: HeatWall[]; cov?: [number, number]; state: 'wait' | 'live' | 'sync' | 'lost' | 'none'; lite: boolean }

const SPAN = 0.006 // 现价上下 0.6%
const N = 4096
const DECADES = [1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8]
const RANGE = 138 // 纵轴显示的宽度（单位是盘口编码的一格），约 3 个数量级

export function Analyzer({ inst, dec, onRead, clockRef }: { inst: string; dec: number; onRead: (r: AnalyzerRead) => void; clockRef?: React.RefObject<HTMLElement> }) {
  const ref = useRef<HTMLCanvasElement>(null)
  const cb = useRef(onRead)
  cb.current = onRead
  // 小数位放在引用里：它变了不能把整台仪器重启
  const decRef = useRef(dec)
  decRef.current = dec
  useEffect(() => {
    const canvas = ref.current
    if (!canvas) return
    const ctx = canvas.getContext('2d', { alpha: false })
    if (!ctx) return
    const still = matchMedia('(prefers-reduced-motion: reduce)').matches
    canvas.dataset.lite = '0'
    let w = 0, h = 0, W = 0, H = 0
    const ro = new ResizeObserver((es) => {
      const e = es[0]
      w = e.contentRect.width; h = e.contentRect.height
      const d = e.devicePixelContentBoxSize?.[0]
      W = d ? d.inlineSize : Math.round(w * devicePixelRatio)
      H = d ? d.blockSize : Math.round(h * devicePixelRatio)
      canvas.width = W; canvas.height = H
    })
    try { ro.observe(canvas, { box: 'device-pixel-content-box' }) } catch { ro.observe(canvas) }

    // 盘口放在一段固定长度的数组里，下标 = 桶号 - base。现价走远了再整体挪。
    let width = 0, base = 0, mid = 0, center = 0, lastFrame = 0
    let sync = false, cov: [number, number] | undefined, everData = false
    const tgt = new Float32Array(N), cur = new Float32Array(N), peak = new Float32Array(N)
    let cTop = 0
    const spikes: { p: number; usd: number; t: number }[] = []
    let walls: HeatWall[] | undefined, flow: [number, number] | undefined
    let state: AnalyzerRead['state'] = 'wait'
    let lite = false

    const rebase = (b: number) => {
      const nb = b - (N >> 2)
      const d = nb - base
      if (!base) { base = nb; return }
      if (d === 0) return
      for (const a of [tgt, cur, peak]) {
        if (Math.abs(d) >= N) { a.fill(0); continue }
        if (d > 0) { a.copyWithin(0, d); a.fill(0, N - d) } else { a.copyWithin(-d, 0, N + d); a.fill(0, 0, -d) }
      }
      base = nb
    }
    const onFrame = (f: HeatFrame) => {
      if (f.flow) { flow = f.flow; walls = f.walls; return }
      lastFrame = performance.now()
      everData = true
      sync = !!f.sync
      if (f.cov) cov = f.cov
      if (f.m > 0) { mid = f.m; if (!center) center = mid }
      if (f.w > 0) width = f.w
      if (f.tr) for (const x of f.tr) {
        const usd = x.p * x.q
        if (usd >= 5000) spikes.push({ p: x.p, usd, t: lastFrame })
      }
      if (spikes.length > 80) spikes.splice(0, spikes.length - 80)
      if (!f.c || !width) return
      const c = decodeB64(f.c)
      if (!base || f.b < base + 64 || f.b + c.length > base + N - 64) rebase(f.b)
      tgt.fill(0)
      const off = f.b - base
      for (let i = 0; i < c.length; i++) {
        const j = off + i
        if (j >= 0 && j < N) tgt[j] = c[i]
      }
    }

    let es: EventSource | null = null
    let retry = 0, closed = false
    const connect = () => {
      if (closed) return
      es = new EventSource(`/api/heat/stream?instrument=${encodeURIComponent(inst)}`)
      es.onmessage = (m) => { try { onFrame(JSON.parse(m.data) as HeatFrame) } catch { /* 跳过坏帧 */ } }
      es.onerror = () => {
        es?.close()
        if (!everData) state = 'none'
        retry = window.setTimeout(connect, 2500)
      }
    }
    connect()

    let raf = 0, last = performance.now(), avg = 16, slow = 0
    let born = 0
    let lastPaint = 0
    let mouse: { x: number; y: number } | null = null
    let markers: { bid: number; ask: number; at: number } = { bid: -1, ask: -1, at: 0 }
    const tell = () => cb.current({ mid, flow, walls, cov, state, lite })
    let told = 0

    const paint = (g: G, now: number, dt: number) => {
      const gctx = g.ctx
      gctx.fillStyle = '#000'; gctx.fillRect(0, 0, g.W, g.H)
      const narrow = g.w < 620
      const x0 = narrow ? 38 : 58, x1 = g.w - (narrow ? 8 : 14), y0 = 12, y1 = g.h - 24
      if (x1 - x0 < 40 || y1 - y0 < 40) return
      const ease = 1 - Math.exp(-dt * 6)
      if (center && mid) center += (mid - center) * ease
      const halfP = center * SPAN
      const Xp = (p: number) => x0 + ((p - (center - halfP)) / (2 * halfP)) * (x1 - x0)
      const warm = still ? 1 : born ? Math.min(1, (now - born) / 900) : 0
      // 纵轴贴着数据走：最高的柱子快到顶，不留一大截空白
      const cBot = () => cTop - RANGE
      const Yc = (c: number) => y1 - Math.max(0, Math.min(1, (c - cBot()) / RANGE)) * (y1 - y0)
      // 刻度：竖 10 格、横按十倍金额，像仪器的分度
      for (let i = 0; i <= 10; i++) {
        const x = x0 + (i / 10) * (x1 - x0)
        const edge = i === 0 || i === 10
        for (let y = y0; y < y1; y += 5) g.vline(x, y, Math.min(y + (edge ? 5 : 1), y1), edge ? C.rule2 : 'rgba(255,255,255,0.13)')
      }
      for (const u of DECADES) {
        if (!cTop) break
        const c = usdCellF(u)
        if (c <= cBot() || c >= cTop) continue
        const y = Yc(c)
        for (let x = x0; x < x1; x += 5) g.hline(y, x, Math.min(x + 1, x1), 'rgba(255,255,255,0.13)')
        g.hline(y, x0 - 4, x0, C.rule3)
        g.text(fmtCompactL(u), x0 - 7, y, { size: 11, color: C.t3, align: 'right', baseline: 'middle' })
      }
      g.hline(y1, x0, x1, C.rule3)
      if (!mid || !width) {
        const msg = state === 'none' ? tr('ab.none') : tr('ab.wait')
        g.text(msg, (x0 + x1) / 2, (y0 + y1) / 2, { size: 13, color: C.t3, align: 'center', baseline: 'middle' })
        return
      }
      // 价格刻度：两格一个读数
      const step = niceStep(((2 * halfP) / 10) * (narrow ? 3 : 2))
      for (let p = Math.ceil((center - halfP) / step) * step; p <= center + halfP; p += step) {
        const x = Xp(p)
        if (x < x0 || x > x1) continue
        g.vline(x, y1, y1 + 5, C.rule3)
        g.text(fmtNum(p, step >= 1 ? 0 : decRef.current), x, y1 + 15, { size: 11, color: C.t2, align: 'center' })
      }
      // 这段范围内挂单才是完整的，范围外只是币安推送的部分档位
      if (cov) {
        const a = Math.max(x0, Xp(cov[0])), b = Math.min(x1, Xp(cov[1]))
        if (b > a) g.hline(y1 + 1, a, b, 'rgba(120,235,235,0.75)', g.px(2))
      }
      // 柱子的动态：涨得快、落得慢，和帧率无关。峰值条悬停一下再慢慢往下掉。
      const up = 1 - Math.exp(-dt * 34), down = 1 - Math.exp(-dt * 2.6)
      const fall = dt * 20
      const lo = Math.max(0, Math.floor((center - halfP) / width) - base - 2), hi = Math.min(N - 1, Math.ceil((center + halfP) / width) - base + 2)
      for (let i = Math.max(0, lo - 40); i <= Math.min(N - 1, hi + 40); i++) {
        const d = tgt[i] - cur[i]
        cur[i] += d * (d > 0 ? up : down)
        peak[i] = cur[i] > peak[i] ? cur[i] : Math.max(cur[i], peak[i] - fall)
      }
      gctx.save()
      gctx.beginPath()
      gctx.rect(g.X(x0), g.Y(y0), g.X(x1) - g.X(x0), g.Y(y1) - g.Y(y0))
      gctx.clip()
      // 按价格划分成列，列跟着价格走，所以现价移动时不会闪。一格（width）画不到 3.2 像素宽时几格并成一列，
      // 这一列的高度是这几格挂单金额的总和（11-28：原来取最大那一格，电脑上平均少算两成、手机上少一半多）
      const perPx = (2 * halfP) / (x1 - x0)
      const bucketPx = width / perPx
      const span = Math.max(1, Math.ceil(3.2 / bucketPx))
      const sumUSD = (a: Float32Array, i: number) => {
        let s = 0
        for (let j = Math.max(0, i); j < Math.min(N, i + span); j++) if (a[j] >= 1) s += cellUSD(a[j]) // 不到一格（< 100 美元）的余量不算，免得空格子攒出底噪
        return s
      }
      // 第 k 格是 (k − ½)·width 到 (k + ½)·width（后端 bucketOf 四舍五入），中心是 k·width
      const colP = (i: number) => (i + base + (span - 1) / 2) * width
      const colRange = (i: number) => span === 1 ? fmtNum((i + base) * width, decRef.current)
        : `${fmtNum((i + base - .5) * width, decRef.current)}–${fmtNum((i + base + span - .5) * width, decRef.current)}`
      const colPx = bucketPx * span
      const barW = Math.max(1, colPx * (colPx >= 4 ? .62 : .72))
      const barGrad = gctx.createLinearGradient(0, g.Y(y1), 0, g.Y(y0))
      barGrad.addColorStop(0, '#011012')
      barGrad.addColorStop(.26, '#04343b')
      barGrad.addColorStop(.5, '#0a8089')
      barGrad.addColorStop(.72, '#17d4d8')
      barGrad.addColorStop(.9, '#a8faf5')
      barGrad.addColorStop(1, '#ffffff')
      const halfW = Math.max(1, (x1 - x0) / 2)
      const xc = Xp(mid)
      const first = Math.floor((lo + base) / span) * span - base
      // 纵轴顶跟着最高的那一根柱子走（按柱子的总额，不是单独一格）
      let top = 0
      for (let i = first; i <= hi; i += span) top = Math.max(top, usdCellF(sumUSD(tgt, i)))
      const want = top + 12
      if (!cTop) cTop = want
      else cTop += (want - cTop) * (1 - Math.exp(-dt * .8))
      const cols: { i: number; x: number; usd: number; v: number }[] = []
      const bars: { x: number; y: number; py: number; v: number; g: number }[] = []
      for (let i = first; i <= hi; i += span) {
        const usd = sumUSD(cur, i)
        const v = usdCellF(usd), pk = usdCellF(sumUSD(peak, i))
        const x = Xp(colP(i))
        cols.push({ i, x, usd, v })
        // 开机：从现价向两边依次升起
        const gx = still ? 1 : Math.max(0, Math.min(1, warm * 1.7 - (Math.abs(x - xc) / halfW) * .7))
        const ge = 1 - Math.pow(1 - gx, 3)
        if (ge <= 0) continue
        bars.push({ x, y: y1 - (y1 - Yc(v)) * ge, py: y1 - (y1 - Yc(pk)) * ge, v, g: ge })
      }
      // 发光：高的柱子外面先铺一层淡的宽光（弱设备不画）
      if (!lite) {
        gctx.globalCompositeOperation = 'lighter'
        gctx.fillStyle = 'rgba(30,200,205,0.10)'
        for (const b of bars) {
          if (y1 - b.y < (y1 - y0) * .42) continue
          gctx.fillRect(g.X(b.x - barW * 1.6), g.Y(b.y), g.px(barW * 3.2), g.Y(y1) - g.Y(b.y))
        }
        gctx.globalCompositeOperation = 'source-over'
      }
      gctx.fillStyle = barGrad
      for (const b of bars) {
        const px0 = g.X(b.x - barW / 2)
        gctx.fillRect(px0, g.Y(b.y), Math.max(1, g.px(barW)), g.Y(y1) - g.Y(b.y))
      }
      // 峰值条：刚撤掉的墙，上面还悬着一条
      for (const b of bars) {
        if (b.py >= y1 - 1) continue
        const gap = b.y - b.py
        gctx.fillStyle = gap > 3 ? 'rgba(255,255,255,0.92)' : 'rgba(255,255,255,0.55)'
        gctx.fillRect(g.X(b.x - barW / 2), g.Y(b.py), Math.max(1, g.px(barW)), Math.max(1, g.px(1)))
      }
      // 成交：大单竖起一根亮线
      for (let i = spikes.length - 1; i >= 0; i--) {
        const s = spikes[i]
        const age = (now - s.t) / 650
        if (age >= 1) { spikes.splice(i, 1); continue }
        const x = Xp(s.p)
        if (x < x0 || x > x1) continue
        // 高度就是这笔成交的金额（和纵轴同一把尺子；11-28 前加了 34 格，约放大 5.5 倍）；太小落在纵轴下面的，留一小截看得见
        const spikeTop = Math.min(Yc(usdCellF(s.usd)), y1 - 14)
        const a = (1 - age) * (1 - age)
        if (!lite) { gctx.fillStyle = `rgba(160,250,250,${a * .22})`; gctx.fillRect(g.X(x - 3), g.Y(spikeTop), g.px(6), g.Y(y1) - g.Y(spikeTop)) }
        gctx.fillStyle = `rgba(255,255,255,${a})`
        gctx.fillRect(g.X(x), g.Y(spikeTop), Math.max(1, g.px(1.6)), g.Y(y1) - g.Y(spikeTop))
      }
      gctx.restore()
      // 现价
      const xm = Xp(mid)
      g.vline(xm, y0, y1, 'rgba(255,255,255,0.75)')
      // 两边最厚的挂单：按画出来的柱子（几格的总和）选，半秒选一次，避免跳来跳去。记的是绝对格号，盘口数组挪了也认得
      if (now - markers.at > 500) {
        let bv = 0, av = 0, bk = -1, ak = -1
        const mb = Math.floor(mid / width + .5) - base // 现价所在的那一格
        for (const c of cols) {
          if (c.i + span - 1 < mb) { if (c.usd > bv) { bv = c.usd; bk = c.i + base } } else if (c.i > mb) { if (c.usd > av) { av = c.usd; ak = c.i + base } }
        }
        markers = { bid: bk, ask: ak, at: now }
        // 给自动检查用：两边最厚那根柱子的价格范围和金额（拿去和交易所的原始盘口对）
        const info = (k: number) => { const c = cols.find((x) => x.i + base === k); return c && { lo: (c.i + base - .5) * width, hi: (c.i + base + span - .5) * width, usd: Math.round(c.usd), span } }
        canvas.dataset.marks = JSON.stringify({ bid: info(bk), ask: info(ak), t: Date.now() })
      }
      const mark = (k: number, label: string, side: 'l' | 'r') => {
        const c = k < 0 || warm < .9 ? undefined : cols.find((x) => x.i + base === k)
        if (!c || c.usd <= 0) return
        const x = c.x, y = Yc(c.v)
        if (x < x0 || x > x1) return
        g.rect(x - 1, y - 6, x + 1, y - 2, '#fff')
        const text = `${label} ${colRange(c.i)}  ${fmtCompactL(c.usd)}`
        const tw = g.measure(text, { size: 11 })
        const tx = side === 'l' ? Math.max(x0 + 2, Math.min(x - 8 - tw, x1 - tw - 2)) : Math.min(x1 - tw - 2, x + 8)
        g.text(text, tx, Math.max(y0 + 8, y - 12), { size: 11, color: C.t1, baseline: 'middle', plate: { color: 'rgba(0,0,0,0.78)', padX: 3, padY: 1 } })
      }
      mark(markers.bid, tr('ab.bid'), 'l')
      mark(markers.ask, tr('ab.ask'), 'r')
      // 光标：吸附到鼠标左右 10 像素里最高的一根，读数就是这根柱子（几格的总和）
      let hov: (typeof cols)[number] | undefined
      if (mouse && mouse.x >= x0 && mouse.x <= x1) for (const c of cols) if (Math.abs(c.x - mouse.x) <= Math.max(10, colPx / 2) && (!hov || c.usd > hov.usd)) hov = c
      if (hov) {
        const p = colP(hov.i)
        const x = hov.x, y = Yc(hov.v)
        for (let yy = y0; yy < y1; yy += 4) g.vline(x, yy, Math.min(yy + 2, y1), 'rgba(255,255,255,0.45)')
        g.rect(x - 2, y - 2, x + 2, y + 2, '#fff')
        const text = `${tr(p < mid ? 'ab.bid' : 'ab.ask')} ${colRange(hov.i)}  ${fmtCompactL(hov.usd)}`
        const tw = g.measure(text, { size: 12 })
        const tx = x + 10 + tw > x1 ? x - 10 - tw : x + 10
        g.text(text, tx, Math.max(y0 + 12, y - 16), { size: 12, color: '#fff', baseline: 'middle', plate: { color: 'rgba(0,0,0,0.88)', padX: 5, padY: 3, border: C.rule2 } })
      }
      if (state === 'lost') g.text(tr('ab.lost'), x1 - 6, y0 + 10, { size: 12, color: C.amber, align: 'right', baseline: 'middle' })
      else if (sync) g.text(tr('ab.sync'), x1 - 6, y0 + 10, { size: 12, color: C.amber, align: 'right', baseline: 'middle' })
    }

    const frame = (now: number) => {
      raf = requestAnimationFrame(frame)
      if (!w || !h || !ctx) return
      // 弱设备：先关余辉和发光，再把刷新降到每秒 15 次
      if (lite && now - lastPaint < 66) return
      const dt = Math.min(.1, (now - last) / 1000)
      last = now
      lastPaint = now
      const t0 = performance.now()
      ctx.setTransform(1, 0, 0, 1, 0, 0)
      if (mid && !born) born = now
      if (lastFrame && now - lastFrame > 3000) state = 'lost'
      else if (lastFrame) state = sync ? 'sync' : 'live'
      paint(makeG(ctx, w, h, W, H, Date.now()), now, dt)
      avg += (performance.now() - t0 - avg) * .05
      if (!lite && avg > 14) { if (++slow > 60) { lite = true; canvas.dataset.lite = '1' } } else slow = 0
      if (clockRef?.current) clockRef.current.textContent = clock(Date.now())
      if (now - told > 200) { told = now; tell() }
    }
    raf = requestAnimationFrame(frame)

    const move = (e: PointerEvent) => { const r = canvas.getBoundingClientRect(); mouse = { x: e.clientX - r.left, y: e.clientY - r.top } }
    const leave = () => { mouse = null }
    canvas.addEventListener('pointermove', move)
    canvas.addEventListener('pointerdown', move)
    canvas.addEventListener('pointerleave', leave)
    return () => {
      closed = true
      cancelAnimationFrame(raf)
      clearTimeout(retry)
      es?.close()
      ro.disconnect()
      canvas.removeEventListener('pointermove', move)
      canvas.removeEventListener('pointerdown', move)
      canvas.removeEventListener('pointerleave', leave)
    }
  }, [inst, clockRef])
  return <canvas ref={ref} className="ab-canvas" data-analyzer />
}
