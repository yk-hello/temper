// 复盘页（模块 5）：交易日志、绩效、MAE / MFE、结论、预案、回放、练习。
// 顶部写算好的数字，下面是依据。整页不滚动，列表自己滚。
import { LIMITS } from '../lib/limits'
import { useBots } from '../lib/bots'
import { statusText } from './trade/BotPanel'
import { lazy, Suspense, useEffect, useMemo, useRef, useState, type ChangeEvent } from 'react'
import { errText, fmtTime, useLang, useT, tr, type Key, listSep } from '../lib/i18n'
import { api, ApiError, useSession } from '../lib/session'
import { baseOf, fmtPrice } from '../lib/format'
import { linkProps, setParam } from '../lib/route'
import { toast, usePrivate } from '../lib/privacy'
import { usePaperAccounts } from '../lib/trade'
import { holdText, median, tzOffsetMin, type Excursion, type Finding, type JStats, type JTrade, type Note, type Origin, type Playbook, type Stat } from '../lib/journal'
import { Bar } from '../ui/Bar'
import { Seg } from '../ui/Seg'
import { Select } from '../ui/Select'
import { Money } from '../ui/Money'
import { Dialog } from '../ui/Dialog'
import { ExcursionChart, UnderwaterChart } from '../charts/ReviewCharts'
import { fmtAmt, fmtSigned } from './trade/OrderPanel'
import { Confirm } from './trade/Lists'
import { locDec } from '../lib/tz'

/** 最深回撤：−0.01，用正经的负号、小数点跟数字格式走 */
const fmtDd = (pts: { dd: number }[]) => { const v = Math.min(0, ...pts.map((p) => p.dd)) * 100; const s = Math.abs(v).toFixed(2); return (Number(s) ? '−' : '') + locDec(s) }
import '../styles/market.css'
import '../styles/trade.css'
import '../styles/account.css'
import '../styles/landing.css'
import '../styles/review.css'
import { BRAND } from '../lib/brand'

const TradeReplay = lazy(() => import('./review/TradeReplay'))
const Practice = lazy(() => import('./review/Practice'))
const Highlights = lazy(() => import('./review/Highlights'))
const Backtest = lazy(() => import('./review/Backtest'))
const Pivot = lazy(() => import('./review/Pivot')) // 自由透视：Perspective 几 MB，只在打开时加载（18 第 10.5 条）

type Tab = 'log' | 'perf' | 'pivot' | 'mae' | 'findings' | 'playbooks' | 'hl' | 'bt' | 'practice'
const TABS: Tab[] = ['log', 'perf', 'pivot', 'mae', 'findings', 'playbooks', 'hl', 'bt', 'practice']
// 这几个标签不看某个账户的记录：不显示账户、来源和汇总
const OWN: Tab[] = ['practice', 'playbooks', 'hl', 'bt']
const tone = (v: number) => (v > 0 ? 'up' : v < 0 ? 'down' : '')
const fmtR = (v: number | null | undefined) => (v == null ? '—' : `${v > 0 ? '+' : v < 0 ? '−' : ''}${locDec(Math.abs(v).toFixed(2))}R`)
const num = (v: number | null | undefined, d = 2) => (v == null ? '—' : locDec(v.toFixed(d)))

function useJ<T>(path: string | null, ver: number): { data: T | null; error: string | null; loading: boolean } {
  const [st, set] = useState<{ data: T | null; error: string | null; loading: boolean }>({ data: null, error: null, loading: !!path })
  useEffect(() => {
    if (!path) { set({ data: null, error: null, loading: false }); return }
    let alive = true
    set((s) => ({ ...s, loading: true }))
    api<T>('GET', path).then((d) => { if (alive) set({ data: d, error: null, loading: false }) }).catch((e) => { if (alive) set({ data: null, error: e instanceof ApiError ? e.code : 'ERR', loading: false }) })
    return () => { alive = false }
  }, [path, ver])
  return st
}

export default function Review() {
  const t = useT()
  const s = useSession()
  const { current } = usePaperAccounts(true)
  const [tab, setTabRaw] = useState<Tab>(() => { const v = new URLSearchParams(location.search).get('tab'); return TABS.includes(v as Tab) ? (v as Tab) : 'log' })
  const setTab = (v: Tab) => { setTabRaw(v); setParam('review', 'tab', v) } // 地址栏写了 tab 的，跟着换
  const [origin, setOrigin] = useState<Origin>('manual')
  const acct = current?.id ?? null // 看哪个账户：顶栏的账户切换（D73），这里不再单独选
  const [ver, setVer] = useState(0)
  const bump = () => setVer((v) => v + 1)
  useEffect(() => { document.title = `${tr('bar.review')} · ${BRAND}` }, [])
  const q = acct ? `/api/journal/a/${acct}` : null
  const trades = useJ<{ trades: JTrade[]; shot_used: number; shot_max: number }>(q && `${q}/trades?origin=${origin}`, ver)
  const stats = useJ<{ stats: JStats; playbook_names: Record<string, string> }>(q && `${q}/stats?origin=${origin}&tz=${tzOffsetMin()}`, ver)
  const pbs = useJ<{ playbooks: Playbook[] }>(s.state === 'in' ? '/api/journal/playbooks' : null, ver)
  const [picked, setPicked] = useState<string | null>(null)
  const all = stats.data?.stats.all
  // 还没有交易时，第一屏不放空表，进回放练习。只自动切一次，之后听用户的。
  const autoPractice = useRef(!new URLSearchParams(location.search).get('tab'))
  // 看所有来源：账户里只有 AI 或机器人的交易时，不能切走，否则连切换来源的按钮都看不到
  const everything = useJ<{ stats: JStats }>(q && `${q}/stats?origin=all&tz=${tzOffsetMin()}`, ver)
  const noTrades = everything.data?.stats.all.n === 0
  // 在练习页切账户或来源，说明想看记录，回到日志
  const leavePractice = () => { if (tab === 'practice') setTab('log') }
  // 在练习页从顶栏换了账户，说明想看那个账户的记录（第一次拿到账户不算）
  const lastAcct = useRef(acct)
  useEffect(() => {
    if (lastAcct.current !== null && lastAcct.current !== acct) leavePractice()
    lastAcct.current = acct
  }, [acct]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!autoPractice.current || !everything.data) return
    autoPractice.current = false
    if (noTrades) setTab('practice')
  }, [everything.data, noTrades])
  const playbooks = pbs.data?.playbooks ?? []
  if (s.state === 'out') {
    return (
      <div className="lp">
        <Bar page="review" />
        <p className="rv-hint">{t('rv.outHint')}<a className="btn primary sm" {...linkProps('login')}>{t('bar.login')}</a></p>
        <div className="rv-pane"><Suspense fallback={null}><Practice /></Suspense></div>
      </div>
    )
  }
  return (
    <div className="lp">
      <Bar page="review" />
      <div className="lp-head rv-head">
        <Seg label={t('rv.tabs')} value={tab} onChange={setTab} options={TABS.map((k) => ({ value: k, label: t(`rv.tab.${k}` as Key) }))} />
        {(!OWN.includes(tab) || (noTrades && (tab === 'practice' || tab === 'playbooks'))) && (
          <div className="rv-tools">
            <Seg label={t('rv.origin')} value={origin} onChange={(v) => { setOrigin(v); leavePractice() }} options={[{ value: 'manual', label: t('rv.o.manual') }, { value: 'ai', label: t('rv.o.ai') }, { value: 'bot', label: t('rv.o.bot') }, { value: 'signal', label: t('rv.o.signal') }, { value: 'all', label: t('rv.o.all') }]} />
          </div>
        )}
      </div>
      {!OWN.includes(tab) && all && <StatLine s={all} />}
      {origin === 'bot' && acct && !OWN.includes(tab) && <BotCards acct={acct} />}
      <div className="rv-pane" key={tab}>
        {tab === 'log' && <LogTab stats={stats.data?.stats} origin={origin} data={trades.data} acct={acct} playbooks={playbooks} picked={picked} onPick={setPicked} onChanged={bump} loading={trades.loading} />}
        {tab === 'perf' && stats.data && <PerfTab st={stats.data.stats} names={stats.data.playbook_names} acct={acct} />}
        {tab === 'pivot' && trades.data && <Suspense fallback={<p className="rv-hint">{t('pv.loading')}</p>}><Pivot trades={trades.data.trades} names={stats.data?.playbook_names ?? {}} /></Suspense>}
        {tab === 'mae' && acct && <MaeTab acct={acct} origin={origin} ver={ver} onPick={(id) => { setPicked(id); setTab('log') }} />}
        {tab === 'findings' && acct && <FindingsTab acct={acct} origin={origin} ver={ver} />}
        {tab === 'playbooks' && <PlaybooksTab list={playbooks} stats={stats.data} onChanged={bump} />}
        {tab === 'practice' && noTrades && <p className="rv-hint">{t('rv.noTradesHint')}</p>}
        {tab === 'practice' && <Suspense fallback={null}><Practice /></Suspense>}
        {tab === 'hl' && <Suspense fallback={null}><Highlights /></Suspense>}
        {tab === 'bt' && <Suspense fallback={null}><Backtest /></Suspense>}
      </div>
    </div>
  )
}

/** 选"机器人"时：每个机器人一张卡（D83 第 9 条） */
function BotCards({ acct }: { acct: number }) {
  const t = useT()
  const d = useBots(true)
  const list = (d?.bots ?? []).filter((b) => b.account === acct)
  if (!list.length) return null
  return (
    <div className="rv-bots mk-scroll" data-bot-cards>
      {list.map((b) => (
        <div className={`rv-bot ${b.status === 'stopped' ? 'off' : ''}`} key={b.id} data-bot-card={b.id}>
          <div className="rv-bot-h"><b>{baseOf(b.params.instrument)}</b><span>{t(`bt.k.${b.kind}` as Key)}</span><em>{statusText(b)}</em></div>
          <div className="rv-bot-n">
            <span><i>{t('bt.c.realized')}</i><b className={`num ${tone(Number(b.realized))}`}><Money text={fmtSigned(Number(b.realized))} /></b></span>
            <span><i>{t('rv.botFills')}</i><b className="num">{b.kind === 'spot_grid' || b.kind === 'perp_grid' ? t('bt.pairs', { n: b.pairs }) : b.fills}</b></span>
          </div>
        </div>
      ))}
    </div>
  )
}

/** 顶部一行：胜率、期望值、盈亏比、利润因子、平均 R。样本太少时写明 */
function StatLine({ s }: { s: Stat }) {
  const t = useT()
  return (
    <div className="heat-read rv-read">
      <span className="hr-cell"><em>{t('rv.s.n')}</em><b className="num">{s.n}</b>{s.small && <i className="rv-small" title={t('rv.smallHint')}>{t('rv.small')}</i>}</span>
      <span className="hr-cell" title={t('rv.hint.win')}><em>{t('rv.s.win')}</em><b className="num">{locDec((s.win_rate * 100).toFixed(1))}%</b></span>
      <span className="hr-cell" title={t('rv.hint.exp')}><em>{t('rv.s.exp')}</em><b className={`num ${tone(s.expectancy)}`}><Money text={fmtSigned(s.expectancy)} /></b></span>
      <span className="hr-cell" title={t('rv.hint.payoff')}><em>{t('rv.s.payoff')}</em><b className="num">{num(s.payoff)}</b></span>
      <span className="hr-cell" title={t('rv.hint.pf')}><em>{t('rv.s.pf')}</em><b className="num">{num(s.profit_factor)}</b></span>
      <span className="hr-cell" title={t('rv.hint.r')}><em>{t('rv.s.r')}</em><b className={`num ${tone(s.avg_r ?? 0)}`}>{fmtR(s.avg_r)}</b><i className="num">{t('rv.rCount', { n: s.r_count })}</i></span>
      <span className="hr-cell hr-flow"><em>{t('rv.s.net')}</em><b className={`num ${tone(s.net)}`}><Money text={fmtSigned(s.net)} unit="USDT" /></b></span>
    </div>
  )
}

// ---------- 交易日志 ----------

const EMOTIONS = ['calm', 'confident', 'fomo', 'fear', 'revenge', 'tired'] as const

function LogTab({ stats, origin, data, acct, playbooks, picked, onPick, onChanged, loading }: { stats?: JStats;  origin: Origin; data: { trades: JTrade[]; shot_used: number; shot_max: number } | null; acct: number | null; playbooks: Playbook[]; picked: string | null; onPick: (id: string | null) => void; onChanged: () => void; loading: boolean }) {
  const t = useT()
  const trades = data?.trades ?? []
  const cur = trades.find((x) => x.id === picked) ?? null
  const hint = (k: 'h' | 'd' | 'm' | 's', n: number) => t(`rv.h.${k}` as Key, { n })
  const hold = (ms: number) => holdText(ms, (k, a) => hint(k.slice(-1) as 'h', a.n))
  // 没选中交易时，右边一直有东西看：盈亏日历和回撤
  const side = !cur && stats && stats.all.n > 0 ? (
    <aside className="rv-side">
      <section className="rv-cell">
        <div className="cn-head" title={t('rv.hint.cal')}><span>{t('rv.calTitle')}</span></div>
        <Calendar days={stats.calendar} />
      </section>
      <section className="rv-cell">
        <div className="cn-head" title={t('rv.hint.under')}><span>{t('rv.underTitle')}</span><em>{t('rv.underSub', { v: fmtDd(stats.underwater) })}</em></div>
        <div className="cn-canvas"><UnderwaterChart points={stats.underwater} /></div>
      </section>
    </aside>
  ) : null
  if (!loading && !trades.length) return <div className="rv-log"><div className="rv-out"><p>{t(origin === 'ai' ? 'rv.emptyAi' : 'rv.empty')}</p></div>{side}</div>
  return (
    <div className={`rv-log ${cur ? 'has-detail' : ''}`}>
      <div className="rv-list">
        <table className="cn-table rv-table">
          <thead><tr><th>{t('rv.c.time')}</th><th>{t('rv.c.inst')}</th><th>{t('rv.c.side')}</th><th className="r">{t('rv.c.net')}</th><th className="r">R</th><th className="r">{t('rv.c.hold')}</th><th>{t('rv.c.flags')}</th><th>{t('rv.c.tags')}</th></tr></thead>
          <tbody>
            {trades.map((x) => (
              <tr key={x.id} className={x.id === picked ? 'on' : ''} onClick={() => onPick(x.id === picked ? null : x.id)} tabIndex={0} onKeyDown={(e) => { if (e.key === 'Enter') onPick(x.id) }}>
                <td className="num">{fmtTime(x.open_time)}</td>
                <td>{baseOf(x.instrument)}{x.origin !== 'manual' && <i className="rv-origin">{t(`rv.o.${x.origin === 'ai' || x.origin === 'bot' || x.origin === 'signal' ? x.origin : 'mixed'}` as Key)}</i>}</td>
                <td className={x.side === 'long' ? 'up' : 'down'}>{t(x.side === 'long' ? 'heat.long' : 'heat.short')}</td>
                <td className={`r num ${tone(x.net)}`}>{x.close_time ? <Money text={fmtSigned(x.net)} /> : <span className="rv-open">{t('rv.open')}</span>}</td>
                <td className={`r num ${tone(x.r ?? 0)}`}>{x.close_time ? (x.r == null ? t('rv.noR') : fmtR(x.r)) : '—'}</td>
                <td className="r num">{x.close_time ? hold(x.hold_ms) : '—'}</td>
                <td>{x.violations.length > 0 && <span className="rv-flag" title={x.violations.map((v) => t(`rv.v.${v}` as Key)).join(listSep())}>{t(`rv.vs.${x.violations[0]}` as Key)}{x.violations.length > 1 ? ` +${x.violations.length - 1}` : ''}</span>}{(x.shots?.length ?? 0) > 0 && <span className="rv-shot-n" title={t('rv.shots')}>▣</span>}</td>
                <td className="rv-tags">{x.note?.tags.slice(0, 3).join(' ')}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {side}
      {cur && acct && <TradeDetail key={cur.id} tr={cur} acct={acct} playbooks={playbooks} used={data?.shot_used ?? 0} max={data?.shot_max ?? 0} onClose={() => onPick(null)} onChanged={onChanged} />}
    </div>
  )
}

function TradeDetail({ tr: x, acct, playbooks, used, max, onClose, onChanged }: { tr: JTrade; acct: number; playbooks: Playbook[]; used: number; max: number; onClose: () => void; onChanged: () => void }) {
  const t = useT()
  const hidden = usePrivate()
  const [tags, setTags] = useState((x.note?.tags ?? []).join(' '))
  const [note, setNote] = useState(x.note?.note ?? '')
  const [emotion, setEmotion] = useState(x.note?.emotion ?? '')
  const [pb, setPb] = useState(String(x.playbook ?? 0))
  const [replay, setReplay] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [delShot, setDelShot] = useState<number | null>(null)
  const file = useRef<HTMLInputElement>(null)
  const save = async () => {
    const body: Note = { tags: tags.split(/[\s,，]+/).filter(Boolean), note, emotion, playbook: Number(pb) }
    try {
      await api('PUT', `/api/journal/a/${acct}/trades/${x.id}/note`, body)
      setDirty(false)
      toast(t('rv.saved'))
      onChanged()
    } catch { toast(t('rv.saveFail')) }
  }
  const upload = async (e: ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0]
    e.target.value = ''
    if (!f) return
    if (f.size > LIMITS.shot_mb * 1048576) { toast(t('rv.shotBig', { mb: LIMITS.shot_mb })); return }
    const r = await fetch(`/api/journal/a/${acct}/trades/${x.id}/shots`, { method: 'POST', body: f, headers: { 'X-QS-Client': 'web' }, credentials: 'include' })
    if (!r.ok) {
      const c = (await r.json().catch(() => ({ code: '' }))).code
      toast(t(c === 'SHOT_QUOTA' ? 'rv.shotQuota' : c === 'SHOT_TYPE' ? 'rv.shotType' : 'rv.shotBig', { mb: c === 'SHOT_QUOTA' ? LIMITS.shots_total_mb : LIMITS.shot_mb }))
      return
    }
    toast(t('rv.saved'))
    onChanged()
  }
  const del = async (id: number) => {
    await api('DELETE', `/api/journal/shots/${id}`)
    onChanged()
  }
  return (
    <aside className="rv-detail">
      <div className="rv-d-head">
        <b>{baseOf(x.instrument)}</b>
        <span className={x.side === 'long' ? 'up' : 'down'}>{t(x.side === 'long' ? 'heat.long' : 'heat.short')}</span>
        <span className="num rv-d-time">{fmtTime(x.open_time)}{x.close_time ? ` – ${fmtTime(x.close_time)}` : ''}</span>
        <button type="button" className="btn secondary sm" onClick={() => setReplay(true)} data-rv="replay">{t('rv.replay')}</button>
        <button type="button" className="icon-btn" onClick={onClose} aria-label={t('common.close')}>×</button>
      </div>
      <div className="rv-d-body">
        <dl className="rv-kv">
          <dt>{t('rv.d.entry')}</dt><dd className="num">{fmtPrice(String(x.entry))}</dd>
          <dt>{t('rv.d.exit')}</dt><dd className="num">{x.close_time ? fmtPrice(String(x.exit)) : '—'}</dd>
          <dt>{t('rv.d.qty')}</dt><dd className="num"><Money text={String(x.qty)} /></dd>
          <dt>{t('rv.d.stop')}</dt><dd className="num">{x.stop ? fmtPrice(String(x.stop)) : t('rv.noStop')}</dd>
          <dt>{t('rv.d.risk')}</dt><dd className="num">{x.risk ? <Money text={fmtAmt(x.risk)} /> : '—'}</dd>
          <dt>R</dt><dd className={`num ${tone(x.r ?? 0)}`}>{x.r == null ? t('rv.noR') : fmtR(x.r)}</dd>
        </dl>
        <TradeFall x={x} />
        {x.violations.length > 0 && (
          <ul className="rv-viol">{x.violations.map((v) => <li key={v}><i className="sq warn" />{t(`rv.v.${v}` as Key)}</li>)}</ul>
        )}
        {x.stops.length > 1 && (
          <div className="rv-stops"><em>{t('rv.d.stops')}</em>{x.stops.map((s, i) => <span key={i} className="num">{s.kind === 'cancel' ? t('rv.stopCancel') : fmtPrice(String(s.px))}</span>)}</div>
        )}
        {x.close_time > 0 && <AiReview key={`${acct}:${x.id}`} acct={acct} id={x.id} />}
        <div className="rv-note">
          {playbooks.length > 0 && <label className="rv-f"><span>{t('rv.d.playbook')}</span><Select label={t('rv.d.playbook')} value={pb} onChange={(v) => { setPb(v); setDirty(true) }} options={[{ value: '0', label: t('rv.none') }, ...playbooks.map((p) => ({ value: String(p.id), label: p.name }))]} /></label>}
          <label className="rv-f"><span>{t('rv.d.tags')}</span><input className="rv-in" value={tags} placeholder={t('rv.tagsPh')} onChange={(e) => { setTags(e.target.value); setDirty(true) }} /></label>
          <div className="rv-f"><span>{t('rv.d.emotion')}</span>
            <div className="rv-chips">{EMOTIONS.map((k) => <button type="button" key={k} className={emotion === k ? 'on' : ''} onClick={() => { setEmotion(emotion === k ? '' : k); setDirty(true) }}>{t(`rv.e.${k}` as Key)}</button>)}</div>
          </div>
          <label className="rv-f"><span>{t('rv.d.note')}</span><textarea className="rv-in" rows={3} value={note} onChange={(e) => { setNote(e.target.value); setDirty(true) }} /></label>
          <div className="rv-note-act"><button type="button" className="btn primary sm" disabled={!dirty} onClick={save} data-rv="save">{t('common.save')}</button></div>
          <div className="rv-f"><span>{t('rv.shots')}</span>
            <div className="rv-shots">
              {(x.shots ?? []).map((s) => (
                <span className="rv-shot" key={s.id}>
                  <a href={`/api/journal/shots/${s.id}`} target="_blank" rel="noreferrer"><img src={`/api/journal/shots/${s.id}`} alt={t('rv.shots')} className={hidden ? 'rv-blur' : ''} /></a>
                  <button type="button" onClick={() => setDelShot(s.id)} aria-label={t('rv.delete')}>×</button>
                </span>
              ))}
              <button type="button" className="rv-shot-add" onClick={() => file.current?.click()} data-rv="shot">+</button>
              <input ref={file} type="file" accept="image/png,image/jpeg,image/webp" hidden onChange={upload} />
            </div>
            <em className="rv-quota">{t('rv.shotQuotaLine', { used: locDec((used / (1 << 20)).toFixed(1)), max: Math.round(max / (1 << 20)) })}</em>
          </div>
        </div>
      </div>
      {replay && <Suspense fallback={null}><TradeReplay trade={x} onClose={() => setReplay(false)} /></Suspense>}
      {/* 小 × 一点就删、删了找不回（11-17 走查）：先问一句 */}
      <Confirm open={delShot != null} title={t('rv.shotDel')} body={t('rv.shotDelBody')} danger btn={t('rv.delete')} onClose={() => setDelShot(null)} run={() => del(delShot!)} />
    </aside>
  )
}

/** AI 点评（模块 7）：用户点了才生成，只点评这一笔已经平掉的交易 */
function AiReview({ acct, id }: { acct: number; id: string }) {
  const t = useT()
  const lang = useLang()
  const [d, setD] = useState<{ enabled: boolean; review?: { text: string } | null; left?: number } | null>(null)
  const [busy, setBusy] = useState(false)
  const path = `/api/journal/a/${acct}/trades/${encodeURIComponent(id)}/review?lang=${lang}`
  useEffect(() => { setD(null); void api<typeof d>('GET', path).then(setD).catch(() => {}) }, [path])
  if (!d?.enabled) return null
  const run = async () => {
    setBusy(true)
    try { setD(await api<typeof d>('POST', path)) } catch (e) {
      toast(errText(e instanceof ApiError ? e.code : 'AI_FAILED'))
    } finally { setBusy(false) }
  }
  const lines = (d.review?.text ?? '').split('\n').filter(Boolean).map((l) => {
    const m = /^([^:：]{1,24})[:：]\s*(.*)$/.exec(l)
    const v = m ? m[2] : l
    return { k: m ? m[1] : '', v: v.charAt(0).toUpperCase() + v.slice(1) }
  })
  return (
    <section className="rv-ai" data-ai>
      <header>
        <b>{t('rv.ai.title')}</b>
        {!d.review && <button type="button" className="btn secondary sm" data-ai-go disabled={busy || d.left === 0} onClick={() => void run()}>{busy ? t('rv.ai.busy') : t('rv.ai.go')}</button>}
      </header>
      {lines.length > 0 && <dl data-ai-text>{lines.map((l, i) => <div key={i}>{l.k && <dt>{l.k}</dt>}<dd>{l.v}</dd></div>)}</dl>}
      <p>{t('rv.ai.note')}{!d.review && d.left != null && <> {t('rv.ai.left', { n: d.left })}</>}</p>
    </section>
  )
}

/** 一笔交易的成本瀑布 */
function TradeFall({ x }: { x: JTrade }) {
  return <Fall w={{ ideal: x.gross + x.slip, slip: x.slip, gross: x.gross, fees: x.fees, funding: x.funding, net: x.net, unknown_slip: x.slip_known ? 0 : 1 }} compact />
}

/** 成本瀑布（D）：一级一级扣到净收益，数字能加回去 */
function Fall({ w, compact }: { w: JStats['waterfall']; compact?: boolean }) {
  const t = useT()
  const hasSlip = !w.unknown_slip || w.slip !== 0
  const rows: { k: string; v: number; from: number; to: number; total?: boolean }[] = []
  let cur = 0
  const push = (k: string, v: number, total?: boolean) => {
    if (total) { rows.push({ k, v, from: 0, to: v, total }); cur = v; return }
    rows.push({ k, v, from: cur, to: cur + v }); cur += v
  }
  if (hasSlip) { push('ideal', w.ideal, true); push('slip', -w.slip); push('gross', w.gross, true) } else push('gross', w.gross, true)
  push('fees', -w.fees)
  push('funding', w.funding)
  push('net', w.net, true)
  const lo = Math.min(0, ...rows.flatMap((r) => [r.from, r.to])), hi = Math.max(0, ...rows.flatMap((r) => [r.from, r.to]))
  const pct = (v: number) => ((v - lo) / Math.max(hi - lo, 1e-9)) * 100
  return (
    <div className={`rv-fall ${compact ? 'compact' : ''}`}>
      {rows.map((r) => (
        <div className={`rv-fall-row ${r.total ? 'total' : ''}`} key={r.k}>
          <span className="rv-fall-k">{t(`rv.f.${r.k}` as Key)}</span>
          <span className="rv-fall-bar"><i className={r.total ? (r.v >= 0 ? 'white' : 'down-bg') : r.v >= 0 ? 'up-bg' : 'down-bg'} style={{ left: `${pct(Math.min(r.from, r.to))}%`, width: `${Math.max(0.5, pct(Math.max(r.from, r.to)) - pct(Math.min(r.from, r.to)))}%` }} /></span>
          <span className={`rv-fall-v num ${r.total ? '' : tone(r.v)}`}><Money text={fmtSigned(r.v, compact ? 2 : 2)} /></span>
        </div>
      ))}
      {w.unknown_slip > 0 && <p className="rv-fall-note">{t('rv.slipUnknown', { n: w.unknown_slip })}</p>}
    </div>
  )
}

// ---------- 绩效 ----------

function PerfTab({ st, names, acct }: { st: JStats; names: Record<string, string>; acct: number | null }) {
  const t = useT()
  const [g, setG] = useState<'instrument' | 'slot' | 'weekday' | 'playbook'>('instrument')
  const rows = st.groups[g] ?? []
  const label = (k: string) => {
    if (g === 'instrument') return baseOf(k)
    if (g === 'slot') return t(`rv.slot.${k}` as Key)
    if (g === 'weekday') return t(`rv.day.${k}` as Key)
    return k === 'none' ? t('rv.none') : names[k] ?? k
  }
  const maxR = Math.max(1, ...st.r_dist.map((b) => b.n))
  const mn = (v: number) => (v < 0 ? `−${-v}` : String(v))
  const rLabel = (lo: number, hi: number) => (lo <= -1e8 ? `< ${mn(hi)}R` : hi >= 1e8 ? `≥ ${mn(lo)}R` : `${mn(lo)} ~ ${mn(hi)}R`)
  return (
    <div className="rv-perf">
      <section className="rv-cell rv-cal">
        <div className="cn-head" title={t('rv.hint.cal')}><span>{t('rv.calTitle')}</span></div>
        <Calendar days={st.calendar} />
      </section>
      <section className="rv-cell rv-under">
        <div className="cn-head" title={t('rv.hint.under')}><span>{t('rv.underTitle')}</span><em>{t('rv.underSub', { v: fmtDd(st.underwater) })}</em></div>
        <div className="cn-canvas"><UnderwaterChart points={st.underwater} /></div>
      </section>
      <section className="rv-cell rv-rdist">
        <div className="cn-head" title={t('rv.hint.rdist')}><span>{t('rv.rTitle')}</span>{st.no_r > 0 && <em>{t('rv.rSub', { n: st.no_r })}</em>}</div>
        {/* 一笔都没有 R（都没设止损）时柱子全是 0，空着像坏了（11-17 走查）：直接说为什么 */}
        {st.r_dist.every((b) => !b.n) ? <p className="rv-rd-empty">{t(st.all.n ? 'rv.rNone' : 'rv.noTradesYet')}</p> : <div className="rv-bars">
          {st.r_dist.map((b) => (
            <div className="rv-bar" key={b.lo} title={`${rLabel(b.lo, b.hi)}: ${b.n}`}>
              <span className="num rv-bar-n">{b.n || ''}</span>
              <i className={b.lo >= 0 ? 'up-bg' : 'down-bg'} style={{ height: `${(b.n / maxR) * 100}%` }} />
              <span className="num rv-bar-k">{b.lo <= -1e8 ? `< ${mn(b.hi)}` : b.hi >= 1e8 ? `≥ ${mn(b.lo)}` : `${mn(b.lo)}~${mn(b.hi)}`}</span>
            </div>
          ))}
        </div>}
      </section>
      <section className="rv-cell rv-wf">
        <div className="cn-head" title={t('rv.hint.fall')}><span>{t('rv.fallTitle')}</span><em>{t('rv.fallSub')}</em></div>
        <Fall w={st.waterfall} />
      </section>
      <section className="rv-cell rv-groups">
        <div className="cn-head rv-g-head">
          <span>{t('rv.groupTitle')}</span>
          <Seg label={t('rv.groupTitle')} value={g} onChange={setG} options={(['instrument', 'slot', 'weekday', 'playbook'] as const).map((k) => ({ value: k, label: t(`rv.g.${k}` as Key) }))} />
          {acct && <span className="rv-export"><em>{t('rv.export')}</em>{(['trades', 'fills', 'funding'] as const).map((k) => <a key={k} className="rv-link" href={`/api/journal/a/${acct}/export?kind=${k}`} download>{t(`rv.x.${k}` as Key)}</a>)}</span>}
        </div>
        <div className="cn-scroll">
          <table className="cn-table">
            <thead><tr><th>{t(`rv.g.${g}` as Key)}</th><th className="r">{t('rv.s.n')}</th><th className="r">{t('rv.s.win')}</th><th className="r">{t('rv.s.exp')}</th><th className="r">{t('rv.s.payoff')}</th><th className="r">{t('rv.s.pf')}</th><th className="r">{t('rv.s.r')}</th><th className="r">{t('rv.s.net')}</th></tr></thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.key}>
                  <td>{label(r.key)}{r.small && <i className="rv-small" title={t('rv.smallHint')}>{t('rv.smallS')}</i>}</td>
                  <td className="r num">{r.n}</td>
                  <td className="r num">{(r.win_rate * 100).toFixed(0)}%</td>
                  <td className={`r num ${tone(r.expectancy)}`}><Money text={fmtSigned(r.expectancy)} /></td>
                  <td className="r num">{num(r.payoff)}</td>
                  <td className="r num">{num(r.profit_factor)}</td>
                  <td className={`r num ${tone(r.avg_r ?? 0)}`}>{fmtR(r.avg_r)}</td>
                  <td className={`r num ${tone(r.net)}`}><Money text={fmtSigned(r.net)} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  )
}

/** 盈亏日历：最近几个月，每天一格。颜色是盈亏方向，深浅是金额 */
function Calendar({ days }: { days: { day: string; net: number; n: number }[] }) {
  const t = useT()
  const lang = useLang()
  const map = new Map(days.map((d) => [d.day, d]))
  const end = days.length ? new Date(`${days[days.length - 1].day}T00:00:00Z`) : new Date()
  const start = new Date(end)
  start.setUTCDate(start.getUTCDate() - 7 * 13 - ((end.getUTCDay() + 6) % 7))
  const cells: { key: string; d?: { net: number; n: number } }[] = []
  for (const c = new Date(start); c <= end; c.setUTCDate(c.getUTCDate() + 1)) {
    const key = c.toISOString().slice(0, 10)
    cells.push({ key, d: map.get(key) })
  }
  const top = Math.max(1, ...days.map((d) => Math.abs(d.net)))
  // 每一列的月份：只在这个月第一次出现的那一列写
  const months: { col: number; label: string }[] = []
  let lastM = ''
  cells.forEach((c, i) => {
    const m = c.key.slice(5, 7)
    if (i % 7 === 0 && m !== lastM) { months.push({ col: i / 7, label: new Date(Date.UTC(2000, Number(m) - 1, 1)).toLocaleString(lang === 'zh' ? 'zh-CN' : 'en-US', { month: 'short', timeZone: 'UTC' }) }); lastM = m }
  })
  return (
    <div className="rv-cal-wrap">
      <div className="rv-cal-months" style={{ gridTemplateColumns: `repeat(${Math.ceil(cells.length / 7)}, 1fr)` }}>
        {months.map((m) => <span key={m.col} style={{ gridColumn: m.col + 1 }}>{m.label}</span>)}
      </div>
      <div className="rv-cal-days">{['d0', 'd2', 'd4'].map((d, i) => <span key={d} style={{ gridRow: i * 2 + 1 }}>{t(`rv.day.${d}` as Key)}</span>)}</div>
      <div className="rv-cal-grid">
        {cells.map((c) => (
          <i key={c.key} className={c.d ? (c.d.net >= 0 ? 'up-bg' : 'down-bg') : ''} style={c.d ? { opacity: 0.25 + 0.75 * Math.sqrt(Math.abs(c.d.net) / top) } : undefined} title={c.d ? `${c.key}  ${t('rv.calTip', { n: c.d.n })}  ${fmtSigned(c.d.net)}` : c.key} data-day={c.key} />
        ))}
      </div>
    </div>
  )
}

// ---------- MAE / MFE ----------

function MaeTab({ acct, origin, ver, onPick }: { acct: number; origin: Origin; ver: number; onPick: (id: string) => void }) {
  const t = useT()
  const hidden = usePrivate()
  const [unit, setUnit] = useState<'r' | 'usd'>('r')
  const { data, loading } = useJ<{ items: Excursion[] }>(`/api/journal/a/${acct}/excursions?origin=${origin}`, ver)
  const items = useMemo(() => data?.items ?? [], [data])
  const src = (k: string) => items.filter((e) => e.source === k).length
  const wins = items.filter((e) => e.net > 0 && e.mae_r != null).map((e) => e.mae_r!)
  const loses = items.filter((e) => e.net <= 0 && e.mae_r != null).map((e) => e.mae_r!)
  const mw = median(wins), ml = median(loses)
  const beyond = items.filter((e) => e.net > 0 && (e.mae_r ?? 0) >= 0.75).length
  const noR = items.filter((e) => e.mae_r == null).length
  if (!loading && !items.length) return <div className="rv-out"><p>{t('rv.empty')}</p></div>
  return (
    <div className="rv-mae">
      <div className="heat-read rv-read">
        <span className="hr-cell" title={t('rv.hint.mae')}><em>{t('rv.maeWin')}</em><b className="num">{mw == null ? '—' : `${locDec(mw.toFixed(2))}R`}</b></span>
        <span className="hr-cell"><em>{t('rv.maeLose')}</em><b className="num">{ml == null ? '—' : `${locDec(ml.toFixed(2))}R`}</b></span>
        <span className="hr-cell" title={t('rv.hint.beyond')}><em>{t('rv.maeBeyond')}</em><b className="num">{beyond}</b></span>
        <span className="hr-cell hr-flow" title={t('rv.hint.src')}><i>{t('rv.maeSrc', { a: src('ticks'), b: src('1m') + src('5m'), c: src('none') })}</i></span>
        <span className="rv-unit"><Seg label={t('rv.unit')} value={unit} onChange={setUnit} options={[{ value: 'r', label: 'R' }, { value: 'usd', label: 'USDT' }]} /></span>
      </div>
      <div className="cn-canvas"><ExcursionChart items={items} unit={unit} hidden={hidden} onPick={onPick} /></div>
      <div className="heat-note">
        <span><i className="lg-dot up" />{t('rv.lgWin')}</span>
        <span><i className="lg-dot down" />{t('rv.lgLose')}</span>
        <span>{t('rv.lgStop')}</span>
        {unit === 'r' && noR > 0 && <span className="heat-note-r">{t('rv.maeNoR', { n: noR })}</span>}
      </div>
    </div>
  )
}

// ---------- 结论 ----------

function FindingsTab({ acct, origin, ver }: { acct: number; origin: Origin; ver: number }) {
  const t = useT()
  const { data, loading } = useJ<{ items: Finding[] }>(`/api/journal/a/${acct}/findings?origin=${origin}`, ver)
  const items = data?.items ?? []
  if (!loading && !items.length) return <div className="rv-out"><p>{t('rv.noFindings')}</p></div>
  const usd = (v: number) => <Money text={fmtSigned(v)} unit="USDT" />
  const text = (f: Finding) => {
    const a = f.args ?? {}
    const d = (v: unknown) => locDec(String(v ?? ''))
    switch (f.code) {
      case 'stop_tighter': return t('rv.fd.tight', { k: d(a.k), n: f.n, base: a.base })
      case 'stop_wider': return t('rv.fd.wide', { k: d(a.k), n: f.n, h: d(Math.round((a.hours ?? 1) * 10) / 10) })
      case 'tp_earlier': return t('rv.fd.tp', { x: d(a.x), n: f.n, base: a.base })
      case 'no_stop': return t('rv.fd.noStop', { n: f.n, of: f.of })
      case 'moved_stop': return t('rv.fd.moved', { n: f.n, of: f.of })
      case 'worst_inst': return t('rv.fd.worst', { k: baseOf(f.key ?? ''), n: f.n })
      case 'cost_share': return t('rv.fd.cost', { pct: d(a.pct) })
      default: return f.code
    }
  }
  const isCf = (c: string) => c === 'stop_tighter' || c === 'stop_wider' || c === 'tp_earlier'
  return (
    <div className="cn-scroll rv-findings">
      <p className="rv-find-note">{t('rv.fdNote')}</p>
      <table className="cn-table rv-find">
        <thead><tr><th>{t('rv.fdWhat')}</th><th className="r">{t('rv.fdDelta')}</th><th /></tr></thead>
        <tbody>
          {items.map((f, i) => (
            <tr key={i} data-code={f.code}>
              <td>{text(f)}</td>
              <td className={`r num ${tone(f.delta)}`}>{isCf(f.code) ? t('rv.fdWould') : ''}{usd(f.delta)}</td>
              <td>{f.small && <i className="rv-small" title={t('rv.smallHint')}>{t('rv.small')}</i>}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// ---------- 预案 ----------

function PlaybooksTab({ list, stats, onChanged }: { list: Playbook[]; stats: { stats: JStats } | null; onChanged: () => void }) {
  const t = useT()
  const empty = { id: 0, name: '', entry: '', stop: '', target: '' }
  const [edit, setEdit] = useState<Playbook | null>(null)
  const [del, setDel] = useState<Playbook | null>(null)
  const groups = stats?.stats.groups.playbook ?? []
  const save = async () => {
    if (!edit) return
    try {
      if (edit.id) await api('PUT', `/api/journal/playbooks/${edit.id}`, edit)
      else await api('POST', '/api/journal/playbooks', edit)
      setEdit(null)
      toast(t('rv.saved'))
      onChanged()
    } catch { toast(t('rv.saveFail')) }
  }
  return (
    <div className="rv-pb">
      <div className="rv-pb-head">
        <p>{t('rv.pbIntro')}</p>
        <button type="button" className="btn primary sm" data-rv="pb-new" onClick={() => setEdit({ ...empty })}>{t('rv.pbNew')}</button>
      </div>
      <div className="cn-scroll">
        {list.length === 0 && <p className="rv-out-p">{t('rv.pbEmpty')}</p>}
        <table className="cn-table">
          {list.length > 0 && <thead><tr><th>{t('rv.pb.name')}</th><th>{t('rv.pb.entry')}</th><th>{t('rv.pb.stop')}</th><th>{t('rv.pb.target')}</th><th className="r">{t('rv.s.n')}</th><th className="r">{t('rv.s.win')}</th><th className="r">{t('rv.s.r')}</th><th /></tr></thead>}
          <tbody>
            {list.map((p) => {
              const g = groups.find((x) => x.key === String(p.id))
              return (
                <tr key={p.id} data-pb={p.id}>
                  <td>{p.name}</td>
                  <td className="rv-wrap">{p.entry}</td>
                  <td className="rv-wrap">{p.stop}</td>
                  <td className="rv-wrap">{p.target}</td>
                  <td className="r num">{g?.n ?? 0}</td>
                  <td className="r num">{g ? `${(g.win_rate * 100).toFixed(0)}%` : '—'}</td>
                  <td className={`r num ${tone(g?.avg_r ?? 0)}`}>{fmtR(g?.avg_r)}</td>
                  <td className="r">
                    <button type="button" className="rv-link" onClick={() => setEdit(p)}>{t('rv.edit')}</button>
                    <button type="button" className="rv-link" onClick={() => setDel(p)}>{t('rv.delete')}</button>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
      <Dialog open={!!edit} title={edit?.id ? t('rv.pbEdit') : t('rv.pbNew')} onClose={() => setEdit(null)} width={460}>
        {edit && (
          <div className="rv-pb-form">
            {(['name', 'entry', 'stop', 'target'] as const).map((k) => (
              <label className="rv-f" key={k}><span>{t(`rv.pb.${k}` as Key)}</span>
                {k === 'name' ? <input className="rv-in" data-pbf={k} value={edit.name} maxLength={60} onChange={(e) => setEdit({ ...edit, name: e.target.value })} />
                  : <textarea className="rv-in" rows={2} data-pbf={k} value={edit[k]} placeholder={t(`rv.pb.${k}Ph` as Key)} onChange={(e) => setEdit({ ...edit, [k]: e.target.value })} />}
              </label>
            ))}
            <div className="rv-note-act"><button type="button" className="btn secondary sm" onClick={() => setEdit(null)}>{t('common.cancel')}</button><button type="button" className="btn primary sm" data-rv="pb-save" disabled={!edit.name.trim()} onClick={save}>{t('common.save')}</button></div>
          </div>
        )}
      </Dialog>
      <Dialog open={!!del} title={t('rv.pbDelTitle')} onClose={() => setDel(null)}>
        <p className="rv-del">{t('rv.pbDelText', { name: del?.name ?? '' })}</p>
        <div className="rv-note-act"><button type="button" className="btn secondary sm" onClick={() => setDel(null)}>{t('common.cancel')}</button><button type="button" className="btn danger sm" data-rv="pb-del" onClick={async () => { if (del) { await api('DELETE', `/api/journal/playbooks/${del.id}`); toast(t('rv.pbDeleted', { name: del.name })); setDel(null); onChanged() } }}>{t('rv.delete')}</button></div>
      </Dialog>
    </div>
  )
}
