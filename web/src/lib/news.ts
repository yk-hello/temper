// 新闻标题（18 第 8.6 条）：后端从公开的 RSS（和部署的人自己配的 X 账号）取，这里只显示标题、来源、时间、原文链接。
// 平台不拿新闻做买卖判断（D100）。
import { recall, remember } from './cache'
import { useEffect, useState } from 'react'

export interface NewsItem { id: string; source: string; title: string; url: string; time: number; coins?: string[]; kind: 'rss' | 'x' | 'notice' }
export interface NewsSource { name: string; kind: string; ok: number; error?: string; items: number; polled: number }
export interface News { items: NewsItem[]; sources: NewsSource[]; feeds: number; x: number; notices?: boolean }

let cache: News | null = recall<News>('/api/news?limit=60') // 刷新浏览器也先显示上一次的（lib/cache.ts）
const subs = new Set<(v: News | null) => void>()
let timer = 0

async function load() {
  try {
    const r = await fetch('/api/news?limit=60')
    if (!r.ok) return
    cache = (await r.json()) as News
    remember('/api/news?limit=60', cache)
    subs.forEach((f) => f(cache))
  } catch { /* 下一轮再取 */ }
}

export function useNews(): News | null {
  const [v, setV] = useState(cache)
  useEffect(() => {
    subs.add(setV)
    if (!timer) { void load(); timer = window.setInterval(load, 120_000) }
    setV(cache)
    return () => {
      subs.delete(setV)
      if (!subs.size) { window.clearInterval(timer); timer = 0 }
    }
  }, [])
  return v
}

/** 只放 http/https 的链接（后端已经筛过，这里再挡一次） */
export const safeHref = (u: string) => (/^https?:\/\//i.test(u) ? u : undefined)
