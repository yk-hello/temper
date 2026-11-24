// 滑块：拖动 1:1 跟手（D68），方形滑块、发丝线轨道、刻度，和全站的刻度语言一样。键盘 ←→ 也能调。
import { useRef } from 'react'

interface Props {
  value: number
  min: number
  max: number
  step: number
  onChange: (v: number) => void
  label: string
  /** 刻度比例（0–1），画在轨道下方 */
  ticks?: number[]
  disabled?: boolean
}

export function Slider({ value, min, max, step, onChange, label, ticks = [0, 0.25, 0.5, 0.75, 1], disabled }: Props) {
  const box = useRef<HTMLDivElement>(null)
  const span = Math.max(max - min, 1e-9)
  const frac = Math.min(1, Math.max(0, (value - min) / span))
  const snap = (v: number) => {
    const s = Math.round((v - min) / step) * step + min
    const dec = (String(step).split('.')[1] ?? '').length
    return Number(Math.min(max, Math.max(min, s)).toFixed(dec))
  }
  const fromX = (x: number) => {
    const r = box.current!.getBoundingClientRect()
    return snap(min + ((x - r.left) / r.width) * span)
  }
  return (
    <div className={`sld ${disabled ? 'off' : ''}`} ref={box} role="slider" tabIndex={disabled ? -1 : 0} aria-label={label} aria-valuemin={min} aria-valuemax={max} aria-valuenow={value}
      onPointerDown={(e) => {
        if (disabled) return
        e.currentTarget.setPointerCapture(e.pointerId)
        e.currentTarget.focus()
        onChange(fromX(e.clientX))
      }}
      onPointerMove={(e) => { if (!disabled && e.currentTarget.hasPointerCapture(e.pointerId)) onChange(fromX(e.clientX)) }}
      onKeyDown={(e) => {
        if (disabled) return
        const big = e.shiftKey ? 10 : 1
        if (e.key === 'ArrowRight' || e.key === 'ArrowUp') { e.preventDefault(); onChange(snap(value + step * big)) }
        if (e.key === 'ArrowLeft' || e.key === 'ArrowDown') { e.preventDefault(); onChange(snap(value - step * big)) }
        if (e.key === 'Home') { e.preventDefault(); onChange(min) }
        if (e.key === 'End') { e.preventDefault(); onChange(max) }
      }}>
      <i className="sld-track" />
      <i className="sld-fill" style={{ width: `${frac * 100}%` }} />
      {ticks.map((k) => <i key={k} className="sld-tick" style={{ left: `${k * 100}%` }} />)}
      <i className="sld-thumb" style={{ left: `${frac * 100}%` }} />
    </div>
  )
}
