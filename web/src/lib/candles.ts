// K 线数据（从 pages/Market.tsx 搬出来：交易页主图、多图同屏的小图、首页都用）。
import { useCallback, useEffect, useRef, useState } from 'react'
import type { Candle } from './demo'

/**
 * K 线：先取最新 300 根；之后每秒（1 秒线）或每 2 秒只取最新的几根合并进来；
 * 往左拖快到头时 more() 再取更早的 300 根（后端内存里没有的从交易所补）。
 */
export function useCandles(instrument: string | undefined, iv: string): { bars: Candle[]; more: (firstT: number) => void } {
  const [bars, setBars] = useState<Candle[]>([])
  const ref = useRef<Candle[]>([])
  const hist = useRef({ loading: false, done: false, asked: 0 })
  const key = `${instrument}|${iv}`
  const keyRef = useRef(key)
  keyRef.current = key
  type Raw = { t: number; o: number; h: number; l: number; c: number; vb: number; vs: number }
  const conv = (b: Raw): Candle => ({ t: b.t, o: b.o, h: b.h, l: b.l, c: b.c, vb: b.vb, vs: b.vs })
  useEffect(() => {
    ref.current = []
    setBars([])
    hist.current = { loading: false, done: false, asked: 0 }
    if (!instrument) return
    let alive = true
    let lastTick = Date.now()
    let reloadAt = 0
    const tick = async () => {
      if (document.hidden) return
      // 电脑睡醒或者切回这个标签页：中间可能缺了一段。先取一次，等后端从交易所补完窟窿（最多 10 秒）再取一次
      const now = Date.now()
      const away = now - lastTick > 20_000
      lastTick = now
      if (away) reloadAt = now + 15_000
      const full = away || (reloadAt > 0 && now >= reloadAt)
      if (full && !away) reloadAt = 0
      const last = ref.current[ref.current.length - 1]
      const q = last && !full ? `&since=${last.t}` : '&limit=300'
      try {
        const r = await fetch(`/api/candles?instrument=${instrument}&interval=${iv}${q}`, { cache: 'no-store' })
        if (!r.ok || !alive) return
        const got: Raw[] = await r.json()
        if (!got.length) return
        const cur = ref.current.filter((b) => b.t < got[0].t)
        // 重新取时，往左拖加载过的更早历史留着
        ref.current = cur.concat(got.map(conv))
        setBars(ref.current)
      } catch { /* 下次再试 */ }
    }
    tick()
    const id = window.setInterval(tick, iv === '1s' ? 1000 : 2000)
    return () => { alive = false; window.clearInterval(id) }
  }, [instrument, iv])
  const more = useCallback((firstT: number) => {
    const h = hist.current
    if (!instrument || h.loading || h.done || h.asked === firstT) return
    h.loading = true
    h.asked = firstT
    const k = key
    fetch(`/api/candles?instrument=${instrument}&interval=${iv}&before=${firstT}&limit=300`, { cache: 'no-store' })
      .then((r) => (r.ok ? r.json() : []))
      .then((got: Raw[]) => {
        if (keyRef.current !== k) return
        const older = got.filter((b) => b.t < (ref.current[0]?.t ?? Infinity)).map(conv)
        if (!older.length) { h.done = true; return }
        ref.current = older.concat(ref.current)
        setBars(ref.current)
      })
      .catch(() => { h.asked = 0 })
      .finally(() => { h.loading = false })
  }, [instrument, iv, key])
  return { bars, more }
}
