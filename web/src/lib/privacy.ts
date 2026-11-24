// 公共场合模式（隐藏金额）。D33 配套、D35。
// 快捷键：
//   H              在任何页面直接按（输入框里打字时不触发）
//   Ctrl + Shift + H（Mac：⌘ + Shift + H） 在输入框里也能用
// 状态记在浏览器里，下次打开保持不变。切换时屏幕底部出现一行提示。
import { lastAnchor } from './anchor'
import { getPrefs, hotKey } from './prefs'
import { useSyncExternalStore } from 'react'
import { tr } from './i18n'

const KEY = 'qs.private'
const subs = new Set<() => void>()
let on = read()

function read(): boolean {
  try { return localStorage.getItem(KEY) === '1' } catch { return false }
}

function apply() {
  document.documentElement.dataset.private = on ? '1' : '0'
  try { localStorage.setItem(KEY, on ? '1' : '0') } catch { /* 无痕模式忽略 */ }
  subs.forEach((f) => f())
}

export function setPrivate(v: boolean, announce = true) {
  if (v === on) return
  on = v
  apply()
  if (announce) toast(tr(on ? 'privacy.toastOn' : 'privacy.toastOff'), 'H')
}
export function togglePrivate() { setPrivate(!on) }
export function isPrivate() { return on }

export function usePrivate(): boolean {
  return useSyncExternalStore((f) => { subs.add(f); return () => subs.delete(f) }, () => on)
}

/** 应用启动时调用一次：挂上快捷键 */
export function installPrivacyHotkey() {
  apply()
  window.addEventListener('keydown', (e) => {
    const mod = e.ctrlKey || e.metaKey
    if (mod && e.shiftKey && e.key.toLowerCase() === 'h') { e.preventDefault(); togglePrivate(); return } // 固定的，不随自定义改
    if (e.key.toLowerCase() !== hotKey('privacy')) return
    if (mod || e.altKey || e.repeat || !getPrefs().keys) return // 交易偏好里关了快捷键：单按 H 不再生效（Ctrl+Shift+H 仍可用）
    const t = e.target as HTMLElement | null
    if (t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) return
    e.preventDefault()
    togglePrivate()
  })
}

/** 屏幕底部的一行提示，1.6 秒后消失 */
let toastEl: HTMLDivElement | null = null
let toastTimer = 0
// 带按钮的提示（撤回）显示期间，后台提醒排在后面：不能把用户还要点的按钮盖掉
let holdUntil = 0
let held: [string, string | undefined] | null = null
export function toast(text: string, key?: string) {
  holdUntil = 0 // 用户自己刚做的操作：结果马上写出来
  held = null
  showToast(text, key)
}
/** 后台来的提醒（止盈止损触发、转手动管理）：有撤回按钮时排在后面 */
export function toastNotice(text: string) {
  if (Date.now() < holdUntil) { held = [text, undefined]; return }
  showToast(text)
}
function showToast(text: string, key?: string) {
  if (!toastEl) {
    toastEl = document.createElement('div')
    toastEl.className = 'toast'
    toastEl.setAttribute('role', 'status')
    document.body.appendChild(toastEl)
  }
  toastEl.innerHTML = ''
  const s = document.createElement('span')
  s.textContent = text
  toastEl.appendChild(s)
  if (key) {
    const k = document.createElement('span')
    k.className = 'toast-key'
    const [a, b] = tr('privacy.again').split('{key}')
    const kb = document.createElement('kbd')
    kb.textContent = key
    k.append(a ?? '', kb, b ?? '')
    toastEl.appendChild(k)
  }
  // 锚定（D68）：用户刚点过的按钮旁边升起；后台推送来的提醒（止盈止损触发、强平）没有锚点，出现在屏幕底部
  const a = lastAnchor(900)
  const el = toastEl
  el.classList.remove('anchored')
  el.style.left = el.style.top = el.style.bottom = ''
  if (a) {
    el.classList.add('anchored')
    const w = Math.min(el.offsetWidth || 260, innerWidth - 16)
    const x = Math.max(8 + w / 2, Math.min(innerWidth - 8 - w / 2, a.x))
    const above = a.y - a.h / 2 - 10
    el.style.left = `${x}px`
    if (above > 60) el.style.top = `${above}px`
    else el.style.top = `${a.y + a.h / 2 + 10 + 40}px`
  }
  el.classList.remove('show')
  void el.offsetWidth // 重新触发进入动画
  el.classList.add('show')
  window.clearTimeout(toastTimer)
  toastTimer = window.setTimeout(() => el.classList.remove('show'), a ? 1800 : 2400)
}

/** 带一个按钮的提示：比如"已撤销 [撤回]"。ms 毫秒后消失，按钮也就没了 */
export function toastAction(text: string, label: string, fn: () => void, ms = 5000) {
  showToast(text)
  holdUntil = Date.now() + ms
  const release = () => {
    holdUntil = 0
    if (held) { const [x, k] = held; held = null; showToast(x, k) }
  }
  const el = toastEl!
  el.style.pointerEvents = 'auto'
  const b = document.createElement('button')
  b.type = 'button'
  b.className = 'toast-act'
  b.textContent = label
  b.onclick = () => { el.classList.remove('show'); el.style.pointerEvents = ''; window.clearTimeout(toastTimer); held = null; holdUntil = 0; fn() }
  el.appendChild(b)
  window.clearTimeout(toastTimer)
  toastTimer = window.setTimeout(() => { el.classList.remove('show'); el.style.pointerEvents = ''; release() }, ms)
}
