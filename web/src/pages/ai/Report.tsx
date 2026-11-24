// 验证报告（D96 第 2 条，18 第 5.5 条）：只摆数字和门槛，门槛由看报告的人自己定（存在这台浏览器里）。
// 胜率不在门槛里：低胜率高盈亏比的策略也可以很好（docs/strategy-guide.md 第四节）。
import { LIMITS } from '../../lib/limits'
import { useState } from 'react'
import { useT, fmtTime, errText, type Key } from '../../lib/i18n'
import { usePoll } from '../../lib/follow'
import { api, ApiError } from '../../lib/session'
import { locDec } from '../../lib/tz'

interface Bucket { label: 'lo' | 'mid' | 'hi'; n: number; avg: number; hit: number }
interface Report {
  strategy_id: string; since: number; days: number; trades: number; return: number
  first_half: number | null; second_half: number | null; sharpe: number | null; max_drawdown: number; fees: number
  cost_plus50: number; inference_usd: number; return_after_inference: number; best_share: number | null
  hold_btc: number | null; momentum: number | null
  random: { trials: number; strategy_mean: number; median: number; p90: number; beat: number } | null
  calibration: Bucket[]; latency_p50_ms: number | null; latency_p90_ms: number | null; signals: number
  versions: { version: string; from: number; signals: number }[]; notes: string[]
}

/** 门槛：默认值和 docs/strategy-guide.md 第四节一样 */
interface Th { days: number; trades: number; sharpe: number; dd: number; best: number; beat: number }
const DEF: Th = { days: 30, trades: 100, sharpe: 1, dd: 0.2, best: 0.25, beat: 0.9 }
const TH_KEY = 'qs.verifyTh'
const loadTh = (): Th => { try { return { ...DEF, ...JSON.parse(localStorage.getItem(TH_KEY) ?? '{}') } } catch { return DEF } }

const pct = (v: number | null | undefined, d = 1) => {
  if (v == null) return '—'
  const s = (v * 100).toFixed(d)
  if (Number(s) === 0) return `${locDec((0).toFixed(d))}%` // 不写 -0.0%
  return `${v > 0 ? '+' : ''}${locDec(s)}%`
}
type Verdict = 'ok' | 'no' | 'na'

export function VerifyReport({ strategy }: { strategy: string }) {
  const t = useT()
  const { data: r, status } = usePoll<Report>(`/api/strategies/${encodeURIComponent(strategy)}/report`, 60000)
  const [th, setTh] = useState<Th>(loadTh)
  const [edit, setEdit] = useState(false)
  const save = (v: Th) => { setTh(v); try { localStorage.setItem(TH_KEY, JSON.stringify(v)) } catch { /* 隐私模式 */ } }
  if (!r && status === 401) return <div className="mk-empty">{t('vr.login')}</div>
  if (!r && status >= 400) return <div className="mk-empty">{t('vr.err')}</div>
  if (!r) return <div className="mk-empty">{t('common.loading')}</div>
  const lo = r.calibration.find((b) => b.label === 'lo'), hi = r.calibration.find((b) => b.label === 'hi')
  const calOk: Verdict = !lo || !hi || lo.n < 10 || hi.n < 10 ? 'na' : hi.avg > lo.avg ? 'ok' : 'no'
  const v = (cond: boolean | null): Verdict => (cond == null ? 'na' : cond ? 'ok' : 'no')
  const rows: { k: Key; val: string; need: string; ok: Verdict; hint?: Key }[] = [
    { k: 'vr.days', val: t('vr.dayN', { n: locDec(r.days.toFixed(1)) }), need: `≥ ${th.days}`, ok: v(r.days >= th.days) },
    { k: 'vr.trades', val: String(r.trades), need: `≥ ${th.trades}`, ok: v(r.trades >= th.trades) },
    { k: 'vr.return', val: `${pct(r.return)}（${t('vr.halves', { a: pct(r.first_half), b: pct(r.second_half) })}）`, need: t('vr.needHalves'), ok: r.first_half == null ? 'na' : v(r.return > 0 && r.first_half > 0 && (r.second_half ?? 0) > 0) },
    { k: 'vr.sharpe', val: r.sharpe == null ? t('vr.tooShort') : locDec(r.sharpe.toFixed(2)), need: `≥ ${th.sharpe}`, ok: r.sharpe == null ? 'na' : v(r.sharpe >= th.sharpe), hint: 'vr.h.sharpe' },
    { k: 'vr.dd', val: pct(-r.max_drawdown), need: `≤ ${(th.dd * 100).toFixed(0)}%`, ok: v(r.max_drawdown <= th.dd) },
    { k: 'vr.cost50', val: pct(r.cost_plus50), need: '> 0', ok: v(r.cost_plus50 > 0), hint: 'vr.h.cost50' },
    { k: 'vr.hold', val: pct(r.hold_btc), need: t('vr.needBeat', { v: pct(r.hold_btc) }), ok: r.hold_btc == null ? 'na' : v(r.return > r.hold_btc) },
    { k: 'vr.momentum', val: pct(r.momentum), need: t('vr.needBeat', { v: pct(r.momentum) }), ok: r.momentum == null ? 'na' : v(r.return > r.momentum), hint: 'vr.h.momentum' },
    { k: 'vr.random', val: r.random ? t('vr.randomV', { s: pct(r.random.strategy_mean, 2), m: pct(r.random.median, 2), b: (r.random.beat * 100).toFixed(0) }) : t('vr.tooFew'), need: t('vr.needRandom', { n: (th.beat * 100).toFixed(0) }), ok: r.random ? v(r.random.beat >= th.beat) : 'na', hint: 'vr.h.random' },
    { k: 'vr.best', val: r.best_share == null ? '—' : `${(r.best_share * 100).toFixed(0)}%`, need: `≤ ${(th.best * 100).toFixed(0)}%`, ok: r.best_share == null ? 'na' : v(r.best_share <= th.best), hint: 'vr.h.best' },
    { k: 'vr.cal', val: hi && lo ? t('vr.calV', { hi: pct(hi.avg, 2), lo: pct(lo.avg, 2) }) : '—', need: t('vr.needCal'), ok: calOk, hint: 'vr.h.cal' },
  ]
  const n = { ok: rows.filter((x) => x.ok === 'ok').length, no: rows.filter((x) => x.ok === 'no').length, na: rows.filter((x) => x.ok === 'na').length }
  const num = (k: keyof Th, label: Key, scale = 1) => (
    <label className="vr-th"><span>{t(label)}</span><input type="number" step="any" value={+(th[k] * scale).toFixed(4)} onChange={(e) => save({ ...th, [k]: Number(e.target.value) / scale })} data-vr-th={k} /></label>
  )
  return (
    <div className="vr" data-vr>
      <p className="vr-sum">
        {t('vr.sum', { ok: n.ok, no: n.no, na: n.na })}
        <button type="button" className="ai-log" onClick={() => setEdit(!edit)} data-vr-edit>{t(edit ? 'vr.thDone' : 'vr.thEdit')}</button>
      </p>
      {edit && (
        <div className="vr-ths" data-vr-ths>
          {num('days', 'vr.days')}{num('trades', 'vr.trades')}{num('sharpe', 'vr.sharpe')}{num('dd', 'vr.ddPct', 100)}{num('best', 'vr.bestPct', 100)}{num('beat', 'vr.beatPct', 100)}
          <button type="button" className="btn ghost sm" onClick={() => save(DEF)}>{t('vr.thReset')}</button>
        </div>
      )}
      <table className="tbl vr-tbl">
        <thead><tr><th>{t('vr.col.item')}</th><th>{t('vr.col.val')}</th><th>{t('vr.col.need')}</th><th /></tr></thead>
        <tbody>
          {rows.map((x) => (
            <tr key={x.k} data-vr-row={x.k} data-vr-ok={x.ok}>
              <td title={x.hint ? t(x.hint) : undefined}>{t(x.k)}</td>
              <td className="num">{x.val}</td>
              <td className="t3">{x.need}</td>
              <td><span className={`vr-v ${x.ok}`}>{t(`vr.v.${x.ok}` as Key)}</span></td>
            </tr>
          ))}
        </tbody>
      </table>
      <div className="vr-more">
        <div><span className="t3">{t('vr.signals')}</span> <b className="num">{r.signals}</b></div>
        <div><span className="t3">{t('vr.latency')}</span> <b className="num">{r.latency_p50_ms == null ? '—' : t('vr.latV', { a: r.latency_p50_ms, b: r.latency_p90_ms ?? '—' })}</b></div>
        <div><span className="t3">{t('vr.fees')}</span> <b className="num">{locDec(r.fees.toFixed(2))} USDT</b></div>
        <div><span className="t3">{t('vr.inference')}</span> <b className="num">{locDec(r.inference_usd.toFixed(2))} USD · {pct(r.return_after_inference)}</b></div>
        <div><span className="t3">{t('vr.versions')}</span> <b>{r.versions.length ? r.versions.map((x) => `${x.version}（${fmtTime(x.from)}，${x.signals}）`).join(' → ') : '—'}</b></div>
      </div>
      <Consistency strategy={strategy} fwd={r.return} fwdTrades={r.trades} fwdFees={r.fees} />
      {r.versions.length > 1 && <p className="vr-note warn-t">{t('vr.versionNote')}</p>}
      {r.notes.map((c) => <p key={c} className="vr-note t3">{t(`vr.note.${c}` as Key)}</p>)}
      <p className="vr-note t3">{t('vr.foot')}</p>
    </div>
  )
}

interface Bt { initial: number; final: number; fees: number; stats: { trades: number }; outcomes: { ok: boolean }[] }
const DAY = 86_400_000

/** 回测一致性（18 第 5.5 条）：把策略收到的目标仓位原样拿去回测（同一套撮合代码），和前向的结果摆在一起。
 *  回测按信号到达时刻市价成交，前向按信号里写的执行方式（挂单、限价），所以总会有差别；差得多说明成交方式对结果影响大。 */
function Consistency({ strategy, fwd, fwdTrades, fwdFees }: { strategy: string; fwd: number; fwdTrades: number; fwdFees: number }) {
  const t = useT()
  const [st, setSt] = useState<{ busy?: boolean; err?: string; bt?: Bt; n?: number; cut?: boolean }>({})
  const table = `/api/strategies/${encodeURIComponent(strategy)}/signal-table`
  const run = async () => {
    setSt({ busy: true })
    try {
      const res = await fetch(table, { headers: { 'X-QS-Client': 'web' } })
      if (!res.ok) throw new ApiError('INTERNAL_ERROR', res.status)
      const lines = await res.text()
      const data = lines.split('\n').filter((l) => l.trim() && !l.startsWith('#'))
      if (!data.length) { setSt({ err: t('vr.bt.none') }); return }
      const to = Date.now()
      const first = Math.floor(Date.parse(data[0].split(',')[0]) / 60_000) * 60_000
      const from = Math.max(first, to - (LIMITS.backtest_days - 1) * DAY) // 回测最多这么多天（部署的人可以改，D103）
      const bt = await api<Bt>('POST', '/api/backtest', { lines, from, to, initial: '10000', leverage: 5, mode: 'cross' })
      setSt({ bt, n: data.length, cut: from > first })
    } catch (e) {
      setSt({ err: e instanceof ApiError ? errText(e.code, e.params) : errText('INTERNAL_ERROR') })
    }
  }
  const btRet = st.bt ? st.bt.final / st.bt.initial - 1 : null
  return (
    <div className="vr-bt" data-vr-bt>
      <div className="vr-bt-h">
        <b>{t('vr.bt.title')}</b>
        <span className="t3">{t('vr.bt.hint')}</span>
      </div>
      {!st.bt && (
        <div className="vr-bt-act">
          <button type="button" className="btn secondary sm" disabled={st.busy} onClick={() => void run()} data-vr-bt-run>{t(st.busy ? 'vr.bt.running' : 'vr.bt.run')}</button>
          <a className="ai-log" href={table} download>{t('vr.bt.download')}</a>
          {st.err && <span className="warn-t">{st.err}</span>}
        </div>
      )}
      {st.bt && btRet != null && (
        <table className="tbl vr-bt-tbl" data-vr-bt-result>
          <thead><tr><th /><th>{t('vr.bt.fwd')}</th><th>{t('vr.bt.back')}</th><th>{t('vr.bt.diff')}</th></tr></thead>
          <tbody>
            <tr><td>{t('vr.return')}</td><td className="num">{pct(fwd, 2)}</td><td className="num">{pct(btRet, 2)}</td><td className="num">{pct(btRet - fwd, 2)}</td></tr>
            <tr><td>{t('vr.trades')}</td><td className="num">{fwdTrades}</td><td className="num">{st.bt.stats.trades}</td><td className="num">{st.bt.stats.trades - fwdTrades}</td></tr>
            <tr><td>{t('vr.fees')}</td><td className="num">{locDec(fwdFees.toFixed(2))} USDT</td><td className="num">{locDec(st.bt.fees.toFixed(2))} USDT</td><td className="num">{locDec((st.bt.fees - fwdFees).toFixed(2))} USDT</td></tr>
          </tbody>
        </table>
      )}
      {st.bt && <p className="vr-note t3">{t('vr.bt.note', { n: st.n ?? 0, bad: st.bt.outcomes.filter((o) => !o.ok).length })}{st.cut ? ' ' + t('vr.bt.cut', { days: LIMITS.backtest_days }) : ''}</p>}
    </div>
  )
}
