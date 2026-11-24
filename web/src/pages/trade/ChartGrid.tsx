// 多图同屏（18 第 9.2 条）：交易页的 K 线区可以分成 2 格或 4 格。第一格是主图（下单、画线、指标、自己的交易线都在它上面），
// 其余几格各看一个品种、一个周期，只看行情；点小图的品种名把它换成主图。只在宽屏、K 线模式下有。
// 记在浏览器里（只是个人习惯）：合约、现货分开记。
import { useEffect, useMemo, useState } from 'react'
import { Scope } from '../../charts/Scope'
import { useCandles } from '../../lib/candles'
import { baseOf, fmtPrice, priceDecimals } from '../../lib/format'
import { useT, type Key } from '../../lib/i18n'
import type { InstrumentState } from '../../lib/types'
import { Select } from '../../ui/Select'
import { RadioPop } from './ChartTools'
import { locDec } from '../../lib/tz'

export type GridN = 1 | 2 | 4
export interface Cell { i: string; iv: string }
const IVS = [
  { value: '1m', ms: 60_000 }, { value: '5m', ms: 300_000 }, { value: '15m', ms: 900_000 },
  { value: '1h', ms: 3_600_000 }, { value: '4h', ms: 14_400_000 }, { value: '1d', ms: 86_400_000 },
]
const key = (spot: boolean) => `qs.grid.${spot ? 'spot' : 'perp'}`

function load(spot: boolean): { n: GridN; cells: Cell[] } {
  try {
    const v = JSON.parse(localStorage.getItem(key(spot)) ?? 'null')
    const n: GridN = v?.n === 2 || v?.n === 4 ? v.n : 1
    const cells = Array.isArray(v?.cells) ? v.cells.filter((c: Cell) => typeof c?.i === 'string' && IVS.some((x) => x.value === c?.iv)).slice(0, 3) : []
    return { n, cells }
  } catch { return { n: 1, cells: [] } }
}

/** 几格、每格看什么。品种列表变了（还没加载、合约和现货切换）时，不在列表里的格子换成别的品种 */
export function useGrid(spot: boolean, main: string | undefined, instruments: InstrumentState[]) {
  const [st, setSt] = useState(() => load(spot))
  useEffect(() => { setSt(load(spot)) }, [spot])
  const save = (v: { n: GridN; cells: Cell[] }) => {
    setSt(v)
    try { localStorage.setItem(key(spot), JSON.stringify(v)) } catch { /* 隐私模式：只在这次有效 */ }
  }
  const cells = useMemo(() => {
    const have = new Set(instruments.map((x) => x.instrument))
    const pool = instruments.map((x) => x.instrument).filter((i) => i !== main)
    const out: Cell[] = []
    for (let k = 0; k < 3; k++) {
      const c = st.cells[k]
      if (c && (have.has(c.i) || !instruments.length)) { out.push(c); continue }
      const used = new Set([main, ...out.map((x) => x.i), ...st.cells.map((x) => x?.i)])
      const pick = pool.find((i) => !used.has(i)) ?? pool[k] ?? main ?? ''
      out.push({ i: pick, iv: c?.iv ?? '15m' })
    }
    return out
  }, [st, instruments, main])
  return {
    n: st.n,
    cells,
    setN: (n: GridN) => save({ n, cells }),
    // 选了另一张小图正在看的品种：两张对调，不出现两张一样的
    setCell: (k: number, c: Cell) => save({ n: st.n, cells: cells.map((x, j) => (j === k ? c : x.i === c.i && cells[k].i !== c.i ? { ...x, i: cells[k].i } : x)) }),
  }
}

/** 标题栏上的"多图"：和"指标""画线"一样的弹层，选一张、两张、四张 */
export function GridPick({ n, onN }: { n: GridN; onN: (n: GridN) => void }) {
  const t = useT()
  return (
    <RadioPop label={t('mg.label')} testId="grid" active={n > 1} value={n} onChange={onN} hint={t('mg.hint')}
      options={([1, 2, 4] as GridN[]).map((v) => ({ value: v, label: t(`mg.n${v}` as Key) }))} />
  )
}

/** 分格时主图上面也有一行，和小图对齐：品种、现价、涨跌，标明是主图 */
export function MainHead({ st }: { st: InstrumentState | undefined }) {
  const t = useT()
  const last = Number(st?.last) || 0
  const chg = Number(st?.change_24h_pct) || 0
  return (
    <div className="mg-head mg-head-main">
      <span className="mg-sym mg-sym-main">{st ? baseOf(st.instrument) : '—'}</span>
      <span className="num mg-px">{last > 0 ? fmtPrice(String(last)) : '—'}</span>
      <span className={`num mg-chg ${chg > 0 ? 'up' : chg < 0 ? 'down' : 't3'}`}>{st?.change_24h_pct ? `${chg > 0 ? '+' : chg < 0 ? '−' : ''}${locDec(Math.abs(chg).toFixed(2))}%` : ''}</span>
      <span className="mg-tag">{t('mg.main')}</span>
    </div>
  )
}

/** 一格小图：品种、周期、现价和涨跌，下面是 K 线（拖动、缩放和主图一样） */
export function MiniChart({ idx, cell, instruments, version, onCell, onMain }: {
  idx: number; cell: Cell; instruments: InstrumentState[]; version: number
  onCell: (c: Cell) => void; onMain: (inst: string) => void
}) {
  const t = useT()
  const { bars, more } = useCandles(cell.i || undefined, cell.iv)
  const st = instruments.find((x) => x.instrument === cell.i)
  const last = Number(st?.last) || bars[bars.length - 1]?.c || 0
  const dec = last > 0 ? priceDecimals(last) : 2
  const chg = Number(st?.change_24h_pct) || 0
  const ms = IVS.find((x) => x.value === cell.iv)?.ms ?? 900_000
  const instOpts = useMemo(() => {
    const o = instruments.map((x) => ({ value: x.instrument, label: baseOf(x.instrument) }))
    return cell.i && !o.some((x) => x.value === cell.i) ? [{ value: cell.i, label: baseOf(cell.i) }, ...o] : o // 品种列表还没到
  }, [instruments, cell.i])
  const ivOpts = useMemo(() => IVS.map((x) => ({ value: x.value, label: t(`mk.iv.${x.value}` as Key) })), [t])
  return (
    <div className="mg-cell" data-cell={idx}>
      <div className="mg-head">
        <button type="button" className="mg-sym" onClick={() => onMain(cell.i)} title={t('mg.toMain')}>{cell.i ? baseOf(cell.i) : '—'}</button>
        <span className="num mg-px">{last > 0 ? fmtPrice(String(last)) : '—'}</span>
        <span className={`num mg-chg ${chg > 0 ? 'up' : chg < 0 ? 'down' : 't3'}`}>{st ? `${chg > 0 ? '+' : chg < 0 ? '−' : ''}${locDec(Math.abs(chg).toFixed(2))}%` : ''}</span>
        <span className="mg-sel">
          <Select label={t('mg.inst')} value={cell.i} options={instOpts} onChange={(i) => onCell({ ...cell, i })} className="mg-inst" />
          <Select label={t('mk.interval')} value={cell.iv} options={ivOpts} onChange={(iv) => onCell({ ...cell, iv })} className="mg-iv" />
        </span>
      </div>
      <div className="mg-body">
        {cell.i && <Scope mode="candle" candles={bars} interval={ms} span={5 * 60_000} dec={dec} live version={version} pan={{ onNeedMore: more }} />}
      </div>
    </div>
  )
}
