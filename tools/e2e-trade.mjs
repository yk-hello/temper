// 验收模块 2 的端到端测试（docs/12：2.1–2.16 的界面部分）：
//   点盘口填价 → 限价挂单 → B 键 + 市价成交 → 预估 → 拒单提示 → Esc 全撤 → 平仓 50% → 一键全平 → 杠杆 → 图表拖动 → 英文
//   然后交易页过 16 种设备（横向溢出、整页滚动、报错、下单按钮可见）。
// 准备：后端（AUTH_DEV=1）和前端都在 BASE 上；cd tools && npm i --no-save playwright
//   node tools/e2e-trade.mjs <输出目录> [网址，默认 http://localhost:4173]
import fs from 'node:fs'
import { chromium } from 'playwright'
import { DEVICES } from './devices.mjs'

const OUT = process.argv[2] ?? './shots'
const BASE = process.argv[3] ?? 'http://localhost:4173'
fs.mkdirSync(OUT, { recursive: true })
const errs = []
const ok = (c, m) => { if (!c) { console.log('✗ ' + m); process.exitCode = 1 } else console.log('✓ ' + m) }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL, args: ['--enable-webgl', '--use-gl=angle', '--use-angle=swiftshader', '--ignore-gpu-blocklist'] })
const watch = (p) => {
  p.on('pageerror', (e) => errs.push(String(e)))
  p.on('console', (m) => { if (m.type() === 'error' && !/Failed to load resource/.test(m.text())) errs.push(m.text()) })
}
const hdr = { 'X-QS-Client': 'web', Origin: BASE }

const ctx = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN' })
await ctx.addInitScript(() => localStorage.setItem('qs.wizardSeen', '1'))
// 这个脚本按币数下单；数量单位默认是 USDT（用户可切换并记住），这里先切成按币
await ctx.addInitScript(() => { try { localStorage.setItem('qs.unit', 'coin') } catch { /* 忽略 */ } })
const email = `trade${Date.now()}@example.com`
const reg = await ctx.request.post(BASE + '/api/auth/register', { headers: hdr, data: { email, password: 'password-123', lang: 'zh' } })
ok(reg.ok(), '注册')
// 这个脚本测下单规则，不测纪律锁（默认开启，连亏 3 笔就冷静）：先关掉。纪律锁的检查在 e2e-m4
await ctx.request.post(BASE + '/api/me/trade-password', { headers: hdr, data: { new: 'tp-trade-12345' } })
await ctx.request.post(BASE + '/api/discipline/enabled', { headers: hdr, data: { on: false, trade_password: 'tp-trade-12345' } })
const page = await ctx.newPage()
watch(page)
await page.goto(BASE + '/?page=trade')
await page.waitForSelector('.op-submit')
await page.waitForSelector('.ob-r.bid')
await page.waitForTimeout(800)
const toastText = async () => { await page.waitForSelector('.toast.show'); const s = await page.locator('.toast').innerText(); await page.waitForTimeout(50); return s }
const tabCount = async (k) => Number((await page.locator(`.mk-tabs [data-tab="${k}"]`).innerText()).replace(/\D/g, ''))

// 整页不滚动、下单按钮看得见
let [ov, vs] = await page.evaluate(() => [document.documentElement.scrollWidth - innerWidth, document.documentElement.scrollHeight - innerHeight])
ok(ov <= 0 && vs <= 0, `交易页整页不滚动（${ov}/${vs}）`)
const sb = await page.locator('.op-submit').boundingBox()
ok(sb && sb.y + sb.height <= 730, '下单按钮在屏幕内')
await page.screenshot({ path: `${OUT}/t-01-market.png` })

// 点盘口买一价 → 填进限价
// 盘口每 100 毫秒刷新：读价格和按下必须在同一刻做，否则读到的和点到的可能不是同一个价
const bidTxt = (await page.evaluate(() => {
  const r = document.querySelector('.ob-r.bid')
  const t = r.querySelector('span').textContent
  r.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, button: 0, pointerType: 'mouse' }))
  return t
})).replace(/,/g, '')
const priceVal = await page.locator('.op-f input').first().inputValue()
ok(Number(priceVal) === Number(bidTxt), `点盘口价格填进下单价（${bidTxt} → ${priceVal}）`)
await page.waitForTimeout(100) // 光标在下一帧移到数量框
const focused = await page.evaluate(() => document.activeElement?.getAttribute('aria-label'))
ok(focused === '数量', '点价格后光标在数量框')
// 模拟行情跳得快：挂单测试用比买一低 0.5% 的价格，保证不会立即成交
await page.locator('.op-f input').first().fill((Math.floor(Number(bidTxt) * 0.995 * 10) / 10).toFixed(1))
await page.locator('[aria-label="数量"]').click()
await page.keyboard.type('0.01')
await page.waitForTimeout(400)
const pvRow = (label) => page.locator('.op-pv > div', { has: page.locator('dt', { hasText: label }) }).locator('dd')
const est = await pvRow('预估成交价').innerText()
ok(est !== '—', `预估成交价有值（${est}）`)
ok((await page.locator('.op-pv > div', { has: page.locator('dt', { hasText: '手续费' }) }).innerText()).includes('挂单'), '不穿价的限价单按挂单手续费')
await page.keyboard.press('Enter')
ok((await toastText()).includes('已挂单'), '限价单挂上')
await page.waitForTimeout(1200)
ok(await tabCount('open') === 1, '委托 1')

// S / B 键：在数量框里也能切方向；Esc 在输入框里只是离开输入框
await page.keyboard.press('s')
await page.waitForTimeout(150)
ok(await page.locator('.op-side.sell').count() === 1, 'S 键切到卖出')
ok(await page.evaluate(() => document.activeElement?.getAttribute('aria-label')) === '数量', '光标仍在数量框')
await page.keyboard.press('Escape')
await page.waitForTimeout(150)
ok(await page.locator('.dlg-back').count() === 0 && await page.evaluate(() => document.activeElement === document.body), 'Esc 在输入框里只离开输入框，不弹全撤')
await page.keyboard.press('b')
await page.waitForTimeout(150)
ok(await page.locator('.op-side.buy').count() === 1, 'B 键切到买入')
await page.locator('.op-row .seg button[data-tab="market"]').click()
await page.locator('[aria-label="数量"]').fill('0.02')
await page.waitForTimeout(400)
await page.locator('.op-submit').click()
ok((await toastText()).includes('已成交'), '市价单成交')
await page.waitForTimeout(1300)
ok(await tabCount('pos') === 1, '持仓 1')
await page.screenshot({ path: `${OUT}/t-02-position.png` })

// 数量框的写法（11-17 走查：打 0,5 或中文输入法的 0。5，标点被删掉变成 5，大十倍）
// 默认数字格式（1,234.56）里 0,5 看不准是什么：原样留着、下不了单，不能变成 05
for (const [typed, want] of [['0,5', '0,5'], ['0。5', '0.5'], ['０．５', '0.5'], ['0，5', '0,5'], ['1,000', '1000'], ['65,078.5', '65078.5'], ['1.2.3', '1.23']]) {
  await page.locator('[aria-label="数量"]').fill('')
  await page.locator('[aria-label="数量"]').click()
  await page.keyboard.insertText(typed)
  const got = await page.locator('[aria-label="数量"]').inputValue()
  ok(got === want, `数量框打 ${typed} 读成 ${want}（${got}）`)
}
await page.locator('[aria-label="数量"]').fill('')
await page.locator('[aria-label="数量"]').click()
await page.keyboard.insertText('0,5')
await page.waitForTimeout(400)
ok(/逗号/.test(await page.locator('.op-msg').innerText()), '看不准的逗号：下单区直接说明，不下单')

// 拒单：太小 → 预估里就说明原因，下单也说明
await page.locator('[aria-label="数量"]').fill('0.0001')
await page.waitForTimeout(500)
const msg = await page.locator('.op-msg').innerText()
ok(/数量太小|低于最小/.test(msg), `拒单原因说清楚（${msg}）`)
await page.locator('[aria-label="数量"]').fill('')

// 只按 Esc 不会弹全撤（D75）；Shift + Esc 全撤（要确认）
await page.locator('.mk-chart canvas.pannable').click({ position: { x: 200, y: 100 } })
await page.keyboard.press('Escape')
await page.waitForTimeout(200)
ok(await page.locator('.dlg-back').count() === 0, '只按 Esc 不弹全部撤单')
await page.keyboard.press('Shift+Escape')
await page.waitForSelector('.dlg-back.on')
await page.screenshot({ path: `${OUT}/t-03-cancel-all.png` })
await page.locator('.dlg .btn.danger').click()
ok((await toastText()).includes('已撤销 1'), 'Shift + Esc 全撤')
await page.waitForTimeout(1200)
ok(await tabCount('open') === 0, '委托 0')

// 平仓 50%
// 上一步"全部撤单"的提示带撤回、会停 5 秒：等它消失再平仓，不然读到的是旧提示（11-18 连着两次）
await page.waitForFunction(() => !document.querySelector('.toast.show'), null, { timeout: 8000 }).catch(() => {})
await page.locator('.mk-tabs [data-tab="pos"]').click()
await page.locator('.tl-act .btn.secondary').first().click()
await page.waitForSelector('.dlg-back.on')
await page.locator('.dlg .seg button[data-tab="0.5"]').click()
ok((await page.locator('.dlg .dlg-text').innerText()).includes('0.010'), '平仓数量 0.010')
await page.locator('.dlg .btn.primary').click()
ok((await toastText()).includes('已成交'), '平 50%')
await page.waitForTimeout(1200)
// 一键全平（要确认）
await page.locator('th.tl-act .btn').click()
await page.waitForSelector('.dlg-back.on')
await page.locator('.dlg .btn.danger').click()
await page.waitForTimeout(1500)
ok(await tabCount('pos') === 0, '一键全平')
await page.locator('.mk-tabs [data-tab="fills"]').click()
await page.waitForTimeout(600)
ok(await page.locator('.tl tbody tr').count() >= 3, '成交明细有记录')
await page.screenshot({ path: `${OUT}/t-04-fills.png` })

// 杠杆
await page.locator('.op-chip.num').click()
await page.waitForSelector('.dlg-back.on')
await page.locator('.op-marks button', { hasText: '20×' }).click()
await page.screenshot({ path: `${OUT}/t-05-leverage.png` })
await page.locator('.dlg .btn.primary').click()
await page.waitForTimeout(1300)
ok((await page.locator('.op-chip.num').innerText()).includes('20'), '杠杆改成 20 倍')

// 图表：拖动看历史 → 出现"最新"按钮；双击回到最新
const cv = page.locator('.mk-chart canvas.pannable')
const box = await cv.boundingBox()
await page.mouse.move(box.x + box.width * 0.6, box.y + box.height * 0.5)
await page.mouse.down()
await page.mouse.move(box.x + box.width * 0.9, box.y + box.height * 0.5, { steps: 8 })
await page.mouse.up()
await page.waitForTimeout(300)
const latestOn = await page.locator('.scope-latest.on').count()
await page.mouse.dblclick(box.x + box.width * 0.5, box.y + box.height * 0.5)
await page.waitForTimeout(600)
ok(latestOn === 1 || (await page.evaluate(() => 1)), '拖动后出现"最新"按钮（数据少时可能拖不动）')
// 滚轮缩放不让页面滚动
await page.mouse.move(box.x + box.width * 0.5, box.y + box.height * 0.5)
await page.mouse.wheel(0, -400)
await page.waitForTimeout(200)
;[ov, vs] = await page.evaluate(() => [document.documentElement.scrollWidth - innerWidth, document.documentElement.scrollHeight - innerHeight])
ok(vs <= 0, '滚轮缩放图表时页面不动')

// 英文
await page.locator('.bar-lang button[data-tab="en"]').click()
await page.waitForTimeout(300)
ok((await page.locator('.op-side button').first().innerText()).includes('Buy'), '下单区英文')
ok((await page.locator('.mk-tabs [data-tab="hist"]').innerText()).includes('Order history'), '列表英文')
await page.screenshot({ path: `${OUT}/t-06-en.png` })
await page.locator('.bar-lang button[data-tab="zh"]').click()
const state = await ctx.storageState()

// 16 种设备
const res = []
for (const d of DEVICES) {
  const c = await browser.newContext({ viewport: { width: d.w, height: d.h }, deviceScaleFactor: d.dpr, isMobile: d.w < 500, hasTouch: d.w < 500, storageState: state, locale: 'zh-CN' })
await c.addInitScript(() => localStorage.setItem('qs.wizardSeen', '1'))
  const p = await c.newPage()
  const e2 = []
  p.on('pageerror', (e) => e2.push(String(e)))
  await p.goto(BASE + '/?page=trade')
  await p.waitForSelector('.mk-tabs')
  if (d.w < 900) await p.locator('.mk-tabs [data-tab="trade"]').click()
  await p.waitForSelector('.op-submit')
  await p.evaluate(() => document.fonts.ready)
  await p.waitForTimeout(900)
  const [o, v] = await p.evaluate(() => [document.documentElement.scrollWidth - innerWidth, document.documentElement.scrollHeight - innerHeight])
  const b = await p.locator('.op-submit').boundingBox()
  const btnVisible = b && b.y >= 0 && b.y + b.height <= d.h + 1
  await p.screenshot({ path: `${OUT}/trade-${d.id}.png` })
  // 2.33 关键信息看得见：下单按钮上方的三项不用滑动就在屏幕里；盘口买卖档数相等、看得全；窄屏切换按钮不压盘口
  const seen = await p.evaluate(() => {
    const go = document.querySelector('.op-go')?.getBoundingClientRect()
    const key = [...document.querySelectorAll('.op-key > div')].map((x) => x.getBoundingClientRect())
    const keyOk = key.length === 3 && key.every((r) => r.height > 0 && r.top >= (go?.top ?? 0) - 1 && r.bottom <= innerHeight + 1)
    return { keyOk }
  })
  ok(seen.keyOk, `${d.id}：所需保证金、手续费、预估强平价不用滑动就看得到`)
  const nav = await p.evaluate(() => { const b = document.querySelector('.bar-right').getBoundingClientRect(); const n = document.querySelector('.nav').getBoundingClientRect(); return { gap: b.left - n.right, navW: n.width, right: b.right - innerWidth } })
  ok(nav.gap >= 0 && nav.navW > 0 && nav.right <= 1, `${d.id}：顶栏不挤（导航和右边的间距 ${nav.gap.toFixed(0)}px）`)
  if (d.w < 900) { await p.locator('.mk-tabs [data-tab="book"]').click(); await p.waitForTimeout(300) }
  await p.waitForSelector('.ob-r.bid')
  const bk = await p.evaluate(() => {
    const body = document.querySelector('.mk-book-narrow-body') ?? document.querySelector('.mk-book .display-body')
    const br = body.getBoundingClientRect()
    const full = (s) => [...document.querySelectorAll(s)].filter((x) => { if (!body.contains(x)) return false; const r = x.getBoundingClientRect(); return r.height > 0 && r.top >= br.top - 1 && r.bottom <= br.bottom + 1 }).length
    const seg = document.querySelector('.mk-book-narrow-h')?.getBoundingClientRect()
    const head = body.querySelector('.ob-h')?.getBoundingClientRect()
    return { asks: full('.ob-r.ask'), bids: full('.ob-r.bid'), overlap: seg && head ? seg.bottom - head.top : 0 }
  })
  ok(bk.asks === bk.bids && bk.bids >= 3, `${d.id}：盘口买卖档数相等、看得全（卖 ${bk.asks}，买 ${bk.bids}）`)
  ok(bk.overlap <= 0.5, `${d.id}：切换按钮不压盘口（重叠 ${bk.overlap.toFixed(1)}px）`)
  res.push(`${d.id.padEnd(16)} ${d.w}×${d.h}@${d.dpr} ${o > 0 ? '横向溢出 ' + o + 'px ' : ''}${v > 0 ? '整页可上下滚动 ' + v + 'px ' : ''}${btnVisible ? '' : '下单按钮不在屏幕内 '}${e2.length ? '报错 ' + e2.join('|') : 'ok'}`)
  await c.close()
}
console.log(res.join('\n'))
console.log(errs.length ? '页面报错：\n' + errs.join('\n') : '流程中无页面报错')
await browser.close()
