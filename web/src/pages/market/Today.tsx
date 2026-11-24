// 今日（市场页"今日"标签）：开盘前看一眼——市场情绪和结构、重点币今天要看的价位、今明两天的经济事件、
// 最近 12 小时的异动、24 小时涨跌榜、新闻标题。数据：币安、alternative.me（恐惧贪婪）、CoinGecko（总市值）、公开 RSS。
import { useMemo, useState } from 'react'
import { useT, fmtTime, fmtCompactL, type Key } from '../../lib/i18n'
import { useNews, safeHref } from '../../lib/news'
import { Seg } from '../../ui/Seg'
import { useToday, type Level, type ScanRow } from '../../lib/scan'
import { useEconEvents, evLong, evVolOk, calMeta } from '../../lib/calendar'
import { fmtPrice } from '../../lib/format'
import { go } from '../../lib/route'
import { EventRow } from './Screener'
import { locDec, numLocale } from '../../lib/tz'

const sign = (v: number) => (v > 0 ? '+' : v < 0 ? '−' : '')
const pct = (v: number, d = 2) => sign(v) + locDec(Math.abs(v).toFixed(d)) + '%'
const dir = (v: number) => (v > 0 ? 'up' : v < 0 ? 'down' : '')
/** 带正负号的紧凑金额：+12.3亿 / −4.5亿 */
const signed = (v: number) => sign(v) + fmtCompactL(Math.abs(v))

export function Today() {
  const t = useT()
  const d = useToday()
  const ev = useEconEvents(true)
  const now = Date.now()
  const soon = ev.filter((e) => e.t > now - 2 * 3600_000 && e.t < now + 36 * 3600_000)
  const miss = (k: string) => !!d?.missing?.includes(k)
  const b = d?.breadth
  return (
    <div className="td mk-scroll" data-td>
      <dl className="td-tiles" data-td="tiles">
        <div>
          <dt>{t('td.fear')}</dt>
          {d?.fear ? <>
            <dd className={`num td-fg ${d.fear.label}`}>{d.fear.value}<small>{t(`td.fg.${d.fear.label}` as Key)}</small></dd>
            <small className="num">{t('td.fearAgo', { prev: d.fear.prev, week: d.fear.week })}</small>
          </> : <dd className="t3 td-na">{t(miss('fear') ? 'td.na' : 'common.loading')}</dd>}
        </div>
        <div>
          <dt>{t('td.mcap')}</dt>
          {d?.global ? <>
            <dd className="num">{fmtCompactL(d.global.mcap)}<small className={dir(d.global.mcap_chg)}>{pct(d.global.mcap_chg)}</small></dd>
            <small className="num">{t('td.dom', { btc: locDec(d.global.btc.toFixed(1)), eth: locDec(d.global.eth.toFixed(1)) })}</small>
          </> : <dd className="t3 td-na">{t(miss('global') ? 'td.na' : 'common.loading')}</dd>}
        </div>
        <div>
          <dt>{t('td.breadth')}</dt>
          {b ? <>
            <dd className="num"><span className="up">{b.up}</span><span className="t3"> / </span><span className="down">{b.down}</span></dd>
            <small className="num">{t('td.median', { v: pct(b.median) })}</small>
          </> : <dd className="t3 td-na">{t('common.loading')}</dd>}
        </div>
        <div>
          <dt>{t('td.vol')}</dt>
          {b ? <dd className="num">{fmtCompactL(b.qv)}</dd> : <dd className="t3 td-na">{t('common.loading')}</dd>}
          <small>{t('td.volSub')}</small>
        </div>
        <div>
          <dt title={t('td.fundHint')}>{t('td.fund')}</dt>
          {b?.funding_w != null ? <>
            <dd className="num">{pct(b.funding_w * 100, 4)}</dd>
            <small>{t(b.funding_w > 0.0002 ? 'td.fundLongHot' : b.funding_w < 0 ? 'td.fundShort' : 'td.fundNormal')}</small>
          </> : <dd className="t3 td-na">{t(d ? 'td.na' : 'common.loading')}</dd>}
        </div>
        <div>
          <dt title={t('td.lsrHint')}>{t('td.lsr')}</dt>
          {d?.lsr ? <>
            <dd className="num">{locDec(d.lsr.ratio.toFixed(2))}</dd>
            <small className="num">{t('td.lsrSub', { long: (d.lsr.long * 100).toFixed(0), prev: locDec(d.lsr.prev.toFixed(2)) })}</small>
          </> : <dd className="t3 td-na">{t(miss('lsr') ? 'td.na' : 'common.loading')}</dd>}
        </div>
      </dl>
      {/* 场外和宏观（18 第 10.2 条）：Coinbase 溢价、期权隐含波动、稳定币总量；配了 FRED 密钥再加美元指数、美债、标普 */}
      <dl className="td-tiles td-tiles2" data-td="extra">
        <div>
          <dt title={t('td.premHint')}>{t('td.prem')}</dt>
          {d?.premium ? <>
            <dd className={`num ${dir(d.premium.now)}`}>{pct(d.premium.now, 3)}</dd>
            <small className="num">{t('td.premAvg', { v: pct(d.premium.avg24, 3) })}</small>
          </> : <dd className="t3 td-na">{t(miss('premium') || (d && !d.premium) ? 'td.na' : 'common.loading')}</dd>}
        </div>
        <div>
          <dt title={t('td.ivHint')}>{t('td.iv')}</dt>
          {d?.iv ? <>
            <dd className="num">{locDec(d.iv.btc.toFixed(1))}<small className={dir(d.iv.btc - d.iv.btc_prev)}>{sign(d.iv.btc - d.iv.btc_prev)}{locDec(Math.abs(d.iv.btc - d.iv.btc_prev).toFixed(1))}</small></dd>
            <small className="num">{t('td.ivSub', { prev: locDec(d.iv.btc_prev.toFixed(1)), eth: d.iv.eth ? locDec(d.iv.eth.toFixed(1)) : '—' })}</small>
          </> : <dd className="t3 td-na">{t(miss('iv') || (d && !d.iv) ? 'td.na' : 'common.loading')}</dd>}
        </div>
        <div>
          <dt title={t('td.stableHint')}>{t('td.stable')}</dt>
          {d?.stable ? <>
            <dd className="num">{fmtCompactL(d.stable.total)}</dd>
            <small className="num">{t('td.stableSub', { day: signed(d.stable.day), week: signed(d.stable.week) })}</small>
          </> : <dd className="t3 td-na">{t(miss('stable') || (d && !d.stable) ? 'td.na' : 'common.loading')}</dd>}
        </div>
        {d?.macro && d.macro.length > 0 ? d.macro.map((m) => (
          <div key={m.id} data-macro={m.id}>
            <dt title={t('td.macroHint', { date: m.date })}>{t(`td.m.${m.id}` as Key)}</dt>
            <dd className="num">{m.id === 'us10y' ? locDec(m.value.toFixed(2)) + '%' : m.value.toLocaleString(numLocale(), { maximumFractionDigits: 2 })}
              <small className={dir(m.value - m.prev)}>{m.id === 'us10y' ? `${sign(m.value - m.prev)}${Math.abs((m.value - m.prev) * 100).toFixed(0)}bp` : pct((m.value / m.prev - 1) * 100)}</small></dd>
            <small className="num">{t('td.macroDate', { date: m.date.slice(5) })}</small>
          </div>
        )) : (
          <div className="td-tile-wide" data-macro-off>
            <dt>{t('td.macro')}</dt>
            <dd className="t3 td-na">{t(d?.macro_off ? 'td.macroOff' : miss('macro') ? 'td.na' : d ? 'td.macroSim' : 'common.loading')}</dd>
          </div>
        )}
      </dl>
      <div className="td-grid">
        <section className="td-card td-levels" data-td="levels">
          <h3>{t('td.levels')}<span className="t3">{t('td.levelsSub')}</span></h3>
          {d && d.levels.length > 0 ? (
            <table className="tbl td-tbl">
              <thead><tr><th>{t('sc.c.coin')}</th><th className="r">{t('td.c.px')}</th><th className="r">{t('td.c.where')}</th><th className="r">{t('td.c.ph')}</th><th className="r">{t('td.c.pl')}</th><th className="r td-opt">{t('td.c.pc')}</th><th className="r td-opt">{t('td.c.wo')}</th><th className="r td-opt2">{t('td.c.wr')}</th></tr></thead>
              <tbody>{d.levels.map((l) => <LevelRow key={l.i} l={l} />)}</tbody>
            </table>
          ) : <p className="t3 td-empty">{t(d ? 'td.noLevels' : 'common.loading')}</p>}
        </section>
        <section className="td-card" data-td="calendar">
          <h3>{t('td.cal')}<span className="t3">{t('td.calSub')}</span></h3>
          <ol className="td-list">
            {soon.map((e) => (
              <li key={`${e.t}-${e.key}`} className={e.t < now ? 't3' : ''}>
                <span className="num">{fmtTime(e.t)}</span>
                <span>{evLong(e.key)}</span>
                <span className="t3 num">{evVolOk(e) ? t('td.calVol', { v: locDec(e.vol!.mean.toFixed(1)), n: e.vol!.n }) : ''}</span>
              </li>
            ))}
            {!soon.length && <li className="t3">{t('td.calNone')}</li>}
          </ol>
          {/* 内置的美国数据日程快用完了（不到 60 天），又没配 FRED：提前说 */}
          {!calMeta.auto && calMeta.until > 0 && calMeta.until - now < 60 * 86_400_000 && (
            <p className="td-cal-warn" data-cal-warn>{t('td.calUntil', { d: fmtTime(calMeta.until).slice(0, 5), y: new Date(calMeta.until).getFullYear() })}</p>
          )}
        </section>
        <section className="td-card" data-td="events">
          <h3>{t('td.events')}<span className="t3">{t('td.eventsSub')}</span></h3>
          <ol className="sc-ev td-ev">
            {(d?.events ?? []).slice(0, 12).map((e) => <EventRow key={`${e.t}-${e.i}-${e.kind}`} e={e} />)}
            {d && !d.events.length && <li className="t3">{t('sc.noEvents')}</li>}
          </ol>
        </section>
        <section className="td-card" data-td="movers">
          <h3>{t('td.movers')}<span className="t3">{t('td.moversSub')}</span></h3>
          <ol className="td-list td-movers">
            {(d?.movers ?? []).map((r) => <MoverRow key={r.i} r={r} />)}
          </ol>
        </section>
        <NewsCard />
      </div>
      <p className="td-src t3">{t('td.sources')}</p>
    </div>
  )
}

function LevelRow({ l }: { l: Level }) {
  const t = useT()
  const where = l.px > l.prev_high ? 'above' : l.px < l.prev_low ? 'below' : 'inside'
  const f = (v: number) => (v > 0 ? fmtPrice(String(v)) : '—')
  return (
    <tr className="sc-row" onClick={() => go(`trade&i=${encodeURIComponent(l.i)}`)}>
      <td><span className="sym">{l.s}</span></td>
      <td className="num r">{f(l.px)}</td>
      <td className={`r td-where ${where === 'above' ? 'up' : where === 'below' ? 'down' : 't2'}`}>{t(`td.w.${where}` as Key)}</td>
      <td className="num r">{f(l.prev_high)}</td>
      <td className="num r">{f(l.prev_low)}</td>
      <td className="num r td-opt">{f(l.prev_close)}</td>
      <td className="num r td-opt">{f(l.week_open)}</td>
      <td className="num r td-opt2 t2">{f(l.week_low)} – {f(l.week_high)}</td>
    </tr>
  )
}

function MoverRow({ r }: { r: ScanRow }) {
  return (
    <li className="sc-row" onClick={() => go(`trade&i=${encodeURIComponent(r.i)}`)}>
      <span className="sym">{r.s}</span>
      <span className="num t2">{fmtPrice(String(r.px))}</span>
      <span className={`num ${dir(r.chg24h)}`}>{pct(r.chg24h)}</span>
    </li>
  )
}

// 新闻标题：只有标题、来源、时间，点开去原网站看（18 第 8.6 条）。平台不拿它做买卖判断。
function NewsCard() {
  const t = useT()
  const n = useNews()
  const [coin, setCoin] = useState('')
  const coins = useMemo(() => {
    const c = new Map<string, number>()
    for (const it of n?.items ?? []) for (const x of it.coins ?? []) c.set(x, (c.get(x) ?? 0) + 1)
    return [...c.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0])).slice(0, 5).map(([k]) => k)
  }, [n])
  // 筛选：全部 / 交易所公告 / 某个币
  const list = (n?.items ?? []).filter((it) => !coin || (coin === '!notice' ? it.kind === 'notice' : it.coins?.includes(coin))).slice(0, 14)
  const hasNotice = (n?.items ?? []).some((it) => it.kind === 'notice')
  const off = n && n.feeds + n.x === 0 && !n.notices
  const down = n && !off && n.sources.length > 0 && n.sources.every((s) => !!s.error)
  return (
    <section className="td-card td-news" data-td="news">
      <h3>{t('td.news')}<span className="t3">{t('td.newsSub')}</span>
        {(coins.length > 0 || hasNotice) && <span className="td-news-f"><Seg label={t('td.newsCoin')} value={coin} onChange={setCoin}
          options={[{ value: '', label: t('td.newsAll') }, ...(hasNotice ? [{ value: '!notice', label: t('td.newsNotice') }] : []), ...coins.map((c) => ({ value: c, label: c }))]} /></span>}
      </h3>
      <ol className="td-list td-newsl">
        {list.map((it) => (
          <li key={it.id}>
            <span className="num t3">{it.time ? fmtTime(it.time) : ''}</span>
            <a href={safeHref(it.url)} target="_blank" rel="noopener noreferrer nofollow" title={it.title}>{it.kind === 'notice' && <span className="td-news-tag">{t('td.newsNoticeTag')}</span>}{it.title}</a>
            <span className="t3 td-news-s">{it.source}</span>
          </li>
        ))}
        {!n && <li className="t3">{t('common.loading')}</li>}
        {off && <li className="t3">{t('td.newsOff')}</li>}
        {down && <li className="t3">{t('td.newsDown')}</li>}
        {n && !off && !down && !list.length && <li className="t3">{t(coin ? 'td.newsNoCoin' : 'td.newsNone')}</li>}
      </ol>
    </section>
  )
}
