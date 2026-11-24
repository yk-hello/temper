// 账户页：通知开关、Telegram 绑定、到价提醒（模块 7，D85）。没有套餐（D90）。
import { fmtPrice, numIn } from '../lib/format'
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { errText, fmtTime, useT, type Key } from '../lib/i18n'
import { addAlert, addRule, delAlert, delRule, delWebhook, linkTelegram, loadAlerts, loadPrefs, loadRules, RULE_FIELDS, ruleSummary, ruleUnit, savePref, setRuleOff, setWebhook, testWebhook, unlinkTelegram, type AlertRule, type MailState, type PrefRow, type PriceAlert, type RuleCond, type RuleField, type TgState, type WebhookState } from '../lib/notify'
import { Seg } from '../ui/Seg'
import { Dialog } from '../ui/Dialog'
import { useSpecs } from '../lib/trade'
import { ApiError } from '../lib/session'
import { Field } from '../ui/Field'
import { toast } from '../lib/privacy'
import { Select } from '../ui/Select'

type Sec = (p: { title: string; hint?: string; children: ReactNode }) => JSX.Element

export function NotifySection({ Section }: { Section: Sec }) {
  const t = useT()
  const [rows, setRows] = useState<PrefRow[]>([])
  const [tg, setTg] = useState<TgState | null>(null)
  const [hook, setHook] = useState<WebhookState | null>(null)
  const [mail, setMail] = useState<MailState | null>(null)
  const [waiting, setWaiting] = useState(false)
  const self = useRef<HTMLDivElement>(null)
  const reload = () => loadPrefs().then((d) => { setRows(d.kinds); setTg(d.tg); setHook(d.webhook ?? null); setMail(d.mail ?? null); return d })
  useEffect(() => { void reload().catch(() => {}) }, [])
  // 铃铛里点"通知设置"过来时滚到这一节
  useEffect(() => {
    if (rows.length && new URLSearchParams(location.search).get('sec') === 'notify') self.current?.scrollIntoView({ block: 'start' })
  }, [rows.length])
  // 在 Telegram 里点完 Start，这里自己变成已绑定
  useEffect(() => {
    if (!waiting) return
    const id = window.setInterval(() => {
      void reload().then((d) => { if (d.tg.linked) { setWaiting(false); toast(t('nt.tgDone')) } }).catch(() => {})
    }, 2500)
    const stop = window.setTimeout(() => setWaiting(false), 600_000)
    return () => { window.clearInterval(id); window.clearTimeout(stop) }
  }, [waiting]) // eslint-disable-line react-hooks/exhaustive-deps
  const flip = async (r: PrefRow, f: 'site' | 'tg' | 'wh' | 'mail', v: boolean) => {
    const next = { ...r, [f]: v }
    setRows((l) => l.map((x) => (x.kind === r.kind ? next : x)))
    try { await savePref(next) } catch (e) {
      setRows((l) => l.map((x) => (x.kind === r.kind ? r : x)))
      toast(errText(e instanceof ApiError ? e.code : 'INTERNAL_ERROR'))
    }
  }
  const link = async () => {
    // 先开窗口再要地址：要等结果再开的话，浏览器会当成弹窗拦掉
    const w = window.open('', '_blank')
    try {
      const { url } = await linkTelegram()
      if (w) { w.opener = null; w.location.href = url } else location.href = url
      setWaiting(true)
    } catch (e) {
      w?.close()
      toast(errText(e instanceof ApiError ? e.code : 'INTERNAL_ERROR'))
    }
  }
  const unlink = async () => {
    await unlinkTelegram()
    await reload()
  }
  return (
    <div ref={self} data-notify-sec>
      <Section title={t('nt.title')} hint={t('nt.hint')}>
        <div className="ac-rows">
          <div className="ac-row" data-tg={tg?.enabled ? (tg.linked ? 'linked' : 'none') : 'off'}>
            <span className="ac-k">Telegram</span>
            {!tg?.enabled ? <span className="ac-v t3">{t('nt.tgOff')}</span> : tg.linked ? (
              <>
                <span className="ac-v">{t('nt.tgLinked')}</span>
                <span className="ac-act"><button className="btn ghost sm" onClick={() => void unlink()}>{t('nt.tgUnlink')}</button></span>
              </>
            ) : (
              <>
                <span className="ac-v t3">{waiting ? t('nt.tgWait') : t('nt.tgNone')}</span>
                <span className="ac-act"><button className="btn secondary sm" data-tg-link onClick={() => void link()}>{t('nt.tgLink')}</button></span>
              </>
            )}
          </div>
          {mail && (
            <div className="ac-row" data-mail={mail.to ? 'on' : 'unverified'}>
              <span className="ac-k">{t('nt.mail')}</span>
              <span className={`ac-v ${mail.to ? '' : 't3'}`}>{mail.to ? t('nt.mailTo', { email: mail.to }) : mail.email ? t('nt.mailVerify', { email: mail.email }) : t('nt.mailNone')}</span>
            </div>
          )}
          <WebhookRow hook={hook} onChange={() => void reload()} />
        </div>
        <div className="ac-tbl-wrap nt-wrap">
          <table className="tbl ac-tbl nt-tbl" data-notify>
            {/* 管理员没配 Telegram（"暂未开通"）时不放这一列：勾了也收不到（11-17 走查） */}
            <thead><tr><th>{t('nt.event')}</th><th className="nt-c">{t('nt.site')}</th>{tg?.enabled && <th className="nt-c">Telegram</th>}{mail && <th className="nt-c">{t('nt.mail')}</th>}{hook && <th className="nt-c">Webhook</th>}</tr></thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.kind} data-pref={r.kind}>
                  <td><span className="ac-name">{t(`ntk.${r.kind}` as Key)}</span><span className="nt-k-sub">{t(`ntk.${r.kind}.sub` as Key)}</span></td>
                  <td className="nt-c"><label className="op-check"><input type="checkbox" aria-label={`${t(`ntk.${r.kind}` as Key)} ${t('nt.site')}`} checked={r.site} onChange={(e) => void flip(r, 'site', e.target.checked)} /></label></td>
                  {tg?.enabled && <td className="nt-c"><label className="op-check"><input type="checkbox" aria-label={`${t(`ntk.${r.kind}` as Key)} Telegram`} disabled={!r.tg_ok} checked={r.tg && r.tg_ok} onChange={(e) => void flip(r, 'tg', e.target.checked)} /></label></td>}
                  {mail && <td className="nt-c"><label className="op-check"><input type="checkbox" aria-label={`${t(`ntk.${r.kind}` as Key)} ${t('nt.mail')}`} disabled={!mail.to} checked={r.mail && !!mail.to} onChange={(e) => void flip(r, 'mail', e.target.checked)} data-pref-mail={r.kind} /></label></td>}
                  {hook && <td className="nt-c"><label className="op-check"><input type="checkbox" aria-label={`${t(`ntk.${r.kind}` as Key)} Webhook`} checked={r.wh} onChange={(e) => void flip(r, 'wh', e.target.checked)} data-pref-wh={r.kind} /></label></td>}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Section>
    </div>
  )
}

/** 对外 Webhook（18 第 6.3 条）：填一个地址，勾选的事件 POST 一份 JSON 过去，带签名 */
function WebhookRow({ hook, onChange }: { hook: WebhookState | null; onChange: () => void }) {
  const t = useT()
  const [editing, setEditing] = useState(false)
  const [url, setUrl] = useState('')
  const [err, setErr] = useState<string>()
  const [secret, setSecret] = useState('')
  const [busy, setBusy] = useState(false)
  const save = async (rotate = false) => {
    setBusy(true); setErr(undefined)
    try {
      const r = await setWebhook(rotate ? hook!.url : url.trim(), rotate)
      if (r.secret) setSecret(r.secret)
      setEditing(false)
      onChange()
    } catch (e) { setErr(errText(e instanceof ApiError ? e.code : 'INTERNAL_ERROR')) } finally { setBusy(false) }
  }
  const test = async () => {
    setBusy(true)
    try {
      const r = await testWebhook()
      toast(r.error ? t('wh.testFail', { why: r.error }) : t('wh.testOk', { status: r.status }))
      onChange()
    } catch (e) { toast(errText(e instanceof ApiError ? e.code : 'INTERNAL_ERROR')) } finally { setBusy(false) }
  }
  const last = hook?.last_at ? (hook.last_err ? t('wh.lastFail', { why: hook.last_err, at: fmtTime(hook.last_at) }) : t('wh.lastOk', { status: hook.last_status, at: fmtTime(hook.last_at) })) : t('wh.never')
  return (
    <div className="ac-row nt-wh" data-wh={hook ? 'set' : 'none'}>
      <span className="ac-k">Webhook</span>
      {hook && !editing ? (
        <>
          <span className="ac-v"><span className="nt-wh-url" title={hook.url}>{hook.url}</span><span className={`nt-wh-last ${hook.last_err ? 'warn-t' : 't3'}`} data-wh-last>{last}</span></span>
          <span className="ac-act">
            <button className="btn ghost sm" disabled={busy} onClick={() => void test()} data-wh-test>{t('wh.test')}</button>
            <button className="btn ghost sm" onClick={() => { setUrl(hook.url); setEditing(true) }}>{t('wh.edit')}</button>
            <button className="btn ghost sm" onClick={() => void delWebhook().then(onChange)} data-wh-del>{t('wh.del')}</button>
          </span>
        </>
      ) : editing || !hook ? (
        <>
          <span className="ac-v nt-wh-form">
            <input className="nt-wh-in" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://" aria-label={t('wh.url')} title={t('wh.chatHint')} data-wh-url />
            {err ? <span className="warn-t nt-wh-err">{err}</span> : url.trim() === '' ? null : /^https:\/\/(discord(app)?\.com\/api\/webhooks\/|hooks\.slack\.com\/)/.test(url.trim()) ? <span className="t3 nt-wh-err" data-wh-chat>{t('wh.chatOk')}</span> : null}
          </span>
          <span className="ac-act">
            {hook && <button className="btn ghost sm" onClick={() => setEditing(false)}>{t('common.cancel')}</button>}
            <button className="btn secondary sm" disabled={busy || !url.trim()} onClick={() => void save()} data-wh-save>{t('wh.save')}</button>
          </span>
        </>
      ) : null}
      <Dialog open={!!secret} title={t('wh.secretTitle')} onClose={() => setSecret('')} width={560}>
        {secret && (
          <div className="au-form" data-wh-secret>
            <p className="t2">{t('wh.secretBody')}</p>
            <div className="mys-kv"><span className="t3">secret</span><code>{secret}</code><button type="button" className="btn ghost sm" onClick={() => void navigator.clipboard?.writeText(secret).then(() => toast(t('mys.copied')))}>{t('mys.copy')}</button></div>
            <code className="mys-cmd">{t('wh.verify')}</code>
            <div className="dlg-btns"><button type="button" className="btn primary" onClick={() => setSecret('')}>{t('mys.saved')}</button></div>
          </div>
        )}
      </Dialog>
    </div>
  )
}

export function AlertsSection({ Section }: { Section: Sec }) {
  const t = useT()
  const specs = useSpecs()
  const insts = useMemo(() => {
    const rank = (i: string) => { const k = ['BTC', 'ETH', 'SOL', 'BNB', 'XRP', 'DOGE'].indexOf(i.replace(/-PERP$/, '')); return k < 0 ? 99 : k }
    return Object.keys(specs ?? {}).filter((i) => i.endsWith('-PERP')).sort((a, b) => rank(a) - rank(b) || a.localeCompare(b))
  }, [specs])
  const [list, setList] = useState<PriceAlert[]>([])
  const [inst, setInst] = useState('')
  const [px, setPx] = useState('')
  const [busy, setBusy] = useState(false)
  const cur = inst || insts[0] || ''
  const reload = () => loadAlerts().then((l) => setList(l.sort((a, b) => a.instrument.localeCompare(b.instrument) || b.px - a.px))).catch(() => {})
  useEffect(() => { void reload() }, [])
  const add = async () => {
    const v = Number(px)
    if (!(v > 0)) { toast(errText('BAD_PRICE')); return }
    setBusy(true)
    try {
      await addAlert(cur, v)
      setPx('')
      await reload()
    } catch (e) {
      toast(errText(e instanceof ApiError ? e.code : 'INTERNAL_ERROR'))
    } finally { setBusy(false) }
  }
  return (
    <Section title={t('nt.alerts')} hint={t('nt.alertsHint')}>
      {insts.length > 0 && <form className="nt-alert-new" onSubmit={(e) => { e.preventDefault(); void add() }}>
        {/* 和下面"条件提醒"一样：品种也写标签、框一样高（11-17 走查：原来品种框比价格框矮一截、没对齐） */}
        <div className="fld"><span className="fld-l">{t('nt.alertInst')}</span><Select className="ac-sel" label={t('nt.alertInst')} value={cur} onChange={setInst} options={insts.map((i) => ({ value: i, label: i.replace(/-PERP$/, '') }))} /></div>
        <Field className="nt-alert-px" label={t('nt.alertPx')} value={px} inputMode="decimal" data-alert-px onChange={(v) => setPx(numIn(v))} />
        <button className="btn secondary" data-alert-add disabled={busy || !px}>{t('nt.alertAdd')}</button>
      </form>}
      {list.length > 0 ? (
        <div className="ac-tbl-wrap nt-wrap">
          <table className="tbl ac-tbl" data-alerts>
            <tbody>
              {list.map((a) => (
                <tr key={a.id}>
                  <td><span className="ac-name">{a.instrument.replace(/-PERP$/, '')}</span></td>
                  <td className="num">{t(a.up ? 'nt.alertUp' : 'nt.alertDown', { px: fmtPrice(String(a.px)) })}</td>
                  <td className="ac-tbl-act"><button className="btn ghost sm" onClick={() => void delAlert(a.id).then(reload)}>{t('nt.alertDel')}</button></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : <p className="nt-sub">{t('nt.alertNone')}</p>}
    </Section>
  )
}

/** 条件提醒（18 第 10.4 条，D102）：选一个币，最多 4 个条件，全部满足或任一满足时提醒。规则只是数据，只提醒，不下单 */
export function RulesSection({ Section }: { Section: Sec }) {
  const t = useT()
  const specs = useSpecs()
  const insts = useMemo(() => {
    const rank = (i: string) => { const k = ['BTC', 'ETH', 'SOL', 'BNB', 'XRP', 'DOGE'].indexOf(i.replace(/-PERP$/, '')); return k < 0 ? 99 : k }
    return Object.keys(specs ?? {}).filter((i) => i.endsWith('-PERP')).sort((a, b) => rank(a) - rank(b) || a.localeCompare(b))
  }, [specs])
  const [list, setList] = useState<AlertRule[]>([])
  const [max, setMax] = useState(20)
  const [inst, setInst] = useState('')
  const [name, setName] = useState('')
  const [conds, setConds] = useState<{ field: RuleField; op: '>' | '<'; value: string }[]>([{ field: 'funding', op: '>', value: '' }])
  const [join, setJoin] = useState<'all' | 'any'>('all')
  const [cooldown, setCooldown] = useState('60')
  const [once, setOnce] = useState(false)
  const [busy, setBusy] = useState(false)
  const cur = inst || insts[0] || ''
  const reload = () => loadRules().then((r) => { setList((r.rules ?? []).sort((a, b) => b.created - a.created)); setMax(r.max) }).catch(() => {})
  useEffect(() => { void reload() }, [])
  const setCond = (i: number, p: Partial<{ field: RuleField; op: '>' | '<'; value: string }>) => setConds(conds.map((c, j) => (j === i ? { ...c, ...p } : c)))
  const ready = conds.every((c) => c.value !== '' && Number.isFinite(Number(c.value)))
  const add = async () => {
    if (!ready) return
    setBusy(true)
    try {
      const made = await addRule({ name, instrument: cur, join, once, cooldown: Number(cooldown), conds: conds.map((c): RuleCond => ({ field: c.field, op: c.op, value: Number(c.value) })) })
      setName(''); setConds([{ field: 'funding', op: '>', value: '' }])
      await reload()
      toast(t(made.now ? 'rl.addedNow' : 'rl.added'))
    } catch (e) {
      toast(errText(e instanceof ApiError ? e.code : 'INTERNAL_ERROR'))
    } finally { setBusy(false) }
  }
  const fieldOpts = RULE_FIELDS.map((f) => ({ value: f, label: t(`rf.${f}` as Key) }))
  const cdOpts = ['5', '15', '30', '60', '240', '1440'].map((v) => ({ value: v, label: t(`rl.cd.${v}` as Key) }))
  return (
    <Section title={t('rl.title')} hint={t('rl.hint')}>
      {insts.length > 0 && list.length < max && <form className="rl-new" data-rule-form onSubmit={(e) => { e.preventDefault(); void add() }}>
        <div className="rl-top">
          <div className="fld"><span className="fld-l">{t('nt.alertInst')}</span><Select className="ac-sel" label={t('nt.alertInst')} value={cur} onChange={setInst} options={insts.map((i) => ({ value: i, label: i.replace(/-PERP$/, '') }))} /></div>
          <Field className="rl-name" label={t('rl.name')} value={name} maxLength={40} placeholder={t('rl.namePh')} onChange={setName} data-rule-name />
        </div>
        <ol className="rl-conds">
          {conds.map((c, i) => (
            <li key={i} data-rule-cond={i}>
              <span className="rl-join">{i === 0 ? t('rl.when') : t(join === 'any' ? 'rl.orW' : 'rl.andW')}</span>
              <Select className="rl-field" label={t('rl.field')} value={c.field} onChange={(f) => setCond(i, { field: f })} options={fieldOpts} />
              <Seg label={t('rl.op')} value={c.op} onChange={(op) => setCond(i, { op })} options={[{ value: '>', label: t('rl.above') }, { value: '<', label: t('rl.below') }]} />
              <span className="fld-box rl-val"><input inputMode="decimal" aria-label={t('rl.value')} value={c.value} data-rule-value onChange={(e) => setCond(i, { value: e.target.value.replace(/[^\d.-]/g, '') })} /><em>{ruleUnit(c.field)}</em></span>
              {conds.length > 1 && <button type="button" className="btn ghost sm" aria-label={t('rl.delCond')} onClick={() => setConds(conds.filter((_, j) => j !== i))}>×</button>}
            </li>
          ))}
        </ol>
        <div className="rl-bottom">
          {conds.length < 4 && <button type="button" className="btn ghost sm" data-rule-more onClick={() => setConds([...conds, { field: 'chg24h', op: '>', value: '' }])}>{t('rl.more')}</button>}
          {conds.length > 1 && <Seg label={t('rl.joinL')} value={join} onChange={setJoin} options={[{ value: 'all', label: t('rl.all') }, { value: 'any', label: t('rl.any') }]} />}
          <Select className="rl-cd" label={t('rl.cooldown')} value={cooldown} onChange={setCooldown} options={cdOpts} />
          <label className="op-check"><input type="checkbox" checked={once} onChange={(e) => setOnce(e.target.checked)} /><span>{t('rl.once')}</span></label>
          <button className="btn secondary" data-rule-add disabled={busy || !ready}>{t('rl.add')}</button>
        </div>
      </form>}
      {list.length > 0 ? (
        <div className="ac-tbl-wrap nt-wrap">
          <table className="tbl ac-tbl" data-rules>
            <tbody>
              {list.map((r) => (
                <tr key={r.id} className={r.off ? 'rl-off' : ''}>
                  <td><span className="ac-name">{r.instrument.replace(/-PERP$/, '')}</span>{r.name && <span className="t3 rl-nm"> · {r.name}</span>}</td>
                  <td className="rl-sum">{ruleSummary(r)}</td>
                  <td className="t3 num rl-st">{r.off ? t(r.once && r.last_hit ? 'rl.done' : 'rl.paused') : r.last_hit ? t('rl.last', { t: fmtTime(r.last_hit) }) : t(r.now ? 'rl.nowMet' : 'rl.never')}</td>
                  <td className="ac-tbl-act">
                    <button className="btn ghost sm" data-rule-toggle onClick={() => void setRuleOff(r.id, !r.off).then(reload)}>{t(r.off ? 'rl.resume' : 'rl.pause')}</button>
                    <button className="btn ghost sm" data-rule-del onClick={() => void delRule(r.id).then(reload)}>{t('nt.alertDel')}</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : <p className="nt-sub">{t('rl.none')}</p>}
    </Section>
  )
}

