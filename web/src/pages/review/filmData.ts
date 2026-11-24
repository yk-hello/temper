// 高光卡片和视频要的数据和文字：档案、示例共用（D91）。
import type { Candle, FilmText, FilmTrade } from '../../charts/film'
import { fmtNum } from '../../charts/scale'
import { evLong } from '../../lib/calendar'
import { coinOf, isSpot, pct, type HlEvent, type HlSnap } from '../../lib/highlight'
import { fmtCompactL, fmtTime, tr, type Key } from '../../lib/i18n'
import { priceDecimals } from '../../lib/format'
import { numLocale } from '../../lib/tz'

const hhmm = (t: number) => fmtTime(t).slice(-5)

export function eventText(e: HlEvent, inst: string): string {
  if (e.kind === 'econ') return evLong(e.key ?? '')
  if (e.kind === 'whale') return tr(`hl.ev.whale.${e.key}` as Key, { coin: coinOf(inst), usd: fmtCompactL(e.usd ?? 0) })
  return tr(e.side === 'long' ? 'hl.ev.liqLong' : 'hl.ev.liqShort', { usd: fmtCompactL(e.usd ?? 0) })
}

/** 持仓时长写成"45 分钟""3 小时 12 分""2 天 4 小时" */
export function holdText(ms: number): string {
  const m = Math.max(1, Math.round(ms / 60_000))
  if (m < 60) return tr('hl.min', { n: m })
  const h = Math.floor(m / 60)
  if (h < 48) return tr('hl.hm', { h, m: m % 60 })
  return tr('hl.dh', { d: Math.floor(h / 24), h: h % 24 })
}

/** 卡片和视频的数据 */
export function filmTrade(s: HlSnap, candles: Candle[], iv: number, events: HlEvent[]): FilmTrade {
  return {
    candles, iv, side: s.side, leverage: isSpot(s.instrument) ? 1 : s.leverage, entry: s.entry, exit: s.exit,
    openTime: s.open_time, closeTime: s.close_time, roe: s.roe, qty: s.qty || undefined, net: s.qty ? s.net : undefined,
    mae: s.path_known ? s.mae : undefined, mfe: s.path_known ? s.mfe : undefined,
    events: events.map((e) => ({ t: e.t, text: eventText(e, s.instrument) })),
  }
}

/** 卡片和视频上的字。demo：示例，按几倍、不计手续费 */
export function filmText(s: HlSnap, _ft: FilmTrade, demo: boolean): FilmText {
  const coin = coinOf(s.instrument)
  const dec = priceDecimals(s.entry)
  const spot = isSpot(s.instrument)
  const side = tr(s.side === 'long' ? 'hl.long' : 'hl.short')
  const mode = tr(s.mode === 'live' ? 'hl.live' : 'hl.sim')
  const lev = Math.max(1, s.leverage)
  const basis = spot ? tr('hl.onCost') : tr('hl.onMargin')
  return {
    title: `${coin} ${tr(spot ? 'hl.spot' : 'hl.perp')} · ${side}`,
    badge: spot ? mode : `${mode} · ${lev}×`,
    demo: demo ? tr('hl.demo') : null,
    basis: demo ? tr('hl.demoBasis', { n: lev }) : s.acct_ret ? `${basis} · ${tr('hl.acct', { p: pct(s.acct_ret) })}` : basis,
    entry: tr('hl.entry'),
    exit: tr('hl.exit'),
    stamp: tr('hl.stamp'),
    mae: tr('hl.maeShort'),
    mfe: tr('hl.mfeShort'),
    labels: { hold: tr('hl.hold'), price: tr('hl.price'), now: tr('hl.priceNow'), mae: tr('hl.mae'), mfe: tr('hl.mfe') },
    hold: holdText,
    date: fmtTime(s.close_time, { year: true }).slice(0, 10),
    foot: tr(demo ? 'hl.footDemo' : s.mode === 'live' ? 'hl.footLive' : 'hl.footSim'),
    pct: (v: number) => pct(v),
    money: (v: number) => `${v > 0 ? '+' : v < 0 ? '−' : ''}${Math.abs(v).toLocaleString(numLocale(), { minimumFractionDigits: 2, maximumFractionDigits: 2 })} USDT`,
    price: (v: number) => fmtNum(v, dec),
    time: hhmm,
  }
}

/** 示例：用录到的一段行情，按 10 倍、不计手续费 */
export function demoSnap(d: { instrument: string; side: 'long' | 'short'; from: number; to: number; entry: number; exit: number; move: number }): HlSnap {
  return {
    trade_id: 'demo', instrument: d.instrument, side: d.side, origin: 'manual', mode: 'sim', leverage: 10,
    open_time: d.from, close_time: d.to, entry: d.entry, exit: d.exit, roe: d.move * 10, acct_ret: 0, mae: 0, mfe: 0, path_known: false, net: 0, qty: 0,
  }
}
