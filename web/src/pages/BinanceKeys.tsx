// 和币安一样的接口（D96 第 1 条，18 第 5.3 条）：给现成的币安程序发一把模拟密钥，绑一个模拟账户。
// 程序把地址换成这里（/bapi），其余代码不用改；验证完把地址换回币安就是实盘。
import { useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { useT, errText, fmtTime, acctName } from '../lib/i18n'
import { api, ApiError, type User } from '../lib/session'
import { usePaperAccounts } from '../lib/trade'
import { toast } from '../lib/privacy'
import { Field, FormError } from '../ui/Field'
import { Dialog } from '../ui/Dialog'
import { Select } from '../ui/Select'

interface Item { api_key: string; account: number; label: string; created_at: string }
interface Created { api_key: string; secret: string; account: number }
type SectionT = (p: { title: string; hint?: string; children: ReactNode }) => ReactNode

const codeOf = (e: unknown) => (e instanceof ApiError ? e.code : 'INTERNAL_ERROR')
const short = (k: string) => `${k.slice(0, 8)}…${k.slice(-4)}`

export function BinanceKeys({ Section }: { user: User; Section: SectionT }) {
  const t = useT()
  const { list } = usePaperAccounts()
  const papers = (list ?? []).filter((a) => !a.live)
  const [data, setData] = useState<{ enabled: boolean; items: Item[] } | null>(null)
  const [adding, setAdding] = useState(false)
  const [revoking, setRevoking] = useState<Item | null>(null)
  const [shown, setShown] = useState<Created | null>(null)
  const load = () => { api<{ enabled: boolean; items: Item[] }>('GET', '/api/binapi-keys').then(setData).catch(() => undefined) }
  useEffect(load, [])
  const can = !!data?.enabled // 不要二级密码（D107）：这种密钥只能动模拟账户
  const nameOf = (id: number) => { const a = papers.find((x) => x.id === id); return a ? acctName(a.name) : `#${id}` }
  return (
    <Section title={t('bnk.title')} hint={t('bnk.hint')}>
      {data && !data.enabled && <p className="ac-sig-note">{t('mys.off')}</p>}
      {!!data?.items.length && (
        <div className="ac-tbl-wrap">
          <table className="tbl ac-tbl adm-tbl" data-bnk-list>
            <thead><tr><th>{t('bnk.col.key')}</th><th>{t('paper.title')}</th><th className="ac-opt">{t('mys.col.since')}</th><th /></tr></thead>
            <tbody>
              {data.items.map((x) => (
                <tr key={x.api_key} data-bnk-row={x.api_key}>
                  <td><span className="ac-name num">{short(x.api_key)}</span>{x.label && <small>{x.label}</small>}</td>
                  <td>{nameOf(x.account)}</td>
                  <td className="ac-opt t3 num">{fmtTime(x.created_at)}</td>
                  <td className="ac-tbl-act"><button className="btn ghost sm" disabled={!can} onClick={() => setRevoking(x)} data-bnk-revoke>{t('mys.revoke')}</button></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <button type="button" className="btn secondary sm" disabled={!can || !papers.length} onClick={() => setAdding(true)} data-bnk-add>+ {t('bnk.add')}</button>
      <Dialog open={adding} title={t('bnk.addTitle')} onClose={() => setAdding(false)}>
        {adding && <AddForm papers={papers.map((a) => ({ value: String(a.id), label: acctName(a.name) }))} onCancel={() => setAdding(false)} onDone={(c) => { setAdding(false); setShown(c); load() }} />}
      </Dialog>
      <Dialog open={!!revoking} title={t('bnk.revokeTitle')} onClose={() => setRevoking(null)}>
        {revoking && <RevokeForm item={revoking} onCancel={() => setRevoking(null)} onDone={() => { setRevoking(null); toast(t('mys.revoked')); load() }} />}
      </Dialog>
      <Dialog open={!!shown} title={t('bnk.secretTitle')} onClose={() => setShown(null)} width={600}>
        {shown && <Shown c={shown} onClose={() => setShown(null)} />}
      </Dialog>
    </Section>
  )
}

function AddForm({ papers, onCancel, onDone }: { papers: { value: string; label: string }[]; onCancel: () => void; onDone: (c: Created) => void }) {
  const t = useT()
  const [acct, setAcct] = useState(papers[0]?.value ?? '')
  const [label, setLabel] = useState('')
  const [err, setErr] = useState<string>()
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr(undefined)
    try { onDone(await api<Created>('POST', '/api/binapi-keys', { account: Number(acct), label: label.trim() })) } catch (e2) { setErr(errText(codeOf(e2))) } finally { setBusy(false) }
  }
  return (
    <form className="au-form" onSubmit={submit} noValidate data-bnk-form>
      <p className="t2">{t('bnk.addBody')}</p>
      <div className="fld"><span className="fld-l">{t('paper.title')}</span><Select label={t('paper.title')} value={acct} onChange={setAcct} options={papers} /></div>
      <Field label={t('mys.name')} value={label} onChange={setLabel} maxLength={40} autoComplete="off" error={err} />
      <div className="dlg-btns">
        <button type="button" className="btn ghost" onClick={onCancel}>{t('common.cancel')}</button>
        <button className="btn primary" disabled={busy || !acct} data-bnk-ok>{t('bnk.add')}</button>
      </div>
    </form>
  )
}

function RevokeForm({ item, onCancel, onDone }: { item: Item; onCancel: () => void; onDone: () => void }) {
  const t = useT()
  const [err, setErr] = useState<string>()
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr(undefined)
    try { await api('POST', `/api/binapi-keys/${item.api_key}/revoke`, {}); onDone() } catch (e2) { setErr(errText(codeOf(e2))) } finally { setBusy(false) }
  }
  return (
    <form className="au-form" onSubmit={submit} noValidate data-bnk-ask>
      <p className="cf-live">{t('bnk.revokeBody', { key: short(item.api_key) })}</p>
      <FormError text={err} />
      <div className="dlg-btns">
        <button type="button" className="btn ghost" onClick={onCancel}>{t('common.cancel')}</button>
        <button className="btn danger" disabled={busy} data-bnk-ask-ok>{t('mys.revoke')}</button>
      </div>
    </form>
  )
}

/** 只显示这一次：密钥、两个地址、一段 ccxt 的改法 */
function Shown({ c, onClose }: { c: Created; onClose: () => void }) {
  const t = useT()
  const rest = `${location.origin}/bapi`
  const ws = `${location.origin.replace(/^http/, 'ws')}/bapi`
  const copy = (v: string) => { void navigator.clipboard?.writeText(v).then(() => toast(t('mys.copied')), () => undefined) }
  const ccxt = [
    "import ccxt",
    `ex = ccxt.binanceusdm({"apiKey": "${c.api_key}", "secret": "${c.secret}"})`,
    `for k in ("fapiPublic", "fapiPublicV2", "fapiPublicV3", "fapiPrivate", "fapiPrivateV2", "fapiPrivateV3"):`,
    `    ex.urls["api"][k] = ex.urls["api"][k].replace("https://fapi.binance.com", "${rest}")`,
    `print(ex.fetch_balance()["USDT"])`,
  ].join('\n')
  const row = (label: string, v: string, data: string) => (
    <div className="mys-kv"><span className="t3">{label}</span><code data-bnk-secret={data}>{v}</code><button type="button" className="btn ghost sm" onClick={() => copy(v)}>{t('mys.copy')}</button></div>
  )
  return (
    <div className="au-form" data-bnk-shown>
      <p className="cf-live">{t('bnk.once')}</p>
      {row('API Key', c.api_key, 'key')}
      {row('Secret', c.secret, 'secret')}
      {row(t('bnk.rest'), rest, 'rest')}
      {row(t('bnk.ws'), ws, 'ws')}
      <p className="t3 mys-cmd-l">{t('bnk.ccxt')}</p>
      <code className="mys-cmd">{ccxt}</code>
      <div className="dlg-btns">
        <button type="button" className="btn ghost" onClick={() => copy(ccxt)}>{t('mys.copyCmd')}</button>
        <button type="button" className="btn primary" onClick={onClose} data-bnk-done>{t('mys.saved')}</button>
      </div>
    </div>
  )
}
