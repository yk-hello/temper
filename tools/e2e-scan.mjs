// 筛选器、异动、今日（市场页）的检查。只读：不注册、不下单，所以测试环境和 8080 都能跑（加 --readonly-ok）。
//   node tools/e2e-scan.mjs <截图目录> <网址>
import { chromium } from 'playwright'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const newPage = async (opts = {}) => {
  const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN', timezoneId: 'Asia/Taipei', ...opts })
  await c.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1') })
  const p = await c.newPage()
  p.errs = []
  p.on('pageerror', (e) => p.errs.push(String(e)))
  p.on('console', (m) => { if (m.type() === 'error' && !/Failed to load resource/.test(m.text())) p.errs.push(m.text()) })
  return p
}
const until = async (fn, ms = 30000) => {
  const t0 = Date.now()
  while (Date.now() - t0 < ms) { const v = await fn().catch(() => null); if (v) return v; await new Promise((r) => setTimeout(r, 300)) }
  return null
}

const p = await newPage()
const api = await (await fetch(BASE + '/api/scan')).json()
ok(Array.isArray(api.rows) && api.rows.length > 0, `接口有行（${api.rows?.length}）`)

// ---------- 筛选 ----------
await p.goto(BASE + '/?page=market&tab=screen')
const rows = p.locator('[data-sc="table"] tbody tr')
ok(!!(await until(async () => (await rows.count()) > 0)), '表格有行')
const n0 = await rows.count()
// 排序：点"24 小时"表头，按 24 小时涨跌从大到小
await p.locator('.sc-th button', { hasText: /^24 小时/ }).click()
const chg = await rows.evaluateAll((trs) => trs.slice(0, 20).map((tr) => Number(tr.children[5].textContent.replace('−', '-').replace(/[+%]/g, ''))))
ok(chg.every((v, i) => i === 0 || v <= chg[i - 1] + 1e-9), `点表头按 24 小时排序（${chg.slice(0, 3).join(', ')}…）`)
// 搜索
await p.locator('[data-sc="q"]').fill('BTC')
ok(!!(await until(async () => { const s = await rows.allInnerTexts(); return s.length >= 1 && s.every((x) => /BTC/.test(x)) })), '搜索 BTC 只剩含 BTC 的')
await p.locator('[data-sc="q"]').fill('')
// 自选
await rows.first().locator('.sc-star button').click()
await p.locator('.seg button', { hasText: '自选' }).click()
ok(await rows.count() === 1, '加一个自选，自选里有 1 个')
await p.reload()
await p.locator('.seg button', { hasText: '自选' }).click()
ok(!!(await until(async () => (await rows.count()) === 1)), '刷新后自选还在')
await p.locator('.seg button', { hasText: '全部' }).click()
await until(async () => (await rows.count()) === n0)
ok((await p.locator('[data-sc="events"]').count()) === 1, '右边有异动栏')
await p.mouse.move(0, 0)
await p.screenshot({ path: `${OUT}/sc-01-screener.png` })
const scroll = await p.evaluate(() => document.scrollingElement.scrollHeight - innerHeight)
ok(scroll <= 1, `整页不滚动（多 ${scroll}px）`)
// 点一行去交易
await rows.first().click()
ok(!!(await until(async () => /page=trade/.test(p.url()))), '点一行去交易页')

// ---------- 今日 ----------
await p.goto(BASE + '/?page=market&tab=today')
const tiles = p.locator('[data-td="tiles"] > div')
ok(!!(await until(async () => (await tiles.count()) === 6)), '六个总览数字')
ok(!!(await until(async () => /\d+ \/ \d+/.test(await tiles.nth(2).innerText()))), `涨跌家数（${(await tiles.nth(2).innerText()).replace(/\s+/g, ' ')}）`)
for (const k of ['levels', 'calendar', 'events', 'movers']) ok((await p.locator(`[data-td="${k}"]`).count()) === 1, `有 ${k}`)
ok(/alternative\.me/.test(await p.locator('.td-src').innerText()), '写明数据来源')
await p.waitForTimeout(1500)
await p.screenshot({ path: `${OUT}/sc-02-today.png` })

// ---------- 窄屏 ----------
const m = await newPage({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true })
for (const tab of ['screen', 'today']) {
  await m.goto(BASE + `/?page=market&tab=${tab}`)
  await m.waitForTimeout(2500)
  const over = await m.evaluate(() => document.documentElement.scrollWidth - innerWidth)
  ok(over <= 1, `${tab} 手机宽度不横向滚动（多 ${over}px）`)
  await m.screenshot({ path: `${OUT}/sc-03-phone-${tab}.png` })
}

ok(p.errs.length === 0 && m.errs.length === 0, `页面没有报错（${[...p.errs, ...m.errs].slice(0, 2).join(' | ')}）`)
console.log(`\n${pass} 通过，${fail} 失败`)
await browser.close()
process.exit(fail ? 1 : 0)
