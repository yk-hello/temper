// 物理像素工具。用户的屏幕是 Windows 125% 缩放（devicePixelRatio = 1.25），
// CSS 的 1px 在那里是 1.25 个物理像素，线会发虚、粗细不一。这里统一处理。

type Listener = (dpr: number) => void
const listeners = new Set<Listener>()

/** 当前缩放比例。窗口拖到另一块屏幕时会变。 */
export function dpr(): number {
  return window.devicePixelRatio || 1
}

/** 把 --hair 设成正好 1 个物理像素，并在缩放比例变化时更新。应用启动时调用一次。 */
export function installHairline(): void {
  const apply = () => {
    const d = dpr()
    const root = document.documentElement
    root.style.setProperty('--hair', `${1 / d}px`)
    root.dataset.dpr = String(d)
    listeners.forEach((l) => l(d))
    // matchMedia 只能监听"当前比例"的变化，所以每次变化后重新挂一次
    const mq = window.matchMedia(`(resolution: ${d}dppx)`)
    mq.addEventListener('change', apply, { once: true })
  }
  apply()
}

export function onDprChange(l: Listener): () => void {
  listeners.add(l)
  return () => listeners.delete(l)
}

