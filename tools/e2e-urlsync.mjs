// 地址栏和页面一致（D118 第 5 条，11-28 外部反馈）：从 ?page=trade&i=ETH-PERP 进来、在页面里切到 SOL，
// 地址栏要跟着变；刷新、去登录再回来都还是 SOL。图表样式、市场页和复盘页的标签同理；地址栏没写的不加。
// 用法：node e2e-urlsync.mjs <截图目录> <前端地址>。会注册账号，不要对着 8080 跑。
import { createRequire } from 'module'
import { randomBytes } from 'crypto'
import fs from 'fs'
const require = createRequire(import.meta.url)
const { chromium } = require('playwright-core')
const OUT = process.argv[2] ?? 'shots/urlsync'
const BASE = process.argv[3] ?? 'http://127.0.0.1:4173'
if (/:8080/.test(BASE)) throw new Error('不要对着 8080 跑')
fs.mkdirSync(OUT, { recursive: true })
let bad = 0, good = 0
const ok = (c, s, extra = '') => { if (c) { good++; console.log('✓', s) } else { bad++; console.log('✗', s, extra) } }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const b = await chromium.launch({ channel: process.env.PW_CHANNEL ?? 'msedge', headless: true })
const errs = []
const name = `us${randomBytes(4).toString('hex')}`
const pw = 'pw-' + randomBytes(12).toString('hex')
const ctxOf = async (w, h, state) => {
  const c = await b.newContext({ viewport: { width: w, height: h }, deviceScaleFactor: 2, locale: 'zh-CN', timezoneId: 'Asia/Shanghai', storageState: state })
  await c.addInitScript(() => { try { localStorage.setItem('qs.wizardSeen', '1'); localStorage.setItem('qs.heroSeen', '1'); sessionStorage.setItem('qs.heroSeen', '1') } catch {} })
  const p = await c.newPage()
  p.on('pageerror', (e) => errs.push(String(e).slice(0, 300)))
  return { c, p }
}
const q = (p) => new URL(p.url()).search
const until = async (fn, ms = 8000) => { const t = Date.now(); while (Date.now() - t < ms) { if (await fn()) return true; await sleep(100) } return false }
const coin = (p) => p.locator('.mk-picker').innerText().catch(() => '')

// 先建一个账号（之后用登录）
{
  const { c, p } = await ctxOf(1536, 730)
  await p.goto(`${BASE}/?page=login&mode=register`)
  await p.waitForSelector('.au-card')
  await p.getByLabel('账户名').fill(name)
  await p.getByLabel('密码', { exact: true }).fill(pw)
  await p.locator('.au-submit').click()
  await until(async () => q(p) === '')
  await p.evaluate(() => fetch('/api/auth/logout', { method: 'POST', headers: { 'X-QS-Client': 'web' } }))
  await c.close()
}

// ---------- 客户说的那一步：链接进 ETH，页面里切到 SOL，不刷新，去登录 ----------
{
  const { c, p } = await ctxOf(390, 844)
  await p.goto(`${BASE}/?page=trade&i=ETH-PERP`)
  await p.waitForSelector('.mk-tabs-main')
  await sleep(800)
  const h0 = await p.evaluate(() => history.length)
  await p.locator('.mk-tabs-main [data-tab=watch]').click()
  await p.locator('.mk-watch-list button:visible', { hasText: 'SOL' }).first().click()
  ok(await until(async () => /SOL/.test(await coin(p))), '页面里切到了 SOL')
  ok(q(p) === '?page=trade&i=SOL-PERP', '地址栏跟着变成 SOL', q(p))
  ok(await p.evaluate(() => history.length) === h0, '不多出历史记录（后退不会一个一个币往回退）')
  await p.locator('.mk-tabs-main [data-tab=trade]').click()
  await p.locator('.op-out-b').click()
  await p.waitForSelector('.au-card')
  ok(await p.evaluate(() => sessionStorage.getItem('qs.back')) === 'trade&i=SOL-PERP', '登录页记住的是 SOL（客户读到的是 ETH）')
  await p.getByLabel('账户名').fill(name)
  await p.getByLabel('密码', { exact: true }).fill(pw)
  await p.locator('.au-submit').click()
  ok(await until(async () => q(p) === '?page=trade&i=SOL-PERP'), '登录后回到交易页 SOL', q(p))
  await p.waitForSelector('.mk-tabs-main'); await sleep(800)
  ok(/SOL/.test(await coin(p)), '页面上是 SOL')
  // 登录状态下再切一次，刷新
  await p.locator('.mk-tabs-main [data-tab=watch]').click()
  await p.locator('.mk-watch-list button:visible', { hasText: 'BTC' }).first().click()
  await until(async () => /BTC/.test(await coin(p)))
  await p.reload(); await p.waitForSelector('.mk-tabs-main'); await sleep(800)
  ok(/BTC/.test(await coin(p)) && q(p) === '?page=trade&i=BTC-PERP', '切到 BTC 后刷新：还是 BTC', `${await coin(p)} ${q(p)}`)
  globalThis.state = await c.storageState()
  await c.close()
}

// ---------- 同一类：图表样式、市场页标签、复盘页标签 ----------
{
  const { c, p } = await ctxOf(1536, 730, globalThis.state)
  await p.goto(`${BASE}/?page=trade&i=BTC-PERP&chart=heat`)
  await p.waitForSelector('.mk-chart [data-tab=candle]', { timeout: 15000 })
  await p.locator('.mk-chart [data-tab=candle]').first().click()
  ok(await until(async () => q(p) === '?page=trade&i=BTC-PERP'), '热力图切回 K 线：地址栏去掉 chart=heat', q(p))
  await p.reload(); await p.waitForSelector('.mk-chart [data-tab=candle]'); await sleep(500)
  ok(await p.locator('.mk-chart [data-tab=candle][aria-pressed=true]').count() === 1, '刷新后还是 K 线')

  await p.goto(`${BASE}/?page=market&tab=heat`)
  await p.waitForSelector('.lp-head [data-tab=today]', { timeout: 15000 })
  await p.locator('.lp-head [data-tab=today]').click()
  ok(await until(async () => q(p) === '?page=market&tab=today'), '市场页从热力切到今日：地址栏 tab=today', q(p))
  await p.reload(); await p.waitForSelector('.lp-head [data-tab=today]'); await sleep(500)
  ok(await p.locator('.lp-head [data-tab=today][aria-pressed=true]').count() === 1, '刷新后还是今日')

  await p.goto(`${BASE}/?page=review&tab=perf`)
  await p.waitForSelector('.rv-head [data-tab=log]', { timeout: 15000 })
  await p.locator('.rv-head [data-tab=log]').click()
  ok(await until(async () => q(p) === '?page=review&tab=log'), '复盘页从绩效切到交易日志：地址栏 tab=log', q(p))

  // 地址栏没写的不加
  await p.goto(`${BASE}/?page=trade`)
  await p.waitForSelector('.mk-picker'); await sleep(500)
  await p.evaluate(() => window.dispatchEvent(new CustomEvent('qs:pick', { detail: 'ETH-PERP' })))
  await until(async () => /ETH/.test(await coin(p)))
  ok(q(p) === '?page=trade', '地址栏本来没写品种的：不加', q(p))
  await p.reload(); await p.waitForSelector('.mk-picker'); await sleep(800)
  ok(/ETH/.test(await coin(p)), '刷新后还是 ETH（记在浏览器里，和原来一样）')
  await c.close()
}
// 登录页：注册切回登录，地址栏去掉 mode=register
{
  const { c, p } = await ctxOf(1536, 730)
  await p.goto(`${BASE}/?page=login&mode=register`)
  await p.waitForSelector('.au-card')
  await p.getByRole('button', { name: '登录' }).last().click()
  ok(await until(async () => q(p) === '?page=login'), '注册切到登录：地址栏去掉 mode=register', q(p))
  await c.close()
}
ok(errs.length === 0, '页面没报错', errs.join(' | '))
await b.close()
console.log(`\n${good} 通过，${bad} 失败`)
process.exit(bad ? 1 : 0)
