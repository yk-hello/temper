// 下拉选择：替代系统自带的 <select>（系统下拉是白底高亮，和整体风格不搭，各系统长得还不一样）。
// 每个选项可以带一行说明，新手看得懂；键盘：↑↓ 移动，Enter / 空格选中，Esc 关闭，Tab 离开。
import { colon } from '../lib/i18n'
import { useEffect, useId, useRef, useState } from 'react'

interface Opt<T extends string> { value: T; label: string; hint?: string }

export function Select<T extends string>({ value, options, onChange, label, className }: { value: T; options: Opt<T>[]; onChange: (v: T) => void; label: string; className?: string }) {
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)
  const [place, setPlace] = useState('')
  const box = useRef<HTMLDivElement>(null)
  const btn = useRef<HTMLButtonElement>(null)
  const list = useRef<HTMLUListElement>(null)
  const id = useId()
  // 选项还没加载（比如品种列表）时不能整页崩掉：先显示当前值本身
  const cur = options.find((o) => o.value === value) ?? options[0] ?? { value, label: String(value) }

  useEffect(() => {
    if (!open) return
    setActive(Math.max(0, options.findIndex((o) => o.value === value)))
    list.current?.focus()
    const away = (e: PointerEvent) => { if (!box.current?.contains(e.target as Node)) setOpen(false) }
    document.addEventListener('pointerdown', away)
    return () => document.removeEventListener('pointerdown', away)
  }, [open]) // eslint-disable-line react-hooks/exhaustive-deps

  // 打开前量一下：按钮下面放不下、上面更宽就往上开；右对齐会伸出可见区域左边就改左对齐。
  // 可见区域 = 窗口和外面所有会裁掉内容的面板的交集（11-17 走查：下单区最底下的"预案"往下开，被下单按钮压住、点不到）
  const show = () => {
    const b = btn.current?.getBoundingClientRect()
    if (b) {
      let top = 0, bottom = window.innerHeight, left = 0
      for (let el = box.current?.parentElement; el && el !== document.body; el = el.parentElement) {
        const s = getComputedStyle(el)
        if (/(auto|scroll|hidden|clip)/.test(`${s.overflowX} ${s.overflowY}`)) {
          const r = el.getBoundingClientRect()
          top = Math.max(top, r.top); bottom = Math.min(bottom, r.bottom); left = Math.max(left, r.left)
        }
      }
      const h = Math.min(320, 8 + options.reduce((n, o) => n + (o.hint ? 50 : 32), 0))
      const up = bottom - b.bottom < h + 8 && b.top - top > bottom - b.bottom
      const toLeft = b.right - 248 < left
      setPlace(`${up ? ' up' : ''}${toLeft ? ' left' : ''}`)
    }
    setOpen(true)
  }
  const pick = (i: number) => { onChange(options[i].value); setOpen(false); btn.current?.focus() }
  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); setActive((a) => Math.min(options.length - 1, a + 1)) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setActive((a) => Math.max(0, a - 1)) }
    else if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); e.stopPropagation(); pick(active) }
    else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); setOpen(false); btn.current?.focus() }
    else if (e.key === 'Tab') setOpen(false)
  }

  return (
    <div className={`sel ${open ? 'on' : ''}${open ? place : ''} ${className ?? ''}`} ref={box}>
      <button type="button" ref={btn} className="sel-btn" aria-haspopup="listbox" aria-expanded={open} aria-label={`${label}${colon()}${cur.label}`} title={label}
        onClick={() => (open ? setOpen(false) : show())}
        onKeyDown={(e) => { if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); show() } }}>
        <span>{cur.label}</span>
        <svg width="9" height="9" viewBox="0 0 9 9" aria-hidden="true"><path d="M1.5 3l3 3 3-3" fill="none" stroke="currentColor" strokeWidth="1.2" /></svg>
      </button>
      {open && (
        <ul className="sel-pop" role="listbox" ref={list} tabIndex={-1} aria-label={label} aria-activedescendant={`${id}-${active}`} onKeyDown={onKey}>
          {options.map((o, i) => (
            <li key={o.value} id={`${id}-${i}`} role="option" aria-selected={o.value === value} className={i === active ? 'act' : ''}
              onPointerEnter={() => setActive(i)}
              // 下拉框放在 <label> 里时（价格旁边的"定价"、触发价旁边的"按什么价"），点选项会被当成点了标签，
              // 浏览器把这一下转给标签里的按钮，下拉框又弹开、挡住下单按钮。取消默认动作就不会转（D103 检查时发现）
              onClick={(e) => { e.preventDefault(); pick(i) }}>
              <span className="sel-l">{o.label}</span>
              {o.hint && <span className="sel-h">{o.hint}</span>}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
