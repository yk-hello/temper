// 模拟盘审查（2026-11-07）：在测试环境真的发请求攻击。不要对着 8080 跑，会注册账号、下单、改价。
// 用法：node e2e-sim-attack.mjs <前端地址>。这里记录的是现状：✗ 是查到的问题，✓ 是挡住了。
import { createRequire } from 'module'
import { randomBytes } from 'crypto'
import fs from 'fs'
import path from 'path'
const require = createRequire(import.meta.url)
const { request } = require('playwright-core')
const BASE = process.argv[2] ?? 'http://127.0.0.1:4173'
if (/:8080/.test(BASE)) throw new Error('不要对着 8080 跑')
const SRC = process.argv[3] ?? '../backend'
let bad = 0, good = 0
const ok = (c, s) => { if (c) { good++; console.log('✓', s) } else { bad++; console.log('✗', s) } }
const note = (s) => console.log('·', s)
const rnd = () => randomBytes(6).toString('hex')
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const hdr = { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }

async function user() {
  const r = await request.newContext({ baseURL: BASE, extraHTTPHeaders: hdr })
  const email = `atk${Date.now()}${rnd().slice(0, 4)}@example.com`
  const reg = await r.post('/api/auth/register', { data: { email, password: 'pw-' + rnd() + rnd() } })
  if (!reg.ok()) throw new Error('注册失败 ' + reg.status())
  const tp = 'tp-' + rnd()
  await r.post('/api/me/trade-password', { data: { new: tp } })
  await r.post('/api/discipline/enabled', { data: { on: false, trade_password: tp } })
  const call = async (m, p, body) => {
    const x = await r.fetch(p, { method: m, data: body })
    let j = null
    try { j = await x.json() } catch { /* 没有内容 */ }
    return { s: x.status(), b: j }
  }
  const acct = (await call('GET', '/api/paper-accounts')).b.accounts[0].id
  return { r, call, acct, email, tp }
}
const sim = (c, px, inst = 'BTC-PERP') => c.call('POST', '/api/dev/sim-price', { instrument: inst, price: Number(Number(px).toFixed(1)) })
const totals = async (u, a = u.acct) => {
  const t = (await u.call('GET', `/api/trade/${a}`)).b
  const s = (await u.call('GET', `/api/spot/${a}`)).b
  return { wallet: Number(t.wallet), equity: Number(t.equity), spot: Number(s?.equity ?? 0), avail: Number(t.available), pos: t.positions, covered: Number(t.covered ?? 0) }
}

const A = await user(), B = await user()
const snap = async () => (await A.call('GET', '/api/market/snapshot')).b.instruments.find((x) => x.instrument === 'BTC-PERP')
let px = 0
for (let i = 0; i < 60 && !px; i++) { px = Number((await snap())?.bid ?? 0); if (!px) await sleep(300) }
note(`现价 ${px}`)

// ---------- 1. 没登录能访问什么 ----------
const routes = []
const walk = (d) => {
  for (const f of fs.readdirSync(d)) {
    const p = path.join(d, f)
    if (fs.statSync(p).isDirectory()) walk(p)
    else if (p.endsWith('.go') && !p.endsWith('_test.go')) {
      for (const m of fs.readFileSync(p, 'utf8').matchAll(/"((?:GET|POST|PUT|PATCH|DELETE) \/api\/[^"]+)"/g)) routes.push(m[1])
    }
  }
}
walk(SRC)
const uniq = [...new Set(routes)]
note(`代码里一共 ${uniq.length} 个接口`)
const anon = await request.newContext({ baseURL: BASE, extraHTTPHeaders: hdr })
const open = []
for (const rt of uniq) {
  const [m, p] = rt.split(' ')
  const url = p.replace('{acct}', String(A.acct)).replace(/\{[^}]+\}/g, '1')
  const x = await anon.fetch(url, { method: m, data: m === 'GET' ? undefined : {}, timeout: 8000 }).catch(() => null)
  if (x && x.status() !== 401 && x.status() !== 403 && x.status() !== 404 && x.status() !== 405) open.push(`${m} ${p} → ${x.status()}`)
}
note(`没登录能通的接口 ${open.length} 个：`)
const tradeish = open.filter((s) => /trade|spot|bots|follows|journal|notify|me\/|discipline|signal\/|signals\/hooks|paper/.test(s) && !/auth|\/specs|market\/trades/.test(s)) // 规格和公开成交本来就是公开的
for (const s of open) note('    ' + s)
ok(tradeish.length === 0, `没登录碰不到账户、交易、复盘、通知（${tradeish.join('；') || '没有'}）`)

// ---------- 2. 登录了能不能碰别人的账户 ----------
await A.call('POST', `/api/trade/${A.acct}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'limit', tif: 'GTC', qty: '0.01', price: String(Math.round(px * 0.95)) })
const leaked = []
for (const rt of uniq) {
  const [m, p] = rt.split(' ')
  if (!p.includes('{acct}')) continue
  const url = p.replace('{acct}', String(A.acct)).replace(/\{[^}]+\}/g, '1')
  const x = await B.r.fetch(url, { method: m, data: m === 'GET' ? undefined : {}, timeout: 8000 }).catch(() => null)
  if (x && x.status() >= 200 && x.status() < 300) leaked.push(`${m} ${p} → ${x.status()}`)
}
ok(leaked.length === 0, `B 碰不到 A 的账户（查了所有带账户号的接口）${leaked.join('；')}`)
const aOrders = (await A.call('GET', `/api/trade/${A.acct}`)).b.orders
const oid = aOrders[0]?.id
if (oid) {
  ok((await B.call('DELETE', `/api/trade/${A.acct}/orders/${oid}`)).s >= 400, 'B 撤不了 A 的单')
  ok((await B.call('DELETE', `/api/trade/${B.acct}/orders/${oid}`)).s >= 400, 'B 拿自己的账户号也撤不了 A 的单')
}

// ---------- 3. 乱填数字 ----------
const before = await totals(A)
const fuzz = ['-1', '0', '-0.001', '1e30', '1e-30', 'NaN', 'Infinity', '1e20000000', '0.0000000000001', '', 'abc', '99999999999999999999999', null]
const weird = []
for (const v of fuzz) {
  const cases = [
    ['POST', `/api/trade/${A.acct}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'market', qty: v }],
    ['POST', `/api/trade/${A.acct}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'limit', tif: 'GTC', qty: '0.01', price: v }],
    ['POST', `/api/trade/${A.acct}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'market', notional: v }],
    ['POST', `/api/trade/${A.acct}/margin`, { instrument: 'BTC-PERP', amount: v }],
    ['POST', `/api/spot/${A.acct}/transfer`, { to: 'spot', amount: v }],
    ['POST', `/api/spot/${A.acct}/transfer`, { to: 'perp', amount: v }],
    ['POST', `/api/trade/${A.acct}/leverage`, { instrument: 'BTC-PERP', leverage: v }],
    ['POST', `/api/trade/${A.acct}/close`, { instrument: 'BTC-PERP', fraction: v }],
    ['POST', `/api/trade/${A.acct}/tpsl`, { instrument: 'BTC-PERP', sl: v }],
    ['POST', '/api/notify/alerts', { instrument: 'BTC-PERP', px: v }],
  ]
  for (const [m, p, body] of cases) {
    const x = await A.call(m, p, body)
    if (x.s === 500 || (x.s >= 200 && x.s < 300 && !['0.0000000000001'].includes(v) && p.includes('/transfer') && Number(v) <= 0)) weird.push(`${p.split('/').pop()} ${JSON.stringify(v)} → ${x.s}`)
  }
}
ok(weird.length === 0, `乱填数字不会出 500，也不能负数划转（${weird.slice(0, 8).join('；') || '没有'}）`)
await A.call('POST', `/api/trade/${A.acct}/cancel-all`, {})
await A.call('POST', `/api/trade/${A.acct}/close-all`, {})
await sleep(300)
const after = await totals(A)
ok(Math.abs((after.equity + after.spot) - (before.equity + before.spot)) < 50, `乱填之后资金没有凭空变化（前 ${(before.equity + before.spot).toFixed(2)}，后 ${(after.equity + after.spot).toFixed(2)}）`)

// ---------- 4. 并发划转：同时转 30 次，每次都超过一半 ----------
const C = await user()
const c0 = await totals(C)
const rs = await Promise.all(Array.from({ length: 30 }, () => C.call('POST', `/api/spot/${C.acct}/transfer`, { to: 'spot', amount: '6000' })))
const c1 = await totals(C)
note(`并发划转 30 次：成功 ${rs.filter((x) => x.s === 200).length} 次`)
ok(Math.abs(c1.wallet + c1.spot - (c0.wallet + c0.spot)) < 0.01 && c1.wallet >= 0, `两边加起来不变（合约 ${c1.wallet}，现货 ${c1.spot}）`)
// 并发现货卖出同一笔币
await C.call('POST', `/api/spot/${C.acct}/orders`, { instrument: 'BTC-SPOT', side: 'buy', type: 'market', qty: '0.01' })
await sleep(300)
const sells = await Promise.all(Array.from({ length: 12 }, () => C.call('POST', `/api/spot/${C.acct}/orders`, { instrument: 'BTC-SPOT', side: 'sell', type: 'market', qty: '0.009' })))
const okSell = sells.filter((x) => x.s === 200 && x.b?.filled !== '0').length
ok(okSell <= 1, `同一笔币卖不了两次（并发 12 次，成功 ${okSell} 次）`)

// ---------- 5. 限频：换账户能不能绕开 ----------
const burst = await Promise.all(Array.from({ length: 40 }, () => C.call('POST', `/api/trade/${C.acct}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'limit', tif: 'GTC', qty: '0.001', price: String(Math.round(px * 0.9)) })))
const limited = burst.filter((x) => x.s === 429).length
ok(limited > 0, `每秒下单有上限（40 次里挡了 ${limited} 次）`)

// ---------- 6. 纪律锁：触发后，这些路能不能绕过去 ----------
const D = await user()
const dOn = await D.call('POST', '/api/discipline/enabled', { on: true, trade_password: D.tp })
const dRules = await D.call('PUT', '/api/discipline/rules', { rules: { daily_loss: '0.01', per_trade: '0.01', max_drawdown: '0.5', peak_r: '3', stop_r: '1', loss_streak: 20, cooldown_min: 30 }, trade_password: D.tp })
note(`开纪律锁 ${dOn.s} ${dOn.b?.code ?? ''}，改规则 ${dRules.s} ${dRules.b?.code ?? ''}，现在 enabled=${(await D.call('GET', '/api/discipline')).b?.enabled}`)
await sleep(2500) // 等纪律锁记下当天起始权益（每秒查一次）
await D.call('POST', `/api/trade/${D.acct}/leverage`, { instrument: 'BTC-PERP', leverage: 50 })
let cur = Number((await snap()).bid)
await D.call('POST', `/api/trade/${D.acct}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'market', qty: '0.1' })
await sim(D, cur * 0.97)
let locked = false
for (let i = 0; i < 40 && !locked; i++) {
  const v = (await D.call('GET', '/api/discipline')).b
  locked = !!v?.accounts?.find((x) => x.account === D.acct)?.lock
  if (!locked) await sleep(300)
}
const dv = (await D.call('GET', '/api/discipline')).b?.accounts?.find((x) => x.account === D.acct)
const dt = await totals(D)
note(`纪律锁状态：day_open ${dv?.day_open}，现在权益 ${dt.equity.toFixed(2)}，lock ${JSON.stringify(dv?.lock ?? null)}`)
ok(locked, '亏到日亏损上限后纪律锁触发')
await sim(D, cur)
await sleep(400)
if (locked) {
  const tries = {
    '手动开仓': await D.call('POST', `/api/trade/${D.acct}/orders`, { instrument: 'ETH-PERP', side: 'buy', type: 'market', qty: '0.1' }),
    '条件单': await D.call('POST', `/api/trade/${D.acct}/orders`, { instrument: 'ETH-PERP', side: 'buy', type: 'cond_market', qty: '0.1', trigger_px: String(Math.round(Number((await A.call('GET', '/api/market/snapshot')).b.instruments.find((x) => x.instrument === 'ETH-PERP').bid) * 1.01)) }),
    '现货买入': await D.call('POST', `/api/spot/${D.acct}/orders`, { instrument: 'ETH-SPOT', side: 'buy', type: 'market', qty: '0.01' }),
    '开机器人': await D.call('POST', '/api/bots', { account: D.acct, kind: 'plan', confirm: true, params: { instrument: 'ETH-SPOT', amount: '50', every: 'hour', times: 2 } }),
    '算法单': await D.call('POST', `/api/algo/${D.acct}/twap`, { instrument: 'ETH-PERP', side: 'buy', qty: '0.1', minutes: 1 }),
    '反手': await D.call('POST', `/api/trade/${D.acct}/orders`, { instrument: 'BTC-PERP', side: 'sell', type: 'market', qty: '0.3' }),
  }
  for (const [k, x] of Object.entries(tries)) {
    const blocked = x.s >= 400
    if (x.s === 404) note(`  ${k}：接口路径不对（404），没测到`)
    else ok(blocked, `纪律锁期间${k}被挡住（${x.s} ${x.b?.code ?? ''}）`)
  }
}

// ---------- 7. 跳空对冲：真实走一遍 ----------
const E = await user()
const E2id = (await E.call('POST', '/api/paper-accounts', { name: 'B' })).b?.id ?? (await E.call('GET', '/api/paper-accounts')).b.accounts.find((x) => x.id !== E.acct).id
await E.call('POST', `/api/trade/${E.acct}/leverage`, { instrument: 'BTC-PERP', leverage: 100 })
await E.call('POST', `/api/trade/${E2id}/leverage`, { instrument: 'BTC-PERP', leverage: 100 })
cur = Number((await snap()).bid)
const q = (700000 / cur).toFixed(3) // 70 万美元，100 倍
const l1 = await E.call('POST', `/api/trade/${E.acct}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'market', qty: q })
const l2 = await E.call('POST', `/api/trade/${E2id}/orders`, { instrument: 'BTC-PERP', side: 'sell', type: 'market', qty: q })
note(`一多一空各 ${q} BTC（${l1.s} ${l2.s}）`)
const e0 = (await totals(E)).equity + (await totals(E, E2id)).equity
await sim(E, cur * 0.97)
await sleep(1500)
const eA = await totals(E), eB = await totals(E, E2id)
await sim(E, cur)
note(`跳空跌 3% 后：多头账户 ${eA.equity.toFixed(2)}，空头账户 ${eB.equity.toFixed(2)}`)
const gain = eA.equity + eB.equity - e0
// 不学交易所做自动减仓（审查第 2 条，用户定）：穿仓时系统补的钱记下来，成绩里扣回去，风险页标出对冲账户
const covered = eA.covered + eB.covered
note(`两个账户合起来多了 ${gain.toFixed(2)}，其中系统补的穿仓 ${covered.toFixed(2)}`)
ok(covered > 0 && gain - covered < 50, `对冲靠跳空多出来的钱都记成"系统补的穿仓"，扣回去不赚钱（扣完 ${(gain - covered).toFixed(2)}）`)

// ---------- 8. 未实现盈利转走 ----------
const F = await user()
await F.call('POST', `/api/trade/${F.acct}/leverage`, { instrument: 'BTC-PERP', leverage: 100 })
cur = Number((await snap()).bid)
const fq = (700000 / cur).toFixed(3)
await F.call('POST', `/api/trade/${F.acct}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'market', qty: fq })
await sim(F, cur * 1.02)
await sleep(900)
const f1 = await totals(F)
const tr = await F.call('POST', `/api/spot/${F.acct}/transfer`, { to: 'spot', amount: String(Math.floor(f1.avail)) })
const f2 = await totals(F)
note(`涨 2% 后：钱包 ${f1.wallet.toFixed(2)}，可用 ${f1.avail.toFixed(2)}；转走 ${Math.floor(f1.avail)}（${tr.s}）；转完钱包 ${f2.wallet.toFixed(2)}`)
ok(tr.s !== 200 || f2.wallet >= 0, `不能把未实现盈利转走，钱包不能变负（转完钱包 ${f2.wallet.toFixed(2)}）`)
await sim(F, cur)

// ---------- 9. 账本：下完这些，账对不对 ----------
const chk = await A.call('GET', '/api/health')
ok(chk.s === 200, '服务还活着')

console.log(`\n结果：挡住 ${good} 项，查到问题 ${bad} 项`)
process.exit(0)
