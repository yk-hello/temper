// 全局停机时，下单区顶上写清楚（11-16 走查：原来只有顶栏一个小标记，点下单才被拒）
import { useT } from '../lib/i18n'
import { useSystem } from '../lib/system'

export function HaltNote() {
  const t = useT()
  const h = useSystem(true)
  if (!h || h.level <= 0) return null
  return (
    <div className="op-lock" role="status" data-halt-note>
      <span>{t(h.level >= 2 ? 'adm.haltNote2' : 'adm.haltNote')}{h.reason ? ` ${t('adm.haltWhy', { r: h.reason })}` : ''}</span>
    </div>
  )
}
