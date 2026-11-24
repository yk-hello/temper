// 分段选择：选中底块从旧选项滑到新选项（不是瞬间切换），让人看清"从哪儿换到了哪儿"。
import { useLayoutEffect, useRef, useState, type ReactNode } from 'react'

interface Props<T extends string> {
  options: { value: T; label: ReactNode }[]
  value: T
  onChange: (v: T) => void
  label: string
}

export function Seg<T extends string>({ options, value, onChange, label }: Props<T>) {
  const box = useRef<HTMLDivElement>(null)
  const [ind, setInd] = useState<{ x: number; w: number } | null>(null)
  const first = useRef(true)
  useLayoutEffect(() => {
    const el = box.current?.querySelector<HTMLButtonElement>('button[aria-pressed="true"]')
    if (!el) return
    setInd({ x: el.offsetLeft, w: el.offsetWidth })
    const b = box.current
    if (b && b.scrollWidth > b.clientWidth && (el.offsetLeft < b.scrollLeft || el.offsetLeft + el.offsetWidth > b.scrollLeft + b.clientWidth)) b.scrollLeft = el.offsetLeft - 8
    const ro = new ResizeObserver(() => setInd({ x: el.offsetLeft, w: el.offsetWidth }))
    ro.observe(el)
    return () => ro.disconnect()
  }, [value])
  useLayoutEffect(() => { if (ind) requestAnimationFrame(() => { first.current = false }) }, [ind])
  // 放不下、可以横着滑的时候，被挡住的那一头渐隐（不然半个字露在边上，像坏了）
  useLayoutEffect(() => {
    const b = box.current
    if (!b) return
    const mark = () => {
      const l = b.scrollLeft > 1, r = b.scrollLeft + b.clientWidth < b.scrollWidth - 1
      const v = (l ? 'l' : '') + (r ? 'r' : '')
      if ((b.dataset.ov ?? '') !== v) { if (v) b.dataset.ov = v; else delete b.dataset.ov }
    }
    mark()
    b.addEventListener('scroll', mark, { passive: true })
    const ro = new ResizeObserver(mark)
    ro.observe(b)
    return () => { b.removeEventListener('scroll', mark); ro.disconnect() }
  }, [options.length])
  return (
    <div className="seg" role="group" aria-label={label} ref={box}>
      {ind && <span className={`seg-ind ${first.current ? 'no-anim' : ''}`} style={{ transform: `translateX(${ind.x - 2}px)`, width: ind.w }} />}
      {options.map((o) => (
        <button type="button" key={o.value} data-tab={o.value} aria-pressed={o.value === value} onClick={() => onChange(o.value)}>{o.label}</button>
      ))}
    </div>
  )
}
