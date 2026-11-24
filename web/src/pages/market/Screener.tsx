// 筛选器和异动（市场页"筛选"标签）：左边全市场的表，能按任意一列排序、按预设筛；右边异动，新的在前。
// 点一行进交易页。自选存在这台设备的浏览器里。
import { useMemo, useState } from 'react'
import { useT, fmtTime, fmtCompactL, type Key } from '../../lib/i18n'
import { useScan, loadStars, saveStars, type ScanEvent, type ScanRow } from '../../lib/scan'
import { fmtPrice } from '../../lib/format'
import { go } from '../../lib/route'
import { Seg } from '../../ui/Seg'
import { locDec } from '../../lib/tz'

type Preset = 'all' | 'star' | 'movers' | 'volume' | 'funding' | 'oi'
type Col = 's' | 'px' | 'chg5m' | 'chg1h' | 'chg24h' | 'qv' | 'rv' | 'funding' | 'oi1h' | 'oi24h'

const sign = (v: number) => (v > 0 ? '+' : v < 0 ? '−' : '')
// 四舍五入后是 0 不带正负号（11-17 走查："−0.0%""+0.000%"）；小数点跟数字格式走
const signed = (v: number, d: number) => { const s = Math.abs(v).toFixed(d); return (Number(s) === 0 ? '' : sign(v)) + locDec(s) + '%' }
const pct = (v: number | null | undefined, d = 2) => (v == null ? '—' : signed(v, d))
const pctF = (v: number | null | undefined, d = 1) => (v == null ? '—' : signed(v * 100, d)) // 小数 → 百分比
const dir = (v: number | null | undefined) => (v == null || v === 0 ? '' : v > 0 ? 'up' : 'down')
const open = (i: string) => go(`trade&i=${encodeURIComponent(i)}`)

/** 这个预设留下哪些行、默认按哪列排 */
const PRESETS: Record<Preset, { keep: (r: ScanRow, star: Set<string>) => boolean; sort: Col; desc: boolean }> = {
  all: { keep: () => true, sort: 'qv', desc: true },
  star: { keep: (r, s) => s.has(r.i), sort: 'qv', desc: true },
  movers: { keep: (r) => r.chg1h != null && Math.abs(r.chg1h) >= 1, sort: 'chg1h', desc: true },
  volume: { keep: (r) => (r.rv ?? 0) >= 1.5, sort: 'rv', desc: true },
  funding: { keep: (r) => r.funding != null && Math.abs(r.funding) >= 0.0003, sort: 'funding', desc: true },
  oi: { keep: (r) => r.oi1h != null && Math.abs(r.oi1h) >= 0.03, sort: 'oi1h', desc: true },
}

export function Screener() {
  const t = useT()
  const data = useScan()
  const [preset, setPreset] = useState<Preset>('all')
  const [q, setQ] = useState('')
  const [sort, setSort] = useState<{ c: Col; desc: boolean } | null>(null)
  const [stars, setStars] = useState(loadStars)
  const p = PRESETS[preset]
  const col = sort?.c ?? p.sort
  const desc = sort?.desc ?? p.desc
  const rows = useMemo(() => {
    const qq = q.trim().toUpperCase()
    const list = (data?.rows ?? []).filter((r) => p.keep(r, stars) && (!qq || r.s.includes(qq)))
    const val = (r: ScanRow): number | string => (col === 's' ? r.s : col === 'funding' ? Math.abs(r.funding ?? -1) : ((r[col] as number | null | undefined) ?? -Infinity))
    return list.sort((a, b) => {
      const x = val(a), y = val(b)
      const c = typeof x === 'string' ? x.localeCompare(y as string) : (x as number) - (y as number)
      return desc ? -c : c
    })
  }, [data, preset, q, col, desc, stars]) // eslint-disable-line react-hooks/exhaustive-deps
  const star = (i: string) => {
    const n = new Set(stars)
    if (n.has(i)) n.delete(i); else n.add(i)
    setStars(n); saveStars(n)
  }
  const th = (c: Col, label: string, cls = '', hint?: string) => (
    <th className={`${cls} sc-th ${col === c ? 'on' : ''}`} title={hint} aria-sort={col === c ? (desc ? 'descending' : 'ascending') : undefined}>
      <button type="button" onClick={() => setSort({ c, desc: col === c ? !desc : c !== 's' })}>{label}{col === c && <span className="sc-arrow">{desc ? '↓' : '↑'}</span>}</button>
    </th>
  )
  return (
    <div className="sc" data-sc>
      <section className="sc-main">
        <div className="sc-head">
          <Seg label={t('sc.preset')} value={preset} onChange={(v) => { setPreset(v); setSort(null) }} options={(['all', 'star', 'movers', 'volume', 'funding', 'oi'] as Preset[]).map((k) => ({ value: k, label: t(`sc.p.${k}` as Key) }))} />
          <input className="rv-in sc-q" placeholder={t('sc.search')} value={q} onChange={(e) => setQ(e.target.value)} aria-label={t('sc.search')} data-sc="q" />
          <span className="sc-n t3 num">{t('sc.count', { n: rows.length })}</span>
        </div>
        <p className="sc-hint t3">{t(`sc.ph.${preset}` as Key)}{data?.warm ? ' ' + t('sc.warm') : ''}</p>
        <div className="sc-wrap mk-scroll">
          <table className="tbl sc-tbl" data-sc="table">
            <thead><tr>
              <th className="sc-star" aria-label={t('sc.p.star')} />
              {th('s', t('sc.c.coin'))}
              {th('px', t('sc.c.px'), 'r')}
              {th('chg5m', t('sc.c.5m'), 'r')}
              {th('chg1h', t('sc.c.1h'), 'r')}
              {th('chg24h', t('sc.c.24h'), 'r')}
              {th('qv', t('sc.c.qv'), 'r sc-opt')}
              {th('rv', t('sc.c.rv'), 'r sc-opt', t('sc.c.rvHint'))}
              {th('funding', t('sc.c.funding'), 'r', t('sc.c.fundingHint'))}
              {th('oi1h', t('sc.c.oi1h'), 'r sc-opt', t('sc.c.oiHint'))}
              {th('oi24h', t('sc.c.oi24h'), 'r sc-opt2')}
            </tr></thead>
            <tbody>{rows.map((r) => (
              <tr key={r.i} className="sc-row" onClick={() => open(r.i)} data-sc-row={r.s}>
                <td className="sc-star"><button type="button" className={stars.has(r.i) ? 'on' : ''} aria-pressed={stars.has(r.i)} aria-label={t('sc.starBtn', { coin: r.s })} onClick={(e) => { e.stopPropagation(); star(r.i) }}>{stars.has(r.i) ? '★' : '☆'}</button></td>
                <td><span className="sym">{r.s}</span>{r.sec && <span className="sc-sec">{r.sec}</span>}</td>
                <td className="num r">{fmtPrice(String(r.px))}</td>
                <td className={`num r ${dir(r.chg5m)}`}>{pct(r.chg5m)}</td>
                <td className={`num r ${dir(r.chg1h)}`}>{pct(r.chg1h)}</td>
                <td className={`num r ${dir(r.chg24h)}`}>{pct(r.chg24h)}</td>
                <td className="num r t2 sc-opt">{fmtCompactL(r.qv)}</td>
                <td className={`num r sc-opt ${(r.rv ?? 0) >= 2 ? 'sc-hot' : 't2'}`}>{r.rv ? `${locDec(r.rv.toFixed(1))}×` : '—'}</td>
                <td className={`num r ${r.funding != null && Math.abs(r.funding) >= 0.0005 ? 'sc-hot' : 't2'}`}>{pctF(r.funding, 3)}</td>
                <td className={`num r sc-opt ${dir(r.oi1h)}`}>{pctF(r.oi1h)}</td>
                <td className={`num r sc-opt2 ${dir(r.oi24h)}`}>{pctF(r.oi24h)}</td>
              </tr>
            ))}</tbody>
          </table>
          {data && rows.length === 0 && <p className="sc-empty t3">{t(preset === 'star' ? 'sc.emptyStar' : 'sc.empty')}</p>}
        </div>
      </section>
      <aside className="sc-side" data-sc="events">
        <div className="sc-side-head"><span className="display-title">{t('sc.events')}</span><span className="t3">{t('sc.eventsSub')}</span></div>
        <ol className="sc-ev mk-scroll">
          {(data?.events ?? []).map((e) => <EventRow key={`${e.t}-${e.i}-${e.kind}`} e={e} />)}
          {data && !data.events.length && <li className="sc-empty t3">{t('sc.noEvents')}</li>}
        </ol>
      </aside>
    </div>
  )
}

/** 一条异动写成一句话："PEPE 5 分钟涨 2.3%" */
export function EventRow({ e }: { e: ScanEvent }) {
  const t = useT()
  const txt = e.kind === 'up' || e.kind === 'down' ? t(`sc.ev.${e.kind}`, { v: locDec(Math.abs(e.v).toFixed(1)) })
    : e.kind === 'volume' ? t('sc.ev.volume', { v: locDec(e.v.toFixed(1)) })
      : e.kind === 'funding_hi' || e.kind === 'funding_lo' ? t(`sc.ev.${e.kind}`, { v: locDec(Math.abs(e.v * 100).toFixed(3)) })
        : t(`sc.ev.${e.kind}`, { v: Math.abs(e.v * 100).toFixed(0) })
  const tone = e.kind === 'up' || e.kind === 'oi_up' ? 'up' : e.kind === 'down' || e.kind === 'oi_down' ? 'down' : 'sc-hot'
  return (
    <li className="sc-ev-row" onClick={() => open(e.i)}>
      <span className="num t3">{fmtTime(e.t)}</span>
      <span className="sym">{e.s}</span>
      <span className={tone}>{txt}</span>
    </li>
  )
}
