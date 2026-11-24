// 订单流的检查（12 的 V.11–V.14）。只对着测试环境（模拟行情）跑，不登录、不下单。
//   node tools/e2e-flow.mjs <截图目录> <网址>
import { chromium } from 'playwright'
import { DEVICES } from './devices.mjs'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const newPage = async (opts = {}) => {
  const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN', timezoneId: 'Asia/Taipei', ...opts })
  await c.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1'); localStorage.setItem('qs.iv', '1m') })
  const p = await c.newPage()
  p.errs = []
  p.on('pageerror', (e) => p.errs.push(String(e)))
  return p
}

const p = await newPage()
await p.goto(BASE + '/?page=market&tab=flow')
await p.waitForSelector('.flow-wrap', { timeout: 15000 })
const api = (path) => p.evaluate(async (u) => { const r = await fetch(u); return { s: r.status, b: await r.json().catch(() => null) } }, path)

// 接口
const now = Date.now()
let fp
for (let i = 0; i < 20; i++) {
  fp = await api(`/api/flow/footprint?instrument=BTC-PERP&interval=60000&from=${now - 3600_000}&to=${Date.now()}`)
  if (fp.b?.bars?.length) break
  await p.waitForTimeout(2000)
}
ok(fp.s === 200 && fp.b.bars.length > 0 && fp.b.step > 0, `足迹接口有数据（${fp.b.bars.length} 根，细档位 ${fp.b.step}）`)
ok(fp.b.bars[0].l.every((x) => x.b >= 0 && x.s >= 0 && x.p > 0), '每档有价格和主动买卖量')
ok((await api('/api/flow/footprint?instrument=ADA-PERP&interval=60000&from=1&to=2')).s === 404, '没录的币返回 404')
ok((await api('/api/flow/footprint?instrument=BTC-PERP&interval=1000&from=1&to=2')).s === 400, '足迹最短 1 分钟')
const oi = await api('/api/flow/oi?instrument=BTC-PERP&interval=300000')
ok(oi.s === 200 && oi.b.points.length > 100, `持仓量拆解有数据（${oi.b.points.length} 点）`)
ok(['new_long', 'new_short', 'short_cover', 'long_close'].every((k) => oi.b.points.some((x) => x.kind === k)), '四种变化都有')
ok(oi.b.points.every((x) => (x.kind === 'new_long' ? x.delta > 0 && x.dp >= 0 : x.kind === 'new_short' ? x.delta > 0 && x.dp < 0 : x.kind === 'short_cover' ? x.delta < 0 && x.dp >= 0 : x.kind === 'long_close' ? x.delta < 0 && x.dp < 0 : true)), '方向和类型对得上')
const rt = await api('/api/flow/ratio?instrument=BTC-PERP')
ok(rt.s === 200 && rt.b.account.ratio > 0 && rt.b.top.ratio > 0, '多空比有数据')
let lq
for (let i = 0; i < 15; i++) {
  lq = await api('/api/flow/liquidations?instrument=BTC-PERP')
  if (lq.b.items.length) break
  await p.waitForTimeout(3000)
}
ok(lq.b.items.length > 0 && lq.b.items[0].usd > 0, `爆仓流有数据（${lq.b.items.length} 条）`)
ok(lq.b.h1.long + lq.b.h1.short > 0, '有 1 小时合计')

// 市场页
await p.waitForTimeout(2500)
const read = await p.locator('.flow-wrap .heat-read').innerText()
ok(/持仓量/.test(read) && /1 小时/.test(read), `顶部写持仓量：${read.replace(/\s+/g, ' ').slice(0, 80)}`)
ok(/主要是(新开多|新开空|空头回补|多头止损)/.test(read), '写主要是哪一种变化')
ok(/多空比/.test(read) && /\d\.\d\d/.test(read), '写多空比')
ok(/爆仓/.test(read), '写 1 小时爆仓')
ok(/推断/.test(await p.locator('.hr-cell').first().getAttribute('title') ?? ''), '指上去写明是推断的')
await p.waitForTimeout(1000)
ok(await p.locator('.fl-row').count() > 0, '右边有爆仓流')
const note = await p.locator('.flow-wrap .heat-note').innerText()
ok(/格内/.test(note) && /亮边/.test(note) && /持仓量变化/.test(note), '底部写清楚格子、亮边、副图的意思')
await p.screenshot({ path: `${OUT}/flow-ov.png` })
ok(p.errs.length === 0, `没有报错（${p.errs[0] ?? ''}）`)

// 切周期
await p.evaluate(() => localStorage.setItem('qs.flowIv', '1m'))
await p.reload()
await p.waitForSelector('.flow-wrap')
await p.waitForTimeout(2500)
await p.screenshot({ path: `${OUT}/flow-1m.png` })
// 滚轮能把格子放大到写数字，宽度单独记
const chart = await p.locator('.flow-chart canvas').first().boundingBox()
await p.mouse.move(chart.x + chart.width * 0.7, chart.y + chart.height * 0.3)
const w0 = await p.evaluate(() => localStorage.getItem('qs.fpBarW'))
await p.mouse.wheel(0, -300)
await p.waitForTimeout(300)
const w1 = await p.evaluate(() => Number(localStorage.getItem('qs.fpBarW')))
ok(w1 > 40 && w1 <= 200 && String(w1) !== w0, `足迹宽度单独记（${w1}）`)
ok(await p.evaluate(() => localStorage.getItem('qs.barW')) === null, '不动 K 线的宽度')
await p.waitForTimeout(400)
await p.screenshot({ path: `${OUT}/flow-zoom.png` })

// 交易页
await p.goto(BASE + '/?page=trade&i=BTC-PERP&chart=flow')
await p.waitForSelector('.flow-wrap', { timeout: 15000 })
await p.waitForTimeout(2000)
ok(await p.locator('.flow-chart canvas').count() > 0, '交易页图表能切到足迹')
await p.screenshot({ path: `${OUT}/flow-trade.png` })
// 秒线时切过来自动用 1 分钟
await p.evaluate(() => localStorage.setItem('qs.iv', '1s'))
await p.reload()
await p.waitForSelector('.flow-wrap')
await p.waitForTimeout(800)
ok(/1 分/.test(await p.locator('.mk-chart .display-sub').innerText()), '秒线时自动用 1 分钟')
await p.evaluate(() => localStorage.setItem('qs.iv', '1m'))
// 没录的币
await p.goto(BASE + '/?page=trade&i=ADA-PERP&chart=flow')
await p.waitForSelector('.flow-wrap', { timeout: 15000 })
await p.waitForTimeout(3000)
ok(/只录了/.test(await p.locator('.flow-wrap .heat-note').innerText()), '没录的币写明白')
ok(p.errs.length === 0, `切换后没有报错（${p.errs[0] ?? ''}）`)

// K 线的持仓量副图接上了数据
await p.goto(BASE + '/?page=trade&i=BTC-PERP')
await p.evaluate(() => { const c = JSON.parse(localStorage.getItem('qs.ind') ?? '{}'); c.oi = { on: true }; localStorage.setItem('qs.ind', JSON.stringify(c)); localStorage.setItem('qs.chartMode', 'candle'); localStorage.setItem('qs.iv', '5m') })
const oiResp = p.waitForResponse((r) => r.url().includes('/api/flow/oi'), { timeout: 15000 })
await p.reload()
const rr = await oiResp
ok(rr.status() === 200, 'K 线打开持仓量指标后取数据')

// 英文
await p.evaluate(() => localStorage.setItem('qs.lang', 'en'))
await p.goto(BASE + '/?page=market&tab=flow')
await p.waitForSelector('.flow-wrap')
await p.waitForTimeout(2500)
const en = await p.locator('.flow-wrap').innerText()
ok(!/[一-鿿]/.test(en), `英文没有中文：${en.replace(/\s+/g, ' ').slice(0, 60)}`)
await p.evaluate(() => localStorage.setItem('qs.lang', 'zh'))

// 16 种设备
for (const d of DEVICES) {
  const q = await newPage({ viewport: { width: d.w, height: d.h }, deviceScaleFactor: d.dpr, isMobile: d.w < 500, hasTouch: d.w < 500 })
  await q.goto(BASE + (d.w < 900 ? '/?page=market&tab=flow' : '/?page=trade&i=BTC-PERP&chart=flow'))
  await q.waitForSelector('.flow-wrap', { timeout: 15000 })
  await q.waitForTimeout(800)
  const m = await q.evaluate(() => {
    const c = document.querySelector('.flow-chart').getBoundingClientRect()
    return { down: document.documentElement.scrollHeight - innerHeight, over: document.documentElement.scrollWidth - innerWidth, w: c.width, h: c.height }
  })
  ok(m.down <= 0 && m.over <= 0 && m.w > 120 && m.h > 100 && q.errs.length === 0, `${d.name}：整页不滚，不溢出，图够大（${m.down}/${m.over}/${Math.round(m.w)}×${Math.round(m.h)}）`)
  if (d.w === 920 || d.w < 400) await q.screenshot({ path: `${OUT}/flow-dev-${d.w}.png` })
  await q.context().close()
}

console.log(`\n通过 ${pass}，失败 ${fail}`)
await browser.close()
process.exit(fail ? 1 : 0)
