// 模拟和实盘对照（18 第 5.6 条）：同一个策略同时跟在模拟账户和交易所账户上，同一条信号两边各自怎么成交。
// 数据就是两边的执行记录（/api/follows 里带着），按"哪条信号 + 哪个品种"配对。只摆数字，不下结论。
import { useMemo } from 'react'
import { useT, fmtTime } from '../../lib/i18n'
import { isRunning, type ExecRec, type Follow } from '../../lib/follow'
import { isLiveId } from '../../lib/live'
import { baseOf, fmtPrice } from '../../lib/format'
import { locDec } from '../../lib/tz'

const num = (s?: string) => Number(s ?? 0) || 0
const done = (r: ExecRec) => num(r.filled) > 0 && num(r.avg_px) > 0
/** 带符号的数：四舍五入后是 0 的写 0，不写 -0.0 */
const signed = (v: number, d: number) => { const s = Math.abs(v).toFixed(d); return Number(s) === 0 ? locDec((0).toFixed(d)) : `${v > 0 ? '+' : '−'}${locDec(s)}` }
const bp = (v: number) => `${signed(v, 1)} bp`

/** 一边的汇总：平均滑点、手续费（占成交额）、成交比例、平均用时 */
function sideStats(rows: ExecRec[]) {
  const filled = rows.filter(done)
  const notional = filled.reduce((a, r) => a + num(r.filled) * num(r.avg_px), 0)
  const fee = filled.reduce((a, r) => a + num(r.fee), 0)
  const slipRows = filled.filter((r) => num(r.model_avg) > 0)
  const timed = rows.filter((r) => r.end && r.end >= r.time)
  return {
    slip: slipRows.length ? slipRows.reduce((a, r) => a + num(r.slip_bps), 0) / slipRows.length : null,
    feeBp: notional > 0 ? (fee / notional) * 1e4 : null,
    fillRate: rows.length ? rows.reduce((a, r) => a + Math.min(1, num(r.qty) > 0 ? num(r.filled) / num(r.qty) : 0), 0) / rows.length : null,
    ms: timed.length ? timed.reduce((a, r) => a + (r.end! - r.time), 0) / timed.length : null,
  }
}

/** 挑一对：模拟一个、交易所一个，跑着的优先，再按开始时间新的优先 */
export function comparePair(mine: Follow[]): [Follow, Follow] | null {
  const pick = (live: boolean) => mine.filter((f) => isLiveId(f.account) === live).sort((a, b) => Number(isRunning(b)) - Number(isRunning(a)) || b.started - a.started)[0]
  const p = pick(false), l = pick(true)
  return p && l ? [p, l] : null
}

export function PaperLiveCompare({ paper, live }: { paper: Follow; live: Follow }) {
  const t = useT()
  const rows = useMemo(() => {
    const byKey = new Map(live.records.filter((r) => r.msg).map((r) => [`${r.msg}|${r.instrument}`, r]))
    return paper.records.filter((r) => r.msg && byKey.has(`${r.msg}|${r.instrument}`)).map((p) => ({ p, l: byKey.get(`${p.msg}|${p.instrument}`)! }))
  }, [paper.records, live.records])
  const sp = sideStats(rows.map((r) => r.p)), sl = sideStats(rows.map((r) => r.l))
  const ret = (f: Follow) => (num(f.params.alloc) > 0 ? (num(f.pnl) / num(f.params.alloc)) * 100 : 0)
  const cell = (v: number | null, fmt: (x: number) => string) => (v === null ? '—' : fmt(v))
  const diff = (a: number | null, b: number | null, fmt: (x: number) => string) => (a === null || b === null ? '—' : fmt(b - a))
  const pct = (v: number) => `${signed(v, 2)}%`
  return (
    <section className="cmp" data-compare>
      <div className="cmp-h">
        <h3>{t('cmp.title')}</h3>
        <p className="t3">{t('cmp.hint')}</p>
      </div>
      <table className="tbl cmp-sum">
        <thead><tr><th /><th>{t('cmp.paper')}</th><th>{t('cmp.live')}</th><th>{t('cmp.diff')}</th></tr></thead>
        <tbody>
          <tr><td>{t('cmp.return')}</td><td className="num">{pct(ret(paper))}</td><td className="num">{pct(ret(live))}</td><td className="num">{pct(ret(live) - ret(paper))}</td></tr>
          <tr><td title={t('fw.slipHint')}>{t('cmp.slip')}</td><td className="num">{cell(sp.slip, bp)}</td><td className="num">{cell(sl.slip, bp)}</td><td className="num">{diff(sp.slip, sl.slip, bp)}</td></tr>
          <tr><td>{t('cmp.fee')}</td><td className="num">{cell(sp.feeBp, (x) => `${locDec(x.toFixed(1))} bp`)}</td><td className="num">{cell(sl.feeBp, (x) => `${locDec(x.toFixed(1))} bp`)}</td><td className="num">{diff(sp.feeBp, sl.feeBp, bp)}</td></tr>
          <tr><td>{t('cmp.fillRate')}</td><td className="num">{cell(sp.fillRate, (x) => `${(x * 100).toFixed(0)}%`)}</td><td className="num">{cell(sl.fillRate, (x) => `${(x * 100).toFixed(0)}%`)}</td><td className="num">{diff(sp.fillRate, sl.fillRate, (x) => `${signed(x * 100, 0)}%`)}</td></tr>
          <tr><td>{t('cmp.ms')}</td><td className="num">{cell(sp.ms, (x) => `${locDec((x / 1000).toFixed(1))} s`)}</td><td className="num">{cell(sl.ms, (x) => `${locDec((x / 1000).toFixed(1))} s`)}</td><td className="num">{diff(sp.ms, sl.ms, (x) => `${signed(x / 1000, 1)} s`)}</td></tr>
        </tbody>
      </table>
      <p className="t3 cmp-n">{t('cmp.n', { n: rows.length })}</p>
      {rows.length > 0 && (
        <div className="cmp-wrap">
          <table className="tbl cmp-rows">
            <thead><tr><th>{t('col.time')}</th><th>{t('col.inst')}</th><th>{t('fw.col.target')}</th><th>{t('cmp.paperPx')}</th><th>{t('cmp.livePx')}</th><th title={t('cmp.diffHint')}>{t('cmp.diff')}</th></tr></thead>
            <tbody>
              {rows.slice(0, 30).map(({ p, l }) => {
                const both = done(p) && done(l)
                const d = both ? ((num(l.avg_px) - num(p.avg_px)) / num(p.avg_px)) * 1e4 * (p.side === 'sell' ? -1 : 1) : null
                const px = (r: ExecRec) => (done(r) ? fmtPrice(r.avg_px) : t(r.state === 'pending' ? 'cmp.stPending' : r.state.startsWith('skipped') ? 'cmp.stSkipped' : 'cmp.stNone'))
                return (
                  <tr key={`${p.msg}|${p.instrument}`}>
                    <td className="t2">{fmtTime(p.time, { seconds: true })}</td>
                    <td><span className="sym">{baseOf(p.instrument)}</span></td>
                    <td className="num">{(num(p.weight) * 100).toFixed(0)}%</td>
                    <td className="num">{px(p)}</td>
                    <td className="num">{px(l)}</td>
                    <td className={`num ${d !== null && d > 0.05 ? 'down' : d !== null && d < -0.05 ? 'up' : 't2'}`}>{d === null ? '—' : bp(d)}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}
