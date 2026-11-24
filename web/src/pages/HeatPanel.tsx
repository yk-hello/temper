// 频谱面板：市场页的"频谱"标签和交易页图表共用。
// 顶部写算好的结论，图只用来看依据；金额下限、叠加层、时长都在工具栏里。
import { useEffect, useMemo, useState } from 'react'
import { HeatChart, type HeatLayers } from '../charts/HeatChart'
import { HEAT_FLOORS, HEAT_SPANS, type HeatStatus, type HeatWall, getHeatRamp, setHeatRamp, type HeatRamp } from '../lib/heat'
import { fmtCompactL, fmtTime, useT, type Key, listSep } from '../lib/i18n'
import { fmtNum } from '../charts/scale'
import { locDec } from '../lib/tz'
import { Seg } from '../ui/Seg'
import { CheckPop, RadioPop } from './trade/ChartTools'
import type { OnchainOverlay, TradeLine } from '../charts/Scope'
import { usePoll } from '../lib/follow'

const LAYER_KEY = 'qs.heatLayers'
const DEFAULT_LAYERS: HeatLayers = { events: true, chain: true, whales: false, own: true }
const FLOOR_KEY = 'qs.heatFloor'
const SPAN_KEY = 'qs.heatSpan'

export function useHeatLayers() {
  const [v, set] = useState<HeatLayers>(() => {
    try { return { ...DEFAULT_LAYERS, ...JSON.parse(localStorage.getItem(LAYER_KEY) ?? '{}') } } catch { return DEFAULT_LAYERS }
  })
  return [v, (n: HeatLayers) => { set(n); try { localStorage.setItem(LAYER_KEY, JSON.stringify(n)) } catch { /* 忽略 */ } }] as const
}

export function useHeatFloor() {
  const [v, set] = useState<number>(() => {
    const n = Number((() => { try { return localStorage.getItem(FLOOR_KEY) } catch { return null } })())
    return HEAT_FLOORS.includes(n) ? n : 0
  })
  return [v, (n: number) => { set(n); try { localStorage.setItem(FLOOR_KEY, String(n)) } catch { /* 忽略 */ } }] as const
}

export function useHeatInstruments(): string[] {
  const d = usePoll<{ instruments: HeatStatus[] }>('/api/heat/status', 60_000).data
  return useMemo(() => d?.instruments.map((x) => x.instrument) ?? [], [d])
}

export function useHeatSpan() {
  const [span, set] = useState<number>(() => {
    const v = Number((() => { try { return localStorage.getItem(SPAN_KEY) } catch { return null } })())
    return HEAT_SPANS.includes(v) ? v : 2 * 3600_000 // 清单写的是"过去几个小时"
  })
  return [span, (s: number) => { set(s); try { localStorage.setItem(SPAN_KEY, String(s)) } catch { /* 忽略 */ } }] as const
}

export function HeatTools({ span, onSpan, layers, onLayers, floor, onFloor, hasOwn }: { span: number; onSpan: (s: number) => void; layers: HeatLayers; onLayers: (l: HeatLayers) => void; floor: number; onFloor: (n: number) => void; hasOwn: boolean }) {
  const t = useT()
  const opts = HEAT_SPANS.map((ms) => ({ value: String(ms), label: t(`heat.span.${ms}` as Key) }))
  // 这一页没有数据的开关不显示，也不算数（比如市场页、没登录时的"我的仓位"）
  const keys = (['events', 'chain', 'whales', 'own'] as const).filter((k) => k !== 'own' || hasOwn)
  const n = keys.filter((k) => layers[k]).length
  const [ramp, setRamp] = useState<HeatRamp>(getHeatRamp)
  return (
    <>
      <RadioPop label={floor ? t('heat.floorOn', { v: fmtCompactL(floor) }) : t('heat.floor')} testId="heat-floor" active={floor > 0} value={floor} onChange={onFloor} hint={t('heat.floorHint')}
        options={HEAT_FLOORS.map((v) => ({ value: v, label: v ? t('heat.floorOn', { v: fmtCompactL(v) }) : t('heat.floorAll') }))} />
      <CheckPop label={t('heat.layers', { n })} testId="heat-layers" active={n > 0} items={keys.map((k) => ({
        key: k, label: t(`heat.layer.${k}` as Key), on: layers[k], set: (on: boolean) => onLayers({ ...layers, [k]: on }),
      }))} />
      <RadioPop label={t(ramp === 'teal' ? 'heat.rampTeal' : 'heat.rampGray')} testId="heat-ramp" active={false} value={ramp} onChange={(v: HeatRamp) => { setHeatRamp(v); setRamp(v) }} hint={t('heat.rampHint')}
        options={[{ value: 'gray' as HeatRamp, label: t('heat.rampGray') }, { value: 'teal' as HeatRamp, label: t('heat.rampTeal') }]} />
      <div className="head-iv"><Seg label={t('heat.spanLabel')} options={opts} value={String(span)} onChange={(v) => onSpan(Number(v))} /></div>
    </>
  )
}

function WallRead({ side, w, dec }: { side: 'up' | 'down'; w?: HeatWall; dec: number }) {
  const t = useT()
  const name = t(side === 'up' ? 'heat.readAbove' : 'heat.readBelow')
  if (!w) return <span className="hr-cell" title={t(side === 'up' ? 'heat.hintAbove' : 'heat.hintBelow')}><em>{name}</em><b className="hr-none">{t('heat.readNone')}</b></span>
  const age = w.age < 60 ? t('heat.ageS', { n: w.age }) : w.age < 3600 ? t('heat.ageM', { n: Math.floor(w.age / 60) }) : t('heat.ageH', { h: Math.floor(w.age / 3600), m: Math.floor((w.age % 3600) / 60) })
  return (
    <span className="hr-cell" title={t(side === 'up' ? 'heat.hintAbove' : 'heat.hintBelow')}>
      <em>{name}</em>
      <b className="num">{fmtNum(w.p, dec)}</b>
      <b className="num">{fmtCompactL(w.usd)}</b>
      <i className="num">{w.dist > 0 ? '+' : w.dist < 0 ? '−' : ''}{locDec(String(Math.abs(w.dist)))}%</i>
      <i>{t('heat.readAge', { age })}</i>
      {w.touch > 0 && <i>{t('heat.readTouch', { n: w.touch })}</i>}
    </span>
  )
}

export function HeatBody({ inst, dec, span, layers, floor = 0, chain, own }: { inst: string; dec: number; span: number; layers: HeatLayers; floor?: number; chain?: OnchainOverlay; own?: TradeLine[] }) {
  const t = useT()
  const known = useHeatInstruments()
  const empty = { connected: false, loaded: false, syncing: false }
  const [st, setSt] = useState<{ connected: boolean; full?: [number, number]; since?: number; loaded: boolean; syncing: boolean }>(empty)
  const [read, setRead] = useState<{ walls: HeatWall[]; flow: [number, number]; mid: number } | null>(null)
  const [scale, setScale] = useState<{ lo: number; hi: number } | null>(null)
  useEffect(() => { setSt(empty); setRead(null); setScale(null) }, [inst]) // eslint-disable-line react-hooks/exhaustive-deps
  if (known.length && !known.includes(inst)) {
    return <div className="heat-out"><p>{t('heat.notRecorded', { list: known.map((x) => x.replace('-PERP', '')).join(listSep()) })}</p></div>
  }
  const above = read?.walls.find((w) => !w.bid)
  const below = read?.walls.find((w) => w.bid)
  const total = read ? read.flow[0] + read.flow[1] : 0
  const buyPct = total > 0 ? Math.round((read!.flow[0] / total) * 100) : 0
  return (
    <div className="heat-wrap">
      <div className="heat-read">
        <WallRead side="up" w={above} dec={dec} />
        <WallRead side="down" w={below} dec={dec} />
        {total > 0 && (
          <span className="hr-cell hr-flow" title={t('heat.hintFlow')}>
            <em>{t('heat.readFlow')}</em>
            <span className="hr-bar" aria-hidden="true"><i style={{ width: `${buyPct}%` }} /></span>
            <b className="num up">{buyPct}%</b>
            <b className="num down">{100 - buyPct}%</b>
          </span>
        )}
      </div>
      <HeatChart inst={inst} dec={dec} span={span} layers={layers} floor={floor} chain={chain} own={own} onStatus={setSt} onRead={setRead} onScale={setScale} />
      <div className="heat-note">
        <span className="hn-scale" title={t('heat.hintScale')}>
          <em>{t('heat.lgBand')}</em>
          {scale && <b className="num">{fmtCompactL(scale.lo)}</b>}
          <i className="lg-band" />
          {scale && <b className="num">{fmtCompactL(scale.hi)}</b>}
        </span>
        <span><i className="lg-dot up" />{t('heat.lgBuy')}</span>
        <span><i className="lg-dot down" />{t('heat.lgSell')}</span>
        {layers.events && <span title={t('heat.hintEvents')}><i className="lg-x" />{t('heat.lgEvents')}</span>}
        {layers.chain && (
          <span title={t('heat.hintLiq')}>
            <i className="lg-liq" />
            {chain ? t('heat.lgLiq', { pct: Math.round((chain.coverage ?? 0) * 100) }) : t('heat.lgLiqWait')}
          </span>
        )}
        <span className="heat-note-r" title={st.full && st.full[0] > 0 ? t('heat.coverMore') : undefined}>
          {st.syncing ? t('heat.syncing') : !st.connected ? t('heat.connecting') : st.full && st.full[0] > 0 ? t('heat.cover', { lo: fmtNum(st.full[0], dec), hi: fmtNum(st.full[1], dec) }) : t('heat.waiting')}
          {st.full && st.full[0] > 0 && <span className="hn-more"> · {t('heat.coverMore')}</span>}
          {st.since ? <span className="hn-more"> · {t('heat.since', { t: fmtTime(st.since) })}</span> : null}
        </span>
      </div>
    </div>
  )
}
