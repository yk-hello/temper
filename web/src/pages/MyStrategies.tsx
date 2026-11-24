// 我的策略（D96 第 2 条）：自己建 qsig/1 策略、拿密钥（只显示一次）、换密钥、吊销、下载前向记录。
// 我们不提供策略（D96 第 3 条）：这里只是把你自己的策略接进来的入口。
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { useT, errText, fmtTime } from '../lib/i18n'
import { api, ApiError, type User } from '../lib/session'
import { toast } from '../lib/privacy'
import { Field } from '../ui/Field'
import { Dialog } from '../ui/Dialog'

interface Item { strategy_id: string; name: string; key_id: string; created_at: string; rotated_at: string }
interface Mine { enabled: boolean; items: Item[]; max: number }
interface Secret { strategy_id: string; key_id: string; secret: string }
type SectionT = (p: { title: string; hint?: string; children: ReactNode }) => ReactNode

const codeOf = (e: unknown) => (e instanceof ApiError ? e.code : 'INTERNAL_ERROR')

export function MyStrategies({ user, Section }: { user: User; Section: SectionT }) {
  const t = useT()
  const [data, setData] = useState<Mine | null>(null)
  const [adding, setAdding] = useState(false)
  const [ask, setAsk] = useState<{ item: Item; kind: 'rotate' | 'revoke' } | null>(null)
  const [shown, setShown] = useState<Secret | null>(null)
  const load = () => { api<Mine>('GET', '/api/my-strategies').then(setData).catch(() => undefined) }
  useEffect(load, [])
  // 从别处点"去接入"过来（?page=account&sec=strategies）：滚到这一节
  const self = useRef<HTMLDivElement>(null)
  useEffect(() => { if (data && new URLSearchParams(location.search).get('sec') === 'strategies') self.current?.scrollIntoView({ block: 'start' }) }, [!!data]) // eslint-disable-line react-hooks/exhaustive-deps
  // 新建不要二级密码（D107：新策略没人跟随，碰不到钱）；换密钥、吊销要（换掉你正在跟随的策略的密钥，就能借它下单）
  const can = !!data?.enabled && user.has_trade_password
  return (
    <Section title={t('mys.title')} hint={t('mys.hint')}>
      <div ref={self} data-sec="strategies" />
      {data && !data.enabled && <p className="ac-sig-note" data-mys-off>{t('mys.off')}</p>}
      {data?.enabled && !user.has_trade_password && !!data.items.length && <p className="ac-sig-note">{t('mys.needTpRotate')}</p>}
      {!!data?.items.length && (
        <div className="ac-tbl-wrap">
          <table className="tbl ac-tbl adm-tbl" data-mys-list>
            <thead><tr><th>{t('mys.col.id')}</th><th className="ac-opt">{t('mys.col.key')}</th><th className="ac-opt">{t('mys.col.since')}</th><th /></tr></thead>
            <tbody>
              {data.items.map((x) => (
                <tr key={x.strategy_id} data-mys-row={x.strategy_id}>
                  <td><span className="ac-name num">{x.strategy_id}</span>{x.name && <small>{x.name}</small>}</td>
                  <td className="ac-opt t3 num">{x.key_id}</td>
                  <td className="ac-opt t3 num">{fmtTime(x.created_at)}</td>
                  <td className="ac-tbl-act">
                    <a className="btn ghost sm" href={`/api/strategies/${encodeURIComponent(x.strategy_id)}/log`} download data-mys-log>{t('mys.log')}</a>
                    <button className="btn ghost sm" disabled={!can} onClick={() => setAsk({ item: x, kind: 'rotate' })} data-mys-rotate>{t('mys.rotate')}</button>
                    <button className="btn ghost sm" disabled={!can} onClick={() => setAsk({ item: x, kind: 'revoke' })} data-mys-revoke>{t('mys.revoke')}</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {!!data?.items.length && <p className="ac-sig-note t3" data-mys-push>{t('mys.pushHint')}</p>}
      <button type="button" className="btn secondary sm" disabled={!data?.enabled} onClick={() => setAdding(true)} data-mys-add>+ {t('mys.add')}</button>
      <Dialog open={adding} title={t('mys.addTitle')} onClose={() => setAdding(false)}>
        {adding && <AddForm onCancel={() => setAdding(false)} onDone={(s) => { setAdding(false); setShown(s); load() }} />}
      </Dialog>
      <Dialog open={!!ask} title={ask ? t(ask.kind === 'rotate' ? 'mys.rotateTitle' : 'mys.revokeTitle', { id: ask.item.strategy_id }) : ''} onClose={() => setAsk(null)}>
        {ask && <AskForm kind={ask.kind} item={ask.item} onCancel={() => setAsk(null)} onDone={(s) => {
          setAsk(null)
          load()
          if (s) setShown(s)
          else toast(t('mys.revoked'))
        }} />}
      </Dialog>
      <Dialog open={!!shown} title={t('mys.secretTitle')} onClose={() => setShown(null)} width={560}>
        {shown && <SecretView s={shown} onClose={() => setShown(null)} />}
      </Dialog>
    </Section>
  )
}

function AddForm({ onCancel, onDone }: { onCancel: () => void; onDone: (s: Secret) => void }) {
  const t = useT()
  const [id, setId] = useState('')
  const [name, setName] = useState('')
  const [err, setErr] = useState<{ f: 'id' | 'other'; m: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr(null)
    try {
      onDone(await api<Secret>('POST', '/api/my-strategies', { strategy_id: id.trim().toLowerCase(), name: name.trim() }))
    } catch (e2) {
      const c = codeOf(e2)
      setErr({ f: c.startsWith('STRATEGY_') ? 'id' : 'other', m: errText(c) })
    } finally { setBusy(false) }
  }
  return (
    <form className="au-form" onSubmit={submit} noValidate data-mys-form>
      <Field label={t('mys.id')} value={id} onChange={(v) => setId(v.toLowerCase().replace(/[^a-z0-9-]/g, ''))} maxLength={40} hint={t('mys.idHint')} error={err?.f === 'id' ? err.m : undefined} autoComplete="off" />
      <Field label={t('mys.name')} value={name} onChange={setName} maxLength={40} autoComplete="off" error={err?.f === 'other' ? err.m : undefined} />
      <div className="dlg-btns">
        <button type="button" className="btn ghost" onClick={onCancel}>{t('common.cancel')}</button>
        <button className="btn primary" disabled={busy || id.length < 3} data-mys-ok>{t('mys.add')}</button>
      </div>
    </form>
  )
}

function AskForm({ kind, item, onCancel, onDone }: { kind: 'rotate' | 'revoke'; item: Item; onCancel: () => void; onDone: (s: Secret | null) => void }) {
  const t = useT()
  const [pw, setPw] = useState('')
  const [err, setErr] = useState<string>()
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr(undefined)
    try {
      const r = await api<Secret | { ok: boolean }>('POST', `/api/my-strategies/${encodeURIComponent(item.strategy_id)}/${kind}`, { trade_password: pw })
      onDone(kind === 'rotate' ? (r as Secret) : null)
    } catch (e2) { setErr(errText(codeOf(e2))) } finally { setBusy(false) }
  }
  return (
    <form className="au-form" onSubmit={submit} noValidate data-mys-ask>
      <p className={kind === 'revoke' ? 'cf-live' : 't2'}>{t(kind === 'rotate' ? 'mys.rotateBody' : 'mys.revokeBody')}</p>
      <Field label={t('acct.tradePw')} type="password" reveal value={pw} onChange={setPw} autoComplete="off" error={err} />
      <div className="dlg-btns">
        <button type="button" className="btn ghost" onClick={onCancel}>{t('common.cancel')}</button>
        <button className={`btn ${kind === 'revoke' ? 'danger' : 'primary'}`} disabled={busy} data-mys-ask-ok>{t(kind === 'rotate' ? 'mys.rotate' : 'mys.revoke')}</button>
      </div>
    </form>
  )
}

/** 密钥只显示这一次：写清楚、能复制、给一条能直接跑的命令 */
function SecretView({ s, onClose }: { s: Secret; onClose: () => void }) {
  const t = useT()
  const copy = (v: string) => { void navigator.clipboard?.writeText(v).then(() => toast(t('mys.copied')), () => undefined) }
  // 在项目文件夹里运行；Windows 用 PowerShell 的写法（11-16 走查：原来只有 macOS / Linux 的写法，Windows 上粘贴就报错，路径也对不上）
  const script = 'docs/examples/qsig_send.py BTC-PERP 0.1'
  const cmd = /Windows/.test(navigator.userAgent)
    ? `$env:QS_BASE="${location.origin}"; $env:QS_KEY="${s.key_id}"; $env:QS_SECRET="${s.secret}"; $env:QS_STRATEGY="${s.strategy_id}"; python ${script}`
    : `QS_BASE=${location.origin} QS_KEY=${s.key_id} QS_SECRET=${s.secret} QS_STRATEGY=${s.strategy_id} python3 ${script}`
  const row = (label: string, v: string, data: string) => (
    <div className="mys-kv"><span className="t3">{label}</span><code data-mys-secret={data}>{v}</code><button type="button" className="btn ghost sm" onClick={() => copy(v)}>{t('mys.copy')}</button></div>
  )
  return (
    <div className="au-form" data-mys-shown>
      <p className="cf-live">{t('mys.once')}</p>
      {row(t('mys.col.id'), s.strategy_id, 'strategy')}
      {row('key id', s.key_id, 'key')}
      {row('secret', s.secret, 'secret')}
      {row(t('mys.base'), location.origin, 'base')}
      <p className="t3 mys-cmd-l">{t('mys.try')}</p>
      <code className="mys-cmd">{cmd}</code>
      <div className="dlg-btns">
        <button type="button" className="btn ghost" onClick={() => copy(cmd)}>{t('mys.copyCmd')}</button>
        <button type="button" className="btn primary" onClick={onClose} data-mys-done>{t('mys.saved')}</button>
      </div>
    </div>
  )
}
