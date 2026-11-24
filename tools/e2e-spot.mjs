// 现货（D44、D83）：划转、下单、撤单、余额、界面。只对着测试环境跑。
//   node tools/e2e-spot.mjs <截图目录> <网址>
import { chromium } from 'playwright'
import { DEVICES } from './devices.mjs'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge', args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'] })
const rnd = () => Math.random().toString(36).slice(2)
const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN', timezoneId: 'Asia/Taipei' })
await c.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1'); localStorage.setItem('qs.inst', 'BTC-PERP') })
const p = await c.newPage()
const errs = []
p.on('pageerror', (e) => errs.push(String(e)))
const call = (method, path, body) => p.evaluate(async ({ method, path, body }) => { const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined }); return { s: r.status, b: await r.json().catch(() => null) } }, { method, path, body })
await p.goto(BASE + '/?page=login')
await call('POST', '/api/auth/register', { email: `spot${Date.now()}${rnd().slice(0, 4)}@example.com`, password: 'pw-' + rnd() + rnd() })
const tp = 'tp-' + rnd()
await call('POST', '/api/me/trade-password', { new: tp })
await call('POST', '/api/discipline/enabled', { on: false, trade_password: tp })
const A = (await call('GET', '/api/paper-accounts')).b.accounts[0].id
const until = async (fn, ms = 8000) => { const t0 = Date.now(); while (Date.now() - t0 < ms) { const v = await fn(); if (v) return v; await new Promise((r) => setTimeout(r, 200)) } return null }

// 接口
ok((await call('GET', '/api/spot/specs')).b.length === 6, '6 个币有规格')
const pxOf = async () => { const s = await call('GET', '/api/market/snapshot'); return Number(s.b.instruments.find((x) => x.instrument === 'BTC-SPOT')?.last ?? 0) }
const px = await until(pxOf, 10000)
ok(px > 0, `现货行情在推（${px}）`)
ok(Math.abs(px / Number((await call('GET', '/api/market/snapshot')).b.instruments.find((x) => x.instrument === 'BTC-PERP').last) - 1) < 0.003, '现货价格贴着合约')
const eq0 = Number((await call('GET', `/api/trade/${A}`)).b.equity)
let r = await call('POST', `/api/spot/${A}/orders`, { instrument: 'BTC-SPOT', side: 'buy', type: 'market', qty: '0.01' })
ok(r.s === 422 && r.b.code === 'INSUFFICIENT_BALANCE', `钱包是空的不能买（${r.b.code}）`)
r = await call('POST', `/api/spot/${A}/transfer`, { to: 'spot', amount: '4000' })
ok(r.s === 200 && Number(r.b.balances[0].free) === 4000, '合约转 4000 到现货')
ok(Number((await call('GET', `/api/trade/${A}`)).b.equity) === eq0 - 4000, '合约权益少 4000')
r = await call('POST', `/api/spot/${A}/transfer`, { to: 'spot', amount: '999999' })
ok(r.s >= 400, '转出不能超过合约可用')
r = await call('POST', `/api/spot/${A}/transfer`, { to: 'perp', amount: '999999' })
ok(r.s === 422 && r.b.code === 'INSUFFICIENT_BALANCE', '转回不能超过现货可用')
r = await call('POST', `/api/spot/${A}/orders`, { instrument: 'BTC-SPOT', side: 'buy', type: 'market', qty: '0.02' })
ok(r.s === 200 && r.b.status === 'filled', `市价买成交（${r.s} ${r.b?.status ?? r.b?.code}）`)
let v = (await call('GET', `/api/spot/${A}`)).b
const btc = v.balances.find((b) => b.coin === 'BTC')
ok(Math.abs(Number(btc?.free) - 0.01998) < 1e-9, `手续费从买到的币里扣（${btc?.free}）`)
ok(Number(btc?.cost) > 0 && v.fills.length >= 1, '记了成本和成交')
r = await call('POST', `/api/spot/${A}/orders`, { instrument: 'BTC-SPOT', side: 'buy', type: 'limit', price: (Math.floor(px * 0.97 * 100) / 100).toFixed(2), qty: '0.01' })
ok(r.s === 200 && r.b.status === 'new', `限价买挂上（${r.b?.code ?? r.b?.status}）`)
const oid = r.b.id
v = (await call('GET', `/api/spot/${A}`)).b
ok(Number(v.balances.find((b) => b.coin === 'USDT').locked) > 0, '挂单锁住 USDT')
r = await call('POST', `/api/spot/${A}/orders`, { instrument: 'BTC-SPOT', side: 'buy', type: 'limit', price: '1e20000000', qty: '1' })
ok(r.s === 422 && r.b.code === 'BAD_REQUEST', '超大数字被拦')

// 界面
await p.goto(BASE + '/?page=trade&i=BTC-PERP')
await p.waitForSelector('.mk-product')
await p.waitForTimeout(1500)
await p.locator('.mk-product button, .mk-product [role=radio]').filter({ hasText: '现货' }).first().click()
await p.waitForSelector('[data-spot]', { timeout: 6000 })
await p.waitForTimeout(2000)
ok(await p.locator('.mk-picker .sym').innerText() === 'BTC', '切到现货还是 BTC')
ok(/现货/.test(await p.locator('.mk-picker').innerText()), '写明现货')
const ro = await p.locator('.mk-ro').innerText()
ok(!/标记价格|资金费率|持仓量/.test(ro), '现货没有标记价、资金费、持仓量')
const heads = await p.locator('.mk-chart .display-head').innerText()
ok(/K 线/.test(heads) && !/频谱|足迹|链上/.test(heads), '现货图表只有 K 线和阶梯线')
ok(await p.locator('[data-spot-assets] tbody tr').count() >= 2, '资产表有 USDT 和 BTC')
await p.screenshot({ path: `${OUT}/spot-trade.png` })
{
  // 画面里只算看得见的线：挂一张离现价很近的单，看完撤掉
  // 太近会马上成交，太远在画面外（模拟行情的价格范围每次不一样），从近到远试
  let o2 = null, tl = null
  for (const k of [0.9998, 0.9996, 0.9992, 0.9985, 0.998, 0.997, 0.995]) { // 刚开的环境只有一根 K 线，纵轴可能只有万分之几宽
    const near = (Math.floor((await pxOf()) * k * 100) / 100).toFixed(2)
    o2 = await call('POST', `/api/spot/${A}/orders`, { instrument: 'BTC-SPOT', side: 'buy', type: 'limit', price: near, qty: '0.001' })
    await p.waitForTimeout(1600)
    tl = await p.locator('.mk-chart canvas.pannable').getAttribute('data-tlines')
    if (/limit/.test(tl ?? '')) break
    if (o2.b?.id) await call('DELETE', `/api/spot/${A}/orders/${o2.b.id}`)
  }
  ok(/limit/.test(tl ?? ''), `图上画出挂单线（${(tl ?? '').slice(0, 60)}）`)
  await p.screenshot({ path: `${OUT}/spot-line.png` })
  await call('DELETE', `/api/spot/${A}/orders/${o2.b.id}`)
}
// 从界面下一张买单
const form = p.locator('[data-spot]')
await form.locator('.op-row button, .op-row [role=radio]').filter({ hasText: '市价' }).first().click()
await form.getByRole('textbox', { name: '数量' }).fill('50')
await p.waitForTimeout(200)
const pv = await form.locator('.op-pv').innerText()
ok(/手续费 0\.1%/.test(pv) && /到账/.test(pv) && /\d/.test(pv), `预览写金额、手续费、到账：${pv.replace(/\s+/g, ' ').slice(0, 60)}`)
const fillsBefore = (await call('GET', `/api/spot/${A}`)).b.fills.length
await form.locator('.op-submit').click()
ok(await until(async () => (await call('GET', `/api/spot/${A}`)).b.fills.length > fillsBefore), '界面下单成交')
// 撤单
await p.locator('.mk-tabs button, .mk-tabs [role=radio]').filter({ hasText: /委托/ }).first().click()
await p.waitForTimeout(1200)
await p.getByRole('button', { name: '撤单' }).first().click()
ok(await until(async () => (await call('GET', `/api/spot/${A}`)).b.orders.every((o) => o.id !== oid)), '界面撤单')
v = (await call('GET', `/api/spot/${A}`)).b
ok(Number(v.balances.find((b) => b.coin === 'USDT').locked) === 0, '撤单后不再锁钱')
// 划转
await form.locator('[data-spot-transfer]').click()
await p.waitForSelector('.dlg')
await p.screenshot({ path: `${OUT}/spot-transfer.png` })
await p.locator('.dlg button, .dlg [role=radio]').filter({ hasText: '现货 → 合约' }).first().click()
await p.locator('.dlg').getByRole('textbox', { name: '金额' }).fill('100')
await p.locator('.dlg .btn.primary').click()
await p.waitForSelector('.dlg', { state: 'detached', timeout: 4000 }).catch(() => {})
ok(await p.locator('.dlg').count() === 0, '划转成功关掉对话框')
ok(Number((await call('GET', `/api/trade/${A}`)).b.equity) === eq0 - 3900, '划转回合约，权益对得上')
// 跟随中不能划转
{
  const strats = (await call('GET', '/api/strategies')).b
  const sid = (strats.strategies ?? strats)[0].strategy_id
  const st = await call('POST', '/api/follows', { strategy: sid, confirm: true, account: A, alloc: '1000', max_loss: '0.2', max_leverage: 3, per_inst: '0.5', blacklist: [] })
  if (st.s === 200) {
    r = await call('POST', `/api/spot/${A}/transfer`, { to: 'spot', amount: '10' })
    ok(r.s === 422 && r.b.code === 'FOLLOWING_NO_TRANSFER', '正在跟 AI 的账户不能划转')
    await call('POST', `/api/follows/${st.b.id}/stop`, { mode: 'close' })
  } else ok(false, `开启跟随失败（${st.s} ${st.b?.code}）`)
}
// 英文
await p.goto(BASE + '/?page=trade&i=ETH-SPOT')
await p.waitForSelector('[data-spot]')
await p.locator('.bar-lang button, .bar-lang [role=radio]').filter({ hasText: 'EN' }).first().click()
await p.waitForTimeout(800)
const en = await p.locator('[data-spot]').innerText()
ok(!/[一-鿿]/.test(en), `英文没有中文：${(en.match(/[一-鿿].{0,10}/) ?? [''])[0]}`)
await p.locator('.bar-lang button, .bar-lang [role=radio]').filter({ hasText: '中' }).first().click()
await p.waitForTimeout(500)
// 16 种设备
const state = await c.storageState()
const bad = []
for (const d of DEVICES) {
  const cc = await browser.newContext({ storageState: state, viewport: { width: d.w, height: d.h }, deviceScaleFactor: d.dpr, isMobile: d.w < 500, hasTouch: d.w < 500, locale: 'zh-CN', timezoneId: 'Asia/Taipei' })
  await cc.addInitScript(() => localStorage.setItem('qs.wizardSeen', '1'))
  const q = await cc.newPage()
  const e2 = []
  q.on('pageerror', (e) => e2.push(String(e)))
  await q.goto(BASE + '/?page=trade&i=BTC-SPOT')
  await q.waitForSelector('.mk-product', { timeout: 15000 }).catch(() => {})
  await q.waitForTimeout(1200)
  const o = await q.evaluate(() => [document.documentElement.scrollHeight - innerHeight, document.documentElement.scrollWidth - innerWidth])
  if (o[0] > 0 || o[1] > 0 || e2.length) bad.push(`${d.id} ${o} ${e2[0] ?? ''}`)
  if (d.w === 393 || d.w === 1097) await q.screenshot({ path: `${OUT}/spot-${d.w}.png` })
  await cc.close()
}
ok(bad.length === 0, `16 种设备整页不滚、不溢出、不报错（${bad.join('；')}）`)
ok(errs.length === 0, `流程中没有报错（${errs[0] ?? ''}）`)
// 划转不是亏损：纪律仪表不能把转去现货的钱算成今天亏的（看截图发现过）
await p.goto(BASE + '/?page=risk')
await p.waitForSelector('.rk-h', { timeout: 15000 })
await p.waitForTimeout(2500)
{
  const rk = (await call('GET', '/api/risk/overview')).b.accounts[0]
  ok(Number(rk.transfers) < 0, `风险接口带累计划转（${rk.transfers}）`)
  ok(Number(rk.spot_equity) > 0, `风险接口带现货资产（${rk.spot_equity}）`)
  const row = await p.locator('.rk-h').innerText()
  const m = row.match(/单日亏损\s*([\d,.]+)/)
  const lost = m ? Number(m[1].replace(/,/g, '')) : NaN
  ok(lost < 100, `划转之后单日亏损还是小数（${m?.[1]}）`)
  ok(/现货资产/.test(await p.locator('.rk-ros').innerText()), '风险页写了现货资产')
}
console.log(`\n通过 ${pass}，失败 ${fail}`)
await browser.close()
