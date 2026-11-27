// K 线指标和画线的检查（12 的 V.1–V.4）。不用登录，不下单，但只对着测试环境跑。
//   node tools/e2e-chart.mjs <截图目录> <网址>
import { chromium } from 'playwright'
import { DEVICES } from './devices.mjs'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const newPage = async (opts = {}) => {
  const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN', timezoneId: 'Asia/Taipei', ...opts })
  await c.addInitScript(() => localStorage.setItem('qs.wizardSeen', '1'))
  const p = await c.newPage()
  p.errs = []
  p.on('pageerror', (e) => p.errs.push(String(e)))
  return p
}
const shapes = (p) => p.evaluate(() => JSON.parse(localStorage.getItem('qs.shapes.BTC-PERP') ?? '[]'))
const open = async (p) => {
  await p.goto(BASE + '/?page=trade&i=BTC-PERP')
  await p.waitForSelector('.mk-chart canvas', { timeout: 15000 })
  await p.waitForTimeout(1500)
}
const box = (p) => p.locator('.mk-chart .display-body canvas').first().boundingBox()
const pickTool = async (p, k) => {
  await p.locator('[data-ct=draw]').click()
  await p.locator(`[data-tool=${k}]`).click()
}

const p = await newPage()
await open(p)

// V.1 指标
await p.locator('[data-ct=ind]').click()
await p.waitForSelector('.ct-pop')
await p.waitForTimeout(250)
ok(await p.locator('.ct-row').count() === 13, '指标菜单 12 项加经济事件')
await p.screenshot({ path: `${OUT}/ct-ind-menu.png` })
for (const i of [0, 2, 4, 5, 7, 8, 9]) await p.locator('.ct-row input[type=checkbox]').nth(i).check()
const cfg = await p.evaluate(() => JSON.parse(localStorage.getItem('qs.ind')))
ok(cfg.ma1.on && cfg.ema1.on && cfg.vwap.on && cfg.boll.on && cfg.rsi.on && cfg.macd.on && cfg.cvd.on, '勾选记在这台设备上')
await p.locator('.ct-row .ct-num').first().fill('5')
ok(await p.evaluate(() => JSON.parse(localStorage.getItem('qs.ind')).ma1.n) === 5, '改参数立刻生效')
await p.locator('.mk-chart .display-title').click() // 点别处（副标题在图表列窄时不显示，用标题）
await p.waitForTimeout(400)
ok(await p.locator('.ct-pop').count() === 0, '点别处关掉菜单')
await p.screenshot({ path: `${OUT}/ct-ind-on.png` })
await p.reload()
await p.waitForSelector('.mk-chart canvas')
ok(/指标 7/.test(await p.locator('[data-ct=ind]').innerText()), '刷新后还在')

// V.2 画线
await p.waitForTimeout(1500) // 图上方的事件行加载出来会把图往下推，等它到位再量坐标
const b = await box(p)
const px = (fx, fy) => [b.x + (b.width - 68) * fx, b.y + b.height * fy]
await pickTool(p, 'trend')
await p.mouse.move(...px(0.3, 0.2))
await p.mouse.down()
await p.mouse.move(...px(0.6, 0.3), { steps: 4 })
await p.mouse.up()
let s = await shapes(p)
ok(s.length === 1 && s[0].kind === 'trend', '拖一下画趋势线')
ok(await p.locator('[data-ct=draw].on').count() === 0, '画完工具自动退出')
await pickTool(p, 'hline')
await p.mouse.click(...px(0.5, 0.15))
s = await shapes(p)
ok(s.length === 2 && s[1].kind === 'hline', '点一下画水平线')
await pickTool(p, 'rect')
await p.mouse.click(...px(0.1, 0.25))
await p.mouse.move(...px(0.2, 0.32), { steps: 3 })
await p.waitForTimeout(80)
ok((await shapes(p)).length === 2, '只点一下时还没画完')
await p.mouse.click(...px(0.2, 0.32))
s = await shapes(p)
ok(s.length === 3 && s[2].kind === 'rect', '点两下画矩形')
await pickTool(p, 'fib')
await p.mouse.move(...px(0.7, 0.18))
await p.mouse.down()
await p.mouse.move(...px(0.85, 0.34), { steps: 4 })
await p.mouse.up()
ok((await shapes(p)).length === 4, '斐波那契回撤')
await pickTool(p, 'measure')
await p.mouse.move(...px(0.4, 0.3))
await p.mouse.down()
await p.mouse.move(...px(0.5, 0.2), { steps: 4 })
await p.mouse.up()
ok((await shapes(p)).length === 4, '测量不留在图上的存档里')
await p.waitForTimeout(150)
await p.screenshot({ path: `${OUT}/ct-shapes.png` })
await p.mouse.click(...px(0.92, 0.5))

// 拖动整条线：按线现在的位置去抓（画布的 data-shapes）。线按时间、价格记，画完到这里行情一动纵轴就缩放，
// 线在屏幕上会挪；原来按画的时候的位置去抓，挪了 5 像素就抓空（11-28 回归）
const before = (await shapes(p))[0]
const onScreen = JSON.parse((await p.locator('.mk-chart .display-body canvas').first().getAttribute('data-shapes')) ?? '[]').find((x) => x.id === before.id)
const b2 = await box(p)
const mid = onScreen ? [b2.x + (onScreen.x0 + onScreen.x1) / 2, b2.y + (onScreen.y0 + onScreen.y1) / 2] : px(0.45, 0.25)
await p.mouse.move(...mid)
await p.mouse.down()
await p.mouse.move(mid[0] + b2.width * 0.05, mid[1] + b2.height * 0.05, { steps: 4 })
await p.mouse.up()
const after = (await shapes(p))[0]
ok(after.a.t > before.a.t && after.b.t > before.b.t && after.a.p !== before.a.p, '拖动整条趋势线')
// 拖端点
const hl = (await shapes(p))[1]
await p.mouse.move(...px(0.5, 0.15))
await p.mouse.down()
await p.mouse.move(...px(0.5, 0.18), { steps: 3 })
await p.mouse.up()
ok((await shapes(p))[1].a.p !== hl.a.p, '上下拖水平线')
// 选中后按 Delete 删除
await p.keyboard.press('Delete')
s = await shapes(p)
ok(s.length === 3 && !s.some((x) => x.kind === 'hline'), '按 Delete 删除选中的线')
// 换周期后还在
await p.reload()
await p.waitForSelector('.mk-chart canvas')
await p.waitForTimeout(800)
ok((await shapes(p)).length === 3, '刷新后画线还在')
await p.locator('.head-iv').first().waitFor()
await p.keyboard.press('5')
await p.waitForTimeout(500)
ok((await shapes(p)).length === 3 && (await shapes(p))[0].a.t === after.a.t, '换周期后还在原来的时间和价格上')
// 清除
await p.locator('[data-ct=draw]').click()
await p.locator('.ct-link').click()
ok((await shapes(p)).length === 0, '一键清除这个币的画线')
// Esc 取消
await pickTool(p, 'trend')
await p.keyboard.press('Escape')
ok(await p.locator('[data-ct=draw].on').count() === 0, 'Esc 退出画线')
ok(p.errs.length === 0, `没有页面报错（${p.errs[0] ?? ''}）`)
await p.context().close()

// V.4 按钮样子的链接没有下划线
{
  const q = await newPage()
  await q.goto(BASE + '/?page=risk')
  await q.waitForSelector('.out .btn')
  ok(await q.locator('.out .btn.btn').evaluate((e) => getComputedStyle(e).textDecorationLine) === 'none', '风险页登录按钮没有下划线')
  ok(await q.locator('.bar-signup').evaluate((e) => getComputedStyle(e).textDecorationLine) === 'none', '顶栏注册按钮没有下划线')
  await q.context().close()
}

// V.3 窄屏和 16 种设备：工具不撑破标题栏、菜单不出屏幕
for (const d of DEVICES) {
  const q = await newPage({ viewport: { width: d.w, height: d.h }, deviceScaleFactor: d.dpr, isMobile: d.w < 500, hasTouch: d.w < 500 })
  await q.addInitScript(() => localStorage.setItem('qs.ind', JSON.stringify({ ma1: { on: true, n: 20 }, ema1: { on: true, n: 12 }, rsi: { on: true, n: 14 }, macd: { on: true, f: 12, s: 26, g: 9 } })))
  await q.goto(BASE + '/?page=trade&i=BTC-PERP')
  await q.waitForSelector('.mk-chart canvas', { timeout: 15000 })
  await q.waitForTimeout(600)
  await q.locator('[data-ct=ind]').click()
  await q.waitForTimeout(250)
  const m = await q.evaluate(() => {
    const pop = document.querySelector('.ct-pop')?.getBoundingClientRect()
    // 最后一行要真的点得到：没有被别的面板盖住，也没有被裁掉
    const rows = [...document.querySelectorAll('.ct-pop .ct-row')]
    const lr = rows[rows.length - 1]?.getBoundingClientRect()
    const hit = lr ? document.elementFromPoint(lr.left + lr.width / 2, lr.top + lr.height / 2) : null
    const lastOk = !!(hit && hit.closest('.ct-pop') && lr.bottom <= innerHeight)
    return { lastOk, down: document.documentElement.scrollHeight - innerHeight, over: document.documentElement.scrollWidth - innerWidth, popOut: pop ? pop.right > innerWidth + 1 || pop.left < -1 || pop.bottom > innerHeight + 1 : true }
  })
  ok(m.down <= 0 && m.over <= 0 && !m.popOut && m.lastOk && q.errs.length === 0, `${d.name}：整页不滚，不溢出，菜单在屏幕内且最后一行点得到（${m.down}/${m.over}/${m.popOut}/${m.lastOk}）`)
  if (d.id === 'win-1080-125' || d.w < 500) await q.screenshot({ path: `${OUT}/ct-dev-${d.id}.png` })
  await q.context().close()
}
{
  const q = await newPage({ viewport: { width: 920, height: 690 } })
  await q.addInitScript(() => localStorage.setItem('qs.ind', JSON.stringify({ ma1: { on: true, n: 20 }, ema1: { on: true, n: 12 }, boll: { on: true, n: 20, k: 2 }, rsi: { on: true, n: 14 }, macd: { on: true, f: 12, s: 26, g: 9 } })))
  await open(q)
  await q.screenshot({ path: `${OUT}/ct-920.png` })
  await q.context().close()
}

console.log(`\n通过 ${pass}，失败 ${fail}`)
await browser.close()
process.exit(fail ? 1 : 0)
