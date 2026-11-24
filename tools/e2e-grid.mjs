// 多图同屏（18 第 9.2 条）：交易页 K 线区分 2 格、4 格；小图换品种、换周期；点小图的品种名换成主图；刷新后还在；
// 窄屏不分格；标题栏放得下。不注册、不下单（只看行情），但用的是测试环境的地址。
// 用法：node tools/e2e-grid.mjs <截图目录> [http://127.0.0.1:4173]
import { chromium } from 'playwright'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const until = async (f, ms = 8000) => { const t0 = Date.now(); while (Date.now() - t0 < ms) { try { const v = await f(); if (v) return v } catch { /* 再试 */ } await sleep(150) } return null }

const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const errs = []
const page = async (vp, dsf = 1.25) => {
  const c = await browser.newContext({ viewport: vp, deviceScaleFactor: dsf, locale: 'zh-CN' })
  await c.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1') })
  const p = await c.newPage()
  p.on('pageerror', (e) => errs.push(String(e)))
  return p
}
const p = await page({ width: 1536, height: 730 })
await p.goto(BASE + '/?page=trade')
const head = p.locator('.mk-chart .display-head')
await head.waitFor()
await until(async () => (await p.locator('.mk-chart canvas').count()) > 0)
const pick = async (q, label) => { await q.locator('[data-ct="grid"]').click(); await q.locator('.ct-pop .ct-item', { hasText: label }).click(); await sleep(300) }
await p.locator('[data-ct="grid"]').click()
const items = p.locator('.ct-pop .ct-item')
ok((await items.count()) === 3, `标题栏"多图"里有一张、两张、四张（${(await items.allInnerTexts()).join(' / ')}）`)
ok(/一张/.test(await p.locator('.ct-pop .ct-item.on').innerText()), '默认一张')
await p.screenshot({ path: `${OUT}/grid-00-menu.png` })
await p.keyboard.press('Escape')

// 两张
await pick(p, '两张')
ok(!!(await until(async () => (await p.locator('.mg-2 [data-cell]').count()) === 1)), '两张：主图 + 一张小图')
const mini = p.locator('[data-cell="0"]')
ok(!!(await until(async () => (await mini.locator('canvas').count()) > 0)), '小图画出来了')
const sym0 = (await mini.locator('.mg-sym').innerText()).trim()
const main0 = (await p.locator('.mk-chart .display-title').innerText()).trim()
ok(sym0 !== '' && sym0 !== main0, `小图默认看别的品种（主图 ${main0}，小图 ${sym0}）`)
const over = await head.evaluate((el) => el.scrollWidth - el.clientWidth)
ok(over <= 1, `标题栏放得下（多 ${over}px）`)
await sleep(2500)
await p.screenshot({ path: `${OUT}/grid-01-two.png` })

// 换周期
await mini.locator('.mg-iv .sel-btn').click()
await p.locator('.sel-pop [role="option"]', { hasText: '1时' }).click()
await sleep(300)
ok(/1时/.test(await mini.locator('.mg-iv .sel-btn').innerText()), '小图换成 1 小时线')
// 换品种
await mini.locator('.mg-inst .sel-btn').click()
const opts = p.locator('.sel-pop [role="option"]')
const pickName = (await opts.allInnerTexts()).map((s) => s.trim()).find((s) => s !== sym0 && s !== main0)
await opts.filter({ hasText: pickName }).first().click()
ok(!!(await until(async () => (await mini.locator('.mg-sym').innerText()).trim() === pickName)), `小图换品种（${pickName}）`)
// 换成主图：主图和小图对调
await mini.locator('.mg-sym').click()
ok(!!(await until(async () => (await p.locator('.mk-chart .display-title').innerText()).trim() === pickName)), '点小图的品种名：它成了主图')
ok(!!(await until(async () => (await mini.locator('.mg-sym').innerText()).trim() === main0)), '原来的主图到小图里')

// 四张
await pick(p, '四张')
ok((await p.locator('.mg-head-main').count()) === 1 && /主图/.test(await p.locator('.mg-head-main').innerText()), '主图上面也有一行，写明是主图')
ok(!!(await until(async () => (await p.locator('.mg-4 [data-cell]').count()) === 3)), '四张：主图 + 三张小图')
const names = await p.locator('[data-cell] .mg-sym').allInnerTexts()
ok(new Set(names).size === 3, `三张小图各看一个品种（${names.join(' ')}）`)
const mainNow = (await p.locator('.mk-chart .display-title').innerText()).trim()
ok(!names.map((s) => s.trim()).includes(mainNow), `小图里没有和主图一样的（主图 ${mainNow}）`)
await until(async () => (await p.locator('.mg-4 canvas').count()) >= 4)
await sleep(2500)
await p.screenshot({ path: `${OUT}/grid-02-four.png` })

// 刷新还在
await p.reload()
ok(!!(await until(async () => (await p.locator('.mg-4 [data-cell]').count()) === 3)), '刷新后还是四张')
ok((await p.locator('[data-cell="0"] .mg-iv .sel-btn').innerText()).includes('1时'), '小图的周期也记住了')

// 切到别的图表类型：不分格
await p.locator('.mk-chart .seg button', { hasText: '阶梯' }).first().click().catch(() => {})
await sleep(400)
ok((await p.locator('.mg').count()) === 0, '阶梯线不分格')
await p.locator('.mk-chart .seg button', { hasText: 'K 线' }).first().click().catch(() => {})
await sleep(400)

// 1920 和 1280 宽
for (const [w, h] of [[1920, 1080], [1280, 720]]) {
  const q = await page({ width: w, height: h }, w === 1920 ? 1 : 1.5)
  await q.goto(BASE + '/?page=trade')
  await q.evaluate(() => localStorage.setItem('qs.grid.perp', JSON.stringify({ n: 4, cells: [] })))
  await q.reload()
  await until(async () => (await q.locator('.mg-4 canvas').count()) >= 4)
  await sleep(2000)
  const o = await q.locator('.mk-chart .display-head').evaluate((el) => el.scrollWidth - el.clientWidth)
  ok(o <= 1, `${w} 宽标题栏放得下（多 ${o}px）`)
  const hOver = await q.locator('.mg-head').first().evaluate((el) => el.scrollWidth - el.clientWidth)
  ok(hOver <= 1, `${w} 宽小图的标题放得下（多 ${hOver}px）`)
  await q.screenshot({ path: `${OUT}/grid-03-${w}.png` })
  await q.context().close()
}

// 窄屏：不分格，也没有选几张的按钮
const m = await page({ width: 390, height: 844 }, 3)
await m.goto(BASE + '/?page=trade')
await m.evaluate(() => localStorage.setItem('qs.grid.perp', JSON.stringify({ n: 4, cells: [] })))
await m.reload()
await sleep(2500)
ok((await m.locator('.mg').count()) === 0 && (await m.locator('[data-ct="grid"]').count()) === 0, '手机上不分格')

// 回到一张
await pick(p, '一张')
await sleep(300)
ok((await p.locator('.mg').count()) === 0, '回到一张')

ok(errs.length === 0, `页面没有报错（${errs.slice(0, 2).join(' | ')}）`)
console.log(`\n${pass} 通过，${fail} 失败`)
await browser.close()
process.exit(fail ? 1 : 0)
