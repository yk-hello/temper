// 交易页 v3（D38：按交易员的习惯布局；模块 2 加上下单）。
//   顶部一条：品种、最新价、涨跌、标记价、资金费和倒计时、24 小时高低和成交额
//   中间：左边图表（K 线 / 阶梯线），中间盘口（卖上买下，点价格填进下单价），右边下单区
//   底部：持仓、委托、历史委托、成交、资金费、AI 决策、AI 策略
// 窄屏时盘口和下单区并入底部标签页；手机上全部纵向排列。整页任何尺寸都不滚动（D39）。
import { evLong, evVolOk, nextEvent, useEconEvents, type EconEvent } from '../lib/calendar'
import { locDec, numLocale } from '../lib/tz'
import { BRAND } from '../lib/brand'
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useMarket, useNow, type BookFrame } from '../lib/useMarket'
import { baseOf, fmtCountdown, fmtFunding, fmtPct, fmtPrice, priceDecimals, sign, spreadBps } from '../lib/format'
import { useT, useLang, tr, fmtCompactL, fmtTime, getLang, type Key } from '../lib/i18n'
import { useSession } from '../lib/session'
import { act, decimalsOf, rejectText, reloadSpecs, setTicket, usePaperAccounts, useSpecs, useTradeAccount, type AccountView } from '../lib/trade'
import { toast, usePrivate } from '../lib/privacy'
import { fmtAmt, OrderPanel } from './trade/OrderPanel'
import { CancelAllConfirm, Fills, Funding, OpenOrders, OrderHistory, Positions } from './trade/Lists'
import type { InstrumentState } from '../lib/types'
import { useCandles } from '../lib/candles'
import { GridPick, MainHead, MiniChart, useGrid } from './trade/ChartGrid'
import { useInd, useIndCfg, useShapes, type ToolKind } from '../lib/chartPrefs'
import { DrawMenu, IndicatorMenu } from './trade/ChartTools'
import { Scope, type OnchainOverlay, type SayPart, type TradeLine, type TradeMarker, type TradeOverlay, type ChainAction } from '../charts/Scope'
import { Seg } from '../ui/Seg'
import { HeatBody, HeatTools, useHeatFloor, useHeatLayers, useHeatSpan } from './HeatPanel'
import { FlowBody } from './FlowPanel'
import { useOIFlow } from '../lib/flow'
import { Price } from '../ui/Price'
import { Money } from '../ui/Money'
import { Bar } from '../ui/Bar'
import { useLayout } from '../lib/layout'
import { flyText, growFrom, lastAnchor, reducedMotion, shrinkTo, type Anchor } from '../lib/anchor'
import { getPrefs, hotKey, hotLabel } from '../lib/prefs'
import { clock } from '../lib/gfx'
import { isRunning, useFollows, usePoll, type Decision, type StrategyState, type StrategyStatus } from '../lib/follow'
import { linkProps } from '../lib/route'
import { hms, lockLeft, ruleName, useDiscipline, useTick } from '../lib/discipline'
import '../styles/risk.css'
import { DecisionFeed, Locked } from './ai/Decisions'
import { recMarkers } from './ai/FollowPanel'
import { useMarketList } from '../lib/coins'
import { Select } from '../ui/Select'
import { SPOT_COINS, isSpot as spotInst, useSpot, useSpotSpecs, type SpotView } from '../lib/spot'
import { SpotAssets, SpotFills, SpotOrders, SpotPanel } from './trade/SpotPanel'
import { BotList, BotPanel } from './trade/BotPanel'
import { PriceMenu, type MenuAt } from './trade/PriceMenu'
import { PaperOnly } from '../ui/AccountSwitcher'
import { DomLadder, groupBook, groupOptions, useAlgos } from './trade/OrderTools'
import { useBots, type Bot, type BotKind, type BotParams, type BotPlan } from '../lib/bots'
import '../styles/market.css'
import '../styles/trade.css'
import '../styles/account.css'

// K 线周期（D59：和交易所一样按周期选；K 线宽度固定，拖动看历史，滚轮缩放）
const IVS = [
  { value: '1s', ms: 1000 }, { value: '1m', ms: 60_000 }, { value: '5m', ms: 300_000 }, { value: '15m', ms: 900_000 },
  { value: '1h', ms: 3_600_000 }, { value: '4h', ms: 14_400_000 }, { value: '1d', ms: 86_400_000 },
]
const IV_KEY = 'qs.iv'
function savedIv(): string {
  try { const v = localStorage.getItem(IV_KEY); return IVS.some((x) => x.value === v) ? v! : '1m' } catch { return '1m' }
}

type ChartMode = 'candle' | 'step' | 'heat' | 'flow'

export default function Market() {
  const t = useT()
  const lang = useLang()
  // 品种：网址里带的（从点阵点进来）优先，其次上次看的（D75 第 6 条）
  const [selected, setSelectedRaw] = useState(() => {
    const u = new URLSearchParams(location.search).get('i')
    if (u) return u
    try { return localStorage.getItem('qs.inst') || 'BTC-PERP' } catch { return 'BTC-PERP' }
  })
  const setSelected = (v: string) => { setSelectedRaw(v); try { localStorage.setItem('qs.inst', v) } catch { /* 忽略 */ } }
  const { snap, conn, history, version, book } = useMarket(selected)
  const now = useNow(1000)
  const [ivKey, setIvKey] = useState(savedIv)
  const pickIv = (v: string) => { setIvKey(v); try { localStorage.setItem(IV_KEY, v) } catch { /* 忽略 */ } }
  const spot = spotInst(selected)
  const [mode0, setModeRaw] = useState<ChartMode>(() => { try { const v = new URLSearchParams(location.search).get('chart') ?? localStorage.getItem('qs.chartMode'); return v === 'heat' || v === 'flow' ? v : 'candle' } catch { return 'candle' } })
  const setMode = (v: ChartMode) => { setModeRaw(v); try { localStorage.setItem('qs.chartMode', v === 'heat' || v === 'flow' ? v : 'candle') } catch { /* 忽略 */ } }
  const [heatSpan, setHeatSpan] = useHeatSpan()
  const [heatLayers, setHeatLayers] = useHeatLayers()
  const [heatFloor, setHeatFloor] = useHeatFloor()
  const [tool, setTool] = useState<ToolKind | null>(null)
  // 命令面板、自选列切品种；全部撤单
  useEffect(() => {
    const pick = (e: Event) => setSelected((e as CustomEvent<string>).detail)
    const cancel = () => { if (keyState.current.view?.orders.length) setCancelAll(true) }
    window.addEventListener('qs:pick', pick)
    window.addEventListener('qs:cancel-all', cancel)
    return () => { window.removeEventListener('qs:pick', pick); window.removeEventListener('qs:cancel-all', cancel) }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps
  // 自选（2.29）：大屏最左一列；窄屏在标签页里；笔记本在品种下拉里
  const [watch, setWatch] = useState<string[]>(() => { try { const v = JSON.parse(localStorage.getItem('qs.watch') ?? 'null'); return Array.isArray(v) ? v : ['BTC-PERP', 'ETH-PERP', 'SOL-PERP'] } catch { return ['BTC-PERP', 'ETH-PERP', 'SOL-PERP'] } })
  const toggleWatch = (x: string) => setWatch((w) => { const n = w.includes(x) ? w.filter((y) => y !== x) : [...w, x]; try { localStorage.setItem('qs.watch', JSON.stringify(n)) } catch { /* 忽略 */ } return n })
  const [chain, setChain] = useState<'on' | 'off'>('on')
  // 盘口 / 最新成交：记住上次看的
  const [bookGroup, setBookGroup] = useState(0) // 盘口合并档位，0 = 不合并
  const [bookTab, setBookTabRaw] = useState<'book' | 'trades' | 'dom'>(() => { try { const v = localStorage.getItem('qs.bookTab'); return v === 'trades' || v === 'dom' ? v : 'book' } catch { return 'book' } })
  const setBookTab = (v: 'book' | 'trades' | 'dom') => { setBookTabRaw(v); try { localStorage.setItem('qs.bookTab', v) } catch { /* 忽略 */ } }
  const [ocAbout, setOcAbout] = useState(false)
  const [bookRows, setBookRows] = useState(0) // 盘口现在放得下几档（按高度算）
  const lay = useLayout()
  const narrow = useMedia('(max-width: 859px)') // 窄屏时盘口在底部标签页里，避免同时渲染两份。分界线 860：iPhone Duo 展开横着 890 宽要用三栏（11-18）
  const vh = () => window.innerHeight
  // 现货和合约分开列；图表模式里频谱和足迹只有合约有
  const mode: ChartMode = spot && (mode0 === 'heat' || mode0 === 'flow') ? 'candle' : mode0
  const instruments = useMemo(() => (snap?.instruments ?? []).filter((i) => spotInst(i.instrument) === spot), [snap, spot])
  // 刚点开的新品种：后端要过一下才开始推，先放一个空壳，图表和下单区立刻就是这个品种（不闪回 BTC）
  const cur = instruments.find((i) => i.instrument === selected) ?? (instruments.length ? { instrument: selected, symbol: '', venue: '', last: '', bid: '', ask: '', mark: '', change_24h_pct: '', quote_volume_24h: '', high_24h: '', low_24h: '', funding_rate: '', next_funding_time: 0 } as unknown as InstrumentState : undefined)
  const lastNum = Number(cur?.last) || 0
  const groupOpts = useMemo(() => {
    const tick = Number(specsTick(selected)) || 0
    return tick > 0 && lastNum > 0 ? groupOptions(tick, lastNum) : []
  }, [selected, lastNum > 0]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { setBookGroup(0) }, [selected])
  const groupedBook = useMemo(() => (book && bookGroup > 0 ? { ...book, asks: groupBook(book.asks, bookGroup, true), bids: groupBook(book.bids, bookGroup, false) } : book), [book, bookGroup])
  const ivWant = IVS.find((x) => x.value === ivKey)!
  const iv = mode === 'flow' && ivWant.ms < 60_000 ? IVS[1] : ivWant // 足迹图最短 1 分钟
  const { bars: candles, more } = useCandles(cur?.instrument, iv.value, cur) // 最后一根跟着 0.1 秒的实时价走
  // 多图同屏（18 第 9.2 条）：宽屏、K 线模式才分格
  const grid = useGrid(spot, cur?.instrument, instruments)
  const gridOn = mode === 'candle' && !narrow && grid.n > 1
  const dec = cur ? priceDecimals(Number(cur.last)) : 1
  const status = snap?.status
  const feedDown = conn === 'open' && !!status && Object.entries(status.connections).some(([k, v]) => !v && (k === 'spot') === spot)
  // 行情积压（后端测的，扣掉了时钟偏差）：超过 2 秒就不说"实时"，直接说延迟几秒
  const lagS = Math.max(0, ...Object.values(status?.lag_ms ?? {})) / 1000
  const feedLag = conn === 'open' && !feedDown && lagS >= 2
  const steps = cur ? history.get(cur.instrument) : undefined
  const onchain = useOnchain(!spot && (mode === 'heat' ? heatLayers.chain || heatLayers.whales : chain === 'on') && cur ? baseOf(cur.instrument) : null)
  const ivOpts = useMemo(() => IVS.map((x) => ({ ...x, label: t(`mk.iv.${x.value}` as Key) })), [lang]) // eslint-disable-line react-hooks/exhaustive-deps

  // 交易：当前模拟账户、账户全貌、品种规格
  const session = useSession()
  // 合约交易可以用实盘账户（D89）；现货和机器人只在模拟账户上
  const { current: perpCur } = usePaperAccounts(true)
  const { current: paperCur } = usePaperAccounts()
  const current = spot ? paperCur : perpCur
  // 顶栏选的是交易所账户：现货和机器人不悄悄换成模拟账户，说明一句、给切换按钮（D73）
  const paperOnly = (what: 'spot' | 'bot') => (perpCur?.live ? <PaperOnly what={what} current={perpCur} first={paperCur} /> : null)
  const { view } = useTradeAccount(session.state === 'in' && current ? current.id : null)
  const specs = useSpecs()
  rememberTicks(specs)
  // 现货余额只在看现货品种时轮询（合约页用不到，11-15 走查发现每 1.4 秒白问一次）
  const spotView = useSpot(spot && session.state === 'in' && paperCur && !perpCur?.live ? paperCur.id : null)
  const spotSpecs = useSpotSpecs()
  rememberTicks(spotSpecs)
  const inst = cur?.instrument ?? selected
  // 现货的价格单位、数量单位比合约细（BTC 现货 0.01 / 0.00001）：盘口和最新成交按现货的规格显示小数，
  // 不然几档价格取整成同一个数、小数量显示成 0.000（11-16 走查）。合约不变
  const bdec = spot && spotSpecs?.[inst]?.tick ? Math.max(dec, decimalsOf(spotSpecs[inst].tick)) : dec
  const qdec = spot && spotSpecs?.[inst]?.step ? Math.max(3, decimalsOf(spotSpecs[inst].step)) : 3
  const [shapes, setShapes] = useShapes(inst)
  const indCfg = useIndCfg()
  const econ = useEconEvents(indCfg.events.on)
  const next = nextEvent(econ, Date.now(), 48 * 3_600_000)
  const oiFlow = useOIFlow(!spot && indCfg.oi.on && mode === 'candle' && cur ? cur.instrument : null, iv.ms)
  const oiSeries = useMemo(() => {
    if (!oiFlow) return undefined
    const m = new Map(oiFlow.points.map((p) => [p.t, p.usd]))
    return candles.map((c) => m.get(c.t) ?? null)
  }, [oiFlow, candles])
  const ind = useInd(candles, oiSeries)
  // 规格只取了当时在跑的品种：新点开的币要再取一次（后端开始收行情后就有）
  useEffect(() => {
    if (!specs || specs[inst] || spot) return
    const id = window.setInterval(reloadSpecs, 1500)
    reloadSpecs()
    return () => window.clearInterval(id)
  }, [specs, inst])
  const [cancelAll, setCancelAll] = useState(false)
  const [priceMenu, setPriceMenu] = useState<MenuAt | null>(null) // 图上右键下单（18 第 6.5 条）

  // 机器人：右边"下单 / 机器人"切换；设置时图上跟着画（D83 第 10 条）
  const [sideTab, setSideTab] = useState<'order' | 'bot'>('order')
  const [botKind, setBotKind] = useState<BotKind>(spot ? 'spot_grid' : 'perp_grid')
  const [botForm, setBotForm] = useState<BotParams | null>(null)
  const [botPlan, setBotPlan] = useState<BotPlan | null>(null)
  const botsData = useBots(session.state === 'in')
  const allBots = botsData?.bots ?? []
  const liveBot = allBots.find((b) => b.params.instrument === inst && b.account === current?.id && b.status !== 'stopped') ?? null
  // 底部"机器人"标签和持仓、委托一样只列当前账户的（11-17 走查：切到别的账户，列的还是原来账户的定投）
  // 当前账户在跟 AI：图上画 AI 开平仓点和目标仓位（3.21）
  const follows = useFollows()
  const follow = current ? follows?.find((f) => f.account === current.id) ?? null : null
  const followOn = !!follow && isRunning(follow)
  const fDec = usePoll<Decision[]>(followOn ? `/api/strategies/${follow!.strategy}/decisions` : null, 10000).data
  const fState = usePoll<StrategyState>(followOn ? `/api/strategies/${follow!.strategy}` : null, 5000).data

  // 图上自己的交易线：持仓开仓价（带浮盈）、强平价、挂单、条件单、止盈止损；挂单、条件单、止盈止损可以拖动改价
  const priv = usePrivate()
  const tradeOverlay = useMemo<TradeOverlay | undefined>(() => {
    if (spot) return spotOverlay(spotView, inst, spotSpecs?.[inst]?.tick)
    if (!view || !current) return undefined
    const spec = specs?.[inst]
    const pd = spec ? decimalsOf(spec.tick) : dec
    const amt = (s: string) => (priv ? '••••' : s)
    const lines: TradeLine[] = []
    const p = view.positions.find((x) => x.instrument === inst)
    // 结果句（D72）：到哪个价、会发生什么、赚还是亏。方向词（涨到 / 跌到）由图表按现价判断
    const dirW = (up: boolean) => tr(up ? 'tl.s.up' : 'tl.s.down')
    const pxs = (v: number) => fmtPrice(String(v), pd)
    const money = (v: number) => (priv ? '••••' : locDec(Math.abs(v).toFixed(2)))
    // 在 px 平掉整个持仓的盈亏（不含手续费），止盈止损标签用
    const pnlAt = (px: number) => p ? (px - Number(p.entry)) * Number(p.qty) * (p.side === 'long' ? 1 : -1) : 0
    const outcome = (v: number): SayPart => ({ t: tr(v >= 0 ? 'tl.s.win' : 'tl.s.lose', { v: money(v) }), tone: v >= 0 ? 'up' : 'down' })
    if (p) {
      const long = p.side === 'long'
      const u = Number(p.upnl)
      const uTxt = (u > 0 ? '+' : u < 0 ? '−' : '') + locDec(Math.abs(u).toFixed(2))
      lines.push({ id: 'pos', kind: 'entry', price: Number(p.entry), buy: long, label: `${tr(long ? 'tl.long' : 'tl.short')} ${amt(p.qty)} · ${amt(uTxt)}`,
        hint: tr('tl.h.entry', { side: tr(long ? 'tl.long' : 'tl.short'), qty: amt(p.qty), px: fmtPrice(p.entry, pd), upnl: amt(uTxt) }),
        say: () => [{ t: tr('tl.s.hold', { side: tr(long ? 'tl.long' : 'tl.short'), qty: amt(p.qty) }) }, { t: tr(u >= 0 ? 'tl.s.upnlP' : 'tl.s.upnlN', { v: money(u) }), tone: u >= 0 ? 'up' : 'down' }] })
      if (Number(p.liq_px) > 0) lines.push({ id: 'liq', kind: 'liq', price: Number(p.liq_px), label: tr('tl.liq'), hint: tr('tl.h.liq', { px: fmtPrice(p.liq_px, pd) }),
        say: (px, up) => [{ t: tr('tl.s.liq', { dir: dirW(up), px: pxs(px) }) }, { t: tr('tl.s.liqW'), tone: 'warn' }] })
    }
    for (const o of view.orders) {
      if (o.instrument !== inst || o.source?.startsWith('bot:')) continue // 机器人的单画成网格线，不重复写
      const buy = o.side === 'buy'
      const left = String(Number((Number(o.qty) - Number(o.filled)).toFixed(8)))
      const sideW = tr(buy ? 'tl.h.buy' : 'tl.h.sell')
      const by = tr(o.trigger_by === 'last' ? 'tl.h.last' : 'tl.h.mark')
      if (o.type === 'limit') lines.push({ id: o.id, kind: 'limit', price: Number(o.price), buy, label: `${tr(buy ? 'tl.limitBuy' : 'tl.limitSell')} ${amt(left)}`, drag: true,
        hint: tr('tl.h.limit', { side: sideW, qty: amt(left), px: fmtPrice(o.price, pd) }),
        say: (px, up) => [{ t: tr('tl.s.limit', { dir: dirW(up), px: pxs(px), side: sideW, qty: amt(left) }) }] })
      else if (o.type === 'cond_market' || o.type === 'cond_limit') lines.push({ id: o.id, kind: 'cond', price: Number(o.trigger_px), buy, label: `${tr(buy ? 'tl.condBuy' : 'tl.condSell')} ${amt(o.qty)}`, drag: o.status === 'new',
        hint: tr('tl.h.cond', { by, px: fmtPrice(o.trigger_px, pd), exec: tr(o.type === 'cond_market' ? 'tl.h.mkt' : 'tl.h.lmt'), side: sideW, qty: amt(o.qty) }),
        say: (px, up) => [{ t: tr('tl.s.cond', { dir: dirW(up), px: pxs(px), side: sideW, qty: amt(o.qty) }) }] })
      else if (o.type === 'take_profit_market') lines.push({ id: o.id, kind: 'tp', price: Number(o.trigger_px), label: tr('tl.tp'), drag: o.status === 'new', hint: tr('tl.h.tp', { by, px: fmtPrice(o.trigger_px, pd) }),
        say: (px, up) => [{ t: tr('tl.s.tp', { dir: dirW(up), px: pxs(px) }) }, outcome(pnlAt(px))] })
      else if (o.type === 'stop_market') lines.push({ id: o.id, kind: 'sl', price: Number(o.trigger_px), label: tr('tl.sl'), drag: o.status === 'new', hint: tr('tl.h.sl', { by, px: fmtPrice(o.trigger_px, pd) }),
        say: (px, up) => [{ t: tr('tl.s.sl', { dir: dirW(up), px: pxs(px) }) }, outcome(pnlAt(px))] })
    }
    const acct = current.id
    const onDrag = async (l: TradeLine, price: number) => {
      const o = view.orders.find((x) => x.id === l.id)
      if (!o) return false
      const px = price.toFixed(pd)
      try {
        if (l.kind === 'limit') await act('PATCH', `/api/trade/${acct}/orders/${o.id}`, { price: px, qty: o.qty })
        else if (l.kind === 'cond') await act('PATCH', `/api/trade/${acct}/orders/${o.id}`, { trigger_px: px, qty: o.qty, price: o.type === 'cond_limit' ? o.price : '0' })
        else if (l.kind === 'tp' || l.kind === 'sl') await act('POST', `/api/trade/${acct}/tpsl`, { instrument: inst, [l.kind]: px, trigger_by: o.trigger_by ?? 'mark' })
        toast(tr('tl.moved', { what: l.label, px: fmtPrice(px, pd) }))
        return true
      } catch (e) {
        toast(rejectText(e))
        return false
      }
    }
    // AI 目标仓位线：写清楚 AI 现在想持有多少。理由和开平仓点在三角上，悬停看
    const aiPos = followOn ? fState?.state.positions.find((x) => x.instrument === inst) : undefined
    if (aiPos && Number(aiPos.target_weight) !== 0 && Number(aiPos.entry_price) > 0) {
      const w = Number(aiPos.target_weight)
      const side = tr(w > 0 ? 'tl.long' : 'tl.short')
      const pct = Math.abs(w * 100).toFixed(0)
      lines.push({ id: 'ai-target', kind: 'ai', price: Number(aiPos.entry_price), label: tr('tl.ai.hold', { side, w: pct }), hint: tr('tl.ai.hint', { side, w: pct, px: fmtPrice(aiPos.entry_price, pd) }),
        say: () => [{ t: tr('tl.ai.hold', { side, w: pct }) }] })
    }
    let markers: TradeMarker[] | undefined
    if (follow) {
      const whyOf = (msg?: string) => {
        const d = msg ? fDec?.find((x) => x.id === msg) : undefined
        const tg = d?.message.targets?.find((x) => x.instrument === inst)
        const sum = tg?.rationale?.summary
        return sum ? (getLang() === 'en' ? sum.en ?? sum.zh : sum.zh ?? sum.en) : undefined
      }
      markers = recMarkers(tr, follow, inst, whyOf, priv)
    }
    return { lines, markers, tick: spec ? Number(spec.tick) : undefined, onDrag }
  }, [view, inst, specs, current, dec, priv, follow, followOn, fDec, fState, lang, spot, spotView, spotSpecs])

  const algos = useAlgos(session.state === 'in' && !spot && current && !current.live ? current.id : null)
  const tradeWithAlgo = useMemo<TradeOverlay | undefined>(() => {
    const live = algos.filter((a) => a.kind === 'trail' && a.status === 'running' && a.live && a.instrument === inst)
    if (!tradeOverlay || !live.length) return tradeOverlay
    return { ...tradeOverlay, lines: [...tradeOverlay.lines, ...live.map((a) => ({ id: `trail-${a.id}`, kind: 'sl' as const, price: Number(a.stop), label: tr('al.lineTrail', { cb: a.callback }), say: () => [{ t: tr('al.lineTrailSay', { px: fmtPrice(a.stop), cb: a.callback }) }] }))] }
  }, [tradeOverlay, algos, inst])
  const chartOverlay = useMemo<TradeOverlay | undefined>(() => withBot(tradeWithAlgo, sideTab === 'bot' ? botForm : null, sideTab === 'bot' ? botPlan : null, botKind, liveBot, setBotForm), [tradeWithAlgo, sideTab, botForm, botPlan, botKind, liveBot])
  // 快捷键：B 买、S 卖（光标跳到数量），Shift + Esc 全撤（要确认）。Esc 只用来关闭和取消，不会误撤单（D75）
  const lastOne = useRef(0)
  const keyIv = useRef(pickIv)
  keyIv.current = pickIv
  const keyState = useRef({ view, max: lay.max })
  keyState.current = { view, max: lay.max }
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement | null
      const k = e.key.toLowerCase()
      // 下单区的数字输入框里字母没有意义：B / S 照样切换方向；Esc 只是离开输入框
      const numIn = el instanceof HTMLInputElement && /decimal|numeric/.test(el.inputMode) && !!el.closest('.op')
      const cancelKey = e.key === 'Escape' && e.shiftKey
      if (numIn && e.key === 'Escape' && !e.shiftKey) { el.blur(); return }
      if (!getPrefs().keys) return // 交易偏好里关了快捷键
      if (el && !numIn && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName))) return
      // 买、卖的键可以自己改（18 第 9.3 条）；改成数字的话，在数字框里按它就是打字，不切换
      const buy = hotKey('buy'), sell = hotKey('sell'), one = hotKey('iv1'), five = hotKey('iv5')
      const side = k === buy || k === sell
      if (numIn && !(side && /\p{L}/u.test(k)) && !cancelKey) return
      if (e.ctrlKey || e.metaKey || e.altKey || e.repeat || document.querySelector('.dlg-back')) return
      if (side) { e.preventDefault(); setTicket({ side: k === buy ? 'buy' : 'sell' }) }
      if (k === one || k === five) {
        if (numIn) return
        const now = performance.now()
        if (k === five && now - lastOne.current < 450) keyIv.current('15m')
        else keyIv.current(k === one ? '1m' : '5m')
        lastOne.current = k === one ? now : 0
      }
      if (cancelKey && keyState.current.view?.orders.length) setCancelAll(true)
    }
    window.addEventListener('keydown', on)
    return () => window.removeEventListener('keydown', on)
  }, [])

  useEffect(() => { document.title = cur ? `${fmtPrice(cur.last)} ${baseOf(cur.instrument)} · ${BRAND}` : BRAND }, [cur?.last, cur?.instrument]) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="shell mk-shell">
      <Bar page="trade">
        <span className="meter mk-opt"><span className={`sq ${conn === 'open' && !feedDown && !feedLag ? 'ok' : conn === 'connecting' ? '' : 'warn'}`} />{t(status?.name === 'sim' ? 'mk.feed.sim' : 'mk.feed.binance')}<b className="num">{feedLag ? t('mk.feed.lag', { s: lagS.toFixed(0) }) : t(conn === 'open' ? (feedDown ? 'mk.feed.reconnecting' : 'mk.feed.live') : conn === 'connecting' ? 'mk.feed.connecting' : 'mk.feed.closed')}</b></span>
        {view && <RiskMeters view={view} />}
      </Bar>

      {view && <LiqBanner view={view} />}
      {(conn === 'closed' || feedDown) && (
        <div className="note mk-note"><span><b>{t(conn === 'closed' ? 'mk.down.server' : 'mk.down.feed')}</b>{t('mk.down.tail')}</span></div>
      )}

      <main className="mk" style={lay.style} data-max={lay.max ?? undefined} data-empty={!spot && (!view || (view.positions.length === 0 && (view.orders?.length ?? 0) === 0)) ? '' : undefined}>
        <Strip inst={cur} want={selected} instruments={instruments} onSelect={setSelected} now={now} watch={watch} toggleWatch={toggleWatch} spot={spot} />
        <aside className="mk-watch" aria-label={t('mk.watch')}>
          <div className="display-head"><span className="display-title">{t('mk.watch')}</span></div>
          <WatchList watch={watch} instruments={instruments} selected={selected} onPick={setSelected} />
        </aside>

        <section className="display mk-chart">
          <div className="display-head" onDoubleClick={(e) => { if (!(e.target as HTMLElement).closest('button, .ruler')) lay.toggleMax('chart') }}>
            <span className="display-title">{cur ? baseOf(cur.instrument) : '—'}</span>
            <span className="display-sub" title={mode === 'candle' ? t('mk.panHint') : undefined}>{mode === 'heat' ? t('mk.heatSub') : mode === 'flow' ? t('mk.flowSub', { iv: ivName(iv.ms) }) : mode === 'candle' ? t('mk.candleSub', { iv: ivName(iv.ms) }) : t('mk.ticks')}</span>
            <div className="display-tools">
              <div className="mk-tools-row">
              <Seg label={t('mk.chartKind')} value={mode} onChange={setMode} options={spot ? [{ value: 'candle' as const, label: t('mk.candle') }, { value: 'step' as const, label: t('mk.step') }] : [{ value: 'candle', label: t('mk.candle') }, { value: 'step', label: t('mk.step') }, { value: 'heat', label: t('mk.heat') }, { value: 'flow', label: t('mk.flow') }]} />
              {!spot && (mode === 'candle' || mode === 'step') && <Seg label={t('mk.onchain')} value={chain} onChange={setChain} options={[{ value: 'on', label: t('mk.onchain.on') }, { value: 'off', label: t('mk.off') }]} />}
              {!spot && (mode === 'candle' || mode === 'step') && chain === 'on' && onchain && !onchain.overlay && <span className="mk-chain-scan">{t('oc.scanShort')}</span>}
              {!spot && (mode === 'candle' || mode === 'step') && chain === 'on' && (
                <button type="button" className="icon-btn" aria-pressed={ocAbout} onClick={() => setOcAbout((v) => !v)} title={t('mk.ocAbout')} aria-label={t('mk.ocAbout')}>
                  <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true"><circle cx="7" cy="7" r="6" fill="none" stroke="currentColor" strokeWidth="1.1" /><path d="M7 6v4M7 3.6v.9" stroke="currentColor" strokeWidth="1.3" /></svg>
                </button>
              )}
              {mode === 'candle' && <IndicatorMenu />}
              {mode === 'candle' && <DrawMenu tool={tool} onTool={setTool} shapes={shapes} onClear={() => setShapes([])} />}
              {mode === 'candle' && !narrow && <GridPick n={grid.n} onN={grid.setN} />}
              {mode === 'heat' && <HeatTools span={heatSpan} onSpan={setHeatSpan} layers={heatLayers} onLayers={setHeatLayers} floor={heatFloor} onFloor={setHeatFloor} hasOwn={!!tradeOverlay} />}
              </div>
              {mode === 'candle' && <div className="head-iv"><Seg label={t('mk.interval')} options={ivOpts} value={ivKey} onChange={pickIv} /></div>}
              {mode === 'flow' && <div className="head-iv"><Seg label={t('mk.interval')} options={ivOpts.filter((x) => x.value !== '1s')} value={iv.value} onChange={pickIv} /></div>}
              <button className="icon-btn" onClick={() => lay.toggleMax('chart')} title={t(lay.max ? 'mk.restore' : 'mk.maximize', { k: hotLabel(hotKey('max')) })} aria-label={t(lay.max ? 'mk.restore' : 'mk.maximize', { k: hotLabel(hotKey('max')) })}>
                <MaxIcon on={!!lay.max} />
              </button>
            </div>
          </div>
          {liveBot && <BotRead b={liveBot} />}
          {mode === 'candle' && next && <EventLine e={next} />}
          <div className="display-body">
            {(() => {
              const main = mode === 'flow' && cur ? <FlowBody inst={cur.instrument} interval={iv.ms} dec={dec} candles={candles} more={more} version={version} trade={tradeOverlay} /> : mode === 'heat' && cur ? <HeatBody inst={cur.instrument} dec={dec} span={heatSpan} layers={heatLayers} floor={heatFloor} chain={onchain?.overlay} own={tradeOverlay?.lines} /> : <Scope events={{ list: econ, band: !!cur && baseOf(cur.instrument) === 'BTC' }} mode={mode === 'heat' || mode === 'flow' ? 'candle' : mode} onPickPrice={(p) => { setSideTab('order'); setTicket({ price: String(p), side: Number(cur?.last) > p ? 'buy' : 'sell' }) }} onPriceMenu={session.state === 'in' && current && !paperOnly(spot ? 'spot' : 'bot') ? (px, x, y) => setPriceMenu({ px, x, y }) : undefined} candles={candles} points={steps} interval={iv.ms} span={5 * 60_000} dec={dec} live version={version} onchain={onchain?.overlay} pan={{ onNeedMore: more }} trade={chartOverlay}
              ind={ind} shapes={shapes} tool={tool} onShapes={setShapes} onToolEnd={() => setTool(null)} />
              return gridOn ? (
              <div className={`mg mg-${grid.n}`} data-grid-n={grid.n}>
                <div className="mg-cell mg-main"><MainHead st={cur} /><div className="mg-body">{main}</div></div>
                {grid.cells.slice(0, grid.n - 1).map((c, k) => (
                  <MiniChart key={k} idx={k} cell={c} instruments={instruments} version={version} onCell={(x) => grid.setCell(k, x)}
                    onMain={(inst) => { if (cur && inst !== cur.instrument) { grid.setCell(k, { ...c, i: cur.instrument }); setSelected(inst) } }} />
                ))}
              </div>
              ) : main
            })()}
            {(mode === 'candle' || mode === 'step') && onchain?.info && ocAbout && (
              <div className="mk-chain-note" onClick={() => setOcAbout(false)}>{onchain.info}</div>
            )}
          </div>
        </section>

        <section className="display mk-book">
          <div className="split split-x" onPointerDown={lay.drag('book', 'x', -1, 216, () => Math.min(520, window.innerWidth * 0.4))} onDoubleClick={lay.reset('book')} title={t('mk.dragW')} />
          <div className="display-head" title={bookTab === 'book' ? t('ob.clickHint') : undefined}>
            <Seg label={t('mk.book')} value={bookTab === 'dom' && spot ? 'book' : bookTab} onChange={setBookTab} options={spot ? [{ value: 'book' as const, label: <BookTab long={t('bk.book')} short={t('bk.bookS')} /> }, { value: 'trades' as const, label: <BookTab long={t('bk.trades')} short={t('bk.tradesS')} /> }] : [{ value: 'book' as const, label: <BookTab long={t('bk.book')} short={t('bk.bookS')} /> }, { value: 'trades' as const, label: <BookTab long={t('bk.trades')} short={t('bk.tradesS')} /> }, { value: 'dom' as const, label: t('al.dom') }]} />
            {bookTab === 'book' && <span className="display-sub mk-opt">{t('mk.bookSub', { venue: t(book?.venue === 'sim' ? 'mk.venue.sim' : 'mk.venue.binance'), n: bookRows })}</span>}
            {bookTab === 'book' && groupOpts.length > 1 && <Select className="ob-group" label={t('al.group')} value={String(bookGroup)} onChange={(v) => setBookGroup(Number(v))} options={[{ value: '0', label: locDec(String(groupOpts[0])), hint: t('al.groupNone') }, ...groupOpts.slice(1).map((g) => ({ value: String(g), label: locDec(String(g)) }))]} />}
          </div>
          <div className="display-body">{!narrow && (bookTab === 'dom' && !spot ? <DomLadder book={book} view={view} inst={inst} spec={specs?.[inst]} dec={dec} /> : bookTab !== 'trades' ? <OrderBook book={groupedBook} inst={cur} dec={bookGroup > 0 ? Math.min(bdec, decOfGroup(bookGroup)) : bdec} qdec={qdec} onFit={setBookRows} /> : <Trades inst={inst} dec={bdec} qdec={qdec} />)}</div>
        </section>

        <aside className="mk-side">
          <div className="split split-x" onPointerDown={lay.drag('side', 'x', -1, 260, () => Math.min(420, window.innerWidth * 0.3))} onDoubleClick={lay.reset('side')} title={t('mk.dragW')} />
          <div className="display-head">
            {followOn ? <span className="display-title">{t('fw.sideTitle')}</span> : <Seg label={t('bt.sideTab')} value={sideTab} onChange={setSideTab} options={[{ value: 'order', label: t('tr.title') }, { value: 'bot', label: t('bt.title') }]} />}
          </div>
          <div className="mk-side-body mk-scroll">{!narrow && sideTab === 'bot' && !followOn ? (paperOnly('bot') ?? <BotPanel busy={!!liveBot} hasPos={!spot && !liveBot && !!view?.positions.some((x) => x.instrument === inst)} inst={inst} last={Number(cur?.last) || 0} dec={spot ? Math.min(dec, 8) : dec} spot={spot} current={paperCur} form={botForm} setForm={setBotForm} kind={botKind} setKind={setBotKind} onPlan={setBotPlan} />) : !narrow && (spot ? (paperOnly('spot') ?? <SpotPanel inst={inst} last={cur?.last} bid={cur?.bid} ask={cur?.ask} spec={spotSpecs?.[inst]} view={spotView} current={current} />) : <OrderPanel inst={inst} last={cur?.last} bid={cur?.bid} ask={cur?.ask} spec={specs?.[inst]} view={view} current={current} />)}</div>
        </aside>

        <section className="display mk-bottom">
          <div className="split split-y" onPointerDown={lay.drag('bottom', 'y', -1, 110, () => vh() - 300)} onDoubleClick={lay.reset('bottom')} title={t('mk.dragH')} />
          <BottomTabs narrow={narrow} view={view} specs={specs} nAlgo={algos.filter((a) => a.status === 'running').length} onPick={setSelected} spot={spot ? spotView : undefined} spotAcct={current?.id} bots={allBots.filter((b) => b.account === (perpCur?.live ? -1 : current?.id))} botPanel={narrow ? (paperOnly('bot') ?? <BotPanel busy={!!liveBot} hasPos={!spot && !liveBot && !!view?.positions.some((x) => x.instrument === inst)} inst={inst} last={Number(cur?.last) || 0} dec={dec} spot={spot} current={paperCur} form={botForm} setForm={setBotForm} kind={botKind} setKind={setBotKind} onPlan={setBotPlan} />) : null}
            watch={narrow ? <WatchList watch={watch} instruments={instruments} selected={selected} onPick={setSelected} /> : null}
            book={narrow ? (
              <div className="mk-book-narrow">
                <div className="mk-book-narrow-h"><Seg label={t('mk.book')} value={bookTab} onChange={setBookTab} options={[{ value: 'book', label: t('bk.book') }, { value: 'trades', label: t('bk.trades') }]} /></div>
                <div className="mk-book-narrow-body">{bookTab === 'book' ? <OrderBook book={book} inst={cur} dec={bdec} qdec={qdec} onFit={setBookRows} /> : <Trades inst={inst} dec={bdec} qdec={qdec} />}</div>
              </div>
            ) : null}
            trade={narrow ? (
              // 窄屏"下单"标签：左边下单表单，右边一个精简盘口（像交易所手机端），点价格直接填进左边；放不下两栏时只有表单
              <div className="mk-trade-split">
                {spot ? (paperOnly('spot') ?? <SpotPanel inst={inst} last={cur?.last} bid={cur?.bid} ask={cur?.ask} spec={spotSpecs?.[inst]} view={spotView} current={current} />) : <OrderPanel inst={inst} last={cur?.last} bid={cur?.bid} ask={cur?.ask} spec={specs?.[inst]} view={view} current={current} />}
                <div className="mk-trade-book"><OrderBook book={book} inst={cur} dec={bdec} qdec={qdec} n={7} compact /></div>
              </div>
            ) : null} />
        </section>
      </main>
      {priceMenu && cur && (spot ? spotSpecs?.[inst] : specs?.[inst]) && (
        <PriceMenu at={priceMenu} last={Number(cur.last)} tick={(spot ? spotSpecs?.[inst] : specs?.[inst])!.tick} dec={dec} spot={spot} acct={current?.id ?? null} inst={inst}
          pos={spot ? null : (view?.positions.find((x) => x.instrument === inst)?.side as 'long' | 'short' | undefined) ?? null}
          onClose={() => setPriceMenu(null)} onTicket={() => setSideTab('order')} />
      )}
      {view && <CancelAllConfirm open={cancelAll} acct={view.id} n={view.orders.length} orders={view.orders} onClose={() => setCancelAll(false)} />}
    </div>
  )
}

/** 币种的中文名：认识的翻译，其余就写代码本身 */
function coinName(base: string): string {
  const k = `inst.${base}` as Key
  const v = tr(k)
  return v === k ? base : v
}

/** 顶部品种条：左边品种和价格，右边一排读数。点品种名弹出列表切换。 */
function Strip({ inst, want, instruments, onSelect, now, watch, toggleWatch, spot }: { spot: boolean; inst?: InstrumentState; want: string; instruments: InstrumentState[]; onSelect: (s: string) => void; now: number; watch: string[]; toggleWatch: (x: string) => void }) {
  const t = useT()
  const [open, setOpen] = useState(false)
  // 品种搜索（2.31）：先列正在看的和自选的，输入后在全市场里找
  const coins = useMarketList()
  const [q, setQ] = useState('')
  useEffect(() => { if (!open) setQ('') }, [open])
  const needle = q.trim().toUpperCase()
  const rows = useMemo(() => {
    const live = new Map(instruments.map((i) => [i.instrument, i]))
    const out: { inst: string; last?: string; chg?: string }[] = []
    const seen = new Set<string>()
    const push = (x: string, last?: string, chg?: string) => { if (!seen.has(x)) { seen.add(x); out.push({ inst: x, last, chg }) } }
    if (!needle) {
      for (const x of watch) { const l = live.get(x); push(x, l?.last, l?.change_24h_pct) }
      for (const i of instruments) push(i.instrument, i.last, i.change_24h_pct)
      if (!spot) for (const c of coins.slice(0, 40)) push(c.inst, String(c.px), String(c.chg))
    } else {
      for (const i of instruments) if (baseOf(i.instrument).startsWith(needle)) push(i.instrument, i.last, i.change_24h_pct)
      if (!spot) for (const c of coins) if (c.base.startsWith(needle)) push(c.inst, String(c.px), String(c.chg))
      for (const i of instruments) if (baseOf(i.instrument).includes(needle)) push(i.instrument, i.last, i.change_24h_pct)
      for (const c of coins) if (c.base.includes(needle)) push(c.inst, String(c.px), String(c.chg))
    }
    return out.slice(0, 60)
  }, [needle, instruments, coins, watch])
  const [act, setAct] = useState(0)
  useEffect(() => { setAct(0) }, [needle])
  // 顶部持仓量（2.28）
  const oi = usePollData<{ value: number }>(inst?.instrument ? `/api/market/oi?instrument=${inst.instrument}` : null, 5000)
  // 从按钮长出来、缩回去（D68）；点别处或按 Esc 就关
  const [mounted, setMounted] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const menu = useRef<HTMLDivElement>(null)
  const anchor = useRef<Anchor | null>(null)
  useEffect(() => {
    if (open) {
      anchor.current = lastAnchor()
      setMounted(true)
      return
    }
    if (!mounted) return
    let done = false
    const finish = () => { if (!done) { done = true; setMounted(false) } }
    const a = menu.current ? shrinkTo(menu.current, anchor.current, 130) : null
    if (a) a.finished.then(finish, finish)
    else finish()
    const id = window.setTimeout(finish, 200)
    return () => window.clearTimeout(id)
  }, [open]) // eslint-disable-line react-hooks/exhaustive-deps
  useLayoutEffect(() => {
    if (mounted && open && menu.current) growFrom(menu.current, anchor.current, 180)
  }, [mounted]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!open) return
    const down = (e: PointerEvent) => { if (!box.current?.contains(e.target as Node)) setOpen(false) }
    const key = (e: KeyboardEvent) => { if (e.key === 'Escape') { e.stopPropagation(); setOpen(false) } }
    window.addEventListener('pointerdown', down, true)
    window.addEventListener('keydown', key, true)
    return () => { window.removeEventListener('pointerdown', down, true); window.removeEventListener('keydown', key, true) }
  }, [open])
  return (
    <section className="mk-strip">
      <div className="mk-product">
        <Seg label={t('sp.product')} value={spot ? 'spot' : 'perp'} onChange={(v) => {
          const c = inst ? baseOf(inst.instrument) : 'BTC'
          onSelect(v === 'spot' ? `${SPOT_COINS.includes(c) ? c : 'BTC'}-SPOT` : `${c}-PERP`)
        }} options={[{ value: 'perp', label: t('sp.perp') }, { value: 'spot', label: t('sp.spot') }]} />
      </div>
      <div className="mk-id" ref={box}>
        <button className="mk-picker" onClick={() => setOpen((o) => !o)} aria-expanded={open}>
          {/* 行情还没到时用要看的那个品种先写上名字：不然名字到了整行往右挪（11-15） */}
          <span className="sym">{baseOf(inst?.instrument ?? want)}</span>
          <span className="t3">{t(spot ? 'sp.name' : 'mk.perp', { name: coinName(baseOf(inst?.instrument ?? want)) })}</span>
          <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true"><path d="M2 3.5 5 6.5 8 3.5" fill="none" stroke="currentColor" strokeWidth="1.2" /></svg>
        </button>
        {mounted && (
          <div className="mk-menu" ref={menu}>
            <input className="mk-search" autoFocus value={q} placeholder={t('mk.searchCoin')} aria-label={t('mk.searchCoin')} spellCheck={false} onChange={(e) => setQ(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); setOpen(false) }
                if (e.key === 'ArrowDown') { e.preventDefault(); setAct((a) => Math.min(rows.length - 1, a + 1)) }
                if (e.key === 'ArrowUp') { e.preventDefault(); setAct((a) => Math.max(0, a - 1)) }
                if (e.key === 'Enter' && rows[act]) { e.preventDefault(); onSelect(rows[act].inst); setOpen(false) }
              }} />
            <div className="mk-menu-list" role="listbox">
              {rows.map((r, i) => (
                <div key={r.inst} role="option" tabIndex={-1} aria-selected={r.inst === inst?.instrument} className={`mk-menu-r ${i === act ? 'act' : ''}`} onPointerEnter={() => setAct(i)} onClick={() => { onSelect(r.inst); setOpen(false) }}>
                  <button type="button" className={`mk-star ${watch.includes(r.inst) ? 'on' : ''}`} aria-pressed={watch.includes(r.inst)} aria-label={t(watch.includes(r.inst) ? 'mk.watchDel' : 'mk.watchAdd')} title={t(watch.includes(r.inst) ? 'mk.watchDel' : 'mk.watchAdd')}
                    onClick={(e) => { e.stopPropagation(); toggleWatch(r.inst) }}><i /></button>
                  <span className="sym">{baseOf(r.inst)}</span>
                  <span className="num">{r.last ? fmtPrice(r.last) : '—'}</span>
                  <span className={`num ${sign(r.chg)}`}>{r.chg ? fmtPct(r.chg) : '—'}</span>
                </div>
              ))}
              {!rows.length && <div className="mk-menu-none">{t('mk.noCoin')}</div>}
            </div>
          </div>
        )}
        <div className="mk-price">
          {inst?.last ? <Price text={fmtPrice(inst.last)} className="mk-last num" /> : <span className="mk-last t4">—</span>}
          <span className={`num ${sign(inst?.change_24h_pct)}`}>{fmtPct(inst?.change_24h_pct)}</span>
        </div>
      </div>
      <div className="mk-ro">
        {!spot && <Ro l={t('mk.mark')} h={t('hint.mark')} v={fmtPrice(inst?.mark)} />}
        {!spot && <Ro l={t('mk.funding')} h={t('hint.funding')} v={fmtFunding(inst?.funding_rate)} s={inst?.next_funding_time ? t('mk.fundingIn', { t: fmtCountdown(inst.next_funding_time, now) }) : undefined} />}
        <Ro l={t('mk.high')} h={t('hint.high')} v={fmtPrice(inst?.high_24h)} cls="mk-opt2" />
        <Ro l={t('mk.low')} h={t('hint.low')} v={fmtPrice(inst?.low_24h)} cls="mk-opt2" />
        <Ro l={t('mk.vol')} h={t('hint.vol')} v={inst?.quote_volume_24h ? fmtCompactL(Number(inst.quote_volume_24h)) : '—'} s="USDT" />
        {!spot && <Ro l={t('mk.oi')} h={t('hint.oi')} v={oi?.value ? fmtCompactL(oi.value) : '—'} s="USDT" cls="mk-opt2" />}
        <Ro l={t('mk.spread')} h={t('hint.spread')} v={inst?.bid ? spreadBps(inst.bid, inst.ask).replace(' bp', '') : '—'} s="bp" cls="mk-opt2" />
      </div>
    </section>
  )
}

/** 顶栏风险条（D38）：权益、保证金占用、离强平最近的距离。悬停看明细。 */
function RiskMeters({ view }: { view: AccountView }) {
  const t = useT()
  const equity = Number(view.equity)
  const used = Number(view.position_margin) + Number(view.order_margin)
  const usedPct = equity > 0 ? (used / equity) * 100 : 0
  let near: { inst: string; pct: number } | null = null
  for (const p of view.positions) {
    const liq = Number(p.liq_px), mark = Number(p.mark)
    if (liq > 0 && mark > 0) {
      const pct = (Math.abs(mark - liq) / mark) * 100
      if (!near || pct < near.pct) near = { inst: p.instrument, pct }
    }
  }
  const tone = near ? (near.pct < 3 ? 'down' : near.pct < 10 ? 'warn-t' : '') : ''
  const ratio = Number(view.margin_ratio) * 100
  return (
    <div className="mk-risk mk-opt ro-has-pop" tabIndex={0}>
      <span className="meter mk-risk-eq">{t('tr.equity')}<b className="num"><Money text={fmtAmt(view.equity)} /></b></span>
      <span className="meter">{t('risk.used')}<b className="num">{locDec(usedPct.toFixed(1))}%</b></span>
      <span className="meter"><span className="mk-risk-l">{t('risk.liq')}</span><b className={`num ${tone}`}>{near ? `${locDec(near.pct.toFixed(1))}%` : '—'}</b></span>
      <LockChip acct={view.id} />
      <div className="ro-pop mk-risk-pop" role="tooltip">
        <div className="rp-row"><span>{t('tr.equity')}</span><span className="num"><Money text={fmtAmt(view.equity)} /></span></div>
        <div className="rp-row"><span>{t('tr.used')}</span><span className="num"><Money text={fmtAmt(used)} /></span></div>
        <div className="rp-row"><span>{t('tr.ratio')}</span><span className="num">{locDec(ratio.toFixed(2))}%</span></div>
        <p className="rp-note">{near ? t('risk.near', { inst: baseOf(near.inst), pct: locDec(near.pct.toFixed(1)) }) : t(view.positions.length ? 'risk.noLiq' : 'risk.noPos')}</p>
      </div>
    </div>
  )
}

function Ro({ l, v, s, cls, h }: { l: string; v: React.ReactNode; s?: string; cls?: string; h?: string }) {
  return (
    <div className={`ro ${cls ?? ''}`} title={h}>
      <span className="ro-l">{l}</span>
      <span className="ro-v num">{v}{s && <small>{s}</small>}</span>
    </div>
  )
}

/**
 * 点盘口价格：价格立刻填进下单区正在用的价格框（限价单的价格、条件单的触发价），光标立刻到数量框——
 * 交易员点完马上按回车，下出去的必须是刚点的价。"飞过去"只是视觉（D68 连续）：飞行期间框里的字先藏起来，落地时出现
 */
function pickPrice(e: React.PointerEvent<HTMLElement>, p: number, text: string, dec?: number) {
  if (e.button !== 0) return
  // 按盘口显示的小数位填：显示 68,325.2 就填 68325.2（原来填原始的 68325.15，下单区再取整成 68325.1，和看到的差一档）
  setTicket({ price: dec != null ? p.toFixed(dec) : String(p) })
  const target = document.querySelector<HTMLElement>('.op [data-fly-target]')
  if (!target || !target.offsetParent || reducedMotion()) return
  const cell = e.currentTarget.querySelector('span') ?? e.currentTarget
  target.classList.add('fly-wait')
  void flyText(text, cell.getBoundingClientRect(), target, 'fly-text num').then(() => target.classList.remove('fly-wait'))
  window.setTimeout(() => target.classList.remove('fly-wait'), 400) // 动画被打断时也一定恢复
}

/** 最新成交：每 0.7 秒取一次，新来的几笔从上面滑进来、闪一下颜色。主动买绿、主动卖红；大单加粗。 */
function Trades({ inst, dec, qdec = 3 }: { inst: string; dec: number; qdec?: number }) {
  const t = useT()
  const [rows, setRows] = useState<{ t: number; px: string; qty: string; buy: boolean }[] | null>(null)
  const seen = useRef<Set<string>>(new Set())
  const [fresh, setFresh] = useState<Set<string>>(new Set())
  useEffect(() => {
    let live = true
    seen.current = new Set()
    setRows(null)
    const load = async () => {
      if (document.hidden) return
      try {
        const r = await fetch(`/api/market/trades?instrument=${encodeURIComponent(inst)}&limit=40`).then((x) => x.json())
        if (!live) return
        const k = (x: { t: number; px: string; qty: string }) => `${x.t}|${x.px}|${x.qty}`
        const first = seen.current.size === 0
        const nw = new Set<string>()
        for (const x of r) { const key = k(x); if (!seen.current.has(key)) { if (!first) nw.add(key); seen.current.add(key) } }
        if (seen.current.size > 2000) seen.current = new Set(r.map(k))
        setFresh(nw)
        setRows(r)
      } catch { /* 下次再取 */ }
    }
    void load()
    const id = window.setInterval(load, 700)
    return () => { live = false; window.clearInterval(id) }
  }, [inst])
  if (!rows) return <div className="mk-empty">{t('ob.waiting')}</div>
  if (!rows.length) return <div className="mk-empty">{t('bk.tradesEmpty')}</div>
  const big = rows.map((r) => Number(r.qty)).sort((a, b) => b - a)[Math.floor(rows.length * 0.1)] ?? Infinity
  const time = (ms: number) => clock(ms, true)
  return (
    <div className="ob tr">
      <div className="ob-h"><span>{t('bk.price')}</span><span>{t('bk.qty')}</span><span>{t('bk.time')}</span></div>
      {rows.map((r) => {
        const key = `${r.t}|${r.px}|${r.qty}`
        const pxs = Number(r.px).toLocaleString(numLocale(), { minimumFractionDigits: dec, maximumFractionDigits: dec })
        return (
          <div className={`ob-r tr-r ${r.buy ? 'bid' : 'ask'} ${fresh.has(key) ? 'tr-new' : ''} ${Number(r.qty) >= big ? 'tr-big' : ''}`} key={key} onPointerDown={(e) => pickPrice(e, Number(r.px), pxs)}>
            <span className="num">{pxs}</span><span className="num">{Number(r.qty) >= 10 ? locDec(Number(r.qty).toFixed(2)) : locDec(Number(r.qty).toFixed(Number(r.qty) >= 1 ? 3 : qdec))}</span><span className="num t3">{time(r.t)}</span>
          </div>
        )
      })}
    </div>
  )
}

const OB_ROW = 20 // 盘口每档的高度
const OB_CHROME = 24 + 38 + 8 // 表头、中间的现价行、上下留白

/** 盘口：卖单在上（从高到低）、买单在下，中间是最新价和价差。横条长度 = 累计数量。 */
function OrderBook({ book, inst, dec, qdec = 3, n, compact, onFit }: { book: BookFrame | null; inst?: InstrumentState; dec: number; qdec?: number; n?: number; compact?: boolean; onFit?: (rows: number) => void }) {
  const t = useT()
  // 买卖档数相等、现价在中间：按盘口这块有多高算放得下几档，不够高就少显示几档，不能把买单切掉（D75）
  const box = useRef<HTMLDivElement>(null)
  const [fit, setFit] = useState(12)
  const ready = !!book
  useLayoutEffect(() => {
    const el = box.current
    if (!el || n) return
    const measure = () => {
      if (!el.clientHeight) return
      const r = Math.max(1, Math.min(20, Math.floor((el.clientHeight - OB_CHROME) / 2 / OB_ROW)))
      setFit(r)
      onFit?.(r)
    }
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [n, ready]) // eslint-disable-line react-hooks/exhaustive-deps
  const N = n ?? fit
  const rows = useMemo(() => {
    if (!book) return null
    const asks = book.asks.slice(0, N).map(([p, q]) => [Number(p), Number(q)] as const)
    const bids = book.bids.slice(0, N).map(([p, q]) => [Number(p), Number(q)] as const)
    let ca = 0, cb = 0
    const A = asks.map(([p, q]) => ({ p, q, c: (ca += q) }))
    const B = bids.map(([p, q]) => ({ p, q, c: (cb += q) }))
    let qmax = 0
    for (const x of A) qmax = Math.max(qmax, x.q)
    for (const x of B) qmax = Math.max(qmax, x.q)
    return { A: A.reverse(), B, max: Math.max(ca, cb) || 1, qmax: qmax || 1 }
  }, [book, N])
  const px = (p: number) => p.toLocaleString(numLocale(), { minimumFractionDigits: dec, maximumFractionDigits: dec })
  const qd = (q: number) => (q >= 1000 ? q.toFixed(0) : locDec(q >= 10 ? q.toFixed(2) : q.toFixed(q >= 1 ? 3 : qdec)))
  if (!rows) return <div className="mk-empty">{t('ob.waiting')}</div>
  const best = rows.A.length && rows.B.length ? (rows.A[rows.A.length - 1].p - rows.B[0].p) : 0
  return (
    <div ref={box} className={`ob ${compact ? 'ob-compact' : ''}`}>
      <div className="ob-h"><span>{t('ob.price')}</span><span>{t('ob.qty')}</span><span>{t('ob.cum')}</span></div>
      {rows.A.map((r) => (
        <div className="ob-r ask" key={'a' + r.p} onPointerDown={(e) => pickPrice(e, r.p, px(r.p), dec)}><b style={{ width: `${(r.c / rows.max) * 100}%` }} /><i style={{ width: `${(r.q / rows.qmax) * 100}%` }} /><span className="num">{px(r.p)}</span><span className="num">{qd(r.q)}</span><span className="num t3">{qd(r.c)}</span></div>
      ))}
      <div className="ob-mid">
        <span className="num">{inst?.last ? fmtPrice(inst.last) : '—'}</span>
        <span className="t3 num">{t('ob.spread', { v: locDec(best.toFixed(dec)) })}</span>
      </div>
      {rows.B.map((r) => (
        <div className="ob-r bid" key={'b' + r.p} onPointerDown={(e) => pickPrice(e, r.p, px(r.p), dec)}><b style={{ width: `${(r.c / rows.max) * 100}%` }} /><i style={{ width: `${(r.q / rows.qmax) * 100}%` }} /><span className="num">{px(r.p)}</span><span className="num">{qd(r.q)}</span><span className="num t3">{qd(r.c)}</span></div>
      ))}
    </div>
  )
}

/** 标签上的数字：变了就跳一下（D68：下单成交后，告诉用户东西去了"持仓"还是"委托"） */
function Counted({ text, n }: { text: string; n: number }) {
  const prev = useRef(n)
  const bump = useRef(0)
  if (n !== prev.current) { bump.current++; prev.current = n }
  const i = text.lastIndexOf(String(n))
  if (i < 0) return <>{text}</>
  return <>{text.slice(0, i)}<span key={bump.current} className={`cnt num ${bump.current ? 'cnt-bump' : ''}`}>{n}</span>{text.slice(i + String(n).length)}</>
}

// ---------- 交易核心的数据 ----------
export { useCandles }

/** 盘口这一列窄的时候标签用短名（market.css 的容器查询切换），三个标签都要看得见 */
function BookTab({ long, short }: { long: string; short: string }) {
  return <><span className="bt-l">{long}</span><span className="bt-s">{short}</span></>
}

function StrategyPanel() {
  const t = useT()
  const list = usePollData<StrategyStatus[]>('/api/strategies', 3000)
  const s = list?.[0]
  const q = usePollData<StrategyState>(s ? `/api/strategies/${s.strategy_id}` : null, 2000)
  if (!list) return <div className="mk-empty">{t('sp.connecting')}</div>
  if (!s) return <div className="mk-empty">{t('sp.none')}</div>
  const nav = q ? Number(q.state.nav) : 10000
  const pnl = nav - 10000
  return (
    <div className="sp">
      <div className="sp-id">
        <span className={`sq ${s.online ? 'ok' : 'warn'}`} />
        <span className="sym">{s.strategy_id}</span>
        <span className="t3">{t(s.online ? 'sp.online' : 'ai.offlineLong')}</span>
        <a className="op-link sp-more" {...linkProps('ai')}>{t('ai.openAi')}</a>
      </div>
      {s.sample && <p className="sp-tag">{t('sp.demo')}</p>}
      <div className="sp-ro">
        <div className="ro lg"><span className="ro-l">{t('sp.nav')}</span><span className="ro-v num"><Money text={nav.toLocaleString(numLocale(), { minimumFractionDigits: 2, maximumFractionDigits: 2 })} /></span></div>
        <div className="ro"><span className="ro-l">{t('sp.pnl')}</span><span className={`ro-v num ${pnl > 0 ? 'up' : pnl < 0 ? 'down' : ''}`}><Money text={(pnl >= 0 ? '+' : '−') + locDec(Math.abs(pnl).toFixed(2))} /></span></div>
        <div className="ro"><span className="ro-l">{t('sp.ret')}</span><span className={`ro-v num ${pnl > 0 ? 'up' : pnl < 0 ? 'down' : ''}`}>{(pnl >= 0 ? '+' : '−') + locDec(Math.abs(pnl / 100).toFixed(2))}%</span></div>
        {q && !q.locked && <div className="ro"><span className="ro-l">{t('sp.npos')}</span><span className="ro-v num">{q.state.positions.filter((p) => Number(p.qty) !== 0).length}</span></div>}
      </div>
      {(s.halted || s.circuit_breaker) && <div className="note"><span><b>{t(s.halted ? 'sp.halted' : 'sp.breaker')}</b>{t('sp.reduceOnly')}</span></div>}
      {q?.locked && <Locked what="ai.lockedPos" compact />}
      <NavSpark points={q?.nav_history ?? []} />
    </div>
  )
}

function NavSpark({ points }: { points: { t: number; nav: number }[] }) {
  if (points.length < 5) return <div className="sp-spark t3">{tr('sp.sparkWait')}</div>
  const lo = Math.min(...points.map((p) => p.nav)), hi = Math.max(...points.map((p) => p.nav))
  const W = 260, H = 56
  const d = points.map((p, i) => `${i ? 'L' : 'M'}${((i / (points.length - 1)) * W).toFixed(1)},${(H - 2 - ((p.nav - lo) / (hi - lo || 1)) * (H - 4)).toFixed(1)}`).join('')
  return <svg className="sp-spark" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none"><path d={d} fill="none" stroke="var(--t2)" strokeWidth="1" vectorEffect="non-scaling-stroke" /></svg>
}

function usePollData<T>(url: string | null, ms: number): T | null { return usePoll<T>(url, ms).data }

function FirstDecisions() {
  const list = usePollData<StrategyStatus[]>('/api/strategies', 10000)
  return <DecisionFeed strategy={list?.[0]?.strategy_id ?? ''} compact />
}

function useMedia(q: string): boolean {
  const [m, setM] = useState(() => matchMedia(q).matches)
  useEffect(() => {
    const mq = matchMedia(q)
    const on = () => setM(mq.matches)
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [q])
  return m
}

type Tab = 'book' | 'trade' | 'watch' | 'pos' | 'open' | 'hist' | 'fills' | 'funding' | 'sig' | 'ai' | 'bots'

/** 自选列表（2.29）。点一下换品种，当前的左边一条白线 */
function WatchList({ watch, instruments, selected, onPick }: { watch: string[]; instruments: InstrumentState[]; selected: string; onPick: (s: string) => void }) {
  const t = useT()
  const live = new Map(instruments.map((i) => [i.instrument, i]))
  if (!watch.length) return <div className="mk-empty">{t('mk.watchEmpty')}</div>
  return (
    <div className="mk-watch-list mk-scroll">
      {watch.map((x) => {
        const i = live.get(x)
        return (
          <button type="button" key={x} className={x === selected ? 'on' : ''} onClick={() => onPick(x)}>
            <span className="sym">{baseOf(x)}</span>
            <span className="num">{i?.last ? fmtPrice(i.last) : '—'}</span>
            <span className={`num ${sign(i?.change_24h_pct)}`}>{i?.change_24h_pct ? fmtPct(i.change_24h_pct) : '—'}</span>
          </button>
        )
      })}
    </div>
  )
}

/** 爆仓提示（第 27 条，3.28）：留住直到点掉，写出亏了多少 */
const LIQ_SEEN = 'qs.liqSeen'
function LiqBanner({ view }: { view: AccountView }) {
  const t = useT()
  const priv = usePrivate()
  const key = `${LIQ_SEEN}.${view.id}`
  const [seen, setSeen] = useState(() => { try { return Number(localStorage.getItem(key) ?? 0) } catch { return 0 } })
  const open = view.notices.filter((n) => n.kind === 'liquidation' && n.seq > seen)
  if (!open.length) return null
  const dismiss = () => {
    const last = open[open.length - 1].seq
    setSeen(last)
    try { localStorage.setItem(key, String(last)) } catch { /* 忽略 */ }
  }
  return (
    <div className="note liq-banner" role="alert">
      <span>
        {open.map((n) => {
          const loss = Number(n.loss ?? 0)
          const base = baseOf(n.instrument)
          const v = priv ? '••••' : fmtAmt(loss)
          if (n.reason === 'partial') return <b key={n.seq}>{t('liq.bannerPartial', { inst: base, q: n.qty, v })} </b> // 部分强平（D103）：剩下的仓位还在
          return <b key={n.seq}>{loss > 0 ? (n.instrument ? t('liq.banner', { inst: base, v }) : t('liq.bannerAll', { v })) : t('liq.bannerNoLoss', { inst: base })} </b>
        })}
        {t('liq.more')}
      </span>
      <button type="button" className="btn secondary sm" onClick={dismiss}>{t('liq.ok')}</button>
    </div>
  )
}

function BottomTabs({ narrow, book, trade, watch, view, specs, onPick, spot, spotAcct, bots, botPanel, nAlgo = 0 }: { nAlgo?: number; spot?: SpotView | null; spotAcct?: number; bots: Bot[]; botPanel: React.ReactNode; narrow: boolean; book: React.ReactNode; trade: React.ReactNode; watch: React.ReactNode; view: AccountView | null; specs: ReturnType<typeof useSpecs>; onPick: (inst: string) => void }) {
  const t = useT()
  const [tab, setTab] = useState<Tab>(narrow ? 'trade' : 'pos')
  useEffect(() => { if (!narrow && (tab === 'book' || tab === 'trade' || tab === 'watch')) setTab('pos') }, [narrow, tab])
  // 从宽变窄（iPhone Duo 合上、窗口拖窄）：宽屏时下单区一直在右边，变窄后接着显示"下单"，别停在持仓上看不见下单区（11-18）
  const wasNarrow = useRef(narrow)
  useEffect(() => { if (narrow && !wasNarrow.current) setTab('trade'); wasNarrow.current = narrow }, [narrow])
  const signedIn = useSession().state === 'in'
  const needView = ['pos', 'open', 'hist', 'fills', 'funding'].includes(tab)
  if (spot !== undefined) return <SpotTabs narrow={narrow} book={book} trade={trade} watch={watch} view={spot} acct={spotAcct} signedIn={signedIn} onPick={onPick} bots={bots} botPanel={botPanel} />
  const nBots = bots.filter((b) => b.status !== 'stopped').length
  return (
    <>
      <div className="display-head mk-tabs">
        <Seg label={t('tab.label')} value={tab} onChange={setTab} options={[
          ...(narrow ? [{ value: 'trade' as const, label: t('tab.trade') }, { value: 'book' as const, label: t('tab.book') }, { value: 'watch' as const, label: t('tab.watch') }] : []),
          { value: 'pos', label: <Counted text={t('tab.pos', { n: view?.positions.length ?? 0 })} n={view?.positions.length ?? 0} /> }, { value: 'open', label: <Counted text={t('tab.open', { n: (view?.orders.length ?? 0) + nAlgo })} n={(view?.orders.length ?? 0) + nAlgo} /> }, // 连正在跑的追价、TWAP、冰山一起算（11-17 走查：TWAP 在跑，标签写委托 0）
          { value: 'hist', label: t('tab.hist') }, { value: 'fills', label: t('tab.fills') }, { value: 'funding', label: t('tab.funding') },
          { value: 'bots' as const, label: <Counted text={t('bt.tab', { n: nBots })} n={nBots} /> },
          { value: 'sig', label: t('tab.sig') }, { value: 'ai', label: t('tab.ai') },
        ]} />
      </div>
      <div className="display-body mk-scroll">
        {tab === 'book' && book}
        {tab === 'watch' && watch}
        {tab === 'trade' && <div className="mk-trade-tab">{trade}</div>}
        {needView && !signedIn && <div className="mk-empty">{t('ls.login')}</div>}
        {needView && signedIn && !view && <div className="mk-empty">{t('common.loading')}</div>}
        {view && tab === 'pos' && <Positions view={view} specs={specs} onPick={onPick} />}
        {view && tab === 'open' && <OpenOrders view={view} specs={specs} />}
        {view && tab === 'hist' && <OrderHistory view={view} />}
        {view && tab === 'fills' && <Fills view={view} />}
        {view && tab === 'funding' && <Funding view={view} />}
        {tab === 'bots' && (signedIn ? <>{narrow && botPanel && <div className="bt-narrow">{botPanel}</div>}<BotList bots={bots} onPick={onPick} /></> : <div className="mk-empty">{t('ls.login')}</div>)}
        {tab === 'sig' && <FirstDecisions />}
        {tab === 'ai' && <StrategyPanel />}
      </div>
    </>
  )
}

/** 现货的底部标签：资产、当前委托、成交 */
function SpotTabs({ narrow, book, trade, watch, view, acct, signedIn, onPick, bots, botPanel }: { narrow: boolean; book: React.ReactNode; trade: React.ReactNode; watch: React.ReactNode; view: SpotView | null; acct?: number; signedIn: boolean; onPick: (i: string) => void; bots: Bot[]; botPanel: React.ReactNode }) {
  const t = useT()
  type T2 = 'assets' | 'open' | 'fills' | 'book' | 'trade' | 'watch' | 'bots'
  const nBots = bots.filter((b) => b.status !== 'stopped').length
  const [tab, setTab] = useState<T2>(narrow ? 'trade' : 'assets')
  const wasNarrow = useRef(narrow)
  useEffect(() => { if (narrow && !wasNarrow.current) setTab('trade'); wasNarrow.current = narrow }, [narrow]) // 从宽变窄：接着显示下单区
  return (
    <>
      <div className="display-head mk-tabs">
        <Seg label={t('tab.label')} value={tab} onChange={setTab} options={[
          ...(narrow ? [{ value: 'trade' as const, label: t('tab.trade') }, { value: 'book' as const, label: t('tab.book') }, { value: 'watch' as const, label: t('tab.watch') }] : []),
          { value: 'assets' as const, label: t('sp.tabAssets') },
          { value: 'open' as const, label: <Counted text={t('tab.open', { n: view?.orders.length ?? 0 })} n={view?.orders.length ?? 0} /> },
          { value: 'fills' as const, label: t('tab.fills') },
          { value: 'bots' as const, label: <Counted text={t('bt.tab', { n: nBots })} n={nBots} /> },
        ]} />
      </div>
      <div className="display-body mk-scroll">
        {tab === 'book' && book}
        {tab === 'watch' && watch}
        {tab === 'trade' && <div className="mk-trade-tab">{trade}</div>}
        {!signedIn && (tab === 'assets' || tab === 'open' || tab === 'fills') && <div className="mk-empty">{t('ls.login')}</div>}
        {view && tab === 'assets' && <SpotAssets view={view} onPick={onPick} />}
        {view && tab === 'open' && acct && <SpotOrders view={view} acct={acct} />}
        {view && tab === 'fills' && <SpotFills view={view} />}
        {tab === 'bots' && (signedIn ? <>{narrow && botPanel && <div className="bt-narrow">{botPanel}</div>}<BotList bots={bots} onPick={onPick} /></> : <div className="mk-empty">{t('ls.login')}</div>)}
      </div>
    </>
  )
}

/** 图上面一行结论：机器人现在怎么样（D82：结论写在图上面） */
function BotRead({ b }: { b: Bot }) {
  const t = useT()
  const priv = usePrivate()
  const m = (s: string) => (priv ? '••••' : s)
  const sgn = (v: string) => { const n = Number(v); return `${n > 0 ? '+' : n < 0 ? '−' : ''}${locDec(Math.abs(n).toFixed(2))}` }
  const cls = (v: string) => (Number(v) > 0 ? 'up' : Number(v) < 0 ? 'down' : '')
  const grid = b.kind === 'spot_grid' || b.kind === 'perp_grid'
  const lines = b.plan?.lines ?? []
  const px = Number(b.price)
  const nextBuy = grid ? (b.grids ?? []).filter((g) => g.side === 'buy').map((g) => Number(lines[g.i])).filter((x) => x < px).sort((x, y) => y - x)[0] : undefined
  const nextSell = grid ? (b.grids ?? []).filter((g) => g.side === 'sell').map((g) => Number(lines[g.i + 1])).filter((x) => x > px).sort((x, y) => x - y)[0] : undefined
  const pct = (x: number) => `${x > px ? '+' : '−'}${locDec(Math.abs((x / px - 1) * 100).toFixed(2))}%`
  const nextRung = !grid ? (b.rungs ?? []).find((r) => !r.filled) : undefined
  return (
    <div className="heat-read bt-read" data-bot-read>
      <span className="hr-cell"><em>{t(`bt.k.${b.kind}` as Key)}</em><b>{t(`bt.s.${b.range && b.range !== 'inside' && b.status === 'running' ? b.range : b.status}` as Key)}</b></span>
      <span className="hr-cell"><em>{t('bt.c.realized')}</em><b className={`num ${cls(b.realized)}`}>{m(sgn(b.realized))}</b></span>
      <span className="hr-cell"><em>{t('bt.c.unreal')}</em><b className={`num ${cls(b.unreal)}`}>{m(sgn(b.unreal))}</b></span>
      {grid && <span className="hr-cell"><em>{t('bt.c.fills')}</em><b className="num">{t('bt.pairs', { n: b.pairs })}</b></span>}
      {grid && nextBuy && <span className="hr-cell"><em>{t('bt.nextBuy')}</em><b className="num">{fmtPrice(String(nextBuy))}</b><i className="num">{pct(nextBuy)}</i></span>}
      {grid && nextSell && <span className="hr-cell hr-opt"><em>{t('bt.nextSell')}</em><b className="num">{fmtPrice(String(nextSell))}</b><i className="num">{pct(nextSell)}</i></span>}
      {!grid && Number(b.avg) > 0 && <span className="hr-cell"><em>{t('bt.l.avg')}</em><b className="num">{fmtPrice(b.avg)}</b></span>}
      {nextRung && <span className="hr-cell"><em>{t('bt.nextAdd')}</em><b className="num">{fmtPrice(nextRung.px)}</b><i className="num">{pct(Number(nextRung.px))}</i></span>}
      {b.kind === 'plan' && b.next_at && b.status === 'running' && <span className="hr-cell"><em>{t('bt.nextPlan')}</em><b className="num">{fmtTime(b.next_at)}</b></span>}
    </div>
  )
}

/** 把机器人画进图：设置时画区间和格子，上下限可以拖；运行时画每一格和成交（E） */
function withBot(base: TradeOverlay | undefined, form: BotParams | null, plan: BotPlan | null, kind: BotKind, live: Bot | null, setForm: (p: BotParams) => void): TradeOverlay | undefined {
  const lines: TradeLine[] = [...(base?.lines ?? [])]
  const grid: NonNullable<TradeOverlay['grid']> = []
  let fit: [number, number] | undefined
  const markers = [...(base?.markers ?? [])]
  if (live) {
    const ls = live.plan?.lines ?? []
    if (ls.length) fit = [Number(ls[0]), Number(ls[ls.length - 1])]
    for (const g of live.grids ?? []) {
      const px = Number(g.side === 'buy' ? ls[g.i] : ls[g.i + 1])
      grid.push({ px, tone: g.side === 'buy' ? 'up' : 'down' })
    }
    for (const r of live.rungs ?? []) if (!r.filled) lines.push({ id: `rung-${r.px}`, kind: 'limit', buy: true, price: Number(r.px), label: tr('bt.lineAdd', { v: Number(r.usd).toFixed(0) }) })
    if (Number(live.avg) > 0) lines.push({ id: 'bot-avg', kind: 'entry', buy: true, price: Number(live.avg), label: tr('bt.lineAvg') })
    for (const r of live.recs.slice(-60)) {
      if ((r.kind === 'fill' || r.kind === 'add' || r.kind === 'buy' || r.kind === 'tp') && Number(r.px) > 0) {
        const buy = r.side ? r.side === 'buy' : r.kind !== 'tp'
        markers.push({ id: `bot-${r.t}-${r.px}`, t: r.t, price: Number(r.px), buy, say: '', hint: `${tr(buy ? 'sp.buy' : 'sp.sell')} ${fmtPrice(r.px)}${Number(r.net) ? ` · ${Number(r.net) > 0 ? '+' : '−'}${locDec(Math.abs(Number(r.net)).toFixed(2))}` : ''}`, tone: Number(r.net) > 0 ? 'up' : Number(r.net) < 0 ? 'down' : undefined })
      }
    }
  }
  if (form && !live) {
    if (plan?.lines?.length) for (const x of plan.lines) grid.push({ px: Number(x), tone: 'dim' })
    const drag = (id: string, price: number, label: string, k: TradeLine['kind']) => lines.push({ id, kind: k, price, label, drag: true, say: () => [{ t: label }] })
    if (kind === 'spot_grid' || kind === 'perp_grid') {
      const lo = Number(form.lower), hi = Number(form.upper)
      if (lo > 0) drag('bot-lower', lo, tr('bt.lineLower', { px: fmtPrice(String(lo)) }), 'cond')
      if (hi > 0) drag('bot-upper', hi, tr('bt.lineUpper', { px: fmtPrice(String(hi)) }), 'cond')
      if (Number(form.stop_loss) > 0) drag('bot-sl', Number(form.stop_loss), tr('bt.lineSl', { px: fmtPrice(form.stop_loss ?? '0') }), 'sl')
      if (Number(form.take_profit) > 0) drag('bot-tp', Number(form.take_profit), tr('bt.lineTp', { px: fmtPrice(form.take_profit ?? '0') }), 'tp')
      if (lo > 0 && hi > lo) fit = [lo, hi]
    }
    if (plan?.ladder?.length) {
      for (const r of plan.ladder) grid.push({ px: Number(r.px), tone: 'dim' })
      fit = [Math.min(...plan.ladder.map((r) => Number(r.px))), Math.max(...plan.ladder.map((r) => Number(r.px)))]
    }
    if (kind === 'dip' && Number(form.trigger_px) > 0) drag('bot-trigger', Number(form.trigger_px), tr('bt.lineTrigger', { px: fmtPrice(form.trigger_px ?? '0') }), 'cond')
  }
  if (!base && !lines.length && !grid.length) return undefined
  const onDrag = async (l: TradeLine, price: number) => {
    if (l.id.startsWith('bot-') && form) {
      const k = { 'bot-lower': 'lower', 'bot-upper': 'upper', 'bot-sl': 'stop_loss', 'bot-tp': 'take_profit', 'bot-trigger': 'trigger_px' }[l.id]
      if (k) { setForm({ ...form, [k]: String(Number(price.toFixed(8))) }); return true } // 图表已经吸附到最小价位，这里不再取整，线才会落到松手的位置
      return false
    }
    return base?.onDrag ? base.onDrag(l, price) : false
  }
  return { ...(base ?? { lines: [] }), lines, grid, fit, markers, onDrag }
}

/** 现货图上的线：持仓成本、挂单、条件单 */
function spotOverlay(v: SpotView | null, inst: string, tick?: string): TradeOverlay | undefined {
  if (!v) return undefined
  const lines: TradeLine[] = []
  const base = baseOf(inst)
  const b = v.balances.find((x) => x.coin === base)
  if (b && Number(b.cost) > 0 && Number(b.free) + Number(b.locked) > 0) lines.push({ id: 'cost', kind: 'entry', buy: true, price: Number(b.cost), label: `${tr('sp.lineCost')} ${fmtPrice(b.cost)}` })
  for (const o of v.orders) {
    if (o.instrument !== inst || o.source.startsWith('bot:')) continue // 机器人的单画成网格线，不重复写
    const px = Number(o.type === 'cond_market' ? o.trigger_px : o.price)
    lines.push({ id: o.id, kind: o.type === 'cond_market' ? 'cond' : 'limit', buy: o.side === 'buy', price: px, label: `${tr(o.side === 'buy' ? 'sp.buy' : 'sp.sell')} ${fmtPrice(String(px))}` })
  }
  return { lines, tick: tick ? Number(tick) : undefined }
}

/** 图上面一行：下一个大事还有多久，历史上同类事件后 BTC 一小时动多少（3 小时内变琥珀色） */
function EventLine({ e }: { e: EconEvent }) {
  const t = useT()
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => { const id = window.setInterval(() => setNow(Date.now()), 30_000); return () => window.clearInterval(id) }, [])
  const left = Math.max(0, e.t - now)
  const h = Math.floor(left / 3_600_000), m = Math.floor((left % 3_600_000) / 60_000)
  const when = h > 0 ? t('ev.inHM', { h, m }) : t('ev.inM', { m })
  return (
    <div className={left < 3 * 3_600_000 ? 'mk-event hot' : 'mk-event'} data-event={e.key}>
      <b>{when}</b><span>{evLong(e.key)}</span>
      <span className="mk-event-v">{evVolOk(e) && e.vol ? t('ev.avg', { n: e.vol.n, v: locDec(e.vol.mean.toFixed(2)) }) : t('ev.few')}</span>
    </div>
  )
}

/** 链上叠加层的数据：Hyperliquid 真实强平价分布 + 最大的 5 个持仓，每 30 秒刷新 */
export function useOnchain(coin: string | null): { overlay?: OnchainOverlay; info: string } | null {
  const liq = usePollData<{ bins: OnchainOverlay['bins']; addresses_scanned: number; positions: number; long_value: number; short_value: number; cycle: number; coverage?: number }>(coin ? `/api/onchain/liqmap?coin=${coin}&range=0.12&bins=240` : null, 30000)
  const wh = usePollData<{ address: string; size: number; entry: number; value: number; upnl: number; liq_px: number; all_time_pnl: number; month_pnl: number }[]>(coin ? `/api/onchain/whales?coin=${coin}&n=5` : null, 30000)
  const ev = usePollData<{ time: number; kind: ChainAction['kind']; from_size: number; to_size: number; value: number; price: number; lag_ms: number }[]>(coin ? `/api/onchain/events?coin=${coin}&min=500000&n=60` : null, 30000)
  if (!coin || !liq) return null
  // 第一轮扫描还没完成，也没有上次存下来的结果：先说明，不画空的叠加层
  if (!liq.cycle || !Array.isArray(liq.bins)) return { info: tr('oc.scanning') }
  const usd = (v: number) => fmtCompactL(v)
  return {
    overlay: { coin, bins: liq.bins, coverage: liq.coverage, whales: (wh ?? []).map((w) => ({ addr: w.address, entry: w.entry, size: w.size, value: w.value, upnl: w.upnl ?? 0, liq: w.liq_px, pnl: w.all_time_pnl ?? 0, month: w.month_pnl ?? 0 })), actions: (ev ?? []).map((e) => ({ t: e.time, kind: e.kind, from: e.from_size, to: e.to_size, value: e.value, price: e.price, lag: e.lag_ms })) },
    info: tr('oc.info', { n: liq.addresses_scanned, coin, pos: liq.positions, long: usd(liq.long_value), short: usd(liq.short_value) }),
  }
}

function MaxIcon({ on }: { on: boolean }) {
  return (
    <svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.2" aria-hidden="true">
      {on
        ? <path d="M5.5 1.5v4h-4M8.5 1.5v4h4M5.5 12.5v-4h-4M8.5 12.5v-4h4" />
        : <path d="M1.5 5V1.5H5M9 1.5h3.5V5M1.5 9v3.5H5M9 12.5h3.5V9" />}
    </svg>
  )
}

function ivName(ms: number): string {
  if (ms >= 86_400_000) return tr('iv.d', { n: ms / 86_400_000 })
  return ms < 60000 ? tr('iv.s', { n: ms / 1000 }) : ms < 3600000 ? tr('iv.m', { n: ms / 60000 }) : tr('iv.h', { n: ms / 3600000 })
}

/** 顶部风险条的纪律状态（4.26）：锁定时写倒计时，点击进风险页 */
function LockChip({ acct }: { acct: number }) {
  const t = useT()
  const v = useDiscipline(5000).view
  const lock = v?.enabled ? v.accounts.find((a) => a.account === acct)?.lock : undefined
  const now = useTick(!!lock)
  if (!lock) return null
  const left = lockLeft(lock, now)
  return <a className="mk-lock" {...linkProps('risk')} title={ruleName(lock.rule)}><span className="sq warn" />{t('disc.chip')}{left != null && <span className="num">{hms(left)}</span>}</a>
}

/** 合并档位后价格保留几位小数 */
function decOfGroup(g: number): number {
  const t = String(g)
  return t.includes('.') ? t.split('.')[1].length : 0
}

/** 现货规格前端写死的最小价位表太重复：直接按币种常见值。合约从规格取 */
let specTicks: Record<string, string> = {}
function specsTick(inst: string): string { return specTicks[inst] ?? '' }
export function rememberTicks(list: Record<string, { tick: string }> | null | undefined) { if (list) specTicks = { ...specTicks, ...Object.fromEntries(Object.entries(list).map(([k, v]) => [k, v.tick])) } }
