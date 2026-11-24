// 账户安全（18 第 6.2 条）：二次验证（验证器 + 恢复码）、登录着的设备、防钓鱼码。
import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from 'react'
import { renderSVG } from 'uqr'
import { useT, errText, fmtTime } from '../lib/i18n'
import { api, ApiError, refreshSession, type User } from '../lib/session'
import { toast } from '../lib/privacy'
import { Field } from '../ui/Field'
import { Dialog } from '../ui/Dialog'

interface Device { id: string; device: string; created: string; current: boolean }
interface Sec { two_fa: boolean; recovery_left: number; devices: Device[]; anti_phish: string }
type SectionT = (p: { title: string; hint?: string; sec?: string; children: ReactNode }) => ReactNode
const codeOf = (e: unknown) => (e instanceof ApiError ? e.code : 'INTERNAL_ERROR')

export function SecuritySection({ user, Section }: { user: User; Section: SectionT }) {
  const t = useT()
  const [sec, setSec] = useState<Sec | null>(null)
  const [setup, setSetup] = useState(false)
  const [off, setOff] = useState(false)
  const [phish, setPhish] = useState('')
  const load = () => { api<Sec>('GET', '/api/me/security').then((s) => { setSec(s); setPhish(s.anti_phish) }).catch(() => undefined) }
  useEffect(load, [user.two_fa])
  const revoke = async (id?: string) => {
    try {
      await api('POST', id ? `/api/me/devices/${id}/revoke` : '/api/me/devices/revoke-others')
      toast(t('scy.revoked'))
      load()
    } catch (e) { toast(errText(codeOf(e))) }
  }
  const savePhish = async () => {
    try { await api('POST', '/api/me/anti-phish', { code: phish.trim() }); toast(t(phish.trim() ? 'scy.phishSaved' : 'scy.phishCleared')); void refreshSession(); load() } catch (e) { toast(errText(codeOf(e))) }
  }
  return (
    <Section title={t('scy.title')} hint={t('scy.hint')} sec="security">
      <div className="ac-rows" data-security>
        <div className="ac-row" data-2fa={sec?.two_fa ? 'on' : 'off'}>
          <span className="ac-k">{t('scy.tfa')}</span>
          <span className="ac-v">
            <span className={`ac-tag ${sec?.two_fa ? 'ok' : 'warn'}`}><span className={`sq ${sec?.two_fa ? 'ok' : 'warn'}`} />{t(sec?.two_fa ? 'scy.tfaOn' : 'scy.tfaOff')}</span>
            {sec?.two_fa && <span className={`t3 sec-left ${sec.recovery_left <= 2 ? 'warn-t' : ''}`}>{t('scy.left', { n: sec.recovery_left })}</span>}
          </span>
          <span className="ac-act">
            {sec?.two_fa
              ? <button className="btn ghost sm" onClick={() => setOff(true)} data-2fa-off>{t('scy.tfaDisable')}</button>
              : <button className="btn secondary sm" onClick={() => setSetup(true)} data-2fa-setup>{t('scy.tfaEnable')}</button>}
          </span>
        </div>
        <div className="ac-row sec-phish">
          <span className="ac-k">{t('scy.phish')}</span>
          <span className="ac-v"><input className="nt-wh-in" value={phish} maxLength={20} onChange={(e) => setPhish(e.target.value)} placeholder={t('scy.phishPh')} aria-label={t('scy.phish')} data-phish /></span>
          <span className="ac-act"><button className="btn ghost sm" disabled={phish.trim() === (sec?.anti_phish ?? '')} onClick={() => void savePhish()} data-phish-save>{t('wh.save')}</button></span>
        </div>
      </div>
      {!!sec?.devices.length && (
        <div className="ac-tbl-wrap">
          <table className="tbl ac-tbl adm-tbl" data-devices>
            <thead><tr><th>{t('scy.device')}</th><th>{t('scy.since')}</th><th /></tr></thead>
            <tbody>
              {sec.devices.map((d) => (
                <tr key={d.id} data-device={d.current ? 'current' : d.id}>
                  <td><span className="ac-name">{d.device}</span>{d.current && <small>{t('scy.thisOne')}</small>}</td>
                  <td className="t3 num">{fmtTime(d.created)}</td>
                  <td className="ac-tbl-act">{!d.current && <button className="btn ghost sm" onClick={() => void revoke(d.id)}>{t('scy.signOut')}</button>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {(sec?.devices.length ?? 0) > 1 && <button type="button" className="btn ghost sm" onClick={() => void revoke()} data-revoke-others>{t('scy.signOutOthers')}</button>}
      <Dialog open={setup} title={t('scy.setupTitle')} onClose={() => setSetup(false)} width={520}>
        {setup && <Setup onCancel={() => setSetup(false)} onDone={() => { setSetup(false); void refreshSession(); load() }} />}
      </Dialog>
      <Dialog open={off} title={t('scy.tfaDisable')} onClose={() => setOff(false)}>
        {off && <Disable onCancel={() => setOff(false)} onDone={() => { setOff(false); toast(t('scy.tfaOffDone')); void refreshSession(); load() }} />}
      </Dialog>
    </Section>
  )
}

/** 设置：扫码（或手输密钥）→ 输验证器上的数 → 显示 8 个恢复码（只这一次） */
function Setup({ onDone, onCancel }: { onDone: () => void; onCancel: () => void }) {
  const t = useT()
  const [s, setS] = useState<{ uri: string; secret: string } | null>(null)
  const [code, setCode] = useState('')
  const [err, setErr] = useState<string>()
  const [codes, setCodes] = useState<string[] | null>(null)
  const [busy, setBusy] = useState(false)
  useEffect(() => { api<{ uri: string; secret: string }>('POST', '/api/me/2fa/setup').then(setS).catch((e) => setErr(errText(codeOf(e)))) }, [])
  const svg = useMemo(() => (s ? renderSVG(s.uri, { border: 2, whiteColor: '#ffffff', blackColor: '#000000' }) : ''), [s])
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr(undefined)
    try { setCodes((await api<{ recovery: string[] }>('POST', '/api/me/2fa/enable', { code: code.trim() })).recovery) } catch (e2) { setErr(errText(codeOf(e2))) } finally { setBusy(false) }
  }
  if (codes) {
    return (
      <div className="au-form" data-2fa-codes>
        <p className="cf-live">{t('scy.codesBody')}</p>
        <code className="mys-cmd sec-codes">{codes.join('\n')}</code>
        <div className="dlg-btns">
          <button type="button" className="btn ghost" onClick={() => void navigator.clipboard?.writeText(codes.join('\n')).then(() => toast(t('mys.copied')))}>{t('mys.copy')}</button>
          <button type="button" className="btn primary" onClick={onDone} data-2fa-done>{t('mys.saved')}</button>
        </div>
      </div>
    )
  }
  return (
    <form className="au-form" onSubmit={submit} noValidate data-2fa-form>
      <p className="t2">{t('scy.setupBody')}</p>
      {s && <div className="sec-qr" aria-label={t('scy.qr')} dangerouslySetInnerHTML={{ __html: svg }} />}
      {s && <div className="mys-kv"><span className="t3">{t('scy.key')}</span><code data-2fa-secret>{s.secret.replace(/(.{4})/g, '$1 ').trim()}</code><button type="button" className="btn ghost sm" onClick={() => void navigator.clipboard?.writeText(s.secret).then(() => toast(t('mys.copied')))}>{t('mys.copy')}</button></div>}
      <Field label={t('tfa.code6')} value={code} onChange={(v) => setCode(v.replace(/[^\d]/g, ''))} inputMode="numeric" autoComplete="one-time-code" maxLength={6} error={err} />
      <div className="dlg-btns"><button type="button" className="btn ghost" onClick={onCancel}>{t('common.cancel')}</button><button className="btn primary" disabled={busy || code.length !== 6} data-2fa-enable>{t('scy.tfaEnable')}</button></div>
    </form>
  )
}

function Disable({ onCancel, onDone }: { onCancel: () => void; onDone: () => void }) {
  const t = useT()
  const [code, setCode] = useState('')
  const [err, setErr] = useState<string>()
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr(undefined)
    try { await api('POST', '/api/me/2fa/disable', { code: code.trim() }); onDone() } catch (e2) { setErr(errText(codeOf(e2))) } finally { setBusy(false) }
  }
  return (
    <form className="au-form" onSubmit={submit} noValidate data-2fa-disable>
      <p className="cf-live">{t('scy.offBody')}</p>
      <Field label={t('tfa.code')} value={code} onChange={setCode} inputMode="numeric" autoComplete="one-time-code" maxLength={12} error={err} />
      <div className="dlg-btns">
        <button type="button" className="btn ghost" onClick={onCancel}>{t('common.cancel')}</button>
        <button className="btn danger" disabled={busy || code.trim().length < 6} data-2fa-off-ok>{t('scy.tfaDisable')}</button>
      </div>
    </form>
  )
}
