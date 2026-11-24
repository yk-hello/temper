// 跨所对比（可视化 5、B）：左边资金费矩阵，右边这个币的价差带。
// 矩阵：格子越亮，这家的资金费越高（绝对值）。数字折算成每 8 小时，不写年化，也不用涨跌色和琥珀色：
// 这是交易所公布的费率，不是收益（D85）。费率差最大的一行只用中性的细框标出来。
// 价差带：其他交易所相对币安差多少基点，一家一条线；最高和最低之间是带子，突然变宽的时段标成琥珀色。
import { recall, remember } from '../lib/cache'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useSurface } from '../lib/gfx'
import { useT, useLang, listSep } from '../lib/i18n'
import { C } from '../charts/scale'
import { clock } from '../lib/gfx'
import { Seg } from '../ui/Seg'
import '../styles/venues.css'
import { locDec } from '../lib/tz'

interface Cell { mark: number; rate: number; hours: number; rate8h: number; next?: number; t: number }
interface Row { coin: string; cells: Record<string, Cell>; spread_bps: number; gap_8h: number; hi: string; lo: string }
interface Matrix { venues: string[]; rows: Row[]; missing: string[] }
interface Spread { t: number[]; series: Record<string, (number | null)[]>; since: number }

const NAMES: Record<string, string> = { binance: 'Binance', hyperliquid: 'Hyperliquid', bybit: 'Bybit', okx: 'OKX' }
const LINE: Record<string, string> = { hyperliquid: '#ffffff', bybit: '#8fb8d8', okx: '#a3a3aa' }

function usePoll<T>(url: string, ms: number): T | null {
  const [v, setV] = useState<T | null>(() => recall<T>(url)) // 先显示上一次的（lib/cache.ts）
  useEffect(() => {
    let live = true
    const go = async () => {
      try {
        const r = await fetch(url)
        if (!r.ok) return
        const d = (await r.json()) as T
        remember(url, d)
        if (live) setV(d)
      } catch { /* 下一轮再取 */ }
    }
    void go()
    const id = window.setInterval(go, ms)
    return () => { live = false; window.clearInterval(id) }
  }, [url, ms])
  return v
}

const sign = (v: number, d: number) => (v > 0 ? '+' : v < 0 ? '−' : '') + locDec(Math.abs(v).toFixed(d))

/** 这张矩阵最值得说的一句话 */
function headline(rows: Row[]): Row | null {
  let best: Row | null = null
  for (const r of rows) if (!best || r.gap_8h > best.gap_8h) best = r
  return best
}

export function VenuesBody() {
  const t = useT()
  const m = usePoll<Matrix>('/api/venues/matrix', 5000)
  const [coin, setCoin] = useState('BTC')
  const sp = usePoll<Spread>(`/api/venues/spread?coin=${coin}`, 10000)
  const rows = m?.rows ?? []
  const venues = m?.venues ?? []
  const top = useMemo(() => headline(rows), [rows])
  const row = rows.find((r) => r.coin === coin)
  const maxRate = useMemo(() => {
    let v = 0.01
    for (const r of rows) for (const c of Object.values(r.cells)) v = Math.max(v, Math.abs(c.rate8h))
    return v
  }, [rows])
  return (
    <div className="vn">
      <section className="vn-matrix">
        <div className="vn-head">
          <h2>{t('vn.matrix')}</h2>
        </div>
        <div className="vn-table" role="table">
          <div className="vn-tr vn-th" role="row">
            <span role="columnheader">{t('vn.coin')}</span>
            {venues.map((v) => <span key={v} role="columnheader">{NAMES[v] ?? v}</span>)}
            <span role="columnheader">{t('vn.carry')}</span>
            <span role="columnheader">{t('vn.spread')}</span>
          </div>
          {rows.map((r) => (
            <button type="button" key={r.coin} className={r.coin === coin ? 'vn-tr on' : 'vn-tr'} onClick={() => setCoin(r.coin)} aria-pressed={r.coin === coin} data-widest={r === top || undefined}>
              <b>{r.coin}</b>
              {venues.map((v) => {
                const c = r.cells[v]
                if (!c) return <span key={v} className="vn-c none">{t('vn.none')}</span>
                const k = Math.min(1, Math.abs(c.rate8h) / maxRate)
                const col = c.rate8h >= 0 ? '#12c6cc' : '#8b90a0'
                const edge = r === top && r.gap_8h > 0 && (v === r.hi || v === r.lo)
                return (
                  <span key={v} className={edge ? 'vn-c edge' : 'vn-c'} style={{ background: `color-mix(in srgb, ${col} ${Math.round(6 + k * 44)}%, transparent)` }} title={t('vn.cellTip', { rate: locDec((c.rate * 100).toFixed(4)), h: c.hours })}>
                    <em>{sign(c.rate8h, 4)}%</em>
                    <small>{t('vn.period', { h: c.hours })}</small>
                  </span>
                )
              })}
              <span className={r === top ? 'vn-n hot' : 'vn-n'}>{locDec(r.gap_8h.toFixed(4))}%</span>
              <span className="vn-n">{locDec(r.spread_bps.toFixed(1))}</span>
            </button>
          ))}
        </div>
        <p className="vn-foot"><span className="vn-w">{t('vn.foot')}</span><span className="vn-n2">{t('vn.footS')}</span>{m?.missing?.length ? ` ${t('vn.missing', { v: m.missing.map((x) => NAMES[x] ?? x).join(listSep()) })}` : ''}</p>
      </section>
      <section className="vn-band">
        <div className="vn-head">
          <h2>{t('vn.band', { coin })}</h2>
          <Seg label={t('vn.coin')} value={coin} onChange={setCoin} options={rows.map((r) => ({ value: r.coin, label: r.coin }))} />
        </div>
        <SpreadBand sp={sp} venues={venues.slice(1).filter((v) => row?.cells[v])} />
      </section>
    </div>
  )
}

function SpreadBand({ sp, venues }: { sp: Spread | null; venues: string[] }) {
  const t = useT()
  const lang = useLang()
  const [mouse, setMouse] = useState<{ x: number } | null>(null)
  const box = useRef({ x0: 0, x1: 0 })
  const [say, setSay] = useState('')
  const { ref } = useSurface((g) => {
    if (!sp || sp.t.length < 2) {
      g.text(t('vn.waiting'), g.w / 2, g.h / 2, { size: 12, color: C.t3, align: 'center' })
      return
    }
    const ctx = g.ctx
    const x0 = 12, x1 = g.w - 56, y0 = 10, y1 = g.h - 24
    box.current = { x0, x1 }
    const ta = sp.t[0], tb = sp.t[sp.t.length - 1]
    const X = (v: number) => x0 + ((v - ta) / Math.max(1, tb - ta)) * (x1 - x0)
    // 每个时刻的最高、最低（币安自己是 0，一直算在里面）
    const n = sp.t.length
    const hi = new Array<number>(n).fill(0), lo = new Array<number>(n).fill(0)
    // 各家的标记价不是同一瞬间采的，一分钟内会有一两个基点的抖动。图上画平滑后的，十字线读原始值。
    const smooth: Record<string, (number | null)[]> = {}
    for (const v of venues) {
      const s = sp.series[v]
      if (!s) continue
      const out = new Array<number | null>(n).fill(null)
      let e: number | null = null
      for (let i = 0; i < n; i++) {
        const x = s[i]
        if (x == null) continue
        e = e == null || (i > 0 && sp.t[i] - sp.t[i - 1] > 180_000) ? x : e + (x - e) * (sp.t[i] - sp.t[i - 1] < 30_000 ? 0.08 : 0.3)
        out[i] = e
        if (e > hi[i]) hi[i] = e
        if (e < lo[i]) lo[i] = e
      }
      smooth[v] = out
    }
    const sorted = hi.map((h, i) => h - lo[i]).sort((a, b) => a - b)
    const typical = sorted[Math.floor(sorted.length / 2)] ?? 0
    const q = sorted[Math.floor(sorted.length * 0.99)] ?? 1
    // 纵轴按 99% 的数据定，个别尖峰不把图压扁
    let top = 1, bot = -1
    for (let i = 0; i < n; i++) { if (hi[i] - lo[i] <= q * 1.05) { top = Math.max(top, hi[i]); bot = Math.min(bot, lo[i]) } }
    top *= 1.15; bot *= 1.15
    const Y = (v: number) => y0 + ((top - Math.max(bot, Math.min(top, v))) / (top - bot)) * (y1 - y0)
    // 刻度
    const step = niceBps((top - bot) / 4)
    for (let v = Math.ceil(bot / step) * step; v <= top; v += step) {
      g.hline(Y(v), x0, x1, Math.abs(v) < 1e-6 ? C.rule3 : C.grid)
      g.hline(Y(v), x1, x1 + 4, C.rule3)
      g.text(sign(v, step < 1 ? 1 : 0), x1 + 8, Y(v), { size: 11, color: Math.abs(v) < 1e-6 ? C.t1 : C.t3, baseline: 'middle' })
    }
    const hours = (tb - ta) / 3_600_000
    const tickH = hours > 3 ? 1 : 0.5
    const first = Math.ceil(ta / (tickH * 3_600_000)) * tickH * 3_600_000
    for (let v = first; v <= tb; v += tickH * 3_600_000) {
      g.vline(X(v), y1, y1 + 4, C.rule3)
      g.text(clock(v, false), X(v), y1 + 15, { size: 11, color: C.t3, align: 'center' })
    }
    // 带子：相邻两个点隔太久（后端重启）就断开
    const gapMs = 180_000
    const widen = Math.max(typical * 2.5, 3)
    ctx.beginPath()
    let opened = false, start = 0
    const closeBand = (end: number) => {
      for (let j = end; j >= start; j--) ctx.lineTo(g.X(X(sp.t[j])), g.Y(Y(lo[j])))
      ctx.closePath()
    }
    for (let i = 0; i < n; i++) {
      if (opened && sp.t[i] - sp.t[i - 1] > gapMs) { closeBand(i - 1); opened = false }
      if (!opened) { ctx.moveTo(g.X(X(sp.t[i])), g.Y(Y(hi[i]))); start = i; opened = true } else ctx.lineTo(g.X(X(sp.t[i])), g.Y(Y(hi[i])))
    }
    if (opened) closeBand(n - 1)
    ctx.fillStyle = 'rgba(255,255,255,0.13)'
    ctx.fill()
    // 变宽的时段：琥珀色
    let hot = 0, lastHot = 0
    ctx.fillStyle = 'rgba(212,164,76,0.5)'
    for (let i = 1; i < n; i++) {
      if (hi[i] - lo[i] < widen || sp.t[i] - sp.t[i - 1] > gapMs) continue
      const a = g.X(X(sp.t[i - 1])), b = g.X(X(sp.t[i]))
      ctx.fillRect(a, g.Y(Y(hi[i])), Math.max(1, b - a), Math.max(1, g.Y(Y(lo[i])) - g.Y(Y(hi[i]))))
      if (sp.t[i] > lastHot + 300_000) hot++
      lastHot = sp.t[i]
    }
    // 每家一条线
    for (const v of venues) {
      const s = smooth[v]
      if (!s) continue
      ctx.beginPath()
      let down = false, prev = 0
      for (let i = 0; i < n; i++) {
        const x = s[i]
        if (x == null || (down && sp.t[i] - prev > gapMs)) { down = false; if (x == null) continue }
        if (!down) { ctx.moveTo(g.X(X(sp.t[i])), g.Y(Y(x))); down = true } else ctx.lineTo(g.X(X(sp.t[i])), g.Y(Y(x)))
        prev = sp.t[i]
      }
      ctx.strokeStyle = LINE[v] ?? C.t2
      ctx.lineWidth = g.px(v === venues[0] ? 1.4 : 1)
      ctx.lineJoin = 'round'
      ctx.stroke()
    }
    // 右端的名字和现在的差
    const placed: number[] = []
    for (const v of venues) {
      const x = smooth[v]?.[n - 1]
      if (x == null) continue
      let y = Y(x)
      while (placed.some((p) => Math.abs(p - y) < 13)) y += 13
      placed.push(y)
      g.text(`${NAMES[v] ?? v} ${sign(x, 1)}`, x1 - 6, y - 8, { size: 11, color: LINE[v] ?? C.t2, align: 'right', plate: { color: 'rgba(0,0,0,0.72)', padX: 3, padY: 1 } })
    }
    g.text(t('vn.zero'), x0 + 4, Y(0) - 8, { size: 11, color: C.t3 })
    // 十字线
    if (mouse) {
      const f = (mouse.x - x0) / Math.max(1, x1 - x0)
      const tt = ta + f * (tb - ta)
      let i = 0
      while (i < n - 1 && sp.t[i] < tt) i++
      const xx = X(sp.t[i])
      g.vline(xx, y0, y1, C.rule3)
      let yy = y0 + 4
      g.text(clock(sp.t[i], false), xx + (xx > g.w / 2 ? -8 : 8), yy, { size: 11, color: C.t1, align: xx > g.w / 2 ? 'right' : 'left', baseline: 'top', plate: { color: 'rgba(0,0,0,0.8)', padX: 3, padY: 1 } })
      for (const v of venues) {
        const x = sp.series[v]?.[i]
        if (x == null) continue
        yy += 17
        g.text(`${NAMES[v] ?? v} ${sign(x, 1)}`, xx + (xx > g.w / 2 ? -8 : 8), yy, { size: 11, color: LINE[v] ?? C.t2, align: xx > g.w / 2 ? 'right' : 'left', baseline: 'top', plate: { color: 'rgba(0,0,0,0.8)', padX: 3, padY: 1 } })
      }
    }
    const nowW = hi[n - 1] - lo[n - 1]
    const line = hot > 0 ? t('vn.bandSay', { now: locDec(nowW.toFixed(1)), typ: locDec(typical.toFixed(1)), n: hot }) : t('vn.bandCalm', { now: locDec(nowW.toFixed(1)), typ: locDec(typical.toFixed(1)) })
    if (line !== sayRef.current) { sayRef.current = line; window.setTimeout(() => setSay(line), 0) }
  }, [sp, venues, mouse, lang])
  const sayRef = useRef('')
  return (
    <>
      <p className="vn-say">{say || ' '}</p>
      <div className="vn-chart">
        <canvas ref={ref} onMouseMove={(e) => { const r = e.currentTarget.getBoundingClientRect(); setMouse({ x: e.clientX - r.left }) }} onMouseLeave={() => setMouse(null)} />
      </div>
      <p className="vn-foot">{t('vn.bandFoot')}</p>
    </>
  )
}

function niceBps(raw: number): number {
  for (const s of [0.5, 1, 2, 5, 10, 20, 50, 100, 200, 500]) if (raw <= s) return s
  return 1000
}
