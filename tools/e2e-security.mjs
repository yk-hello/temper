// 账户安全（18 第 6.2 条）：二次验证（扫码、确认、恢复码、登录第二步、恢复码只能用一次）、登录设备、新设备登录提醒、防钓鱼码。
// 会注册账号：只能对着测试环境跑。
// 用法：node tools/e2e-security.mjs <截图目录> [http://127.0.0.1:4173]
import { chromium } from 'playwright'
import { createHmac } from 'node:crypto'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const rand = () => Math.random().toString(36).slice(2)

// RFC 6238：和手机上的验证器算法一样
const b32 = (s) => {
  const a = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  let bits = '', out = []
  for (const ch of s.replace(/\s/g, '').toUpperCase()) bits += a.indexOf(ch).toString(2).padStart(5, '0')
  for (let i = 0; i + 8 <= bits.length; i += 8) out.push(parseInt(bits.slice(i, i + 8), 2))
  return Buffer.from(out)
}
const totp = (secret, t = Date.now()) => {
  const msg = Buffer.alloc(8)
  msg.writeBigUInt64BE(BigInt(Math.floor(t / 1000 / 30)))
  const h = createHmac('sha1', b32(secret)).update(msg).digest()
  const o = h[h.length - 1] & 15
  return String((h.readUInt32BE(o) & 0x7fffffff) % 1000000).padStart(6, '0')
}
const nextStep = async () => { const ms = 30000 - (Date.now() % 30000) + 300; await sleep(ms) } // 同一个时间步的数不能用两次

const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const ctx = (ua) => browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN', ...(ua ? { userAgent: ua } : {}) })
const c = await ctx()
await c.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1') })
const p = await c.newPage()
const errs = []
p.on('pageerror', (e) => errs.push(String(e)))
const call = (pg, method, path, body) => pg.evaluate(async ({ method, path, body }) => {
  const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
  return { s: r.status, b: await r.json().catch(() => null) }
}, { method, path, body })
const EMAIL = `sc${Date.now()}${rand().slice(0, 4)}@example.com`, PASS = `sc-${rand()}-pass`
await p.goto(BASE + '/?page=login')
await call(p, 'POST', '/api/auth/register', { email: EMAIL, password: PASS })

// 开二次验证
await p.goto(BASE + '/?page=account')
const sec = p.locator('[data-security]')
await sec.waitFor()
await sec.scrollIntoViewIfNeeded()
ok(await p.locator('[data-2fa="off"]').count() === 1, '开始时没开')
await p.locator('[data-2fa-setup]').click()
const form = p.locator('[data-2fa-form]')
await form.waitFor()
await form.locator('[data-2fa-secret]').waitFor()
ok(await form.locator('.sec-qr svg').count() === 1, '有二维码给验证器扫')
const SECRET = (await form.locator('[data-2fa-secret]').innerText()).replace(/\s/g, '')
ok(/^[A-Z2-7]{32}$/.test(SECRET), '也给了能手输的密钥')
await p.locator('.dlg:has([data-2fa-form])').screenshot({ path: `${OUT}/sec-01-setup.png` })
await form.locator('input').last().fill('000000')
await form.locator('[data-2fa-enable]').click()
ok(!!(await form.locator('.fld.bad .fld-m').waitFor({ timeout: 5000 }).then(() => form.locator('.fld.bad .fld-m').innerText()).catch(() => '')).match(/不对/), '错的数：说清楚')
await form.locator('input').last().fill(totp(SECRET))
await form.locator('[data-2fa-enable]').click()
const codesBox = p.locator('[data-2fa-codes]')
await codesBox.waitFor()
const codes = (await codesBox.locator('code').innerText()).split('\n').map((s) => s.trim()).filter(Boolean)
ok(codes.length === 8 && codes.every((x) => /^[a-z2-7]{4}-[a-z2-7]{4}$/.test(x)), `给了 8 个恢复码（${codes[0]}…）`)
await p.locator('.dlg:has([data-2fa-codes])').screenshot({ path: `${OUT}/sec-02-codes.png` })
await codesBox.locator('[data-2fa-done]').click()
ok(!!(await p.locator('[data-2fa="on"]').waitFor({ timeout: 5000 }).then(() => true).catch(() => false)), '状态变成已开启')

// 防钓鱼码
await p.locator('[data-phish]').fill('蓝色海豚')
await p.locator('[data-phish-save]').click()
await sleep(500)
ok((await call(p, 'GET', '/api/me')).b.user.anti_phish === '蓝色海豚', '防钓鱼码存上了')

// 换个设备登录：密码对了要验证码；恢复码能用一次
const SAFARI = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15'
const c2 = await ctx(SAFARI)
const q = await c2.newPage()
await q.goto(BASE + '/?page=login')
await q.locator('input[data-auth-name]').fill(EMAIL) // 账户名（D113）：老脚本的 EMAIL 就是账户名
await q.locator('input[type="password"]').fill(PASS)
await q.locator('form button.btn.primary').click()
const step = q.locator('[data-tfa-step]')
ok(!!(await step.waitFor({ timeout: 5000 }).then(() => true).catch(() => false)), '密码对了：要输验证码')
await q.screenshot({ path: `${OUT}/sec-03-login-step.png` })
await step.locator('input').fill('123456')
await step.locator('button.btn.primary').click()
ok(!!(await step.locator('.fld.bad .fld-m').waitFor({ timeout: 5000 }).then(() => step.locator('.fld.bad .fld-m').innerText()).catch(() => '')).match(/不对/), '错的验证码：说清楚')
await step.locator('input').fill(codes[0])
await step.locator('button.btn.primary').click()
ok(!!(await q.waitForURL((u) => !u.search.includes('page=login'), { timeout: 8000 }).then(() => true).catch(() => false)), '恢复码登录成功')
// 新设备登录提醒（站内）
let note = null
for (let i = 0; i < 20 && !note; i++) { await sleep(250); note = (await call(p, 'GET', '/api/notify')).b?.items?.find((x) => x.kind === 'login_new') }
ok(!!note && /Safari/.test(note.params?.device ?? ''), `新设备登录提醒（${note?.params?.device}）`)
// 用过的恢复码不能再用
const c3 = await ctx()
const r3 = await c3.newPage()
await r3.goto(BASE + '/?page=login')
let lr = await call(r3, 'POST', '/api/auth/login', { email: EMAIL, password: PASS })
ok(lr.b?.need_2fa === true, '再登录还是要验证码')
let fr = await call(r3, 'POST', '/api/auth/2fa', { ticket: lr.b.ticket, code: codes[0] })
ok(fr.s === 401 && fr.b?.code === 'BAD_2FA', '用过的恢复码不能再用')
await nextStep()
fr = await call(r3, 'POST', '/api/auth/2fa', { ticket: lr.b.ticket, code: totp(SECRET) })
ok(fr.s === 200, '验证器的数能登录')

// 登录设备：退出其他所有
await p.reload()
await sec.waitFor()
await p.locator('[data-devices]').scrollIntoViewIfNeeded()
const n0 = await p.locator('[data-devices] tbody tr').count()
ok(n0 >= 3, `列出登录着的设备（${n0} 个）`)
ok(/Safari · macOS/.test(await p.locator('[data-devices]').innerText()), '设备写成"Safari · macOS"这样')
await p.locator('.ac-sec:has([data-security])').screenshot({ path: `${OUT}/sec-04-section.png` })
await p.locator('[data-revoke-others]').click()
await sleep(800)
ok(await p.locator('[data-devices] tbody tr').count() === 1, '退出其他设备后只剩当前')
ok((await call(q, 'GET', '/api/me')).s === 401, '别的设备被退出了')

// 关掉：要有效的数
await nextStep()
await p.locator('[data-2fa-off]').click()
const off = p.locator('[data-2fa-disable]')
await off.waitFor()
await off.locator('input').fill(totp(SECRET))
await off.locator('[data-2fa-off-ok]').click()
ok(!!(await p.locator('[data-2fa="off"]').waitFor({ timeout: 5000 }).then(() => true).catch(() => false)), '关掉了')
lr = await call(r3, 'POST', '/api/auth/login', { email: EMAIL, password: PASS })
ok(lr.s === 200 && !lr.b?.need_2fa, '关掉后只要密码')
ok(errs.length === 0, `页面没有报错（${errs.join(' | ').slice(0, 200)}）`)
await browser.close()
console.log(`\n${pass} 通过，${fail} 失败`)
process.exit(fail ? 1 : 0)
