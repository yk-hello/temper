// 全站的悬停说明：不用系统自带的灰色提示（3.29），所有 title 都换成这个样子。
// 做法：鼠标移到带 title 的元素上时，把 title 收到 data-tip（这样系统提示不会再出来），自己画一个。
// 从业者不悬停就不打扰（2.30）。键盘聚焦、点击、滚动时收起。

let el: HTMLDivElement | null = null
let timer = 0
let cur: HTMLElement | null = null

function hide() {
  window.clearTimeout(timer)
  cur = null
  el?.classList.remove('show')
}

function show(target: HTMLElement, text: string) {
  if (!el) {
    el = document.createElement('div')
    el.className = 'tip-pop'
    el.setAttribute('role', 'tooltip')
    document.body.appendChild(el)
  }
  el.textContent = text
  el.classList.remove('show')
  const r = target.getBoundingClientRect()
  el.style.left = '0px'
  el.style.top = '0px'
  const w = el.offsetWidth, h = el.offsetHeight
  const x = Math.max(8, Math.min(innerWidth - w - 8, r.left + r.width / 2 - w / 2))
  const below = r.bottom + 6 + h < innerHeight
  el.style.left = `${x}px`
  el.style.top = `${below ? r.bottom + 6 : Math.max(8, r.top - h - 6)}px`
  el.classList.toggle('above', !below)
  void el.offsetWidth
  el.classList.add('show')
}

export function installTips() {
  if (typeof window === 'undefined' || matchMedia('(hover: none)').matches) return
  document.addEventListener('pointerover', (e) => {
    if ((e as PointerEvent).pointerType === 'touch') return
    const t = (e.target as Element | null)?.closest?.('[title], [data-tip]') as HTMLElement | null
    if (!t) { hide(); return }
    if (t.hasAttribute('title')) {
      const v = t.getAttribute('title') ?? ''
      t.removeAttribute('title')
      if (v) t.dataset.tip = v
    }
    const text = t.dataset.tip
    if (t === cur) return
    hide()
    if (!text) return
    cur = t
    timer = window.setTimeout(() => { if (cur === t && t.isConnected) show(t, text) }, 260)
  }, true)
  document.addEventListener('pointerdown', hide, true)
  window.addEventListener('scroll', hide, true)
  window.addEventListener('keydown', hide, true)
  window.addEventListener('blur', hide)
}
