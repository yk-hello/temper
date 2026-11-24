// 交易所账户（D89）的检查：接入密钥 → 开启下单 → 每笔都要确认 → 仓位、撤单、复盘。
// 只能对着开了假交易所（QS_LIVE_FAKE）的测试环境跑：会注册账号、下单（不要对着 8080）。
//   node tools/e2e-live.mjs <截图目录> <网址>
// 假交易所的密钥是测试值，和测试环境的 QS_LIVE_FAKE 一样（环境变量 QS_E2E_LIVE 可以换）。
import { chromium } from 'playwright'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
const [FKEY, FSECRET] = (process.env.QS_E2E_LIVE || 'e2efakekey-0123456789abcd:e2efakesecret-0123456789abcd').split(':')
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const newPage = async (opts = {}) => {
  const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN', timezoneId: 'Asia/Taipei', ...opts })
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
const register = async (p) => {
  const r = await call(p, 'POST', '/api/auth/register', { email: `lv${Date.now()}${rand().slice(0, 4)}@example.com`, password: `lv-${rand()}-pass` })
  if (r.s >= 300) throw new Error('注册失败 ' + JSON.stringify(r))
  const tp = 'tp-' + rand()
  await call(p, 'POST', '/api/me/trade-password', { new: tp })
  await call(p, 'POST', '/api/discipline/enabled', { on: false, trade_password: tp })
  return tp
}
const toastText = async (p) => (await until(async () => { const n = await p.locator('.toast').count(); return n ? p.locator('.toast').last().innerText() : null }, 6000)) ?? ''
const INST = 'BTC-PERP'
const shot = (p, name, el) => (el ? p.locator(el).first().screenshot({ path: `${OUT}/${name}.png` }) : p.screenshot({ path: `${OUT}/${name}.png` }))

const p = await newPage()
await p.goto(BASE + '/?page=login')
const TP = await register(p)

// ---------- 账户页：没接之前 ----------
await p.goto(BASE + '/?page=account')
const sec = p.locator('.ac-sec', { has: p.locator('h2', { hasText: '交易所账户' }) })
await sec.waitFor()
const ov0 = await call(p, 'GET', '/api/live')
ok(ov0.s === 200 && ov0.b.enabled === true && ov0.b.envs.includes('fake'), `服务器开了实盘、有测试交易所（${JSON.stringify(ov0.b?.envs)}）`)
ok(await sec.locator('[data-live-add]').isEnabled(), '有二级密码时可以接入')
await sec.scrollIntoViewIfNeeded()
await shot(p, 'lv-01-empty', '.ac-sec:has(h2:text("交易所账户"))')

// ---------- 接入：先说清楚怎么建密钥 ----------
await sec.locator('[data-live-add]').click()
const dlg = p.locator('.dlg[aria-label="接入币安 U 本位合约"]')
await dlg.waitFor()
const steps = await dlg.locator('.lv-steps li').allInnerTexts()
ok(steps.length === 3 && /只勾选「允许合约」/.test(steps[0]) && /提现/.test(steps[1]) && /单向持仓/.test(steps[2]), '写清楚：只开合约、不开提现划转、单向持仓')
await dlg.locator('button', { hasText: '本机测试交易所' }).click()
await dlg.locator('[data-live-key]').fill('wrongkey-0123456789abcdef')
await dlg.locator('[data-live-secret]').fill(FSECRET)
await dlg.locator('[data-live-pw]').fill(TP)
await dlg.locator('[data-live-save]').click()
const wrong = await until(async () => { const t = await dlg.locator('.fld.bad .fld-m').first().innerText(); return t.trim() ? t : null })
ok(/币安不认这把密钥/.test(wrong ?? ''), `密钥不对：说清楚检查什么（${wrong}）`)
await shot(p, 'lv-02-wrong-key', '.dlg')
await dlg.locator('[data-live-key]').fill(FKEY)
await dlg.locator('[data-live-pw]').fill('not-the-password')
await dlg.locator('[data-live-save]').click()
ok(!!(await until(async () => /二级密码/.test(await dlg.locator('.fld.bad .fld-m').last().innerText()))), '二级密码错：标在密码框下面')
await dlg.locator('[data-live-pw]').fill(TP)
await dlg.locator('input').nth(0).fill('测试账户')
await dlg.locator('[data-live-save]').click()
ok(/已接入 测试账户/.test(await toastText(p)), '保存成功提示')
const row = sec.locator('[data-live-row]')
await row.first().waitFor()
const ov1 = await call(p, 'GET', '/api/live')
const LA = ov1.b.accounts[0]?.id
ok(LA >= 1e12 && ov1.b.keys[0].hint.includes('…') && !JSON.stringify(ov1.b).includes(FKEY), '账户编号从 1 万亿起；接口里看不到完整密钥')
ok(!!(await until(async () => /已连接/.test(await row.first().innerText()))), '连接状态：已连接')
ok(/未开启下单/.test(await row.first().innerText()), '还没开启下单')
await p.mouse.move(0, 0)
await shot(p, 'lv-03-connected', '.ac-sec:has(h2:text("交易所账户"))')

// ---------- 交易页：没开启下单时 ----------
await row.first().locator('button', { hasText: '去交易' }).click()
await p.waitForSelector('.op-submit')
await until(async () => (await p.locator('[data-live-badge]').count()) > 0)
ok((await p.locator('[data-live-badge]').innerText()).includes('测试交易所'), '账户名旁边标明是交易所账户')
ok(/测试交易所/.test(await p.locator('[data-sw]').innerText()) && /测试账户/.test(await p.locator('[data-sw]').innerText()), '顶栏写着当前是交易所账户（D73）')
ok(await p.locator('.op-acct-sel').count() === 0, '下单区不再有自己的账户下拉，全站只在顶栏切换')
ok(await p.locator('[data-live-arm]').count() === 1 && await p.locator('.op-submit').isDisabled(), '没开启下单：说明原因，按钮不能点')
ok(await p.locator('.op-algo-row').count() === 0, '实盘不显示分批、追价、TWAP')
await shot(p, 'lv-04-not-armed', '.mk-side')

// ---------- 开启下单 ----------
await p.goto(BASE + '/?page=account')
await sec.waitFor()
await sec.locator('[data-live-arm-btn]').click()
const arm = p.locator('.dlg[aria-label="开启下单"]')
await arm.waitFor()
ok(/测试交易所|测试资金/.test(await arm.locator('.cf-live').innerText()), '开启前写清楚会发到哪里')
await shot(p, 'lv-05-arm', '.dlg')
await arm.locator('input[type="password"]').fill(TP)
await arm.locator('[data-live-arm-ok]').click()
ok(/已开启下单/.test(await toastText(p)), '开启下单')
ok(!!(await until(async () => /可下单/.test(await sec.locator('[data-live-row]').first().innerText()))), '状态变成可下单')

// ---------- 下单：每一笔都弹确认，不能关 ----------
await p.goto(BASE + '/?page=trade&i=' + INST)
await p.waitForSelector('.op-submit')
await until(async () => (await p.locator('[data-live-badge]').count()) > 0 && await p.locator('.op-submit').isEnabled())
await p.locator('.op-row .seg button[data-tab="market"]').click()
await p.locator('.op-unit button', { hasText: 'BTC' }).first().click().catch(() => {})
await p.locator('[aria-label="数量"]').fill('0.01')
await p.waitForTimeout(500)
await p.locator('.op-submit').click()
const cf = p.locator('.dlg[aria-label="确认下单"]')
await cf.waitFor()
ok(await cf.locator('[data-live-confirm]').count() === 1, '确认框顶上写明会发到交易所')
ok(await cf.locator('.cf-skip').count() === 0, '实盘的确认框里没有"不再提醒"（要在账户页用二级密码关）')
ok((await cf.locator('[data-cf-go]').innerText()).includes('发到交易所'), '确认按钮写明发到交易所')
await shot(p, 'lv-06-confirm', '.dlg')
await cf.locator('[data-cf-go]').click()
ok(/成交/.test(await toastText(p)), '市价单成交')
const v1 = await until(async () => { const v = await call(p, 'GET', `/api/trade/${LA}`); return v.b.positions?.find((x) => x.instrument === INST) ? v : null })
ok(!!v1, '交易所上有了仓位')
await p.waitForTimeout(1500)
await shot(p, 'lv-07-position')

// 价格动了：浮盈跟着实时行情（假交易所的价格跟着测试行情）
const px0 = Number(v1.b.positions.find((x) => x.instrument === INST).entry)
await call(p, 'POST', '/api/dev/sim-price', { instrument: INST, price: Math.round(px0 * 1.01 * 10) / 10 })
const v2 = await until(async () => { const v = await call(p, 'GET', `/api/trade/${LA}`); const x = v.b.positions?.find((y) => y.instrument === INST); return x && Number(x.upnl) > 0 ? x : null })
ok(!!v2, `涨了 1%：浮盈为正（${v2?.upnl}）`)

// 限价单：撤了不提供撤回（交易所撤掉的单撤不回来）
const lim = await call(p, 'POST', `/api/trade/${LA}/orders`, { instrument: INST, side: 'buy', type: 'limit', price: String(Math.round(px0 * 0.9)), qty: '0.01' })
ok(lim.s === 200 && lim.b.status === 'new', '限价单挂上')
const und = await call(p, 'POST', `/api/trade/${LA}/orders/${lim.b.id}/undo`, {})
ok(und.s === 400 && und.b.code === 'NOT_SUPPORTED_LIVE', '撤回：实盘不支持')
await p.locator('.mk-tabs [data-tab="open"]').click()
const limRow = p.locator(`.tbl tbody tr[data-id="${lim.b.id}"] button`, { hasText: /^撤单$/ })
await until(async () => (await limRow.count()) > 0, 8000)
await limRow.click()
const ct = await until(async () => { const all = await p.locator('.toast').allInnerTexts(); return all.find((x) => /撤/.test(x)) ?? null }, 8000)
ok(!!ct && (await p.locator('.toast-act').count()) === 0, `撤单提示没有"撤回"按钮（${ct}）`)
ok(!!(await until(async () => !(await call(p, 'GET', `/api/trade/${LA}`)).b.orders.some((o) => o.id === lim.b.id))), '交易所上的限价单撤掉了')

// TradingView 信号绑在交易所账户上：单子发到交易所
// D107：地址里带着密钥，谁拿到谁就能往这个账户下单，所以绑交易所账户要二级密码
ok((await call(p, 'POST', '/api/signal-hooks', { account: LA, name: 'tv', budget: '0' })).b.code === 'TRADE_PASSWORD_EMPTY', '绑交易所账户不填二级密码被拒')
const hook = await call(p, 'POST', '/api/signal-hooks', { account: LA, name: 'tv', budget: '0', trade_password: TP })
ok(hook.s === 200 && hook.b.token, '交易所账户能绑 TradingView 信号（要二级密码）')
const before = Number((await call(p, 'GET', `/api/trade/${LA}`)).b.positions.find((x) => x.instrument === INST)?.qty ?? 0)
const sent = await p.evaluate(async (tok) => (await fetch(`/api/hook/tv/${tok}`, { method: 'POST', body: '做多 BTC 5%' })).json(), hook.b.token)
ok(sent.status === 'ok' && sent.order, `信号下单（${JSON.stringify(sent).slice(0, 80)}）`)
const after = await until(async () => { const x = (await call(p, 'GET', `/api/trade/${LA}`)).b.positions.find((y) => y.instrument === INST); return x && Number(x.qty) > before ? Number(x.qty) : null })
ok(!!after, `信号的单在交易所成交，仓位 ${before} → ${after}`)
const spotSig = await p.evaluate(async (tok) => (await fetch(`/api/hook/tv/${tok}`, { method: 'POST', body: '买入 BTC 5%' })).json(), hook.b.token)
ok(spotSig.code === 'SIG_LIVE_SPOT', `现货信号说明交易所账户只接合约（${spotSig.code}）`)
await call(p, 'DELETE', `/api/signal-hooks/${hook.b.hook.id}`)

// 别人看不到
const q = await newPage()
await q.goto(BASE + '/?page=login')
await register(q)
ok((await call(q, 'GET', `/api/trade/${LA}`)).s === 404 && (await call(q, 'POST', `/api/trade/${LA}/orders`, { instrument: INST, side: 'buy', type: 'market', qty: '0.01' })).s === 404, '别人的交易所账户：查不到、下不了单')
await q.context().close()

// ---------- 平仓、复盘 ----------
const cl = await call(p, 'POST', `/api/trade/${LA}/close`, { instrument: INST })
ok(cl.s === 200, '平仓')
ok(!!(await until(async () => { const v = await call(p, 'GET', `/api/trade/${LA}`); return v.s === 200 && !v.b.positions.some((x) => x.instrument === INST) })), '仓位没了')
const tr = await until(async () => { const r = await call(p, 'GET', `/api/journal/a/${LA}/trades?origin=all`); return r.s === 200 && r.b.trades?.length ? r.b.trades : null }, 70000)
ok(!!tr && tr[0].instrument === INST, `复盘里有这笔交易（${tr?.length ?? 0} 笔）`)
await p.goto(BASE + '/?page=review')
await p.waitForTimeout(1500)
const swr = await p.locator('[data-sw]').innerText()
ok(/测试账户/.test(swr) && /测试交易所/.test(swr), `复盘看的是顶栏选中的交易所账户（${swr.replace(/\s+/g, ' ')}）`)
ok(await p.locator('.rv-tools .sel').count() === 0, '复盘不再有自己的账户下拉')
await shot(p, 'lv-10-review')

// ---------- 窄屏 ----------
const m = await newPage({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true })
await m.context().addCookies(await p.context().cookies())
await m.goto(BASE + '/?page=account')
const msec = m.locator('.ac-sec', { has: m.locator('h2', { hasText: '交易所账户' }) })
await msec.waitFor()
await msec.scrollIntoViewIfNeeded()
const over = await m.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
ok(over <= 1, `手机宽度不横向滚动（多出 ${over}px）`)
const tblOver = await m.evaluate(() => { const w = document.querySelector('[data-live-accounts]')?.closest('.ac-tbl-wrap'); return w ? w.scrollWidth - w.clientWidth : -1 })
ok(tblOver >= 0 && tblOver <= 1, `手机宽度账户表格放得下，不用左右拖（多出 ${tblOver}px）`)
await shot(m, 'lv-08-phone', '.ac-sec:has(h2:text("交易所账户"))')

// ---------- 下单确认可以关（D96）：关要二级密码，关了以后下单不弹确认 ----------
await p.goto(BASE + '/?page=account')
await sec.waitFor()
await sec.locator('[data-live-confirm-btn="on"]').click()
const nc = p.locator('[data-live-noconfirm]')
await nc.waitFor()
await nc.locator('input[type="password"]').fill('wrong-' + rand())
await nc.locator('[data-live-noconfirm-ok]').click()
ok(!!(await until(async () => /二级密码/.test(await nc.locator('.fld-m').innerText()))), '关确认：二级密码错说明原因')
await nc.locator('input[type="password"]').fill(TP)
await nc.locator('[data-live-noconfirm-ok]').click()
ok(!!(await until(async () => (await sec.locator('[data-live-confirm-btn="off"]').count()) === 1)), '关掉了')
await p.goto(BASE + '/?page=trade&i=' + INST)
await p.waitForSelector('.op-submit')
await until(async () => (await p.locator('[data-live-badge]').count()) > 0 && await p.locator('.op-submit').isEnabled())
await p.locator('.op-row .seg button[data-tab="market"]').click()
await p.locator('.op-unit button', { hasText: 'BTC' }).first().click().catch(() => {})
await p.locator('[aria-label="数量"]').fill('0.01')
await p.waitForTimeout(500)
await p.locator('.op-submit').click()
await p.waitForTimeout(1200)
ok(await p.locator('.dlg[aria-label="确认下单"]').count() === 0, '关了确认：点下单不弹确认')
ok(!!(await until(async () => { const v = await call(p, 'GET', `/api/trade/${LA}`); return v.b.positions?.some((x) => x.instrument === INST) })), '直接发到交易所成交')
await call(p, 'POST', `/api/trade/${LA}/close`, { instrument: INST })
ok((await call(p, 'POST', `/api/live/accounts/${LA}/confirm`, { on: true })).s === 200, '恢复确认不用二级密码')

// ---------- 顶栏切换账户（D73）：切到模拟不问；切到交易所账户弹一次确认；现货、机器人说明只能用模拟账户 ----------
await p.goto(BASE + '/?page=trade&i=' + INST)
await p.waitForSelector('.op-submit')
await until(async () => (await p.locator('[data-live-badge]').count()) > 0)
ok(!/实盘/.test(await p.locator('.op-submit').innerText()), '测试交易所的下单按钮不写"实盘"（不是真钱）')
await p.locator('[data-sw]').click()
const pop = p.locator('.sw-pop')
await pop.waitFor()
const groups = await pop.locator('.sw-group').allInnerTexts()
ok(groups.join('/') === '模拟账户/交易所账户', `下拉分两组（${groups.join(' / ')}）`)
{ const vw = p.viewportSize().width; await p.screenshot({ path: `${OUT}/sw-01-open.png`, clip: { x: vw - 760, y: 0, width: 760, height: 260 } }) }
await pop.locator('li', { hasNot: p.locator('.sw-tag.live') }).first().click()
ok(await p.locator('[data-sw-ask]').count() === 0, '切到模拟账户不弹确认')
ok(!!(await until(async () => (await p.locator('[data-live-badge]').count()) === 0)) && /模拟/.test(await p.locator('[data-sw]').innerText()), '下单区跟着换成模拟账户')
await p.locator('[data-sw]').click()
await p.locator(`[data-sw-item="${LA}"]`).click()
const ask = p.locator('[data-sw-ask]')
await ask.waitFor()
ok(/测试交易所|假钱|测试账户/.test(await ask.innerText()), '切到交易所账户先确认一次')
await shot(p, 'sw-02-ask', '.dlg')
await ask.locator('button', { hasText: '取消' }).click()
ok(!!(await until(async () => (await p.locator('[data-sw-ask]').count()) === 0)) && (await p.locator('[data-live-badge]').count()) === 0, '取消：还是模拟账户')
await p.locator('[data-sw]').click()
await p.locator(`[data-sw-item="${LA}"]`).click()
await p.locator('[data-sw-ok]').click()
ok(!!(await until(async () => (await p.locator('[data-live-badge]').count()) > 0)), '确认后换成交易所账户')
await p.goto(BASE + '/?page=trade&i=ETH-SPOT')
const po = p.locator('[data-paper-only]').first()
await po.waitFor()
ok(/只能用模拟账户/.test(await po.innerText()), '现货：选着交易所账户时说明只能用模拟账户，不悄悄换账户')
await shot(p, 'sw-03-spot', '.mk-side')
await po.locator('button').click()
ok(!!(await until(async () => (await p.locator('[data-paper-only]').count()) === 0 && (await p.locator('.op-submit').count()) > 0)), '点一下切回模拟账户，现货能下单')
await p.goto(BASE + '/?page=trade&i=' + INST)
await p.locator('[data-sw]').click()
await p.locator(`[data-sw-item="${LA}"]`).click()
await p.locator('[data-sw-ok]').click()
await until(async () => (await p.locator('[data-live-badge]').count()) > 0)

// ---------- 策略跟随驱动交易所账户（18 第 5.2 条）：向导里能选，真钱要二级密码 ----------
await p.goto(BASE + '/?page=trade&i=' + INST)
await p.locator('.bar-help-btn').click()
await p.locator('.bar-help-pop button', { hasText: '跟随引导' }).click()
await p.waitForSelector('.wz-steps')
await p.locator('.wz-foot .btn.primary').click()
await p.locator('.wz-sel .sel-btn').click()
await p.locator('.sel-pop li', { hasText: '交易所账户' }).first().click()
ok(!!(await until(async () => /单子下到你的币安账户里/.test(await p.locator('[data-wz-live]').innerText().catch(() => '')))), '向导里选交易所账户：写明单子下到币安')
await shot(p, 'lv-11-follow-pick', '.dlg')
await p.locator('.wz-foot .btn.primary').click()
const lc = p.locator('[data-wz-live-confirm]')
await lc.waitFor()
const startBtn = p.locator('.wz-foot .btn.danger')
ok(/真钱|交易所账户/.test(await lc.innerText()) && await startBtn.isDisabled(), '确认页写明是交易所账户，没输二级密码不能开')
await lc.locator('input[type="password"]').fill('wrong-' + rand())
await startBtn.click()
ok(!!(await until(async () => /二级密码/.test(await p.locator('.wz-body .form-err').innerText().catch(() => '')))), '二级密码错：说清楚，不开')
await lc.locator('input[type="password"]').fill(TP)
await shot(p, 'lv-12-follow-confirm', '.dlg')
await startBtn.click()
await p.waitForSelector('.wz-steps', { state: 'detached', timeout: 10000 })
const fl = (await call(p, 'GET', '/api/follows')).b.follows
ok(fl.length === 1 && fl[0].account === LA && fl[0].status === 'active', `跟随开在交易所账户上（${fl[0]?.account}）`)
// 18 第 5.6 条：同一个策略再跟在模拟账户上，等两边都执行过同一条信号（示例策略 45 秒一条），策略页"我的跟随"里出对照
const pf = await call(p, 'POST', '/api/follows', { strategy: fl[0].strategy, alloc: '5000', confirm: true })
ok(pf.s === 200 && pf.b.account < 1e12, `同一个策略再跟在模拟账户上（${pf.b?.account}）`)
const common = await until(async () => {
  const fs = (await call(p, 'GET', '/api/follows')).b.follows
  const a = fs.find((f) => f.id === fl[0].id), b = fs.find((f) => f.id === pf.b.id)
  const keys = new Set((a?.records ?? []).filter((r) => r.msg && r.filled !== '0').map((r) => r.msg + '|' + r.instrument))
  return (b?.records ?? []).some((r) => r.msg && keys.has(r.msg + '|' + r.instrument)) || null
}, 150000)
ok(!!common, '两边执行了同一条信号')
await p.goto(BASE + '/?page=ai')
await p.locator('.ai .seg button', { hasText: '我的跟随' }).click()
const cmpEl = p.locator('[data-compare]')
ok(!!(await until(async () => (await cmpEl.count()) > 0)), '策略页出现"模拟和实盘对照"')
const cmpText = await cmpEl.innerText()
ok(/平均滑点[\s\S]*手续费[\s\S]*成交比例[\s\S]*平均执行用时/.test(cmpText) && (await cmpEl.locator('.cmp-rows tbody tr').count()) >= 1, '对照：滑点、手续费、成交比例、用时，逐条信号两边的成交价')
ok(!/NaN|undefined/.test(cmpText), '对照里没有 NaN')
await shot(p, 'lv-13-compare', '[data-compare]')
await call(p, 'POST', `/api/follows/${pf.b.id}/stop`, { mode: 'keep' })
// 交易所账户上的跟随留着：下面停用账户时应该自动停掉

// ---------- 停用 ----------
await p.goto(BASE + '/?page=account')
await sec.waitFor()
await sec.locator('button', { hasText: '停用' }).first().click()
const st = p.locator('.dlg[aria-label^="停用"]')
await st.waitFor()
ok(/不会被平掉或撤掉/.test(await st.innerText()), '停用前说清楚交易所上的仓位和委托不动')
await shot(p, 'lv-09-stop', '.dlg')
await st.locator('[data-live-stop-ok]').click()
ok(!!(await until(async () => (await call(p, 'GET', `/api/trade/${LA}`)).s === 404)), '停用后交易接口查不到')
const lf = (await call(p, 'GET', '/api/follows')).b.follows.find((f) => f.id === fl[0].id)
ok(lf?.status === 'stopped' && lf?.reason === 'archived', `停用交易所账户：上面的跟随自动停掉（${lf?.status} ${lf?.reason}）`)
ok(!!(await until(async () => (await sec.locator('.lv-key').count()) === 1)), '密钥还在，可以删除')

ok(p.errs.length === 0, `页面没有报错（${p.errs.slice(0, 3).join(' | ')}）`)
console.log(`\n${pass} 通过，${fail} 失败`)
await browser.close()
process.exit(fail ? 1 : 0)
