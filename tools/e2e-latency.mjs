// 模拟下单延迟（D94）的检查：选地区以后，下单真的晚到、按到达时的盘口成交；界面写着延迟多少。
// 会注册账号、下单，只能对着测试环境跑（不要对着 8080）。
//   node tools/e2e-latency.mjs <截图目录> <网址>
import { chromium } from 'playwright'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const ctx = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN' })
await ctx.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1') })
const p = await ctx.newPage()
const errs = []
p.on('pageerror', (e) => errs.push(String(e)))
const call = (method, path, body) => p.evaluate(async ({ method, path, body }) => {
  const t0 = performance.now()
  const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
  let j = null
  try { j = await r.json() } catch { /* 空 */ }
  return { s: r.status, b: j, ms: performance.now() - t0 }
}, { method, path, body })
const rand = () => Math.random().toString(36).slice(2)

await p.goto(BASE + '/?page=login')
await call('POST', '/api/auth/register', { email: `lat${Date.now()}${rand().slice(0, 4)}@example.com`, password: `lat-${rand()}-pass` })
const tp = 'tp-' + rand()
await call('POST', '/api/me/trade-password', { new: tp })
await call('POST', '/api/discipline/enabled', { on: false, trade_password: tp })
const acct = (await call('GET', '/api/paper-accounts')).b.accounts[0].id

const i0 = await call('GET', '/api/trade/latency')
ok(i0.s === 200 && i0.b.mode === 'measured' && i0.b.regions.length === 8, `默认跟随实测，8 个地区（中国大陆那一项删了，D99）（${i0.b?.mode}）`)
ok(i0.b.measured_ms === null, '模拟行情测不到往返时间：跟随实测时立即成交')
ok((await call('PUT', '/api/trade/latency', { mode: 'mars' })).s === 400, '不认识的地区被拒')

// 基准：关闭时下单很快
await call('PUT', '/api/trade/latency', { mode: 'off' })
const fast = await call('POST', `/api/trade/${acct}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'market', qty: '0.002' })
ok(fast.s === 200, `关闭：立即成交（${Math.round(fast.ms)} 毫秒）`)

// 法兰克福：一次往返约 230 毫秒
const fr = await call('PUT', '/api/trade/latency', { mode: 'frankfurt' })
ok(fr.s === 200 && fr.b.mode === 'frankfurt', '改成法兰克福')
const slow = await call('POST', `/api/trade/${acct}/orders`, { instrument: 'BTC-PERP', side: 'sell', type: 'market', qty: '0.002' })
ok(slow.s === 200 && slow.ms >= 220 && slow.ms < 1500, `法兰克福：回报在一个往返之后（${Math.round(slow.ms)} 毫秒）`)
ok(slow.ms - fast.ms > 150, '比关闭时明显慢')

// 界面：账户页的设置、下单区的延迟一行
await call('PUT', '/api/trade/latency', { mode: 'hongkong' })
await p.goto(BASE + '/?page=account')
const row = p.locator('[data-latency]')
await row.waitFor()
ok(/香港/.test(await row.innerText()), `账户页显示当前选的地区（${(await row.innerText()).replace(/\s+/g, ' ').slice(0, 60)}）`)
await row.scrollIntoViewIfNeeded()
await row.locator('.sel-btn').click()
await p.waitForTimeout(300)
const opts = await row.locator('.sel-pop li').allInnerTexts()
ok(opts.length === 10 && opts.some((x) => /法兰克福/.test(x) && /毫秒/.test(x)) && opts.some((x) => /跟随实测/.test(x) && /测不到/.test(x)), `选项：实测、8 个地区（带毫秒）、关闭（${opts.length}）`)
await p.screenshot({ path: `${OUT}/lat-01-account.png`, clip: await row.boundingBox().then((b) => ({ x: b.x - 8, y: b.y - 8, width: b.width + 16, height: Math.min(540, b.height + 380) })) })
await row.locator('.sel-pop li', { hasText: '新加坡' }).click()
ok(!!(await p.locator('.toast').filter({ hasText: '已保存' }).first().waitFor({ timeout: 5000 }).then(() => true).catch(() => false)), '选了马上保存')
ok((await call('GET', '/api/trade/latency')).b.mode === 'singapore', '服务器记住了')

await p.goto(BASE + '/?page=trade&i=BTC-PERP')
await p.waitForSelector('.op-submit')
const lr = p.locator('[data-op="latency"]')
await lr.waitFor({ timeout: 10000 }).catch(() => {})
ok(/毫秒/.test(await lr.innerText().catch(() => '')) && /新加坡/.test(await lr.innerText().catch(() => '')), `下单区写着延迟（${await lr.innerText().catch(() => '无')}）`)
await p.locator('.mk-side').first().screenshot({ path: `${OUT}/lat-02-panel.png` })

await call('PUT', '/api/trade/latency', { mode: 'measured' })
ok(errs.length === 0, `页面没有报错（${errs.slice(0, 2).join(' | ')}）`)
console.log(`\n${pass} 通过，${fail} 失败`)
await browser.close()
process.exit(fail ? 1 : 0)
