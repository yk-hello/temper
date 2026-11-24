// 对话框：从打开它的按钮"长出来"，关闭时缩回那个按钮（锚定，D68）；背景同时淡入淡出。
// Esc 或点背景关闭；打开时焦点进入第一个输入框，关闭后回到原处。
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { growFrom, lastAnchor, shrinkTo, type Anchor } from '../lib/anchor'

export function Dialog({ open, title, onClose, children, width = 420 }: { open: boolean; title: string; onClose: () => void; children: ReactNode; width?: number }) {
  const [mounted, setMounted] = useState(open)
  const [shown, setShown] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const back = useRef<HTMLElement | null>(null)
  const anchor = useRef<Anchor | null>(null)
  // onClose 每次重绘都是新的函数：放进 ref，不让下面的效果跟着重跑（重跑会把焦点抢回第一个输入框，正在别的框里打字的人会被打断）
  const closeRef = useRef(onClose)
  closeRef.current = onClose

  useEffect(() => {
    if (open) {
      back.current = document.activeElement as HTMLElement
      anchor.current = lastAnchor()
      setMounted(true)
      requestAnimationFrame(() => setShown(true))
    } else if (mounted) {
      setShown(false)
      let done = false
      const finish = () => { if (done) return; done = true; setMounted(false); back.current?.focus?.() }
      const a = box.current ? shrinkTo(box.current, anchor.current) : null
      if (a) a.finished.then(finish, finish)
      const id = window.setTimeout(finish, 220)
      return () => window.clearTimeout(id)
    }
  }, [open]) // eslint-disable-line react-hooks/exhaustive-deps

  // 挂上之后立刻从锚点长出来（在浏览器画第一帧之前开始，不会闪一下全尺寸）
  useLayoutEffect(() => {
    if (mounted && open && box.current) growFrom(box.current, anchor.current)
  }, [mounted]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!shown) return
    box.current?.querySelector<HTMLElement>('input, button.primary, button')?.focus()
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape') { e.stopPropagation(); closeRef.current() }
      if (e.key === 'Tab' && box.current) {
        // 焦点只在对话框里循环
        const els = Array.from(box.current.querySelectorAll<HTMLElement>('input, button, a[href], [tabindex]:not([tabindex="-1"])')).filter((x) => !x.hasAttribute('disabled'))
        if (!els.length) return
        const i = els.indexOf(document.activeElement as HTMLElement)
        if (e.shiftKey && i <= 0) { e.preventDefault(); els[els.length - 1].focus() }
        else if (!e.shiftKey && i === els.length - 1) { e.preventDefault(); els[0].focus() }
      }
    }
    window.addEventListener('keydown', key, true)
    return () => window.removeEventListener('keydown', key, true)
  }, [shown])

  // 分几步的对话框（比如先填邮箱、再填验证码）：上一步有焦点的输入框整个没了，焦点掉到页面上时，放回对话框里第一个输入框。
  // 只认"原来有焦点的元素已经不在页面上"：点另一个按钮的一瞬间焦点也会短暂离开，那时不能抢（会把焦点拽到别的按钮上）
  const lastFocus = useRef<Element | null>(null)
  useEffect(() => {
    const el = box.current
    if (!shown || !el) return
    const on = (e: FocusEvent) => { lastFocus.current = e.target as Element }
    el.addEventListener('focusin', on)
    return () => el.removeEventListener('focusin', on)
  }, [shown])
  useEffect(() => {
    const el = box.current
    if (!shown || !el || el.contains(document.activeElement) || !lastFocus.current || lastFocus.current.isConnected) return
    el.querySelector<HTMLElement>('input, button.primary, button')?.focus()
  })

  if (!mounted) return null
  return createPortal(
    <div className={`dlg-back ${shown ? 'on' : ''}`} onMouseDown={(e) => { if (e.target === e.currentTarget) onClose() }}>
      <div className="dlg" role="dialog" aria-modal="true" aria-label={title} ref={box} style={{ width }}>
        <div className="dlg-head">{title}</div>
        <div className="dlg-body">{children}</div>
      </div>
    </div>,
    document.body,
  )
}
