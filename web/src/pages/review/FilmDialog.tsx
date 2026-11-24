// 高光视频：先实时预览，满意了再逐帧生成 4K 或 1080p 的 MP4，下载或直接分享。可以不带声音（发短视频时在平台里配乐）。
import { useEffect, useRef, useState } from 'react'
import { canEncode, DURATION, loadFilmFonts, paint, Q1080, Q4K, renderVideo, sfxPlan, type FilmText, type FilmTrade, type Rendered } from '../../charts/film'
import { synth } from '../../lib/sfx'
import { useT } from '../../lib/i18n'
import { Dialog } from '../../ui/Dialog'
import { Seg } from '../../ui/Seg'
import { locDec } from '../../lib/tz'

interface Props {
  open: boolean
  onClose: () => void
  trade: FilmTrade | null
  text: FilmText | null
  name: string // 下载的文件名（不含扩展名）
}

export function FilmDialog({ open, onClose, trade, text, name }: Props) {
  const t = useT()
  const [cv, setCv] = useState<HTMLCanvasElement | null>(null) // 对话框打开后才挂上画布：用状态，挂上时预览才开始
  const [playing, setPlaying] = useState(true)
  const [listen, setListen] = useState(false)
  const [withSound, setWithSound] = useState(true)
  const [q, setQ] = useState<'4k' | '1080p'>('1080p')
  const [has4k, setHas4k] = useState(false)
  const [busy, setBusy] = useState<number | null>(null)
  const [out, setOut] = useState<(Rendered & { url: string }) | null>(null)
  const [err, setErr] = useState('')
  const abort = useRef<AbortController | null>(null)

  useEffect(() => {
    if (!open) return
    void canEncode(Q4K).then((ok) => { setHas4k(ok); if (ok && !matchMedia('(pointer: coarse)').matches) setQ('4k') })
  }, [open])
  useEffect(() => () => { if (out) URL.revokeObjectURL(out.url) }, [out])

  // 预览：540 × 960，实时循环播放
  useEffect(() => {
    if (!open || !trade || !text || !cv || busy !== null || out) return
    let raf = 0, live = true
    let actx: AudioContext | null = null
    const start = performance.now()
    const g = cv.getContext('2d')!
    void loadFilmFonts().then(async () => {
      if (!live) return
      if (listen) {
        actx = new AudioContext()
        const buf = await synth(DURATION, sfxPlan(trade))
        if (!live) return
        const src = actx.createBufferSource(); src.buffer = buf; src.loop = true; src.loopEnd = DURATION
        src.connect(actx.destination); src.start(0, ((performance.now() - start) / 1000) % DURATION)
      }
      const frame = (now: number) => {
        raf = requestAnimationFrame(frame)
        if (!playing) return
        paint(g, trade, text, ((now - start) / 1000) % (DURATION + 1.2)) // 停在卡片上一会儿再重播
      }
      raf = requestAnimationFrame(frame)
    })
    return () => { live = false; cancelAnimationFrame(raf); void actx?.close() }
  }, [open, trade, text, playing, listen, busy, out, cv])

  const go = async () => {
    if (!trade || !text) return
    setErr(''); setBusy(0)
    const ac = new AbortController(); abort.current = ac
    try {
      await loadFilmFonts()
      const r = await renderVideo(trade, text, q === '4k' ? Q4K : Q1080, withSound, (p) => setBusy(p), ac.signal)
      setOut({ ...r, url: URL.createObjectURL(r.blob) })
    } catch (e) {
      if ((e as DOMException)?.name !== 'AbortError') setErr(t('hl.renderFail'))
    } finally { setBusy(null); abort.current = null }
  }
  const file = () => (out ? new File([out.blob], `${name}.${out.ext}`, { type: out.mime }) : null)
  const share = async () => {
    const f = file()
    if (!f) return
    try { await navigator.share({ files: [f] }) } catch { /* 用户取消 */ }
  }
  const f0 = file()
  const canShare = !!f0 && typeof navigator.canShare === 'function' && navigator.canShare({ files: [f0] })
  const close = () => { abort.current?.abort(); setOut(null); onClose() }

  return (
    <Dialog open={open} title={t('hl.film')} onClose={close} width={720}>
      <div className="hl-film">
        <div className="hl-stage">
          {out
            ? <video src={out.url} controls autoPlay loop playsInline data-hl-video />
            : <canvas ref={setCv} width={540} height={960} data-hl-preview />}
          {busy !== null && (
            <div className="hl-busy" role="status">
              <span>{t('hl.rendering', { p: Math.round(busy * 100) })}</span>
              <i style={{ transform: `scaleX(${busy})` }} />
            </div>
          )}
        </div>
        <div className="hl-side">
          {!out && (
            <>
              <div className="hl-row">
                <span>{t('hl.quality')}</span>
                <Seg<'4k' | '1080p'> label={t('hl.quality')} value={q} onChange={setQ} options={has4k ? [{ value: '4k', label: '4K · 60' }, { value: '1080p', label: '1080p · 60' }] : [{ value: '1080p', label: '1080p · 60' }]} />
              </div>
              <label className="op-check hl-check"><input type="checkbox" checked={withSound} onChange={(e) => setWithSound(e.target.checked)} disabled={busy !== null} data-hl-sound /> {t('hl.withSound')}</label>
              <p className="hl-meta">{t(withSound ? 'hl.soundNote' : 'hl.silentNote')}</p>
              <div className="hl-row">
                <button type="button" className="btn secondary sm" onClick={() => setPlaying((p) => !p)} disabled={busy !== null}>{t(playing ? 'hl.pause' : 'hl.play')}</button>
                <button type="button" className="btn secondary sm" onClick={() => setListen((s) => !s)} disabled={busy !== null} aria-pressed={listen}>{t(listen ? 'hl.listenOff' : 'hl.listen')}</button>
              </div>
              <p className="hl-meta">{t(q === '4k' ? 'hl.meta4k' : 'hl.meta1080')}</p>
              {busy === null
                ? <button type="button" className="btn primary" onClick={() => void go()} disabled={!trade} data-hl-render>{t('hl.render')}</button>
                : <button type="button" className="btn secondary" onClick={() => abort.current?.abort()} data-hl-cancel>{t('hl.cancel')}</button>}
            </>
          )}
          {out && (
            <>
              <p className="hl-meta">{t('hl.done', { w: out.w, h: out.h, mb: locDec((out.blob.size / 1048576).toFixed(1)) })}{out.offline ? '' : ` ${t('hl.realtime')}`}</p>
              <a className="btn primary" href={out.url} download={`${name}.${out.ext}`} data-hl-download>{t('hl.download')}</a>
              {canShare && <button type="button" className="btn secondary" onClick={() => void share()}>{t('hl.share')}</button>}
              <button type="button" className="btn secondary" onClick={() => setOut(null)}>{t('hl.again')}</button>
            </>
          )}
          {err && <p className="hl-err" role="alert">{err}</p>}
        </div>
      </div>
    </Dialog>
  )
}
