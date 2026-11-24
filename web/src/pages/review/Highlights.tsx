// 生涯档案（D91）：每笔平仓按保证金的回报够门槛，就记一张卡片。卡片是这笔交易自己的 K 线，从开仓到平仓。
// 每张可以生成视频、保存图片、删除。门槛在这里改。"看示例"用录到的一段行情生成，全程写"演示"。
import { useEffect, useMemo, useRef, useState } from 'react'
import { drawCard, loadFilmFonts, type FilmText, type FilmTrade } from '../../charts/film'
import { delHighlight, highlightScene, loadDemo, loadHighlights, pct, setMinRoe, coinOf, type Highlight, type HlList } from '../../lib/highlight'
import { fmtTime, useT } from '../../lib/i18n'
import { toast } from '../../lib/privacy'
import { Dialog } from '../../ui/Dialog'
import { FilmDialog } from './FilmDialog'
import { Money } from '../../ui/Money'
import { numLocale } from '../../lib/tz'
import { demoSnap, filmText, filmTrade } from './filmData'
import '../../styles/highlight.css'
import { numIn } from '../../lib/format'

type Film = { trade: FilmTrade; text: FilmText; name: string }

const fileName = (inst: string, t: number) => `highlight-${coinOf(inst)}-${fmtTime(t, { year: true }).replace(/[^0-9]/g, '')}`

/** 一条高光的卡片数据：K 线和持仓期间的事件 */
async function prepare(h: Highlight): Promise<Film> {
  const s = h.snap
  const sc = await highlightScene(h.id)
  const trade = filmTrade(s, sc.candles, sc.iv, sc.events)
  return { trade, text: filmText(s, trade, false), name: fileName(s.instrument, s.close_time) }
}

export default function Highlights() {
  const t = useT()
  const [d, setD] = useState<HlList | null>(null)
  const [err, setErr] = useState(false)
  const [film, setFilm] = useState<Film | null>(null)
  const [del, setDel] = useState<Highlight | null>(null)
  const [draft, setDraft] = useState('')
  const [demoBusy, setDemoBusy] = useState(false)
  const focus = useMemo(() => Number(new URLSearchParams(location.search).get('h') ?? 0), [])
  useEffect(() => {
    loadHighlights().then((x) => { setD(x); setDraft(String(Math.round(x.min_roe * 1000) / 10)) }).catch(() => setErr(true))
  }, [])

  const saveMin = async () => {
    if (!d) return
    const v = Number(draft) / 100
    if (!(v > 0)) { setDraft(String(Math.round(d.min_roe * 1000) / 10)); return }
    try {
      const r = await setMinRoe(v)
      setD({ ...d, min_roe: r.min_roe })
      setDraft(String(Math.round(r.min_roe * 1000) / 10))
      toast(t('hl.minSaved', { p: `${Math.round(r.min_roe * 1000) / 10}%` }))
    } catch { toast(t('hl.saveFail')) }
  }
  const demo = async () => {
    setDemoBusy(true)
    try {
      const r = await loadDemo()
      const s = demoSnap(r.demo)
      const trade = filmTrade(s, r.candles, r.iv, r.events)
      setFilm({ trade, text: filmText(s, trade, true), name: `highlight-demo-${coinOf(s.instrument)}` })
    } catch { toast(t('hl.demoNone')) } finally { setDemoBusy(false) }
  }

  if (err) return <div className="rv-out"><p>{t('hl.loadFail')}</p></div>
  if (!d) return null
  const best = d.items.reduce((m, h) => Math.max(m, h.snap.roe), -Infinity)
  return (
    <div className="hl">
      <div className="hl-top">
        <div className="hl-stats" data-hl-stats>
          <span>{t('hl.count')}<b>{d.items.length}</b></span>
          {d.items.length > 0 && <span>{t('hl.best')}<b>{pct(best)}</b></span>}
          {d.items.length > 0 && <span>{t('hl.last')}<b>{fmtTime(d.items[0].created, { year: true }).slice(0, 10)}</b></span>}
        </div>
        <form className="hl-min" onSubmit={(e) => { e.preventDefault(); void saveMin() }}>
          <label htmlFor="hl-min">{t('hl.minLabel')}</label>
          <span className="hl-min-in">≥<input id="hl-min" inputMode="decimal" value={draft} onChange={(e) => setDraft(numIn(e.target.value))} onBlur={() => void saveMin()} data-hl-min />%</span>
        </form>
        <button type="button" className="btn secondary sm" onClick={() => void demo()} disabled={demoBusy} data-hl-demo>{t('hl.demoBtn')}</button>
      </div>
      {d.items.length === 0 ? (
        <div className="rv-out" data-hl-empty>
          <p>{t('hl.empty', { p: `${Math.round(d.min_roe * 1000) / 10}%` })}</p>
          <p className="hl-meta">{t('hl.emptyNote')}</p>
        </div>
      ) : (
        <div className="hl-grid">
          {d.items.map((h) => <Card key={h.id} h={h} focus={h.id === focus} onFilm={setFilm} onDelete={() => setDel(h)} />)}
        </div>
      )}
      <FilmDialog open={!!film} onClose={() => setFilm(null)} trade={film?.trade ?? null} text={film?.text ?? null} name={film?.name ?? 'highlight'} />
      <Dialog open={!!del} title={t('hl.delTitle')} onClose={() => setDel(null)}>
        <p className="hl-dlg">{t('hl.delBody')}</p>
        <div className="hl-dlg-acts">
          <button type="button" className="btn secondary" onClick={() => setDel(null)}>{t('hl.cancel')}</button>
          <button type="button" className="btn danger" data-hl-del-ok onClick={async () => {
            const h = del!; setDel(null)
            try { await delHighlight(h.id); setD((x) => x && { ...x, items: x.items.filter((i) => i.id !== h.id) }) } catch { toast(t('hl.saveFail')) }
          }}>{t('hl.delOk')}</button>
        </div>
      </Dialog>
    </div>
  )
}

function Card({ h, focus, onFilm, onDelete }: { h: Highlight; focus: boolean; onFilm: (f: Film) => void; onDelete: () => void }) {
  const t = useT()
  const cv = useRef<HTMLCanvasElement>(null)
  const box = useRef<HTMLDivElement>(null)
  const [f, setF] = useState<Film | null>(null)
  const s = h.snap
  useEffect(() => {
    let live = true
    void prepare(h).then((x) => { if (live) setF(x) })
    return () => { live = false }
  }, [h.id]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    const c = cv.current
    if (!c || !f) return
    void loadFilmFonts().then(() => drawCard(c, f.trade, f.text))
  }, [f])
  useEffect(() => { if (focus) box.current?.scrollIntoView({ block: 'center' }) }, [focus])
  const png = async () => {
    if (!f) return
    await loadFilmFonts()
    const c = document.createElement('canvas')
    c.width = 1080; c.height = 1920
    drawCard(c, f.trade, f.text)
    c.toBlob((b) => {
      if (!b) return
      const a = document.createElement('a')
      a.href = URL.createObjectURL(b)
      a.download = `${f.name}.png`
      a.click()
      setTimeout(() => URL.revokeObjectURL(a.href), 4000)
    }, 'image/png')
  }
  return (
    <div className={`hl-card${focus ? ' focus' : ''}`} ref={box} data-hl-card={h.id}>
      <canvas ref={cv} width={540} height={960} aria-label={`${coinOf(s.instrument)} ${pct(s.roe)}`} />
      <div className="hl-card-b">
        <div className="hl-card-h">
          <b>{coinOf(s.instrument)} {pct(s.roe)}{s.qty ? <span className={`hl-net num ${s.net >= 0 ? 'up' : 'down'}`}><Money text={`${s.net >= 0 ? '+' : '−'}${Math.abs(s.net).toLocaleString(numLocale(), { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`} unit="USDT" /></span> : null}</b>
          <span>{t(`hl.o.${['manual', 'ai', 'mixed', 'bot', 'signal'].includes(s.origin) ? s.origin : 'manual'}` as 'hl.o.manual')}</span>
        </div>
        <div className="hl-acts">
          <button type="button" className="btn primary sm" onClick={() => f && onFilm(f)} disabled={!f} data-hl-film>{t('hl.makeFilm')}</button>
          <button type="button" className="btn secondary sm" onClick={() => void png()} disabled={!f} data-hl-png>{t('hl.savePng')}</button>
          <button type="button" className="rv-link" onClick={onDelete}>{t('hl.delete')}</button>
        </div>
      </div>
    </div>
  )
}
