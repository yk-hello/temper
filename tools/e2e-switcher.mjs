// 顶栏账户切换（D73）在各种宽度、缩放下的样子：截图 + 量下拉有没有出屏幕、顶栏有没有挤出横向滚动。
// 会注册账号、接测试交易所：只能对着开了 QS_LIVE_FAKE 的测试环境跑（不要对着 8080）。
// 用法：node tools/e2e-switcher.mjs <截图目录> [http://127.0.0.1:4173]
import { chromium } from 'playwright'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
const [FKEY, FSECRET] = (process.env.QS_E2E_LIVE || 'e2efakekey-0123456789abcd:e2efakesecret-0123456789abcd').split(':')
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const rand = () => Math.random().toString(36).slice(2)
const call = (p, method, path, body) => p.evaluate(async ({ method, path, body }) => {
  const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
  let j = null
  try { j = await r.json() } catch { /* 不是 JSON */ }
  return { s: r.status, b: j }
}, { method, path, body })

// 一个人：一个模拟账户 + 一个测试交易所账户（名字故意长一点，看省略号）
const c0 = await browser.newContext({ viewport: { width: 1536, height: 730 }, locale: 'zh-CN' })
const p0 = await c0.newPage()
await p0.goto(BASE + '/?page=login')
await call(p0, 'POST', '/api/auth/register', { email: `sw${Date.now()}${rand().slice(0, 4)}@example.com`, password: `sw-${rand()}-pass` })
const tp = 'tp-' + rand()
await call(p0, 'POST', '/api/me/trade-password', { new: tp })
const k = await call(p0, 'POST', '/api/live/keys', { env: 'fake', label: 'fake', key: FKEY, secret: FSECRET, trade_password: tp })
ok(k.s === 200, '接入测试交易所')
const a = await call(p0, 'POST', '/api/live/accounts', { key_id: k.b.key.id, name: '主账户 BTC 波段' })
const LA = a.b.id
ok(LA >= 1e12, '建了交易所账户')
const cookies = await c0.cookies()
await c0.close()

const sizes = [
  [1920, 1080, 1], [1536, 864, 1.25], [1280, 720, 1.5], [960, 540, 2],
  [1366, 768, 1], [1024, 768, 1.25], [768, 1024, 2], [390, 844, 3],
]
for (const [w, h, dpr] of sizes) {
  const mobile = w < 768
  const c = await browser.newContext({ viewport: { width: w, height: h }, deviceScaleFactor: dpr, locale: 'zh-CN', isMobile: mobile, hasTouch: mobile })
  await c.addCookies(cookies)
  await c.addInitScript((id) => { localStorage.setItem('qs.wizardSeen', '1'); localStorage.setItem('qs.paper', String(id)) }, LA)
  const p = await c.newPage()
  const errs = []
  p.on('pageerror', (e) => errs.push(String(e)))
  await p.goto(BASE + '/?page=trade&i=BTC-PERP')
  await p.locator('[data-sw]').waitFor()
  await p.waitForTimeout(1200)
  const over = await p.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
  const btn = await p.locator('[data-sw]').boundingBox()
  ok(over <= 1 && btn && btn.x >= 0 && btn.x + btn.width <= w + 1, `${w}×${h} @${dpr}x：顶栏放得下，账户按钮在屏幕里（多出 ${over}px）`)
  await p.locator('[data-sw]').click()
  await p.locator('.sw-pop').waitFor()
  await p.waitForTimeout(250)
  const r = await p.locator('.sw-pop').boundingBox()
  ok(r && r.x >= 0 && r.x + r.width <= w + 1, `${w}×${h} @${dpr}x：下拉在屏幕里（${Math.round(r?.x ?? -1)}–${Math.round((r?.x ?? 0) + (r?.width ?? 0))}）`)
  const clipW = Math.min(w, 820)
  await p.screenshot({ path: `${OUT}/sw-${w}x${h}@${dpr}.png`, clip: { x: w - clipW, y: 0, width: clipW, height: Math.min(h, 300) } })
  ok(errs.length === 0, `${w}×${h}：没有报错 ${errs.join(' ')}`)
  await c.close()
}
await browser.close()
console.log(`\n${pass} 通过，${fail} 失败`)
process.exit(fail ? 1 : 0)
