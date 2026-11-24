// 新的高光：只在顶部亮一条（D91），写一句发生了什么，点"查看"去生涯档案。8 秒后自己收起。
// 不推"趁热再来一单"之类的话（`05` 第 7 节）。
import { useEffect, useState } from 'react'
import { coinOf } from '../lib/highlight'
import { useT } from '../lib/i18n'
import { go } from '../lib/route'

export interface HlPing { id: string; inst: string; side: string; roe: string; lev: string }

export function HighlightToast() {
  const t = useT()
  const [p, setP] = useState<HlPing | null>(null)
  useEffect(() => {
    const on = (e: Event) => setP((e as CustomEvent<HlPing>).detail)
    window.addEventListener('qs:highlight', on)
    return () => window.removeEventListener('qs:highlight', on)
  }, [])
  useEffect(() => {
    if (!p) return
    const id = window.setTimeout(() => setP(null), 8000)
    return () => window.clearTimeout(id)
  }, [p])
  if (!p) return null
  const spot = p.inst.endsWith('-SPOT')
  return (
    <div className="hl-ping" role="status" data-hl-ping>
      <span className="hl-ping-k">{t('hl.moment')}</span>
      <span className="hl-ping-t">
        {coinOf(p.inst)} {t(p.side === 'short' ? 'hl.short' : 'hl.long')} <b>+{p.roe}%</b> {t(spot ? 'hl.pingSpot' : 'hl.pingBasis', { n: p.lev })}
      </span>
      <button type="button" className="hl-ping-go" onClick={() => { setP(null); go(`review&tab=hl&h=${p.id}`) }}>{t('hl.view')}</button>
      <button type="button" className="hl-ping-x" aria-label={t('hl.close')} onClick={() => setP(null)}>×</button>
    </div>
  )
}
