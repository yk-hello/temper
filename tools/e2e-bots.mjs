// 机器人（D41、D83）：参数面板和图同步、启动、成交、暂停恢复、停止、冲突规则。只对着测试环境跑。
//   node tools/e2e-bots.mjs <截图目录> <网址>
import { chromium } from 'playwright'
import { DEVICES } from './devices.mjs'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge', args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'] })
const rnd = () => Math.random().toString(36).slice(2)
const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN', timezoneId: 'Asia/Taipei' })
await c.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1'); localStorage.setItem('qs.inst', 'BTC-SPOT') })
const p = await c.newPage()
const errs = []
p.on('pageerror', (e) => errs.push(String(e)))
const call = (method, path, body) => p.evaluate(async ({ method, path, body }) => { const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined }); return { s: r.status, b: await r.json().catch(() => null) } }, { method, path, body })
const until = async (fn, ms = 10000) => { const t0 = Date.now(); while (Date.now() - t0 < ms) { const v = await fn(); if (v) return v; await new Promise((r) => setTimeout(r, 250)) } return null }
await p.goto(BASE + '/?page=login')
await call('POST', '/api/auth/register', { email: `bot${Date.now()}${rnd().slice(0, 4)}@example.com`, password: 'pw-' + rnd() + rnd() })
const tp = 'tp-' + rnd()
await call('POST', '/api/me/trade-password', { new: tp })
await call('POST', '/api/discipline/enabled', { on: false, trade_password: tp })
const A = (await call('GET', '/api/paper-accounts')).b.accounts[0].id
await call('POST', `/api/spot/${A}/transfer`, { to: 'spot', amount: '6000' })
const snap = async (inst) => (await call('GET', '/api/market/snapshot')).b.instruments.find((x) => x.instrument === inst)
const px0 = Number((await until(async () => { const s = await snap('BTC-SPOT'); return s?.bid ? s : null })).bid)
const round = (v, d = 2) => Number(v).toFixed(d)
const bots = async () => (await call('GET', '/api/bots')).b.bots
const botOf = async (id) => (await bots()).find((b) => b.id === id)
const stopBot = (id, mode = 'close') => call('POST', `/api/bots/${id}/stop`, { mode })
const params = (extra = {}) => ({ instrument: 'BTC-SPOT', lower: round(px0 * 0.98), upper: round(px0 * 1.02), grids: 10, invest: '1000', ...extra })

// 接口：算计划
let r = await call('POST', '/api/bots/preview', { kind: 'spot_grid', params: params() })
ok(r.s === 200 && r.b.lines.length === 11, `算出 11 条线（${r.s}）`)
ok(Number(r.b.net_lo) > 0 && Number(r.b.need) === 1000 && r.b.warn.includes('GRID_NO_STOP'), `每格净赚 ${r.b.net_lo}（数量 ${r.b.qty} / ${r.b.sell_qty}，线 ${r.b.lines[0]}–${r.b.lines[1]}），占用 1000，提醒没设止损`)
r = await call('POST', '/api/bots/preview', { kind: 'spot_grid', params: params({ grids: 500 }) })
ok(r.s === 422 && r.b.code === 'GRID_BAD_COUNT', '格数太多被拒')
r = await call('POST', '/api/bots/preview', { kind: 'spot_grid', params: params({ lower: round(px0 * 0.9999), upper: round(px0 * 1.0001), grids: 10 }) })
ok(r.s === 200 && r.b.warn.includes('GRID_NET_NEGATIVE'), `格子太窄提醒赚不够手续费（${r.b?.warn ?? r.b?.code}）`)

// 界面：面板和图同步
await p.goto(BASE + '/?page=trade&i=BTC-SPOT')
await p.waitForSelector('.mk-product')
await p.waitForTimeout(1500)
await p.locator('.mk-side .display-head button, .mk-side .display-head [role=radio]').filter({ hasText: '机器人' }).first().click()
await p.waitForSelector('[data-bot-panel]')
await until(() => p.locator('[data-bot-plan]').count())
ok(await p.locator('[data-bot-plan]').count() === 1, '默认的现货网格算好了')
const planText = await p.locator('[data-bot-plan]').innerText()
ok(/每格净赚/.test(planText) && /占用资金/.test(planText) && /大约亏/.test(planText), `写着净赚、占用、最多亏：${planText.replace(/\s+/g, ' ').slice(0, 70)}`)
await p.waitForTimeout(800)
const cv = p.locator('.mk-chart canvas.pannable')
const lineOf = async (id) => JSON.parse((await cv.getAttribute('data-tlines')) ?? '[]').find((l) => l.id === id)
let up = await until(() => lineOf('bot-upper'))
ok(!!up && !!(await lineOf('bot-lower')), '图上画出上下限')
await p.screenshot({ path: `${OUT}/bot-setup.png` })
// 在图上拖上限：面板跟着变
const bb = await cv.boundingBox()
const upperIn = p.locator('[data-bot-field=upper]')
const before = await upperIn.inputValue()
await p.mouse.move(bb.x + up.x + 10, bb.y + up.ty)
await p.mouse.down()
await p.mouse.move(bb.x + up.x + 10, bb.y + up.ty + 26, { steps: 6 })
await p.mouse.up()
await p.waitForTimeout(500)
const after = await upperIn.inputValue()
ok(after !== before && Number(after) < Number(before), `在图上拖上限，面板跟着变（${before} → ${after}）`)
// 在面板改：图跟着变
const typed = round(Number((await snap('BTC-SPOT')).bid) * 1.015, 2) // 按现在的价算：贴着现价的线会让给现价标签，不算在画面里
await upperIn.fill(typed)
await p.waitForTimeout(1200)
await p.screenshot({ path: `${OUT}/bot-typed.png` })
up = await lineOf('bot-upper')
ok(up && Math.abs(Number(up.p) - Number(typed)) < 1, `面板输入，图上的线跟着变（${up?.p}；${(await cv.getAttribute('data-tlines'))?.slice(0, 140)}）`)
// 格子太窄的提醒看得见
await p.locator('[data-bot-field=grids]').fill('100')
await p.waitForTimeout(800)
const warnN = await p.locator('[data-bot-warn=GRID_NET_NEGATIVE]').count()
ok(warnN === 1, '格子太多时写明赚不够手续费')
await p.locator('[data-bot-field=grids]').fill('6')
await p.locator('[data-bot-field=lower]').fill(round(px0 * 0.985))
await p.locator('[data-bot-field=upper]').fill(round(px0 * 1.015))
await p.locator('[data-bot-field=invest]').fill('1500')
await p.waitForTimeout(800)
// 启动
await p.locator('[data-bot-start]').click()
await p.waitForSelector('[data-bot-confirm]')
await p.screenshot({ path: `${OUT}/bot-confirm.png` })
await p.locator('[data-bot-confirm]').click()
const b1 = await until(async () => (await bots()).find((b) => b.kind === 'spot_grid'))
ok(!!b1 && b1.status === 'running', '启动了')
const open1 = (await call('GET', `/api/spot/${A}`)).b.orders.length
ok(open1 === 6, `挂了 6 张单（${open1}）`)
await p.locator('.mk-tabs button, .mk-tabs [role=radio]').filter({ hasText: /机器人/ }).first().click()
await p.waitForSelector('[data-bot-list]')
await p.waitForSelector('[data-bot-read]')
ok(/现货网格/.test(await p.locator('[data-bot-read]').innerText()), '图上面一行写机器人现在怎么样')
await p.waitForTimeout(1500)
await p.screenshot({ path: `${OUT}/bot-running.png` })
// 让价格走：跌穿买单，再涨回来，赚一格
const inject = (px) => call('POST', '/api/dev/sim-price', { instrument: 'BTC-PERP', price: Number(px) })
const lines = b1.plan.lines.map(Number)
const below = lines.filter((x) => x < px0).sort((a, c2) => c2 - a)[0]
const above = lines.filter((x) => x > below)[0]
await inject(round(below * 0.999, 1))
const filled = await until(async () => { const b = await botOf(b1.id); return b.fills > 1 ? b : null }, 15000)
ok(!!filled, `价格跌穿买单，成交了（${filled?.fills} 笔）`)
await inject(round(above * 1.003, 1))
const earned = await until(async () => { const b = await botOf(b1.id); return b.pairs >= 1 ? b : null }, 15000)
ok(!!earned && Number(earned.realized) > 0, `涨回来卖掉，赚一格（${earned?.realized}）`)
await p.locator('[data-bot-row]').first().click()
await p.waitForSelector('.bt-recs')
await p.screenshot({ path: `${OUT}/bot-earned.png` })
// 手动单碰上机器人的币：直接成交，机器人暂停（D83）
r = await call('POST', `/api/spot/${A}/orders`, { instrument: 'BTC-SPOT', side: 'buy', type: 'market', qty: '0.001' })
ok(r.s === 200, '手动单不被拦')
const paused = await until(async () => { const b = await botOf(b1.id); return b.status === 'paused' ? b : null })
ok(paused?.reason === 'MANUAL', `机器人暂停，写明原因（${paused?.reason}）`)
ok((await call('GET', `/api/spot/${A}`)).b.orders.length === 0, '暂停时挂单撤掉')
await p.waitForTimeout(1800)
ok(/手动单成交/.test(await p.locator('[data-bot-list]').innerText()), '列表写明因为手动单暂停')
// 恢复
await p.locator('[data-bot-resume]').first().click()
const resumed = await until(async () => { const b = await botOf(b1.id); return b.status === 'running' && (await call('GET', `/api/spot/${A}`)).b.orders.length === 6 ? b : null })
ok(!!resumed, '恢复后接着挂单')
// 同一个币不能开两个
r = await call('POST', '/api/bots', { kind: 'spot_grid', account: A, confirm: true, params: params() })
ok(r.s === 422 && r.b.code === 'BOT_COIN_BUSY', '同一个账户同一个币不能开两个')
r = await call('POST', '/api/bots', { kind: 'spot_grid', account: A, params: params() })
ok(r.s === 400, '没确认不能开')
// 停止：留货转手动
await p.waitForTimeout(1600)
await p.locator('[data-bot-stop]').first().click()
await p.waitForSelector('[data-bot-stop-mode=keep]')
await p.screenshot({ path: `${OUT}/bot-stop.png` })
const held = Number((await call('GET', `/api/spot/${A}`)).b.balances.find((x) => x.coin === 'BTC').free)
await p.locator('[data-bot-stop-mode=keep]').click()
const stopped = await until(async () => { const b = await botOf(b1.id); return b.status === 'stopped' ? b : null })
ok(stopped?.reason === 'USER', '停了')
const v0 = (await call('GET', `/api/spot/${A}`)).b
ok(v0.orders.length === 0, '挂单全撤')
ok(Number(v0.balances.find((x) => x.coin === 'BTC').free) >= held - 1e-8, '留着货')

// DCA：现货 ETH
const eth = Number((await until(async () => { const s = await snap('ETH-SPOT'); return s?.bid ? s : null })).bid)
r = await call('POST', '/api/bots', { kind: 'dca', account: A, confirm: true, params: { instrument: 'ETH-SPOT', side: 'long', first: '100', add: '100', step_pct: '2', mult: '1.5', max_adds: 3, tp_pct: '1.5' } })
ok(r.s === 200 && r.b.rungs.length === 3 && Number(r.b.avg) > 0, `DCA 买了首单，挂 3 档补仓（${r.b?.code ?? ''}）`)
const dca = r.b.id
await call('POST', '/api/dev/sim-price', { instrument: 'ETH-PERP', price: Number(round(eth * 0.975, 2)) })
const added = await until(async () => { const b = await botOf(dca); return b.rungs.some((x) => x.filled) ? b : null }, 15000)
ok(!!added && Number(added.avg) < eth, `跌了补仓，均价下来（${added?.avg}）`)
await call('POST', '/api/dev/sim-price', { instrument: 'ETH-PERP', price: Number(round(eth * 1.05, 2)) })
const dcaDone = await until(async () => { const b = await botOf(dca); return b.status === 'stopped' ? b : null }, 15000)
ok(dcaDone?.reason === 'TAKE_PROFIT' && Number(dcaDone.realized) > 0, `到止盈结束，赚钱（${dcaDone?.realized}）`)

// 抄底：SOL，没跌到不买
const sol = Number((await until(async () => { const s = await snap('SOL-SPOT'); return s?.bid ? s : null })).bid)
r = await call('POST', '/api/bots', { kind: 'dip', account: A, confirm: true, params: { instrument: 'SOL-SPOT', side: 'long', first: '100', add: '100', step_pct: '3', mult: '1', max_adds: 1, tp_pct: '2', trigger_px: round(sol * 0.96, 2) } })
ok(r.s === 200 && r.b.status === 'waiting', '抄底等价格跌到')
const dip = r.b.id
await call('POST', '/api/dev/sim-price', { instrument: 'SOL-PERP', price: Number(round(sol * 0.95, 2)) })
ok(await until(async () => { const b = await botOf(dip); return b.status === 'running' && Number(b.avg) > 0 }, 15000), '跌到才买第一笔')
await stopBot(dip)

// 定投：XRP
r = await call('POST', '/api/bots', { kind: 'plan', account: A, confirm: true, params: { instrument: 'XRP-SPOT', amount: '50', every: 'day', times: 2 } })
ok(r.s === 200, `定投开了（${r.b?.code ?? ''}）`)
const plan = r.b.id
ok(await until(async () => (await botOf(plan)).count === 1), '第一次马上买')
await stopBot(plan)

// 合约网格：这个账户没有仓位
const pp = Number((await snap('BTC-PERP')).last)
const perpP = { instrument: 'BTC-PERP', lower: round(pp * 0.98, 1), upper: round(pp * 1.02, 1), grids: 8, invest: '500', dir: 'neutral', leverage: 3, stop_loss: round(pp * 0.96, 1) }
r = await call('POST', '/api/bots/preview', { kind: 'perp_grid', params: perpP })
ok(r.s === 200 && Number(r.b.liq_px) > 0, `合约网格算强平价（${r.b?.liq_px}）`)
r = await call('POST', '/api/bots', { kind: 'perp_grid', account: A, confirm: true, params: perpP })
ok(r.s === 200, `合约网格开了（${r.b?.code ?? ''}）`)
const pg = r.b?.id
if (pg) {
  const tr = (await call('GET', `/api/trade/${A}`)).b
  ok(tr.orders.filter((o) => o.source?.startsWith('bot:')).length >= 4, `合约挂了网格单（${tr.orders.length}）`)
  // 手动下合约单：同样暂停
  r = await call('POST', `/api/trade/${A}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'market', qty: '0.002' })
  ok(r.s === 200, `手动合约单不被拦（${r.s} ${r.b?.code ?? ''}）`)
  ok(await until(async () => (await botOf(pg)).status === 'paused'), '合约机器人也暂停')
  await stopBot(pg)
  await call('POST', `/api/trade/${A}/orders`, { instrument: 'BTC-PERP', side: 'sell', type: 'market', qty: '0.002', reduce_only: true })
}
// 优先级
r = await call('POST', '/api/bots/order', { order: ['bot', 'ai', 'signal'] })
ok(r.s === 200, '能改引擎优先级')
ok((await call('GET', '/api/bots')).b.order[0] === 'bot', '改了记住')
await call('POST', '/api/bots/order', { order: ['ai', 'signal', 'bot'] })
r = await call('POST', '/api/bots/order', { order: ['ai', 'ai', 'bot'] })
ok(r.s === 422, '顺序写错被拒')
// 复盘：机器人的单默认不算
const ai = (await call('GET', `/api/journal/a/${A}/trades?origin=bot`)).b.trades
const man = (await call('GET', `/api/journal/a/${A}/trades`)).b.trades
ok(ai.length > 0, `机器人的单单独能看（${ai.length} 笔）`)
ok(man.every((t) => t.origin !== 'bot'), '默认只看手动：机器人的单不混进来')
// 风险页：写着机器人
ok(errs.length === 0, `流程中没有报错（${errs[0] ?? ''}）`)
// 英文
await p.goto(BASE + '/?page=trade&i=ETH-SPOT')
await p.waitForSelector('.mk-product')
await p.locator('.bar-lang button, .bar-lang [role=radio]').filter({ hasText: 'EN' }).first().click()
await p.locator('.mk-side .display-head button, .mk-side .display-head [role=radio]').filter({ hasText: 'Bots' }).first().click()
await p.waitForSelector('[data-bot-panel]')
await p.waitForTimeout(1200)
const en = await p.locator('[data-bot-panel]').innerText()
ok(!/[一-鿿]/.test(en), `英文没有中文：${(en.match(/[一-鿿].{0,10}/) ?? [''])[0]}`)
await p.screenshot({ path: `${OUT}/bot-en.png` })
await p.locator('.bar-lang button, .bar-lang [role=radio]').filter({ hasText: '中' }).first().click()
await p.waitForTimeout(500)
// 设备
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
  await q.waitForTimeout(800)
  if (d.w >= 900) await q.locator('.mk-side .display-head button, .mk-side .display-head [role=radio]').filter({ hasText: '机器人' }).first().click().catch(() => {})
  else await q.locator('.mk-tabs button, .mk-tabs [role=radio]').filter({ hasText: /机器人/ }).first().click().catch(() => {})
  await q.waitForTimeout(900)
  const o = await q.evaluate(() => [document.documentElement.scrollHeight - innerHeight, document.documentElement.scrollWidth - innerWidth])
  if (o[0] > 0 || o[1] > 0 || e2.length) bad.push(`${d.id} ${o} ${e2[0] ?? ''}`)
  if (d.w === 393 || d.w === 1097 || d.w === 1366) await q.screenshot({ path: `${OUT}/bot-${d.w}.png` })
  await cc.close()
}
ok(bad.length === 0, `16 种设备整页不滚、不溢出、不报错（${bad.join('；')}）`)
// 复盘：选机器人时每个机器人一张卡（D83 第 9 条）
await p.goto(BASE + '/?page=review')
await p.waitForSelector('.rv-head', { timeout: 15000 })
await p.locator('.rv-tools button, .rv-tools [role=radio]').filter({ hasText: '机器人' }).first().click()
await p.waitForTimeout(1800)
ok(await p.locator('[data-bot-card]').count() >= 1, `复盘里每个机器人一张卡（${await p.locator('[data-bot-card]').count()}）`)
await p.screenshot({ path: `${OUT}/bot-review.png` })
console.log(`\n通过 ${pass}，失败 ${fail}`)
await browser.close()
