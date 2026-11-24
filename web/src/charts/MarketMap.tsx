// 全市场方块图（`10` 第 8 项，3.15，D79）：每个币一块，面积是 24 小时成交额，颜色是 24 小时涨跌，按板块分组。
// 点一块进它的交易页，点板块名放大这个板块。普通笔记本 60 帧：只有数据变了、鼠标动了、动画进行中才重画。
import { recall, remember } from '../lib/cache'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useSurface, type G } from '../lib/gfx'
import { fmtCompactL, tr, useT, type Key } from '../lib/i18n'
import { fmtPrice } from '../lib/format'
import { reducedMotion } from '../lib/anchor'
import { C, dirColors } from './scale'
import { locDec } from '../lib/tz'

export interface MapNode { s: string; i: string; px: number; chg: number; qv: number; sec: string; rv?: number; f?: boolean }
export interface MapSnap { t: number; state: 'loading' | 'ok' | 'stale'; source: string; total: number; min_vol: number; nodes: MapNode[]; problem?: 'throttled' | 'restricted' | 'unreachable'; wait_min?: number }
export type Conn = 'connecting' | 'ok' | 'down'
export interface PickAt { x: number; y: number; w: number; h: number; color: string }

/** 取方块图数据。conn：接口连不上时是 'down' */
export function useMapData(ms = 5000): { snap: MapSnap | null; conn: Conn } {
  const [snap, setSnap] = useState<MapSnap | null>(() => recall<MapSnap>('/api/marketmap')) // 先显示上一次的（lib/cache.ts）
  const [conn, setConn] = useState<Conn>('connecting')
  useEffect(() => {
    let alive = true
    const tick = async () => {
      if (document.hidden) return
      try {
        const r = await fetch('/api/marketmap', { cache: 'no-store' })
        if (!r.ok) throw new Error(String(r.status))
        const v = await r.json() as MapSnap
        if (v.nodes?.length) remember('/api/marketmap', v)
        if (alive) { setSnap(v); setConn('ok') }
      } catch { if (alive) setConn('down') }
    }
    void tick()
    const id = window.setInterval(tick, ms)
    return () => { alive = false; window.clearInterval(id) }
  }, [ms])
  return { snap, conn }
}

// ---------- 板块 ----------
const OTHER = 'other'
const SEC_KEYS: Record<string, Key> = {
  'PoW': 'sec.pow', 'Layer-1': 'sec.l1', 'Layer-2': 'sec.l2', 'DeFi': 'sec.defi', 'Meme': 'sec.meme', 'AI': 'sec.ai', 'Payment': 'sec.pay',
  'Infrastructure': 'sec.infra', 'Storage': 'sec.storage', 'Gaming': 'sec.game', 'Metaverse': 'sec.meta', 'NFT': 'sec.nft', 'RWA': 'sec.rwa', 'Alpha': 'sec.alpha',
}
export const secLabel = (sec: string): string => {
  if (!sec || sec === OTHER || sec === 'Crypto') return tr('sec.other')
  const k = SEC_KEYS[sec]
  return k ? tr(k) : sec
}

const median = (a: number[]) => {
  if (!a.length) return 0
  const s = [...a].sort((x, y) => x - y)
  return s[s.length >> 1]
}

export function mapStats(nodes: MapNode[]) {
  const by = (s: string) => nodes.find((n) => n.s === s)
  return {
    up: nodes.filter((n) => n.chg > 0).length,
    down: nodes.filter((n) => n.chg < 0).length,
    median: median(nodes.map((n) => n.chg)),
    total: nodes.reduce((s, n) => s + n.qv, 0),
    hot: nodes.filter((n) => (n.rv ?? 0) >= 2).length,
    btc: by('BTC')?.chg ?? null,
    eth: by('ETH')?.chg ?? null,
  }
}

interface Group { key: string; nodes: MapNode[]; weight: number }

const weightOf = (n: MapNode) => Math.pow(Math.max(n.qv, 1), 0.55)

/** 不到 3 个币的板块并进"其他"，太小了放不下名字 */
function groupNodes(nodes: MapNode[]): Group[] {
  const m = new Map<string, MapNode[]>()
  for (const n of nodes) {
    const k = !n.sec || n.sec === 'Crypto' ? OTHER : n.sec
    if (!m.has(k)) m.set(k, [])
    m.get(k)!.push(n)
  }
  const rest: MapNode[] = m.get(OTHER) ?? []
  const out: Group[] = []
  for (const [key, list] of m) {
    if (key === OTHER) continue
    if (list.length < 3) { rest.push(...list); continue }
    out.push({ key, nodes: list, weight: list.reduce((s, n) => s + weightOf(n), 0) })
  }
  out.sort((a, b) => b.weight - a.weight)
  if (rest.length) out.push({ key: OTHER, nodes: rest, weight: rest.reduce((s, n) => s + weightOf(n), 0) })
  return out
}

// ---------- 排版：矩形树图（Squarified），块尽量接近正方形 ----------
interface Rect { x: number; y: number; w: number; h: number }

function squarify<T>(items: { v: T; w: number }[], box: Rect): (Rect & { v: T })[] {
  const out: (Rect & { v: T })[] = []
  const total = items.reduce((s, i) => s + i.w, 0)
  if (total <= 0 || box.w <= 0 || box.h <= 0) return out
  const k = (box.w * box.h) / total
  const rest = items.map((i) => ({ v: i.v, a: i.w * k }))
  let { x, y, w, h } = box
  while (rest.length) {
    const side = Math.max(Math.min(w, h), 1e-6)
    const row: typeof rest = []
    let sum = 0, best = Infinity
    while (rest.length) {
      const n = rest[0]
      const s2 = sum + n.a
      let lo = n.a, hi = n.a
      for (const r of row) { lo = Math.min(lo, r.a); hi = Math.max(hi, r.a) }
      const worst = Math.max((side * side * hi) / (s2 * s2), (s2 * s2) / (side * side * lo))
      if (worst > best) break
      best = worst; sum = s2
      row.push(rest.shift()!)
    }
    const th = sum / side
    let o = 0
    for (const r of row) {
      const l = r.a / th
      if (w >= h) out.push({ v: r.v, x, y: y + o, w: th, h: l })
      else out.push({ v: r.v, x: x + o, y, w: l, h: th })
      o += l
    }
    if (w >= h) { x += th; w -= th } else { y += th; h -= th }
  }
  return out
}

interface Head extends Rect { key: string; bar: number }
interface Layout { tiles: Map<string, Rect>; heads: Head[] }

const HEAD_H = 18

function computeLayout(groups: Group[], W: number, H: number, focus: string | null): Layout {
  const tiles = new Map<string, Rect>()
  const heads: Head[] = []
  const shown = focus ? groups.filter((g) => g.key === focus) : groups
  for (const r of squarify(shown.map((g) => ({ v: g, w: g.weight })), { x: 0, y: 0, w: W, h: H })) {
    const bar = r.h >= 40 && r.w >= 34 ? HEAD_H : 0
    heads.push({ key: r.v.key, x: r.x, y: r.y, w: r.w, h: r.h, bar })
    const list = [...r.v.nodes].sort((a, b) => b.qv - a.qv).map((n) => ({ v: n, w: weightOf(n) }))
    for (const c of squarify(list, { x: r.x + 1, y: r.y + bar, w: r.w - 2, h: r.h - bar - 1 })) tiles.set(c.v.s, { x: c.x, y: c.y, w: c.w, h: c.h })
  }
  return { tiles, heads }
}

// ---------- 颜色：涨跌越大越亮，±14% 到头 ----------
const COLOR_MAX = 14
const rgb = (s: string) => [parseInt(s.slice(1, 3), 16), parseInt(s.slice(3, 5), 16), parseInt(s.slice(5, 7), 16)]
export function tileColor(chg: number): string {
  const { up, down } = dirColors()
  const c = rgb(chg >= 0 ? up : down)
  const a = 0.1 + 0.62 * Math.pow(Math.min(1, Math.abs(chg) / COLOR_MAX), 0.8)
  return `rgb(${Math.round(c[0] * a)},${Math.round(c[1] * a)},${Math.round(c[2] * a)})`
}
const signedPct = (v: number, d = 1) => (v > 0 ? '+' : v < 0 ? '−' : '') + locDec(Math.abs(v).toFixed(d)) + '%'

/** 图例：面积、颜色刻度、白色角标 */
export function MapLegend() {
  const t = useT()
  const stops = [-14, -10, -6, -3, -1, 0, 1, 3, 6, 10, 14]
  const bg = `linear-gradient(90deg, ${stops.map((v, i) => `${tileColor(v)} ${(i / (stops.length - 1)) * 100}%`).join(', ')})`
  return (
    <div className="mb-legend">
      <span>{t('mb.lgArea')}</span>
      <span className="mb-scale-wrap">
        {t('mb.lgColor')}
        <span className="mb-scale-box">
          <i className="mb-scale" style={{ background: bg }} />
          <span className="mb-scale-tk num"><b>−14%</b><b>0</b><b>+14%</b></span>
        </span>
      </span>
      <span className="mb-lg-focus"><i className="mb-frame" />{t('mb.lgFocus')}</span>
      <span className="mb-lg-hot"><i className="mb-corner" />{t('mb.lgHot')}</span>
    </div>
  )
}

// ---------- 主体 ----------
interface Anim { from: Rect; to: Rect; a0: number; a1: number }
type Hover = { kind: 'tile'; s: string } | { kind: 'head'; key: string } | null
interface Tip { hover: NonNullable<Hover>; x: number; y: number; right: boolean; below: boolean }

const RELAYOUT_MS = 5 * 60_000
const ANIM_MS = 380
const easeOut = (k: number) => 1 - Math.pow(1 - k, 3)

export function MarketMap({ snap, conn, onPick, holds }: { snap: MapSnap | null; conn: Conn; onPick?: (n: MapNode, at: PickAt) => void; holds?: Map<string, 'long' | 'short'> }) {
  const t = useT()
  const nodes = useMemo(() => snap?.nodes ?? [], [snap])
  const nodeMap = useRef(new Map<string, MapNode>())
  nodeMap.current = useMemo(() => new Map(nodes.map((n) => [n.s, n])), [nodes])
  const groups = useRef<Group[]>([])
  const anims = useRef(new Map<string, Anim>())
  const heads = useRef<Head[]>([])
  const t0 = useRef(0)
  const dur = useRef(0)
  const size = useRef({ w: 0, h: 0 })
  const layoutKey = useRef('')
  const layoutAt = useRef(0)
  const hover = useRef<Hover>(null)
  const pointer = useRef('mouse')
  const [focus, setFocus] = useState<string | null>(null)
  const focusRef = useRef<string | null>(null)
  const [tip, setTip] = useState<Tip | null>(null)
  const holdsRef = useRef(holds)
  holdsRef.current = holds

  const progress = () => (dur.current <= 0 ? 1 : easeOut(Math.min(1, (performance.now() - t0.current) / dur.current)))
  const rectNow = (a: Anim, k: number): Rect => ({
    x: a.from.x + (a.to.x - a.from.x) * k, y: a.from.y + (a.to.y - a.from.y) * k,
    w: a.from.w + (a.to.w - a.from.w) * k, h: a.from.h + (a.to.h - a.from.h) * k,
  })

  const relayout = (animate: boolean) => {
    const { w, h } = size.current
    if (!w || !h) return
    groups.current = groupNodes([...nodeMap.current.values()])
    const full = computeLayout(groups.current, w, h, null)
    const zoom = focusRef.current ? computeLayout(groups.current, w, h, focusRef.current) : null
    const k = progress()
    const next = new Map<string, Anim>()
    for (const [s, base] of full.tiles) {
      const shown = zoom ? zoom.tiles.get(s) : base
      const a1 = shown ? 1 : 0
      if (!animate) { next.set(s, { from: shown ?? base, to: shown ?? base, a0: a1, a1 }); continue }
      const prev = anims.current.get(s)
      if (!prev) { // 新出现的币：从自己的中心长出来
        const to = shown ?? base
        next.set(s, { from: { x: to.x + to.w / 2, y: to.y + to.h / 2, w: 0, h: 0 }, to, a0: 1, a1 })
        continue
      }
      const cur = rectNow(prev, k)
      const a0 = prev.a0 + (prev.a1 - prev.a0) * k
      // 要隐藏的留在原地淡出；隐藏过的在目标位置淡入；一直显示的移过去
      if (!shown) next.set(s, { from: cur, to: cur, a0, a1 })
      else if (a0 < 0.01) next.set(s, { from: shown, to: shown, a0, a1 })
      else next.set(s, { from: cur, to: shown, a0, a1 })
    }
    anims.current = next
    heads.current = (zoom ?? full).heads
    layoutAt.current = performance.now()
    t0.current = performance.now()
    dur.current = animate && !reducedMotion() ? ANIM_MS : 0
  }

  const { ref, redraw } = useSurface((g: G) => {
    if (g.w !== size.current.w || g.h !== size.current.h) {
      size.current = { w: g.w, h: g.h }
      relayout(false)
    }
    const k = progress()
    if (k < 1) requestAnimationFrame(() => redraw()) // 画的过程中要求重画会被清掉，放到下一帧
    const ctx = g.ctx
    const list = [...anims.current.entries()]
    // 淡出的先画，放大的盖在上面
    list.sort((a, b) => a[1].a1 - b[1].a1)
    for (const [s, a] of list) {
      const n = nodeMap.current.get(s)
      if (!n) continue
      const alpha = a.a0 + (a.a1 - a.a0) * k
      if (alpha <= 0.01) continue
      const r = rectNow(a, k)
      drawTile(g, n, r, alpha, hover.current?.kind === 'tile' && hover.current.s === s, holdsRef.current?.get(n.i))
    }
    ctx.globalAlpha = 1
    // 板块名和边框
    const hv = hover.current
    for (const h of heads.current) {
      g.hline(h.y, h.x, h.x + h.w, C.rule2); g.hline(h.y + h.h - 1 / g.ky, h.x, h.x + h.w, C.rule2)
      g.vline(h.x, h.y, h.y + h.h, C.rule2); g.vline(h.x + h.w - 1 / g.kx, h.y, h.y + h.h, C.rule2)
      if (!h.bar) continue
      const on = hv?.kind === 'head' && hv.key === h.key
      const label = focusRef.current ? `‹ ${tr('mb.back')} / ${secLabel(h.key)}` : secLabel(h.key)
      const lw = g.measure(label, { size: 12 })
      if (lw <= h.w - 10) {
        g.text(label, h.x + 6, h.y + HEAD_H / 2 + 0.5, { size: 12, color: on ? C.t1 : C.t2, baseline: 'middle' })
        if (on) g.hline(h.y + HEAD_H - 3, h.x + 6, h.x + 6 + lw, C.t2)
      }
      const list2 = groups.current.find((x) => x.key === h.key)?.nodes
      if (list2) {
        const live = list2.map((x) => nodeMap.current.get(x.s)?.chg ?? x.chg)
        const m = median(live)
        const txt = signedPct(m)
        if (lw + g.measure(txt, { size: 11 }) + 20 <= h.w) g.text(txt, h.x + h.w - 6, h.y + HEAD_H / 2 + 0.5, { size: 11, color: m > 0 ? dirColors().up : m < 0 ? dirColors().down : C.t2, align: 'right', baseline: 'middle' })
      }
    }
  }, [snap, focus, holds])

  // 币种变了或到时间了才重新排，平时只换颜色和数字，方块不会一直跳
  useEffect(() => {
    if (!nodes.length) return
    const key = nodes.map((n) => n.s).sort().join()
    if (key !== layoutKey.current || performance.now() - layoutAt.current > RELAYOUT_MS) {
      layoutKey.current = key
      relayout(true)
    }
    redraw()
  }, [nodes]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    focusRef.current = focus
    setTip(null)
    hover.current = null
    relayout(true)
    redraw()
    if (!focus) return
    const key = (e: KeyboardEvent) => { if (e.key === 'Escape') setFocus(null) }
    window.addEventListener('keydown', key)
    return () => window.removeEventListener('keydown', key)
  }, [focus]) // eslint-disable-line react-hooks/exhaustive-deps

  const hitTest = (mx: number, my: number): Hover => {
    for (const h of heads.current) if (h.bar && mx >= h.x && mx <= h.x + h.w && my >= h.y && my <= h.y + h.bar) return { kind: 'head', key: h.key }
    for (const [s, a] of anims.current) {
      if (a.a1 === 0) continue
      const r = a.to
      if (mx >= r.x && mx < r.x + r.w && my >= r.y && my < r.y + r.h) return { kind: 'tile', s }
    }
    return null
  }
  const sameHover = (a: Hover, b: Hover) => (a?.kind === b?.kind) && (a?.kind === 'tile' ? a.s === (b as { s: string }).s : a?.kind === 'head' ? a.key === (b as { key: string }).key : true)
  const show = (el: HTMLCanvasElement, clientX: number, clientY: number) => {
    const r = el.getBoundingClientRect()
    const mx = clientX - r.left, my = clientY - r.top
    const h = hitTest(mx, my)
    if (!sameHover(h, hover.current)) { hover.current = h; redraw() }
    el.style.cursor = h ? 'pointer' : 'default'
    setTip(h ? { hover: h, x: mx, y: my, right: mx > r.width - 240, below: my < 170 } : null)
    return { h, r }
  }
  const pick = (h: Extract<NonNullable<Hover>, { kind: 'tile' }>, canvas: DOMRect) => {
    const n = nodeMap.current.get(h.s)
    const a = anims.current.get(h.s)
    if (n && a && onPick) onPick(n, { x: canvas.left + a.to.x, y: canvas.top + a.to.y, w: a.to.w, h: a.to.h, color: tileColor(n.chg) })
  }
  const onClick = (e: React.MouseEvent<HTMLCanvasElement>) => {
    const was = hover.current
    const { h, r } = show(e.currentTarget, e.clientX, e.clientY)
    if (!h) return
    if (h.kind === 'head') { setFocus((f) => (f ? null : h.key)); return }
    // 手指没有悬停：第一下看读数，再点同一块才进去
    if (pointer.current === 'touch' && !sameHover(was, h)) return
    pick(h, r)
  }

  // 取不到币安数据时写原因（限流、地区被拒、连不上），不要一直"正在连接"（11-18）
  const stateText = conn === 'down' ? t('mb.down')
    : snap?.problem && snap.state !== 'ok' ? t(`mb.p.${snap.problem}` as Key, { min: snap.wait_min ?? 1 })
    : !snap || snap.state === 'loading' ? t('mb.loading')
    : snap.state === 'stale' ? t('mb.stale')
    : null
  return (
    <div className="mb-map">
      <canvas ref={ref} onPointerDown={(e) => { pointer.current = e.pointerType }}
        onPointerMove={(e) => { if (e.pointerType !== 'touch') show(e.currentTarget, e.clientX, e.clientY) }}
        onPointerLeave={(e) => { if (e.pointerType !== 'touch') { hover.current = null; setTip(null); redraw() } }}
        onClick={onClick} />
      {stateText && <div className="mb-state" role="status" data-mb-problem={snap?.problem}><span className={`sq ${conn === 'down' || snap?.state === 'stale' || snap?.problem ? 'warn' : ''}`} />{stateText}</div>}
      {tip && <MapTip tip={tip} nodes={nodeMap.current} groups={groups.current} zoomed={!!focus} />}
    </div>
  )
}

function drawTile(g: G, n: MapNode, r: Rect, alpha: number, on: boolean, hold?: 'long' | 'short') {
  const ctx = g.ctx
  const a = g.X(r.x), b = g.Y(r.y), c = g.X(r.x + r.w), d = g.Y(r.y + r.h)
  const w = c - a - 1, h = d - b - 1
  if (w < 1 || h < 1) return
  ctx.globalAlpha = alpha
  ctx.fillStyle = tileColor(n.chg)
  ctx.fillRect(a, b, w, h)
  const m = Math.min(r.w, r.h)
  // 成交额是平时 2 倍以上：右上角一个白色三角
  if ((n.rv ?? 0) >= 2 && m >= 14) {
    const s = g.px(m >= 28 ? 7 : 4)
    ctx.fillStyle = 'rgba(255,255,255,0.92)'
    ctx.beginPath(); ctx.moveTo(a + w - s, b); ctx.lineTo(a + w, b); ctx.lineTo(a + w, b + s); ctx.closePath(); ctx.fill()
  }
  // 名字和涨跌：放得下才写。字号跟着方块走，名字写不下就缩，缩到 11 还放不下就不写
  if (m >= 16 && r.w >= 22) {
    let fs = Math.min(22, Math.max(11, m / 4.2))
    const inner = r.w - 6
    const nw = g.measure(n.s, { size: fs, weight: 500 })
    if (nw > inner) fs = (fs * inner) / nw
    if (fs >= 10.5) {
      const ps = Math.max(11, fs * 0.68)
      const pct = signedPct(n.chg)
      const two = r.h >= fs + ps + 8 && g.measure(pct, { size: ps }) <= inner
      const cx = r.x + r.w / 2, cy = r.y + r.h / 2
      g.text(n.s, cx, two ? cy - ps * 0.62 : cy + 0.5, { size: fs, weight: 500, color: '#ffffff', align: 'center', baseline: 'middle' })
      if (two) g.text(pct, cx, cy + fs * 0.62, { size: ps, color: 'rgba(255,255,255,0.82)', align: 'center', baseline: 'middle' })
    }
  }
  ctx.globalAlpha = 1
  // AI 的持仓：瞄准框（四个角）和一行字
  if (hold && m >= 14) {
    const inset = g.px(3), len = Math.round(Math.min(g.px(9), (m * g.kx) / 4)), t = g.px(1)
    const x0 = a + inset, y0 = b + inset, x1 = a + w - inset, y1 = b + h - inset
    ctx.fillStyle = '#ffffff'
    for (const [x, y, sx, sy] of [[x0, y0, 1, 1], [x1, y0, -1, 1], [x0, y1, 1, -1], [x1, y1, -1, -1]] as const) {
      ctx.fillRect(sx > 0 ? x : x - len, sy > 0 ? y : y - t, len, t)
      ctx.fillRect(sx > 0 ? x : x - t, sy > 0 ? y : y - len, t, len)
    }
    if (r.h >= 44 && r.w >= 56) g.text(tr(hold === 'long' ? 'mb.aiLong' : 'mb.aiShort'), r.x + 7, r.y + 8, { size: 12, color: '#ffffff', baseline: 'top' })
  }
  // 重点币种：细白框。鼠标移上去的块：粗一点的白框
  if (on || n.f) {
    const t = g.px(on ? 2 : 1)
    ctx.globalAlpha = on ? alpha : alpha * 0.78
    ctx.fillStyle = '#ffffff'
    ctx.fillRect(a, b, w, t); ctx.fillRect(a, b + h - t, w, t); ctx.fillRect(a, b, t, h); ctx.fillRect(a + w - t, b, t, h)
    ctx.globalAlpha = 1
  }
}

function MapTip({ tip, nodes, groups, zoomed }: { tip: Tip; nodes: Map<string, MapNode>; groups: Group[]; zoomed: boolean }) {
  const t = useT()
  const cls = `mb-tip ${tip.right ? 'r' : ''} ${tip.below ? 'b' : ''}`
  if (tip.hover.kind === 'head') {
    const key = tip.hover.key
    const g = groups.find((x) => x.key === key)
    if (!g) return null
    const total = groups.reduce((s, x) => s + x.nodes.reduce((a, n) => a + (nodes.get(n.s)?.qv ?? n.qv), 0), 0)
    const qv = g.nodes.reduce((a, n) => a + (nodes.get(n.s)?.qv ?? n.qv), 0)
    const m = median(g.nodes.map((n) => nodes.get(n.s)?.chg ?? n.chg))
    return (
      <div className={cls} style={{ left: tip.x, top: tip.y }} role="tooltip">
        <b>{secLabel(key)}</b>
        <dl>
          <dt>{t('mb.secCount')}</dt><dd className="num">{g.nodes.length}</dd>
          <dt>{t('mb.median')}</dt><dd className={`num ${m > 0 ? 'up' : m < 0 ? 'down' : ''}`}>{signedPct(m)}</dd>
          <dt>{t('mb.tipVol')}</dt><dd className="num">{fmtCompactL(qv)}</dd>
          <dt>{t('mb.secShare')}</dt><dd className="num">{total > 0 ? locDec(((qv / total) * 100).toFixed(1)) : locDec('0.0')}%</dd>
        </dl>
        <i>{t(zoomed ? 'mb.secBack' : 'mb.secZoom')}</i>
      </div>
    )
  }
  const n = nodes.get(tip.hover.s)
  if (!n) return null
  return (
    <div className={cls} style={{ left: tip.x, top: tip.y }} role="tooltip">
      <b className="sym">{t('mk.perp', { name: n.s })}</b>
      <dl>
        <dt>{t('mb.tipPx')}</dt><dd className="num">{fmtPrice(String(n.px))}</dd>
        <dt>{t('mb.tipChg')}</dt><dd className={`num ${n.chg > 0 ? 'up' : n.chg < 0 ? 'down' : ''}`}>{signedPct(n.chg, 2)}</dd>
        <dt>{t('mb.tipVol')}</dt><dd className="num">{fmtCompactL(n.qv)}</dd>
        {n.rv ? <><dt>{t('mb.tipRv')}</dt><dd className="num">{n.rv.toFixed(n.rv >= 10 ? 0 : 1)}×</dd></> : null}
        <dt>{t('mb.tipSec')}</dt><dd>{secLabel(n.sec)}</dd>
      </dl>
    </div>
  )
}
