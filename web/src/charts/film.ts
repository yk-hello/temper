// 高光卡片和视频（D91）：这笔交易自己的 K 线，从开仓到平仓。9:16 竖屏，2D。
// 视频约 8 秒：K 线一根根长出来，回报数字跟着真实走势跳（先跌后涨就先变红），平仓那根落下时盖"平仓"，
// 金光扫过金色区域（开仓价和价格之间"从市场拿下的那一块"），最后收成卡片。卡片就是视频最后一帧，截图和下载的一模一样。
// 全程写"模拟盘 · 几倍"（宪法第 17 条）；演示全程写"演示"。卡片上只写比例，不写金额。
import { ArrayBufferTarget, Muxer } from 'mp4-muxer'
import { SR, synth, type SfxPlan } from '../lib/sfx'
import { dirColors } from './scale'

export const DURATION = 8
const T_IN = 0.6 // K 线开始长
const T_GROW = 4.0 // 长多久
const T_EXIT = T_IN + T_GROW // 平仓那根落下
const T_SWEEP = T_EXIT + 0.55
const T_TABLE = T_EXIT + 1.35
export const T_END = 6.8 // 之后就是卡片，一动不动

const GOLD = '#d4b37a'
const GOLD_HI = '#f1dcae'
const T1 = '#ececee'
const T2 = '#a3a3aa'
const T3 = '#6e6e76'
const FONT = "'Geist Variable', 'Noto Sans SC', 'PingFang SC', 'Microsoft YaHei UI', sans-serif"
const MONO = "'Geist Mono Variable', 'Geist Variable', ui-monospace, monospace"

const clamp01 = (x: number) => Math.max(0, Math.min(1, x))
const easeOut = (x: number) => 1 - Math.pow(1 - clamp01(x), 3)
const easeIO = (x: number) => { const k = clamp01(x); return k * k * (3 - 2 * k) }
const back = (x: number) => { const k = clamp01(x), s = 1.7; return 1 + (s + 1) * Math.pow(k - 1, 3) + s * Math.pow(k - 1, 2) }

export interface Candle { t: number; o: number; h: number; l: number; c: number }

/** 一笔交易：开平仓、方向、杠杆、回报。K 线是从开仓到平仓的那一段 */
export interface FilmTrade {
  candles: Candle[]
  iv: number // K 线周期（毫秒）
  side: 'long' | 'short'
  leverage: number
  entry: number
  exit: number
  openTime: number
  closeTime: number
  roe: number // 净回报（按保证金），最后停在这个数
  qty?: number // 数量（币）：回放时按它算盈亏金额
  net?: number // 净盈亏（USDT，扣了手续费和资金费）：平仓后停在这个数。示例没有
  mae?: number // 按保证金，≤ 0；没有就按 K 线算
  mfe?: number
  events: { t: number; text: string }[]
}

/** 画面上的字，界面那边按语言准备好 */
export interface FilmText {
  title: string // BTC 永续 · 做多
  badge: string // 模拟盘 · 10×
  demo: string | null // 演示
  basis: string // 按保证金 · 占账户 +0.8%
  entry: string // 开
  exit: string // 平
  stamp: string // 平仓
  mae: string // 最深
  mfe: string // 最高
  labels: { hold: string; price: string; now: string; mae: string; mfe: string } // 下面四行的名字：持仓、开仓 → 平仓（平仓前是开仓 → 现价）、最深浮亏、最高浮盈
  hold: (ms: number) => string // 45 分钟
  date: string
  foot: string // 模拟盘成交，不是真实资金
  pct: (v: number) => string // +8.3%
  money: (v: number) => string // +1,234.56 USDT（D96：高光照常显示金额）
  price: (v: number) => string
  time: (t: number) => string // 09:32
}

interface Geo {
  W: number; H: number; k: number; m: number
  top: number; bot: number; left: number; right: number
  lo: number; hi: number
  x: (i: number) => number
  y: (p: number) => number
  cw: number
  maeI: number; mfeI: number
  mae: number; mfe: number
  sign: number
}

function geo(tr: FilmTrade, W: number, H: number): Geo {
  const k = W / 1080, m = 72 * k
  const top = 560 * k, bot = 1290 * k, left = m, right = W - m - 150 * k
  let lo = Math.min(tr.entry, tr.exit), hi = Math.max(tr.entry, tr.exit)
  for (const c of tr.candles) { lo = Math.min(lo, c.l); hi = Math.max(hi, c.h) }
  const pad = (hi - lo) * 0.14 || hi * 0.001
  lo -= pad; hi += pad
  const n = Math.max(1, tr.candles.length)
  const x = (i: number) => left + ((i + 0.5) / n) * (right - left)
  const y = (p: number) => bot - ((p - lo) / (hi - lo)) * (bot - top)
  const sign = tr.side === 'long' ? 1 : -1
  // 最深浮亏、最高浮盈落在哪一根：多单看最低、最高；空单反过来
  let maeI = 0, mfeI = 0
  tr.candles.forEach((c, i) => {
    const a = tr.candles[maeI], b = tr.candles[mfeI]
    if (sign > 0 ? c.l < a.l : c.h > a.h) maeI = i
    if (sign > 0 ? c.h > b.h : c.l < b.l) mfeI = i
  })
  const cMae = tr.candles[maeI], cMfe = tr.candles[mfeI]
  const lev = Math.max(1, tr.leverage)
  const mae = tr.mae ?? (cMae ? Math.min(0, sign * ((sign > 0 ? cMae.l : cMae.h) / tr.entry - 1) * lev) : 0)
  const mfe = tr.mfe ?? (cMfe ? Math.max(0, sign * ((sign > 0 ? cMfe.h : cMfe.l) / tr.entry - 1) * lev) : 0)
  // K 线宽度：根数少时不能宽成一块板
  return { W, H, k, m, top, bot, left, right, lo, hi, x, y, cw: Math.max(2 * k, Math.min(44 * k, ((right - left) / n) * 0.58)), maeI, mfeI, mae, mfe, sign }
}

/** 画一帧。t ≥ T_END 就是卡片（drawCard 也调这个） */
export function paint(g: CanvasRenderingContext2D, tr: FilmTrade, tx: FilmText, t: number) {
  const W = g.canvas.width, H = g.canvas.height
  const G = geo(tr, W, H)
  const { up: UP, down: DOWN } = dirColors() // 跟用户的红涨绿跌设置走
  const { k, m } = G
  const n = tr.candles.length
  g.save()
  g.fillStyle = '#050506'
  g.fillRect(0, 0, W, H)
  // 平仓那一下，画面抖两三下
  const shake = t > T_EXIT && t < T_EXIT + 0.18 ? Math.sin((t - T_EXIT) * 90) * 4 * k * (1 - (t - T_EXIT) / 0.18) : 0
  g.translate(shake, 0)
  g.textBaseline = 'alphabetic'

  // 顶上一行：品种、方向；模拟盘、杠杆（全程都在）
  const head = easeOut(t / 0.4)
  g.globalAlpha = head
  g.font = `400 ${34 * k}px ${FONT}`
  g.fillStyle = T2
  g.textAlign = 'left'
  g.fillText(tx.title, m, 110 * k)
  g.textAlign = 'right'
  g.fillText(tx.badge, W - m, 110 * k)
  if (tx.demo) { // 演示：右上角杠杆左边一个金框标签，全程都在
    g.font = `400 ${34 * k}px ${FONT}`
    const bw = g.measureText(tx.badge).width
    g.font = `600 ${28 * k}px ${FONT}`
    const tw = g.measureText(tx.demo).width
    const px = W - m - bw - tw - 52 * k
    g.textAlign = 'left'
    g.strokeStyle = GOLD; g.lineWidth = 2 * k
    g.strokeRect(px, 76 * k, tw + 28 * k, 46 * k)
    g.fillStyle = GOLD
    g.fillText(tx.demo, px + 14 * k, 109 * k)
  }

  // K 线长到第几根（可以是小数：最后一根正在长）
  const grow = n ? clamp01((t - T_IN) / T_GROW) * n : 0
  const shown = Math.min(n, Math.ceil(grow))
  const lastK = shown ? Math.min(1, grow - (shown - 1)) : 0
  const lev = Math.max(1, tr.leverage)
  const cur = shown ? tr.candles[shown - 1] : null
  const curPx = cur ? cur.o + (cur.c - cur.o) * easeOut(lastK * 1.6) : tr.entry
  const live = G.sign * (curPx / tr.entry - 1) * lev
  const done = t >= T_EXIT

  // 回报数字：跟着走势跳，平仓后停在净回报
  const num = done ? tr.roe : t < T_IN ? 0 : live
  const pop = done ? back((t - T_EXIT) / 0.35) : 1
  g.globalAlpha = head
  g.textAlign = 'left'
  g.save()
  g.translate(m, 330 * k)
  g.scale(pop, pop)
  g.font = `600 ${190 * k}px ${FONT}`
  g.fillStyle = num < 0 ? DOWN : T1
  g.fillText(tx.pct(num), 0, 0)
  g.restore()
  g.font = `400 ${32 * k}px ${FONT}`
  g.fillStyle = T3
  // 金额跟着回报一起跳（按数量算毛盈亏），平仓后停在净盈亏；示例没有数量，只写按什么算
  const amt = tr.net === undefined || !tr.qty ? null : done ? tr.net : t < T_IN ? 0 : G.sign * (curPx - tr.entry) * tr.qty
  g.fillText(amt === null ? tx.basis : `${tx.money(amt)}  ·  ${tx.basis}`, m + 4 * k, 392 * k)
  // 一条金色细线：平仓后出现
  const lineK = easeOut((t - T_EXIT - 0.1) / 0.5)
  if (lineK > 0) {
    g.strokeStyle = GOLD; g.lineWidth = 3 * k
    g.beginPath(); g.moveTo(m + 4 * k, 432 * k); g.lineTo(m + 4 * k + 200 * k * lineK, 432 * k); g.stroke()
  }

  // 网格
  g.globalAlpha = head
  g.strokeStyle = 'rgba(255,255,255,0.06)'; g.lineWidth = Math.max(1, 1.2 * k)
  for (let i = 0; i <= 4; i++) { const yy = G.top + ((G.bot - G.top) * i) / 4; g.beginPath(); g.moveTo(G.left, yy); g.lineTo(G.right, yy); g.stroke() }

  // 开仓价虚线：先画出来
  const ey = G.y(tr.entry)
  const lineIn = easeOut((t - 0.15) / 0.45)
  g.setLineDash([12 * k, 10 * k])
  g.strokeStyle = 'rgba(255,255,255,0.5)'; g.lineWidth = 2 * k
  g.beginPath(); g.moveTo(G.left, ey); g.lineTo(G.left + (G.right - G.left) * lineIn, ey); g.stroke()
  g.setLineDash([])

  // 金色区域：开仓价和收盘价之间赚钱的那一侧
  if (shown) {
    const pts: [number, number][] = []
    for (let i = 0; i < shown; i++) {
      const c = i === shown - 1 ? curPx : tr.candles[i].c
      const v = G.sign > 0 ? Math.max(c, tr.entry) : Math.min(c, tr.entry)
      pts.push([G.x(i), G.y(v)])
    }
    const sweep = clamp01((t - T_SWEEP) / 0.75)
    g.beginPath(); g.moveTo(G.x(0), ey)
    for (const [px, py] of pts) g.lineTo(px, py)
    g.lineTo(pts[pts.length - 1][0], ey); g.closePath()
    g.fillStyle = `rgba(212,179,122,${0.2 + (done ? 0.06 : 0)})`
    g.fill()
    // 金光扫过：一道斜着的亮带，只在金色区域里
    if (sweep > 0 && sweep < 1) {
      g.save(); g.clip()
      const cx = G.left - 300 * k + (G.right - G.left + 600 * k) * easeIO(sweep)
      const grad = g.createLinearGradient(cx - 220 * k, G.top, cx + 220 * k, G.bot)
      grad.addColorStop(0, 'rgba(241,220,174,0)'); grad.addColorStop(0.5, 'rgba(241,220,174,0.75)'); grad.addColorStop(1, 'rgba(241,220,174,0)')
      g.fillStyle = grad
      g.fillRect(G.left, G.top, G.right - G.left, G.bot - G.top)
      g.restore()
    }
  }

  // K 线
  for (let i = 0; i < shown; i++) {
    const c = tr.candles[i]
    const part = i === shown - 1 ? easeOut(lastK * 1.6) : 1
    const close = c.o + (c.c - c.o) * part
    const hi = c.o + (c.h - c.o) * part, lo = c.o + (c.l - c.o) * part
    const up = close >= c.o
    g.strokeStyle = up ? UP : DOWN; g.fillStyle = up ? UP : DOWN
    g.lineWidth = Math.max(1, 2 * k)
    g.beginPath(); g.moveTo(G.x(i), G.y(Math.max(hi, c.o, close))); g.lineTo(G.x(i), G.y(Math.min(lo, c.o, close))); g.stroke()
    const y1 = G.y(Math.max(c.o, close)), y2 = G.y(Math.min(c.o, close))
    g.fillRect(G.x(i) - G.cw / 2, y1, G.cw, Math.max(2 * k, y2 - y1))
  }

  // 右边的价格：开仓价、平仓价
  g.font = `400 ${26 * k}px ${MONO}`
  g.textAlign = 'left'
  g.fillStyle = T2
  g.globalAlpha = head * lineIn
  g.fillText(tx.price(tr.entry), G.right + 16 * k, ey + 9 * k)

  // 最深浮亏、最高浮盈：那一根长出来时标出来
  const mark = (i: number, v: number, label: string, color: string, below: boolean) => {
    if (i >= shown || (i === shown - 1 && lastK < 1)) return
    const c = tr.candles[i]
    const at = T_IN + ((i + 1) / n) * T_GROW
    const a = easeOut((t - at) / 0.3)
    const py = G.y(below ? (G.sign > 0 ? c.l : c.h) : (G.sign > 0 ? c.h : c.l))
    g.globalAlpha = a
    g.font = `500 ${28 * k}px ${FONT}`
    g.fillStyle = color
    const s = `${label} ${tx.pct(v)}`
    const tw = g.measureText(s).width
    const lx = Math.max(G.left, Math.min(G.right - tw, G.x(i) - tw / 2))
    const dy = (below ? 1 : -1) * (G.sign > 0 ? 1 : -1)
    g.fillText(s, lx, py + dy * 44 * k + (dy > 0 ? 10 * k : 0))
  }
  if (G.mae < -0.0005) mark(G.maeI, G.mae, tx.mae, DOWN, true)
  if (G.mfe > 0.0005) mark(G.mfeI, G.mfe, tx.mfe, GOLD_HI, false)

  // 开仓点、平仓点
  g.globalAlpha = 1
  const dot = (px: number, py: number, color: string, label: string, right: boolean, a: number) => {
    if (a <= 0) return
    g.globalAlpha = a
    g.fillStyle = color
    g.beginPath(); g.arc(px, py, 10 * k, 0, Math.PI * 2); g.fill()
    g.font = `500 ${28 * k}px ${FONT}`
    g.textAlign = right ? 'left' : 'right'
    g.fillText(label, px + (right ? 20 * k : -20 * k), py + 44 * k) // 字放在点的下面：上面是 K 线
    g.textAlign = 'left'
  }
  dot(G.x(0), ey, '#ffffff', tx.entry, true, easeOut((t - 0.35) / 0.3))
  if (done && n) {
    const xy = G.y(tr.exit)
    dot(G.x(n - 1), xy, GOLD_HI, tx.exit, false, easeOut((t - T_EXIT) / 0.2))
    g.globalAlpha = 1
    g.font = `400 ${26 * k}px ${MONO}`
    g.fillStyle = GOLD
    g.fillText(tx.price(tr.exit), G.right + 16 * k, xy + 9 * k)
    // 平仓印：盖下来，过一会儿淡掉
    const sa = easeOut((t - T_EXIT) / 0.12) * (1 - easeIO((t - T_TABLE - 0.1) / 0.4))
    if (sa > 0) {
      const sc = 1 + 0.5 * (1 - easeOut((t - T_EXIT) / 0.16))
      g.save()
      g.globalAlpha = sa
      g.translate(G.x(n - 1) - 150 * k, (G.top + G.bot) / 2)
      g.rotate(-0.1)
      g.scale(sc, sc)
      g.strokeStyle = GOLD; g.lineWidth = 5 * k
      g.strokeRect(-110 * k, -46 * k, 220 * k, 92 * k)
      g.fillStyle = GOLD
      g.font = `600 ${52 * k}px ${FONT}`
      g.textAlign = 'center'
      g.fillText(tx.stamp, 0, 18 * k)
      g.restore()
    }
  }

  // 时间轴：开始、结束；持仓期间的事件
  g.globalAlpha = head * lineIn
  g.font = `400 ${26 * k}px ${MONO}`
  g.fillStyle = T3
  g.textAlign = 'left'
  g.fillText(tx.time(tr.openTime), G.left, G.bot + 50 * k)
  if (done) { g.textAlign = 'right'; g.fillText(tx.time(tr.closeTime), G.right, G.bot + 50 * k) }
  const span = Math.max(1, tr.closeTime - tr.openTime)
  let row = 0
  for (const e of tr.events) {
    const f = (e.t - tr.openTime) / span
    if (f < 0 || f > 1 || grow / Math.max(1, n) < f) continue
    const ex = G.left + f * (G.right - G.left)
    const at = T_IN + f * T_GROW
    g.globalAlpha = easeOut((t - at) / 0.3)
    g.strokeStyle = GOLD; g.lineWidth = 2 * k
    g.beginPath(); g.moveTo(ex, G.bot + 8 * k); g.lineTo(ex, G.bot + 22 * k); g.stroke()
    g.font = `400 ${24 * k}px ${FONT}`
    g.fillStyle = T2
    g.textAlign = ex > W / 2 ? 'right' : 'left'
    g.fillText(e.text, ex, G.bot + 90 * k + row * 34 * k)
    row = (row + 1) % 3
  }

  // 下面四行：K 线开始长时出现，跟着走势实时跳（持仓时长、现价、最深浮亏、最高浮盈），平仓时定格成最终值
  const span2 = Math.max(1, tr.closeTime - tr.openTime)
  const curT = done ? tr.closeTime : t < T_IN ? tr.openTime : tr.openTime + span2 * Math.min(1, grow / Math.max(1, n))
  let runMae = 0, runMfe = 0
  for (let i = 0; i < shown; i++) {
    const c = tr.candles[i]
    const part = i === shown - 1 ? easeOut(lastK * 1.6) : 1
    const hi = c.o + (c.h - c.o) * part, lo = c.o + (c.l - c.o) * part
    runMae = Math.min(runMae, G.sign * ((G.sign > 0 ? lo : hi) / tr.entry - 1) * lev)
    runMfe = Math.max(runMfe, G.sign * ((G.sign > 0 ? hi : lo) / tr.entry - 1) * lev)
  }
  const small = (v: number) => (Math.abs(v) < 0.0005 ? '0.0%' : tx.pct(v))
  const rows: [string, string][] = [
    [tx.labels.hold, `${tx.time(tr.openTime)} → ${tx.time(curT)} · ${tx.hold(curT - tr.openTime)}`],
    [done ? tx.labels.price : tx.labels.now, `${tx.price(tr.entry)} → ${tx.price(done ? tr.exit : curPx)}`],
    [tx.labels.mae, small(done ? G.mae : runMae)],
    [tx.labels.mfe, small(done ? G.mfe : runMfe)],
  ]
  const ra = easeOut((t - T_IN + 0.2) / 0.5)
  rows.forEach(([a, b], i) => {
    if (ra <= 0) return
    const yy = 1500 * k + i * 78 * k
    g.globalAlpha = ra
    g.strokeStyle = 'rgba(255,255,255,0.12)'; g.lineWidth = Math.max(1, 1.5 * k)
    g.beginPath(); g.moveTo(m, yy - 50 * k); g.lineTo(W - m, yy - 50 * k); g.stroke()
    g.font = `400 ${30 * k}px ${FONT}`
    g.fillStyle = T2
    g.textAlign = 'left'
    g.fillText(a, m, yy)
    g.fillStyle = i === 2 && !done && runMae < -0.0005 ? DOWN : T1
    g.textAlign = 'right'
    g.fillText(b, W - m, yy)
  })
  if (ra > 0) {
    g.globalAlpha = ra
    g.beginPath(); g.moveTo(m, 1500 * k + rows.length * 78 * k - 50 * k); g.lineTo(W - m, 1500 * k + rows.length * 78 * k - 50 * k); g.stroke()
  }
  const fa = easeOut((t - T_TABLE - 0.4) / 0.4)
  if (fa > 0) {
    g.globalAlpha = fa
    g.font = `400 ${26 * k}px ${FONT}`
    g.fillStyle = T3
    g.textAlign = 'left'
    g.fillText(tx.date, m, H - 80 * k)
    g.textAlign = 'right'
    g.fillText(tx.foot, W - m, H - 80 * k)
  }
  g.restore()
}

/** 卡片：视频的最后一帧 */
export function drawCard(c: HTMLCanvasElement, tr: FilmTrade, tx: FilmText) {
  paint(c.getContext('2d')!, tr, tx, DURATION)
}

/** 音效的时间点：每根 K 线一声（音高跟着价格），最深处一声低音，平仓、金光、表格 */
export function sfxPlan(tr: FilmTrade): SfxPlan {
  const n = tr.candles.length
  let lo = Infinity, hi = -Infinity
  for (const c of tr.candles) { lo = Math.min(lo, c.c); hi = Math.max(hi, c.c) }
  const sign = tr.side === 'long' ? 1 : -1
  const G = geo(tr, 1080, 1920)
  return {
    entry: 0.38,
    candles: tr.candles.map((c, i) => ({ t: T_IN + ((i + 1) / n) * T_GROW, k: hi > lo ? (sign > 0 ? (c.c - lo) / (hi - lo) : (hi - c.c) / (hi - lo)) : 0.5 })),
    low: G.mae < -0.0005 ? T_IN + ((G.maeI + 1) / n) * T_GROW : null,
    exit: T_EXIT,
    sweep: T_SWEEP,
    rows: T_TABLE,
  }
}

// ---------- 编码 ----------

export interface Quality { w: number; h: number; fps: number; label: '4k' | '1080p' }
export const Q4K: Quality = { w: 2160, h: 3840, fps: 60, label: '4k' }
export const Q1080: Quality = { w: 1080, h: 1920, fps: 60, label: '1080p' }

async function videoConfig(q: Quality): Promise<VideoEncoderConfig | null> {
  if (typeof VideoEncoder === 'undefined') return null
  const bitrate = Math.round(q.w * q.h * q.fps * 0.06)
  for (const codec of ['avc1.640034', 'avc1.640033', 'avc1.64002a', 'avc1.4d0034', 'avc1.42e034']) {
    const c: VideoEncoderConfig = { codec, width: q.w, height: q.h, bitrate, framerate: q.fps, avc: { format: 'avc' } }
    try {
      const s = await VideoEncoder.isConfigSupported(c)
      if (s.supported) return c
    } catch { /* 下一个 */ }
  }
  return null
}

async function audioConfig(): Promise<{ cfg: AudioEncoderConfig; codec: 'aac' | 'opus' } | null> {
  if (typeof AudioEncoder === 'undefined') return null
  for (const [codec, kind] of [['mp4a.40.2', 'aac'], ['opus', 'opus']] as [string, 'aac' | 'opus'][]) {
    const cfg: AudioEncoderConfig = { codec, sampleRate: SR, numberOfChannels: 2, bitrate: 192_000 }
    try {
      const s = await AudioEncoder.isConfigSupported(cfg)
      if (s.supported) return { cfg, codec: kind }
    } catch { /* 下一个 */ }
  }
  return null
}

// 让出主线程：不用定时器。页面在后台（用户切到别的标签页）时浏览器把定时器限到一分钟一次，生成会卡住；
// MessageChannel 的消息不受这个限制
const yieldNow = () => new Promise<void>((r) => { const c = new MessageChannel(); c.port1.onmessage = () => r(); c.port2.postMessage(0) })

/** 等编码器队列降下来。有"出队"事件就等事件，但有的浏览器（无头模式下实测）事件不来，所以同时每 15 毫秒自己看一眼；
 *  看的时候用 MessageChannel 让出，不用定时器（后台标签页里定时器被限流） */
// 等编码队列降到 max 以下。点了取消就不等了：慢电脑上一帧要编好几秒，排着的几帧编完才响应取消，像卡死
async function drained(ve: VideoEncoder, max: number, signal?: AbortSignal) {
  const ev = ve as unknown as EventTarget
  while (ve.encodeQueueSize > max && !signal?.aborted) {
    await new Promise<void>((r) => {
      let done = false
      const fin = () => { if (!done) { done = true; ev.removeEventListener('dequeue', fin); r() } }
      ev.addEventListener('dequeue', fin)
      const t0 = performance.now()
      const spin = () => { if (done) return; if (performance.now() - t0 > 15) fin(); else void yieldNow().then(spin) }
      spin()
    })
  }
}

/** 这台浏览器能不能离线编码这个画质 */
export async function canEncode(q: Quality): Promise<boolean> { return (await videoConfig(q)) !== null }

export interface Rendered { blob: Blob; mime: string; ext: 'mp4' | 'webm'; w: number; h: number; offline: boolean }

/** 逐帧画好再编码成 MP4。不支持离线编码的浏览器退回实时录制（要等 8 秒，质量低一些）。sound = false 导出不带声音的版本 */
export async function renderVideo(tr: FilmTrade, tx: FilmText, q: Quality, sound: boolean, onProgress: (p: number) => void, signal?: AbortSignal): Promise<Rendered> {
  const vc = await videoConfig(q)
  const cv = document.createElement('canvas')
  cv.width = vc ? q.w : Q1080.w; cv.height = vc ? q.h : Q1080.h
  const g = cv.getContext('2d', { alpha: false })!
  if (!vc) return recordRealtime(cv, g, tr, tx, sound, onProgress, signal)
  const ac = sound ? await audioConfig() : null
  const target = new ArrayBufferTarget()
  const muxer = new Muxer({
    target,
    video: { codec: 'avc', width: q.w, height: q.h, frameRate: q.fps },
    audio: ac ? { codec: ac.codec, numberOfChannels: 2, sampleRate: SR } : undefined,
    fastStart: 'in-memory',
    firstTimestampBehavior: 'offset',
  })
  let failed: unknown = null
  const ve = new VideoEncoder({ output: (c, m) => muxer.addVideoChunk(c, m), error: (e) => { failed = e } })
  ve.configure(vc)
  if (ac) {
    const buf = await synth(DURATION, sfxPlan(tr))
    const ae = new AudioEncoder({ output: (c, m) => muxer.addAudioChunk(c, m), error: (e) => { failed = e } })
    ae.configure(ac.cfg)
    const L = buf.getChannelData(0), R = buf.getChannelData(1)
    for (let i = 0; i < buf.length; i += 4096) {
      const n = Math.min(4096, buf.length - i)
      const data = new Float32Array(n * 2)
      data.set(L.subarray(i, i + n), 0)
      data.set(R.subarray(i, i + n), n)
      const ad = new AudioData({ format: 'f32-planar', sampleRate: SR, numberOfFrames: n, numberOfChannels: 2, timestamp: Math.round((i / SR) * 1e6), data })
      ae.encode(ad)
      ad.close()
    }
    await ae.flush()
    ae.close()
  }
  const frames = Math.round(DURATION * q.fps)
  for (let i = 0; i < frames; i++) {
    if (signal?.aborted) { ve.close(); throw new DOMException('aborted', 'AbortError') }
    if (failed) throw failed
    paint(g, tr, tx, i / q.fps)
    const vf = new VideoFrame(cv, { timestamp: Math.round((i / q.fps) * 1e6), duration: Math.round(1e6 / q.fps) })
    ve.encode(vf, { keyFrame: i % (q.fps * 2) === 0 })
    vf.close()
    await drained(ve, 4, signal)
    if (i % 4 === 0) { onProgress(i / frames); await yieldNow() }
  }
  if (signal?.aborted) { ve.close(); throw new DOMException('aborted', 'AbortError') }
  await ve.flush()
  ve.close()
  if (failed) throw failed
  muxer.finalize()
  onProgress(1)
  return { blob: new Blob([target.buffer], { type: 'video/mp4' }), mime: 'video/mp4', ext: 'mp4', w: q.w, h: q.h, offline: true }
}

/** 实时录制：一帧帧画到画布上，用浏览器的录像功能录下来 */
async function recordRealtime(cv: HTMLCanvasElement, g: CanvasRenderingContext2D, tr: FilmTrade, tx: FilmText, sound: boolean, onProgress: (p: number) => void, signal?: AbortSignal): Promise<Rendered> {
  paint(g, tr, tx, 0)
  const stream = cv.captureStream(60)
  let actx: AudioContext | null = null
  let src: AudioBufferSourceNode | null = null
  if (sound) {
    actx = new AudioContext({ sampleRate: SR })
    const buf = await synth(DURATION, sfxPlan(tr))
    src = actx.createBufferSource()
    src.buffer = buf
    const dest = actx.createMediaStreamDestination()
    src.connect(dest)
    for (const t of dest.stream.getAudioTracks()) stream.addTrack(t)
  }
  const mime = ['video/mp4;codecs=avc1,mp4a.40.2', 'video/mp4', 'video/webm;codecs=vp9,opus', 'video/webm'].find((m) => MediaRecorder.isTypeSupported(m)) ?? ''
  const rec = new MediaRecorder(stream, { mimeType: mime || undefined, videoBitsPerSecond: 12_000_000 })
  const parts: Blob[] = []
  rec.ondataavailable = (e) => { if (e.data.size) parts.push(e.data) }
  const stopped = new Promise<void>((r) => { rec.onstop = () => r() })
  rec.start(250)
  src?.start()
  const t0 = performance.now()
  try {
    await new Promise<void>((resolve, reject) => {
      const tick = () => {
        if (signal?.aborted) { reject(new DOMException('aborted', 'AbortError')); return }
        const t = (performance.now() - t0) / 1000
        paint(g, tr, tx, Math.min(DURATION, t))
        onProgress(Math.min(1, t / DURATION))
        if (t >= DURATION) { resolve(); return }
        requestAnimationFrame(tick)
      }
      requestAnimationFrame(tick)
    })
  } finally {
    if (rec.state !== 'inactive') rec.stop()
  }
  await stopped
  void actx?.close()
  const type = rec.mimeType || mime || 'video/webm'
  return { blob: new Blob(parts, { type }), mime: type, ext: type.includes('mp4') ? 'mp4' : 'webm', w: cv.width, h: cv.height, offline: false }
}

/** 画布不会等字体：先加载好 */
export async function loadFilmFonts() {
  try {
    await Promise.all([
      document.fonts.load(`600 40px 'Geist Variable'`), document.fonts.load(`400 40px 'Geist Variable'`),
      document.fonts.load(`500 40px 'Geist Variable'`), document.fonts.load(`400 40px 'Geist Mono Variable'`),
      document.fonts.load(`400 40px 'Noto Sans SC'`, '模拟盘'), document.fonts.load(`500 40px 'Noto Sans SC'`, '模拟盘'),
    ])
  } catch { /* 用系统字体 */ }
}
