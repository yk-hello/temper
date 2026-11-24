// K 线图标题栏上的"指标"和"画线"（12 的 V.1、V.2）。弹层从按钮长出来，点别处或按 Esc 关。
import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useT, type Key } from '../../lib/i18n'
import { growFrom, lastAnchor } from '../../lib/anchor'
import { DEFAULT_IND, setIndCfg, useIndCfg, type IndCfg, type Shape, type ToolKind } from '../../lib/chartPrefs'

function Pop({ label, active, children, testId }: { label: string; active?: boolean; children: ReactNode; testId: string }) {
  const [open, setOpen] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const pop = useRef<HTMLDivElement>(null)
  const [place, setPlace] = useState<CSSProperties>({})
  useLayoutEffect(() => {
    if (!open || !pop.current || !box.current) return
    // 挂在 body 上用固定定位：图表面板放不下时也不会被裁掉。窗口矮时分两栏
    const r = box.current.getBoundingClientRect()
    const vw = window.innerWidth, vh = window.innerHeight
    const top = Math.min(r.bottom + 4, vh - 120)
    const full = pop.current.scrollHeight
    const two = full > vh - top - 8
    const w = two ? 452 : 232
    setPlace({ top, left: Math.max(8, Math.min(r.right - w, vw - w - 8)), width: Math.min(w, vw - 16), maxHeight: vh - top - 8, columnCount: two ? 2 : undefined })
    growFrom(pop.current, lastAnchor(), 160)
  }, [open])
  useEffect(() => {
    if (!open) return
    const away = (e: PointerEvent) => { if (!box.current?.contains(e.target as Node) && !pop.current?.contains(e.target as Node)) setOpen(false) }
    const key = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    document.addEventListener('pointerdown', away)
    window.addEventListener('keydown', key)
    return () => { document.removeEventListener('pointerdown', away); window.removeEventListener('keydown', key) }
  }, [open])
  return (
    <div className="ct" ref={box}>
      <button type="button" className={`ct-btn ${active ? 'on' : ''}`} data-ct={testId} aria-expanded={open} onClick={() => setOpen(!open)}>{label}</button>
      {open && createPortal(<div className="ct-pop" ref={pop} style={place} onClick={(e) => { if ((e.target as HTMLElement).closest('[data-close]')) setOpen(false) }}>{children}</div>, document.body)}
    </div>
  )
}

function Num({ value, min, max, onChange, step = 1, label }: { value: number; min: number; max: number; onChange: (v: number) => void; step?: number; label: string }) {
  const [text, setText] = useState(String(value))
  useEffect(() => { setText(String(value)) }, [value])
  return (
    <input className="ct-num num" inputMode="decimal" aria-label={label} value={text}
      onChange={(e) => {
        setText(e.target.value)
        const v = Number(e.target.value)
        if (Number.isFinite(v) && v >= min && v <= max && (step !== 1 || Number.isInteger(v))) onChange(v)
      }}
      onBlur={() => setText(String(value))} />
  )
}

/** 一组勾选项的弹层（频谱的叠加层用） */
export function CheckPop({ label, testId, active, items }: { label: string; testId: string; active?: boolean; items: { key: string; label: string; on: boolean; set: (on: boolean) => void }[] }) {
  return (
    <Pop label={label} active={active} testId={testId}>
      {items.map((x) => (
        <div className="ct-row" key={x.key}>
          <label className="op-check"><input type="checkbox" checked={x.on} onChange={(e) => x.set(e.target.checked)} /><span>{x.label}</span></label>
        </div>
      ))}
    </Pop>
  )
}

/** 单选的弹层（频谱的金额下限用） */
export function RadioPop<T extends string | number>({ label, testId, active, value, onChange, options, hint }: { label: string; testId: string; active?: boolean; value: T; onChange: (v: T) => void; options: { value: T; label: string }[]; hint?: string }) {
  return (
    <Pop label={label} active={active} testId={testId}>
      {options.map((o) => (
        <button type="button" key={String(o.value)} className={`ct-item ${o.value === value ? 'on' : ''}`} data-close="1" onClick={() => onChange(o.value)}>{o.label}</button>
      ))}
      {hint && <p className="ct-hint">{hint}</p>}
    </Pop>
  )
}

type OnKey = { [K in keyof IndCfg]: K }[keyof IndCfg]

export function IndicatorMenu() {
  const t = useT()
  const c = useIndCfg()
  const on = (k: OnKey, v: boolean) => setIndCfg((o) => ({ ...o, [k]: { ...o[k], on: v } }))
  const set = <K extends OnKey>(k: K, patch: Partial<IndCfg[K]>) => setIndCfg((o) => ({ ...o, [k]: { ...o[k], ...patch } }))
  const row = (k: OnKey, label: string, extra?: ReactNode) => (
    <div className="ct-row" key={k}>
      <label className="op-check"><input type="checkbox" checked={c[k].on} onChange={(e) => on(k, e.target.checked)} /><span>{label}</span></label>
      {extra && <span className="ct-args">{extra}</span>}
    </div>
  )
  const count = (Object.keys(c) as OnKey[]).filter((k) => k !== 'vol' && k !== 'events' && c[k].on).length
  return (
    <Pop label={count ? `${t('ct.ind')} ${count}` : t('ct.ind')} active={count > 0} testId="ind">
      <h4>{t('ct.main')}</h4>
      {row('ma1', 'MA', <Num label="MA" value={c.ma1.n} min={2} max={400} onChange={(n) => set('ma1', { n })} />)}
      {row('ma2', 'MA', <Num label="MA" value={c.ma2.n} min={2} max={400} onChange={(n) => set('ma2', { n })} />)}
      {row('ema1', 'EMA', <Num label="EMA" value={c.ema1.n} min={2} max={400} onChange={(n) => set('ema1', { n })} />)}
      {row('ema2', 'EMA', <Num label="EMA" value={c.ema2.n} min={2} max={400} onChange={(n) => set('ema2', { n })} />)}
      {row('vwap', 'VWAP')}
      {row('boll', t('ct.boll'), <><Num label={t('ct.boll')} value={c.boll.n} min={2} max={400} onChange={(n) => set('boll', { n })} /><Num label="k" value={c.boll.k} min={0.5} max={5} step={0.1} onChange={(k) => set('boll', { k })} /></>)}
      {row('vol', t('ct.vol'))}
      <h4>{t('ct.sub')}</h4>
      {row('rsi', 'RSI', <Num label="RSI" value={c.rsi.n} min={2} max={100} onChange={(n) => set('rsi', { n })} />)}
      {row('macd', 'MACD', <>
        <Num label="fast" value={c.macd.f} min={2} max={100} onChange={(f) => set('macd', { f })} />
        <Num label="slow" value={c.macd.s} min={2} max={200} onChange={(s) => set('macd', { s })} />
        <Num label="signal" value={c.macd.g} min={2} max={100} onChange={(g) => set('macd', { g })} />
      </>)}
      {row('cvd', t('ct.cvd'))}
      {row('delta', t('ct.delta'))}
      {row('oi', t('ct.oi'))}
      <h4>{t('ct.other')}</h4>
      {row('events', t('ct.events'))}
      <button type="button" className="ct-link" onClick={() => setIndCfg(() => DEFAULT_IND)}>{t('ct.reset')}</button>
    </Pop>
  )
}

const TOOLS: { k: ToolKind; label: Key }[] = [
  { k: 'hline', label: 'ct.t.hline' }, { k: 'trend', label: 'ct.t.trend' }, { k: 'rect', label: 'ct.t.rect' }, { k: 'fib', label: 'ct.t.fib' }, { k: 'measure', label: 'ct.t.measure' },
]

export function DrawMenu({ tool, onTool, shapes, onClear }: { tool: ToolKind | null; onTool: (k: ToolKind | null) => void; shapes: Shape[]; onClear: () => void }) {
  const t = useT()
  const cur = TOOLS.find((x) => x.k === tool)
  return (
    <Pop label={cur ? t(cur.label) : t('ct.draw')} active={!!tool} testId="draw">
      {TOOLS.map((x) => (
        <button type="button" key={x.k} className={`ct-item ${tool === x.k ? 'on' : ''}`} data-close data-tool={x.k} onClick={() => onTool(tool === x.k ? null : x.k)}>{t(x.label)}</button>
      ))}
      <p className="ct-hint">{t('ct.drawHint')}</p>
      {shapes.length > 0 && <button type="button" className="ct-link" data-close onClick={onClear}>{t('ct.clear', { n: shapes.length })}</button>}
    </Pop>
  )
}
