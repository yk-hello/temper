// 交易偏好（账户页"交易偏好"里改）：下单前确认、默认数量单位、键盘快捷键。
// 只存在这台设备的浏览器里（以后接实盘时搬到服务器，跟着账号走，见 docs/13）。
import { useSyncExternalStore } from 'react'

export interface Prefs {
  /** 下单前弹确认摘要：市价单、限价单、条件单（平仓本来就要在对话框里确认，一键全平、全撤也一直有确认） */
  confirm: { market: boolean; limit: boolean; cond: boolean }
  /** 数量默认按币还是按 USDT */
  unit: 'coin' | 'usdt'
  /** 键盘快捷键（B / S / Enter / Esc / H） */
  keys: boolean
  /** 自己改过的快捷键（18 第 9.3 条）：没改的用默认 */
  hot: Partial<Record<HotId, string>>
  /** 涨跌颜色（3.26） */
  colorMode: 'green-up' | 'red-up'
  /** 时区：local = 跟随这台电脑 */
  tz: string
  /** 数字格式：en-US 1,234.56；de-DE 1.234,56；fr-FR 1 234,56 */
  numFmt: string
}

// ---------- 快捷键（18 第 9.3 条） ----------
// 能改的是单个键的那几个。Enter 下单、Esc 关闭、Shift + Esc 全撤、Ctrl + K 搜索、Ctrl + Shift + H 不让改：
// 这些是安全相关或者到处都在用的（D75：Esc 只用来关闭和取消，不会误撤单）。
export type HotId = 'buy' | 'sell' | 'iv1' | 'iv5' | 'max' | 'privacy'
export const HOT_DEFAULT: Record<HotId, string> = { buy: 'b', sell: 's', iv1: '1', iv5: '5', max: 'f', privacy: 'h' }
export const HOT_IDS = Object.keys(HOT_DEFAULT) as HotId[]
/** 能当快捷键的：一个字母、数字或符号（不含空格） */
export const hotOk = (k: string) => k.length === 1 && /\S/.test(k)
function cleanHot(v: unknown): Partial<Record<HotId, string>> {
  const out: Partial<Record<HotId, string>> = {}
  if (!v || typeof v !== 'object') return out
  const used = new Set<string>()
  for (const id of HOT_IDS) {
    const k = (v as Record<string, unknown>)[id]
    if (typeof k === 'string' && hotOk(k) && !used.has(k.toLowerCase())) { out[id] = k.toLowerCase(); used.add(out[id]!) }
  }
  return out
}
/** 这个动作现在是哪个键（小写） */
export function hotKey(id: HotId): string { return cur.hot[id] ?? HOT_DEFAULT[id] }
/** 显示用：字母大写 */
export const hotLabel = (k: string) => k.toUpperCase()
/** 换一个键：和别的动作冲突时返回那个动作，不改 */
export function setHot(id: HotId, k: string): HotId | null {
  k = k.toLowerCase()
  const clash = HOT_IDS.find((x) => x !== id && hotKey(x) === k)
  if (clash) return clash
  setPrefs((p) => ({ ...p, hot: k === HOT_DEFAULT[id] ? Object.fromEntries(Object.entries(p.hot).filter(([x]) => x !== id)) : { ...p.hot, [id]: k } }))
  return null
}
export function resetHot() { setPrefs((p) => ({ ...p, hot: {} })) }

// 模拟盘默认不确认（练手要快）；一键全平、一键全撤本来就有确认，不受这里影响
const DEFAULTS: Prefs = { confirm: { market: false, limit: false, cond: false }, unit: 'usdt', keys: true, hot: {}, colorMode: 'green-up', tz: 'local', numFmt: 'en-US' }
const KEY = 'qs.prefs'
const subs = new Set<() => void>()

function read(): Prefs {
  try {
    const raw = JSON.parse(localStorage.getItem(KEY) ?? 'null')
    // 兼容以前单独存的数量单位
    const unit = raw?.unit ?? (localStorage.getItem('qs.unit') === 'coin' ? 'coin' : 'usdt')
    return { ...DEFAULTS, ...raw, unit, confirm: { ...DEFAULTS.confirm, ...raw?.confirm }, hot: cleanHot(raw?.hot) }
  } catch {
    return DEFAULTS
  }
}

let cur = typeof window === 'undefined' ? DEFAULTS : read()

export function getPrefs(): Prefs { return cur }

function applyDisplay() {
  if (typeof document !== 'undefined') document.documentElement.dataset.colorMode = cur.colorMode
}
applyDisplay()

export function setPrefs(fn: (p: Prefs) => Prefs) {
  cur = fn(cur)
  applyDisplay()
  try { localStorage.setItem(KEY, JSON.stringify(cur)); localStorage.setItem('qs.unit', cur.unit) } catch { /* 隐私模式等：只在这次打开期间有效 */ }
  subs.forEach((f) => f())
}

export function usePrefs(): Prefs {
  return useSyncExternalStore((f) => { subs.add(f); return () => subs.delete(f) }, getPrefs, getPrefs)
}

if (typeof window !== 'undefined') {
  // 另一个标签页改了偏好：这里也跟着变
  window.addEventListener('storage', (e) => { if (e.key === KEY) { cur = read(); applyDisplay(); subs.forEach((f) => f()) } })
}
