// 模拟盘最大限度拟真（D103）：下单区的"按盘口定价"（币安 priceMatch）、"有效到"（GTD）、条件单"价格保护"，
// 和大仓位的部分强平（造场景：开 300 BTC，把价格打到刚过强平价）。
// 会注册账号、下模拟单、改模拟行情价格：只能对着测试环境（模拟行情）跑。
// 用法：node tools/e2e-binrules.mjs <截图目录> [http://127.0.0.1:4173]
import { chromium } from 'playwright'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const rand = () => Math.random().toString(36).slice(2)
const until = async (f, ms = 15000) => { const t0 = Date.now(); while (Date.now() - t0 < ms) { try { const v = await f(); if (v) return v } catch { /* 再试 */ } await sleep(300) } return null }

const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN' })
await c.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1') })
const p = await c.newPage()
const errs = []
p.on('pageerror', (e) => errs.push(String(e)))
const call = (method, path, body) => p.evaluate(async ({ method, path, body }) => {
  const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
  return { s: r.status, b: await r.json().catch(() => null) }
}, { method, path, body })
await p.goto(BASE + '/?page=login')
await call('POST', '/api/auth/register', { email: `br${Date.now()}${rand().slice(0, 4)}@example.com`, password: `br-${rand()}-${rand()}` })
const TP = 'tp-' + rand()
await call('POST', '/api/me/trade-password', { new: TP })
await call('POST', '/api/discipline/enabled', { on: false, trade_password: TP })
const acct = (await call('GET', '/api/paper-accounts')).b.accounts[0].id
const orders = async (id = acct) => (await call('GET', `/api/trade/${id}`)).b.orders ?? []
const pick = async (sel, text) => { await p.locator(`${sel} .sel-btn`).click(); await p.locator('.sel-pop li', { hasText: text }).first().click() }

await p.goto(BASE + '/?page=trade')
await p.waitForSelector('.op-submit')
await sleep(1500)
// 按币数下单，好核对
await p.locator('.op-unit button', { hasText: 'BTC' }).click()

// 1. 按盘口定价：排队价
await p.locator('.op-pm .sel-btn').click()
await sleep(200)
const pmOpts = await p.locator('.sel-pop .sel-l').allInnerTexts()
ok(pmOpts.length === 9 && pmOpts.includes('对手价第 5 档') && pmOpts.includes('排队价'), `定价有 9 种（自己填、对手价、排队价各 4 档）：${pmOpts.join(' / ')}`)
await p.screenshot({ path: `${OUT}/br-01-pm.png` })
await p.locator('.sel-pop li', { hasText: '排队价' }).first().click()
ok(/下单那一刻按排队价定价/.test(await p.locator('[data-pm-say]').innerText()), '价格框换成"下单那一刻按排队价定价"')
await p.locator('input[aria-label="数量"]').fill('0.01')
await p.locator('.op-submit').click()
const pmOrder = await until(async () => (await orders()).find((o) => o.price_match === 'QUEUE'))
if (!pmOrder) console.log('诊断：', JSON.stringify({ msg: await p.locator('.op-msg').innerText().catch(() => ''), orders: (await orders()).map((o) => [o.type, o.price, o.tif, o.price_match]), dlg: await p.locator('.dlg').count() }))
const mk = Number((await call('GET', `/api/trade/${acct}`)).b.positions?.[0]?.mark ?? 0)
ok(!!pmOrder && Number(pmOrder.price) > 0, `挂上了按排队价定价的买单（${pmOrder?.price}）`)
// 2. 有效到（GTD）4 小时
await pick('.op-pm', '自己填')
const last = Number(pmOrder?.price ?? 0)
await p.locator('input[aria-label="价格"]').fill(String(Math.round(last * 0.98))) // 币安 BTC 价格带 ±3%：离边上远一点
await pick('.op-tif', '有效到')
await p.waitForSelector('.op-gtd')
await pick('.op-gtd', '4 小时')
await p.locator('input[aria-label="数量"]').fill('0.01') // 下过一单后数量会清空
await p.screenshot({ path: `${OUT}/br-02-gtd.png` })
const t0 = Date.now()
await p.locator('.op-submit').click()
const g = await until(async () => (await orders()).find((o) => o.tif === 'GTD'))
if (!g) console.log('诊断 GTD：', JSON.stringify({ msg: await p.locator('.op-msg').innerText().catch(() => ''), orders: (await orders()).map((o) => [o.type, o.price, o.tif]) }))
ok(!!g && Math.abs(g.good_till - (t0 + 4 * 3600_000)) < 120_000, `有效到 4 小时后（${g ? new Date(g.good_till).toISOString() : '无'}）`)
await p.locator('.seg button', { hasText: /^委托\s*\d*$/ }).first().click()
await sleep(600)
const gtdRow = await p.locator('tr', { hasText: '有效到' }).first().innerText().catch(() => '')
ok(/有效到 \S+/.test(gtdRow), `委托列表写着"有效到"和到期时间（${gtdRow.replace(/\s+/g, ' ').slice(0, 60)}）`)
await p.screenshot({ path: `${OUT}/br-02b-list.png` })
await pick('.op-tif', '一直有效')

// 3. 条件单 + 价格保护
await p.locator('.op-row .seg button', { hasText: '条件' }).click()
await p.waitForSelector('[data-protect]')
ok(/\d+(\.\d+)?%/.test((await p.locator('[data-protect]').getAttribute('title')) ?? ''), '价格保护的说明写着阈值（按交易所的 triggerProtect）')
await p.locator('input[aria-label="触发价"]').fill(String(Math.round(last * 1.03)))
await p.locator('[data-protect] input').check()
await p.locator('input[aria-label="数量"]').fill('0.01')
await p.screenshot({ path: `${OUT}/br-03-protect.png` })
await p.locator('.op-submit').click()
const pr = await until(async () => (await orders()).find((o) => o.type === 'cond_market' && o.price_protect))
ok(!!pr, '条件单带上了价格保护')
await sleep(600)
ok(/价格保护/.test(await p.locator('body').innerText()), '委托列表标着"价格保护"')
ok(errs.length === 0, `页面没有报错（${errs.slice(0, 2).join(' | ')}）`)

// 4. 部分强平：大账户开 300 BTC（20 倍，第 5 档），价格打到刚过强平价
const big = (await call('POST', '/api/paper-accounts', { name: '大仓位', initial: '2000000' })).b
await call('POST', `/api/trade/${big.id}/leverage`, { instrument: 'BTC-PERP', leverage: 20 })
for (let i = 0; i < 3; i++) await call('POST', `/api/trade/${big.id}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'market', qty: '100' })
await sleep(1000)
let v = (await call('GET', `/api/trade/${big.id}`)).b
const q0 = Number(v.positions?.[0]?.qty ?? 0)
ok(q0 >= 290, `大仓位开出来了（${q0} BTC）`)
const liq = Number(v.positions?.[0]?.liq_px ?? 0)
await call('POST', '/api/dev/sim-price', { instrument: 'BTC-PERP', price: Number((liq - 2).toFixed(1)) })
const after = await until(async () => {
  const x = (await call('GET', `/api/trade/${big.id}`)).b
  return x.notices?.some((n) => n.kind === 'liquidation') ? x : null
}, 20000)
const n = after?.notices?.filter((x) => x.kind === 'liquidation').at(-1)
const q1 = Number(after?.positions?.[0]?.qty ?? 0)
ok(n?.reason === 'partial' && q1 > 0 && q1 < q0, `刚过强平价只平掉一部分（平掉 ${n?.qty}，剩 ${q1} BTC）`)
ok(Number(n?.amount) > 0 && Math.abs(Number(n.amount) - Number(n.qty) * Number(n.px) * 0.0125) < 0.01, `清算费只按平掉的部分收（${n?.amount}）`)
// 画面：切到这个账户看强平提示
await p.evaluate((id) => localStorage.setItem('qs.paper', String(id)), big.id)
await p.goto(BASE + '/?page=trade')
await p.waitForSelector('.liq-banner', { timeout: 15000 }).catch(() => {})
const banner = await p.locator('.liq-banner').innerText().catch(() => '')
ok(/部分强平/.test(banner) && /剩下的仓位还在/.test(banner), `强平提示写明是部分强平（${banner.slice(0, 50)}）`)
await p.screenshot({ path: `${OUT}/br-04-partial.png` })

// 手机：下单区放得下新的控件
const m = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true, locale: 'zh-CN', storageState: await c.storageState() })
const mp = await m.newPage()
mp.on('pageerror', (e) => errs.push(String(e)))
await mp.goto(BASE + '/?page=trade')
await mp.waitForSelector('.op-submit', { timeout: 20000 }).catch(() => {})
await sleep(1200)
const mover = await mp.evaluate(() => document.documentElement.scrollWidth - innerWidth)
ok(mover <= 1, `手机不横向滚动（${mover}）`)
await mp.screenshot({ path: `${OUT}/br-05-phone.png` })

ok(errs.length === 0, `全程页面没有报错（${errs.slice(0, 2).join(' | ')}）`)
console.log(`\n${pass} 通过，${fail} 失败`)
await browser.close()
process.exit(fail ? 1 : 0)
