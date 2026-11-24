// 所有页面共用的顶栏：标志、导航、页面自己的状态读数（children）、语言、隐藏金额、账户。
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { growFrom, lastAnchor } from '../lib/anchor'
import { openWizard } from '../lib/follow'
import { BRAND } from '../lib/brand'
import { fmtTime, useLang, setLang, useT } from '../lib/i18n'
import { linkProps, go } from '../lib/route'
import { useSession, userLabel } from '../lib/session'
import { PrivacyButton } from './Money'
import { togglePrivate, usePrivate } from '../lib/privacy'
import { Bell } from './Notify'
import { usePaperAccounts } from '../lib/trade'
import { AccountSwitcher } from './AccountSwitcher'
import { useSystem } from '../lib/system'
import { Seg } from './Seg'

/** 帮助（3.18、3.26）：跟随引导、术语表、命令面板。从按钮长出来，点别处或按 Esc 关 */
function Help() {
  const t = useT()
  const s = useSession()
  const lang = useLang()
  const hidden = usePrivate()
  const [open, setOpen] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const pop = useRef<HTMLDivElement>(null)
  useLayoutEffect(() => { if (open && pop.current) growFrom(pop.current, lastAnchor(), 160) }, [open])
  useEffect(() => {
    if (!open) return
    const away = (e: PointerEvent) => { if (!box.current?.contains(e.target as Node)) setOpen(false) }
    const key = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    document.addEventListener('pointerdown', away)
    window.addEventListener('keydown', key)
    return () => { document.removeEventListener('pointerdown', away); window.removeEventListener('keydown', key) }
  }, [open])
  const item = (label: string, fn: () => void, k?: string) => (
    <button type="button" role="menuitem" onClick={() => { setOpen(false); fn() }}><span>{label}</span>{k && <kbd>{k}</kbd>}</button>
  )
  return (
    <div className="bar-help" ref={box}>
      <button type="button" className="bar-help-btn" aria-label={t('help.title')} aria-expanded={open} title={t('help.title')} onClick={() => setOpen(!open)}>?</button>
      {open && (
        <div className="bar-help-pop" role="menu" ref={pop}>
          {s.state === 'in' && item(t('help.follow'), openWizard)}
          {s.state === 'in' && s.user.owner && item(t('adm.title'), () => go('admin'))}
          {item(t('help.glossary'), () => void import('./Palette').then((m) => m.openGlossary()))}
          {item(t('bar.about'), () => go('about'))}
          {item(t('help.hero'), () => go('hero'))}
          {item(t('help.palette'), () => window.dispatchEvent(new Event('qs:palette')), 'Ctrl K')}
          <div className="bar-help-extra" data-help-extra>
            <div className="bar-help-lang"><span>{t('bar.langTitle')}</span><Seg label={t('bar.langTitle')} value={lang} onChange={(l) => setLang(l)} options={[{ value: 'zh', label: '中' }, { value: 'en', label: 'EN' }]} /></div>
            {item(t(hidden ? 'privacy.on' : 'privacy.off'), togglePrivate)}
          </div>
        </div>
      )}
    </div>
  )
}

/** 管理员全局停机时，每个人的顶栏都亮一个标记（D89 第 5 条）。管理员点它进管理后台 */
function HaltMark() {
  const t = useT()
  const s = useSession()
  const h = useSystem(true)
  if (!h?.level) return null
  const tip = [t(h.level === 2 ? 'adm.markTip2' : 'adm.markTip1'), h.reason, h.at ? fmtTime(h.at) : ''].filter(Boolean).join(' · ')
  // 停机了谁都要看得到；能点进"系统"页的只有所有者（D113）
  const owner = s.state === 'in' && s.user.owner
  return owner
    ? <a className="bar-halt" {...linkProps('admin')} data-tip={tip} data-halt-mark={h.level}><span className="sq warn" /><span>{t('adm.mark')}</span></a>
    : <span className="bar-halt" data-tip={tip} data-halt-mark={h.level}><span className="sq warn" /><span>{t('adm.mark')}</span></span>
}

const FIT_LEVELS = 8

export function Bar({ page, children }: { page: string; children?: ReactNode }) {
  const t = useT()
  const lang = useLang()
  const s = useSession()
  const nav = useRef<HTMLElement>(null)
  const head = useRef<HTMLElement>(null)
  // 窄屏上导航放不下时可以左右滑，当前页要在看得见的地方
  const showCurrent = () => {
    const n = nav.current
    const cur = n?.querySelector<HTMLElement>('[aria-current=page]')
    if (!n || !cur || n.scrollWidth <= n.clientWidth) return
    const a = cur.getBoundingClientRect(), b = n.getBoundingClientRect()
    if (a.left < b.left || a.right > b.right) n.scrollLeft += a.left - b.left - 8
  }
  useLayoutEffect(showCurrent, [page])
  // 放不下时逐级让位（ui.css 的 f1…f8）：按实际宽度量，不靠猜屏幕宽度。英文更长、交易页读数更多、账户切换和铃铛是登录后才出来的
  useLayoutEffect(() => {
    const h = head.current
    const right = h?.querySelector<HTMLElement>('.bar-right')
    if (!h || !right) return
    let raf = 0
    const fit = () => {
      for (let i = 1; i <= FIT_LEVELS; i++) h.classList.remove('f' + i)
      const over = () => right.getBoundingClientRect().right > h.getBoundingClientRect().right + 0.5
      for (let i = 1; i <= FIT_LEVELS && over(); i++) h.classList.add('f' + i)
      showCurrent()
    }
    fit()
    const ro = new ResizeObserver(() => { cancelAnimationFrame(raf); raf = requestAnimationFrame(fit) })
    ro.observe(h)
    ro.observe(right)
    return () => { ro.disconnect(); cancelAnimationFrame(raf) }
  }, [])
  const accts = usePaperAccounts(true).list
  const [late, setLate] = useState(false) // 万一账户列表一直取不到：1.5 秒后照样显示，不能让顶栏右边一直空着
  useEffect(() => { const id = setTimeout(() => setLate(true), 1500); return () => clearTimeout(id) }, [])
  const ready = late || s.state === 'out' || (s.state === 'in' && accts !== null)
  return (
    <header className="bar" ref={head}>
      <a className="mark" {...linkProps('')}>{BRAND}</a>
      <nav className="nav" ref={nav}>
        <a {...linkProps('market')} aria-current={page === 'market' ? 'page' : undefined}>{t('bar.overview')}</a>
        <a {...linkProps('ai')} aria-current={page === 'ai' ? 'page' : undefined}>{t('bar.ai')}</a>
        <a {...linkProps('trade')} aria-current={page === 'trade' ? 'page' : undefined}>{t('bar.trade')}</a>
        <a {...linkProps('risk')} aria-current={page === 'risk' ? 'page' : undefined}>{t('bar.risk')}</a>
        <a {...linkProps('review')} aria-current={page === 'review' ? 'page' : undefined}>{t('bar.review')}</a>
        <a data-nav="about" {...linkProps('about')} aria-current={page === 'about' ? 'page' : undefined}>{t('bar.about')}</a>
      </nav>
      {/* 刷新时右边先是"没登录"的样子，再一个个冒出账户、铃铛、邮箱，整条顶栏像抽了一下（用户 11-15）。
          登录状态和账户列表都到了再一次性淡入 */}
      <div className={`bar-right${ready ? '' : ' pending'}`}>
        {children}
        {s.state === 'in' && <HaltMark />}
        {s.state === 'in' && <AccountSwitcher />}
        <div className="bar-lang">
          <Seg label={t('bar.langTitle')} value={lang} onChange={(l) => setLang(l)} options={[{ value: 'zh', label: '中' }, { value: 'en', label: 'EN' }]} />
        </div>
        <PrivacyButton />
        <Help />
        {/* "系统"页（全局停机、发信邮箱、AI 点评、注册开关……）：原来只在"？"菜单和账户页最底下，用户找不到（11-16） */}
        {/* 只有所有者能进"系统"页（D113）：别的账号不显示这个入口 */}
        {s.state === 'in' && s.user.owner && (
          <a className="bar-sys" {...linkProps('admin')} aria-label={t('adm.title')} title={t('adm.title')} aria-current={page === 'admin' ? 'page' : undefined} data-sys-entry>
            <svg width="13" height="13" viewBox="0 0 13 13" fill="none" aria-hidden="true">
              <path d="M10.53 5.50 L12.17 5.55 L12.17 7.45 L10.53 7.50 L10.06 8.64 L11.18 9.84 L9.84 11.18 L8.64 10.06 L7.50 10.53 L7.45 12.17 L5.55 12.17 L5.50 10.53 L4.36 10.06 L3.16 11.18 L1.82 9.84 L2.94 8.64 L2.47 7.50 L0.83 7.45 L0.83 5.55 L2.47 5.50 L2.94 4.36 L1.82 3.16 L3.16 1.82 L4.36 2.94 L5.50 2.47 L5.55 0.83 L7.45 0.83 L7.50 2.47 L8.64 2.94 L9.84 1.82 L11.18 3.16 L10.06 4.36 Z" stroke="currentColor" strokeWidth="1.1" strokeLinejoin="round" />
              <circle cx="6.5" cy="6.5" r="1.7" stroke="currentColor" strokeWidth="1.1" />
            </svg>
          </a>
        )}
        {s.state === 'in' && <Bell />}
        {s.state === 'in' ? (
          <a className="bar-user" {...linkProps('account')} title={t('bar.account')}>
            <svg className="bar-user-ic" width="13" height="13" viewBox="0 0 13 13" fill="none" aria-hidden="true">
              <circle cx="6.5" cy="4.4" r="2.4" stroke="currentColor" strokeWidth="1.1" />
              <path d="M1.9 12.2c.4-2.5 2.3-4 4.6-4s4.2 1.5 4.6 4" stroke="currentColor" strokeWidth="1.1" />
            </svg>
            <span className="bar-user-name">{userLabel(s.user)}</span>
          </a>
        ) : s.state === 'out' ? (
          <>
            <a className="bar-user bar-login" {...linkProps('login')}>{t('bar.login')}</a>
            <a className="btn primary sm bar-signup" {...linkProps('login&mode=register')}>{t('bar.signup')}</a>
          </>
        ) : null}
      </div>
    </header>
  )
}
