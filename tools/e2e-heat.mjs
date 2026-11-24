// 流动性频谱的检查（12 的 V.5–V.10）。只对着测试环境（模拟盘口）跑，不登录、不下单。
//   node tools/e2e-heat.mjs <截图目录> <网址>
import { chromium } from 'playwright'
import { DEVICES } from './devices.mjs'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge', args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'] })
const newPage = async (opts = {}) => {
  const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN', timezoneId: 'Asia/Taipei', ...opts })
  await c.addInitScript(() => localStorage.setItem('qs.wizardSeen', '1'))
  const p = await c.newPage()
  p.errs = []
  p.on('pageerror', (e) => p.errs.push(String(e)))
  return p
}
const dbg = (p) => p.evaluate(() => {
  const d = document.querySelector('.heat-canvas')?.__heat
  return d ? { frames: d.frames, rows: d.rows, dt: d.dt, events: d.events, gaps: d.gaps, follow: d.follow, view: d.view } : null
})
// 显卡画布上真的有东西：不是全黑
const lit = (p) => p.evaluate(() => {
  const gl = document.querySelector('.surface canvas')
  const c = document.createElement('canvas')
  c.width = gl.width; c.height = gl.height
  const x = c.getContext('2d')
  x.drawImage(gl, 0, 0)
  const d = x.getImageData(0, 0, c.width, c.height).data
  let n = 0
  for (let i = 0; i < d.length; i += 4) if (Math.max(d[i], d[i + 1], d[i + 2]) > 24) n++
  return n / (d.length / 4)
})

// ---------- 接口 ----------
const p = await newPage()
await p.goto(BASE + '/?page=market&tab=heat')
await p.waitForSelector('.heat-canvas', { timeout: 15000 })
const api = (path) => p.evaluate(async (u) => { const r = await fetch(u); return { s: r.status, b: await r.json().catch(() => null) } }, path)
let st
for (let i = 0; i < 30; i++) {
  st = await api('/api/heat/status')
  if (st.b?.instruments?.every((x) => x.ready && x.width > 0)) break
  await p.waitForTimeout(1000)
}
ok(st.b.instruments.length === 6 && st.b.instruments.every((x) => x.ready), `6 个币都拼好了盘口（${st.b.instruments.filter((x) => x.ready).length}）`)
ok(st.b.keep_days === 30, '保留 30 天')
ok(st.b.instruments.every((x) => x.full[0] > 0 && x.full[1] > x.full[0]), '报告完整盘口的覆盖范围')
const now = Date.now()
const h = await api(`/api/heat/history?instrument=BTC-PERP&from=${now - 120_000}&to=${now}`)
ok(h.s === 200 && h.b.rows > 0 && h.b.dt >= 1000 && h.b.mids.some((m) => m > 0), `历史接口有数据（${h.b.rows} 行，${h.b.dt} 毫秒一列）`)
ok(h.b.q_lo === 100 && h.b.q_hi === 50000000, '接口写明了亮度的换算范围')
const nr = await api('/api/heat/history?instrument=ADA-PERP')
ok(nr.s === 404, '没录的币返回 404')
const wide = await api(`/api/heat/history?instrument=BTC-PERP&from=${now - 3 * 86400_000}&to=${now}`)
ok(wide.s === 200 && wide.b.t0 >= now - 24 * 3600_000 - 3600_000, '一次最多取 24 小时')

// ---------- V.7 市场页 ----------
await p.waitForTimeout(3000)
let a = await dbg(p)
await p.waitForTimeout(3000)
let b = await dbg(p)
ok(b.frames - a.frames >= 15, `实时一层每 100 毫秒一列（3 秒 ${b.frames - a.frames} 列）`)
ok(b.follow === true, '默认跟着最新')
ok(await lit(p) > 0.0005, `画布上有挂单（亮的像素 ${(await lit(p) * 100).toFixed(1)}%）`)
const note = await p.locator('.heat-note').innerText()
ok(/完整盘口/.test(note), `底部写明覆盖范围（V.10）：${note.slice(0, 60)}`)
ok(/开始录制/.test(note), '写明从什么时候开始录')
await p.screenshot({ path: `${OUT}/heat-ov.png` })
// 顶部结论：标签 + 数字；没有大单时写明白
let read = ''
for (let i = 0; i < 25; i++) {
  read = await p.locator('.heat-read').innerText()
  if (/上方大单\s*[\d,]/.test(read) || /下方大单\s*[\d,]/.test(read)) break
  await p.waitForTimeout(2000)
}
ok(/上方大单/.test(read) && /下方大单/.test(read), `顶部写上下大单：${read.replace(/\s+/g, ' ').slice(0, 70)}`)
ok(/[\d,]+(\.\d+)?\s*[\d.]+[万亿K]?/.test(read), '大单有价格和金额')
ok(/5 分钟主动买卖/.test(read) && /\d+%/.test(read), '写最近 5 分钟主动买卖比例')
ok(/大单|主动/.test(await p.locator('.hr-cell').first().getAttribute('title') ?? ''), '指上去有白话说明')
const dw = await p.evaluate(() => document.querySelector('.heat-canvas').__heat.walls)
ok(dw.every((w) => w.usd >= 100000 && w.age >= 30), `价格尺上的大单都够大、挂够久（${dw.length} 个）`)
// 亮度标尺：底部有上下限；金额下限提高黑点
const noteScale = await p.locator('.hn-scale').innerText()
ok(/\d/.test(noteScale), `底部有亮度标尺：${noteScale.replace(/\s+/g, ' ')}`)
const tone0 = await p.evaluate(() => document.querySelector('.heat-canvas').__heat.tone)
await p.locator('[data-ct=heat-floor]').click()
await p.waitForSelector('.ct-pop')
ok(await p.locator('.ct-pop .ct-item').count() === 5, '金额下限 5 档')
await p.locator('.ct-pop .ct-item').nth(4).click()
await p.waitForTimeout(400)
const tone1 = await p.evaluate(() => document.querySelector('.heat-canvas').__heat.tone)
ok(tone1.black > tone0.black, `金额下限提高后，小单不亮（${tone0.black.toFixed(2)} → ${tone1.black.toFixed(2)}）`)
ok(await p.evaluate(() => localStorage.getItem('qs.heatFloor')) === '5000000', '金额下限记在这台设备上')
await p.locator('[data-ct=heat-floor]').click()
await p.locator('.ct-pop .ct-item').nth(0).click()
// 缩放时亮度标尺不变
const b0 = await p.locator('.heat-canvas').boundingBox()
await p.mouse.move(b0.x + b0.width * 0.4, b0.y + b0.height * 0.4)
await p.mouse.wheel(0, 200)
await p.waitForTimeout(600)
const tone2 = await p.evaluate(() => document.querySelector('.heat-canvas').__heat.tone)
ok(Math.abs(tone2.black - tone0.black) < 0.02, '缩放时亮度标尺不变')

// 切币
await p.locator('.lp-heat .seg button, .lp-heat [role=radio]').filter({ hasText: 'ETH' }).first().click()
await p.waitForTimeout(2500)
const e = await dbg(p)
ok(e && e.frames > 0, '切到 ETH 后有实时数据')
ok(p.errs.length === 0, `没有报错（${p.errs[0] ?? ''}）`)

// 交互：滚轮缩放、拖动、双击
await p.goto(BASE + '/?page=trade&i=BTC-PERP&chart=heat')
await p.waitForSelector('.heat-canvas')
await p.waitForTimeout(2500)
const box = await p.locator('.heat-canvas').boundingBox()
const cx = box.x + box.width * 0.4, cy = box.y + box.height * 0.4
const v0 = (await dbg(p)).view
await p.mouse.move(cx, cy)
await p.mouse.wheel(0, 300)
await p.waitForTimeout(500)
const v1 = (await dbg(p)).view
ok(v1.tb - v1.ta > (v0.tb - v0.ta) * 1.3, '滚轮缩放时间')
await p.mouse.move(box.x + box.width - 40, cy)
await p.mouse.wheel(0, -200)
await p.waitForTimeout(400)
const v2 = (await dbg(p)).view
ok(v2.pb - v2.pa < v1.pb - v1.pa, '在价格尺上滚动缩放价格')
await p.mouse.move(cx, cy)
await p.mouse.wheel(0, -900) // 先放大：录得还不够长时，看全部数据就没有地方可拖
await p.waitForTimeout(400)
await p.mouse.down()
await p.mouse.move(cx + 120, cy + 10, { steps: 5 })
await p.mouse.up()
await p.waitForTimeout(300)
ok((await dbg(p)).follow === false, '拖走后不再跟着最新')
await p.mouse.dblclick(cx, cy)
await p.waitForTimeout(600)
ok((await dbg(p)).follow === true, '双击回到最新')
await p.mouse.move(cx, cy)
await p.waitForTimeout(300)
await p.screenshot({ path: `${OUT}/heat-trade-hover.png` })

// 时长按钮（11-23 起是分段按钮，原来是刻度尺）
await p.locator(`.head-iv .seg button[data-tab="${30 * 60_000}"]`).first().click()
await p.waitForTimeout(800)
const vs = (await dbg(p)).view
ok(Math.abs((vs.tb - vs.ta) - 30 * 60_000) < 5 * 60_000, `时长按钮切时长（现在 ${Math.round((vs.tb - vs.ta) / 60000)} 分钟）`)
ok(await p.evaluate(() => Number(localStorage.getItem('qs.heatSpan'))) === 30 * 60_000, '时长记在这台设备上')
await p.waitForTimeout(1500)
const longer = await dbg(p)
ok(longer.dt >= 1000, `看得远时一列变宽（${longer.dt} 毫秒）`)

// V.9 叠加开关
await p.locator('[data-ct=heat-layers]').click()
await p.waitForSelector('.ct-pop')
ok(await p.locator('.ct-pop .ct-row').count() === 3, '没登录时叠加菜单 3 项（我的仓位没有数据，不显示）')
await p.locator('.ct-pop input[type=checkbox]').nth(0).uncheck()
ok(await p.evaluate(() => JSON.parse(localStorage.getItem('qs.heatLayers')).events) === false, '关掉疑似行为会记住')
await p.locator('.ct-pop input[type=checkbox]').nth(0).check()
await p.locator('.heat-note').click()
await p.waitForTimeout(200)
ok(await p.locator('.ct-pop').count() === 0, '点别处关掉菜单')

// D83：这几条之前脚本都查不出来，是用户看出来的
await p.goto(BASE + '/?page=market&tab=heat')
await p.waitForSelector('.heat-canvas')
await p.waitForTimeout(4000)
const info = (pg = p) => pg.evaluate(() => { const d = document.querySelector('.heat-canvas').__heat; return { view: d.view, yAuto: d.yAuto, follow: d.follow, rows: d.rows, chips: d.chips, histStart: d.histStart, histEnd: d.histEnd } })
const lineFits = async () => {
  const i = await info()
  const h = await api(`/api/heat/history?instrument=BTC-PERP&from=${Math.floor(i.view.ta)}&to=${Math.ceil(Math.min(i.view.tb, Date.now()))}&cols=600&rows=100`)
  const ms = h.b.mids.filter((m) => m > 0)
  if (!ms.length) return { ok: true, lo: 0, hi: 0, i }
  const lo = Math.min(...ms), hi = Math.max(...ms), tol = hi * 0.0003
  return { ok: i.view.pa <= lo + tol && i.view.pb >= hi - tol, lo, hi, i }
}
let lf = await lineFits()
ok(lf.ok && lf.i.yAuto, `折线整条都在图内（线 ${lf.lo}–${lf.hi}，图 ${lf.i.view.pa.toFixed(1)}–${lf.i.view.pb.toFixed(1)}）`)
const bx = await p.locator('.heat-canvas').boundingBox()
const px = bx.x + bx.width * 0.45, py = bx.y + bx.height * 0.45
const dragBy = async (dx, dy = 0, x = px, y = py) => { await p.mouse.move(x, y); await p.mouse.down(); await p.mouse.move(x + dx, y + dy, { steps: 8 }); await p.mouse.up() }
await dragBy(260, 70)
await p.waitForTimeout(900)
let st2 = await info()
ok(st2.yAuto && !st2.follow, '在图上斜着拖：价格范围还是自动，不再跟着最新')
ok(await p.locator('.heat-ctl .scope-latest.on').count() === 1, '离开最新后出现"回到最新"按钮')
lf = await lineFits()
ok(lf.ok, '拖走以后折线还在图内')
await p.locator('.heat-ctl .scope-latest.on').click()
await p.waitForTimeout(700)
st2 = await info()
ok(st2.follow && await p.locator('.heat-ctl .scope-latest.on').count() === 0, '点"回到最新"回来，按钮消失')
// 反复左右滑：手里的数据一直盖住视野，不会滑到未来
let bad = 0, future = 0
for (let r = 0; r < 4; r++) {
  await dragBy(300); await p.waitForTimeout(1300)
  await dragBy(300); await p.waitForTimeout(1300)
  await dragBy(-300); await p.waitForTimeout(1300)
  await dragBy(-300); await p.waitForTimeout(1800)
  const i = await info(), sp = i.view.tb - i.view.ta, now = Date.now()
  if (i.view.tb > now + sp * 0.26) future++
  const since = st.b.first ?? 0
  if (i.rows === 0 || i.histStart > Math.max(i.view.ta, since) + 5000 || i.histEnd < Math.min(i.view.tb, now - 60_000) - 5000) bad++
}
ok(bad === 0, `反复左右滑 4 轮，手里的数据一直盖住视野（不够 ${bad} 轮）`)
ok(future === 0, '滑不到未来太远')
await p.screenshot({ path: `${OUT}/heat-pan-after.png` })
await p.mouse.dblclick(px, py)
await p.waitForTimeout(600)
// 在价格尺上拖才上下动
const v3 = (await info()).view
await dragBy(0, 60, bx.x + bx.width - 30, py)
await p.waitForTimeout(300)
st2 = await info()
ok(!st2.yAuto && Math.abs(st2.view.pa - v3.pa) > 1e-6, '在价格尺上拖：上下移动，停止自动')
ok(await p.locator('.heat-ctl .scope-latest.on').count() === 1, '手动调过范围后出现"自动范围"按钮')
await p.locator('.heat-ctl .scope-latest.on').click()
await p.waitForTimeout(700)
st2 = await info()
lf = await lineFits()
ok(st2.yAuto && lf.ok, '点"自动范围"恢复，折线回到图内')
// 叠加项：开着就要看得见
const chipsOf = async (pg) => { for (let i = 0; i < 20; i++) { const c = (await info(pg)).chips; if (c.length) return c; await pg.waitForTimeout(1000) } return [] }
let chips0 = await chipsOf(p)
ok(chips0.length > 0, `链上强平在图外的带子里：${(chips0[0]?.text ?? '').slice(0, 40)}`)
ok(/链上强平/.test(await p.locator('.heat-note').innerText()), '图例写明链上强平的来源')
await p.locator('[data-ct=heat-layers]').click()
await p.waitForSelector('.ct-pop')
await p.locator('.ct-pop input[type=checkbox]').nth(2).check()
await p.locator('.heat-note').click()
await p.waitForTimeout(1500)
const chips1 = (await info()).chips
ok(chips1.length >= chips0.length, `打开大额持仓开仓价后带子里有它（${chips0.length} → ${chips1.length} 个）`)
await p.screenshot({ path: `${OUT}/heat-chips.png` })
const c0 = chips1[0]
await p.mouse.click(bx.x + (c0.x0 + c0.x1) / 2, bx.y + (c0.y0 + c0.y1) / 2)
await p.waitForTimeout(900)
st2 = await info()
ok(!st2.yAuto && st2.view.pa <= c0.p && st2.view.pb >= c0.p, `点带子里的价位，视野扩到 ${c0.p}`)
await p.screenshot({ path: `${OUT}/heat-chip-expanded.png` })
await p.locator('.heat-ctl .scope-latest.on').click()
await p.locator('[data-ct=heat-layers]').click()
await p.waitForSelector('.ct-pop')
await p.locator('.ct-pop input[type=checkbox]').nth(2).uncheck()
await p.locator('.ct-pop input[type=checkbox]').nth(1).uncheck()
await p.locator('.heat-note').click()
await p.waitForTimeout(500)
ok((await info()).chips.length === 0, '关掉叠加项，带子上就没有了')
await p.locator('[data-ct=heat-layers]').click()
await p.waitForSelector('.ct-pop')
await p.locator('.ct-pop input[type=checkbox]').nth(1).check()
await p.locator('.heat-note').click()
// 用户真实的窄窗口
const p790 = await newPage({ viewport: { width: 790, height: 690 } })
await p790.goto(BASE + '/?page=market&tab=heat')
await p790.waitForSelector('.heat-canvas')
await p790.waitForTimeout(3000)
const c790 = await chipsOf(p790)
const b790 = await p790.locator('.heat-canvas').boundingBox()
ok(c790.length > 0 && c790.every((c) => c.x0 >= 0 && c.x1 <= b790.width), `790 宽窗口也看得见链上强平（${c790.length} 个）`)
// 手机上直接点（之前没有移动过鼠标）也要生效
const ph = await newPage({ viewport: { width: 393, height: 760 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })
await ph.goto(BASE + '/?page=market&tab=heat')
await ph.waitForSelector('.heat-canvas')
await ph.waitForTimeout(3000)
const cPh = await chipsOf(ph)
const bPh = await ph.locator('.heat-canvas').boundingBox()
ok(cPh.length > 0, `手机上也看得见链上强平（${cPh.length} 个）`)
if (cPh.length) {
  await ph.touchscreen.tap(bPh.x + (cPh[0].x0 + cPh[0].x1) / 2, bPh.y + (cPh[0].y0 + cPh[0].y1) / 2)
  await ph.waitForTimeout(900)
  const sPh = await info(ph)
  ok(!sPh.yAuto && sPh.view.pa <= cPh[0].p && sPh.view.pb >= cPh[0].p, '手机上点一下带子，视野扩过去')
}
await ph.screenshot({ path: `${OUT}/heat-phone.png` })
await ph.context().close()
await p790.screenshot({ path: `${OUT}/heat-790.png` })
await p790.context().close()
// 完整盘口的范围跟着现价
const cov = await api('/api/heat/status')
ok(cov.b.instruments.every((x) => x.full[0] <= x.mid && x.mid <= x.full[1]), '6 个币的完整盘口范围都包住现价')

// 没录的币写明白
await p.goto(BASE + '/?page=trade&i=ADA-PERP&chart=heat')
await p.waitForSelector('.heat-out', { timeout: 15000 })
const out = await p.locator('.heat-out').innerText()
ok(/只录制/.test(out) && /BTC/.test(out), `没录的币写明白：${out.slice(0, 30)}`)

// 切回 K 线和英文
await p.goto(BASE + '/?page=trade&i=BTC-PERP&chart=heat')
await p.waitForSelector('.heat-canvas')
await p.evaluate(() => localStorage.setItem('qs.lang', 'en'))
await p.reload()
await p.waitForSelector('.heat-canvas')
await p.waitForTimeout(1500)
const en = await p.locator('.heat-note').innerText()
ok(!/[一-鿿]/.test(en), `英文没有中文：${en.slice(0, 50)}`)
await p.evaluate(() => localStorage.setItem('qs.lang', 'zh'))

// V.8 模拟盘口里的大单被撤掉，应该能报出来（等最多 90 秒）
await p.goto(BASE + '/?page=trade&i=BTC-PERP&chart=heat')
await p.waitForSelector('.heat-canvas')
let evs = 0
for (let i = 0; i < 18; i++) {
  const nowT = Date.now()
  const r = await api(`/api/heat/history?instrument=BTC-PERP&from=${nowT - 600_000}&to=${nowT}`)
  evs = r.b.events.filter((x) => x.kind === 'spoof').length
  if (evs) {
    const x = r.b.events.find((y) => y.kind === 'spoof')
    ok(x.usd > 0 && x.ev.age >= 5 && x.ev.dist >= 0, `识别结果带判断依据（挂了 ${x.ev.age} 秒，离现价 ${x.ev.dist}%）`)
    break
  }
  await p.waitForTimeout(5000)
}
ok(evs > 0, `能识别出疑似撤单（${evs} 个）`)
await p.waitForTimeout(1200)
await p.screenshot({ path: `${OUT}/heat-events.png` })

// V.21 16 种设备
for (const d of DEVICES) {
  const q = await newPage({ viewport: { width: d.w, height: d.h }, deviceScaleFactor: d.dpr, isMobile: d.w < 500, hasTouch: d.w < 500 })
  await q.goto(BASE + (d.w < 900 ? '/?page=market&tab=heat' : '/?page=trade&i=BTC-PERP&chart=heat'))
  await q.waitForSelector('.heat-canvas', { timeout: 15000 })
  await q.waitForTimeout(700)
  const m = await q.evaluate(() => {
    const c = document.querySelector('.heat-canvas').getBoundingClientRect()
    return { down: document.documentElement.scrollHeight - innerHeight, over: document.documentElement.scrollWidth - innerWidth, w: c.width, h: c.height }
  })
  ok(m.down <= 0 && m.over <= 0 && m.w > 120 && m.h > 120 && q.errs.length === 0, `${d.name}：整页不滚，不溢出，图够大（${m.down}/${m.over}/${Math.round(m.w)}×${Math.round(m.h)}）`)
  if (d.w === 920 || d.w < 500 || d.name.includes('1080p')) await q.screenshot({ path: `${OUT}/heat-dev-${d.w}.png` })
  await q.context().close()
}

console.log(`\n通过 ${pass}，失败 ${fail}`)
await browser.close()
process.exit(fail ? 1 : 0)
