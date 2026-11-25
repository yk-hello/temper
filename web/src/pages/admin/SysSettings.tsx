// 系统设置（2026-11-15，用户："别人用不能也要当场写吧？""别人不能全靠AI配置吧，比如其他的AI复盘API填写"）：
// 部署的人在这里填发信邮箱、AI 复盘点评、Telegram、数据源密钥，不用改配置文件、不用重启。只有所有者能看。
// 密钥加密存，这里只显示头尾几位；留空 = 不改。每一组有"测试"，填错了当场说清楚哪里不对。
import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { useT, errText, type Key } from '../../lib/i18n'
import { api, ApiError } from '../../lib/session'
import { BRAND } from '../../lib/brand'
import { Field } from '../../ui/Field'
import { Select } from '../../ui/Select'
import { Seg } from '../../ui/Seg'

interface SField { key: string; group: string; env: string; secret: boolean; source: '' | 'web' | 'env'; value: string }
interface Resp { fields: SField[]; crypt: boolean }
type Ask = { title: string; body: string; ok: string; run: (pw: string) => Promise<unknown>; done: string }

// 没有 Outlook：微软从 2024-09-16 起个人 Outlook 邮箱只认 OAuth2 登录，授权码发不了信；Microsoft 365 从 2026 年底起默认也关掉
// （11-25 又查了一遍；Gmail、QQ、163 的发信服务器都还接受授权码登录）。"其他"里填了微软的服务器直接标出来（后端 mailer.Microsoft 也拦）。
const msHost = (h: string) => /(^|\.)(outlook|office365|hotmail|live)\.com\.?$/i.test(h.trim())
const MAIL_PRESETS: { id: string; host: string; port: string; help?: string }[] = [
  { id: 'gmail', host: 'smtp.gmail.com', port: '465', help: 'https://myaccount.google.com/apppasswords' },
  { id: 'qq', host: 'smtp.qq.com', port: '465', help: 'https://mail.qq.com' },
  { id: '163', host: 'smtp.163.com', port: '465', help: 'https://mail.163.com' },
  { id: 'custom', host: '', port: '465' },
]

export function SysSettings({ setAsk }: { setAsk: (a: Ask) => void }) {
  const t = useT()
  const [d, setD] = useState<Resp | null>(null)
  const [err, setErr] = useState<string>()
  const load = useCallback(() => { api<Resp>('GET', '/api/admin/settings').then((r) => { setD(r); setErr(undefined) }).catch((e) => setErr(e instanceof ApiError ? e.code : 'INTERNAL_ERROR')) }, [])
  useEffect(load, [load])
  if (err === 'FORBIDDEN') return null // 只有所有者能看
  if (!d) return err ? <p className="ac-sig-note">{errText(err)}</p> : null
  const f = (k: string) => d.fields.find((x) => x.key === k)!
  const save = (title: string, values: Record<string, string>, clear: string[] = []) => setAsk({
    title, body: t(clear.length ? 'sys.clearBody' : 'sys.saveBody'), ok: t(clear.length ? 'sys.clear' : 'common.save'),
    run: (pw) => api('POST', '/api/admin/settings', { values, clear, trade_password: pw }).then(load), done: t('sys.saved'),
  })
  return (
    <>
      {!d.crypt && <p className="ac-sig-note" data-sys-nokey>{t('sys.noMaster')}</p>}
      <MailGroup f={f} save={save} />
      <AiGroup setAsk={setAsk} />
      <TgGroup f={f} save={save} />
      <DataGroup f={f} save={save} />
    </>
  )
}

type GroupProps = { f: (k: string) => SField; save: (title: string, values: Record<string, string>, clear?: string[]) => void }

const DEFAULTED = new Set(['mail.port', 'mail.from_name'])

/** 一组的外框：标题、说明、现在用的是哪里的设置、测试按钮 */
function Group({ id, title, hint, src, children, onSave, onClear }: { id: string; title: string; hint: ReactNode; src: SField[]; children: ReactNode; onSave: () => void; onClear?: () => void }) {
  const t = useT()
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null)
  const [busy, setBusy] = useState(false)
  // 配置文件里"配过"只看真正要填的项（地址、账号、密钥）：端口、模型名这些部署配置自带默认值，
  // 不能因为它们就写"现在用的是配置文件里的设置"（11-16 走查：什么都没配的干净部署，发信邮箱和 AI 两组都这么写）
  const web = src.some((x) => x.source === 'web'), env = src.some((x) => x.source === 'env' && !DEFAULTED.has(x.key))
  const test = async () => {
    setBusy(true); setMsg(null)
    try { const r = await api<{ ok: boolean; message: string }>('POST', '/api/admin/settings/test', { group: id }); setMsg({ ok: r.ok, text: r.message }) } catch (e) { setMsg({ ok: false, text: errText(e instanceof ApiError ? e.code : 'INTERNAL_ERROR') }) } finally { setBusy(false) }
  }
  return (
    <section className="ac-sec" data-sys={id}>
      <div className="ac-sec-l"><h2>{title}</h2><p>{hint}</p></div>
      <div className="ac-sec-r">
        <p className="sys-src t3" data-sys-src>{t(web ? 'sys.src.web' : env ? 'sys.src.env' : 'sys.src.none')}</p>
        <div className="sys-fields">{children}</div>
        <div className="sys-btns">
          <button type="button" className="btn secondary sm" onClick={onSave} data-sys-save>{t('common.save')}</button>
          <button type="button" className="btn ghost sm" onClick={test} disabled={busy} data-sys-test>{t(busy ? 'sys.testing' : 'sys.test')}</button>
          {web && onClear && <button type="button" className="btn ghost sm" onClick={onClear} data-sys-clear>{t('sys.clear')}</button>}
        </div>
        {msg && <p className={`sys-msg ${msg.ok ? 'ok' : 'bad'}`} data-sys-msg role="status">{msg.text}</p>}
      </div>
    </section>
  )
}

/** 密钥输入框：已经有的只显示头尾几位，留空 = 不改 */
function Secret({ label, f, value, onChange, hint }: { label: string; f: SField; value: string; onChange: (v: string) => void; hint?: string }) {
  const t = useT()
  return <Field label={label} type="password" reveal autoComplete="off" value={value} onChange={onChange}
    placeholder={f.value ? t('sys.saved1', { v: f.value }) : ''} hint={hint ?? (f.value ? t('sys.keepHint') : undefined)} data-sys-key={f.key} />
}

function MailGroup({ f, save }: GroupProps) {
  const t = useT()
  const host0 = f('mail.host').value
  const p0 = MAIL_PRESETS.find((p) => p.host && p.host === host0)?.id ?? (host0 ? 'custom' : 'gmail')
  const [preset, setPreset] = useState(p0)
  const [host, setHost] = useState(host0 || MAIL_PRESETS[0].host)
  const [port, setPort] = useState(f('mail.port').value || '465')
  const [user, setUser] = useState(f('mail.user').value)
  const [pass, setPass] = useState('')
  const [name, setName] = useState(f('mail.from_name').value)
  const ps = MAIL_PRESETS.find((p) => p.id === preset)!
  const pick = (id: string) => { const p = MAIL_PRESETS.find((x) => x.id === id)!; setPreset(id); if (p.host) { setHost(p.host); setPort(p.port) } }
  const keys = ['mail.host', 'mail.port', 'mail.user', 'mail.pass', 'mail.from_name']
  return (
    <Group id="mail" title={t('sys.mail')} hint={t('sys.mailHint')} src={keys.map(f)}
      onSave={() => save(t('sys.mail'), { 'mail.host': host.trim(), 'mail.port': port.trim(), 'mail.user': user.trim(), 'mail.pass': pass, 'mail.from_name': name.trim() })}
      onClear={() => save(t('sys.mail'), {}, keys)}>
      <div className="fld sys-sel sys-preset"><span className="fld-l">{t('sys.provider')}</span><Select label={t('sys.provider')} value={preset} onChange={pick}
        options={MAIL_PRESETS.map((p) => ({ value: p.id, label: t(`sys.mp.${p.id}` as Key) }))} /></div>
      {preset === 'custom' && <div className="sys-row">
        <Field label={t('sys.host')} value={host} onChange={setHost} placeholder="smtp.example.com" error={msHost(host) ? t('sys.msHost') : undefined} data-sys-key="mail.host" />
        <Field label={t('sys.port')} value={port} onChange={setPort} inputMode="numeric" hint={t('sys.portHint')} data-sys-key="mail.port" />
      </div>}
      <Field label={t('sys.mailUser')} value={user} onChange={setUser} placeholder="you@gmail.com" autoComplete="off" data-sys-key="mail.user" />
      <Secret label={t('sys.mailPass')} f={f('mail.pass')} value={pass} onChange={setPass}
        hint={f('mail.pass').value ? t('sys.keepHint') : t(`sys.passHint.${preset === 'gmail' ? 'gmail' : 'other'}` as Key)} />
      {ps.help && <a className="au-link sys-help" href={ps.help} target="_blank" rel="noreferrer noopener" data-sys-help>{t('sys.getPass')}</a>}
      <Field label={t('sys.fromName')} value={name} onChange={setName} placeholder={BRAND} hint={t('sys.fromNameHint')} data-sys-key="mail.from_name" />
    </Group>
  )
}

// ---------- AI 复盘点评：自定义厂商列表（D112，11-23，用户："主要就是自定义厂家，你可以参考 ccswitch 设计""其实只用做自定义厂商就好了"） ----------
// 每家：名字、接口格式、地址、密钥、模型、思考程度。同一时间用一家；每家能单独测试；加、改、切换、删都要二级密码。
type Effort = '' | 'none' | 'low' | 'medium' | 'high' | 'xhigh'
interface AiP { id: string; name: string; format: 'openai' | 'anthropic'; base: string; model: string; effort: Effort; key_hint?: string }
interface AiResp { active: string; list: AiP[]; env: { format?: string; model?: string; effort?: string }; crypt: boolean }
type Msg = { ok: boolean; text: string } | null
const EFFORTS: Effort[] = ['', 'none', 'low', 'medium', 'high', 'xhigh']
const BLANK: AiP = { id: '', name: '', format: 'openai', base: '', model: '', effort: '' }

function AiGroup({ setAsk }: { setAsk: (a: Ask) => void }) {
  const t = useT()
  const [d, setD] = useState<AiResp | null>(null)
  const [edit, setEdit] = useState<AiP | null>(null) // 正在加或改的那一家
  const [msgs, setMsgs] = useState<Record<string, Msg>>({})
  const [busy, setBusy] = useState('')
  const load = useCallback(() => { api<AiResp>('GET', '/api/admin/ai').then(setD).catch(() => setD(null)) }, [])
  useEffect(load, [load])
  if (!d) return null
  const envOn = !!d.env.format
  const cur = d.list.find((p) => p.id === d.active)
  const fmtL = (f?: string) => t(f === 'anthropic' ? 'ai.fmt.anthropic' : 'ai.fmt.openai')
  const effL = (e?: string) => t(`ai.ef.${e || 'default'}` as Key)
  const modelL = (p: { format?: string; model?: string }) => p.model || (p.format === 'anthropic' ? 'claude-opus-5-5' : '—')
  const test = async (key: string, body: Record<string, unknown>) => {
    setBusy(key); setMsgs((m) => ({ ...m, [key]: null }))
    try {
      const r = await api<{ ok: boolean; message?: string; code?: string; model?: string; reply?: string; tokens?: number }>('POST', '/api/admin/ai/test', body)
      const text = r.ok ? t('ai.testOk', { model: r.model ?? '', reply: r.reply ?? '', n: r.tokens ?? 0 }) : r.code ? errText(r.code) : r.message ?? ''
      setMsgs((m) => ({ ...m, [key]: { ok: r.ok, text } }))
    } catch (e) { setMsgs((m) => ({ ...m, [key]: { ok: false, text: errText(e instanceof ApiError ? e.code : 'INTERNAL_ERROR') } })) } finally { setBusy('') }
  }
  const use = (id: string, name: string) => setAsk({ title: t('ai.useTitle', { name }), body: t('ai.useBody'), ok: t('ai.use'),
    run: (pw) => api('POST', '/api/admin/ai/use', { id, trade_password: pw }).then(load), done: t('sys.saved') })
  const del = (p: AiP) => setAsk({ title: t('ai.delTitle', { name: p.name }), body: t(p.id === d.active ? 'ai.delBodyOn' : 'ai.delBody'), ok: t('ai.del'),
    run: (pw) => api('POST', '/api/admin/ai/delete', { id: p.id, trade_password: pw }).then(load), done: t('ai.deleted') })
  const row = (key: string, name: string, meta: string, on: boolean, acts: ReactNode) => (
    <li key={key} className={on ? 'aiv-row on' : 'aiv-row'} data-ai-row={key}>
      <div className="aiv-row-l"><span className="aiv-name">{name}{on && <span className="aiv-on" data-ai-on>{t('ai.on')}</span>}</span><span className="aiv-meta">{meta}</span></div>
      <div className="aiv-row-r">{acts}</div>
      {msgs[key] && <p className={`sys-msg ${msgs[key]!.ok ? 'ok' : 'bad'}`} data-sys-msg role="status">{msgs[key]!.text}</p>}
    </li>
  )
  const btn = (label: string, on: () => void, attr: string, disabled = false) => <button type="button" className="btn ghost sm" onClick={on} disabled={disabled} {...{ [attr]: '' }}>{label}</button>
  return (
    <section className="ac-sec" data-sys="ai">
      <div className="ac-sec-l"><h2>{t('sys.ai')}</h2><p>{t('ai.hint')}</p></div>
      <div className="ac-sec-r">
        <p className="sys-src t3" data-sys-src>{cur ? t('ai.using', { name: cur.name }) : envOn ? t('ai.usingEnv') : t('ai.usingNone')}</p>
        {(d.list.length > 0 || envOn) && <ul className="aiv-list">
          {d.list.map((p) => row(p.id, p.name, [fmtL(p.format), modelL(p), t('ai.efShort', { e: effL(p.effort) })].join(' · '), p.id === d.active, <>
            {p.id !== d.active && btn(t('ai.use'), () => use(p.id, p.name), 'data-ai-use')}
            {btn(t(busy === p.id ? 'sys.testing' : 'sys.test'), () => void test(p.id, { id: p.id }), 'data-ai-test', busy === p.id)}
            {btn(t('ai.edit'), () => { setEdit({ ...p }); setMsgs((m) => ({ ...m, form: null })) }, 'data-ai-edit')}
            {btn(t('ai.del'), () => del(p), 'data-ai-del')}
          </>))}
          {envOn && row('env', t('ai.envRow'), [fmtL(d.env.format), modelL(d.env), t('ai.efShort', { e: effL(d.env.effort) })].join(' · '), !cur, <>
            {cur && btn(t('ai.use'), () => use('', t('ai.envRow')), 'data-ai-use')}
            {btn(t(busy === 'env' ? 'sys.testing' : 'sys.test'), () => void test('env', { env: true }), 'data-ai-test', busy === 'env')}
          </>)}
        </ul>}
        {edit ? <AiForm p={edit} setP={setEdit} msg={msgs.form ?? null} busy={busy === 'form'}
          onTest={(key) => void test('form', { ...edit, key })}
          onSave={(key) => setAsk({ title: t(edit.id ? 'ai.saveTitleE' : 'ai.saveTitle', { name: edit.name || '' }), body: t('sys.saveBody'), ok: t('common.save'),
            run: (pw) => api('POST', '/api/admin/ai', { ...edit, key, trade_password: pw }).then(() => { setEdit(null); load() }), done: t('sys.saved') })}
          onCancel={() => setEdit(null)} />
          : <div className="sys-btns"><button type="button" className="btn secondary sm" onClick={() => setEdit({ ...BLANK })} disabled={d.list.length >= 20} data-ai-add>{t('ai.add')}</button></div>}
        {!d.crypt && <p className="ac-sig-note">{t('sys.noMaster')}</p>}
      </div>
    </section>
  )
}

function AiForm({ p, setP, msg, busy, onTest, onSave, onCancel }: { p: AiP; setP: (p: AiP) => void; msg: Msg; busy: boolean; onTest: (key: string) => void; onSave: (key: string) => void; onCancel: () => void }) {
  const t = useT()
  const [key, setKey] = useState('')
  const [models, setModels] = useState<string[] | null>(null) // 这家有哪些模型（点了"获取模型列表"以后）
  const [mq, setMq] = useState('')
  const [mBusy, setMBusy] = useState(false)
  const [mErr, setMErr] = useState('')
  const set = (k: Partial<AiP>) => setP({ ...p, ...k })
  const c = p.format === 'anthropic'
  const fetchModels = async () => {
    setMBusy(true); setMErr(''); setModels(null)
    try {
      const r = await api<{ ok: boolean; models?: string[]; code?: string; message?: string }>('POST', '/api/admin/ai/models', { ...p, key })
      if (r.ok && r.models) { setModels(r.models); setMq('') } else setMErr(r.code ? errText(r.code) : r.message ?? '')
    } catch (e) { setMErr(errText(e instanceof ApiError ? e.code : 'INTERNAL_ERROR')) } finally { setMBusy(false) }
  }
  const shown = models?.filter((m) => m.toLowerCase().includes(mq.trim().toLowerCase())) ?? []
  return (
    <div className="aiv-form" data-ai-form>
      <p className="aiv-form-h">{t(p.id ? 'ai.editing' : 'ai.adding')}</p>
      <div className="sys-fields">
        <Field label={t('ai.f.name')} value={p.name} onChange={(v) => set({ name: v })} placeholder={t('ai.f.nameHolder')} maxLength={40} data-ai-f="name" />
        <div className="fld sys-sel"><span className="fld-l">{t('ai.f.format')}</span><Select label={t('ai.f.format')} value={p.format} onChange={(v) => set({ format: v })}
          options={[{ value: 'openai', label: t('ai.fmt.openai') }, { value: 'anthropic', label: t('ai.fmt.anthropic') }]} /></div>
        <Field label={t('ai.f.base')} value={p.base} onChange={(v) => set({ base: v })} placeholder={c ? 'https://api.anthropic.com' : 'https://api.example.com/v1'} hint={t(c ? 'ai.f.baseHintC' : 'ai.f.baseHintO')} data-ai-f="base" />
        <Field label={t('ai.f.key')} type="password" reveal autoComplete="off" value={key} onChange={setKey} placeholder={p.key_hint ? t('sys.saved1', { v: p.key_hint }) : ''} hint={p.key_hint ? t('sys.keepHint') : undefined} data-ai-f="key" />
        <div className="aiv-model">
          <Field label={t('ai.f.model')} value={p.model} onChange={(v) => set({ model: v })} placeholder={c ? 'claude-opus-5-5' : 'model-name'} hint={t(c ? 'ai.f.modelHintC' : 'ai.f.modelHintO')} data-ai-f="model" />
          <button type="button" className="btn ghost sm aiv-model-btn" onClick={() => void fetchModels()} disabled={mBusy} data-ai-models>{t(mBusy ? 'ai.modelsLoading' : 'ai.models')}</button>
        </div>
        {mErr && <p className="sys-msg bad" data-ai-models-err role="status">{mErr}</p>}
        {models && (
          <div className="aiv-models" data-ai-models-list>
            <div className="aiv-models-h">
              <input className="aiv-models-q" value={mq} onChange={(e) => setMq(e.target.value)} placeholder={t('ai.modelsFilter')} aria-label={t('ai.modelsFilter')} spellCheck={false} autoFocus data-ai-models-q />
              <span className="aiv-models-n">{t('ai.modelsN', { n: models.length })}</span>
            </div>
            <ul>
              {shown.slice(0, 300).map((m) => (
                <li key={m}><button type="button" className={m === p.model ? 'on' : ''} onClick={() => { set({ model: m }); setModels(null) }} data-ai-model={m}>{m}</button></li>
              ))}
              {!shown.length && <li className="aiv-models-none">{t('ai.modelsNone')}</li>}
            </ul>
          </div>
        )}
        <div className="fld"><span className="fld-l">{t('ai.f.effort')}</span>
          <Seg label={t('ai.f.effort')} value={p.effort} onChange={(v) => set({ effort: v })} options={EFFORTS.map((e) => ({ value: e, label: t(`ai.ef.${e || 'default'}` as Key) }))} />
          <span className="fld-m t3">{t(c ? 'ai.f.effortHintC' : 'ai.f.effortHintO')}</span></div>
      </div>
      <div className="sys-btns">
        <button type="button" className="btn secondary sm" onClick={() => onSave(key)} data-ai-save>{t('common.save')}</button>
        <button type="button" className="btn ghost sm" onClick={() => onTest(key)} disabled={busy} data-ai-ftest>{t(busy ? 'sys.testing' : 'sys.test')}</button>
        <button type="button" className="btn ghost sm" onClick={onCancel} data-ai-cancel>{t('common.cancel')}</button>
      </div>
      {msg && <p className={`sys-msg ${msg.ok ? 'ok' : 'bad'}`} data-sys-msg role="status">{msg.text}</p>}
    </div>
  )
}

function TgGroup({ f, save }: GroupProps) {
  const t = useT()
  const [tok, setTok] = useState('')
  return (
    <Group id="tg" title={t('sys.tg')} hint={t('sys.tgHint')} src={[f('tg.token')]}
      onSave={() => save(t('sys.tg'), { 'tg.token': tok })} onClear={() => save(t('sys.tg'), {}, ['tg.token'])}>
      <Secret label={t('sys.tgToken')} f={f('tg.token')} value={tok} onChange={setTok} hint={f('tg.token').value ? t('sys.keepHint') : t('sys.tgTokenHint')} />
    </Group>
  )
}

function DataGroup({ f, save }: GroupProps) {
  const t = useT()
  const [fred, setFred] = useState('')
  const [xt, setXt] = useState('')
  const [xa, setXa] = useState(f('x.accounts').value)
  const [bk, setBk] = useState('')
  const [bs, setBs] = useState('')
  const keys = ['fred.key', 'x.token', 'x.accounts', 'bn.key', 'bn.secret']
  return (
    <Group id="data" title={t('sys.data')} hint={t('sys.dataHint')} src={keys.map(f)}
      onSave={() => save(t('sys.data'), { 'fred.key': fred, 'x.token': xt, 'x.accounts': xa.trim(), 'bn.key': bk, 'bn.secret': bs })}
      onClear={() => save(t('sys.data'), {}, keys)}>
      <Secret label={t('sys.fred')} f={f('fred.key')} value={fred} onChange={setFred} hint={f('fred.key').value ? t('sys.keepHint') : t('sys.fredHint')} />
      <Secret label={t('sys.xToken')} f={f('x.token')} value={xt} onChange={setXt} hint={f('x.token').value ? t('sys.keepHint') : t('sys.xHint')} />
      <Field label={t('sys.xAccounts')} value={xa} onChange={setXa} placeholder="WuBlockchain, lookonchain" hint={t('sys.xAccountsHint')} data-sys-key="x.accounts" />
      <div className="sys-row">
        <Secret label={t('sys.bnKey')} f={f('bn.key')} value={bk} onChange={setBk} hint={f('bn.key').value ? t('sys.keepHint') : t('sys.bnHint')} />
        <Secret label={t('sys.bnSecret')} f={f('bn.secret')} value={bs} onChange={setBs} />
      </div>
    </Group>
  )
}
