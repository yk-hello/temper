// 画布底座：所有 2D 图表都通过它画。
// 1. 画布缓冲区 = 元素在屏幕上占的物理像素（用 devicePixelContentBoxSize 精确获取），浏览器不会再缩放它
// 2. 绘制坐标直接用物理像素；传入的 CSS 坐标经 X()/Y() 取整，所有细线、方块落在整像素上
// 3. 缩放比例变化（窗口拖到另一块屏幕）时自动重建
import { useEffect, useRef } from 'react'
import { wall } from './tz'

export interface G {
  ctx: CanvasRenderingContext2D
  /** CSS 尺寸 */
  w: number
  h: number
  /** 物理像素尺寸 */
  W: number
  H: number
  /** 每个 CSS 像素对应的物理像素 */
  kx: number
  ky: number
  /** 当前时间（毫秒），一帧内统一 */
  now: number
  /** CSS 坐标 → 物理像素（取整） */
  X(x: number): number
  Y(y: number): number
  /** CSS 长度 → 物理像素，至少 1 */
  px(n: number): number
  /** 填充矩形（CSS 坐标，边缘对齐像素） */
  rect(x0: number, y0: number, x1: number, y1: number, color: string): void
  /** 1 个物理像素的横线 / 竖线（CSS 坐标），t = 粗细（物理像素） */
  hline(y: number, x0: number, x1: number, color: string, t?: number): void
  vline(x: number, y0: number, y1: number, color: string, t?: number): void
  /** 文字（CSS 坐标与字号） */
  text(s: string, x: number, y: number, o: TextOpts): void
  measure(s: string, o: Pick<TextOpts, 'size' | 'weight' | 'font'>): number
}

export interface TextOpts {
  size: number
  color: string
  weight?: number
  font?: 'ui' | 'code'
  align?: CanvasTextAlign
  baseline?: CanvasTextBaseline
  /** 文字底板（读数标签用），CSS px 内边距 */
  plate?: { color: string; padX: number; padY: number; border?: string }
}

export const FONT_UI = '"Geist Variable", "Noto Sans SC", "PingFang SC", "Microsoft YaHei UI", sans-serif'
export const FONT_CODE = '"Geist Mono Variable", ui-monospace, monospace'

export function makeG(ctx: CanvasRenderingContext2D, w: number, h: number, W: number, H: number, now: number): G {
  const kx = W / w, ky = H / h
  const k = (kx + ky) / 2
  const X = (x: number) => Math.round(x * kx)
  const Y = (y: number) => Math.round(y * ky)
  const fontStr = (size: number, weight = 450, font: 'ui' | 'code' = 'ui') =>
    `${weight} ${(size * k).toFixed(2)}px ${font === 'code' ? FONT_CODE : FONT_UI}`
  const g: G = {
    ctx, w, h, W, H, kx, ky, now, X, Y,
    px: (n) => Math.max(1, Math.round(n * k)),
    rect(x0, y0, x1, y1, color) {
      const a = X(x0), b = Y(y0)
      ctx.fillStyle = color
      ctx.fillRect(a, b, Math.max(X(x1) - a, 1), Math.max(Y(y1) - b, 1))
    },
    hline(y, x0, x1, color, t = 1) {
      ctx.fillStyle = color
      const a = X(x0)
      ctx.fillRect(a, Y(y), Math.max(X(x1) - a, 1), t)
    },
    vline(x, y0, y1, color, t = 1) {
      ctx.fillStyle = color
      const b = Y(y0)
      ctx.fillRect(X(x), b, t, Math.max(Y(y1) - b, 1))
    },
    text(s, x, y, o) {
      ctx.font = fontStr(o.size, o.weight, o.font)
      ctx.textAlign = o.align ?? 'left'
      ctx.textBaseline = o.baseline ?? 'alphabetic'
      const tx = X(x), ty = Y(y)
      if (o.plate) {
        const tw = ctx.measureText(s).width
        const padX = Math.round(o.plate.padX * kx), padY = Math.round(o.plate.padY * ky)
        const sz = Math.round(o.size * k)
        let left = tx
        if (ctx.textAlign === 'center') left = Math.round(tx - tw / 2)
        else if (ctx.textAlign === 'right' || ctx.textAlign === 'end') left = Math.round(tx - tw)
        let top = ty - sz
        if (ctx.textBaseline === 'middle') top = Math.round(ty - sz / 2)
        else if (ctx.textBaseline === 'top') top = ty
        const bx = left - padX, by = top - padY, bw = Math.ceil(tw) + padX * 2, bh = sz + padY * 2
        ctx.fillStyle = o.plate.color
        ctx.fillRect(bx, by, bw, bh)
        if (o.plate.border) {
          ctx.fillStyle = o.plate.border
          ctx.fillRect(bx, by, bw, 1); ctx.fillRect(bx, by + bh - 1, bw, 1)
          ctx.fillRect(bx, by, 1, bh); ctx.fillRect(bx + bw - 1, by, 1, bh)
        }
      }
      ctx.fillStyle = o.color
      ctx.fillText(s, tx, ty)
    },
    measure(s, o) {
      ctx.font = fontStr(o.size, o.weight, o.font)
      return ctx.measureText(s).width / k
    },
  }
  return g
}

export interface SurfaceOpts {
  /** 每帧重画（实时图表）；否则只在尺寸或依赖变化时重画 */
  animate?: boolean
  /** 不透明画布（屏幕底色），能让文字抗锯齿更好 */
  opaque?: string
}

/**
 * 画布 hook。draw 每次都拿到一个新的 G（物理像素坐标系，已清空）。
 * 返回 ref（挂到 <canvas> 上）和 redraw（要求下一帧重画，比如鼠标移动后）。canvas 的 CSS 尺寸由外层决定。
 */
export function useSurface(draw: (g: G) => void, deps: unknown[], opts: SurfaceOpts = {}) {
  const ref = useRef<HTMLCanvasElement>(null)
  const drawRef = useRef(draw)
  drawRef.current = draw
  const redraw = useRef<() => void>(() => {})

  useEffect(() => {
    const canvas = ref.current
    if (!canvas) return
    const ctx = canvas.getContext('2d', { alpha: !opts.opaque })
    if (!ctx) return
    let w = 0, h = 0, W = 0, H = 0, raf = 0, dirty = true

    const paint = () => {
      if (!w || !h) return
      if (canvas.width !== W || canvas.height !== H) { canvas.width = W; canvas.height = H }
      ctx.setTransform(1, 0, 0, 1, 0, 0)
      if (opts.opaque) { ctx.fillStyle = opts.opaque; ctx.fillRect(0, 0, W, H) } else ctx.clearRect(0, 0, W, H)
      drawRef.current(makeG(ctx, w, h, W, H, Date.now()))
    }
    redraw.current = () => { dirty = true }

    const ro = new ResizeObserver((entries) => {
      const e = entries[0]
      w = e.contentRect.width; h = e.contentRect.height
      const d = e.devicePixelContentBoxSize?.[0]
      if (d) { W = d.inlineSize; H = d.blockSize } else {
        const r = window.devicePixelRatio || 1
        W = Math.round(w * r); H = Math.round(h * r)
      }
      dirty = true
      paint(); dirty = false
    })
    try { ro.observe(canvas, { box: 'device-pixel-content-box' }) } catch { ro.observe(canvas) }

    const loop = () => {
      raf = requestAnimationFrame(loop)
      if (opts.animate || dirty) { paint(); dirty = false }
    }
    raf = requestAnimationFrame(loop)
    return () => { cancelAnimationFrame(raf); ro.disconnect() }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [opts.animate, opts.opaque])

  useEffect(() => { redraw.current() }, deps) // eslint-disable-line react-hooks/exhaustive-deps
  return { ref, redraw: () => redraw.current() }
}

/** "好看"的刻度步长：1、2、2.5、5 × 10^n */
export function niceStep(raw: number): number {
  if (!(raw > 0) || !Number.isFinite(raw)) return 1
  const p = Math.pow(10, Math.floor(Math.log10(raw)))
  const f = raw / p
  return (f < 1.5 ? 1 : f < 2.25 ? 2 : f < 3.5 ? 2.5 : f < 7.5 ? 5 : 10) * p
}

const TIME_STEPS = [1e3, 2e3, 5e3, 10e3, 15e3, 30e3, 60e3, 120e3, 300e3, 600e3, 900e3, 1800e3, 3600e3, 7200e3, 14400e3, 21600e3, 43200e3, 86400e3]
export function niceTimeStep(spanMs: number, maxTicks: number): number {
  for (const s of TIME_STEPS) if (spanMs / s <= maxTicks) return s
  return 86400e3 * 7
}

export function clock(t: number, withSec = true, withMs = false): string {
  const d = wall(t)
  const p = (n: number, l = 2) => String(n).padStart(l, '0')
  let s = `${p(d.h)}:${p(d.mi)}`
  if (withSec) s += `:${p(d.s)}`
  if (withMs) s += `.${p(d.ms, 3)}`
  return s
}

/** 确定性伪随机（示例数据用，每次打开都一样，截图可比对） */
export function rng(seed: number) {
  let s = seed >>> 0
  return () => {
    s = (s + 0x6d2b79f5) >>> 0
    let t = s
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}
export function gauss(r: () => number): number {
  let u = 0, v = 0
  while (u === 0) u = r()
  while (v === 0) v = r()
  return Math.sqrt(-2 * Math.log(u)) * Math.cos(2 * Math.PI * v)
}
