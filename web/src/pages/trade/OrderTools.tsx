// 下单工具（D83 第 12 条）：分批挂单、追价、TWAP、冰山单、按成交量比例（18 第 9.5 条）的参数，盈亏比和仓位计算，下单模板，
// 持仓的跟踪止损、分批止盈、一键反手，盘口合并档位，盘口点击交易。
import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { api } from '../../lib/session'
import { errText, fmtTime, useT, type Key } from '../../lib/i18n'
import { baseOf, fmtPrice, numIn } from '../../lib/format'
import { Seg } from '../../ui/Seg'
import { Select } from '../../ui/Select'
import { Dialog } from '../../ui/Dialog'
import { Money } from '../../ui/Money'
import { toast } from '../../lib/privacy'
import { act, rejectText, refreshTrade, toTick, type AccountView, type PosView, type Spec } from '../../lib/trade'
import { locDec } from '../../lib/tz'

// ---------- 算法单 ----------

export type AlgoMode = 'none' | 'scale' | 'chase' | 'twap' | 'iceberg' | 'pov'
export interface AlgoForm { lo: string; hi: string; n: string; skew: 'equal' | 'up' | 'down'; dist: string; minutes: string; limit: string; pieces?: string; pct?: string }
export const ALGO0: AlgoForm = { lo: '', hi: '', n: '5', skew: 'equal', dist: '0.5', minutes: '10', limit: '', pieces: '5', pct: '10' }

const NumF = ({ label, value, onChange, unit, hint, id }: { label: string; value: string; onChange: (v: string) => void; unit?: string; hint?: string; id?: string }) => (
  <label className="op-f bt-f" title={hint}>
    <span className="op-l">{label}</span>
    <span className="op-in"><input inputMode="decimal" data-algo={id} aria-label={label} value={value} onChange={(e) => onChange(numIn(e.target.value))} />{unit && <em>{unit}</em>}</span>
  </label>
)

export function AlgoFields({ mode, f, set }: { mode: AlgoMode; f: AlgoForm; set: (p: Partial<AlgoForm>) => void }) {
  const t = useT()
  if (mode === 'scale') {
    return (
      <div className="op-algo" data-algo-form="scale">
        <div className="bt-two">
          <NumF id="lo" label={t('al.lo')} value={f.lo} onChange={(v) => set({ lo: v })} unit="USDT" />
          <NumF id="hi" label={t('al.hi')} value={f.hi} onChange={(v) => set({ hi: v })} unit="USDT" />
        </div>
        <div className="bt-two">
          <NumF id="n" label={t('al.n')} value={f.n} onChange={(v) => set({ n: v })} hint={t('al.nHint')} />
          <label className="op-f bt-f"><span className="op-l">{t('al.skew')}</span>
            <Select label={t('al.skew')} value={f.skew} onChange={(v) => set({ skew: v })} options={(['equal', 'up', 'down'] as const).map((x) => ({ value: x, label: t(`al.skew.${x}` as Key) }))} />
          </label>
        </div>
      </div>
    )
  }
  if (mode === 'chase') return <div className="op-algo" data-algo-form="chase"><NumF id="dist" label={t('al.dist')} value={f.dist} onChange={(v) => set({ dist: v })} unit="%" hint={t('al.distHint')} /></div>
  if (mode === 'iceberg') return <div className="op-algo" data-algo-form="iceberg"><NumF id="pieces" label={t('al.pieces')} value={f.pieces ?? '5'} onChange={(v) => set({ pieces: v.replace(/\D/g, '') })} unit={t('al.piecesUnit')} hint={t('al.piecesHint')} /></div>
  if (mode === 'pov') {
    return (
      <div className="op-algo" data-algo-form="pov">
        <div className="bt-two">
          <NumF id="pct" label={t('al.pct')} value={f.pct ?? '10'} onChange={(v) => set({ pct: v })} unit="%" hint={t('al.pctHint')} />
          <NumF id="minutes" label={t('al.maxMinutes')} value={f.minutes} onChange={(v) => set({ minutes: v })} unit={t('al.minUnit')} hint={t('al.maxMinutesHint')} />
        </div>
        <NumF id="limit" label={t('al.limit')} value={f.limit} onChange={(v) => set({ limit: v })} unit="USDT" hint={t('al.limitHintPov')} />
      </div>
    )
  }
  if (mode === 'twap') {
    return (
      <div className="op-algo bt-two" data-algo-form="twap">
        <NumF id="minutes" label={t('al.minutes')} value={f.minutes} onChange={(v) => set({ minutes: v })} unit={t('al.minUnit')} hint={t('al.minutesHint')} />
        <NumF id="limit" label={t('al.limit')} value={f.limit} onChange={(v) => set({ limit: v })} unit="USDT" hint={t('al.limitHint')} />
      </div>
    )
  }
  return null
}

/** 下一张算法单 */
export function runAlgo(acct: number, mode: AlgoMode, f: AlgoForm, r: { instrument: string; side: string; qty: string; reduce_only: boolean }, price = '') {
  if (mode === 'iceberg') return act('POST', `/api/algo/${acct}/iceberg`, { ...r, price, count: Number(f.pieces || '5') })
  if (mode === 'pov') return act('POST', `/api/algo/${acct}/pov`, { ...r, pct: f.pct || '10', minutes: Number(f.minutes), limit_px: f.limit || '0' })
  if (mode === 'scale') return act('POST', `/api/algo/${acct}/scale`, { ...r, lower: f.lo, upper: f.hi, count: Number(f.n), skew: f.skew })
  if (mode === 'chase') return act('POST', `/api/algo/${acct}/chase`, { ...r, max_dist: f.dist })
  return act('POST', `/api/algo/${acct}/twap`, { ...r, minutes: Number(f.minutes), limit_px: f.limit || '0' })
}

export interface AlgoView {
  id: number; kind: 'trail' | 'twap' | 'chase' | 'iceberg' | 'pov'; instrument: string; status: string; reason?: string; side: string; qty: string; filled: string
  callback: string; activate: string; best: string; stop: string; live: boolean; slices: number; done: number; next_at?: number; skipped: number; moves: number; created: number
  price?: string; pct?: string; seen?: string; until?: number
}

export function useAlgos(acct: number | null | undefined): AlgoView[] {
  const [list, setList] = useState<AlgoView[]>([])
  useEffect(() => {
    if (!acct) { setList([]); return }
    let off = false
    const load = async () => { try { const r = await api<{ algos: AlgoView[] }>('GET', `/api/algo/${acct}`); if (!off) setList(r.algos) } catch { /* 下次再试 */ } }
    void load()
    const id = window.setInterval(load, 1500)
    return () => { off = true; window.clearInterval(id) }
  }, [acct])
  return list
}

/** 正在跑的算法单：放在当前委托上面 */
export function AlgoList({ acct, algos }: { acct: number; algos: AlgoView[] }) {
  const t = useT()
  const live = algos.filter((a) => a.status === 'running')
  if (!live.length) return null
  return (
    <table className="tbl tl op-algo-list" data-algo-list>
      <tbody>
        {live.map((a) => (
          <tr key={a.id}>
            <td><span className="sym">{baseOf(a.instrument)}</span> <span className="tl-tag">{t(`al.k.${a.kind}` as Key)}</span></td>
            <td className={a.side === 'buy' ? 'up' : 'down'}>{t(a.side === 'buy' ? 'sp.buy' : 'sp.sell')}</td>
            <td className="num">
              {a.kind === 'trail' && (a.live ? t('al.trailLive', { stop: fmtPrice(a.stop), cb: a.callback }) : t('al.trailWait', { px: fmtPrice(a.activate) }))}
              {a.kind === 'twap' && t('al.twapProg', { d: a.done, n: a.slices, q: String(Number(a.filled)), total: String(Number(a.qty)) })}
              {a.kind === 'chase' && t('al.chaseProg', { n: a.moves })}
              {a.kind === 'iceberg' && t('al.iceProg', { d: a.done, n: a.slices, px: fmtPrice(a.price ?? ''), q: String(Number(a.filled)), total: String(Number(a.qty)) })}
              {a.kind === 'pov' && t('al.povProg', { pct: String(Number(a.pct ?? 0)), q: String(Number(a.filled)), total: String(Number(a.qty)), seen: String(Number(a.seen ?? 0)) })}
            </td>
            <td className="t3 num">{fmtTime(a.created)}</td>
            <td className="tl-act"><button className="btn ghost sm" onClick={() => void api('DELETE', `/api/algo/${acct}/${a.id}`).catch((e) => toast(rejectText(e)))}>{t('ls.cancel')}</button></td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

// ---------- 盈亏比和仓位计算 ----------

export function RiskCalc({ entry, tp, sl, equity, side, onQty }: { entry: number; tp: number; sl: number; equity: number; side: 'buy' | 'sell'; onQty: (q: number) => void }) {
  const t = useT()
  const [risk, setRisk] = useState('1')
  const stopDist = sl > 0 ? (side === 'buy' ? entry - sl : sl - entry) : 0
  const gain = tp > 0 ? (side === 'buy' ? tp - entry : entry - tp) : 0
  const rr = stopDist > 0 && gain > 0 ? gain / stopDist : 0
  const riskUsd = equity * (Number(risk) / 100)
  const q = stopDist > 0 ? riskUsd / stopDist : 0
  if (!(entry > 0)) return null
  return (
    <div className="op-calc" data-risk-calc>
      <div><span>{t('al.rr')}</span><b className={`num ${rr >= 1 ? 'up' : rr > 0 ? 'warn-t' : ''}`}>{rr > 0 ? `1 : ${locDec(rr.toFixed(2))}` : '—'}</b></div>
      <div className="op-calc-risk">
        <label>{t('al.riskOf')}<input inputMode="decimal" aria-label={t('al.riskOf')} data-risk-pct value={risk} onChange={(e) => setRisk(numIn(e.target.value))} />%</label>
        <span className="num"><Money text={locDec(riskUsd.toFixed(2))} unit="USDT" /></span>
        <button type="button" className="op-link" disabled={!(q > 0)} data-risk-apply title={t('al.riskHint')} onClick={() => onQty(q)}>{t('al.riskApply')}</button>
      </div>
    </div>
  )
}

// ---------- 下单模板 ----------

export interface Template { name: string; type: 'limit' | 'market' | 'cond'; tif: string; unit: 'coin' | 'usdt'; qty: string; pct: number | null; reduce: boolean; tpPct: string; slPct: string; algo: AlgoMode; f: AlgoForm }
const TPL_KEY = 'qs.templates'
export function loadTemplates(): Template[] {
  try { const v = JSON.parse(localStorage.getItem(TPL_KEY) ?? '[]'); return Array.isArray(v) ? v : [] } catch { return [] }
}
export function saveTemplates(list: Template[]) {
  try { localStorage.setItem(TPL_KEY, JSON.stringify(list)) } catch { /* 忽略 */ }
}

export function TemplateBar({ current, onApply }: { current: () => Omit<Template, 'name'>; onApply: (t: Template) => void }) {
  const t = useT()
  const [list, setList] = useState<Template[]>(loadTemplates)
  const [naming, setNaming] = useState(false)
  const [name, setName] = useState('')
  const [sel, setSel] = useState('')
  const save = (e: FormEvent) => {
    e.preventDefault()
    e.stopPropagation() // 这个对话框在下单表单里面：不让"保存"冒泡成下单
    const n = name.trim()
    if (!n) return
    const next = [...list.filter((x) => x.name !== n), { ...current(), name: n }].slice(-20)
    setList(next); saveTemplates(next); setNaming(false); setName(''); setSel(n)
    toast(t('al.tplSaved'))
  }
  return (
    <div className="op-tpl" data-tpl>
      {list.length > 0 ? (
        <Select className="op-tpl-sel" label={t('al.tpl')} value={sel} onChange={(v) => { setSel(v); const x = list.find((y) => y.name === v); if (x) onApply(x) }}
          options={[{ value: '', label: t('al.tpl') }, ...list.map((x) => ({ value: x.name, label: x.name }))]} />
      ) : <span className="t3">{t('al.tpl')}</span>}
      <button type="button" className="op-link" data-tpl-save onClick={() => setNaming(true)}>{t('al.tplSave')}</button>
      {sel && <button type="button" className="op-link" onClick={() => { const next = list.filter((x) => x.name !== sel); setList(next); saveTemplates(next); setSel('') }}>{t('al.tplDel')}</button>}
      <Dialog open={naming} title={t('al.tplSave')} onClose={() => setNaming(false)}>
        <form className="au-form" onSubmit={save} noValidate>
          <label className="op-f"><span className="op-l">{t('al.tplName')}</span><span className="op-in"><input data-tpl-name autoFocus maxLength={20} value={name} onChange={(e) => setName(e.target.value)} aria-label={t('al.tplName')} /></span></label>
          <p className="dlg-text op-small">{t('al.tplHint')}</p>
          <div className="dlg-btns">
            <button type="button" className="btn ghost" onClick={() => setNaming(false)}>{t('common.cancel')}</button>
            <button className="btn primary" disabled={!name.trim()}>{t('common.save')}</button>
          </div>
        </form>
      </Dialog>
    </div>
  )
}

// ---------- 持仓：跟踪止损、分批止盈、反手 ----------

export function ExitDialog({ pos, acct, spec, onClose }: { pos: PosView; acct: number; spec?: Spec; onClose: () => void }) {
  const t = useT()
  const [tab, setTab] = useState<'trail' | 'levels' | 'reverse'>('trail')
  const [cb, setCb] = useState('1')
  const [act0, setAct0] = useState('')
  const [rows, setRows] = useState([{ px: '', pct: '50' }, { px: '', pct: '30' }, { px: '', pct: '' }])
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const long = pos.side === 'long'
  const mark = Number(pos.mark)
  const stop = mark * (long ? 1 - Number(cb) / 100 : 1 + Number(cb) / 100)
  const levels = rows.filter((r) => Number(r.px) > 0 && Number(r.pct) > 0)
  const totalPct = levels.reduce((s, r) => s + Number(r.pct), 0)
  const go = async (fn: () => Promise<unknown>, done: string) => {
    setBusy(true); setErr('')
    try { await fn(); refreshTrade(); toast(done); onClose() } catch (e) { setErr(rejectText(e)) } finally { setBusy(false) }
  }
  return (
    <Dialog open title={t('al.exitTitle', { inst: baseOf(pos.instrument) })} onClose={onClose}>
      <div className="au-form" data-exit-dialog>
        <Seg label={t('al.exitTitle', { inst: '' })} value={tab} onChange={setTab} options={[{ value: 'trail', label: t('al.k.trail') }, { value: 'levels', label: t('al.levels') }, { value: 'reverse', label: t('al.reverse') }]} />
        {tab === 'trail' && (
          <>
            <p className="dlg-text op-small">{t('al.trailText')}</p>
            <div className="bt-two">
              <NumF id="cb" label={t('al.cb')} value={cb} onChange={setCb} unit="%" />
              <NumF id="act" label={t('al.activate')} value={act0} onChange={setAct0} unit="USDT" hint={t('al.activateHint')} />
            </div>
            <p className="dlg-text op-small" data-trail-say>{t('al.trailSay', { px: spec ? fmtPrice(toTick(stop, spec.tick)) : fmtPrice(String(stop)), cb })}</p>
            <div className="dlg-btns">
              <button type="button" className="btn ghost" onClick={onClose}>{t('common.cancel')}</button>
              <button type="button" className="btn primary" disabled={busy} data-trail-go onClick={() => void go(() => act('POST', `/api/algo/${acct}/trail`, { instrument: pos.instrument, callback: cb, activate: act0 || '0' }), t('al.trailStarted'))}>{t('al.start')}</button>
            </div>
          </>
        )}
        {tab === 'levels' && (
          <>
            <p className="dlg-text op-small">{t('al.levelsText', { q: pos.qty })}</p>
            {rows.map((r, i) => (
              <div className="bt-two" key={i}>
                <NumF id={`lv-px-${i}`} label={t('al.lvPx', { n: i + 1 })} value={r.px} onChange={(v) => setRows(rows.map((x, j) => (j === i ? { ...x, px: v } : x)))} unit="USDT" />
                <NumF id={`lv-pct-${i}`} label={t('al.lvPct')} value={r.pct} onChange={(v) => setRows(rows.map((x, j) => (j === i ? { ...x, pct: v } : x)))} unit="%" />
              </div>
            ))}
            {totalPct > 100 && <p className="op-err">{errText('ALGO_LEVELS_OVER')}</p>}
            <div className="dlg-btns">
              <button type="button" className="btn ghost" onClick={onClose}>{t('common.cancel')}</button>
              <button type="button" className="btn primary" disabled={busy || !levels.length || totalPct > 100} data-levels-go onClick={() => void go(() => act('POST', `/api/algo/${acct}/exits`, { instrument: pos.instrument, levels: levels.map((r) => ({ px: r.px, pct: r.pct })) }), t('al.levelsDone'))}>{t('al.start')}</button>
            </div>
          </>
        )}
        {tab === 'reverse' && (
          <>
            <p className="dlg-text">{t(long ? 'al.reverseToShort' : 'al.reverseToLong', { q: pos.qty, base: baseOf(pos.instrument) })}</p>
            <div className="dlg-btns">
              <button type="button" className="btn ghost" onClick={onClose}>{t('common.cancel')}</button>
              <button type="button" className="btn primary" disabled={busy} data-reverse-go onClick={() => void go(() => act('POST', `/api/algo/${acct}/reverse`, { instrument: pos.instrument }), t('al.reversed'))}>{t('al.reverseDo')}</button>
            </div>
          </>
        )}
        {err && <p className="op-err">{err}</p>}
      </div>
    </Dialog>
  )
}

// ---------- 盘口合并档位 ----------

/** 合并档位的选项：最小价位的 1、10、100、1000 倍，太细的（比价格的万分之一还小）不要 */
export function groupOptions(tick: number, price: number): number[] {
  const out = [tick]
  for (const m of [10, 50, 100, 500, 1000]) {
    const g = tick * m
    if (g <= price * 0.005) out.push(Number(g.toFixed(10)))
  }
  return out
}

export function groupBook(levels: [string, string][], g: number, up: boolean): [string, string][] {
  if (!(g > 0)) return levels
  const map = new Map<number, number>()
  for (const [p, q] of levels) {
    const k = (up ? Math.ceil(Number(p) / g - 1e-9) : Math.floor(Number(p) / g + 1e-9)) * g
    map.set(k, (map.get(k) ?? 0) + Number(q))
  }
  const keys = [...map.keys()].sort((a, b) => (up ? a - b : b - a))
  return keys.map((k) => [String(Number(k.toFixed(10))), String(map.get(k))])
}

// ---------- 盘口点击交易 ----------

export function DomLadder({ book, view, inst, spec, dec }: { book: { asks: [string, string][]; bids: [string, string][] } | null; view: AccountView | null; inst: string; spec?: Spec; dec: number }) {
  const t = useT()
  const [qty, setQty] = useState('')
  const [armed, setArmed] = useState(false)
  const mine = useMemo(() => {
    const m = new Map<number, { id: string; qty: string; side: string }[]>()
    for (const o of view?.orders ?? []) {
      if (o.instrument !== inst || o.type !== 'limit') continue
      const k = Number(o.price)
      m.set(k, [...(m.get(k) ?? []), { id: o.id, qty: o.qty, side: o.side }])
    }
    return m
  }, [view, inst])
  if (!book) return <div className="mk-empty">{t('ob.waiting')}</div>
  if (!view) return <div className="mk-empty">{t('ls.login')}</div>
  const asks = book.asks.slice(0, 12).reverse()
  const bids = book.bids.slice(0, 12)
  const place = async (side: 'buy' | 'sell', px: string) => {
    if (!armed) { toast(t('al.domOff')); return }
    if (!(Number(qty) > 0)) { toast(errText('QTY_REQUIRED')); return }
    try {
      await act('POST', `/api/trade/${view.id}/orders`, { instrument: inst, side, type: 'limit', tif: 'GTC', price: spec ? toTick(px, spec.tick) : px, qty })
      toast(t('al.domPlaced', { side: t(side === 'buy' ? 'sp.buy' : 'sp.sell'), q: qty, px: fmtPrice(px, dec) }))
    } catch (e) { toast(rejectText(e)) }
  }
  const row = (p: string, q: string, isAsk: boolean) => {
    const px = Number(p)
    const own = mine.get(px)
    return (
      <div className={`dom-r ${isAsk ? 'ask' : 'bid'}`} key={(isAsk ? 'a' : 'b') + p} data-dom-row={p}>
        <button type="button" className="dom-buy" data-dom-buy={p} onClick={() => void place('buy', p)}>{!isAsk ? locDec(Number(q).toFixed(3)) : ''}</button>
        <span className="dom-px num">{fmtPrice(p, dec)}{own && <button type="button" className="dom-own" title={t('ls.cancel')} data-dom-own onClick={() => own.forEach((o) => void act('DELETE', `/api/trade/${view.id}/orders/${o.id}`).catch(() => undefined))}>{own.reduce((s, o) => s + Number(o.qty), 0)} ×</button>}</span>
        <button type="button" className="dom-sell" data-dom-sell={p} onClick={() => void place('sell', p)}>{isAsk ? locDec(Number(q).toFixed(3)) : ''}</button>
      </div>
    )
  }
  return (
    <div className="dom" data-dom>
      <div className="dom-h">
        <label className="op-in dom-qty"><input inputMode="decimal" data-dom-qty aria-label={t('tr.qty')} placeholder={t('tr.qty')} value={qty} onChange={(e) => setQty(numIn(e.target.value))} /><em>{baseOf(inst)}</em></label>
        <button type="button" className={`op-chip ${armed ? 'dom-on' : ''}`} aria-pressed={armed} data-dom-arm title={t('al.domHint')} onClick={() => setArmed(!armed)}>{t(armed ? 'al.domArmed' : 'al.domSafe')}</button>
      </div>
      <div className="dom-cols"><span>{t('sp.buy')}</span><span>{t('ob.price')}</span><span>{t('sp.sell')}</span></div>
      {asks.map(([p, q]) => row(p, q, true))}
      <div className="dom-mid" />
      {bids.map(([p, q]) => row(p, q, false))}
    </div>
  )
}
