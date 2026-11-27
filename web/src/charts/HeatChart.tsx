// 实时流动性频谱（12 的 V.7–V.10）：横轴时间，纵轴价格，亮度是挂单金额。
// 图内只有光、价格线、成交点和小记号，文字都在图外：大单列在价格尺上，疑似行为排在底部的事件带上。
// 两层纹理：历史（后端录的，每秒一列，看得远时合并）+ 最近 3 分钟（每 100 毫秒一列）。
// 亮度是固定标尺：一个币用同一套，缩放平移不会变，同样的亮度永远是同样的金额。
import { useEffect, useRef } from 'react'
import { HeatGL, autoTone, type HeatLayer } from './heatgl'
import { C, cursorTag, dirColors, fmtNum, timeScale, valueScale, type Box } from './scale'
import { dashedH, dashedV } from './Spectrum'
import { liqClusters, type OnchainOverlay, type TradeLine } from './Scope'
import { clock, makeG, type G } from '../lib/gfx'
import { approachAll } from '../lib/motion'
import { fmtCompactL, tr, type Key } from '../lib/i18n'
import { cellUSD, decodeB64, getHeatRamp, usdCell, type HeatEvent, type HeatFrame, type HeatHistory, type HeatWall } from '../lib/heat'
import { locDec } from '../lib/tz'

export interface HeatLayers { events: boolean; chain: boolean; whales: boolean; own: boolean }

interface Props {
  inst: string
  dec: number
  span: number
  layers: HeatLayers
  /** 金额下限（美元）：低于它的挂单不亮 */
  floor?: number
  chain?: OnchainOverlay
  own?: TradeLine[]
  /** 复盘回放（模块 5）：只看这段历史，不连实时 */
  replay?: { from: number; to: number; marks?: { t: number; p: number; buy: boolean; label: string }[]; at?: number }
  onStatus?: (s: { connected: boolean; full?: [number, number]; since?: number; loaded: boolean; syncing: boolean }) => void
  /** 每秒一次：现在的大单、最近 5 分钟主动买卖、现价 */
  onRead?: (r: { walls: HeatWall[]; flow: [number, number]; mid: number }) => void
  /** 亮度标尺定下来了：lo 以下全黑，hi 以上全白（美元） */
  onScale?: (s: { lo: number; hi: number }) => void
}

const LIVE_COLS = 1800 // 100 毫秒一列，3 分钟
const GAMMA = 1.35

type View = { ta: number; tb: number; pa: number; pb: number }
type Dot = { t: number; p: number; u: number; buy: boolean; x: number; y: number; r: number }
type Hit = { x0: number; y0: number; x1: number; y1: number; lines: string[] }

const ageText = (s: number) => (s < 60 ? tr('heat.ageS', { n: s }) : s < 3600 ? tr('heat.ageM', { n: Math.floor(s / 60) }) : tr('heat.ageH', { h: Math.floor(s / 3600), m: Math.floor((s % 3600) / 60) }))

function evTitle(e: HeatEvent): string {
  return tr(`heat.ev.${e.kind}` as Key, { v: fmtCompactL(e.usd), side: tr(e.bid ? 'heat.sideBid' : 'heat.sideAsk') })
}
function evWhy(e: HeatEvent): string {
  const x = e.ev ?? {}
  const f = (n: number | undefined) => (n == null ? '—' : locDec(String(n)))
  switch (e.kind) {
    case 'spoof': return tr('heat.why.spoof', { age: f(x.age), dist: f(x.dist), filled: f(x.filled) })
    case 'absorb': return tr('heat.why.absorb', { secs: f(x.secs), left: fmtCompactL(x.left ?? 0) })
    case 'iceberg': return tr('heat.why.iceberg', { shown: fmtCompactL(x.shown ?? 0), mult: f(x.mult), secs: f(x.secs) })
    case 'sweep': return tr('heat.why.sweep', { secs: f(x.secs), move: f(x.move), side: f(x.oneSide), mult: f(x.mult) })
    default: return tr('heat.why.large', { mult: f(x.mult) })
  }
}

// 小记号：疑似行为只有这一种画法，差别靠形状
function glyph(g: G, kind: HeatEvent['kind'], x: number, y: number, bid: boolean, color: string) {
  const c = g.ctx, X = g.X(x) + 0.5, Y = g.Y(y) + 0.5, s = g.px(3)
  c.strokeStyle = color
  c.lineWidth = g.px(1)
  c.beginPath()
  if (kind === 'spoof') { c.moveTo(X - s, Y - s); c.lineTo(X + s, Y + s); c.moveTo(X + s, Y - s); c.lineTo(X - s, Y + s) }
  else if (kind === 'iceberg') { c.moveTo(X, Y - s); c.lineTo(X + s, Y); c.lineTo(X, Y + s); c.lineTo(X - s, Y); c.closePath() }
  else if (kind === 'absorb') { c.rect(X - s, Y - s, s * 2, s * 2) }
  else if (kind === 'sweep') { const d = bid ? 1 : -1; c.moveTo(X - s, Y - d * s); c.lineTo(X, Y + d * s); c.lineTo(X + s, Y - d * s) }
  else { c.arc(X, Y, s * 0.7, 0, Math.PI * 2) }
  c.stroke()
}

export function HeatChart({ inst, dec, span, layers, floor = 0, chain, own, replay, onStatus, onRead, onScale }: Props) {
  const glc = useRef<HTMLCanvasElement>(null)
  const ovc = useRef<HTMLCanvasElement>(null)
  // 这些变了不用重新连数据，下一帧画的时候读
  const props = useRef({ layers, chain, own, dec })
  props.current = { layers, chain, own, dec }
  const spanRef = useRef<(s: number) => void>(() => undefined)
  const floorRef = useRef<(v: number) => void>(() => undefined)
  const cb = useRef({ onStatus, onRead, onScale })
  cb.current = { onStatus, onRead, onScale }

  useEffect(() => { spanRef.current(span) }, [span])
  useEffect(() => { floorRef.current(floor) }, [floor])
  // 回放的播放头：右边的盘口剖面取这一刻，后面的画面压暗
  const atRef = useRef<number | undefined>(replay?.at)
  atRef.current = replay?.at
  const dirtyRef = useRef<() => void>(() => undefined)
  const latestBtn = useRef<HTMLButtonElement>(null)
  const autoBtn = useRef<HTMLButtonElement>(null)
  const ctlBox = useRef<HTMLDivElement>(null)
  const goLatestRef = useRef<() => void>(() => undefined)
  const goAutoRef = useRef<() => void>(() => undefined)
  useEffect(() => { dirtyRef.current() }, [replay?.at])

  useEffect(() => {
    const gc = glc.current, oc = ovc.current
    if (!gc || !oc) return
    let gl: HeatGL | null = null
    let hLayer: HeatLayer | null = null, lLayer: HeatLayer | null = null
    try {
      gl = location.search.includes('nogl=1') ? null : new HeatGL(gc)
      hLayer = gl?.layer() ?? null
      lLayer = gl?.layer() ?? null
    } catch { gl = null }
    const ctx = oc.getContext('2d')!
    const now0 = replay ? replay.to : Date.now()
    const initialSpan = replay ? replay.to - replay.from : span
    const v: View = { ta: now0 - initialSpan, tb: now0 + initialSpan * 0.02, pa: 0, pb: 0 }
    const vt: View = { ...v }
    let follow = !replay
    let w = 0, h = 0, W = 0, H = 0, dirty = true, raf = 0, last = performance.now()
    let mouse: { x: number; y: number } | null = null
    let drag: { x: number; y: number; v: View; axis: boolean } | null = null
    let moved = false
    let idleAt = 0
    let yAuto = true // 价格范围自动包住这段时间的折线；用户手动调过就停，双击或按钮恢复
    let fitAt = 0
    let chipHits: { x0: number; y0: number; x1: number; y1: number; p: number; text: string }[] = []
    let clMemo: { bins: unknown; mid: number; out: ReturnType<typeof liqClusters> } | null = null
    let hits: Hit[] = []
    let dots: Dot[] = []

    // ---------- 亮度标尺 ----------
    // 第一次有 5 分钟以上的数据时定下来，之后不再变。没到 5 分钟就先用现有的。
    let scale = { black: 0.55, white: 0.95 }, scaleFixed = false, floorUSD = floor
    const toneNow = () => {
      const fb = floorUSD > 0 ? usdCell(floorUSD) / 255 : 0
      return { black: Math.max(scale.black, fb), white: Math.max(scale.white, fb + 0.05) }
    }
    const tellScale = () => {
      const t = toneNow()
      cb.current.onScale?.({ lo: cellUSD(Math.round(t.black * 255)), hi: cellUSD(Math.round(t.white * 255)) })
    }

    // ---------- 数据 ----------
    let hist: HeatHistory | null = null
    let hRaw = new Uint8Array(0)
    let loading = false, loadedAt = 0, histSeq = 0
    const live = {
      t0: 0, n: 0, rows: 0, base: 0, width: 0,
      raw: new Uint8Array(0), disp: new Uint8Array(0), mids: new Float64Array(LIVE_COLS), buy: new Float64Array(LIVE_COLS), sell: new Float64Array(LIVE_COLS),
      col: new Uint8Array(0), mipAt: 0,
    }
    let liveDots = new Map<string, { t: number; p: number; u: number; buy: boolean }>()
    const events = new Map<string, HeatEvent>()
    let cover: [number, number] | undefined
    let walls: HeatWall[] = []
    let connected = false, syncing = false
    const addEvents = (list: HeatEvent[]) => { for (const e of list) events.set(`${e.kind}:${e.t}:${e.p}`, e) }
    const tell = () => cb.current.onStatus?.({ connected, full: cover, since: hist?.since, loaded: !!hist, syncing })

    // 完整盘口之外的挂单数据不全：越远越暗，让亮带跟着价格走
    const halfCover = (m: number) => Math.max(0.0015, cover && cover[1] > cover[0] ? (cover[1] - cover[0]) / 2 / Math.max(m, 1e-9) : 0.002)
    const fadeOf = (d: number, d0: number) => (d <= d0 ? 1 : d >= d0 * 2.5 ? 0.3 : 1 - ((d - d0) / (d0 * 1.5)) * 0.7)
    const uploadHist = () => {
      if (!gl || !hLayer || !hist || !hist.rows || !hRaw.length) return
      const { cols, rows, mids, p0, step } = hist
      const out = new Uint8Array(hRaw)
      const b = Math.round(toneNow().black * 255)
      let m = mids.find((x) => x > 0) ?? 0
      for (let c = 0; c < cols; c++) {
        if (mids[c] > 0) m = mids[c]
        if (!m) continue
        const d0 = halfCover(m)
        for (let r = 0; r < rows; r++) {
          const i = r * cols + c, val = out[i]
          if (val <= b) continue
          const f = fadeOf(Math.abs(p0 + r * step - m) / m, d0)
          if (f < 1) out[i] = b + (val - b) * f
        }
      }
      gl.setLayer(hLayer, cols, rows, out)
    }

    const P0 = (): Box & { prof: number; ax: number; vol: number; ev: number; edge: number } => {
      const ax = w >= 900 ? 188 : w >= 560 ? 132 : 72
      const prof = w >= 900 ? 60 : 0
      const vol = h >= 300 ? 34 : 22, ev = h >= 240 ? 16 : 0
      // 叠加项开着时，上下各留一条带子放视野外的价位（和 K 线一样，不压图）
      const { layers: ly, chain: oc2, own: ol } = props.current
      const on = (ly.chain && oc2?.bins?.length) || (ly.whales && oc2?.whales?.length) || (ly.own && ol?.length)
      const edge = on && h >= 260 ? 20 : 0
      return { x0: 0, y0: edge, x1: w - ax - prof, y1: h - 22 - vol - ev - edge, prof, ax, vol, ev, edge }
    }

    const recenter = (mid: number) => {
      const half = Math.max(mid * 0.003, (live.width || mid * 0.00002) * 30)
      v.pa = vt.pa = mid - half
      v.pb = vt.pb = mid + half
    }

    // 这段时间折线的范围，上下留一点；最窄现价上下 0.3%
    const fitRange = (): [number, number] | null => {
      let lo = Infinity, hi = -Infinity
      const take = (m: number) => { if (m > 0) { if (m < lo) lo = m; if (m > hi) hi = m } }
      const useLive = live.n > 0 && !replay
      if (hist) {
        const a = Math.max(0, Math.floor((vt.ta - hist.t0) / hist.dt)), b = Math.min(hist.cols - 1, Math.ceil((vt.tb - hist.t0) / hist.dt))
        for (let i = a; i <= b; i++) {
          if (useLive && hist.t0 + i * hist.dt >= live.t0) break
          take(hist.mids[i])
        }
      }
      if (useLive) {
        const a = Math.max(0, Math.floor((vt.ta - live.t0) / 100)), b = Math.min(live.n - 1, Math.ceil((vt.tb - live.t0) / 100))
        for (let i = a; i <= b; i++) take(live.mids[i])
      }
      if (!isFinite(lo)) return null
      const c = (lo + hi) / 2, least = Math.max(c * 0.003, (live.width || c * 0.00002) * 30)
      const pad = (hi - lo) * 0.18
      return hi - lo + 2 * pad < least * 2 ? [c - least, c + least] : [lo - pad, hi + pad]
    }
    const fitY = (instant = false) => {
      if (!yAuto) return
      const r = fitRange()
      if (!r) return
      const ph = vt.pb - vt.pa
      if (!instant && ph > 0 && Math.abs(r[0] - vt.pa) < ph * 0.015 && Math.abs(r[1] - vt.pb) < ph * 0.015) return // 变化太小不动，图不抖
      vt.pa = r[0]; vt.pb = r[1]
      if (instant || !(v.pb > v.pa)) { v.pa = r[0]; v.pb = r[1] }
      dirty = true
    }
    // 不能滑到未来太远，也不能滑到开始录制之前太远
    const clampX = () => {
      if (replay) return
      const sp = vt.tb - vt.ta
      if (hist?.since && vt.ta < hist.since - sp * 0.25) { vt.ta = hist.since - sp * 0.25; vt.tb = vt.ta + sp }
      const maxB = Date.now() + sp * 0.25
      if (vt.tb > maxB) { vt.tb = maxB; vt.ta = maxB - sp }
    }
    const expandTo = (p: number) => {
      const r = fitRange() ?? [vt.pa, vt.pb]
      const lo = Math.min(r[0], p), hi = Math.max(r[1], p), pad = (hi - lo) * 0.2
      yAuto = false
      vt.pa = lo - pad; vt.pb = hi + pad
      idleAt = performance.now()
      dirty = true
    }

    const load = async () => {
      if (loading || !w) return
      loading = true
      const seq = ++histSeq
      const P = P0()
      const sp = vt.tb - vt.ta
      const end = replay ? replay.to + 1000 : Date.now()
      // 左右各多取一屏：滑的时候数据已经在手里，不露黑
      const from = Math.floor(replay ? replay.from - 1000 : vt.ta - sp)
      const to = Math.ceil(replay ? end : Math.min(end, vt.tb + sp))
      const q = new URLSearchParams({ instrument: inst, from: String(from), to: String(to), cols: String(Math.round(Math.min(4000, (P.x1 - P.x0) * devicePixelRatio * 1.25 * (replay ? 1 : 3)))), rows: String(Math.round(Math.min(2000, (P.y1 - P.y0) * devicePixelRatio * 2))) })
      if (!yAuto && vt.pb > vt.pa) { // 自动时后端按这段时间的价格范围给，滑动时不用重新取
        const ps = vt.pb - vt.pa
        q.set('pa', String(vt.pa - ps * 0.6))
        q.set('pb', String(vt.pb + ps * 0.6))
      }
      try {
        const r = await fetch(`/api/heat/history?${q}`)
        if (!r.ok) throw new Error(String(r.status))
        const d = (await r.json()) as HeatHistory
        if (seq !== histSeq || disposed) return
        const first = !hist
        hist = d
        hRaw = d.cells ? decodeB64(d.cells) : new Uint8Array(0)
        // 亮度标尺只在数据够多之前更新
        const covered = d.mids.filter((m) => m > 0).length * d.dt
        if (!scaleFixed && hRaw.length && covered > 0) {
          const t = autoTone(hRaw, d.cols, d.rows, 0, d.cols - 1, 0, d.rows - 1, 0.58, 0.998, true)
          scale = { black: t.black, white: t.white }
          if (covered >= 300_000) scaleFixed = true
          tellScale()
        }
        uploadHist()
        addEvents(d.events ?? [])
        fitY(!(vt.pb > vt.pa))
        if (!(vt.pb > vt.pa)) {
          const mid = [...d.mids].reverse().find((m) => m > 0)
          if (mid) recenter(mid)
        }
        // 第一次打开：录制还没这么长时，时间轴从开始录制处算起，不露大片空白
        if (first && follow && d.since && vt.ta < d.since) {
          vt.ta = d.since - (vt.tb - d.since) * 0.01
          v.ta = vt.ta
        }
        loadedAt = Date.now()
        dirty = true
        tell()
      } catch { /* 下次再试 */ } finally { loading = false }
    }

    let disposed = false
    let es: EventSource | null = null
    let retry = 0
    const connect = () => {
      if (replay || disposed) return
      es = new EventSource(`/api/heat/stream?instrument=${encodeURIComponent(inst)}`)
      es.onopen = () => { connected = true; tell() }
      es.onerror = () => {
        connected = false
        tell()
        es?.close()
        clearTimeout(retry)
        retry = window.setTimeout(connect, 2000)
      }
      es.onmessage = (m) => onFrame(JSON.parse(m.data) as HeatFrame)
    }

    const shiftLive = () => {
      const k = LIVE_COLS / 3 | 0
      const r = live.rows
      for (const a of [live.raw, live.disp]) {
        for (let y = 0; y < r; y++) {
          a.copyWithin(y * LIVE_COLS, y * LIVE_COLS + k, (y + 1) * LIVE_COLS)
          a.fill(0, y * LIVE_COLS + LIVE_COLS - k, (y + 1) * LIVE_COLS)
        }
      }
      for (const a of [live.mids, live.buy, live.sell]) { a.copyWithin(0, k); a.fill(0, LIVE_COLS - k) }
      live.t0 += k * 100
      live.n -= k
      if (gl && lLayer) gl.setLayer(lLayer, LIVE_COLS, r, live.disp)
    }

    const onFrame = (f: HeatFrame) => {
      if (f.ev?.length) { addEvents(f.ev); dirty = true }
      if (f.flow) {
        walls = f.walls ?? []
        cb.current.onRead?.({ walls, flow: f.flow, mid: f.m })
        dirty = true
        return
      }
      if (f.tr?.length) {
        const bk = f.w > 0 ? f.w : 1
        for (const x of f.tr) {
          const u = x.p * x.q
          const key = `${Math.floor(x.t / 1000)}:${Math.round(x.p / bk)}:${x.b ? 1 : 0}`
          const o = liveDots.get(key)
          if (o) o.u += u
          else liveDots.set(key, { t: Math.floor(x.t / 1000) * 1000 + 500, p: Math.round(x.p / bk) * bk, u, buy: !!x.b })
          if (live.t0) {
            const i = Math.min(LIVE_COLS - 1, Math.max(0, Math.round((x.t - live.t0) / 100)))
            if (x.b) live.buy[i] += u
            else live.sell[i] += u
          }
        }
        if (liveDots.size > 6000) {
          const cut = Date.now() - 200_000
          const next = new Map<string, { t: number; p: number; u: number; buy: boolean }>()
          liveDots.forEach((val, key) => { if (val.t >= cut) next.set(key, val) })
          liveDots = next
        }
      }
      if (f.ev && !f.c) return
      if (f.sync) { if (!syncing) { syncing = true; tell() } return }
      if (syncing) { syncing = false; tell() }
      if (f.cov && (!cover || cover[0] !== f.cov[0] || cover[1] !== f.cov[1])) { cover = f.cov; uploadHist(); tell() }
      if (!f.c) return
      const col = decodeB64(f.c)
      // 第一列：定下这一层的价格范围（现价上下多留一半）；价格走出去了就重新开一层
      const outside = live.rows && (f.b < live.base || f.b + col.length > live.base + live.rows)
      if (!live.rows || outside || live.width !== f.w) {
        const margin = (col.length / 2) | 0
        live.base = f.b - margin
        live.rows = col.length + 2 * margin
        live.width = f.w
        live.t0 = f.t
        live.n = 0
        live.raw = new Uint8Array(live.rows * LIVE_COLS)
        live.disp = new Uint8Array(live.rows * LIVE_COLS)
        live.mids.fill(0); live.buy.fill(0); live.sell.fill(0)
        live.col = new Uint8Array(live.rows)
        if (gl && lLayer) gl.setLayer(lLayer, LIVE_COLS, live.rows, live.disp)
        void load()
      }
      if (!(vt.pb > vt.pa)) recenter(f.m)
      let i = Math.round((f.t - live.t0) / 100)
      while (i >= LIVE_COLS) { shiftLive(); i = Math.round((f.t - live.t0) / 100) }
      if (i < live.n) return // 乱序的，丢掉
      // 这一列写进内存和纹理；中间漏掉的（最多 1 秒）用这一列补上，再久就留空
      const off = f.b - live.base
      const b = Math.round(toneNow().black * 255), d0 = halfCover(f.m)
      live.col.fill(0)
      for (let y = 0; y < col.length; y++) {
        const val = col[y]
        if (val <= b) { live.col[y + off] = val; continue }
        const f2 = fadeOf(Math.abs((f.b + y) * f.w - f.m) / f.m, d0)
        live.col[y + off] = f2 < 1 ? b + (val - b) * f2 : val
      }
      const fromI = i - live.n <= 10 ? live.n : i
      for (let c = fromI; c <= i; c++) {
        for (let y = 0; y < col.length; y++) {
          const at = (y + off) * LIVE_COLS + c
          live.raw[at] = col[y]
          live.disp[at] = live.col[y + off]
        }
        live.mids[c] = f.m
        if (gl && lLayer) gl.setColumn(lLayer, c, live.col)
      }
      live.n = i + 1
      const t = performance.now()
      if (gl && lLayer && t - live.mipAt > 500) { live.mipAt = t; gl.mips(lLayer) }
      if (follow) {
        const sp = vt.tb - vt.ta
        vt.tb = f.t + sp * 0.02
        vt.ta = vt.tb - sp
        if (yAuto) {
          if (t - fitAt > 1000) { fitAt = t; fitY() }
        } else {
          // 手动调过范围：现价快到边上时挪够就停，范围大小不变，也不把用户正在看的价位挪走
          const gap = (vt.pb - vt.pa) * 0.12
          const d = f.m > vt.pb - gap ? f.m - (vt.pb - gap) : f.m < vt.pa + gap ? f.m - (vt.pa + gap) : 0
          if (d && vt.pb > vt.pa) { vt.pa += d; vt.pb += d }
        }
      }
      dirty = true
    }

    // ---------- 画 ----------
    const lastMid = (): number => {
      if (live.n > 0) for (let i = live.n - 1; i >= 0; i--) if (live.mids[i] > 0) return live.mids[i]
      if (hist) for (let i = hist.mids.length - 1; i >= 0; i--) if (hist.mids[i] > 0) return hist.mids[i]
      return 0
    }

    const paint = () => {
      if (!w || !h) return
      for (const c of [gc, oc]) if (c.width !== W || c.height !== H) { c.width = W; c.height = H }
      const g = makeG(ctx, w, h, W, H, Date.now())
      const P = P0()
      const liveT1 = live.t0 + live.n * 100
      const tn = toneNow()
      if (gl && hLayer && lLayer) {
        const pxOf = (t: number) => g.X(P.x0 + ((t - v.ta) / (v.tb - v.ta)) * (P.x1 - P.x0))
        const y = g.Y(P.y0), hh = g.Y(P.y1) - y
        gl.clear()
        const look = { black: tn.black, white: tn.white, gamma: GAMMA, tint: 0.7, ramp: getHeatRamp() === 'teal' ? 1 : 0 }
        const quad = (l: HeatLayer, t0: number, t1: number, cols: number, dt: number, baseT: number, pLo: number, pHi: number) => {
          const a = Math.max(v.ta, t0), b = Math.min(v.tb, t1)
          if (b <= a) return
          const x0 = pxOf(a), x1 = pxOf(b)
          const tt = cols * dt
          gl!.draw(l, H, x0, y, x1 - x0, hh, { u0: (a - baseT) / tt, u1: (b - baseT) / tt, v0: (v.pa - pLo) / (pHi - pLo), v1: (v.pb - pLo) / (pHi - pLo) }, look)
        }
        if (hist && hist.rows) {
          const pLo = hist.p0 - hist.step / 2, pHi = hist.p0 + (hist.rows - 0.5) * hist.step
          quad(hLayer, hist.t0, hist.t0 + hist.cols * hist.dt, hist.cols, hist.dt, hist.t0, pLo, pHi)
        }
        if (live.n && !replay) {
          const pLo = (live.base - 0.5) * live.width, pHi = (live.base + live.rows - 0.5) * live.width
          quad(lLayer, live.t0, liveT1, LIVE_COLS, 100, live.t0, pLo, pHi)
        }
      }
      ctx.setTransform(1, 0, 0, 1, 0, 0)
      ctx.clearRect(0, 0, W, H)
      if (!gl) g.rect(P.x0, P.y0, P.x1, P.y1, C.void)
      g.rect(P.x1, 0, w, h, C.void)
      g.rect(0, P.y1, w, h, C.void)
      if (P.y0 > 0) g.rect(0, 0, P.x1, P.y0, C.void)
      overlay(g, P, liveT1)
      latestBtn.current?.classList.toggle('on', !follow && !replay)
      autoBtn.current?.classList.toggle('on', !yAuto && !replay)
      if (ctlBox.current) { ctlBox.current.style.right = `${w - P.x1 + 8}px`; ctlBox.current.style.bottom = `${h - P.y1 + 8}px` }
    }

    const overlay = (g: G, P: ReturnType<typeof P0>, liveT1: number) => {
      const dc = dirColors()
      const c2 = g.ctx
      const k = g.kx
      const { layers: ly, chain: oc2, own: ownLines, dec: dc2 } = props.current
      const dec2 = dc2
      const xOf = (t: number) => P.x0 + ((t - v.ta) / (v.tb - v.ta)) * (P.x1 - P.x0)
      const yOf = (p: number) => P.y0 + (1 - (p - v.pa) / (v.pb - v.pa)) * (P.y1 - P.y0)
      hits = []
      const xAxis = P.x1 + P.prof // 价格尺的左边缘

      c2.save()
      c2.beginPath()
      c2.rect(g.X(P.x0), g.Y(P.y0), g.X(P.x1) - g.X(P.x0), g.Y(P.y1) - g.Y(P.y0))
      c2.clip()

      // 没有数据的时段：只留一层极淡的底，读数放到时间轴和悬停里
      const gapBand = (a: number, b: number) => {
        const x0 = Math.max(P.x0, xOf(a)), x1 = Math.min(P.x1, xOf(b))
        if (x1 <= x0) return
        g.rect(x0, P.y0, x1, P.y1, 'rgba(255,255,255,0.022)')
        hits.push({ x0, y0: P.y0, x1, y1: P.y1, lines: [tr('heat.noData', { a: clock(a, b - a < 300_000), b: clock(b, b - a < 300_000) })] })
      }
      const gaps = hist?.gaps ?? []
      if (hist) {
        const first = hist.since || liveT1
        if (first && v.ta < first) gapBand(v.ta, first)
        for (const [a, b] of gaps) if (a >= first) gapBand(a, b)
      }

      // 价格：白色阶梯线。先垫一层黑，亮带上也看得清
      const trace = () => {
        c2.beginPath()
        let started = false, py = 0, lastX = -1e9
        const stepTo = (t: number, m: number) => {
          if (!(m > 0)) { started = false; return }
          const x = g.X(xOf(t)) + 0.5, y = g.Y(yOf(m)) + 0.5
          if (!started) { c2.moveTo(x, y); started = true } else { if (x - lastX < 1 && y === py) return; c2.lineTo(x, py); c2.lineTo(x, y) }
          py = y; lastX = x
        }
        if (hist) {
          const a = Math.max(0, Math.floor((v.ta - hist.t0) / hist.dt) - 1), b = Math.min(hist.cols - 1, Math.ceil((v.tb - hist.t0) / hist.dt))
          const stop = live.n && !replay ? live.t0 : Infinity
          for (let i = a; i <= b; i++) {
            const t = hist.t0 + i * hist.dt
            if (t >= stop) break
            stepTo(t, hist.mids[i])
          }
        }
        if (live.n && !replay) {
          const a = Math.max(0, Math.floor((v.ta - live.t0) / 100)), b = Math.min(live.n - 1, Math.ceil((v.tb - live.t0) / 100))
          for (let i = a; i <= b; i++) stepTo(live.t0 + i * 100, live.mids[i])
        }
      }
      trace()
      c2.strokeStyle = 'rgba(0,0,0,0.85)'
      c2.lineWidth = Math.max(3, Math.round(3 * k))
      c2.stroke()
      trace()
      c2.strokeStyle = C.white
      c2.lineWidth = Math.max(1, Math.round(k))
      c2.stroke()

      // 成交圆点：同一小块屏幕里的合并，大的才画
      const cellMs = ((v.tb - v.ta) / (P.x1 - P.x0)) * 6
      const cellP = ((v.pb - v.pa) / (P.y1 - P.y0)) * 6
      const groups = new Map<string, Dot>()
      const add = (t: number, p: number, u: number, buy: boolean) => {
        if (t < v.ta || t > v.tb || p < v.pa || p > v.pb) return
        const key = `${Math.floor(t / cellMs)}:${Math.floor(p / cellP)}:${buy ? 1 : 0}`
        const o = groups.get(key)
        if (o) o.u += u
        else groups.set(key, { t, p, u, buy, x: 0, y: 0, r: 0 })
      }
      const liveFrom = live.n && !replay ? live.t0 : Infinity
      if (hist) for (const x of hist.trades) if (x[0] < liveFrom) add(x[0], x[1], x[2], x[3] === 1)
      if (!replay) liveDots.forEach((x) => { if (x.t >= liveFrom) add(x.t, x.p, x.u, x.buy) })
      const all = [...groups.values()]
      const sizes = all.map((a) => a.u).sort((a, b) => a - b)
      const thr = sizes[Math.floor(sizes.length * 0.985)] ?? Infinity
      const top = sizes[sizes.length - 1] ?? 1
      dots = all.filter((a) => a.u >= thr).sort((a, b) => a.u - b.u).map((a) => ({ ...a, x: xOf(a.t), y: yOf(a.p), r: 2 + 5 * Math.sqrt((a.u - thr) / Math.max(top - thr, 1e-9)) }))
      for (const d of dots) {
        c2.beginPath()
        c2.arc(g.X(d.x), g.Y(d.y), d.r * k, 0, Math.PI * 2)
        c2.fillStyle = d.buy ? dc.up : dc.down
        c2.fill()
        c2.lineWidth = Math.max(1, Math.round(k))
        c2.strokeStyle = '#000'
        c2.stroke()
      }

      // 疑似行为：图里只有小记号，指上去看原因
      const shownEv = ly.events ? [...events.values()].filter((e) => e.kind !== 'large' && e.t >= v.ta && e.t <= v.tb) : []
      for (const e of shownEv) {
        if (e.p < v.pa || e.p > v.pb) continue
        const x = xOf(e.t), y = yOf(e.p)
        glyph(g, e.kind, x, y, e.bid, C.amber)
        hits.push({ x0: x - 6, y0: y - 6, x1: x + 6, y1: y + 6, lines: [evTitle(e), evWhy(e), `${fmtNum(e.p, dec2)}  ${clock(e.t)}`] })
      }
      // 回放：播放头之后的画面压暗，还没发生
      if (atRef.current != null) {
        const xa = Math.max(P.x0, xOf(atRef.current))
        g.rect(xa, P.y0, P.x1, P.y1, 'rgba(0,0,0,0.78)')
        g.vline(xa, P.y0, P.y1, C.white)
      }
      // 复盘回放（模块 5）：这笔交易的进出点
      if (replay?.marks) {
        for (const m of replay.marks) {
          const x = xOf(m.t), y = yOf(m.p)
          g.vline(x, P.y0, P.y1, 'rgba(255,255,255,0.28)')
          c2.beginPath()
          c2.arc(g.X(x), g.Y(y), g.px(4), 0, Math.PI * 2)
          c2.fillStyle = m.buy ? dc.up : dc.down
          c2.fill()
          hits.push({ x0: x - 7, y0: y - 7, x1: x + 7, y1: y + 7, lines: [m.label, `${fmtNum(m.p, dec2)}  ${clock(m.t)}`] })
        }
      }
      c2.restore()

      // 成交量带：主动买向上，主动卖向下
      const V: Box = { x0: P.x0, y0: P.y1 + P.edge, x1: P.x1, y1: P.y1 + P.edge + P.vol }
      const E: Box = { x0: P.x0, y0: V.y1, x1: P.x1, y1: V.y1 + P.ev }
      g.hline(V.y0, V.x0, xAxis + P.ax, C.rule2)
      const nb = Math.max(1, Math.floor((P.x1 - P.x0) / 3))
      const bw = (v.tb - v.ta) / nb
      const vb = new Float64Array(nb), vs = new Float64Array(nb)
      const bin = (t: number, b: number, s: number) => {
        const j = Math.floor((t - v.ta) / bw)
        if (j >= 0 && j < nb) { vb[j] += b; vs[j] += s }
      }
      if (hist) {
        const a = Math.max(0, Math.floor((v.ta - hist.t0) / hist.dt)), b = Math.min(hist.cols - 1, Math.ceil((v.tb - hist.t0) / hist.dt))
        for (let i = a; i <= b; i++) { const t = hist.t0 + i * hist.dt; if (t < liveFrom) bin(t, hist.buy[i] ?? 0, hist.sell[i] ?? 0) }
      }
      if (live.n && !replay) for (let i = 0; i < live.n; i++) if (live.buy[i] || live.sell[i]) bin(live.t0 + i * 100, live.buy[i], live.sell[i])
      let vmax = 0
      for (let j = 0; j < nb; j++) vmax = Math.max(vmax, vb[j], vs[j])
      const vm = (V.y0 + V.y1) / 2
      g.hline(vm, V.x0, V.x1, C.rule)
      const cw = Math.max(1, g.X(V.x0 + (V.x1 - V.x0) / nb) - g.X(V.x0) - 1)
      for (let j = 0; j < nb; j++) {
        const x = g.X(V.x0 + (j / nb) * (V.x1 - V.x0))
        const hb = vb[j] > 0 ? Math.ceil(Math.sqrt(vb[j] / (vmax || 1)) * (P.vol / 2 - 3) * g.ky) : 0
        const hs = vs[j] > 0 ? Math.ceil(Math.sqrt(vs[j] / (vmax || 1)) * (P.vol / 2 - 3) * g.ky) : 0
        c2.globalAlpha = 0.8
        c2.fillStyle = dc.up
        if (hb > 0) c2.fillRect(x, g.Y(vm) - hb, cw, hb)
        c2.fillStyle = dc.down
        if (hs > 0) c2.fillRect(x, g.Y(vm) + 1, cw, hs)
        c2.globalAlpha = 1
      }
      g.text(tr('heat.volLane'), xAxis + 8, vm, { size: 11, color: C.t3, baseline: 'middle' })

      // 事件带：疑似行为按时间排，和图共用时间轴
      if (P.ev > 0) {
        g.hline(E.y0, E.x0, xAxis + P.ax, C.rule)
        const my = (E.y0 + E.y1) / 2
        for (const e of shownEv) {
          const x = xOf(e.t)
          glyph(g, e.kind, x, my, e.bid, C.amber)
          hits.push({ x0: x - 5, y0: E.y0, x1: x + 5, y1: E.y1, lines: [evTitle(e), evWhy(e), `${fmtNum(e.p, dec2)}  ${clock(e.t)}`] })
        }
        g.text(tr('heat.evLane'), xAxis + 8, my, { size: 11, color: C.t3, baseline: 'middle' })
      }

      // 价格尺：普通刻度暗，大单亮并列出金额和挂了多久
      const mid = lastMid()
      const curY = yOf(mid)
      const tagGap = 15
      type Tag = { y: number; ty?: number; p: number; text?: string; color?: string; wall?: HeatWall; line?: string[]; prio: number }
      const tags: Tag[] = []
      // 叠加项：视野里画线，金额写在价格尺上；视野外放到图外的带子里，点一下把视野扩过去（D83）
      type Chip = { p: number; text: string; color: string; tip: string[] }
      const chips: Chip[] = []
      const dash = (y: number, color: string) => { for (let xx = P.x0; xx < P.x1; xx += 6) g.hline(y, xx, Math.min(xx + 3, P.x1), color) }
      const pctOf = (p: number) => { const x = (p / Math.max(mid, 1e-9) - 1) * 100; return `${x > 0 ? '+' : '−'}${locDec(Math.abs(x).toFixed(1))}%` }
      const inView = (p: number) => p > v.pa && p < v.pb
      if (ly.own && ownLines) for (const l of ownLines) {
        const color = l.kind === 'liq' ? C.amber : l.buy ? dc.up : dc.down
        if (inView(l.price)) tags.push({ y: yOf(l.price), p: l.price, text: l.label, color, prio: 3 })
        else chips.push({ p: l.price, text: `${l.label} ${fmtNum(l.price, dec2)}`, color, tip: [`${l.label} ${fmtNum(l.price, dec2)} (${pctOf(l.price)})`, tr('heat.clickExpand')] })
      }
      if (ly.chain && oc2?.bins?.length && mid > 0) {
        if (!clMemo || clMemo.bins !== oc2.bins || Math.abs(clMemo.mid / mid - 1) > 0.001) {
          const all = liqClusters(oc2.bins, mid)
          const top = Math.max(0, ...all.map((c) => c.value))
          clMemo = { bins: oc2.bins, mid, out: all.filter((c) => c.value >= top * 0.1) }
        }
        for (const c of clMemo.out) {
          const say = tr(c.side === 'long' ? 'chart.liqLong' : 'chart.liqShort', { v: fmtCompactL(c.value), px: fmtNum(c.mid, c.mid >= 1000 ? 0 : dec2), pct: pctOf(c.mid) })
          if (inView(c.mid)) {
            const y = yOf(c.mid)
            dash(y, C.amber)
            tags.push({ y, p: c.mid, text: tr('heat.tagLiq', { v: fmtCompactL(c.value) }), color: C.amber, line: [say], prio: 2 })
          } else chips.push({ p: c.mid, text: say, color: C.amber, tip: [say, tr('heat.clickExpand')] })
        }
      }
      if (ly.whales && oc2?.whales) for (const x of oc2.whales.slice(0, 8)) {
        if (x.entry <= v.pa || x.entry >= v.pb) {
          const line = tr(x.size > 0 ? 'heat.whaleLong' : 'heat.whaleShort', { v: fmtCompactL(x.value), px: fmtNum(x.entry, dec2) })
          chips.push({ p: x.entry, text: line, color: x.size > 0 ? dc.up : dc.down, tip: [line, pctOf(x.entry), tr('heat.clickExpand')] })
          continue
        }
        const y = yOf(x.entry)
        for (let xx = P.x0; xx < P.x1; xx += 6) g.hline(y, xx, Math.min(xx + 2, P.x1), x.size > 0 ? 'rgba(76,185,138,0.55)' : 'rgba(226,98,92,0.55)')
        tags.push({ y, p: x.entry, text: `${tr(x.size > 0 ? 'heat.long' : 'heat.short')} ${fmtCompactL(x.value)}`, color: x.size > 0 ? dc.up : dc.down, line: [tr(x.size > 0 ? 'heat.whaleLong' : 'heat.whaleShort', { v: fmtCompactL(x.value), px: fmtNum(x.entry, dec2) })], prio: 2 })
      }
      // 大额持仓的动作：小三角落在当时的价格线上
      if (ly.whales && oc2?.actions) {
        for (const ac of oc2.actions.slice(0, 40)) {
          if (ac.t < v.ta || ac.t > v.tb) continue
          const m0 = midAt(ac.t)
          if (!(m0 > v.pa && m0 < v.pb)) continue
          const x = xOf(ac.t), y = yOf(m0), buy = ac.to > ac.from
          const s = g.px(3), X = g.X(x), Y = g.Y(y)
          c2.fillStyle = buy ? dc.up : dc.down
          c2.beginPath()
          c2.moveTo(X, buy ? Y - s : Y + s)
          c2.lineTo(X - s, buy ? Y + s : Y - s)
          c2.lineTo(X + s, buy ? Y + s : Y - s)
          c2.closePath()
          c2.fill()
          const long = ac.to > 0 || (ac.kind === 'close' && ac.from > 0)
          hits.push({ x0: x - 6, y0: y - 6, x1: x + 6, y1: y + 6, lines: [tr('cn.actHint', { kind: tr(`cn.kind.${ac.kind}` as Key), side: tr(long ? 'heat.long' : 'heat.short'), v: fmtCompactL(ac.value), px: fmtNum(ac.price, dec2), n: Math.round(ac.lag / 1000) })] })
        }
      }
      for (const wl of walls) if (wl.p > v.pa && wl.p < v.pb) tags.push({ y: yOf(wl.p), p: wl.p, wall: wl, prio: 1 })
      tags.sort((a, b) => b.prio - a.prio)
      const placed: Tag[] = []
      const curClear = (y: number) => Math.abs(y - curY) >= 18
      // 字写在 ty（横线还是画在真实价位 y）。离现价太近的大单原来直接省掉，可顶上那行"上方大单 / 下方大单"
      // 说的正是离现价最近的那张（11-28 抽查：顶上写 59,807，价格尺上找不到）：字挪到现价标签的外侧，连一小段竖线
      for (const t of tags) {
        const ty = !curClear(t.y) && t.wall ? curY + (t.wall.bid ? 18 : -18) : t.y
        if (curClear(ty) && placed.every((o) => Math.abs((o.ty ?? o.y) - ty) >= tagGap)) placed.push({ ...t, ty })
      }
      valueScale(g, { x0: P.x0, y0: P.y0, x1: xAxis, y1: P.y1 }, v.pa, v.pb, (x) => fmtNum(x, dec2), { grid: false, avoid: [curY, ...placed.map((t) => t.ty ?? t.y), ...(P.ax >= 188 ? [4, 10] : [])] })
      const c1 = xAxis + 8
      const cAmt = xAxis + 112, cAge = xAxis + P.ax - 8
      for (const t0 of placed) {
        const t = { ...t0, y: t0.ty ?? t0.y } // 下面写字、悬停都按字的位置
        g.hline(t0.y, P.x1 - 6, xAxis + 5, t.color ?? 'rgba(255,255,255,0.7)')
        if (t.y !== t0.y) g.vline(xAxis + 4, Math.min(t0.y, t.y), Math.max(t0.y, t.y), t.color ?? 'rgba(255,255,255,0.7)')
        g.text(fmtNum(t.p, dec2), c1, t.y, { size: 11, color: t.color ?? C.t1, baseline: 'middle', weight: 500 })
        if (t.wall) {
          if (P.ax >= 132) g.text(fmtCompactL(t.wall.usd), P.ax >= 188 ? cAmt : cAge, t.y, { size: 11, color: C.t1, baseline: 'middle', align: 'right' })
          if (P.ax >= 188) g.text(ageText(t.wall.age), cAge, t.y, { size: 11, color: C.t3, baseline: 'middle', align: 'right' })
          hits.push({ x0: xAxis, y0: t.y - 7, x1: w, y1: t.y + 7, lines: [
            tr(t.wall.bid ? 'heat.tipWallBid' : 'heat.tipWallAsk', { px: fmtNum(t.wall.p, dec2), v: fmtCompactL(t.wall.usd), pct: `${t.wall.dist > 0 ? '+' : ''}${t.wall.dist}%` }),
            t.wall.touch > 0 ? tr('heat.tipWallTouch', { age: ageText(t.wall.age), n: t.wall.touch }) : tr('heat.tipWallAge', { age: ageText(t.wall.age) }),
          ] })
        } else if (t.text && P.ax >= 132) {
          g.text(t.text, cAge, t.y, { size: 11, color: t.color ?? C.t2, baseline: 'middle', align: 'right' })
          if (t.line) hits.push({ x0: xAxis, y0: t.y - 7, x1: w, y1: t.y + 7, lines: t.line })
        }
      }
      if (P.ax >= 188) {
        g.text(tr('heat.colPx'), c1, 8, { size: 10.5, color: C.t3, baseline: 'middle' })
        g.text(tr('heat.colAmt'), cAmt, 8, { size: 10.5, color: C.t3, baseline: 'middle', align: 'right' })
        g.text(tr('heat.colAge'), cAge, 8, { size: 10.5, color: C.t3, baseline: 'middle', align: 'right' })
      }

      // 视野外的价位：离现价近的贴着价格尺，往左排，放不下的写还有几个
      chipHits = []
      if (P.edge > 0) {
        const tf = { size: 11, weight: 500 }
        g.hline(P.y0, P.x0, P.x1, C.rule)
        g.hline(P.y1, P.x0, P.x1, C.rule)
        for (const up of [true, false]) {
          const list = chips.filter((c) => (up ? c.p > v.pb : c.p < v.pa)).sort((a, b) => (up ? a.p - b.p : b.p - a.p))
          const y = up ? P.y0 / 2 : P.y1 + P.edge / 2
          const arrow = up ? '↑' : '↓'
          let x = P.x1 - 4, more = 0
          for (const c of list) {
            const text = `${arrow} ${c.text}`
            const tw = g.measure(text, tf) + 14
            if (x - tw < P.x0 + 64) { more++; continue }
            g.text(text, x - 7, y, { ...tf, color: c.color, align: 'right', baseline: 'middle', plate: { color: '#111113', padX: 7, padY: 2, border: C.rule } })
            chipHits.push({ x0: x - tw, y0: y - 9, x1: x, y1: y + 9, p: c.p, text })
            hits.push({ x0: x - tw, y0: y - 9, x1: x, y1: y + 9, lines: c.tip })
            x -= tw + 6
          }
          if (more) g.text(`${arrow} ${tr('chart.moreWhales', { n: more })}`, x - 4, y, { ...tf, color: C.t3, align: 'right', baseline: 'middle' })
        }
      }

      // 时间尺。没有数据的时段在尺上加粗一段
      timeScale(g, { x0: P.x0, x1: P.x1, y0: P.y0, y1: E.y1 }, v.ta, v.tb, { grid: false, avoidX: mouse && !drag && mouse.x > P.x0 && mouse.x < P.x1 && mouse.y > P.y0 && mouse.y < P.y1 ? mouse.x : undefined })
      for (const [a, b] of gaps) {
        const x0 = Math.max(P.x0, xOf(a)), x1 = Math.min(P.x1, xOf(b))
        if (x1 > x0) g.rect(x0, E.y1 - 1, x1, E.y1 + 1, C.t3)
      }

      // 当前盘口的剖面：和左边同一套亮度
      if (P.prof > 0) {
        const tn = toneNow()
        const x0 = P.x1, x1 = xAxis
        g.vline(x0, P.y0, P.y1, C.rule2)
        g.vline(x1, P.y0, P.y1, C.rule2)
        const bar = (p: number, cell: number, rowPx: number) => {
          const y = yOf(p)
          if (y < P.y0 || y > P.y1) return
          const l = Math.pow(Math.max(0, Math.min(1, (cell / 255 - tn.black) / Math.max(tn.white - tn.black, 1e-3))), GAMMA)
          const len = l * (x1 - x0 - 6)
          if (len < 0.5) return
          const hh = Math.max(rowPx - (rowPx * g.ky >= 5 ? 1 / g.ky : 0), 1 / g.ky)
          g.rect(x0 + 3, y - hh / 2, x0 + 3 + len, y + hh / 2, `rgba(255,255,255,${(0.3 + 0.7 * l).toFixed(3)})`)
        }
        if (live.n && !replay) {
          const rowPx = (P.y1 - P.y0) / ((v.pb - v.pa) / live.width)
          const a = Math.max(0, Math.floor(v.pa / live.width) - live.base), b = Math.min(live.rows - 1, Math.ceil(v.pb / live.width) - live.base)
          for (let r = a; r <= b; r++) { const c = live.raw[r * LIVE_COLS + live.n - 1]; if (c) bar((live.base + r) * live.width, c, rowPx) }
        } else if (hist && hist.rows) {
          const rowPx = (P.y1 - P.y0) / ((v.pb - v.pa) / hist.step)
          const col = Math.min(hist.cols - 1, Math.max(0, Math.floor(((atRef.current ?? v.tb) - hist.t0) / hist.dt)))
          for (let r = 0; r < hist.rows; r++) { const c = hRaw[r * hist.cols + col]; if (c) bar(hist.p0 + r * hist.step, c, rowPx) }
        }
        // 链上强平价：在剖面里往左长的琥珀横条
        if (ly.chain && oc2?.bins?.length) {
          const vmax2 = Math.max(1, ...oc2.bins.map((b) => Math.max(b.long_value, b.short_value)))
          for (const b of oc2.bins) {
            const u = Math.max(b.long_value, b.short_value)
            const m = (b.lo + b.hi) / 2
            if (u < vmax2 * 0.03 || m < v.pa || m > v.pb) continue
            const y = yOf(m), len = (x1 - x0 - 4) * Math.sqrt(u / vmax2)
            g.rect(x1 - len, y - 1.5, x1, y + 1.5, C.amber)
            const pct = ((m - mid) / Math.max(mid, 1)) * 100
            hits.push({ x0, y0: y - 5, x1, y1: y + 5, lines: [tr(b.long_value >= b.short_value ? 'chart.liqLong' : 'chart.liqShort', { px: fmtNum(m, 0), pct: `${pct > 0 ? '+' : ''}${locDec(pct.toFixed(1))}%`, v: fmtCompactL(u) })] })
          }
        }
      }
      if (mid > 0) {
        g.hline(curY, P.x1, xAxis + 6, C.white)
        cursorTag(g, xAxis + 6, curY, fmtNum(mid, dec2))
      }

      // 十字准线和读数：专业的人要的精确数字
      const m = mouse
      if (m && !drag) {
        const inPlot = m.x > P.x0 && m.x < P.x1 && m.y > P.y0 && m.y < P.y1
        const hit = hits.find((b) => m.x >= b.x0 && m.x <= b.x1 && m.y >= b.y0 && m.y <= b.y1)
        if (inPlot) {
          const t = v.ta + ((m.x - P.x0) / (P.x1 - P.x0)) * (v.tb - v.ta)
          const p = v.pa + (1 - (m.y - P.y0) / (P.y1 - P.y0)) * (v.pb - v.pa)
          dashedV(g, m.x, P.y0, E.y1)
          dashedH(g, m.y, P.x0, P.x1)
          cursorTag(g, xAxis + 6, m.y, fmtNum(p, dec2), { color: C.plate, ink: C.t1 })
          cursorTag(g, m.x, E.y1 + 13, clock(t), { color: C.plate, ink: C.t1 })
          const dot = dots.find((q) => Math.hypot(q.x - m.x, q.y - m.y) <= q.r + 3)
          let lines: string[] = []
          if (hit) lines = hit.lines
          else if (dot) lines = [tr(dot.buy ? 'heat.tipBuy' : 'heat.tipSell', { v: fmtCompactL(dot.u) }), `${fmtNum(dot.p, dec2)}  ${clock(dot.t)}`]
          else {
            const u = cellAt(t, p)
            if (u != null) lines = [u > 0 ? tr(p > midAt(t) ? 'heat.tipAsk' : 'heat.tipBid', { v: fmtCompactL(u) }) : tr('heat.tipNone'), `${fmtNum(p, dec2)}  ${clock(t)}`]
          }
          if (lines.length) tip(g, m.x, m.y, lines, { x0: 0, y0: 0, x1: w, y1: h })
        } else if (hit) {
          tip(g, m.x, m.y, hit.lines, { x0: 0, y0: 0, x1: w, y1: h })
        }
      }
    }

    const midAt = (t: number): number => {
      if (live.n && t >= live.t0 && !replay) return live.mids[Math.min(live.n - 1, Math.floor((t - live.t0) / 100))] || lastMid()
      if (hist) return hist.mids[Math.max(0, Math.min(hist.cols - 1, Math.floor((t - hist.t0) / hist.dt)))] || lastMid()
      return lastMid()
    }
    const cellAt = (t: number, p: number): number | null => {
      if (live.n && t >= live.t0 && t < live.t0 + live.n * 100 && !replay) {
        const r = Math.round(p / live.width) - live.base, c = Math.floor((t - live.t0) / 100)
        if (r < 0 || r >= live.rows) return 0
        return cellUSD(live.raw[r * LIVE_COLS + c])
      }
      if (hist && hist.rows) {
        const c = Math.floor((t - hist.t0) / hist.dt), r = Math.round((p - hist.p0) / hist.step)
        if (c < 0 || c >= hist.cols || r < 0 || r >= hist.rows || !(hist.mids[c] > 0)) return null
        return cellUSD(hRaw[r * hist.cols + c])
      }
      return null
    }

    // ---------- 交互 ----------
    const ro = new ResizeObserver((es2) => {
      const e = es2[0]
      w = e.contentRect.width; h = e.contentRect.height
      const d = e.devicePixelContentBoxSize?.[0]
      W = d ? d.inlineSize : Math.round(w * devicePixelRatio)
      H = d ? d.blockSize : Math.round(h * devicePixelRatio)
      paint(); dirty = false
      if (!hist) void load()
    })
    try { ro.observe(oc, { box: 'device-pixel-content-box' }) } catch { ro.observe(oc) }
    let want = 0
    const loop = () => {
      raf = requestAnimationFrame(loop)
      const now = performance.now(), dt = Math.min(64, now - last)
      last = now
      if (yAuto && now - idleAt < 500 && now - fitAt > 120) { fitAt = now; fitY() }
      if (!drag && vt.pb > vt.pa && approachAll(v, vt, dt, 14, { ta: (vt.tb - vt.ta) * 2e-4, tb: (vt.tb - vt.ta) * 2e-4, pa: (vt.pb - vt.pa) * 2e-4, pb: (vt.pb - vt.pa) * 2e-4 })) dirty = true
      if (drag && !drag.axis && vt.pb > vt.pa) { // 左右拖时价格范围还是要平滑地跟上
        const yv = { pa: v.pa, pb: v.pb }
        if (approachAll(yv, { pa: vt.pa, pb: vt.pb }, dt, 14, (vt.pb - vt.pa) * 2e-4)) dirty = true
        v.pa = yv.pa; v.pb = yv.pb
      }
      if (dirty) { paint(); dirty = false }
      if (!loading && hist && !replay && now > want) {
        // 快滑到手里数据的边上：边滑边取，两个方向都要
        const sp = vt.tb - vt.ta
        const t1 = hist.t0 + hist.cols * hist.dt
        const nearL = vt.ta - sp * 0.3 < hist.t0 && (!hist.since || hist.t0 > hist.since)
        const nearR = !follow && vt.tb + sp * 0.3 > t1 && t1 < Date.now() - 60_000
        // 视图停下来 300 毫秒后：分辨率不够就重新取；跟随最新时每分钟取一次，接上实时那一层
        let idleNeed = false
        if (now - idleAt > 300) {
          const P = P0()
          const needP = !yAuto && vt.pb > vt.pa && hist.rows > 0 && (vt.pa < hist.p0 || vt.pb > hist.p0 + hist.rows * hist.step)
          const dtPx = sp / Math.max(1, P.x1 - P.x0)
          const fineT = hist.dt > 1000 && hist.dt > dtPx * 3
          const pxP = (vt.pb - vt.pa) / Math.max(1, P.y1 - P.y0)
          const fineP = !yAuto && hist.step > (live.width || 0) && hist.step > pxP * 3
          idleNeed = needP || fineT || fineP || (follow && Date.now() - loadedAt > 60_000)
        }
        if (nearL || nearR || idleNeed) { want = now + 700; void load() }
      }
    }
    raf = requestAnimationFrame(loop)

    dirtyRef.current = () => { dirty = true }
    spanRef.current = (s: number) => {
      if (replay) return
      const anchor = follow ? Date.now() : (vt.ta + vt.tb) / 2
      if (follow) { vt.tb = anchor + s * 0.02; vt.ta = vt.tb - s } else { vt.ta = anchor - s / 2; vt.tb = anchor + s / 2 }
      clampX()
      idleAt = performance.now()
      dirty = true
    }
    floorRef.current = (val: number) => {
      floorUSD = val
      uploadHist()
      tellScale()
      dirty = true
    }

    const pos = (e: MouseEvent) => { const r = oc.getBoundingClientRect(); return { x: e.clientX - r.left, y: e.clientY - r.top } }
    const onWheel = (e: WheelEvent) => {
      e.preventDefault()
      const m = pos(e), P = P0()
      const f = Math.exp(Math.max(-1, Math.min(1, e.deltaY * (e.deltaMode === 1 ? 0.05 : 0.0018))))
      if (e.shiftKey || m.x > P.x1) {
        yAuto = false
        const p = vt.pa + (1 - (m.y - P.y0) / (P.y1 - P.y0)) * (vt.pb - vt.pa)
        const sp = Math.min(Math.max((vt.pb - vt.pa) * f, (live.width || 1) * 12), (vt.pa + vt.pb) * 0.04)
        const a = (p - vt.pa) / (vt.pb - vt.pa)
        vt.pa = p - a * sp; vt.pb = vt.pa + sp
      } else {
        const t = vt.ta + ((m.x - P.x0) / (P.x1 - P.x0)) * (vt.tb - vt.ta)
        const sp = Math.min(Math.max((vt.tb - vt.ta) * f, 10_000), 24 * 3600_000)
        const a = follow ? 0.98 : (t - vt.ta) / (vt.tb - vt.ta)
        const c = follow ? vt.tb : t
        vt.ta = c - a * sp; vt.tb = vt.ta + sp
        clampX()
      }
      idleAt = performance.now()
      dirty = true
    }
    const onDown = (e: PointerEvent) => {
      const m = pos(e), P = P0()
      mouse = m
      Object.assign(vt, { ta: v.ta, tb: v.tb })
      if (!yAuto) Object.assign(vt, { pa: v.pa, pb: v.pb })
      drag = { ...m, v: { ...v }, axis: m.x > P.x1 && m.y < P.y1 }
      moved = false
      oc.setPointerCapture(e.pointerId)
    }
    const onMove = (e: PointerEvent) => {
      mouse = pos(e)
      const P = P0()
      if (drag) {
        if (Math.abs(mouse.x - drag.x) > 3 || Math.abs(mouse.y - drag.y) > 3) moved = true
        if (drag.axis) {
          // 在价格尺上拖：上下移动。这才算手动调过范围
          if (moved) yAuto = false
          const dy = ((mouse.y - drag.y) / (P.y1 - P.y0)) * (drag.v.pb - drag.v.pa)
          v.pa = vt.pa = drag.v.pa + dy
          v.pb = vt.pb = drag.v.pb + dy
        } else {
          // 在图上拖：只左右移动，不上下漂
          const dx = ((mouse.x - drag.x) / (P.x1 - P.x0)) * (drag.v.tb - drag.v.ta)
          if (Math.abs(mouse.x - drag.x) > 3 && !replay) follow = false
          vt.ta = drag.v.ta - dx; vt.tb = drag.v.tb - dx
          clampX()
          v.ta = vt.ta; v.tb = vt.tb
        }
        idleAt = performance.now()
      } else {
        const m = mouse
        oc.style.cursor = chipHits.some((c) => m.x >= c.x0 && m.x <= c.x1 && m.y >= c.y0 && m.y <= c.y1) ? 'pointer' : m.x > P.x1 && m.y < P.y1 ? 'ns-resize' : ''
      }
      dirty = true
    }
    const onUp = () => {
      if (drag && !moved) {
        const m = drag // 按下的位置：手机上点一下之前没有移动，不能用上一次的鼠标位置
        const c = chipHits.find((q) => m.x >= q.x0 && m.x <= q.x1 && m.y >= q.y0 && m.y <= q.y1)
        if (c) expandTo(c.p)
      }
      // 往新的方向滑回了最新的位置：接着跟随。往历史方向拖不算，哪怕数据短、拖不动
      if (drag && !drag.axis && moved && !replay && mouse && mouse.x < drag.x && vt.tb >= Date.now()) follow = true
      drag = null; dirty = true
      idleAt = performance.now()
    }
    const onLeave = () => { mouse = null; dirty = true }
    const goAuto = () => {
      yAuto = true
      fitY()
      idleAt = performance.now()
      dirty = true
    }
    const onDbl = () => {
      if (replay) return
      const sp = vt.tb - vt.ta
      follow = true
      vt.tb = Date.now() + sp * 0.02
      vt.ta = vt.tb - sp
      goAuto()
    }
    goLatestRef.current = onDbl
    goAutoRef.current = goAuto
    oc.addEventListener('wheel', onWheel, { passive: false })
    oc.addEventListener('pointerdown', onDown)
    oc.addEventListener('pointermove', onMove)
    oc.addEventListener('pointerup', onUp)
    oc.addEventListener('pointerleave', onLeave)
    oc.addEventListener('dblclick', onDbl)
    connect()
    // 给检查脚本读
    const debug = { get chips() { return chipHits }, get yAuto() { return yAuto }, get histEnd() { return hist ? hist.t0 + hist.cols * hist.dt : 0 }, get histStart() { return hist?.t0 ?? 0 }, get frames() { return live.n }, get rows() { return hist?.rows ?? 0 }, get dt() { return hist?.dt ?? 0 }, get events() { return events.size }, get gaps() { return hist?.gaps ?? [] }, get view() { return { ...v } }, get follow() { return follow }, get walls() { return walls }, get tone() { return toneNow() } }
    ;(oc as unknown as { __heat: unknown }).__heat = debug
    return () => {
      disposed = true
      clearTimeout(retry)
      es?.close()
      cancelAnimationFrame(raf); ro.disconnect()
      oc.removeEventListener('wheel', onWheel)
      oc.removeEventListener('pointerdown', onDown)
      oc.removeEventListener('pointermove', onMove)
      oc.removeEventListener('pointerup', onUp)
      oc.removeEventListener('pointerleave', onLeave)
      oc.removeEventListener('dblclick', onDbl)
    }
  }, [inst, replay?.from, replay?.to]) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="surface">
      <canvas ref={glc} />
      <canvas ref={ovc} className="interactive heat-canvas" />
      {!replay && (
        <div ref={ctlBox} className="heat-ctl">
          <button ref={autoBtn} className="scope-latest" onClick={() => goAutoRef.current()} title={tr('heat.autoYHint')}>{tr('heat.autoY')}</button>
          <button ref={latestBtn} className="scope-latest" onClick={() => goLatestRef.current()} title={tr('chart.latestHint')}>
            {tr('chart.latest')}
            <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true"><path d="M2 2l3 3-3 3M6 2v6" fill="none" stroke="currentColor" strokeWidth="1.2" /></svg>
          </button>
        </div>
      )}
    </div>
  )
}

function tip(g: G, x: number, y: number, lines: string[], P: Box) {
  const wMax = Math.max(...lines.map((s) => g.measure(s, { size: 12, weight: 500 })))
  const bw = Math.min(wMax + 20, P.x1 - P.x0 - 8), bh = lines.length * 18 + 12
  let bx = x + 16, by = y - bh - 10
  if (bx + bw > P.x1 - 4) bx = x - 16 - bw
  if (bx < P.x0 + 4) bx = P.x0 + 4
  if (by < P.y0 + 4) by = y + 16
  g.rect(bx, by, bx + bw, by + bh, 'rgba(17,17,19,0.97)')
  g.hline(by, bx, bx + bw, C.rule2); g.hline(by + bh - 1 / g.ky, bx, bx + bw, C.rule2)
  g.vline(bx, by, by + bh, C.rule2); g.vline(bx + bw - 1 / g.kx, by, by + bh, C.rule2)
  lines.forEach((s, i) => g.text(s, bx + 10, by + 15 + i * 18, { size: 12, weight: i ? 420 : 500, color: i ? C.t2 : C.t1, baseline: 'middle' }))
}
