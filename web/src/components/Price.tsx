import { useRef } from 'react'
import { fmtPrice } from '../lib/format'

/**
 * 价格数字。变化时，只在"变动的那几位"下方闪过一道强调色细线，数字本身不变色。
 * 不变的高位保持安静，视线自然落在真正在动的地方。
 */
export function Price({ value, className }: { value: string | undefined; className?: string }) {
  const st = useRef({ text: '', n: 0, from: -1 })
  const text = value ? fmtPrice(value) : ''
  const s = st.current
  if (text && text !== s.text) {
    if (s.text) {
      let i = 0
      while (i < text.length && i < s.text.length && text[i] === s.text[i]) i++
      s.from = text.length === s.text.length ? i : 0
      s.n++
    }
    s.text = text
  }
  if (!text) return <span className={className}><span className="skel" /></span>
  if (s.from < 0) return <span className={className}>{text}</span>
  return (
    <span className={className}>
      {text.slice(0, s.from)}
      <span key={s.n} className="tick">{text.slice(s.from)}</span>
    </span>
  )
}
