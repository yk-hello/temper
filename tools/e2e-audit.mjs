// 模块 2 逐项核对（接口层）：每种订单、每种拒单、杠杆和模式、止盈止损、平仓、列表分页、限流、越权、重启恢复。
// 用法：后端用模拟行情跑在 :8000，前端 vite preview 跑在 :4173（会把 /api 转给后端）
//   node tools/e2e-audit.mjs [--restart-cmd "<重启后端的命令>"]
// 不带 --restart-cmd 时跳过"重启后恢复"那一项（会标注跳过）。
import { request } from './node_modules/playwright/index.mjs'
import { execSync } from 'node:child_process'

const BASE = process.env.BASE ?? 'http://localhost:4173'
const restartCmd = (() => { const i = process.argv.indexOf('--restart-cmd'); return i > 0 ? process.argv[i + 1] : null })()
const hdr = { 'X-QS-Client': 'web', Origin: BASE }
let pass = 0, fail = 0
const ok = (c, name) => { if (c) pass++; else fail++; console.log(`${c ? '✓' : '✗'} ${name}`) }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
// 只有模拟行情才有的价格注入
const simPrice = (instrument, price) => fetch(BASE + '/api/dev/sim-price', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ instrument, price }) })

async function user() {
  const r = await request.newContext({ baseURL: BASE, extraHTTPHeaders: hdr })
  const reg = await r.post('/api/auth/register', { data: { email: `audit${Date.now()}${Math.random().toString(36).slice(2, 6)}@example.com`, password: 'password-123', lang: 'zh' } })
  if (!reg.ok()) throw new Error('注册失败 ' + reg.status())
  // 这个脚本测下单规则，不测纪律锁（默认开启，连亏 3 笔就冷静）：先关掉。纪律锁的检查在 e2e-m4
  await r.post('/api/me/trade-password', { data: { new: 'tp-audit-12345' } })
  await r.post('/api/discipline/enabled', { data: { on: false, trade_password: 'tp-audit-12345' } })
  const accts = await (await r.get('/api/paper-accounts')).json()
  return { r, acct: (accts.accounts ?? accts)[0].id }
}
// 每个用户每秒最多 10 次操作（2.17）：脚本自己也要守规矩，写操作之间隔 120 毫秒
const call = async (u, method, path, data) => {
  if (method !== 'GET' && !u.burst) { const w = 120 - (Date.now() - (u.last ?? 0)); if (w > 0) await sleep(w); u.last = Date.now() }
  const res = await u.r.fetch(`/api/trade/${u.acct}${path}`, { method, data })
  let body = null
  try { body = await res.json() } catch { /* 空 */ }
  return { status: res.status(), body }
}
const order = (u, o) => call(u, 'POST', '/orders', { instrument: 'BTC-PERP', ...o })
const view = async (u) => (await call(u, 'GET', '')).body
const pos = async (u) => (await view(u)).positions.find((p) => p.instrument === 'BTC-PERP')
const code = (x) => x.body?.code
const mid = async (u) => { const s = await (await u.r.get('/api/market/snapshot')).json(); const b = s.instruments.find((i) => i.instrument === 'BTC-PERP'); return { bid: Number(b.bid), ask: Number(b.ask), mark: Number(b.mark) } }
const tick = (v) => (Math.round(v * 10) / 10).toFixed(1)

const u = await user()
const specs = await (await u.r.get('/api/trade/specs')).json()
const btc = (specs.specs ?? specs).find?.((s) => s.instrument === 'BTC-PERP') ?? (specs.specs ?? specs)['BTC-PERP']
ok(btc && Number(btc.tick) === 0.1 && Number(btc.step) === 0.001 && Number(btc.min_notional) === 50 && Number(btc.price_up) === 1.03, `品种规则：BTC 价格最小变动 0.1、数量最小 0.001、最小名义 50 USDT、价格上下限 ±3%（D92，币安官方）（${JSON.stringify(btc).slice(0, 120)}）`)
const v0 = await view(u)
ok(Number(v0.equity) === 10000, '新账户权益 10,000 USDT')
ok(v0.settings?.['BTC-PERP']?.leverage === 10 || !v0.settings?.['BTC-PERP'], '默认杠杆 10 倍（D61）')

// ---------- 2.1 订单类型 ----------
let m = await mid(u)
let r = await order(u, { side: 'buy', type: 'market', qty: '0.010' })
ok(r.status === 200 && r.body?.status === 'filled', `市价买 0.01 成交（${r.body?.status}）`)
const fee = Number(r.body?.fee), avg = Number(r.body?.avg_px)
ok(Math.abs(fee - 0.01 * avg * 0.0005) < 1e-6, `市价单按吃单费 0.05%（${fee}）`)
r = await order(u, { side: 'buy', type: 'limit', tif: 'GTC', price: tick(m.bid * 0.98), qty: '0.010' })
const gtcId = r.body?.id
ok(r.body?.status === 'new', '限价 GTC 不穿价：挂着')
r = await order(u, { side: 'buy', type: 'limit', tif: 'GTX', price: tick(m.ask * 1.002), qty: '0.010' })
ok(r.body?.status === 'expired' && r.body?.reason === 'POST_ONLY_WOULD_TAKE', `只挂单（Post-only）会吃单时拒（${r.body?.reason}）`)
r = await order(u, { side: 'buy', type: 'limit', tif: 'GTX', price: tick(m.bid * 0.985), qty: '0.010' })
ok(r.body?.status === 'new', '只挂单不穿价：挂着')
m = await mid(u)
r = await order(u, { side: 'buy', type: 'limit', tif: 'FOK', price: tick(m.bid * 0.999), qty: '0.010' })
ok(r.body?.status === 'expired' && r.body?.reason === 'FOK_NOT_FILLED' && Number(r.body?.filled) === 0, `FOK 不能全部成交就全撤、一点不成交（${r.body?.reason}）`)
r = await order(u, { side: 'sell', type: 'limit', tif: 'IOC', price: tick(m.bid * 0.998), qty: '0.010' })
ok(['filled', 'expired'].includes(r.body?.status), `IOC 能成交的成交、剩余撤销（${r.body?.status}）`)

// ---------- 2.2 数量 ----------
m = await mid(u)
r = await order(u, { side: 'buy', type: 'market', notional: '200' })
ok(r.status === 200 && Math.abs(Number(r.body.qty) - Math.floor(200 / m.ask * 1000) / 1000) <= 0.001, `按 USDT 金额下单：200 USDT ≈ ${r.body?.qty} BTC`)
r = await call(u, 'POST', '/preview', { instrument: 'BTC-PERP', side: 'buy', type: 'market', margin_pct: '10' })
ok(r.status === 200 && Number(r.body.qty) > 0, `按可用保证金百分比：10% → ${r.body.qty} BTC`)
// BTC 一个最小数量（0.001）就超过 50 USDT，碰不到最小名义金额；用 ETH（最小 20 USDT，0.001 ETH 只有几美元）
r = await call(u, 'POST', '/orders', { instrument: 'ETH-PERP', side: 'buy', type: 'market', qty: '0.001' })
ok(code(r) === 'MIN_NOTIONAL' && r.body.params?.min === '20', `低于最小名义金额拒单并给出数值（${JSON.stringify(r.body.params)}）`)
r = await order(u, { side: 'buy', type: 'market', qty: '0.0001' })
ok(code(r) === 'QTY_TOO_SMALL' || code(r) === 'MIN_NOTIONAL', `低于最小数量拒单（${code(r)}）`)
r = await order(u, { side: 'buy', type: 'market', qty: '0.0123' })
ok(Number(r.body?.qty) === 0.012, `数量按最小单位向下取整（0.0123 → ${r.body?.qty}）`)
r = await order(u, { side: 'buy', type: 'market' })
ok(code(r) === 'QTY_REQUIRED', `没填数量拒单（${code(r)}）`)

// ---------- 价格校验 ----------
r = await order(u, { side: 'buy', type: 'limit', price: '60000.05', qty: '0.01' })
ok(code(r) === 'PRICE_TICK', `价格不是 0.1 的整数倍拒单（${code(r)}）`)
r = await order(u, { side: 'buy', type: 'limit', price: tick(m.mark * 1.2), qty: '0.01' })
ok(code(r) === 'PRICE_OUT_OF_RANGE' && r.body.params?.max, `价格超出标记价上下限（BTC ±3%）拒单并给出范围（${JSON.stringify(r.body.params)}）`)
r = await order(u, { side: 'buy', type: 'market', qty: '50' })
ok(code(r) === 'INSUFFICIENT_MARGIN' && r.body.params?.need, `保证金不够拒单并给出需要多少（${JSON.stringify(r.body.params)}）`)

// ---------- 只减仓 ----------
const u2 = await user()
r = await order(u2, { side: 'sell', type: 'market', qty: '0.01', reduce_only: true })
ok(code(r) === 'REDUCE_ONLY_NO_POSITION', `没有仓位时只减仓拒单（${code(r)}）`)
r = await order(u, { side: 'buy', type: 'market', qty: '0.01', reduce_only: true })
ok(code(r) === 'REDUCE_ONLY_SAME_SIDE', `只减仓不能加仓（${code(r)}）`)
let p = await pos(u)
r = await order(u, { side: 'sell', type: 'market', qty: '9', reduce_only: true })
const p2 = await pos(u)
ok(r.status === 200 && !p2, `只减仓数量超过持仓：只平掉持仓（原 ${p?.qty}，现在 ${p2?.qty ?? 0}）`)

// ---------- 2.3 杠杆和模式 ----------
r = await call(u, 'POST', '/leverage', { instrument: 'BTC-PERP', leverage: 200 })
ok(code(r) === 'LEVERAGE_RANGE', `杠杆超过上限拒绝（${code(r)} ${JSON.stringify(r.body?.params)}）`)
r = await call(u, 'POST', '/leverage', { instrument: 'BTC-PERP', leverage: 20 })
ok(r.status === 200 && (await view(u)).settings['BTC-PERP'].leverage === 20, '杠杆改成 20 倍')
r = await call(u, 'POST', '/margin-mode', { instrument: 'BTC-PERP', mode: 'isolated' })
ok(code(r) === 'MODE_HAS_ORDERS', `有挂单时不能切换全仓/逐仓（${code(r)}）`)
await call(u, 'POST', '/cancel-all', {})
r = await call(u, 'POST', '/margin-mode', { instrument: 'BTC-PERP', mode: 'isolated' })
ok(r.status === 200 && (await view(u)).settings['BTC-PERP'].mode === 'isolated', '没仓位没挂单时切到逐仓')
await order(u, { side: 'buy', type: 'market', qty: '0.02' })
p = await pos(u)
ok(p && Math.abs(Number(p.margin) - 0.02 * Number(p.entry) / 20) < 0.5, `逐仓保证金 = 名义 ÷ 杠杆（${p?.margin}）`)
r = await call(u, 'POST', '/margin-mode', { instrument: 'BTC-PERP', mode: 'cross' })
ok(code(r) === 'MODE_HAS_POSITION', `有仓位时不能切换模式（${code(r)}）`)
r = await call(u, 'POST', '/leverage', { instrument: 'BTC-PERP', leverage: 5 })
const p3 = await pos(u)
ok(r.status === 200 && Number(p3.margin) > Number(p.margin) * 3, `逐仓降杠杆从可用余额补保证金（${p.margin} → ${p3.margin}）`)

// ---------- 2.5 强平价（逐仓，对照币安公式） ----------
{
  const q = Number(p3.qty), ep = Number(p3.entry), wb = Number(p3.margin)
  // 第一档：维持保证金率 0.4%，速算额 0
  const lp = (wb - q * ep) / (q * 0.004 - q)
  ok(Math.abs(Number(p3.liq_px) - lp) < 1, `逐仓强平价和手算一致（接口 ${p3.liq_px}，手算 ${lp.toFixed(1)}）`)
}

// ---------- 2.10 止盈止损 ----------
m = await mid(u)
r = await call(u, 'POST', '/tpsl', { instrument: 'BTC-PERP', tp: tick(m.mark * 0.99), sl: '' })
ok(code(r) === 'WOULD_TRIGGER', `多单止盈价低于现价会立即触发：拒绝并说明（${code(r)}）`)
r = await call(u, 'POST', '/tpsl', { instrument: 'BTC-PERP', tp: tick(m.mark * 1.03), sl: tick(m.mark * 0.97) })
let v = await view(u)
ok(r.status === 200 && v.orders.filter((o) => o.close_position).length === 2, '持仓上设止盈止损：挂上 2 张条件单')
p = v.positions.find((x) => x.instrument === 'BTC-PERP')
ok(Number(p.tp) > 0 && Number(p.sl) > 0, `持仓行显示止盈 ${p.tp} / 止损 ${p.sl}`)
r = await call(u, 'POST', '/tpsl', { instrument: 'BTC-PERP', tp: '0' })
v = await view(u)
ok(v.orders.filter((o) => o.close_position).length === 1, '只取消止盈，止损保留')
// 设一个止损，再用价格注入把行情打穿它（D75：造场景，不等运气）
m = await mid(u)
r = await call(u, 'POST', '/tpsl', { instrument: 'BTC-PERP', sl: tick(m.mark * 0.998) })
await simPrice('BTC-PERP', m.mark * 0.99)
let fired = false
for (let i = 0; i < 40 && !fired; i++) { await sleep(500); fired = !(await pos(u)) }
v = await view(u)
ok(fired || code(r) === 'WOULD_TRIGGER', `止损按标记价触发后市价平仓（${fired ? '已触发' : code(r) ?? '20 秒内没触发'}）`)
if (fired) ok(v.notices.some((n) => n.kind === 'sl'), '止损触发有通知')
ok(v.orders.filter((o) => o.close_position).length === 0, '仓位平掉后止盈止损自动撤销')

// 随单止盈止损
await call(u, 'POST', '/margin-mode', { instrument: 'BTC-PERP', mode: 'cross' })
m = await mid(u)
r = await order(u, { side: 'buy', type: 'market', qty: '0.01', tp: tick(m.ask * 1.05), sl: tick(m.ask * 0.95) })
v = await view(u)
ok(v.orders.filter((o) => o.close_position && o.parent === r.body.id).length === 2, '下单时带止盈止损：成交后挂到仓位上')
r = await order(u, { side: 'buy', type: 'market', qty: '0.01', tp: tick(m.ask * 0.9) })
ok(code(r) === 'BAD_TPSL', `随单止盈方向错了拒单（${code(r)}）`)

// ---------- 2.8 改单、撤单 ----------
m = await mid(u)
// BTC 的价格上下限是标记价 ±3%（D92）：挂单和改价都留在这个范围里
r = await order(u, { side: 'buy', type: 'limit', price: tick(m.bid * 0.985), qty: '0.01' })
const lid = r.body.id
r = await call(u, 'PATCH', `/orders/${lid}`, { price: tick(m.bid * 0.98), qty: '0.02' })
ok(r.status === 200 && Number(r.body?.qty) === 0.02 && Number(r.body.price) === Number(tick(m.bid * 0.98)), `改价改量（${r.body?.price} × ${r.body?.qty}）`)
r = await call(u, 'PATCH', `/orders/${lid}`, { price: '1' })
ok(code(r) === 'PRICE_OUT_OF_RANGE', `改到离谱的价格拒绝（${code(r)}）`)
r = await call(u, 'DELETE', `/orders/${lid}`)
ok(r.body?.status === 'canceled' && r.body?.reason === 'USER', '撤单')
r = await call(u, 'DELETE', `/orders/${lid}`)
ok(r.status === 404, '撤已经撤掉的单：404')
for (let i = 0; i < 3; i++) await order(u, { side: 'buy', type: 'limit', price: tick(m.bid * (0.985 - i * 0.002)), qty: '0.01' })
r = await call(u, 'POST', '/cancel-all', {})
ok(r.body.canceled === 5, `全部撤单（含止盈止损）：撤了 ${r.body.canceled} 张`)
// 限价单被成交：挂一个马上会被市场成交价穿过的卖单
m = await mid(u)
await order(u, { side: 'buy', type: 'market', qty: '0.01' })
r = await order(u, { side: 'sell', type: 'limit', price: tick(m.ask + 0.1), qty: '0.01' })
const restId = r.body.id
let filled = r.body.status === 'filled'
if (!filled) await simPrice('BTC-PERP', m.ask * 1.002) // 把成交价推过挂单价
for (let i = 0; i < 60 && !filled; i++) { await sleep(500); filled = !(await view(u)).orders.some((o) => o.id === restId) }
ok(filled, `挂单被市场成交价穿过后成交（${filled ? '成交' : '30 秒内没成交，行情没走到'}）`)
const hist = (await call(u, 'GET', '/history?kind=fills&page=1&size=50')).body
const mk = hist.items.find((f) => f.order_id === restId)
// 下单那一刻行情可能已经走过这个价，那就是立即吃单成交（按吃单费是对的）；只有先挂上的才应该按挂单费
const rested = r.body.status === 'new'
ok(!mk || !rested || mk.maker, `先挂上再成交的按挂单费（${mk ? `挂上=${rested} maker=${mk.maker} fee=${mk.fee}` : '没成交，跳过'}）`)
ok(!mk || rested || !mk.maker, `一下单就穿价的按吃单费（${mk ? `挂上=${rested} maker=${mk.maker}` : '没成交，跳过'}）`)

// ---------- 2.9 平仓 ----------
await call(u, 'POST', '/close-all', {})
await order(u, { side: 'sell', type: 'market', qty: '0.04' })
r = await call(u, 'POST', '/close', { instrument: 'BTC-PERP', fraction: '0.25', type: 'market' })
p = await pos(u)
ok(r.status === 200 && Number(p.qty) === 0.03, `平 25%（剩 ${p?.qty}）`)
m = await mid(u)
r = await call(u, 'POST', '/close', { instrument: 'BTC-PERP', fraction: '1', type: 'limit', price: tick(m.bid * 0.99) })
ok(r.status === 200 && r.body?.reduce_only && r.body?.status === 'new', '限价平仓：挂一张只减仓的买单')
r = await call(u, 'POST', '/close-all', {})
ok(r.status === 200 && !(await pos(u)), '一键全平')
v = await view(u)
ok(v.orders.length === 0, '全平后只减仓的挂单自动撤销')
r = await call(u, 'POST', '/close', { instrument: 'BTC-PERP', fraction: '1', type: 'market' })
ok(code(r) === 'NO_POSITION', `没仓位时平仓说明原因（${code(r)}）`)

// ---------- 2.14 账户 ----------
v = await view(u)
const s = v
ok(Math.abs(Number(s.equity) - (Number(s.wallet) + Number(s.upnl ?? 0))) < 0.01, `权益 = 钱包余额 + 未实现盈亏（${s.equity}）`)
ok(Number(s.fees) > 0 || Number(s.wallet) < 10000, `手续费记账（钱包 ${s.wallet}）`)

// ---------- 2.13 列表分页 ----------
const h1 = (await call(u, 'GET', '/history?kind=orders&page=1&size=5')).body
const h2 = (await call(u, 'GET', '/history?kind=orders&page=2&size=5')).body
ok(h1.total > 10 && h1.items.length === 5 && h2.items[0]?.id !== h1.items[0]?.id, `历史委托分页（共 ${h1.total} 条）`)
ok(h1.items.every((o) => o.status !== 'new'), '历史委托里只有已结束的')
const fl = (await call(u, 'GET', '/history?kind=fills&page=1&size=500')).body
ok(fl.items.length > 5 && fl.items.every((f) => f.fee !== undefined), `成交明细带手续费（${fl.items.length} 条）`)
const fd = (await call(u, 'GET', '/history?kind=funding&page=1&size=20')).body
ok(Array.isArray(fd.items), `资金费记录可查（${fd.total} 条；8 小时结算一次，测试期间一般没有）`)

// ---------- 2.17 安全 ----------
r = await u2.r.fetch(`/api/trade/${u.acct}`)
ok(r.status() === 404, '看别人的账户：404')
r = await u2.r.fetch(`/api/trade/${u.acct}/orders`, { method: 'POST', data: { instrument: 'BTC-PERP', side: 'buy', type: 'market', qty: '0.01' } })
ok(r.status() === 404, '给别人的账户下单：404')
const anon = await request.newContext({ baseURL: BASE, extraHTTPHeaders: hdr })
r = await anon.fetch(`/api/trade/${u.acct}`)
ok(r.status() === 401, '没登录：401')
r = await u.r.fetch(`/api/trade/${u.acct}/orders`, { method: 'POST', headers: { 'X-QS-Client': '' }, data: { instrument: 'BTC-PERP', side: 'buy', type: 'market', qty: '0.01' } })
ok(r.status() === 403 || r.status() === 400, `缺少防跨站请求头：拒绝（${r.status()}）`)
m = await mid(u)
u.burst = true
const burst = await Promise.all(Array.from({ length: 14 }, (_, i) => order(u, { side: 'buy', type: 'limit', price: tick(m.bid * (0.9 - i * 0.001)), qty: '0.01' })))
ok(burst.some((x) => x.status === 429 && code(x) === 'RATE_LIMITED'), `每秒超过 10 单：限流（${burst.filter((x) => x.status === 429).length} 张被拒）`)

// ---------- 模块 2 补齐：条件单、触发价类型、逐仓调整保证金、最新成交、实时推送 ----------
{
  const u3 = await user()
  let m3 = await mid(u3)
  // 条件市价单：离现价很近的触发价，模拟行情一会儿就会碰到
  r = await order(u3, { side: 'buy', type: 'cond_market', trigger_px: tick(m3.mark * 1.0004), qty: '0.01' })
  ok(r.status === 200 && r.body.status === 'new' && r.body.trigger_dir === 'up' && r.body.trigger_by === 'mark', `条件市价单挂上：涨到触发价才下单（方向 ${r.body?.trigger_dir}）`)
  const condId = r.body.id
  let v3 = await view(u3)
  ok(Number(v3.order_margin) === 0, `条件单触发前不占保证金（挂单保证金 ${v3.order_margin}）`)
  // 改到离现价只差几个价位的地方（模拟行情很快会碰到）；方向按改的那一刻的价格重新判断
  m3 = await mid(u3)
  const nt = tick(m3.mark * 1.00005)
  r = await call(u3, 'PATCH', `/orders/${condId}`, { trigger_px: nt, qty: '0.02' })
  ok(r.status === 200 && Number(r.body.qty) === 0.02 && Number(r.body.trigger_px) === Number(nt), `改条件单的触发价和数量（${r.body?.trigger_px} × ${r.body?.qty}，方向 ${r.body?.trigger_dir}）`)
  // 再挂一张反方向的（跌到触发）：模拟行情往哪边走都有一张会碰到
  m3 = await mid(u3)
  const r2 = await order(u3, { side: 'buy', type: 'cond_market', trigger_px: tick(m3.mark * 0.99995), qty: '0.02' })
  const ids = [condId, r2.body.id]
  let firedId = null
  for (let i = 0; i < 80 && !firedId; i++) { await sleep(500); const left = (await view(u3)).orders.map((o) => o.id); firedId = ids.find((id) => !left.includes(id)) ?? null }
  const trig = !!firedId
  for (const id of ids) if (id !== firedId) await call(u3, 'DELETE', `/orders/${id}`)
  v3 = await view(u3)
  const h3 = (await call(u3, 'GET', '/history?kind=orders&page=1&size=20')).body.items
  const ch = h3.find((o) => o.id === firedId)
  ok(trig && ch?.status === 'triggered' && ch?.reason === 'TRIGGERED', `价格到了触发：条件单记为"已触发"（${ch?.status ?? '40 秒内两张都没触发'}，触发的是${firedId === condId ? '涨到' : '跌到'}那张）`)
  const child = h3.find((o) => o.parent === firedId)
  ok(!trig || (child && child.type === 'market' && child.source === 'cond'), `触发后生成一张市价单（${child?.type}/${child?.status}）`)
  ok(!trig || v3.notices.some((n) => n.kind === 'cond'), '触发有通知')
  // 拒单
  m3 = await mid(u3)
  r = await order(u3, { side: 'buy', type: 'cond_market', trigger_px: '60000.05', qty: '0.01' })
  ok(code(r) === 'TRIGGER_TICK', `触发价不在价格单位上：拒（${code(r)}）`)
  r = await order(u3, { side: 'buy', type: 'cond_market', trigger_px: tick(m3.mark * 1.01), qty: '0.01', trigger_by: 'oracle' })
  ok(code(r) === 'BAD_TRIGGER_BY', `触发价类型只能是标记价、最新价或指数价（${code(r)}）`)
  r = await order(u3, { side: 'sell', type: 'cond_limit', trigger_px: tick(m3.mark * 0.99), price: tick(m3.mark * 0.9), qty: '0.01' })
  ok(code(r) === 'PRICE_OUT_OF_RANGE', `条件限价单的限价离触发价太远：拒（${code(r)}）`)
  r = await order(u3, { side: 'sell', type: 'cond_limit', trigger_px: tick(m3.mark * 0.99), price: tick(m3.mark * 0.989), qty: '0.01', trigger_by: 'last' })
  ok(r.status === 200 && r.body.type === 'cond_limit' && r.body.trigger_by === 'last' && r.body.trigger_dir === 'down', `条件限价单按最新价、跌到触发（${r.body?.trigger_by}/${r.body?.trigger_dir}）`)
  // 止盈止损按最新价
  if (!(await pos(u3))) await order(u3, { side: 'buy', type: 'market', qty: '0.01' })
  m3 = await mid(u3)
  r = await call(u3, 'POST', '/tpsl', { instrument: 'BTC-PERP', tp: tick(m3.mark * 1.05), trigger_by: 'last' })
  v3 = await view(u3)
  ok(r.status === 200 && v3.orders.some((o) => o.type === 'take_profit_market' && o.trigger_by === 'last'), '持仓止盈可以按最新价触发')
  await call(u3, 'POST', '/cancel-all', {})
  await call(u3, 'POST', '/close-all', {})
  // 逐仓调整保证金
  await call(u3, 'POST', '/margin-mode', { instrument: 'BTC-PERP', mode: 'isolated' })
  r = await call(u3, 'POST', '/margin', { instrument: 'BTC-PERP', amount: '10' })
  ok(code(r) === 'NO_POSITION', `没仓位不能调保证金（${code(r)}）`)
  await order(u3, { side: 'buy', type: 'market', qty: '0.02' })
  const p0 = await pos(u3)
  r = await call(u3, 'POST', '/margin', { instrument: 'BTC-PERP', amount: '100', dry: true })
  ok(r.status === 200 && Number(r.body.liq_px) < Number(p0.liq_px) && Number((await pos(u3)).margin) === Number(p0.margin), `预览：追加 100 后强平价从 ${p0.liq_px} 降到 ${r.body?.liq_px}，预览不改仓位`)
  r = await call(u3, 'POST', '/margin', { instrument: 'BTC-PERP', amount: '100' })
  const p1 = await pos(u3)
  ok(r.status === 200 && Math.abs(Number(p1.margin) - Number(p0.margin) - 100) < 0.01 && Number(p1.liq_px) < Number(p0.liq_px), `追加 100：保证金 ${p0.margin} → ${p1.margin}，强平价 ${p0.liq_px} → ${p1.liq_px}`)
  r = await call(u3, 'POST', '/margin', { instrument: 'BTC-PERP', amount: '-100000' })
  ok(code(r) === 'MARGIN_REMOVE_TOO_MUCH' && r.body.params?.max, `减太多：拒并给出最多能减多少（${JSON.stringify(r.body?.params)}）`)
  r = await call(u3, 'POST', '/margin', { instrument: 'BTC-PERP', amount: '1000000' })
  ok(code(r) === 'MARGIN_ADD_TOO_MUCH', `追加超过可用余额：拒（${code(r)}）`)
  r = await call(u3, 'POST', '/margin', { instrument: 'BTC-PERP', amount: '-50' })
  ok(r.status === 200 && Math.abs(Number((await pos(u3)).margin) - Number(p1.margin) + 50) < 0.01, '减少 50')
  const v4 = await view(u3)
  ok(Math.abs(Number(v4.equity) - (Number(v4.wallet) + Number(v4.upnl))) < 0.01, '调整保证金不改变权益（钱只是在可用和逐仓之间挪）')
  // 最新成交（公开接口）
  const tr = await (await u3.r.get('/api/market/trades?instrument=BTC-PERP&limit=20')).json()
  ok(Array.isArray(tr) && tr.length > 0 && tr.length <= 20 && tr[0].t >= (tr[tr.length - 1]?.t ?? 0) && 'buy' in tr[0], `最新成交：新的在前（${tr.length} 笔）`)
  // 实时推送：连上就收到一帧完整账户视图
  const ctl = new AbortController()
  const res = await fetch(BASE + `/api/trade/${u3.acct}/stream`, { headers: { cookie: (await u3.r.storageState()).cookies.map((c) => `${c.name}=${c.value}`).join('; ') }, signal: ctl.signal })
  const reader = res.body.getReader()
  let buf = ''
  const t0 = Date.now()
  while (!buf.includes('\n\n') && Date.now() - t0 < 5000) { const { value, done } = await reader.read(); if (done) break; buf += new TextDecoder().decode(value) }
  ctl.abort()
  let frame = null
  try { frame = JSON.parse(buf.split('\n').find((l) => l.startsWith('data: ')).slice(6)) } catch { /* 下面会失败 */ }
  ok(res.headers.get('content-type')?.startsWith('text/event-stream') && frame?.id === u3.acct && Array.isArray(frame?.positions), `账户实时推送：连上就收到账户视图（${res.headers.get('content-type')}）`)
  const other = await fetch(BASE + `/api/trade/${u.acct}/stream`, { headers: { cookie: (await u3.r.storageState()).cookies.map((c) => `${c.name}=${c.value}`).join('; ') } })
  ok(other.status === 404, '订阅别人账户的推送：404')
}

// ---------- 2.12 重启恢复 ----------
u.burst = false
await sleep(1200)
await call(u, 'POST', '/cancel-all', {})
m = await mid(u)
await order(u, { side: 'buy', type: 'market', qty: '0.015' })
await order(u, { side: 'buy', type: 'limit', price: tick(m.bid * 0.975), qty: '0.01' }) // 价格上下限 ±3% 以内，但离现价够远：重启那几十秒里不会成交
await call(u, 'POST', '/tpsl', { instrument: 'BTC-PERP', sl: tick(m.mark * 0.9) })
const before = await view(u)
if (restartCmd) {
  await sleep(2500) // 等写库
  execSync(restartCmd, { stdio: 'inherit' })
  let after = null
  for (let i = 0; i < 60 && !after; i++) { await sleep(1000); try { const x = await call(u, 'GET', ''); if (x.status === 200) after = x.body } catch { /* 还没起来 */ } }
  // 只是默认值（10 倍、全仓）的品种不比：被拒的单碰过一下就会出现在列表里，重启后没有，实际设置一样
  const pick = (x) => JSON.stringify({ w: x.wallet, p: x.positions.map((q) => [q.instrument, q.qty, q.entry]), o: x.orders.map((o) => [o.id, o.status, o.price, o.qty, o.trigger_px, o.trigger_by, o.trigger_dir]).sort(), st: Object.fromEntries(Object.entries(x.settings ?? {}).filter(([, v]) => !(v.leverage === 10 && v.mode === 'cross'))), m: x.positions.map((q) => (q.mode === 'isolated' ? q.margin : '')) })
  ok(after && pick(after) === pick(before), `后端重启后：余额、持仓、挂单、止盈止损、杠杆设置完全一样${after && pick(after) !== pick(before) ? `（前 ${pick(before).slice(0, 300)} / 后 ${pick(after).slice(0, 300)}）` : ''}`)
} else {
  console.log('- 跳过重启恢复（没有给 --restart-cmd）')
}

console.log(`\n通过 ${pass}，失败 ${fail}`)
process.exit(fail ? 1 : 0)
