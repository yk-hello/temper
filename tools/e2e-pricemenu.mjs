// 图上点右键下单（18 第 6.5 条）：价位下面是"限价买入 / 跌到卖出"，上面是"限价卖出 / 涨到买入"，
// 有仓位时多一项止盈或止损；下单的几项只填进下单区。会注册账号、下单：只能对着测试环境跑。
// 用法：node tools/e2e-pricemenu.mjs <截图目录> [http://127.0.0.1:4173]
import { chromium } from 'playwright'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const rand = () => Math.random().toString(36).slice(2)
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
await call('POST', '/api/auth/register', { email: `pm${Date.now()}${rand().slice(0, 4)}@example.com`, password: `pm-${rand()}-pass` })
const TP = 'tp-' + rand()
await call('POST', '/api/me/trade-password', { new: TP })
await call('POST', '/api/discipline/enabled', { on: false, trade_password: TP })
const acct = (await call('GET', '/api/paper-accounts')).b.accounts[0].id
await p.goto(BASE + '/?page=trade&i=BTC-PERP')
await p.waitForSelector('.op-submit')
await p.waitForTimeout(2500)
const cv = p.locator('.mk-chart .surface canvas.interactive').first()
const box = await cv.boundingBox()
const at = (fy) => ({ x: box.x + box.width * 0.5, y: box.y + box.height * fy })
const lastPx = async () => Number((await call('GET', '/api/market/snapshot')).b.instruments.find((i) => i.instrument === 'BTC-PERP').last)
// 在图上找一个价位在现价下面（或上面）的点右键（现价在纵轴上的位置不固定：链上爆仓价会把纵轴拉开，现价可能在很靠下的地方；按菜单里的价判断；最下面成交量那一条不出菜单）
const menuOn = async (want) => {
  // 行情平静时图上下只留一成的空（价格的万分之几），步子小一点才点得到（11-16）
  for (let fy = want === 'below' ? 0.8 : 0.015; fy > 0.01 && fy < 0.82; fy += want === 'below' ? -0.015 : 0.015) {
    const q = at(fy)
    await p.mouse.click(q.x, q.y, { button: 'right' })
    const m = p.locator('[data-price-menu]')
    if (!(await m.waitFor({ timeout: 1500 }).then(() => true).catch(() => false))) continue
    const px = Number((await m.locator('.pm-px').innerText()).replace(/,/g, ''))
    const last = await lastPx()
    if ((want === 'below' && px < last * 0.9999) || (want === 'above' && px > last * 1.0001)) return { px, q }
    await p.keyboard.press('Escape')
  }
  return null
}

// 下面：限价买入、跌到卖出
const menu = p.locator('[data-price-menu]')
let hit = await menuOn('below')
ok(!!hit && await menu.count() === 1, '右键出菜单')
ok(await menu.locator('[data-pm="limit-buy"]').count() === 1 && await menu.locator('[data-pm="cond-sell"]').count() === 1, '价位在现价下面：限价买入、跌到卖出')
ok(await menu.locator('[data-pm="tp"], [data-pm="sl"]').count() === 0, '没有仓位时没有止盈止损')
await p.screenshot({ path: `${OUT}/pm-01-below.png` })
const px = hit?.px
await menu.locator('[data-pm="limit-buy"]').click()
await p.waitForTimeout(400)
const priceVal = await p.locator('.op [aria-label="价格"]').inputValue().catch(() => '')
ok(Number(priceVal) === Number(px), `价格填进下单区（${priceVal} = ${px}）`)
ok(await p.locator('.op-side button[aria-pressed="true"]').innerText().then((s) => /买/.test(s)), '方向是买')
ok(await menu.count() === 0, '选了以后菜单关掉')
const before = (await call('GET', `/api/trade/${acct}`)).b.orders.length
ok(before === 0, '只是填进下单区，没有下单')

// 上面：限价卖出、涨到买入；Esc 关
hit = await menuOn('above')
ok(!!hit && await menu.locator('[data-pm="limit-sell"]').count() === 1 && await menu.locator('[data-pm="cond-buy"]').count() === 1, '价位在现价上面：限价卖出、涨到买入')
await menu.locator('[data-pm="cond-buy"]').click()
await p.waitForTimeout(400)
ok(await p.locator('.op .seg button[data-tab="cond"][aria-pressed="true"]').count() === 1 || await p.locator('.op-cond').count() > 0, '选"涨到买入"：下单区换成条件单')
await p.mouse.click(hit.q.x, hit.q.y, { button: 'right' })
await menu.waitFor()
await p.keyboard.press('Escape')
ok(await menu.count() === 0, 'Esc 关菜单')

// 有多仓：下面那一项是止损，点了直接设上
await call('POST', `/api/trade/${acct}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'market', qty: '0.01' })
await p.waitForTimeout(1200)
hit = await menuOn('below')
ok(!!hit && await menu.locator('[data-pm="sl"]').count() === 1, '有多仓：现价下面是"止损设在"')
const slPx = hit?.px
await p.screenshot({ path: `${OUT}/pm-02-sl.png` })
await menu.locator('[data-pm="sl"]').click()
let sl = null
for (let i = 0; i < 20 && !sl; i++) { await p.waitForTimeout(250); sl = (await call('GET', `/api/trade/${acct}`)).b.orders.find((o) => o.type === 'stop_market') }
ok(!!sl && Math.abs(Number(sl.trigger_px) - slPx) < 1, `止损设上了（${sl?.trigger_px}）`)
hit = await menuOn('above')
ok(!!hit && await menu.locator('[data-pm="tp"]').count() === 1, '有多仓：现价上面是"止盈设在"')
await p.keyboard.press('Escape')
// 自动减仓排队（18 第 6.1 条）：持仓表强平价后面 4 格；悬停写明是什么，模拟盘是估的
const adl = p.locator('[data-adl]').first()
await adl.waitFor({ timeout: 5000 }).catch(() => {})
ok(await adl.count() === 1, `持仓表里有自动减仓排队（第 ${await adl.getAttribute('data-adl').catch(() => '?')} 档）`)
if (await adl.count()) {
  await adl.hover()
  await p.waitForTimeout(400)
  const tip = await p.locator('.tip-pop').innerText().catch(() => '')
  ok(/自动减仓排队/.test(tip) && /估/.test(tip), `悬停说明（${tip.slice(0, 40)}）`)
  await p.locator('.mk-bottom').screenshot({ path: `${OUT}/pm-03-adl.png` })
}
ok(errs.length === 0, `页面没有报错（${errs.join(' | ').slice(0, 200)}）`)
await browser.close()
console.log(`\n${pass} 通过，${fail} 失败`)
process.exit(fail ? 1 : 0)
