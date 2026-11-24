// 刻度尺选择器：把 ProArt 机身上的实体旋钮"展开"成一把带刻度的尺子。
// 用来选时间范围、杠杆、资金比例这类"在一条连续刻度上取值"的设置。
// 可以点、拖、滚轮、方向键操作。
import { useEffect, useRef, useState } from 'react'
import { useSurface } from '../lib/gfx'
import { C } from '../charts/scale'

interface Props<T extends string | number> {
  options: { value: T; label: string }[]
  value: T
  onChange: (v: T) => void
  /** 两个主刻度之间的副刻度数量 */
  minor?: number
  label: string
}

export function Ruler<T extends string | number>({ options, value, onChange, minor = 4, label }: Props<T>) {
  const idx = Math.max(0, options.findIndex((o) => o.value === value))
  const [drag, setDrag] = useState<number | null>(null) // 拖动中指针的位置（0..n-1 的小数）
  const [hover, setHover] = useState<number | null>(null)
  const anim = useRef({ pos: idx, target: idx })
  anim.current.target = drag ?? idx
  const n = options.length
  const PAD = 18

  const { ref, redraw } = useSurface((g) => {
    const a = anim.current
    a.pos += (a.target - a.pos) * 0.35
    if (Math.abs(a.target - a.pos) < 0.002) a.pos = a.target
    else requestAnimationFrame(redraw)
    const x0 = PAD, x1 = g.w - PAD
    const xOf = (i: number) => x0 + (n > 1 ? (i / (n - 1)) * (x1 - x0) : 0)
    const base = g.h - 6
    const t = g.px(1)
    g.hline(base, x0 - 6, x1 + 6, C.rule2)
    // 尺子短的时候（窄屏幕上的图表标题栏）字会挤在一起：隔一个或隔两个写，选中的和鼠标下的总是写，离它们太近的不写
    const wOf = (s: string) => [...s].reduce((w, ch) => w + (ch.charCodeAt(0) > 255 ? 12 : 7), 0)
    const gap = n > 1 ? (x1 - x0) / (n - 1) : Infinity
    const need = Math.max(...options.map((o) => wOf(o.label))) + 6
    const every = gap >= need ? 1 : gap * 2 >= need ? 2 : 3
    const selI = Math.round(a.pos)
    const showLabel = (i: number) => every === 1 || i === selI || i === hover || (i % every === 0 && Math.abs(i - selI) >= every && (hover == null || Math.abs(i - hover) >= every))
    for (let i = 0; i < n; i++) {
      if (i < n - 1) for (let j = 1; j <= minor; j++) {
        const x = xOf(i + j / (minor + 1))
        const near = Math.abs(i + j / (minor + 1) - a.pos) < 0.5
        g.vline(x, base - 4, base, near ? C.rule3 : C.rule2, t)
      }
      const sel = Math.round(a.pos) === i
      const x = xOf(i)
      g.vline(x, base - 8, base, sel ? C.t2 : C.rule3, t)
      if (showLabel(i)) g.text(options[i].label, x, base - 15, {
        size: 12, weight: sel ? 550 : 450, align: 'center',
        color: sel ? C.white : hover === i ? C.t2 : C.t3,
      })
    }
    // 指针：一根白色细线贯穿刻度，底部一个小方块
    const px = xOf(a.pos)
    g.vline(px, base - 10, base + 1, C.white, g.px(1.5))
    const s = g.px(4)
    g.ctx.fillStyle = C.white
    g.ctx.fillRect(Math.round(g.X(px) + g.px(1.5) / 2 - s / 2), g.Y(base) + g.px(1), s, g.px(2))
  }, [idx, drag, hover, n])

  useEffect(() => { redraw() }, [idx, drag, redraw])

  const posOf = (clientX: number, el: HTMLElement) => {
    const r = el.getBoundingClientRect()
    const f = (clientX - r.left - PAD) / (r.width - PAD * 2)
    return Math.max(0, Math.min(n - 1, f * (n - 1)))
  }
  const commit = (p: number) => {
    const i = Math.round(p)
    if (options[i].value !== value) onChange(options[i].value)
  }

  return (
    <div
      className="ruler"
      role="slider"
      tabIndex={0}
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={n - 1}
      aria-valuenow={idx}
      aria-valuetext={options[idx]?.label}
      onPointerDown={(e) => { e.currentTarget.setPointerCapture(e.pointerId); setDrag(posOf(e.clientX, e.currentTarget)) }}
      onPointerMove={(e) => {
        const p = posOf(e.clientX, e.currentTarget)
        if (drag !== null) setDrag(p)
        else setHover(Math.round(p))
        redraw()
      }}
      onPointerUp={(e) => { if (drag !== null) { commit(posOf(e.clientX, e.currentTarget)); setDrag(null) } }}
      onPointerLeave={() => { setHover(null); redraw() }}
      onWheel={(e) => { const i = Math.max(0, Math.min(n - 1, idx + Math.sign(e.deltaY || e.deltaX))); commit(i) }}
      onKeyDown={(e) => {
        if (e.key === 'ArrowLeft' || e.key === 'ArrowDown') { e.preventDefault(); commit(Math.max(0, idx - 1)) }
        if (e.key === 'ArrowRight' || e.key === 'ArrowUp') { e.preventDefault(); commit(Math.min(n - 1, idx + 1)) }
      }}
    >
      <canvas ref={ref} />
    </div>
  )
}
