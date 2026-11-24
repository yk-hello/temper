// 组件展厅：v3 设计系统的全部基础件。每次改设计都在这里先过一遍多屏幕比例检查。
// 打开方式：http://localhost:8080/?page=showroom
import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { demoBook, toCandles } from '../lib/demo'
import { useSurface } from '../lib/gfx'
import { BRAND } from '../lib/brand'
import { onDprChange } from '../lib/pixel'
import { C, timeScale } from '../charts/scale'
import { Scope } from '../charts/Scope'
import { Spectrum, SpectrumLegend } from '../charts/Spectrum'
import { DeltaBars } from '../charts/Bars'
import { Ruler } from '../ui/Ruler'
import { Seg } from '../ui/Seg'
import { Price } from '../ui/Price'
import { Money, PrivacyButton } from '../ui/Money'
import { usePrivate, setPrivate } from '../lib/privacy'
import '../styles/showroom.css'
// 字体对照用（只在组件展厅加载）
import '@fontsource-variable/inter'
import '@fontsource/ibm-plex-sans/400.css'
import '@fontsource/ibm-plex-sans/500.css'
import '@fontsource/ibm-plex-sans/600.css'

const RANGES = [
  { value: '1m', label: '1分' }, { value: '5m', label: '5分' }, { value: '15m', label: '15分' },
  { value: '1h', label: '1时' }, { value: '4h', label: '4时' }, { value: '1d', label: '1日' },
]
const LEVERAGE = [1, 2, 3, 5, 10, 20].map((v) => ({ value: v, label: `${v}×` }))

export default function Showroom() {
  const book = useMemo(() => demoBook(), [])
  const c1m = useMemo(() => toCandles(book.trades, 60_000), [book])
  const c5s = useMemo(() => toCandles(book.trades, 5_000), [book])
  const steps = useMemo(() => {
    const out: { t: number; v: number }[] = []
    for (const t of book.trades) if (!out.length || out[out.length - 1].v !== t.p) out.push({ t: t.t, v: t.p })
    return out
  }, [book])
  const tEnd = book.t0 + book.cols * book.dt

  return (
    <div className="shell">
      <header className="bar">
        <div className="mark">{BRAND}</div>
        <nav className="nav">
          <a href="?">行情</a>
          <a href="?page=showroom" aria-current="page">组件展厅</a>
        </nav>
        <div className="bar-right">
          <DeviceMeter />
          <PrivacyButton />
        </div>
      </header>

      <main className="sr">
        <section className="sr-intro">
          <h1>设计系统 v3</h1>
          <p>
            外壳是哑光石墨，数据画面是纯黑的屏幕。亮度表示多少，颜色只表示方向和警报，白色是唯一的强调色。
            所有坐标轴和控件共用一套刻度，所有线条对齐到物理像素。下面的图表都是示例数据。
          </p>
        </section>

        <Sec title="字体与字号" desc="拉丁字母和数字用 Geist，中文用思源黑体。数字一律等宽对齐，但不用等宽字体。中文最小 12 像素。">
          <FontLab />
          <div className="type-scale">
            <TypeRow px={48} w={380} sample="77,429.5" note="主价格" num />
            <TypeRow px={32} w={400} sample="+1,284.20 USDT" note="大号读数" num />
            <TypeRow px={24} w={420} sample="流动性频谱 Liquidity" note="页面标题" />
            <TypeRow px={18} w={480} sample="比特币永续合约 BTC-PERP" note="区块标题" />
            <TypeRow px={15} w={450} sample="资金费率 0.0100% · 下次结算 03:12:44" note="读数" num />
            <TypeRow px={13} w={420} sample="当价格接近大单时，挂单被撤走，这是典型的虚假挂单。" note="正文" />
            <TypeRow px={12} w={450} sample="24 小时成交额 99.08 亿 USDT" note="标签" />
            <TypeRow px={11} w={450} sample="12:30:05  77,400  77,450  0.0100%" note="坐标刻度（仅数字）" num />
          </div>
        </Sec>

        <Sec title="颜色" desc="九成画面是灰阶。涨跌色只出现在涨跌数字和成交方向上；琥珀色只用于提醒；香槟金只属于高光时刻。">
          <div className="swatches">
            <Sw name="屏幕" v="#000000" c="var(--void)" />
            <Sw name="机身" v="#111113" c="var(--chassis)" />
            <Sw name="控件面" v="#17171A" c="var(--plate)" />
            <Sw name="悬停" v="#1E1E22" c="var(--hover)" />
            <Sw name="文字 1" v="#EDEDEF" c="var(--t1)" />
            <Sw name="文字 2" v="#A3A3AA" c="var(--t2)" />
            <Sw name="文字 3" v="#6E6E76" c="var(--t3)" />
            <Sw name="文字 4" v="#48484E" c="var(--t4)" />
            <Sw name="强调" v="#FFFFFF" c="var(--white)" />
            <Sw name="上涨" v="#4CB98A" c="var(--green)" />
            <Sw name="下跌" v="#E2625C" c="var(--red)" />
            <Sw name="提醒" v="#D4A44C" c="var(--amber)" />
            <Sw name="高光" v="#D4B37A" c="var(--gold)" />
          </div>
        </Sec>

        <Sec title="线与刻度" desc="发丝线永远是 1 个物理像素。下面这块用来在不同缩放比例下检查锐利度：每条线都应该是一样细、一样亮，没有灰边。">
          <div className="display sr-test"><div className="display-body"><PixelTest /></div></div>
        </Sec>

        <Sec title="读数" desc="主价格变化时，只有变动的那几位先变成纯白，再慢慢回到正常亮度。不闪红绿。">
          <Instrument />
        </Sec>

        <Sec title="控件" desc="主按钮是白底黑字，全站同一时刻只出现一个。刻度尺来自 ProArt 机身上的旋钮，可以点、拖、滚轮和方向键。">
          <Controls />
        </Sec>

        <Sec title="表格" desc="数字右对齐成列；选中行左侧一道白线。任何文字都不截断，窄屏时整列隐藏。">
          <TableDemo />
        </Sec>

        <Sec title="价格图" desc="K 线和阶梯线。不再用斜线连接稀疏的成交价。移动鼠标看读数。">
          <div className="sr-charts">
            <SwitchableChart c1m={c1m} c5s={c5s} steps={steps} tEnd={tEnd} />
            <Panel title="BTC-PERP" sub="逐笔成交价，最近 3 分钟">
              <Scope mode="step" points={steps} span={3 * 60_000} t1={tEnd} dec={1} volume={false} />
            </Panel>
            <Panel title="订单流" sub="柱 = 每分钟主动买入减主动卖出 · 线 = 累计（CVD）">
              <DeltaBars candles={c1m} interval={60_000} span={30 * 60_000} t1={tEnd} />
            </Panel>
          </div>
        </Sec>

        <Sec title="流动性频谱" desc="过去半小时每一档价格上挂了多少单。亮的横线是厚挂单（常说的“墙”），它们会挡住价格；突然消失的是撤单，常见于虚假挂单。只有大额成交画成圆点，小额成交汇总在底部的成交量条里。滚轮缩放时间，Shift + 滚轮缩放价格，拖动平移，双击复位。">
          <div className="display sr-spectrum">
            <div className="display-head">
              <span className="display-title">流动性频谱</span>
              <span className="display-sub">BTC-PERP　示例数据</span>
              <div className="display-tools"><SpectrumLegend /></div>
            </div>
            <div className="display-body"><Spectrum book={book} span={15 * 60_000} /></div>
          </div>
        </Sec>

        <Sec title="隐藏金额" desc="公共场合模式：随时按 H 键隐藏所有金额和持仓数量，再按一次恢复；在输入框里用 Ctrl + Shift + H。价格和涨跌幅照常显示。设置会被记住。">
          <MaskDemo />
        </Sec>
      </main>
    </div>
  )
}

function Sec({ title, desc, children }: { title: string; desc: string; children: ReactNode }) {
  return (
    <section className="sr-sec">
      <div className="sr-side">
        <h2>{title}</h2>
        <p>{desc}</p>
      </div>
      <div className="sr-main">{children}</div>
    </section>
  )
}

function Panel({ title, sub, tools, children }: { title: string; sub: string; tools?: ReactNode; children: ReactNode }) {
  return (
    <div className="display sr-chart">
      <div className="display-head">
        <span className="display-title">{title}</span>
        <span className="display-sub">{sub}</span>
        {tools && <div className="display-tools">{tools}</div>}
      </div>
      <div className="display-body">{children}</div>
    </div>
  )
}

function DeviceMeter() {
  const [d, setD] = useState(() => ({ dpr: devicePixelRatio, w: innerWidth, h: innerHeight }))
  useEffect(() => {
    const up = () => setD({ dpr: devicePixelRatio, w: innerWidth, h: innerHeight })
    addEventListener('resize', up)
    const off = onDprChange(up)
    return () => { removeEventListener('resize', up); off() }
  }, [])
  return (
    <>
      <span className="meter">缩放 <b className="num">{+d.dpr.toFixed(3)}×</b></span>
      <span className="meter sr-opt">视口 <b className="num">{d.w} × {d.h}</b></span>
    </>
  )
}

function FontLab() {
  const [cjk, setCjk] = useState(document.documentElement.dataset.cjk ?? 'noto')
  const [latin, setLatin] = useState(document.documentElement.dataset.latin ?? 'geist')
  useEffect(() => {
    const r = document.documentElement
    if (cjk === 'noto') delete r.dataset.cjk; else r.dataset.cjk = cjk
    if (latin === 'geist') delete r.dataset.latin; else r.dataset.latin = latin
  }, [cjk, latin])
  return (
    <div className="fontlab">
      <span className="t3">字体对照（只用于在不同屏幕上比较渲染效果）</span>
      <Seg label="中文字体" value={cjk} onChange={setCjk} options={[{ value: 'noto', label: '思源黑体' }, { value: 'yahei', label: '微软雅黑' }]} />
      <Seg label="西文字体" value={latin} onChange={setLatin} options={[{ value: 'geist', label: 'Geist' }, { value: 'inter', label: 'Inter' }, { value: 'plex', label: 'IBM Plex' }]} />
    </div>
  )
}

function TypeRow({ px, w, sample, note, num }: { px: number; w: number; sample: string; note: string; num?: boolean }) {
  return (
    <div className="type-row">
      <span className="type-meta num">{px} / {w}</span>
      <span className="type-sample" style={{ fontSize: px, fontWeight: w, lineHeight: 1.3, letterSpacing: px >= 32 ? '-0.02em' : px >= 18 ? '-0.005em' : 0, fontVariantNumeric: num ? 'tabular-nums' : undefined }}>{sample}</span>
      <span className="type-note">{note}</span>
    </div>
  )
}

function Sw({ name, v, c }: { name: string; v: string; c: string }) {
  return (
    <div className="sw">
      <i style={{ background: c }} />
      <span>{name}</span>
      <span className="code t3">{v}</span>
    </div>
  )
}

/** 锐利度测试：1 物理像素的线、棋盘格、刻度尺、画布文字 */
function PixelTest() {
  const { ref } = useSurface((g) => {
    g.rect(0, 0, g.w, g.h, C.void)
    // 左：一组 1 物理像素的横线和竖线，间隔 1 物理像素（像素级条纹，发虚会立刻变成一片灰）
    const ctx = g.ctx
    ctx.fillStyle = '#fff'
    const bx = g.X(16), by = g.Y(16)
    for (let i = 0; i < 24; i++) ctx.fillRect(bx + i * 2, by, 1, g.px(72))
    for (let i = 0; i < 24; i++) ctx.fillRect(bx + 60 * g.px(1) + 0, by + i * 2, g.px(48), 1)
    g.text('1 物理像素条纹', 16, 112, { size: 12, color: C.t3 })
    // 中：刻度尺
    const P = { x0: 150, y0: 20, x1: Math.max(260, g.w - 230), y1: 76 }
    timeScale(g, P, Date.UTC(2026, 10, 3, 4, 0, 0), Date.UTC(2026, 10, 3, 4, 30, 0), { grid: true })
    g.hline(P.y0, P.x0, P.x1, C.rule)
    g.text('时间刻度：主刻度带数字，副刻度 5 等分', P.x0, 112, { size: 12, color: C.t3 })
    // 右：画布文字（和 DOM 文字对比）
    const rx = g.w - 210
    if (rx > P.x1 + 10) {
      g.text('77,429.5', rx, 34, { size: 11, color: C.t2 })
      g.text('77,429.5', rx + 80, 34, { size: 13, color: C.t1, weight: 500 })
      g.text('挂单 32.0 BTC', rx, 58, { size: 12, color: C.t1, weight: 500, plate: { color: C.plate, padX: 6, padY: 4, border: C.rule2 } })
      g.text('77,431.0', rx, 86, { size: 11, weight: 550, color: '#000', baseline: 'middle', plate: { color: C.white, padX: 6, padY: 4 } })
      g.text('画布文字', rx, 112, { size: 12, color: C.t3 })
    }
  }, [])
  return <div className="surface"><canvas ref={ref} /></div>
}

function Instrument() {
  const [p, setP] = useState(77429.5)
  useEffect(() => {
    const id = setInterval(() => setP((x) => Math.round((x + (Math.random() - 0.5) * 3) * 2) / 2), 1400)
    return () => clearInterval(id)
  }, [])
  const txt = p.toLocaleString('en-US', { minimumFractionDigits: 1, maximumFractionDigits: 1 })
  return (
    <div className="inst">
      <div className="inst-id">
        <div><span className="sym">BTC-PERP</span><span className="t3">比特币永续 · 币安</span></div>
        <div className="inst-price">
          <Price text={txt} className="price-xl" />
          <span className="num up" style={{ fontSize: 15 }}>+1.24%</span>
        </div>
      </div>
      <div className="inst-ro">
        <Ro l="标记价格" v="77,431.2" />
        <Ro l="资金费率" v="0.0100%" s="03:12:44 后结算" />
        <Ro l="24 小时最高" v="78,102.0" />
        <Ro l="24 小时最低" v="76,055.5" />
        <Ro l="24 小时成交额" v="99.08 亿" s="USDT" />
        <Ro l="持仓量" v="8.91 万" s="BTC" />
        <Ro l="买卖价差" v="0.13" s="bp" />
      </div>
    </div>
  )
}

function Ro({ l, v, s, cls }: { l: string; v: ReactNode; s?: string; cls?: string }) {
  return (
    <div className="ro">
      <span className="ro-l">{l}</span>
      <span className={`ro-v ${cls ?? ''}`}>{v}{s && <small>{s}</small>}</span>
    </div>
  )
}

function Controls() {
  const [range, setRange] = useState('15m')
  const [lev, setLev] = useState(3)
  const [mode, setMode] = useState('candle')
  const [on, setOn] = useState(true)
  const [amt, setAmt] = useState('2,000')
  return (
    <div className="ctl-grid">
      <div className="ctl-row">
        <button className="btn primary">分配资金</button>
        <button className="btn secondary">模拟运行</button>
        <button className="btn ghost">查看明细</button>
        <button className="btn danger">全部平仓</button>
        <button className="btn secondary" disabled>已暂停</button>
      </div>
      <div className="ctl-row">
        <Seg label="图表类型" value={mode} onChange={setMode} options={[{ value: 'candle', label: 'K 线' }, { value: 'step', label: '阶梯线' }, { value: 'spectrum', label: '频谱' }]} />
        <label className="switch">
          <input type="checkbox" checked={on} onChange={(e) => setOn(e.target.checked)} />
          <span className="track"><span className="knob" /></span>
          红涨绿跌
        </label>
        <label className="field" style={{ width: 180 }}>
          <span>分配金额</span>
          <span className="input"><input value={amt} onChange={(e) => setAmt(e.target.value)} inputMode="decimal" /><em>USDT</em></span>
        </label>
      </div>
      <div className="ctl-rulers">
        <div className="field"><span>时间范围 <b className="num t1">{RANGES.find((r) => r.value === range)?.label}</b></span><Ruler label="时间范围" options={RANGES} value={range} onChange={setRange} /></div>
        <div className="field"><span>最大杠杆 <b className="num t1">{lev}×</b></span><Ruler label="最大杠杆" options={LEVERAGE} value={lev} onChange={setLev} /></div>
      </div>
      <div className="note"><span><b>行情中断</b>，正在自动重连（第 2 次）。期间不会发出新的交易指令。</span></div>
    </div>
  )
}

const ROWS = [
  ['BTC', '比特币', '77,429.5', '+1.24', '0.0100%', '0.13 bp', '99.08 亿'],
  ['ETH', '以太坊', '2,914.37', '+2.87', '0.0081%', '0.34 bp', '61.20 亿'],
  ['SOL', 'Solana', '187.214', '−0.66', '−0.0032%', '0.53 bp', '18.77 亿'],
  ['BNB', 'BNB', '912.430', '+0.18', '0.0050%', '1.10 bp', '3.04 亿'],
  ['XRP', '瑞波币', '2.4871', '−1.92', '0.0100%', '0.40 bp', '9.86 亿'],
  ['DOGE', '狗狗币', '0.24113', '+5.41', '0.0213%', '0.41 bp', '12.31 亿'],
]

function TableDemo() {
  const [sel, setSel] = useState('BTC')
  const wrap = useRef<HTMLDivElement>(null)
  const [bar, setBar] = useState<{ y: number; h: number; anim: boolean } | null>(null)
  useLayoutEffect(() => {
    const row = wrap.current?.querySelector<HTMLTableRowElement>('tr.sel')
    if (row) setBar((b) => ({ y: row.offsetTop, h: row.offsetHeight, anim: !!b }))
  }, [sel])
  return (
    <div className="display sr-table tbl-wrap" ref={wrap}>
      {bar && <span className={`tbl-sel ${bar.anim ? '' : 'no-anim'}`} style={{ transform: `translateY(${bar.y}px)`, height: bar.h }} />}
      <table className="tbl">
        <thead><tr><th>品种</th><th>最新价</th><th>24h</th><th className="opt">资金费率</th><th className="opt">价差</th><th className="opt">24h 成交额</th></tr></thead>
        <tbody>
          {ROWS.map((r) => (
            <tr key={r[0]} className={sel === r[0] ? 'sel' : ''} onClick={() => setSel(r[0])}>
              <td><span className="sym">{r[0]}</span><span className="sub">{r[1]}</span></td>
              <td>{r[2]}</td>
              <td className={r[3].startsWith('+') ? 'up' : 'down'}>{r[3]}%</td>
              <td className="opt">{r[4]}</td>
              <td className="opt t2">{r[5]}</td>
              <td className="opt">{r[6]}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function MaskDemo() {
  const hidden = usePrivate()
  return (
    <div className="mask-demo">
      <div className="mask-head">
        <label className="switch">
          <input type="checkbox" checked={hidden} onChange={(e) => setPrivate(e.target.checked)} />
          <span className="track"><span className="knob" /></span>
          隐藏金额
        </label>
        <span className="t3">快捷键 <kbd>H</kbd>　输入框里 <kbd>Ctrl</kbd> + <kbd>Shift</kbd> + <kbd>H</kbd></span>
      </div>
      <div className="inst-ro">
        <Ro l="账户权益" v={<Money text="12,480.22" unit="USDT" />} />
        <Ro l="今日盈亏" v={<Money text="+384.10" unit="USDT" />} cls={hidden ? '' : 'up'} />
        <Ro l="持仓数量" v={<Money text="0.420" unit="BTC" />} />
        <Ro l="今日收益率" v="+3.17%" cls="up" />
        <Ro l="BTC 价格" v="77,429.5" />
      </div>
    </div>
  )
}

const SPANS = [
  { value: 2 * 60_000, label: '2分' }, { value: 5 * 60_000, label: '5分' }, { value: 10 * 60_000, label: '10分' },
  { value: 20 * 60_000, label: '20分' }, { value: 30 * 60_000, label: '30分' },
]

/** 可切换时间范围和图表类型的价格图：演示切换时的平滑过渡 */
function SwitchableChart({ c1m, c5s, steps, tEnd }: { c1m: ReturnType<typeof toCandles>; c5s: ReturnType<typeof toCandles>; steps: { t: number; v: number }[]; tEnd: number }) {
  const [span, setSpan] = useState(10 * 60_000)
  const [mode, setMode] = useState<'candle' | 'step'>('candle')
  const candles = span <= 10 * 60_000 ? c5s : c1m
  const interval = span <= 10 * 60_000 ? 5_000 : 60_000
  return (
    <div className="display sr-chart sr-chart-wide">
      <div className="display-head">
        <span className="display-title">BTC-PERP</span>
        <span className="display-sub">{mode === 'candle' ? `${interval === 5_000 ? '5 秒' : '1 分钟'} K 线` : '逐笔成交价'}</span>
        <div className="display-tools">
          <Seg label="图表类型" value={mode} onChange={setMode} options={[{ value: 'candle', label: 'K 线' }, { value: 'step', label: '阶梯线' }]} />
          <div className="head-ruler"><Ruler label="时间范围" options={SPANS} value={span} onChange={setSpan} minor={3} /></div>
        </div>
      </div>
      <div className="display-body">
        <Scope mode={mode} candles={candles} points={steps} interval={interval} span={span} t1={tEnd} dec={1} volume={mode === 'candle'} />
      </div>
    </div>
  )
}
