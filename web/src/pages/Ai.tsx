// AI 策略页（3.22）：收益、回撤、夏普、胜率、交易次数、跟随人数和资金；净值曲线叠 BTC 持有；
// 回撤水下图；每笔交易表；决策流；信心校准；我的跟随。整页不滚，只有下面的列表在面板里滚（D79）。
// 汇总和已平仓交易谁都能看（D75 第 17 条），理由和没平的仓位只给开启服务的人（第 18 条）。
import { locDec, numLocale } from '../lib/tz'
import { useEffect, useMemo, useState } from 'react'
import { useT, type Key } from '../lib/i18n'
import { useSession } from '../lib/session'
import { isRunning, openWizard, useFollows, usePoll, type Performance, type StrategyState, type StrategyStatus } from '../lib/follow'
import { baseOf, fmtPrice } from '../lib/format'
import { Bar } from '../ui/Bar'
import { Seg } from '../ui/Seg'
import { Money } from '../ui/Money'
import { Select } from '../ui/Select'
import { NavChart, type PxPoint } from '../charts/NavChart'
import { fmtAmt, fmtSigned } from './trade/OrderPanel'
import { Calibration, DecisionFeed, Locked, TradesTable } from './ai/Decisions'
import { FollowDetail, statusText } from './ai/FollowPanel'
import { PaperLiveCompare, comparePair } from './ai/Compare'
import { VerifyReport } from './ai/Report'
import { go } from '../lib/route'
import '../styles/market.css'
import '../styles/trade.css'
import '../styles/account.css'
import '../styles/ai.css'
import { BRAND } from '../lib/brand'

type Tab = 'trades' | 'decisions' | 'cal' | 'pos' | 'mine' | 'report'

const pct = (v: number, d = 2) => (v > 0 ? '+' : v < 0 ? '−' : '') + locDec(Math.abs(v * 100).toFixed(d)) + '%'

function intervalFor(spanMs: number) {
  if (spanMs < 2 * 86400_000) return '5m'
  if (spanMs < 14 * 86400_000) return '1h'
  return '4h'
}

export default function Ai() {
  const t = useT()
  const session = useSession()
  const list = usePoll<StrategyStatus[]>('/api/strategies', 5000).data
  const [sid, setSid] = useState<string | null>(null)
  const s = list?.find((x) => x.strategy_id === sid) ?? list?.[0] ?? null
  const id = s?.strategy_id ?? ''
  const perf = usePoll<Performance>(id ? `/api/strategies/${id}/performance` : null, 15000).data
  const state = usePoll<StrategyState>(id ? `/api/strategies/${id}` : null, 5000).data
  const follows = useFollows()
  const mine = follows?.filter((f) => f.strategy === id) ?? []
  const running = mine.find(isRunning)
  const pair = comparePair(mine) // 同一个策略同时跟在模拟账户和交易所账户上：对照（18 第 5.6 条）
  const [tab, setTab] = useState<Tab>('trades')
  useEffect(() => { document.title = `${t('bar.ai')} · ${BRAND}` })

  const span = perf?.curve.length ? perf.curve[perf.curve.length - 1].t - perf.curve[0].t : 0
  const since = perf?.curve[0]?.t ?? 0
  const iv = intervalFor(span)
  const btcRaw = usePoll<{ t: number; c: number }[]>(since ? `/api/candles?instrument=BTC-PERP&interval=${iv}&since=${since}` : null, 60000).data
  const btc = useMemo<PxPoint[]>(() => btcRaw ?? [], [btcRaw])
  const initial = Number(perf?.initial ?? 10000)
  const ret = perf?.return ?? 0
  const openPos = state?.state.positions.filter((p) => Number(p.qty) !== 0) ?? []

  const tabs: { value: Tab; label: string }[] = [
    { value: 'trades', label: t('ai.tab.trades', { n: perf?.trades ?? 0 }) },
    { value: 'decisions', label: t('ai.tab.decisions') },
    { value: 'cal', label: t('ai.tab.cal') },
    { value: 'report', label: t('vr.tab') },
    { value: 'pos', label: t('ai.tab.pos') },
    ...(mine.length ? [{ value: 'mine' as const, label: t('ai.tab.mine') }] : []),
  ]
  return (
    <div className="shell ai-shell">
      <Bar page="ai" />
      <main className="ai">
        {!list && <div className="mk-empty ai-empty">{t('sp.connecting')}</div>}
        {list && !s && (
          <div className="ai-none" data-ai-none>
            <h2>{t('ai.noneTitle')}</h2>
            <p>{t('ai.noneText')}</p>
            <ul>
              <li>{t('ai.noneTv')}</li>
              <li>{t('ai.noneQsig')}</li>
              <li>{t('ai.noneBin')}</li>
            </ul>
            <button type="button" className="btn primary" onClick={() => go('account&sec=strategies')}>{t('wz.noneGo')}</button>
          </div>
        )}
        {s && (
          <>
            <header className="ai-head">
              <div className="ai-title">
                {list && list.length > 1
                  ? <Select label={t('ai.strategy')} value={id} onChange={setSid} options={list.map((x) => ({ value: x.strategy_id, label: x.strategy_id }))} />
                  : <h1 className="sym">{id}</h1>}
                {s.sample && <span className="ai-tag" title={t('sp.demo')}>{t('ai.sample')}</span>}
                <span className="ai-online"><span className={`sq ${s.online ? 'ok' : 'warn'}`} />{t(s.online ? 'sp.online' : 'ai.offlineLong')}</span>
                {session.state === 'in' && <a className="ai-log" href={`/api/strategies/${encodeURIComponent(id)}/log`} download data-ai-log title={t('mys.logHint')}>{t('mys.log')}</a>}
                {(s.halted || s.circuit_breaker) && <span className="warn ai-halt">{t(s.halted ? 'sp.halted' : 'sp.breaker')}{t('sp.reduceOnly')}</span>}
              </div>
              <div className="ai-cta">
                {running
                  ? <button type="button" className="ai-mine" onClick={() => setTab('mine')}>
                      <span className={`sq ${running.status === 'active' ? 'ok' : 'warn'}`} />{statusText(t, running)}
                      <b className={`num ${Number(running.pnl) > 0 ? 'up' : Number(running.pnl) < 0 ? 'down' : ''}`}><Money text={fmtSigned(running.pnl)} /></b>
                    </button>
                  : session.state === 'in'
                    ? <button type="button" className="btn primary" onClick={openWizard}>{t('fw.start')}</button>
                    : <button type="button" className="btn primary" onClick={() => go('login&mode=register')}>{t('ai.signupToFollow')}</button>}
              </div>
            </header>
            {s.sample && <p className="ai-sample-note">{t('sp.demo')}</p>}

            <section className="ai-ro" aria-label={t('ai.summary')}>
              <Stat l={t('ai.return')} v={pct(ret)} cls={ret > 0 ? 'up' : ret < 0 ? 'down' : ''} big hint={t('ai.h.return')} />
              <Stat l={t('ai.nav')} v={<Money text={fmtAmt(perf?.nav)} />} hint={t('ai.h.nav')} />
              <Stat l={t('ai.maxdd')} v={perf ? pct(-perf.max_drawdown) : '—'} hint={t('ai.h.maxdd')} />
              <Stat l={t('ai.sharpe')} v={perf?.sharpe != null ? locDec(perf.sharpe.toFixed(2)) : t('ai.tooShort')} dim={perf?.sharpe == null} hint={t('ai.h.sharpe')} />
              <Stat l={t('ai.winrate')} v={perf?.win_rate != null ? `${locDec((perf.win_rate * 100).toFixed(1))}%` : '—'} hint={t('ai.h.winrate')} />
              <Stat l={t('ai.trades')} v={String(perf?.trades ?? '—')} hint={t('ai.h.trades')} />
              <Stat l={t('ai.followers')} v={String(perf?.followers ?? '—')} />
              <Stat l={t('ai.capital')} v={<Money text={perf ? Number(perf.follow_capital).toLocaleString(numLocale()) : '—'} />} />
            </section>

            <section className="display ai-chart">
              <div className="display-head">
                <span className="display-title">{t('ai.curve')}</span>
                <div className="legend">
                  <span><i className="ai-lg self" />{t('ai.nav')}</span>
                  <span><i className="ai-lg btc" />{t('ai.btcHold')}</span>
                  <span><i className="ai-lg dd" />{t('ai.ddLegend')}</span>
                </div>
              </div>
              <div className="display-body"><NavChart curve={perf?.curve ?? []} initial={initial} btc={btc} /></div>
            </section>

            <section className="display ai-list">
              <div className="display-head mk-tabs"><Seg label={t('ai.tabs')} value={tab} onChange={setTab} options={tabs} /></div>
              <div className="ai-list-body mk-scroll">
                {tab === 'trades' && <TradesTable strategy={id} closed={perf?.closed ?? []} />}
                {tab === 'decisions' && <DecisionFeed strategy={id} />}
                {tab === 'cal' && <Calibration strategy={id} />}
                {tab === 'report' && <VerifyReport strategy={id} />}
                {tab === 'pos' && (state?.locked !== false
                  ? <Locked what="ai.lockedPos" />
                  : !openPos.length ? <div className="mk-empty">{t('ai.noPos')}</div> : <PosTable rows={openPos} />)}
                {tab === 'mine' && <>{pair && <PaperLiveCompare paper={pair[0]} live={pair[1]} />}{mine.map((f) => <FollowDetail key={f.id} f={f} />)}</>}
              </div>
            </section>
            <p className="ai-risk">{t('ai.risk')}</p>
          </>
        )}
      </main>
    </div>
  )
}

function Stat({ l, v, cls, big, hint, dim }: { l: string; v: React.ReactNode; cls?: string; big?: boolean; hint?: string; dim?: boolean }) {
  return (
    <div className={`ro ${big ? 'lg' : ''}`} title={hint}>
      <span className="ro-l">{l}</span>
      <span className={`ro-v num ${cls ?? ''} ${dim ? 'ai-dim' : ''}`}>{v}</span>
    </div>
  )
}

function PosTable({ rows }: { rows: StrategyState['state']['positions'] }) {
  const t = useT()
  return (
    <table className="tbl">
      <thead><tr><th>{t('col.inst')}</th><th>{t('sig.target')}</th><th>{t('ai.actual')}</th><th>{t('ai.qty')}</th><th>{t('ai.entry')}</th><th>{t('ai.mark')}</th><th>{t('ai.upnl')}</th></tr></thead>
      <tbody>{rows.map((p) => {
        const u = Number(p.unrealized_pnl)
        return (
          <tr key={p.instrument}>
            <td><span className="sym">{baseOf(p.instrument)}</span>{p.horizon_expired && <span className="ai-tag warn" title={t('ai.expiredHint')}>{t('ai.expired')}</span>}</td>
            <td>{locDec((Number(p.target_weight) * 100).toFixed(1))}%</td>
            <td className="t2">{locDec((Number(p.actual_weight) * 100).toFixed(1))}%</td>
            <td>{Number(p.qty)}</td>
            <td>{fmtPrice(p.entry_price)}</td>
            <td className="t2">{fmtPrice(p.mark_price)}</td>
            <td className={u > 0 ? 'up' : u < 0 ? 'down' : ''}><Money text={fmtSigned(u)} /></td>
          </tr>
        )
      })}</tbody>
    </table>
  )
}

export type { Key }
