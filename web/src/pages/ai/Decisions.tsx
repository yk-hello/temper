// 决策流（3.23）、每笔交易复盘（3.24）、信心校准（3.25）。这些都只给开启服务的人；
// 没开启时写一句话，加"开启跟随"入口，不是空白（3.14）。
import { Fragment, useState } from 'react'
import { useT, useLang, fmtTime, type Key } from '../../lib/i18n'
import { go } from '../../lib/route'
import { useSession } from '../../lib/session'
import { openWizard, usePoll, type Bucket, type ClosedTrade, type Decision, type Execution, type TargetMsg, type TradeDetail } from '../../lib/follow'
import { baseOf, fmtPrice } from '../../lib/format'
import { Money } from '../../ui/Money'
import { fmtAmt, fmtSigned } from '../trade/OrderPanel'
import { useRowsMotion } from './rows'
import { locDec } from '../../lib/tz'

const dirOf = (v: number) => (v > 0 ? 'up' : v < 0 ? 'down' : '')
/** 一位小数；不写 -0.0 */
const oneDec = (v: number) => (Math.abs(v) < 0.05 ? locDec('0.0') : locDec(v.toFixed(1)))

/** 没开启服务时的那句话 */
export function Locked({ what, compact }: { what: Key; compact?: boolean }) {
  const t = useT()
  const s = useSession()
  const out = s.state !== 'in'
  return (
    <div className={`ai-locked ${compact ? 'compact' : ''}`}>
      <p>{t(what)}</p>
      <button type="button" className="btn primary sm" onClick={() => { if (out) go('login&mode=register'); else openWizard() }}>{t(out ? 'ai.signupToFollow' : 'fw.start')}</button>
    </div>
  )
}

export function holdText(t: ReturnType<typeof useT>, ms: number): string {
  const m = Math.round(ms / 60000)
  if (m < 60) return t('ai.holdM', { n: Math.max(1, m) })
  if (m < 1440) return t('ai.holdH', { h: Math.floor(m / 60), m: m % 60 })
  return t('ai.holdD', { d: Math.floor(m / 1440), h: Math.floor((m % 1440) / 60) })
}

function execSentence(t: ReturnType<typeof useT>, e: Execution): string {
  const b = baseOf(e.instrument)
  switch (e.state) {
    case 'filled': return t('dc.filled', { px: fmtPrice(e.avg_price), slip: oneDec(Number(e.slippage_bps ?? 0)), fee: fmtAmt(e.fee, 2) })
    case 'partial': return t('dc.partial', { px: fmtPrice(e.avg_price), w: locDec((Number(e.achieved_weight) * 100).toFixed(1)) })
    case 'pending': return t('dc.pending')
    case 'expired': return t('dc.expired')
    case 'interrupted': return t('dc.interrupted')
    case 'skipped_price_moved': return t('dc.moved', { b })
    case 'skipped_no_change': return t('dc.noChange')
    case 'skipped_min_adjust': return t('dc.small')
    default: return e.state
  }
}

function targetText(t: ReturnType<typeof useT>, w: number): string {
  if (w > 0) return t('sig.long', { w: Math.abs(w * 100).toFixed(0) })
  if (w < 0) return t('sig.short', { w: Math.abs(w * 100).toFixed(0) })
  return t('sig.flat')
}

/** 信心：一条细刻度，白色部分的长度就是把握 */
function Conf({ v }: { v: number }) {
  return <span className="dc-conf num" title={String(v)}><i><b style={{ width: `${Math.min(100, v * 100)}%` }} /></i>{(v * 100).toFixed(0)}%</span>
}

function Rationale({ r }: { r?: TargetMsg['rationale'] }) {
  const t = useT()
  if (!r) return null
  return (
    <>
      {!!r.factors?.length && (
        <div className="dc-factors">
          <span className="dc-l">{t('dc.factors')}</span>
          {r.factors.map((f) => (
            <span className="dc-f" key={f.key}>
              <span>{f.label}</span>
              <i><b className={f.score >= 0 ? 'pos' : 'neg'} style={{ width: `${Math.abs(f.score) * 50}%`, [f.score >= 0 ? 'left' : 'right']: '50%' }} /></i>
              <span className="num t2">{f.score >= 0 ? '+' : '−'}{locDec(Math.abs(f.score).toFixed(2))}</span>
            </span>
          ))}
        </div>
      )}
      {!!r.evidence?.length && (
        <div className="dc-ev">
          <span className="dc-l">{t('dc.evidence')}</span>
          {r.evidence.map((e, i) => (
            <p key={i}>
              {e.url ? <a href={e.url} target="_blank" rel="noreferrer noopener">{e.title}</a> : <b>{e.title}</b>}
              {e.source && <span className="t3"> · {e.source}</span>}
              {e.excerpt && <span className="t2"> {e.excerpt}</span>}
            </p>
          ))}
        </div>
      )}
    </>
  )
}

/** 决策流：心跳不占名额（后端已过滤），新的一条滑进来 */
export function DecisionFeed({ strategy, compact }: { strategy: string; compact?: boolean }) {
  const t = useT()
  const lang = useLang()
  const q = usePoll<Decision[]>(strategy ? `/api/strategies/${strategy}/decisions` : null, 4000)
  const rows = useRowsMotion(q.data ?? [], (d) => d.id)
  const [open, setOpen] = useState<string | null>(null)
  if (q.status === 401 || q.status === 403) return <Locked what="ai.lockedDecisions" compact={compact} />
  if (!q.data) return <div className="mk-empty">{t('sig.reading')}</div>
  if (!q.data.length) return <div className="mk-empty">{t('sig.none')}</div>
  return (
    <div className="dc-list">
      {rows.map(({ item: d, key, state }) => {
        const targets = d.message.targets ?? []
        const isOpen = open === d.id
        if (d.type === 'halt') {
          return (
            <div className={`dc-row ${state ? `tl-${state}` : ''}`} key={key}>
              <div className="dc-main static">
                <span className="t2 dc-time">{fmtTime(d.received_at, { seconds: true })}</span>
                <span className="warn">{t(d.message.action === 'flatten_all' ? 'dc.haltFlat' : 'dc.haltPause')}</span>
                <span className="t2 dc-why">{d.message.reason?.zh}</span>
              </div>
            </div>
          )
        }
        return (
          <Fragment key={key}>
            {targets.map((tg, i) => {
              const w = Number(tg.weight)
              const ex = d.executions.find((e) => e.instrument === tg.instrument)
              const why = tg.rationale?.summary
              const rowKey = `${d.id}/${tg.instrument}`
              return (
                <div className={`dc-row ${state ? `tl-${state}` : ''} ${isOpen ? 'open' : ''}`} key={rowKey}>
                  <button type="button" className="dc-main" aria-expanded={isOpen} onClick={() => setOpen(isOpen ? null : d.id)}>
                    <span className="t2 dc-time">{i === 0 ? fmtTime(d.received_at, { seconds: true }) : ''}</span>
                    <span className="sym dc-inst">{baseOf(tg.instrument)}</span>
                    <span className={`dc-target ${dirOf(w)}`}>{targetText(t, w)}</span>
                    <Conf v={tg.confidence} />
                    <span className="dc-why">{why?.[lang] ?? why?.zh ?? why?.en ?? ''}</span>
                    <span className="dc-res t2">{ex ? execSentence(t, ex) : t('dc.pending')}</span>
                  </button>
                  {isOpen && (
                    <div className="dc-more">
                      <div className="dc-kv num">
                        <span><em>{t('dc.refPx')}</em>{fmtPrice(tg.ref_price)}</span>
                        {ex?.avg_price && <span><em>{t('dc.avgPx')}</em>{fmtPrice(ex.avg_price)}</span>}
                        <span><em>{t('dc.horizon')}</em>{holdText(t, tg.horizon_min * 60000)}</span>
                        {ex && <span><em>{t('dc.achieved')}</em>{locDec((Number(ex.achieved_weight) * 100).toFixed(1))}%</span>}
                      </div>
                      <Rationale r={tg.rationale} />
                    </div>
                  )}
                </div>
              )
            })}
          </Fragment>
        )
      })}
    </div>
  )
}

/** 每笔交易：公开部分谁都能看（时间、方向、价格、盈亏）；开启服务的人点开看复盘 */
export function TradesTable({ strategy, closed }: { strategy: string; closed: ClosedTrade[] }) {
  const t = useT()
  const q = usePoll<TradeDetail[]>(strategy ? `/api/strategies/${strategy}/trades` : null, 8000)
  const unlocked = q.status === 200
  const [open, setOpen] = useState<string | null>(null)
  const rows = useRowsMotion(closed, (x) => `${x.instrument}/${x.open_time}`)
  const detailOf = (x: ClosedTrade) => q.data?.find((d) => d.instrument === x.instrument && d.open_time === x.open_time)
  return (
    <>
      {!unlocked && q.status !== 0 && <Locked what="ai.lockedTrades" compact />}
      {!closed.length ? <div className="mk-empty">{t('ai.noTrades')}</div> : (
        <table className="tbl ai-trades">
          <thead><tr>
            <th>{t('ai.closeTime')}</th><th>{t('col.inst')}</th><th>{t('col.side')}</th><th>{t('ai.entry')}</th><th>{t('ai.exit')}</th><th>{t('ai.qty')}</th><th>{t('ai.hold')}</th><th>{t('ai.net')}</th>
          </tr></thead>
          <tbody>{rows.map(({ item: x, key, state }) => {
            const net = Number(x.net)
            const isOpen = open === key
            const d = detailOf(x)
            return (
              <Fragment key={key}>
                <tr className={`${state ? `tl-${state}` : ''} ${unlocked ? 'ai-click' : ''} ${isOpen ? 'ai-on' : ''}`} onClick={() => { if (unlocked) setOpen(isOpen ? null : key) }} tabIndex={unlocked ? 0 : undefined}
                  onKeyDown={(e) => { if (unlocked && e.key === 'Enter') setOpen(isOpen ? null : key) }}>
                  <td className="t2">{fmtTime(x.close_time ?? x.open_time, { seconds: true })}</td>
                  <td><span className="sym">{baseOf(x.instrument)}</span></td>
                  <td>{t(x.side === 'long' ? 'tl.long' : 'tl.short')}</td>
                  <td>{fmtPrice(x.entry)}</td>
                  <td>{fmtPrice(x.exit)}</td>
                  <td>{Number(Number(x.qty).toFixed(6))}</td>
                  <td className="t2">{holdText(t, (x.close_time ?? x.open_time) - x.open_time)}</td>
                  <td className={dirOf(net)}><Money text={fmtSigned(net)} /></td>
                </tr>
                {isOpen && d && <tr className="ai-rv-row"><td colSpan={8}><Review d={d} /></td></tr>}
              </Fragment>
            )
          })}</tbody>
        </table>
      )}
    </>
  )
}

/** AI 交易复盘第一版（3.24）：当时的证据、决策价、成交价、滑点、手续费、资金费，一路算到净收益 */
function Review({ d }: { d: TradeDetail }) {
  const t = useT()
  const lang = useLang()
  // 滑点成本已经含在平仓盈亏里，单独写出来
  const slipCost = d.legs.reduce((s, l) => s + (Number(l.ref_px) > 0 ? (Number(l.slip_bps) / 10000) * Number(l.ref_px) * Number(l.qty) : 0), 0)
  const fund = Number(d.funding), fees = Number(d.fees), gross = Number(d.gross), net = Number(d.net)
  return (
    <div className="rv">
      {d.decisions.length > 0 && (
        <div className="rv-why">
          {d.decisions.map((x) => {
            const why = x.rationale?.summary
            return (
              <div key={x.id} className="rv-dec">
                <p><span className="num t2">{t('dc.toTarget', { w: (Number(x.weight) * 100).toFixed(0) })} · {t('sig.conf')} {(x.confidence * 100).toFixed(0)}%</span> {why?.[lang] ?? why?.zh ?? why?.en}</p>
                <Rationale r={x.rationale} />
              </div>
            )
          })}
        </div>
      )}
      <table className="tbl rv-legs">
        <thead><tr><th>{t('col.time')}</th><th>{t('col.side')}</th><th>{t('ai.qty')}</th><th>{t('dc.refPx')}</th><th>{t('dc.avgPx')}</th><th>{t('rv.slip')}</th><th>{t('rv.fee')}</th><th>{t('rv.realized')}</th></tr></thead>
        <tbody>
          {d.legs.map((l, i) => (
            <tr key={i}>
              <td className="t2">{fmtTime(l.time, { seconds: true })}</td>
              <td className={l.side === 'buy' ? 'up' : 'down'}>{t(l.side === 'buy' ? 'tr.buy' : 'tr.sell')}{l.closed_by && <span className="t3"> · {t(`fw.n.${l.closed_by}` as Key)}</span>}{l.maker && <span className="t3"> · {t('rv.maker')}</span>}</td>
              <td>{Number(Number(l.qty).toFixed(6))}</td>
              <td className="t2">{Number(l.ref_px) > 0 ? fmtPrice(l.ref_px) : '—'}</td>
              <td>{fmtPrice(l.px)}</td>
              <td className="t2">{Number(l.ref_px) > 0 ? `${oneDec(Number(l.slip_bps))} bp` : '—'}</td>
              <td className="t2"><Money text={fmtAmt(l.fee, 4)} /></td>
              <td className={dirOf(Number(l.realized))}>{Number(l.realized) ? <Money text={fmtSigned(l.realized, 4)} /> : '—'}</td>
            </tr>
          ))}
          {d.fundings.map((f, i) => (
            <tr key={`f${i}`}>
              <td className="t2">{fmtTime(f.time, { seconds: true })}</td>
              <td className="t2" colSpan={5}>{t('rv.funding', { rate: locDec((Number(f.rate) * 100).toFixed(4)) })}</td>
              <td className={dirOf(Number(f.amount))} colSpan={2}><Money text={fmtSigned(f.amount, 4)} /></td>
            </tr>
          ))}
        </tbody>
      </table>
      <dl className="rv-sum num">
        <div><dt>{t('rv.gross')}{Math.abs(slipCost) >= 0.0001 && <small> {t('rv.inclSlip', { v: fmtAmt(Math.abs(slipCost), 2) })}</small>}</dt><dd className={dirOf(gross)}><Money text={fmtSigned(gross, 4)} /></dd></div>
        <div><dt>{t('rv.fees')}</dt><dd className="down"><Money text={fmtSigned(-fees, 4)} /></dd></div>
        <div><dt>{t('rv.fundings')}</dt><dd className={dirOf(fund)}><Money text={fmtSigned(fund, 4)} /></dd></div>
        <div className="rv-net"><dt>{t('rv.net')}</dt><dd className={dirOf(net)}><Money text={fmtSigned(net, 4)} /></dd></div>
      </dl>
    </div>
  )
}

/** 信心校准图（C）：AI 说有多大把握，实际赢了多少。每档不到 30 笔只写"样本不够" */
export function Calibration({ strategy }: { strategy: string }) {
  const t = useT()
  const q = usePoll<Bucket[]>(strategy ? `/api/strategies/${strategy}/calibration` : null, 15000)
  if (q.status === 401 || q.status === 403) return <Locked what="ai.lockedCal" />
  if (!q.data) return <div className="mk-empty">{t('sig.reading')}</div>
  return (
    <div className="cal">
      <p className="cal-lead">{t('cal.lead')}</p>
      {q.data.map((b) => {
        const mid = b.lo === 0 ? null : (b.lo + b.hi) / 2
        return (
          <div className="cal-row" key={b.lo}>
            <span className="cal-l num">{b.lo === 0 ? t('cal.below', { p: Math.round(b.hi * 100) }) : `${Math.round(b.lo * 100)}–${Math.round(b.hi * 100)}%`}</span>
            <span className="cal-bar">
              {b.enough && b.win_rate !== null && <i className="cal-v" style={{ width: `${b.win_rate * 100}%` }} />}
              {mid !== null && <i className="cal-mid" style={{ left: `${mid * 100}%` }} title={t('cal.expected')} />}
              {!b.enough && <span className="cal-none">{t('cal.notEnough', { n: b.trades, need: 30 })}</span>}
            </span>
            <span className="cal-v-t num">{b.enough && b.win_rate !== null ? `${(b.win_rate * 100).toFixed(0)}%` : '—'}</span>
            <span className="cal-n num t3">{t('cal.nTrades', { n: b.trades })}</span>
          </div>
        )
      })}
      <p className="cal-foot t3">{t('cal.foot')}</p>
    </div>
  )
}
