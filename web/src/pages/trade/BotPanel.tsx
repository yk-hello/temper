// 机器人的参数面板和列表（D83 第 10 条）：面板是主体，每个值都能键入；图和面板实时同步，不碰图也能设完。
// 旁边只写算好的数字：扣费后每格净赚、占用资金、最多亏多少、强平价。
import { useEffect, useMemo, useState } from 'react'
import { api, type PaperAccount } from '../../lib/session'
import { acctName, errText, fmtTime, latinWord, tr, useT, type Key, colon } from '../../lib/i18n'
import { baseOf, fmtPrice, numIn } from '../../lib/format'
import { Seg } from '../../ui/Seg'
import { Dialog } from '../../ui/Dialog'
import { Money } from '../../ui/Money'
import { toast, usePrivate } from '../../lib/privacy'
import { rejectText } from '../../lib/trade'
import { cleanParams, refreshBots, usePlan, type Bot, type BotKind, type BotParams, type BotPlan } from '../../lib/bots'
import { locDec, numLocale } from '../../lib/tz'

const num = (v: string | number | undefined, d = 2) => Number(v ?? 0).toLocaleString(numLocale(), { minimumFractionDigits: d, maximumFractionDigits: d })
const signed = (v: string | number | undefined, d = 2) => { const n = Number(v ?? 0); return `${n > 0 ? '+' : n < 0 ? '−' : ''}${num(Math.abs(n), d)}` }
const tone = (v: string | number | undefined) => { const n = Number(v ?? 0); return n > 0 ? 'up' : n < 0 ? 'down' : '' }

/** 表单的初值：区间默认现价上下 5%，20 格 */
export function draftOf(kind: BotKind, inst: string, last: number, dec: number): BotParams {
  const f = (x: number) => x.toFixed(dec)
  const p: BotParams = { instrument: inst }
  if (kind === 'spot_grid' || kind === 'perp_grid') {
    Object.assign(p, { lower: f(last * 0.95), upper: f(last * 1.05), grids: 20, geo: false, invest: '1000', stop_loss: '', take_profit: '' })
    if (kind === 'perp_grid') Object.assign(p, { dir: 'long', leverage: 3, stop_loss: f(last * 0.92) })
  } else if (kind === 'dca' || kind === 'dip') {
    Object.assign(p, { side: 'long', first: '100', add: '100', step_pct: '2', mult: '1.5', max_adds: 5, tp_pct: '1.5', sl_pct: '', repeat: false })
    if (kind === 'dip') Object.assign(p, { trigger_px: f(last * 0.97), drop_pct: '' })
  } else {
    Object.assign(p, { amount: '100', every: 'day', times: 0 })
  }
  return p
}

function Field({ label, value, onChange, unit, hint, testId }: { label: string; value: string | undefined; onChange: (v: string) => void; unit?: string; hint?: string; testId?: string }) {
  return (
    <label className="op-f bt-f" title={hint}>
      <span className="op-l">{label}</span>
      <span className="op-in"><input inputMode="decimal" value={value ?? ''} data-bot-field={testId} onChange={(e) => onChange(numIn(e.target.value))} aria-label={label} />{unit && <em>{unit}</em>}</span>
    </label>
  )
}

function Row({ k, v, cls, hint }: { k: string; v: React.ReactNode; cls?: string; hint?: string }) {
  return <div title={hint}><dt>{k}</dt><dd className={`num ${cls ?? ''}`}>{v}</dd></div>
}

export function BotPanel({ busy: coinBusy, hasPos = false, inst, last, dec, spot, current, form, setForm, kind, setKind, onPlan }: {
  busy: boolean; hasPos?: boolean; inst: string; last: number; dec: number; spot: boolean; current: PaperAccount | null
  form: BotParams | null; setForm: (p: BotParams | null) => void; kind: BotKind; setKind: (k: BotKind) => void; onPlan: (p: BotPlan | null) => void
}) {
  const t = useT()
  const [confirm, setConfirm] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const kinds: BotKind[] = spot ? ['spot_grid', 'dca', 'dip', 'plan'] : ['perp_grid', 'dca', 'dip']
  // 切币或切种类：重新给初值（现价还没到时等一下）
  useEffect(() => { if (!kinds.includes(kind)) setKind(kinds[0]) }, [spot]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { if (last > 0 && kinds.includes(kind) && (!form || form.instrument !== inst)) setForm(draftOf(kind, inst, last, dec)) }, [inst, kind, last > 0]) // eslint-disable-line react-hooks/exhaustive-deps
  const up = (patch: Partial<BotParams>) => { if (form) setForm({ ...form, ...patch }) }
  const pick = (k: BotKind) => { setKind(k); setErr(''); setForm(last > 0 ? draftOf(k, inst, last, dec) : null) }
  const params = useMemo(() => (form ? cleanParams(form) : null), [form])
  const { plan, err: planErr } = usePlan(form && kinds.includes(kind) ? kind : null, params)
  useEffect(() => { onPlan(plan) }, [plan]) // eslint-disable-line react-hooks/exhaustive-deps
  const start = async () => {
    if (!current || !params) return
    setBusy(true); setErr('')
    try {
      await api('POST', '/api/bots', { kind, account: current.id, params, confirm: true })
      refreshBots()
      setConfirm(false)
      toast(t('bt.started'))
    } catch (e) { setErr(rejectText(e)); setConfirm(false) } finally { setBusy(false) }
  }
  if (!current) return <div className="op-login"><p>{t('ls.login')}</p></div>
  const grid = kind === 'spot_grid' || kind === 'perp_grid'
  const dca = kind === 'dca' || kind === 'dip'
  const base = baseOf(inst)
  return (
    <div className="op bt" data-bot-panel>
      <div className="op-acct">
        <span className="op-acct-name" title={t('tr.switchAccount')}>{acctName(current.name)}</span>
      </div>
      <div className="bt-kinds" role="group" aria-label={t('bt.kind')}>
        {kinds.map((k) => <button key={k} type="button" aria-pressed={k === kind} data-bot-kind={k} onClick={() => pick(k)}>{t(`bt.k.${k}` as Key)}</button>)}
      </div>
      {!form && <p className="bt-note">{t('common.loading')}</p>}
      {form && grid && (
        <>
          <div className="bt-two">
            <Field label={t('bt.lower')} value={form.lower} onChange={(v) => up({ lower: v })} unit="USDT" testId="lower" />
            <Field label={t('bt.upper')} value={form.upper} onChange={(v) => up({ upper: v })} unit="USDT" testId="upper" />
          </div>
          <div className="bt-two">
            <Field label={t('bt.grids')} value={String(form.grids ?? '')} onChange={(v) => up({ grids: Number(v) || 0 })} testId="grids" hint={t('bt.gridsHint')} />
            <label className="op-f bt-f"><span className="op-l">{t('bt.mode')}</span>
              <Seg label={t('bt.mode')} value={form.geo ? 'geo' : 'arith'} onChange={(v) => up({ geo: v === 'geo' })} options={[{ value: 'arith', label: t('bt.arith') }, { value: 'geo', label: t('bt.geo') }]} />
            </label>
          </div>
          {kind === 'perp_grid' && (
            <div className="bt-two">
              <label className="op-f bt-f"><span className="op-l">{t('bt.dir')}</span>
                <Seg label={t('bt.dir')} value={form.dir ?? 'long'} onChange={(v) => up({ dir: v })} options={[{ value: 'long', label: t('bt.long') }, { value: 'short', label: t('bt.short') }, { value: 'neutral', label: t('bt.neutral') }]} />
              </label>
              <Field label={t('bt.lev')} value={String(form.leverage ?? '')} onChange={(v) => up({ leverage: Number(v) || 0 })} unit="×" testId="leverage" />
            </div>
          )}
          <Field label={t('bt.invest')} value={form.invest} onChange={(v) => up({ invest: v })} unit="USDT" testId="invest" />
          <div className="bt-two">
            <Field label={t('bt.sl')} value={form.stop_loss} onChange={(v) => up({ stop_loss: v })} unit="USDT" testId="stop_loss" hint={t('bt.slHint')} />
            <Field label={t('bt.tp')} value={form.take_profit} onChange={(v) => up({ take_profit: v })} unit="USDT" testId="take_profit" />
          </div>
        </>
      )}
      {form && dca && (
        <>
          {kind === 'dip' && (
            <div className="bt-two">
              <Field label={t('bt.trigger')} value={form.trigger_px} onChange={(v) => up({ trigger_px: v })} unit="USDT" testId="trigger_px" hint={t('bt.triggerHint')} />
              <Field label={t('bt.drop')} value={form.drop_pct} onChange={(v) => up({ drop_pct: v })} unit="%" testId="drop_pct" hint={t('bt.dropHint')} />
            </div>
          )}
          {!spot && (
            <label className="op-f bt-f"><span className="op-l">{t('bt.dir')}</span>
              <Seg label={t('bt.dir')} value={form.side ?? 'long'} onChange={(v) => up({ side: v })} options={[{ value: 'long', label: t('bt.long') }, { value: 'short', label: t('bt.short') }]} />
            </label>
          )}
          <div className="bt-two">
            <Field label={t('bt.first')} value={form.first} onChange={(v) => up({ first: v })} unit="USDT" testId="first" />
            <Field label={t('bt.add')} value={form.add} onChange={(v) => up({ add: v })} unit="USDT" testId="add" />
          </div>
          <div className="bt-two">
            <Field label={t('bt.step')} value={form.step_pct} onChange={(v) => up({ step_pct: v })} unit="%" testId="step_pct" hint={t('bt.stepHint')} />
            <Field label={t('bt.mult')} value={form.mult} onChange={(v) => up({ mult: v })} unit="×" testId="mult" hint={t('bt.multHint')} />
          </div>
          <div className="bt-two">
            <Field label={t('bt.adds')} value={String(form.max_adds ?? '')} onChange={(v) => up({ max_adds: Number(v) || 0 })} testId="max_adds" />
            <Field label={t('bt.tpPct')} value={form.tp_pct} onChange={(v) => up({ tp_pct: v })} unit="%" testId="tp_pct" />
          </div>
          <div className="bt-two">
            <Field label={t('bt.slPct')} value={form.sl_pct} onChange={(v) => up({ sl_pct: v })} unit="%" testId="sl_pct" />
            {kind === 'dca' && <label className="op-check bt-repeat"><input type="checkbox" checked={!!form.repeat} onChange={(e) => up({ repeat: e.target.checked })} /><span>{t('bt.repeat')}</span></label>}
          </div>
        </>
      )}
      {form && kind === 'plan' && (
        <>
          <Field label={t('bt.amount')} value={form.amount} onChange={(v) => up({ amount: v })} unit="USDT" testId="amount" />
          <label className="op-f bt-f"><span className="op-l">{t('bt.every')}</span>
            <Seg label={t('bt.every')} value={form.every ?? 'day'} onChange={(v) => up({ every: v })} options={(['hour', 'day', 'week', 'month'] as const).map((x) => ({ value: x, label: t(`bt.e.${x}` as Key) }))} />
          </label>
          <Field label={t('bt.times')} value={form.times ? String(form.times) : ''} onChange={(v) => up({ times: Number(v) || 0 })} testId="times" hint={t('bt.timesHint')} />
        </>
      )}
      <PlanBox kind={kind} plan={plan} base={base} dec={dec} last={last} />
      {planErr && <p className="op-err" data-bot-err>{errText(planErr.code, planErr.params)}</p>}
      {plan?.warn.map((w) => <p className="bt-warn" key={w} data-bot-warn={w}>{t(`bt.w.${w}` as Key)}</p>)}
      {err && <p className="op-err">{err}</p>}
      {coinBusy && <p className="bt-warn">{t('err.BOT_COIN_BUSY')}</p>}
      {/* 这个币上有手动仓位：合约机器人开不了，先说清楚、按钮变灰，不要等点了确认才报错（11-17 走查） */}
      {!coinBusy && hasPos && <p className="bt-warn" data-bot-haspos>{t('err.BOT_COIN_HAS_POSITION')}</p>}
      <button type="button" className="op-submit buy" disabled={!plan || busy || coinBusy || hasPos} data-bot-start onClick={() => setConfirm(true)}>{t('bt.start', { kind: latinWord(t(`bt.k.${kind}` as Key)).trimEnd() })}</button>
      <Dialog open={confirm} title={t('bt.start', { kind: latinWord(t(`bt.k.${kind}` as Key)).trimEnd() })} onClose={() => setConfirm(false)}>
        <div className="au-form">
          <p className="dlg-text">{t('bt.confirm', { inst: base, need: num(plan?.need) })}</p>
          <PlanBox kind={kind} plan={plan} base={base} dec={dec} last={last} />
          <p className="dlg-text op-small">{t('bt.confirmNote')}</p>
          <div className="dlg-btns">
            <button type="button" className="btn ghost" onClick={() => setConfirm(false)}>{t('common.cancel')}</button>
            <button type="button" className="btn primary" disabled={busy} data-bot-confirm onClick={() => void start()}>{t('bt.startDo')}</button>
          </div>
        </div>
      </Dialog>
    </div>
  )
}

function PlanBox({ kind, plan, base, dec, last }: { kind: BotKind; plan: BotPlan | null; base: string; dec: number; last: number }) {
  const t = useT()
  if (!plan) return null
  if (kind === 'spot_grid' || kind === 'perp_grid') {
    return (
      <dl className="op-pv bt-plan" data-bot-plan>
        <Row k={t('bt.p.net')} hint={t('bt.p.netHint')} cls={Number(plan.net_lo) > 0 ? 'up' : 'down'} v={<Money text={`${num(plan.net_lo, 3)} ~ ${num(plan.net_hi, 3)}`} unit="USDT" />} />
        <Row k={t('bt.p.netPct')} cls={Number(plan.net_lo) > 0 ? 'up' : 'down'} v={`${num(plan.net_lo_pct, 3)}% ~ ${num(plan.net_hi_pct, 3)}%`} />
        <Row k={t('bt.p.grids')} v={t('bt.p.gridsV', { b: plan.below, a: plan.above, q: String(Number(plan.qty ?? 0)), base })} />
        {plan.init_side && <Row k={t('bt.p.init')} hint={t('bt.p.initHint')} v={`${t(plan.init_side === 'buy' ? 'sp.buy' : 'sp.sell')} ${Number(plan.init_qty)} ${base}`} />}
        <Row k={t('bt.p.need')} v={<Money text={num(plan.need)} unit="USDT" />} />
        <Row k={t('bt.p.worst')} hint={t('bt.p.worstHint')} cls="down" v={<Money text={num(plan.worst_loss)} unit="USDT" />} />
        {Number(plan.liq_px) > 0 && <Row k={t('bt.p.liq')} cls="warn-t" v={fmtPrice(plan.liq_px ?? '0', dec)} />}
      </dl>
    )
  }
  if (kind === 'dca' || kind === 'dip') {
    return (
      <div className="bt-plan" data-bot-plan>
        <table className="bt-ladder">
          <thead><tr><th>{t('bt.l.px')}</th><th>{t('bt.l.usd')}</th><th>{t('bt.l.avg')}</th><th>{t('bt.l.tp')}</th></tr></thead>
          <tbody>
            {(plan.ladder ?? []).map((r) => (
              <tr key={r.px}>
                <td className="num">{fmtPrice(r.px, dec)} <i>{last > 0 ? `${Number(r.down_pc) > 0 ? '+' : '−'}${locDec(Math.abs(Number(r.down_pc)).toFixed(1))}%` : ''}</i></td>
                <td className="num">{num(r.usd, 0)}</td>
                <td className="num">{fmtPrice(r.avg, dec)}</td>
                <td className="num up">{fmtPrice(r.tp, dec)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        <dl className="op-pv">
          <Row k={t('bt.p.need')} v={<Money text={num(plan.need)} unit="USDT" />} />
          {Number(plan.worst_loss) > 0 && <Row k={t('bt.p.worstSl')} cls="down" v={<Money text={num(plan.worst_loss)} unit="USDT" />} />}
        </dl>
      </div>
    )
  }
  return null
}

// ---------- 列表 ----------

const dur = (ms: number) => {
  const m = Math.floor(ms / 60000)
  if (m < 60) return tr('bt.dur.m', { m })
  const h = Math.floor(m / 60)
  if (h < 48) return tr('bt.dur.h', { h, m: m % 60 })
  return tr('bt.dur.d', { d: Math.floor(h / 24), h: h % 24 })
}

export function statusText(b: Bot): string {
  if (b.status === 'running' && b.range && b.range !== 'inside') return tr(`bt.s.${b.range}` as Key)
  const s = tr(`bt.s.${b.status}` as Key)
  if (!b.reason) return s
  const rk = `bt.r.${b.reason}` as Key
  const why = tr(rk)
  return `${s}${colon()}${why === rk ? errText(b.reason) : why}`
}

export function BotList({ bots, onPick }: { bots: Bot[]; onPick: (inst: string) => void }) {
  const t = useT()
  const priv = usePrivate()
  const [stop, setStop] = useState<Bot | null>(null)
  const [open, setOpen] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)
  const m = (s: string) => (priv ? '••••' : s)
  if (!bots.length) return <div className="mk-empty bt-empty"><p>{t('bt.none')}</p><p className="t3">{t('bt.noneHint')}</p></div>
  const act = async (b: Bot, what: 'pause' | 'resume') => {
    try {
      await api('POST', `/api/bots/${b.id}/${what}`, {}); refreshBots()
      // 暂停、恢复后说一声（11-17 走查：点了没有任何反馈，只有状态那一格变了）
      toast(t(what === 'pause' ? 'bt.pausedToast' : 'bt.resumedToast', { coin: baseOf(b.params.instrument), kind: latinWord(t(`bt.k.${b.kind}` as Key)).trimStart() }))
    } catch (e) { toast(rejectText(e)) }
  }
  const doStop = async (mode: 'close' | 'keep') => {
    if (!stop) return
    setBusy(true)
    const perp = stop.params.instrument.endsWith('-PERP')
    try {
      await api('POST', `/api/bots/${stop.id}/stop`, { mode }); refreshBots(); setStop(null)
      // 停掉以后说清楚结果（11-16 走查：原来对话框一关什么都不说）
      toast(t(mode === 'keep' ? 'bt.stoppedKeep' : perp ? 'bt.stoppedClosePerp' : 'bt.stoppedClose'))
    } catch (e) { toast(rejectText(e)) } finally { setBusy(false) }
  }
  return (
    <>
      <table className="tbl tl bt-list" data-bot-list>
        <thead><tr><th>{t('col.inst')}</th><th>{t('bt.c.kind')}</th><th>{t('bt.c.status')}</th><th className="r">{t('bt.c.invest')}</th><th className="r">{t('bt.c.realized')}</th><th className="r">{t('bt.c.unreal')}</th><th className="r">{t('bt.c.fills')}</th><th className="r">{t('bt.c.time')}</th><th /></tr></thead>
        <tbody>
          {bots.map((b) => {
            const inv = b.kind === 'plan' ? b.spent : b.plan?.need
            const end = b.stopped || b.last_ok || Date.now()
            return (
              <>
                <tr key={b.id} data-bot-row={b.id} className={b.status === 'stopped' ? 'bt-off' : ''} onClick={() => setOpen(open === b.id ? null : b.id)}>
                  <td><button type="button" className="tl-sym" onClick={(e) => { e.stopPropagation(); onPick(b.params.instrument) }}><span className="sym">{baseOf(b.params.instrument)}</span></button><span className="tl-tag">{t(b.params.instrument.endsWith('-SPOT') ? 'sp.spot' : 'sp.perp')}</span></td>
                  <td>{t(`bt.k.${b.kind}` as Key)}</td>
                  <td className={b.status === 'paused' ? 'warn-t' : b.status === 'stopped' ? 't3' : ''}>{statusText(b)}</td>
                  <td className="r num">{m(num(inv))}</td>
                  <td className={`r num ${tone(b.realized)}`}>{m(signed(b.realized))}</td>
                  <td className={`r num ${tone(b.unreal)}`}>{b.status === 'stopped' ? '—' : m(signed(b.unreal))}</td>
                  <td className="r num">{b.kind === 'spot_grid' || b.kind === 'perp_grid' ? t('bt.pairs', { n: b.pairs }) : b.fills}</td>
                  <td className="r num">{dur(end - b.created)}</td>
                  <td className="r" onClick={(e) => e.stopPropagation()}>
                    {(b.status === 'running' || b.status === 'waiting') && <button className="btn ghost sm" onClick={() => void act(b, 'pause')}>{t('bt.pause')}</button>}
                    {b.status === 'paused' && <button className="btn ghost sm" data-bot-resume onClick={() => void act(b, 'resume')}>{t('bt.resume')}</button>}
                    {b.status !== 'stopped' && <button className="btn ghost sm" data-bot-stop onClick={() => setStop(b)}>{t('bt.stop')}</button>}
                  </td>
                </tr>
                {open === b.id && (
                  <tr key={`${b.id}-d`} className="bt-detail"><td colSpan={9}>
                    <ul className="bt-recs">
                      {[...b.recs].reverse().slice(0, 30).map((r, i) => (
                        <li key={i}>
                          <span className="num t3">{fmtTime(r.t, { seconds: true })}</span>
                          <span>{recText(r)}</span>
                          {r.side && <span className={r.side === 'buy' ? 'up' : 'down'}>{t(r.side === 'buy' ? 'sp.buy' : 'sp.sell')}</span>}
                          {Number(r.px) > 0 && <span className="num">{fmtPrice(r.px)}</span>}
                          {Number(r.qty) > 0 && <span className="num">{m(String(Number(r.qty)))}</span>}
                          {Number(r.net) !== 0 && <span className={`num ${tone(r.net)}`}>{m(signed(r.net, 3))}</span>}
                        </li>
                      ))}
                    </ul>
                  </td></tr>
                )}
              </>
            )
          })}
        </tbody>
      </table>
      <Dialog open={!!stop} title={t('bt.stopTitle')} onClose={() => setStop(null)}>
        <div className="au-form">
          {/* 合约机器人手里是仓位（做空网格也有），不是"货"：说"平掉"，不说"卖出" */}
          <p className="dlg-text">{t(stop?.params.instrument.endsWith('-PERP') ? 'bt.stopTextPerp' : 'bt.stopText')}</p>
          <div className="bt-stop-opts">
            <button type="button" className="btn secondary" disabled={busy} data-bot-stop-mode="close" onClick={() => void doStop('close')}>{t(stop?.params.instrument.endsWith('-PERP') ? 'bt.stopClosePerp' : 'bt.stopClose')}</button>
            <button type="button" className="btn ghost" disabled={busy} data-bot-stop-mode="keep" onClick={() => void doStop('keep')}>{t('bt.stopKeep')}</button>
          </div>
        </div>
      </Dialog>
    </>
  )
}

function recText(r: { kind: string; note?: string }): string {
  if (r.kind === 'down') return tr('bt.rec.down', { m: r.note ?? '' })
  if (r.kind === 'pause' || r.kind === 'stop') {
    const k = `bt.r.${r.note}` as Key
    const why = r.note ? tr(k) : ''
    return `${tr(`bt.rec.${r.kind}` as Key)}${why && why !== k ? `${colon()}${why}` : ''}`
  }
  if (r.kind === 'fill' && r.note === 'init') return tr('bt.rec.init')
  return tr(`bt.rec.${r.kind}` as Key)
}
