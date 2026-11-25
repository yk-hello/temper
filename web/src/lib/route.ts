// 页面切换不整页刷新（更像原生应用）：只改地址栏，浏览器前进后退照常可用。
// 11-25 去掉了切换时的淡入淡出（视图过渡）：用户说"每次打开切换页面都感觉像 PPT"。实测新页面 60～90 毫秒就画好了，
// 淡入淡出却要拖到 330 毫秒，中间旧页面定格；现在点了就换。交易页里拖动面板的过渡（lib/layout.ts）不受影响。
import { useSyncExternalStore } from 'react'

const subs = new Set<() => void>()
let search = location.search
/** 这次打开（或刷新）页面时的地址：登录页"刷新回首页"只看它，页面里点过去的不算 */
export const bootSearch = location.search

function emit() {
  search = location.search
  subs.forEach((f) => f())
}
window.addEventListener('popstate', emit)

export function usePage(): string {
  const s = useSyncExternalStore((f) => { subs.add(f); return () => subs.delete(f) }, () => search)
  return new URLSearchParams(s).get('page') ?? ''
}

/** 跳到 ?page=xxx（空字符串回到行情页）。replace：不留历史记录（例如登录成功后）。 */
export function go(page: string, replace = false) {
  const url = page ? `?page=${page}` : location.pathname
  if (url === location.search || (!page && !location.search)) return
  history[replace ? 'replaceState' : 'pushState'](null, '', url)
  emit()
  window.scrollTo(0, 0)
}

/** 链接点击：普通左键走内部切换，Ctrl/⌘/中键照常新开标签页 */
export function linkProps(page: string) {
  return {
    href: page ? `?page=${page}` : '?',
    onClick: (e: React.MouseEvent) => {
      if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
      e.preventDefault()
      go(page)
    },
  }
}

/** 切到某个币的交易页：已经在交易页就直接换品种，不重新加载（命令面板、品种搜索、自选列用） */
export function pickInstrument(inst: string) {
  const onTrade = new URLSearchParams(location.search).get('page') === 'trade' || (!location.search && document.querySelector('.mk'))
  if (onTrade) window.dispatchEvent(new CustomEvent('qs:pick', { detail: inst }))
  else go(`trade&i=${encodeURIComponent(inst)}`)
}
