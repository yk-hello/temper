// 链上页的检查（12 的 V.15–V.20）。只对着测试环境（模拟持仓，ONCHAIN=sim）跑。
//   node tools/e2e-chain.mjs <截图目录> <网址>
import { chromium } from 'playwright'
import { DEVICES } from './devices.mjs'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const newPage = async (opts = {}) => {
  const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN', timezoneId: 'Asia/Taipei', ...opts })
  await c.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1'); localStorage.setItem('qs.iv', '1h') })
  const p = await c.newPage()
  p.errs = []
  p.on('pageerror', (e) => p.errs.push(String(e)))
  return p
}

const p = await newPage()
await p.goto(BASE + '/?page=market&tab=chain')
await p.waitForSelector('.lp-heat', { timeout: 15000 })
const api = (path) => p.evaluate(async (u) => { const r = await fetch(u); return { s: r.status, b: await r.json().catch(() => null) } }, path)
let m
for (let i = 0; i < 30; i++) {
  m = await api('/api/onchain/liqmap?coin=BTC&range=0.2&bins=200')
  if (m.b?.cycle > 0 && m.b?.coverage > 0) break
  await p.waitForTimeout(1000)
}
ok(m.s === 200 && m.b.cycle > 0, `扫描有结果（${m.b.addresses_scanned} 个地址，第 ${m.b.cycle} 轮）`)
ok(m.b.coverage > 0 && m.b.coverage <= 1, `接口给出覆盖率（${(m.b.coverage * 100).toFixed(0)}%）`)
ok(m.b.bins[0].lo < m.b.center * 0.85 && m.b.bins[m.b.bins.length - 1].hi > m.b.center * 1.15, '强平地图覆盖现价上下 20%')
const pnl = await api('/api/onchain/pnl?coin=BTC')
ok(pnl.s === 200 && pnl.b.buckets.length === 8, '盈亏分布 8 组')
const sumB = pnl.b.buckets.reduce((a, b) => a + b.long_value, 0)
ok(Math.abs(sumB - (pnl.b.long_up + pnl.b.long_dn)) < 1, `多单每组加起来等于合计（${Math.round(sumB)}）`)
const ev = await api('/api/onchain/events?coin=BTC&min=1&n=50')
ok(ev.b.length > 0 && ev.b.every((e) => e.lag_ms >= 0), `动作带延迟（${ev.b.length} 条）`)
ok(new Set(ev.b.map((e) => e.kind)).size >= 2, '有多种动作')
const wh = await api('/api/onchain/whales?coin=BTC&n=20')
ok(wh.b.length > 0 && wh.b[0].value >= wh.b[wh.b.length - 1].value, '大额持仓按金额排序')

// 界面
await p.reload()
await p.waitForSelector('.cn-grid', { timeout: 15000 })
await p.waitForTimeout(2500)
const read = await p.locator('.cn-wrap .heat-read').innerText()
ok(/上方最近强平/.test(read) && /下方最近强平/.test(read), `顶部写上下最近的强平：${read.replace(/\s+/g, ' ').slice(0, 70)}`)
ok(/覆盖\s*\d+%/.test(read), '顶部写覆盖率')
ok(/个地址/.test(read), '写扫描了多少地址')
ok(await p.locator('.cn-map canvas').count() === 1, '有强平地图')
const rows = await p.locator('.cn-table tbody tr').count()
ok(rows > 0, `大额持仓表有 ${rows} 行`)
const heads = await p.locator('.cn-table th').allInnerTexts()
ok(['方向', '金额', '开仓价', '浮盈', '强平价', '地址历史盈亏', '地址'].every((h) => heads.includes(h)), `表头齐：${heads.join(' ')}`)
const link = await p.locator('.cn-addr').first().getAttribute('href')
ok(link?.startsWith('https://app.hyperliquid.xyz/explorer/address/0x'), '地址能去 Hyperliquid 浏览器')
ok(await p.locator('.cn-ev').count() > 0, '有动作流')
ok(/≤\d+(秒|分)/.test(await p.locator('.cn-ev').first().innerText()), '每条动作写明数据延迟')
ok(await p.locator('.cn-pnl-row').count() === 8, '盈亏分布 8 行')
ok(/多单 \d+% 浮亏，空单 \d+% 浮亏/.test(await p.locator('.cn-pnl .cn-head').innerText()), '盈亏分布写结论')
ok(!/大户|聪明钱/.test(await p.locator('.cn-wrap').innerText()), '不写大户、聪明钱（D64）')
await p.screenshot({ path: `${OUT}/chain-ov.png` })
// 悬停读数
const box = await p.locator('.cn-map canvas').boundingBox()
await p.mouse.move(box.x + box.width * 0.4, box.y + box.height * 0.3)
await p.waitForTimeout(200)
await p.screenshot({ path: `${OUT}/chain-hover.png` })
ok(p.errs.length === 0, `没有报错（${p.errs[0] ?? ''}）`)

// 动作流每条只占一行（英文两个词也不换行）
ok(await p.evaluate(() => [...document.querySelectorAll('.cn-ev')].slice(0, 10).every((e) => e.getBoundingClientRect().height <= 24 && [...e.children].every((c) => c.getBoundingClientRect().height <= 20))), '动作流每条一行')
// 切币
await p.locator('.lp-heat [role=radio], .lp-heat .seg button').filter({ hasText: 'ETH' }).first().click()
await p.waitForTimeout(1200)
ok(await p.locator('.cn-table tbody tr').count() > 0, '切到 ETH 有数据')

// V.19 行情图上标出动作：K 线打开链上时取动作数据
const q2 = await newPage()
const evResp = q2.waitForResponse((r) => r.url().includes('/api/onchain/events') && r.url().includes('min=500000'), { timeout: 15000 })
await q2.goto(BASE + '/?page=trade&i=BTC-PERP')
ok((await evResp).status() === 200, 'K 线取大额动作')
await q2.waitForTimeout(1500)
await q2.screenshot({ path: `${OUT}/chain-kline.png` })
ok(q2.errs.length === 0, `K 线没有报错（${q2.errs[0] ?? ''}）`)
await q2.context().close()

// 英文
await p.evaluate(() => localStorage.setItem('qs.lang', 'en'))
await p.reload()
await p.waitForSelector('.cn-grid', { timeout: 15000 })
await p.waitForTimeout(1500)
const en = await p.locator('.cn-wrap').innerText()
ok(!/[一-鿿]/.test(en), `英文没有中文：${en.replace(/\s+/g, ' ').slice(0, 60)}`)
await p.evaluate(() => localStorage.setItem('qs.lang', 'zh'))

// 窄屏分标签，16 种设备
for (const d of DEVICES) {
  const q = await newPage({ viewport: { width: d.w, height: d.h }, deviceScaleFactor: d.dpr, isMobile: d.w < 500, hasTouch: d.w < 500 })
  await q.goto(BASE + '/?page=market&tab=chain')
  await q.waitForSelector('.cn-grid', { timeout: 15000 })
  await q.waitForTimeout(700)
  const narrow = d.w < 1100
  if (narrow) {
    for (const k of ['大额持仓', '动作流', '盈亏分布', '强平地图']) await q.locator('.cn-tabs button, .cn-tabs [role=radio]').filter({ hasText: k }).first().click()
  }
  const r = await q.evaluate(() => {
    const c = document.querySelector('.cn-map')?.getBoundingClientRect()
    return { down: document.documentElement.scrollHeight - innerHeight, over: document.documentElement.scrollWidth - innerWidth, h: c?.height ?? 0, w: c?.width ?? 0 }
  })
  ok(r.down <= 0 && r.over <= 0 && r.h > 100 && r.w > 200 && q.errs.length === 0, `${d.name}：整页不滚，不溢出（${r.down}/${r.over}/${Math.round(r.w)}×${Math.round(r.h)}）`)
  if (d.w === 920 || d.w < 400) await q.screenshot({ path: `${OUT}/chain-dev-${d.w}.png` })
  await q.context().close()
}

console.log(`\n通过 ${pass}，失败 ${fail}`)
await browser.close()
process.exit(fail ? 1 : 0)
