import { useRef } from 'react'

/**
 * 价格数字。变化时，只有"变动的那几位"先变成纯白，再在一秒内回到正常亮度；
 * 不变的高位保持安静，视线自然落在真正在动的地方。不闪红绿。
 */
export function Price({ text, className }: { text: string; className?: string }) {
  const st = useRef({ text: '', n: 0, from: -1 })
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
  if (s.from < 0) return <span className={className}>{text}</span>
  return (
    <span className={className}>
      {text.slice(0, s.from)}
      <span key={s.n} className="settle">{text.slice(s.from)}</span>
    </span>
  )
}
