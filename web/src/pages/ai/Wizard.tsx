// 三步跟随（3.18）：选策略 → 拖滑块分配资金，同时看最坏情况 → 确认。每一步从上一步滑过来。
// 注册后第一次进来出现，可以跳过，以后在帮助里再打开。
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useT, errText, type Key, listSep } from '../../lib/i18n'
import { api, ApiError, useSession } from '../../lib/session'
import { refreshPaperAccounts, usePaperAccounts, selectAccount, type AccountView, useSpecs } from '../../lib/trade'
import { closeWizard, startFollow, useFollows, usePoll, useWizardOpen, type Performance, type StrategyStatus, isRunning } from '../../lib/follow'
import { simulate, stepReturns, type CloudResult } from '../../lib/cloud'
import { Cloud } from '../../charts/Cloud'
import { Dialog } from '../../ui/Dialog'
import { Select } from '../../ui/Select'
import { Slider } from '../../ui/Slider'
import { Money } from '../../ui/Money'
import { fmtAmt } from '../trade/OrderPanel'
import { baseOf, numIn } from '../../lib/format'
import { toast, usePrivate } from '../../lib/privacy'
import { reducedMotion } from '../../lib/anchor'
import { go } from '../../lib/route'
import { useLive, isLiveId } from '../../lib/live'
import { Field } from '../../ui/Field'
import { locDec } from '../../lib/tz'

const STEP_MS = 4 * 3600_000 // 每 4 小时一段
const CLOUD_DAYS = 30
const MIN_SEGMENTS = 30 // 少于这么多段，就算样本不够
const NEW = 'new'

/** 自动建的跟随账户的名字（后端 follow.PaperInitialName，加上以前的叫法） */
const FOLLOW_NAMES = ['策略跟随', 'Strategy follow', 'AI 跟随', 'AI Follow']

/** 跟随比例：百分数 → "0.5×" */
const fmtRatio = (pct: number) => `${locDec(String(+(pct / 100).toFixed(2)))}×`

export function WizardHost() {
  const open = useWizardOpen()
  const s = useSession()
  if (s.state !== 'in') return null
  return <Wizard open={open} />
}

function Wizard({ open }: { open: boolean }) {
  const t = useT()
  const [step, setStep] = useState(0)
  const dirRef = useRef(1)
  const list = usePoll<StrategyStatus[]>(open ? '/api/strategies' : null, 10000).data
  const [sid, setSid] = useState<string | null>(null)
  const strat = list?.find((x) => x.strategy_id === sid) ?? list?.[0] ?? null
  const perf = usePoll<Performance>(open && strat ? `/api/strategies/${strat.strategy_id}/performance` : null, 30000).data
  const { list: accounts } = usePaperAccounts()
  const live = useLive(open) // 交易所账户也能跟（18 第 5.2 条）：真钱，开启要二级密码
  const [tradePw, setTradePw] = useState('')
  const follows = useFollows()
  const specs = useSpecs()

  // 账户：默认新建或用现有的"策略跟随"账户（以前叫"AI 跟随"）；手动和跟策略默认分开（D75 第 12 条）
  const [acct, setAcct] = useState<string>(NEW)
  useEffect(() => {
    if (!open) return
    const ai = accounts?.find((a) => FOLLOW_NAMES.includes(a.name))
    setAcct(ai ? String(ai.id) : NEW)
    setStep(0); setErr(null); setTradePw('')
  }, [open, accounts?.length]) // eslint-disable-line react-hooks/exhaustive-deps
  const [avail, setAvail] = useState(10000)
  useEffect(() => {
    if (!open) return
    if (acct === NEW) { setAvail(10000); return }
    let alive = true
    api<AccountView>('GET', `/api/trade/${acct}`).then((v) => { if (alive) setAvail(Number(v.available)) }).catch(() => {})
    return () => { alive = false }
  }, [open, acct])
  const busyAcct = follows?.find((f) => isRunning(f) && String(f.account) === acct) ?? null

  const [allocPct, setAllocPct] = useState(50)
  // 模拟账户：金额自己填，不限额（D103，用户 11-15"我们不是无限的吗"）：比可用的多时后端把差的补进去，记成加钱。
  // 交易所账户：真钱，只能拿可用的一部分
  const [paperAmt, setPaperAmt] = useState('5000')
  const isLive = isLiveId(Number(acct))
  const alloc = isLive ? Math.max(100, Math.round(((avail * allocPct) / 100) / 10) * 10) : Math.max(0, Math.round(Number(numIn(paperAmt)) || 0))
  const [maxLoss, setMaxLoss] = useState(20)
  const [lev, setLev] = useState(3)
  const [perInst, setPerInst] = useState(100)
  const [ratio, setRatio] = useState(100) // 跟随比例（18 第 6.6 条），百分数：100 = 1 倍
  const [black, setBlack] = useState<string[]>([])
  const [more, setMore] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  // 概率云的样本：策略记录够用就用策略自己的；不够就用 BTC 的波动，并且写明
  const own = useMemo(() => stepReturns((perf?.curve ?? []).map((p) => ({ t: p.t, v: p.nav })), STEP_MS), [perf?.curve])
  const needFallback = own.length < MIN_SEGMENTS
  const btc = usePoll<{ t: number; c: number }[]>(open && needFallback ? '/api/candles?instrument=BTC-PERP&interval=4h&limit=540' : null, 600000).data
  const rets = useMemo(() => {
    if (!needFallback) return own
    if (!btc || btc.length < 10) return []
    return stepReturns(btc.map((b) => ({ t: b.t, v: b.c })), STEP_MS)
  }, [needFallback, own, btc])
  const cloud = useMemo<CloudResult | null>(() => {
    if (!rets.length) return null
    return simulate({ returns: rets, steps: Math.round((CLOUD_DAYS * 24 * 3600_000) / STEP_MS), paths: 3000, maxLoss: maxLoss / 100 })
  }, [rets, maxLoss])

  // 每一步从上一步滑过来（只是给眼睛看的：内容和焦点立刻就绪）
  const stepEl = useRef<HTMLDivElement>(null)
  const first = useRef(true)
  useLayoutEffect(() => {
    if (first.current) { first.current = false; return }
    if (!stepEl.current || reducedMotion()) return
    stepEl.current.animate([{ opacity: 0, transform: `translateX(${dirRef.current * 20}px)` }, { opacity: 1, transform: 'none' }], { duration: 200, easing: 'cubic-bezier(0.16, 1, 0.3, 1)' })
  }, [step])
  const goStep = (n: number) => { dirRef.current = n > step ? 1 : -1; setErr(null); setStep(n) }

  const priv = usePrivate()
  const insts = Object.keys(specs ?? {}).sort()

  const confirm = async () => {
    if (!strat || busy) return
    setBusy(true); setErr(null)
    try {
      const f = await startFollow({ strategy: strat.strategy_id, account: acct === NEW ? undefined : Number(acct), alloc: String(alloc), max_loss: String(maxLoss / 100), max_leverage: lev, per_inst: String(perInst / 100), blacklist: black, ratio: String(ratio / 100), ...(isLive ? { trade_password: tradePw } : {}) })
      refreshPaperAccounts() // 可能刚自动建了"策略跟随"账户
      selectAccount(f.account)
      toast(t('fw.started', { v: fmtAmt(alloc, 0) }))
      closeWizard()
    } catch (e) {
      setErr(e instanceof ApiError ? errText(e.code, e.params) : errText('INTERNAL_ERROR'))
    } finally {
      setBusy(false)
    }
  }

  // 交易所账户只能跟自己的策略（D99）；示例策略不能接真钱账户。服务器说哪些能选，没问到之前一个也不列
  const liveIds = (live?.accounts ?? []).map((a) => a.id)
  const liveCheck = usePoll<{ accounts: Record<string, string> }>(open && strat && liveIds.length
    ? `/api/follows/live-ok?strategy=${encodeURIComponent(strat.strategy_id)}&accounts=${liveIds.join(',')}` : null, 30000).data
  const liveAllowed = (live?.accounts ?? []).filter((a) => liveCheck?.accounts[String(a.id)] === '')
  const liveBlocked = liveCheck ? Object.values(liveCheck.accounts).find((c) => c === 'LIVE_OWN_STRATEGY' || c === 'LIVE_DEMO_REAL') : undefined
  useEffect(() => { // 换了策略，原来选的交易所账户不能再用了：退回默认
    if (isLive && liveCheck && liveCheck.accounts[acct] !== '') setAcct(NEW)
  }, [liveCheck, acct]) // eslint-disable-line react-hooks/exhaustive-deps
  const lossV = (alloc * maxLoss) / 100
  const steps: Key[] = ['wz.s1', 'wz.s2', 'wz.s3']
  const acctName = acct === NEW ? t('wz.newAcct') : (accounts?.find((a) => String(a.id) === acct) ?? live?.accounts.find((a) => String(a.id) === acct))?.name ?? ''
  return (
    <Dialog open={open} title={t('wz.title')} onClose={closeWizard} width={780}>
      <ol className="wz-steps">
        {steps.map((k, i) => <li key={k} className={i === step ? 'on' : i < step ? 'done' : ''}><span className="num">{i + 1}</span>{t(k)}</li>)}
      </ol>
      <div className="wz-body" ref={stepEl} key={step}>
        {step === 0 && (
          <>
            <p className="wz-lead">{t('wz.s1Lead')}</p>
            {!list && <div className="mk-empty">{t('common.loading')}</div>}
            {list && !list.length && (
              <div className="wz-none" data-wz-none>
                <p>{t('wz.none')}</p>
                <button type="button" className="btn secondary" onClick={() => { closeWizard(); go('account&sec=strategies') }}>{t('wz.noneGo')}</button>
              </div>
            )}
            {list && list.length > 0 && (
              <table className="tbl wz-tbl">
                <thead><tr><th>{t('ai.strategy')}</th><th>{t('ai.return')}</th><th>{t('ai.maxdd')}</th><th>{t('ai.trades')}</th><th>{t('ai.status')}</th></tr></thead>
                <tbody>
                  {list.map((s) => {
                    const on = s.strategy_id === strat?.strategy_id
                    const p = on ? perf : null
                    return (
                      <tr key={s.strategy_id} className={on ? 'ai-on' : ''} onClick={() => setSid(s.strategy_id)} tabIndex={0}
                        onKeyDown={(e) => { if (e.key === 'Enter') setSid(s.strategy_id) }}>
                        <td><span className="sym">{s.strategy_id}</span>{s.sample && <span className="ai-tag">{t('ai.sample')}</span>}</td>
                        <td className={p ? (p.return > 0 ? 'up' : p.return < 0 ? 'down' : '') : ''}>{p ? fmtPctV(p.return) : '—'}</td>
                        <td>{p ? fmtPctV(-p.max_drawdown) : '—'}</td>
                        <td>{p ? p.trades : '—'}</td>
                        <td className={s.online ? 't2' : 'warn'}>{t(s.online ? 'sp.online' : 'ai.offline')}</td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            )}
            {strat?.sample && <p className="wz-note">{t('sp.demo')}</p>}
            <p className="wz-note">{t('wz.noPromise')}</p>
          </>
        )}
        {step === 1 && (
          <div className="wz-alloc">
            <div className="wz-form">
              <div className="wz-field">
                <span className="wz-l">{t('wz.account')}</span>
                <Select className="wz-sel" label={t('wz.account')} value={acct} onChange={setAcct}
                  options={[...(accounts?.some((a) => FOLLOW_NAMES.includes(a.name)) ? [] : [{ value: NEW, label: t('wz.newAcct'), hint: t('wz.newAcctHint') }]),
                    ...(accounts ?? []).map((a) => ({ value: String(a.id), label: a.name })),
                    ...liveAllowed.map((a) => ({ value: String(a.id), label: a.name, hint: t(a.env === 'main' ? 'wz.liveMain' : 'wz.liveDemo') }))]} />
                {!isLive && liveBlocked && <span className="wz-h t3" data-wz-live-blocked>{t(liveBlocked === 'LIVE_DEMO_REAL' ? 'wz.liveDemoReal' : 'wz.liveOwnOnly')}</span>}
                {isLive && <span className="wz-h warn-t" data-wz-live>{t('wz.liveNote')}</span>}
              </div>
              <div className="wz-field">
                {isLive ? <>
                  <span className="wz-l">{t('wz.alloc')}<span className="wz-v num"><Money text={fmtAmt(alloc, 0)} unit="USDT" /></span></span>
                  <Slider label={t('wz.alloc')} value={allocPct} min={5} max={100} step={1} onChange={setAllocPct} />
                  <span className="wz-h num">{t('wz.availOf', { p: allocPct })} <Money text={fmtAmt(avail, 0)} /></span>
                </> : <>
                  <Field label={`${t('wz.alloc')} (USDT)`} value={paperAmt} onChange={setPaperAmt} inputMode="decimal" data-wz-alloc />
                  <span className="wz-h">{acct === NEW ? t('wz.allocNewHint') : t('wz.allocPaperHint', { v: fmtAmt(avail, 0) })}</span>
                </>}
              </div>
              <div className="wz-field">
                <span className="wz-l">{t('wz.maxLoss')}<span className="wz-v num">{maxLoss}%</span></span>
                <Slider label={t('wz.maxLoss')} value={maxLoss} min={5} max={50} step={1} onChange={setMaxLoss} ticks={[0, 1 / 3, 2 / 3, 1]} />
                <span className="wz-h">{t('wz.maxLossHint')}</span>
              </div>
              <button type="button" className="wz-more" aria-expanded={more} onClick={() => setMore(!more)}>{t('wz.more')}<i aria-hidden="true" /></button>
              {more && (
                <div className="wz-morebox">
                  <div className="wz-field">
                    <span className="wz-l">{t('wz.lev')}<span className="wz-v num">{lev}×</span></span>
                    <Slider label={t('wz.lev')} value={lev} min={1} max={10} step={1} onChange={setLev} ticks={[0, 2 / 9, 1]} />
                    <span className="wz-h">{t('wz.levHint')}</span>
                  </div>
                  <div className="wz-field">
                    <span className="wz-l">{t('wz.ratio')}<span className="wz-v num">{fmtRatio(ratio)}</span></span>
                    <Slider label={t('wz.ratio')} value={ratio} min={25} max={300} step={25} onChange={setRatio} ticks={[0, 3 / 11, 1]} />
                    <span className="wz-h">{t('wz.ratioHint', { a: '40%', b: `${Math.round(40 * ratio / 100)}%` })}</span>
                  </div>
                  <div className="wz-field">
                    <span className="wz-l">{t('wz.perInst')}<span className="wz-v num">{perInst}%</span></span>
                    <Slider label={t('wz.perInst')} value={perInst} min={10} max={100} step={5} onChange={setPerInst} />
                  </div>
                  <div className="wz-field">
                    <span className="wz-l">{t('wz.black')}</span>
                    <div className="wz-chips">
                      {insts.map((x) => (
                        <button type="button" key={x} aria-pressed={black.includes(x)} onClick={() => setBlack((b) => b.includes(x) ? b.filter((y) => y !== x) : [...b, x])}>{baseOf(x)}</button>
                      ))}
                    </div>
                  </div>
                </div>
              )}
            </div>
            <div className="wz-cloud">
              <Cloud cloud={cloud} alloc={alloc} maxLoss={maxLoss / 100} days={CLOUD_DAYS} priv={priv} />
              {!cloud && <div className="mk-empty wz-cloud-wait">{t(needFallback && btc !== null ? 'wz.cloudNone' : 'wz.cloudWait')}</div>}
              <p className="wz-worst">{t('wz.worst', { v: priv ? '••••' : fmtAmt(lossV, 0) })}</p>
              <p className="wz-note">{!cloud ? '' : needFallback ? t('wz.cloudFallback', { n: own.length, need: MIN_SEGMENTS }) : t('wz.cloudHow', { n: own.length })}{cloud ? ' ' + t('wz.cloudStop', { p: (cloud.stopped * 100).toFixed(0) }) : ''}</p>
            </div>
          </div>
        )}
        {step === 2 && (
          <>
            <dl className="wz-sum">
              <dt>{t('ai.strategy')}</dt><dd><span className="sym">{strat?.strategy_id}</span>{strat?.sample && <span className="ai-tag">{t('ai.sample')}</span>}</dd>
              <dt>{t('wz.account')}</dt><dd>{acctName}{isLive && <span className="wz-live-tag">{t('wz.liveTag')}</span>}</dd>
              <dt>{t('wz.alloc')}</dt><dd className="num"><Money text={fmtAmt(alloc, 0)} unit="USDT" /></dd>
              <dt>{t('wz.maxLoss')}</dt><dd className="num">{maxLoss}% · <Money text={fmtAmt(lossV, 0)} unit="USDT" /></dd>
              <dt>{t('wz.lev')}</dt><dd className="num">{lev}× · {t('wz.isolated')}</dd>
              {ratio !== 100 && <><dt>{t('wz.ratio')}</dt><dd className="num">{fmtRatio(ratio)}</dd></>}
              {perInst < 100 && <><dt>{t('wz.perInst')}</dt><dd className="num">{perInst}%</dd></>}
              {black.length > 0 && <><dt>{t('wz.black')}</dt><dd>{black.map(baseOf).join(listSep())}</dd></>}
            </dl>
            <div className="note wz-auth"><span><b>{t('wz.authB')}</b>{t('wz.authT')}</span></div>
            {isLive && (
              <div className="wz-live" data-wz-live-confirm>
                <p className="cf-live">{t('wz.liveConfirm')}</p>
                <Field label={t('acct.tradePw')} type="password" value={tradePw} onChange={setTradePw} autoComplete="off" />
              </div>
            )}
            <p className="wz-note">{t('wz.afterStart')}</p>
            <p className="wz-note">{t('wz.worst', { v: priv ? '••••' : fmtAmt(lossV, 0) })}</p>
            {busyAcct && <div className="form-err on" role="alert"><div><span>{errText('ACCOUNT_FOLLOWING', { strategy: busyAcct.strategy })}</span></div></div>}
            {err && <div className="form-err on" role="alert"><div><span>{err}</span></div></div>}
          </>
        )}
      </div>
      <div className="dlg-btns wz-foot">
        <button type="button" className="btn ghost" onClick={closeWizard}>{t(step === 0 ? 'wz.skip' : 'common.close')}</button>
        <span className="wz-sp" />
        {step > 0 && <button type="button" className="btn secondary" onClick={() => goStep(step - 1)}>{t('wz.prev')}</button>}
        {step < 2 && <button type="button" className="btn primary" disabled={!strat} onClick={() => goStep(step + 1)}>{t('wz.next')}</button>}
        {step === 2 && <button type="button" className={`btn ${isLive ? 'danger' : 'primary'}`} disabled={busy || !!busyAcct || (isLive && !tradePw)} onClick={confirm}>{t(busy ? 'common.loading' : isLive ? 'wz.confirmLive' : 'wz.confirm')}</button>}
      </div>
    </Dialog>
  )
}

function fmtPctV(v: number) {
  return (v > 0 ? '+' : v < 0 ? '−' : '') + locDec(Math.abs(v * 100).toFixed(2)) + '%'
}
