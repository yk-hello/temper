// 回测（D95）：把一份信号表放到历史行情上，用模拟盘同一个撮合引擎跑。
// 左边写信号和条件，右边看结果：汇总数字、权益曲线（叠"一直拿着"和回撤）、每笔交易、每条信号的结果。
import { useMemo, useState } from 'react'
import { useT, errText, type Key } from '../../lib/i18n'
import { api, ApiError } from '../../lib/session'
import { fmtMs, rttOf, useLatency } from '../../lib/latency'
import { locDec, numLocale } from '../../lib/tz'
import { NavChart } from '../../charts/NavChart'
import { Seg } from '../../ui/Seg'
import { FormError } from '../../ui/Field'
import { numIn } from '../../lib/format'

interface BtTrade { id: string; instrument: string; side: 'long' | 'short'; open_time: number; close_time: number; hold_ms: number; qty: number; entry: number; exit: number; fees: number; funding: number; net: number }
interface BtOutcome { i: number; t: number; exec: number; ok: boolean; code?: string; params?: Record<string, string> }
interface BtResult {
  instruments: string[]; from: number; to: number; initial: number; final: number
  stats: { return: number; max_drawdown: number; sharpe: number | null; trades: number; wins: number; win_rate: number | null; days: number }
  journal: { n: number; profit_factor: number | null; expectancy: number; payoff: number | null }
  fees: number; funding: number; liq_fees: number; liquidations: number
  equity: { t: number; equity: number; price: number }[]
  trades: BtTrade[]; outcomes: BtOutcome[]; bars: number; notes: string[]; elapsed_ms: number
}

const DAY = 86_400_000
const isoDay = (ms: number) => new Date(ms).toISOString().slice(0, 10)
// 表里的时间和信号表一样按 UTC 写：11-06 02:00（带秒：11-06 02:00:00.120）
const utc = (ms: number, sec = false) => new Date(ms).toISOString().slice(5, sec ? 23 : 16).replace('T', ' ').replace(/\.000$/, '')
const pct = (v: number | null | undefined, d = 2) => (v == null ? '—' : (v > 0 ? '+' : v < 0 ? '−' : '') + locDec(Math.abs(v * 100).toFixed(d)) + '%')
const money = (v: number) => (v > 0 ? '+' : v < 0 ? '−' : '') + Math.abs(v).toLocaleString(numLocale(), { minimumFractionDigits: 2, maximumFractionDigits: 2 })
const dir = (v: number) => (v > 0 ? 'up' : v < 0 ? 'down' : '')

export default function Backtest() {
  const t = useT()
  const lat = useLatency()
  const today = Math.floor(Date.now() / DAY) * DAY
  const [from, setFrom] = useState(isoDay(today - 7 * DAY))
  const [to, setTo] = useState(isoDay(today))
  const [lines, setLines] = useState('')
  const [initial, setInitial] = useState('10000')
  const [lev, setLev] = useState('10')
  const [mode, setMode] = useState<'cross' | 'isolated'>('cross')
  const [latMs, setLatMs] = useState<string | null>(null) // null = 按我的模拟下单延迟
  const [slip, setSlip] = useState('0')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string>()
  const [res, setRes] = useState<BtResult | null>(null)
  const [view, setView] = useState<'trades' | 'signals'>('trades')
  const myRtt = rttOf(lat)
  const latency = latMs ?? (myRtt != null ? String(Math.round(myRtt)) : '0')

  const run = async () => {
    setBusy(true); setErr(undefined)
    try {
      const r = await api<BtResult>('POST', '/api/backtest', {
        lines, from: Date.parse(from + 'T00:00:00Z'), to: Date.parse(to + 'T00:00:00Z') + DAY, initial, leverage: Number(lev) || 10, mode,
        latency_ms: Number(latency) || 0, slippage_bps: Number(slip) || 0,
      })
      setRes(r)
      setView('trades')
    } catch (e) {
      setErr(e instanceof ApiError ? errText(e.code, e.params) : errText('INTERNAL_ERROR'))
    } finally { setBusy(false) }
  }
  const rejected = res?.outcomes.filter((o) => !o.ok).length ?? 0
  const base = res?.instruments[0]?.replace(/-PERP$/, '') ?? 'BTC'
  const curve = useMemo(() => res?.equity.map((p) => ({ t: p.t, nav: p.equity })) ?? [], [res])
  const trades = useMemo(() => [...(res?.trades ?? [])].sort((a, b) => a.open_time - b.open_time), [res]) // 按时间先后
  const hold = useMemo(() => res?.equity.filter((p) => p.price > 0).map((p) => ({ t: p.t, c: p.price })) ?? [], [res])

  return (
    <div className="bt">
      <form className="bt-form mk-scroll" onSubmit={(e) => { e.preventDefault(); if (!busy) void run() }} data-bt="form">
        <label className="rv-f"><span>{t('btx.signals')}</span>
          <textarea className="rv-in bt-lines" rows={9} spellCheck={false} value={lines} placeholder={t('btx.placeholder')} onChange={(e) => setLines(e.target.value)} data-bt="lines" />
        </label>
        <p className="bt-help">{t('btx.help')}</p>
        <div className="bt-grid">
          <label className="rv-f"><span>{t('btx.from')}</span><input className="rv-in bt-date" type="date" value={from} max={to} onChange={(e) => setFrom(e.target.value)} data-bt="from" /></label>
          <label className="rv-f"><span>{t('btx.to')}</span><input className="rv-in bt-date" type="date" value={to} min={from} max={isoDay(today)} onChange={(e) => setTo(e.target.value)} data-bt="to" /></label>
          <label className="rv-f"><span>{t('btx.initial')}</span><input className="rv-in num" inputMode="decimal" value={initial} onChange={(e) => setInitial(numIn(e.target.value))} /></label>
          <label className="rv-f"><span>{t('btx.leverage')}</span><input className="rv-in num" inputMode="numeric" value={lev} onChange={(e) => setLev(e.target.value.replace(/\D/g, ''))} /></label>
          <label className="rv-f"><span title={t('btx.latencyHint')}>{t('btx.latency')}</span><input className="rv-in num" inputMode="numeric" value={latency} onChange={(e) => setLatMs(e.target.value.replace(/\D/g, ''))} data-bt="latency" /></label>
          <label className="rv-f"><span title={t('btx.slipHint')}>{t('btx.slip')}</span><input className="rv-in num" inputMode="decimal" value={slip} onChange={(e) => setSlip(numIn(e.target.value))} /></label>
        </div>
        <Seg label={t('btx.mode')} value={mode} onChange={setMode} options={[{ value: 'cross', label: t('tr.cross') }, { value: 'isolated', label: t('tr.isolated') }]} />
        <FormError text={err} />
        <button className="btn primary bt-run" disabled={busy || !lines.trim()} data-bt="run">{busy && <span className="spin" aria-hidden />}{t(busy ? 'btx.running' : 'btx.run')}</button>
        <p className="bt-help t3">{t('btx.engineNote')}</p>
      </form>

      <section className="bt-out" data-bt="out">
        {!res && <div className="rv-out"><p>{t(busy ? 'btx.downloading' : 'btx.empty')}</p></div>}
        {res && (
          <>
            <dl className="bt-tiles" data-bt="tiles">
              <div><dt>{t('btx.k.return')}</dt><dd className={`num ${dir(res.stats.return)}`}>{pct(res.stats.return)}</dd><small className="num">{money(res.final - res.initial)} USDT</small></div>
              <div><dt>{t('btx.k.dd')}</dt><dd className="num">{pct(-res.stats.max_drawdown)}</dd></div>
              <div><dt>{t('btx.k.trades')}</dt><dd className="num">{res.stats.trades}</dd><small className="num">{t('btx.k.winRate', { v: res.stats.win_rate == null ? '—' : (res.stats.win_rate * 100).toFixed(0) + '%' })}</small></div>
              <div><dt>{t('btx.k.pf')}</dt><dd className="num">{res.journal.profit_factor == null ? '—' : locDec(res.journal.profit_factor.toFixed(2))}</dd><small className="num">{t('btx.k.exp', { v: money(res.journal.expectancy) })}</small></div>
              <div><dt>{t('btx.k.sharpe')}</dt><dd className="num">{res.stats.sharpe == null ? '—' : locDec(res.stats.sharpe.toFixed(2))}</dd></div>
              <div><dt>{t('btx.k.costs')}</dt><dd className="num">{money(-res.fees)}</dd><small className="num">{t('btx.k.funding', { v: money(res.funding) })}</small></div>
              <div><dt>{t('btx.k.liq')}</dt><dd className={`num ${res.liquidations ? 'down' : ''}`}>{res.liquidations}</dd>{res.liq_fees > 0 && <small className="num">{t('btx.k.liqFee', { v: money(-res.liq_fees) })}</small>}</div>
            </dl>
            {/* 图例：和策略页一样（11-16 走查：往下走的灰线没写是什么） */}
            <div className="bt-legend" data-bt="legend">
              <span><i className="bt-lg self" />{t('ai.nav')}</span>
              <span><i className="bt-lg btc" />{t('btx.hold', { coin: base })}</span>
              <span><i className="bt-lg dd" />{t('ai.ddLegend')}</span>
            </div>
            <div className="bt-chart"><NavChart curve={curve} initial={res.initial} btc={hold} holdLabel={t('btx.hold', { coin: base })} /></div>
            <div className="bt-tabs">
              <Seg label={t('btx.view')} value={view} onChange={setView} options={[{ value: 'trades', label: t('btx.v.trades', { n: res.trades.length }) }, { value: 'signals', label: t('btx.v.signals', { n: res.outcomes.length, bad: rejected }) }]} />
              <span className="t3 bt-meta num">{t('btx.meta', { bars: res.bars.toLocaleString(numLocale()), took: res.elapsed_ms < 1000 ? t('lat.ms', { v: fmtMs(res.elapsed_ms) }) : t('btx.sec', { v: locDec((res.elapsed_ms / 1000).toFixed(1)) }) })}</span>
            </div>
            <div className="bt-tbl-wrap mk-scroll">
              {view === 'trades' ? (
                <table className="tbl bt-tbl" data-bt="trades">
                  <thead><tr><th>{t('col.inst')}</th><th>{t('btx.c.open')}</th><th>{t('btx.c.close')}</th><th className="r">{t('col.qty')}</th><th className="r">{t('btx.c.entry')}</th><th className="r">{t('btx.c.exit')}</th><th className="r">{t('btx.c.costs')}</th><th className="r">{t('btx.c.net')}</th></tr></thead>
                  <tbody>{trades.map((x) => (
                    <tr key={x.id}>
                      <td><span className="sym">{x.instrument.replace(/-PERP$/, '')}</span><span className={`tl-tag ${x.side === 'long' ? 'up' : 'down'}`}>{t(x.side === 'long' ? 'side.long' : 'side.short')}</span></td>
                      <td className="num t2">{utc(x.open_time)}</td>
                      <td className="num t2">{x.close_time ? utc(x.close_time) : t('btx.stillOpen')}</td>
                      <td className="num r">{locDec(String(x.qty))}</td>
                      <td className="num r">{x.entry.toLocaleString(numLocale())}</td>
                      <td className="num r">{x.close_time ? x.exit.toLocaleString(numLocale()) : '—'}</td>
                      <td className="num r t2">{money(-x.fees + x.funding)}</td>
                      <td className={`num r ${dir(x.net)}`}>{money(x.net)}</td>
                    </tr>
                  ))}</tbody>
                </table>
              ) : (
                <table className="tbl bt-tbl" data-bt="outcomes">
                  <thead><tr><th>#</th><th>{t('btx.c.sent')}</th><th>{t('btx.c.arrive')}</th><th>{t('btx.c.result')}</th></tr></thead>
                  <tbody>{res.outcomes.map((o) => (
                    <tr key={o.i}>
                      <td className="num t3">{o.i + 1}</td>
                      <td className="num t2">{utc(o.t, true)}</td>
                      <td className="num t2">{o.exec ? utc(o.exec, true) : '—'}</td>
                      <td className={o.ok ? '' : 'warn-t'}>{o.ok ? t('btx.ok') : errText(o.code ?? 'INTERNAL_ERROR', o.params)}</td>
                    </tr>
                  ))}</tbody>
                </table>
              )}
            </div>
            <ul className="bt-notes">{res.notes.map((n) => <li key={n}>{t(`btx.note.${n}` as Key)}</li>)}</ul>
          </>
        )}
      </section>
    </div>
  )
}
