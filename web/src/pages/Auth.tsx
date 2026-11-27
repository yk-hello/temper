// 登录 / 注册（模块 1，验收 1.1–1.4、1.10）。
// 左边一块纯黑屏幕放实时行情（让人一眼看到"这里的数据是真的、在跳"），右边是表单。窄屏只留表单。
import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useT, useLang, setLang, errText } from '../lib/i18n'
import { BRAND } from '../lib/brand'
import { api, ApiError, setUser, useSession, type User } from '../lib/session'
import { markWizardPending } from '../lib/follow'
import { bootSearch, go, linkProps, setParam, takeBack } from '../lib/route'
import { useMarket } from '../lib/useMarket'
import { baseOf, fmtPct, fmtPrice, sign } from '../lib/format'
import { Field, FormError } from '../ui/Field'
import { Seg } from '../ui/Seg'
import { Price } from '../ui/Price'
import { isSpot } from '../lib/spot'
import { toast } from '../lib/privacy'
import '../styles/account.css'

type Mode = 'login' | 'register'
let reloadChecked = false // 这一页打开后只看一次"是不是刚刷新"

export default function Auth() {
  const t = useT()
  const lang = useLang()
  const s = useSession()
  const [mode, setMode] = useState<Mode>(() => (new URLSearchParams(location.search).get('mode') === 'register' ? 'register' : 'login'))
  const [verify, setVerify] = useState<{ email: string; dev?: string } | null>(null)
  const [tfa, setTfa] = useState<string | null>(null) // 二次验证：密码对了、还差验证码（18 第 6.2 条）
  const [forgot, setForgot] = useState(false) // 忘记密码（1.12，D100）
  // 注册关着时（"系统"页里关的，第一个账号以后默认关，D104），注册页直接说明，不让人白填
  const [regOpen, setRegOpen] = useState(true)
  useEffect(() => { api<{ open: boolean }>('GET', '/api/registration').then((r) => setRegOpen(r.open)).catch(() => undefined) }, [])
  // 没登录时在登录页按刷新：回到 3D 首页（用户 11-15："未登录账户刷新应该切回 3D 页面"）。
  // 只管刚刷新完的那一次；带参数的链接（注册、邮件里的重设密码链接）不算
  useEffect(() => {
    if (reloadChecked) return
    reloadChecked = true
    const nav = performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming | undefined
    // 只认"在登录页上按了刷新"：刷新的是别的页面、之后点过来的（比如刚退出登录）不算
    if (nav?.type === 'reload' && bootSearch === '?page=login' && location.search === '?page=login') go('', true)
  }, [])

  // 已经登录的人打开登录页：直接去账户页。在这个页面上刚登录、刚注册的不算（它们自己决定去哪）。
  const prev = useRef(s.state)
  useEffect(() => {
    if (s.state === 'in' && prev.current !== 'out' && prev.current !== 'in') go('account', true)
    prev.current = s.state
  }, [s.state])
  useEffect(() => { document.title = `${t(mode === 'login' ? 'auth.loginTitle' : 'auth.registerTitle')} · ${BRAND}` }, [mode, lang]) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="au-shell">
      <a className="au-mark mark" {...linkProps('')}>{BRAND}</a>
      <Tape />
      <main className="au-main">
        <div className="au-card" key={verify ? 'verify' : tfa ? 'tfa' : forgot ? 'forgot' : mode}>
          {verify ? (
            <Verify email={verify.email} dev={verify.dev} onDone={() => go(takeBack(), true)} />
          ) : forgot ? (
            <ForgotForm onBack={() => setForgot(false)} />
          ) : tfa ? (
            <TwoFAStep ticket={tfa} onBack={() => setTfa(null)} />
          ) : (
            <>
              <h1 className="au-title">{t(mode === 'login' ? 'auth.loginTitle' : 'auth.registerTitle')}</h1>
              <p className="au-tag">{t('auth.tagline')}</p>
              <Insecure />
              {s.state === 'unavailable' && <div className="note au-note">{t('auth.unavailable')}</div>}
              {mode === 'register' && !regOpen
                ? <div className="note au-note" data-reg-closed>{t('adm.regClosedNote')}</div>
                : <EmailForm mode={mode} onRegistered={(u, dev) => u.email && !u.email_verified ? setVerify({ email: u.email, dev }) : go(takeBack(), true)} onNeed2FA={setTfa} />}
              {mode === 'login' && (
                <p className="au-switch au-forgot"><button type="button" className="au-link" onClick={() => setForgot(true)} data-forgot>{t('auth.forgot')}</button></p>
              )}
              {(
                <p className="au-switch">
                  {t(mode === 'login' ? 'auth.toRegister' : 'auth.toLogin')}{' '}
                  <button type="button" className="au-link" onClick={() => { const m = mode === 'login' ? 'register' : 'login'; setMode(m); setParam('login', 'mode', m === 'register' ? 'register' : null) }}>
                    {t(mode === 'login' ? 'auth.toRegisterLink' : 'auth.toLoginLink')}
                  </button>
                </p>
              )}
            </>
          )}
        </div>
        <footer className="au-foot">
          <Seg label={t('bar.langTitle')} value={lang} onChange={(l) => setLang(l)} options={[{ value: 'zh', label: t('lang.zh') }, { value: 'en', label: t('lang.en') }]} />
          <span>{t('auth.paperNote')}</span>
        </footer>
      </main>
    </div>
  )
}

/** 忘记密码（1.12、D113）：输账户名，验证码发到这个账户绑定的邮箱；没绑邮箱、这台没配发信邮箱时用本机命令。
 *  账户名有没有注册、绑没绑邮箱，提示都一样（不暴露） */
// 连接没加密（用 http 打开、又不是本机）：同一网络里的人能看到输入的密码和验证码（11-25，用户："加一个吧"）。
// 本机 localhost、SSH 隧道、HTTPS 都算加密（浏览器自己的 isSecureContext）。
function Insecure() {
  const t = useT()
  if (typeof window === 'undefined' || window.isSecureContext) return null
  return <div className="note au-note" role="alert" data-insecure><b>{t('auth.insecure')}</b><span>{t('auth.insecureHint')}</span></div>
}

function ForgotForm({ onBack }: { onBack: () => void }) {
  const t = useT()
  const [mail, setMail] = useState<{ mail: boolean; dev: boolean } | null>(null)
  const [name, setName] = useState('')
  const [sent, setSent] = useState<{ dev?: string } | null>(null)
  const [code, setCode] = useState('')
  const [pw, setPw] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<{ field?: 'name' | 'code' | 'pw'; text: string } | null>(null)
  const [done, setDone] = useState(false)
  useEffect(() => { api<{ mail: boolean; dev: boolean }>('GET', '/api/auth/mail').then(setMail).catch(() => setMail({ mail: false, dev: false })) }, [])
  const off = mail && !mail.mail && !mail.dev
  const doSend = async (): Promise<boolean> => {
    if (!name.trim()) { setErr({ field: 'name', text: errText('BAD_NAME') }); return false }
    setBusy(true); setErr(null)
    try {
      const r = await api<{ dev_code?: string }>('POST', '/api/auth/forgot', { name })
      setSent({ dev: r.dev_code || undefined })
      return true
    } catch (e2) { setErr({ text: errText(e2 instanceof ApiError ? e2.code : 'INTERNAL_ERROR') }); return false } finally { setBusy(false) }
  }
  const send = (e: FormEvent) => { e.preventDefault(); void doSend() }
  const reset = async (e: FormEvent) => {
    e.preventDefault()
    if (!/^\d{6}$/.test(code.trim())) { setErr({ field: 'code', text: errText('BAD_CODE') }); return }
    if ([...pw].length < 10) { setErr({ field: 'pw', text: errText('WEAK_PASSWORD') }); return }
    setBusy(true); setErr(null)
    try {
      await api('POST', '/api/auth/reset', { name, code: code.trim(), password: pw })
      setDone(true)
    } catch (e2) {
      const c = e2 instanceof ApiError ? e2.code : 'INTERNAL_ERROR'
      setErr({ field: c === 'BAD_CODE' ? 'code' : c === 'WEAK_PASSWORD' || c === 'SAME_AS_LOGIN' ? 'pw' : undefined, text: errText(c) })
    } finally { setBusy(false) }
  }
  const cmd = (
    <div className="au-cmd-box" data-mail-off>
      <p className="au-tag">{t('auth.forgotCmd')}</p>
      <pre className="au-cmd"><code>{t('auth.forgotCmdLine')}</code></pre>
      <p className="au-tag">{t('auth.forgotCmd2')}</p>
    </div>
  )
  return (
    <div data-forgot-form>
      <h1 className="au-title">{t('auth.forgotTitle')}</h1>
      <Insecure />
      {done ? (
        <>
          <p className="au-tag" data-forgot-done>{t('auth.resetDone')}</p>
          <button type="button" className="btn primary au-submit" onClick={onBack}>{t('auth.toLoginLink')}</button>
        </>
      ) : off ? cmd : !sent ? (
        <form className="au-form" onSubmit={send} noValidate>
          <p className="au-tag">{t('auth.forgotHint')}</p>
          <Field label={t('auth.name')} autoComplete="username" autoCapitalize="none" placeholder={t('auth.namePh')} value={name} onChange={setName} error={err?.field === 'name' ? err.text : undefined} autoFocus data-forgot-name />
          <FormError text={err && !err.field ? err.text : undefined} />
          <button className="btn primary au-submit" disabled={busy}>{busy ? <span className="spin" aria-hidden /> : null}{t('auth.sendCode')}</button>
          <p className="au-tag au-tag-sm">{t('auth.forgotNoMail')}</p>
        </form>
      ) : (
        <form className="au-form" onSubmit={reset} noValidate>
          <p className="au-tag" data-forgot-sent>{t('auth.forgotSent')}</p>
          {sent.dev && <div className="note au-note"><b>{t('common.devMode')}</b><span>{t('auth.verifyDev', { code: sent.dev })}</span></div>}
          <Field label={t('auth.code')} className="au-code" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={(v) => setCode(v.replace(/[^\d]/g, ''))} error={err?.field === 'code' ? err.text : undefined} autoFocus />
          <Field label={t('auth.newPassword')} type="password" reveal autoComplete="new-password" value={pw} onChange={setPw} hint={t('auth.passwordHint')} error={err?.field === 'pw' ? err.text : undefined} />
          <FormError text={err && !err.field ? err.text : undefined} />
          <button className="btn primary au-submit" disabled={busy}>{busy ? <span className="spin" aria-hidden /> : null}{t('auth.resetBtn')}</button>
          <p className="au-switch au-resend">
            <button type="button" className="au-link" disabled={busy} onClick={() => { void doSend().then((sentOk) => { if (sentOk) toast(t('auth.resent')) }) }} data-forgot-resend>{t('auth.resendCode')}</button>
          </p>
        </form>
      )}
      {!done && <p className="au-switch"><button type="button" className="au-link" onClick={onBack}>{t('tfa.back')}</button></p>}
    </div>
  )
}

/** 登录第二步：验证器上的 6 位数，或者一个恢复码 */
function TwoFAStep({ ticket, onBack }: { ticket: string; onBack: () => void }) {
  const t = useT()
  const [code, setCode] = useState('')
  const [err, setErr] = useState<string>()
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr(undefined)
    try {
      const r = await api<{ user: User }>('POST', '/api/auth/2fa', { ticket, code: code.trim() })
      setUser(r.user)
      go(takeBack(), true)
    } catch (e2) {
      const c = e2 instanceof ApiError ? e2.code : 'INTERNAL_ERROR'
      if (c === '2FA_EXPIRED') { onBack(); return }
      setErr(errText(c))
    } finally { setBusy(false) }
  }
  return (
    <form className="au-form" onSubmit={submit} noValidate data-tfa-step>
      <h1 className="au-title">{t('tfa.title')}</h1>
      <p className="au-tag">{t('tfa.loginHint')}</p>
      <Field label={t('tfa.code')} value={code} onChange={setCode} inputMode="numeric" autoComplete="one-time-code" error={err} autoFocus maxLength={12} />
      <button className="btn primary au-submit" disabled={busy || code.trim().length < 6}>{busy ? <span className="spin" aria-hidden /> : null}{t('auth.loginBtn')}</button>
      <p className="au-switch"><button type="button" className="au-link" onClick={onBack}>{t('tfa.back')}</button></p>
    </form>
  )
}

function EmailForm({ mode, onRegistered, onNeed2FA }: { mode: Mode; onRegistered: (u: User, dev?: string) => void; onNeed2FA: (ticket: string) => void }) {
  const t = useT()
  const lang = useLang()
  const [email, setEmail] = useState('')
  const [pw, setPw] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<{ field?: 'email' | 'pw'; code: string } | null>(null)
  const tried = useRef(false)

  // 本地先检查（提交过一次以后，边输边提示）。账户名（D113）：2–64 个字，不能有空格
  const localErr = (): typeof err => {
    const n = [...email.trim()].length
    if (n < 2 || n > 64 || /\s/.test(email.trim())) return { field: 'email', code: 'BAD_NAME' }
    if (mode === 'register' && [...pw].length < 10) return { field: 'pw', code: 'WEAK_PASSWORD' }
    if (!pw) return { field: 'pw', code: 'BAD_LOGIN' }
    return null
  }
  useEffect(() => { if (tried.current) setErr(localErr()) }, [email, pw]) // eslint-disable-line react-hooks/exhaustive-deps

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    tried.current = true
    const le = localErr()
    if (le) { setErr(le); return }
    setBusy(true)
    setErr(null)
    try {
      const r = await api<{ user: User; dev_code?: string; need_2fa?: boolean; ticket?: string }>('POST', mode === 'login' ? '/api/auth/login' : '/api/auth/register', { name: email, password: pw, lang })
      if (r.need_2fa && r.ticket) { onNeed2FA(r.ticket); return }
      if (mode === 'register') { markWizardPending(); onRegistered(r.user, r.dev_code) }
      setUser(r.user)
      if (mode === 'login') go(takeBack(), true) // 回到去登录前的页面（没有就是行情页）
    } catch (e) {
      const code = e instanceof ApiError ? e.code : 'INTERNAL_ERROR'
      setErr({ field: code === 'BAD_NAME' || code === 'NAME_TAKEN' ? 'email' : code === 'WEAK_PASSWORD' ? 'pw' : undefined, code })
    } finally {
      setBusy(false)
    }
  }

  const fieldErr = (f: 'email' | 'pw') => (err?.field !== f ? undefined : errText(err.code))
  return (
    <form className="au-form" onSubmit={submit} noValidate>
      <Field label={t('auth.name')} autoComplete="username" autoCapitalize="none" placeholder={t('auth.namePh')} value={email} onChange={setEmail} error={fieldErr('email')} autoFocus
        hint={mode === 'register' ? t('auth.nameHint') : undefined} data-auth-name />
      <Field label={t('auth.password')} type="password" reveal autoComplete={mode === 'login' ? 'current-password' : 'new-password'} value={pw} onChange={setPw}
        hint={mode === 'register' ? t('auth.passwordHint') : undefined} error={fieldErr('pw')} />
      <FormError text={err && !err.field ? errText(err.code) : undefined} />
      <button className="btn primary au-submit" disabled={busy}>
        {busy ? <span className="spin" aria-hidden /> : null}
        {t(mode === 'login' ? 'auth.loginBtn' : 'auth.registerBtn')}
      </button>
    </form>
  )
}

/** 注册后的邮箱验证。也被账户页复用（绑定邮箱后）。 */
export function Verify({ email, dev, onDone, onLater }: { email: string; dev?: string; onDone: () => void; onLater?: () => void }) {
  const t = useT()
  const [code, setCode] = useState('')
  const [devCode, setDevCode] = useState(dev)
  const [err, setErr] = useState<string>()
  const [busy, setBusy] = useState(false)
  const [wait, setWait] = useState(60)
  useEffect(() => {
    if (wait <= 0) return
    const id = window.setTimeout(() => setWait(wait - 1), 1000)
    return () => window.clearTimeout(id)
  }, [wait])

  const submit = async (e?: FormEvent) => {
    e?.preventDefault()
    if (!/^\d{6}$/.test(code)) { setErr(errText('BAD_CODE')); return }
    setBusy(true)
    try {
      const r = await api<{ user: User }>('POST', '/api/auth/verify-email', { code })
      setUser(r.user)
      onDone()
    } catch (e) {
      setErr(errText(e instanceof ApiError ? e.code : 'INTERNAL_ERROR'))
    } finally {
      setBusy(false)
    }
  }
  // 输满 6 位自动提交
  useEffect(() => { if (code.length === 6) void submit() }, [code]) // eslint-disable-line react-hooks/exhaustive-deps

  const resend = async () => {
    setErr(undefined)
    try {
      const r = await api<{ dev_code?: string }>('POST', '/api/auth/resend-code')
      setDevCode(r.dev_code)
      setWait(60)
    } catch (e) {
      setErr(errText(e instanceof ApiError ? e.code : 'INTERNAL_ERROR'))
    }
  }
  return (
    <form className="au-form" onSubmit={submit} noValidate>
      <h1 className="au-title">{t('auth.verifyTitle')}</h1>
      <p className="au-tag">{t(devCode ? 'auth.verifyDevLead' : 'auth.verifySent', { email })}</p>
      {devCode && <div className="note au-note"><b>{t('common.devMode')}</b><span>{t('auth.verifyDev', { code: devCode })}</span></div>}
      <Field label={t('auth.code')} className="au-code" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code}
        onChange={(v) => { setErr(undefined); setCode(v.replace(/\D/g, '').slice(0, 6)) }} error={err} autoFocus />
      <button className="btn primary au-submit" disabled={busy}>{busy ? <span className="spin" aria-hidden /> : null}{t('auth.verifyBtn')}</button>
      <div className="au-row">
        <button type="button" className="au-link" onClick={resend} disabled={wait > 0}>{wait > 0 ? t('auth.resendIn', { s: wait }) : t('auth.resend')}</button>
        <button type="button" className="au-link dim" onClick={onLater ?? onDone}>{t('auth.later')}</button>
      </div>
    </form>
  )
}

/** 左边的实时行情带 */
function Tape() {
  const { snap, conn } = useMarket()
  const t = useT()
  // 标题写的是合约：现货（同一个币另一个价）不放进来，不然 BTC、DOGE 各出现两次、价格还不一样
  const list = (snap?.instruments ?? []).filter((i) => !isSpot(i.instrument))
  return (
    <aside className="au-tape display" aria-hidden="true">
      <div className="au-tape-head"><span className={`sq ${conn === 'open' ? 'ok' : ''}`} />{t(snap?.status?.name === 'sim' ? 'auth.tapeSim' : 'auth.tapeLive')}</div>
      <div className="au-tape-rows">
        {list.map((i) => (
          <div className="au-tape-row" key={i.instrument}>
            <span className="au-sym">{baseOf(i.instrument)}</span>
            <Price className="num au-px" text={fmtPrice(i.last)} />
            <span className={`num au-chg ${sign(i.change_24h_pct)}`}>{fmtPct(i.change_24h_pct)}</span>
          </div>
        ))}
      </div>
    </aside>
  )
}

