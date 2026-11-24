// 12 项下单功能（D83）：跟踪止损、分批挂单、TWAP、一键反手、分批止盈、盈亏比计算、合并档位、点图下单、按指数价触发、追价、模板、点击交易；
// 加上冰山单、按成交量比例下单（18 第 9.5 条）。
// 只对着测试环境跑。  node tools/e2e-algo.mjs <截图目录> <网址>
import { chromium } from 'playwright'
import { DEVICES } from './devices.mjs'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge', args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'] })
const rnd = () => Math.random().toString(36).slice(2)
const c = await browser.newContext({ viewport: { width: 1536, height: 900 }, deviceScaleFactor: 1.25, locale: 'zh-CN', timezoneId: 'Asia/Taipei' })
await c.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1'); localStorage.setItem('qs.inst', 'BTC-PERP'); localStorage.setItem('qs.chartMode', 'candle') })
const p = await c.newPage()
const errs = []
p.on('pageerror', (e) => errs.push(String(e)))
const call = (method, path, body) => p.evaluate(async ({ method, path, body }) => { const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined }); return { s: r.status, b: await r.json().catch(() => null) } }, { method, path, body })
const until = async (fn, ms = 10000) => { const t0 = Date.now(); while (Date.now() - t0 < ms) { const v = await fn(); if (v) return v; await new Promise((r) => setTimeout(r, 250)) } return null }
await p.goto(BASE + '/?page=login')
await call('POST', '/api/auth/register', { email: `algo${Date.now()}${rnd().slice(0, 4)}@example.com`, password: 'pw-' + rnd() + rnd() })
const tp = 'tp-' + rnd()
await call('POST', '/api/me/trade-password', { new: tp })
await call('POST', '/api/discipline/enabled', { on: false, trade_password: tp })
const A = (await call('GET', '/api/paper-accounts')).b.accounts[0].id
const INST = 'BTC-PERP'
const view = async () => (await call('GET', `/api/trade/${A}`)).b
const lastPx = async () => Number((await call('GET', '/api/market/snapshot')).b.instruments.find((x) => x.instrument === INST).last)
const inject = (px) => call('POST', '/api/dev/sim-price', { instrument: INST, price: Number(Number(px).toFixed(1)) })
const flatten = async () => { await call('POST', `/api/trade/${A}/cancel-all`, {}); await call('POST', `/api/trade/${A}/close-all`, {}); await until(async () => (await view()).positions.length === 0) }
const px0 = await lastPx()

// 1. 分批挂单
let r = await call('POST', `/api/algo/${A}/scale`, { instrument: INST, side: 'buy', qty: '0.05', lower: (px0 * 0.98).toFixed(1), upper: (px0 * 0.995).toFixed(1), count: 5, skew: 'equal' })
ok(r.s === 200 && r.b.orders.length === 5, `分批挂单：挂了 5 张（${r.b?.code ?? ''}）`)
let v = await view()
const bk = v.orders.filter((o) => o.type === 'limit').map((o) => Number(o.price)).sort((a, b) => a - b)
ok(bk.length === 5 && bk[0] < bk[4], '5 张价格从低到高')
r = await call('POST', `/api/algo/${A}/scale`, { instrument: INST, side: 'buy', qty: '0.05', lower: (px0 * 0.98).toFixed(1), upper: (px0 * 0.995).toFixed(1), count: 30, skew: 'equal' })
ok(r.s === 422 && r.b.code === 'ALGO_BAD_COUNT', '张数太多被拒')
r = await call('POST', `/api/algo/${A}/scale`, { instrument: INST, side: 'buy', qty: '0.0001', lower: (px0 * 0.98).toFixed(1), upper: (px0 * 0.995).toFixed(1), count: 5, skew: 'equal' })
ok(r.s === 422 && r.b.code === 'ALGO_SCALE_TOO_SMALL', '每张太小被拒')
await call('POST', `/api/trade/${A}/cancel-all`, {})

// 2. TWAP
r = await call('POST', `/api/algo/${A}/twap`, { instrument: INST, side: 'buy', qty: '0.02', minutes: 1 })
ok(r.s === 200 && r.b.slices === 2, `TWAP：1 分钟拆 2 片（${r.b?.code ?? r.b?.slices}）`)
const tw = r.b.id
ok(await until(async () => (await view()).positions.length > 0, 5000), '第一片马上下')
const q1 = Number((await view()).positions[0]?.qty)
ok(Math.abs(q1 - 0.01) < 1e-9, `第一片是一半（${q1}）`)
await call('DELETE', `/api/algo/${A}/${tw}`)
const stopped = (await call('GET', `/api/algo/${A}`)).b.algos.find((a) => a.id === tw)
ok(stopped.status === 'stopped' && stopped.reason === 'USER', '能中途取消')
await flatten()

// 3. 追价
r = await call('POST', `/api/algo/${A}/chase`, { instrument: INST, side: 'buy', qty: '0.01', max_dist: '0.05' })
ok(r.s === 200, `追价开始（${r.b?.code ?? ''}）`)
const ch = r.b.id
const chFin = await until(async () => { const a = (await call('GET', `/api/algo/${A}`)).b.algos.find((x) => x.id === ch); return a.status !== 'running' ? a : null }, 25000)
ok(!!chFin, `追价有结果（${chFin?.status} ${chFin?.reason ?? ''}，改价 ${chFin?.moves} 次）`)
ok(!chFin || chFin.status === 'done' || chFin.reason === 'CHASE_TOO_FAR', '要么成交，要么超过最大距离撤单')
await flatten()

// 3b. 冰山单（18 第 9.5 条）：盘口上一次只挂一张
{
  const pxI = await lastPx()
  const icePx = (pxI * 0.996).toFixed(1)
  r = await call('POST', `/api/algo/${A}/iceberg`, { instrument: INST, side: 'buy', qty: '0.03', price: icePx, count: 3 })
  ok(r.s === 200 && r.b.slices === 3 && Number(r.b.show) === 0.01, `冰山单：拆 3 张，一张 0.01（${r.b?.code ?? r.b?.show}）`)
  const ice = r.b.id
  let ov = (await view()).orders
  ok(ov.length === 1 && Number(ov[0].qty) === 0.01 && Number(ov[0].price) === Number(icePx), `盘口上只挂一张（${ov.length} 张）`)
  r = await call('POST', `/api/algo/${A}/iceberg`, { instrument: INST, side: 'buy', qty: '0.03', price: icePx, count: 1 })
  ok(r.s === 422 && r.b.code === 'ALGO_ICE_COUNT', '一张不叫冰山，被拒')
  // 价格跌穿挂单价：一张成交，下一张挂上
  await inject(pxI * 0.99)
  const next = await until(async () => { const a = (await call('GET', `/api/algo/${A}`)).b.algos.find((x) => x.id === ice); return a.done >= 2 ? a : null }, 20000)
  ok(!!next && Number(next.filled) >= 0.01, `一张成交后挂下一张（第 ${next?.done} 张，已成交 ${next?.filled}）`)
  ov = (await view()).orders
  ok(ov.length <= 1, `还是最多挂一张（${ov.length}）`)
  await inject(pxI)
  await call('DELETE', `/api/algo/${A}/${ice}`)
  const iceEnd = (await call('GET', `/api/algo/${A}`)).b.algos.find((a) => a.id === ice)
  ok(iceEnd.status !== 'running' && (await view()).orders.length === 0, '取消冰山单，挂着的那张一起撤')
  await flatten()
}

// 3c. 按成交量比例：模拟行情一直有成交，跟 50% 很快就会下第一笔
{
  r = await call('POST', `/api/algo/${A}/pov`, { instrument: INST, side: 'buy', qty: '0.05', pct: '60', minutes: 2 })
  ok(r.s === 422 && r.b.code === 'ALGO_POV_PCT', '比例超过 50% 被拒')
  r = await call('POST', `/api/algo/${A}/pov`, { instrument: INST, side: 'buy', qty: '0.05', pct: '50', minutes: 2 })
  ok(r.s === 200 && r.b.kind === 'pov', `按成交量比例开始（${r.b?.code ?? ''}）`)
  const pv = r.b.id
  const got = await until(async () => { const a = (await call('GET', `/api/algo/${A}`)).b.algos.find((x) => x.id === pv); return Number(a.seen) > 0 ? a : null }, 25000)
  ok(!!got, `看得到市场成交了多少（${got?.seen}）`)
  ok(!got || Number(got.filled) <= Number(got.seen) * 0.5 + 0.0011, `成交不超过市场成交的 50%（市场 ${got?.seen}，自己 ${got?.filled}）`)
  await call('DELETE', `/api/algo/${A}/${pv}`)
  await flatten()
}

// 4. 跟踪止损
await call('POST', `/api/trade/${A}/orders`, { instrument: INST, side: 'buy', type: 'market', qty: '0.01' })
await until(async () => (await view()).positions.length > 0)
r = await call('POST', `/api/algo/${A}/trail`, { instrument: INST, callback: '1' })
ok(r.s === 200 && r.b.live && Number(r.b.stop) < Number(r.b.best), `跟踪止损：止损价 ${r.b?.stop}`)
const trail = r.b.id
r = await call('POST', `/api/algo/${A}/trail`, { instrument: INST, callback: '1' })
ok(r.s === 422 && r.b.code === 'ALGO_TRAIL_EXISTS', '同一个持仓不能开两个')
const stop0 = Number((await call('GET', `/api/algo/${A}`)).b.algos.find((a) => a.id === trail).stop)
const up = (await lastPx()) * 1.02
await inject(up)
const moved = await until(async () => { const a = (await call('GET', `/api/algo/${A}`)).b.algos.find((x) => x.id === trail); return Number(a.stop) > stop0 ? a : null })
ok(!!moved, `涨了止损跟着上去（${stop0} → ${moved?.stop}）`)
await inject(up * 0.97)
const closed = await until(async () => (await view()).positions.length === 0, 15000)
ok(!!closed, '回撤到止损价就平掉')
ok((await call('GET', `/api/algo/${A}`)).b.algos.find((a) => a.id === trail).reason === 'TRIGGERED', '记下是跟踪止损触发的')

// 5. 分批止盈
await call('POST', `/api/trade/${A}/orders`, { instrument: INST, side: 'buy', type: 'market', qty: '0.02' })
await until(async () => (await view()).positions.length > 0)
const nowPx = await lastPx()
r = await call('POST', `/api/algo/${A}/exits`, { instrument: INST, levels: [{ px: (nowPx * 1.01).toFixed(1), pct: '50' }, { px: (nowPx * 1.02).toFixed(1), pct: '50' }] })
ok(r.s === 200 && r.b.orders.length === 2, `分批止盈挂了 2 档（${r.b?.code ?? ''}）`)
r = await call('POST', `/api/algo/${A}/exits`, { instrument: INST, levels: [{ px: (nowPx * 1.03).toFixed(1), pct: '80' }, { px: (nowPx * 1.04).toFixed(1), pct: '50' }] })
ok(r.s === 422 && r.b.code === 'ALGO_LEVELS_OVER', '加起来超过 100% 被拒')
await inject(nowPx * 1.012)
const half = await until(async () => { const x = (await view()).positions[0]; return x && Math.abs(Number(x.qty) - 0.01) < 1e-9 ? x : null }, 15000)
ok(!!half, '到第一档平掉一半')
await flatten()

// 6. 一键反手
await call('POST', `/api/trade/${A}/orders`, { instrument: INST, side: 'buy', type: 'market', qty: '0.01' })
await until(async () => (await view()).positions.length > 0)
r = await call('POST', `/api/algo/${A}/reverse`, { instrument: INST })
ok(r.s === 200, `反手（${r.b?.code ?? ''}）`)
const rv = await until(async () => { const x = (await view()).positions[0]; return x?.side === 'short' ? x : null })
ok(rv && Math.abs(Number(rv.qty) - 0.01) < 1e-9, '变成同样大小的空单')
await flatten()
r = await call('POST', `/api/algo/${A}/reverse`, { instrument: INST })
ok(r.s === 422 && r.b.code === 'NO_POSITION', '没仓位不能反手')

// 7. 按指数价触发
r = await call('POST', `/api/trade/${A}/orders`, { instrument: INST, side: 'buy', type: 'cond_market', qty: '0.01', trigger_px: ((await lastPx()) * 1.005).toFixed(1), trigger_by: 'index' })
ok(r.s === 200 && r.b.trigger_by === 'index', `条件单能按指数价（${r.b?.code ?? ''}）`)
const cid = r.b.id
await inject((await lastPx()) * 1.01)
ok(await until(async () => (await view()).positions.length > 0, 15000), '指数价到了就触发')
await flatten()
void cid

// 界面
await p.goto(BASE + '/?page=trade&i=BTC-PERP')
await p.waitForSelector('.op')
await p.waitForTimeout(1500)
// 下单方式：分批挂单
await p.locator('.op-algo-sel button, .op-algo-sel select').first().click()
await p.getByText('分批挂单', { exact: true }).last().click()
await p.waitForSelector('[data-algo-form=scale]')
ok(await p.locator('[data-algo-form=scale] input').count() === 3, '分批挂单有价格从、到、张数')
await p.screenshot({ path: `${OUT}/algo-scale.png` })
const pxNow = await lastPx()
await p.locator('[data-algo=lo]').fill((pxNow * 0.98).toFixed(1))
await p.locator('[data-algo=hi]').fill((pxNow * 0.995).toFixed(1))
await p.locator('.op').getByRole('textbox', { name: '数量' }).fill('1000')
await p.waitForTimeout(400)
await p.locator('.op-submit').click()
ok(await until(async () => (await view()).orders.length === 5), '界面下分批挂单：5 张')
await call('POST', `/api/trade/${A}/cancel-all`, {})
// 模板（11-17 走查：对话框在下单表单里面，点"保存"冒泡成了下单。先填上数量，存完委托数不能变）
await p.locator('.op').getByRole('textbox', { name: '数量' }).fill('1000')
await p.waitForTimeout(300)
await p.locator('[data-tpl-save]').click()
await p.locator('[data-tpl-name]').fill('回踩分批')
await p.locator('.dlg .btn.primary').click()
await p.waitForTimeout(1200)
ok(await p.evaluate(() => JSON.parse(localStorage.getItem('qs.templates'))[0].algo === 'scale'), '存了模板，下单方式也存了')
ok((await view()).orders.length === 0, '存模板不会顺手下单')
await p.reload()
await p.waitForSelector('.op')
await p.waitForTimeout(800)
await p.locator('.op-tpl-sel button, .op-tpl-sel select').first().click()
await p.getByText('回踩分批', { exact: true }).last().click()
await p.waitForTimeout(300)
ok(await p.locator('[data-algo-form=scale]').count() === 1, '套用模板，分批挂单回来了')
// 冰山单、按成交量比例的界面
await p.locator('.op-algo-sel button, .op-algo-sel select').first().click()
await p.getByText('冰山单', { exact: true }).last().click()
ok(!!(await p.waitForSelector('[data-algo-form=iceberg]', { timeout: 3000 }).catch(() => null)), '限价单里有冰山单，填拆成几张')
await p.locator('.op-algo-row').screenshot({ path: `${OUT}/algo-iceberg.png` }).catch(() => {})
await p.locator('.op-row .seg button[data-tab="market"]').click()
await p.waitForTimeout(200)
await p.locator('.op-algo-sel button, .op-algo-sel select').first().click()
await p.getByText('按成交量比例', { exact: true }).last().click()
ok(!!(await p.waitForSelector('[data-algo-form=pov]', { timeout: 3000 }).catch(() => null)), '市价单里有按成交量比例，填比例、最长多久、价格上限')
ok(await p.locator('[data-algo-form=pov] input').count() === 3, '三个参数')
await p.locator('.op').screenshot({ path: `${OUT}/algo-pov.png` })
await p.locator('.op-row .seg button[data-tab="limit"]').click()
await p.waitForTimeout(200)
// 计算器
await p.locator('.op-algo-sel button, .op-algo-sel select').first().click()
await p.getByText('一次下完', { exact: true }).last().click()
await p.locator('.op-check').filter({ hasText: '止盈止损' }).locator('input').check()
await p.waitForSelector('[data-risk-calc]')
const entry = await lastPx()
await p.locator('.op-tpsl input').nth(0).fill((entry * 1.04).toFixed(1))
await p.locator('.op-tpsl input').nth(1).fill((entry * 0.98).toFixed(1))
await p.waitForTimeout(300)
const calc = await p.locator('[data-risk-calc]').innerText()
ok(/1 : [12]\.\d\d/.test(calc), `写盈亏比：${calc.replace(/\s+/g, ' ').slice(0, 50)}`)
await p.locator('[data-risk-apply]').click()
await p.waitForTimeout(300)
ok(Number(await p.locator('.op').getByRole('textbox', { name: '数量' }).inputValue()) > 0, '按风险算出数量')
await p.screenshot({ path: `${OUT}/algo-calc.png` })
// 合并档位
const bookBefore = await p.locator('.ob-r.ask').first().innerText()
await p.locator('.ob-group button, .ob-group select').first().click().catch(() => {})
const opt = p.locator('[role=option], .sel-opt, .select-item').filter({ hasText: /^\d/ }).last()
if (await opt.count()) await opt.click()
await p.waitForTimeout(800)
ok(await p.locator('.ob-group').count() === 1, `盘口有合并档位（${bookBefore.replace(/\s+/g, ' ').slice(0, 20)}）`)
await p.screenshot({ path: `${OUT}/algo-group.png` })
// 点图下单：点价格轴
const cv = p.locator('.mk-chart canvas.pannable')
const bb = await cv.boundingBox()
const priceBefore = await p.locator('.op').getByRole('textbox', { name: '价格' }).inputValue().catch(() => '')
await p.mouse.click(bb.x + bb.width - 30, bb.y + bb.height * 0.3)
await p.waitForTimeout(400)
const priceAfter = await p.locator('.op').getByRole('textbox', { name: '价格' }).inputValue().catch(() => '')
ok(priceAfter && priceAfter !== priceBefore, `点价格轴选价（${priceBefore} → ${priceAfter}）`)
// 点击交易
await p.locator('.mk-book .display-head button, .mk-book .display-head [role=radio]').filter({ hasText: '点击交易' }).first().click()
await p.waitForSelector('[data-dom]')
await p.waitForTimeout(800)
await p.locator('[data-dom-qty]').fill('0.002')
await p.locator('[data-dom-buy]').last().click() // 不按价格找：梯子随行情滚动，那一档可能已经不在了
await p.waitForTimeout(400)
ok((await view()).orders.length === 0, '没打开一键下单时，点了不下单')
await p.locator('[data-dom-arm]').click()
// 挂在买盘中间那档：最下面一档价格一涨就滚出梯子，后面"点自己的单"就点不到了
const domRows = await p.locator('[data-dom-buy]').count()
await p.locator('[data-dom-buy]').nth(domRows - 4).click()
ok(await until(async () => (await view()).orders.length === 1), '打开后点一下挂限价单')
await p.screenshot({ path: `${OUT}/algo-dom.png` })
// 梯子跟着行情滚：一次没点中（那一档刚好滚走）就再找再点，最多 15 秒
ok(await until(async () => {
  if ((await view()).orders.length === 0) return true
  await p.locator('[data-dom-own]').first().click({ timeout: 1000 }).catch(() => {})
  await p.waitForTimeout(400)
  return (await view()).orders.length === 0
}, 15000), '点自己的单撤掉')
await p.locator('.mk-book .display-head button, .mk-book .display-head [role=radio]').filter({ hasText: '盘口' }).first().click()
// 持仓对话框
await call('POST', `/api/trade/${A}/orders`, { instrument: INST, side: 'buy', type: 'market', qty: '0.01' })
await p.waitForSelector('[data-exit-open]', { timeout: 6000 })
await p.locator('[data-exit-open]').first().click()
await p.waitForSelector('[data-exit-dialog]')
ok(/止损价/.test(await p.locator('[data-trail-say]').innerText()), '跟踪止损写明止损价')
await p.screenshot({ path: `${OUT}/algo-exit.png` })
await p.locator('[data-trail-go]').click()
ok(await until(async () => (await call('GET', `/api/algo/${A}`)).b.algos.some((a) => a.kind === 'trail' && a.status === 'running')), '界面开了跟踪止损')
await p.waitForTimeout(1800)
const tl = await cv.getAttribute('data-tlines')
ok(/trail-/.test(tl ?? ''), '图上画出跟踪止损线')
await p.locator('.mk-tabs button, .mk-tabs [role=radio]').filter({ hasText: /委托/ }).first().click()
await p.waitForSelector('[data-algo-list]', { timeout: 4000 })
ok(/跟踪止损/.test(await p.locator('[data-algo-list]').innerText()), '当前委托里列出跟踪止损')
await p.screenshot({ path: `${OUT}/algo-list.png` })
await flatten()
// 英文（模板名是用户自己写的，先去掉）
await p.evaluate(() => localStorage.removeItem('qs.templates'))
await p.reload()
await p.waitForSelector('.op')
await p.locator('.bar-lang button, .bar-lang [role=radio]').filter({ hasText: 'EN' }).first().click()
await p.waitForTimeout(800)
const en = await p.locator('.op').first().innerText()
ok(!/[一-鿿]/.test(en), `英文没有中文：${(en.match(/[一-鿿].{0,10}/) ?? [''])[0]}`)
await p.locator('.bar-lang button, .bar-lang [role=radio]').filter({ hasText: '中' }).first().click()
await p.waitForTimeout(400)
// 设备
const state = await c.storageState()
const bad = []
for (const d of DEVICES) {
  const cc = await browser.newContext({ storageState: state, viewport: { width: d.w, height: d.h }, deviceScaleFactor: d.dpr, isMobile: d.w < 500, hasTouch: d.w < 500, locale: 'zh-CN', timezoneId: 'Asia/Taipei' })
  await cc.addInitScript(() => localStorage.setItem('qs.wizardSeen', '1'))
  const q = await cc.newPage()
  const e2 = []
  q.on('pageerror', (e) => e2.push(String(e)))
  await q.goto(BASE + '/?page=trade&i=BTC-PERP')
  await q.waitForSelector('.mk-product', { timeout: 15000 }).catch(() => {})
  await q.waitForTimeout(800)
  const o = await q.evaluate(() => [document.documentElement.scrollHeight - innerHeight, document.documentElement.scrollWidth - innerWidth])
  if (o[0] > 0 || o[1] > 0 || e2.length) bad.push(`${d.id} ${o} ${e2[0] ?? ''}`)
  await cc.close()
}
ok(bad.length === 0, `16 种设备整页不滚、不溢出、不报错（${bad.join('；')}）`)
ok(errs.length === 0, `流程中没有报错（${errs[0] ?? ''}）`)
console.log(`\n通过 ${pass}，失败 ${fail}`)
await browser.close()
