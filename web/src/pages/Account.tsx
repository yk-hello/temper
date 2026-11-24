// 账户设置（模块 1，验收 1.5–1.7、1.9、1.10）：登录方式、语言、二级密码、模拟账户、退出。
import { AlertsSection, NotifySection, RulesSection } from './NotifySettings'
import { LiveSection } from './LiveAccounts'
import { MyStrategies } from './MyStrategies'
import { BinanceKeys } from './BinanceKeys'
import { SecuritySection } from './Security'
import { locDec, NUM_OPTIONS, TZ_OPTIONS } from '../lib/tz'
import { openGlossary } from '../ui/Palette'
import { openWizard } from '../lib/follow'
import { fmtMs, setLatency, useLatency } from '../lib/latency'
import { Select } from '../ui/Select'
import { numLocale } from '../lib/tz'
import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { useT, useLang, setLang, errText, fmtTime, type Key, acctName, tr } from '../lib/i18n'
import { refreshBots, useBots } from '../lib/bots'
import { acctLabel, refreshPaperAccounts, usePaperAccounts } from '../lib/trade'
import { Confirm } from './trade/Lists'
import { api, ApiError, logout, refreshSession, setUser, useSession, type PaperAccount, type User } from '../lib/session'
import { go } from '../lib/route'
import { toast } from '../lib/privacy'
import { Bar } from '../ui/Bar'
import { Seg } from '../ui/Seg'
import { Field, FormError } from '../ui/Field'
import { EmailError } from '../ui/EmailError'
import { setPrefs, usePrefs, type Prefs, HOT_IDS, hotKey, hotLabel, hotOk, setHot, resetHot, type HotId } from '../lib/prefs'
import { emailIssue } from '../lib/email'
import { Dialog } from '../ui/Dialog'
import { Money } from '../ui/Money'
import { Verify } from './Auth'
import '../styles/account.css'
import { numIn } from '../lib/format'
import { BRAND } from '../lib/brand'

const errOf = (e: unknown) => errText(e instanceof ApiError ? e.code : 'INTERNAL_ERROR')

export default function Account() {
  const t = useT()
  const lang = useLang()
  const s = useSession()
  useEffect(() => { if (s.state === 'out') go('login', true) }, [s.state])
  useEffect(() => { document.title = `${t('acct.title')} · ${BRAND}` }, [lang]) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="shell ac-shell">
      <Bar page="account" />
      <main className="ac mk-scroll">
        {s.state === 'in' ? (
          <div className="ac-in">
            <h1 className="ac-title">{t('acct.title')}</h1>
            <Methods user={s.user} />
            <Section title={t('acct.lang')} hint={t('acct.langHint')}>
              <Seg label={t('acct.lang')} value={lang} onChange={(l) => setLang(l)} options={[{ value: 'zh', label: t('lang.zh') }, { value: 'en', label: t('lang.en') }]} />
            </Section>
            {s.user.has_password && <LoginPassword />}
            <TradePassword user={s.user} />
            <SecuritySection user={s.user} Section={Section} />
            <NotifySection Section={Section} />
            <AlertsSection Section={Section} />
            <RulesSection Section={Section} />
            <Papers />
            <LiveSection user={s.user} Section={Section} />
            <EnginePriority />
            <SignalHooks />
            <MyStrategies user={s.user} Section={Section} />
            <BinanceKeys user={s.user} Section={Section} />
            <Display />
            <TradingPrefs />
            {/* 只有所有者能进"系统"页（D113） */}
            {s.user.owner && <Section title={t('adm.title')} hint={t('adm.entryHint')}>
              <button className="btn secondary" onClick={() => go('admin')} data-admin-entry>{t('adm.open')}</button>
            </Section>}
            <Section title={t('acct.logout')} hint={t('acct.logoutHint')}>
              <button className="btn secondary" onClick={() => logout().then(() => go('login', true))}>{t('acct.logout')}</button>
            </Section>
            <DeleteMe user={s.user} />
          </div>
        ) : s.state === 'unavailable' ? (
          <div className="note">{t('auth.unavailable')}</div>
        ) : null}
      </main>
    </div>
  )
}

/** 交易偏好：下单前确认（逐类开关）、默认数量单位、键盘快捷键。存在这台设备的浏览器里（lib/prefs.ts）。 */
function TradingPrefs() {
  const t = useT()
  const p = usePrefs()
  const [hk, setHk] = useState(false)
  const kl = (id: HotId) => hotLabel(hotKey(id))
  const sw = (k: keyof Prefs['confirm'], label: string) => (
    <label className="op-check pf-sw"><input type="checkbox" checked={p.confirm[k]} onChange={(e) => setPrefs((x) => ({ ...x, confirm: { ...x.confirm, [k]: e.target.checked } }))} /><span>{label}</span></label>
  )
  return (
    <Section title={t('pf.title')} hint={t('pf.sub')}>
      <div className="ac-rows">
        <div className="ac-row pf-row">
          <span className="ac-k">{t('pf.confirm')}<small className="pf-sub">{t('pf.confirmSub')}</small></span>
          <span className="pf-grid">{sw('market', t('pf.cMarket'))}{sw('limit', t('pf.cLimit'))}{sw('cond', t('pf.cCond'))}</span>
        </div>
        <div className="ac-row pf-row">
          <span className="ac-k">{t('pf.unit')}</span>
          <span className="ac-act"><Seg label={t('pf.unit')} value={p.unit} onChange={(u) => setPrefs((x) => ({ ...x, unit: u }))} options={[{ value: 'usdt', label: 'USDT' }, { value: 'coin', label: t('pf.unitCoin') }]} /></span>
        </div>
        <LatencyRow />
        <div className="ac-row pf-row">
          <span className="ac-k">{t('pf.keys')}<small className="pf-sub">{t('pf.keysSub', { buy: kl('buy'), sell: kl('sell'), privacy: kl('privacy'), max: kl('max') })}</small></span>
          <span className="ac-act">
            <button className="btn secondary sm" data-hk-open disabled={!p.keys} onClick={() => setHk(true)}>{t('hk.open')}</button>
            <Seg label={t('pf.keys')} value={p.keys ? 'on' : 'off'} onChange={(v) => setPrefs((x) => ({ ...x, keys: v === 'on' }))} options={[{ value: 'on', label: t('pf.on') }, { value: 'off', label: t('pf.off') }]} />
          </span>
        </div>
      </div>
      <HotkeysDialog open={hk} onClose={() => setHk(false)} />
    </Section>
  )
}

/** 改快捷键（18 第 9.3 条）：点一个键，再按想用的键。和别的冲突时不改、说清楚冲突的是哪个。只存在这台设备的浏览器里 */
function HotkeysDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const t = useT()
  usePrefs()
  const [wait, setWait] = useState<HotId | null>(null)
  const [err, setErr] = useState('')
  const close = () => { setWait(null); setErr(''); onClose() }
  const onKey = (id: HotId) => (e: React.KeyboardEvent) => {
    if (wait !== id || e.key === 'Escape' || e.key === 'Tab') return // Esc 关对话框（不改），Tab 照常换焦点
    e.preventDefault()
    e.stopPropagation() // 别让全局的快捷键（比如隐藏金额）也收到
    if (['Shift', 'Control', 'Alt', 'Meta', 'CapsLock', 'Dead'].includes(e.key)) return
    if (e.ctrlKey || e.metaKey || e.altKey || !hotOk(e.key)) { setErr(t('hk.bad')); return }
    const clash = setHot(id, e.key)
    if (clash) { setErr(t('hk.clash', { k: hotLabel(e.key), what: t(`hk.${clash}` as Key) })); return }
    setErr('')
    setWait(null)
  }
  return (
    <Dialog open={open} title={t('hk.title')} onClose={close} width={440}>
      <p className="dlg-text hk-sub">{t('hk.sub')}</p>
      <ul className="hk-list" data-hk-list>
        {HOT_IDS.map((id) => (
          <li key={id}>
            <span>{t(`hk.${id}` as Key)}</span>
            <button type="button" className={`hk-key ${wait === id ? 'on' : ''}`} data-hk={id} aria-pressed={wait === id}
              aria-label={t('hk.change', { what: t(`hk.${id}` as Key), k: hotLabel(hotKey(id)) })}
              onClick={() => { setWait(wait === id ? null : id); setErr('') }} onKeyDown={onKey(id)} onBlur={() => { if (wait === id) setWait(null) }}>
              {wait === id ? <span className="hk-press">{t('hk.press')}</span> : <kbd>{hotLabel(hotKey(id))}</kbd>}
            </button>
          </li>
        ))}
      </ul>
      <p className={`hk-err ${err ? 'bad' : ''}`} role="alert">{err}</p>
      <p className="hk-fixed">{t('hk.fixed')}</p>
      <div className="dlg-btns">
        <button type="button" className="btn secondary" data-hk-reset onClick={() => { resetHot(); setErr(''); setWait(null) }}>{t('hk.reset')}</button>
        <button type="button" className="btn primary" onClick={close}>{t('common.close')}</button>
      </div>
    </Dialog>
  )
}

/** 模拟下单延迟（D94）：单子在网上跑的时间。存在服务器上，所有模拟账户都按它成交 */
function LatencyRow() {
  const t = useT()
  const lat = useLatency()
  if (!lat) return null
  const ms = (v: number) => t('lat.ms', { v: fmtMs(v) })
  const opts = [
    { value: 'measured', label: t('lat.measured'), hint: lat.measured_ms != null ? ms(lat.measured_ms) : t('lat.noMeasure') },
    ...lat.regions.map((r) => ({ value: r.key, label: t(`lat.r.${r.key}` as Key), hint: ms(r.rtt_ms) })),
    { value: 'off', label: t('lat.off'), hint: t('lat.offHint') },
  ]
  return (
    <div className="ac-row pf-row" data-latency>
      <span className="ac-k">{t('lat.title')}<small className="pf-sub">{t('lat.sub')}</small></span>
      <span className="ac-act"><Select className="pf-lat" label={t('lat.title')} value={lat.mode} options={opts}
        onChange={(m) => { setLatency(m).then(() => toast(t('lat.saved'))).catch(() => toast(errText('INTERNAL_ERROR'))) }} /></span>
    </div>
  )
}

/** 显示设置（3.26）：涨跌颜色、时区、数字格式，帮助 */
function Display() {
  const t = useT()
  const p = usePrefs()
  const sample = (1234567.89).toLocaleString(p.numFmt, { minimumFractionDigits: 2, maximumFractionDigits: 2 })
  return (
    <Section title={t('set.title')} hint={t('set.hint')}>
      <div className="ac-set">
        <span className="ac-set-l">{t('set.color')}</span>
        <Seg label={t('set.color')} value={p.colorMode} onChange={(v) => setPrefs((x) => ({ ...x, colorMode: v }))}
          options={[{ value: 'green-up', label: <><i className="ac-dot up" />{t('set.greenUp')}</> }, { value: 'red-up', label: <><i className="ac-dot up" />{t('set.redUp')}</> }]} />
        <span className="ac-set-l">{t('set.tz')}</span>
        <Select className="ac-sel" label={t('set.tz')} value={p.tz} onChange={(v) => setPrefs((x) => ({ ...x, tz: v }))}
          options={TZ_OPTIONS.map((z) => ({ value: z as string, label: t(`set.tz.${z}` as Key) }))} />
        <span className="ac-set-l">{t('set.num')}</span>
        <Select className="ac-sel" label={t('set.num')} value={p.numFmt} onChange={(v) => setPrefs((x) => ({ ...x, numFmt: v }))}
          options={NUM_OPTIONS.map((n) => ({ value: n as string, label: (1234567.89).toLocaleString(n, { minimumFractionDigits: 2, maximumFractionDigits: 2 }) }))} />
        <span className="ac-set-l">{t('set.help')}</span>
        <span className="ac-set-btns">
          <button type="button" className="btn ghost sm" onClick={openGlossary}>{t('help.glossary')}</button>
          <button type="button" className="btn ghost sm" onClick={openWizard}>{t('help.follow')}</button>
        </span>
      </div>
      <span className="ac-sample num" aria-hidden="true">{sample}</span>
    </Section>
  )
}

function Section({ title, hint, aside, children, sec }: { title: string; hint?: string; aside?: React.ReactNode; children: React.ReactNode; sec?: string }) {
  return (
    <section className="ac-sec" data-sec={sec}>
      <div className="ac-sec-l">
        <h2>{title}{aside}</h2>
        {hint && <p>{hint}</p>}
      </div>
      <div className="ac-sec-r">{children}</div>
    </section>
  )
}

// ---------- 登录方式 ----------

function Methods({ user }: { user: User }) {
  const t = useT()
  const [dlg, setDlg] = useState<'email' | 'verify' | null>(null)
  const [dev, setDev] = useState<string>()
  const [err, setErr] = useState<string>()
  const [unbind, setUnbind] = useState<'email' | 'wallet' | null>(null)

  const doUnbind = async () => {
    try {
      const r = await api<{ user: User }>('POST', '/api/me/unbind', { kind: unbind })
      setUser(r.user)
      setUnbind(null)
    } catch (e) {
      setErr(errOf(e))
      setUnbind(null)
    }
  }

  return (
    <Section title={t('acct.methods')} hint={t('acct.methodsHint')}>
      <div className="ac-rows">
        <div className="ac-row">
          <span className="ac-k">{t('auth.name')}</span>
          <span className="ac-v" data-acct-name>{user.name || '—'}</span>
          <span />
          <span className="ac-act" />
        </div>
        <div className="ac-row">
          <span className="ac-k">{t('acct.email')}</span>
          {user.email ? (
            <>
              <span className="ac-v" data-acct-email>{user.email}</span>
              <span className={`ac-tag ${user.email_verified ? 'ok' : 'warn'}`}><span className={`sq ${user.email_verified ? 'ok' : 'warn'}`} />{t(user.email_verified ? 'acct.verified' : 'acct.unverified')}</span>
              <span className="ac-act">
                {!user.email_verified && <button className="btn ghost sm" onClick={() => setDlg('verify')}>{t('acct.verifyNow')}</button>}
                {user.name && <button className="btn ghost sm" onClick={() => setUnbind('email')} data-acct-unbind>{t('acct.unbind')}</button>}
              </span>
            </>
          ) : (
            <>
              <span className="ac-v t3">{t('acct.noEmail')}</span>
              <span />
              <span className="ac-act"><button className="btn secondary sm" onClick={() => setDlg('email')} data-acct-bind>{t('acct.bindEmail')}</button></span>
            </>
          )}
        </div>
      </div>
      <FormError text={err} />

      <Dialog open={dlg === 'email'} title={t('acct.bindEmailTitle')} onClose={() => setDlg(null)}>
        <BindEmail onDone={(d, verified) => { if (verified) { setDlg(null); toast(t('auth.verified')); return } setDev(d); setDlg('verify') }} onCancel={() => setDlg(null)} />
      </Dialog>
      <Dialog open={dlg === 'verify'} title={t('auth.verifyTitle')} onClose={() => setDlg(null)}>
        <Verify email={user.email ?? ''} dev={dev} onDone={() => { setDlg(null); toast(t('auth.verified')) }} onLater={() => setDlg(null)} />
      </Dialog>
      <Dialog open={!!unbind} title={t('acct.unbind')} onClose={() => setUnbind(null)}>
        <p className="dlg-text">{t('acct.unbindConfirm', { what: unbind === 'email' ? t('acct.email') : t('acct.wallet') })}</p>
        <div className="dlg-btns">
          <button className="btn ghost" onClick={() => setUnbind(null)}>{t('common.cancel')}</button>
          <button className="btn danger" onClick={doUnbind}>{t('acct.unbind')}</button>
        </div>
      </Dialog>
    </Section>
  )
}

function BindEmail({ onDone, onCancel }: { onDone: (dev?: string, verified?: boolean) => void; onCancel: () => void }) {
  const t = useT()
  const s = useSession()
  const hasPw = s.state === 'in' && !!s.user.has_password
  const [email, setEmail] = useState('')
  const [pw, setPw] = useState('')
  const [err, setErr] = useState<{ f?: 'email' | 'pw'; m: ReactNode }>()
  const [busy, setBusy] = useState(false)
  const tried = useRef(false)
  const check = (v: string) => { const ei = emailIssue(v); return ei ? { f: 'email' as const, m: <EmailError issue={ei} onFix={setEmail} /> } : undefined }
  // 提交过一次以后，边输边提示
  useEffect(() => { if (tried.current) setErr(check(email)) }, [email]) // eslint-disable-line react-hooks/exhaustive-deps
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    tried.current = true
    const le = check(email)
    if (le) { setErr(le); return }
    setBusy(true)
    try {
      const r = await api<{ user: User; dev_code?: string }>('POST', '/api/me/bind-email', { email, password: hasPw ? undefined : pw })
      setUser(r.user)
      onDone(r.dev_code, r.user.email_verified) // 没配发信邮箱的部署：直接算已验证，不用再填验证码
    } catch (e) {
      const c = e instanceof ApiError ? e.code : ''
      setErr({ f: c === 'BAD_EMAIL' || c === 'EMAIL_TAKEN' ? 'email' : c === 'WEAK_PASSWORD' ? 'pw' : undefined, m: errOf(e) })
    } finally {
      setBusy(false)
    }
  }
  return (
    <form className="au-form" onSubmit={submit} noValidate>
      <p className="dlg-text">{t('acct.bindEmailHint')}</p>
      <Field label={t('auth.email')} type="email" autoComplete="email" value={email} onChange={setEmail} error={err?.f === 'email' ? err.m : undefined} />
      {/* 已经有登录密码（D113 起都有）：绑邮箱不用再填 */}
      {!hasPw && <Field label={t('auth.password')} type="password" reveal autoComplete="new-password" hint={t('auth.passwordHint')} value={pw} onChange={setPw} error={err?.f === 'pw' ? err.m : undefined} />}
      <FormError text={err && !err.f ? err.m : undefined} />
      <div className="dlg-btns">
        <button type="button" className="btn ghost" onClick={onCancel}>{t('common.cancel')}</button>
        <button className="btn primary" disabled={busy}>{t('acct.bindEmail')}</button>
      </div>
    </form>
  )
}

// ---------- 二级密码 ----------

function LoginPassword() {
  const t = useT()
  const [open, setOpen] = useState(false)
  const [old, setOld] = useState('')
  const [pw, setPw] = useState('')
  const [err, setErr] = useState<{ f?: 'old' | 'new'; m: string }>()
  const [busy, setBusy] = useState(false)
  useEffect(() => { if (!open) { setOld(''); setPw(''); setErr(undefined) } }, [open])
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if ([...pw].length < 10) { setErr({ f: 'new', m: errText('WEAK_PASSWORD') }); return }
    setBusy(true)
    try {
      await api('POST', '/api/me/password', { old, new: pw })
      setOpen(false)
      toast(t('acct.pwSaved'))
    } catch (e) {
      const c = e instanceof ApiError ? e.code : ''
      setErr({ f: c === 'BAD_LOGIN' || c === 'LOCKED' ? 'old' : c === 'WEAK_PASSWORD' || c === 'SAME_AS_LOGIN' ? 'new' : undefined, m: c === 'BAD_LOGIN' ? t('acct.pwBadOld') : errOf(e) })
    } finally {
      setBusy(false)
    }
  }
  return (
    <Section title={t('acct.pw')} hint={t('acct.pwHint')}>
      <div className="ac-row single">
        <span className="ac-act"><button className="btn ghost sm" onClick={() => setOpen(true)}>{t('acct.pwChange')}</button></span>
      </div>
      <Dialog open={open} title={t('acct.pwChange')} onClose={() => setOpen(false)}>
        <form className="au-form" onSubmit={submit} noValidate>
          <Field label={t('acct.pwOld')} type="password" reveal autoComplete="current-password" value={old} onChange={setOld} error={err?.f === 'old' ? err.m : undefined} />
          <Field label={t('acct.pwNew')} type="password" reveal autoComplete="new-password" hint={t('acct.pwNewHint')} value={pw} onChange={setPw} error={err?.f === 'new' ? err.m : undefined} />
          <FormError text={err && !err.f ? err.m : undefined} />
          <div className="dlg-btns">
            <button type="button" className="btn ghost" onClick={() => setOpen(false)}>{t('common.cancel')}</button>
            <button className="btn primary" disabled={busy}>{t('common.save')}</button>
          </div>
        </form>
      </Dialog>
    </Section>
  )
}

function DeleteMe({ user }: { user: User }) {
  const t = useT()
  const [open, setOpen] = useState(false)
  const [pw, setPw] = useState('')
  const [err, setErr] = useState<string>()
  const [busy, setBusy] = useState(false)
  useEffect(() => { if (!open) { setPw(''); setErr(undefined) } }, [open])
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    try {
      await api('POST', '/api/me/delete', { trade_password: pw })
      await logout().catch(() => undefined)
      go('login', true)
      toast(t('acct.deleted')) // 11-17 走查：删完直接回登录页，一句话都没有
    } catch (e) {
      setErr(errOf(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Section title={t('acct.del')} hint={t('acct.delHint')}>
      <div className="ac-row single">
        {/* 没设二级密码时按钮是灰的：原因直接写出来，并能跳到上面"二级密码"那一节（11-17 走查：原来只在鼠标悬停提示里） */}
        {!user.has_trade_password && (
          <span className="ac-v t3 ac-del-need">{t('acct.delNeedPw')}<button type="button" className="ac-del-go" onClick={() => document.querySelector('[data-sec="tradepw"]')?.scrollIntoView({ block: 'center', behavior: 'smooth' })}>{t('acct.delGoTp')}</button></span>
        )}
        <span className="ac-act"><button className="btn ghost sm" disabled={!user.has_trade_password} onClick={() => setOpen(true)}>{t('acct.delBtn')}</button></span>
      </div>
      <Dialog open={open} title={t('acct.delBtn')} onClose={() => setOpen(false)}>
        <form className="au-form" onSubmit={submit} noValidate>
          <p className="dlg-text">{t('acct.delWarn')}</p>
          <Field label={t('acct.tradePwOld')} type="password" reveal autoComplete="off" value={pw} onChange={setPw} error={err} />
          <div className="dlg-btns">
            <button type="button" className="btn ghost" onClick={() => setOpen(false)}>{t('common.cancel')}</button>
            <button className="btn danger" disabled={busy || !pw}>{t('acct.delBtn')}</button>
          </div>
        </form>
      </Dialog>
    </Section>
  )
}

/** 引擎优先级（D42、D83）：手动永远最高；别的引擎按这个顺序，靠前的要交易同一个币时，靠后的让路 */
function EnginePriority() {
  const t = useT()
  const data = useBots(true)
  const order = data?.order ?? ['ai', 'signal', 'bot']
  const save = async (next: string[]) => {
    try { await api('POST', '/api/bots/order', { order: next }); refreshBots(); toast(t('acct.prioSaved')) } catch (e) { toast(errOf(e)) }
  }
  const move = (i: number, d: number) => { const n = [...order]; const j = i + d; [n[i], n[j]] = [n[j], n[i]]; void save(n) }
  return (
    <Section title={t('acct.prio')} hint={t('acct.prioHint')}>
      <ol className="ac-prio" data-prio>
        <li><b>1</b><span>{t('acct.prio.manual')}</span><em>{t('acct.prioFixed')}</em></li>
        {order.map((x, i) => (
          <li key={x} data-prio-item={x}><b>{i + 2}</b><span>{t(`acct.prio.${x}` as Key)}</span>
            <span className="ac-prio-btns">
              <button className="btn ghost sm" disabled={i === 0} aria-label={t('acct.prioUp')} onClick={() => move(i, -1)}>↑</button>
              <button className="btn ghost sm" disabled={i === order.length - 1} aria-label={t('acct.prioDown')} onClick={() => move(i, 1)}>↓</button>
            </span>
          </li>
        ))}
      </ol>
    </Section>
  )
}

interface SigHook { id: number; account: number; name: string; budget: string; enabled: boolean; created: number }
interface SigLog { t: number; hook: number; raw: string; status: string; code?: string; order?: string }

/** TradingView 警报和 Webhook（D48）。地址里的密钥只在创建时显示一次 */
function SignalHooks() {
  const t = useT()
  const { list: accounts, current } = usePaperAccounts(true) // 交易所账户也能绑信号（D93）
  const [acctSel, setAcctSel] = useState('')
  const target = accounts?.find((a) => String(a.id) === acctSel) ?? current
  const [data, setData] = useState<{ hooks: SigHook[]; logs: SigLog[] } | null>(null)
  const [name, setName] = useState('')
  const [budget, setBudget] = useState('')
  // 绑到交易所账户（真钱）要二级密码（D107）：地址里带着密钥，谁拿到谁就能从任何地方往这个账户下单
  const [tpw, setTpw] = useState('')
  const [created, setCreated] = useState<{ url: string; token: string } | null>(null)
  const [test, setTest] = useState(() => tr('sig.testDefault'))
  const [msg, setMsg] = useState('')
  const load = async () => { try { setData(await api('GET', '/api/signal-hooks')) } catch { /* 下次再试 */ } }
  useEffect(() => { void load() }, [])
  const add = async () => {
    if (!target) return
    try {
      const r = await api<{ token: string }>('POST', '/api/signal-hooks', { account: target.id, name, budget: budget || '0', trade_password: target.live ? tpw : undefined })
      setCreated({ token: r.token, url: `${location.origin}/api/hook/tv/${r.token}` })
      setName(''); setBudget(''); setTpw('')
      void load()
    } catch (e) { toast(errOf(e)) }
  }
  const send = async () => {
    setMsg('')
    if (!created) {
      try {
        const res = await fetch('/api/signal-hooks/parse', { method: 'POST', headers: { 'X-QS-Client': 'web' }, body: test })
        const r = await res.json()
        setMsg(res.ok ? t('acct.sigParsed', { a: t(`acct.sigA.${r.action}` as Key), c: r.coin, p: r.pct }) : errText(r.code))
      } catch { setMsg(errText('INTERNAL_ERROR')) }
      return
    }
    try {
      const r = await fetch(`/api/hook/tv/${created.token}`, { method: 'POST', body: test })
      const j = await r.json()
      setMsg(j.status === 'ok' ? t('acct.sigOk') : j.status === 'duplicate' ? t('acct.sigDup') : errText(j.code))
      void load()
    } catch { setMsg(errText('INTERNAL_ERROR')) }
  }
  const local = /^(localhost|127\.|\[::1\])/.test(location.hostname)
  const nameOf = (id: number) => { const a = accounts?.find((x) => x.id === id); return a ? acctLabel(a) : '' }
  return (
    <Section title={t('acct.sig')} hint={t('acct.sigHint')}>
      {local && <p className="ac-sig-note" data-sig-local>{t('acct.sigLocal')}</p>}
      <div className="ac-sig-new">
        {(accounts?.length ?? 0) > 1 && target && (
          <div className="fld ac-sig-acct"><span className="fld-l">{t('paper.title')}</span>
            <Select label={t('paper.title')} value={String(target.id)} onChange={setAcctSel} options={(accounts ?? []).map((a) => ({ value: String(a.id), label: acctLabel(a) }))} />
          </div>
        )}
        <Field label={t('acct.sigName')} value={name} onChange={setName} maxLength={40} />
        <Field label={t('acct.sigBudget')} value={budget} onChange={(v) => setBudget(numIn(v))} hint={t('acct.sigBudgetHint')} />
        {target?.live && <Field label={t('acct.sigLivePw')} type="password" reveal value={tpw} onChange={setTpw} autoComplete="off" hint={t('acct.sigLivePwHint')} />}
        <button className="btn secondary" data-sig-add onClick={() => void add()}>{t('acct.sigAdd')}</button>
      </div>
      {created && (
        <div className="ac-sig-url" data-sig-url>
          <p>{t('acct.sigOnce')}</p>
          <code>{created.url}</code>
        </div>
      )}
      <div className="ac-sig-test">
        <Field label={t('acct.sigFormat')} value={test} onChange={setTest} hint={t('acct.sigFormatHint')} />
        <button className="btn ghost" data-sig-send onClick={() => void send()}>{t(created ? 'acct.sigSend' : 'acct.sigCheck')}</button>
        {msg && <span className="ac-sig-msg" data-sig-msg>{msg}</span>}
      </div>
      {!!data?.hooks.length && (
        <table className="ac-tbl" data-sig-hooks>
          <thead><tr><th>{t('acct.sigName')}</th><th>{t('paper.title')}</th><th>{t('acct.sigBudgetS')}</th><th /></tr></thead>
          <tbody>{data.hooks.map((h) => (
            <tr key={h.id}><td>{h.name || `#${h.id}`}</td><td>{nameOf(h.account)}</td><td className="num">{Number(h.budget) > 0 ? h.budget : t('acct.sigAvail')}</td>
              <td className="r"><button className="btn ghost sm" onClick={() => void api('DELETE', `/api/signal-hooks/${h.id}`).then(load)}>{t('acct.sigDel')}</button></td></tr>
          ))}</tbody>
        </table>
      )}
      {!!data?.logs.length && (
        <table className="ac-tbl" data-sig-logs>
          <thead><tr><th>{t('sp.time')}</th><th>{t('acct.sigRaw')}</th><th>{t('bt.c.status')}</th></tr></thead>
          <tbody>{data.logs.slice(0, 10).map((l, i) => (
            <tr key={i}><td className="num">{fmtTime(l.t, { seconds: true })}</td><td className="ac-sig-raw">{l.raw}</td><td className={l.status === 'ok' ? 'up' : l.status === 'rejected' ? 'warn-t' : 't3'}>{l.status === 'ok' ? t('acct.sigSt.ok') : l.status === 'duplicate' ? t('acct.sigSt.dup') : errText(l.code ?? 'INTERNAL_ERROR')}</td></tr>
          ))}</tbody>
        </table>
      )}
    </Section>
  )
}

function TradePassword({ user }: { user: User }) {
  const t = useT()
  const [open, setOpen] = useState(false)
  const [old, setOld] = useState('')
  const [pw, setPw] = useState('')
  const [err, setErr] = useState<{ f?: 'old' | 'new' | 'login' | 'code'; m: string }>()
  const [busy, setBusy] = useState(false)
  // 忘了二级密码（D107）：输一遍登录密码（开了二次验证的再输 6 位数）就能设新的，不用邮箱、不用命令。
  // 重置后所有设备退出、交易所账户暂停开新仓、发提醒。只绑了钱包的没有登录密码可验：先绑邮箱，或者用本机命令
  const [forgot, setForgot] = useState(false)
  const [login, setLogin] = useState('')
  const [code, setCode] = useState('')
  // 从风险页这类地方点"设置二级密码"过来（?page=account&sec=tradepw&back=risk）：直接打开设置框，设好回原来的页面
  // （11-16 走查：原来跳到账户页顶上，要自己再找按钮，设完还要自己回去再点一次）
  const back = useRef<string | null>(null)
  useEffect(() => {
    const q = new URLSearchParams(location.search)
    if (q.get('sec') !== 'tradepw') return
    back.current = q.get('back')
    document.querySelector('[data-sec="tradepw"]')?.scrollIntoView({ block: 'center' })
    if (!user.has_trade_password) setOpen(true)
  }, []) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { if (!open) { setOld(''); setPw(''); setErr(undefined); setForgot(false); setLogin(''); setCode('') } }, [open])
  const canReset = !!user.has_password && !!user.email
  const resetByLogin = async (e: FormEvent) => {
    e.preventDefault()
    if (!login) { setErr({ f: 'login', m: t('acct.tpLoginNeeded') }); return }
    if (user.two_fa && !code.trim()) { setErr({ f: 'code', m: errText('BAD_2FA') }); return }
    if ([...pw].length < 6) { setErr({ f: 'new', m: errText('WEAK_PASSWORD') }); return }
    setBusy(true)
    try {
      await api('POST', '/api/me/trade-password/reset', { password: login, code: code.trim(), new: pw })
      // 所有设备都退出了，包括这台：回登录页，说清楚接下来要做什么
      setOpen(false)
      await refreshSession()
      go('login', true)
      toast(t('acct.tpResetDone'))
    } catch (e2) {
      const c = e2 instanceof ApiError ? e2.code : ''
      setErr({ f: c === 'BAD_LOGIN' || c === 'LOCKED' ? 'login' : c === 'BAD_2FA' ? 'code' : c === 'WEAK_PASSWORD' || c === 'SAME_AS_LOGIN' ? 'new' : undefined,
        m: c === 'BAD_LOGIN' ? t('acct.tpBadLogin') : c === 'NEED_EMAIL' ? t('acct.tpForgotNoEmail') : errOf(e2) })
    } finally { setBusy(false) }
  }
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if ([...pw].length < 6) { setErr({ f: 'new', m: errText('WEAK_PASSWORD') }); return }
    setBusy(true)
    try {
      const r = await api<{ user: User }>('POST', '/api/me/trade-password', { old, new: pw })
      setUser(r.user)
      setOpen(false)
      toast(t('acct.tradePwSaved'))
      if (back.current) { const to = back.current; back.current = null; go(to) }
    } catch (e) {
      const c = e instanceof ApiError ? e.code : ''
      setErr({ f: c === 'BAD_TRADE_PASSWORD' || c === 'TRADE_PASSWORD_EMPTY' || c === 'LOCKED' ? 'old' : c === 'WEAK_PASSWORD' || c === 'SAME_AS_LOGIN' ? 'new' : undefined, m: errOf(e) })
    } finally {
      setBusy(false)
    }
  }
  return (
    <Section title={t('acct.tradePw')} hint={t('acct.tradePwHint')} sec="tradepw">
      <div className="ac-row single">
        <span className={`ac-tag ${user.has_trade_password ? 'ok' : 'warn'}`}><span className={`sq ${user.has_trade_password ? 'ok' : 'warn'}`} />{t(user.has_trade_password ? 'acct.tradePwSet' : 'acct.tradePwNotSet')}</span>
        <span className="ac-act"><button className={`btn ${user.has_trade_password ? 'ghost' : 'secondary'} sm`} onClick={() => setOpen(true)}>{t(user.has_trade_password ? 'acct.tradePwChangeBtn' : 'acct.tradePwSetBtn')}</button></span>
      </div>
      <Dialog open={open} title={t(forgot ? 'acct.tpForgotTitle' : user.has_trade_password ? 'acct.tradePwChangeBtn' : 'acct.tradePwSetBtn')} onClose={() => setOpen(false)}>
        {forgot ? (
          canReset ? (
            <form className="au-form" onSubmit={resetByLogin} noValidate data-tp-reset>
              <p className="dlg-text">{t('acct.tpResetBody')}</p>
              <Field label={t('acct.tpLoginPw')} type="password" reveal autoComplete="current-password" value={login} onChange={(v) => { setLogin(v); setErr(undefined) }} error={err?.f === 'login' ? err.m : undefined} autoFocus />
              {user.two_fa && <Field label={t('acct.tp2fa')} inputMode="numeric" autoComplete="one-time-code" value={code} onChange={(v) => { setCode(v.trim()); setErr(undefined) }} error={err?.f === 'code' ? err.m : undefined} />}
              <Field label={t('acct.tradePwNew')} type="password" reveal autoComplete="new-password" hint={t('acct.tradePwNewHint')} value={pw} onChange={(v) => { setPw(v); setErr(undefined) }} error={err?.f === 'new' ? err.m : undefined} />
              <FormError text={err && !err.f ? err.m : undefined} />
              <div className="dlg-btns">
                <button type="button" className="btn ghost" onClick={() => { setForgot(false); setErr(undefined) }}>{t('acct.tpBack')}</button>
                <button className="btn danger" disabled={busy} data-tp-reset-ok>{t('acct.tpResetBtn')}</button>
              </div>
            </form>
          ) : (
            <div className="au-form" data-tp-no-email>
              <p className="dlg-text">{t('acct.tpForgotNoEmail')}</p>
              <div className="dlg-btns"><button type="button" className="btn ghost" onClick={() => setForgot(false)}>{t('acct.tpBack')}</button></div>
            </div>
          )
        ) : (
        <form className="au-form" onSubmit={submit} noValidate>
          {user.has_trade_password && <Field label={t('acct.tradePwOld')} type="password" reveal autoComplete="off" value={old} onChange={setOld} error={err?.f === 'old' ? err.m : undefined} />}
          {user.has_trade_password && <button type="button" className="au-link tp-forgot" onClick={() => { setForgot(true); setErr(undefined) }} data-tp-forgot-link>{t('acct.tpForgotLink')}</button>}
          <Field label={t('acct.tradePwNew')} type="password" reveal autoComplete="new-password" hint={t('acct.tradePwNewHint')} value={pw} onChange={setPw} error={err?.f === 'new' ? err.m : undefined} />
          <FormError text={err && !err.f ? err.m : undefined} />
          <div className="dlg-btns">
            <button type="button" className="btn ghost" onClick={() => setOpen(false)}>{t('common.cancel')}</button>
            <button className="btn primary" disabled={busy}>{t('common.save')}</button>
          </div>
        </form>
        )}
      </Dialog>
    </Section>
  )
}

// ---------- 模拟账户 ----------

function Papers() {
  const t = useT()
  const [list, setList] = useState<PaperAccount[] | null>(null)
  const [max, setMax] = useState(10)
  const [err, setErr] = useState<string>()
  const [rename, setRename] = useState<PaperAccount | null>(null)
  const [reset, setReset] = useState<PaperAccount | null>(null)
  const [fresh, setFresh] = useState<number | null>(null) // 刚新建或重置的那一行，亮一下
  const [size, setSize] = useState('10000') // 新账户的初始资金：随便填，下面几个是快捷金额（D103，改 D75 第 8 条的三档）
  const [fees, setFees] = useState<PaperAccount | null>(null)
  const [funds, setFunds] = useState<PaperAccount | null>(null)
  const [del, setDel] = useState<PaperAccount | null>(null)

  const load = useCallback(async () => {
    try {
      const r = await api<{ accounts: PaperAccount[]; max: number }>('GET', '/api/paper-accounts')
      setList(r.accounts)
      setMax(r.max)
    } catch (e) {
      setErr(errOf(e))
    }
  }, [])
  useEffect(() => { void load() }, [load])

  const create = async () => {
    setErr(undefined)
    try {
      const a = await api<PaperAccount>('POST', '/api/paper-accounts', { name: '', initial: size.replace(/[,\s]/g, '') })
      setFresh(a.id)
      await load()
      toast(t('paper.created1', { name: acctName(a.name) }))
    } catch (e) {
      setErr(errOf(e))
    }
  }
  const full = !!list && list.length >= max
  const fmtDate = (s: string) => fmtTime(s, { year: true })

  return (
    <Section title={t('paper.title')} hint={t('paper.hint', { max })}
      aside={list && <span className="ac-count num">{t('paper.count', { n: list.length, max })}</span>}>
      <div className="ac-tbl-wrap">
        <table className="tbl ac-tbl" data-papers>
          <thead>
            <tr><th>{t('paper.name')}</th><th className="r">{t('paper.initial')}</th><th className="ac-opt">{t('paper.created')}</th><th /></tr>
          </thead>
          <tbody>
            {list?.map((a) => (
              <tr key={a.id} className={fresh === a.id ? 'fresh' : undefined}>
                <td>
                  <span className="ac-name">{acctName(a.name)}</span>
                  {/* 手续费写在名字下面：表在右栏很窄，单独一列会被收起来看不到（D103） */}
                  <span className="ac-fee num" data-fee={a.id}>
                    {a.taker_fee != null && t('paper.feeCell', { t: pct(a.taker_fee), m: pct(a.maker_fee) })}
                    {a.custom_fee && <span className="ac-fee-tag">{t('paper.feeCustom')}</span>}
                  </span>
                </td>
                <td className="num r" data-l={t('paper.initial')}><Money text={Number(a.initial).toLocaleString(numLocale(), { maximumFractionDigits: 2 })} unit="USDT" /></td>
                <td className="num t3 ac-opt">{fmtDate(a.created_at)}</td>
                <td className="ac-tbl-act">
                  <button className="btn ghost sm" onClick={() => setRename(a)}>{t('common.edit')}</button>
                  <button className="btn ghost sm" data-funds-edit={a.id} onClick={() => setFunds(a)}>{t('paper.fundsBtn')}</button>
                  <button className="btn ghost sm" data-fee-edit={a.id} onClick={() => setFees(a)}>{t('paper.fee')}</button>
                  <button className="btn ghost sm" onClick={() => setReset(a)}>{t('paper.reset')}</button>
                  {list.length > 1 && <button className="btn ghost sm" data-paper-del={a.id} onClick={() => setDel(a)}>{t('paper.del')}</button>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="ac-under">
        <label className="ac-amt">
          <span className="t2">{t('paper.size')}</span>
          <span className="fld-box ac-amt-box">
            <input className="num" inputMode="decimal" value={size} onChange={(e) => { setSize(numIn(e.target.value)); setErr(undefined) }}
              onKeyDown={(e) => { if (e.key === 'Enter') void create() }} data-paper-initial aria-label={t('paper.amt')} aria-describedby="ac-amt-h" />
            <em>USDT</em>
          </span>
          <span className="t3 ac-amt-h" id="ac-amt-h">{t('paper.amtHint')}</span>
        </label>
        <span className="ac-quick">
          {['1000', '10000', '100000', '1000000'].map((v) => (
            <button key={v} type="button" className={`btn ghost sm ${size === v ? 'on' : ''}`} data-quick={v} onClick={() => { setSize(v); setErr(undefined) }}>{Number(v).toLocaleString(numLocale())}</button>
          ))}
        </span>
        <button className="btn secondary sm" onClick={create} disabled={!list || full} title={full ? t('paper.full', { max }) : undefined}>+ {t('paper.new')}</button>
        {full && <span className="t3">{t('paper.full', { max })}</span>}
      </div>
      <FormError text={err} />

      {/* 删除（D108）：至少留一个；删的是正在用的那个时，自动换到第一个 */}
      <Confirm open={!!del} title={del ? t('paper.delTitle', { name: acctName(del.name) }) : ''} body={t('paper.delBody')} danger btn={t('paper.del')} onClose={() => setDel(null)}
        run={async () => {
          const a = del!
          await api('DELETE', `/api/paper-accounts/${a.id}`)
          await load()
          refreshPaperAccounts()
          toast(t('paper.deleted', { name: acctName(a.name) }))
        }} />
      <Dialog open={!!rename} title={t('paper.renameTitle')} onClose={() => setRename(null)}>
        {rename && <Rename acc={rename} onDone={async (n) => { setRename(null); await load(); toast(t('paper.renamed', { name: n })) }} onCancel={() => setRename(null)} />}
      </Dialog>
      <Dialog open={!!reset} title={reset ? t('paper.resetTitle', { name: acctName(reset.name) }) : ''} onClose={() => setReset(null)}>
        {reset && <Reset acc={reset} onDone={async (a) => { setReset(null); setFresh(a.id); await load(); toast(t('paper.resetDone', { name: acctName(a.name), amt: Number(a.initial).toLocaleString(numLocale()) })) }} onCancel={() => setReset(null)} />}
      </Dialog>
      <Dialog open={!!fees} title={fees ? t('paper.feeTitle', { name: acctName(fees.name) }) : ''} onClose={() => setFees(null)}>
        {fees && <Fees acc={fees} onDone={async () => { const n = acctName(fees.name); setFees(null); await load(); toast(t('paper.feeSaved', { name: n })) }} onCancel={() => setFees(null)} />}
      </Dialog>
      <Dialog open={!!funds} title={funds ? t('paper.fundsTitle', { name: acctName(funds.name) }) : ''} onClose={() => setFunds(null)}>
        {funds && <Funds acc={funds} onDone={async (p) => { const n = acctName(funds.name); setFunds(null); setFresh(funds.id); await load(); toast(t('paper.fundsDone', { name: n, p: Number(p).toLocaleString(numLocale(), { maximumFractionDigits: 2 }) })) }} onCancel={() => setFunds(null)} />}
      </Dialog>
    </Section>
  )
}

function Rename({ acc, onDone, onCancel }: { acc: PaperAccount; onDone: (name: string) => void; onCancel: () => void }) {
  const t = useT()
  const [name, setName] = useState(acc.name)
  const [err, setErr] = useState<string>()
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const n = name.trim()
    if (!n || [...n].length > 24) { setErr(t('paper.nameHint')); return }
    try {
      await api('PATCH', `/api/paper-accounts/${acc.id}`, { name: n })
      onDone(n)
    } catch (e) {
      setErr(errOf(e))
    }
  }
  return (
    <form className="au-form" onSubmit={submit} noValidate>
      <Field label={t('paper.name')} value={name} onChange={setName} hint={t('paper.nameHint')} error={err} maxLength={24} />
      <div className="dlg-btns">
        <button type="button" className="btn ghost" onClick={onCancel}>{t('common.cancel')}</button>
        <button className="btn primary">{t('common.save')}</button>
      </div>
    </form>
  )
}

/** 费率小数 → 百分比文字：0.0005 → 0.05% */
const pct = (s?: string) => `${locDec(String(+(Number(s ?? 0) * 100).toFixed(4)))}%`

/** 模拟账户的手续费（D103）：按百分比填，只影响以后的成交 */
function Fees({ acc, onDone, onCancel }: { acc: PaperAccount; onDone: () => void; onCancel: () => void }) {
  const t = useT()
  const [taker, setTaker] = useState(String(+(Number(acc.taker_fee ?? 0.0005) * 100).toFixed(4)))
  const [maker, setMaker] = useState(String(+(Number(acc.maker_fee ?? 0.0002) * 100).toFixed(4)))
  const [err, setErr] = useState<string>()
  const [busy, setBusy] = useState(false)
  const send = async (body: object) => {
    setBusy(true)
    try {
      await api('POST', `/api/trade/${acc.id}/fees`, body)
      onDone()
    } catch (e) {
      setErr(errOf(e))
    } finally {
      setBusy(false)
    }
  }
  const frac = (s: string) => {
    const n = Number(s.trim().replace('%', ''))
    return Number.isFinite(n) && s.trim() !== '' ? (n / 100).toFixed(6) : 'x'
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    const tk = frac(taker), mk = frac(maker)
    if (tk === 'x' || mk === 'x' || +tk < 0 || +mk < 0 || +tk > 0.01 || +mk > 0.01) { setErr(t('err.BAD_FEE')); return }
    void send({ taker: tk, maker: mk })
  }
  return (
    <form className="au-form" onSubmit={submit} noValidate data-fee-form>
      <p className="dlg-text">{t('paper.feeHint')}</p>
      <div className="ac-fee-row">
        <Field label={t('paper.feeTaker')} inputMode="decimal" value={taker} onChange={(v) => { setTaker(numIn(v)); setErr(undefined) }} data-fee-taker />
        <Field label={t('paper.feeMaker')} inputMode="decimal" value={maker} onChange={(v) => { setMaker(numIn(v)); setErr(undefined) }} data-fee-maker />
      </div>
      <FormError text={err} />
      <div className="dlg-btns">
        {acc.custom_fee && <button type="button" className="btn ghost dlg-left" disabled={busy} onClick={() => void send({ default: true })} data-fee-default>{t('paper.feeDefault')}</button>}
        <button type="button" className="btn ghost" onClick={onCancel}>{t('common.cancel')}</button>
        <button className="btn primary" disabled={busy}>{t('common.save')}</button>
      </div>
    </form>
  )
}

/** 金额框的文字 → 规整的数字串；不是大于 0 的数就是 null（模拟资金不限额度，D103） */
const amountOf = (s: string) => {
  const v = numIn(s)
  return /^\d+(\.\d+)?$/.test(v) && +v > 0 ? v : null
}

/** 重置：不要二级密码（D103，"不再限制额度和重置，完全自由"）；可以换金额，旧记录归档保留 */
function Reset({ acc, onDone, onCancel }: { acc: PaperAccount; onDone: (a: PaperAccount) => void; onCancel: () => void }) {
  const t = useT()
  const [amt, setAmt] = useState(String(Number(acc.initial)))
  const [err, setErr] = useState<string>()
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const v = amountOf(amt)
    if (!v) { setErr(t('err.BAD_INITIAL')); return }
    setBusy(true)
    try {
      onDone(await api<PaperAccount>('POST', `/api/paper-accounts/${acc.id}/reset`, { initial: v }))
    } catch (e) {
      setErr(errOf(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <form className="au-form" onSubmit={submit} noValidate>
      <p className="dlg-text">{t('paper.resetBody')}</p>
      <Field label={t('paper.amt')} hint={t('paper.amtHint')} inputMode="decimal" value={amt} onChange={(v) => { setAmt(v); setErr(undefined) }} error={err} data-reset-initial />
      <div className="dlg-btns">
        <button type="button" className="btn ghost" onClick={onCancel}>{t('common.cancel')}</button>
        <button className="btn danger" disabled={busy}>{t('paper.reset')}</button>
      </div>
    </form>
  )
}

/** 加减资金（D103）：不用重置，本金跟着变，加进来的钱不算盈利。减的上限和转去现货一样 */
function Funds({ acc, onDone, onCancel }: { acc: PaperAccount; onDone: (principal: string) => void; onCancel: () => void }) {
  const t = useT()
  const [dir, setDir] = useState<'add' | 'sub'>('add')
  const [amt, setAmt] = useState('')
  const [err, setErr] = useState<string>()
  const [busy, setBusy] = useState(false)
  const [now, setNow] = useState<{ initial: string; transferable: string } | null>(null)
  useEffect(() => {
    api<{ initial: string; transferable: string }>('GET', `/api/trade/${acc.id}`).then(setNow).catch(() => {})
  }, [acc.id])
  const fmt = (s?: string) => Number(s ?? 0).toLocaleString(numLocale(), { maximumFractionDigits: 2 })
  // 最多能拿走：可转出的部分，本金也要留一点（和后端同一个算法）
  const maxOut = now ? Math.max(0, Math.min(Number(now.transferable), Number(now.initial) - 0.01)) : 0
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const v = amountOf(amt)
    if (!v) { setErr(t('err.BAD_AMOUNT')); return }
    setBusy(true)
    try {
      const r = await api<{ initial: string }>('POST', `/api/trade/${acc.id}/funds`, { amount: dir === 'add' ? v : '-' + v })
      onDone(r.initial)
    } catch (e) {
      setErr(e instanceof ApiError ? errText(e.code, e.params) : errOf(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <form className="au-form" onSubmit={submit} noValidate data-funds-form>
      <p className="dlg-text">{t('paper.fundsBody')}</p>
      <Seg label={t('paper.funds')} value={dir} onChange={(d) => { setDir(d); setErr(undefined) }} options={[{ value: 'add', label: t('paper.fundsAdd') }, { value: 'sub', label: t('paper.fundsSub') }]} />
      <Field label={t('paper.fundsAmt')} inputMode="decimal" value={amt} onChange={(v) => { setAmt(v); setErr(undefined) }} error={err} data-funds-amt autoFocus
        hint={now ? t(dir === 'add' ? 'paper.fundsNowAdd' : 'paper.fundsNowSub', { p: fmt(now.initial), m: maxOut.toLocaleString(numLocale(), { maximumFractionDigits: 2 }) }) : ' ' /* 先占住这一行，读到数字时不跳 */} />
      <div className="dlg-btns">
        <button type="button" className="btn ghost" onClick={onCancel}>{t('common.cancel')}</button>
        <button className="btn primary" disabled={busy}>{t(dir === 'add' ? 'paper.fundsAddBtn' : 'paper.fundsSubBtn')}</button>
      </div>
    </form>
  )
}
