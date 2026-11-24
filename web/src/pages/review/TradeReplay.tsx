// 交易回放（K，12 的 5.9）：回到当时的 K 线、频谱和盘口，叠上买卖点和笔记，能播放、暂停、调速。
// 录制之前的交易没有频谱，只有 K 线，写明白。
import { useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Scope, type TradeLine, type TradeOverlay } from '../../charts/Scope'
import { HeatChart } from '../../charts/HeatChart'
import { Seg } from '../../ui/Seg'
import { clock } from '../../lib/gfx'
import { baseOf, priceDecimals } from '../../lib/format'
import { fmtTime, useT } from '../../lib/i18n'
import type { Candle } from '../../lib/demo'
import type { JTrade } from '../../lib/journal'

const SPEEDS = [1, 5, 20, 60, 300] as const

export default function TradeReplay({ trade: x, onClose }: { trade: JTrade; onClose: () => void }) {
  const t = useT()
  const end = x.close_time || Date.now()
  const hold = end - x.open_time
  const pad = Math.max(60_000, hold * 0.25)
  const from = x.open_time - pad, to = end + pad
  const iv = hold < 45 * 60_000 ? 60_000 : hold < 8 * 3600_000 ? 300_000 : 3600_000
  const ivKey = iv === 60_000 ? '1m' : iv === 300_000 ? '5m' : '1h'
  const [bars, setBars] = useState<Candle[]>([])
  const [hasHeat, setHasHeat] = useState<boolean | null>(null)
  const [at, setAt] = useState(from)
  const [playing, setPlaying] = useState(false)
  const [speed, setSpeed] = useState<number>(20)
  const dec = priceDecimals(x.entry)
  useEffect(() => {
    let alive = true
    fetch(`/api/candles?instrument=${x.instrument}&interval=${ivKey}&before=${Math.ceil(to / iv + 2) * iv}&limit=600`).then((r) => r.json()).then((d: Candle[]) => { if (alive) setBars(d ?? []) }).catch(() => {})
    fetch(`/api/heat/history?instrument=${x.instrument}&from=${Math.round(from)}&to=${Math.round(to)}&cols=60&rows=60`).then((r) => (r.ok ? r.json() : null)).then((d) => { if (alive) setHasHeat(!!(d && d.rows > 0)) }).catch(() => { if (alive) setHasHeat(false) })
    return () => { alive = false }
  }, [x.instrument]) // eslint-disable-line react-hooks/exhaustive-deps
  // 播放：真实时间乘以倍速
  const last = useRef(0)
  useEffect(() => {
    if (!playing) return
    let raf = 0
    last.current = performance.now()
    const tick = (now: number) => {
      const dt = now - last.current
      last.current = now
      let stop = false
      setAt((v) => { const n = v + dt * speed; if (n >= to) { stop = true; return to } return n })
      if (stop) setPlaying(false)
      else raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(raf)
  }, [playing, speed, to])
  useEffect(() => {
    const k = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
      if (e.key === ' ') { e.preventDefault(); setPlaying((p) => !p) }
    }
    window.addEventListener('keydown', k)
    return () => window.removeEventListener('keydown', k)
  }, [onClose])
  // K 线只显示到播放头：不能提前看到后面
  const shown = useMemo(() => {
    const out: Candle[] = []
    for (const b of bars) {
      if (b.t > at) break
      out.push(b.t + iv <= at ? b : { ...b, c: b.c })
    }
    return out
  }, [bars, at, iv])
  const legs = x.legs.filter((l) => l.t <= at)
  const overlay = useMemo<TradeOverlay>(() => {
    const lines: TradeLine[] = []
    if (at >= x.open_time) lines.push({ id: 'entry', kind: 'entry', price: x.entry, buy: x.side === 'long', label: t('rv.d.entry') })
    const stop = [...x.stops].reverse().find((s) => s.t <= at)
    if (stop && stop.kind !== 'cancel') lines.push({ id: 'sl', kind: 'sl', price: stop.px, buy: x.side !== 'long', label: t('rv.d.stop') })
    return {
      lines,
      markers: legs.map((l, i) => ({ id: `${l.t}-${i}`, t: l.t, price: l.px, buy: l.side === 'buy', say: `${t(l.side === 'buy' ? 'rv.buy' : 'rv.sell')} ${l.qty}`, hint: `${fmtTime(l.t, { seconds: true })}  ${l.px}` })),
    }
  }, [at, legs.length]) // eslint-disable-line react-hooks/exhaustive-deps
  const marks = useMemo(() => x.legs.map((l) => ({ t: l.t, p: l.px, buy: l.side === 'buy', label: `${t(l.side === 'buy' ? 'rv.buy' : 'rv.sell')} ${l.qty}` })), [x.legs]) // eslint-disable-line react-hooks/exhaustive-deps
  const pct = ((at - from) / (to - from)) * 100
  return createPortal(
    <div className="rv-replay" role="dialog" aria-label={t('rv.replay')}>
      <div className="rv-r-head">
        <b>{baseOf(x.instrument)}</b>
        <span className={x.side === 'long' ? 'up' : 'down'}>{t(x.side === 'long' ? 'heat.long' : 'heat.short')}</span>
        <span className="num rv-r-clock" data-rv="clock">{fmtTime(at, { seconds: true })}</span>
        <button type="button" className="btn primary sm" data-rv="play" onClick={() => { if (at >= to) setAt(from); setPlaying(!playing) }}>{t(playing ? 'rv.pause' : 'rv.play')}</button>
        <Seg label={t('rv.speed')} value={String(speed)} onChange={(v) => setSpeed(Number(v))} options={SPEEDS.map((s) => ({ value: String(s), label: `${s}×` }))} />
        <button type="button" className="icon-btn rv-r-x" onClick={onClose} aria-label={t('common.close')}>×</button>
      </div>
      <div className="rv-r-track" onPointerDown={(e) => {
        const box = e.currentTarget.getBoundingClientRect()
        const seek = (cx: number) => setAt(from + Math.max(0, Math.min(1, (cx - box.left) / box.width)) * (to - from))
        seek(e.clientX)
        setPlaying(false)
        const mv = (ev: PointerEvent) => seek(ev.clientX)
        const up = () => { window.removeEventListener('pointermove', mv); window.removeEventListener('pointerup', up) }
        window.addEventListener('pointermove', mv)
        window.addEventListener('pointerup', up)
      }}>
        <i className="rv-r-fill" style={{ width: `${pct}%` }} />
        {x.legs.map((l, i) => <i key={i} className={`rv-r-mark ${l.side === 'buy' ? 'up-bg' : 'down-bg'}`} style={{ left: `${((l.t - from) / (to - from)) * 100}%` }} title={`${clock(l.t)} ${l.px}`} />)}
      </div>
      <div className={`rv-r-body ${hasHeat ? '' : 'one'}`}>
        <section className="rv-r-cell">
          <div className="cn-head"><span>{t('rv.r.kline')}</span></div>
          <div className="cn-canvas"><Scope key={ivKey} mode="candle" candles={shown} interval={iv} span={to - from} t1={to} dec={dec} volume version={Math.floor(at / 1000)} trade={overlay} /></div>
        </section>
        {hasHeat && (
          <section className="rv-r-cell">
            <div className="cn-head"><span>{t('rv.r.heat')}</span><em>{t('rv.r.heatSub')}</em></div>
            <div className="cn-canvas"><HeatChart inst={x.instrument} dec={dec} span={to - from} layers={{ events: true, chain: false, whales: false, own: false }} replay={{ from, to, marks, at }} /></div>
          </section>
        )}
      </div>
      <div className="heat-note rv-r-foot">
        {hasHeat === false && <span>{t('rv.r.noHeat')}</span>}
        {x.note?.note && <span className="rv-r-note"><em>{t('rv.d.note')}</em>{x.note.note}</span>}
        <span className="heat-note-r">{t('rv.r.keys')}</span>
      </div>
    </div>,
    document.body,
  )
}
