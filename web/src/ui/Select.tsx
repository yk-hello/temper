// 下拉选择：替代系统自带的 <select>（系统下拉是白底高亮，和整体风格不搭，各系统长得还不一样）。
// 每个选项可以带一行说明，新手看得懂；键盘：↑↓ 移动，Enter / 空格选中，Esc 关闭，Tab 离开。
import { colon } from '../lib/i18n'
import { useEffect, useId, useLayoutEffect, useRef, useState } from 'react'

interface Opt<T extends string> { value: T; label: string; hint?: string }

export function Select<T extends string>({ value, options, onChange, label, className }: { value: T; options: Opt<T>[]; onChange: (v: T) => void; label: string; className?: string }) {
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)
  const [up, setUp] = useState(false)
  const [pos, setPos] = useState<React.CSSProperties>({})
  const box = useRef<HTMLDivElement>(null)
  const btn = useRef<HTMLButtonElement>(null)
  const list = useRef<HTMLUListElement>(null)
  const id = useId()
  // 选项还没加载（比如品种列表）时不能整页崩掉：先显示当前值本身
  const cur = options.find((o) => o.value === value) ?? options[0] ?? { value, label: String(value) }

  useEffect(() => {
    if (!open) return
    setActive(Math.max(0, options.findIndex((o) => o.value === value)))
    list.current?.focus({ preventScroll: true })
    const away = (e: PointerEvent) => { if (!box.current?.contains(e.target as Node)) setOpen(false) }
    document.addEventListener('pointerdown', away)
    return () => document.removeEventListener('pointerdown', away)
  }, [open]) // eslint-disable-line react-hooks/exhaustive-deps

  // 打开后、画到屏幕前量一下真实大小（11-25 走查：下单区"定价"菜单 9 项带说明，实际 540 多像素高，
  // 原来按最高 320 估算；下单区窄于菜单时又改成往右开，伸出面板、被裁掉一半）。
  // 可见区域 = 窗口和外面所有会裁掉内容的面板的交集（11-17 走查：下单区最底下的"预案"往下开，被下单按钮压住、点不到）。
  // 上下：下面放得下就往下开；放不下、上面更宽就往上开；都放不下就限高、菜单里面滚。
  // 左右：先按样式放（一般和按钮右边对齐），伸出可见区域就挪回来；可见区域比菜单还窄就把菜单收窄。
  useLayoutEffect(() => {
    if (!open) return
    const ul = list.current, bx = box.current, bt = btn.current
    if (!ul || !bx || !bt) return
    let top = 0, bottom = window.innerHeight, left = 0, right = window.innerWidth
    for (let el = bx.parentElement; el && el !== document.body; el = el.parentElement) {
      const st = getComputedStyle(el)
      if (/(auto|scroll|hidden|clip)/.test(`${st.overflowX} ${st.overflowY}`)) {
        const r = el.getBoundingClientRect()
        top = Math.max(top, r.top); bottom = Math.min(bottom, r.bottom); left = Math.max(left, r.left); right = Math.min(right, r.right)
      }
    }
    const pad = 8, gap = 4
    const b = bt.getBoundingClientRect(), o = bx.getBoundingClientRect(), r = ul.getBoundingClientRect()
    const need = ul.scrollHeight
    const below = bottom - b.bottom - gap - pad, above = b.top - top - gap - pad
    const goUp = need > below && above > below
    const next: React.CSSProperties = { maxHeight: Math.max(96, Math.floor(Math.min(need, goUp ? above : below))) }
    const w = Math.min(r.width, right - left - 2 * pad)
    let x = r.left
    if (x + w > right - pad) x = right - pad - w
    if (x < left + pad) x = left + pad
    if (Math.abs(x - r.left) > 0.5 || w < r.width - 0.5) Object.assign(next, { left: Math.round(x - o.left), right: 'auto', width: Math.floor(w) })
    setUp(goUp)
    setPos(next)
  }, [open]) // eslint-disable-line react-hooks/exhaustive-deps
  // 量的时候先把菜单压成 0 高：不然没限高的那一下会把外面的面板撑出滚动条，按钮跟着挪，量出来的位置就偏了
  const show = () => { setPos({ maxHeight: 0 }); setUp(false); setOpen(true) }
  // 菜单限高、里面能滚时，用键盘上下移动要把当前项滚到看得见
  // （只滚菜单自己，不用 scrollIntoView：它会连外面的下单区一起滚）
  useEffect(() => {
    const ul = list.current, li = ul?.querySelector<HTMLElement>('.act')
    if (!open || !ul || !li) return
    if (li.offsetTop < ul.scrollTop) ul.scrollTop = li.offsetTop - 4
    else if (li.offsetTop + li.offsetHeight > ul.scrollTop + ul.clientHeight) ul.scrollTop = li.offsetTop + li.offsetHeight - ul.clientHeight + 4
  }, [open, active, pos])
  const pick = (i: number) => { onChange(options[i].value); setOpen(false); btn.current?.focus() }
  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); setActive((a) => Math.min(options.length - 1, a + 1)) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setActive((a) => Math.max(0, a - 1)) }
    else if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); e.stopPropagation(); pick(active) }
    else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); setOpen(false); btn.current?.focus() }
    else if (e.key === 'Tab') setOpen(false)
  }

  return (
    <div className={`sel ${open ? 'on' : ''}${open && up ? ' up' : ''} ${className ?? ''}`} ref={box}>
      <button type="button" ref={btn} className="sel-btn" aria-haspopup="listbox" aria-expanded={open} aria-label={`${label}${colon()}${cur.label}`} title={label}
        onClick={() => (open ? setOpen(false) : show())}
        onKeyDown={(e) => { if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); show() } }}>
        <span>{cur.label}</span>
        <svg width="9" height="9" viewBox="0 0 9 9" aria-hidden="true"><path d="M1.5 3l3 3 3-3" fill="none" stroke="currentColor" strokeWidth="1.2" /></svg>
      </button>
      {open && (
        <ul className="sel-pop" role="listbox" ref={list} tabIndex={-1} style={pos} aria-label={label} aria-activedescendant={`${id}-${active}`} onKeyDown={onKey}>
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
