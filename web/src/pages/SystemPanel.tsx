// 系统控制台（可视化 11）：数据从交易所到你屏幕的路径，每一段都是后端算出来的真实数字。
// 光点的疏密跟着每秒消息数走，断开的线路变成虚线。没有装饰用的假数据。
import { useEffect, useRef, useState } from 'react'
import { useSurface, type G } from '../lib/gfx'
import { useT, useLang } from '../lib/i18n'
import { C } from '../charts/scale'
import '../styles/system.css'
import { locDec, numLocale } from '../lib/tz'

interface Line { venue: string; rate: number; trades: number; depth: number; marks: number; ms: number; total: number; last: number }
interface Poll { id: string; ms?: number; ok: boolean; at?: number; n?: number }
interface Snap {
  t: number; lines: Line[]; polls: Poll[]; up: Record<string, boolean>; reconnects: number; clients: number; heat: number; bots: number; algos: number; follows: number; signals_min: number
  db?: { ping_ms: number; busy: number; total: number }
  runtime: { goroutines: number; heap_mb: number; gc_ms: number; up_sec: number }
}

const NAMES: Record<string, string> = { hyperliquid: 'Hyperliquid', bybit: 'Bybit', okx: 'OKX', sim: 'SIM' }
const UP_KEY: Record<string, string[]> = { 'binance-usdm': ['market', 'public'], 'binance-spot': ['spot'], hyperliquid: ['hyperliquid'], sim: ['sim'] }

function fmtRate(v: number) { return v >= 1000 ? (v / 1000).toFixed(1) + 'k' : v >= 100 ? v.toFixed(0) : v.toFixed(v >= 10 ? 0 : 1) }
function fmtUp(s: number) {
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60)
  return d > 0 ? `${d}d ${h}h` : h > 0 ? `${h}h ${m}m` : `${m}m ${s % 60}s`
}
function ago(ms: number, t: (k: any, p?: any) => string) {
  const s = Math.max(0, Math.round(ms / 1000))
  return s < 90 ? t('sy.agoS', { n: s }) : s < 5400 ? t('sy.agoM', { n: Math.round(s / 60) }) : t('sy.agoH', { n: Math.round(s / 3600) })
}

interface Node { id: string; col: number; title: string; big: string; unit?: string; sub?: string; bad?: boolean; rate: number; spark?: number[] }
interface Wire { a: string; b: string; rate: number; down?: boolean }

export function SystemBody() {
  const t = useT()
  const lang = useLang()
  const [snap, setSnap] = useState<Snap | null>(null)
  const hist = useRef<Record<string, number[]>>({})
  useEffect(() => {
    let live = true
    const go = async () => {
      try {
        const r = await fetch('/api/system')
        if (!r.ok || !live) return
        const d = (await r.json()) as Snap
        const add = (k: string, v: number) => {
          const h = (hist.current[k] ??= [])
          h.push(v)
          if (h.length > 60) h.shift()
        }
        let all = 0
        for (const l of d.lines ?? []) { add(l.venue, l.rate); all += l.rate }
        add('__all', all)
        setSnap(d)
      } catch { /* 下一轮再取 */ }
    }
    void go()
    const id = window.setInterval(go, 1000)
    return () => { live = false; window.clearInterval(id) }
  }, [])
  const snapRef = useRef(snap)
  snapRef.current = snap
  const reduce = useRef(matchMedia('(prefers-reduced-motion: reduce)').matches)
  const { ref } = useSurface((g) => { if (snapRef.current) paint(g, build(snapRef.current, hist.current, t), reduce.current) }, [snap, lang], { animate: !reduce.current })
  const run = snap?.runtime
  return (
    <div className="sy">
      <div className="sy-map"><canvas ref={ref} /></div>
      <footer className="sy-strip">
        <span><i>{t('sy.up')}</i><b>{run ? fmtUp(run.up_sec) : '—'}</b></span>
        <span><i>{t('sy.reconnects')}</i><b>{snap?.reconnects ?? '—'}</b></span>
        <span><i>{t('sy.goroutines')}</i><b>{run?.goroutines ?? '—'}</b></span>
        <span><i>{t('sy.heap')}</i><b>{run ? `${run.heap_mb.toFixed(0)} MB` : '—'}</b></span>
        <span><i>{t('sy.gc')}</i><b>{run ? `${locDec(run.gc_ms.toFixed(2))} ms` : '—'}</b></span>
        <em>{t('sy.note')}</em>
      </footer>
    </div>
  )
}

function build(s: Snap, hist: Record<string, number[]>, t: (k: any, p?: any) => string) {
  const nodes: Node[] = []
  const wires: Wire[] = []
  const total = (s.lines ?? []).reduce((a, l) => a + l.rate, 0)
  const trades = (s.lines ?? []).reduce((a, l) => a + l.trades, 0)
  const depth = (s.lines ?? []).reduce((a, l) => a + l.depth, 0)
  for (const l of s.lines ?? []) {
    const keys = UP_KEY[l.venue] ?? []
    const silent = s.t - l.last
    // 连着但没有消息，和断开一样算故障（网络卡住时交易所不会告诉你断了）
    const up = (keys.length === 0 || keys.every((k) => s.up?.[k] !== false)) && silent < 5000
    const lineName = l.venue === 'binance-usdm' ? t('sy.binPerp') : l.venue === 'binance-spot' ? t('sy.binSpot') : NAMES[l.venue] ?? l.venue
    nodes.push({ id: l.venue, col: 0, title: lineName, big: l.ms >= 1000 ? (l.ms / 1000).toFixed(2) : l.ms < 1 ? '<1' : l.ms.toFixed(0), unit: l.ms >= 1000 ? 's' : 'ms', sub: silent >= 5000 ? t('sy.silent', { n: Math.round(silent / 1000) }) : t('sy.perSec', { n: fmtRate(l.rate) }), bad: !up, rate: up ? l.rate : 0, spark: hist[l.venue] })
    wires.push({ a: l.venue, b: 'bus', rate: up ? l.rate : 0, down: !up })
  }
  for (const p of s.polls ?? []) {
    const name = p.id === 'chain' ? t('sy.chain') : NAMES[p.id] ?? p.id
    const sub = p.at ? ago(s.t - p.at, t) : t('sy.never')
    if (p.id === 'chain') nodes.push({ id: p.id, col: 0, title: name, big: (p.n ?? 0).toLocaleString(numLocale()), unit: t('sy.addr'), sub, bad: !p.ok, rate: 0.05 })
    else nodes.push({ id: p.id, col: 0, title: name, big: (p.ms ?? 0).toFixed(0), unit: 'ms', sub, bad: !p.ok, rate: 0.05 })
    wires.push({ a: p.id, b: 'bus', rate: p.ok ? 0.5 : 0, down: !p.ok })
  }
  nodes.push({ id: 'bus', col: 1, title: t('sy.bus'), big: fmtRate(total), unit: t('sy.unit'), rate: total, spark: hist.__all })
  const mid = [
    { id: 'book', title: t('sy.book'), big: fmtRate(depth), unit: t('sy.unit'), rate: depth },
    { id: 'bars', title: t('sy.bars'), big: fmtRate(trades), unit: t('sy.unit'), rate: trades },
    { id: 'heat', title: t('sy.heat'), big: String(s.heat), unit: t('sy.coins'), rate: depth / 4 },
    { id: 'auto', title: t('sy.auto'), big: String(s.follows + s.bots + s.algos), unit: t('sy.running'), sub: [s.follows && `${t('sy.follows')} ${s.follows}`, s.bots && `${t('sy.bots')} ${s.bots}`, s.algos && `${t('sy.algos')} ${s.algos}`].filter(Boolean).join(' · '), rate: (s.follows + s.bots + s.algos) * 2 },
    { id: 'sig', title: t('sy.sig'), big: locDec(s.signals_min.toFixed(s.signals_min >= 10 ? 0 : 1)), unit: t('sy.perMin'), rate: s.signals_min },
  ]
  for (const m of mid) {
    nodes.push({ ...m, col: 2 })
    wires.push({ a: 'bus', b: m.id, rate: m.rate })
  }
  nodes.push({ id: 'hub', col: 3, title: t('sy.hub'), big: String(s.clients), unit: t('sy.clients'), rate: s.clients * 4 })
  nodes.push({ id: 'db', col: 3, title: t('sy.db'), big: s.db ? locDec(s.db.ping_ms.toFixed(1)) : '—', unit: 'ms', sub: s.db ? t('sy.dbSub', { b: s.db.busy, n: s.db.total }) : undefined, bad: !s.db, rate: s.db ? 3 : 0 })
  wires.push({ a: 'book', b: 'hub', rate: Math.min(depth, 20) }, { a: 'bars', b: 'hub', rate: Math.min(trades, 20) }, { a: 'heat', b: 'db', rate: 1 }, { a: 'bars', b: 'db', rate: 1 }, { a: 'auto', b: 'db', rate: s.follows + s.bots + s.algos }, { a: 'sig', b: 'auto', rate: s.signals_min })
  return { nodes, wires }
}

function paint(g: G, m: { nodes: Node[]; wires: Wire[] }, still: boolean) {
  const narrow = g.w < 700
  const pad = narrow ? 10 : 24
  const colW = narrow ? (g.w - pad * 2 - 3 * 18) / 4 : Math.min(190, (g.w - pad * 2) / 5.2)
  const gap = (g.w - pad * 2 - colW * 4) / 3
  const X = (c: number) => pad + c * (colW + gap)
  const colHead = narrow ? 8 : 20
  const pos: Record<string, { x: number; y: number; h: number }> = {}
  for (let c = 0; c < 4; c++) {
    const list = m.nodes.filter((n) => n.col === c)
    const nh = Math.min(narrow ? 66 : 92, (g.h - colHead - 12 - (list.length - 1) * 8) / Math.max(1, list.length))
    const total = list.length * nh + (list.length - 1) * 8
    const y0 = colHead + (g.h - colHead - 12 - total) / 2
    list.forEach((n, i) => { pos[n.id] = { x: X(c), y: y0 + i * (nh + 8), h: nh } })
  }
  const ctx = g.ctx
  // 线
  for (const w of m.wires) {
    const a = pos[w.a], b = pos[w.b]
    if (!a || !b) continue
    const same = Math.abs(a.x - b.x) < 2 // 同一列（信号 → 自动交易）：从右边绕一下
    const ax = g.X(a.x + colW), ay = g.Y(a.y + a.h / 2), bx = g.X(same ? b.x + colW : b.x), by = g.Y(b.y + b.h / 2)
    const dx = same ? g.px(gap * 0.3) : (bx - ax) * 0.5
    const c1x = ax + dx, c2x = same ? bx + dx : bx - dx
    ctx.beginPath()
    ctx.moveTo(ax, ay)
    ctx.bezierCurveTo(c1x, ay, c2x, by, bx, by)
    ctx.setLineDash(w.down ? [g.px(3), g.px(4)] : [])
    ctx.strokeStyle = w.down ? 'rgba(226,98,92,0.7)' : w.rate > 1 ? 'rgba(255,255,255,0.3)' : w.rate > 0 ? 'rgba(255,255,255,0.2)' : C.rule2
    ctx.lineWidth = 1
    ctx.stroke()
    ctx.setLineDash([])
    if (still || w.down || w.rate <= 0) continue
    // 光点：越忙越密
    const count = Math.max(1, Math.min(14, Math.round(Math.log10(w.rate + 1) * 5)))
    const speed = 0.00022 + Math.min(0.00028, Math.log10(w.rate + 1) * 0.00008)
    for (let i = 0; i < count; i++) {
      const u = (g.now * speed + i / count + (hash(w.a + w.b) % 97) / 97) % 1
      const v = 1 - u
      const px = v * v * v * ax + 3 * v * v * u * c1x + 3 * v * u * u * c2x + u * u * u * bx
      const py = v * v * v * ay + 3 * v * v * u * ay + 3 * v * u * u * by + u * u * u * by
      const size = g.px(2)
      ctx.fillStyle = `rgba(255,255,255,${0.35 + 0.6 * Math.sin(u * Math.PI)})`
      ctx.fillRect(Math.round(px - size / 2), Math.round(py - size / 2), size, size)
    }
  }
  // 节点
  for (const n of m.nodes) {
    const p = pos[n.id]
    if (!p) continue
    g.rect(p.x, p.y, p.x + colW, p.y + p.h, '#0b0b0d')
    const border = n.bad ? 'rgba(226,98,92,0.7)' : C.rule2
    g.hline(p.y, p.x, p.x + colW, border)
    g.hline(p.y + p.h, p.x, p.x + colW, border)
    g.vline(p.x, p.y, p.y + p.h, border)
    g.vline(p.x + colW, p.y, p.y + p.h, border)
    // 接线柱：线从这里进出，看着像接在设备上
    if (n.col > 0) g.rect(p.x - 2, p.y + p.h / 2 - 3, p.x + 1, p.y + p.h / 2 + 3, n.bad ? C.down : C.t2)
    if (n.col < 3) g.rect(p.x + colW - 1, p.y + p.h / 2 - 3, p.x + colW + 2, p.y + p.h / 2 + 3, n.bad ? C.down : C.t2)
    const ix = p.x + (narrow ? 6 : 12)
    const short = p.h < 60
    g.text(n.title, ix, p.y + (short ? 7 : 10), { size: narrow ? 11 : 12, color: C.t3, baseline: 'top' })
    const bigSize = narrow ? 15 : short ? 17 : 24
    const by = p.y + (short ? 22 : 28)
    g.text(n.big, ix, by, { size: bigSize, color: n.bad ? C.down : C.t1, baseline: 'top', weight: 400 })
    if (n.unit) g.text(n.unit, ix + g.measure(n.big, { size: bigSize }) + 5, by + bigSize - 12, { size: 11, color: C.t3, baseline: 'top' })
    if (n.sub && p.h >= (narrow ? 52 : 60)) g.text(n.sub, ix, p.y + p.h - (narrow ? 15 : 20), { size: 11, color: n.bad ? C.down : C.t3, baseline: 'top' })
    // 最近一分钟的消息量
    if (n.spark && n.spark.length > 1 && !narrow && p.h >= 70) {
      const s = n.spark, max = Math.max(...s, 1)
      const w = Math.min(66, colW * 0.4), x1 = p.x + colW - 10, x0 = x1 - w, bh = 22, y1 = p.y + p.h - 12
      const bw = w / 60
      for (let i = 0; i < s.length; i++) {
        const h = Math.max(1, (s[i] / max) * bh)
        g.rect(x1 - (s.length - i) * bw, y1 - h, x1 - (s.length - i) * bw + Math.max(1, bw - 0.4), y1, i === s.length - 1 ? '#fff' : 'rgba(255,255,255,0.4)')
      }
      void x0
    }
  }
}

function hash(s: string) {
  let h = 7
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) % 9973
  return h
}
