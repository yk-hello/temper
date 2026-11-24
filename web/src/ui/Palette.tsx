// 命令面板（D38，2.24）：Ctrl + K 切品种、跳页面、执行操作。全站一份。
import { useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import { createPortal } from 'react-dom'
import { useT, useLang, setLang, tr } from '../lib/i18n'
import { go, pickInstrument } from '../lib/route'
import { togglePrivate } from '../lib/privacy'
import { openWizard } from '../lib/follow'
import { useSession } from '../lib/session'
import { growFrom, lastAnchor, shrinkTo } from '../lib/anchor'
import { GLOSSARY } from '../i18n/glossary'
import { Dialog } from './Dialog'
import { useMarketList, type CoinRow } from '../lib/coins'

// ---------- 术语表（全站一份，帮助和命令面板都能打开） ----------
let glossaryOpen = false
const gSubs = new Set<() => void>()
export function openGlossary() { glossaryOpen = true; gSubs.forEach((f) => f()) }
function closeGlossary() { glossaryOpen = false; gSubs.forEach((f) => f()) }

export function GlossaryHost() {
  const t = useT()
  const lang = useLang()
  const open = useSyncExternalStore((f) => { gSubs.add(f); return () => gSubs.delete(f) }, () => glossaryOpen)
  return (
    <Dialog open={open} title={t('gl.title')} onClose={closeGlossary} width={620}>
      <dl className="gl">
        {GLOSSARY.map((x) => {
          const [k, v] = x[lang]
          return <div key={x.zh[0]}><dt>{k}</dt><dd>{v}</dd></div>
        })}
      </dl>
      <div className="dlg-btns"><button type="button" className="btn secondary" onClick={closeGlossary}>{t('common.close')}</button></div>
    </Dialog>
  )
}

// ---------- 命令面板 ----------
interface Cmd { id: string; label: string; hint?: string; kw: string; run: () => void }

export function Palette() {
  const [open, setOpen] = useState(false)
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); setOpen((v) => !v) }
    }
    window.addEventListener('keydown', on)
    const pop = () => setOpen(true)
    window.addEventListener('qs:palette', pop)
    return () => { window.removeEventListener('keydown', on); window.removeEventListener('qs:palette', pop) }
  }, [])
  if (!open) return null
  return <Panel onClose={() => setOpen(false)} />
}

function Panel({ onClose }: { onClose: () => void }) {
  const t = useT()
  const lang = useLang()
  const s = useSession()
  const coins = useMarketList()
  const [q, setQ] = useState('')
  const [act, setAct] = useState(0)
  const box = useRef<HTMLDivElement>(null)
  const list = useRef<HTMLUListElement>(null)
  const anchor = useRef(lastAnchor())
  const closing = useRef(false)
  useLayoutEffect(() => { if (box.current) growFrom(box.current, anchor.current, 180) }, [])
  const close = () => {
    if (closing.current) return
    closing.current = true
    const a = box.current ? shrinkTo(box.current, anchor.current, 120) : null
    if (a) a.finished.then(onClose, onClose)
    else onClose()
  }

  const commands = useMemo<Cmd[]>(() => {
    const out: Cmd[] = [
      { id: 'go-market', label: t('bar.overview'), hint: t('cmd.page'), kw: 'market overview shichang 市场', run: () => go('market') },
      { id: 'go-ai', label: t('bar.ai'), hint: t('cmd.page'), kw: 'ai strategy celue 策略', run: () => go('ai') },
      { id: 'go-trade', label: t('bar.trade'), hint: t('cmd.page'), kw: 'trade jiaoyi 交易 行情', run: () => go('trade') },
      { id: 'go-about', label: t('bar.about'), hint: t('cmd.page'), kw: 'about intro analyzer jieshao 介绍', run: () => go('about') },
      { id: 'go-review', label: t('bar.review'), hint: t('cmd.page'), kw: 'review journal fupan 复盘 日志 预案 回放 练习 playbook', run: () => go('review') },
      { id: 'go-risk', label: t('bar.risk'), hint: t('cmd.page'), kw: 'risk fengxian 风险 纪律 压力 discipline stress', run: () => go('risk') },
    ]
    if (s.state === 'in') {
      out.push({ id: 'go-account', label: t('bar.account'), hint: t('cmd.page'), kw: 'account settings shezhi 账户 设置', run: () => go('account') })
      out.push({ id: 'wizard', label: t('cmd.follow'), hint: t('cmd.action'), kw: 'follow ai genshui 跟随 引导', run: openWizard })
      out.push({ id: 'cancel-all', label: t('cmd.cancelAll'), hint: 'Shift + Esc', kw: 'cancel all quanche 全撤 撤单', run: () => window.dispatchEvent(new Event('qs:cancel-all')) })
    }
    out.push({ id: 'privacy', label: t('cmd.privacy'), hint: 'H', kw: 'hide privacy yincang 隐藏 金额', run: togglePrivate })
    out.push({ id: 'glossary', label: t('gl.title'), hint: t('cmd.action'), kw: 'glossary help shuyu 术语 帮助', run: openGlossary })
    out.push({ id: 'lang', label: t('cmd.lang'), hint: t('cmd.action'), kw: 'language english chinese yuyan 语言', run: () => setLang(lang === 'zh' ? 'en' : 'zh') })
    return out
  }, [s.state, lang]) // eslint-disable-line react-hooks/exhaustive-deps

  const needle = q.trim().toLowerCase()
  const shown = useMemo<Cmd[]>(() => {
    const cmd = commands.filter((c) => !needle || c.label.toLowerCase().includes(needle) || c.kw.includes(needle))
    const coin = (needle ? coins.filter((c) => c.base.toLowerCase().startsWith(needle)) : coins.slice(0, 6)).slice(0, 12)
      .map((c: CoinRow): Cmd => ({ id: `i-${c.inst}`, label: c.base, hint: tr('cmd.coin'), kw: '', run: () => pickInstrument(c.inst) }))
    return needle ? [...coin, ...cmd] : [...cmd, ...coin]
  }, [commands, coins, needle])
  useEffect(() => { setAct(0) }, [needle])
  useEffect(() => { list.current?.querySelector('[aria-selected="true"]')?.scrollIntoView({ block: 'nearest' }) }, [act])

  const run = (c?: Cmd) => { if (!c) return; onClose(); c.run() }
  return createPortal(
    <div className="pal-back" onMouseDown={(e) => { if (e.target === e.currentTarget) close() }}>
      <div className="pal" ref={box} role="dialog" aria-modal="true" aria-label={t('cmd.title')}>
        <input autoFocus className="pal-in" value={q} placeholder={t('cmd.placeholder')} onChange={(e) => setQ(e.target.value)} spellCheck={false}
          onKeyDown={(e) => {
            if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); close() }
            if (e.key === 'ArrowDown') { e.preventDefault(); setAct((a) => Math.min(shown.length - 1, a + 1)) }
            if (e.key === 'ArrowUp') { e.preventDefault(); setAct((a) => Math.max(0, a - 1)) }
            if (e.key === 'Enter') { e.preventDefault(); run(shown[act]) }
          }} />
        <ul className="pal-list" ref={list} role="listbox">
          {shown.map((c, i) => (
            <li key={c.id} role="option" aria-selected={i === act} className={i === act ? 'on' : ''} onPointerEnter={() => setAct(i)} onClick={() => run(c)}>
              <span>{c.label}</span>{c.hint && <em>{c.hint}</em>}
            </li>
          ))}
          {!shown.length && <li className="pal-none">{t('cmd.none')}</li>}
        </ul>
      </div>
    </div>,
    document.body,
  )
}
