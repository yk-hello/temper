// TradingView 信号和引擎优先级（D48、D83）。只对着测试环境跑。
//   node tools/e2e-signal.mjs <截图目录> <网址>
import { chromium } from 'playwright'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const rnd = () => Math.random().toString(36).slice(2)
const c = await browser.newContext({ viewport: { width: 1536, height: 900 }, deviceScaleFactor: 1.25, locale: 'zh-CN', timezoneId: 'Asia/Taipei' })
await c.addInitScript(() => localStorage.setItem('qs.wizardSeen', '1'))
const p = await c.newPage()
const errs = []
p.on('pageerror', (e) => errs.push(String(e)))
const call = (method, path, body) => p.evaluate(async ({ method, path, body }) => { const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined }); return { s: r.status, b: await r.json().catch(() => null) } }, { method, path, body })
const hook = (tok, text) => p.evaluate(async ({ tok, text }) => { const r = await fetch(`/api/hook/tv/${tok}`, { method: 'POST', body: text }); return { s: r.status, b: await r.json().catch(() => null) } }, { tok, text })
const until = async (fn, ms = 8000) => { const t0 = Date.now(); while (Date.now() - t0 < ms) { const v = await fn(); if (v) return v; await new Promise((r) => setTimeout(r, 250)) } return null }
await p.goto(BASE + '/?page=login')
await call('POST', '/api/auth/register', { email: `sig${Date.now()}${rnd().slice(0, 4)}@example.com`, password: 'pw-' + rnd() + rnd() })
const tp = 'tp-' + rnd()
await call('POST', '/api/me/trade-password', { new: tp })
await call('POST', '/api/discipline/enabled', { on: false, trade_password: tp })
const A = (await call('GET', '/api/paper-accounts')).b.accounts[0].id

// 接口
let r = await call('POST', '/api/signal-hooks', { account: A, name: '趋势', budget: '1000' })
ok(r.s === 200 && r.b.token?.length > 20, '建了信号地址，给一次密钥')
const tok = r.b.token
ok(!JSON.stringify((await call('GET', '/api/signal-hooks')).b).includes(tok), '列表里不带密钥')
ok((await call('POST', '/api/signal-hooks', { account: 999999, name: 'x', budget: '0' })).s === 404, '别人的账户不能建')
ok((await hook('wrong-token', '做多 BTC 10%')).s === 404, '地址不对返回 404，不说原因')
r = await hook(tok, '做多 BTC 30%')
ok(r.s === 200 && r.b.status === 'ok', `一句话能下单（${r.b?.status} ${r.b?.code ?? ''}）`)
let v = await until(async () => { const x = (await call('GET', `/api/trade/${A}`)).b; return x.positions.length ? x : null })
const pos = v?.positions[0]
ok(pos?.instrument === 'BTC-PERP' && pos?.side === 'long', '开了 BTC 多单')
ok(Math.abs(Number(pos?.qty) * Number(pos?.entry) - 300) < 110, `金额是资金桶 1000 的 30%（${(Number(pos?.qty) * Number(pos?.entry)).toFixed(1)}）`)
r = await hook(tok, '做多 BTC 30%')
ok(r.b.status === 'duplicate', 'TradingView 重发，不重复下单')
r = await hook(tok, '随便写点什么')
ok(r.b.status === 'rejected' && r.b.code === 'SIG_BAD_ACTION', '看不懂的写明原因')
r = await hook(tok, '{"action":"close","symbol":"BINANCE:BTCUSDT.P"}')
ok(r.b.status === 'ok', `JSON 平仓（${r.b.code ?? ''}）`)
ok(await until(async () => (await call('GET', `/api/trade/${A}`)).b.positions.length === 0), '平掉了')
// 11-18：按 TradingView 服务器的样子发——不带 cookie、没有 Origin，User-Agent 是 Go-http-client，JSON 时 Content-Type 是 application/json
const tv = async (text) => {
  let json = false
  try { JSON.parse(text); json = true } catch {}
  const t0 = Date.now()
  const x = await fetch(`${BASE}/api/hook/tv/${tok}`, { method: 'POST', body: text, headers: { 'Content-Type': json ? 'application/json; charset=utf-8' : 'text/plain; charset=utf-8', 'User-Agent': 'Go-http-client/1.1' } })
  return { s: x.status, b: await x.json().catch(() => null), ms: Date.now() - t0 }
}
r = await tv('buy BTCUSDT.P 30%')
ok(r.s === 200 && r.b?.status === 'ok' && r.ms < 3000, `TradingView 服务器那样发也能下单，${r.ms} 毫秒回应（它等 3 秒）`)
v = await until(async () => { const x = (await call('GET', `/api/trade/${A}`)).b; return x.positions.length ? x : null })
ok(v?.positions[0]?.instrument === 'BTC-PERP' && v?.positions[0]?.side === 'long', 'BTCUSDT.P 上的 buy 是合约做多，不是买现货')
r = await tv('buy @ 0.01 filled on BTCUSDT.P. New strategy position is 0.01')
ok(r.b?.status === 'rejected' && r.b?.code === 'SIG_TV_DEFAULT', 'TradingView 策略警报的默认消息：写明该怎么改')
r = await tv('{"action":"flat","symbol":"BINANCE:BTCUSDT.P"}')
ok(r.b?.status === 'ok' && await until(async () => (await call('GET', `/api/trade/${A}`)).b.positions.length === 0), 'JSON 的 flat 平仓')
// 现货
await call('POST', `/api/spot/${A}/transfer`, { to: 'spot', amount: '500' })
r = await hook(tok, 'buy ETH 50%')
ok(r.b.status === 'ok', `现货买入（${r.b.code ?? ''}）`)
ok((await call('GET', `/api/spot/${A}`)).b.balances.some((b) => b.coin === 'ETH'), '买到 ETH')
// 纪律锁：信号也过风控
await call('POST', '/api/discipline/enabled', { on: true })
const hist = (await call('GET', `/api/journal/a/${A}/trades?origin=signal`)).b.trades
ok(hist.length >= 1 && hist.every((t) => t.origin === 'signal'), `信号的单单独能看（${hist.length} 笔）`)
ok((await call('GET', `/api/journal/a/${A}/trades`)).b.trades.every((t) => t.origin !== 'signal'), '默认只看手动：信号的单不混进来')
// 机器人更优先时，信号要被拒
const px = Number((await call('GET', '/api/market/snapshot')).b.instruments.find((x) => x.instrument === 'SOL-SPOT').bid)
await call('POST', `/api/spot/${A}/transfer`, { to: 'spot', amount: '1500' })
const bot = await call('POST', '/api/bots', { kind: 'spot_grid', account: A, confirm: true, params: { instrument: 'SOL-SPOT', lower: (px * 0.97).toFixed(2), upper: (px * 1.03).toFixed(2), grids: 4, invest: '400' } })
ok(bot.s === 200, `开了 SOL 网格（${bot.b?.code ?? ''}）`)
r = await hook(tok, 'buy SOL 5%')
ok(r.b.status === 'ok', '默认信号优先：放行')
ok(await until(async () => (await call('GET', '/api/bots')).b.bots.find((b) => b.id === bot.b.id).status === 'paused'), '机器人让路')
ok((await call('GET', '/api/bots')).b.bots.find((b) => b.id === bot.b.id).reason === 'SIGNAL', '写明因为信号暂停')
await call('POST', `/api/bots/${bot.b.id}/resume`, {})
await call('POST', '/api/bots/order', { order: ['bot', 'ai', 'signal'] })
await new Promise((res) => setTimeout(res, 5500))
r = await hook(tok, 'buy SOL 6%')
ok(r.b.status === 'rejected' && r.b.code === 'BOT_HAS_PRIORITY', `机器人调到前面后拒绝信号（${r.b.code}）`)
await call('POST', `/api/bots/${bot.b.id}/stop`, { mode: 'close' })

// 界面
await p.goto(BASE + '/?page=account')
await p.waitForSelector('[data-prio]')
ok(/手动交易/.test(await p.locator('[data-prio]').innerText()), '账户页有引擎优先级，手动在第一')
ok((await p.locator('[data-prio-item]').first().getAttribute('data-prio-item')) === 'bot', '显示刚才改的顺序')
await p.locator('[data-prio-item=bot] button').nth(1).click()
await until(async () => (await call('GET', '/api/bots')).b.order[0] === 'ai')
ok((await call('GET', '/api/bots')).b.order[0] === 'ai', '界面上能调整优先级')
ok(await p.locator('[data-sig-local]').count() === 1, '本机时写明 TradingView 连不到')
await p.locator('[data-sig-send]').click()
await p.waitForSelector('[data-sig-msg]')
ok(/看得懂/.test(await p.locator('[data-sig-msg]').innerText()), '能检查格式')
await p.locator('[data-sig-add]').click()
await p.waitForSelector('[data-sig-url]')
const url = await p.locator('[data-sig-url] code').innerText()
ok(/\/api\/hook\/tv\/[0-9a-f]{40,}/.test(url), '显示完整地址')
await p.locator('[data-sig-send]').click()
await p.waitForFunction(() => /已下单|重复|不够|拒|纪律/.test(document.querySelector('[data-sig-msg]')?.textContent ?? ''), null, { timeout: 5000 }).catch(() => {})
ok(await p.locator('[data-sig-logs] tbody tr').count() > 0, '收到的信号都列出来')
await p.locator('[data-sig-url]').scrollIntoViewIfNeeded()
await p.screenshot({ path: `${OUT}/signal-account.png` })
ok(errs.length === 0, `流程中没有报错（${errs[0] ?? ''}）`)
console.log(`\n通过 ${pass}，失败 ${fail}`)
await browser.close()
