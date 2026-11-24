// 邮件（D100、1.12、1.13、18 第 8.1–8.2 条）：注册验证码、忘记登录密码、忘了二级密码、管理员重置二级密码、通知邮件。
// 测试环境把信发到本机 2525 端口（tools/dev/restart.ps1），这个脚本在那里起一个收信端，看信是不是真的发出来了、内容对不对。
// 开发模式同时开着，验证码也显示在页面上（两边的数应该一样）。
// 第一个注册的人是所有者：对着刚清空的测试库跑（tools/dev/regress.sh mail）。不要对着 8080。
// 用法：node tools/e2e-mail.mjs <截图目录> [http://127.0.0.1:4173]
import { chromium } from 'playwright'
import net from 'node:net'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const rand = () => Math.random().toString(36).slice(2)
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const until = async (fn, ms = 10000) => {
  const t0 = Date.now()
  while (Date.now() - t0 < ms) {
    const v = await fn().catch(() => null)
    if (v) return v
    await sleep(200)
  }
  return null
}

// ---------- 本机收信端：记下每封信的收件人、标题、正文 ----------
const inbox = []
// 标题是 RFC 2047 的 Q 编码（=?utf-8?q?...?=），可能分成几段
const decodeWords = (s) => s.replace(/=\?utf-8\?q\?([^?]*)\?=(\s+(?==\?))?/gi, (_, q) =>
  Buffer.from(q.replace(/_/g, ' ').replace(/=([0-9A-F]{2})/gi, (__, h) => String.fromCharCode(parseInt(h, 16))), 'latin1').toString('utf8'))
const smtp = net.createServer((c) => {
  let buf = '', inData = false, to = ''
  const w = (s) => c.write(s + '\r\n')
  w('220 sink')
  c.on('error', () => {})
  c.on('data', (d) => {
    buf += d.toString('latin1')
    for (;;) {
      if (inData) {
        const end = buf.indexOf('\r\n.\r\n')
        if (end < 0) return
        const raw = buf.slice(0, end)
        buf = buf.slice(end + 5)
        const cut = raw.indexOf('\r\n\r\n')
        const head = raw.slice(0, cut)
        const subject = decodeWords((head.match(/^Subject: (.*)$/mi) || [])[1] || '')
        const body = Buffer.from(raw.slice(cut + 4).replace(/\s+/g, ''), 'base64').toString('utf8')
        inbox.push({ to, subject, body, head })
        inData = false
        w('250 queued')
        continue
      }
      const i = buf.indexOf('\r\n')
      if (i < 0) return
      const line = buf.slice(0, i)
      buf = buf.slice(i + 2)
      const up = line.toUpperCase()
      if (up.startsWith('RCPT TO:')) { to = line.slice(8).replace(/[<> ]/g, ''); w('250 ok') }
      else if (up === 'DATA') { inData = true; w('354 go') }
      else if (up === 'QUIT') { w('221 bye'); c.end(); return }
      else w('250 ok')
    }
  })
})
await new Promise((res, rej) => { smtp.once('error', rej); smtp.listen(2525, '127.0.0.1', res) })
/** 等发给 to、标题对得上的信（发信在后台，稍等一下）；nth = 第几封（从 0 起） */
const mailTo = (to, re, nth = 0, ms = 8000) => until(async () => inbox.filter((x) => x.to === to && re.test(x.subject))[nth] ?? null, ms)
const codeIn = (m) => m?.body.match(/\d{6}/)?.[0] ?? ''

const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const newPage = async (userAgent) => {
  const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN', ...(userAgent ? { userAgent } : {}) })
  await c.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1') })
  const p = await c.newPage()
  p.errs = []
  p.on('pageerror', (e) => p.errs.push(String(e)))
  return p
}
const call = (p, method, path, body) => p.evaluate(async ({ method, path, body }) => {
  const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
  let j = null
  try { j = await r.json() } catch { /* 不是 JSON */ }
  return { s: r.status, b: j }
}, { method, path, body })
const shot = (p, name, el) => (el ? p.locator(el).first().screenshot({ path: `${OUT}/${name}.png` }) : p.screenshot({ path: `${OUT}/${name}.png` }))
const devCode = async (p, scope) => (await until(async () => (await p.locator(`${scope} .au-note`).innerText()).match(/\d{6}/)?.[0])) ?? ''

// 两个账号（D105 没有角色：权限一样）
const o = await newPage()
await o.goto(BASE + '/?page=login')
const OP = `ow-${rand()}-pass`, OTP = 'otp-' + rand()
const ou = (await call(o, 'POST', '/api/auth/register', { email: `owner${Date.now()}@example.com`, password: OP })).b.user
if (!ou) { console.log('✗ 注册不了（测试环境要 QS_SIGNUP=open）'); await browser.close(); smtp.close(); process.exit(1) }
await call(o, 'POST', '/api/me/trade-password', { new: OTP })
const NAME = `mem${Date.now()}`, EMAIL = `${NAME}@example.com`, P1 = `m1-${rand()}-pass`, TP1 = 'tp1-' + rand()
const m = await newPage()
await m.goto(BASE + '/?page=login')
// 注册只要账户名（D113），不发信；邮箱在账户页绑定
const r0 = await call(m, 'POST', '/api/auth/register', { name: NAME, password: P1 })
ok(r0.s === 200 && !r0.b.dev_code && !r0.b.user.email, '注册只要账户名：不要邮箱、不发验证码')
const reg = await call(m, 'POST', '/api/me/bind-email', { email: EMAIL })

// ---------- 绑定邮箱的验证码真的发出来了 ----------
const mailState = (await call(m, 'GET', '/api/auth/mail')).b
ok(mailState.mail === true, `测试环境配了发信邮箱（发到本机收信端）（${JSON.stringify(mailState)}）`)
const vmail = await mailTo(EMAIL, /邮箱验证码/)
ok(!!vmail && codeIn(vmail) === reg.b.dev_code, `绑定邮箱：验证码邮件发出来了，和页面上的一样（${codeIn(vmail)}）`)
ok((await call(m, 'POST', '/api/auth/verify-email', { code: codeIn(vmail) })).s === 200, '填验证码，邮箱验证好了（验证过的邮箱才能找回密码）')
ok(!!vmail && !/https?:\/\//.test(vmail.body), '信里没有任何链接')
ok(!!vmail && /Content-Type: text\/plain; charset=UTF-8/i.test(vmail.head) && /^From: .*qs-test@example\.com/mi.test(vmail.head), '信头：纯文本 UTF-8，发件人是配的地址')
await call(m, 'POST', '/api/me/trade-password', { new: TP1 })
await call(m, 'POST', '/api/auth/logout')

// ---------- 忘记登录密码 ----------
await m.goto(BASE + '/?page=login')
await m.locator('[data-forgot]').click()
const ff = m.locator('[data-forgot-form]')
await ff.waitFor()
await ff.locator('input[data-forgot-name]').fill(NAME)
await ff.locator('button.btn.primary').click()
await ff.locator('[data-forgot-sent]').waitFor()
ok(/如果这个账户绑定了邮箱，验证码已经发过去了/.test(await ff.locator('[data-forgot-sent]').innerText()), '发了验证码：不说账户名有没有、绑没绑邮箱')
const code = await devCode(m, '[data-forgot-form]')
const fmail = await mailTo(EMAIL, /重设登录密码的验证码/)
ok(/^\d{6}$/.test(code) && codeIn(fmail) === code, `重设密码的验证码邮件发出来了（${codeIn(fmail)}）`)
await ff.locator('input[inputmode="numeric"]').fill('000000')
const P2 = `m2-${rand()}-pass`
await ff.locator('input[type="password"]').fill(P2)
await ff.locator('button.btn.primary').click()
ok(!!(await until(async () => /验证码/.test(await ff.locator('.fld.bad .fld-m').innerText()))), '错的验证码：标在验证码下面')
// 重新发送：旧的验证码作废，用新的
await ff.locator('[data-forgot-resend]').click()
ok(!!(await until(async () => /验证码已重新发送/.test(await m.locator('.toast').last().innerText()))), '能重新发送验证码')
const fmail2 = await mailTo(EMAIL, /重设登录密码的验证码/, 1)
const code2 = codeIn(fmail2)
ok(/^\d{6}$/.test(code2), `重新发送：又来了一封（${code2}）`)
await ff.locator('input[inputmode="numeric"]').fill(code2)
await shot(m, 'mail-01-reset', '.au-card')
await ff.locator('button.btn.primary').click()
ok(!!(await until(async () => (await ff.locator('[data-forgot-done]').count()) > 0)), '重设成功')
ok(!!(await mailTo(EMAIL, /登录密码已重设/)), '重设以后再发一封"登录密码已重设"')
ok((await call(m, 'POST', '/api/auth/login', { name: NAME, password: P1 })).s === 401, '旧密码不能登录了')
ok((await call(m, 'POST', '/api/auth/login', { name: NAME, password: P2 })).s === 200, '新密码能登录')
ok((await call(m, 'POST', '/api/auth/login', { name: EMAIL, password: P2 })).s === 401, '邮箱不能拿来登录（只有账户名能）')
// 没注册的账户名：回复一样，但不发信
const NOBODY = `nobody${Date.now()}`
const n0 = inbox.length
const nob = await call(m, 'POST', '/api/auth/forgot', { name: NOBODY })
await sleep(1200)
ok(nob.s === 200 && nob.b.ok === true && inbox.length === n0, '没注册的账户名也回"发过去了"，实际不发信')

// ---------- 忘了二级密码（D107）：输登录密码就能重置，不发验证码；所有设备退出；发一封安全提醒 ----------
await m.goto(BASE + '/?page=account')
const tsec = m.locator('.ac-sec', { has: m.locator('h2', { hasText: '二级密码' }) })
await tsec.waitFor()
await tsec.locator('.ac-act button').click()
await m.locator('[data-tp-forgot-link]').click()
const tr = m.locator('[data-tp-reset]')
await tr.waitFor()
ok(/登录密码/.test(await tr.innerText()) && !/验证码/.test(await tr.innerText()), '不发验证码：输登录密码就能重置，写明重置后会怎样')
const TP2 = 'tp2-' + rand()
const pws = tr.locator('input[type="password"]')
await pws.nth(0).fill('wrong-login-pw')
await pws.nth(1).fill(TP2)
await tr.locator('[data-tp-reset-ok]').click()
ok(!!(await until(async () => /登录密码不对/.test(await tr.innerText()))), '登录密码错了说清楚')
await pws.nth(0).fill(P2)
await shot(m, 'mail-02-trade', '.dlg')
await tr.locator('[data-tp-reset-ok]').click()
ok(!!(await until(async () => /page=login/.test(m.url()))), '重置后这台也退出，回到登录页')
ok(!!(await mailTo(EMAIL, /二级密码刚刚被重置/)), '发了一封安全提醒邮件')
ok((await call(m, 'POST', '/api/auth/login', { name: NAME, password: P2 })).s === 200, '用登录密码重新登录')
ok((await call(m, 'POST', '/api/me/trade-password', { old: TP2, new: 'tp3-' + rand() })).s === 200, '新的二级密码能用')

// ---------- 通知邮件（18 第 8.2 条）：默认只开和钱、安全有关的几种；能一种一种关 ----------
await m.goto(BASE + '/?page=account&sec=notify')
const nsec = m.locator('[data-notify-sec]')
await nsec.waitFor()
ok(!!(await until(async () => (await nsec.locator('[data-mail="on"]').innerText()).includes(EMAIL))), '通知设置里写明邮件发到哪个邮箱（重设过密码，邮箱算验证过了）')
ok(await nsec.locator('[data-pref-mail="login_new"]').isChecked() && await nsec.locator('[data-pref-mail="liq_warn"]').isChecked() && !(await nsec.locator('[data-pref-mail="fill"]').isChecked()), '默认：新设备登录、强平预警发邮件，成交不发')
await nsec.scrollIntoViewIfNeeded()
await shot(m, 'mail-04-notify', '[data-notify-sec]')
const SAFARI = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15'
const s2 = await newPage(SAFARI)
await s2.goto(BASE + '/?page=login')
ok((await call(s2, 'POST', '/api/auth/login', { name: NAME, password: P2 })).s === 200, '换一个没用过的设备登录')
const nmail = await mailTo(EMAIL, /新设备登录/)
ok(!!nmail && /Safari/.test(nmail.subject + nmail.body) && /在那里可以关掉/.test(nmail.body), `新设备登录的提醒邮件发出来了（${nmail?.subject.slice(0, 30)}）`)
// 关掉这一种：再换设备登录就不发
await nsec.locator('[data-pref-mail="login_new"]').uncheck()
await until(async () => (await call(m, 'GET', '/api/notify/prefs')).b.kinds.find((k) => k.kind === 'login_new').mail === false)
const FIREFOX = 'Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0'
const s3 = await newPage(FIREFOX)
await s3.goto(BASE + '/?page=login')
await call(s3, 'POST', '/api/auth/login', { name: NAME, password: P2 })
await sleep(1500)
ok(inbox.filter((x) => x.to === EMAIL && /新设备登录/.test(x.subject)).length === 1, '关掉以后再换设备登录：不发邮件')
const siteNote = (await call(m, 'GET', '/api/notify')).b.items.filter((x) => x.kind === 'login_new').length
ok(siteNote >= 2, `站内通知照常（${siteNote} 条）：关邮件不影响别的渠道`)

// ---------- D105 独立使用：谁也重置不了别人的二级密码（自己输登录密码重置，或者在部署的电脑上用本机命令） ----------
await o.goto(BASE + '/?page=admin')
const row = o.locator('tr', { hasText: NAME }) // 账号表显示账户名（D113）
await row.waitFor()
ok(await row.locator('button').count() === 0, '"系统"页的账号表里没有管别人的按钮')
const rp = await call(o, 'POST', `/api/admin/users/${reg.b.user.id}/reset-trade-password`, { trade_password: OTP })
ok(rp.s === 404 || rp.s === 405, `接口也没有了（${rp.s}）`)
await shot(o, 'mail-03-admin')

ok(m.errs.length === 0 && o.errs.length === 0, `页面没有报错（${[...m.errs, ...o.errs].slice(0, 2).join(' | ')}）`)
console.log(`\n${pass} 通过，${fail} 失败（收到 ${inbox.length} 封信）`)
await browser.close()
smtp.close()
process.exit(fail ? 1 : 0)
