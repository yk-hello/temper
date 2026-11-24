// 跟随状态和控制（3.9、3.20）：下单区和 AI 页共用。暂停、恢复（先告诉用户会产生什么交易）、两种停止。
import { useState } from 'react'
import { useT, errText, fmtTime, reasonText, type Key, acctName, listSep, stop } from '../../lib/i18n'
import { ApiError, type PaperAccount } from '../../lib/session'
import { pauseFollow, resumeFollow, resumePlan, stopFollow, usePoll, type ExecRec, type Follow, type PlanItem, type StrategyStatus } from '../../lib/follow'
import { baseOf, fmtPrice } from '../../lib/format'
import { toast, usePrivate } from '../../lib/privacy'
import { Dialog } from '../../ui/Dialog'
import { Money } from '../../ui/Money'
import { fmtAmt, fmtSigned } from '../trade/OrderPanel'
import { useRowsMotion } from './rows'
import type { TradeMarker } from '../../charts/Scope'
import { locDec } from '../../lib/tz'

const trim = (s: string | number) => String(Number(Number(s).toFixed(6)))

/** 图上的 AI 开平仓点：平仓的那一笔按这笔赚亏上色，开仓是中性色（D72）。why 给理由。 */
export function recMarkers(t: ReturnType<typeof useT>, f: Follow, inst: string, why: (msg?: string) => string | undefined, priv: boolean): TradeMarker[] {
  let qty = 0, avg = 0
  const out: TradeMarker[] = []
  for (const r of [...f.records].reverse()) {
    const filled = Number(r.filled)
    if (r.instrument !== inst || !r.side || filled <= 0) continue
    const px = Number(r.avg_px)
    const q = r.side === 'buy' ? filled : -filled
    let tone: TradeMarker['tone']
    if (qty !== 0 && Math.sign(qty) !== Math.sign(q)) {
      const c = Math.min(Math.abs(q), Math.abs(qty))
      tone = (px - avg) * c * Math.sign(qty) >= 0 ? 'up' : 'down'
      const left = qty + q
      if (Math.sign(left) === Math.sign(qty)) qty = left
      else { qty = left; avg = px }
    } else {
      avg = qty === 0 ? px : (avg * Math.abs(qty) + px * filled) / (Math.abs(qty) + filled)
      qty += q
    }
    const reason = why(r.msg) ?? noteText(t, r.note)
    const say = recSentence(t, r, priv)
    // 图上的标签写短的（整句压在 K 线上太长，11-17 走查）；整句和原因放在悬停提示里
    const mk = t('fw.mk', { side: t(r.side === 'buy' ? 'fw.bought' : 'fw.sold'), qty: priv ? '••••' : String(Number(filled.toFixed(6))), base: baseOf(r.instrument), px: fmtPrice(r.avg_px) })
    out.push({ id: String(r.id), t: r.end ?? r.time, price: px, buy: r.side === 'buy', say: mk, tone, hint: reason ? `${say}${stop()}${reason}` : say })
  }
  return out
}

/** 哪些币现在是手动管理（4.1） */
function ManualList({ f }: { f: Follow }) {
  const t = useT()
  const list = Object.keys(f.manual ?? {}).map(baseOf)
  return list.length ? <p className="fw-manual-list">{t('fw.manualList', { list: list.join(listSep()) })}</p> : null
}

/** 按你的跟随设置，已买入 0.03 BTC（80,120）。D75 第 23 条：写用户授权的规则，不写"AI 替你"。 */
export function recSentence(t: ReturnType<typeof useT>, r: ExecRec, priv = false): string {
  const base = baseOf(r.instrument)
  const trim = (x: string | number) => (priv ? '••••' : String(Number(Number(x).toFixed(6))))
  switch (r.state) {
    case 'filled':
    case 'partial': {
      if (Number(r.filled) <= 0) return t('fw.r.none')
      const k: Key = r.state === 'partial' ? 'fw.r.partial' : 'fw.r.filled'
      return t(k, { side: t(r.side === 'buy' ? 'fw.bought' : 'fw.sold'), qty: trim(r.filled), base, px: fmtPrice(r.avg_px) })
    }
    case 'pending': return t('fw.r.pending', { side: t(r.side === 'buy' ? 'fw.buying' : 'fw.selling'), qty: trim(r.qty), base })
    case 'expired': return r.reject ? t('fw.r.rejected', { why: errText(r.reject) }) : t('fw.r.expired', { base })
    case 'interrupted': return t('fw.r.interrupted', { base })
    case 'skipped_blacklist': return t('fw.r.blacklist', { base })
    case 'skipped_expired': return t('fw.r.sigExpired', { base })
    case 'skipped_price_moved': return t('fw.r.moved', { base })
    case 'skipped_min_adjust': return t('fw.r.small', { base })
    case 'skipped_manual': return t('fw.r.skipManual', { base })
    case 'manual': return t('fw.r.manual', { base })
    case 'manual_end': return t('fw.r.manualEnd', { base })
    default: return r.state
  }
}

export function noteText(t: ReturnType<typeof useT>, n?: string): string {
  if (!n) return ''
  const k = `fw.n.${n}` as Key
  return t(k) === k ? reasonText(n) : t(k)
}

export function statusText(t: ReturnType<typeof useT>, f: Follow): string {
  if (f.status === 'stopped' || f.status === 'closing') {
    const k = `fw.stop.${f.reason ?? 'user'}` as Key
    return f.status === 'closing' ? t('fw.closing') : t(k)
  }
  return t(f.status === 'paused' ? 'fw.paused' : 'fw.active')
}

/** 暂停 / 恢复 / 停止 三个按钮和它们的对话框 */
export function FollowControls({ f, compact }: { f: Follow; compact?: boolean }) {
  const t = useT()
  const [busy, setBusy] = useState(false)
  const [plan, setPlan] = useState<PlanItem[] | null>(null)
  const [stopOpen, setStopOpen] = useState(false)
  const run = async (fn: () => Promise<unknown>, ok?: string) => {
    if (busy) return
    setBusy(true)
    try {
      await fn()
      if (ok) toast(ok)
    } catch (e) {
      toast(e instanceof ApiError ? errText(e.code, e.params) : errText('INTERNAL_ERROR'))
    } finally {
      setBusy(false)
    }
  }
  const askResume = () => run(async () => { setPlan(await resumePlan(f.id)) })
  if (f.status !== 'active' && f.status !== 'paused') return null
  return (
    <div className={`fw-ctl ${compact ? 'compact' : ''}`}>
      {f.status === 'active'
        ? <button type="button" className="btn secondary sm" disabled={busy} onClick={() => run(() => pauseFollow(f.id), t('fw.pausedToast'))}>{t('fw.pause')}</button>
        : <button type="button" className="btn primary sm" disabled={busy} onClick={askResume}>{t('fw.resume')}</button>}
      <button type="button" className="btn ghost sm" disabled={busy} onClick={() => setStopOpen(true)}>{t('fw.stop')}</button>
      <Dialog open={plan !== null} title={t('fw.resumeTitle')} onClose={() => setPlan(null)}>
        <p className="fw-p">{t(plan?.length ? 'fw.resumeTrades' : 'fw.resumeNone')}</p>
        {!!plan?.length && (
          <table className="tbl fw-plan">
            <tbody>{plan.map((x) => (
              <tr key={x.instrument}>
                <td><span className="sym">{baseOf(x.instrument)}</span></td>
                <td className={x.side === 'buy' ? 'up' : 'down'}>{t(x.side === 'buy' ? 'tr.buy' : 'tr.sell')}</td>
                <td><Money text={trim(x.qty)} /></td>
                <td className="t2"><Money text={fmtAmt(x.notional)} unit="USDT" /></td>
              </tr>
            ))}</tbody>
          </table>
        )}
        <div className="dlg-btns">
          <button type="button" className="btn ghost" onClick={() => setPlan(null)}>{t('common.cancel')}</button>
          <button type="button" className="btn primary" disabled={busy} onClick={() => run(async () => { await resumeFollow(f.id); setPlan(null) }, t(plan?.length ? 'fw.resumedToast' : 'fw.resumedNone'))} /* 不用交易时别说"补齐"（11-17 走查） */>{t('fw.resumeGo')}</button>
        </div>
      </Dialog>
      <Dialog open={stopOpen} title={t('fw.stopTitle')} onClose={() => setStopOpen(false)}>
        <p className="fw-p">{t('fw.stopLead')}</p>
        <div className="fw-stops">
          <button type="button" className="fw-stop-opt" disabled={busy} onClick={() => run(async () => { await stopFollow(f.id, 'close'); setStopOpen(false) }, t('fw.stoppedClose'))}>
            <b>{t('fw.stopClose')}</b><span>{t('fw.stopCloseH')}</span>
          </button>
          <button type="button" className="fw-stop-opt" disabled={busy} onClick={() => run(async () => { await stopFollow(f.id, 'keep'); setStopOpen(false) }, t('fw.stoppedKeep'))}>
            <b>{t('fw.stopKeep')}</b><span>{t('fw.stopKeepH')}</span>
          </button>
        </div>
        <div className="dlg-btns"><button type="button" className="btn ghost" onClick={() => setStopOpen(false)}>{t('common.cancel')}</button></div>
      </Dialog>
    </div>
  )
}

/** 下单区里的跟随状态（D38）：替换下单表单，能切回手动下单 */
export function FollowSide({ f, current, onManual }: { f: Follow; current: PaperAccount; onManual: () => void }) {
  const t = useT()
  const list = usePoll<StrategyStatus[]>('/api/strategies', 8000).data
  const s = list?.find((x) => x.strategy_id === f.strategy)
  const pnl = Number(f.pnl), nav = Number(f.nav), stopAt = Number(f.stop_at), alloc = Number(f.params.alloc)
  const room = Math.max(0, nav - stopAt)
  const roomFrac = alloc - stopAt > 0 ? Math.min(1, room / (alloc - stopAt)) : 0
  const last = f.records[0]
  const priv = usePrivate()
  return (
    <div className="fw-side">
      <div className="op-acct">
        <span className="op-acct-name" title={t('tr.switchAccount')}>{acctName(current.name)}</span>
      </div>
      <div className="fw-st">
        <span className={`sq ${f.status === 'active' ? (s && !s.online ? 'warn' : 'ok') : 'warn'}`} />
        <span>{f.status === 'active' && s && !s.online ? t('ai.offlineLong') : statusText(t, f)}</span>
      </div>
      <div className="fw-who"><span className="sym">{f.strategy}</span>{s?.sample && <span className="ai-tag">{t('ai.sample')}</span>}</div>
      <ManualList f={f} />
      <div className="fw-ro">
        <div className="ro"><span className="ro-l">{t('wz.alloc')}</span><span className="ro-v num"><Money text={fmtAmt(alloc, 0)} /></span></div>
        <div className="ro"><span className="ro-l">{t('fw.pnl')}</span><span className={`ro-v num ${pnl > 0 ? 'up' : pnl < 0 ? 'down' : ''}`}><Money text={fmtSigned(pnl)} /></span></div>
        <div className="ro fw-room" title={t('fw.roomHint')}>
          <span className="ro-l">{t('fw.room')}</span>
          <span className="ro-v num"><Money text={fmtAmt(room)} /></span>
          <span className="op-meter"><i style={{ width: `${roomFrac * 100}%` }} className={roomFrac < 0.25 ? 'bad' : roomFrac < 0.5 ? 'warn' : ''} /></span>
        </div>
      </div>
      {last && <p className="fw-last"><span className="t3">{fmtTime(last.time, { seconds: true })}</span> {recSentence(t, last, priv)}</p>}
      <FollowControls f={f} />
      <button type="button" className="op-link fw-manual" onClick={onManual}>{t('fw.toManual')}</button>
    </div>
  )
}

/** 我的跟随：参数和每条目标的执行记录（AI 页） */
export function FollowDetail({ f }: { f: Follow }) {
  const t = useT()
  const rows = useRowsMotion(f.records, (r) => String(r.id))
  const priv = usePrivate()
  const pnl = Number(f.pnl)
  return (
    <div className="fw-detail">
      <div className="fw-detail-h">
        <span className="fw-st"><span className={`sq ${f.status === 'active' ? 'ok' : f.status === 'paused' ? 'warn' : ''}`} />{statusText(t, f)}</span>
        <FollowControls f={f} />
      </div>
      <div className="fw-ro wide">
        <div className="ro"><span className="ro-l">{t('wz.alloc')}</span><span className="ro-v num"><Money text={fmtAmt(f.params.alloc, 0)} unit="USDT" /></span></div>
        <div className="ro"><span className="ro-l">{t('fw.pnl')}</span><span className={`ro-v num ${pnl > 0 ? 'up' : pnl < 0 ? 'down' : ''}`}><Money text={fmtSigned(pnl)} /></span></div>
        <div className="ro"><span className="ro-l">{t('fw.stopAt')}</span><span className="ro-v num"><Money text={fmtAmt(f.stop_at, 0)} /></span></div>
        <div className="ro"><span className="ro-l">{t('wz.lev')}</span><span className="ro-v num">{f.params.max_leverage}×</span></div>
        {Number(f.params.ratio ?? 1) !== 1 && <div className="ro"><span className="ro-l">{t('wz.ratio')}</span><span className="ro-v num">{locDec(String(+Number(f.params.ratio).toFixed(2)))}×</span></div>}
        <div className="ro"><span className="ro-l">{t('wz.perInst')}</span><span className="ro-v num">{(Number(f.params.per_inst) * 100).toFixed(0)}%</span></div>
        {f.params.blacklist.length > 0 && <div className="ro"><span className="ro-l">{t('wz.black')}</span><span className="ro-v">{f.params.blacklist.map(baseOf).join(listSep())}</span></div>}
      </div>
      {f.records.length === 0 ? <div className="mk-empty">{t('fw.noRecords')}</div> : (
        <table className="tbl">
          <thead><tr><th>{t('col.time')}</th><th>{t('col.inst')}</th><th>{t('fw.col.target')}</th><th className="fw-left">{t('fw.col.result')}</th><th>{t('fw.col.fee')}</th><th title={t('fw.slipHint')}>{t('fw.col.slip')}</th></tr></thead>
          <tbody>{rows.map(({ item: r, key, state }) => {
            const scaled = r.note === 'scaled_instrument' || r.note === 'scaled_leverage' || Math.abs(Number(r.applied) - Number(r.weight)) > 1e-9 // 跟随比例不是 1 时也写出实际用的
            return (
              <tr key={key} className={state ? `tl-${state}` : ''}>
                <td className="t2">{fmtTime(r.time, { seconds: true })}</td>
                <td><span className="sym">{baseOf(r.instrument)}</span></td>
                <td className="num">{(Number(r.weight) * 100).toFixed(0)}%{scaled && <span className="t3"> → {(Number(r.applied) * 100).toFixed(0)}%</span>}</td>
                <td className="fw-left">{recSentence(t, r, priv)}{r.note && r.note !== 'catch_up' && <span className="t3"> · {noteText(t, r.note)}</span>}{r.note === 'catch_up' && <span className="t3"> · {t('fw.n.catch_up')}</span>}</td>
                <td className="t2"><Money text={Number(r.fee) > 0 ? fmtAmt(r.fee, 4) : '—'} /></td>
                <td className="t2 num">{Number(r.model_avg) > 0 && Number(r.filled) > 0 ? `${Math.abs(Number(r.slip_bps)) < 0.05 ? locDec('0.0') : locDec(Number(r.slip_bps).toFixed(1))} bp` : '—'}</td>
              </tr>
            )
          })}</tbody>
        </table>
      )}
    </div>
  )
}
