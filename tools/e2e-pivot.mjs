// 复盘"自由透视"（18 第 10.5 条）：开平几笔，打开"自由透视"，Perspective 的表格加载出来、按品种分组、几个预设能切、
// 刷新后还是上次的布局；没打开这一页时不下载它的几 MB；手机宽度不横向滚动。
// 会注册账号、下模拟单：只能对着测试环境跑。
// 用法：node tools/e2e-pivot.mjs <截图目录> [http://127.0.0.1:4173]
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
const errs = [], wasm = []
p.on('pageerror', (e) => errs.push(String(e)))
p.on('request', (r) => { if (/\.wasm/.test(r.url())) wasm.push(r.url()) })
const call = (method, path, body) => p.evaluate(async ({ method, path, body }) => {
  const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
  return { s: r.status, b: await r.json().catch(() => null) }
}, { method, path, body })
await p.goto(BASE + '/?page=login')
await call('POST', '/api/auth/register', { email: `pv${Date.now()}${rand().slice(0, 4)}@example.com`, password: `pv-${rand()}-${rand()}` })
const acct = (await call('GET', '/api/paper-accounts')).b.accounts[0].id
const TP = 'tp-' + rand()
await call('POST', '/api/me/trade-password', { new: TP })
await call('POST', '/api/discipline/enabled', { on: false, trade_password: TP })
// 开平 4 笔：BTC 多、ETH 空、BTC 空、SOL 多
for (const [inst, side, qty] of [['BTC-PERP', 'buy', '0.02'], ['ETH-PERP', 'sell', '0.3'], ['BTC-PERP', 'sell', '0.02'], ['SOL-PERP', 'buy', '3']]) {
  await call('POST', `/api/trade/${acct}/orders`, { instrument: inst, side, type: 'market', qty })
  await sleep(700)
  await call('POST', `/api/trade/${acct}/close`, { instrument: inst })
  await sleep(700)
}
const n = await until(async () => { const r = await call('GET', `/api/journal/a/${acct}/trades?origin=all`); return r.b?.trades?.length >= 4 ? r.b.trades.length : null }, 20000)
ok(n >= 4, `有 ${n} 笔平仓的交易`)

await p.goto(BASE + '/?page=review')
await p.waitForSelector('.rv-head')
await sleep(1500)
ok(wasm.length === 0, `没打开自由透视时不下载它（${wasm.length}）`)
await p.locator('.rv-head .seg button', { hasText: '自由透视' }).click()
// 来源切到"全部"（开平的单来源是手动，默认就看得到；保险起见）
const grid = await until(async () => p.evaluate(() => {
  const v = document.querySelector('perspective-viewer')
  const g = v?.querySelector('perspective-viewer-datagrid')
  const txt = (g?.shadowRoot ?? g)?.textContent ?? ''
  return txt.length > 20 ? txt : null
}), 45000)
ok(!!grid, '表格加载出来了')
ok(wasm.some((u) => /perspective-server/.test(u)) && wasm.some((u) => /perspective-viewer/.test(u)), '打开时才下载它的两个 wasm')
ok(/BTC/.test(grid ?? '') && /ETH/.test(grid ?? ''), '按品种分组：有 BTC、ETH')
ok(/品种|净盈亏|笔数/.test(grid ?? ''), '列名是中文')
ok(/摩根大通开源的 Perspective/.test(await p.locator('.pv-note').innerText()), '写明用的是谁的开源组件')
await sleep(800)
await p.screenshot({ path: `${OUT}/pv-01-coin.png` })
// 只有净盈亏按正负上色：笔数、手续费是正数，也不能是绿色（看着像赚了）
const colors = await p.evaluate(() => {
  const g = document.querySelector('perspective-viewer perspective-viewer-datagrid')
  const root = g?.shadowRoot ?? g
  const row = [...(root?.querySelectorAll('tbody tr') ?? [])].find((tr) => /BTC/.test(tr.textContent ?? ''))
  return [...(row?.querySelectorAll('td') ?? [])].map((td) => getComputedStyle(td).color)
})
const greenish = (c) => { const m = /(\d+), (\d+), (\d+)/.exec(c ?? ''); return !!m && +m[2] > +m[1] + 40 && +m[2] > +m[3] }
ok(colors.length >= 3 && !greenish(colors[0]) && !greenish(colors[2]), `笔数、手续费不上绿色（${colors.slice(0, 3).join(' / ')}）`)
ok(/2[0-9][0-9], [0-9]{1,2}, |rgb\(2[0-9][0-9], [0-9]{2,3}, [0-9]{2,3}\)/.test(colors[1] ?? '') && !greenish(colors[1]), `净盈亏是亏的，红色（${colors[1]}）`)
// "自己分组"打开 Perspective 的拖列面板
await p.locator('[data-pv-custom]').click()
const opened = await until(async () => p.evaluate(() => document.querySelector('perspective-viewer')?.hasAttribute('settings')), 8000)
ok(!!opened, '"自己分组"打开拖列面板')
const panelTxt = await p.evaluate(() => { const v = document.querySelector('perspective-viewer'); return [...(v?.shadowRoot?.querySelectorAll('*') ?? [])].map((e) => getComputedStyle(e, '::before').content).filter((c) => c && c !== 'none' && c !== 'normal').join(' ') })
ok(/分组/.test(panelTxt) && /排序/.test(panelTxt) && !/Group By|订购/.test(panelTxt), `拖列面板是我们写的中文（${panelTxt.slice(0, 60)}）`)
ok(await p.locator('[data-pv-custom]').getAttribute('aria-pressed') === 'true', '按钮显示为按下')
await sleep(800)
await p.screenshot({ path: `${OUT}/pv-04-custom.png` })
await p.locator('[data-pv-custom]').click()
const closed = await until(async () => p.evaluate(() => !document.querySelector('perspective-viewer')?.hasAttribute('settings')), 8000)
ok(!!closed, '再点一下收起')
await p.locator('[data-pv-preset="when"]').click()
const whenTxt = await until(async () => p.evaluate(() => {
  const g = document.querySelector('perspective-viewer perspective-viewer-datagrid')
  const t = (g?.shadowRoot ?? g)?.textContent ?? ''
  return /周[一二三四五六日]/.test(t) ? t : null
}), 15000)
ok(!!whenTxt, '"按星期看赢亏"：按星期分组')
await sleep(600)
await p.screenshot({ path: `${OUT}/pv-02-when.png` })
// 刷新后还是这个布局
await p.reload()
await p.waitForSelector('.rv-head')
await p.locator('.rv-head .seg button', { hasText: '自由透视' }).click()
const after = await until(async () => p.evaluate(() => {
  const g = document.querySelector('perspective-viewer perspective-viewer-datagrid')
  const t = (g?.shadowRoot ?? g)?.textContent ?? ''
  return t.length > 20 ? t : null
}), 45000)
ok(/周[一二三四五六日]/.test(after ?? ''), '刷新后还是上次的布局')
const over = await p.evaluate(() => document.documentElement.scrollWidth - innerWidth)
ok(over <= 1, `不横向滚动（${over}）`)

// 手机
const m = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true, locale: 'zh-CN', storageState: await c.storageState() })
const mp = await m.newPage()
mp.on('pageerror', (e) => errs.push(String(e)))
await mp.goto(BASE + '/?page=review&tab=pivot')
await until(async () => mp.evaluate(() => !!document.querySelector('perspective-viewer perspective-viewer-datagrid')), 45000)
await sleep(1500)
const mover = await mp.evaluate(() => document.documentElement.scrollWidth - innerWidth)
ok(mover <= 1, `手机不横向滚动（${mover}）`)
await mp.screenshot({ path: `${OUT}/pv-03-phone.png` })

ok(errs.length === 0, `页面没有报错（${errs.slice(0, 2).join(' | ')}）`)
console.log(`\n${pass} 通过，${fail} 失败`)
await browser.close()
process.exit(fail ? 1 : 0)
