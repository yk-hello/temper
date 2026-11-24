// 动效的"锚点"：记住用户最近一次点的是哪里（或者键盘焦点在哪），弹出来的东西从那里长出来、关的时候缩回去，
// 提示从触发它的按钮旁边升起。这样每个变化都看得出"从哪来、到哪去"（D68）。

export interface Anchor { x: number; y: number; w: number; h: number; t: number }

let last: Anchor | null = null

function record(el: Element | null, x?: number, y?: number) {
  if (!el || !(el instanceof Element)) return
  const r = el.getBoundingClientRect()
  last = { x: x ?? r.left + r.width / 2, y: y ?? r.top + r.height / 2, w: r.width, h: r.height, t: performance.now() }
}

if (typeof window !== 'undefined') {
  // 捕获阶段记录：按钮自己的点击处理还没跑，记录就已经在了
  window.addEventListener('pointerdown', (e) => {
    const el = (e.target as Element | null)?.closest('button, a, [role="button"], [role="option"], tr, .ob-r, canvas') ?? (e.target as Element)
    record(el, e.clientX, e.clientY)
  }, true)
  window.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' || e.key === ' ') record(document.activeElement)
  }, true)
}

/** 最近一次用户动作的位置；超过 maxAge 毫秒就算没有（比如后台推送来的提醒，就不该从按钮里冒出来） */
export function lastAnchor(maxAge = 1200): Anchor | null {
  return last && performance.now() - last.t <= maxAge ? last : null
}

/** 系统设置了"减少动态效果"：所有位移动画都跳过 */
export function reducedMotion(): boolean {
  return typeof window !== 'undefined' && window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
}

export const EASE_OUT = 'cubic-bezier(0.16, 1, 0.3, 1)'
export const EASE_IN = 'cubic-bezier(0.5, 0, 0.75, 0)'

/** 从锚点"长出来"：元素先缩小、挪到锚点那边，再展开到自己的位置。返回动画（可以 await .finished）。 */
export function growFrom(el: HTMLElement, a: Anchor | null, ms = 240): Animation | null {
  if (reducedMotion()) return null
  const r = el.getBoundingClientRect()
  if (!a) return el.animate([{ opacity: 0, transform: 'translateY(6px) scale(0.98)' }, { opacity: 1, transform: 'none' }], { duration: ms * 0.8, easing: EASE_OUT })
  const dx = a.x - (r.left + r.width / 2), dy = a.y - (r.top + r.height / 2)
  const s = Math.max(0.3, Math.min(0.9, Math.max(a.w / r.width, a.h / r.height)))
  return el.animate(
    [{ opacity: 0, transform: `translate(${dx}px, ${dy}px) scale(${s})` }, { opacity: 1, offset: 0.35 }, { opacity: 1, transform: 'none' }],
    { duration: ms, easing: EASE_OUT },
  )
}

/** 缩回锚点（关闭时） */
export function shrinkTo(el: HTMLElement, a: Anchor | null, ms = 170): Animation | null {
  if (reducedMotion()) return null
  const r = el.getBoundingClientRect()
  if (!a) return el.animate([{ opacity: 1, transform: 'none' }, { opacity: 0, transform: 'translateY(4px) scale(0.98)' }], { duration: ms, easing: EASE_IN, fill: 'forwards' })
  const dx = a.x - (r.left + r.width / 2), dy = a.y - (r.top + r.height / 2)
  const s = Math.max(0.3, Math.min(0.9, Math.max(a.w / r.width, a.h / r.height)))
  return el.animate(
    [{ opacity: 1, transform: 'none' }, { opacity: 0, transform: `translate(${dx}px, ${dy}px) scale(${s})` }],
    { duration: ms, easing: EASE_IN, fill: 'forwards' },
  )
}

/** 一段文字从一个位置飞到另一个元素上（共享元素过渡：比如盘口价格飞进价格输入框） */
export function flyText(text: string, from: DOMRect, to: Element, cls = 'fly-text'): Promise<void> {
  if (reducedMotion()) return Promise.resolve()
  const t = to.getBoundingClientRect()
  const el = document.createElement('div')
  el.className = cls
  el.textContent = text
  el.style.left = `${from.left}px`
  el.style.top = `${from.top}px`
  el.style.height = `${from.height}px`
  document.body.appendChild(el)
  const w = el.getBoundingClientRect().width
  const dx = t.right - 12 - w - from.left, dy = t.top + (t.height - from.height) / 2 - from.top
  const anim = el.animate(
    [{ transform: 'translate(0, 0)', opacity: 1 }, { transform: `translate(${dx}px, ${dy}px)`, opacity: 1, offset: 0.85 }, { transform: `translate(${dx}px, ${dy}px)`, opacity: 0 }],
    { duration: 320, easing: EASE_OUT },
  )
  return anim.finished.then(() => el.remove(), () => el.remove())
}
