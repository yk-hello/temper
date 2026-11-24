// 介绍页：一台频谱分析仪（D85）。不用登录，数字都是实时的，也没有收益数字。弱设备也跑得动。
import { useCallback, useEffect, useRef, useState } from 'react'
import { Analyzer, type AnalyzerRead } from '../charts/Analyzer'
import { useT, fmtCompactL, listSep } from '../lib/i18n'
import { linkProps } from '../lib/route'
import { Bar } from '../ui/Bar'
import { HomeSwitch } from '../ui/HomeSwitch'
import { priceDecimals } from '../lib/format'
import { fmtNum } from '../charts/scale'
import '../styles/about.css'
import { numLocale } from '../lib/tz'
import { BRAND } from '../lib/brand'

const COINS = ['BTC', 'ETH', 'SOL', 'BNB', 'XRP', 'DOGE']
const NAMES: Record<string, string> = { 'binance-usdm': 'Binance', 'binance-spot': 'Binance', hyperliquid: 'Hyperliquid', bybit: 'Bybit', okx: 'OKX' }

interface Sys { lines: { venue: string; rate: number; ms: number }[]; polls: { id: string; n?: number }[]; heat: number }

export default function About() {
  const t = useT()
  const [ch, setCh] = useState(() => { try { return Math.max(0, COINS.indexOf(localStorage.getItem('qs.aboutCh') ?? 'BTC')) } catch { return 0 } })
  const [read, setRead] = useState<AnalyzerRead>({ mid: 0, state: 'wait', lite: false })
  const [sys, setSys] = useState<Sys | null>(null)
  const clockRef = useRef<HTMLElement>(null)
  const coin = COINS[ch]
  const dec = read.mid > 0 ? priceDecimals(read.mid) : 1
  // 换币之前的价格不能留在新币的读数上
  const onRead = useCallback((r: AnalyzerRead) => setRead(r), [])
  const pick = useCallback((i: number) => {
    setCh(i)
    setRead({ mid: 0, state: 'wait', lite: false })
    try { localStorage.setItem('qs.aboutCh', COINS[i]) } catch { /* 忽略 */ }
  }, [])
  useEffect(() => { document.title = `${t('ab.title')} · ${BRAND}` }, [t])
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return
      const n = Number(e.key)
      if (n >= 1 && n <= COINS.length) pick(n - 1)
    }
    window.addEventListener('keydown', key)
    return () => window.removeEventListener('keydown', key)
  }, [pick])
  useEffect(() => {
    let live = true
    const go = async () => {
      try {
        const r = await fetch('/api/system')
        if (r.ok && live) setSys((await r.json()) as Sys)
      } catch { /* 下一轮再取 */ }
    }
    void go()
    const id = window.setInterval(go, 2000)
    return () => { live = false; window.clearInterval(id) }
  }, [])
  const canTerrain = !!(document.createElement('canvas').getContext('webgl2'))
  const venues = [...new Set([...(sys?.lines ?? []).map((l) => l.venue), ...(sys?.polls ?? []).map((p) => p.id)].map((v) => NAMES[v]).filter(Boolean))]
  const rate = (sys?.lines ?? []).reduce((a, l) => a + l.rate, 0)
  const primary = sys?.lines?.find((l) => l.venue === 'binance-usdm') ?? sys?.lines?.[0]
  const chain = sys?.polls?.find((p) => p.id === 'chain')?.n
  const buy = read.flow?.[0] ?? 0, sell = read.flow?.[1] ?? 0
  const buyPct = buy + sell > 0 ? (buy / (buy + sell)) * 100 : 0
  return (
    <div className="ab">
      <Bar page="about" />

      <section className="ab-read">
        <div className="ab-price">
          <span className="ab-sym">{coin}</span>
          <b data-ab-price>{read.mid > 0 ? fmtNum(read.mid, dec) : '—'}</b>
        </div>
        <dl className="ab-ros">
          <div><dt>{t('ab.span')}</dt><dd>±0.60%</dd></div>
          <div><dt>{t('ab.flow')}</dt><dd>{buy + sell > 0 ? `${buyPct.toFixed(0)}% / ${(100 - buyPct).toFixed(0)}%` : '—'}</dd></div>
          <div><dt>{t('ab.clock')}</dt><dd ref={clockRef as React.RefObject<HTMLElement>} data-ab-clock>—</dd></div>
        </dl>
      </section>

      <section className="ab-screen">
        <Analyzer key={coin} inst={`${coin}-PERP`} dec={dec} onRead={onRead} clockRef={clockRef} />
      </section>

      <nav className="ab-ch" aria-label={t('ab.channel')}>
        {COINS.map((c, i) => (
          <button type="button" key={c} aria-pressed={i === ch} data-ch={c} onClick={() => pick(i)}>{c}</button>
        ))}
        <a className="ab-go" {...linkProps(`trade&i=${coin}-PERP`)} data-ab-trade>{t('ab.trade', { coin })}</a>
        <span className="ab-axis">{t('ab.axis')}</span>
        {canTerrain && <HomeSwitch current="spec" />}
      </nav>

      <section className="ab-spec">
        <dl>
          <div><dt>{t('ab.s.feed')}</dt><dd>{venues.length ? venues.join(listSep()) : t('ab.s.sim')}</dd><dd className="n">{sys ? t('ab.perSec', { n: rate >= 100 ? rate.toFixed(0) : rate.toFixed(1) }) : '—'}</dd></div>
          <div><dt>{t('ab.s.book')}</dt><dd>{t('ab.s.bookD')}</dd><dd className="n">{sys ? t('ab.coins', { n: sys.heat }) : '—'}</dd></div>
          {chain !== undefined && <div><dt>{t('ab.s.chain')}</dt><dd>{t('ab.s.chainD')}</dd><dd className="n">{t('ab.addrs', { n: chain >= 10000 ? fmtCompactL(chain) : chain.toLocaleString(numLocale()) })}</dd></div>}
          <div><dt>{t('ab.s.lag')}</dt><dd>{t('ab.s.lagD')}</dd><dd className="n">{primary ? `${primary.ms < 1 ? '<1' : primary.ms.toFixed(0)} ms` : '—'}</dd></div>
          <div><dt>{t('ab.s.trade')}</dt><dd>{t('ab.s.tradeD')}</dd><dd className="n">{t('ab.s.tradeN')}</dd></div>
        </dl>
        <p className="ab-cta">{t('ab.risk')}</p>
      </section>
    </div>
  )
}
