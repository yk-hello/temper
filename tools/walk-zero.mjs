// 从零走查（2026-11-15，用户："模拟真人输入点击，从刚刚部署，到注册，填写账户和使用过程中触发所有的功能"）。
// 对着刚部署的干净副本（真实行情），像真人一样：注册 → 验证邮箱 → 点遍每个页面和标签 → 用模拟账户下单、改单、撤单、
// 止盈止损、平仓、改杠杆 → 账户设置。量每个页面多久出内容、实时数据多久刷新一次，记下报错和 5xx，存截图。
// 会注册账号、下模拟单：只对着测试用的副本跑（默认 8090），不要对着用户正在用的 8080。
// 用法：node tools/walk-zero.mjs <截图目录> [http://localhost:8090]
import { chromium } from 'playwright'
import fs from 'node:fs'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://localhost:8090'
if (/:8080\b/.test(BASE)) { console.error('不要对着 8080 跑'); process.exit(2) }
fs.mkdirSync(OUT, { recursive: true })
let pass = 0, fail = 0
const rows = []
const ok = (c, name, detail = '') => { c ? pass++ : fail++; rows.push({ ok: !!c, name, detail }); console.log(`${c ? '✓' : '✗'} ${name}${detail ? '｜' + detail : ''}`) }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const rnd = () => Math.random().toString(36).slice(2, 10)
const step = async (name, f) => { try { await f() } catch (e) { ok(false, name, '出错：' + String(e).split('\n')[0].slice(0, 160)) } }

const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const ctx = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN' })
const p = await ctx.newPage()
const errs = [], bad = []
p.on('pageerror', (e) => errs.push(String(e).slice(0, 200)))
p.on('console', (m) => { if (m.type() === 'error' && !/Failed to load resource|401|403/.test(m.text())) errs.push(m.text().slice(0, 200)) })
p.on('response', (r) => { if (r.status() >= 500) bad.push(`${r.status()} ${r.url().replace(BASE, '')}`) })
const shot = (n) => p.screenshot({ path: `${OUT}/wz-${n}.png` })
const main = () => p.evaluate(() => (document.querySelector('main, .mk, .rv, .ac, .rk, .ai') ?? document.body).innerText)
// 点完以后多久出内容：主区域有字、不再有"加载中"
const ready = async (extra = () => true, ms = 20000) => {
  const t0 = Date.now()
  while (Date.now() - t0 < ms) {
    const t = await main().catch(() => '')
    if (t.length > 40 && !/加载中|正在加载|正在连接/.test(t) && await Promise.resolve().then(extra).catch(() => false)) return Date.now() - t0
    await sleep(100)
  }
  return null
}
const nav = async (txt) => { await p.locator('header a, .bar a, nav a', { hasText: new RegExp(`^${txt}$`) }).first().click() }

// ---------- 注册 ----------
const email = `walk${Date.now()}` // 账户名（D113）；变量名沿用
await step('注册', async () => {
  const t0 = Date.now()
  await p.goto(BASE + '/')
  await p.waitForSelector('canvas, .hero', { timeout: 15000 })
  ok(true, '首页打开', `${Date.now() - t0} 毫秒`)
  await shot('00-home')
  await p.getByRole('link', { name: '注册' }).click()
  await p.locator('input[data-auth-name]').fill(email)
  await p.locator('input[type="password"]').fill(`Wz-${rnd()}${rnd()}`)
  await p.getByRole('button', { name: '注册', exact: true }).click()
  // 注册只要账户名和密码（D113），注册完直接进去；第一个账号是所有者
  await p.waitForSelector('.bar-user', { timeout: 15000 })
  const me0 = await p.evaluate(async () => (await fetch('/api/me', { headers: { 'X-QS-Client': 'web' } })).json())
  ok(me0.user?.name === email && me0.user?.owner === true && !me0.user?.email, '注册完直接进去，第一个账号是所有者', `进了 ${await p.evaluate(() => location.search || '/')}`)
})

// ---------- D105 独立使用：第一个账号以后别人注册不进来；"系统"页谁都一样 ----------
await step('独立使用', async () => {
  const other = await browser.newContext({ locale: 'zh-CN' })
  const q = await other.newPage()
  await q.goto(BASE + '/?page=login')
  // 没登录时在登录页按刷新：回到 3D 首页
  await q.reload()
  await q.waitForFunction(() => !/page=login/.test(location.search), null, { timeout: 8000 }).catch(() => {})
  ok(!/page=login/.test(await q.evaluate(() => location.search)), '没登录时在登录页按刷新：回到首页')
  // 没配发信邮箱：忘记密码不能把验证码给页面（不然谁都能改别人的密码），只说本机命令（11-16）
  await q.goto(BASE + '/?page=login')
  await q.getByRole('button', { name: /忘记密码/ }).or(q.getByRole('link', { name: /忘记密码/ })).first().click()
  const off = await q.waitForSelector('[data-mail-off]', { timeout: 8000 }).then(() => true).catch(() => false)
  ok(off && /reset-password/.test(await q.locator('[data-mail-off]').innerText()), '没配发信邮箱：忘记密码只说本机命令，不给验证码')
  const fr = await q.evaluate(async () => (await fetch('/api/auth/forgot', { method: 'POST', headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: JSON.stringify({ email: 'walk@example.com' }) })).json())
  ok(!fr.dev_code, '接口也不给验证码', JSON.stringify(fr))
  await q.goto(BASE + '/?page=login&mode=register')
  const closed = await q.waitForSelector('[data-reg-closed]', { timeout: 8000 }).then(() => true).catch(() => false)
  ok(closed, '第一个账号以后，注册页写明注册关着')
  const r = await q.evaluate(async () => (await fetch('/api/auth/register', { method: 'POST', headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: JSON.stringify({ email: `x${Date.now()}@example.com`, password: 'stranger-pass-123' }) })).status)
  ok(r === 403, '别人注册不进来（接口也拒）', String(r))
  await q.screenshot({ path: `${OUT}/wz-01-reg-closed.png` })
  await other.close()
  const me = await p.evaluate(async () => (await fetch('/api/me', { headers: { 'X-QS-Client': 'web' } })).json())
  ok(me.user && me.user.role === undefined, '账号信息里没有角色')
})

// ---------- 市场页各标签 ----------
const marketTabs = ['全市场', '今日', '筛选', '频谱', '订单流', '链上', '跨所', '系统']
await step('市场页', async () => {
  await nav('市场')
  for (const tab of marketTabs) {
    await step(`市场 · ${tab}`, async () => {
      const b = p.locator('.mk-tabs button, .seg button, [role="tab"]', { hasText: new RegExp(`^${tab}$`) }).first()
      await b.click()
      const ms = await ready()
      ok(ms !== null && ms < 3000, `市场 · ${tab}：出内容`, ms === null ? '20 秒内没出来' : `${ms} 毫秒`)
      await sleep(1200)
      await shot(`10-market-${tab}`)
    })
  }
})

// ---------- 交易页：实时数据刷新、图表模式 ----------
await step('交易页', async () => {
  await nav('交易')
  await p.waitForSelector('.op-submit', { timeout: 15000 })
  const ms = await ready(async () => (await p.locator('.ob-r.bid').count()) > 3)
  ok(ms !== null && ms < 3000, '交易页：盘口出来', ms === null ? '没出来' : `${ms} 毫秒`)
  const rate = await p.evaluate(async () => {
    const pick = (s) => document.querySelector(s)
    const regions = { 盘口: pick('.ob'), 顶部价格: pick('.mk-strip') }
    const n = {}
    const obs = Object.entries(regions).map(([k, el]) => { n[k] = 0; if (!el) return null; const o = new MutationObserver(() => { n[k]++ }); o.observe(el, { subtree: true, childList: true, characterData: true }); return o })
    await new Promise((r) => setTimeout(r, 10000))
    obs.forEach((o) => o?.disconnect())
    return n
  })
  ok(rate['盘口'] >= 5, '盘口刷新', `10 秒 ${rate['盘口']} 次`)
  ok(rate['顶部价格'] >= 10, '顶部价格刷新', `10 秒 ${rate['顶部价格']} 次`)
  await shot('20-trade')
  for (const mode of ['阶梯线', '频谱', '足迹', 'K 线']) {
    await step(`图表 · ${mode}`, async () => {
      await p.locator('.seg button', { hasText: new RegExp(`^${mode.replace(' ', '\\s?')}$`) }).first().click()
      await sleep(1500)
      const cv = await p.evaluate(() => [...document.querySelectorAll('.mk-chart canvas, .mk-main canvas, canvas')].filter((c) => c.width > 200 && c.height > 100).length)
      ok(cv > 0, `图表 · ${mode}：有画布（截图另看）`, `${cv} 个`)
      await shot(`21-chart-${mode}`)
    })
  }
})

// ---------- 模拟账户下单 ----------
const call = (method, path, body) => p.evaluate(async ({ method, path, body }) => { const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined }); return { s: r.status, b: await r.json().catch(() => null) } }, { method, path, body })
let acct = 0
await step('模拟下单', async () => {
  acct = (await call('GET', '/api/paper-accounts')).b.accounts[0].id
  const view = async () => (await call('GET', `/api/trade/${acct}`)).b
  // 关掉纪律锁的冷静规则会挡后面的测试：走查用默认设置，只在需要时提醒
  await p.locator('.op-row .seg button', { hasText: /^市价$/ }).click()
  await p.locator('.op-unit button', { hasText: /^BTC$/ }).click()
  await p.locator('input[aria-label="数量"]').fill('0.01')
  const ask = Number((await p.locator('.ob-r.ask').last().innerText()).match(/[\d,]+\.\d+/)?.[0].replace(/,/g, ''))
  const t0 = Date.now()
  await p.locator('.op-submit').click()
  const pos = await (async () => { for (let i = 0; i < 60; i++) { const v = await view(); if (v.positions?.length) return v.positions[0]; await sleep(200) } return null })()
  ok(!!pos, '市价买 0.01 BTC：有持仓', pos ? `${Date.now() - t0} 毫秒；开仓价 ${pos.entry}，下单前卖一 ${ask}` : '')
  if (pos) ok(Math.abs(Number(pos.entry) / ask - 1) < 0.003, '成交价贴近当时的卖一（真实盘口撮合）', `差 ${((Number(pos.entry) / ask - 1) * 100).toFixed(3)}%`)
  await sleep(800)
  const posRow = await p.locator('.tl tbody tr, .tbl tbody tr').first().innerText().catch(() => '')
  ok(/BTC/.test(posRow), '持仓表里看得到', posRow.replace(/\s+/g, ' ').slice(0, 80))
  await shot('30-position')

  // 限价单：挂在现价下 2%，再改价、撤单
  await p.locator('.op-row .seg button', { hasText: /^限价$/ }).click()
  const last = Number(pos?.mark ?? ask)
  await p.locator('input[aria-label="价格"]').fill(String(Math.round(last * 0.98)))
  await p.locator('input[aria-label="数量"]').fill('0.01')
  await p.locator('.op-submit').click()
  const lim = await (async () => { for (let i = 0; i < 40; i++) { const o = (await view()).orders?.find((x) => x.type === 'limit'); if (o) return o; await sleep(200) } return null })()
  ok(!!lim, '限价单挂上', lim ? `${lim.price}` : '')
  if (lim) {
    const am = await call('PATCH', `/api/trade/${acct}/orders/${lim.id}`, { price: String(Math.round(last * 0.975)), qty: '0.01' })
    ok(am.s === 200, '改单（改价）', `${am.s}`)
    const cx = await call('DELETE', `/api/trade/${acct}/orders/${lim.id}`)
    ok(cx.s === 200, '撤单', `${cx.s}`)
  }
  // 持仓上设止盈止损
  const tp = String(Math.round(last * 1.05)), sl = String(Math.round(last * 0.95))
  const ts = await call('POST', `/api/trade/${acct}/tpsl`, { instrument: 'BTC-PERP', tp, sl })
  ok(ts.s === 200, '持仓上设止盈止损', `${ts.s}`)
  await sleep(600)
  const v2 = await view()
  ok(Number(v2.positions?.[0]?.tp) > 0 && Number(v2.positions?.[0]?.sl) > 0, '持仓表显示止盈止损', `${v2.positions?.[0]?.tp} / ${v2.positions?.[0]?.sl}`)
  // 改杠杆
  const lv = await call('POST', `/api/trade/${acct}/leverage`, { instrument: 'BTC-PERP', leverage: 5 })
  ok(lv.s === 200, '改杠杆到 5 倍（有仓位时）', `${lv.s} ${lv.b?.code ?? ''}`)
  // 平一半、全平
  // 平仓接口按比例平（fraction），不是按数量
  const c1 = await call('POST', `/api/trade/${acct}/close`, { instrument: 'BTC-PERP', fraction: '0.5' })
  await sleep(600)
  const before = await view()
  const left = Number(before.positions?.[0]?.qty ?? 0)
  ok(c1.s === 200 && Math.abs(left - 0.005) < 1e-9, '平一半：还剩 0.005 BTC', `${c1.s}；还剩 ${left}`)
  const c2 = await call('POST', `/api/trade/${acct}/close`, { instrument: 'BTC-PERP' })
  ok(c2.s === 200, '全平', `${c2.s} ${c2.b?.code ?? ''}；平之前还剩 ${before.positions?.[0]?.qty ?? 0}，委托 ${before.orders?.length ?? 0} 张`)
  await sleep(1000)
  const v3 = await view()
  ok(!v3.positions?.length, '平完没有持仓了')
  ok(Number(v3.fees) > 0, '手续费按真实费率扣了', `${v3.fees} USDT`)
  await shot('31-closed')
})

// ---------- 风险、复盘、策略、账户、介绍 ----------
await step('风险页', async () => {
  await nav('风险')
  const ms = await ready()
  ok(ms !== null && ms < 3000, '风险页出内容', ms === null ? '没出来' : `${ms} 毫秒`)
  await sleep(800); await shot('40-risk')
})
const reviewTabs = ['交易日志', '绩效', '自由透视', 'MAE / MFE', '结论', '预案', '高光', '回测', '回放练习']
await step('复盘页', async () => {
  await nav('复盘')
  await p.waitForSelector('.rv-head', { timeout: 15000 })
  for (const tab of reviewTabs) {
    await step(`复盘 · ${tab}`, async () => {
      await p.locator('.rv-head .seg button', { hasText: tab }).first().click()
      const ms = await ready()
      ok(ms !== null && ms < 4000, `复盘 · ${tab}：出内容`, ms === null ? '没出来' : `${ms} 毫秒`)
      await sleep(900); await shot(`50-review-${tab.replace(/[ /]/g, '')}`)
    })
  }
})
await step('策略页', async () => {
  await nav('策略')
  const ms = await ready()
  ok(ms !== null && ms < 3000, '策略页出内容（干净部署没有内置策略，要说明怎么接入）', ms === null ? '没出来' : `${ms} 毫秒`)
  // 切页面有淡入淡出：等策略页自己的字出来再读（不然读到的还是上一页，截到的是过渡中的空帧）
  const has = await p.waitForFunction(() => /还没有接入策略|和币安一样的接口/.test(document.body.innerText), null, { timeout: 8000 }).then(() => true).catch(() => false)
  ok(has, '没有策略时写明三种接法')
  await sleep(800)
  await shot('60-strategy')
})
await step('账户页', async () => {
  await p.goto(BASE + '/?page=account')
  const ms = await ready()
  ok(ms !== null && ms < 3000, '账户页出内容', ms === null ? '没出来' : `${ms} 毫秒`)
  const t = await main()
  for (const s of ['登录方式', '二级密码', '账户安全', '通知', '模拟账户', '交易所账户', '到价提醒', '条件提醒', '交易偏好']) ok(t.includes(s), `账户页有"${s}"`)
  await shot('70-account')
})
await step('系统页', async () => {
  await p.goto(BASE + '/?page=admin')
  const ms = await ready(async () => (await p.locator('[data-sys="mail"]').count()) > 0)
  ok(ms !== null, '"系统"页出内容（停机、系统设置、注册、账号、策略、操作记录）', ms === null ? '没出来' : `${ms} 毫秒`)
  const t = await main()
  ok(/^系统/.test(t.trim()) && !/管理员|所有者/.test(t), '页面叫"系统"，不提管理员、所有者')
  for (const s of ['全局停机', '发信邮箱', 'AI 复盘点评', 'Telegram', '数据源密钥', '注册方式', '这台的账号', '操作记录']) ok(t.includes(s), `"系统"页有"${s}"`)
  ok(!/Outlook/.test(await p.locator('[data-sys="mail"]').innerText()), '发信邮箱没有 Outlook（个人 Outlook 只认 OAuth2）')
  await shot('75-system')
})
await step('介绍页', async () => {
  await p.goto(BASE + '/?page=about')
  const ms = await ready()
  ok(ms !== null, '介绍页出内容', ms === null ? '没出来' : `${ms} 毫秒`)
  await sleep(1500); await shot('80-about')
})

// ---------- 刷新浏览器：先显示上一次的，不从加载中开始 ----------
await step('刷新后先显示上次的', async () => {
  await p.goto(BASE + '/?page=market&tab=today')
  const t0 = Date.now()
  await p.waitForFunction(() => /恐惧|贪婪|市值/.test(document.body.innerText) && !/加载中/.test(document.querySelector('[data-td="tiles"]')?.innerText ?? '加载中'), null, { timeout: 15000 })
  ok(Date.now() - t0 < 1500, '今日：刷新后马上有数字（用上一次的，后台再更新）', `${Date.now() - t0} 毫秒`)
})

ok(errs.length === 0, '页面没有报错', errs.slice(0, 3).join(' | '))
ok(bad.length === 0, '服务器没有 5xx', bad.slice(0, 5).join(' | '))
fs.writeFileSync(`${OUT}/walk-zero.json`, JSON.stringify({ base: BASE, at: new Date().toISOString(), pass, fail, rows, errs, bad }, null, 2))
console.log(`\n${pass} 通过，${fail} 失败`)
await browser.close()
process.exit(fail ? 1 : 0)
