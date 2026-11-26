// 落地页（3.15、3.16，D79）：没登录先看到它。一屏不滚动，用标签切换：全市场方块图、强平分布，都是真实画面。
// 原来的示例仓位、策略净值两个标签去掉了：不做宣传（D90 第 4 条）。
// 全市场页（导航"市场"）是同一张方块图，占满整页。
import { lazy, Suspense, useEffect, useMemo, useState, type ReactNode } from 'react'
import { useT, tr, fmtCompactL, type Key } from '../lib/i18n'
import { BRAND } from '../lib/brand'
import { go } from '../lib/route'
import { isRunning, useFollows, usePoll, type StrategyState } from '../lib/follow'
import { baseOf, priceDecimals } from '../lib/format'
import { useMarket } from '../lib/useMarket'
import { reducedMotion, EASE_OUT } from '../lib/anchor'
import { clock } from '../lib/gfx'
import { Bar } from '../ui/Bar'
import { Seg } from '../ui/Seg'
import { MapLegend, MarketMap, mapStats, useMapData, type MapNode, type MapSnap, type PickAt } from '../charts/MarketMap'
import { Scope } from '../charts/Scope'
import { useCandles, useOnchain } from './Market'
import { HeatBody, HeatTools, useHeatFloor, useHeatInstruments, useHeatLayers, useHeatSpan } from './HeatPanel'
import { FlowBody } from './FlowPanel'
import { ChainBody } from './ChainPanel'
import { VenuesBody } from './VenuesPanel'
import { SystemBody } from './SystemPanel'
import '../styles/market.css'
import '../styles/trade.css'
import '../styles/account.css'
import '../styles/ai.css'
import '../styles/landing.css'
import { locDec } from '../lib/tz'

/** 点一块：方块放大过渡成它的交易页。页面和数据立刻切过去，这层动画只是给眼睛看的（D68）。 */
export function pickCoin(n: MapNode, at: PickAt) {
  const url = `trade&i=${encodeURIComponent(n.i)}`
  if (!reducedMotion()) {
    const el = document.createElement('div')
    el.className = 'mb-zoom'
    document.body.appendChild(el)
    const box = (x: number, y: number, w: string, h: string) => ({ left: `${x}px`, top: `${y}px`, width: w, height: h })
    const a = el.animate([
      { ...box(at.x, at.y, `${at.w}px`, `${at.h}px`), background: at.color, opacity: 1 },
      { ...box(0, 0, '100vw', '100vh'), background: '#111113', opacity: 1, offset: 0.7 },
      { ...box(0, 0, '100vw', '100vh'), background: '#111113', opacity: 0 },
    ], { duration: 340, easing: EASE_OUT, fill: 'forwards' })
    const done = () => el.remove()
    a.finished.then(done, done)
  }
  go(url)
}

const pct = (v: number | null) => (v == null ? '—' : (v > 0 ? '+' : v < 0 ? '−' : '') + locDec(Math.abs(v).toFixed(1)) + '%')
const tone = (v: number | null) => (v == null ? '' : v > 0 ? 'up' : v < 0 ? 'down' : '')

function MapMeta({ snap }: { snap: MapSnap | null }) {
  const t = useT()
  if (!snap || !snap.nodes.length) return null
  return (
    <span className="mb-meta">
      <span className="mb-meta-l">{t('mb.scope', { total: snap.total, min: fmtCompactL(snap.min_vol), n: snap.nodes.length })}</span>
      <span className="mb-meta-s">{t('mb.scopeS', { total: snap.total, n: snap.nodes.length })}</span>
      <span className="num mb-clock">{clock(snap.t)}</span>
    </span>
  )
}

function MapStrip({ snap }: { snap: MapSnap | null }) {
  const t = useT()
  const s = useMemo(() => mapStats(snap?.nodes ?? []), [snap])
  const has = !!snap?.nodes.length
  const Ro = ({ l, v, cls, opt }: { l: string; v: ReactNode; cls?: string; opt?: boolean }) => (
    <div className={`mb-ro ${opt ? 'opt' : ''}`}><span>{l}</span><b className={`num ${cls ?? ''}`}>{has ? v : '—'}</b></div>
  )
  return (
    <div className="mb-strip">
      <div className="mb-ros">
        <Ro l={t('mb.upDown')} v={<><i className="up">{s.up}</i><i className="t4"> / </i><i className="down">{s.down}</i></>} />
        <Ro l={t('mb.median')} v={pct(s.median)} cls={tone(s.median)} />
        <Ro l="BTC" v={pct(s.btc)} cls={tone(s.btc)} />
        <Ro l="ETH" v={pct(s.eth)} cls={tone(s.eth)} opt />
        <Ro l={t('mb.total')} v={fmtCompactL(s.total)} opt />
        <Ro l={t('mb.hot')} v={s.hot} opt />
      </div>
      <MapLegend />
    </div>
  )
}

type Tab = 'map' | 'liq' // 示例仓位、策略净值两个标签去掉了：不做宣传（D90 第 4 条）

export default function Landing() {
  const t = useT()
  const { snap, conn } = useMapData()
  const [tab, setTab] = useState<Tab>('map')
  useEffect(() => { document.title = BRAND }, [])
  return (
    <div className="lp">
      <Bar page="landing" />
      <div className="lp-head">
        <Seg label={t('lp.tabs')} value={tab} onChange={setTab} options={[
          { value: 'map', label: t('lp.tab.map') },
          { value: 'liq', label: t('lp.tab.liq') },
        ]} />
        {tab === 'map' && <MapMeta snap={snap} />}
      </div>
      <div className="lp-pane" key={tab}>
        {tab === 'map' && (
          <>
            <MapStrip snap={snap} />
            <MarketMap snap={snap} conn={conn} onPick={pickCoin} />
          </>
        )}
        {tab === 'liq' && <Shot note={t('lp.note.liq')}><ChainShot /></Shot>}
      </div>
      <footer className="lp-foot">{t('lp.risk')}</footer>
    </div>
  )
}

/** 全市场页（导航"市场"）：同一张方块图。跟随中的人，AI 的持仓用瞄准框标出来 */
// 3D 地形只在开屏（首页第一屏）出现，这里不再有地形页（D87，用户 2026-11-09）
type OvTab = 'map' | 'today' | 'screen' | 'heat' | 'flow' | 'chain' | 'venues' | 'system'
const TodayTab = lazy(() => import('./market/Today').then((m) => ({ default: m.Today })))
const ScreenTab = lazy(() => import('./market/Screener').then((m) => ({ default: m.Screener })))
export function Overview() {
  const t = useT()
  const { snap, conn } = useMapData()
  const holds = useAiHolds()
  useEffect(() => { document.title = `${tr('bar.overview')} · ${BRAND}` }, [])
  const [tab, setTabRaw] = useState<OvTab>(() => {
    const v = new URLSearchParams(location.search).get('tab') ?? (() => { try { return localStorage.getItem('qs.ovTab') } catch { return null } })()
    return v === 'today' || v === 'screen' || v === 'heat' || v === 'flow' || v === 'chain' || v === 'venues' || v === 'system' ? v : 'map'
  })
  const setTab = (v: OvTab) => { setTabRaw(v); try { localStorage.setItem('qs.ovTab', v) } catch { /* 忽略 */ } }
  return (
    <div className="lp">
      <Bar page="market" />
      <div className="lp-head">
        <Seg label={t('ov.tabs')} value={tab} onChange={setTab} options={[
          { value: 'map', label: t('lp.tab.map') },
          { value: 'today', label: t('ov.tab.today') },
          { value: 'screen', label: t('ov.tab.screen') },
          { value: 'heat', label: t('ov.tab.heat') },
          { value: 'flow', label: t('ov.tab.flow') },
          { value: 'chain', label: t('ov.tab.chain') },
          { value: 'venues', label: t('ov.tab.venues') },
          { value: 'system', label: t('ov.tab.system') },
        ]} />
        {tab === 'map' && <MapMeta snap={snap} />}
      </div>
      <div className="lp-pane" key={tab}>
        {tab === 'map' && <>
          <MapStrip snap={snap} />
          <MarketMap snap={snap} conn={conn} onPick={pickCoin} holds={holds} />
        </>}
        {tab === 'today' && <Suspense fallback={null}><TodayTab /></Suspense>}
        {tab === 'screen' && <Suspense fallback={null}><ScreenTab /></Suspense>}
        {tab === 'heat' && <OverviewHeat />}
        {tab === 'flow' && <OverviewFlow />}
        {tab === 'chain' && <OverviewChain />}
        {tab === 'venues' && <VenuesBody />}
        {tab === 'system' && <SystemBody />}
      </div>
    </div>
  )
}

/** 市场页的频谱：6 个重点币之间切换，图占满 */
function OverviewHeat() {
  const t = useT()
  const list = useHeatInstruments()
  const [inst, setInst] = useState(lastPerp)
  const { snap } = useMarket(inst)
  const cur = snap?.instruments.find((i) => i.instrument === inst)
  const dec = cur ? priceDecimals(Number(cur.last)) : 1
  const [span, setSpan] = useHeatSpan()
  const [layers, setLayers] = useHeatLayers()
  const [floor, setFloor] = useHeatFloor()
  const oc = useOnchain(layers.chain || layers.whales ? baseOf(inst) : null)
  useEffect(() => { if (list.length && !list.includes(inst)) setInst(list.includes('BTC-PERP') ? 'BTC-PERP' : list[0]) }, [list, inst])
  return (
    <section className="display lp-heat">
      <div className="display-head">
        {list.length > 0 && <Seg label={t('mk.heat')} value={inst} onChange={setInst} options={[...list].sort(byFocus).map((x) => ({ value: x, label: baseOf(x) }))} />}
        <span className="display-sub">{t('mk.heatSub')}</span>
        <div className="display-tools"><HeatTools span={span} onSpan={setSpan} layers={layers} onLayers={setLayers} floor={floor} onFloor={setFloor} hasOwn={false} /></div>
      </div>
      <div className="display-body"><HeatBody inst={inst} dec={dec} span={span} layers={layers} floor={floor} chain={oc?.overlay} /></div>
    </section>
  )
}
/** 市场页的链上：Hyperliquid 公开的持仓 */
function OverviewChain() {
  const t = useT()
  const [inst, setInst] = useState(lastPerp)
  const { snap } = useMarket(inst)
  const cur = snap?.instruments.find((i) => i.instrument === inst)
  const dec = cur ? priceDecimals(Number(cur.last)) : 1
  return (
    <section className="display lp-heat">
      <div className="display-head">
        <Seg label={t('ov.tab.chain')} value={inst} onChange={setInst} options={FOCUS.map((x) => ({ value: `${x}-PERP`, label: x }))} />
        <span className="display-sub">{t('cn.sub')}</span>
      </div>
      <div className="display-body"><ChainBody coin={baseOf(inst)} dec={dec} /></div>
    </section>
  )
}

/** 市场页的订单流：足迹图只有 6 个重点币 */
const FLOW_IVS = [{ value: '1m', ms: 60_000 }, { value: '5m', ms: 300_000 }, { value: '15m', ms: 900_000 }, { value: '1h', ms: 3_600_000 }]
function OverviewFlow() {
  const t = useT()
  const list = useHeatInstruments()
  const [inst, setInst] = useState(lastPerp)
  const [ivKey, setIvKey] = useState(() => { try { const v = localStorage.getItem('qs.flowIv'); return FLOW_IVS.some((x) => x.value === v) ? v! : '5m' } catch { return '5m' } })
  const pick = (v: string) => { setIvKey(v); try { localStorage.setItem('qs.flowIv', v) } catch { /* 忽略 */ } }
  const iv = FLOW_IVS.find((x) => x.value === ivKey)!
  const { snap, version } = useMarket(inst)
  const cur = snap?.instruments.find((i) => i.instrument === inst)
  const dec = cur ? priceDecimals(Number(cur.last)) : 1
  const { bars, more } = useCandles(inst, iv.value, cur)
  useEffect(() => { if (list.length && !list.includes(inst)) setInst(list.includes('BTC-PERP') ? 'BTC-PERP' : list[0]) }, [list, inst])
  return (
    <section className="display lp-heat">
      <div className="display-head">
        {list.length > 0 && <Seg label={t('mk.flow')} value={inst} onChange={setInst} options={[...list].sort(byFocus).map((x) => ({ value: x, label: baseOf(x) }))} />}
        <span className="display-sub">{t('mk.flowSub2')}</span>
        <div className="display-tools"><div className="head-iv"><Seg label={t('mk.interval')} options={FLOW_IVS.map((x) => ({ value: x.value, label: t(`mk.iv.${x.value}` as Key) }))} value={ivKey} onChange={pick} /></div></div>
      </div>
      <div className="display-body"><FlowBody inst={inst} interval={iv.ms} dec={dec} candles={bars} more={more} version={version} /></div>
    </section>
  )
}
const FOCUS = ['BTC', 'ETH', 'SOL', 'BNB', 'XRP', 'DOGE']
// 上次在交易页看的币，换成它的合约（订单流、链上只有合约的数据）。11-16 走查：上次看的是 ETH 现货，订单流拿现货去查持仓量和多空比，服务器回 502
function lastPerp(): string {
  try { const c = baseOf(localStorage.getItem('qs.inst') ?? ''); return FOCUS.includes(c) ? `${c}-PERP` : 'BTC-PERP' } catch { return 'BTC-PERP' }
}
const byFocus = (a: string, b: string) => FOCUS.indexOf(baseOf(a)) - FOCUS.indexOf(baseOf(b))

function useAiHolds(): Map<string, 'long' | 'short'> | undefined {
  const running = useFollows()?.find(isRunning)
  const state = usePoll<StrategyState>(running ? `/api/strategies/${running.strategy}` : null, 15000).data
  return useMemo(() => {
    if (!state || state.locked !== false) return undefined
    const m = new Map<string, 'long' | 'short'>()
    for (const p of state.state.positions) {
      const q = Number(p.qty)
      if (q !== 0) m.set(p.instrument, q > 0 ? 'long' : 'short')
    }
    return m
  }, [state])
}

function Shot({ note, children }: { note: string; children: ReactNode }) {
  return (
    <>
      <p className="lp-note">{note}</p>
      <div className="display lp-shot"><div className="display-body">{children}</div></div>
    </>
  )
}

/** 真实的链上强平分布：和交易页同一张图 */
function ChainShot() {
  const t = useT()
  const { snap, version } = useMarket()
  const cur = snap?.instruments.find((i) => i.instrument === 'BTC-PERP')
  const { bars, more } = useCandles('BTC-PERP', '1h')
  const oc = useOnchain('BTC')
  const dec = cur ? priceDecimals(Number(cur.last)) : 1
  return (
    <>
      <Scope mode="candle" candles={bars} interval={3_600_000} span={3_600_000 * 48} dec={dec} live version={version} onchain={oc?.overlay} pan={{ onNeedMore: more }} />
      {!oc?.overlay && <span className="lp-tag">{t('lp.scanning')}</span>}
    </>
  )
}

