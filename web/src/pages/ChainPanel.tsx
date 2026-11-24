// 链上页（12 的 V.15–V.20）：Hyperliquid 公开的持仓。强平地图、大额持仓、动作流、盈亏分布。
// 顶部写结论和覆盖率：这些只是扫描到的地址，不是全部。
import { useEffect, useState } from 'react'
import { LiqMapChart, type LiqBin } from '../charts/LiqMapChart'
import { fmtNum } from '../charts/scale'
import { clock } from '../lib/gfx'
import { fmtCompactL, useT, type Key } from '../lib/i18n'
import { usePoll } from '../lib/follow'
import { Seg } from '../ui/Seg'
import { locDec, numLocale } from '../lib/tz'

interface LiqMap { bins: LiqBin[]; addresses_scanned: number; positions: number; long_value: number; short_value: number; cycle: number; cycle_end: number; coverage: number; oi: number; center: number }
export interface ChainWhale { address: string; size: number; entry: number; value: number; upnl: number; liq_px: number; all_time_pnl: number; month_pnl: number; leverage: number }
export interface ChainEvent { time: number; address: string; coin: string; kind: 'open' | 'add' | 'reduce' | 'close' | 'flip'; from_size: number; to_size: number; price: number; value: number; lag_ms: number }
interface PnLBucket { lo: number; hi: number; long_value: number; short_value: number; long_n: number; short_n: number }
interface PnLDist { buckets: PnLBucket[]; long_up: number; long_dn: number; short_up: number; short_dn: number }

const shortAddr = (a: string) => `${a.slice(0, 6)}…${a.slice(-4)}`
const signed = (v: number) => `${v >= 0 ? '+' : '−'}${fmtCompactL(Math.abs(v))}`
const explorer = (a: string) => `https://app.hyperliquid.xyz/explorer/address/${a}`

type Pane = 'map' | 'whales' | 'events' | 'pnl'

export function ChainBody({ coin, dec }: { coin: string; dec: number }) {
  const t = useT()
  const map = usePoll<LiqMap>(`/api/onchain/liqmap?coin=${coin}&range=0.2&bins=200`, 30_000)
  const whales = usePoll<ChainWhale[]>(`/api/onchain/whales?coin=${coin}&n=60`, 30_000).data
  const events = usePoll<ChainEvent[]>(`/api/onchain/events?coin=${coin}&min=50000&n=80`, 10_000).data
  const pnl = usePoll<PnLDist>(`/api/onchain/pnl?coin=${coin}`, 60_000).data
  const [pane, setPane] = useState<Pane>('map')
  const [narrow, setNarrow] = useState(() => window.innerWidth < 1100)
  useEffect(() => {
    const f = () => setNarrow(window.innerWidth < 1100)
    window.addEventListener('resize', f)
    return () => window.removeEventListener('resize', f)
  }, [])
  const m = map.data
  if (map.status && map.status !== 200) return <div className="heat-out"><p>{t('cn.off')}</p></div>
  if (m && !m.cycle) return <div className="heat-out"><p>{t('cn.scanning', { n: m.addresses_scanned })}</p></div>
  const mid = m?.center ?? 0
  // 顶部结论：上下最近的大块强平价
  const clusters = (m?.bins ?? []).map((b) => {
    const c = (b.lo + b.hi) / 2
    return c >= mid ? { p: b.short_px || c, v: b.short_value, up: true } : { p: b.long_px || c, v: b.long_value, up: false }
  }).filter((x) => x.v > 0)
  const top = Math.max(0, ...clusters.map((c) => c.v))
  const big = clusters.filter((c) => c.v >= top * 0.25)
  const nearUp = big.filter((c) => c.up).sort((a, b) => a.p - b.p)[0]
  const nearDn = big.filter((c) => !c.up).sort((a, b) => b.p - a.p)[0]
  const pct = (p: number) => { const v = (p / mid - 1) * 100; return `${v > 0 ? '+' : ''}${locDec(v.toFixed(1))}%` }
  const age = m?.cycle_end ? Math.max(0, Math.round((Date.now() - m.cycle_end) / 60000)) : null
  return (
    <div className="flow-wrap cn-wrap">
      <div className="heat-read">
        {nearUp && <span className="hr-cell" title={t('cn.hintUp')}><em>{t('cn.readUp')}</em><b className="num">{fmtNum(nearUp.p, dec >= 1 ? 0 : dec)}</b><b className="num">{fmtCompactL(nearUp.v)}</b><i className="num">{pct(nearUp.p)}</i></span>}
        {nearDn && <span className="hr-cell" title={t('cn.hintDn')}><em>{t('cn.readDn')}</em><b className="num">{fmtNum(nearDn.p, dec >= 1 ? 0 : dec)}</b><b className="num">{fmtCompactL(nearDn.v)}</b><i className="num">{pct(nearDn.p)}</i></span>}
        {m && <span className="hr-cell" title={t('cn.hintCover')}><em>{t('cn.readCover')}</em><b className="num">{m.coverage > 0 ? `${Math.round(m.coverage * 100)}%` : '—'}</b><i>{t('cn.readAddr', { n: m.addresses_scanned })}</i></span>}
        {age != null && <span className="hr-cell hr-flow" title={t('cn.hintAge')}><i>{age < 1 ? t('cn.ageNow') : t('cn.ageMin', { n: age })}</i></span>}
      </div>
      {narrow && (
        <div className="cn-tabs"><Seg label={t('cn.tabs')} value={pane} onChange={setPane} options={(['map', 'whales', 'events', 'pnl'] as const).map((k) => ({ value: k, label: t(`cn.tab.${k}` as Key) }))} /></div>
      )}
      <div className={`cn-grid ${narrow ? 'one' : ''}`}>
        {(!narrow || pane === 'map') && (
          <section className="cn-map">
            <div className="cn-head" title={t('cn.hintMap')}><span>{t('cn.mapTitle')}</span><em>{t('cn.mapSub')}</em></div>
            <div className="cn-canvas">{m && <LiqMapChart bins={m.bins} mid={mid} dec={dec} />}</div>
          </section>
        )}
        {(!narrow || pane === 'whales') && (
          <section className="cn-whales">
            <div className="cn-head"><span>{t('cn.whalesTitle')}</span><em>{t('cn.whalesSub')}</em></div>
            <div className="cn-scroll">
              <table className="cn-table">
                <thead><tr><th>{t('cn.col.side')}</th><th className="r">{t('cn.col.value')}</th><th className="r">{t('cn.col.entry')}</th><th className="r">{t('cn.col.upnl')}</th><th className="r">{t('cn.col.liq')}</th><th className="r" title={t('cn.hintHist')}>{t('cn.col.hist')}</th><th>{t('cn.col.addr')}</th></tr></thead>
                <tbody>
                  {(whales ?? []).map((w) => (
                    <tr key={`${w.address}`}>
                      <td className={w.size > 0 ? 'up' : 'down'}>{t(w.size > 0 ? 'heat.long' : 'heat.short')}</td>
                      <td className="r num" title={`${Math.abs(w.size).toLocaleString(numLocale())} ${coin}`}>{fmtCompactL(w.value)}</td>
                      <td className="r num">{fmtNum(w.entry, dec)}</td>
                      <td className={`r num ${w.upnl >= 0 ? 'up' : 'down'}`}>{signed(w.upnl)}</td>
                      <td className="r num" title={w.liq_px > 0 ? pct(w.liq_px) : undefined}>{w.liq_px > 0 ? fmtNum(w.liq_px, dec) : t('cn.noLiq')}</td>
                      <td className={`r num ${w.all_time_pnl >= 0 ? 'up' : 'down'}`}>{signed(w.all_time_pnl)}</td>
                      <td><a className="cn-addr num" href={explorer(w.address)} target="_blank" rel="noopener noreferrer">{shortAddr(w.address)}</a></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        )}
        {(!narrow || pane === 'pnl') && pnl && <PnLPanel d={pnl} />}
        {(!narrow || pane === 'events') && (
          <section className="cn-events">
            <div className="cn-head" title={t('cn.hintLag')}><span>{t('cn.eventsTitle')}</span><em>{t('cn.eventsSub')}</em></div>
            <div className="cn-scroll">
              {(events ?? []).map((e, i) => {
                const long = e.to_size > 0 || (e.kind === 'close' && e.from_size > 0)
                return (
                  <a className="cn-ev" key={`${e.time}-${e.address}-${i}`} href={explorer(e.address)} target="_blank" rel="noopener noreferrer" title={t('cn.tipLag', { n: Math.round(e.lag_ms / 1000) })}>
                    <span className="num cn-ev-t">{clock(e.time)}</span>
                    <span className={long ? 'up' : 'down'}>{t(`cn.kind.${e.kind}` as Key)}{t(long ? 'heat.long' : 'heat.short')}</span>
                    <span className="num cn-ev-v">{fmtCompactL(e.value)}</span>
                    <span className="num cn-ev-p">{fmtNum(e.price, dec)}</span>
                    <span className="num cn-ev-l">{e.lag_ms >= 120_000 ? t('cn.lagM', { n: Math.ceil(e.lag_ms / 60000) }) : t('cn.lag', { n: Math.round(e.lag_ms / 1000) })}</span>
                  </a>
                )
              })}
              {events && events.length === 0 && <p className="fl-empty">{t('cn.noEvents')}</p>}
            </div>
          </section>
        )}
      </div>
    </div>
  )
}

function PnLPanel({ d }: { d: PnLDist }) {
  const t = useT()
  const longAll = d.long_up + d.long_dn, shortAll = d.short_up + d.short_dn
  const max = Math.max(1, ...d.buckets.map((b) => Math.max(b.long_value, b.short_value)))
  const label = (b: PnLBucket) => (b.lo <= -1e8 ? t('cn.pnlBelow', { n: b.hi }) : b.hi >= 1e8 ? t('cn.pnlAbove', { n: b.lo }) : `${b.lo > 0 ? '+' : ''}${b.lo}% ~ ${b.hi > 0 ? '+' : ''}${b.hi}%`)
  return (
    <section className="cn-pnl">
      <div className="cn-head" title={t('cn.hintPnl')}>
        <span>{t('cn.pnlTitle')}</span>
        <em>{t('cn.pnlSub', { l: longAll > 0 ? Math.round((d.long_dn / longAll) * 100) : 0, s: shortAll > 0 ? Math.round((d.short_dn / shortAll) * 100) : 0 })}</em>
      </div>
      <div className="cn-pnl-rows">
        {[...d.buckets].reverse().map((b) => (
          <div className="cn-pnl-row" key={b.lo}>
            <span className="cn-bar l"><i className="up-bg" style={{ width: `${(b.long_value / max) * 100}%` }} /></span>
            <span className="num cn-pnl-v">{b.long_value >= 1000 ? fmtCompactL(b.long_value) : ''}</span>
            <span className="num cn-pnl-k">{label(b)}</span>
            <span className="num cn-pnl-v r">{b.short_value >= 1000 ? fmtCompactL(b.short_value) : ''}</span>
            <span className="cn-bar"><i className="down-bg" style={{ width: `${(b.short_value / max) * 100}%` }} /></span>
          </div>
        ))}
        <div className="cn-pnl-legend"><span>{t('cn.pnlLong')}</span><span>{t('cn.pnlKey')}</span><span>{t('cn.pnlShort')}</span></div>
      </div>
    </section>
  )
}
