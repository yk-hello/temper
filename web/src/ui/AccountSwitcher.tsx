// 顶栏的交易账户切换（D73）：全站只有这一个入口，任何页面都看得出现在用的是模拟还是实盘。
// 切到实盘账户时弹窗确认一次；登录后进上次用的账户，是实盘就提示一次（D75 第 6 条）。
import { useEffect, useRef, useState } from 'react'
import { useT, acctName } from '../lib/i18n'
import { acctEnv, selectAccount, usePaperAccounts } from '../lib/trade'
import type { PaperAccount } from '../lib/session'
import { linkProps } from '../lib/route'
import { toast } from '../lib/privacy'
import { Dialog } from './Dialog'

const NOTICE = 'qs.liveNotice' // 这次打开已经提示过"当前是实盘"

export function AccountSwitcher() {
  const t = useT()
  const { list, current } = usePaperAccounts(true)
  const [open, setOpen] = useState(false)
  const [ask, setAsk] = useState<PaperAccount | null>(null)
  const box = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const down = (e: PointerEvent) => { if (!box.current?.contains(e.target as Node)) setOpen(false) }
    const key = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    window.addEventListener('pointerdown', down)
    window.addEventListener('keydown', key)
    return () => { window.removeEventListener('pointerdown', down); window.removeEventListener('keydown', key) }
  }, [open])
  // 进来时用的是实盘账户：提示一次
  useEffect(() => {
    if (!current?.live) return
    try {
      if (sessionStorage.getItem(NOTICE) === String(current.id)) return
      sessionStorage.setItem(NOTICE, String(current.id))
    } catch { /* 隐私模式：每次都提示也无妨 */ }
    toast(t('sw.liveNow', { name: current.name, env: acctEnv(current) }))
  }, [current?.id]) // eslint-disable-line react-hooks/exhaustive-deps
  if (!list || !current) return null
  const paper = list.filter((a) => !a.live)
  const live = list.filter((a) => a.live)
  const pick = (a: PaperAccount) => {
    setOpen(false)
    if (a.id === current.id) return
    if (a.live) { setAsk(a); return }
    selectAccount(a.id)
  }
  const tag = (a: PaperAccount) => (
    <span className={`sw-tag ${a.live ? `live ${a.env ?? 'main'}` : 'paper'}`}>{a.live ? acctEnv(a) : t('sw.paper')}</span>
  )
  const item = (a: PaperAccount) => (
    <li key={a.id} role="option" aria-selected={a.id === current.id} className={a.id === current.id ? 'on' : ''} onClick={() => pick(a)} data-sw-item={a.id}>
      {tag(a)}<span className="sw-name">{a.live ? a.name : acctName(a.name)}</span>
    </li>
  )
  return (
    <div className="sw" ref={box}>
      <button type="button" className={`sw-btn ${current.live ? 'live' : ''}`} aria-haspopup="listbox" aria-expanded={open} title={t('sw.title')} onClick={() => setOpen(!open)} data-sw>
        {tag(current)}<span className="sw-name">{current.live ? current.name : acctName(current.name)}</span>
        <svg width="9" height="9" viewBox="0 0 9 9" aria-hidden="true"><path d="M1.5 3l3 3 3-3" fill="none" stroke="currentColor" strokeWidth="1.2" /></svg>
      </button>
      {open && (
        <div className="sw-pop" role="listbox" aria-label={t('sw.title')}>
          <div className="sw-group">{t('sw.paperGroup')}</div>
          <ul>{paper.map(item)}</ul>
          {live.length > 0 && <><div className="sw-group">{t('sw.liveGroup')}</div><ul>{live.map(item)}</ul></>}
          <a className="sw-manage" {...linkProps('account')} onClick={() => setOpen(false)}>{t('sw.manage')}</a>
        </div>
      )}
      <Dialog open={!!ask} title={t('sw.askTitle')} onClose={() => setAsk(null)}>
        {ask && (
          <div className="au-form" data-sw-ask>
            <p className="cf-live">{t(ask.env === 'main' || !ask.env ? 'sw.askMain' : 'sw.askTest', { name: ask.name, env: acctEnv(ask) })}</p>
            <div className="dlg-btns">
              <button type="button" className="btn ghost" onClick={() => setAsk(null)}>{t('common.cancel')}</button>
              <button type="button" className="btn primary" data-sw-ok onClick={() => { selectAccount(ask.id); setAsk(null) }}>{t('sw.askOk')}</button>
            </div>
          </div>
        )}
      </Dialog>
    </div>
  )
}

/** 顶栏选着交易所账户时，现货、机器人这些只能用模拟账户的地方：说明一句，给一个切回模拟账户的按钮。
 *  不悄悄换成别的账户下单（顶栏写的是哪个，就是哪个） */
export function PaperOnly({ what, current, first }: { what: 'spot' | 'bot'; current: PaperAccount; first: PaperAccount | null }) {
  const t = useT()
  return (
    <div className="op op-out" data-paper-only>
      <p className="op-out-h">{t(what === 'spot' ? 'sw.onlyPaperSpot' : 'sw.onlyPaperBot')}</p>
      <p className="op-out-s">{t('sw.paperOnly', { name: current.name })}</p>
      {first && <button type="button" className="btn secondary op-out-b" onClick={() => selectAccount(first.id)}>{t('sw.toPaper', { name: acctName(first.name) })}</button>}
    </div>
  )
}
