// 上一次取到的数据（2026-11-15，用户："今日等了半天才有显示……尽可能看不出延迟"）。
// 进页面、切回来、刷新浏览器时先显示上一次的，后台取到新的再换，不再从"加载中"开始。
//   - 公开的行情数据（今日、筛选、方块图、策略战绩、链上、新闻、日历）还存进浏览器，刷新页面、重开浏览器也马上有；
//   - 其他（账户、风险这些个人数据）只记在这个页面里，换账号或退出时清掉，不写进浏览器。
// 这里不要引用 session、i18n：它们会用到这里，互相引用会转圈。

type Entry = { at: number; data: unknown }

const mem = new Map<string, Entry>()
const KEY = 'qs.cache.'
const MAX_AGE = 12 * 3600_000 // 浏览器里存的超过 12 小时就不用了
const MAX_SIZE = 300_000 // 太大的不往浏览器里存（字符数）

/** 公开、和登录的人无关的接口（存进浏览器）。别的只在内存里记。 */
const PUBLIC = /^\/api\/(today|scan|marketmap|calendar|news|venues\/matrix|heat\/status|onchain\/|candles\?|strategies(\/[\w-]+(\/(performance|report|calibration|trades))?)?(\?|$))/

const isPublic = (url: string) => PUBLIC.test(url)

/** 上一次的结果；没有就是 null。 */
export function recall<T>(url: string | null): T | null {
  if (!url) return null
  const m = mem.get(url)
  if (m) return m.data as T
  if (!isPublic(url)) return null
  try {
    const raw = localStorage.getItem(KEY + url)
    if (!raw) return null
    const e = JSON.parse(raw) as Entry
    if (Date.now() - e.at > MAX_AGE) return null
    mem.set(url, e)
    return e.data as T
  } catch {
    return null
  }
}

/** 记下这一次的结果。 */
export function remember(url: string, data: unknown) {
  const e = { at: Date.now(), data }
  mem.set(url, e)
  if (!isPublic(url)) return
  try {
    const raw = JSON.stringify(e)
    if (raw.length <= MAX_SIZE) localStorage.setItem(KEY + url, raw)
  } catch { /* 浏览器不让存就算了 */ }
}

/** 换账号、退出时：个人数据的记忆清掉（公开的留着）。 */
export function forgetPrivate() {
  for (const k of [...mem.keys()]) if (!isPublic(k)) mem.delete(k)
}
