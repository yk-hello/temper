// 行情页的可调布局（D39）：拖动分隔线调整盘口宽度、右栏宽度、底部面板高度；双击分隔线恢复默认；
// 双击面板标题（或按 F）把图表放大到占满，Esc 恢复。尺寸记在浏览器里（只是个人习惯，不需要同步）。
import { useCallback, useEffect, useRef, useState } from 'react'
import { flushSync } from 'react-dom'
import { getPrefs, hotKey } from './prefs'

export interface Sizes { book?: number; side?: number; bottom?: number }
const KEY = 'qs.layout.v1'

function load(): Sizes {
  try { return JSON.parse(localStorage.getItem(KEY) || '{}') as Sizes } catch { return {} }
}
function save(s: Sizes) {
  try { localStorage.setItem(KEY, JSON.stringify(s)) } catch { /* 无痕模式忽略 */ }
}

/** 用浏览器的视图过渡（Chrome 111+）做布局切换的形变动画；不支持时直接切换 */
export function morph(update: () => void) {
  const d = document as Document & { startViewTransition?: (cb: () => void) => unknown }
  // flushSync：让 React 在过渡回调里同步更新页面，浏览器才能拍到"变化后"的样子
  if (d.startViewTransition && !matchMedia('(prefers-reduced-motion: reduce)').matches) d.startViewTransition(() => flushSync(update))
  else update()
}

export function useLayout() {
  const [sizes, setSizes] = useState<Sizes>(load)
  const [max, setMax] = useState<string | null>(null)
  const ref = useRef(sizes)
  ref.current = sizes
  const maxRef = useRef(max)
  maxRef.current = max

  const style = {
    ...(sizes.book ? { '--book-w': `${sizes.book}px` } : {}),
    ...(sizes.side ? { '--side-w': `${sizes.side}px` } : {}),
    ...(sizes.bottom ? { '--bottom-h': `${sizes.bottom}px` } : {}),
  } as React.CSSProperties

  /** 开始拖动。key：调哪个尺寸；axis：x 或 y；dir：鼠标往正方向移动时尺寸是变大（1）还是变小（-1） */
  const drag = useCallback((key: keyof Sizes, axis: 'x' | 'y', dir: 1 | -1, min: number, maxFn: () => number) => (e: React.PointerEvent<HTMLElement>) => {
    e.preventDefault()
    const el = e.currentTarget
    const panel = el.parentElement as HTMLElement
    const start = axis === 'x' ? e.clientX : e.clientY
    const startSize = ref.current[key] ?? (axis === 'x' ? panel.getBoundingClientRect().width : panel.getBoundingClientRect().height)
    el.setPointerCapture(e.pointerId)
    document.body.classList.add(axis === 'x' ? 'resizing-x' : 'resizing-y')
    const move = (ev: PointerEvent) => {
      const d = ((axis === 'x' ? ev.clientX : ev.clientY) - start) * dir
      const v = Math.round(Math.max(min, Math.min(maxFn(), startSize + d)))
      setSizes((s) => ({ ...s, [key]: v }))
    }
    const up = () => {
      el.removeEventListener('pointermove', move)
      el.removeEventListener('pointerup', up)
      el.removeEventListener('pointercancel', up)
      document.body.classList.remove('resizing-x', 'resizing-y')
      save(ref.current)
    }
    el.addEventListener('pointermove', move)
    el.addEventListener('pointerup', up)
    el.addEventListener('pointercancel', up)
  }, [])

  const reset = useCallback((key: keyof Sizes) => () => {
    morph(() => setSizes((s) => { const n = { ...s }; delete n[key]; save(n); return n }))
  }, [])

  const toggleMax = useCallback((panel: string) => morph(() => setMax((m) => (m === panel ? null : panel))), [])

  // F 放大图表 / 恢复，Esc 恢复（输入框里打字时不触发）
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null
      if (t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) return
      if (e.ctrlKey || e.metaKey || e.altKey) return
      if (e.key.toLowerCase() === hotKey('max') && !e.repeat && getPrefs().keys) { e.preventDefault(); morph(() => setMax(maxRef.current ? null : 'chart')) }
      if (e.key === 'Escape' && maxRef.current) morph(() => setMax(null))
    }
    window.addEventListener('keydown', on)
    return () => window.removeEventListener('keydown', on)
  }, [])

  return { style, max, drag, reset, toggleMax }
}
