// 未登录的第一屏：全屏 3D 地形（D85 第 20–22 条）。山是过去两小时真实的挂单金额。
// 第一次打开有开场：自检、等高线长成山、价格线点亮。点击或按键跳过。设备带不动就换介绍页。
import { useEffect, useRef, useState } from 'react'
import { Terrain, Z_SPAN } from '../charts/terrain'
import { buildScene, clampPitch, clampYaw, heroView, HS, wantFog as fogFor } from '../charts/heroScene'
import { fmtNum } from '../charts/scale'
import { cellUSD, decodeB64, type HeatFrame, type HeatHistory } from '../lib/heat'
import { forcedTier, quickTier, rememberTier, tierOf, type Tier } from '../lib/device'
import { fmtCompactL, useT } from '../lib/i18n'
import { go } from '../lib/route'
import { priceDecimals } from '../lib/format'
import { Bar } from '../ui/Bar'
import { HomeSwitch } from '../ui/HomeSwitch'
import About from './About'
import '../styles/hero.css'

const SEEN = 'qs.heroSeen'
const easeOut = (x: number) => 1 - Math.pow(1 - Math.max(0, Math.min(1, x)), 3)
const easeIO = (x: number) => { const k = Math.max(0, Math.min(1, x)); return k * k * (3 - 2 * k) }


export default function Hero() {
  const [tier, setTier] = useState<Tier | 'test'>(() => quickTier() ?? 'test')
  if (tier === 'low') return <About />
  return <Scene tier={tier} onTier={setTier} />
}

function Scene({ tier, onTier }: { tier: Tier | 'test'; onTier: (t: Tier) => void }) {
  const t = useT()
  const gl = useRef<HTMLCanvasElement>(null)
  const priceEl = useRef<HTMLElement>(null)
  const readEl = useRef<HTMLDivElement>(null)
  const [ui, setUi] = useState(() => { try { return sessionStorage.getItem(SEEN) === '1' } catch { return false } })
  const [leaving, setLeaving] = useState(false)
  const enterRef = useRef<() => void>(() => {})
  const tierRef = useRef(tier)
  tierRef.current = tier

  useEffect(() => {
    const canvas = gl.current
    if (!canvas) return
    let tr: Terrain
    try { tr = new Terrain(canvas) } catch { onTier('low'); return }
    let raf = 0, live = true
    let w = 0, h = 0, W = 0, H = 0
    const seen = ui
    const ro = new ResizeObserver((es) => {
      const e = es[0]
      w = e.contentRect.width; h = e.contentRect.height
      const d = e.devicePixelContentBoxSize?.[0]
      const cap = tierRef.current === 'mid' ? Math.min(1, devicePixelRatio) : Math.min(1.5, devicePixelRatio)
      const k = d ? d.inlineSize / Math.max(1, w) : devicePixelRatio
      const s = Math.min(k, cap)
      W = Math.round(w * s); H = Math.round(h * s)
      canvas.width = W; canvas.height = H
    })
    ro.observe(canvas)

    // ---------- 数据 ----------
    let cols = 0, rows = 0
    let heights: Float32Array | null = null, shown: Float32Array | null = null, from: Float32Array | null = null, blendAt = 0
    let cells: Uint8Array | null = null
    let meta: { t0: number; dt: number; p0: number; step: number; shift?: number[] } | null = null
    let ready = false
    let readyAt = 0
    const low = () => tierRef.current === 'mid'
    const load = async () => {
      try {
        const r = await fetch(`/api/heat/hero?instrument=BTC-PERP${low() ? '&q=lo' : ''}`)
        if (!r.ok) throw new Error(String(r.status))
        const d0 = (await r.json()) as HeatHistory
        if (!live || !d0.cells || d0.rows < 2 || d0.cols < 2) return
        const sc = buildScene(d0, decodeB64(d0.cells))
        const d = sc.d, c = sc.c, hs = sc.heights, price = sc.price
        const same = cols === d.cols && rows === d.rows
        tr.setData({ cols: d.cols, rows: d.rows, heights: same && shown ? shown : hs, line: sc.line, dots: sc.dots })
        // 新快照慢慢过渡过去：挂单墙真的在长高、塌下去
        if (same && shown) { from = shown.slice(); blendAt = performance.now() } else { from = null; shown = hs.slice() }
        heights = hs; cells = c; cols = d.cols; rows = d.rows
        meta = { t0: d.t0, dt: d.dt, p0: d.p0, step: d.step, shift: d.shift }
        if (!mid && price > 0) mid = price
        if (!ready) { ready = true; readyAt = performance.now() }
      } catch { /* 取不到就先不画，20 秒后再试 */ }
    }

    // ---------- 实时：价格和大单 ----------
    let mid = 0, shownPrice = 0
    const rip = new Float32Array(24)
    const ripT: number[] = [0, 0, 0, 0, 0, 0]
    let ripI = 0
    const es = new EventSource('/api/heat/stream?instrument=BTC-PERP')
    es.onmessage = (m) => {
      let f: HeatFrame
      try { f = JSON.parse(m.data) as HeatFrame } catch { return }
      if (f.m > 0) mid = f.m
      if (f.tr && meta && rows) for (const x of f.tr) {
        const usd = x.p * x.q
        if (usd < 40000) continue
        const r = (x.p - meta.p0) / meta.step
        if (r < 0 || r > rows - 1) continue
        const i = ripI++ % 6
        rip[i * 4] = 0.97; rip[i * 4 + 1] = Z_SPAN * (1 - (2 * r) / (rows - 1)); rip[i * 4 + 2] = 0
        rip[i * 4 + 3] = Math.min(1.6, 0.5 + Math.log10(usd / 40000) * 0.9)
        ripT[i] = performance.now()
      }
    }


    // ---------- 设备实测：先空转画一会儿假地形，黑屏里量帧率 ----------
    let testing = tierRef.current === 'test'
    if (testing) {
      canvas.style.opacity = '0' // 测的时候要真画，但不能让人看见这张假地形
      const n = 320, m = 170
      const hh = new Float32Array(n * m)
      for (let j = 0; j < m; j++) for (let i = 0; i < n; i++) hh[j * n + i] = (Math.sin(i * .13) * Math.cos(j * .17) * .5 + .5) * .7
      tr.setData({ cols: n, rows: m, heights: hh, line: new Float32Array(0), dots: new Float32Array(0) })
    }
    const testStart = performance.now()
    let tFrames = 0, tSum = 0, tLast = 0
    void load()
    const reload = window.setInterval(load, 20000)

    // ---------- 主循环 ----------
    const cam = { yaw: 0.3, pitch: 0.6, drag: null as null | { x: number; y: number; yaw: number; pitch: number }, idleAt: 0, phase: 0 }
    let last = performance.now()
    let doneUi = seen
    let flatAt = 0
    let slow = 0, avg = 16
    let fogEnd = 0
    let lastFrame = performance.now()
    let mouse: { x: number; y: number } | null = null
    let skipped = seen

    const skip = () => { skipped = true }
    enterRef.current = () => { if (!flatAt) { flatAt = performance.now(); setLeaving(true) } }
    const onKey = (e: KeyboardEvent) => {
      if (!doneUi) { skip(); return }
      if (e.key === 'Enter') enterRef.current()
    }
    const onDown = () => { if (!doneUi) skip() }
    window.addEventListener('keydown', onKey)
    window.addEventListener('pointerdown', onDown)

    const frame = (now: number) => {
      raf = requestAnimationFrame(frame)
      if (!w || !h) return
      const dt = Math.min(.1, (now - last) / 1000)
      last = now
      // 实测阶段
      if (testing) {
        tr.setCamera(w / h, .4, .4, 2.2)
        tr.draw(W, H, 1, 2.2, { fog: [0, .0001], points: true })
        if (tLast && now - testStart > 90) { tSum += now - tLast; tFrames++ }
        tLast = now
        if (now - testStart > 480 && tFrames >= 6) {
          const a = tSum / tFrames
          const nt = tierOf(a)
          testing = false
          canvas.style.opacity = ''
          if (document.hasFocus() || nt === 'high') rememberTier(nt)
          onTier(nt)
          if (nt === 'low') return
          if (ready && heights) { void load() }
        }
        return
      }
      if (!ready || !heights || !shown) return
      // 开场：等高线长成山 → 价格线点亮 → 界面出现
      const u = skipped ? 99 : (now - readyAt) / 1000
      const lvl = u < .5 ? 0.001 : easeIO((u - .5) / 1.3)
      const lineK = u < 1.7 ? 0 : easeOut((u - 1.7) / .9)
      if (!doneUi && u > 2.5) {
        doneUi = true
        try { sessionStorage.setItem(SEEN, '1') } catch { /* 无痕模式 */ }
        setUi(true)
      }
      // 新快照过渡
      if (from && heights) {
        const k = easeIO((now - blendAt) / 1600)
        for (let i = 0; i < shown.length; i++) shown[i] = from[i] + (heights[i] - from[i]) * k
        tr.setHeights(shown)
        if (k >= 1) from = null
      }
      // 价格数字：开场滚到实时价，之后跟手
      if (mid) {
        shownPrice = shownPrice ? shownPrice + (mid - shownPrice) * (u < 2.6 ? 1 - Math.exp(-dt * 5) : 1 - Math.exp(-dt * 22)) : mid * .985
        if (priceEl.current) priceEl.current.textContent = fmtNum(shownPrice, priceDecimals(mid))
      }
      // 涟漪老化
      for (let i = 0; i < 6; i++) rip[i * 4 + 2] = ripT[i] ? (now - ripT[i]) / 1000 : 9
      // 镜头：低角度沿山脊慢慢走（算法见 heroView）
      if (!cam.drag && now - cam.idleAt > 3000) cam.phase += dt * 0.09
      const asp = w / h
      const v = heroView(cam, asp)
      let { yaw, pitch, dist } = v
      const { tgt, tanV, tanH } = v
      let hs = HS
      // 开场时镜头从近处拉开一点
      const push = 1 - easeOut(u / 2.8)
      dist *= 1 - push * .1
      // 进入：镜头升到正上方，山压平
      let fade = 0
      if (flatAt) {
        const k = easeIO((now - flatAt) / 750)
        pitch += (1.5 - pitch) * k
        yaw *= 1 - k
        hs *= 1 - k
        tgt[0] *= 1 - k; tgt[2] *= 1 - k
        // 正上方的高度刚好让地形铺满屏幕，压平时也看不到边
        dist += (Math.min(1 / tanH, Z_SPAN / tanV) * .98 - dist) * k
        fade = k
        if (now - flatAt > 780) { cancelAnimationFrame(raf); live = false; go('market'); return }
      }
      tr.setCamera(asp, yaw, pitch, dist, tgt, 2 * Math.atan(tanV))
      // 雾在边界之前黑掉：屏幕里看到的边界都已经是黑的。慢慢跟，边界进出屏幕时不会闪
      const wantFog = flatAt ? dist + 3 : fogFor(tr, dist)
      fogEnd = fogEnd ? fogEnd + (wantFog - fogEnd) * (1 - Math.exp(-dt * 4)) : wantFog
      const t0 = performance.now()
      tr.draw(W, H, W / w, dist, { hs, lvl, lineK, rip, points: !low() && lvl >= .98, dots: lineK >= 1, fog: [fogEnd * .68, fogEnd], lineCore: 1.3 })
      canvas.style.opacity = fade ? String(1 - fade * .85) : ''
      // 运行中继续看：撑不住就降档
      avg += (performance.now() - t0 + (dt * 1000 - (performance.now() - t0)) - avg) * .03
      // 窗口没焦点时系统会放慢画面，这时的帧率不算数。降档只在这次生效，不记下来：
      // 之前记下来，RTX 4060 被误判成低配，之后一直进不了地形
      const watching = document.visibilityState === 'visible' && document.hasFocus() && now - lastFrame < 300
      lastFrame = now
      if (watching && avg > 46) slow += dt; else slow = Math.max(0, slow - dt * .5)
      if (slow > 6 && !forcedTier()) { // 用户自己选了 3D，就不再替他降
        slow = 0
        onTier(tierRef.current === 'mid' ? 'low' : 'mid')
      }
      // 鼠标读数：这座山是多少钱
      if (readEl.current) {
        let html = ''
        if (mouse && lvl >= 1 && !flatAt && cells && meta && heights) {
          const hit = tr.pick((mouse.x / w) * 2 - 1, 1 - (mouse.y / h) * 2, shown, cols, rows, hs)
          if (hit) {
            const usd = cellUSD(cells[hit.r * cols + hit.c])
            const p = meta.p0 + hit.r * meta.step + (meta.shift?.[hit.c] ?? 0) // 拼接过的列加回平移，是真实价格
            if (usd > 0) {
              const side = p < mid ? t('ab.bid') : t('ab.ask')
              html = `${side} ${fmtNum(p, priceDecimals(p))} · ${fmtCompactL(usd)}`
            }
          }
        }
        if (readEl.current.textContent !== html) readEl.current.textContent = html
        if (mouse) readEl.current.style.transform = `translate(${Math.min(w - 220, mouse.x + 14)}px, ${mouse.y - 12}px)`
      }
    }
    raf = requestAnimationFrame(frame)

    const move = (e: PointerEvent) => {
      const r = canvas.getBoundingClientRect()
      mouse = { x: e.clientX - r.left, y: e.clientY - r.top }
      if (cam.drag) {
        cam.yaw = clampYaw(cam.drag.yaw - (e.clientX - cam.drag.x) * .004)
        cam.pitch = clampPitch(cam.drag.pitch + (e.clientY - cam.drag.y) * .003)
        cam.idleAt = performance.now()
      }
    }
    const down = (e: PointerEvent) => { cam.drag = { x: e.clientX, y: e.clientY, yaw: cam.yaw, pitch: cam.pitch } }
    const up = () => { cam.drag = null; cam.idleAt = performance.now() }
    const lost = () => { mouse = null }
    canvas.addEventListener('pointermove', move)
    canvas.addEventListener('pointerdown', down)
    window.addEventListener('pointerup', up)
    canvas.addEventListener('pointerleave', lost)
    canvas.addEventListener('webglcontextlost', () => { onTier('low') }) // 睡眠唤醒后常见，不代表设备差，不记下来

    return () => {
      live = false
      cancelAnimationFrame(raf)
      window.clearInterval(reload)
      es.close()
      ro.disconnect()
      window.removeEventListener('keydown', onKey)
      window.removeEventListener('pointerdown', onDown)
      window.removeEventListener('pointerup', up)
      tr.dispose()
    }
    // 档位变了不重启：只影响精度
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <div className={leaving ? 'hero leaving' : 'hero'} data-hero data-tier={tier}>
      <canvas ref={gl} className="hero-gl" />
      <div ref={readEl} className="hero-read" />
      <div className={ui ? 'hero-bar on' : 'hero-bar'}><Bar page="landing" /></div>
      <div className={ui ? 'hero-copy on' : 'hero-copy'}>
        <div className="hero-price"><span>BTC</span><b ref={priceEl} data-hero-price>—</b></div>
        <p className="hero-line">{t('hero.line')}</p>
        <div className="hero-acts">
          <button type="button" className="btn primary" data-hero-enter onClick={() => enterRef.current()}>{t('hero.enter')}</button>
          <HomeSwitch current="3d" />
        </div>
        <p className="hero-risk">{t('ab.risk')}</p>
      </div>
    </div>
  )
}

