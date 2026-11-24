// 管理后台（D89 第 5 条）走一遍：角色、全局停机两档（真的走下单路径）、注册方式、添加和停用用户、策略、操作记录。
// 第一个注册的人是所有者，所以要对着刚清空的测试库跑（tools/dev/regress.sh admin）。不要对着 8080。
// 用法：node tools/e2e-admin.mjs <截图目录> [http://127.0.0.1:4173]
import { chromium } from 'playwright'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const rand = () => Math.random().toString(36).slice(2)
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const until = async (fn, ms = 15000) => {
  const t0 = Date.now()
  while (Date.now() - t0 < ms) {
    const v = await fn().catch(() => null)
    if (v) return v
    await sleep(250)
  }
  return null
}
const newPage = async (opts = {}) => {
  const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN', ...opts })
  await c.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1') })
  const p = await c.newPage()
  p.errs = []
  p.on('pageerror', (e) => p.errs.push(String(e)))
  p.on('console', (m) => { if (m.type() === 'error' && !/Failed to load resource/.test(m.text())) p.errs.push(m.text()) })
  return p
}
const call = (p, method, path, body) => p.evaluate(async ({ method, path, body }) => {
  const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
  let j = null
  try { j = await r.json() } catch { /* 不是 JSON */ }
  return { s: r.status, b: j }
}, { method, path, body })
const pw = () => `ad-${rand()}-pass`
const register = async (p, email, password) => {
  await p.goto(BASE + '/?page=login')
  const r = await call(p, 'POST', '/api/auth/register', { email, password })
  if (r.s !== 200) throw new Error('注册失败 ' + JSON.stringify(r))
  return r.b.user
}
const TP = 'tp-' + rand()
const shot = (p, name, el) => (el ? p.locator(el).first().screenshot({ path: `${OUT}/${name}.png` }) : p.screenshot({ path: `${OUT}/${name}.png` }))
// 提示条是同一个元素换文字：等到出现想要的那句（不然会读到上一条）
const toastLike = async (p, re) => (await until(async () => { const n = await p.locator('.toast').count(); const s = n ? await p.locator('.toast').last().innerText() : ''; return re.test(s) ? s : null }, 8000)) ?? ''

// ---------- 两个账号：第一个是所有者（D113） ----------
const o = await newPage()
await register(o, `first${Date.now()}@example.com`, pw())
await call(o, 'POST', '/api/me/trade-password', { new: TP })
const b = await newPage()
const bEmail = `second${Date.now()}@example.com`, bPass = pw()
const bu = await register(b, bEmail, bPass)
ok(!!bu && bu.role === undefined, '账号信息里没有角色')
await call(b, 'POST', '/api/me/trade-password', { new: TP })
await call(b, 'POST', '/api/discipline/enabled', { on: false, trade_password: TP })
const BA = (await call(b, 'GET', '/api/paper-accounts')).b.accounts[0].id

// 只有所有者（第一个注册的账号）能进"系统"页（D113，11-24）：后注册的账号看不到全部账号、改不了全局设置
await b.goto(BASE + '/?page=account')
await b.waitForSelector('.ac-in')
ok(await b.locator('[data-admin-entry]').count() === 0 && await b.locator('[data-sys-entry]').count() === 0, '后注册的账号：账户页、顶栏都没有"系统"入口')
ok((await call(b, 'GET', '/api/admin')).s === 403 && (await call(b, 'GET', '/api/admin/settings')).s === 403, '后注册的账号：系统页接口都拒（403）')
await b.goto(BASE + '/?page=admin')
ok(!!(await until(async () => (await b.locator('[data-admin-forbidden]').count()) > 0)) && await b.locator('[data-admin-users]').count() === 0, '后注册的账号直接打开"系统"页：写明只有所有者能看，看不到账号表')

// ---------- "系统"页 ----------
await o.goto(BASE + '/?page=account')
await o.locator('[data-admin-entry]').click()
await o.waitForSelector('[data-admin-users]')
await o.waitForTimeout(900) // 等页面切换的淡入淡出走完再截图
ok(/系统/.test(await o.locator('h1.ac-title').innerText()), '页面叫"系统"，不叫管理后台')
const rows = await o.locator('[data-admin-users] tbody tr').count()
ok(rows === 2, `账号表有 2 个（${rows}）`)
const headTxt = await o.locator('[data-admin-users] thead').innerText()
ok(!/角色/.test(headTxt) && await o.locator('[data-admin-users] tbody button').count() === 0, '账号表没有角色、没有管别人的按钮')
ok(!!(await until(async () => (await o.locator('[data-admin-strat]').count()) > 0)), '策略表里有接入的策略')
await shot(o, 'adm-01-page')
const ask = o.locator('[data-admin-ask]')

// ---------- 全局停机第一档：真的走下单路径 ----------
const INST = 'BTC-PERP'
let r = await call(b, 'POST', `/api/trade/${BA}/orders`, { instrument: INST, side: 'buy', type: 'market', qty: '0.01' })
ok(r.s === 200, `停机前开一点仓（${r.b?.status ?? r.b?.code}）`)
await o.locator('.adm-reason input').fill('测试：交易所维护')
await o.locator('[data-admin-halt1]').click()
ok(!!(await toastLike(o, /已停止开新仓/)), '停止开新仓：不要二级密码')
ok(!!(await until(async () => (await o.locator('[data-admin-halt="1"]').count()) > 0)), '状态变成已停止开新仓')
r = await call(b, 'POST', `/api/trade/${BA}/orders`, { instrument: INST, side: 'buy', type: 'market', qty: '0.01' })
ok(r.b?.code === 'SYSTEM_HALTED', `停机后开仓被拒（${r.s} ${r.b?.code}）`)
r = await call(b, 'POST', `/api/trade/${BA}/orders`, { instrument: INST, side: 'sell', type: 'market', qty: '0.005', reduce_only: true })
ok(r.s === 200, `停机后减仓照常（${r.b?.status ?? r.b?.code}）`)
// 每个人的顶栏都亮标记；下单区直接说原因
await b.goto(BASE + '/?page=trade&i=' + INST)
await b.waitForSelector('.op-submit')
ok(!!(await until(async () => (await b.locator('[data-halt-mark="1"]').count()) > 0, 25000)), '别人的顶栏亮着"停止开新仓"')
await b.locator('.op-row .seg button[data-tab="market"]').click()
await b.locator('.op-unit button', { hasText: 'BTC' }).first().click().catch(() => {}) // 数量按 BTC 填
await b.locator('[aria-label="数量"]').fill('0.01')
await b.waitForTimeout(400)
await b.locator('.op-submit').click()
const cf = b.locator('.dlg[aria-label="确认下单"] .btn.primary')
if (await cf.count()) await cf.click()
const opMsg = await until(async () => { const s = await b.locator('.op').innerText(); return /已停止开新仓（在"系统"页里停的）/.test(s) && !/管理员/.test(s) ? s : null })
ok(!!opMsg, `下单区写明已停止开新仓、不提管理员（${opMsg ? '' : (await b.locator('.op').innerText()).replace(/\s+/g, ' ').slice(0, 200)}）`)
await shot(b, 'adm-03-halted-trade', '.bar')

// ---------- 第二档：撤掉开仓挂单（合约限价单、现货买单） ----------
await call(o, 'POST', '/api/admin/halt', { level: 0, trade_password: TP })
const mark = Number((await call(b, 'GET', `/api/trade/${BA}`)).b.positions?.[0]?.mark ?? 0) || 70000
r = await call(b, 'POST', `/api/trade/${BA}/orders`, { instrument: INST, side: 'buy', type: 'limit', price: (Math.round(mark * 0.985 * 10) / 10).toFixed(1), qty: '0.01' })
ok(r.s === 200 && r.b.status === 'new', `挂一张开仓限价单（${r.b?.status ?? r.b?.code}）`)
await call(b, 'POST', `/api/spot/${BA}/transfer`, { to: 'spot', amount: '2000' })
const spx = Number((await call(b, 'GET', '/api/spot/specs')).b.find?.((s) => s.instrument === 'BTC-SPOT')?.last ?? 0) || mark
r = await call(b, 'POST', `/api/spot/${BA}/orders`, { instrument: 'BTC-SPOT', side: 'buy', type: 'limit', price: (Math.floor(spx * 0.97 * 100) / 100).toFixed(2), qty: '0.01' })
ok(r.s === 200 && r.b.status === 'new', `挂一张现货买单（${r.b?.status ?? r.b?.code}）`)
await o.reload()
await o.waitForSelector('[data-admin-halt2]')
await o.locator('[data-admin-halt2]').click()
const t2 = await toastLike(o, /撤了/)
ok(/撤了 [1-9]\d* 张/.test(t2), `第二档：撤了开仓挂单（${t2}）`)
const perpLeft = (await call(b, 'GET', `/api/trade/${BA}`)).b.orders.filter((x) => !x.reduce_only && !x.close_position).length
const spotLeft = ((await call(b, 'GET', `/api/spot/${BA}`)).b.orders ?? []).filter((x) => x.side === 'buy').length
ok(perpLeft === 0 && spotLeft === 0, `合约开仓单和现货买单都撤了（剩 ${perpLeft} / ${spotLeft}）`)
const posLeft = (await call(b, 'GET', `/api/trade/${BA}`)).b.positions.length
ok(posLeft === 1, '仓位不动（停机只拦开新仓）')
await shot(o, 'adm-04-halt2', '.ac-sec')

// 恢复要二级密码
await o.locator('[data-admin-resume]').click()
await ask.waitFor()
await ask.locator('input[type="password"]').fill(TP)
await ask.locator('[data-admin-ask-ok]').click()
ok(!!(await until(async () => (await o.locator('[data-admin-halt="0"]').count()) > 0)), '恢复交易')
r = await call(b, 'POST', `/api/trade/${BA}/orders`, { instrument: INST, side: 'buy', type: 'market', qty: '0.01' })
ok(r.s === 200, `恢复后能开仓（${r.b?.status ?? r.b?.code}）`)

// ---------- 注册方式、添加账号 ----------
// 关注册：往安全的方向，不要二级密码（D107）
await o.locator('.ac-sec', { hasText: '注册方式' }).locator('.seg button', { hasText: '关闭' }).click()
await ask.waitFor()
ok(await ask.locator('input[type="password"]').count() === 0, '关注册不要二级密码（往安全的方向，D107）')
await shot(o, 'adm-02-ask', '.dlg')
await ask.locator('[data-admin-ask-ok]').click()
await until(async () => (await o.locator('[data-admin-ask]').count()) === 0)
const x = await newPage()
await x.goto(BASE + '/?page=login&mode=register')
ok(!!(await until(async () => (await x.locator('[data-reg-closed]').count()) > 0)), '注册页直接写明注册关着')
ok(!/管理员/.test(await x.locator('main').innerText()), '注册页不再提"管理员"')
let r2 = await call(x, 'POST', '/api/auth/register', { email: `x${Date.now()}@example.com`, password: pw() })
ok(r2.s === 403 && r2.b?.code === 'REGISTRATION_CLOSED', `接口也拒（${r2.s} ${r2.b?.code}）`)
await shot(x, 'adm-05-reg-closed')
const cEmail = `added${Date.now()}@example.com`
await o.locator('[data-admin-add]').click()
const add = o.locator('[data-admin-add-form]')
await add.waitFor()
await add.locator('input').first().fill(cEmail)
await add.locator('[data-admin-gen]').click()
const cPass = await add.locator('input').nth(1).inputValue()
ok(cPass.length >= 10, '生成了初始密码')
await add.locator('input[type="password"]').fill('wrong-' + rand())
await add.locator('[data-admin-add-ok]').click()
ok(!!(await until(async () => /二级密码/.test(await add.innerText()))), '添加账号：二级密码错写在框下面')
await add.locator('input[type="password"]').fill(TP)
await shot(o, 'adm-06-add', '.dlg')
await add.locator('[data-admin-add-ok]').click()
ok(!!(await toastLike(o, /已添加/)), '添加了账号')
r2 = await call(x, 'POST', '/api/auth/login', { email: cEmail, password: cPass })
ok(r2.s === 200, `添加的账号用初始密码能登录（${r2.s}）`)

// ---------- 操作记录 ----------
await o.reload()
await o.waitForSelector('[data-admin-log]')
const acts = await o.locator('[data-admin-log] tbody tr').evaluateAll((rs) => rs.map((r) => r.getAttribute('data-admin-log-row')))
ok(['halt', 'registration', 'user_add'].every((a) => acts.includes(a)), `每一步都记了（${acts.join(' ')}）`)
ok(/撤了 \d+ 张开仓挂单（?/.test(await o.locator('[data-admin-log]').innerText()), '记录写成人话（撤了几张）')
await o.locator('[data-admin-log]').scrollIntoViewIfNeeded()
await shot(o, 'adm-08-log')

// ---------- 手机 ----------
const m = await newPage({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true })
await m.context().addCookies(await o.context().cookies())
await m.goto(BASE + '/?page=admin')
await m.waitForSelector('[data-admin-users]')
const over = await m.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
ok(over <= 1, `手机宽度不横向滚动（多出 ${over}px）`)
await shot(m, 'adm-09-phone')

// 收尾：把注册打开回来，免得后面的检查脚本注册不了
await call(o, 'POST', '/api/admin/registration', { mode: 'open', trade_password: TP })
ok([o, b, x, m].every((p) => p.errs.length === 0), `页面没有报错（${[o, b, x, m].flatMap((p) => p.errs).join(' | ').slice(0, 300)}）`)
await browser.close()
console.log(`\n${pass} 通过，${fail} 失败`)
process.exit(fail ? 1 : 0)
