// 订单流面板：市场页的"订单流"标签和交易页图表共用。
// 顶部写持仓量、多空比、爆仓的结论；中间是足迹图和三个副图，时间轴对齐；右边是爆仓流。
import { useEffect, useMemo, useRef, useState } from 'react'
import { fmtCompactL, useT, type Key } from '../lib/i18n'
import { flowInd, oiSummary, useFootprint, useLiqs, useOIFlow, useRatios } from '../lib/flow'
import { clock } from '../lib/gfx'
import { Scope, type TradeOverlay } from '../charts/Scope'
import type { Candle } from '../lib/demo'
import { fmtNum } from '../charts/scale'
import { locDec } from '../lib/tz'

interface Props {
  inst: string
  interval: number
  dec: number
  candles: Candle[]
  more?: (firstT: number) => void
  version?: number
  trade?: TradeOverlay
}

export function FlowBody({ inst, interval, dec, candles, more, version, trade }: Props) {
  const t = useT()
  const fp = useFootprint(inst, interval)
  const oi = useOIFlow(inst, interval)
  const ratios = useRatios(inst)
  const liq = useLiqs(inst)
  const box = useRef<HTMLDivElement>(null)
  const [wide, setWide] = useState(true)
  useEffect(() => {
    if (!box.current) return
    const ro = new ResizeObserver((es) => setWide(es[0].contentRect.width >= 860))
    ro.observe(box.current)
    return () => ro.disconnect()
  }, [])
  const pts = useMemo(() => oi?.points ?? [], [oi])
  const ind = useMemo(() => flowInd(candles, pts), [candles, pts])
  const sum = oiSummary(pts, Date.now(), 3600_000)
  const footprint = useMemo(() => ({ bars: fp.bars, step: fp.recorded ? fp.step : 0, version: fp.version }), [fp])
  return (
    <div className="flow-wrap" ref={box}>
      <div className="heat-read">
        {sum && (
          <span className="hr-cell" title={t('fl.hintOI')}>
            <em>{t('fl.readOI')}</em>
            <b className="num">{fmtCompactL(sum.usd)}</b>
            <i className="num">{t('fl.readOI1h')} {sum.pct > 0 ? '+' : ''}{locDec(sum.pct.toFixed(2))}%</i>
            {sum.main && <i>{t('fl.readMain', { kind: t(`fl.kind.${sum.main}` as Key) })}</i>}
          </span>
        )}
        {ratios && (
          <span className="hr-cell" title={t('fl.hintRatio')}>
            <em>{t('fl.readRatio')}</em>
            <b className="num">{locDec(ratios.account.ratio.toFixed(2))}</b>
            <span className="hr-opt"><i>{t('fl.readTop')}</i><b className="num">{locDec(ratios.top.ratio.toFixed(2))}</b></span>
          </span>
        )}
        {liq && (
          <span className="hr-cell hr-liq2" title={t('fl.hintLiq')}>
            <em>{t('fl.read1h')}</em>
            <i>{t('fl.longLiq')}</i><b className="num down">{fmtCompactL(liq.h1.long)}</b>
            <i>{t('fl.shortLiq')}</i><b className="num up">{fmtCompactL(liq.h1.short)}</b>
          </span>
        )}
      </div>
      <div className="flow-main">
        <div className="flow-chart">
          <Scope key="fp" mode="candle" candles={candles} interval={interval} span={5 * 60_000} dec={dec} live version={version} pan={{ onNeedMore: more }} ind={ind} footprint={footprint} trade={trade} />
        </div>
        {wide && (
          <aside className="flow-liq" aria-label={t('fl.liqTitle')}>
            <div className="fl-head" title={t('fl.hintStream')}>{t('fl.liqTitle')}</div>
            <div className="fl-rows">
              {liq?.items.map((x, i) => (
                <div className="fl-row" key={`${x.t}-${i}`} title={t(x.side === 'long' ? 'fl.tipLong' : 'fl.tipShort', { px: fmtNum(x.p, dec), v: fmtCompactL(x.usd) })}>
                  <span className="num fl-t">{clock(x.t)}</span>
                  <span className="fl-ven" title={t(`fl.ven.${x.venue ?? 'binance'}` as Key)}>{t(`fl.venS.${x.venue ?? 'binance'}` as Key)}</span>
                  <span className={x.side === 'long' ? 'down' : 'up'}>{t(x.side === 'long' ? 'fl.long' : 'fl.short')}</span>
                  <span className="num fl-v">{fmtCompactL(x.usd)}</span>
                  <span className="num fl-p">{fmtNum(x.p, dec)}</span>
                </div>
              ))}
              {liq && liq.items.length === 0 && <p className="fl-empty">{t('fl.liqEmpty', { t: clock(liq.since, false) })}</p>}
            </div>
          </aside>
        )}
      </div>
      <div className="heat-note">
        <span title={t('fl.hintCells')}>{t('fl.lgCells')}</span>
        <span title={t('fl.hintImb')}><i className="fl-imb" />{t('fl.lgImb', { n: 3 })}</span>
        <span title={t('fl.hintOI')}>{t('fl.lgOI')}</span>
        <span className="heat-note-r">
          {!fp.recorded ? t('fl.notRecorded') : interval < 60_000 ? t('fl.noSec') : fp.since ? t('fl.since', { t: clock(fp.since, false) }) : ''}
        </span>
      </div>
    </div>
  )
}
