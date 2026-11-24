// 风险页的"历史情景"和"最坏的一天"（18 第 10.3 条）：开两个仓位（多 BTC、多 SOL），看 8 个历史情景都有结果、
// 2025-10-10 那天 SOL 跌得比 BTC 狠、高杠杆的仓位会被强平；最坏的一天三行数字；全部账户和单个账户；手机宽度不横向滚动。
// 历史日线取的是币安合约的公开接口（测试环境也要能上网）。会注册账号、下模拟单：只能对着测试环境跑。
// 用法：node tools/e2e-riskhist.mjs <截图目录> [http://127.0.0.1:4173]
import { chromium } from 'playwright'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const rand = () => Math.random().toString(36).slice(2)
const until = async (f, ms = 30000) => { const t0 = Date.now(); while (Date.now() - t0 < ms) { try { const v = await f(); if (v) return v } catch { /* 再试 */ } await sleep(300) } return null }

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
await call('POST', '/api/auth/register', { email: `rh${Date.now()}${rand().slice(0, 4)}@example.com`, password: `rh-${rand()}-${rand()}` })
const acct = (await call('GET', '/api/paper-accounts')).b.accounts[0].id
const TP = 'tp-' + rand()
await call('POST', '/api/me/trade-password', { new: TP })
await call('POST', '/api/discipline/enabled', { on: false, trade_password: TP })
await call('POST', `/api/trade/${acct}/margin-mode`, { instrument: 'SOL-PERP', mode: 'isolated' }) // 逐仓：只有这份保证金扛着
await call('POST', `/api/trade/${acct}/leverage`, { instrument: 'SOL-PERP', leverage: 20 })
let r1 = await call('POST', `/api/trade/${acct}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'market', qty: '0.05' })
let r2 = await call('POST', `/api/trade/${acct}/orders`, { instrument: 'SOL-PERP', side: 'buy', type: 'market', qty: '100' })
ok(r1.s === 200 && r2.s === 200, `开两个仓位（${r1.b?.code ?? ''} ${r2.b?.code ?? ''}）`)

// 接口
const sc = await until(async () => { const r = await call('GET', '/api/risk/scenarios'); return r.s === 200 && r.b.accounts?.[0]?.scenarios?.length ? r.b : null }, 45000)
ok(!!sc, '接口：取到了历史情景')
const mine = sc?.accounts?.[0]
ok(mine?.scenarios?.length === 8, `8 个历史情景（${mine?.scenarios?.length}）`)
const oct = mine?.scenarios?.find((s) => s.id === 'oct10')
const mv = (inst) => Number(oct?.worst?.positions?.find((x) => x.instrument === inst)?.move)
ok(oct && mv('SOL-PERP') < mv('BTC-PERP') && mv('BTC-PERP') < -0.1, `2025-10-10：SOL 当天最低跌得比 BTC 狠（SOL ${(mv('SOL-PERP') * 100).toFixed(1)}%，BTC ${(mv('BTC-PERP') * 100).toFixed(1)}%）`)
ok(oct?.worst?.positions?.some((x) => x.instrument === 'SOL-PERP' && x.liq), '20 倍逐仓的 SOL 那天会被强平')
ok(!oct?.worst?.positions?.find((x) => x.instrument === 'BTC-PERP')?.liq, '全仓的 BTC 有整个钱包扛着，不强平')
const tesla = mine?.scenarios?.find((s) => s.id === 'tesla')
ok(tesla && Number(tesla.close.after) > Number(tesla.close.before), '暴涨那天多单是赚的')
const w = mine?.worst_day
ok(w && w.days >= 300 && w.worst <= w.p99 && w.p99 <= w.p95 && w.p95 < 0, `最坏的一天：最差 ≤ 百里挑一 ≤ 二十里挑一（${w?.worst} ${w?.p99} ${w?.p95}，${w?.days} 天）`)

// 页面
await p.goto(BASE + '/?page=risk')
await p.locator('.rk-tabs .seg button', { hasText: '压力测试' }).click().catch(() => {})
const head = p.locator('.rk-g-head')
await head.waitFor()
await head.locator('.seg button', { hasText: '历史情景' }).click()
ok(!!(await until(async () => (await p.locator('[data-scenarios] tbody tr').count()) === 8)), '历史情景表：8 行')
const octRow = p.locator('[data-sc="oct10"]')
ok(/SOL/.test(await octRow.locator('td').last().innerText()), `2025-10-10 那行写着 SOL 会被强平（${await octRow.locator('td').last().innerText()}）`)
ok(/最低 −/.test(await octRow.innerText()), 'BTC 当天最低多少')
const lastCell = await p.locator('[data-scenarios] thead th').last().evaluate((th) => { const t = th.getBoundingClientRect(), box = th.closest('.rk-panel').getBoundingClientRect(); return Math.round(box.right - t.right) })
ok(lastCell >= 0, `最后一列（会被强平）完整显示（离右边 ${lastCell}px）`)
ok(/不算止损/.test(await p.locator('.rk-wd-note').innerText()), '写明怎么算的')
const over = await p.evaluate(() => document.documentElement.scrollWidth - innerWidth)
ok(over <= 1, `不横向滚动（${over}）`)
await p.screenshot({ path: `${OUT}/rh-01-hist.png` })
await head.locator('.seg button', { hasText: '最坏的一天' }).click()
await p.waitForSelector('[data-worst-day]')
ok((await p.locator('.rk-wd-row').count()) === 3, '最坏的一天：三行')
ok(/100 天里最差/.test(await p.locator('[data-worst-day]').innerText()), '大白话，不写 VaR')
await p.screenshot({ path: `${OUT}/rh-02-worst.png` })
await p.reload()
await p.locator('.rk-tabs .seg button', { hasText: '压力测试' }).click().catch(() => {})
ok(!!(await until(async () => (await p.locator('[data-worst-day]').count()) === 1, 15000)), '刷新后还停在"最坏的一天"')
await head.locator('.seg button', { hasText: 'BTC 涨跌' }).click()

// 手机
const m = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true, locale: 'zh-CN', storageState: await c.storageState() })
const mp = await m.newPage()
mp.on('pageerror', (e) => errs.push(String(e)))
await mp.goto(BASE + '/?page=risk')
await mp.locator('.rk-tabs .seg button', { hasText: '压力测试' }).click().catch(() => {})
await mp.locator('.rk-g-head .seg button', { hasText: '历史情景' }).click()
await until(async () => (await mp.locator('[data-scenarios] tbody tr').count()) === 8)
const mover = await mp.evaluate(() => document.documentElement.scrollWidth - innerWidth)
ok(mover <= 1, `手机不横向滚动（${mover}）`)
const mLast = await mp.locator('[data-scenarios] thead th').last().evaluate((th) => Math.round(innerWidth - th.getBoundingClientRect().right))
ok(mLast >= 0, `手机上最后一列也看得到（离右边 ${mLast}px）`)
await mp.screenshot({ path: `${OUT}/rh-03-phone.png` })

ok(errs.length === 0, `页面没有报错（${errs.slice(0, 2).join(' | ')}）`)
console.log(`\n${pass} 通过，${fail} 失败`)
await browser.close()
process.exit(fail ? 1 : 0)
