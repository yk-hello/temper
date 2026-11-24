import { usePrivate, togglePrivate } from '../lib/privacy'
import { useT } from '../lib/i18n'
import { hotKey, hotLabel } from '../lib/prefs'

/** 金额、持仓数量。公共场合模式下显示成圆点，宽度大致不变，版面不跳。 */
export function Money({ text, unit, className }: { text: string; unit?: string; className?: string }) {
  const hidden = usePrivate()
  const t = useT()
  return (
    <span className={className}>
      {hidden ? <span className="masked" aria-label={t('privacy.masked')}>{'•'.repeat(Math.min(8, Math.max(4, text.replace(/[^0-9]/g, '').length - 1)))}</span> : text}
      {unit && <small className="mu">{unit}</small>}
    </span>
  )
}

/** 顶栏上的开关按钮，旁边标出快捷键 */
export function PrivacyButton() {
  const hidden = usePrivate()
  const t = useT()
  return (
    <button className={`privacy-btn ${hidden ? 'on' : ''}`} onClick={togglePrivate} aria-pressed={hidden} title={t('privacy.title', { k: hotLabel(hotKey('privacy')) })}>
      <EyeIcon off={hidden} />
      <span>{t(hidden ? 'privacy.on' : 'privacy.off')}</span>
      <kbd>H</kbd>
    </button>
  )
}

function EyeIcon({ off }: { off: boolean }) {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.25" aria-hidden="true">
      <path d="M1.5 8s2.4-4.5 6.5-4.5S14.5 8 14.5 8s-2.4 4.5-6.5 4.5S1.5 8 1.5 8Z" />
      <circle cx="8" cy="8" r="2" />
      {off && <path d="M2.5 13.5l11-11" />}
    </svg>
  )
}
