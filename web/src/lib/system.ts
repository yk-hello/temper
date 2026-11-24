// 全站共用的系统状态：管理员有没有全局停机（D89 第 5 条）。登录后每 20 秒取一次，顶栏亮一个标记。
import { useEffect, useSyncExternalStore } from 'react'
import { api } from './session'

export interface Halt { level: number; reason?: string; at?: number; by?: string }
let cache: Halt | null = null
let users = 0
const subs = new Set<() => void>()

export function refreshSystem(): void {
  api<{ halt: Halt }>('GET', '/api/halt').then((r) => { cache = r.halt; subs.forEach((f) => f()) }).catch(() => { /* 没登录或还在启动 */ })
}

export function useSystem(on: boolean): Halt | null {
  const v = useSyncExternalStore((f) => { subs.add(f); return () => subs.delete(f) }, () => cache)
  useEffect(() => {
    if (!on) return
    users++
    if (!cache) refreshSystem()
    const id = window.setInterval(() => { if (users > 0) refreshSystem() }, 20000)
    return () => { users--; window.clearInterval(id) }
  }, [on])
  return on ? v : null
}
