// 现货下单区和资产列表（D83）。样式沿用合约下单区，少掉杠杆、保证金、强平。
import { HaltNote } from '../../ui/HaltNote'
import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { api, type PaperAccount } from '../../lib/session'
import { acctName, fmtTime, useT } from '../../lib/i18n'
import { baseOf, fmtPrice, numIn, priceDecimals } from '../../lib/format'
import { Seg } from '../../ui/Seg'
import { Dialog } from '../../ui/Dialog'
import { Money } from '../../ui/Money'
import { toast, usePrivate } from '../../lib/privacy'
import { rejectText, type Order } from '../../lib/trade'
import { orderToast } from './OrderPanel'
import { refreshSpot, type SpotOrder, type SpotSpec, type SpotView } from '../../lib/spot'
import { go } from '../../lib/route'
import { locDec, numLocale } from '../../lib/tz'

const fmtQty = (v: string | number, d = 6) => {
  const n = Number(v)
  return Number.isFinite(n) ? n.toLocaleString(numLocale(), { maximumFractionDigits: d }) : '—'
}
const fmtUsd = (v: string | number, d = 2) => Number(v).toLocaleString(numLocale(), { minimumFractionDigits: d, maximumFractionDigits: d })
const decOf = (step: string) => (step.includes('.') ? step.split('.')[1].replace(/0+$/, '').length : 0)

const FormError = ({ text }: { text?: string }) => (text ? <p className="op-err" role="alert">{text}</p> : null)

export function SpotPanel({ inst, last, bid, ask, spec, view, current, pickPrice }: {
  inst: string; last?: string; bid?: string; ask?: string; spec?: SpotSpec; view: SpotView | null
  current: PaperAccount | null; pickPrice?: string
}) {
  const t = useT()
  const priv = usePrivate()
  const base = baseOf(inst)
  const [side, setSide] = useState<'buy' | 'sell'>('buy')
  const [type, setType] = useState<'limit' | 'market' | 'cond'>('limit')
  const [price, setPrice] = useState('')
  const [trigger, setTrigger] = useState('')
  const [qty, setQty] = useState('')
  const [unit, setUnit] = useState<'coin' | 'usdt'>('usdt')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [xfer, setXfer] = useState(false)
  useEffect(() => { setPrice(''); setTrigger(''); setQty(''); setErr('') }, [inst])
  useEffect(() => { if (pickPrice) setPrice(pickPrice) }, [pickPrice])
  const bal = (coin: string) => view?.balances.find((b) => b.coin === coin)
  const freeQ = Number(bal('USDT')?.free ?? 0), freeB = Number(bal(base)?.free ?? 0)
  const ref = type === 'limit' ? Number(price) || Number(side === 'buy' ? ask : bid) : type === 'cond' ? Number(trigger) || Number(last) : Number(side === 'buy' ? ask : bid)
  const qd = spec ? decOf(spec.step) : 6
  // 数量：统一成币的数量算预览
  const coinQty = useMemo(() => {
    const q = Number(qty)
    if (!q || !ref) return 0
    const raw = unit === 'coin' ? q : q / ref
    const step = Number(spec?.step ?? 0) || 1e-8
    return Math.floor(raw / step + 1e-9) * step
  }, [qty, unit, ref, spec])
  const notional = coinQty * ref
  const feeRate = Number(spec?.fee ?? 0.001)
  const receive = side === 'buy' ? coinQty * (1 - feeRate) : notional * (1 - feeRate)
  const setPct = (p: number) => {
    if (side === 'buy') { setUnit('usdt'); setQty((freeQ * p / 100).toFixed(2)) } else { setUnit('coin'); setQty((Math.floor((freeB * p) / 100 / (Number(spec?.step) || 1e-8)) * (Number(spec?.step) || 1e-8)).toFixed(qd)) }
  }
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (e.target !== e.currentTarget) return // 只认这个表单自己的提交（里面的划转对话框提交会冒泡上来）
    if (!current || busy) return
    setErr('')
    if (!(coinQty > 0)) { setErr(t('err.BAD_QTY')); return }
    setBusy(true)
    try {
      const o = await api<Order>('POST', `/api/spot/${current.id}/orders`, {
        instrument: inst, side, type: type === 'cond' ? 'cond_market' : type,
        price: type === 'limit' ? (price || String(ref)) : '0', trigger_px: type === 'cond' ? trigger : '0', qty: coinQty.toFixed(qd),
      })
      setQty('')
      refreshSpot()
      // 说清楚成交了多少、挂在哪个价（11-17 走查：原来买、卖都只写"已保存"）。现货的条件单按最新价触发
      toast(orderToast({ ...o, trigger_by: 'last' }, t))
    } catch (e2) { setErr(rejectText(e2)) } finally { setBusy(false) }
  }
  if (!current) return <div className="op-login"><p>{t('ls.login')}</p></div>
  const mask = (s: string) => (priv ? '••••' : s)
  return (
    <form className="op" onSubmit={submit} noValidate data-spot>
      <div className="op-acct">
        <span className="op-acct-name" title={t('tr.switchAccount')}>{acctName(current.name)}</span>
        <button type="button" className="op-link" data-spot-transfer onClick={() => setXfer(true)}>{t('sp.transfer')}</button>
        <button type="button" className="op-link" onClick={() => go('account')}>{t('tr.manage')}</button>
      </div>
      <div className="op-ro">
        <div className="ro op-n"><span className="ro-l">{t('sp.availQuote')}</span><span className="ro-v num op-big-v">{mask(fmtUsd(freeQ))}</span></div>
        <div className="ro op-n"><span className="ro-l">{t('sp.availBase', { base })}</span><span className="ro-v num op-big-v">{mask(fmtQty(freeB, qd))}</span></div>
      </div>
      <HaltNote />
      {freeQ === 0 && freeB === 0 && (
        <div className="op-follow-note"><span>{t('sp.emptyHint')}</span><button type="button" className="op-link" onClick={() => setXfer(true)}>{t('sp.transfer')}</button></div>
      )}
      <div className={`op-side ${side}`} role="group" aria-label={t('col.side')}>
        <button type="button" aria-pressed={side === 'buy'} onClick={() => setSide('buy')}>{t('sp.buy')}</button>
        <button type="button" aria-pressed={side === 'sell'} onClick={() => setSide('sell')}>{t('sp.sell')}</button>
      </div>
      <div className="op-row">
        <Seg label={t('col.type')} value={type} onChange={setType} options={[{ value: 'limit', label: t('tr.limit') }, { value: 'market', label: t('tr.market') }, { value: 'cond', label: t('tr.cond') }]} />
      </div>
      {type === 'cond' && (
        <label className="op-f"><span className="op-l">{t('tr.trigger')}</span>
          <span className="op-in"><input inputMode="decimal" value={trigger} onChange={(e) => setTrigger(numIn(e.target.value))} aria-label={t('tr.trigger')} /><em>USDT</em></span>
        </label>
      )}
      {type === 'limit' && (
        <label className="op-f"><span className="op-l">{t('tr.price')}</span>
          <span className="op-in"><input inputMode="decimal" value={price} placeholder={last ? Number(last).toFixed(priceDecimals(Number(last))) : ''} onChange={(e) => setPrice(numIn(e.target.value))} aria-label={t('tr.price')} /><em>USDT</em></span>
        </label>
      )}
      <label className="op-f"><span className="op-l">{t('tr.qty')}</span>
        <span className="op-in">
          <input inputMode="decimal" value={qty} onChange={(e) => setQty(numIn(e.target.value))} aria-label={t('tr.qty')} />
          <span className="op-unit" role="group">
            <button type="button" aria-pressed={unit === 'coin'} onClick={() => { setUnit('coin'); setQty('') }}>{base}</button>
            <button type="button" aria-pressed={unit === 'usdt'} onClick={() => { setUnit('usdt'); setQty('') }}>USDT</button>
          </span>
        </span>
      </label>
      <div className="op-pct" role="group">
        {[25, 50, 75, 100].map((x) => <button type="button" key={x} onClick={() => setPct(x)}>{x}%</button>)}
      </div>
      <dl className="op-pv op-key">
        <div><dt>{t('sp.amount')}</dt><dd className="num">{coinQty > 0 ? <Money text={fmtUsd(notional)} unit="USDT" /> : '—'}</dd></div>
        <div><dt>{t('sp.fee', { p: locDec((feeRate * 100).toFixed(1)) })}</dt><dd className="num">{coinQty > 0 ? <Money text={side === 'buy' ? `${fmtQty(coinQty * feeRate, qd + 2)} ${base}` : `${fmtUsd(notional * feeRate, 4)} USDT`} /> : '—'}</dd></div>
        <div><dt>{t('sp.receive')}</dt><dd className="num">{coinQty > 0 ? <Money text={side === 'buy' ? `${fmtQty(receive, qd + 2)} ${base}` : `${fmtUsd(receive)} USDT`} /> : '—'}</dd></div>
      </dl>
      <FormError text={err || undefined} />
      <button className={`op-submit ${side}`} disabled={busy}>{busy && <span className="spin" aria-hidden />}{t(side === 'buy' ? 'sp.submitBuy' : 'sp.submitSell', { base })}</button>
      <TransferDialog open={xfer} onClose={() => setXfer(false)} acct={current.id} spotFree={freeQ} />
    </form>
  )
}

function TransferDialog({ open, onClose, acct, spotFree }: { open: boolean; onClose: () => void; acct: number; spotFree: number }) {
  const t = useT()
  const [to, setTo] = useState<'spot' | 'perp'>('spot')
  const [amt, setAmt] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [perpFree, setPerpFree] = useState<number | null>(null)
  useEffect(() => {
    if (!open) { setAmt(''); setErr(''); return }
    // 合约转出上限不算未实现盈利（平仓后才能转），后端给的是 transferable
    void api<{ available: string; transferable?: string }>('GET', `/api/trade/${acct}`).then((v) => setPerpFree(Number(v.transferable ?? v.available))).catch(() => undefined)
  }, [open, acct])
  const max = to === 'spot' ? perpFree ?? 0 : spotFree
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr('')
    try {
      await api('POST', `/api/spot/${acct}/transfer`, { to, amount: amt })
      refreshSpot()
      toast(t('sp.transferred'))
      onClose()
    } catch (e2) { setErr(rejectText(e2)) } finally { setBusy(false) }
  }
  return (
    <Dialog open={open} title={t('sp.transfer')} onClose={onClose}>
      <form className="au-form" onSubmit={submit} noValidate>
        <div className="op-dl-row"><span className="op-l">{t('sp.dir')}</span>
          <Seg label={t('sp.dir')} value={to} onChange={setTo} options={[{ value: 'spot', label: t('sp.toSpot') }, { value: 'perp', label: t('sp.toPerp') }]} />
        </div>
        <div className="op-dl-row"><span className="op-l">{t('sp.amt')}</span>
          <span className="op-in"><input inputMode="decimal" value={amt} onChange={(e) => setAmt(numIn(e.target.value))} aria-label={t('sp.amt')} /><em>USDT</em></span>
        </div>
        <p className="dlg-text op-small">{t('sp.maxOut', { v: fmtUsd(max) })} <button type="button" className="op-link" onClick={() => setAmt(max.toFixed(2))}>{t('sp.all')}</button></p>
        <FormError text={err || undefined} />
        <div className="dlg-btns">
          <button type="button" className="btn ghost" onClick={onClose}>{t('common.cancel')}</button>
          <button className="btn primary" disabled={busy || !(Number(amt) > 0)}>{t('sp.transferDo')}</button>
        </div>
      </form>
    </Dialog>
  )
}

export function SpotAssets({ view, onPick }: { view: SpotView; onPick: (inst: string) => void }) {
  const t = useT()
  const priv = usePrivate()
  const m = (s: string) => (priv ? '••••' : s)
  return (
    <table className="tbl tl" data-spot-assets>
      <thead><tr><th>{t('sp.coin')}</th><th className="r">{t('sp.free')}</th><th className="r">{t('sp.locked')}</th><th className="r">{t('sp.cost')}</th><th className="r">{t('sp.value')}</th><th className="r">{t('sp.unreal')}</th></tr></thead>
      <tbody>
        {view.balances.map((b) => {
          const up = Number(b.unreal)
          return (
            <tr key={b.coin} onClick={b.coin === 'USDT' ? undefined : () => onPick(`${b.coin}-SPOT`)} style={b.coin === 'USDT' ? undefined : { cursor: 'pointer' }}>
              <td>{b.coin}</td>
              <td className="r num">{m(b.coin === 'USDT' ? fmtUsd(b.free) : fmtQty(b.free, 6))}</td>
              <td className="r num">{m(b.coin === 'USDT' ? fmtUsd(b.locked) : fmtQty(b.locked, 6))}</td>
              <td className="r num">{b.coin === 'USDT' ? '—' : fmtPrice(b.cost)}</td>
              <td className="r num">{m(fmtUsd(b.value))}</td>
              <td className={`r num ${up > 0 ? 'up' : up < 0 ? 'down' : ''}`}>{b.coin === 'USDT' ? '—' : m(`${up > 0 ? '+' : up < 0 ? '−' : ''}${fmtUsd(Math.abs(up))}`)}</td>
            </tr>
          )
        })}
        {!view.balances.length && <tr><td colSpan={6} className="t3">{t('sp.noAssets')}</td></tr>}
      </tbody>
    </table>
  )
}

const typeName = (t: ReturnType<typeof useT>, o: SpotOrder) => t(o.type === 'market' ? 'tr.market' : o.type === 'limit' ? 'tr.limit' : 'tr.cond')

export function SpotOrders({ view, acct }: { view: SpotView; acct: number }) {
  const t = useT()
  if (!view.orders.length) return <div className="mk-empty">{t('sp.noOpen')}</div>
  return (
    <table className="tbl tl">
      <thead><tr><th>{t('col.inst')}</th><th>{t('col.side')}</th><th>{t('col.type')}</th><th className="r">{t('tr.price')}</th><th className="r">{t('tr.qty')}</th><th className="r">{t('sp.filled')}</th><th>{t('sp.time')}</th><th /></tr></thead>
      <tbody>
        {view.orders.map((o) => (
          <tr key={o.id}>
            <td>{baseOf(o.instrument)}</td>
            <td className={o.side === 'buy' ? 'up' : 'down'}>{t(o.side === 'buy' ? 'sp.buy' : 'sp.sell')}</td>
            <td>{typeName(t, o)}</td>
            <td className="r num">{o.type === 'cond_market' ? `${o.trigger_dir === 'up' ? '≥' : '≤'} ${fmtPrice(o.trigger_px)}` : fmtPrice(o.price)}</td>
            <td className="r num">{fmtQty(o.qty, 6)}</td>
            <td className="r num">{fmtQty(o.filled, 6)}{o.type === 'limit' && Number(o.queue_ahead ?? 0) > 0 && <span className="tl-sub" title={t('ls.queueHint')}>{t('ls.queue', { q: String(Number(o.queue_ahead)) })}</span>}</td>
            <td className="num">{fmtTime(o.created)}</td>
            <td className="r"><button className="btn ghost sm" onClick={() => void api('DELETE', `/api/spot/${acct}/orders/${o.id}`).then(refreshSpot)}>{t('ls.cancel')}</button></td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

export function SpotFills({ view }: { view: SpotView }) {
  const t = useT()
  const priv = usePrivate()
  if (!view.fills.length) return <div className="mk-empty">{t('sp.noFills')}</div>
  return (
    <table className="tbl tl">
      <thead><tr><th>{t('sp.time')}</th><th>{t('col.inst')}</th><th>{t('col.side')}</th><th className="r">{t('tr.price')}</th><th className="r">{t('tr.qty')}</th><th className="r">{t('sp.feeCol')}</th><th className="r">{t('sp.realized')}</th></tr></thead>
      <tbody>
        {view.fills.map((f) => {
          const r = Number(f.realized)
          return (
            <tr key={f.seq}>
              <td className="num">{fmtTime(f.time)}</td>
              <td>{baseOf(f.instrument)}</td>
              <td className={f.side === 'buy' ? 'up' : 'down'}>{t(f.side === 'buy' ? 'sp.buy' : 'sp.sell')}</td>
              <td className="r num">{fmtPrice(f.px)}</td>
              <td className="r num">{fmtQty(f.qty, 6)}</td>
              <td className="r num">{priv ? '••••' : `${fmtQty(f.fee, 6)} ${f.fee_coin}`}</td>
              <td className={`r num ${r > 0 ? 'up' : r < 0 ? 'down' : ''}`}>{f.side === 'sell' ? (priv ? '••••' : `${r > 0 ? '+' : r < 0 ? '−' : ''}${fmtUsd(Math.abs(r))}`) : '—'}</td>
            </tr>
          )
        })}
      </tbody>
    </table>
  )
}
