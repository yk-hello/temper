// 和币安一样的接口（18 第 5.3 条）：界面上建一把模拟密钥，然后像外面的币安程序一样签名、下单、收推送。
// 会注册账号、下单：只能对着测试环境跑（不要对着 8080）。
// 用法：node tools/e2e-binapi.mjs <截图目录> [http://127.0.0.1:4173]
import { chromium } from 'playwright'
import { createHmac } from 'node:crypto'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const rand = () => Math.random().toString(36).slice(2)
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
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
await call('POST', '/api/auth/register', { email: `bk${Date.now()}${rand().slice(0, 4)}@example.com`, password: `bk-${rand()}-pass` })
const TP = 'tp-' + rand()
await call('POST', '/api/me/trade-password', { new: TP })
await call('POST', '/api/discipline/enabled', { on: false, trade_password: TP })
const acct = (await call('GET', '/api/paper-accounts')).b.accounts[0].id

// 界面上建密钥
await p.goto(BASE + '/?page=account')
const sec = p.locator('.ac-sec', { has: p.locator('h2', { hasText: '币安兼容接口' }) })
await sec.waitFor()
await sec.scrollIntoViewIfNeeded()
await sec.locator('[data-bnk-add]').click()
const form = p.locator('[data-bnk-form]')
await form.waitFor()
ok(await form.locator('input[type="password"]').count() === 0, '建模拟账户的接口密钥不要二级密码（D107：只能动模拟账户）')
await form.locator('[data-bnk-ok]').click()
const shown = p.locator('[data-bnk-shown]')
await shown.waitFor()
const KEY = await shown.locator('[data-bnk-secret="key"]').innerText()
const SECRET = await shown.locator('[data-bnk-secret="secret"]').innerText()
const REST = await shown.locator('[data-bnk-secret="rest"]').innerText()
ok(/^[0-9a-f]{64}$/.test(KEY) && /^[0-9a-f]{64}$/.test(SECRET), '建好后显示 API Key 和 Secret（和币安一样 64 位）')
ok(REST.endsWith('/bapi'), `接口地址是 /bapi（${REST}）`)
ok(/ccxt/.test(await shown.innerText()), '给了 ccxt 的改法')
await p.waitForTimeout(400)
await p.locator('.dlg:has([data-bnk-shown])').screenshot({ path: `${OUT}/bnk-01-key.png` })
await shown.locator('[data-bnk-done]').click()

// 像外面的程序一样：HMAC-SHA256 签查询串
const signed = async (method, path, params = {}, key = KEY, secret = SECRET) => {
  const q = new URLSearchParams({ ...params, timestamp: String(Date.now()), recvWindow: '5000' })
  q.set('signature', createHmac('sha256', secret).update(q.toString()).digest('hex'))
  const r = await fetch(`${REST}${path}?${q}`, { method, headers: { 'X-MBX-APIKEY': key } })
  return { s: r.status, b: await r.json().catch(() => null) }
}
let r = await signed('GET', '/fapi/v2/balance')
ok(r.s === 200 && r.b[0]?.asset === 'USDT' && Number(r.b[0].balance) === 10000, `余额：${r.b?.[0]?.balance} USDT`)
r = await signed('GET', '/fapi/v2/balance', {}, KEY, 'x'.repeat(64))
ok(r.s === 400 && r.b?.code === -1022, `签名错：币安的错误码 -1022（${r.b?.code}）`)
const info = await (await fetch(`${REST}/fapi/v1/exchangeInfo`)).json()
const btc = info.symbols.find((x) => x.symbol === 'BTCUSDT')
ok(!!btc && btc.filters.some((f) => f.filterType === 'MIN_NOTIONAL'), '品种规则（不要密钥）')

// 推送：申请 listenKey、连上
const lk = await (await fetch(`${REST}/fapi/v1/listenKey`, { method: 'POST', headers: { 'X-MBX-APIKEY': KEY } })).json()
ok(/^[0-9a-f]{64}$/.test(lk.listenKey ?? ''), '拿到 listenKey')
const events = []
const ws = new WebSocket(`${REST.replace(/^http/, 'ws')}/private/ws/${lk.listenKey}`)
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; setTimeout(rej, 8000) }).catch(() => {})
ws.onmessage = (m) => events.push(JSON.parse(m.data))
ok(ws.readyState === 1, '推送连上了（/bapi/private/ws/<listenKey>）')

// 下单：市价买 0.01 BTC
r = await signed('POST', '/fapi/v1/order', { symbol: 'BTCUSDT', side: 'BUY', type: 'MARKET', quantity: '0.01', newClientOrderId: 'e2e-1' })
ok(r.s === 200 && r.b.status === 'FILLED' && r.b.clientOrderId === 'e2e-1' && Number(r.b.executedQty) === 0.01, `市价单成交（${r.b?.status} ${r.b?.avgPrice}）`)
r = await signed('GET', '/fapi/v3/positionRisk')
ok(r.s === 200 && Number(r.b[0]?.positionAmt) === 0.01, `仓位 ${r.b?.[0]?.positionAmt}`)
// 平台界面上也看得到（同一个模拟账户）
const view = (await call('GET', `/api/trade/${acct}`)).b
ok(view.positions.some((x) => x.instrument === 'BTC-PERP' && Number(x.qty) === 0.01), '平台的交易页里也是这笔仓位（同一个模拟账户）')
let got = false
for (let i = 0; i < 30 && !got; i++) { await sleep(200); got = events.some((e) => e.e === 'ORDER_TRADE_UPDATE' && e.o?.x === 'TRADE' && e.o?.c === 'e2e-1') }
ok(got, `推送收到这笔成交（${events.map((e) => e.e).join(',')}）`)
ok(events.some((e) => e.e === 'ACCOUNT_UPDATE'), '推送收到账户变化')
r = await signed('POST', '/fapi/v1/order', { symbol: 'BTCUSDT', side: 'BUY', type: 'MARKET', quantity: '100' })
ok(r.s === 400 && r.b?.code === -2019, `保证金不够：-2019（${r.b?.code} ${r.b?.msg}）`)
r = await signed('GET', '/fapi/v1/notSimulated')
ok(r.s === 404 && /not supported/.test(r.b?.msg ?? ''), '没仿的接口说清楚')
ws.close()

// 吊销：马上不能用
await p.reload()
await sec.waitFor()
await sec.scrollIntoViewIfNeeded()
await p.screenshot({ path: `${OUT}/bnk-02-list.png` })
await sec.locator(`[data-bnk-row="${KEY}"] [data-bnk-revoke]`).click()
const ask = p.locator('[data-bnk-ask]')
await ask.waitFor()
ok(await ask.locator('input[type="password"]').count() === 0, '删掉模拟账户的密钥不要二级密码（D107）')
await ask.locator('[data-bnk-ask-ok]').click()
await p.waitForTimeout(800)
r = await signed('GET', '/fapi/v2/balance')
ok(r.s === 401 && r.b?.code === -2015, `吊销后：-2015（${r.b?.code}）`)
ok(errs.length === 0, `页面没有报错（${errs.join(' | ').slice(0, 200)}）`)
await browser.close()
console.log(`\n${pass} 通过，${fail} 失败`)
process.exit(fail ? 1 : 0)
