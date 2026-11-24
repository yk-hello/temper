// 模块 7 的检查：通知中心和开关、到价提醒、Telegram 绑定（假服务器）、AI 点评（假服务器）、试用到期。
// 用法：node e2e-notify.mjs <截图目录> <前端地址>。会注册账号、下单，不要对着 8080 跑。
// 后端要配 TELEGRAM_BASE、DEEPSEEK_BASE 指向这里起的假服务器（默认 127.0.0.1:18999），token 和 key 随便填。
import { createRequire } from 'module'
import { execFileSync } from 'child_process'
import { randomBytes } from 'crypto'
import fs from 'fs'
import http from 'http'
const require = createRequire(import.meta.url)
const { chromium } = require('playwright-core')
const OUT = process.argv[2] ?? 'shots/notify'
const BASE = process.argv[3] ?? 'http://127.0.0.1:4173'
const PG = process.env.QS_TEST_PG ?? 'qs-test-pg'
const PGDB = process.env.QS_TEST_PGDB ?? 'qs_e2e'
if (/:8080/.test(BASE)) throw new Error('不要对着 8080 跑')
fs.mkdirSync(OUT, { recursive: true })
let bad = 0, good = 0
const ok = (c, s) => { if (c) { good++; console.log('✓', s) } else { bad++; console.log('✗', s) } }
const rnd = () => randomBytes(6).toString('hex')

// ---------- 假的 Telegram 和 DeepSeek ----------
const fake = { sent: [], updates: [], ai: [], mode: 'ok', nextId: Date.now() } // 编号要一直变大：后端记着上次收到哪一条
const AI_ZH = '做对了：按计划离场。\n先改这个：单笔风险超过了上限。\n下次的规则：单笔风险不超过 1%。'
const AI_EN = 'Did well: you left as planned.\nFix first: risk on this trade was over your limit.\nRule for next time: risk at most 1% per trade.'
const srv = http.createServer((req, res) => {
  let raw = ''
  req.on('data', (d) => { raw += d })
  req.on('end', () => {
    const body = raw ? JSON.parse(raw) : {}
    const send = (code, v) => { res.writeHead(code, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(v)) }
    if (req.url.endsWith('/getMe')) return send(200, { ok: true, result: { username: 'FakeBot' } })
    if (req.url.endsWith('/getUpdates')) {
      // 和真的 Telegram 一样：下一次请求带上 offset 才算收到，没收到的会再给一遍
      fake.updates = fake.updates.filter((u) => u.update_id >= (body.offset ?? 0))
      if (fake.updates.length) return send(200, { ok: true, result: fake.updates })
      return setTimeout(() => send(200, { ok: true, result: [] }), 300)
    }
    if (req.url.endsWith('/sendMessage')) { fake.sent.push(body); return send(200, { ok: true, result: {} }) }
    if (req.url.endsWith('/chat/completions')) {
      fake.ai.push({ auth: req.headers.authorization, body })
      if (fake.mode === 'fail') return send(500, { error: { message: 'boom' } })
      const content = fake.mode === 'bad' ? '建议买入 BTC，年化可以到 30%' : (body.messages[0].content.includes('Did well') ? AI_EN : AI_ZH)
      return send(200, { model: 'fake-model', choices: [{ message: { content } }], usage: { total_tokens: 99 } })
    }
    send(404, {})
  })
})
await new Promise((r) => srv.listen(18999, '127.0.0.1', r))

const b = await chromium.launch({ channel: process.env.PW_CHANNEL ?? 'msedge', headless: true })
const ctx = await b.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN', timezoneId: 'Asia/Shanghai' })
await ctx.addInitScript(() => { try { localStorage.setItem('qs.wizardSeen', '1'); localStorage.setItem('qs.heroSeen', '1'); sessionStorage.setItem('qs.heroSeen', '1') } catch {} })
const p = await ctx.newPage()
const errs = []
p.on('pageerror', (e) => errs.push(String(e).slice(0, 300)))
const call = (method, path, body) => p.evaluate(async ({ method, path, body }) => {
  const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
  let j = null
  try { j = await r.json() } catch { /* 没有内容 */ }
  return { s: r.status, b: j }
}, { method, path, body })
const until = async (fn, ms = 12000) => { const t0 = Date.now(); while (Date.now() - t0 < ms) { const v = await fn(); if (v) return v; await new Promise((r) => setTimeout(r, 250)) } return null }
const sql = (q) => execFileSync('docker', ['exec', PG, 'psql', '-U', 'qs', '-d', PGDB, '-tAc', q], { encoding: 'utf8' }).trim()
const items = async () => (await call('GET', '/api/notify')).b.items
const has = (kind) => until(async () => (await items()).find((x) => x.kind === kind))
const simPrice = (px, inst = 'BTC-PERP') => call('POST', '/api/dev/sim-price', { instrument: inst, price: Number(Number(px).toFixed(1)) })
const noZh = (s) => !/[一-鿿＀-￯　-〿]/.test(s)

// ---------- 没登录 ----------
await p.goto(`${BASE}/?page=market`)
await p.waitForSelector('.bar')
ok(await p.locator('[data-bell]').count() === 0, '没登录时没有铃铛')

// ---------- 注册 ----------
const email = `nt${Date.now()}${rnd().slice(0, 4)}@example.com`
await call('POST', '/api/auth/register', { email, password: 'pw-' + rnd() + rnd() })
const tp = 'tp-' + rnd()
await call('POST', '/api/me/trade-password', { new: tp })
await call('POST', '/api/discipline/enabled', { on: false, trade_password: tp })
const A = (await call('GET', '/api/paper-accounts')).b.accounts[0].id
const snap = async (inst) => (await call('GET', '/api/market/snapshot')).b.instruments.find((x) => x.instrument === inst)
const px0 = Number((await until(async () => { const s = await snap('BTC-PERP'); return s?.bid ? s : null })).bid)
await p.goto(`${BASE}/?page=market`)
await p.waitForSelector('[data-bell]', { timeout: 10000 })
ok(true, '登录后有铃铛')
await p.locator('[data-bell]').click()
ok(/没有通知/.test(await p.locator('[data-bell-pop]').innerText()), '没有通知时写明')
await p.keyboard.press('Escape')
ok(await p.locator('[data-bell-pop]').count() === 0, '按 Esc 关掉')

// ---------- 设置 ----------
await p.goto(`${BASE}/?page=account&sec=notify`)
await p.waitForSelector('[data-notify] tbody tr', { timeout: 10000 })
ok(await p.locator('[data-notify] tbody tr').count() === 16, '16 种事件（含每日复盘、我的策略调仓、条件提醒）')
const on = (k, col) => p.locator(`[data-pref=${k}] td:nth-child(${col}) input`).isChecked()
ok(await on('liq_warn', 2) && await on('margin_high', 2) && await on('discipline_lock', 2) && await on('bot_error', 2), '强平、保证金率、纪律锁、机器人默认开')
ok(!(await on('fill', 2)) && !(await on('whale', 2)) && !(await on('econ', 2)) && !(await on('stop_hit', 2)) && !(await on('follow_stop', 2)), '成交、大户、经济事件、止盈止损、跟随默认关')
await p.locator('[data-pref=fill] td:nth-child(2) input').click()
await p.waitForTimeout(400)
await p.reload()
await p.waitForSelector('[data-notify] tbody tr')
ok(await on('fill', 2), '开关保存住了')
ok(await p.locator('[data-tg]').getAttribute('data-tg') === 'none', 'Telegram 配了 token：显示未绑定')
await p.locator('[data-notify-sec]').screenshot({ path: `${OUT}/设置-中文.png` })

// ---------- 成交通知 ----------
await call('POST', `/api/trade/${A}/leverage`, { instrument: 'BTC-PERP', leverage: 20, mode: 'cross' })
const qty = (0.9 * 10000 * 20 / px0 / 1).toFixed(3)
const ord = await call('POST', `/api/trade/${A}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'market', qty })
ok(ord.s === 200, `高杠杆开多（${ord.s} ${ord.b?.code ?? ''}）`)
const fill = await has('fill')
ok(!!fill && fill.params.side === 'buy', '成交通知到了')

// ---------- 到价提醒 ----------
await p.locator('[data-alert-px] input, input[data-alert-px]').first().fill(String(Math.round(px0 * 1.01)))
await p.locator('[data-alert-add]').click()
await p.waitForSelector('[data-alerts] tbody tr', { timeout: 5000 })
ok(/涨到/.test(await p.locator('[data-alerts]').innerText()), '价格在现价上面：写涨到')
await p.goto(`${BASE}/?page=market`)
await p.waitForSelector('[data-bell]')
await p.waitForTimeout(800)
await simPrice(px0 * 1.012)
const hit = await has('price_alert')
ok(!!hit && hit.params.dir === 'up', '到价提醒响了')
ok((await call('GET', '/api/notify/alerts')).b.alerts.length === 0, '响过就删掉')
ok(!!(await until(() => p.locator('.toast.show').filter({ hasText: '涨到' }).count())), '在站内弹了提示')
ok((await call('POST', '/api/notify/alerts', { instrument: 'BTC-PERP', px: -1 })).b.code === 'BAD_PRICE', '负价格被拒')
ok((await call('POST', '/api/notify/alerts', { instrument: 'NOPE-PERP', px: 1 })).b.code === 'UNKNOWN_INSTRUMENT', '不认识的品种被拒')

// ---------- Telegram 绑定 ----------
const got = await until(async () => { const r = await call('GET', '/api/notify/prefs'); return r.b.tg.bot === 'FakeBot' ? r.b : null }, 40000)
ok(!!got && got.tg.enabled, '后端连上了假的 Telegram')
const link = await call('POST', '/api/notify/telegram/link')
const code = /start=([0-9a-f]+)$/.exec(link.b.url ?? '')?.[1]
ok(!!code && /t\.me\/FakeBot/.test(link.b.url), '生成绑定链接')
fake.updates.push({ update_id: fake.nextId++, message: { text: `/start ${code}`, chat: { id: 4242, type: 'private' } } })
ok(!!(await until(async () => (await call('GET', '/api/notify/prefs')).b.tg.linked)), '点 Start 后绑定成功')
fake.updates.push({ update_id: fake.nextId++, message: { text: `/start ${code}`, chat: { id: 5555, type: 'private' } } })
ok(!!(await until(() => fake.sent.find((m) => m.chat_id === 5555 && /失效/.test(m.text)))), '同一个链接不能用第二次')
if (!fake.sent.find((m) => m.chat_id === 5555)) console.log('已发', JSON.stringify(fake.sent), '待取', fake.updates.length)

// ---------- 强平预警：站内和 Telegram ----------
await until(async () => { await simPrice(px0 * 1.0); return true }, 500)
const view = (await call('GET', `/api/trade/${A}`)).b
const pos = view.positions.find((x) => x.instrument === 'BTC-PERP')
const liq = Number(pos.liq_px)
ok(liq > 0, `持仓有强平价 ${liq}`)
await simPrice(liq * 1.03)
const warn = await until(async () => (await items()).find((x) => x.kind === 'liq_warn' && x.params.done !== '1'))
ok(!!warn && Number(warn.params.dist) < 5, `强平预警（距离 ${warn?.params.dist}%）`)
ok(!!(await until(() => fake.sent.find((m) => m.chat_id === 4242 && /强平预警/.test(m.text)))), 'Telegram 收到了强平预警')
await p.waitForTimeout(3000)
ok((await items()).filter((x) => x.kind === 'liq_warn').length === 1, '两秒一轮不重复响')
await p.waitForSelector('[data-bell-n]', { timeout: 11000 })
await p.locator('[data-bell]').click()
await p.waitForSelector('.nt-row.safety', { timeout: 5000 })
const listText = await p.locator('[data-bell-pop]').innerText()
ok(/离强平价/.test(listText) && /涨到/.test(listText) && /买入/.test(listText), '列表里能看到这几条')
ok(await p.locator('.nt-row.safety').count() >= 1, '安全类有标记')
await p.screenshot({ path: `${OUT}/铃铛-中文.png` })
await p.keyboard.press('Escape')
await p.waitForTimeout(400)
ok((await call('GET', '/api/notify')).b.unread === 0, '打开后算读过')
ok(await p.locator('[data-bell-n]').count() === 0, '铃铛上的数字没了')

// ---------- 真的被强平 ----------
await simPrice(liq * 0.99)
const done = await until(async () => (await items()).find((x) => x.kind === 'liq_warn' && x.params.done === '1'), 15000)
ok(!!done && Number(done.params.loss) > 0 && !done.params.px, `全仓强平通知带亏损、不带价格（${done?.params.loss}）`)
await simPrice(px0)

// ---------- AI 点评 ----------
await call('PUT', `/api/journal/a/${A}/trades/nope/note`, { tags: [], note: '', emotion: '', playbook: 0 })
const trades = (await until(async () => { const r = (await call('GET', `/api/journal/a/${A}/trades`)).b.trades; return r?.some((x) => x.close_time) ? r : null })) ?? []
const closed = trades.find((x) => x.close_time)
ok(!!closed, '有一笔平掉的交易')
const secret = '只有我自己知道的笔记' + rnd()
await call('PUT', `/api/journal/a/${A}/trades/${closed.id}/note`, { tags: ['x'], note: secret, emotion: '', playbook: 0 })
await p.goto(`${BASE}/?page=review`)
await p.waitForSelector('.rv-table', { timeout: 15000 })
await p.locator('.rv-table tbody tr').first().click()
await p.waitForSelector('[data-ai-go]', { timeout: 8000 })
ok(/不构成投资建议/.test(await p.locator('[data-ai]').innerText()), '写明 AI 生成、不构成投资建议')
ok(/第三方 AI 服务/.test(await p.locator('[data-ai]').innerText()), '写明发给第三方')
await p.locator('[data-ai-go]').click()
await p.waitForSelector('[data-ai-text]', { timeout: 10000 })
const aiText = await p.locator('[data-ai-text]').innerText()
ok(/做对了/.test(aiText) && /先改这个/.test(aiText) && /下次的规则/.test(aiText), '三行点评显示出来')
await p.locator('[data-ai]').scrollIntoViewIfNeeded()
await p.locator('.rv-detail').screenshot({ path: `${OUT}/AI点评-中文.png` })
ok(fake.ai.length === 1 && fake.ai[0].auth === 'Bearer test-deepseek', '调用了一次，带 key')
const sentText = JSON.stringify(fake.ai[0].body.messages)
ok(!sentText.includes(secret), '笔记没有发出去')
ok(!sentText.includes(email) && !sentText.includes(closed.id), '邮箱和交易编号没有发出去')
ok(/coin.{1,6}BTC/.test(sentText) && /net_usdt/.test(sentText), '发的是这笔交易的数字')
ok(!/\b(buy|sell)\b.*\bshould\b/.test(sentText), '提示词没有让它给交易建议')
await p.reload()
await p.waitForSelector('.rv-table')
await p.locator('.rv-table tbody tr').first().click()
await p.waitForSelector('[data-ai-text]', { timeout: 8000 })
ok(fake.ai.length === 1, '再打开直接读缓存，不花钱')
// 英文
await p.locator('.bar-lang button, .bar-lang [role=radio]').filter({ hasText: 'EN' }).first().click()
await p.waitForSelector('[data-ai-go]', { timeout: 8000 })
await p.locator('[data-ai-go]').click()
await p.waitForSelector('[data-ai-text]', { timeout: 10000 })
const aiEn = await p.locator('[data-ai]').innerText()
ok(/Did well/.test(aiEn) && /Rule for next time/.test(aiEn) && noZh(aiEn), `英文点评，没有中文（${noZh(aiEn)}）`)
ok(fake.ai.length === 2 && /Did well/.test(fake.ai[1].body.messages[0].content), '英文用英文提示词')
await p.locator('[data-ai]').scrollIntoViewIfNeeded()
await p.locator('.rv-detail').screenshot({ path: `${OUT}/AI点评-英文.png` })
await p.locator('[data-bell]').click()
await p.waitForTimeout(200)
const listEn = await p.locator('[data-bell-pop]').innerText()
ok(!/ at 0/.test(listEn) && /cross-margin liquidation/.test(listEn), '全仓强平不写价格 0')
ok(noZh(listEn) && /liquidat/i.test(listEn), '通知列表切英文没有中文')
await p.screenshot({ path: `${OUT}/铃铛-英文.png` })
await p.keyboard.press('Escape')
await p.goto(`${BASE}/?page=account&sec=notify`)
await p.waitForSelector('[data-notify] tbody tr')
const setEn = await p.locator('[data-notify-sec]').innerText()
ok(noZh(setEn), '通知设置切英文没有中文')
await p.locator('[data-notify-sec]').screenshot({ path: `${OUT}/设置-英文.png` })
await p.locator('.bar-lang button, .bar-lang [role=radio]').filter({ hasText: '中' }).first().click()
await p.waitForTimeout(300)

// 被内容检查拦下、服务出错：都不存，不扣次数
// 上面那个账户已经被强平，换一个账户
await call('POST', '/api/paper-accounts', { name: 'B' })
const B = (await call('GET', '/api/paper-accounts')).b.accounts.find((x) => x.id !== A).id
const second = await call('POST', `/api/trade/${B}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'market', qty: '0.01' })
await p.waitForTimeout(600)
if (second.s !== 200) console.log('第二笔被拒', second.s, JSON.stringify(second.b))
const closeIt = await call('POST', `/api/trade/${B}/close`, { instrument: 'BTC-PERP' })
if (closeIt.s !== 200) console.log('平仓被拒', closeIt.s, JSON.stringify(closeIt.b))
const t2 = await until(async () => { const r = (await call('GET', `/api/journal/a/${B}/trades`)).b.trades; return r?.find((x) => x.close_time) })
ok(second.s === 200 && !!t2, '第二笔平掉的交易')
if (t2) {
  const path = `/api/journal/a/${B}/trades/${t2.id}/review`
  const left0 = (await call('GET', path)).b.left
  fake.mode = 'bad'
  ok((await call('POST', path)).b.code === 'AI_FILTERED', '模型给交易建议：被拦下')
  fake.mode = 'fail'
  ok((await call('POST', path)).b.code === 'AI_FAILED', '服务出错：说连不上')
  fake.mode = 'ok'
  const after = (await call('GET', path)).b
  ok(!after.review && after.left === left0, '都没存，也没扣次数')
}
const open = await call('POST', `/api/trade/${B}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'market', qty: '0.01' })
await p.waitForTimeout(600)
const openT = (await call('GET', `/api/journal/a/${B}/trades`)).b.trades.find((x) => !x.close_time)
ok(open.s === 200 && !!openT && (await call('POST', `/api/journal/a/${B}/trades/${openT.id}/review`)).b.code === 'TRADE_OPEN', '没平的交易不点评')

// ---------- 没有套餐和试用（D90）----------
ok((await call('GET', '/api/me/plan')).s === 404, '套餐接口没有了')
await p.goto(`${BASE}/?page=account`)
await p.waitForSelector('.ac-title', { timeout: 8000 })
ok(await p.locator('[data-plan]').count() === 0 && !/套餐|试用/.test(await p.locator('body').innerText()), '账户页没有套餐和试用')
const pf = (await call('GET', '/api/notify/prefs')).b.kinds
ok(pf.every((x) => x.tg_ok) && !pf.some((x) => x.kind === 'plan_due'), '所有事件都能发 Telegram，没有订阅到期这一类')

// ---------- 解绑和删账号 ----------
fake.updates.push({ update_id: fake.nextId++, message: { text: '/stop', chat: { id: 4242, type: 'private' } } })
ok(!!(await until(async () => !(await call('GET', '/api/notify/prefs')).b.tg.linked)), '/stop 解绑')

ok(errs.length === 0, `页面没有报错 ${errs.join(' | ').slice(0, 300)}`)
console.log(`\n通过 ${good}，失败 ${bad}`)
await b.close()
srv.close()
process.exit(bad ? 1 : 0)
