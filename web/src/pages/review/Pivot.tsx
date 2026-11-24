// 复盘"自由透视"（18 第 10.5 条，D102）：用摩根大通开源的 Perspective（Apache-2.0，现在归 FINOS）把每笔平仓的交易
// 按品种、方向、来源、预案、星期、开仓小时……随意拖着分组，看哪里赚、哪里亏。程序包有几 MB，只在打开这一页时才加载。
// 布局记在这台设备的浏览器里。只摆数字，不做买卖判断。
import { useEffect, useRef, useState } from 'react'
import perspective from '@finos/perspective'
import perspective_viewer from '@finos/perspective-viewer'
import '@finos/perspective-viewer-datagrid'
import SERVER_WASM from '@finos/perspective/dist/wasm/perspective-server.wasm?url'
import CLIENT_WASM from '@finos/perspective-viewer/dist/wasm/perspective-viewer.wasm?url'
import '@finos/perspective-viewer/dist/css/pro-dark.css'
import { useT, useLang, tr, type Key } from '../../lib/i18n'
import type { JTrade } from '../../lib/journal'
import { baseOf } from '../../lib/format'
import '../../styles/pivot.css'

let ready: Promise<void> | null = null
const init = () => (ready ??= Promise.all([perspective.init_server(fetch(SERVER_WASM)), perspective_viewer.init_client(fetch(CLIENT_WASM))]).then(() => undefined))

type Preset = 'coin' | 'when' | 'playbook' | 'side'
const SAVE = 'qs.pivot.v1'

/** 列名跟着界面语言 */
const col = (k: string) => tr(`pv.c.${k}` as Key)

function rowsOf(trades: JTrade[], names: Record<string, string>) {
  const wd = [0, 1, 2, 3, 4, 5, 6].map((i) => tr(`pv.wd.${i}` as Key))
  const p2 = (n: number) => String(n).padStart(2, '0')
  return trades.map((x) => {
    const d = new Date(x.open_time)
    const c = new Date(x.close_time)
    return {
      [col('coin')]: baseOf(x.instrument),
      [col('side')]: tr(x.side === 'long' ? 'pv.long' : 'pv.short'),
      [col('origin')]: tr(`rv.o.${x.origin === 'mixed' ? 'all' : x.origin}` as Key),
      [col('playbook')]: x.playbook ? names[String(x.playbook)] ?? `#${x.playbook}` : tr('pv.noPlaybook'),
      [col('weekday')]: wd[d.getDay()],
      [col('hour')]: `${p2(d.getHours())}:00`,
      [col('day')]: `${c.getFullYear()}-${p2(c.getMonth() + 1)}-${p2(c.getDate())}`,
      [col('month')]: `${c.getFullYear()}-${p2(c.getMonth() + 1)}`,
      [col('result')]: tr(x.net > 0 ? 'pv.win' : x.net < 0 ? 'pv.loss' : 'pv.flat'),
      [col('count')]: 1,
      [col('net')]: Math.round(x.net * 100) / 100,
      [col('fees')]: Math.round(x.fees * 100) / 100,
      [col('funding')]: Math.round(x.funding * 100) / 100,
      [col('r')]: x.r ?? null,
      [col('hold')]: Math.round(x.hold_ms / 60_000),
    }
  })
}

/** 列的类型写死：只有一笔、或者都没填 R 时，Perspective 猜不出类型会直接报错（11-14 踩过） */
function schemaOf() {
  const s: Record<string, 'string' | 'integer' | 'float'> = {}
  for (const k of ['coin', 'side', 'origin', 'playbook', 'weekday', 'hour', 'day', 'month', 'result']) s[col(k)] = 'string'
  s[col('count')] = 'integer'
  s[col('hold')] = 'integer'
  for (const k of ['net', 'fees', 'funding', 'r']) s[col(k)] = 'float'
  return s
}

function presetCfg(p: Preset) {
  const values = [col('count'), col('net'), col('fees'), col('r')]
  const aggregates = { [col('count')]: 'sum', [col('net')]: 'sum', [col('fees')]: 'sum', [col('r')]: 'avg', [col('hold')]: 'avg', [col('funding')]: 'sum' }
  // 只有净盈亏、R 按正负上色；笔数、手续费、持仓分钟是正数也不是"赚了"，不上色
  const plain = { number_fg_mode: 'disabled' }
  const columns_config = { [col('count')]: plain, [col('fees')]: plain, [col('hold')]: plain }
  const base = { title: tr(`pv.p.${p}` as Key), plugin: 'Datagrid', columns: values, aggregates, columns_config, sort: [[col('net'), 'desc']] as [string, string][], split_by: [] as string[], filter: [] as unknown[] }
  switch (p) {
    case 'when': return { ...base, group_by: [col('weekday')], split_by: [col('result')], columns: [col('count'), col('net')], sort: [] }
    case 'playbook': return { ...base, group_by: [col('playbook')] }
    case 'side': return { ...base, group_by: [col('side'), col('coin')] }
    default: return { ...base, group_by: [col('coin')] }
  }
}

export default function Pivot({ trades, names }: { trades: JTrade[]; names: Record<string, string> }) {
  const t = useT()
  const lang = useLang()
  const box = useRef<HTMLDivElement>(null)
  const viewer = useRef<HTMLElement & { load: (t: unknown) => Promise<void>; restore: (c: unknown) => Promise<void>; save: () => Promise<unknown>; toggleConfig: (force?: boolean) => Promise<unknown>; delete?: () => Promise<void> } | null>(null)
  const [state, setState] = useState<'loading' | 'ready' | 'error'>('loading')
  const [panel, setPanel] = useState(false)
  const [preset, setPreset] = useState<Preset | ''>('')
  // 数据变了才重建（父组件每次重画都给一个新的 {}、新的数组，按内容比，不按对象比）
  const dataKey = trades.map((x) => x.id).join(',') + '|' + JSON.stringify(names)
  const latest = useRef({ trades, names })
  latest.current = { trades, names }

  useEffect(() => {
    let off = false
    let table: { delete: () => Promise<void> } | null = null
    const el = document.createElement('perspective-viewer') as unknown as NonNullable<typeof viewer.current>
    el.setAttribute('theme', 'Pro Dark')
    el.className = 'pv-viewer'
    box.current?.appendChild(el)
    viewer.current = el
    const onPanel = (e: Event) => setPanel(!!(e as CustomEvent<boolean>).detail)
    el.addEventListener('perspective-toggle-settings', onPanel)
    const onChange = () => { void el.save().then((cfg) => { try { localStorage.setItem(SAVE + lang, JSON.stringify(cfg)) } catch { /* 忽略 */ } }) }
    ;(async () => {
      try {
        await init()
        const worker = await perspective.worker()
        const tb = await worker.table(schemaOf() as unknown as Record<string, unknown[]>) // 类型声明里没写"列类型表"这种写法，实际支持
        await tb.update(rowsOf(latest.current.trades, latest.current.names))
        table = tb as unknown as { delete: () => Promise<void> }
        if (off) return
        await el.load(tb)
        let saved: unknown = null
        try { saved = JSON.parse(localStorage.getItem(SAVE + lang) ?? 'null') } catch { /* 忽略 */ }
        await el.restore(saved ?? presetCfg('coin'))
        el.addEventListener('perspective-config-update', onChange)
        if (!off) setState('ready')
      } catch (e) {
        console.error('自由透视加载失败', e)
        if (!off) setState('error')
      }
    })()
    return () => {
      off = true
      el.removeEventListener('perspective-config-update', onChange)
      el.removeEventListener('perspective-toggle-settings', onPanel)
      void el.delete?.().catch(() => {})
      el.remove()
      void table?.delete().catch(() => {})
    }
  }, [dataKey, lang])

  const apply = (p: Preset) => { setPreset(p); void viewer.current?.restore(presetCfg(p)) }

  return (
    <div className="pv" data-pivot>
      <div className="pv-bar">
        <span className="pv-lead">{t('pv.lead', { n: trades.length })}</span>
        <span className="pv-presets">
          {(['coin', 'when', 'playbook', 'side'] as Preset[]).map((p) => (
            <button key={p} type="button" className={`btn ghost sm ${preset === p ? 'on' : ''}`} data-pv-preset={p} onClick={() => apply(p)} disabled={state !== 'ready'}>{t(`pv.p.${p}` as Key)}</button>
          ))}
          <button type="button" className={`btn ghost sm pv-custom ${panel ? 'on' : ''}`} data-pv-custom onClick={() => void viewer.current?.toggleConfig()} disabled={state !== 'ready'} aria-pressed={panel}>{t('pv.custom')}</button>
        </span>
      </div>
      {trades.length === 0 && <p className="rv-hint">{t('pv.empty')}</p>}
      {state === 'loading' && trades.length > 0 && <p className="pv-wait t3">{t('pv.loading')}</p>}
      {state === 'error' && <p className="pv-wait t3">{t('pv.error')}</p>}
      <div className="pv-box" ref={box} hidden={trades.length === 0} />
      <p className="pv-note t3">{t('pv.note')}</p>
    </div>
  )
}
