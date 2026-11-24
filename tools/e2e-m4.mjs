// 模块 4 的检查（4.1–4.27）：手动优先、纪律锁、风险页。用价格注入真的触发锁定。
// 会注册账号、下单，只能对着模拟行情的测试环境跑。
//   node tools/e2e-m4.mjs <截图目录> <网址>
import { chromium } from 'playwright'
import { DEVICES } from './devices.mjs'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || process.env.BASE || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }

const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const newPage = async (opts = {}) => {
  const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN', timezoneId: 'Asia/Taipei', ...opts })
  await c.addInitScript(() => localStorage.setItem('qs.wizardSeen', '1'))
  const p = await c.newPage()
  p.errs = []
  p.on('pageerror', (e) => p.errs.push(String(e)))
  return p
}
const call = (p, method, path, body) => p.evaluate(async ({ method, path, body }) => {
  const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
  let j = null
  try { j = await r.json() } catch { /* 空 */ }
  return { s: r.status, b: j }
}, { method, path, body })
const hasText = async (p, re) => re.test(await p.locator('body').innerText())
const until = async (fn, ms = 8000) => {
  const t0 = Date.now()
  while (Date.now() - t0 < ms) {
    const v = await fn()
    if (v) return v
    await new Promise((r) => setTimeout(r, 200))
  }
  return null
}
const rand = () => Math.random().toString(36).slice(2)
const register = async (p) => {
  const email = `m4${Date.now()}${rand().slice(0, 4)}@example.com`
  const password = `m4-${rand()}-pass`
  await p.goto(BASE + '/?page=login')
  const r = await call(p, 'POST', '/api/auth/register', { email, password })
  if (r.s >= 300) throw new Error('注册失败 ' + JSON.stringify(r))
  // D107：新用户的纪律锁默认关着；这个脚本测纪律锁，自己打开（打开不要二级密码）
  const d0 = (await call(p, 'GET', '/api/discipline')).b
  ok(d0.enabled === false, '新用户的纪律锁默认关着（D107）')
  ok((await call(p, 'POST', '/api/discipline/enabled', { on: true })).b.enabled === true, '打开不要二级密码')
  return { email, password }
}
const noScroll = (p) => p.evaluate(() => document.documentElement.scrollHeight <= innerHeight && document.documentElement.scrollWidth <= innerWidth)
const price = async (p, inst) => {
  const v = await call(p, 'GET', '/api/market/snapshot')
  return Number(v.b.instruments.find((x) => x.instrument === inst).last)
}
const inject = (p, inst, px) => call(p, 'POST', '/api/dev/sim-price', { instrument: inst, price: px })
const marketOrder = (p, acct, side, qty, extra = {}) => call(p, 'POST', `/api/trade/${acct}/orders`, { instrument: 'BTC-PERP', side, type: 'market', qty: String(qty), ...extra })
const discOf = async (p, acct) => {
  const v = (await call(p, 'GET', '/api/discipline?tz=480')).b
  return { v, a: v.accounts.find((x) => x.account === acct) }
}

// ---------- 4.21 没登录 ----------
{
  const p = await newPage()
  const probe = await p.request.post(BASE + '/api/dev/sim-price', { data: {} })
  if (probe.status() === 404) { console.error('这不是模拟行情的测试环境，退出'); process.exit(2) }
  await p.goto(BASE + '/?page=risk')
  await p.waitForSelector('.out', { timeout: 10000 })
  ok(/假设/.test(await p.locator('.out').innerText()) && await p.locator('.out .btn').count() === 1, '没登录：写明假设，加登录入口')
  ok(await p.locator('nav a').allInnerTexts().then((x) => x.join(',') === '市场,策略,交易,风险,复盘,介绍'), '导航有风险和复盘')
  ok(await noScroll(p), '整页不滚动')
  ok((await p.request.get(BASE + '/api/risk/overview')).status() === 401 && (await p.request.get(BASE + '/api/discipline')).status() === 401, '接口没登录 401')
  await p.context().close()
}

// ---------- 用户 A：纪律锁 ----------
const p = await newPage()
const cred = await register(p)
const tradePw = `tp-${rand()}`
ok((await call(p, 'POST', '/api/me/trade-password', { new: tradePw })).s === 200, '设置二级密码')
const list = (await call(p, 'GET', '/api/paper-accounts')).b
const papers = list.accounts ?? list
const A = papers[0].id
await p.goto(BASE + '/?page=risk')
await p.waitForSelector('.rk-main', { timeout: 10000 })

// 4.10 默认规则
{
  const { v, a } = await until(async () => { const d = await discOf(p, A); return d.a ? d : null })
  ok(v.enabled === true, '默认开启')
  ok(Number(v.rules.daily_loss) === 0.05 && Number(v.rules.per_trade) === 0.01 && Number(v.rules.max_drawdown) === 0.15 && Number(v.rules.peak_r) === 3 && Number(v.rules.stop_r) === 1 && v.rules.loss_streak === 3 && v.rules.cooldown_min === 30, '默认规则和 D80 一样')
  ok(!a.lock, '开始没有锁定')
  ok(/没有持仓/.test(await p.locator('.rk-f .rk-none').innerText()) && await p.locator('.rk-main[data-empty]').count() === 1, '没有持仓时写没有持仓')
}

// 4.14 每笔风险
const px0 = await price(p, 'BTC-PERP')
{
  const r = await marketOrder(p, A, 'buy', '0.05', { sl: String(Math.round(px0 * 0.9)) })
  ok(r.s === 400 && r.b.code === 'DISCIPLINE_RISK' && Number(r.b.params.limit) === 100, `带止损最多亏超过 1% 拒单（${r.b.code} 风险 ${r.b.params?.risk} 上限 ${r.b.params?.limit}）`)
  const q = (1000000 / px0 / 10000 / 0.005).toFixed(3) // 止损 0.5%、最多亏不到 100
  const ok2 = await marketOrder(p, A, 'buy', Math.min(0.02, Number(q)).toFixed(3), { sl: String(Math.round(px0 * 0.995)) })
  ok(ok2.s === 200, `止损够近就接受（${ok2.s} ${ok2.b?.code ?? ''}）`)
  await call(p, 'POST', `/api/trade/${A}/close`, { instrument: 'BTC-PERP' })
}

// 4.14 下单区：没带止损写明，带了止损写最多亏多少
{
  await p.goto(BASE + '/?page=trade&i=BTC-PERP')
  await p.waitForSelector('.op-submit', { timeout: 10000 })
  await p.locator('.op-unit button').last().click()
  await p.locator('.op-in input').last().fill('2000')
  await p.waitForTimeout(800)
  ok(/最多亏/.test(await p.locator('.op-key').innerText()) && /没设止损/.test(await p.locator('.op-key').innerText()), '下单区写明没设止损')
  await p.locator('.op-check input').nth(1).check()
  await p.waitForSelector('.op-tpsl')
  await p.locator('.op-tpsl input').nth(1).fill(String(Math.round(px0 * 0.9)))
  await p.waitForTimeout(800)
  const row = await p.locator('.op-key').innerText()
  ok(/最多亏/.test(row) && /\/ *(100|99\.\d+)/.test(row.replace(/,/g, '')), `带止损写最多亏和上限（${row.replace(/\s+/g, ' ').slice(-40)}）`)
  ok(await p.locator('.op-risk-over').count() === 1, '超过上限标红')
  await p.screenshot({ path: `${OUT}/m4-trade-risk.png` })
  await p.goto(BASE + '/?page=risk')
  await p.waitForSelector('.rk-main', { timeout: 10000 })
}

// 4.22、4.24 有持仓时的风险页
const qty = (Math.floor((30000 / px0) * 1000) / 1000).toFixed(3) // 名义价值约 3 万
{
  const r = await marketOrder(p, A, 'buy', qty)
  ok(r.s === 200, `没带止损不拦（${r.s} ${r.b?.code ?? ''}）`)
  await p.reload()
  await p.waitForSelector('.rk-row', { timeout: 10000 })
  ok(await p.locator('.rk-row').count() === 1 && /手动/.test(await p.locator('.rk-grp').first().innerText()), '持仓列在手动分组里')
  const t = await p.locator('.rk-ros').innerText()
  ok(/总敞口/.test(t) && /净敞口/.test(t) && /折合 BTC/.test(t) && /保证金占用/.test(t) && /BTC 跌 1%，这些仓位约亏/.test(t), '写总敞口、净敞口、折合 BTC、保证金占用和 BTC 跌 1% 的结果')
  const ov = (await call(p, 'GET', '/api/risk/overview')).b.accounts.find((x) => x.id === A)
  const pos = ov.positions[0]
  ok(Number(pos.beta) === 1 && pos.engine === 'manual', 'BTC 联动系数是 1，归手动')
  ok(Math.abs(Number(ov.gross) - Number(pos.notional)) < 0.01 && Math.abs(Number(ov.btc_equiv) - Number(pos.notional)) < 0.01, '单个多单时总敞口 = 折合 BTC = 名义价值')
  // 手算压力测试：跌 10% 时权益 = 现在 − 名义价值 × 10%（10 倍全仓，还没到强平）
  const s = ov.stress
  const i0 = s.moves.indexOf(0), i10 = s.moves.indexOf(-10)
  const want = Number(s.equity[i0]) - Number(pos.qty) * Number(pos.mark) * 0.1
  ok(Math.abs(Number(s.equity[i10]) - want) < 1, `压力测试手算对得上（${s.equity[i10]} / ${want.toFixed(2)}）`)
  await p.screenshot({ path: `${OUT}/m4-risk-pos.png` })
  // 滑块：键盘和在图上拖都能改，句子跟着变
  const before = await p.locator('.rk-g-say').innerText()
  await p.locator('.rk-g .sld').focus()
  await p.keyboard.press('ArrowLeft')
  await p.waitForTimeout(80)
  ok((await p.locator('.rk-slide-v').innerText()).includes('11') && (await p.locator('.rk-g-say').innerText()) !== before, '方向键调滑块，结果立刻变')
  const box = await p.locator('.rk-chart').boundingBox()
  await p.mouse.move(box.x + 60, box.y + box.height / 2)
  await p.mouse.down()
  await p.waitForTimeout(40)
  ok((await p.locator('.rk-slide-v').innerText()).includes('50'), '在图上按住拖到最左：BTC −50%')
  await p.mouse.up()
  ok(/强平/.test(await p.locator('.rk-g-say').innerText()), 'BTC −50% 时写强平')
  await p.screenshot({ path: `${OUT}/m4-risk-stress.png` })
}

// 4.13、4.18 单日亏损锁定：把价格打下去
{
  const lim = await call(p, 'POST', `/api/trade/${A}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'limit', tif: 'GTC', price: String(Math.round(px0 * 0.98)), qty: '0.01' }) // BTC 价格上下限 ±3%（D92）
  ok(lim.s === 200, `先挂一张开仓单（${lim.s} ${lim.b?.code ?? ''}）`)
  await inject(p, 'BTC-PERP', px0 * 0.975)
  const got = await until(async () => { const d = await discOf(p, A); return d.a?.lock ? d : null }, 10000)
  ok(got?.a.lock.rule === 'daily_loss', `亏过 5% 锁定（${JSON.stringify(got?.a.lock ?? null).slice(0, 80)}）`)
  ok(got?.a.lock.until > Date.now(), '写明解开时间')
  const open = await marketOrder(p, A, 'buy', '0.01')
  ok(open.s === 400 && open.b.code === 'DISCIPLINE_LOCKED' && open.b.params.rule === 'daily_loss', '开仓被拒，写明规则')
  const view = (await call(p, 'GET', `/api/trade/${A}`)).b
  ok(!view.orders.some((o) => o.id === lim.b.id), '已挂的开仓单撤掉')
  ok(view.notices.some((n) => n.kind === 'discipline_lock'), '记了提醒')
  const undo = await call(p, 'POST', `/api/trade/${A}/orders/${lim.b.id}/undo`, {})
  ok(undo.s >= 400, '撤回也不能绕过')
  const red = await call(p, 'POST', `/api/trade/${A}/close`, { instrument: 'BTC-PERP', fraction: '0.1' })
  ok(red.s === 200, `减仓照样接受（${red.s} ${red.b?.code ?? ''}）`)
  const off = await call(p, 'POST', '/api/discipline/enabled', { on: false, trade_password: tradePw })
  ok(off.s === 409 && off.b.code === 'DISCIPLINE_IS_LOCKED', '锁定中不能关')
  const v0 = (await discOf(p, A)).v
  const loose = await call(p, 'PUT', '/api/discipline/rules', { trade_password: tradePw, rules: { ...v0.rules, daily_loss: '0.2' } })
  ok(loose.s === 409, '锁定中不能放宽')
  // 界面
  await p.goto(BASE + '/?page=risk')
  await p.waitForSelector('.rk-lock', { timeout: 10000 })
  ok(/已锁定：单日亏损。只能减仓和平仓/.test(await p.locator('.rk-lock').innerText()) && /解开还有/.test(await p.locator('.rk-lock').innerText()), '风险页写规则和倒计时')
  const a = await p.locator('.rk-lock-t b').innerText()
  await p.waitForTimeout(1100)
  ok(a !== await p.locator('.rk-lock-t b').innerText(), '倒计时在走')
  await p.screenshot({ path: `${OUT}/m4-risk-locked.png` })
  // 下单区和顶部
  await p.goto(BASE + `/?page=trade&i=BTC-PERP`)
  await p.waitForSelector('.op-lock', { timeout: 10000 })
  ok(/只能减仓和平仓/.test(await p.locator('.op-lock').innerText()), '下单区写明只能减仓和平仓')
  ok(await p.locator('.mk-lock').count() === 1, '顶部风险条有锁定标记')
  await p.locator('.mk-chart').waitFor()
  await p.screenshot({ path: `${OUT}/m4-trade-locked.png` })
}

// 4.19 解锁
{
  await p.goto(BASE + '/?page=risk')
  await p.waitForSelector('.rk-lock button')
  await p.locator('.rk-lock button').click()
  await p.waitForSelector('.dlg input')
  await p.locator('.dlg input').fill('wrong-password')
  await p.locator('.dlg .btn.primary').click()
  await p.waitForTimeout(400)
  ok(await p.locator('.dlg').count() === 1 && (await p.locator('.dlg').innerText()).length > 10, '二级密码错了：留在对话框')
  ok(!(await discOf(p, A)).a.unlock_used, '密码错不算次数')
  await p.locator('.dlg input').fill(tradePw)
  await p.locator('.dlg .btn.primary').click()
  await p.waitForTimeout(500)
  const { a } = await discOf(p, A)
  ok(a.lock.unlock_at > Date.now() + 590000 && a.lock.unlock_at < Date.now() + 610000, '10 分钟后生效')
  ok(a.unlock_used, '当天的次数用掉了')
  ok(/解锁倒计时/.test(await p.locator('.rk-lock').innerText()), '界面写解锁倒计时')
  const again = await call(p, 'POST', `/api/discipline/${A}/unlock`, { trade_password: tradePw })
  ok(again.s === 409 && again.b.code === 'UNLOCK_PENDING', '不能重复申请')
  const still = await marketOrder(p, A, 'buy', '0.01')
  ok(still.s === 400 && still.b.code === 'DISCIPLINE_LOCKED', '等待期间还是锁定的')
  const hist = (await call(p, 'GET', `/api/discipline/${A}/history`)).b.events
  ok(hist.some((e) => e.k === 'lock') && hist.some((e) => e.k === 'unlock_req') && hist.some((e) => e.k === 'reject'), `锁定、申请解锁、拒单都有记录（${hist.length} 条）`)
  await p.screenshot({ path: `${OUT}/m4-risk-unlocking.png` })
  ok(await until(() => p.locator('.rk-hist').innerText().then((x) => /锁定：单日亏损/.test(x) && /申请解锁/.test(x) && /拒单/.test(x)).catch(() => false), 8000) === true, '界面能查到什么时候锁的、谁申请解的、拒过什么单')
  // 4.27 隐藏金额
  await p.keyboard.press('h')
  await p.waitForTimeout(200)
  const masked = await p.locator('.rk-ros').innerText()
  ok(/•/.test(masked) && !/\d,\d{3}\.\d{2}/.test(masked), '按 H 隐藏金额')
  await p.keyboard.press('h')
}
await inject(p, 'BTC-PERP', px0) // 还原价格

// ---------- 用户 B：规则、开关、手动优先 ----------
const q = await newPage()
const credB = await register(q)
const pwB = `tp-${rand()}`
await call(q, 'POST', '/api/me/trade-password', { new: pwB })
{
  // 4.12
  const v = await until(async () => { const d = (await call(q, 'GET', '/api/discipline')).b; return d.rules ? d : null })
  ok((await call(q, 'PUT', '/api/discipline/rules', { rules: { ...v.rules, daily_loss: '0.03' } })).s === 200, '收紧不要密码')
  ok((await call(q, 'PUT', '/api/discipline/rules', { rules: { ...v.rules, daily_loss: '0.08' } })).b.code === 'TRADE_PASSWORD_EMPTY', '放宽要二级密码（空着只提示要填，不算输错）')
  ok((await call(q, 'PUT', '/api/discipline/rules', { trade_password: pwB, rules: { ...v.rules, daily_loss: '0.08' } })).s === 200, '放宽输密码可以')
  ok((await call(q, 'PUT', '/api/discipline/rules', { trade_password: pwB, rules: { ...v.rules, daily_loss: '0.9' } })).b.code === 'BAD_DAILY_LOSS', '超范围拒绝')
  ok((await call(q, 'PUT', '/api/discipline/rules', { rules: v.rules })).s === 200, '恢复默认（从 8% 收回 5%）')
  // 4.11
  ok((await call(q, 'POST', '/api/discipline/enabled', { on: false, trade_password: 'x' })).b.code === 'BAD_TRADE_PASSWORD', '关闭要二级密码')
  ok((await call(q, 'POST', '/api/discipline/enabled', { on: false, trade_password: pwB })).b.enabled === false, '输对密码可以关')
  await call(q, 'POST', '/api/auth/logout', {})
  const li = await call(q, 'POST', '/api/auth/login', { email: credB.email, password: credB.password })
  ok(li.s === 200, '重新登录')
  ok((await call(q, 'GET', '/api/discipline')).b.enabled === false, '登录不会自动打开：关了就一直关着（D107）')
  await call(q, 'POST', '/api/discipline/enabled', { on: true })
  // 规则对话框
  await q.goto(BASE + '/?page=risk')
  await q.waitForSelector('.rk-acts button')
  await q.locator('.rk-acts button').first().click()
  await q.waitForSelector('.rk-rules')
  await q.locator('.rk-rules input').first().fill('3')
  await q.waitForTimeout(100)
  ok(await q.locator('.rk-rules input[type=password]').count() === 0, '收紧时不显示密码框')
  await q.locator('.rk-rules input').first().fill('9')
  await q.waitForTimeout(100)
  ok(await q.locator('.rk-rules input[type=password]').count() === 1, '放宽时显示密码框')
  await q.screenshot({ path: `${OUT}/m4-rules.png` })
  await q.locator('.rk-rules .btn.ghost').click()
}

// 4.1–4.8 手动优先
{
  const strat = (await call(q, 'GET', '/api/strategies')).b[0].strategy_id
  const st = await call(q, 'POST', '/api/follows', { strategy: strat, alloc: '5000', confirm: true })
  ok(st.s === 200 || st.s === 201, `开启跟随（${st.s} ${st.b?.code ?? ''}）`)
  const B = st.b.account
  await q.goto(BASE + '/?page=trade&i=BTC-PERP')
  await q.waitForSelector('.mk-chart', { timeout: 10000 })
  await q.evaluate((id) => localStorage.setItem('qs.paper', String(id)), B)
  await q.reload()
  await q.waitForSelector('.mk-chart', { timeout: 10000 })
  const px = await price(q, 'BTC-PERP')
  const o = await marketOrder(q, B, 'buy', (Math.ceil((200 / px) * 1000) / 1000).toFixed(3))
  ok(o.s === 200, `手动下单直接生效，没有拦（${o.s} ${o.b?.code ?? ''}）`)
  const f = (await call(q, 'GET', '/api/follows')).b.follows.find((x) => x.account === B)
  ok(!!f.manual['BTC-PERP'], '这个币转为手动管理')
  ok(f.records[0].state === 'manual', '记进执行记录')
  ok(await until(() => hasText(q, /BTC 已转为手动管理，策略不再调整/), 4000) === true, '马上提示')
  await q.screenshot({ path: `${OUT}/m4-takeover.png` })
  ok(await until(() => hasText(q, /手动管理：BTC/), 8000) === true, '下单区列出手动管理的币')
  const ov = (await call(q, 'GET', '/api/risk/overview')).b.accounts.find((x) => x.id === B)
  ok(ov.following && ov.positions.find((x) => x.instrument === 'BTC-PERP').engine === 'manual', '风险页把它归到手动')
  await call(q, 'POST', `/api/trade/${B}/close`, { instrument: 'BTC-PERP' })
  const f2 = await until(async () => { const x = (await call(q, 'GET', '/api/follows')).b.follows.find((y) => y.account === B); return !x.manual['BTC-PERP'] ? x : null })
  ok(!!f2 && f2.records.some((r) => r.state === 'manual_end'), '平仓后交回策略')
  ok(await until(() => hasText(q, /BTC 已平仓，交回策略/), 4000) === true, '提示交回策略')
  // 下单区不再弹"会被调仓改掉"
  ok(!(await hasText(q, /下一次调仓可能改掉你的仓位/)), '不再先弹窗拦')
  await q.goto(BASE + '/?page=risk')
  await q.waitForSelector('.rk-f')
  ok(/手动优先级最高/.test(await q.locator('.rk-f .display-head').innerText()), '4.9 写明优先级')
  await q.goto(BASE + '/?page=market')
  await q.waitForSelector('.mb-map canvas')
  await q.waitForTimeout(3000)
  await q.screenshot({ path: `${OUT}/m4-market-ai.png` })
}

// ---------- 多种窗口看一遍（登录、有持仓、有锁定） ----------
const state = await p.context().storageState()
for (const [name, w, h, dpr] of [['1536', 1536, 730, 1.25], ['1142', 1142, 778, 1.25], ['800', 800, 730, 1.25]]) {
  const v = await newPage({ viewport: { width: w, height: h }, deviceScaleFactor: dpr, storageState: state })
  await v.goto(BASE + '/?page=risk')
  await v.waitForSelector('.rk-main', { timeout: 10000 })
  await v.waitForTimeout(2500)
  await v.screenshot({ path: `${OUT}/m4-risk-${name}.png` })
  if (w <= 1000) {
    for (const tab of ['g', 'h']) {
      await v.locator(`.rk-tabs [data-tab="${tab}"]`).click()
      await v.waitForTimeout(300)
      ok(await v.locator(`.rk-${tab}.on`).count() === 1, `窄屏切到 ${tab}`)
      await v.screenshot({ path: `${OUT}/m4-risk-${name}-${tab}.png` })
    }
  }
  await v.context().close()
}

// 英文
{
  const v = await newPage({ storageState: state, locale: 'en-US' })
  await v.goto(BASE + '/?page=risk')
  await v.waitForSelector('.rk-main', { timeout: 10000 })
  await v.evaluate(() => fetch('/api/me/lang', { method: 'POST', headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: JSON.stringify({ lang: 'en' }) }))
  await v.locator('.bar-lang button').nth(1).click()
  await v.waitForTimeout(800)
  const txt = await v.locator('body').innerText()
  ok(!/rk\.|disc\.|bar\.risk|undefined|NaN/.test(txt), '英文没有漏词条')
  ok(/Stress test/.test(txt) && /Discipline/.test(txt), '英文标题对')
  await v.screenshot({ path: `${OUT}/m4-risk-en.png` })
  await v.locator('.bar-lang button').nth(0).click()
  await v.waitForTimeout(300)
  await v.context().close()
}

// 4.27 16 种设备
for (const d of DEVICES) {
  for (const [name, url, sel] of [['风险页', '/?page=risk', '.rk-main'], ['设置页', '/?page=account', '.ac'], ['AI 页', '/?page=ai', '.ai-head']]) {
    const v = await newPage({ viewport: { width: d.w, height: d.h }, deviceScaleFactor: d.dpr, isMobile: d.w < 500, hasTouch: d.w < 500, storageState: state })
    await v.goto(BASE + url)
    await v.waitForSelector(sel, { timeout: 15000 })
    await v.waitForTimeout(700)
    const m = await v.evaluate(() => ({ down: document.documentElement.scrollHeight - innerHeight, over: document.documentElement.scrollWidth - innerWidth, nan: /NaN|undefined/.test(document.body.innerText), bar: document.querySelector('.bar').scrollWidth - document.querySelector('.bar').clientWidth }))
    ok(m.down <= 0 && m.over <= 0 && !m.nan && m.bar <= 0 && v.errs.length === 0, `${d.name} ${name}：整页不滚，不溢出，无 NaN，顶栏不挤（${m.down}/${m.over}/${m.bar}）${v.errs[0] ?? ''}`)
    await v.context().close()
  }
}

ok(p.errs.length === 0 && q.errs.length === 0, `没有页面报错（${p.errs[0] ?? q.errs[0] ?? ''}）`)
console.log(`\n通过 ${pass}，失败 ${fail}`)
await browser.close()
process.exit(fail ? 1 : 0)
