import { useEffect, useRef, useState } from 'react'
import type { Point, Snapshot } from './types'

export type Conn = 'connecting' | 'open' | 'closed'

const HISTORY_MS = 10 * 60 * 1000 // 每个品种在浏览器里只保留最近 10 分钟（环形，内存不涨）

/** 订阅后端行情推送。断线自动重连（1s → 最多 15s）。 */
export interface BookFrame { type: 'book'; instrument: string; venue: string; ts: number; bids: [string, string][]; asks: [string, string][] }

/** 订阅后端行情推送。断线自动重连（1s → 最多 15s）。bookInstrument 不为空时同时订阅该品种的盘口。 */
export function useMarket(bookInstrument?: string) {
  const [snap, setSnap] = useState<Snapshot | null>(null)
  const [book, setBook] = useState<BookFrame | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const bookRef = useRef(bookInstrument)
  bookRef.current = bookInstrument
  const [conn, setConn] = useState<Conn>('connecting')
  const history = useRef(new Map<string, Point[]>())
  const [version, setVersion] = useState(0)

  useEffect(() => {
    let ws: WebSocket | null = null
    let stopped = false
    let wait = 1000
    let timer: number | undefined

    const connect = () => {
      setConn('connecting')
      const proto = location.protocol === 'https:' ? 'wss' : 'ws'
      ws = new WebSocket(`${proto}://${location.host}/ws`)
      wsRef.current = ws
      ws.onopen = () => {
        wait = 1000
        setConn('open')
        if (bookRef.current) ws?.send(JSON.stringify({ book: bookRef.current }))
      }
      ws.onmessage = (e) => {
        let s: Snapshot
        try {
          s = JSON.parse(e.data)
        } catch {
          return
        }
        if ((s as unknown as BookFrame).type === 'book') { setBook(s as unknown as BookFrame); return }
        if (s.type !== 'snapshot') return
        const now = Date.now()
        for (const inst of s.instruments) {
          const v = Number(inst.last)
          if (!Number.isFinite(v) || v <= 0) continue
          let arr = history.current.get(inst.instrument)
          if (!arr) history.current.set(inst.instrument, (arr = []))
          const prev = arr[arr.length - 1]
          if (!prev || prev.v !== v || now - prev.t > 1000) arr.push({ t: now, v })
          while (arr.length && now - arr[0].t > HISTORY_MS) arr.shift()
        }
        setSnap(s)
        setVersion((x) => x + 1)
      }
      ws.onclose = () => {
        setConn('closed')
        if (stopped) return
        timer = window.setTimeout(connect, wait)
        wait = Math.min(wait * 2, 15000)
      }
      ws.onerror = () => ws?.close()
    }
    connect()
    return () => {
      stopped = true
      window.clearTimeout(timer)
      ws?.close()
    }
  }, [])

  useEffect(() => {
    setBook(null)
    const ws = wsRef.current
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ book: bookInstrument ?? '' }))
  }, [bookInstrument])

  // 阶梯线一打开就有最近几分钟（2026-11-15 走查时发现：原来只从打开页面那一刻开始攒，刚打开只有几秒）：
  // 选中品种时取最近 300 根 1 秒 K 线的收盘价，补在已有的点前面，之后接着用实时推送
  useEffect(() => {
    const inst = bookInstrument
    if (!inst) return
    let alive = true
    fetch(`/api/candles?instrument=${encodeURIComponent(inst)}&interval=1s&limit=300`, { cache: 'no-store' })
      .then((r) => (r.ok ? r.json() : null))
      .then((cs: { t: number; c: number }[] | null) => {
        if (!alive || !Array.isArray(cs) || !cs.length) return
        const arr = history.current.get(inst) ?? []
        const first = arr.length ? arr[0].t : Infinity
        const now = Date.now()
        const old = cs.filter((c) => c.t + 1000 < first && now - c.t <= HISTORY_MS && Number.isFinite(c.c) && c.c > 0).map((c) => ({ t: c.t + 999, v: c.c }))
        if (!old.length) return
        history.current.set(inst, [...old, ...arr])
        setVersion((x) => x + 1)
      })
      .catch(() => { /* 补不上就从现在开始攒 */ })
    return () => { alive = false }
  }, [bookInstrument])

  return { snap, conn, history: history.current, version, book }
}

/** 每秒跳一次的"现在"，用于倒计时和"几秒前"。 */
export function useNow(intervalMs = 1000) {
  const [now, setNow] = useState(Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), intervalMs)
    return () => window.clearInterval(id)
  }, [intervalMs])
  return now
}
