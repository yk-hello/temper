// "系统"页（以前叫管理后台）：全局停机、系统设置、注册方式、这台的账号、策略、操作记录。
// D104 独立使用：一台装的东西是一个人自己用的，登录的账号都能进，没有角色，谁也管不了别人的账号。
// 停机往上调不要二级密码（出事时要快），其余改动都要。没有收费、没有资金操作（D90）。
import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { useT, useLang, errText, fmtTime, type Key } from '../lib/i18n'
import { api, ApiError, useSession, type User } from '../lib/session'
import { go } from '../lib/route'
import { toast } from '../lib/privacy'
import { refreshSystem } from '../lib/system'
import { Bar } from '../ui/Bar'
import { Seg } from '../ui/Seg'
import { Field, FormError } from '../ui/Field'
import { Dialog } from '../ui/Dialog'
import { SysSettings } from './admin/SysSettings'
import '../styles/account.css'
import { BRAND } from '../lib/brand'

interface Member { id: number; email?: string; wallet?: string; created_at: string; last_seen?: string; accounts: number }
interface Halt { level: number; reason?: string; at?: number; by?: string }
interface Strat { strategy_id: string; online: boolean; last_heartbeat: number; halted?: string; halt_reason?: string; circuit_breaker: boolean; sample: boolean }
interface Entry { id: number; at: string; actor_email: string; action: string; target: string; detail: Record<string, unknown> }
interface Overview { me: { id: number }; users: Member[]; registration: 'open' | 'admin'; halt: Halt; strategies: Strat[]; log: Entry[] }

const codeOf = (e: unknown) => (e instanceof ApiError ? e.code : 'INTERNAL_ERROR')
const nameOf = (m: { name?: string; email?: string; wallet?: string }) => m.name || m.email || (m.wallet ? `${m.wallet.slice(0, 6)}…${m.wallet.slice(-4)}` : '')

/** 要二级密码的操作：一个对话框，写清楚会发生什么 */
interface Ask { title: string; body: string; danger?: boolean; ok: string; run: (pw: string) => Promise<unknown>; done: string; noPw?: boolean } // noPw：往安全的方向改（关注册），不要二级密码

export default function Admin() {
  const t = useT()
  const lang = useLang()
  const s = useSession()
  const [ov, setOv] = useState<Overview | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [ask, setAsk] = useState<Ask | null>(null)
  const [adding, setAdding] = useState(false)
  const [more, setMore] = useState<Entry[]>([])
  const [end, setEnd] = useState(false)
  const load = useCallback(() => {
    api<Overview>('GET', '/api/admin').then((r) => { setOv(r); setErr(null); setMore([]); setEnd(r.log.length < 50) }).catch((e) => setErr(codeOf(e)))
  }, [])
  useEffect(() => { if (s.state === 'out') go('login', true) }, [s.state])
  useEffect(() => { document.title = `${t('adm.title')} · ${BRAND}` }, [lang]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { if (s.state === 'in') load() }, [s.state, load])
  const after = (msg: string) => { toast(msg); load(); refreshSystem() }
  const user = s.state === 'in' ? s.user : null

  return (
    <div className="shell ac-shell">
      <Bar page="admin" />
      <main className="ac adm-page mk-scroll">
        <div className="ac-in" data-admin>
          <h1 className="ac-title">{t('adm.title')}</h1>
          {err && <p className="ac-sig-note" data-admin-forbidden={err === 'FORBIDDEN' ? '' : undefined}>{err === 'FORBIDDEN' ? t('adm.ownerOnly') : errText(err)}</p>}
          {!ov && !err && <p className="t3">{t('common.loading')}</p>}
          {ov && user && (
            <>
              {!user.has_trade_password && <p className="ac-sig-note">{t('adm.needTradePw')}</p>}
              <HaltSection ov={ov} setAsk={setAsk} after={after} />
              <SysSettings setAsk={setAsk} />
              <Section title={t('adm.reg')} hint={t('adm.regHint')}>
                <Seg label={t('adm.reg')} value={ov.registration} onChange={(m) => setAsk({
                  title: t('adm.regTitle'), body: t(m === 'admin' ? 'adm.regToAdmin' : 'adm.regToOpen'), ok: t('common.confirm'),
                  run: (pw) => api('POST', '/api/admin/registration', { mode: m, trade_password: pw }), done: t('adm.saved'), noPw: m === 'admin',
                })} options={[{ value: 'open', label: t('adm.regOpen') }, { value: 'admin', label: t('adm.regAdmin') }]} />
              </Section>
              <Users ov={ov} user={user} onAdd={() => setAdding(true)} />
              <Strategies list={ov.strategies} setAsk={setAsk} />
              <Section title={t('adm.log')} hint={t('adm.logHint')}>
                <Log list={[...ov.log, ...more]} />
                {!end && <button type="button" className="btn ghost sm" onClick={() => {
                  const all = [...ov.log, ...more]
                  api<{ log: Entry[] }>('GET', `/api/admin/log?before=${all[all.length - 1]?.id ?? 0}`).then((r) => { setMore([...more, ...r.log]); if (r.log.length < 100) setEnd(true) }).catch(() => undefined)
                }}>{t('adm.logMore')}</button>}
              </Section>
            </>
          )}
        </div>
      </main>
      <Dialog open={!!ask} title={ask?.title ?? ''} onClose={() => setAsk(null)}>
        {ask && <AskForm ask={ask} onCancel={() => setAsk(null)} onDone={() => { const m = ask.done; setAsk(null); after(m) }} />}
      </Dialog>
      <Dialog open={adding} title={t('adm.addTitle')} onClose={() => setAdding(false)}>
        {adding && <AddUser onCancel={() => setAdding(false)} onDone={(email) => { setAdding(false); after(t('adm.added', { email })) }} />}
      </Dialog>
    </div>
  )
}

function Section({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <section className="ac-sec">
      <div className="ac-sec-l"><h2>{title}</h2>{hint && <p>{hint}</p>}</div>
      <div className="ac-sec-r">{children}</div>
    </section>
  )
}

// ---------- 全局停机 ----------

function HaltSection({ ov, setAsk, after }: { ov: Overview; setAsk: (a: Ask) => void; after: (m: string) => void }) {
  const t = useT()
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const h = ov.halt
  // 往上调（停）不要二级密码：直接执行
  const stop = async (level: number) => {
    setBusy(true)
    try {
      const r = await api<{ cancelled: number }>('POST', '/api/admin/halt', { level, reason })
      setReason('')
      after(level === 2 ? t('adm.haltedCancel', { n: r.cancelled }) : t('adm.halted'))
    } catch (e) { toast(errText(codeOf(e))) } finally { setBusy(false) }
  }
  return (
    <Section title={t('adm.halt')} hint={t('adm.haltHint')}>
      <div className={`adm-halt lv${h.level}`} data-admin-halt={h.level}>
        <span className={`sq ${h.level ? 'warn' : 'ok'}`} />
        <span className="adm-halt-s">{t(`adm.haltLv${h.level}` as Key)}</span>
        {h.level > 0 && <span className="adm-halt-m">{[h.reason, h.by, h.at ? fmtTime(h.at) : ''].filter(Boolean).join(' · ')}</span>}
      </div>
      {h.level < 2 && (
        <>
          <Field label={t('adm.haltReason')} value={reason} onChange={setReason} maxLength={200} placeholder={t('adm.haltReasonPh')} className="adm-reason" />
          <div className="adm-btns">
            {h.level < 1 && <button type="button" className="btn danger" disabled={busy} onClick={() => void stop(1)} data-admin-halt1>{t('adm.halt1')}</button>}
            <button type="button" className="btn danger" disabled={busy} onClick={() => void stop(2)} data-admin-halt2>{t('adm.halt2')}</button>
          </div>
        </>
      )}
      {h.level > 0 && (
        <button type="button" className="btn secondary" data-admin-resume onClick={() => setAsk({
          title: t('adm.resumeTitle'), body: t('adm.resumeBody'), ok: t('adm.resume'),
          run: (pw) => api('POST', '/api/admin/halt', { level: 0, trade_password: pw }), done: t('adm.resumed'),
        })}>{t('adm.resume')}</button>
      )}
    </Section>
  )
}

// ---------- 这台的账号 ----------
// 只是列出来：没有角色，也不能动别人的账号（D104）。注册关着时在这里添加账号。

function Users({ ov, user, onAdd }: { ov: Overview; user: User; onAdd: () => void }) {
  const t = useT()
  return (
    <Section title={t('adm.users')} hint={t('adm.usersHint')}>
      <div className="ac-tbl-wrap">
        <table className="tbl ac-tbl adm-tbl" data-admin-users>
          <thead><tr><th>{t('adm.col.user')}</th><th className="ac-opt">{t('adm.col.seen')}</th><th className="ac-opt r">{t('adm.col.accts')}</th></tr></thead>
          <tbody>
            {ov.users.map((m) => (
              <tr key={m.id} data-admin-user={nameOf(m)}>
                <td><span className="ac-name">{nameOf(m)}</span>{m.id === user.id && <small>{t('adm.you')}</small>}</td>
                <td className="ac-opt t3 num">{m.last_seen ? fmtTime(m.last_seen) : t('adm.never')}</td>
                <td className="ac-opt r num">{m.accounts}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="adm-under">
        <button type="button" className="btn secondary sm" onClick={onAdd} data-admin-add>+ {t('adm.add')}</button>
      </div>
    </Section>
  )
}

function AddUser({ onCancel, onDone }: { onCancel: () => void; onDone: (email: string) => void }) {
  const t = useT()
  const lang = useLang()
  const [email, setEmail] = useState('')
  const [pass, setPass] = useState('')
  const [pw, setPw] = useState('')
  const [err, setErr] = useState<{ f: 'email' | 'pass' | 'pw'; m: string } | null>(null)
  const [busy, setBusy] = useState(false)
  // 生成一个初始密码：管理员线下告诉他，他登录后自己改
  const gen = () => {
    const a = 'abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789'
    const r = crypto.getRandomValues(new Uint32Array(14))
    setPass(Array.from(r, (x) => a[x % a.length]).join(''))
  }
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr(null)
    try {
      await api('POST', '/api/admin/users', { name: email.trim(), password: pass, lang, trade_password: pw })
      onDone(email.trim())
    } catch (e2) {
      const c = codeOf(e2)
      setErr({ f: c === 'BAD_NAME' || c === 'NAME_TAKEN' ? 'email' : c === 'WEAK_PASSWORD' ? 'pass' : 'pw', m: errText(c) })
    } finally { setBusy(false) }
  }
  return (
    <form className="au-form" onSubmit={submit} noValidate data-admin-add-form>
      <p className="t2">{t('adm.addBody')}</p>
      <Field label={t('auth.name')} value={email} onChange={setEmail} autoComplete="off" placeholder={t('auth.namePh')} error={err?.f === 'email' ? err.m : undefined} data-admin-add-name />
      <div className="adm-pass">
        <Field label={t('adm.initPass')} value={pass} onChange={setPass} autoComplete="off" hint={t('adm.initPassHint')} error={err?.f === 'pass' ? err.m : undefined} />
        <button type="button" className="btn ghost sm" onClick={gen} data-admin-gen>{t('adm.gen')}</button>
      </div>
      <Field label={t('acct.tradePw')} type="password" reveal value={pw} onChange={setPw} autoComplete="off" error={err?.f === 'pw' ? err.m : undefined} />
      <div className="dlg-btns">
        <button type="button" className="btn ghost" onClick={onCancel}>{t('common.cancel')}</button>
        <button className="btn primary" disabled={busy} data-admin-add-ok>{t('adm.add')}</button>
      </div>
    </form>
  )
}

/** 转让所有者：选一个人，你变成管理员 */
// ---------- 策略 ----------

function Strategies({ list, setAsk }: { list: Strat[]; setAsk: (a: Ask) => void }) {
  const t = useT()
  return (
    <Section title={t('adm.strats')} hint={t('adm.stratsHint')}>
      {list.length === 0 ? <p className="t3">{t('adm.noStrats')}</p> : (
        <div className="ac-tbl-wrap">
          <table className="tbl ac-tbl adm-tbl" data-admin-strats>
            <thead><tr><th>{t('adm.col.strat')}</th><th>{t('adm.col.state')}</th><th className="ac-opt">{t('adm.col.beat')}</th><th /></tr></thead>
            <tbody>
              {list.map((x) => {
                const st = x.halted ? 'halted' : x.circuit_breaker ? 'breaker' : x.online ? 'ok' : 'offline'
                return (
                  <tr key={x.strategy_id} data-admin-strat={x.strategy_id}>
                    <td><span className="ac-name num">{x.strategy_id}</span>{x.sample && <small>{t('adm.sample')}</small>}</td>
                    <td>
                      <span className="ac-tag" title={x.halt_reason || undefined}><span className={`sq ${st === 'ok' ? 'ok' : 'warn'}`} />{t(`adm.st.${st}` as Key)}</span>
                    </td>
                    <td className="ac-opt t3 num">{x.last_heartbeat ? fmtTime(x.last_heartbeat, { seconds: true }) : t('adm.noBeat') /* 重启时从最近 30 天的前向记录接上最后一次心跳；再早的不知道，不写"从未"（11-17 走查） */}</td>
                    <td className="ac-tbl-act">
                      {!x.sample && <button className="btn ghost sm" data-admin-strat-revoke onClick={() => setAsk({
                        title: t('adm.stratRevokeTitle'), body: t('adm.stratRevokeBody', { id: x.strategy_id }), ok: t('mys.revoke'), danger: true,
                        run: (pw) => api('POST', `/api/admin/strategies/${encodeURIComponent(x.strategy_id)}/revoke`, { trade_password: pw }), done: t('mys.revoked'),
                      })}>{t('mys.revoke')}</button>}
                      {x.halted && <button className="btn ghost sm" data-admin-strat-resume onClick={() => setAsk({
                        title: t('adm.stratResumeTitle'), body: t('adm.stratResumeBody', { id: x.strategy_id }), ok: t('adm.resume'),
                        run: (pw) => api('POST', `/api/admin/strategies/${encodeURIComponent(x.strategy_id)}/resume`, { trade_password: pw }), done: t('adm.saved'),
                      })}>{t('adm.resume')}</button>}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </Section>
  )
}

// ---------- 操作记录 ----------

function Log({ list }: { list: Entry[] }) {
  const t = useT()
  if (!list.length) return <p className="t3">{t('adm.logEmpty')}</p>
  const what = (e: Entry) => {
    const d = e.detail ?? {}
    switch (e.action) {
      case 'halt': return t(`adm.lg.halt${e.target}` as Key, { reason: d.reason ? `（${String(d.reason)}）` : '', n: String(d.cancelled ?? 0) })
      case 'role': return d.to === 'owner' ? t('adm.lg.owner', { who: e.target }) : t('adm.lg.role', { who: e.target, role: String(d.to) }) // 以前的记录（D104 之前有角色）
      case 'registration': return t(e.target === 'admin' ? 'adm.lg.regAdmin' : 'adm.lg.regOpen')
      case 'user_add': return t('adm.lg.add', { who: e.target })
      case 'user_disable': return t('adm.lg.disable', { who: e.target })
      case 'user_enable': return t('adm.lg.enable', { who: e.target })
      case 'strategy_resume': return t('adm.lg.stratResume', { id: e.target })
      case 'strategy_revoke': return t('adm.lg.stratRevoke', { id: e.target })
      case 'reset_2fa': return t('adm.lg.reset2fa', { who: e.target })
      case 'reset_trade_pw': return t('adm.lg.resetTp', { who: e.target })
      default: return `${e.action} ${e.target}`
    }
  }
  return (
    <div className="ac-tbl-wrap">
      <table className="tbl ac-tbl adm-tbl adm-log" data-admin-log>
        <thead><tr><th>{t('adm.col.time')}</th><th className="ac-opt">{t('adm.col.who')}</th><th>{t('adm.col.what')}</th></tr></thead>
        <tbody>
          {list.map((e) => (
            <tr key={e.id} data-admin-log-row={e.action}>
              <td className="t3 num adm-when">{fmtTime(e.at)}</td>
              <td className="ac-opt">{e.actor_email}</td>
              <td>{what(e)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function AskForm({ ask, onCancel, onDone }: { ask: Ask; onCancel: () => void; onDone: () => void }) {
  const t = useT()
  const [pw, setPw] = useState('')
  const [err, setErr] = useState<string>()
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr(undefined)
    try { await ask.run(pw); onDone() } catch (e2) { setErr(errText(codeOf(e2))) } finally { setBusy(false) }
  }
  return (
    <form className="au-form" onSubmit={submit} noValidate data-admin-ask>
      <p className={ask.danger ? 'cf-live' : 't2'}>{ask.body}</p>
      {ask.noPw ? <FormError text={err} /> : <Field label={t('acct.tradePw')} type="password" reveal value={pw} onChange={setPw} autoComplete="off" error={err} />}
      <div className="dlg-btns">
        <button type="button" className="btn ghost" onClick={onCancel}>{t('common.cancel')}</button>
        <button className={`btn ${ask.danger ? 'danger' : 'primary'}`} disabled={busy} data-admin-ask-ok>{ask.ok}</button>
      </div>
    </form>
  )
}
