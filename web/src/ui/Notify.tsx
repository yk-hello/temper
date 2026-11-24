// 顶栏的通知铃铛（模块 7）：从按钮长出来的列表。打开就算读过，点别处或按 Esc 关。
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { growFrom, lastAnchor } from '../lib/anchor'
import { fmtTime, useT } from '../lib/i18n'
import { isSafety, noticeText, useNotices } from '../lib/notify'
import { go } from '../lib/route'

export function Bell() {
  const t = useT()
  const { items, unread, markRead, reload } = useNotices(true)
  const [open, setOpen] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const pop = useRef<HTMLDivElement>(null)
  // 打开时看到的"未读"要留到关上：不然一打开标记就没了，分不清哪些是新的
  const [fresh, setFresh] = useState<Set<number>>(new Set())
  useLayoutEffect(() => { if (open && pop.current) growFrom(pop.current, lastAnchor(), 160) }, [open])
  useEffect(() => {
    if (!open) return
    const away = (e: PointerEvent) => { if (!box.current?.contains(e.target as Node)) setOpen(false) }
    const key = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    document.addEventListener('pointerdown', away)
    window.addEventListener('keydown', key)
    return () => { document.removeEventListener('pointerdown', away); window.removeEventListener('keydown', key) }
  }, [open])
  const toggle = async () => {
    if (open) { setOpen(false); return }
    setOpen(true)
    const r = await reload()
    setFresh(new Set((r?.items ?? items).filter((n) => !n.read).map((n) => n.id)))
    if (r?.unread ?? unread) void markRead()
  }
  return (
    <div className="bar-bell" ref={box}>
      <button type="button" className="bar-bell-btn" data-bell aria-label={t('nt.title')} aria-expanded={open} title={t('nt.title')} onClick={() => void toggle()}>
        <svg width="13" height="13" viewBox="0 0 13 13" fill="none" aria-hidden="true">
          <path d="M3 9.5V5.6a3.5 3.5 0 0 1 7 0v3.9l1 1H2l1-1Z" stroke="currentColor" strokeWidth="1.1" />
          <path d="M5.4 11.7h2.2" stroke="currentColor" strokeWidth="1.1" />
        </svg>
        {unread > 0 && <b className="bar-bell-n num" data-bell-n>{unread > 99 ? '99+' : unread}</b>}
      </button>
      {open && (
        <div className="bar-bell-pop" ref={pop} data-bell-pop>
          <div className="nt-head">
            <b>{t('nt.title')}</b>
            <button type="button" className="ct-link" onClick={() => { setOpen(false); go('account&sec=notify') }}>{t('nt.settings')}</button>
          </div>
          {items.length === 0 ? <p className="nt-empty">{t('nt.empty')}</p> : (
            <ul className="nt-list">
              {items.map((n) => (
                <li key={n.id} className={`nt-row ${fresh.has(n.id) ? 'new' : ''} ${isSafety(n.kind) ? 'safety' : ''}`} data-nt={n.kind}>
                  <span className="nt-text">{noticeText(n)}</span>
                  <time className="nt-time num">{fmtTime(n.t)}</time>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  )
}
