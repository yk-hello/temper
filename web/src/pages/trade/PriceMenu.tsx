// 图上点右键下单（18 第 6.5 条）：在点的这个价位挂限价单、设条件单、设止盈止损、复制价格。
// 下单的几项只把价格、方向、类型填进右边的下单区（数量、确认照常），不会一点就成交；止盈止损和拖线一样直接改。
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useT } from '../../lib/i18n'
import { act, rejectText, setTicket, toTick } from '../../lib/trade'
import { fmtPrice } from '../../lib/format'
import { toast } from '../../lib/privacy'

export interface MenuAt { px: number; x: number; y: number }

export function PriceMenu({ at, last, tick, dec, spot, acct, inst, pos, onClose, onTicket }: {
  at: MenuAt
  last: number
  tick: string
  dec: number
  spot: boolean
  acct: number | null
  inst: string
  /** 这个品种现在的仓位：long / short，没有是 null */
  pos: 'long' | 'short' | null
  onClose: () => void
  /** 填进下单区之前：切到"下单"标签 */
  onTicket: () => void
}) {
  const t = useT()
  const box = useRef<HTMLDivElement>(null)
  const [xy, setXy] = useState({ x: at.x, y: at.y })
  const px = toTick(at.px, tick)
  const pxs = fmtPrice(px, dec)
  const below = at.px < last
  useLayoutEffect(() => {
    const el = box.current
    if (!el) return
    const r = el.getBoundingClientRect()
    setXy({ x: Math.min(at.x, window.innerWidth - r.width - 8), y: Math.min(at.y, window.innerHeight - r.height - 8) })
  }, [at.x, at.y])
  useEffect(() => {
    const away = (e: PointerEvent) => { if (!box.current?.contains(e.target as Node)) onClose() }
    const key = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('pointerdown', away, true)
    window.addEventListener('keydown', key)
    return () => { window.removeEventListener('pointerdown', away, true); window.removeEventListener('keydown', key) }
  }, [onClose])
  const ticket = (side: 'buy' | 'sell', kind: 'limit' | 'cond') => { onTicket(); setTicket({ price: px, side, kind }); onClose() }
  const tpsl = async (kind: 'tp' | 'sl') => {
    onClose()
    if (!acct) return
    try {
      await act('POST', `/api/trade/${acct}/tpsl`, { instrument: inst, [kind]: px, trigger_by: 'mark' })
      toast(t(kind === 'tp' ? 'pm.tpSet' : 'pm.slSet', { px: pxs }))
    } catch (e) { toast(rejectText(e)) }
  }
  // 止盈在赚钱那一边、止损在亏钱那一边：多仓的止盈在上面，空仓的止盈在下面
  const tpSide = pos && ((pos === 'long') !== below)
  const item = (label: string, fn: () => void, data: string) => (
    <button type="button" role="menuitem" onClick={fn} data-pm={data}>{label}</button>
  )
  return (
    <div className="pm" ref={box} role="menu" style={{ left: xy.x, top: xy.y }} data-price-menu>
      <div className="pm-px num">{pxs}</div>
      {below
        ? <>{item(t('pm.limitBuy', { px: pxs }), () => ticket('buy', 'limit'), 'limit-buy')}{!spot && item(t('pm.condSell', { px: pxs }), () => ticket('sell', 'cond'), 'cond-sell')}</>
        : <>{item(t('pm.limitSell', { px: pxs }), () => ticket('sell', 'limit'), 'limit-sell')}{!spot && item(t('pm.condBuy', { px: pxs }), () => ticket('buy', 'cond'), 'cond-buy')}</>}
      {!spot && pos && acct && (tpSide
        ? item(t('pm.tp', { px: pxs }), () => void tpsl('tp'), 'tp')
        : item(t('pm.sl', { px: pxs }), () => void tpsl('sl'), 'sl'))}
      {item(t('pm.copy'), () => { void navigator.clipboard?.writeText(px).then(() => toast(t('pm.copied', { px: pxs })), () => undefined); onClose() }, 'copy')}
      <p className="pm-note">{t('pm.note')}</p>
    </div>
  )
}
