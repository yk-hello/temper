// 交易列表（模块 2，验收 2.8–2.10、2.13）：持仓、当前委托、历史委托、成交明细、资金费记录。
import { useEnterExit } from '../../lib/rowmotion'
import { isLiveId } from '../../lib/live'
import { Fragment, useCallback, useEffect, useState, type FormEvent } from 'react'
import { useT, reasonText, fmtTime } from '../../lib/i18n'
import { api } from '../../lib/session'
import { act, decimalsOf, refreshTrade, rejectText, toTick, type AccountView, type Fill, type FundingRec, type Order, type PosView, type Spec } from '../../lib/trade'
import { baseOf, fmtPct, fmtPrice, numIn } from '../../lib/format'
import { toast, toastAction, usePrivate } from '../../lib/privacy'
import { Money } from '../../ui/Money'
import { Seg } from '../../ui/Seg'
import { Dialog } from '../../ui/Dialog'
import { fmtAmt, fmtSigned, orderToast } from './OrderPanel'
import { AlgoList, ExitDialog, useAlgos } from './OrderTools'
import { locDec } from '../../lib/tz'

const dir = (v: number) => (v > 0 ? 'up' : v < 0 ? 'down' : '')
const time = (ms: number) => fmtTime(ms, { seconds: true })

// ---------- 持仓 ----------

/** 自动减仓排队（18 第 6.1 条）：4 格，亮得越多越先被交易所强制减仓（行情极端、穿仓的人多时才会发生） */
function Adl({ n, est }: { n: number; est: boolean }) {
  const t = useT()
  const tip = t(est ? 'adl.tipEst' : 'adl.tip', { n })
  return (
    <span className={`adl ${n >= 4 ? 'hot' : ''}`} data-tip={tip} aria-label={tip} data-adl={n}>
      {[1, 2, 3, 4].map((i) => <i key={i} className={i <= n ? 'on' : ''} />)}
    </span>
  )
}

export function Positions({ view, specs, onPick }: { view: AccountView; specs: Record<string, Spec> | null; onPick: (inst: string) => void }) {
  const t = useT()
  const [close, setClose] = useState<PosView | null>(null)
  const [tpsl, setTpsl] = useState<PosView | null>(null)
  const [mg, setMg] = useState<PosView | null>(null)
  const [exit, setExit] = useState<PosView | null>(null)
  const [all, setAll] = useState(false)
  const rows = useEnterExit(view.positions, (p) => p.instrument)
  if (!rows.length) return <div className="mk-empty">{t('ls.empty.pos')}</div>
  return (
    <>
      <table className="tbl tl">
        <thead><tr>
          <th>{t('col.inst')}</th><th>{t('col.qty')}</th><th className="mk-opt2">{t('col.value')}</th><th>{t('col.entry')}</th><th title={t('hint.mark')}>{t('col.mark')}</th><th title={t('hint.liq')}>{t('col.liq')}</th>
          <th className="mk-opt2">{t('col.margin')}</th><th className="mk-opt2" title={t('hint.marginR')}>{t('col.marginR')}</th><th>{t('col.upnl')}</th><th className="mk-opt2">{t('col.tpsl')}</th>
          <th className="tl-act"><button className="btn ghost sm" onClick={() => setAll(true)}>{t('ls.closeAll')}</button></th>
        </tr></thead>
        <tbody>{rows.map(({ item: p, key, state }) => {
          const pd = specs?.[p.instrument] ? decimalsOf(specs[p.instrument].tick) : undefined
          const u = Number(p.upnl)
          return (
            <tr key={key} className={state ? `tl-${state} ${p.side === 'long' ? 'up' : 'down'}` : ''} aria-hidden={state === "exit" ? true : undefined}>
              <td>
                <button className="tl-sym" onClick={() => onPick(p.instrument)}><span className="sym">{baseOf(p.instrument)}</span></button>
                <span className={`tl-tag ${p.side === 'long' ? 'up' : 'down'}`}>{t(p.side === 'long' ? 'side.long' : 'side.short')}</span>
                <span className="tl-tag">{t(p.mode === 'cross' ? 'tr.cross' : 'tr.isolated')} {p.leverage}×</span>
              </td>
              <td><Money text={locDec(p.qty)} /></td>
              <td className="mk-opt2"><Money text={fmtAmt(p.notional)} unit="USDT" /></td>
              <td>{fmtPrice(p.entry, pd)}</td>
              <td>{fmtPrice(p.mark, pd)}</td>
              <td className={Number(p.liq_px) > 0 ? 'warn-t' : 't3'}>{Number(p.liq_px) > 0 ? fmtPrice(p.liq_px, pd) : t('tr.noLiq')}<Adl n={p.adl ?? 0} est={!!p.adl_est} /></td>
              <td className="mk-opt2">
                <Money text={fmtAmt(p.margin)} />
                {p.mode === 'isolated' && (
                  <button type="button" className="tl-edit" onClick={() => setMg(p)} title={t('mg.title')} aria-label={`${t('mg.title')} · ${baseOf(p.instrument)}`}>
                    <svg width="11" height="11" viewBox="0 0 11 11" aria-hidden="true"><path d="M1.5 9.5h2l5.2-5.2-2-2L1.5 7.5v2zM6 3l2 2" fill="none" stroke="currentColor" strokeWidth="1.1" /></svg>
                  </button>
                )}
              </td>
              {(() => {
                // 逐仓：维持保证金 ÷ (逐仓保证金 + 浮盈)；全仓：账户的保证金率（2.32）
                const bal = Number(p.margin) + u
                const r = p.mode === 'isolated' ? (bal > 0 ? Number(p.maint) / bal : 1) : Number(view.margin_ratio)
                return <td className={`mk-opt2 num ${r >= 0.8 ? 'down' : r >= 0.5 ? 'warn-t' : 't2'}`}>{locDec((r * 100).toFixed(2))}%</td>
              })()}
              <td className={dir(u)}><Money text={fmtSigned(u)} /><small className="tl-roe">{fmtSigned(Number(p.roe) * 100)}%</small></td>
              <td className="mk-opt2 t2">{Number(p.tp) > 0 ? fmtPrice(p.tp, pd) : '—'} / {Number(p.sl) > 0 ? fmtPrice(p.sl, pd) : '—'}</td>
              <td className="tl-act">
                {!isLiveId(view.id) && <button className="btn ghost sm" data-exit-open onClick={() => setExit(p)}>{t('al.more')}</button>}
                <button className="btn ghost sm" onClick={() => setTpsl(p)}>{t('ls.setTpsl')}</button>
                <button className="btn secondary sm" onClick={() => setClose(p)}>{t('ls.close')}</button>
              </td>
            </tr>
          )
        })}</tbody>
      </table>
      {close && <CloseDialog pos={close} spec={specs?.[close.instrument]} acct={view.id} onClose={() => setClose(null)} />}
      {tpsl && <TpslDialog pos={tpsl} spec={specs?.[tpsl.instrument]} acct={view.id} orders={view.orders} onClose={() => setTpsl(null)} />}
      {mg && <MarginDialog pos={mg} spec={specs?.[mg.instrument]} acct={view.id} onClose={() => setMg(null)} />}
      {exit && <ExitDialog pos={exit} acct={view.id} spec={specs?.[exit.instrument]} onClose={() => setExit(null)} />}
      <Confirm open={all} title={t('ls.closeAll')} body={t('dlg.closeAll.body', { n: view.positions.length })} danger btn={t('ls.closeAll')} onClose={() => setAll(false)}
        run={async () => { await act('POST', `/api/trade/${view.id}/close-all`, {}); toast(t('tr.closed')) }} />
    </>
  )
}

function CloseDialog({ pos, spec, acct, onClose }: { pos: PosView; spec?: Spec; acct: number; onClose: () => void }) {
  const t = useT()
  const priv = usePrivate()
  const [frac, setFrac] = useState('1')
  const [type, setType] = useState<'market' | 'limit'>('market')
  const [price, setPrice] = useState(spec ? toTick(pos.mark, spec.tick) : pos.mark)
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const step = spec ? Number(spec.step) : 0.001
  const q = frac === '1' ? Number(pos.qty) : Math.floor((Number(pos.qty) * Number(frac)) / step) * step
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr(null)
    try {
      const o = await act<Order>('POST', `/api/trade/${acct}/close`, { instrument: pos.instrument, fraction: frac, type, price: type === 'limit' ? price : undefined })
      toast(orderToast(o, t))
      onClose()
    } catch (e2) { setErr(rejectText(e2)) } finally { setBusy(false) }
  }
  return (
    <Dialog open title={t('dlg.close.title', { inst: baseOf(pos.instrument) })} onClose={onClose}>
      <form className="au-form" onSubmit={submit} noValidate>
        <div className="op-dl-row"><span className="op-l">{t('dlg.close.fraction')}</span>
          <Seg label={t('dlg.close.fraction')} value={frac} onChange={setFrac} options={[{ value: '0.25', label: '25%' }, { value: '0.5', label: '50%' }, { value: '0.75', label: '75%' }, { value: '1', label: '100%' }]} />
        </div>
        <div className="op-dl-row"><span className="op-l">{t('dlg.close.type')}</span>
          <Seg label={t('dlg.close.type')} value={type} onChange={setType} options={[{ value: 'market', label: t('tr.market') }, { value: 'limit', label: t('tr.limit') }]} />
        </div>
        {type === 'limit' && (
          <div className="op-dl-row"><span className="op-l">{t('tr.price')}</span>
            <span className="op-in"><input inputMode="decimal" value={price} onChange={(e) => setPrice(numIn(e.target.value))} aria-label={t('tr.price')} /><em>USDT</em></span>
          </div>
        )}
        <p className="dlg-text num">{t('dlg.close.qty', { q: priv ? '••••' : locDec(q.toFixed(spec ? decimalsOf(spec.step) : 3)), base: baseOf(pos.instrument) })}</p>
        <div className={`op-msg ${err ? 'on' : ''}`} role="alert"><div><span>{err}</span></div></div>
        <div className="dlg-btns">
          <button type="button" className="btn ghost" onClick={onClose}>{t('common.cancel')}</button>
          <button className="btn primary" disabled={busy}>{t('dlg.close.btn')}</button>
        </div>
      </form>
    </Dialog>
  )
}

function TpslDialog({ pos, spec, acct, orders, onClose }: { pos: PosView; spec?: Spec; acct: number; orders: Order[]; onClose: () => void }) {
  const t = useT()
  const [tp, setTp] = useState(Number(pos.tp) > 0 ? pos.tp : '')
  const [sl, setSl] = useState(Number(pos.sl) > 0 ? pos.sl : '')
  const [by, setBy] = useState<'mark' | 'last' | 'index'>(orders.find((o) => o.close_position && o.instrument === pos.instrument)?.trigger_by ?? 'mark')
  const [err, setErr] = useState<string | null>(null)
  const priv = usePrivate()
  // 填了价格就说离现在多远、触发时大概赚亏多少（11-16 走查：原来只有两个空框，要自己心算）
  const est = (v: string) => {
    const px = Number(v), mark = Number(pos.mark), entry = Number(pos.entry), q = Number(pos.qty)
    if (!(px > 0) || !(mark > 0)) return null
    const pl = (px - entry) * q * (pos.side === 'long' ? 1 : -1)
    return t(pl >= 0 ? 'dlg.tpsl.estWin' : 'dlg.tpsl.estLoss', { pct: fmtPct(String((px / mark - 1) * 100)), amt: priv ? '••••' : fmtAmt(Math.abs(pl)) })
  }
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    try {
      const n = (v: string) => (v ? (spec ? toTick(v, spec.tick) : v) : '')
      await act('POST', `/api/trade/${acct}/tpsl`, { instrument: pos.instrument, tp: n(tp), sl: n(sl), trigger_by: by })
      // 写清楚设成了什么（11-17 走查：原来只写"已保存"）
      toast(!tp && !sl ? t('dlg.tpsl.cleared', { base: baseOf(pos.instrument) }) : t('dlg.tpsl.done', { base: baseOf(pos.instrument), tp: tp ? fmtPrice(n(tp)) : '—', sl: sl ? fmtPrice(n(sl)) : '—' }))
      onClose()
    } catch (e2) { setErr(rejectText(e2)) }
  }
  return (
    <Dialog open title={t('dlg.tpsl.title', { inst: baseOf(pos.instrument) })} onClose={onClose}>
      <form className="au-form" onSubmit={submit} noValidate>
        <p className="dlg-text">{t('dlg.tpsl.hint', { mark: fmtPrice(pos.mark) })}</p>
        <div className="op-dl-row"><span className="op-l">{t('tr.tp')}</span><span className="op-in"><input inputMode="decimal" value={tp} onChange={(e) => setTp(numIn(e.target.value))} aria-label={t('tr.tp')} /></span></div>
        {est(tp) && <p className="dlg-text op-small op-dl-hint" data-tpsl-est="tp">{est(tp)}</p>}
        <div className="op-dl-row"><span className="op-l">{t('tr.sl')}</span><span className="op-in"><input inputMode="decimal" value={sl} onChange={(e) => setSl(numIn(e.target.value))} aria-label={t('tr.sl')} /></span></div>
        {est(sl) && <p className="dlg-text op-small op-dl-hint" data-tpsl-est="sl">{est(sl)}</p>}
        <div className="op-dl-row"><span className="op-l">{t('tr.trigBy')}</span>
          <Seg label={t('tr.trigBy')} value={by} onChange={setBy} options={[{ value: 'mark', label: t('tr.trigBy.mark') }, { value: 'last', label: t('tr.trigBy.last') }, { value: 'index', label: t('tr.trigBy.index') }]} />
        </div>
        <p className="dlg-text op-small">{t(`tr.trigByHint.${by}`)}</p>
        <div className={`op-msg ${err ? 'on' : ''}`} role="alert"><div><span>{err}</span></div></div>
        <div className="dlg-btns">
          <button type="button" className="btn ghost" onClick={onClose}>{t('common.cancel')}</button>
          <button className="btn primary">{t('common.save')}</button>
        </div>
      </form>
    </Dialog>
  )
}

/** 逐仓调整保证金：追加或减少，实时预览调整后的强平价（后端 dry 计算，和真正执行同一套代码） */
function MarginDialog({ pos, spec, acct, onClose }: { pos: PosView; spec?: Spec; acct: number; onClose: () => void }) {
  const t = useT()
  const priv = usePrivate()
  const [dirn, setDirn] = useState<'add' | 'remove'>('add')
  const [amt, setAmt] = useState('')
  const [pv, setPv] = useState<{ liq_px: string; margin: string; max_add: string; max_remove: string } | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const pd = spec ? decimalsOf(spec.tick) : undefined
  const signed = amt ? (dirn === 'add' ? amt : `-${amt}`) : '0'
  useEffect(() => {
    let live = true
    const id = window.setTimeout(async () => {
      try {
        const r = await api<typeof pv>('POST', `/api/trade/${acct}/margin`, { instrument: pos.instrument, amount: signed, dry: true })
        if (live) { setPv(r); setErr(null) }
      } catch (e) { if (live) setErr(rejectText(e)) }
    }, 150)
    return () => { live = false; window.clearTimeout(id) }
  }, [signed, acct, pos.instrument])
  const max = pv ? (dirn === 'add' ? pv.max_add : pv.max_remove) : undefined
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!Number(amt)) return
    setBusy(true); setErr(null)
    try {
      await act('POST', `/api/trade/${acct}/margin`, { instrument: pos.instrument, amount: signed })
      // 写清楚加了 / 减了多少、强平价变成多少（11-17 走查：原来只写"已保存"）
      const liq = pv && Number(pv.liq_px) > 0 ? fmtPrice(pv.liq_px, pd) : t('tr.noLiq')
      toast(t(dirn === 'add' ? 'mg.doneAdd' : 'mg.doneRemove', { v: priv ? '••••' : fmtAmt(Math.abs(Number(signed))), liq }))
      onClose()
    } catch (e2) { setErr(rejectText(e2)) } finally { setBusy(false) }
  }
  return (
    <Dialog open title={`${t('mg.title')} · ${baseOf(pos.instrument)}`} onClose={onClose}>
      <form className="au-form" onSubmit={submit} noValidate>
        <div className="op-dl-row"><span className="op-l">{t('mg.dir')}</span>
          <Seg label={t('mg.dir')} value={dirn} onChange={(v) => { setDirn(v); setAmt('') }} options={[{ value: 'add', label: t('mg.add') }, { value: 'remove', label: t('mg.remove') }]} />
        </div>
        <div className="op-dl-row"><span className="op-l">{t('mg.amount')}{max !== undefined && <button type="button" className="op-link mg-max num" onClick={() => setAmt(String(Number(max)))}>{t(dirn === 'add' ? 'mg.maxAdd' : 'mg.maxRemove', { v: priv ? '••••' : fmtAmt(max) })}</button>}</span>
          <span className="op-in"><input inputMode="decimal" value={amt} onChange={(e) => setAmt(numIn(e.target.value))} aria-label={t('mg.amount')} /><em>USDT</em></span>
        </div>
        <dl className="cf-sum mg-sum">
          <div><dt>{t('mg.margin')}</dt><dd className="num"><Money text={fmtAmt(pos.margin)} />{amt && pv ? <> → <Money text={fmtAmt(pv.margin)} /></> : null}</dd></div>
          <div><dt>{t('col.liq')}</dt><dd className="num warn-t">{Number(pos.liq_px) > 0 ? fmtPrice(pos.liq_px, pd) : t('tr.noLiq')}{amt && pv ? <> → {Number(pv.liq_px) > 0 ? fmtPrice(pv.liq_px, pd) : '—'}</> : null}</dd></div>
        </dl>
        <p className="dlg-text op-small">{t(dirn === 'add' ? 'mg.hintAdd' : 'mg.hintRemove')}</p>
        <div className={`op-msg ${err ? 'on' : ''}`} role="alert"><div><span>{err}</span></div></div>
        <div className="dlg-btns">
          <button type="button" className="btn ghost" onClick={onClose}>{t('common.cancel')}</button>
          <button className="btn primary" disabled={busy || !Number(amt) || !!err}>{t('common.confirm')}</button>
        </div>
      </form>
    </Dialog>
  )
}

// ---------- 当前委托 ----------

export function OpenOrders({ view, specs }: { view: AccountView; specs: Record<string, Spec> | null }) {
  const algos = useAlgos(view.id)
  const t = useT()
  const [amend, setAmend] = useState<Order | null>(null)
  const [all, setAll] = useState(false)
  const [open, setOpen] = useState<string | null>(null)
  const rows = useEnterExit(view.orders, (o) => o.id)
  if (!rows.length) return algos.some((x) => x.status === 'running') ? <AlgoList acct={view.id} algos={algos} /> : <div className="mk-empty">{t('ls.empty.open')}</div>
  const cancel = async (o: Order) => {
    try {
      await act('DELETE', `/api/trade/${view.id}/orders/${o.id}`)
      // 撤错了 5 秒内能撤回（D38）；持仓上的止盈止损不算委托，在持仓那里重新设
      if (!isLiveId(view.id) && (o.type === 'limit' || o.type === 'cond_market' || o.type === 'cond_limit')) { // 实盘：交易所撤掉的单撤不回来
        toastAction(t('tr.canceled', { n: 1 }), t('undo.btn'), () => {
          act('POST', `/api/trade/${view.id}/orders/${o.id}/undo`, {}).then(() => toast(t('undo.done'))).catch((e) => toast(rejectText(e)))
        })
      } else toast(t('tr.canceled', { n: 1 }))
    } catch (e) { toast(rejectText(e)) }
  }
  return (
    <>
      <AlgoList acct={view.id} algos={algos} />
      <table className="tbl tl">
        <thead><tr>
          <th>{t('col.time')}</th><th>{t('col.inst')}</th><th>{t('col.type')}</th><th>{t('col.price')}</th><th>{t('col.qty')}</th><th>{t('col.filled')}</th>
          <th className="tl-act"><button className="btn ghost sm" onClick={() => setAll(true)}>{t('ls.cancelAll')}</button></th>
        </tr></thead>
        <tbody>{rows.map(({ item: o, state }) => {
          const pd = specs?.[o.instrument] ? decimalsOf(specs[o.instrument].tick) : undefined
          const cond = o.type === 'stop_market' || o.type === 'take_profit_market'
          const entry = o.type === 'cond_market' || o.type === 'cond_limit'
          const has = Number(o.filled) > 0
          const byTxt = t(`tr.trigBy.${o.trigger_by ?? 'mark'}`)
          return (
            <Fragment key={o.id}>
            <tr className={`${open === o.id ? 'tl-open' : ''} ${state ? `tl-${state} ${o.side === 'buy' ? 'up' : 'down'}` : ''}`} data-id={o.id} aria-hidden={state === "exit" ? true : undefined}>
              <td className="t2"><ExpandTime has={has} on={open === o.id} toggle={() => setOpen(open === o.id ? null : o.id)} t={o.created} /></td>
              <td><span className="sym">{baseOf(o.instrument)}</span><span className={`tl-tag ${o.side === 'buy' ? 'up' : 'down'}`}>{t(o.side === 'buy' ? 'side.buy' : 'side.sell')}</span></td>
              <td>{t(`otype.${o.type}` as 'otype.limit')}{o.tif && o.tif !== 'GTC' && <span className="tl-sub">{t(`tif.${o.tif}` as 'tif.GTC')}{o.tif === 'GTD' && o.good_till ? ` ${fmtTime(o.good_till)}` : ''}</span>}{o.price_protect && <span className="tl-sub">{t('tr.protect')}</span>}{o.reduce_only && !cond && <span className="tl-sub">{t('ls.reduce')}</span>}</td>
              <td>
                {cond || entry
                  ? <><span className="t2">{entry ? (o.trigger_dir === 'up' ? '≥ ' : '≤ ') : `${t('col.trigger')} `}</span>{fmtPrice(o.trigger_px, pd)}<span className="tl-sub">{byTxt}{o.type === 'cond_limit' ? ` · ${t('tr.condPrice')} ${fmtPrice(o.price, pd)}` : ''}</span></>
                  : fmtPrice(o.price, pd)}
              </td>
              <td>{o.close_position && Number(o.qty) === 0 ? <span className="t2">{t('ls.closePos')}</span> : <Money text={locDec(o.qty)} />}</td>
              <td className="t2"><Money text={locDec(o.filled)} />{o.type === 'limit' && Number(o.queue_ahead ?? 0) > 0 && <span className="tl-sub" title={t('ls.queueHint')}>{t('ls.queue', { q: locDec(String(Number(o.queue_ahead))) })}</span>}</td>
              <td className="tl-act">
                {(o.type === 'limit' || (entry && o.status === 'new')) && <button className="btn ghost sm" onClick={() => setAmend(o)}>{t('ls.amend')}</button>}
                {o.status !== 'triggered' && <button className="btn ghost sm" onClick={() => cancel(o)}>{t('ls.cancel')}</button>}
              </td>
            </tr>
            {open === o.id && <OrderFillsRow acct={view.id} o={o} cols={7} seq={view.seq} />}
            </Fragment>
          )
        })}</tbody>
      </table>
      {amend && <AmendDialog o={amend} spec={specs?.[amend.instrument]} acct={view.id} onClose={() => setAmend(null)} />}
      <CancelAllConfirm open={all} acct={view.id} n={view.orders.length} orders={view.orders} onClose={() => setAll(false)} />
    </>
  )
}

export function CancelAllConfirm({ open, acct, n, orders, onClose }: { open: boolean; acct: number; n: number; orders: Order[]; onClose: () => void }) {
  const t = useT()
  return (
    <Confirm open={open} title={t('ls.cancelAll')} body={t('dlg.cancelAll.body', { n })} danger btn={t('ls.cancelAll')} onClose={onClose}
      run={async () => {
        // 撤之前记下来：5 秒内能一起撤回（持仓上的止盈止损不算委托）
        const ids = orders.filter((o) => o.type === 'limit' || o.type === 'cond_market' || o.type === 'cond_limit').map((o) => o.id)
        const r = await act<{ canceled: number }>('POST', `/api/trade/${acct}/cancel-all`, {})
        if (!ids.length || isLiveId(acct)) { toast(t('tr.canceled', { n: r.canceled })); return }
        toastAction(t('tr.canceled', { n: r.canceled }), t('undo.btn'), () => {
          Promise.allSettled(ids.map((id) => act('POST', `/api/trade/${acct}/orders/${id}/undo`, {}))).then((res) => {
            const bad = res.find((x) => x.status === 'rejected') as PromiseRejectedResult | undefined
            toast(bad ? rejectText(bad.reason) : t('undo.done'))
          })
        })
      }} />
  )
}

function AmendDialog({ o, spec, acct, onClose }: { o: Order; spec?: Spec; acct: number; onClose: () => void }) {
  const t = useT()
  const entry = o.type === 'cond_market' || o.type === 'cond_limit'
  const [price, setPrice] = useState(Number(o.price) > 0 ? o.price : '')
  const [qty, setQty] = useState(o.qty)
  const [trig, setTrig] = useState(o.trigger_px)
  const [err, setErr] = useState<string | null>(null)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const tk = (v: string) => (v && spec ? toTick(v, spec.tick) : v)
    try {
      const r = await act<Order>('PATCH', `/api/trade/${acct}/orders/${o.id}`, { price: o.type === 'cond_market' ? '0' : tk(price), qty, ...(entry ? { trigger_px: tk(trig) } : {}) })
      toast(orderToast(r, t, true))
      onClose()
    } catch (e2) { setErr(rejectText(e2)) }
  }
  return (
    <Dialog open title={`${t('dlg.amend.title')} · ${baseOf(o.instrument)}`} onClose={onClose}>
      <form className="au-form" onSubmit={submit} noValidate>
        {entry && <div className="op-dl-row"><span className="op-l">{t('tr.trigger')}</span><span className="op-in"><input inputMode="decimal" value={trig} onChange={(e) => setTrig(numIn(e.target.value))} aria-label={t('tr.trigger')} /><em>USDT</em></span></div>}
        {o.type !== 'cond_market' && <div className="op-dl-row"><span className="op-l">{t(entry ? 'tr.condPrice' : 'tr.price')}</span><span className="op-in"><input inputMode="decimal" value={price} onChange={(e) => setPrice(numIn(e.target.value))} aria-label={t(entry ? 'tr.condPrice' : 'tr.price')} /><em>USDT</em></span></div>}
        <div className="op-dl-row"><span className="op-l">{t('tr.qty')}</span><span className="op-in"><input inputMode="decimal" value={qty} onChange={(e) => setQty(numIn(e.target.value))} aria-label={t('tr.qty')} /><em>{baseOf(o.instrument)}</em></span></div>
        <div className={`op-msg ${err ? 'on' : ''}`} role="alert"><div><span>{err}</span></div></div>
        <div className="dlg-btns">
          <button type="button" className="btn ghost" onClick={onClose}>{t('common.cancel')}</button>
          <button className="btn primary">{t('common.save')}</button>
        </div>
      </form>
    </Dialog>
  )
}

// ---------- 历史委托、成交、资金费（分页） ----------

function usePaged<T>(acct: number, kind: string, seq: number) {
  const [page, setPage] = useState(1)
  const [data, setData] = useState<{ total: number; items: T[] } | null>(null)
  const size = 50
  const load = useCallback(async () => {
    try { setData(await api<{ total: number; items: T[] }>('GET', `/api/trade/${acct}/history?kind=${kind}&page=${page}&size=${size}`)) } catch { /* 下次再试 */ }
  }, [acct, kind, page])
  useEffect(() => { void load() }, [load, seq])
  const pages = Math.max(1, Math.ceil((data?.total ?? 0) / size))
  return { data, page, pages, setPage }
}

function Pager({ page, pages, setPage }: { page: number; pages: number; setPage: (p: number) => void }) {
  const t = useT()
  if (pages <= 1) return null
  return (
    <div className="tl-pager">
      <button className="btn ghost sm" disabled={page <= 1} onClick={() => setPage(page - 1)} aria-label={t('ls.prev')}>‹</button>
      <span className="num t2">{t('ls.page', { p: page, n: pages })}</span>
      <button className="btn ghost sm" disabled={page >= pages} onClick={() => setPage(page + 1)} aria-label={t('ls.next')}>›</button>
    </div>
  )
}

export function OrderHistory({ view }: { view: AccountView }) {
  const t = useT()
  const { data, page, pages, setPage } = usePaged<Order>(view.id, 'orders', view.seq)
  const [open, setOpen] = useState<string | null>(null)
  if (!data) return null
  if (!data.items.length) return <div className="mk-empty">{t('ls.empty.hist')}</div>
  return (
    <>
      <table className="tbl tl">
        <thead><tr><th>{t('col.time')}</th><th>{t('col.inst')}</th><th>{t('col.type')}</th><th>{t('col.price')}</th><th>{t('col.avg')}</th><th>{t('col.filled')}</th><th className="mk-opt2">{t('col.fee')}</th><th>{t('col.status')}</th></tr></thead>
        <tbody>{data.items.map((o) => {
          const cond = o.type === 'stop_market' || o.type === 'take_profit_market'
          const has = Number(o.filled) > 0
          return (
            <Fragment key={o.id}>
            <tr className={open === o.id ? 'tl-open' : ''}>
              <td className="t2"><ExpandTime has={has} on={open === o.id} toggle={() => setOpen(open === o.id ? null : o.id)} t={o.updated} /></td>
              <td><span className="sym">{baseOf(o.instrument)}</span><span className={`tl-tag ${o.side === 'buy' ? 'up' : 'down'}`}>{t(o.side === 'buy' ? 'side.buy' : 'side.sell')}</span></td>
              <td>{t(`otype.${o.type}` as 'otype.limit')}</td>
              <td>{o.type === 'market' ? '—' : fmtPrice(cond ? o.trigger_px : o.price)}</td>
              <td>{Number(o.filled) > 0 ? fmtPrice(o.avg_px) : '—'}</td>
              <td><Money text={locDec(o.filled)} /><span className="t3"> / {Number(o.qty) > 0 ? locDec(o.qty) : '—'}</span></td>
              <td className="t2 mk-opt2">{has ? <Money text={fmtAmt(o.fee, 4)} /> : '—'}</td>
              <td className="tl-status"><span className={o.status === 'filled' ? '' : 't2'}>{t(`status.${o.status}` as 'status.new')}</span>{o.reason && o.reason !== 'USER' && <span className="tl-sub">{reasonText(o.reason)}</span>}</td>
            </tr>
            {open === o.id && <OrderFillsRow acct={view.id} o={o} cols={8} seq={view.seq} />}
            </Fragment>
          )
        })}</tbody>
      </table>
      <Pager page={page} pages={pages} setPage={setPage} />
    </>
  )
}

/** 时间格：有成交的委托可以点开看逐笔成交 */
function ExpandTime({ has, on, toggle, t: ms }: { has: boolean; on: boolean; toggle: () => void; t: number }) {
  const t = useT()
  if (!has) return <span className="tl-exp-pad">{time(ms)}</span>
  return (
    <button type="button" className="tl-exp" aria-expanded={on} onClick={toggle} title={t(on ? 'ls.hideFills' : 'ls.showFills')}>
      <svg width="8" height="8" viewBox="0 0 8 8" aria-hidden="true"><path d="M2 1l4 3-4 3" fill="none" stroke="currentColor" strokeWidth="1.2" /></svg>
      {time(ms)}
    </button>
  )
}

/** 一张委托的逐笔成交：每笔的价格、数量、挂单还是吃单、手续费、平仓盈亏，最后一行合计 */
function OrderFillsRow({ acct, o, cols, seq }: { acct: number; o: Order; cols: number; seq: number }) {
  const t = useT()
  const [fills, setFills] = useState<Fill[] | null>(null)
  useEffect(() => {
    let live = true
    api<Fill[]>('GET', `/api/trade/${acct}/history?order=${encodeURIComponent(o.id)}`).then((f) => live && setFills(f)).catch(() => live && setFills([]))
    return () => { live = false }
  }, [acct, o.id, seq])
  const fee = (fills ?? []).reduce((s, f) => s + Number(f.fee), 0)
  const pnl = (fills ?? []).reduce((s, f) => s + Number(f.realized), 0)
  const maker = (fills ?? []).filter((f) => f.maker).length
  return (
    <tr className="tl-fills"><td colSpan={cols}>
      {!fills ? <span className="t3">…</span> : !fills.length ? <span className="t3">{t('ls.fillsGone')}</span> : (
        <table className="tl-sub-tbl">
          <thead><tr><th>{t('col.time')}</th><th>{t('col.price')}</th><th>{t('col.qty')}</th><th>{t('col.role')}</th><th>{t('col.fee')}</th><th>{t('col.realized')}</th></tr></thead>
          <tbody>
            {fills.map((f) => (
              <tr key={f.seq}>
                <td className="t2">{time(f.time)}</td>
                <td>{fmtPrice(f.px)}</td>
                <td><Money text={locDec(f.qty)} /></td>
                <td className={f.liquidation ? 'down' : 't2'}>{t(f.liquidation ? 'role.liq' : f.maker ? 'role.maker' : 'role.taker')}</td>
                <td className="t2"><Money text={fmtAmt(f.fee, 4)} /></td>
                <td className={dir(Number(f.realized))}>{Number(f.realized) ? <Money text={fmtSigned(f.realized)} /> : <span className="t3">—</span>}</td>
              </tr>
            ))}
            <tr className="tl-sum">
              <td className="t2">{t('ls.fillsSum', { n: fills.length, m: maker })}</td>
              <td>{fmtPrice(o.avg_px)}</td>
              <td><Money text={locDec(o.filled)} /></td>
              <td />
              <td><Money text={fmtAmt(fee, 4)} /></td>
              <td className={dir(pnl)}>{pnl ? <Money text={fmtSigned(pnl)} /> : <span className="t3">—</span>}</td>
            </tr>
          </tbody>
        </table>
      )}
    </td></tr>
  )
}

export function Fills({ view }: { view: AccountView }) {
  const t = useT()
  const { data, page, pages, setPage } = usePaged<Fill>(view.id, 'fills', view.seq)
  if (!data) return null
  if (!data.items.length) return <div className="mk-empty">{t('ls.empty.fills')}</div>
  return (
    <>
      <table className="tbl tl">
        <thead><tr><th>{t('col.time')}</th><th>{t('col.inst')}</th><th>{t('col.price')}</th><th>{t('col.qty')}</th><th>{t('col.fee')}</th><th>{t('col.realized')}</th><th className="mk-opt2">{t('col.role')}</th></tr></thead>
        <tbody>{data.items.map((f) => (
          <tr key={f.seq}>
            <td className="t2">{time(f.time)}</td>
            <td><span className="sym">{baseOf(f.instrument)}</span><span className={`tl-tag ${f.side === 'buy' ? 'up' : 'down'}`}>{t(f.side === 'buy' ? 'side.buy' : 'side.sell')}</span></td>
            <td>{fmtPrice(f.px)}</td>
            <td><Money text={locDec(f.qty)} /></td>
            <td className="t2"><Money text={fmtAmt(f.fee, 4)} /></td>
            <td className={dir(Number(f.realized))}>{Number(f.realized) ? <Money text={fmtSigned(f.realized)} /> : <span className="t3">—</span>}</td>
            <td className={`mk-opt2 ${f.liquidation ? 'down' : 't3'}`}>{t(f.liquidation ? 'role.liq' : f.maker ? 'role.maker' : 'role.taker')}</td>
          </tr>
        ))}</tbody>
      </table>
      <Pager page={page} pages={pages} setPage={setPage} />
    </>
  )
}

export function Funding({ view }: { view: AccountView }) {
  const t = useT()
  const { data, page, pages, setPage } = usePaged<FundingRec>(view.id, 'funding', view.seq)
  if (!data) return null
  if (!data.items.length) return <div className="mk-empty">{t('ls.empty.funding')}</div>
  return (
    <>
      <table className="tbl tl">
        <thead><tr><th>{t('col.time')}</th><th>{t('col.inst')}</th><th>{t('col.rate')}</th><th>{t('col.pos')}</th><th>{t('col.mark')}</th><th>{t('col.amount')}</th></tr></thead>
        <tbody>{data.items.map((f) => (
          <tr key={f.seq}>
            <td className="t2">{time(f.time)}</td>
            <td><span className="sym">{baseOf(f.instrument)}</span></td>
            <td>{locDec((Number(f.rate) * 100).toFixed(4))}%</td>
            <td className={dir(Number(f.qty))}><Money text={locDec(f.qty)} /></td>
            <td>{fmtPrice(f.mark)}</td>
            <td className={dir(Number(f.amount))}><Money text={fmtSigned(f.amount, 4)} /></td>
          </tr>
        ))}</tbody>
      </table>
      <Pager page={page} pages={pages} setPage={setPage} />
    </>
  )
}

// ---------- 二次确认 ----------

export function Confirm({ open, title, body, btn, danger, onClose, run }: { open: boolean; title: string; body: string; btn: string; danger?: boolean; onClose: () => void; run: () => Promise<void> }) {
  const t = useT()
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  useEffect(() => { if (open) setErr(null) }, [open])
  const go = async () => {
    setBusy(true)
    try { await run(); onClose() } catch (e) { setErr(rejectText(e)); refreshTrade() } finally { setBusy(false) }
  }
  return (
    <Dialog open={open} title={title} onClose={onClose}>
      <p className="dlg-text">{body}</p>
      <div className={`op-msg ${err ? 'on' : ''}`} role="alert"><div><span>{err}</span></div></div>
      <div className="dlg-btns">
        <button className="btn ghost" onClick={onClose}>{t('common.cancel')}</button>
        <button className={`btn ${danger ? 'danger' : 'primary'}`} onClick={go} disabled={busy}>{btn}</button>
      </div>
    </Dialog>
  )
}
