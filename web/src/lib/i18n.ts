// 界面语言（D49）。所有文字通过 t('键') 取；切换立即生效。
// 记忆：没登录时记在浏览器里；登录后以账户里保存的语言为准，切换时同步到服务器。
import { limitParams } from './limits'
import { locDec, numLocale, wall } from './tz'
import { useSyncExternalStore } from 'react'
import { zh, type Key } from '../i18n/zh'
import { en } from '../i18n/en'

export type Lang = 'zh' | 'en'
export type { Key }
const KEY = 'qs.lang'
const dicts: Record<Lang, Record<Key, string>> = { zh, en }
const subs = new Set<() => void>()

function initial(): Lang {
  try {
    const v = localStorage.getItem(KEY)
    if (v === 'zh' || v === 'en') return v
  } catch { /* 无痕模式 */ }
  return /^zh\b/i.test(navigator.language || '') ? 'zh' : 'en'
}

let lang: Lang = initial()
document.documentElement.lang = lang === 'zh' ? 'zh-CN' : 'en'

/** 切换语言。onChange 由账户模块挂上，用来同步到服务器。 */
export const langHooks: { onChange?: (l: Lang) => void } = {}

export function setLang(l: Lang, sync = true) {
  if (l === lang) return
  lang = l
  document.documentElement.lang = l === 'zh' ? 'zh-CN' : 'en'
  try { localStorage.setItem(KEY, l) } catch { /* 忽略 */ }
  if (sync) langHooks.onChange?.(l)
  subs.forEach((f) => f())
}

export function getLang(): Lang { return lang }
/** 中文句子里夹的英文词（DCA 这类）两边补一个空格；英文界面原样（11-17 走查："启动DCA"） */
export function latinWord(s: string): string { return lang === 'zh' && /^[A-Za-z]/.test(s) ? ` ${s} ` : s }

/** 拼接文字时用的标点，跟着语言走。英文界面不能出现中文标点。 */
export const listSep = () => (lang === 'zh' ? '、' : ', ')
export const colon = () => (lang === 'zh' ? '：' : ': ')
export const stop = () => (lang === 'zh' ? '。' : '. ')

/** 不在组件里时用（例如提示条）。 */
export function tr(key: Key, vars?: Record<string, string | number>): string {
  let s = dicts[lang][key] ?? zh[key] ?? key
  // 文案里写死的小数（"0.75R""0.05%"）也跟数字格式走；前面是字母、点、横线的不算（v1.2、Apache-2.0）。参数在这之后才填
  if (/\d\.\d/.test(s)) s = s.replace(/(^|[^\w.-])(\d+\.\d+)(?![\d.])/g, (_, a: string, n: string) => a + locDec(n))
  if (vars) for (const k in vars) { const v = vars[k]; s = s.split(`{${k}}`).join(typeof v === 'number' ? locDec(String(v)) : String(v)) }
  return s
}

export function useLang(): Lang {
  return useSyncExternalStore((f) => { subs.add(f); return () => subs.delete(f) }, () => lang)
}

/** 组件里用：const t = useT()；语言变化时组件自动重绘。 */
export function useT() {
  useLang()
  return tr
}

/** 服务器错误码 → 说明怎么改的提示 */
export function errText(code: string, params?: Record<string, string>): string {
  const k = `err.${code}` as Key
  if (!(k in zh)) return tr('err.INTERNAL_ERROR')
  if (code === 'DISCIPLINE_LOCKED') {
    if (!params?.rule) return tr('disc.lockedShort')
    const at = Number(params.unlock_at || params.until || 0)
    return tr(k, { rule: tr(`disc.rule.${params.rule}` as Key), when: at ? tr('disc.until', { t: fmtTime(at) }) : tr('disc.noExpiry') })
  }
  // 服务器给的数字参数（价格、金额）按数字格式排，和界面其他地方一样：68348.9 → 68,348.9 / 68.348,9；小数位不动
  // 上限类的提示（"最多 N 个"）：数字取这台服务器现在用的上限（部署的人可以改，D103）
  const fmt: Record<string, string> = {}
  for (const [n, v] of Object.entries({ ...limitParams(code), ...params })) {
    const d = /^-?\d+(\.\d+)?$/.test(v) ? Math.min(20, (v.split('.')[1] ?? '').length) : -1
    fmt[n] = d >= 0 ? Number(v).toLocaleString(numLocale(), { minimumFractionDigits: d, maximumFractionDigits: d }) : v
  }
  return tr(k, fmt)
}

/** 委托撤销、过期的原因 */
export function reasonText(code?: string): string {
  if (!code) return ''
  const k = `reason.${code}` as Key
  return k in zh ? tr(k) : code
}

/** 本地化数字（紧凑写法：1.2万 / 12K） */
// 金额缩写全站用 K、M、B、T，中文界面也一样，不写万、亿（D109，用户 11-18："都用 M、K 等国际单位"）
const compactFmt = new Intl.NumberFormat('en-US', { notation: 'compact', maximumSignificantDigits: 3 })
export function fmtCompactL(v: number): string {
  return locDec(compactFmt.format(v))
}

/** 时间统一写成 2026-11-05 00:14:36（币安的写法），中英文一样，不会把 11 月 5 日看成 5 月 11 日。
 *  year：带年份（账户页）；seconds：带秒（委托、成交列表）。用本机时区。 */
export function fmtTime(t: number | string | Date, opts: { year?: boolean; seconds?: boolean } = {}): string {
  const d = wall(t)
  const p = (n: number) => String(n).padStart(2, '0')
  const date = `${opts.year ? d.y + '-' : ''}${p(d.mo)}-${p(d.d)}`
  return `${date} ${p(d.h)}:${p(d.mi)}${opts.seconds ? ':' + p(d.s) : ''}`
}

/** 默认账户名是后端写的中文（模拟账户 1），界面按当前语言显示；用户自己改的名字不动 */
export function acctName(name: string): string {
  const m = /^模拟账户 (\d+)$/.exec(name)
  if (m) return tr('paper.defaultName', { n: m[1] })
  // 跟随自动建的账户按建的时候的语言起名（后端 follow.PaperInitialName）；换了语言也跟着换（11-17 走查）
  if (name === '策略跟随' || name === 'Strategy follow') return tr('paper.followName')
  return name
}
