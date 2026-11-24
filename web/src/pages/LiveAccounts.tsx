// 交易所账户（D89）：用户接自己的币安 U 本位合约账户。钱一直在用户自己的交易所账户里，这里不经手（D90）。
// 保存密钥前后端先查权限：开了提现或划转的密钥直接拒绝；账户要是单向持仓。保存、开启下单、删除都要二级密码。
import { useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { useT, errText, fmtTime, type Key } from '../lib/i18n'
import { api, ApiError, type User } from '../lib/session'
import { refreshLive, useLive, type LiveAcct, type LiveEnv, type LiveKey, type LivePerms } from '../lib/live'
import { refreshPaperAccounts, selectAccount } from '../lib/trade'
import { go } from '../lib/route'
import { toast } from '../lib/privacy'
import { Dialog } from '../ui/Dialog'
import { Field, FormError } from '../ui/Field'
import { Seg } from '../ui/Seg'

type SectionC = (p: { title: string; hint?: string; aside?: ReactNode; children: ReactNode }) => JSX.Element
const codeOf = (e: unknown) => (e instanceof ApiError ? e.code : 'INTERNAL_ERROR')

export function LiveSection({ user, Section }: { user: User; Section: SectionC }) {
  const t = useT()
  const live = useLive(true)
  const [adding, setAdding] = useState(false)
  const [arming, setArming] = useState<LiveAcct | null>(null)
  const [stopping, setStopping] = useState<LiveAcct | null>(null)
  const [deleting, setDeleting] = useState<LiveKey | null>(null)
  const [noConfirm, setNoConfirm] = useState<LiveAcct | null>(null)
  const keyOf = (id: number) => live?.keys.find((k) => k.id === id)
  const done = async (msg: string) => { await refreshLive(); refreshPaperAccounts(); toast(msg) }
  const unused = live?.keys.filter((k) => !live.accounts.some((a) => a.key_id === k.id)) ?? []

  return (
    <Section title={t('live.title')} hint={t('live.hint')}>
      {live && !live.enabled && <p className="ac-sig-note" data-live-off>{t('live.disabled')}</p>}
      {!user.has_trade_password && live?.enabled && <p className="ac-sig-note">{t('live.needTradePw')}</p>}
      {!!live?.accounts.length && (
        <div className="ac-tbl-wrap">
          <table className="tbl ac-tbl lv-tbl" data-live-accounts>
            <thead><tr><th>{t('paper.name')}</th><th className="lv-envcol">{t('live.col.env')}</th><th className="ac-opt">{t('live.col.key')}</th><th>{t('live.col.state')}</th><th /></tr></thead>
            <tbody>
              {live.accounts.map((a) => {
                const k = keyOf(a.key_id)
                const st = a.status
                const ok = st.stream && !st.error
                return (
                  <tr key={a.id} data-live-row={a.id}>
                    <td><span className="ac-name">{a.name}</span><span className={`lv-env lv-sub ${a.env}`}>{t(`live.env.${a.env}` as Key)}</span></td>
                    <td className="lv-envcol"><span className={`lv-env ${a.env}`}>{t(`live.env.${a.env}` as Key)}</span></td>
                    <td className="num t3 ac-opt">{k?.hint}</td>
                    <td>
                      <span className={`ac-tag ${ok ? 'ok' : 'warn'}`} title={st.error ? errText(st.error) : st.why || undefined}>
                        <span className={`sq ${ok ? 'ok' : 'warn'}`} />{t(st.error ? 'live.st.error' : st.stream ? 'live.st.ok' : 'live.st.down')}
                      </span>
                      <span className={`lv-armed ${a.armed ? 'on' : ''}`}>{t(a.armed ? 'live.armed' : 'live.notArmedS')}</span>
                    </td>
                    <td className="ac-tbl-act">
                      {!a.armed && <button className="btn secondary sm" data-live-arm-btn disabled={!user.has_trade_password} onClick={() => setArming(a)}>{t('live.arm')}</button>}
                      <button className="btn ghost sm" onClick={() => { selectAccount(a.id); go('trade') }}>{t('live.trade')}</button>
                      <button className="btn ghost sm" data-live-confirm-btn={a.confirm ? 'on' : 'off'} title={t(a.confirm ? 'live.confirmIsOn' : 'live.confirmIsOff')} onClick={() => (a.confirm ? setNoConfirm(a) : void api('POST', `/api/live/accounts/${a.id}/confirm`, { on: true }).then(() => done(t('live.confirmBack'))))}>{t(a.confirm ? 'live.confirmOffBtn' : 'live.confirmOnBtn')}</button>
                      <button className="btn ghost sm" onClick={() => setStopping(a)}>{t('live.stop')}</button>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      {!!unused.length && (
        <div className="lv-keys">
          {unused.map((k) => (
            <div key={k.id} className="ac-row single lv-key">
              <span><span className="ac-name">{k.label}</span> <span className="num t3">{k.hint}</span> <span className={`lv-env ${k.env}`}>{t(`live.env.${k.env}` as Key)}</span></span>
              <span className="ac-act"><button className="btn ghost sm" onClick={() => setDeleting(k)}>{t('live.delKey')}</button></span>
            </div>
          ))}
        </div>
      )}
      <div className="ac-under">
        <button className="btn secondary sm" data-live-add disabled={!live?.enabled || !user.has_trade_password} onClick={() => setAdding(true)}>+ {t('live.add')}</button>
        {live?.enabled && !!live.accounts.length && <span className="t3">{t('live.underNote')}</span>}
      </div>

      <Dialog open={adding} title={t('live.addTitle')} onClose={() => setAdding(false)}>
        {adding && live && <AddKey envs={live.envs} twoFA={!!user.two_fa} onDone={async (name) => { setAdding(false); await done(t('live.added', { name })) }} onCancel={() => setAdding(false)} />}
      </Dialog>
      <Dialog open={!!arming} title={t('live.armTitle')} onClose={() => setArming(null)}>
        {arming && <Arm acct={arming} onDone={async () => { setArming(null); await done(t('live.armedDone', { name: arming.name })) }} onCancel={() => setArming(null)} />}
      </Dialog>
      <Dialog open={!!stopping} title={t('live.stopTitle', { name: stopping?.name ?? '' })} onClose={() => setStopping(null)}>
        {stopping && <Stop acct={stopping} keyLabel={keyOf(stopping.key_id)} onDone={async () => { setStopping(null); await done(t('live.stopped')) }} onCancel={() => setStopping(null)} />}
      </Dialog>
      <Dialog open={!!noConfirm} title={t('live.confirmOffTitle')} onClose={() => setNoConfirm(null)}>
        {noConfirm && <NoConfirm acct={noConfirm} onDone={async () => { setNoConfirm(null); await done(t('live.confirmOffDone')) }} onCancel={() => setNoConfirm(null)} />}
      </Dialog>
      <Dialog open={!!deleting} title={t('live.delKeyTitle')} onClose={() => setDeleting(null)}>
        {deleting && <DeleteKey k={deleting} onDone={async () => { setDeleting(null); await done(t('live.keyDeleted')) }} onCancel={() => setDeleting(null)} />}
      </Dialog>
    </Section>
  )
}

/** 接一把币安密钥：先写清楚该怎么建（只开合约、不开提现和划转），后端查过权限才保存，再建一个实盘账户。 */
function AddKey({ envs, twoFA, onDone, onCancel }: { envs: LiveEnv[]; twoFA: boolean; onDone: (name: string) => void; onCancel: () => void }) {
  const t = useT()
  const [env, setEnv] = useState<LiveEnv>(envs.includes('main') ? 'main' : envs[0])
  const [label, setLabel] = useState('')
  const [key, setKey] = useState('')
  const [secret, setSecret] = useState('')
  const [pw, setPw] = useState('')
  const [err, setErr] = useState<{ f?: 'key' | 'pw'; m: string }>()
  const [perms, setPerms] = useState<LivePerms | null>(null)
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (key.trim().length < 16 || secret.trim().length < 16) { setErr({ f: 'key', m: t('live.keyShort') }); return }
    setBusy(true); setErr(undefined); setPerms(null)
    try {
      const name = label.trim() || t('live.defaultName')
      const r = await api<{ key: LiveKey }>('POST', '/api/live/keys', { env, label: name, key: key.trim(), secret: secret.trim(), trade_password: pw })
      await api('POST', '/api/live/accounts', { key_id: r.key.id, name })
      onDone(name)
    } catch (e2) {
      const c = codeOf(e2)
      if (e2 instanceof ApiError && e2.body && typeof e2.body === 'object' && 'perms' in e2.body) setPerms((e2.body as { perms: LivePerms }).perms)
      setErr({ f: c === 'BAD_TRADE_PASSWORD' || c === 'TRADE_PASSWORD_EMPTY' || c === 'NEED_TRADE_PASSWORD' || c === 'LOCKED' ? 'pw' : c === 'KEY_REJECTED' ? 'key' : undefined, m: errText(c) })
    } finally { setBusy(false) }
  }
  return (
    <form className="au-form lv-form" onSubmit={submit} noValidate data-live-form>
      <ol className="lv-steps">
        <li>{t('live.step1')}</li>
        <li>{t('live.step2')}</li>
        <li>{t('live.step3')}</li>
      </ol>
      {envs.length > 1 && <Seg label={t('live.col.env')} value={env} onChange={setEnv} options={envs.map((x) => ({ value: x, label: t(`live.env.${x}` as Key) }))} />}
      {env === 'testnet' && <p className="lv-note">{t('live.testnetNote')}</p>}
      {/* 接真钱前提示开二次验证（D107）：二级密码现在输登录密码就能重置，知道你登录密码的人挡不住，挡他的是二次验证 */}
      {env === 'main' && !twoFA && (
        <p className="lv-note" data-live-2fa-note>{t('live.mainNo2fa')} <button type="button" className="au-link" onClick={() => { onCancel(); document.querySelector('[data-sec="security"]')?.scrollIntoView({ block: 'center' }) }}>{t('live.go2fa')}</button></p>
      )}
      <Field label={t('live.label')} value={label} onChange={setLabel} maxLength={24} placeholder={t('live.defaultName')} />
      <Field label="API Key" value={key} onChange={setKey} autoComplete="off" error={err?.f === 'key' ? err.m : undefined} data-live-key />
      <Field label="Secret Key" type="password" reveal value={secret} onChange={setSecret} autoComplete="off" data-live-secret />
      <Field label={t('acct.tradePw')} type="password" reveal value={pw} onChange={setPw} autoComplete="off" error={err?.f === 'pw' ? err.m : undefined} data-live-pw />
      <FormError text={err && !err.f ? err.m : undefined} />
      {perms && (
        <dl className="lv-perms" data-live-perms>
          <div><dt>{t('live.p.futures')}</dt><dd className={perms.futures ? 'up' : 'down'}>{t(perms.futures ? 'live.p.on' : 'live.p.off')}</dd></div>
          <div><dt>{t('live.p.withdraw')}</dt><dd className={perms.withdraw ? 'down' : 'up'}>{t(perms.withdraw ? 'live.p.on' : 'live.p.off')}</dd></div>
          <div><dt>{t('live.p.transfer')}</dt><dd className={perms.transfer || perms.universal_transfer ? 'down' : 'up'}>{t(perms.transfer || perms.universal_transfer ? 'live.p.on' : 'live.p.off')}</dd></div>
          <div><dt>{t('live.p.mode')}</dt><dd className={perms.dual_side ? 'down' : 'up'}>{t(perms.dual_side ? 'live.p.dual' : 'live.p.oneWay')}</dd></div>
        </dl>
      )}
      <p className="lv-note t3">{t('live.storeNote')}</p>
      <div className="dlg-btns">
        <button type="button" className="btn ghost" onClick={onCancel}>{t('common.cancel')}</button>
        <button className="btn primary" disabled={busy} data-live-save>{busy && <span className="spin" aria-hidden />}{t('live.save')}</button>
      </div>
    </form>
  )
}

/** 开启下单：写清楚这是真钱、下单会真的成交；要二级密码。 */
function Arm({ acct, onDone, onCancel }: { acct: LiveAcct; onDone: () => void; onCancel: () => void }) {
  const t = useT()
  const [pw, setPw] = useState('')
  const [err, setErr] = useState<string>()
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr(undefined)
    try { await api('POST', `/api/live/accounts/${acct.id}/arm`, { trade_password: pw }); onDone() } catch (e2) { setErr(errText(codeOf(e2))) } finally { setBusy(false) }
  }
  return (
    <form className="au-form" onSubmit={submit} noValidate data-live-arm-form>
      <p className="cf-live">{t(acct.env === 'main' ? 'live.armBody' : 'live.armBodyTest', { name: acct.name, env: t(acct.env === 'fake' ? 'live.env.fake' : 'live.env.testnet') })}</p>
      <ul className="lv-list">
        <li>{t('live.armP1')}</li>
        <li>{t('live.armP2')}</li>
        <li>{t('live.armP3')}</li>
      </ul>
      <Field label={t('acct.tradePw')} type="password" reveal value={pw} onChange={setPw} autoComplete="off" error={err} />
      <div className="dlg-btns">
        <button type="button" className="btn ghost" onClick={onCancel}>{t('common.cancel')}</button>
        <button className="btn primary" disabled={busy} data-live-arm-ok>{t('live.armOk')}</button>
      </div>
    </form>
  )
}

/** 停用：这里不再管这个账户；交易所上的仓位和委托不动。 */
function Stop({ acct, keyLabel, onDone, onCancel }: { acct: LiveAcct; keyLabel?: LiveKey; onDone: () => void; onCancel: () => void }) {
  const t = useT()
  const [err, setErr] = useState<string>()
  const [busy, setBusy] = useState(false)
  const run = async () => {
    setBusy(true)
    try { await api('DELETE', `/api/live/accounts/${acct.id}`); onDone() } catch (e) { setErr(errText(codeOf(e))) } finally { setBusy(false) }
  }
  return (
    <div className="au-form">
      <p>{t('live.stopBody')}</p>
      {keyLabel && <p className="t3">{t('live.stopKey', { hint: keyLabel.hint })}</p>}
      <FormError text={err} />
      <div className="dlg-btns">
        <button type="button" className="btn ghost" onClick={onCancel}>{t('common.cancel')}</button>
        <button className="btn danger" disabled={busy} onClick={() => void run()} data-live-stop-ok>{t('live.stop')}</button>
      </div>
    </div>
  )
}

/** 关掉实盘下单确认：要二级密码（D96） */
function NoConfirm({ acct, onDone, onCancel }: { acct: LiveAcct; onDone: () => void; onCancel: () => void }) {
  const t = useT()
  const [pw, setPw] = useState('')
  const [err, setErr] = useState<string>()
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr(undefined)
    try { await api('POST', `/api/live/accounts/${acct.id}/confirm`, { on: false, trade_password: pw }); onDone() } catch (e2) { setErr(errText(codeOf(e2))) } finally { setBusy(false) }
  }
  return (
    <form className="au-form" onSubmit={submit} noValidate data-live-noconfirm>
      <p className="cf-live">{t('live.confirmOffBody', { name: acct.name })}</p>
      <Field label={t('acct.tradePw')} type="password" reveal value={pw} onChange={setPw} autoComplete="off" error={err} />
      <div className="dlg-btns">
        <button type="button" className="btn ghost" onClick={onCancel}>{t('common.cancel')}</button>
        <button className="btn danger" disabled={busy} data-live-noconfirm-ok>{t('live.confirmOffBtn')}</button>
      </div>
    </form>
  )
}

function DeleteKey({ k, onDone, onCancel }: { k: LiveKey; onDone: () => void; onCancel: () => void }) {
  const t = useT()
  const [pw, setPw] = useState('')
  const [err, setErr] = useState<string>()
  const [busy, setBusy] = useState(false)
  useEffect(() => setErr(undefined), [pw])
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    try { await api('DELETE', `/api/live/keys/${k.id}`, { trade_password: pw }); onDone() } catch (e2) { setErr(errText(codeOf(e2))) } finally { setBusy(false) }
  }
  return (
    <form className="au-form" onSubmit={submit} noValidate>
      <p>{t('live.delKeyBody', { label: k.label, hint: k.hint, date: fmtTime(k.created, { year: true }) })}</p>
      <Field label={t('acct.tradePw')} type="password" reveal value={pw} onChange={setPw} autoComplete="off" error={err} />
      <div className="dlg-btns">
        <button type="button" className="btn ghost" onClick={onCancel}>{t('common.cancel')}</button>
        <button className="btn danger" disabled={busy}>{t('live.delKey')}</button>
      </div>
    </form>
  )
}
