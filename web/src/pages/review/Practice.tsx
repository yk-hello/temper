// 历史回放练习（12 的 5.11）：选一段历史逐根播放，能下练习单。所有东西只在这个页面里，不进任何账户。
import { useEffect, useMemo, useState } from 'react'
import { Scope, type TradeLine, type TradeOverlay } from '../../charts/Scope'
import { Seg } from '../../ui/Seg'
import { Money } from '../../ui/Money'
import { baseOf, fmtPrice, priceDecimals } from '../../lib/format'
import { fmtTime, useT, type Key } from '../../lib/i18n'
import { PRACTICE_FEE, practicePnl } from '../../lib/journal'
import type { Candle } from '../../lib/demo'
import { fmtSigned } from '../trade/OrderPanel'

const COINS = ['BTC-PERP', 'ETH-PERP', 'SOL-PERP', 'BNB-PERP', 'XRP-PERP', 'DOGE-PERP']
const IVS = [{ value: '1m', ms: 60_000 }, { value: '5m', ms: 300_000 }, { value: '15m', ms: 900_000 }, { value: '1h', ms: 3_600_000 }]
const CONTEXT = 120 // 开始之前先给这么多根做背景

interface Pos { side: 'long' | 'short'; entry: number; notional: number; t: number }
interface Done extends Pos { exit: number; closeT: number; net: number; fee: number }

export default function Practice() {
  const t = useT()
  const [inst, setInst] = useState('BTC-PERP')
  const [ivKey, setIvKey] = useState('5m')
  const iv = IVS.find((x) => x.value === ivKey)!
  const [startDay, setStartDay] = useState(() => { const d = new Date(Date.now() - 14 * 86400_000); return d.toISOString().slice(0, 10) })
  const [bars, setBars] = useState<Candle[]>([])
  const [n, setN] = useState(CONTEXT)
  const [playing, setPlaying] = useState(false)
  const [speed, setSpeed] = useState(2)
  const [notional, setNotional] = useState('1000')
  const [pos, setPos] = useState<Pos | null>(null)
  const [done, setDone] = useState<Done[]>([])
  const [error, setError] = useState(false)
  useEffect(() => {
    let alive = true
    setPlaying(false); setPos(null); setDone([]); setN(CONTEXT); setBars([]); setError(false)
    const start = Date.parse(`${startDay}T00:00:00Z`)
    if (!Number.isFinite(start)) return
    const before = start + 800 * iv.ms
    fetch(`/api/candles?instrument=${inst}&interval=${iv.value}&before=${before}&limit=1000`).then((r) => r.json()).then((d: Candle[]) => {
      if (!alive) return
      const list = (d ?? []).filter((b) => b.t < Date.now())
      setBars(list)
      setError(list.length < 10)
      setN(Math.min(Math.max(Math.min(CONTEXT, list.length >> 1), list.findIndex((b) => b.t >= start)), Math.max(0, list.length - 1)))
    }).catch(() => { if (alive) setError(true) })
    return () => { alive = false }
  }, [inst, iv.value, iv.ms, startDay])
  useEffect(() => {
    if (!playing) return
    const h = window.setInterval(() => setN((v) => { if (v + 1 >= bars.length) { setPlaying(false); return v } return v + 1 }), 1000 / speed)
    return () => clearInterval(h)
  }, [playing, speed, bars.length])
  const shown = useMemo(() => bars.slice(0, n + 1), [bars, n])
  const cur = shown[shown.length - 1]
  const dec = cur ? priceDecimals(cur.c) : 1
  const open = (side: 'long' | 'short') => {
    if (!cur || pos) return
    const v = Number(notional)
    if (!(v > 0)) return
    setPos({ side, entry: cur.c, notional: v, t: cur.t })
  }
  const close = () => {
    if (!pos || !cur) return
    const r = practicePnl(pos.side, pos.entry, cur.c, pos.notional)
    setDone((d) => [{ ...pos, exit: cur.c, closeT: cur.t, net: r.net, fee: r.fee }, ...d])
    setPos(null)
  }
  const live = pos && cur ? practicePnl(pos.side, pos.entry, cur.c, pos.notional) : null
  const total = done.reduce((a, x) => a + x.net, 0)
  const wins = done.filter((x) => x.net > 0).length
  const overlay = useMemo<TradeOverlay>(() => {
    const lines: TradeLine[] = pos ? [{ id: 'p', kind: 'entry', price: pos.entry, buy: pos.side === 'long', label: t('rv.p.pos') }] : []
    return { lines, markers: done.flatMap((d, i) => [
      { id: `o${i}`, t: d.t, price: d.entry, buy: d.side === 'long', say: t('rv.p.open'), hint: '' },
      { id: `c${i}`, t: d.closeT, price: d.exit, buy: d.side !== 'long', say: fmtSigned(d.net), hint: '', tone: d.net >= 0 ? 'up' as const : 'down' as const },
    ]) }
  }, [pos, done]) // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <div className="rv-prac">
      <div className="heat-read rv-read rv-prac-bar">
        <Seg label={t('rv.p.coin')} value={inst} onChange={setInst} options={COINS.map((x) => ({ value: x, label: baseOf(x) }))} />
        <Seg label={t('rv.p.iv')} value={ivKey} onChange={setIvKey} options={IVS.map((x) => ({ value: x.value, label: t(`mk.iv.${x.value}` as Key) }))} />
        <label className="rv-prac-date"><em>{t('rv.p.start')}</em><input className="rv-in" type="date" value={startDay} data-rv="p-date" max={new Date().toISOString().slice(0, 10)} onChange={(e) => setStartDay(e.target.value)} /></label>
        <span className="hr-cell hr-flow" title={t('rv.p.hint')}><i>{t('rv.p.isolated')}</i></span>
      </div>
      <div className="rv-prac-main">
        <div className="cn-canvas"><Scope key={`${inst}-${ivKey}`} mode="candle" candles={shown} interval={iv.ms} span={iv.ms * 80} t1={(cur?.t ?? 0) + iv.ms * 6} dec={dec} version={n} trade={overlay} /></div>
        <aside className="rv-prac-side">
          <div className="rv-p-clock num" data-rv="p-clock">{cur ? fmtTime(cur.t) : '—'}</div>
          <div className="rv-p-row">
            <button type="button" className="btn primary sm" data-rv="p-play" disabled={!bars.length} onClick={() => setPlaying(!playing)}>{t(playing ? 'rv.pause' : 'rv.play')}</button>
            <button type="button" className="btn secondary sm" data-rv="p-next" disabled={playing || n + 1 >= bars.length} onClick={() => setN(n + 1)}>{t('rv.p.next')}</button>
          </div>
          <Seg label={t('rv.speed')} value={String(speed)} onChange={(v) => setSpeed(Number(v))} options={[1, 2, 5, 10].map((s) => ({ value: String(s), label: t('rv.p.perSec', { n: s }) }))} />
          <label className="rv-f"><span>{t('rv.p.notional')}</span><input className="rv-in num" inputMode="decimal" value={notional} data-rv="p-size" onChange={(e) => setNotional(e.target.value)} /></label>
          {!pos ? (
            <div className="rv-p-row">
              <button type="button" className="btn rv-buy sm" data-rv="p-long" disabled={!cur} onClick={() => open('long')}>{t('rv.p.long')}</button>
              <button type="button" className="btn rv-sell sm" data-rv="p-short" disabled={!cur} onClick={() => open('short')}>{t('rv.p.short')}</button>
            </div>
          ) : (
            <>
              <dl className="rv-kv">
                <dt>{t('rv.c.side')}</dt><dd className={pos.side === 'long' ? 'up' : 'down'}>{t(pos.side === 'long' ? 'heat.long' : 'heat.short')}</dd>
                <dt>{t('rv.d.entry')}</dt><dd className="num">{fmtPrice(String(pos.entry))}</dd>
                <dt>{t('rv.p.float')}</dt><dd className={`num ${live && live.net >= 0 ? 'up' : 'down'}`} data-rv="p-float"><Money text={fmtSigned(live?.net ?? 0)} /></dd>
              </dl>
              <button type="button" className="btn secondary sm" data-rv="p-close" onClick={close}>{t('rv.p.close')}</button>
            </>
          )}
          <div className="rv-p-sum">
            <span><em>{t('rv.s.n')}</em><b className="num" data-rv="p-n">{done.length}</b></span>
            <span><em>{t('rv.s.win')}</em><b className="num">{done.length ? `${Math.round((wins / done.length) * 100)}%` : '—'}</b></span>
            <span><em>{t('rv.s.net')}</em><b className={`num ${total >= 0 ? 'up' : 'down'}`}><Money text={fmtSigned(total)} /></b></span>
          </div>
          <div className="cn-scroll rv-p-list">
            {done.map((d, i) => (
              <div className="rv-p-item" key={i}>
                <span className={d.side === 'long' ? 'up' : 'down'}>{t(d.side === 'long' ? 'heat.long' : 'heat.short')}</span>
                <span className="num">{fmtPrice(String(d.entry))} → {fmtPrice(String(d.exit))}</span>
                <span className={`num ${d.net >= 0 ? 'up' : 'down'}`}><Money text={fmtSigned(d.net)} /></span>
              </div>
            ))}
          </div>
          <p className="rv-p-note">{t('rv.p.fee', { pct: PRACTICE_FEE * 100 })}</p>
        </aside>
      </div>
      {error && <p className="rv-p-err">{t('rv.p.noData')}</p>}
    </div>
  )
}
