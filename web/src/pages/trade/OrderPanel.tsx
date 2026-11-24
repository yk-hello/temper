// 下单区（模块 2，验收 2.1–2.6、2.14、2.15）。
// 从上到下：账户读数 → 保证金模式和杠杆 → 买卖方向 → 限价/市价 → 价格 → 数量 → 只减仓、止盈止损 → 预估 → 下单按钮。
// 预估由后端算（和真正下单同一套代码），输入停顿 200 毫秒后刷新。
import { locDec, numLocale } from '../../lib/tz'
import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { useT, errText, reasonText, acctName } from '../../lib/i18n'
import { useSession } from '../../lib/session'
import { linkProps, go } from '../../lib/route'
import { api, ApiError } from '../../lib/session'
import { act, acctEnv, decimalsOf, rejectText, toTick, useTicket, type AccountView, type Order, type Preview, type Spec } from '../../lib/trade'
import type { PaperAccount } from '../../lib/session'
import { baseOf, fmtPrice, numIn } from '../../lib/format'
import { isPrivate, toast, usePrivate } from '../../lib/privacy'
import { Money } from '../../ui/Money'
import { Seg } from '../../ui/Seg'
import { Select } from '../../ui/Select'
import { Dialog } from '../../ui/Dialog'
import { setPrefs, usePrefs } from '../../lib/prefs'
import { useFollows, type Follow } from '../../lib/follow'
import { fmtMs, rttOf, useLatency } from '../../lib/latency'
import { ruleName, useDiscipline, type Rules } from '../../lib/discipline'
import { FollowSide } from '../ai/FollowPanel'
import { HaltNote } from '../../ui/HaltNote'
import { ALGO0, AlgoFields, RiskCalc, TemplateBar, runAlgo, type AlgoForm, type AlgoMode, type Template } from './OrderTools'
import '../../styles/ai.css'

type Side = 'buy' | 'sell'
type Unit = 'coin' | 'usdt'

export function fmtAmt(s: string | number | undefined, d = 2): string {
  const v = Number(s)
  if (s === undefined || s === '' || !Number.isFinite(v)) return '—'
  return v.toLocaleString(numLocale(), { minimumFractionDigits: d, maximumFractionDigits: d })
}
export function fmtSigned(s: string | number | undefined, d = 2): string {
  const v = Number(s)
  if (!Number.isFinite(v)) return '—'
  return (v > 0 ? '+' : v < 0 ? '−' : '') + Math.abs(v).toLocaleString(numLocale(), { minimumFractionDigits: d, maximumFractionDigits: d })
}
const dir = (v: number) => (v > 0 ? 'up' : v < 0 ? 'down' : '')

interface Props {
  inst: string
  last?: string
  bid?: string
  ask?: string
  spec?: Spec
  view: AccountView | null
  current: PaperAccount | null
}


/** GTD 有效多久（D103）：币安要求至少 10 分钟 */
type GtdFor = '1h' | '4h' | '24h' | '3d' | '7d'
const GTD_MS: Record<GtdFor, number> = { '1h': 3_600_000, '4h': 14_400_000, '24h': 86_400_000, '3d': 259_200_000, '7d': 604_800_000 }
/** 按盘口定价（币安 priceMatch）：对手价、排队价，第 1、5、10、20 档 */
const PM_OPTS = ['', 'OPPONENT', 'OPPONENT_5', 'OPPONENT_10', 'OPPONENT_20', 'QUEUE', 'QUEUE_5', 'QUEUE_10', 'QUEUE_20']

export function OrderPanel(p: Props) {
  const t = useT()
  const s = useSession()
  if (s.state !== 'in') {
    return (
      <div className="op op-out">
        <p className="op-out-h">{t('tr.login')}</p>
        <p className="op-out-s">{t('tr.loginSub')}</p>
        <a className="btn primary op-out-b" {...linkProps('login')}>{t('tr.loginBtn')}</a>
      </div>
    )
  }
  if (!p.spec || !p.current) return <div className="mk-empty">{t('common.loading')}</div>
  return <FollowOrTicket {...p} spec={p.spec} current={p.current} />
}

/** 当前账户在跟 AI：下单区换成 AI 状态和分配资金，能切回手动下单（D38，3.20） */
function FollowOrTicket(p: Props & { spec: Spec; current: PaperAccount }) {
  const follows = useFollows()
  const f = follows?.find((x) => x.account === p.current.id && x.status !== 'stopped') ?? null
  const [manual, setManual] = useState<number | null>(null)
  if (f && manual !== p.current.id) return <FollowSide f={f} current={p.current} onManual={() => setManual(p.current.id)} />
  return <Ticket {...p} follow={f} onBackToFollow={() => setManual(null)} />
}

function Ticket({ inst, last, bid, ask, spec, view, current, follow, onBackToFollow }: Props & { spec: Spec; current: PaperAccount; follow: Follow | null; onBackToFollow: () => void }) {
  const t = useT()
  const base = baseOf(inst)
  const acct = current.id
  const set = view?.settings[inst] ?? { leverage: 10, mode: 'cross' as const }
  const pos = view?.positions.find((x) => x.instrument === inst)
  const [side, setSide] = useState<Side>('buy')
  // 预案（5.3）：只有建过才多一行
  const [playbooks, setPlaybooks] = useState<{ id: number; name: string }[]>([])
  const [pbSel, setPbSel] = useState('0')
  useEffect(() => { api<{ playbooks: { id: number; name: string }[] }>('GET', '/api/journal/playbooks').then((r) => setPlaybooks(r.playbooks)).catch(() => undefined) }, [])
  const [type, setType] = useState<'limit' | 'market' | 'cond'>('limit')
  const [tif, setTif] = useState('GTC')
  const [gtd, setGtd] = useState<GtdFor>('24h') // GTD 有效多久（D103）
  const [pm, setPm] = useState('') // 按盘口定价（币安 priceMatch）：空 = 自己填价格
  const [protect, setProtect] = useState(false) // 条件单和随单止盈止损的价格保护
  const [price, setPrice] = useState('')
  const [qty, setQty] = useState('')
  // 条件单：触发价、触发后市价还是限价；触发价按标记价还是最新价（条件单和随单止盈止损共用）
  const [trig, setTrig] = useState('')
  const [exec, setExec] = useState<'market' | 'limit'>('market')
  const [trigBy, setTrigBy] = useState<'mark' | 'last' | 'index'>('mark')
  // 算法单：分批挂单、追价（限价时）；TWAP（市价时）
  const [algo, setAlgo] = useState<AlgoMode>('none')
  const [algoF, setAlgoF] = useState<AlgoForm>(ALGO0)
  // 数量单位默认 USDT（大多数人想的是"投多少 U"），用户切换后记住（交易偏好）
  const prefs = usePrefs()
  const unit: Unit = prefs.unit
  const setUnit = (u: Unit) => setPrefs((p) => ({ ...p, unit: u }))
  const [confirming, setConfirming] = useState(false)
  const [pct, setPct] = useState<number | null>(null)
  const [reduce, setReduce] = useState(false)
  const [tpsl, setTpsl] = useState(false)
  const [tp, setTp] = useState('')
  const [sl, setSl] = useState('')
  const [pv, setPv] = useState<Preview | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [levOpen, setLevOpen] = useState(false)
  const qtyRef = useRef<HTMLInputElement>(null)
  const priv = usePrivate()
  const disc = useDiscipline(5000).view
  const dst = disc?.accounts.find((a) => a.account === acct)
  const dlock = disc?.enabled ? dst?.lock : undefined
  const pd = decimalsOf(spec.tick)
  const qd = decimalsOf(spec.step)

  // 换品种：价格换成新品种的最新价，数量清空；分批的价格区间、追价和 TWAP 的限价也清掉
  // （11-16 走查：BTC 上填的"64,500 到 64,900"切到 ETH 还在，点买入就按 64,500 挂 ETH 买单，等于市价吃进去）
  useEffect(() => {
    setPrice(last ? toTick(last, spec.tick) : '')
    setQty(''); setPct(null); setTp(''); setSl(''); setTrig(''); setErr(null)
    setAlgoF((f) => ({ ...f, lo: '', hi: '', limit: '' }))
  }, [inst]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { if (last) setPrice((p) => p || toTick(last, spec.tick)) }, [last]) // eslint-disable-line react-hooks/exhaustive-deps

  // 点盘口价格 → 填价格；B / S → 切方向并把光标放到数量
  const ticket = useTicket()
  const firstTicket = useRef(ticket.n)
  useEffect(() => {
    if (ticket.n === firstTicket.current) return
    if (ticket.price) {
      if (ticket.kind === 'cond' || (!ticket.kind && type === 'cond')) { setType('cond'); setTrig(toTick(ticket.price, spec.tick)) } // 条件单：价格填进触发价
      else { setType('limit'); setPrice(toTick(ticket.price, spec.tick)); setPm('') }
    }
    if (ticket.side) setSide(ticket.side)
    requestAnimationFrame(() => { qtyRef.current?.focus(); qtyRef.current?.select() })
  }, [ticket.n]) // eslint-disable-line react-hooks/exhaustive-deps

  // 只减仓时，百分比按仓位算
  const reducePos = reduce && pos && ((pos.side === 'long') !== (side === 'buy'))
  const req = useMemo(() => {
    const r: Record<string, unknown> = { instrument: inst, side, type, reduce_only: reduce, trigger_by: trigBy }
    if (type === 'limit') {
      if (pm) r.price_match = pm // 按盘口定价：不带价格，下单那一刻由盘口定
      else r.price = price
      // 分批：预估按真正会挂的价算（区间中点），不按藏起来的价格框（11-16 走查：分批挂在现价下面，预估却写"成交价 65,054、吃单手续费"）
      if (!current.live && algo === 'scale' && Number(algoF.lo) > 0 && Number(algoF.hi) > 0) {
        r.price = toTick(String((Number(algoF.lo) + Number(algoF.hi)) / 2), spec.tick); delete r.price_match
      }
      r.tif = tif
      if (tif === 'GTD') r.good_till = Date.now() + GTD_MS[gtd]
    }
    if (type === 'cond') {
      r.type = exec === 'market' ? 'cond_market' : 'cond_limit'
      r.trigger_px = trig
      if (exec === 'limit') r.price = price
    }
    if (pct !== null) {
      if (reducePos) r.qty = String(Math.floor((Number(pos!.qty) * pct) / 100 / Number(spec.step)) * Number(spec.step))
      else r.margin_pct = String(pct)
    } else if (qty) {
      if (unit === 'usdt') r.notional = qty
      else r.qty = qty
    }
    if (tpsl && !reduce) { if (tp) r.tp = tp; if (sl) r.sl = sl }
    if (protect && (type === 'cond' || (tpsl && !reduce))) r.price_protect = true
    return r
  }, [inst, side, type, price, tif, gtd, pm, protect, qty, unit, pct, reduce, reducePos, pos, tpsl, tp, sl, spec.step, spec.tick, trig, exec, trigBy, algo, algoF.lo, algoF.hi, current.live])

  // 预估（停顿 200 毫秒后请求；只用最新一次的结果）。填了数量就每 1.5 秒重算一次：成交价、保证金、强平价都跟着盘口变
  // （11-17 走查：按 25% 填市价单后预估成交价停在 64,960.1，卖一早到了 64,861）
  const reqKey = JSON.stringify(req)
  const [beat, setBeat] = useState(0)
  const filled = pct !== null || !!qty
  useEffect(() => {
    if (!filled) return
    const id = window.setInterval(() => setBeat((x) => x + 1), 1500)
    return () => window.clearInterval(id)
  }, [filled])
  useEffect(() => {
    let alive = true
    const id = window.setTimeout(async () => {
      try {
        const r = await api<Preview>('POST', `/api/trade/${acct}/preview`, req)
        if (alive) setPv(r)
      } catch { /* 预估失败不打扰，下单时会说明原因 */ }
    }, 200)
    return () => { alive = false; window.clearTimeout(id) }
  }, [reqKey, acct, view?.seq, beat]) // eslint-disable-line react-hooks/exhaustive-deps

  // 框里留着看不准的逗号（0,5：小数点还是千位分隔？numIn 不猜）：直接说，下面的预估也不按乱数算
  const comma = [qty, price, trig, tp, sl].some((x) => x.includes(','))
  const hasQty = !comma && (pct !== null || !!qty)
  // 底部那块（报错、预估、按钮）变高时，面板跟着往下滚同样的高度：原来看得见的那一行不被盖住
  // （11-17 走查：730 高的屏幕上出报错时，"只减仓、止盈止损"那行正好压在报错框下面）
  const goRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = goRef.current
    const sc = el?.closest('.mk-scroll') as HTMLElement | null
    if (!el || !sc || typeof ResizeObserver === 'undefined') return
    let h = el.offsetHeight
    const ro = new ResizeObserver(() => {
      const nh = el.offsetHeight
      if (nh > h && sc.scrollHeight > sc.clientHeight) sc.scrollTop += nh - h
      h = nh
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  // 按 USDT 填数量时，"最多""最少"也按 USDT 说（11-16 走查：框里是 USDT，提示写"最多 1.5 BTC""最小 0.001"，按哪个填都对不上）
  const unitPx = Number(type === 'cond' ? (exec === 'limit' && price ? price : trig) : type === 'limit' && price ? price : last) || 0
  const qtyText = (code: string, params?: Record<string, string>) => {
    if (code === 'QTY_TOO_SMALL' && params?.min) {
      return unit === 'usdt' && unitPx > 0
        ? t('tr.minUsdt', { min: params.min, base, u: fmtAmt(Number(params.min) * unitPx, 2) })
        : t('tr.minCoin', { min: params.min, base })
    }
    return errText(code, params)
  }
  const reject = (e: unknown) => (e instanceof ApiError ? qtyText(e.code, e.params) : rejectText(e))
  const problem = comma ? t('tr.commaAmb') : hasQty && pv?.problem && pv.problem.code !== 'QTY_REQUIRED' ? qtyText(pv.problem.code, pv.problem.params) : null

  // 条件单的白话说明："价格涨到 61,000 时，市价买入"
  const refPx = trigBy === 'last' ? Number(last) : Number(view?.positions.find((x) => x.instrument === inst)?.mark ?? last)
  const condUp = Number(trig) > refPx
  const confirmKind = type === 'cond' ? 'cond' : type === 'market' ? 'market' : 'limit'

  const lat = useLatency(!current.live)
  const rtt = rttOf(lat) // 模拟下单延迟（D94）：一次往返大约多少毫秒
  const live = !!current.live // 实盘账户（D89）：每一笔都弹确认；分批、追价、TWAP 这类工具还只在模拟盘上
  const realMoney = live && (current.env ?? 'main') === 'main'
  const algoOn: AlgoMode = !live && ((type === 'limit' && (algo === 'scale' || algo === 'chase' || algo === 'iceberg')) || (type === 'market' && (algo === 'twap' || algo === 'pov'))) ? algo : 'none'
  const submit = async (e?: FormEvent, confirmed = false) => {
    e?.preventDefault()
    // 只认这个表单自己的提交：里面对话框（存模板、改杠杆……）的表单提交会冒泡到这里，不能顺手下一单
    // （11-17 走查：存模板点"保存"，真的挂了一张买单）
    if (e && e.target !== e.currentTarget) return
    if (busy) return
    if (comma) { setErr(t('tr.commaAmb')); return }
    if (!hasQty) { setErr(errText('QTY_REQUIRED')); qtyRef.current?.focus(); return }
    if (algoOn !== 'none') {
      setBusy(true); setErr(null)
      try {
        await runAlgo(acct, algoOn, algoF, { instrument: inst, side, qty: pv?.qty ? String(pv.qty) : qty, reduce_only: reduce }, price)
        toast(t(`al.started.${algoOn}`))
        setQty(''); setPct(null)
      } catch (e2) { setErr(reject(e2)) } finally { setBusy(false) }
      return
    }
    if (type === 'cond' && !Number(trig)) { setErr(errText('TRIGGER_REQUIRED')); return }
    // 交易偏好里开了确认：先弹摘要
    if (live && !current.armed) { setErr(t('live.notArmed')); return }
    if (!confirmed && (live ? current.confirm !== false : prefs.confirm[confirmKind])) { setConfirming(true); return } // 实盘的确认在账户页关（要二级密码）
    setConfirming(false)
    setBusy(true); setErr(null)
    try {
      const o = await act<Order>('POST', `/api/trade/${acct}/orders`, req)
      toast(orderToast(o, t))
      if (pbSel !== '0') api('POST', `/api/journal/a/${acct}/order-playbook`, { order_id: o.id, playbook: Number(pbSel) }).catch(() => undefined)
      setQty(''); setPct(null)
    } catch (e2) {
      setErr(reject(e2))
    } finally {
      setBusy(false)
    }
  }

  const ratio = Number(view?.margin_ratio ?? 0)
  return (
    <>
    <form className="op" onSubmit={submit} noValidate>
      <div className="op-acct">
        {/* 交易所账户只放环境标记（名字在顶栏，侧栏窄时名字会被挤成"主…"） */}
        {live ? <span className={`op-live ${current.env ?? 'main'}`} data-live-badge title={`${current.name} · ${t('live.badgeHint')}`}>{acctEnv(current)}</span>
          : <span className="op-acct-name" title={t('tr.switchAccount')}>{acctName(current.name)}</span>}
        <span className="op-set">
          <button type="button" className="op-chip" onClick={() => setLevOpen(true)}>{t(set.mode === 'cross' ? 'tr.cross' : 'tr.isolated')}</button>
          <button type="button" className="op-chip num" onClick={() => setLevOpen(true)}>{set.leverage}×</button>
        </span>
        <button type="button" className="op-link" onClick={() => go('account')}>{t('tr.manage')}</button>
      </div>
      {follow && (
        <div className="op-follow-note">
          <span>{t('fw.manualNote')}</span>
          <button type="button" className="op-link" onClick={onBackToFollow}>{t('fw.toAi')}</button>
        </div>
      )}
      {live && view?.notices.some((n) => n.kind === 'live_stream_down') && (
        <div className="op-lock" role="status" data-live-down><span>{t('live.streamDown')}</span></div>
      )}
      {/* 交易所合约钱包是 0：说清楚去哪里放钱（11-15 走查：朋友的模拟账户钱包是 0，下单被拦但没说为什么） */}
      {live && view && Number(view.wallet) === 0 && Number(view.equity) === 0 && (
        <div className="op-lock" role="status" data-live-empty><span>{t(current.env === 'testnet' ? 'live.emptyDemo' : 'live.empty')}</span></div>
      )}
      {live && !current.armed && (
        <div className="op-lock op-live-arm" role="status" data-live-arm>
          <span>{t('live.notArmed')}</span>
          <button type="button" className="op-link" onClick={() => go('account')}>{t('live.armGo')}</button>
        </div>
      )}
      <HaltNote />
      {dlock && (
        <div className="op-lock" role="status">
          <span>{t('disc.banner', { rule: ruleName(dlock.rule) })}</span>
          <a className="op-link" {...linkProps('risk')}>{t('disc.open')}</a>
        </div>
      )}
      {/* 桌面上权益、已用保证金、保证金率在顶栏风险条（D38），这里只留下单时要看的；窄屏没有风险条，留权益、可用、保证金率 */}
      <div className="op-ro">
        <Ro h={t('hint.equity')} l={t('tr.equity')} v={<Money text={fmtAmt(view?.equity)} />} big cls="" wrap="op-n" />
        <Ro h={t('hint.available')} l={t('tr.available')} v={<Money text={fmtAmt(view?.available)} />} big />
        <div className="ro op-n" title={t('tr.ratioHint')}>
          <span className="ro-l">{t('tr.ratio')}</span>
          <span className={`ro-v num op-big-v ${ratio >= 0.8 ? 'down' : ratio >= 0.5 ? 'warn' : ''}`}>{locDec((ratio * 100).toFixed(2))}%</span>
          <span className="op-meter"><i style={{ width: `${Math.min(100, ratio * 100)}%` }} className={ratio >= 0.8 ? 'bad' : ratio >= 0.5 ? 'warn' : ''} /></span>
        </div>
        <Ro h={t('hint.upnl')} l={t('tr.upnl')} v={<Money text={fmtSigned(view?.upnl)} />} cls={dir(Number(view?.upnl))} wrap="op-w" />
        {(() => {
          // 已实现盈亏按币安持仓的算法：扣掉手续费、计入资金费和强平费，这样和权益的变化对得上
          const net = view ? Number(view.realized) - Number(view.fees) + Number(view.funding) - Number(view.liq_fees) : 0
          return <Ro l={t('tr.realized')} v={<Money text={fmtSigned(net)} />} cls={dir(net)} wrap="op-w" pop={view ? <RealizedPop v={view} /> : undefined} />
        })()}
        <Ro h={t('hint.used')} l={t('tr.used')} v={<Money text={fmtAmt(Number(view?.position_margin ?? 0) + Number(view?.order_margin ?? 0))} />} wrap="op-n" />
      </div>

      <div className={`op-side ${side}`} role="group" aria-label={t('col.side')}>
        <button type="button" aria-pressed={side === 'buy'} onClick={() => setSide('buy')}>{t('tr.buy')}</button>
        <button type="button" aria-pressed={side === 'sell'} onClick={() => setSide('sell')}>{t('tr.sell')}</button>
      </div>

      <div className="op-row">
        <Seg label={t('col.type')} value={type} onChange={setType} options={[{ value: 'limit', label: t('tr.limit') }, { value: 'market', label: t('tr.market') }, { value: 'cond', label: t('tr.cond') }]} />
        {(type === 'limit' || (type === 'cond' && exec === 'limit')) && algoOn === 'none' && (
          <Select className="op-tif" label={t('tr.tif')} value={type === 'cond' && tif === 'GTD' ? 'GTC' : tif} onChange={setTif}
            options={(type === 'limit' ? ['GTC', 'IOC', 'FOK', 'GTX', 'GTD'] as const : ['GTC', 'IOC', 'FOK', 'GTX'] as const).map((x) => ({ value: x, label: t(`tif.${x}`), hint: t(`tifHint.${x}`) }))} />
        )}
        {type === 'limit' && tif === 'GTD' && algoOn === 'none' && (
          <Select className="op-gtd" label={t('tr.gtdFor')} value={gtd} onChange={setGtd}
            options={(Object.keys(GTD_MS) as GtdFor[]).map((x) => ({ value: x, label: t(`tr.gtd.${x}` as 'tr.gtd.24h') }))} />
        )}
      </div>
      {type !== 'cond' && !live && (
        <div className="op-row op-algo-row">
          <Select className="op-algo-sel" label={t('al.mode')} value={algoOn} onChange={(v) => setAlgo(v as AlgoMode)}
            options={type === 'limit'
              ? [{ value: 'none', label: t('al.none') }, { value: 'scale', label: t('al.k.scale'), hint: t('al.scaleHint') }, { value: 'chase', label: t('al.k.chase'), hint: t('al.chaseHint') }, { value: 'iceberg', label: t('al.k.iceberg'), hint: t('al.icebergHint') }]
              : [{ value: 'none', label: t('al.none') }, { value: 'twap', label: t('al.k.twap'), hint: t('al.twapHint') }, { value: 'pov', label: t('al.k.pov'), hint: t('al.povHint') }]} />
        </div>
      )}
      {algoOn !== 'none' && <AlgoFields mode={algoOn} f={algoF} set={(p) => setAlgoF({ ...algoF, ...p })} />}

      {type === 'cond' && (
        <div className="op-cond">
          <label className="op-f">
            <span className="op-l">{t('tr.trigger')}
              <Select className="op-by" label={t('tr.trigBy')} value={trigBy} onChange={setTrigBy}
                options={(['mark', 'last', 'index'] as const).map((x) => ({ value: x, label: t(`tr.trigBy.${x}`), hint: t(`tr.trigByHint.${x}`) }))} />
            </span>
            <span className="op-in">
              <input inputMode="decimal" value={trig} onChange={(e) => setTrig(numIn(e.target.value))} onBlur={() => trig && setTrig(toTick(trig, spec.tick))} aria-label={t('tr.trigger')} data-fly-target="" />
              <em>USDT</em>
            </span>
          </label>
          <div className="op-row op-exec">
            <span className="op-l">{t('tr.exec')}</span>
            <Seg label={t('tr.exec')} value={exec} onChange={setExec} options={[{ value: 'market', label: t('tr.execMarket') }, { value: 'limit', label: t('tr.execLimit') }]} />
          </div>
          {Number(trig) > 0 && Number.isFinite(refPx) && refPx > 0 && (
            <p className="op-hint op-cond-say">{t('tr.condSay', {
              by: t(`tr.trigBy.${trigBy}`), dir: t(condUp ? 'tr.condUp' : 'tr.condDown'), px: fmtPrice(trig, pd),
              act: exec === 'market' ? t(side === 'buy' ? 'tr.condActMktBuy' : 'tr.condActMktSell') : t(side === 'buy' ? 'tr.condActLmtBuy' : 'tr.condActLmtSell', { px: price ? fmtPrice(price, pd) : '—' }),
            })}</p>
          )}
        </div>
      )}

      <label className={`op-f ${(type === 'cond' && exec === 'market') || algoOn === 'scale' || algoOn === 'chase' ? 'op-hide' : ''}`}>
        <span className="op-l">{t(type === 'cond' ? 'tr.condPrice' : 'tr.price')}
          {type === 'limit' && algoOn === 'none' && (
            <Select className="op-pm" label={t('tr.pm')} value={pm} onChange={setPm}
              options={PM_OPTS.map((x) => ({ value: x, label: t(x ? `tr.pm.${x}` as 'tr.pm.QUEUE' : 'tr.pm.none'), hint: x ? t(x.startsWith('OPP') ? 'tr.pmHint.opp' : 'tr.pmHint.queue') : undefined }))} />
          )}
        </span>
        {type === 'limit' && pm && algoOn === 'none' ? <span className="op-in op-in-dim" data-pm-say>{t('tr.pmSay', { pm: t(`tr.pm.${pm}` as 'tr.pm.QUEUE') })}</span>
        : type === 'limit' || (type === 'cond' && exec === 'limit') ? (
          <span className="op-in">
            <input inputMode="decimal" value={price} onChange={(e) => setPrice(numIn(e.target.value))} onBlur={() => price && setPrice(toTick(price, spec.tick))} aria-label={t('tr.price')} {...(type !== 'cond' ? { 'data-fly-target': '' } : {})} />
            <em>USDT</em>
          </span>
        ) : <span className="op-in op-in-dim">{t('tr.marketPrice')}</span>}
      </label>
      {(type === 'limit' || type === 'cond') && algoOn !== 'scale' && algoOn !== 'chase' && (
        <div className="op-quick">
          {[['tr.bid', bid], ['tr.last', last], ['tr.ask', ask]].map(([k, v]) => (
            <button type="button" key={k} onClick={() => { if (!v) return; if (type === 'cond') setTrig(toTick(v, spec.tick)); else { setPrice(toTick(v, spec.tick)); setPm('') } }} disabled={!v}>{t(k as 'tr.bid')}</button>
          ))}
        </div>
      )}

      <label className="op-f">
        <span className="op-l">{t('tr.qty')}{(() => {
          // 单向持仓：反方向下单会先平掉现有仓位，剩下的才开新仓。拆开写，不然"最多 4.213"和持仓 4.154 对不上
          const posQty = pos && (pos.side === 'long') !== (side === 'buy') ? Number(pos.qty) : 0
          const mask = (x: string) => (priv ? '••••' : x)
          if (reduce) return posQty > 0 ? <span className="op-max num">{t('tr.max', { q: `${mask(fmtAmt(posQty, qd))} ${base}` })}</span> : null
          if (!pv?.max_qty || Number(pv.max_qty) <= 0) return null
          const mx = Number(pv.max_qty), open = Math.max(0, mx - posQty)
          return <span className="op-max num" title={posQty > 0 ? t('tr.maxSplitHint') : undefined}>{posQty > 0
            ? t('tr.maxSplit', { q: mask(fmtAmt(mx, qd)), c: mask(fmtAmt(posQty, qd)), o: mask(fmtAmt(open, qd)), side: t(side === 'buy' ? 'tr.openLong' : 'tr.openShort'), base })
            : unit === 'usdt' && unitPx > 0 ? t('tr.max', { q: `${mask(fmtAmt(mx * unitPx, 2))} USDT` }) : t('tr.max', { q: `${mask(fmtAmt(mx, qd))} ${base}` })}</span>
        })()}</span>
        <span className="op-in">
          <input ref={qtyRef} inputMode="decimal" value={pct !== null ? '' : qty} placeholder={pct !== null ? t(reducePos ? 'tr.pctOfPos' : 'tr.pctOf', { p: pct }) : ''}
            onChange={(e) => { setPct(null); setQty(numIn(e.target.value)) }} aria-label={t('tr.qty')} />
          <span className="op-unit" role="group">
            <button type="button" aria-pressed={unit === 'coin'} onClick={() => { setUnit('coin'); setQty('') }}>{base}</button>
            <button type="button" aria-pressed={unit === 'usdt'} onClick={() => { setUnit('usdt'); setQty('') }}>USDT</button>
          </span>
        </span>
      </label>
      <div className="op-pct" role="group">
        {[25, 50, 75, 100].map((x) => (
          <button type="button" key={x} aria-pressed={pct === x} onClick={() => setPct(pct === x ? null : x)}>{x}%</button>
        ))}
      </div>

      <div className="op-checks">
        <label className="op-check"><input type="checkbox" checked={reduce} onChange={(e) => { setReduce(e.target.checked); if (e.target.checked) setTpsl(false) }} /><span>{t('tr.reduceOnly')}</span></label>
        <label className="op-check"><input type="checkbox" checked={tpsl} disabled={reduce} onChange={(e) => setTpsl(e.target.checked)} /><span>{t('tr.tpsl')}</span></label>
        {(type === 'cond' || (tpsl && !reduce)) && (
          <label className="op-check" title={t('tr.protectHint', { p: locDec(String(+(Number(spec.trigger_protect ?? 0.05) * 100).toFixed(2))) })} data-protect>
            <input type="checkbox" checked={protect} onChange={(e) => setProtect(e.target.checked)} /><span>{t('tr.protect')}</span>
          </label>
        )}
      </div>
      {tpsl && !reduce && (
        <div className="op-tpsl">
          <label className="op-f"><span className="op-l">{t('tr.tp')}</span><span className="op-in"><input inputMode="decimal" value={tp} onChange={(e) => setTp(numIn(e.target.value))} onBlur={() => tp && setTp(toTick(tp, spec.tick))} /></span></label>
          <label className="op-f"><span className="op-l">{t('tr.sl')}</span><span className="op-in"><input inputMode="decimal" value={sl} onChange={(e) => setSl(numIn(e.target.value))} onBlur={() => sl && setSl(toTick(sl, spec.tick))} /></span></label>
          <RiskCalc entry={Number(type === 'limit' ? price : side === 'buy' ? ask : bid) || 0} tp={Number(tp)} sl={Number(sl)} equity={Number(view?.equity ?? 0)} side={side} onQty={(q) => { setUnit('coin'); setPct(null); setQty((Math.floor(q / Number(spec.step)) * Number(spec.step)).toFixed(qd)) }} />
          <p className="op-hint op-tpsl-by">{t('tr.tpslHint')}
            {type !== 'cond' && (
              <Select className="op-by" label={t('tr.trigBy')} value={trigBy} onChange={setTrigBy}
                options={(['mark', 'last', 'index'] as const).map((x) => ({ value: x, label: t(`tr.trigByShort.${x}`), hint: t(`tr.trigByHint.${x}`) }))} />
            )}
          </p>
        </div>
      )}

      <dl className="op-pv">
        <div><dt>{t('tr.qty')}</dt><dd className="num">{hasQty && pv?.qty && Number(pv.qty) > 0 ? <Money text={`${fmtAmt(pv.qty, qd)} ${base}`} /> : '—'}</dd></div>
        <div><dt>{t('tr.estPx')}</dt><dd className="num">{hasQty && pv?.est_px && Number(pv.est_px) > 0 ? fmtPrice(pv.est_px, pd) : '—'}</dd></div>
        <div><dt>{t('tr.notional')}</dt><dd className="num">{hasQty && pv?.notional ? <Money text={fmtAmt(pv.notional)} unit="USDT" /> : '—'}{hasQty && pv?.notional && unit === 'usdt' && Number(qty) - Number(pv.notional) > Number(qty) * 0.002 ? <small title={t('tr.roundedHint', { step: String(spec.step), base })}> {t('tr.rounded')}</small> : null}</dd></div>
        {!live && rtt != null && <div data-op="latency"><dt title={t('lat.rowHint')}>{t('lat.row')}</dt><dd className="num">{t('lat.ms', { v: fmtMs(rtt) })}<small> {lat?.mode === 'measured' ? t('lat.measured') : t(`lat.r.${lat?.mode}` as 'lat.r.tokyo')}</small></dd></div>}
      </dl>

      {/* 贴在下单按钮上：不管表单多长、屏幕多矮，下单前都看得到要花多少、会不会爆仓、为什么下不了（D75） */}
      <TemplateBar current={() => ({ type, tif, unit, qty, pct, reduce, tpPct: '', slPct: '', algo, f: algoF })} onApply={(x: Template) => {
        setType(x.type); setTif(x.tif); setUnit(x.unit); setQty(x.qty); setPct(x.pct); setReduce(x.reduce); setAlgo(x.algo); setAlgoF(x.f)
      }} />
      <div className="op-go" ref={goRef}>
        <div className={`op-msg ${problem || err ? 'on' : ''}`} role="alert"><div><span>{err ?? problem}</span></div></div>
        <dl className="op-pv op-key">
          <div><dt>{t('tr.margin')}</dt><dd className="num">{hasQty && pv?.margin ? <Money text={fmtAmt(pv.margin)} /> : '—'}</dd></div>
          <div><dt>{t('tr.fee')}{hasQty && pv?.fee ? <small>{t(pv.maker ? 'tr.maker' : 'tr.taker')}</small> : null}</dt><dd className="num">{hasQty && pv?.fee ? <Money text={fmtAmt(pv.fee, 4)} /> : '—'}</dd></div>
          <div><dt>{t('tr.liq')}</dt><dd className="num">{hasQty && pv ? (Number(pv.liq_px) > 0 ? fmtPrice(pv.liq_px, pd) : t('tr.noLiq')) : '—'}</dd></div>
          {playbooks.length > 0 && !reduce && (
            <div className="op-row op-pb" data-op="playbook">
              <span className="op-l">{t('rv.d.playbook')}</span>
              <Select label={t('rv.d.playbook')} value={pbSel} onChange={setPbSel} options={[{ value: '0', label: t('rv.pbNone') }, ...playbooks.map((p) => ({ value: String(p.id), label: p.name }))]} />
            </div>
          )}
          {disc?.enabled && view && hasQty && pv && !reduce && <RiskRow view={view} rules={disc.rules} qty={pv.qty} est={pv.est_px} sl={tpsl ? sl : ''} />}
        </dl>
        <button className={`op-submit ${side}`} disabled={busy || (live && !current.armed)} data-live-submit={live ? '' : undefined}>
          {busy && <span className="spin" aria-hidden />}
          {/* 真钱的账户：按钮上写"实盘"（D73）；币安模拟交易是假钱，不写 */}
          {t(realMoney ? (type === 'cond' ? (side === 'buy' ? 'tr.submitLiveCondBuy' : 'tr.submitLiveCondSell') : side === 'buy' ? 'tr.submitLiveBuy' : 'tr.submitLiveSell')
            : type === 'cond' ? (side === 'buy' ? 'tr.submitCondBuy' : 'tr.submitCondSell') : side === 'buy' ? 'tr.submitBuy' : 'tr.submitSell', { base })}
          {prefs.keys && <kbd>Enter</kbd>}
        </button>
      </div>

    </form>
    <LeverageDialog open={levOpen} onClose={() => setLevOpen(false)} acct={acct} inst={inst} spec={spec} cur={set} />
    <Dialog open={confirming} title={t('cf.title')} onClose={() => setConfirming(false)}>
      <form className="au-form" onSubmit={(e) => { e.preventDefault(); e.stopPropagation(); void submit(undefined, true) }}>
        {live && <p className="cf-live" data-live-confirm>{t(current.env === 'main' || !current.env ? 'live.confirmMain' : 'live.confirmTest', { name: current.name })}</p>}
        <dl className="cf-sum">
          <div><dt>{t('col.side')}</dt><dd className={side === 'buy' ? 'up' : 'down'}>{t(side === 'buy' ? 'tr.buy' : 'tr.sell')} · {base}</dd></div>
          <div><dt>{t('col.type')}</dt><dd>{type === 'cond' ? `${t('tr.cond')} · ${t(exec === 'market' ? 'tr.execMarket' : 'tr.execLimit')}` : t(type === 'market' ? 'tr.market' : 'tr.limit')}{reduce ? ` · ${t('tr.reduceOnly')}` : ''}</dd></div>
          {type === 'cond' && <div><dt>{t('tr.trigger')}</dt><dd className="num">{fmtPrice(trig, pd)} <small>{t(`tr.trigBy.${trigBy}`)}</small></dd></div>}
          {(type === 'limit' || (type === 'cond' && exec === 'limit')) && <div><dt>{t('tr.price')}</dt><dd className="num">{fmtPrice(price, pd)}</dd></div>}
          <div><dt>{t('tr.qty')}</dt><dd className="num"><Money text={pv?.qty ? `${fmtAmt(pv.qty, qd)} ${base}` : '—'} /></dd></div>
          <div><dt>{t('tr.notional')}</dt><dd className="num"><Money text={fmtAmt(pv?.notional)} unit="USDT" /></dd></div>
          <div><dt>{t('tr.margin')}</dt><dd className="num"><Money text={fmtAmt(pv?.margin)} unit="USDT" /> <small>{set.leverage}× {t(set.mode === 'cross' ? 'tr.cross' : 'tr.isolated')}</small></dd></div>
          <div><dt>{t('tr.fee')}</dt><dd className="num"><Money text={fmtAmt(pv?.fee, 4)} unit="USDT" /></dd></div>
          <div><dt>{t('tr.liq')}</dt><dd className="num">{pv && Number(pv.liq_px) > 0 ? fmtPrice(pv.liq_px, pd) : t('tr.noLiq')}</dd></div>
          {tpsl && !reduce && (tp || sl) && <div><dt>{t('tr.tpsl')}</dt><dd className="num">{tp ? fmtPrice(tp, pd) : '—'} / {sl ? fmtPrice(sl, pd) : '—'}</dd></div>}
        </dl>
        {!live && <label className="op-check cf-skip"><input type="checkbox" onChange={(e) => setPrefs((p) => ({ ...p, confirm: { ...p.confirm, [confirmKind]: !e.target.checked } }))} /><span>{t('cf.skip')}</span></label>}
        <div className="dlg-btns">
          <button type="button" className="btn ghost" onClick={() => setConfirming(false)}>{t('common.cancel')}</button>
          <button className={`btn primary cf-go ${side}`} disabled={busy} data-cf-go>{t(live ? 'live.confirmOk' : 'cf.ok')}</button>
        </div>
      </form>
    </Dialog>
    </>
  )
}

function Ro({ l, v, cls, big, pop, wrap, h }: { l: string; v: React.ReactNode; cls?: string; big?: boolean; pop?: React.ReactNode; wrap?: string; h?: string }) {
  return (
    <div className={`ro ${big ? 'op-big' : ''} ${pop ? 'ro-has-pop' : ''} ${wrap ?? ''}`} tabIndex={pop ? 0 : undefined} title={h}>
      <span className="ro-l">{l}</span>
      <span className={`ro-v num ${cls ?? ''}`}>{v}</span>
      {pop && <div className="ro-pop" role="tooltip">{pop}</div>}
    </div>
  )
}

/** 已实现盈亏的明细：平仓盈亏 − 手续费 + 资金费 − 强平费，再加未实现盈亏 = 权益相对初始资金的变化。几项加起来一定对得上。 */
function RealizedPop({ v }: { v: AccountView }) {
  const t = useT()
  const n = (x: string | number) => Number(x)
  const net = n(v.realized) - n(v.fees) + n(v.funding) - n(v.liq_fees)
  const row = (k: string, x: number, show = true) => show && (
    <div className="rp-row"><span>{k}</span><span className={`num ${dir(x)}`}><Money text={fmtSigned(x)} /></span></div>
  )
  return (
    <>
      {row(t('rz.closed'), n(v.realized))}
      {row(t('rz.fees'), -n(v.fees))}
      {row(t('rz.funding'), n(v.funding))}
      {/* liq_fees 里已经减掉了系统补的穿仓，拆开显示：强平费是你付的，穿仓那部分是系统补的 */}
      {row(t('rz.liq'), -(n(v.liq_fees) + n(v.covered ?? 0)), n(v.liq_fees) + n(v.covered ?? 0) !== 0)}
      {row(t('rz.covered'), n(v.covered ?? 0), n(v.covered ?? 0) > 0)}
      <div className="rp-row rp-sum"><span>{t('tr.realized')}</span><span className={`num ${dir(net)}`}><Money text={fmtSigned(net)} /></span></div>
      {row(t('tr.upnl'), n(v.upnl))}
      <div className="rp-row rp-sum"><span>{t('rz.total', { init: fmtAmt(v.initial, 0) })}</span><span className={`num ${dir(n(v.equity) - n(v.initial))}`}><Money text={fmtSigned(n(v.equity) - n(v.initial))} /></span></div>
      <p className="rp-note">{t('rz.note')}</p>
      {n(v.covered ?? 0) > 0 && <p className="rp-note">{t('rz.coveredNote')}</p>}
    </>
  )
}

export function orderToast(o0: Order, t: ReturnType<typeof useT>, amended = false): string {
  // 隐藏金额时提示里也不露数量（2.15）
  const o = isPrivate() ? { ...o0, qty: '••••', filled: '••••' } : o0
  const base = baseOf(o.instrument)
  if ((o.type === 'cond_market' || o.type === 'cond_limit') && o.status === 'new') {
    return t(amended ? 'tr.done.condAmended' : 'tr.done.cond', { by: t(`tr.trigBy.${o.trigger_by ?? 'mark'}`), dir: t(o.trigger_dir === 'up' ? 'tr.condUp' : 'tr.condDown'),
      px: fmtPrice(o.trigger_px), side: t(o.side === 'buy' ? 'side.buy' : 'side.sell'), qty: o.qty, base })
  }
  // 交易所还没给均价（币安市价单刚成交时偶尔是 0）就不写均价，别写"均价 0.00000"
  if (o.status === 'filled') return Number(o0.avg_px) > 0 ? t('tr.done.filled', { qty: o.filled, base, px: fmtPrice(o.avg_px) }) : t('tr.done.filledNoPx', { qty: o.filled, base })
  if (o.status === 'partially_filled') return t('tr.done.partial', { filled: o.filled, qty: o.qty, base })
  if (o.status === 'new') return t(amended ? 'tr.done.amended' : 'tr.done.new', { side: t(o.side === 'buy' ? 'side.buy' : 'side.sell'), qty: o.qty, base, px: fmtPrice(o.price) })
  // 一点没成交时不说"没有完全成交"（11-17 走查：只挂单越价被撤，提示写"委托没有完全成交：只挂单：会立即成交，已撤销"）
  return Number(o0.filled) > 0 ? t('tr.done.expired', { reason: reasonText(o.reason), filled: o.filled, base }) : t('tr.done.none', { reason: reasonText(o.reason) })
}

// ---------- 杠杆和保证金模式 ----------

function LeverageDialog({ open, onClose, acct, inst, spec, cur }: { open: boolean; onClose: () => void; acct: number; inst: string; spec: Spec; cur: { leverage: number; mode: 'cross' | 'isolated' } }) {
  const t = useT()
  const [lev, setLev] = useState(cur.leverage)
  const [mode, setMode] = useState(cur.mode)
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  useEffect(() => { if (open) { setLev(cur.leverage); setMode(cur.mode); setErr(null) } }, [open]) // eslint-disable-line react-hooks/exhaustive-deps
  const maxLev = spec.brackets[0]?.max_leverage ?? 20
  const cap = spec.brackets.find((b) => b.max_leverage < lev)
  // 满仓时价格反向多少就强平（只看这笔保证金；全仓时账户里其他余额会多撑一些）：约 1/杠杆 − 维持保证金率
  const move = Math.max(0, (1 / lev - Number(spec.brackets[0]?.mmr ?? 0.004)) * 100)
  const marks = [...new Set([1, 5, 10, 20, 50, 100, maxLev])].filter((x) => x <= maxLev).sort((a, b) => a - b)
  const save = async (e: FormEvent) => {
    e.preventDefault()
    e.stopPropagation()
    setBusy(true); setErr(null)
    try {
      if (mode !== cur.mode) await act('POST', `/api/trade/${acct}/margin-mode`, { instrument: inst, mode })
      if (lev !== cur.leverage) await act('POST', `/api/trade/${acct}/leverage`, { instrument: inst, leverage: lev })
      // 写清楚现在是几倍、什么模式（11-17 走查：原来只写"已保存"）
      toast(t('lev.done', { base: baseOf(inst), lev, mode: t(mode === 'isolated' ? 'tr.isolated' : 'tr.cross') }))
      onClose()
    } catch (e2) {
      setErr(rejectText(e2))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open={open} title={`${t('tr.levTitle')} · ${baseOf(inst)}`} onClose={onClose}>
      <form className="au-form" onSubmit={save} noValidate>
        <div className="op-dl-row">
          <span className="op-l">{t('tr.mode')}</span>
          <Seg label={t('tr.mode')} value={mode} onChange={setMode} options={[{ value: 'cross', label: t('tr.cross') }, { value: 'isolated', label: t('tr.isolated') }]} />
        </div>
        <p className="dlg-text op-small">{t('tr.modeHint')}</p>
        <div className="op-dl-row">
          <span className="op-l">{t('tr.lev')}</span>
          <span className="op-in op-lev-in"><input inputMode="numeric" value={lev} onChange={(e) => setLev(Math.max(1, Math.min(maxLev, Number(e.target.value.replace(/\D/g, '')) || 1)))} aria-label={t('tr.lev')} /><em>×</em></span>
        </div>
        <input className="op-range" type="range" min={1} max={maxLev} value={lev} onChange={(e) => setLev(Number(e.target.value))} aria-label={t('tr.lev')} />
        <div className="op-marks">{marks.map((m) => <button type="button" key={m} aria-pressed={lev === m} onClick={() => setLev(m)}>{m}×</button>)}</div>
        <p className={`dlg-text op-small ${lev >= 20 ? 'op-warn' : ''}`}>{t('tr.levMove', { lev, pct: move < 1 ? locDec(move.toFixed(2)) : locDec(move.toFixed(1)) })}</p>
        <p className="dlg-text op-small">{cap ? t('tr.levHint', { lev, max: fmtAmt(cap.floor, 0) }) : ''}</p>
        {spec.bracket_source === 'default' && <p className="dlg-text op-small" data-bracket-est>{t('tr.bracketEst')}</p>}
        <div className={`op-msg ${err ? 'on' : ''}`} role="alert"><div><span>{err}</span></div></div>
        <div className="dlg-btns">
          <button type="button" className="btn ghost" onClick={onClose}>{t('common.cancel')}</button>
          <button className="btn primary" disabled={busy}>{t('common.save')}</button>
        </div>
      </form>
    </Dialog>
  )
}

/** 这笔最多亏多少（4.14）：带止损的和每笔风险上限比，没带的写明没有止损 */
function RiskRow({ view, rules, qty, est, sl }: { view: { equity: string }; rules: Rules; qty: string; est: string; sl: string }) {
  const t = useT()
  const stop = Number(sl), px = Number(est), q = Number(qty)
  if (!(stop > 0)) return <div><dt>{t('disc.riskRow')}</dt><dd className="op-risk-none">{t('disc.noStop')}</dd></div>
  const risk = Math.abs(px - stop) * q
  const limit = Number(view.equity) * Number(rules.per_trade)
  return (
    <div><dt>{t('disc.riskRow')}</dt><dd className={`num ${risk > limit ? 'op-risk-over' : ''}`}><Money text={fmtAmt(risk)} /> <small>/ <Money text={fmtAmt(limit)} /></small></dd></div>
  )
}
