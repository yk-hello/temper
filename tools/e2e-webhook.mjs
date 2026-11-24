// 对外 Webhook（18 第 6.3 条）：在本机起一个接收端，界面上填地址、拿签名密钥、发测试，再触发一次真的到价提醒，
// 接收端验签名。会注册账号、改模拟价格：只能对着测试环境跑（要 QS_WEBHOOK_PRIVATE=1，tools/dev/restart.ps1 里开着）。
// 用法：node tools/e2e-webhook.mjs <截图目录> [http://127.0.0.1:4173]
import { chromium } from 'playwright'
import { createServer } from 'node:http'
import { createHmac } from 'node:crypto'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const rand = () => Math.random().toString(36).slice(2)

// 接收端：记下每一条（原始请求体和签名头）
const got = []
const srv = createServer((req, res) => {
  let b = ''
  req.on('data', (c) => { b += c })
  req.on('end', () => { got.push({ body: b, ts: req.headers['x-qs-timestamp'], sig: req.headers['x-qs-signature'], ev: req.headers['x-qs-event'] }); res.writeHead(200); res.end('ok') })
})
await new Promise((r) => srv.listen(0, '127.0.0.1', r))
const HOOK = `http://127.0.0.1:${srv.address().port}/hook`

const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
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
await call('POST', '/api/auth/register', { email: `wh${Date.now()}${rand().slice(0, 4)}@example.com`, password: `wh-${rand()}-pass` })

await p.goto(BASE + '/?page=account&sec=notify')
const row = p.locator('[data-wh]')
await row.waitFor()
await row.scrollIntoViewIfNeeded()
ok(await row.getAttribute('data-wh') === 'none', '还没设 Webhook')
await row.locator('[data-wh-url]').fill('ftp://x')
await row.locator('[data-wh-save]').click()
ok(!!(await row.locator('.nt-wh-err').innerText().catch(() => '')).match(/http/), '地址不对：说清楚要 http 或 https')
await row.locator('[data-wh-url]').fill(HOOK)
await row.locator('[data-wh-save]').click()
const dlg = p.locator('[data-wh-secret]')
await dlg.waitFor()
const SECRET = (await dlg.locator('code').first().innerText()).trim()
ok(/^whsec_[0-9a-f]{48}$/.test(SECRET), '保存后给一次签名密钥')
ok(/X-QS-Signature/.test(await dlg.innerText()), '写了怎么验证')
await p.locator('.dlg:has([data-wh-secret])').screenshot({ path: `${OUT}/wh-01-secret.png` })
await dlg.locator('.btn.primary').click()
ok(!!(await p.locator('[data-notify] th', { hasText: 'Webhook' }).count()), '每种事件多了一列 Webhook')

// 测试
await row.locator('[data-wh-test]').click()
for (let i = 0; i < 20 && !got.length; i++) await sleep(200)
const t0 = got[0]
ok(!!t0 && JSON.parse(t0.body).event === 'test' && t0.ev === 'test', '测试消息到了')
ok(!!t0 && t0.sig === createHmac('sha256', SECRET).update(`${t0.ts}.${t0.body}`).digest('hex'), '签名对得上')
ok(!!(await p.locator('[data-wh-last]').innerText().then((s) => /成功（200）/.test(s)).catch(() => false)) || !!(await (async () => { await sleep(800); return /成功（200）/.test(await p.locator('[data-wh-last]').innerText()) })()), '显示最近一次成功')
await row.screenshot({ path: `${OUT}/wh-02-row.png` })

// 真的事件：到价提醒（默认勾着 Webhook）
const px0 = Number((await call('GET', '/api/market/snapshot')).b.instruments.find((i) => i.instrument === 'BTC-PERP').last)
await call('POST', '/api/notify/alerts', { instrument: 'BTC-PERP', px: Math.round(px0 * 1.01) })
await call('POST', '/api/dev/sim-price', { instrument: 'BTC-PERP', price: Number((px0 * 1.012).toFixed(1)) })
let ev = null
for (let i = 0; i < 40 && !ev; i++) { await sleep(250); ev = got.map((g) => ({ ...g, j: JSON.parse(g.body) })).find((g) => g.j.event === 'price_alert') }
ok(!!ev && ev.j.params?.dir === 'up' && /BTC/.test(ev.j.text ?? ''), `到价提醒发到了（${ev?.j.text ?? '没收到'}）`)
ok(!!ev && ev.sig === createHmac('sha256', SECRET).update(`${ev.ts}.${ev.body}`).digest('hex'), '到价提醒的签名也对')

// 关掉这种事件的 Webhook：不再发
await p.reload()
await row.waitFor()
const box = p.locator('[data-pref-wh="price_alert"]')
await box.scrollIntoViewIfNeeded()
await box.uncheck()
await sleep(500)
const n = got.length
await call('POST', '/api/notify/alerts', { instrument: 'BTC-PERP', px: Math.round(px0 * 1.02) })
await call('POST', '/api/dev/sim-price', { instrument: 'BTC-PERP', price: Number((px0 * 1.022).toFixed(1)) })
await sleep(3000)
ok(got.length === n, '取消勾选后这种事件不再发')
await row.locator('[data-wh-del]').click()
await sleep(500)
ok(await row.getAttribute('data-wh') === 'none', '删掉了')
ok(errs.length === 0, `页面没有报错（${errs.join(' | ').slice(0, 200)}）`)
await browser.close()
srv.close()
console.log(`\n${pass} 通过，${fail} 失败`)
process.exit(fail ? 1 : 0)
