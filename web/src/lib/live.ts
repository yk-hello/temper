// 实盘（D89）：用户接自己的币安 U 本位合约账户。钱在用户自己的交易所账户里，这里不经手（D90）。
import { useEffect, useSyncExternalStore } from 'react'
import { api } from './session'

export type LiveEnv = 'main' | 'testnet' | 'fake'
export interface LiveKey { id: number; exchange: string; env: LiveEnv; label: string; hint: string; perms: string; created: number; checked: number }
export interface LiveStatus { stream: boolean; why?: string; error?: string; at: number; env: LiveEnv }
export interface LiveAcct { id: number; key_id: number; name: string; armed: boolean; confirm: boolean; created: number; status: LiveStatus; env: LiveEnv }
export interface LiveOverview { enabled: boolean; envs: LiveEnv[]; keys: LiveKey[]; accounts: LiveAcct[] }
export interface LivePerms { futures: boolean; withdraw: boolean; transfer: boolean; universal_transfer: boolean; ip_restrict: boolean; dual_side: boolean; wallet: string }

/** 实盘账户的编号从 1 万亿开始（和后端 trading.LiveBase 一样） */
export const LIVE_BASE = 1_000_000_000_000
export const isLiveId = (id: number | null | undefined) => !!id && id >= LIVE_BASE

let cache: LiveOverview | null = null
let loading: Promise<void> | null = null
let users = 0
const subs = new Set<() => void>()

export function refreshLive(): Promise<void> {
  loading = api<LiveOverview>('GET', '/api/live').then((r) => { cache = r; subs.forEach((f) => f()) }).catch(() => { /* 没登录或服务器没开实盘：当作没有 */ }).finally(() => { loading = null })
  return loading
}

/** 这个人接的密钥和实盘账户。on = false 时不取（没登录） */
export function useLive(on = true): LiveOverview | null {
  const v = useSyncExternalStore((f) => { subs.add(f); return () => subs.delete(f) }, () => cache)
  useEffect(() => {
    if (!on) return
    users++
    if (!cache && !loading) void refreshLive()
    // 连接状态（推送是否连着）每 20 秒更新一次；有页面在用才取
    const id = window.setInterval(() => { if (users > 0) void refreshLive() }, 20000)
    return () => { users--; window.clearInterval(id) }
  }, [on])
  return on ? v : null
}

export function clearLive() { cache = null; subs.forEach((f) => f()) }
