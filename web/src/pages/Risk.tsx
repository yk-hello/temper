// 风险页（模块 4，4.21–4.26）：仓位全景（F）、压力测试（G）、纪律仪表（H）。整页不滚动。
import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from 'react'
import { useT, errText, fmtTime, acctName, listSep, type Key } from '../lib/i18n'
import { api, ApiError, useSession } from '../lib/session'
import { baseOf, numIn } from '../lib/format'
import { go, linkProps } from '../lib/route'
import { toast, usePrivate } from '../lib/privacy'
import { usePoll } from '../lib/follow'
import { hms, lockLeft, ruleName, useDiscipline, useTick, type AcctDisc, type DiscView, type Rules } from '../lib/discipline'
import type { PosView } from '../lib/trade'
import { Bar } from '../ui/Bar'
import { Seg } from '../ui/Seg'
import { Select } from '../ui/Select'
import { Slider } from '../ui/Slider'
import { Money } from '../ui/Money'
import { Dialog } from '../ui/Dialog'
import { Field } from '../ui/Field'
import { StressChart, type LiqMark } from '../charts/StressChart'
import { fmtAmt, fmtSigned } from './trade/OrderPanel'
import '../styles/market.css'
import '../styles/trade.css'
import '../styles/account.css'
import '../styles/ai.css'
import '../styles/landing.css'
import '../styles/risk.css'
import { locDec, numLocale } from '../lib/tz'
import { BRAND } from '../lib/brand'

interface RkPos extends PosView { engine: 'manual' | 'ai' | 'bot'; beta: string; corr: string; beta_fallback?: boolean }
interface StressPos { instrument: string; side: 'long' | 'short'; mode: string; beta: string; pnl: string[]; liq_down?: number; liq_up?: number }
interface RkAcct {
  id: number; name: string; following: boolean; equity: string; spot_equity?: string; transfers?: string; covered?: string; available: string; position_margin: string
  gross: string; net: string; btc_equiv: string; positions: RkPos[]; stress: { moves: number[]; equity: string[]; positions: StressPos[] }
}
type Scope = number | 'all'
type Panel = 'f' | 'g' | 'h'

const sum = (list: RkAcct[], f: (a: RkAcct) => string) => list.reduce((s, a) => s + Number(f(a)), 0)
const errOf = (e: unknown) => errText(e instanceof ApiError ? e.code : 'INTERNAL_ERROR')

export default function Risk() {
  const t = useT()
  const s = useSession()
  useEffect(() => { document.title = `${t('bar.risk')} · ${BRAND}` })
  const data = usePoll<{ accounts: RkAcct[] }>(s.state === 'in' ? '/api/risk/overview' : null, 2000).data
  const [late, setLate] = useState(false) // 数据 2 秒还没到（服务器慢、断网）：照样显示，各块自己写着"加载中"或出错
  useEffect(() => { const id = setTimeout(() => setLate(true), 2000); return () => clearTimeout(id) }, [])
  const disc = useDiscipline()
  const [scope, setScope] = useState<Scope>('all')
  const [panel, setPanel] = useState<Panel>('f')
  const accounts = useMemo(() => data?.accounts ?? [], [data])
  const sel = scope === 'all' ? accounts : accounts.filter((a) => a.id === scope)
  if (s.state === 'out') {
    return (
      <div className="rk">
        <Bar page="risk" />
        <RiskOut />
      </div>
    )
  }
  return (
    <div className="rk">
      <Bar page="risk" />
      <Readouts list={sel}>
        <Select className="rk-acct" label={t('rk.acct')} value={String(scope)} onChange={(v) => setScope(v === 'all' ? 'all' : Number(v))}
          options={[{ value: 'all', label: t('rk.all') }, ...accounts.map((a) => ({ value: String(a.id), label: acctName(a.name) }))]} />
      </Readouts>
      <Fairness accounts={accounts} sel={sel} />
      <div className="rk-tabs"><Seg label={t('rk.title')} value={panel} onChange={setPanel} options={[{ value: 'f', label: t('rk.f.title') }, { value: 'g', label: t('rk.g.title') }, { value: 'h', label: t('rk.d.title') }]} /></div>
      {/* 数据到之前不知道有没有持仓，两种排版差很多：先不显示，到了再一次性淡入（刷新时不再先排一种再跳成另一种，11-15） */}
      <div className="rk-main" data-empty={data && !sel.some((a) => a.positions.length) ? '' : undefined} data-loading={data || late ? undefined : ''}>
        <section className={`rk-panel rk-f ${panel === 'f' ? 'on' : ''}`}><Exposure list={sel} showAcct={scope === 'all' && accounts.length > 1} /></section>
        <section className={`rk-panel rk-g ${panel === 'g' ? 'on' : ''}`}><StressPanel list={sel} all={scope === 'all'} /></section>
        <section className={`rk-panel rk-h ${panel === 'h' ? 'on' : ''}`}><Gauges accounts={accounts} scope={scope} disc={disc} /></section>
      </div>
    </div>
  )
}

/**
 * 模拟盘和真实交易所不一样的地方，会让成绩虚高的，直接写出来（审查第 2 条）：
 * 穿仓时超出保证金的亏损由系统补；同一个人两个账户一多一空，跳空时亏的一边最多亏光保证金，赚的一边全拿。
 */
function Fairness({ accounts, sel }: { accounts: RkAcct[]; sel: RkAcct[] }) {
  const t = useT()
  const covered = sel.reduce((x, a) => x + Number(a.covered ?? 0), 0)
  const pairs: string[] = []
  const seen = new Set<string>()
  for (const a of accounts) for (const p of a.positions) for (const b of accounts) {
    if (b.id <= a.id) continue
    const q = b.positions.find((x) => x.instrument === p.instrument && x.side !== p.side)
    if (!q) continue
    const na = Math.abs(Number(p.notional)), nb = Math.abs(Number(q.notional))
    if (Math.min(na, nb) < Math.max(na, nb) * 0.3 || seen.has(`${a.id}-${b.id}-${p.instrument}`)) continue
    seen.add(`${a.id}-${b.id}-${p.instrument}`)
    pairs.push(t('rk.hedge', { a: acctName(a.name), b: acctName(b.name), inst: baseOf(p.instrument) }))
  }
  if (!pairs.length && covered <= 0) return null
  return (
    <div className="note rk-note">
      {covered > 0 && <span>{t('rk.covered', { v: fmtAmt(covered) })}</span>}
      {pairs.slice(0, 2).map((x) => <span key={x}>{x}</span>)}
    </div>
  )
}

function Ro({ l, v, cls, h, opt }: { l: string; v: ReactNode; cls?: string; h?: string; opt?: boolean }) {
  return <div className={`mb-ro ${opt ? 'opt' : ''}`} title={h}><span>{l}</span><b className={`num ${cls ?? ''}`}>{v}</b></div>
}

function Readouts({ list, children }: { list: RkAcct[]; children: ReactNode }) {
  const t = useT()
  const eq = sum(list, (a) => a.equity), margin = sum(list, (a) => a.position_margin), btc = sum(list, (a) => a.btc_equiv)
  const spot = list.reduce((s, a) => s + Number(a.spot_equity ?? 0), 0)
  const has = list.some((a) => a.positions.length)
  const per = Math.abs(btc) / 100
  return (
    <div className="mb-strip rk-ros">
      <div className="mb-ros">
        {children}
        <Ro l={t('rk.equity')} v={<Money text={fmtAmt(eq)} />} />
        {spot > 0 && <Ro l={t('rk.spot')} v={<Money text={fmtAmt(spot)} />} h={t('rk.hint.spot')} />}
        <Ro l={t('rk.gross')} v={<Money text={fmtAmt(sum(list, (a) => a.gross))} />} h={t('rk.hint.gross')} />
        <Ro l={t('rk.net')} v={<Money text={fmtSigned(sum(list, (a) => a.net))} />} h={t('rk.hint.net')} opt />
        <Ro l={t('rk.btc')} v={<Money text={fmtSigned(btc)} />} h={t('rk.hint.btc')} opt />
        <Ro l={t('rk.margin')} v={eq > 0 ? `${locDec(((margin / eq) * 100).toFixed(1))}%` : '—'} h={t('rk.hint.margin')} />
      </div>
      {/* 没仓位时下面"仓位全景"那一行已经写了，这里不再重复（11-16 走查：一屏三个"没有持仓。"） */}
      <p className="rk-say">{!has ? null : btc >= 0
        ? <>{t('rk.sayDown')} <b className="num down"><Money text={fmtAmt(per)} unit="USDT" /></b></>
        : <>{t('rk.sayDownWin')} <b className="num up"><Money text={fmtAmt(per)} unit="USDT" /></b></>}</p>
    </div>
  )
}

/** 未登录：用一个写明"示例"的账户让人拖一下压力测试 */
function RiskOut() {
  const t = useT()
  const [x, setX] = useState(-15)
  const moves = useMemo(() => Array.from({ length: 101 }, (_, i) => i - 50), [])
  const equity = useMemo(() => moves.map((m) => Math.max(0, 10000 * (1 + (3 * m) / 100))), [moves])
  const left = equity[moves.indexOf(x)]
  return (
    <div className="out">
      <div className="out-l">
        <h2>{t('rk.outT')}</h2>
        <ul><li>{t('rk.out1')}</li><li>{t('rk.out2')}</li><li>{t('rk.out3')}</li></ul>
        <a className="btn primary" {...linkProps('login')}>{t('bar.login')}</a>
      </div>
      <div className="out-r">
        <div className="cn-head"><span>{t('rk.g.title')}</span><em>{t('rk.outSample', { v: (x > 0 ? '+' : x < 0 ? '−' : '') + locDec(String(Math.abs(x))) + '%', left: Math.round(left).toLocaleString(numLocale()) })}</em></div>
        <div className="cn-canvas"><StressChart moves={moves} equity={equity} value={x} marks={[{ x: -33, label: 'BTC' }]} onChange={setX} hidden={false} /></div>
      </div>
    </div>
  )
}

// ---------- F 仓位全景 ----------

function Exposure({ list, showAcct }: { list: RkAcct[]; showAcct: boolean }) {
  const t = useT()
  const rows = list.flatMap((a) => a.positions.map((p) => ({ a, p })))
  const groups: { key: 'manual' | 'ai' | 'bot'; label: string; rows: typeof rows }[] = [
    { key: 'manual', label: t('rk.manual'), rows: rows.filter((r) => r.p.engine === 'manual') },
    { key: 'ai', label: t('rk.ai'), rows: rows.filter((r) => r.p.engine === 'ai') },
    { key: 'bot', label: t('rk.bot'), rows: rows.filter((r) => r.p.engine === 'bot') },
  ]
  const maxN = Math.max(1, ...rows.map((r) => Math.abs(Number(r.p.notional))))
  const anyFallback = rows.some((r) => r.p.beta_fallback)
  return (
    <>
      <div className="display-head"><span className="display-title">{t('rk.f.title')}</span><span className="rk-sub">{t('rk.priority')}</span></div>
      <div className="rk-body mk-scroll">
        {!rows.length ? (
          <div className="rk-none">
            <span>{t('rk.noPosLong')}</span>
            <a className="op-link" {...linkProps('trade')}>{t('rk.goTrade')}</a>
            <a className="op-link" {...linkProps('ai')}>{t('rk.goAi')}</a>
          </div>
        ) : (
          <table className="tbl rk-tbl">
            <thead><tr>
              <th>{t('col.inst')}</th>
              {showAcct && <th className="rk-opt">{t('rk.col.acct')}</th>}
              <th className="rk-barh"><span>{t('tl.short')}</span><span>{t('tl.long')}</span></th>
              <th>{t('rk.col.size')}</th><th>{t('rk.col.pnl')}</th>
              <th className="rk-opt" title={t('rk.hint.beta')}>{t('rk.col.beta')}</th>
              <th className="rk-opt">{t('rk.col.margin')}</th><th>{t('rk.col.liq')}</th>
            </tr></thead>
            {groups.filter((g) => g.rows.length).map((g) => (
              <tbody key={g.key}>
                <tr className="rk-grp"><td colSpan={showAcct ? 8 : 7}>{g.label}<span className="num">{g.rows.length}</span></td></tr>
                {g.rows.map(({ a, p }) => {
                  const n = Math.abs(Number(p.notional)), u = Number(p.upnl), mark = Number(p.mark), liq = Number(p.liq_px)
                  const dist = liq > 0 && mark > 0 ? (Math.abs(mark - liq) / mark) * 100 : null
                  return (
                    <tr key={`${a.id}/${p.instrument}`} className="rk-row" onClick={() => go(`trade&i=${encodeURIComponent(p.instrument)}`)}>
                      <td><span className="sym">{baseOf(p.instrument)}</span></td>
                      {showAcct && <td className="t2 rk-opt">{acctName(a.name)}</td>}
                      <td className="rk-barc"><span className="rk-bar"><i className={`rk-fill ${p.side}`} style={{ width: `${(n / maxN) * 50}%` }} /></span></td>
                      <td><Money text={fmtAmt(n)} /></td>
                      <td className={u > 0 ? 'up' : u < 0 ? 'down' : ''}><Money text={fmtSigned(u)} /></td>
                      <td className={`rk-opt ${p.beta_fallback ? 't3' : ''}`}>{locDec(Number(p.beta).toFixed(2))}{p.beta_fallback ? '*' : ''}</td>
                      <td className="t2 rk-opt"><Money text={fmtAmt(p.margin)} /></td>
                      <td className={dist != null && dist < 3 ? 'down' : dist != null && dist < 10 ? 'warn-t' : ''}>{dist == null ? '—' : `${locDec(dist.toFixed(1))}%`}</td>
                    </tr>
                  )
                })}
              </tbody>
            ))}
          </table>
        )}
        {anyFallback && <p className="rk-foot">{t('rk.betaNote')}</p>}
      </div>
      {rows.length > 0 && <Summary list={list} />}
    </>
  )
}

/** 押了多少、押在什么方向；是不是都在赌同一件事；保证金还剩多少 */
function Summary({ list }: { list: RkAcct[] }) {
  const t = useT()
  const ps = list.flatMap((a) => a.positions)
  const notional = (p: RkPos) => Math.abs(Number(p.notional))
  const long = ps.filter((p) => p.side === 'long').reduce((v, p) => v + notional(p), 0)
  const short = ps.filter((p) => p.side === 'short').reduce((v, p) => v + notional(p), 0)
  const big = Math.max(long, short, 1)
  const weighted = ps.reduce((v, p) => v + notional(p) * Math.abs(Number(p.beta)), 0)
  const same = weighted > 0 ? Math.abs(sum(list, (a) => a.btc_equiv)) / weighted : 0
  const eq = sum(list, (a) => a.equity), margin = sum(list, (a) => a.position_margin), avail = sum(list, (a) => a.available)
  const used = eq > 0 ? margin / eq : 0
  return (
    <div className="rk-sum">
      <div className="rk-g-row">
        <span className="rk-g-l">{t('rk.s.dir')}</span>
        <span className="rk-bar rk-sum-bar"><i className="rk-fill short" style={{ width: `${(short / big) * 50}%` }} /><i className="rk-fill long" style={{ width: `${(long / big) * 50}%` }} /></span>
        <span className="rk-g-v num">{t('tl.short')} <Money text={fmtAmt(short, 0)} /> · {t('tl.long')} <Money text={fmtAmt(long, 0)} /></span>
      </div>
      <div className="rk-g-row" title={t('rk.hint.same')}>
        <span className="rk-g-l">{t('rk.s.same')}</span>
        <Meter frac={same} tone={same >= 0.8 ? 'warn' : ''} />
        <span className="rk-g-v num">{(same * 100).toFixed(0)}%</span>
      </div>
      <div className="rk-g-row">
        <span className="rk-g-l">{t('rk.s.margin')}</span>
        <Meter frac={used} tone={used >= 0.7 ? 'warn' : ''} />
        <span className="rk-g-v num">{locDec((used * 100).toFixed(1))}% <span className="t3">· {t('tr.available')} <Money text={fmtAmt(avail, 0)} /></span></span>
      </div>
    </div>
  )
}

// ---------- G 压力测试 ----------

function Stress({ list }: { list: RkAcct[] }) {
  const t = useT()
  const [x, setX] = useState(-10)
  const moves = list[0]?.stress.moves ?? []
  const equity = useMemo(() => moves.map((_, i) => list.reduce((s, a) => s + Number(a.stress.equity[i] ?? 0), 0)), [list, moves])
  const idx = moves.indexOf(x)
  const zero = moves.indexOf(0)
  const rows = list.flatMap((a) => a.stress.positions.map((p) => ({ a, p })))
  const liqAt = (p: StressPos) => (x < 0 ? (p.liq_down != null && x <= p.liq_down) : (p.liq_up != null && x >= p.liq_up))
  const dead = rows.filter((r) => liqAt(r.p)).map((r) => baseOf(r.p.instrument))
  const marks = useMemo<LiqMark[]>(() => {
    const out: LiqMark[] = []
    for (const r of rows) {
      if (r.p.liq_down != null) out.push({ x: r.p.liq_down, label: baseOf(r.p.instrument) })
      if (r.p.liq_up != null) out.push({ x: r.p.liq_up, label: baseOf(r.p.instrument) })
    }
    return out
  }, [list]) // eslint-disable-line react-hooks/exhaustive-deps
  const now = equity[zero] ?? 0, then = equity[idx] ?? 0
  const change = now > 0 ? ((then - now) / now) * 100 : 0
  const has = rows.length > 0
  const priv = usePrivate()
  return (
    <>
      <div className="rk-g-body">
        <div className="rk-slide">
          <span className="rk-slide-l">{t('rk.slider')}</span>
          <Slider value={x} min={-50} max={50} step={1} onChange={setX} label={t('rk.slider')} ticks={[0, 0.25, 0.5, 0.75, 1]} disabled={!has} />
          <b className="num rk-slide-v">{x > 0 ? '+' : x < 0 ? '−' : ''}{Math.abs(x)}%</b>
        </div>
        <p className="rk-g-say">
          {!has ? t('rk.noPos') : <>
            {t('rk.stressEq')} <b className="num"><Money text={fmtAmt(now)} /></b> → <b className={`num ${then < now ? 'down' : then > now ? 'up' : ''}`}><Money text={fmtAmt(then)} /></b>
            <span className="num t3"> ({change > 0 ? '+' : change < 0 ? '−' : ''}{locDec(Math.abs(change).toFixed(1))}%)</span>
            <span className={dead.length ? 'down' : 't3'}> · {dead.length ? t('rk.stressLiq', { list: dead.join(listSep()) }) : t('rk.stressSafe')}</span>
          </>}
        </p>
        <div className="rk-chart-box"><StressChart moves={moves} equity={equity} value={x} marks={marks} onChange={setX} hidden={priv} /></div>
        {has && (
          <div className="rk-g-tbl mk-scroll">
            <table className="tbl">
              <thead><tr><th>{t('col.inst')}</th><th>{t('col.side')}</th><th className="rk-opt">{t('rk.col.beta')}</th><th>{t('rk.col.at', { x: `${x > 0 ? '+' : x < 0 ? '−' : ''}${Math.abs(x)}` })}</th><th>{t('rk.col.st')}</th></tr></thead>
              <tbody>{rows.map(({ a, p }) => {
                const v = Number(p.pnl[idx] ?? 0)
                const isDead = liqAt(p)
                const at = x < 0 ? p.liq_down : p.liq_up
                return (
                  <tr key={`${a.id}/${p.instrument}`}>
                    <td><span className="sym">{baseOf(p.instrument)}</span></td>
                    <td>{t(p.side === 'long' ? 'tl.long' : 'tl.short')}</td>
                    <td className="t2 rk-opt">{locDec(Number(p.beta).toFixed(2))}</td>
                    <td className={v > 0 ? 'up' : v < 0 ? 'down' : ''}><Money text={fmtSigned(v)} /></td>
                    <td className={isDead ? 'down' : 't3'}>{isDead ? t('rk.st.liq') : at != null ? t('rk.st.at', { x: `${at > 0 ? '+' : '−'}${Math.abs(at)}` }) : t('rk.st.ok')}</td>
                  </tr>
                )
              })}</tbody>
            </table>
          </div>
        )}
      </div>
    </>
  )
}

// ---------- G 的另外两种看法：历史情景、最坏的一天（18 第 10.3 条，D102） ----------

type GMode = 'btc' | 'hist' | 'worst'
interface ScPos { instrument: string; side: string; move: string; pnl: string; liq: boolean }
interface ScRes { before: string; after: string; positions: ScPos[] }
interface ScOut { id: string; date: string; up: boolean; btc?: { O: number; H: number; L: number; C: number }; worst: ScRes; close: ScRes; estimated?: string[] }
interface WorstDay { days: number; p99: number; p95: number; worst: number; worst_date: string; equity: number }
interface ScData { accounts: { id: number; scenarios: ScOut[]; worst_day?: WorstDay }[]; all_worst_day?: WorstDay }

function StressPanel({ list, all }: { list: RkAcct[]; all: boolean }) {
  const t = useT()
  const [mode, setModeRaw] = useState<GMode>(() => { try { const v = localStorage.getItem('qs.rk.g'); return v === 'hist' || v === 'worst' ? v : 'btc' } catch { return 'btc' } })
  const setMode = (m: GMode) => { setModeRaw(m); try { localStorage.setItem('qs.rk.g', m) } catch { /* 忽略 */ } }
  return (
    <>
      <div className="display-head rk-g-head">
        <span className="display-title">{t('rk.g.title')}</span>
        <Seg label={t('rk.g.title')} value={mode} onChange={setMode} options={[{ value: 'btc', label: t('rk.g.btc') }, { value: 'hist', label: t('rk.g.hist') }, { value: 'worst', label: t('rk.g.worst') }]} />
        <span className="rk-sub">{t(mode === 'btc' ? 'rk.g.note' : mode === 'hist' ? 'rk.hs.note' : 'rk.wd.note')}</span>
      </div>
      {mode === 'btc' ? <Stress list={list} /> : <Scenarios list={list} all={all} mode={mode} />}
    </>
  )
}

const pctOf = (v: number, base: number) => (base > 0 ? (v / base) * 100 : 0)
// 四舍五入后是 0 就不带正负号（11-17 走查："−0.0%"）；小数点跟数字格式走
const signPct = (v: number, d = 1) => { const s = Math.abs(v).toFixed(d); return `${Number(s) === 0 ? '' : v > 0 ? '+' : '−'}${locDec(s)}%` }

function Scenarios({ list, all, mode }: { list: RkAcct[]; all: boolean; mode: 'hist' | 'worst' }) {
  const t = useT()
  const s = useSession()
  const q = usePoll<ScData>(s.state === 'in' ? '/api/risk/scenarios' : null, 60_000)
  const ids = new Set(list.map((a) => a.id))
  const accts = (q.data?.accounts ?? []).filter((a) => ids.has(a.id))
  const has = list.some((a) => a.positions.length > 0)
  if (!has) return <div className="rk-g-body"><p className="rk-g-say">{t('rk.noPos')}</p></div>
  if (!q.data) return <div className="rk-g-body"><p className="rk-g-say t3">{q.status >= 500 ? t('rk.hs.down') : t('rk.hs.loading')}</p></div>
  if (mode === 'worst') {
    const w = all && accts.length > 1 ? q.data.all_worst_day : accts.find((a) => a.worst_day)?.worst_day
    if (!w) return <div className="rk-g-body"><p className="rk-g-say t3">{t('rk.hs.down')}</p></div>
    const row = (label: string, v: number, sub?: string) => (
      <div className="rk-wd-row">
        <span className="rk-wd-l">{label}</span>
        <b className={`num ${v < 0 ? 'down' : v > 0 ? 'up' : ''}`}><Money text={fmtSigned(v)} /></b>
        <span className="num t3">{signPct(pctOf(v, w.equity))}{sub ? ` · ${sub}` : ''}</span>
      </div>
    )
    return (
      <div className="rk-g-body rk-wd" data-worst-day>
        {row(t('rk.wd.p99'), w.p99)}
        {row(t('rk.wd.p95'), w.p95)}
        {row(t('rk.wd.worst'), w.worst, w.worst_date)}
        <p className="rk-wd-note t3">{t('rk.wd.how', { n: w.days })}</p>
      </div>
    )
  }
  // 历史情景：选中的几个账户加起来
  const defs = accts[0]?.scenarios ?? []
  const rows = defs.map((d) => {
    let before = 0, worstAfter = 0, closeAfter = 0
    const liq = new Set<string>(), est = new Set<string>()
    for (const a of accts) {
      const sc = a.scenarios.find((x) => x.id === d.id)
      if (!sc) continue
      before += Number(sc.worst.before); worstAfter += Number(sc.worst.after); closeAfter += Number(sc.close.after)
      sc.worst.positions.forEach((p) => p.liq && liq.add(baseOf(p.instrument)))
      sc.estimated?.forEach((i) => est.add(baseOf(i)))
    }
    return { d, before, worst: worstAfter - before, close: closeAfter - before, liq: [...liq], est: [...est] }
  })
  return (
    <div className="rk-g-body">
      <div className="rk-g-tbl rk-hs mk-scroll" data-scenarios>
        <table className="tbl">
          <thead><tr><th>{t('rk.hs.day')}</th><th className="rk-opt">{t('rk.hs.btc')}</th><th>{t('rk.hs.worst')}</th><th className="rk-opt">{t('rk.hs.close')}</th><th>{t('rk.hs.liq')}</th></tr></thead>
          <tbody>{rows.map((r) => {
            const b = r.d.btc
            const ext = b ? (r.d.up ? b.H / b.O - 1 : b.L / b.O - 1) * 100 : 0
            const cl = b ? (b.C / b.O - 1) * 100 : 0
            return (
              <tr key={r.d.id} data-sc={r.d.id}>
                <td><span className="num t3 rk-hs-date">{r.d.date}</span> {t(`rk.sc.${r.d.id}` as Key)}{r.est.length > 0 && <span className="rk-hs-est" title={t('rk.hs.est', { list: r.est.join(listSep()) })}>*</span>}</td>
                <td className="num rk-opt" title={t('rk.hs.cl', { v: signPct(cl) })}><span className={ext < 0 ? 'down' : 'up'}>{t(r.d.up ? 'rk.hs.high' : 'rk.hs.low', { v: signPct(ext) })}</span></td>
                <td className={`num ${r.worst < 0 ? 'down' : r.worst > 0 ? 'up' : ''}`}><Money text={fmtSigned(r.worst)} /><span className="t3"> {signPct(pctOf(r.worst, r.before))}</span></td>
                <td className={`num rk-opt ${r.close < 0 ? 'down' : r.close > 0 ? 'up' : ''}`}><Money text={fmtSigned(r.close)} /></td>
                <td className={r.liq.length ? 'down' : 't3'}>{r.liq.length ? r.liq.join(listSep()) : t('rk.hs.none')}</td>
              </tr>
            )
          })}</tbody>
        </table>
      </div>
      <p className="rk-wd-note t3">{t('rk.hs.how')}</p>
    </div>
  )
}

// ---------- H 纪律仪表 ----------

function Meter({ frac, tone }: { frac: number; tone?: string }) {
  return <span className="rk-meter"><i className={tone ?? ''} style={{ width: `${Math.min(100, Math.max(0, frac * 100))}%` }} /><b style={{ left: '25%' }} /><b style={{ left: '50%' }} /><b style={{ left: '75%' }} /></span>
}

function Gauges({ accounts, scope, disc }: { accounts: RkAcct[]; scope: Scope; disc: ReturnType<typeof useDiscipline> }) {
  const t = useT()
  const s = useSession()
  const v = disc.view
  const [picked, setPicked] = useState<number | null>(null)
  const [dlg, setDlg] = useState<'off' | 'rules' | 'unlock' | null>(null)
  // "全部账户"时默认看锁定的账户；否则第一个
  const locked = v?.accounts.find((a) => a.lock)?.account
  const id = scope !== 'all' ? scope : picked ?? locked ?? accounts[0]?.id ?? null
  const acct = accounts.find((a) => a.id === id)
  const st = v?.accounts.find((a) => a.account === id)
  const now = useTick(!!st?.lock)
  const hasPw = s.state === 'in' && !!s.user.has_trade_password
  if (!v || !acct || !st) return (
    <>
      <div className="display-head"><span className="display-title">{t('rk.d.title')}</span></div>
      <div className="mk-empty">{t('common.loading')}</div>
    </>
  )
  const r = v.rules
  const eq = Number(acct.equity) - Number(acct.transfers ?? 0) // 和后端判断用同一个数：划转不是赚亏
  // 新账户在纪律模块第一次记录之前开盘权益是 0，按现在的权益算，不要显示"亏了 -10000 / 0"
  const open = Number(st.day_open) > 0 ? Number(st.day_open) : eq, peak = Number(st.peak) > 0 ? Number(st.peak) : eq, dayPeak = Number(st.day_peak) > 0 ? Number(st.day_peak) : eq
  const pnl = eq - open
  const limit = open * Number(r.daily_loss)
  const R = open * Number(r.per_trade)
  const peakR = Number(r.peak_r), stopR = Number(r.stop_r)
  const armed = R > 0 && dayPeak - open >= R * peakR
  const dd = peak > 0 ? Math.max(0, (peak - eq) / peak) : 0
  const ddMax = Number(r.max_drawdown)
  const lock = st.lock
  const left = lock ? lockLeft(lock, now) : null
  return (
    <>
      <div className="display-head">
        <span className="display-title">{t('rk.d.title')}</span>
        {scope === 'all' && accounts.length > 1 && (
          <Select className="rk-acct" label={t('rk.acct')} value={String(id)} onChange={(x) => setPicked(Number(x))} options={accounts.map((a) => ({ value: String(a.id), label: acctName(a.name) }))} />
        )}
        <span className={`rk-state ${v.enabled ? '' : 'off'}`}><span className={`sq ${lock ? 'warn' : v.enabled ? 'ok' : ''}`} />{t(lock ? 'rk.d.isLocked' : v.enabled ? 'rk.d.on' : 'rk.d.off')}</span>
        <span className="rk-acts">
          <button type="button" className="op-link" onClick={() => setDlg('rules')}>{t('rk.d.edit')}</button>
          {v.enabled
            ? <button type="button" className="op-link" disabled={!!lock} title={lock ? errText('DISCIPLINE_IS_LOCKED') : undefined} onClick={() => setDlg('off')}>{t('rk.d.turnOff')}</button>
            : <button type="button" className="op-link" onClick={() => void api<DiscView>('POST', '/api/discipline/enabled', { on: true }).then(disc.set)}>{t('rk.d.turnOn')}</button>}
        </span>
      </div>
      <div className="rk-body mk-scroll rk-h-body">
        {lock && (
          <div className="rk-lock" role="status">
            <p>{t('rk.d.locked', { rule: ruleName(lock.rule) })}</p>
            <p className="rk-lock-t">
              {lock.unlock_at ? <>{t('rk.d.unlocking')} <b className="num">{hms(lock.unlock_at - now)}</b></>
                : left != null ? <>{t('rk.d.until')} <b className="num">{hms(left)}</b></>
                : t('rk.d.noExpiry')}
            </p>
            {!lock.unlock_at && (st.unlock_used
              ? <span className="t3">{t('rk.d.unlockUsed')}</span>
              : <button type="button" className="btn secondary sm" onClick={() => setDlg('unlock')}>{t('rk.d.unlock')}</button>)}
          </div>
        )}
        <div className="rk-g-row">
          <span className="rk-g-l">{t('disc.rule.daily_loss')}</span>
          <Meter frac={limit > 0 && pnl < 0 ? -pnl / limit : 0} tone={pnl < 0 && -pnl >= limit * 0.75 ? 'bad' : ''} />
          <span className="rk-g-v num">{pnl < 0
            ? <><Money text={fmtAmt(-pnl)} /> / <Money text={fmtAmt(limit)} /></>
            : <><span className="up"><Money text={fmtSigned(pnl)} /></span> <span className="t3">/ <Money text={fmtAmt(limit)} /></span></>}</span>
        </div>
        <div className="rk-g-row">
          <span className="rk-g-l">{t('disc.rule.give_back')}</span>
          <span className="rk-meter rk-gb">
            <i className={armed ? 'armed' : ''} style={{ width: `${R > 0 ? Math.min(100, Math.max(0, (pnl / R / peakR) * 100)) : 0}%` }} />
            <b className="stop" style={{ left: `${(stopR / peakR) * 100}%` }} />
          </span>
          <span className="rk-g-v num">{R > 0 ? `${pnl >= 0 ? '+' : '−'}${locDec(Math.abs(pnl / R).toFixed(1))}R` : '—'} <span className="t3">/ +{peakR}R → +{stopR}R</span></span>
        </div>
        <div className="rk-g-row">
          <span className="rk-g-l">{t('disc.rule.max_drawdown')}</span>
          <Meter frac={ddMax > 0 ? dd / ddMax : 0} tone={dd >= ddMax * 0.75 ? 'bad' : ''} />
          <span className="rk-g-v num">{locDec((dd * 100).toFixed(1))}% <span className="t3">/ {locDec((ddMax * 100).toFixed(0))}%</span></span>
        </div>
        <div className="rk-g-row">
          <span className="rk-g-l">{t('disc.rule.loss_streak')}</span>
          <span className="rk-pips">{Array.from({ length: r.loss_streak }, (_, i) => <i key={i} className={i < st.streak ? 'on' : ''} />)}</span>
          <span className="rk-g-v num">{st.streak} <span className="t3">/ {r.loss_streak} · {t('rk.d.cool', { n: r.cooldown_min })}{Number(r.min_loss ?? 0) > 0 ? ` · ${t('rk.d.small', { p: locDec(pctStr(r.min_loss!)) })}` : ''}</span></span>
        </div>
        <div className="rk-g-row">
          <span className="rk-g-l">{t('disc.rule.per_trade')}</span>
          <span className="rk-g-note">{t('rk.d.perTrade')}</span>
          <span className="rk-g-v num"><Money text={fmtAmt(eq * Number(r.per_trade))} /> <span className="t3">({locDec((Number(r.per_trade) * 100).toFixed(1))}%)</span></span>
        </div>
        <p className="rk-foot">{t('rk.d.dayReset', { t: fmtTime(st.next_day) })}</p>
        <History acct={acct.id} />
      </div>
      <Dialog open={dlg === 'rules'} title={t('rk.r.title')} onClose={() => setDlg(null)}>
        {dlg === 'rules' && <RulesForm v={v} hasPw={hasPw} onDone={(n) => { disc.set(n); setDlg(null); toast(t('rk.r.saved')) }} onCancel={() => setDlg(null)} />}
      </Dialog>
      <Dialog open={dlg === 'off'} title={t('rk.off.title')} onClose={() => setDlg(null)}>
        {dlg === 'off' && <PwForm body={t('rk.off.body')} go_={t('rk.d.turnOff')} hasPw={hasPw} danger
          run={(pw) => api<DiscView>('POST', '/api/discipline/enabled', { on: false, trade_password: pw })}
          onDone={(n) => { disc.set(n); setDlg(null) }} onCancel={() => setDlg(null)} />}
      </Dialog>
      <Dialog open={dlg === 'unlock'} title={t('rk.d.unlock')} onClose={() => setDlg(null)}>
        {dlg === 'unlock' && <PwForm body={t('rk.d.unlockBody')} go_={t('rk.d.unlock')} hasPw={hasPw}
          run={(pw) => api<DiscView>('POST', `/api/discipline/${id}/unlock`, { trade_password: pw })}
          onDone={(n) => { disc.set(n); setDlg(null); toast(t('rk.d.unlockOk')) }} onCancel={() => setDlg(null)} />}
      </Dialog>
    </>
  )
}

interface DiscEvent { seq: number; t: number; k: string; on?: boolean; by?: string; rule?: string; inst?: string; lock?: { rule: string } }

/** 什么时候锁的、为什么、谁解的、规则改过什么（4.20） */
function History({ acct }: { acct: number }) {
  const t = useT()
  const list = usePoll<{ events: DiscEvent[] }>(`/api/discipline/${acct}/history`, 5000).data?.events ?? []
  const text = (e: DiscEvent) => {
    switch (e.k) {
      case 'lock': return t('rk.ev.lock', { rule: ruleName(e.lock?.rule ?? '') })
      case 'unlock_req': return t('rk.ev.req')
      case 'unlock': return t(e.by === 'password' ? 'rk.ev.unlockPw' : 'rk.ev.unlockTime')
      case 'reject': return t('rk.ev.reject', { inst: baseOf(e.inst ?? ''), rule: ruleName(e.rule ?? '') })
      case 'settings': return t('rk.ev.settings')
      case 'enabled': return t(!e.on ? (e.by === 'default' ? 'rk.ev.offDefault' : 'rk.ev.off') : e.by === 'login' ? 'rk.ev.onLogin' : 'rk.ev.on')
      default: return e.k
    }
  }
  if (!list.length) return null
  return (
    <div className="rk-hist">
      <h3>{t('rk.ev.title')}</h3>
      {list.slice(0, 12).map((e) => <div key={e.seq} className="rk-ev"><span className="num t3">{fmtTime(e.t)}</span><span>{text(e)}</span></div>)}
    </div>
  )
}

/** 要二级密码的操作：没设过就引导去设置 */
function PwForm({ body, go_, hasPw, danger, run, onDone, onCancel }: { body: string; go_: string; hasPw: boolean; danger?: boolean; run: (pw: string) => Promise<DiscView>; onDone: (v: DiscView) => void; onCancel: () => void }) {
  const t = useT()
  const [pw, setPw] = useState('')
  const [err, setErr] = useState<string>()
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    try { onDone(await run(pw)) } catch (x) { setErr(errOf(x)) } finally { setBusy(false) }
  }
  return (
    <form className="au-form" onSubmit={submit} noValidate>
      <p className="dlg-text">{body}</p>
      {hasPw
        ? <Field label={t('acct.tradePw')} type="password" reveal autoComplete="off" value={pw} onChange={(v) => { setPw(v); setErr(undefined) }} error={err} />
        : <div className="note">{t('rk.needPw')} <a className="op-link" {...linkProps('account&sec=tradepw&back=risk')}>{t('acct.tradePwSetBtn')}</a></div>}
      <div className="dlg-btns">
        <button type="button" className="btn ghost" onClick={onCancel}>{t('common.cancel')}</button>
        <button className={`btn ${danger ? 'danger' : 'primary'}`} disabled={!hasPw || !pw || busy}>{go_}</button>
      </div>
    </form>
  )
}

const pctStr = (s: string) => String(Number((Number(s) * 100).toFixed(2)))

function RulesForm({ v, hasPw, onDone, onCancel }: { v: DiscView; hasPw: boolean; onDone: (n: DiscView) => void; onCancel: () => void }) {
  const t = useT()
  const fill = (r: Rules) => ({ daily: pctStr(r.daily_loss), per: pctStr(r.per_trade), dd: pctStr(r.max_drawdown), peak: String(Number(r.peak_r)), stop: String(Number(r.stop_r)), streak: String(r.loss_streak), cool: String(r.cooldown_min), small: pctStr(r.min_loss ?? '0') })
  const [f, setF] = useState(() => fill(v.rules))
  const [pw, setPw] = useState('')
  const [err, setErr] = useState<string>()
  const [busy, setBusy] = useState(false)
  const set = (k: keyof typeof f) => (x: string) => { setF((o) => ({ ...o, [k]: numIn(x) })); setErr(undefined) }
  const cur = v.rules
  const looser = Number(f.daily) > Number(cur.daily_loss) * 100 + 1e-9 || Number(f.per) > Number(cur.per_trade) * 100 + 1e-9 || Number(f.dd) > Number(cur.max_drawdown) * 100 + 1e-9 ||
    Number(f.peak) > Number(cur.peak_r) || Number(f.stop) < Number(cur.stop_r) || Number(f.streak) > cur.loss_streak || Number(f.cool) < cur.cooldown_min ||
    Number(f.small) > Number(cur.min_loss ?? 0) * 100 + 1e-9
  const dec = (x: string) => String(Number((Number(x) / 100).toFixed(6)))
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    try {
      onDone(await api<DiscView>('PUT', '/api/discipline/rules', { trade_password: pw, rules: {
        daily_loss: dec(f.daily), per_trade: dec(f.per), max_drawdown: dec(f.dd), peak_r: f.peak, stop_r: f.stop, loss_streak: Number(f.streak), cooldown_min: Number(f.cool), min_loss: dec(f.small),
      } }))
    } catch (x) { setErr(errOf(x)) } finally { setBusy(false) }
  }
  return (
    <form className="au-form rk-rules" onSubmit={submit} noValidate>
      <div className="rk-rules-g">
        <Field label={t('rk.r.daily')} value={f.daily} onChange={set('daily')} inputMode="decimal" />
        <Field label={t('rk.r.per')} value={f.per} onChange={set('per')} inputMode="decimal" />
        <Field label={t('rk.r.dd')} value={f.dd} onChange={set('dd')} inputMode="decimal" />
        <Field label={t('rk.r.streak')} value={f.streak} onChange={set('streak')} inputMode="numeric" />
        <Field label={t('rk.r.peak')} value={f.peak} onChange={set('peak')} inputMode="decimal" />
        <Field label={t('rk.r.stop')} value={f.stop} onChange={set('stop')} inputMode="decimal" />
        <Field label={t('rk.r.cool')} value={f.cool} onChange={set('cool')} inputMode="numeric" />
        <Field label={t('rk.r.small')} hint={t('rk.r.smallHint')} value={f.small} onChange={set('small')} inputMode="decimal" />
      </div>
      {looser && (hasPw
        ? <Field label={t('rk.r.needPw')} type="password" reveal autoComplete="off" value={pw} onChange={(x) => { setPw(x); setErr(undefined) }} />
        : <div className="note">{t('rk.needPw')} <a className="op-link" {...linkProps('account&sec=tradepw&back=risk')}>{t('acct.tradePwSetBtn')}</a></div>)}
      {err && <p className="rk-err" role="alert">{err}</p>}
      <div className="dlg-btns rk-rules-btns">
        <button type="button" className="op-link" onClick={() => setF(fill(v.defaults))}>{t('rk.r.reset')}</button>
        <span className="rk-grow" />
        <button type="button" className="btn ghost" onClick={onCancel}>{t('common.cancel')}</button>
        <button className="btn primary" disabled={busy || (looser && (!hasPw || !pw))}>{t('rk.r.save')}</button>
      </div>
    </form>
  )
}

export type { AcctDisc }
