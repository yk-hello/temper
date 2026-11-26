// 价格示波器：K 线或阶梯线（v3 起不再用斜线连接稀疏成交价）。
// 所有线条、K 线实体、影线都对齐物理像素：1×、1.25×、1.5×、2× 下都是锐利的。
import { evLong, evShort, evVolOk, type EconEvent } from '../lib/calendar'
import { tr, fmtCompactL, type Key } from '../lib/i18n'
import { useEffect, useRef, useState } from 'react'
import { useSurface, clock, type G } from '../lib/gfx'
import { newShapeId, type IndData, type Shape, type ShapePt, type ToolKind } from '../lib/chartPrefs'
import { drawFootprintBar, drawLegend, drawMainInd, drawMeasure, drawPane, drawShape, shapeAt, type Proj, type ShapeHit } from './chartExtras'
import { mergedBar, niceUp125, type FPBar } from '../lib/flow'
import type { Candle } from '../lib/demo'
import { C, cursorTag, dirColors, fmtNum, niceRange, timeScale, valueScale, type Box } from './scale'
import { dashedH, dashedV } from './Spectrum'
import { approachAll } from '../lib/motion'
import { locDec, numLocale, wall } from '../lib/tz'

export interface StepPoint { t: number; v: number }

interface Props {
  mode: 'candle' | 'step'
  candles?: Candle[]
  points?: StepPoint[]
  /** K 线周期（毫秒） */
  interval?: number
  /** 可见时长 */
  span: number
  /** 结束时刻；不传 = 实时（每帧取当前时间） */
  t1?: number
  dec: number
  volume?: boolean
  live?: boolean
  version?: number
  /** 链上叠加层：Hyperliquid 真实强平价分布（从右边缘向左的横条）和大户开仓均价线 */
  onchain?: OnchainOverlay
  /**
   * 交易所式视图（D59）：K 线宽度固定，按住拖动看历史，滚轮缩放，双击回到最新。
   * 给了这个参数时 span 只用于阶梯线。onNeedMore：快看到最早一根时调用，让外面去取更早的历史。
   */
  pan?: { onNeedMore?: (firstT: number) => void }
  /** 指标（V.1）：主图线、副图 */
  ind?: IndData
  /** 画线（V.2）：tool 不为空时下一次点击开始画；onShapes 存下改动；onToolEnd 画完一条 */
  shapes?: Shape[]
  tool?: ToolKind | null
  onShapes?: (s: Shape[]) => void
  onToolEnd?: () => void
  /** 足迹图（V.11）：有录制的 K 线拆成每个价位的主动买卖 */
  footprint?: { bars: Map<number, FPBar>; step: number; version: number }
  /** 自己的交易线：持仓开仓价、强平价、挂单、条件单、止盈止损；可拖动的线拖完调 onDrag 改价 */
  trade?: TradeOverlay
  /** 按住 Alt 点图、或者点价格轴：选这个价下单 */
  onPickPrice?: (price: number) => void
  /** 在图上点右键：在这个价位下单、设止盈止损（18 第 6.5 条）。x、y 是屏幕坐标，菜单放在那里 */
  onPriceMenu?: (price: number, x: number, y: number) => void
  /** 经济事件（可视化 A）：时间轴上插小旗；band 时画过去同类事件后一小时的平均振幅 */
  events?: { list: EconEvent[]; band: boolean }
}

export interface TradeLine {
  id: string
  kind: 'entry' | 'liq' | 'limit' | 'cond' | 'tp' | 'sl' | 'ai'
  price: number
  /** 方向：决定颜色（买 / 多 = 涨色，卖 / 空 = 跌色） */
  buy?: boolean
  label: string
  drag?: boolean
  /** 悬停时显示的一句白话说明 */
  hint?: string
  /**
   * 图上标签写的"结果句"：到哪个价、会发生什么、赚还是亏（D72）。px 是线现在的价（拖动中是新价），
   * up = 这个价在现价上方。每段可以带语气色：up 赚（涨色）、down 亏（跌色）、warn 爆仓（琥珀）
   */
  say?: (px: number, up: boolean) => SayPart[]
}
export interface SayPart { t: string; tone?: 'up' | 'down' | 'warn' }
/** AI 开平仓点（3.21）：t 时刻成交在 price。say 是结果句，hint 是悬停看的理由，tone 是这一笔赚亏（开仓没有） */
export interface TradeMarker { id: string; t: number; price: number; buy: boolean; say: string; hint: string; tone?: 'up' | 'down' }
export interface TradeOverlay {
  lines: TradeLine[]
  markers?: TradeMarker[]
  /** 拖到新价格松手时调用；返回 false = 被拒绝，线会弹回原价 */
  onDrag?: (line: TradeLine, price: number) => Promise<boolean>
  /** 价格最小变动单位：拖动时吸附 */
  tick?: number
  /** 机器人的网格线、补仓线：细线，不抢 K 线（E） */
  grid?: { px: number; tone: 'up' | 'down' | 'dim' }[]
  /** 纵轴要装下这个范围（设网格时看得到上下限） */
  fit?: [number, number]
}

export interface OnchainOverlay {
  coin?: string
  bins: { lo: number; hi: number; long_value: number; short_value: number; long_px?: number; short_px?: number }[]
  whales: Whale[]
  /** 大额持仓的动作：开仓、加仓、减仓、平仓、反手（V.19） */
  actions?: ChainAction[]
  /** 已扫到的持仓占这个币链上总持仓的比例 */
  coverage?: number
}

export interface ChainAction { t: number; kind: 'open' | 'add' | 'reduce' | 'close' | 'flip'; from: number; to: number; value: number; price: number; lag: number }

/** 链上大户的一笔持仓（Hyperliquid 公开数据） */
export interface Whale {
  addr: string
  /** 带符号：正多负空（币的数量） */
  size: number
  entry: number
  /** 名义价值（美元） */
  value: number
  /** 未实现盈亏（美元） */
  upnl: number
  /** 强平价；0 = 没有（保证金很足） */
  liq: number
  /** 这个地址在 Hyperliquid 上的历史累计盈亏 / 近一个月盈亏（美元） */
  pnl: number
  month: number
}

const AXIS = 68
const BOTTOM = 26
const PAD_BARS = 4 // 最新一根右边留几根的空
const BAR_KEY = 'qs.barW'
const FP_BAR_KEY = 'qs.fpBarW'
const barMax = (fp: boolean) => (fp ? 200 : 40)
function savedBarW(fp: boolean) {
  try { const v = Number(localStorage.getItem(fp ? FP_BAR_KEY : BAR_KEY)); return v >= 2 && v <= barMax(fp) ? v : fp ? 72 : 8 } catch { return fp ? 72 : 8 }
}

export function Scope({ mode, candles, points, interval = 60_000, span: spanProp, t1, dec, volume = true, live = false, version = 0, onchain, pan, ind, shapes, tool, onShapes, onToolEnd, trade, footprint: footprintProp , onPickPrice, onPriceMenu, events }: Props) {
  const mouse = useRef<{ x: number; y: number } | null>(null)
  const fpRef = useRef(footprintProp)
  fpRef.current = footprintProp
  // 视图：barW 每根 K 线占的宽度（CSS 像素）；off 从最新往左移了几根（0 = 跟随最新）；vel 松手后的惯性（根/毫秒）
  const view = useRef({ barW: savedBarW(!!footprintProp), off: 0, tOff: 0, vel: 0, lastT: 0, drag: null as null | { x: number; off: number; samples: { t: number; off: number }[] }, ivKey: 0 })
  const latestBtn = useRef<HTMLButtonElement>(null)
  const ocA = useRef(0) // 链上叠加层的透明度（开关时渐变）
  const lastOc = useRef<OnchainOverlay | null>(null)
  const geo = useRef({ x1: 0, tb: 0, iv: interval, ta: 0, sp: 1 })
  // 画线：正在画的一条、选中的、正在拖的。拖动时先画预览，松手才存
  const draft = useRef<{ kind: ToolKind; a: ShapePt; b: ShapePt; fixed: boolean } | null>(null)
  const shapeSel = useRef<string | null>(null)
  const shapeHot = useRef<string | null>(null)
  const shapeDrag = useRef<{ hit: ShapeHit; start: ShapePt; orig: Shape; now: Shape; moved: boolean } | null>(null)
  const measure = useRef<{ a: ShapePt; b: ShapePt } | null>(null)
  const shapesRef = useRef(shapes ?? [])
  shapesRef.current = shapes ?? []
  const toolRef = useRef(tool ?? null)
  toolRef.current = tool ?? null
  const onShapesRef = useRef(onShapes)
  onShapesRef.current = onShapes
  const onToolEndRef = useRef(onToolEnd)
  onToolEndRef.current = onToolEnd
  const projRef = useRef<Proj | null>(null)
  // 自己的交易线：每帧记下位置（命中、拖动用）；拖动中的线、松手后等服务器确认的价、被拒后弹回的动画
  const yMap = useRef({ a: 0, b: 1, y0: 0, y1: 1 })
  const tHits = useRef<{ line: TradeLine; y: number; tx0: number; tx1: number; ty: number; p: number }[]>([])
  const tDrag = useRef<{ line: TradeLine; y0: number; price: number; moved: boolean } | null>(null)
  const tPending = useRef(new Map<string, number>())
  const tBack = useRef(new Map<string, { from: number; to: number; t0: number }>())
  const tHot = useRef('')
  const tradeRef = useRef(trade)
  tradeRef.current = trade
  // 大户持仓线：每帧记下位置用于鼠标命中；hot = 鼠标下的那条（地址）；card = 详情卡（悬停显示，点击固定）
  const hits = useRef<{ addr: string; y: number; ty: number; tx0: number; tx1?: number }[]>([])
  // 图上每个标签的"白话说明"区域：鼠标停在上面时在旁边显示（自己的交易线、价格轴价位、链上强平聚集区）
  const hintHits = useRef<{ x0: number; y0: number; x1: number; y1: number; text: string }[]>([])
  const [hint, setHint] = useState<{ text: string; x: number; y: number; right: boolean; v: 'mid' | 'top' | 'bot' } | null>(null)
  const hintRef = useRef('')
  const hot = useRef('')
  const lastPx = useRef(NaN)
  const down = useRef<{ x: number; y: number } | null>(null)
  const [card, setCard] = useState<{ addr: string; x: number; y: number; pinned: boolean } | null>(null)
  const cardRef = useRef(card)
  cardRef.current = card
  const panning = !!pan && mode === 'candle'
  const needMore = useRef(pan?.onNeedMore)
  needMore.current = pan?.onNeedMore
  // 动画状态：可见时长和价格范围平滑过渡到目标值（换时间范围、新高新低时不跳）
  const an = useRef<{ span: number; a: number; b: number } | null>(null)
  const lastFrame = useRef(0)
  const { ref, redraw } = useSurface((g) => draw(g), [candles, points, mode, spanProp, t1, dec, version, onchain, ind, shapes, tool, footprintProp?.version], { animate: live })

  function rangeOf(ta: number, tb: number): [number, number] {
    let lo = Infinity, hi = -Infinity
    if (mode === 'candle') {
      for (const c of candles ?? []) if (c.t + interval >= ta && c.t <= tb) { lo = Math.min(lo, c.l); hi = Math.max(hi, c.h) }
    } else {
      const ps = points ?? []
      for (let i = 0; i < ps.length; i++) {
        if (ps[i].t > tb) break
        if (ps[i].t >= ta || (i + 1 < ps.length && ps[i + 1].t >= ta)) { lo = Math.min(lo, ps[i].v); hi = Math.max(hi, ps[i].v) }
      }
    }
    const f = tradeRef.current?.fit
    if (f && hi >= lo) { lo = Math.min(lo, f[0]); hi = Math.max(hi, f[1]) }
    return [lo, hi]
  }

  function draw(g: G) {
    const hh: typeof hintHits.current = []
    hintHits.current = hh
    const addHint = (x0: number, y0: number, x1: number, y1: number, text?: string) => { if (text) hh.push({ x0, y0, x1, y1, text }) }
    let tb = t1 ?? g.now
    const now = performance.now()
    const dt = lastFrame.current ? Math.min(64, now - lastFrame.current) : 16
    lastFrame.current = now
    // 数据比所选时长短时（刚启动），可见时长缩到数据长度，不让 K 线挤在最右边；
    // 但 K 线最宽约 14 像素：只有两三根时不会被放大成大色块
    const firstT = mode === 'candle' ? candles?.[0]?.t : points?.[0]?.t
    const minSpan = mode === 'candle' ? Math.max(60_000, (interval * Math.max(1, g.w - AXIS)) / 14) : 60_000
    let span = firstT ? Math.min(spanProp, Math.max(minSpan, (tb - firstT) * 1.08)) : spanProp
    let panMoving = false
    if (panning) {
      // 交易所式视图：右边缘 = 最新一根之后留 (footprintProp ? 0.6 : PAD_BARS) 根空；宽度 = 绘图区宽 ÷ 每根宽度
      const v = view.current
      const n = candles?.length ?? 0
      const lastT = n ? candles![n - 1].t : Math.floor(g.now / interval) * interval
      if (v.ivKey !== interval) { v.ivKey = interval; v.off = v.tOff = 0; v.vel = 0; v.lastT = 0 }
      if (v.lastT && lastT > v.lastT && v.off > 0.5) { const k = Math.round((lastT - v.lastT) / interval); v.off += k; v.tOff += k } // 看历史时不被新 K 线推着走
      v.lastT = lastT
      if (!v.drag && v.vel) {
        v.tOff += v.vel * dt
        v.vel *= Math.exp(-dt / 260)
        if (Math.abs(v.vel) < 0.0004) v.vel = 0
      }
      const maxOff = Math.max(0, n - 10)
      v.tOff = Math.max(0, Math.min(maxOff, v.tOff))
      if (v.drag) v.off = v.tOff
      else if (v.off !== v.tOff) { v.off += (v.tOff - v.off) * (1 - Math.exp(-dt / 70)); if (Math.abs(v.off - v.tOff) < 0.01) v.off = v.tOff }
      panMoving = !!v.vel || v.off !== v.tOff
      const plotW = Math.max(1, g.w - AXIS)
      tb = lastT + interval * (1 + (footprintProp ? 0.6 : PAD_BARS)) - v.off * interval
      span = (plotW / v.barW) * interval
      geo.current = { x1: plotW, tb, iv: interval, ta: tb - span, sp: span }
      if (latestBtn.current) latestBtn.current.classList.toggle('on', v.off > 3)
      if (n && tb - span < candles![0].t + 40 * interval) needMore.current?.(candles![0].t)
    }
    if (!an.current) an.current = { span, a: NaN, b: NaN }
    const cur = an.current
    if (panning) cur.span = span
    // 目标价格范围：按"目标时长"窗口里的数据算
    let [tlo, thi] = rangeOf(tb - span, tb)
    // 链上爆仓打开时：价格轴放宽到包含"足够大"的强平聚集区（占这一边强平总额 10% 以上），但有节制：
    // 放宽后的范围不超过 K 线范围的 2.5 倍（K 线至少占图高的四成），更远的用上下边缘的箭头提示。
    // 2026-11-15 从零走查时发现：原来"至少允许到现价的 4%"，行情平静时 K 线只有 0.15% 的波动，被撑到 4% 后压成一条线
    const ocNow = onchain && candles?.length ? onchain : null
    if (ocNow) lastOc.current = ocNow
    const pxNow = mode === 'candle' ? (candles?.length ? candles[candles.length - 1].c : NaN) : (points?.length ? points[points.length - 1].v : NaN)
    const ocUse = onchain && Number.isFinite(pxNow) ? onchain : null
    const cl = ocUse ? liqClusters(ocUse.bins, pxNow) : []
    if (cl.length && thi >= tlo) {
      const sideTotal = { long: 0, short: 0 }
      for (const bn of ocUse!.bins) { sideTotal.long += bn.long_value; sideTotal.short += bn.short_value }
      const maxSpan = (thi - tlo) * 2.5
      const big = cl.filter((c) => c.value >= sideTotal[c.side] * 0.1).sort((x, y) => Math.abs(x.mid - pxNow) - Math.abs(y.mid - pxNow))
      for (const c of big) {
        const lo = Math.min(tlo, c.lo), hi = Math.max(thi, c.hi)
        if (hi - lo > maxSpan) continue
        tlo = lo; thi = hi
      }
    }
    if (!(thi >= tlo)) { cur.span = span }
    const tgt = thi >= tlo ? (() => { const [a, b] = niceRange(tlo, thi, 0.1); return { span, a, b } })() : { span, a: cur.a, b: cur.b }
    if (!Number.isFinite(cur.a)) { cur.a = tgt.a; cur.b = tgt.b }
    const moving = approachAll(cur, tgt, dt, 11, { span: 1, a: Math.abs(tgt.b - tgt.a) * 2e-4, b: Math.abs(tgt.b - tgt.a) * 2e-4 })
    if ((moving || panMoving) && !live) redraw()
    const sp = cur.span
    const ta = tb - sp
    const P: Box = { x0: 0, y0: 10, x1: g.w - AXIS, y1: g.h - BOTTOM }
    g.rect(0, 0, g.w, g.h, C.void)
    const dc = dirColors()
    const allPanes = mode === 'candle' ? ind?.panes ?? [] : []
    const shownPanes = allPanes.slice(0, g.h < 250 ? 0 : g.h < 340 ? 1 : g.h < 520 ? 2 : 3)
    const paneH = shownPanes.length ? Math.max(46, Math.min(100, Math.round((P.y1 - P.y0) * 0.16))) : 0
    const volBottom = P.y1 - shownPanes.length * paneH
    const showVol = volume && (ind ? ind.vol : true)
    const volH = showVol ? Math.round((volBottom - P.y0) * 0.16) : 0
    const BAND = mode === 'candle' ? 22 : 0 // 顶上的读数带
    const EDGE = onchain ? 22 : 0 // 画面外的价位放在图外的上下两条带子里，不压 K 线
    const PP: Box = { ...P, y0: P.y0 + BAND + EDGE, y1: volBottom - volH - (showVol ? 6 : 0) - EDGE }

    let fpDs = 0 // 足迹图现在显示的档位宽度
    // 可见数据与范围
    let lo = Infinity, hi = -Infinity, last: number | undefined
    const vc = mode === 'candle' ? (candles ?? []).filter((c) => c.t + interval >= ta && c.t <= tb) : []
    const vp: StepPoint[] = []
    if (mode === 'candle') {
      for (const c of vc) { lo = Math.min(lo, c.l); hi = Math.max(hi, c.h) }
      last = vc.length ? vc[vc.length - 1].c : undefined
    } else {
      const ps = points ?? []
      // 阶梯线需要窗口左侧之前的最后一个点
      let start = 0
      while (start < ps.length - 1 && ps[start + 1].t < ta) start++
      for (let i = start; i < ps.length && ps[i].t <= tb; i++) vp.push(ps[i])
      for (const p of vp) { lo = Math.min(lo, p.v); hi = Math.max(hi, p.v) }
      last = vp.length ? vp[vp.length - 1].v : undefined
    }
    { const mm = mouse.current; timeScale(g, P, ta, tb, { avoidX: mm && !view.current.drag && mm.x >= P.x0 && mm.x <= P.x1 && mm.y >= P.y0 && mm.y <= P.y1 ? mm.x : undefined }) }
    if (!(hi >= lo)) {
      g.text(tr('chart.waiting'), (P.x0 + P.x1) / 2, (P.y0 + P.y1) / 2, { size: 13, color: C.t3, align: 'center', baseline: 'middle' })
      return
    }
    const a = cur.a, b = cur.b
    const yOf = (v: number) => PP.y0 + (1 - (v - a) / (b - a)) * (PP.y1 - PP.y0)
    yMap.current = { a, b, y0: PP.y0, y1: PP.y1 }
    const xOf = (t: number) => P.x0 + ((t - ta) / sp) * (P.x1 - P.x0)
    const curY = last !== undefined ? yOf(last) : -99
    valueScale(g, { ...PP, x1: P.x1 }, a, b, (v) => fmtNum(v, dec), { avoid: [curY] })
    if (showVol) g.hline(PP.y1 + 3, P.x0, P.x1, C.rule)

    const ctx = g.ctx
    ctx.save()
    ctx.beginPath()
    ctx.rect(g.X(P.x0), 0, g.X(P.x1) - g.X(P.x0), g.H)
    ctx.clip()

    if (mode === 'candle') {
      const slot = ((P.x1 - P.x0) / sp) * interval * g.kx
      let bw = Math.max(1, Math.floor(Math.min(slot * 0.68, 18 * g.kx))) // 超宽屏上也不超过 18 像素
      if (bw % 2 === 0) bw -= 1
      if (bw < 1) bw = 1
      let vmax = 0
      for (const c of vc) vmax = Math.max(vmax, c.vb + c.vs)
      // 足迹图：每根够宽时拆开；档位按屏幕放得下算，大约 13 像素一档
      const fp = footprintProp
      const fpOn = !!fp && fp.step > 0 && slot / g.kx >= 16
      let fpMax = 0
      if (fpOn) {
        fpDs = Math.max(fp!.step, niceUp125((b - a) / Math.max(4, (PP.y1 - PP.y0) / 13)))
        for (const c of vc) { const fb = fp!.bars.get(c.t); if (fb) fpMax = Math.max(fpMax, mergedBar(fb, fpDs).maxSide) }
      }
      for (const c of vc) {
        const fb = fpOn ? fp!.bars.get(c.t) : undefined
        if (fb) {
          ctx.save()
          ctx.beginPath()
          ctx.rect(g.X(P.x0), g.Y(PP.y0), g.X(P.x1) - g.X(P.x0), g.Y(PP.y1) - g.Y(PP.y0))
          ctx.clip()
          drawFootprintBar(g, c, mergedBar(fb, fpDs), fpDs, xOf(c.t) + 1, xOf(c.t + interval) - 1, yOf, fpMax, dc.up, dc.down)
          ctx.restore()
          continue
        }
        const cx = Math.round(g.X(xOf(c.t + interval / 2)))
        const up = c.c >= c.o
        const col = up ? dc.up : dc.down
        const yh = Math.round(g.Y(yOf(c.h))), yl = Math.round(g.Y(yOf(c.l)))
        const yo = Math.round(g.Y(yOf(c.o))), yc = Math.round(g.Y(yOf(c.c)))
        ctx.fillStyle = col
        ctx.fillRect(cx, yh, 1, Math.max(1, yl - yh))
        const top = Math.min(yo, yc), hgt = Math.max(1, Math.abs(yc - yo))
        ctx.fillRect(cx - (bw - 1) / 2, top, bw, hgt)
        if (showVol && vmax > 0) {
          const vh = Math.max(1, Math.round(((c.vb + c.vs) / vmax) * volH * g.ky))
          ctx.fillStyle = up ? 'rgba(255,255,255,0.32)' : 'rgba(255,255,255,0.16)'
          ctx.fillRect(cx - (bw - 1) / 2, g.Y(volBottom) - vh, bw, vh)
        }
      }
    } else if (vp.length) {
      const lw = Math.max(1, Math.round(g.kx))
      const off = lw % 2 ? 0.5 : 0
      ctx.beginPath()
      let py = 0
      vp.forEach((p, i) => {
        const x = Math.round(g.X(Math.max(P.x0, xOf(p.t)))) + off
        const y = Math.round(g.Y(yOf(p.v))) + off
        if (i === 0) ctx.moveTo(x, y)
        else { ctx.lineTo(x, py); ctx.lineTo(x, y) }
        py = y
      })
      ctx.lineTo(Math.round(g.X(xOf(tb))) + off, py)
      ctx.strokeStyle = C.t1
      ctx.lineWidth = lw
      ctx.lineJoin = 'miter'
      ctx.stroke()
    }
    ctx.restore()

    // 指标和画线
    const proj: Proj = { xOf, yOf, box: PP, dec }
    projRef.current = proj
    if (panning) geo.current = { ...geo.current, ta, sp }
    const barsAll = candles ?? []
    if (mode === 'candle' && ind) {
      if (ind.main.length || ind.band) drawMainInd(g, ind, barsAll, interval, ta, tb, xOf, yOf, PP)
      let idx = barsAll.length - 1
      const mp = mouse.current
      if (mp && mp.x >= P.x0 && mp.x <= P.x1) {
        const tm = ta + ((mp.x - P.x0) / (P.x1 - P.x0)) * sp
        const k = barsAll.findIndex((c) => tm >= c.t && tm < c.t + interval)
        if (k >= 0) idx = k
      }
      shownPanes.forEach((pn, i) => drawPane(g, pn, { x0: P.x0, x1: P.x1, y0: volBottom + i * paneH, y1: volBottom + (i + 1) * paneH }, barsAll, interval, ta, tb, xOf, idx, dec))
    }
    if (panning) {
      const live0 = shapeDrag.current?.now
      for (const sh of shapesRef.current) drawShape(g, live0 && live0.id === sh.id ? live0 : sh, proj, shapeSel.current === sh.id, shapeHot.current === sh.id)
      const dr = draft.current
      if (dr && dr.kind !== 'measure') drawShape(g, { id: 'draft', kind: dr.kind, a: dr.a, b: dr.b }, proj, false, true)
      const ms = dr?.kind === 'measure' ? dr : measure.current
      if (ms) drawMeasure(g, ms.a, ms.b, interval, proj)
    }

    // 链上叠加层：强平价分布（价格跌到这里会强平的多单 = 下跌色；涨到这里会强平的空单 = 上涨色）
    // 开关时渐入渐出；最大的几个聚集区直接标金额；在屏幕外的在上下边缘提示
    ocA.current += ((ocNow ? 1 : 0) - ocA.current) * (1 - Math.exp(-dt / 140))
    if (Math.abs(ocA.current - (ocNow ? 1 : 0)) < 0.01) ocA.current = ocNow ? 1 : 0
    const oc = ocNow ?? (ocA.current > 0 ? lastOc.current : null)
    if (oc && ocA.current > 0) {
      const fade = ocA.current
      const dc2 = dirColors()
      // 按屏幕分辨率合并：每段约 6 像素高；再轻微平滑，读起来是"一片强平区"而不是一根细线。
      // 宽度和亮度都表示金额（越大越宽越亮），零散的小额几乎看不见
      const segPx = 6
      const nSeg = Math.max(8, Math.floor((PP.y1 - PP.y0) / segPx))
      const step = (b - a) / nSeg
      const L = new Float64Array(nSeg), Sh = new Float64Array(nSeg)
      // 每个统计格子按和屏幕段的重叠比例分摊（放大看时一个格子横跨好几段；只按中点放会画成一条条横纹）
      for (const bn of oc.bins) {
        const lo = Math.max(bn.lo, a), hi = Math.min(bn.hi, b), bw = bn.hi - bn.lo
        if (!(hi > lo) || !(bw > 0)) continue
        const k0 = Math.max(0, Math.floor((lo - a) / step)), k1 = Math.min(nSeg - 1, Math.floor((hi - a) / step))
        for (let k = k0; k <= k1; k++) {
          const f = (Math.min(hi, a + (k + 1) * step) - Math.max(lo, a + k * step)) / bw
          if (f <= 0) continue
          L[k] += bn.long_value * f
          Sh[k] += bn.short_value * f
        }
      }
      const smooth = (v: Float64Array) => {
        const o = new Float64Array(v.length)
        for (let i = 0; i < v.length; i++) o[i] = v[i] + 0.35 * ((v[i - 1] ?? 0) + (v[i + 1] ?? 0)) + 0.1 * ((v[i - 2] ?? 0) + (v[i + 2] ?? 0))
        return o
      }
      const Ls = smooth(L), Ss = smooth(Sh)
      let vmax = 0
      for (let i = 0; i < nSeg; i++) vmax = Math.max(vmax, Ls[i], Ss[i])
      const maxW = (P.x1 - P.x0) * 0.26
      ctx.save()
      ctx.beginPath()
      ctx.rect(g.X(P.x0), g.Y(PP.y0), g.X(P.x1) - g.X(P.x0), g.Y(PP.y1) - g.Y(PP.y0))
      ctx.clip()
      const xr = g.X(P.x1)
      for (let i = 0; i < nSeg && vmax > 0; i++) {
        const y0 = Math.round(g.Y(yOf(a + (i + 1) * step))), y1 = Math.round(g.Y(yOf(a + i * step)))
        const hgt = Math.max(1, y1 - y0 - 1)
        for (const [v, col] of [[Ls[i], dc2.down], [Ss[i], dc2.up]] as const) {
          const r = v / vmax
          if (r < 0.015) continue
          const wpx = Math.round(Math.pow(r, 0.8) * maxW * g.kx * fade)
          ctx.globalAlpha = (0.12 + 0.68 * Math.pow(r, 0.7)) * fade
          ctx.fillStyle = col
          ctx.fillRect(xr - wpx, y0, wpx, hgt)
        }
      }
      ctx.globalAlpha = 1
      ctx.globalAlpha = fade
      // 大户开仓价：细虚线 + 右侧贴着价格轴的小标签（方向 数量）；细节在鼠标悬停 / 点击后的卡片里。
      // 标签互相避让：太近时往下错开，线仍在真实价位
      let lastTagY = -Infinity
      const hl: typeof hits.current = []
      const sel = hot.current || (cardRef.current?.addr ?? '')
      // 画面外的东西（强平聚集区、大户开仓价）不藏：在上下边缘列成一小列，像价格轴的延伸。
      // 最远的贴着边缘，越近越往里，和真实价格的上下关系一致。日线和 1 分钟线看到的是同一批，只是有的画成线、有的在边缘
      const tot = { long: 0, short: 0 }
      for (const bn of oc.bins) { tot.long += bn.long_value; tot.short += bn.short_value }
      // 聚集区门槛：占这一边强平总额 5% 以上（画面里外同一个门槛）
      const shown = (c: (typeof cl)[number]) => c.value >= tot[c.side] * 0.05
      type EdgeItem = { px: number; liq?: (typeof cl)[number]; whale?: Whale; h: number; ty: number }
      const whaleTag = (w: Whale, arrow: string) => `${arrow ? arrow + ' ' : ''}${tr('chart.whaleTag', { side: tr(w.size > 0 ? 'chart.longS' : 'chart.shortS'), size: Math.abs(w.size).toLocaleString(numLocale(), { maximumFractionDigits: 0 }), coin: oc.coin ?? '' })}${arrow ? tr('chart.whaleAt', { px: fmtNum(w.entry, lblDec(w.entry, dec)) }) : ''}`
      const whaleTf = { size: 11, weight: 550 }
      // 宽屏：画面里大户的标签独占价格轴旁边那一列（永远贴着自己的线），画面外的清单在它左边另起一列，两者不抢位置。
      // 窄屏放不下两列：清单贴着价格轴，画面里的标签让开它
      const inView = oc.whales.filter((w) => w.entry >= a && w.entry <= b)
      const tagColW = inView.length ? Math.max(...inView.map((w) => g.measure(whaleTag(w, ''), whaleTf) + 12)) + 6 : 0
      const wide = P.x1 - P.x0 - tagColW >= 520
      const listR = wide ? P.x1 - 4 - tagColW : P.x1 - 4 // 清单的右边缘
      // 画面外清单横着排、贴着上下边缘（价格轴本来就在上下各留了一截空），尽量不挡最新的 K 线：
      // 离现价最近的放在最右边（挨着价格轴），往左排，一行放不下再往里换一行，最多两行
      const liqTf = { size: 11, weight: 500 }
      const liqText = (c: (typeof cl)[number], arrow: string) => {
        const pct = ((c.mid / (last ?? pxNow) - 1) * 100)
        return `${arrow} ${tr(c.side === 'long' ? 'chart.liqLong' : 'chart.liqShort', { v: fmtCompactL(c.value), px: fmtNum(c.mid, lblDec(c.mid, dec)), pct: (pct > 0 ? '+' : '−') + locDec(Math.abs(pct).toFixed(1)) + '%' })}`
      }
      const liqHint = (c: (typeof cl)[number]) => {
        const pct = locDec(Math.abs((c.mid / (last ?? pxNow) - 1) * 100).toFixed(1)) + '%'
        return tr(c.side === 'long' ? 'chart.h.liqLong' : 'chart.h.liqShort', { v: fmtCompactL(c.value), px: fmtNum(c.mid, lblDec(c.mid, dec)), pct })
      }
      const GAP = 6
      // 左边是自己的交易线标签那一列（开仓、挂单、止盈止损），清单不排到那里去
      const tradeColW = trade?.lines.length ? Math.max(...trade.lines.map((l) => g.measure('↑ ' + (l.say ? l.say(l.price, l.price > (last ?? 0)).map((q) => q.t).join('') : l.label), { size: 11, weight: 550 }) + 24)) + 8 : 0
      const minX = P.x0 + 6 + tradeColW
      const edgeOf = (up: boolean) => {
        const arrow = up ? '↑' : '↓'
        const items: EdgeItem[] = [
          ...cl.filter((c) => shown(c) && (up ? c.mid > b : c.mid < a)).map((c) => ({ px: c.mid, liq: c, h: g.measure(liqText(c, arrow), liqTf) + 14, ty: 0 })),
          ...oc.whales.filter((w) => (up ? w.entry > b : w.entry < a)).map((w) => ({ px: w.entry, whale: w, h: g.measure(whaleTag(w, arrow), whaleTf) + 12, ty: 0 })),
        ]
        items.sort((x, y) => (up ? x.px - y.px : y.px - x.px)) // 离现价近的在前（排在最右边）
        // 这里的 h 借来存宽度；x1 是右边缘
        const place = (extra: number) => {
          const pos: { x1: number; row: number }[] = []
          let x = listR, row = 0
          for (const it of items) {
            if (x - it.h < minX && x !== listR) { row++; x = listR }
            pos.push({ x1: x, row })
            x -= it.h + GAP
          }
          if (extra && x - extra < minX && x !== listR) row++
          return { pos, rows: items.length ? row + 1 : 0, moreX: x, moreRow: row }
        }
        let hidden = 0
        let lay = place(0)
        while (lay.rows > 1) { // 带子只有一行：先去掉金额小的持仓，再去掉最远的价位
          const ws = items.filter((it) => it.whale).sort((x, y) => x.whale!.value - y.whale!.value)
          if (!ws.length && items.length <= 1) break
          if (ws.length) items.splice(items.indexOf(ws[0]), 1)
          else items.pop()
          hidden++
          lay = place(90)
        }
        const moreRow = hidden ? (lay.moreX - 90 < minX ? lay.moreRow + 1 : lay.moreRow) : 0
        const moreX = hidden ? (moreRow > lay.moreRow ? listR : lay.moreX) : 0
        // 排的时候近的在第一行；画的时候倒过来：远的那一行贴着边缘，近的那一行在里面（和价格的上下关系一致）
        const yOfRow = (_r: number) => (up ? PP.y0 - EDGE / 2 : PP.y1 + EDGE / 2)
        const xs = items.map((it, i) => { it.ty = yOfRow(lay.pos[i].row); return lay.pos[i].x1 })
        const edge = up ? PP.y0 : PP.y1
        return { items, xs, edge, hidden, moreX, moreY: yOfRow(moreRow), arrow }
      }
      const eTop = edgeOf(true), eBot = edgeOf(false)
      const tyMin = !wide && eTop.items.length ? eTop.edge + 9 : PP.y0 + 9
      const tyMax = !wide && eBot.items.length ? eBot.edge - 9 : PP.y1 - 9
      for (const w of [...oc.whales].filter((w) => w.entry >= a && w.entry <= b).sort((x, y) => y.entry - x.entry)) {
        const y = yOf(w.entry)
        const col = w.size > 0 ? dc2.up : dc2.down
        const on = w.addr === sel
        ctx.globalAlpha = fade * (on ? 1 : 0.7)
        if (on) g.hline(y, P.x0, P.x1, col)
        else for (let x = P.x0; x < P.x1; x += 6) g.hline(y, x, Math.min(x + 3, P.x1), col)
        const ty = Math.max(Math.min(Math.max(y, tyMin), tyMax), lastTagY + 17)
        if (ty > tyMax) continue
        lastTagY = ty
        const tag = tr('chart.whaleTag', { side: tr(w.size > 0 ? 'chart.longS' : 'chart.shortS'), size: Math.abs(w.size).toLocaleString(numLocale(), { maximumFractionDigits: 0 }), coin: oc.coin ?? '' })
        const tf = { size: 11, weight: 550 }
        const tw = g.measure(tag, tf)
        const tx1 = P.x1 - 4, tx0 = tx1 - tw - 12
        ctx.globalAlpha = fade
        if (Math.abs(ty - y) > 3) g.vline(P.x1 - 2, Math.min(y, ty), Math.max(y, ty), col) // 标签被挤开时，在标签和价格轴之间用一根短竖线连回自己的线
        g.rect(tx0, ty - 8, tx1, ty + 8, on ? col : '#111113')
        if (!on) { g.rect(tx0, ty - 8, tx0 + 2, ty + 8, col) }
        g.text(tag, tx1 - 6, ty, { ...tf, color: on ? '#000' : C.t1, align: 'right', baseline: 'middle' })
        hl.push({ addr: w.addr, y, ty, tx0 })
      }
      ctx.globalAlpha = fade
      // 聚集区金额标签（在横条左端）
      const px0 = last ?? pxNow
      const placed: number[] = []
      for (const c of cl.filter((c) => c.mid >= a && c.mid <= b && shown(c)).sort((x, y) => y.value - x.value)) {
        // 让开上下边缘的画面外清单
        const y = Math.min(Math.max(yOf(c.mid), eTop.edge + 11), eBot.edge - 11)
        if (placed.some((p) => Math.abs(p - y) < 20)) continue
        placed.push(y)
        const pct = ((c.mid / px0 - 1) * 100)
        const txt = tr(c.side === 'long' ? 'chart.liqLong' : 'chart.liqShort', { v: fmtCompactL(c.value), px: fmtNum(c.mid, lblDec(c.mid, dec)), pct: (pct > 0 ? '+' : '−') + locDec(Math.abs(pct).toFixed(1)) + '%' })
        g.text(txt, P.x1 - maxW - 10, y, { size: 11, weight: 500, color: c.side === 'long' ? dc2.down : dc2.up, align: 'right', baseline: 'middle', plate: { color: 'rgba(0,0,0,0.78)', padX: 6, padY: 3 } })
        addHint(P.x1 - maxW - 16 - g.measure(txt, { size: 11, weight: 500 }), y - 10, P.x1 - maxW - 4, y + 10, liqHint(c))
      }
      ctx.restore()
      // 画面外的清单（见上面 edgeOf）：聚集区是带边框的小牌子，大户是和画面里一样的标签，前面加箭头和开仓价。
      // 两种都右对齐在价格轴旁边；大户标签可以悬停 / 点击看详情卡
      for (const e of [eTop, eBot]) {
        e.items.forEach((it, i) => {
          const x1 = e.xs[i]
          if (it.liq) {
            const c = it.liq
            addHint(x1 - it.h, it.ty - 10, x1, it.ty + 10, liqHint(c))
            g.text(liqText(c, e.arrow), x1 - 7, it.ty, { ...liqTf, color: c.side === 'long' ? dc2.down : dc2.up, align: 'right', baseline: 'middle', plate: { color: 'rgba(17,17,19,0.92)', padX: 7, padY: 3, border: C.rule } })
          } else if (it.whale) {
            const w = it.whale
            const col = w.size > 0 ? dc2.up : dc2.down
            const on = w.addr === sel
            const tag = whaleTag(w, e.arrow)
            const tx1 = x1, tx0 = tx1 - it.h
            g.rect(tx0, it.ty - 8, tx1, it.ty + 8, on ? col : '#111113')
            if (!on) g.rect(tx0, it.ty - 8, tx0 + 2, it.ty + 8, col)
            g.text(tag, tx1 - 6, it.ty, { ...whaleTf, color: on ? '#000' : C.t1, align: 'right', baseline: 'middle' })
            hl.push({ addr: w.addr, y: NaN, ty: it.ty, tx0, tx1 }) // 没有线：只有悬停在标签上才算
          }
        })
        if (e.hidden) {
          const mt = `${e.arrow} ${tr('chart.moreWhales', { n: e.hidden })}`
          addHint(e.moreX - 18 - g.measure(mt, { size: 11, weight: 500 }), e.moreY - 9, e.moreX, e.moreY + 9, tr('chart.h.more', { n: e.hidden, where: tr(e === eTop ? 'chart.h.above' : 'chart.h.below') }))
        }
        if (e.hidden) g.text(`${e.arrow} ${tr('chart.moreWhales', { n: e.hidden })}`, e.moreX - 6, e.moreY, { size: 11, weight: 500, color: C.t3, align: 'right', baseline: 'middle', plate: { color: 'rgba(17,17,19,0.85)', padX: 6, padY: 2 } })
      }
      hits.current = hl
      // 大额持仓的动作：K 线上下的小三角，买方向在下面、卖方向在上面，指上去看是什么
      if (mode === 'candle' && oc.actions?.length) {
        for (const ac of oc.actions.slice(0, 60)) {
          if (ac.t < ta || ac.t > tb) continue
          const bar = vc.find((c) => ac.t >= c.t && ac.t < c.t + interval)
          if (!bar) continue
          const buy = ac.to > ac.from // 仓位往多的方向变
          const x = xOf(bar.t + interval / 2)
          const y = yOf(buy ? bar.l : bar.h) + (buy ? 7 : -7)
          if (y < PP.y0 || y > PP.y1) continue
          const s = g.px(3), X = g.X(x), Y = g.Y(y)
          ctx.fillStyle = buy ? dc2.up : dc2.down
          ctx.globalAlpha = fade
          ctx.beginPath()
          ctx.moveTo(X, buy ? Y - s : Y + s)
          ctx.lineTo(X - s, buy ? Y + s : Y - s)
          ctx.lineTo(X + s, buy ? Y + s : Y - s)
          ctx.closePath()
          ctx.fill()
          ctx.globalAlpha = 1
          const long = ac.to > 0 || (ac.kind === 'close' && ac.from > 0)
          addHint(x - 6, y - 6, x + 6, y + 6, tr('cn.actHint', { kind: tr(`cn.kind.${ac.kind}` as Key), side: tr(long ? 'heat.long' : 'heat.short'), v: fmtCompactL(ac.value), px: fmtNum(ac.price, lblDec(ac.price, dec)), n: Math.round(ac.lag / 1000) }))
        }
      }
      ctx.globalAlpha = 1
    }

    if (!oc || ocA.current === 0) hits.current = []
    lastPx.current = last ?? NaN

    // 机器人的线：在 K 线后面，细
    if (trade?.grid?.length) {
      const colOf = (x: 'up' | 'down' | 'dim') => (x === 'up' ? 'rgba(76,185,138,0.5)' : x === 'down' ? 'rgba(226,98,92,0.5)' : 'rgba(255,255,255,0.28)')
      for (const l of trade.grid) if (l.px > a && l.px < b) g.hline(yOf(l.px), P.x0, P.x1, colOf(l.tone))
    }
    // 自己的交易线（D68：拖动跟手、吸附到价格单位、被拒绝时弹回原价）
    const th: typeof tHits.current = []
    let offDnN = 0 // 左下角已经占了几行
    if (trade?.lines.length) {
      const now = performance.now()
      // 颜色表示结果，不表示买卖方向：止盈是"赚"（涨色）、止损是"亏"（跌色）、强平琥珀；持仓、挂单、条件单统一中性色
      const col = (l: TradeLine) => l.kind === 'liq' ? C.amber : l.kind === 'tp' ? dc.up : l.kind === 'sl' ? dc.down : C.t2
      const toneCol = (t?: SayPart['tone']) => t === 'up' ? dc.up : t === 'down' ? dc.down : t === 'warn' ? C.amber : C.t1
      const pxNowT = last ?? NaN
      const partsOf = (l: TradeLine, p: number): SayPart[] => l.say ? l.say(p, Number.isFinite(pxNowT) ? p > pxNowT : false) : [{ t: l.label }]
      const tfo = { size: 11, weight: 550 }
      const partsW = (ps: SayPart[]) => ps.reduce((w, x) => w + g.measure(x.t, tfo), 0)
      const drawParts = (ps: SayPart[], x: number, y: number, ink?: string) => { for (const q of ps) { g.text(q.t, x, y, { ...tfo, color: ink ?? toneCol(q.tone), baseline: 'middle' }); x += g.measure(q.t, tfo) } }
      const priceOf = (l: TradeLine) => {
        const d = tDrag.current
        if (d && d.line.id === l.id) return d.price
        const bk = tBack.current.get(l.id)
        if (bk) {
          const k = Math.min(1, (now - bk.t0) / 280)
          if (k >= 1) tBack.current.delete(l.id)
          else { redraw(); return bk.from + (bk.to - bk.from) * (1 - Math.pow(1 - k, 3) * Math.cos(k * 4) ) }
        }
        const pd = tPending.current.get(l.id)
        if (pd !== undefined) {
          if (Math.abs(pd - l.price) < (trade.tick ?? 1e-9) / 2) tPending.current.delete(l.id)
          else return pd
        }
        return l.price
      }
      const all = trade.lines.map((l) => ({ l, p: priceOf(l) }))
      const items = all.filter((x) => x.p >= a && x.p <= b).sort((x, y) => y.p - x.p)
      // 画面外的（比如 1 分钟线上离得远的止盈止损、条件单）：在左列上下边缘列出来，最远的贴边
      const offUp = all.filter((x) => x.p > b).sort((x, y) => y.p - x.p)
      const offDn = all.filter((x) => x.p < a).sort((x, y) => x.p - y.p)
      const offTag = (x: { l: TradeLine; p: number }, ty: number, arrow: string) => {
        const c = col(x.l)
        const ps = [{ t: arrow + ' ' }, ...partsOf(x.l, x.p)]
        const tx0 = P.x0 + 8, tx1 = tx0 + partsW(ps) + 12
        addHint(tx0, ty - 8, tx1, ty + 8, x.l.hint && x.l.hint + (x.l.drag ? tr(arrow === '↑' ? 'tl.h.up' : 'tl.h.down') : ''))
        g.rect(tx0, ty - 8, tx1, ty + 8, '#111113')
        g.rect(tx0, ty - 8, tx0 + 2, ty + 8, c)
        drawParts(ps, tx0 + 7, ty)
        return tx1 - tx0
      }
      offUp.forEach((x, i) => { offTag(x, PP.y0 + 9 + i * 18, '↑') })
      offDn.forEach((x, i) => offTag(x, PP.y1 - 9 - i * 18, '↓'))
      offDnN = offDn.length
      const tagMin = PP.y0 + 9 + offUp.length * 18, tagMax = PP.y1 - 9 - offDn.length * 18
      // 标签位置：先从上往下排（间隔 18），超出下界的再从下往上推回去，不压住下边缘的"画面外"标签
      const tys = items.map((x) => Math.min(Math.max(yOf(x.p), tagMin), tagMax))
      for (let i = 1; i < tys.length; i++) tys[i] = Math.max(tys[i], tys[i - 1] + 18)
      for (let i = tys.length - 1; i >= 0; i--) tys[i] = Math.min(tys[i], i === tys.length - 1 ? tagMax : tys[i + 1] - 18)
      for (const [idx, { l, p }] of items.entries()) {
        const y = yOf(p)
        const c = col(l)
        const on = tHot.current === l.id || tDrag.current?.line.id === l.id
        const pending = tPending.current.has(l.id)
        ctx.globalAlpha = pending ? 0.55 : on ? 1 : 0.85
        if (l.kind === 'entry') g.hline(y, P.x0, P.x1, c, on ? 1.5 : 1)
        else {
          const [dash, gap] = l.kind === 'cond' ? [2, 3] : l.kind === 'liq' ? [6, 3] : [5, 4]
          for (let x = P.x0; x < P.x1; x += dash + gap) g.hline(y, x, Math.min(x + dash, P.x1), c, on ? 1.5 : 1)
        }
        // 左侧标签：深色底、细边框、字是线的颜色；拖动中显示新价格
        const ty = tys[idx]
        const ps = partsOf(l, p)
        const tw = partsW(ps) + (l.drag ? 12 : 0)
        const tx0 = P.x0 + 8, tx1 = tx0 + tw + 12
        if (Math.abs(ty - y) > 3) g.vline(tx0 + 1, Math.min(y, ty), Math.max(y, ty), c)
        // 深底 + 线色细框；悬停 / 拖动时框变亮。字：中性白，"赚 / 亏 / 爆仓"带语气色
        g.rect(tx0, ty - 9, tx1, ty + 9, '#111113')
        const bc = on ? C.t1 : c
        g.rect(tx0, ty - 9, tx1, ty - 8, bc); g.rect(tx0, ty + 8, tx1, ty + 9, bc); g.rect(tx0, ty - 9, tx0 + 1, ty + 9, bc); g.rect(tx1 - 1, ty - 9, tx1, ty + 9, bc)
        if (l.drag) for (const dy of [-3, 0, 3]) g.rect(tx0 + 5, ty + dy - 0.5, tx0 + 10, ty + dy + 0.5, c) // 拖动把手
        drawParts(ps, tx0 + 6 + (l.drag ? 9 : 0), ty)
        ctx.globalAlpha = 1
        th.push({ line: l, y, tx0, tx1, ty, p })
        addHint(tx0, ty - 9, tx1, ty + 9, l.hint && l.hint + (l.drag && tradeRef.current?.onDrag ? tr('tl.h.drag') : ''))
      }
      ctx.globalAlpha = 1
    }
    tHits.current = th
    // AI 开平仓点：三角在成交的那根 K 线上，颜色表示这一笔赚亏（D72），开仓是中性色。最近的 3 笔写结果句，
    // 其余的只画三角，悬停看；价格在画面外的贴上下边缘，时间在画面左边的写"更早还有 N 笔"（D71）
    if (trade?.markers?.length) {
      const xAt = (t: number) => P.x0 + (((mode === 'candle' ? Math.floor(t / interval) * interval + interval / 2 : t) - ta) / sp) * (P.x1 - P.x0)
      const toneC = (m: TradeMarker) => (m.tone === 'up' ? dc.up : m.tone === 'down' ? dc.down : C.t1)
      const fo = { size: 11, weight: 550 }
      const vis: { m: TradeMarker; x: number; y: number }[] = []
      const offU: TradeMarker[] = [], offD: TradeMarker[] = []
      let older = 0
      for (const m of trade.markers) {
        const x = xAt(m.t)
        if (x < P.x0) { older++; continue }
        if (x > P.x1) continue
        if (m.price > b) offU.push(m)
        else if (m.price < a) offD.push(m)
        else vis.push({ m, x, y: yOf(m.price) })
      }
      // 给自动检查用：画面里的 AI 点
      const tm = JSON.stringify(vis.map((v) => ({ id: v.m.id, x: Math.round(v.x), y: Math.round(v.y), buy: v.m.buy, tone: v.m.tone ?? '' })))
      if (ref.current && ref.current.dataset.tmarks !== tm) ref.current.dataset.tmarks = tm
      for (const { m, x, y } of vis) {
        const dir = m.buy ? 1 : -1 // 买在点的下方朝上指，卖在上方朝下指
        const py = y + dir * 3
        ctx.beginPath()
        ctx.moveTo(g.X(x), g.Y(py))
        ctx.lineTo(g.X(x - 4.5), g.Y(py + dir * 7))
        ctx.lineTo(g.X(x + 4.5), g.Y(py + dir * 7))
        ctx.closePath()
        ctx.fillStyle = toneC(m)
        ctx.strokeStyle = '#000'
        ctx.lineWidth = g.px(1)
        ctx.stroke()
        ctx.fill()
        addHint(x - 7, Math.min(py, py + dir * 7) - 2, x + 7, Math.max(py, py + dir * 7) + 2, m.hint)
      }
      const tag = (m: TradeMarker, x: number, ty: number, prefix = '') => {
        const text = prefix + m.say
        const w = g.measure(text, fo) + 14
        const right = x - 10 - w >= P.x0
        const tx0 = right ? x - 10 - w : Math.min(x + 10, P.x1 - w), tx1 = tx0 + w
        g.rect(tx0, ty - 9, tx1, ty + 9, '#111113')
        g.rect(right ? tx1 - 2 : tx0, ty - 9, right ? tx1 : tx0 + 2, ty + 9, toneC(m))
        g.text(text, tx0 + 7, ty, { ...fo, color: C.t1, baseline: 'middle' })
        addHint(tx0, ty - 9, tx1, ty + 9, m.hint)
      }
      const used: number[] = []
      for (const { m, x, y } of vis.slice(-3).reverse()) {
        let ty = y + (m.buy ? 22 : -22)
        ty = Math.max(PP.y0 + 28, Math.min(PP.y1 - 10, ty))
        for (let i = 0; i < 4 && used.some((u) => Math.abs(u - ty) < 20); i++) ty += m.buy ? 20 : -20
        used.push(ty)
        tag(m, x, ty)
      }
      offU.slice(-2).reverse().forEach((m, i) => tag(m, P.x1 - 2, PP.y0 + 9 + i * 20, '↑ '))
      offD.slice(-2).reverse().forEach((m, i) => tag(m, P.x1 - 2, PP.y1 - 9 - i * 20, '↓ '))
      if (older > 0) {
        const text = tr('tl.older', { n: older })
        const ty = PP.y1 - 9 - offDnN * 18
        const w = g.measure(text, fo) + 12
        g.rect(P.x0 + 8, ty - 8, P.x0 + 8 + w, ty + 8, '#111113')
        g.text(text, P.x0 + 14, ty, { ...fo, color: C.t2, baseline: 'middle' })
        addHint(P.x0 + 8, ty - 8, P.x0 + 8 + w, ty + 8, tr('tl.olderHint', { n: older }))
      }
    }
    // 给自动检查用：交易线在画布上的位置（只在变化时写）
    const tl = JSON.stringify(th.map((h) => ({ id: h.line.id, k: h.line.kind, y: Math.round(h.y), ty: Math.round(h.ty), x: Math.round(h.tx0 + 14), p: h.p }))) // 测试用：线现在画在哪个价
    const cv = ref.current
    if (cv && cv.dataset.tlines !== tl) cv.dataset.tlines = tl
    // 价格轴上只留行情现价：自己的价位已经写在线上的句子里，再放到轴上反而让人以为是行情价（D72）
    // 当前价：一条细虚线 + 刻度尺上的白色游标
    if (last !== undefined) {
      for (let x = P.x0; x < P.x1; x += 5) g.hline(curY, x, Math.min(x + 2, P.x1), 'rgba(255,255,255,0.32)')
      g.hline(curY, P.x1, P.x1 + 6, C.white)
      cursorTag(g, P.x1 + 6, curY, fmtNum(last, dec))
    }

    // 读数带：鼠标指着的那根（否则是最新一根）的开高低收，后面跟指标数值
    const m = mouse.current
    if (mode === 'candle') {
      const yB = P.y0 + BAND / 2 - 1
      g.rect(0, 0, P.x1, P.y0 + BAND - 2, C.void)
      const inside = m && m.x >= P.x0 && m.x <= P.x1 && m.y >= P.y0 && m.y <= P.y1
      const tM = inside ? ta + ((m!.x - P.x0) / (P.x1 - P.x0)) * sp : 0
      const all = candles ?? []
      const c = inside ? vc.find((x) => tM >= x.t && tM < x.t + interval) : vc[vc.length - 1]
      let xB = 10
      if (c) {
        const s = tr('chart.ohlc', { o: fmtNum(c.o, dec), h: fmtNum(c.h, dec), l: fmtNum(c.l, dec), c: fmtNum(c.c, dec) })
        g.text(s, xB, yB, { size: 11.5, color: C.t2, baseline: 'middle' })
        xB += g.measure(s, { size: 11.5 }) + 14
      }
      // 足迹图：鼠标指着的那一格的主动买卖
      if (c && inside && fpDs > 0 && m!.y >= PP.y0 && m!.y <= PP.y1) {
        const fb = footprintProp?.bars.get(c.t)
        const pr = a + (1 - (m!.y - PP.y0) / (PP.y1 - PP.y0)) * (b - a)
        const lv = fb ? mergedBar(fb, fpDs).levels.find((l) => pr >= l.p && pr < l.p + fpDs) : undefined
        if (lv) {
          const s = tr('fl.cell', { px: fmtNum(lv.p, dec), b: lv.b.toFixed(lv.b >= 100 ? 0 : 3), s: lv.s.toFixed(lv.s >= 100 ? 0 : 3), d: (lv.b - lv.s).toFixed(Math.abs(lv.b - lv.s) >= 100 ? 0 : 3) })
          g.text(s, xB, yB, { size: 11.5, color: C.t1, baseline: 'middle' })
          xB += g.measure(s, { size: 11.5 }) + 14
        }
      }
      let xMax = P.x1 - 8
      if (allPanes.length > shownPanes.length) {
        const s = tr('ct.noRoom', { n: allPanes.length - shownPanes.length })
        const w = g.measure(s, { size: 11 })
        if (xB + w < xMax) { g.text(s, xMax, yB, { size: 11, color: C.t3, align: 'right', baseline: 'middle' }); xMax -= w + 14 }
      }
      if (ind?.main.length) drawLegend(g, ind.main, c ? all.indexOf(c) : all.length - 1, xB, yB, xMax, dec)
    }
    // 经济事件：一面小旗，旗后面一小时是平均振幅的淡带子
    if (events?.list.length && mode === 'candle') {
      const bars = candles ?? []
      let tipAt = -1, tipText = ''
      for (const e of events.list) {
        if (e.t < ta || e.t > tb) continue
        const x = xOf(e.t)
        if (x < P.x0 || x > P.x1) continue
        if (events.band && evVolOk(e) && e.vol) {
          // 带子的中心是事件发生时的价格
          let lo = 0, hi = bars.length - 1
          while (lo < hi) { const mid = (lo + hi + 1) >> 1; if (bars[mid].t <= e.t) lo = mid; else hi = mid - 1 }
          const px = bars[lo]?.c
          if (px) {
            const half = px * (e.vol.mean / 100) / 2
            const x1 = Math.min(P.x1, xOf(e.t + 3_600_000))
            g.rect(x, yOf(px + half), x1, yOf(px - half), 'rgba(212,164,76,0.1)')
            g.hline(yOf(px + half), x, x1, 'rgba(212,164,76,0.35)')
            g.hline(yOf(px - half), x, x1, 'rgba(212,164,76,0.35)')
          }
        }
        dashedV(g, x, PP.y0, PP.y1, 'rgba(212,164,76,0.55)')
        const label = evShort(e.key)
        const lw = g.measure(label, { size: 11 }) + 8
        g.rect(x, PP.y0, x + lw, PP.y0 + 16, 'rgba(212,164,76,0.9)')
        g.text(label, x + 4, PP.y0 + 8, { size: 11, color: '#111', baseline: 'middle', weight: 500 })
        if (m && Math.abs(m.x - x) < 8) { tipAt = x; tipText = evVolOk(e) && e.vol ? tr('ev.tip', { name: evLong(e.key), n: e.vol.n, v: e.vol.mean.toFixed(2) }) : tr('ev.tipNone', { name: evLong(e.key) }) }
      }
      if (tipAt >= 0) {
        const w = g.measure(tipText, { size: 12 })
        const right = tipAt + w + 20 > P.x1
        g.text(tipText, right ? tipAt - 8 : tipAt + 8, PP.y0 + 28, { size: 12, color: C.t1, align: right ? 'right' : 'left', baseline: 'middle', plate: { color: 'rgba(0,0,0,0.85)', padX: 5, padY: 3, border: C.rule2 } })
      }
    }
    // 十字准线
    if (m && m.x >= P.x0 && m.x <= P.x1 && m.y >= PP.y0 && m.y <= PP.y1) {
      const t = ta + ((m.x - P.x0) / (P.x1 - P.x0)) * sp
      const v = a + (1 - (m.y - PP.y0) / (PP.y1 - PP.y0)) * (b - a)
      dashedV(g, m.x, P.y0, P.y1)
      dashedH(g, m.y, P.x0, P.x1)
      cursorTag(g, P.x1 + 6, m.y, fmtNum(v, dec), { color: C.plate, ink: C.t1 })
      const d = wall(t)
      const tl = mode === 'candle' && interval >= 3600e3 ? `${d.mo}/${d.d} ${clock(t, false)}` : clock(t, mode !== 'candle' || interval < 60e3, mode === 'step')
      if (!view.current.drag) cursorTag(g, m.x, P.y1 + 13, tl, { color: C.plate, ink: C.t1 })
    }
  }

  // 滚轮：上下 = 以鼠标为中心缩放；左右（触控板）或 Shift + 滚轮 = 平移。要阻止页面默认行为，所以手动挂监听
  useEffect(() => {
    const el = ref.current
    if (!el || !panning) return
    const onWheel = (e: WheelEvent) => {
      e.preventDefault()
      const v = view.current
      const k = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? 400 : 1
      const dx = e.deltaX * k, dy = e.deltaY * k
      if (Math.abs(dx) > Math.abs(dy) || e.shiftKey) {
        v.vel = 0
        v.tOff = v.off = Math.max(0, v.off + (dx || dy) / v.barW)
      } else {
        const nw = Math.max(2, Math.min(barMax(!!fpRef.current), v.barW * Math.exp(-dy * 0.0016)))
        if (nw === v.barW) return
        // 鼠标下面那根 K 线不动（跟随最新时右边缘不动）
        if (v.off > 0.5) {
          const r = el.getBoundingClientRect()
          const fromRight = Math.max(0, geo.current.x1 - (e.clientX - r.left)) // 鼠标到绘图区右边缘的距离
          const barsRight = fromRight / v.barW
          v.tOff = v.off = Math.max(0, v.off + barsRight - fromRight / nw)
        }
        v.barW = nw
        try { localStorage.setItem(fpRef.current ? FP_BAR_KEY : BAR_KEY, nw.toFixed(2)) } catch { /* 忽略 */ }
      }
      redraw()
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [panning, ref, redraw])

  // 鼠标位置下自己的交易线：离线 4 像素内，或在左侧标签上
  function tradeAt(x: number, y: number) {
    let best: (typeof tHits.current)[number] | null = null, bd = Infinity
    for (const h of tHits.current) {
      const d = x >= h.tx0 && x <= h.tx1 && Math.abs(y - h.ty) <= 9 ? 0 : Math.abs(y - h.y)
      if (d <= 4 && d < bd) { bd = d; best = h }
    }
    return best
  }
  const pxOfY = (y: number) => { const m = yMap.current; return m.a + (1 - (y - m.y0) / (m.y1 - m.y0)) * (m.b - m.a) }
  const snap = (p: number) => { const tk = tradeRef.current?.tick; return tk ? Math.round(p / tk) * tk : p }
  // 拖完松手：交给外面改价；成功先停在新价（半透明）等服务器确认，失败弹回原价
  const dropTrade = async () => {
    const d = tDrag.current
    tDrag.current = null
    if (!d || !d.moved || Math.abs(d.price - d.line.price) < (tradeRef.current?.tick ?? 1e-9) / 2) { redraw(); return }
    const fn = tradeRef.current?.onDrag
    if (!fn) { redraw(); return }
    tPending.current.set(d.line.id, d.price)
    redraw()
    let ok = false
    try { ok = await fn(d.line, d.price) } catch { ok = false }
    if (!ok) {
      tPending.current.delete(d.line.id)
      tBack.current.set(d.line.id, { from: d.price, to: d.line.price, t0: performance.now() })
    } else window.setTimeout(() => { tPending.current.delete(d.line.id); redraw() }, 3000) // 兜底：3 秒内服务器的新价格会到
    redraw()
  }
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const d = tDrag.current
      if (e.key !== 'Escape' || !d) return
      tDrag.current = null
      tBack.current.set(d.line.id, { from: d.price, to: d.line.price, t0: performance.now() })
      redraw()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [redraw])

  // 屏幕位置 → 时间和价格（画线用）
  function pointAt(x: number, y: number): ShapePt | null {
    const gm = geo.current, ym = yMap.current
    if (!gm.x1 || x > gm.x1 || y < ym.y0 || y > ym.y1) return null
    return { t: gm.ta + (x / gm.x1) * gm.sp, p: ym.a + (1 - (y - ym.y0) / (ym.y1 - ym.y0)) * (ym.b - ym.a) }
  }
  function finishDraft() {
    const dr = draft.current
    if (!dr) return
    draft.current = null
    if (dr.kind === 'measure') measure.current = { a: dr.a, b: dr.b }
    else onShapesRef.current?.([...shapesRef.current, { id: newShapeId(), kind: dr.kind, a: dr.a, b: dr.b }])
    onToolEndRef.current?.()
  }
  useEffect(() => {
    if (!panning) return
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement
      if (el.closest?.('input, textarea, [contenteditable]')) return
      if (e.key === 'Escape') {
        if (draft.current || toolRef.current || measure.current || shapeSel.current) {
          draft.current = null
          measure.current = null
          shapeSel.current = null
          if (toolRef.current) onToolEndRef.current?.()
          redraw()
        }
      } else if ((e.key === 'Delete' || e.key === 'Backspace') && shapeSel.current) {
        const id = shapeSel.current
        shapeSel.current = null
        onShapesRef.current?.(shapesRef.current.filter((q) => q.id !== id))
        redraw()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [panning, redraw])

  // 鼠标位置下的大户线：离线 4 像素内，或在右侧标签上
  function whaleAt(x: number, y: number) {
    let best: (typeof hits.current)[number] | null = null, bd = Infinity
    for (const h of hits.current) {
      const d = x >= h.tx0 && x <= (h.tx1 ?? Infinity) && Math.abs(y - h.ty) <= 8 ? 0 : Math.abs(y - h.y)
      if (d <= 4 && d < bd) { bd = d; best = h }
    }
    return best
  }

  // Esc 关掉固定的卡片
  useEffect(() => {
    if (!card?.pinned) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') { setCard(null); redraw() } }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [card?.pinned, redraw])

  const toLatest = () => { const v = view.current; v.vel = 0; v.tOff = 0; redraw() }

  return (
    // data-last：最后一根 K 线的收盘价（检查脚本读它，看最后一根是不是跟着实时价走）
    <div className="surface" data-last={mode === 'candle' && candles?.length ? candles[candles.length - 1].c : undefined}>
      <canvas
        ref={ref}
        className={`interactive ${panning ? 'pannable' : ''} ${tool ? 'drawing' : ''}`}
        onContextMenu={(e) => {
          if (!onPriceMenu || !yMap.current || tool) return
          const r0 = e.currentTarget.getBoundingClientRect()
          const y0 = e.clientY - r0.top
          const ym = yMap.current
          if (y0 < ym.y0 || y0 > ym.y1) return
          e.preventDefault()
          onPriceMenu(ym.a + (1 - (y0 - ym.y0) / (ym.y1 - ym.y0)) * (ym.b - ym.a), e.clientX, e.clientY)
        }}
        onPointerDown={(e) => {
          down.current = { x: e.clientX, y: e.clientY }
          if (e.button !== 0) return
          if (onPickPrice && yMap.current) {
            const r0 = e.currentTarget.getBoundingClientRect()
            const x0 = e.clientX - r0.left, y0 = e.clientY - r0.top
            const ym = yMap.current
            if ((e.altKey || x0 > r0.width - AXIS) && y0 >= ym.y0 && y0 <= ym.y1) {
              onPickPrice(ym.a + (1 - (y0 - ym.y0) / (ym.y1 - ym.y0)) * (ym.b - ym.a))
              return
            }
          }
          // 按在可拖动的交易线上：拖线改价，不拖图表
          const r0 = e.currentTarget.getBoundingClientRect()
          const th = tradeAt(e.clientX - r0.left, e.clientY - r0.top)
          if (th && th.line.drag && tradeRef.current?.onDrag) {
            e.currentTarget.setPointerCapture(e.pointerId)
            tDrag.current = { line: th.line, y0: e.clientY, price: th.line.price, moved: false }
            setCard(null)
            redraw()
            return
          }
          if (panning && onShapesRef.current) {
            const x = e.clientX - r0.left, y = e.clientY - r0.top
            const pt = pointAt(x, y)
            const tk = toolRef.current
            const dr = draft.current
            if (dr && dr.fixed && pt) { // 第二下点击：画完
              dr.b = pt
              finishDraft()
              redraw()
              return
            }
            if (tk && pt) {
              measure.current = null
              if (tk === 'hline') {
                onShapesRef.current([...shapesRef.current, { id: newShapeId(), kind: 'hline', a: pt, b: pt }])
                onToolEndRef.current?.()
              } else {
                draft.current = { kind: tk, a: pt, b: pt, fixed: false }
                e.currentTarget.setPointerCapture(e.pointerId)
              }
              redraw()
              return
            }
            if (measure.current) { measure.current = null; redraw() }
            const pj = projRef.current
            const hit = pj && pt ? shapeAt(shapesRef.current, x, y, pj, shapeSel.current) : null
            if (hit && pt) {
              if (hit.part === 'del') {
                onShapesRef.current(shapesRef.current.filter((q) => q.id !== hit.id))
                shapeSel.current = null
              } else {
                const orig = shapesRef.current.find((q) => q.id === hit.id)!
                shapeSel.current = hit.id
                shapeDrag.current = { hit, start: pt, orig, now: orig, moved: false }
                e.currentTarget.setPointerCapture(e.pointerId)
              }
              redraw()
              return
            }
            if (shapeSel.current) { shapeSel.current = null; redraw() }
          }
          if (!panning) return
          const v = view.current
          e.currentTarget.setPointerCapture(e.pointerId)
          v.vel = 0
          v.drag = { x: e.clientX, off: v.off, samples: [{ t: performance.now(), off: v.off }] }
          e.currentTarget.classList.add('dragging')
        }}
        onPointerMove={(e) => {
          const r = e.currentTarget.getBoundingClientRect()
          mouse.current = { x: e.clientX - r.left, y: e.clientY - r.top }
          const v = view.current
          const td = tDrag.current
          {
            const dr = draft.current
            const pt = pointAt(mouse.current.x, mouse.current.y)
            if (dr && pt) { dr.b = pt; redraw(); return }
            const sd = shapeDrag.current
            if (sd && pt) {
              const dt = pt.t - sd.start.t, dp = pt.p - sd.start.p
              if (Math.abs(dt) > 0 || Math.abs(dp) > 0) sd.moved = true
              const o = sd.orig
              sd.now = sd.hit.part === 'a' ? { ...o, a: pt, b: o.kind === 'hline' ? pt : o.b }
                : sd.hit.part === 'b' ? { ...o, b: pt }
                : { ...o, a: { t: o.a.t + dt, p: o.a.p + dp }, b: { t: o.b.t + dt, p: o.b.p + dp } }
              redraw()
              return
            }
            if (e.pointerType === 'mouse' && !v.drag && !td && panning && projRef.current && onShapesRef.current) {
              const h = shapeAt(shapesRef.current, mouse.current.x, mouse.current.y, projRef.current, shapeSel.current)
              const id = h?.id ?? null
              if (id !== shapeHot.current) {
                shapeHot.current = id
                e.currentTarget.style.cursor = id ? (h!.part === 'del' ? 'pointer' : 'move') : ''
              }
            }
          }
          // 白话说明：鼠标停在某个标签上（拖动中不显示）
          {
            const m = mouse.current
            const hh = !td && !v.drag && e.pointerType === 'mouse' ? hintHits.current.find((h) => m.x >= h.x0 && m.x <= h.x1 && m.y >= h.y0 && m.y <= h.y1) : undefined
            if ((hh?.text ?? '') !== hintRef.current) {
              hintRef.current = hh?.text ?? ''
              // 靠近上下边缘的标签：说明框改成往下 / 往上展开，不被图表边缘切掉
              const v = hh ? (hh.y0 < 60 ? 'top' : hh.y1 > r.height - 60 ? 'bot' : 'mid') : 'mid'
              setHint(hh ? { text: hh.text, x: hh.x0 < r.width / 2 ? hh.x1 + 8 : hh.x0 - 8, y: v === 'top' ? hh.y0 : v === 'bot' ? hh.y1 : (hh.y0 + hh.y1) / 2, right: hh.x0 >= r.width / 2, v } : null)
            }
          }
          if (td) {
            if (Math.abs(e.clientY - td.y0) >= 3) td.moved = true
            if (td.moved) td.price = snap(pxOfY(mouse.current.y))
            redraw()
            return
          }
          if (v.drag) {
            v.tOff = v.off = Math.max(0, v.drag.off + (e.clientX - v.drag.x) / v.barW)
            const now = performance.now()
            v.drag.samples.push({ t: now, off: v.off })
            while (v.drag.samples.length > 2 && now - v.drag.samples[0].t > 90) v.drag.samples.shift()
          } else if (e.pointerType === 'mouse' && tradeAt(mouse.current.x, mouse.current.y)) {
            // 悬停在自己的交易线上：高亮；能拖的线显示上下拖动的光标
            const th = tradeAt(mouse.current.x, mouse.current.y)!
            if (tHot.current !== th.line.id) { tHot.current = th.line.id; hot.current = '' }
            e.currentTarget.style.cursor = th.line.drag && tradeRef.current?.onDrag ? 'ns-resize' : 'default'
          } else if (e.pointerType === 'mouse') {
            if (tHot.current) { tHot.current = ''; e.currentTarget.style.cursor = '' }
            // 悬停在大户线 / 标签上：高亮并显示卡片（已固定的卡片不跟着变）
            const h = whaleAt(mouse.current.x, mouse.current.y)
            const addr = h?.addr ?? ''
            if (addr !== hot.current) {
              hot.current = addr
              e.currentTarget.style.cursor = addr ? 'pointer' : ''
              if (!cardRef.current?.pinned) setCard(h ? { addr, x: mouse.current.x, y: h.y, pinned: false } : null)
            }
          }
          redraw()
        }}
        onPointerUp={(e) => {
          if (tDrag.current) { down.current = null; void dropTrade(); return }
          {
            const dr = draft.current
            if (dr && !dr.fixed) {
              const d = down.current
              const far = d ? Math.hypot(e.clientX - d.x, e.clientY - d.y) >= 6 : true
              down.current = null
              if (far) finishDraft(); else dr.fixed = true // 只点了一下：等第二下
              redraw()
              return
            }
            const sd = shapeDrag.current
            if (sd) {
              shapeDrag.current = null
              down.current = null
              if (sd.moved) onShapesRef.current?.(shapesRef.current.map((q) => (q.id === sd.now.id ? sd.now : q)))
              redraw()
              return
            }
          }
          // 点击（没拖动）：点在大户线上 = 固定 / 取消固定它的卡片；点在别处 = 关掉固定的卡片
          const d0 = down.current
          down.current = null
          if (d0 && Math.hypot(e.clientX - d0.x, e.clientY - d0.y) < 5) {
            const r = e.currentTarget.getBoundingClientRect()
            const mx = e.clientX - r.left, my = e.clientY - r.top
            const h = whaleAt(mx, my)
            const c = cardRef.current
            setCard(h ? (c?.pinned && c.addr === h.addr ? null : { addr: h.addr, x: mx, y: h.y, pinned: true }) : null)
            if (!h) hot.current = ''
          }
          const v = view.current
          if (!v.drag) return
          const s = v.drag.samples
          const a = s[0], b = s[s.length - 1]
          if (b.t - a.t > 10 && performance.now() - b.t < 60) v.vel = (b.off - a.off) / (b.t - a.t)
          v.drag = null
          e.currentTarget.classList.remove('dragging')
          redraw()
        }}
        onPointerCancel={(e) => {
          view.current.drag = null
          e.currentTarget.classList.remove('dragging')
          setHint(null); hintRef.current = ''
          const d = tDrag.current
          if (d) { tDrag.current = null; tBack.current.set(d.line.id, { from: d.price, to: d.line.price, t0: performance.now() }); redraw() }
        }}
        onPointerLeave={() => {
          mouse.current = null
          hintRef.current = ''
          setHint(null)
          hot.current = ''
          tHot.current = ''
          if (!cardRef.current?.pinned) setCard(null)
          redraw()
        }}
        onDoubleClick={() => panning && toLatest()}
      />
      {hint && <div className={`chart-hint ${hint.right ? 'r' : ''} ${hint.v}`} style={{ left: hint.x, top: hint.y }} role="tooltip">{hint.text}</div>}
      {card && onchain && <WhaleCard card={card} onchain={onchain} px={lastPx.current} dec={dec} onClose={() => { setCard(null); redraw() }} />}
      {panning && (
        <button ref={latestBtn} className="scope-latest" onClick={toLatest} title={tr('chart.latestHint')}>
          {tr('chart.latest')}
          <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true"><path d="M2 2l3 3-3 3M6 2v6" fill="none" stroke="currentColor" strokeWidth="1.2" /></svg>
        </button>
      )}
    </div>
  )
}

/** 大户持仓详情卡：悬停时跟随显示（不挡鼠标），点击线条后固定，可点地址去 Hyperliquid 浏览器 */
function WhaleCard({ card, onchain, px, dec, onClose }: { card: { addr: string; x: number; y: number; pinned: boolean }; onchain: OnchainOverlay; px: number; dec: number; onClose: () => void }) {
  const w = onchain.whales.find((x) => x.addr === card.addr)
  const box = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null)
  // 放在线的下方（靠下时放上方），水平方向在鼠标右侧（靠右时放左侧），不出图表
  useEffect(() => {
    const el = box.current, host = el?.parentElement
    if (!el || !host) return
    const W = host.clientWidth, H = host.clientHeight, cw = el.offsetWidth, ch = el.offsetHeight
    const left = Math.max(8, Math.min(card.x + 16 + cw > W - AXIS ? card.x - 16 - cw : card.x + 16, W - AXIS - cw - 8))
    const top = card.y + 12 + ch > H - BOTTOM ? Math.max(8, card.y - 12 - ch) : card.y + 12
    setPos({ left, top })
  }, [card.x, card.y, card.addr, card.pinned])
  if (!w) return null
  const usd = (v: number) => tr('wc.usd', { v: fmtCompactL(Math.abs(v)) })
  const signed = (v: number) => (v > 0 ? '+' : v < 0 ? '−' : '') + usd(v)
  const cls = (v: number) => (v > 0 ? 'up' : v < 0 ? 'down' : '')
  const liqPct = w.liq > 0 && px > 0 ? (w.liq / px - 1) * 100 : NaN
  const short = `${card.addr.slice(0, 6)}…${card.addr.slice(-4)}`
  return (
    <div ref={box} className={`whale-card ${card.pinned ? 'pinned' : ''}`} style={pos ? pos : { visibility: 'hidden' }} role={card.pinned ? 'dialog' : 'tooltip'} aria-label={tr('wc.title')}>
      <div className="wc-head">
        <span className={`wc-side ${w.size > 0 ? 'up' : 'down'}`}>{tr(w.size > 0 ? 'chart.long' : 'chart.short')}</span>
        <span className="num">{Math.abs(w.size).toLocaleString(numLocale(), { maximumFractionDigits: 2 })} {onchain.coin}</span>
        <span className="wc-sub">{tr('wc.approx', { v: usd(w.value) })}</span>
        {card.pinned && <button type="button" className="wc-x" onClick={onClose} aria-label={tr('wc.close')}>×</button>}
      </div>
      <dl className="wc-rows">
        <dt>{tr('wc.entry')}</dt><dd className="num">{fmtNum(w.entry, dec)}</dd>
        <dt>{tr('wc.upnl')}</dt><dd className={`num ${cls(w.upnl)}`}>{signed(w.upnl)}</dd>
        <dt>{tr('wc.liq')}</dt><dd className="num">{w.liq > 0 ? <>{fmtNum(w.liq, dec)}{Number.isFinite(liqPct) && <span className="wc-pct">{(liqPct > 0 ? '+' : '−') + locDec(Math.abs(liqPct).toFixed(1))}%</span>}</> : tr('wc.liqNone')}</dd>
        <dt>{tr('wc.pnlAll')}</dt><dd className={`num ${cls(w.pnl)}`}>{signed(w.pnl)}</dd>
        <dt>{tr('wc.pnlMonth')}</dt><dd className={`num ${cls(w.month)}`}>{signed(w.month)}</dd>
      </dl>
      <div className="wc-foot">
        {card.pinned
          ? <a href={`https://app.hyperliquid.xyz/explorer/address/${card.addr}`} target="_blank" rel="noopener noreferrer"><span className="num">{short}</span> · {tr('wc.open')} ↗</a>
          : <span><span className="num">{short}</span> · {tr('wc.pinHint')}</span>}
      </div>
    </div>
  )
}

/**
 * 强平聚集区：把相邻 5 格（BTC 一格 100 美元，合起来约 0.6%）合在一起找金额最大的几段，两边各取前 3 段，互相至少隔开约 1%。
 * 多单的强平价在现价下方（long_value），空单的在上方（short_value）。
 * mid 是这一段里强平价按金额加权的平均价（不是格子中点），所以谁在什么时候打开，看到的价格都一样。
 */
/** 聚集区标签的价格位数：约 5 位有效数字（加权平均价，小数点后几位没有意义） */
function lblDec(px: number, dec: number): number {
  return Math.max(0, Math.min(dec, 4 - Math.floor(Math.log10(Math.abs(px) || 1))))
}

export function liqClusters(bins: OnchainOverlay['bins'], px: number): { side: 'long' | 'short'; lo: number; hi: number; mid: number; value: number }[] {
  const n = bins.length
  const W = 5
  const out: { side: 'long' | 'short'; lo: number; hi: number; mid: number; value: number }[] = []
  for (const side of ['long', 'short'] as const) {
    const val = (i: number) => (side === 'long' ? bins[i].long_value : bins[i].short_value)
    const used = new Array(n).fill(false)
    for (let r = 0; r < 3; r++) {
      let best = -1, bv = 0
      for (let i = 0; i + W <= n; i++) {
        if (used[i]) continue
        const mid = (bins[i].lo + bins[i + W - 1].hi) / 2
        if (side === 'long' ? mid >= px : mid <= px) continue
        let sum = 0
        for (let k = i; k < i + W; k++) sum += val(k)
        if (sum > bv) { bv = sum; best = i }
      }
      if (best < 0 || bv <= 0) break
      for (let k = Math.max(0, best - 8); k < Math.min(n, best + W + 8); k++) used[k] = true
      let wpx = 0
      for (let k = best; k < best + W; k++) {
        const bpx = side === 'long' ? bins[k].long_px : bins[k].short_px
        wpx += val(k) * (bpx && bpx > 0 ? bpx : (bins[k].lo + bins[k].hi) / 2)
      }
      out.push({ side, lo: bins[best].lo, hi: bins[best + W - 1].hi, mid: wpx / bv, value: bv })
    }
  }
  return out
}

