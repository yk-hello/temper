// 列表行的进出场（D68 连续）：新出现的行从上面滑进来、高亮一下再淡掉；消失的行（撤单、平仓、成交）
// 先淡出再移除，而不是一下子不见了。第一次渲染不做动画（打开页面时不该满屏乱动）。
import { useEffect, useRef, useState } from 'react'

export type RowState = '' | 'enter' | 'exit'

export function useEnterExit<T>(items: T[], key: (x: T) => string, ms = 240): { item: T; key: string; state: RowState }[] {
  const seen = useRef<Map<string, number> | null>(null) // key → 第一次出现的时间
  const ghosts = useRef(new Map<string, { item: T; at: number; idx: number }>()) // 刚消失的行
  const prev = useRef<{ item: T; key: string }[]>([])
  const [, tick] = useState(0)
  const now = performance.now()

  const cur = items.map((item) => ({ item, key: key(item) }))
  const curKeys = new Set(cur.map((x) => x.key))
  const first = seen.current === null
  if (first) seen.current = new Map(cur.map((x) => [x.key, -Infinity]))
  const s = seen.current!
  for (const x of cur) if (!s.has(x.key)) s.set(x.key, now)
  // 上一次有、这一次没有的：变成"幽灵行"，留 ms 毫秒做退场
  prev.current.forEach((x, idx) => {
    if (!curKeys.has(x.key) && !ghosts.current.has(x.key)) ghosts.current.set(x.key, { item: x.item, at: now, idx })
  })
  for (const [k, g] of ghosts.current) if (curKeys.has(k) || now - g.at > ms) ghosts.current.delete(k)
  prev.current = cur

  useEffect(() => {
    if (!ghosts.current.size) return
    const id = window.setTimeout(() => tick((n) => n + 1), ms + 20)
    return () => window.clearTimeout(id)
  })

  const out: { item: T; key: string; state: RowState }[] = cur.map((x) => ({ ...x, state: now - (s.get(x.key) ?? -Infinity) < 600 ? 'enter' as const : '' as const }))
  for (const [k, g] of ghosts.current) out.splice(Math.min(g.idx, out.length), 0, { item: g.item, key: k, state: 'exit' })
  return out
}
