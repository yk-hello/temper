// 验收模块 1 的端到端测试（docs/12：1.1–1.10）：
//   注册（账户名 + 密码，D113）→ 账户页绑定邮箱并验证 → 二级密码 → 模拟账户（新建、改名、重置）→ 语言 → 退出 → 登录（登录页没有钱包），
//   然后登录页、账户页过 16 种设备（横向溢出、页面报错、键盘焦点）。
// 准备：后端用 AUTH_DEV=1（默认）运行，前端在 BASE 上能访问（vite preview 或 docker 的 8080）。
//   cd tools && npm i --no-save playwright ethers@6.13.4
//   node tools/e2e-account.mjs <输出目录> [网址，默认 http://localhost:4173]
// 钱包用的是公开的测试私钥（Hardhat 测试账户 1），没有任何资产。
import fs from 'node:fs'
import { chromium } from 'playwright'
import { DEVICES } from './devices.mjs'

const OUT = process.argv[2] ?? './shots'
const BASE = process.argv[3] ?? 'http://localhost:4173'
fs.mkdirSync(OUT, { recursive: true })
const errs = []
const ok = (c, m) => { if (!c) { console.log('✗ ' + m); process.exitCode = 1 } else console.log('✓ ' + m) }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL })
const watch = (page) => {
  page.on('pageerror', (e) => errs.push(String(e)))
  page.on('console', (m) => { if (m.type() === 'error' && !/401|Failed to load resource/.test(m.text())) errs.push(m.text()) })
}

const ctx = await browser.newContext({ viewport: { width: 1536, height: 864 }, deviceScaleFactor: 1.25, locale: 'zh-CN' })
await ctx.addInitScript(() => localStorage.setItem('qs.wizardSeen', '1'))
const page = await ctx.newPage()
watch(page)
const email = `e2e${Date.now()}` // 账户名（D113）；变量名沿用

await page.goto(BASE + '/?page=login')
await page.waitForSelector('.au-card')
ok(await page.locator('.au-title').innerText() === '登录', '登录页中文')
await page.screenshot({ path: `${OUT}/flow-01-login.png` })
await page.getByRole('button', { name: '注册' }).click()
await page.waitForFunction(() => document.querySelector('.au-title')?.textContent === '注册')
// 账户名（D113）：太短、带空格都说清楚；不要邮箱
ok(await page.locator('input[type="email"]').count() === 0, '注册不要邮箱')
await page.getByLabel('账户名').fill('a')
await page.locator('.au-submit').click()
ok((await page.locator('.fld.bad .fld-m').innerText()).includes('2–64 个字'), '账户名太短的提示')
await page.getByLabel('账户名').fill('has space')
ok((await page.locator('.fld.bad .fld-m').innerText()).includes('不能有空格'), '账户名带空格的提示')
await page.screenshot({ path: `${OUT}/flow-01b-name-err.png` })
await page.getByLabel('账户名').fill(email)
await page.getByLabel('密码', { exact: true }).fill('short')
await page.locator('.au-submit').click()
ok((await page.locator('.fld.bad .fld-m').innerText()).includes('至少 10 位'), '短密码提示怎么改')
await page.getByLabel('密码', { exact: true }).fill('password-123')
await page.locator('.au-submit').click()
// 不用验证码，直接进去
await page.waitForFunction(() => !location.search.includes('login'), null, { timeout: 8000 })
await page.waitForSelector('.bar-user')
ok((await page.locator('.bar-user').innerText()).includes(email), '注册完直接登录，顶栏显示账户名')

await page.locator('.bar-user').click()
await page.waitForSelector('[data-papers] tbody tr')
ok(await page.locator('[data-acct-name]').innerText() === email && /没绑定/.test(await page.locator('.ac-row', { hasText: '邮箱' }).first().innerText()), '账户页：账户名、邮箱没绑定')
// 在账户页绑定邮箱（找回密码、收提醒用）：已经有登录密码，不用再填；开发模式给出验证码
await page.locator('[data-acct-bind]').click()
await page.waitForSelector('.dlg-back.on')
ok(await page.locator('.dlg input[type="password"]').count() === 0, '绑邮箱不用再填登录密码')
await page.locator('.dlg input[type="email"]').fill(`${email}@example.com`)
await page.locator('.dlg .btn.primary').click()
await page.waitForSelector('text=验证邮箱')
const note = await page.locator('.dlg .au-note').innerText()
const code = note.match(/\d{6}/)?.[0]
ok(!!code && note.includes('开发模式'), '开发模式标明并给出验证码 ' + code)
await page.screenshot({ path: `${OUT}/flow-02-verify.png` })
await page.locator('.dlg').getByLabel('6 位验证码').fill(code)
await page.waitForSelector('.dlg-back.on', { state: 'detached' }).catch(() => {})
await page.waitForTimeout(500)
ok((await page.locator('.ac-row', { hasText: '邮箱' }).first().innerText()).includes('已验证'), '账户页：邮箱已验证')
ok(await page.locator('[data-papers] tbody tr').count() === 1, '注册送一个模拟账户')
ok((await page.locator('[data-papers] tbody tr').first().innerText()).includes('10,000'), '初始 10,000 USDT')
await page.screenshot({ path: `${OUT}/flow-03-account.png`, fullPage: true })

// 二级密码
await page.getByRole('button', { name: '设置二级密码' }).click()
await page.waitForSelector('.dlg-back.on')
await page.getByLabel('新的二级密码').fill('password-123')
await page.locator('.dlg .btn.primary').click()
await page.waitForSelector('.dlg .fld.bad')
ok((await page.locator('.dlg .fld.bad .fld-m').innerText()).includes('不能和登录密码一样'), '二级密码不能等于登录密码')
await page.screenshot({ path: `${OUT}/flow-04-tradepw.png` })
await page.getByLabel('新的二级密码').fill('trade-pw-1')
await page.locator('.dlg .btn.primary').click()
await page.waitForSelector('.dlg-back', { state: 'detached' })
ok((await page.locator('.ac-row.single .ac-tag').innerText()).includes('已设置'), '二级密码已设置')

// 宽屏分两栏时内容只能往下长，不能往右跑出屏幕
{
  const over = await page.locator('.ac').evaluate((e) => e.scrollWidth - e.clientWidth)
  ok(over <= 1, `账户页没有横向溢出（${over}）`)
  const box = await page.getByText('TradingView 信号', { exact: false }).first().boundingBox()
  ok(box && box.x < page.viewportSize().width, '信号设置在屏幕宽度之内')
}

// 模拟账户：新建、改名、重置
await page.getByRole('button', { name: '+ 新建', exact: true }).click()
await page.waitForFunction(() => document.querySelectorAll('[data-papers] tbody tr').length === 2)
ok(true, '新建模拟账户')
await page.locator('[data-papers] tbody tr').first().getByRole('button', { name: '改名' }).click()
await page.getByRole('dialog').getByLabel('名称').fill('网格测试')
await page.keyboard.press('Enter')
await page.waitForFunction(() => document.querySelector('[data-papers] tbody tr .ac-name')?.textContent === '网格测试')
ok(true, '改名')
await page.locator('[data-papers] tbody tr').first().getByRole('button', { name: '重置' }).click()
await page.waitForSelector('.dlg-back.on')
await page.screenshot({ path: `${OUT}/flow-05-reset.png` })
// 重置不要二级密码（D103，"不再限制额度和重置，完全自由"）；二级密码的锁定由 Go 测试 TestTradePassword 覆盖
ok(await page.locator('.dlg input[type="password"]').count() === 0, '重置不用填二级密码')
await page.locator('.dlg .btn.danger').click()
await page.waitForSelector('.dlg-back', { state: 'detached' })
ok(await page.locator('[data-papers] tbody tr').count() === 2, '重置后数量不变')
// Esc 关闭对话框
await page.locator('[data-papers] tbody tr').first().getByRole('button', { name: '改名' }).click()
await page.waitForSelector('.dlg-back.on')
await page.keyboard.press('Escape')
await page.waitForSelector('.dlg-back', { state: 'detached' })
ok(true, 'Esc 关闭对话框')

// 语言
await page.locator('.bar-lang button[data-tab="en"]').click()
await page.waitForFunction(() => document.querySelector('.ac-title')?.textContent === 'Account')
ok(await page.evaluate(() => document.documentElement.lang) === 'en', '切换英文即时生效')
await page.waitForTimeout(300)
await page.evaluate(() => localStorage.removeItem('qs.lang'))
await page.reload()
await page.waitForSelector('.ac-title')
await page.waitForFunction(() => document.querySelector('.ac-title')?.textContent === 'Account')
ok(true, '语言记在账户里（清掉浏览器记录后刷新仍是英文）')
await page.screenshot({ path: `${OUT}/flow-06-account-en.png`, fullPage: true })

// 退出、登录
await page.getByRole('button', { name: 'Sign out' }).last().click()
await page.waitForSelector('.au-card')
ok(true, '退出后回到登录页')
await page.getByLabel('Account name').fill(email)
await page.getByLabel('Password', { exact: true }).fill('wrong-password')
await page.locator('.au-submit').click()
await page.waitForSelector('.form-err.on')
ok((await page.locator('.form-err').innerText()).includes('Wrong account name or password'), '登录失败提示')
await page.screenshot({ path: `${OUT}/flow-07-login-err.png` })
await page.getByLabel('Password', { exact: true }).fill('password-123')
await page.locator('.au-submit').click()
await page.waitForSelector('.bar-user')
ok(true, '账户名登录成功')
const state = await ctx.storageState()

// 登录页没有钱包、没有邮箱（D113）；英文浏览器默认英文
{
  const wctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1, locale: 'en-US' })
  await wctx.addInitScript(() => localStorage.setItem('qs.wizardSeen', '1'))
  const wp = await wctx.newPage()
  watch(wp)
  await wp.goto(BASE + '/?page=login')
  await wp.waitForSelector('.au-card')
  ok(await wp.locator('.au-title').innerText() === 'Sign in', '英文浏览器默认英文')
  ok(await wp.locator('button[data-tab="wallet"]').count() === 0 && await wp.locator('input[type="email"]').count() === 0, '登录页没有钱包、没有邮箱，只有账户名和密码')
  await wp.screenshot({ path: `${OUT}/flow-08-login-en.png` })
  await wctx.close()
}

// 1.11 改登录密码、1.14 删除账户（密码都是随机的，只在这个测试账号上用）
{
  const rnd = () => Math.random().toString(36).slice(2)
  const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, locale: 'zh-CN', timezoneId: 'Asia/Taipei' })
  await c.addInitScript(() => localStorage.setItem('qs.wizardSeen', '1'))
  const q = await c.newPage()
  const call = (method, path, body) => q.evaluate(async ({ method, path, body }) => { const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined }); return { s: r.status, b: await r.json().catch(() => null) } }, { method, path, body })
  await q.goto(BASE + '/?page=login')
  const email = `del${Date.now()}${rnd().slice(0, 4)}@example.com`, pw0 = 'pw-' + rnd() + rnd(), pw1 = 'pw-' + rnd() + rnd(), tp = 'tp-' + rnd()
  await call('POST', '/api/auth/register', { email, password: pw0 })
  await q.goto(BASE + '/?page=account')
  await q.waitForSelector('[data-papers] tbody tr')
  await q.getByRole('button', { name: '修改登录密码' }).click()
  await q.waitForSelector('.dlg')
  await q.locator('.dlg input').nth(0).fill('wrong-password-1')
  await q.locator('.dlg input').nth(1).fill(pw1)
  await q.locator('.dlg').getByRole('button', { name: '保存' }).click()
  await q.waitForTimeout(600)
  ok(/当前登录密码不对/.test(await q.locator('.dlg').innerText()), '改登录密码：旧密码不对写清楚')
  await q.locator('.dlg input').nth(0).fill(pw0)
  await q.locator('.dlg').getByRole('button', { name: '保存' }).click()
  await q.waitForSelector('.dlg', { state: 'detached', timeout: 4000 }).catch(() => {})
  ok(await q.locator('.dlg').count() === 0, '改登录密码：成功后关掉对话框')
  await q.getByRole('button', { name: '删除账户' }).scrollIntoViewIfNeeded()
  ok(await q.getByRole('button', { name: '删除账户' }).isDisabled(), '没设二级密码时不能删除账户')
  await call('POST', '/api/me/trade-password', { new: tp })
  await q.reload()
  await q.waitForSelector('[data-papers] tbody tr')
  await q.getByRole('button', { name: '删除账户' }).click()
  await q.waitForSelector('.dlg')
  ok(/找不回来/.test(await q.locator('.dlg').innerText()), '删除前写明找不回来')
  await q.screenshot({ path: `${OUT}/account-delete.png` })
  await q.locator('.dlg input').fill(tp)
  await q.locator('.dlg').getByRole('button', { name: '删除账户' }).click()
  await q.waitForURL(/page=login/, { timeout: 6000 }).catch(() => {})
  ok(/page=login/.test(q.url()), '删除后回到登录页')
  ok((await call('POST', '/api/auth/login', { email, password: pw1 })).s === 401, '删除后不能再登录')
  await c.close()
}

// 16 种设备：登录页、账户页
const res = []
for (const d of DEVICES) {
  for (const [name, url, st] of [['login', '/?page=login', undefined], ['account', '/?page=account', state]]) {
    const c = await browser.newContext({ viewport: { width: d.w, height: d.h }, deviceScaleFactor: d.dpr, isMobile: d.w < 500, hasTouch: d.w < 500, storageState: st, locale: 'zh-CN' })
await c.addInitScript(() => localStorage.setItem('qs.wizardSeen', '1'))
    const p = await c.newPage()
    const e2 = []
    p.on('pageerror', (e) => e2.push(String(e)))
    await p.goto(BASE + url)
    await p.waitForSelector(name === 'login' ? '.au-card' : '[data-papers] tbody tr')
    await p.evaluate(() => document.fonts.ready)
    await p.waitForTimeout(500)
    const [ov] = await p.evaluate(() => [document.documentElement.scrollWidth - document.documentElement.clientWidth])
    // 键盘焦点样式：Tab 到第一个可聚焦元素，检查有轮廓或阴影变化
    await p.keyboard.press('Tab')
    const focus = await p.evaluate(() => { const a = document.activeElement; if (!a || a === document.body) return 'none'; const s = getComputedStyle(a); return s.outlineStyle !== 'none' ? 'outline' : 'other' })
    await p.screenshot({ path: `${OUT}/${name}-${d.id}.png` })
    res.push(`${name.padEnd(8)} ${d.id.padEnd(16)} ${d.w}×${d.h}@${d.dpr} ${ov > 0 ? '横向溢出 ' + ov + 'px ' : ''}${e2.length ? '报错 ' + e2.join('|') : 'ok'} 焦点:${focus}`)
    await c.close()
  }
}
console.log(res.join('\n'))
console.log(errs.length ? '页面报错：\n' + errs.join('\n') : '流程中无页面报错')
await browser.close()
