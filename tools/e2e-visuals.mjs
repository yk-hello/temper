// 可视化 5、B、A、11、7 的检查：跨所矩阵和价差带、经济事件、系统控制台、开屏 3D 地形。
// 用法：node e2e-visuals.mjs <截图目录> <前端地址>。只读，不注册，不下单。不要对着 8080 跑 run_all，这个脚本本身不写数据。
import { createRequire } from 'module'
import fs from 'fs'
const require = createRequire(import.meta.url)
const { chromium } = require('playwright-core')
const OUT = process.argv[2] ?? 'shots/visuals'
const BASE = process.argv[3] ?? 'http://127.0.0.1:4173'
fs.mkdirSync(OUT, { recursive: true })
let bad = 0, good = 0
const ok = (c, s) => { if (c) { good++; console.log('✓', s) } else { bad++; console.log('✗', s) } }

const b = await chromium.launch({ channel: process.env.PW_CHANNEL ?? 'msedge', headless: true, args: ['--enable-webgl', '--use-angle=d3d11', '--ignore-gpu-blocklist', '--enable-unsafe-swiftshader'] })
const ctx = await b.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN', timezoneId: 'Asia/Taipei' })
await ctx.addInitScript(() => { try { localStorage.setItem('qs.wizardSeen', '1') } catch {} })
const p = await ctx.newPage()
const errs = []
p.on('pageerror', (e) => errs.push(String(e).slice(0, 300)))
const fit = async (s) => {
  const o = await p.evaluate(() => ({ sw: document.documentElement.scrollWidth, cw: document.documentElement.clientWidth, sh: document.documentElement.scrollHeight, ch: document.documentElement.clientHeight }))
  ok(o.sw <= o.cw && o.sh <= o.ch, `${s}：整页不滚动（${o.sw}/${o.cw}，${o.sh}/${o.ch}）`)
}
const lit = (sel) => p.evaluate((q) => {
  const c = document.querySelector(q)
  if (!c) return -1
  const x = c.getContext('2d').getImageData(0, 0, c.width, c.height).data
  let n = 0
  for (let i = 3; i < x.length; i += 4) if (x[i] > 0) n++
  return n
}, sel)

// ---------- 跨所 ----------
await p.goto(`${BASE}/?page=market&tab=venues`)
await p.waitForSelector('.vn-tr[data-hot], .vn-tr:not(.vn-th)', { timeout: 15000 })
await p.waitForFunction(() => document.querySelectorAll('.vn-tr:not(.vn-th) .vn-c:not(.none)').length >= 24, null, { timeout: 30000 }).catch(() => {})
ok(await p.locator('.vn-tr:not(.vn-th)').count() === 6, '矩阵 6 个币')
ok(!/年化|保底|收益/.test((await p.locator('.vn-matrix').innerText()).replace('不是收益', '')), '跨所矩阵没有年化和收益字眼')
ok(/不是收益/.test(await p.locator('.vn-matrix').innerText()), '写明费率差不是收益')
ok(await p.locator('.vn-th').innerText().then((s) => /Binance/.test(s) && /Hyperliquid/.test(s) && /Bybit/.test(s) && /OKX/.test(s)), '4 家交易所都在表头')
ok(await p.locator('.vn-tr:not(.vn-th) .vn-c:not(.none)').count() === 24, '24 个格子都有数')
const tip = await p.locator('.vn-tr:not(.vn-th) .vn-c:not(.none)').first().getAttribute('title')
ok(/每 \d+ 小时结算/.test(tip ?? ''), `格子能看到这一期的费率（${tip}）`)
await p.locator('.vn-tr:not(.vn-th)').nth(1).click()
await p.waitForTimeout(600)
ok(/ETH/.test(await p.locator('.vn-band h2').innerText()), '点一行，价差带跟着换币')
await p.locator('.vn-band [role=radio], .vn-band button').filter({ hasText: 'BTC' }).first().click()
await p.waitForTimeout(600)
ok(/BTC/.test(await p.locator('.vn-band h2').innerText()), '上面的按钮也能换币')
await p.waitForFunction(async () => { const r = await fetch('/api/venues/spread?coin=BTC'); const d = await r.json(); return d.t.length >= 2 }, null, { timeout: 30000, polling: 2000 }).catch(() => {})
await p.waitForTimeout(10500)
ok(await lit('.vn-chart canvas') > 100, '价差带画出来了')
ok(/基点/.test(await p.locator('.vn-band .vn-say').innerText()), '图上面有一句结论')
await fit('跨所 1536')
await p.screenshot({ path: `${OUT}/venues.png` })
await p.setViewportSize({ width: 790, height: 690 })
await p.waitForTimeout(500)
await fit('跨所 790')
ok(await p.locator('.vn-tr:not(.vn-th)').last().isVisible(), '790 宽矩阵最后一行还在')
await p.screenshot({ path: `${OUT}/venues-790.png` })
await p.setViewportSize({ width: 1536, height: 730 })

// ---------- 系统 ----------
await p.goto(`${BASE}/?page=market&tab=system`)
await p.waitForSelector('.sy-map canvas')
await p.waitForTimeout(3000)
const sys = await p.evaluate(async () => (await fetch('/api/system')).json())
ok(sys.lines.length > 0 && sys.lines[0].rate > 0, `后端有消息速率（${sys.lines[0]?.venue} ${sys.lines[0]?.rate?.toFixed(0)}/秒）`)
ok(sys.db && sys.db.ping_ms >= 0, '数据库延迟有数')
ok(sys.heat === 6, '频谱录制 6 个币')
ok(await lit('.sy-map canvas') > 500, '拓扑图画出来了')
ok(/已运行/.test(await p.locator('.sy-strip').innerText()), '底部有运行时间')
const a = await p.locator('.sy-map canvas').screenshot()
await p.waitForTimeout(700)
const c = await p.locator('.sy-map canvas').screenshot()
ok(!a.equals(c), '光点在动')
await fit('系统 1536')
await p.screenshot({ path: `${OUT}/system.png` })
await p.setViewportSize({ width: 790, height: 690 })
await p.waitForTimeout(500)
await fit('系统 790')
await p.setViewportSize({ width: 1536, height: 730 })

// 地形页去掉了：3D 地形只在开屏（D87），开屏的检查在后面

// ---------- 经济事件 ----------
const cal = await p.evaluate(async () => (await fetch('/api/calendar')).json())
ok(cal.events.length >= 3 && cal.events.some((e) => e.vol?.n >= 3), `日历接口有事件（${cal.events.length}）`)
await ctx.addInitScript(() => { try { localStorage.removeItem('qs.ind') } catch {} })
await p.goto(`${BASE}/?page=trade&i=BTC-PERP`)
await p.waitForSelector('.mk-chart canvas', { timeout: 20000 })
await p.waitForTimeout(2500)
const line = await p.locator('.mk-event').innerText().catch(() => '')
ok(/后/.test(line) && /议息/.test(line) && /平均振幅/.test(line), `图上方写着下一个事件（${line.replace(/\n/g, ' ')}）`)
await p.locator('[data-testid=ind] button, button:has-text("指标")').first().click()
await p.waitForTimeout(200)
await p.locator('label:has-text("经济事件") input').click()
await p.waitForTimeout(500)
ok(await p.locator('.mk-event').count() === 0, '指标菜单能关掉经济事件')
await p.locator('label:has-text("经济事件") input').click()
await p.waitForTimeout(500)
ok(await p.locator('.mk-event').count() === 1, '再打开又出现')
await p.keyboard.press('Escape')
await p.mouse.click(5, 300)

// ---------- 介绍页：频谱分析仪 ----------
{
  const brightness = () => p.evaluate(() => {
    const c = document.querySelector('[data-analyzer]')
    const x = c.getContext('2d').getImageData(0, 0, c.width, c.height).data
    let n = 0
    for (let i = 0; i < x.length; i += 4) if (x[i] + x[i + 1] + x[i + 2] > 60) n++
    return n / (x.length / 4)
  })
  await ctx.clearCookies()
  await p.goto(`${BASE}/?page=about`)
  await p.waitForSelector('[data-analyzer]', { timeout: 10000 })
  await p.waitForFunction(() => document.querySelector('[data-ab-price]').textContent !== '—', null, { timeout: 15000 })
  await p.waitForTimeout(1800)
  ok(await brightness() > .03, `介绍页不用登录，光柱亮起来了（${(await brightness()).toFixed(3)}）`)
  const a0 = await p.locator('.ab-screen').screenshot()
  await p.waitForTimeout(600)
  ok(!a0.equals(await p.locator('.ab-screen').screenshot()), '光柱在动')
  const c0 = await p.locator('[data-ab-clock]').innerText()
  await p.waitForTimeout(1100)
  ok(/^\d\d:\d\d:\d\d$/.test(c0) && c0 !== await p.locator('[data-ab-clock]').innerText(), `时钟走到秒、不显示毫秒（${c0}；D111）`)
  const price0 = await p.locator('[data-ab-price]').innerText()
  await p.keyboard.press('2')
  await p.waitForFunction((x) => document.querySelector('[data-ab-price]').textContent !== x, price0, { timeout: 8000 }).catch(() => {})
  ok(await p.locator('[data-ch=ETH][aria-pressed=true]').count() === 1, '按 2 切到 ETH')
  await p.waitForTimeout(1200)
  ok(await brightness() > .03, '切币后光柱重新亮起来')
  await p.locator('[data-ch=BTC]').click()
  await p.waitForFunction(() => document.querySelector('[data-ab-price]').textContent !== '—', null, { timeout: 8000 })
  await p.waitForTimeout(1200)
  ok(await p.locator('.ab-spec').innerText().then((s) => /条\/秒/.test(s) && /个币/.test(s)), '规格行有实时数字')
  ok(!/年化|收益率|保底|稳赚/.test(await p.locator('.ab').innerText()), '介绍页没有收益类字眼')
  // 介绍页是产品里的正常页面：随时切到交易，再切回来
  await p.locator('.nav a').filter({ hasText: '交易' }).click()
  await p.waitForSelector('.mk-chart canvas', { timeout: 15000 })
  ok(true, '从介绍页切到交易')
  await p.locator('.nav a').filter({ hasText: '介绍' }).click()
  await p.waitForSelector('[data-analyzer]', { timeout: 8000 })
  await p.waitForFunction(() => document.querySelector('[data-ab-price]').textContent !== '—', null, { timeout: 8000 })
  ok(true, '从交易切回介绍页，光柱接着跑')
  await p.locator('[data-ch=SOL]').click()
  await p.locator('[data-ab-trade]').click()
  await p.waitForSelector('.mk-chart canvas', { timeout: 15000 })
  ok(/i=SOL-PERP/.test(p.url()), '介绍页一键去交易当前的币')
  await p.goto(`${BASE}/?page=about`)
  await p.waitForSelector('[data-analyzer]')
  await p.locator('[data-ch=BTC]').click()
  await p.waitForTimeout(800)
  for (const [w, h] of [[1536, 730], [1024, 768], [790, 690], [393, 852], [1000, 1000], [1280, 400], [2560, 1080]]) {
    await p.setViewportSize({ width: w, height: h })
    await p.waitForTimeout(350)
    await fit(`介绍页 ${w}×${h}`)
    const box = await p.locator('.ab-screen').boundingBox()
    ok(box.height >= 110, `介绍页 ${w}×${h}：屏幕还有 ${Math.round(box.height)} 高`)
    await p.screenshot({ path: `${OUT}/about-${w}x${h}.png` })
  }
  await p.setViewportSize({ width: 1536, height: 730 })
}

// ---------- 首页：全屏地形、开场、设备档位 ----------
{
  const setForce = (v) => p.evaluate((x) => { sessionStorage.clear(); if (x) localStorage.setItem('qs.tierForce', x); else localStorage.removeItem('qs.tierForce') }, v)
  await ctx.clearCookies()
  await p.goto(`${BASE}/?page=about`)
  await setForce('high')
  await p.goto(`${BASE}/`)
  await p.waitForSelector('[data-hero]', { timeout: 15000 })
  ok(await p.locator('.hero-bar.on').count() === 0, '首页：开场时界面还没出来')
  await p.waitForSelector('.hero-bar.on', { timeout: 15000 }).catch(() => {})
  ok(await p.locator('.hero-bar.on').count() === 1, '首页：开场结束，界面出现')
  await p.waitForTimeout(500)
  ok(/\d/.test(await p.locator('[data-hero-price]').innerText()), '首页：大号价格是实时的')
  const h0 = await p.locator('.hero-gl').screenshot()
  await p.waitForTimeout(700)
  ok(!h0.equals(await p.locator('.hero-gl').screenshot()), '首页：镜头在动')
  ok(await p.evaluate(() => sessionStorage.getItem('qs.heroSeen')) === '1', '首页：记住已经看过开场')
  await p.reload()
  await p.waitForSelector('.hero-bar.on', { timeout: 2500 }).catch(() => {})
  ok(await p.locator('.hero-bar.on').count() === 1, '首页：同一次浏览不重复开场')
  await p.evaluate(() => sessionStorage.clear())
  await p.reload()
  await p.waitForSelector('[data-hero]')
  await p.waitForTimeout(300)
  await p.mouse.click(200, 200)
  await p.waitForSelector('.hero-bar.on', { timeout: 5000 }).catch(() => {})
  ok(await p.locator('.hero-bar.on').count() === 1, '首页：点一下跳过开场')
  await p.waitForTimeout(600)
  // 在画面上扫，读到就算过。无头浏览器是软件渲染，一帧可能很久，要等画面刷新再读，镜头也在转，不能先记位置
  let got = ''
  const t0Read = Date.now()
  scan: for (const fy of [.3, .4, .5, .6, .25, .35, .45, .55]) {
    for (let fx = .08; fx < 1; fx += .07) {
      await p.mouse.move(1536 * fx, 730 * fy)
      await p.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))))
      got = await p.locator('.hero-read').innerText().catch(() => '')
      if (got || Date.now() - t0Read > 90000) break scan
    }
  }
  ok(/买单|卖单/.test(got), `首页：鼠标移到山上能读数（${got || '扫完没读到'}）`)
  for (const [w, h] of [[1536, 730], [790, 690], [393, 852], [844, 390], [1000, 1000], [2560, 1080]]) {
    await p.setViewportSize({ width: w, height: h })
    await p.waitForTimeout(350)
    await fit(`首页 ${w}×${h}`)
    const b = await p.locator('[data-hero-price]').boundingBox()
    ok(b && b.x + b.width <= w && b.y >= 0, `首页 ${w}×${h}：价格没有被裁`)
    const en = await p.locator('[data-hero-enter]').boundingBox()
    ok(en && en.y + en.height <= h && en.x >= 0, `首页 ${w}×${h}：进入按钮看得见`)
  }
  await p.setViewportSize({ width: 1536, height: 730 })
  await p.locator('[data-hero-enter]').click()
  await p.waitForSelector('.mb-map canvas', { timeout: 8000 })
  ok(/page=market/.test(p.url()), '首页：点进入，压平后到方块图')
  // 档位
  await setForce('mid')
  await p.goto(`${BASE}/`)
  await p.waitForSelector('.hero-bar.on', { timeout: 15000 }).catch(() => {})
  ok(await p.locator('[data-hero][data-tier=mid]').count() === 1, '首页：中档还是 3D')
  await p.locator('[data-home-switch] [data-tab="spec"]').click()
  await p.waitForSelector('[data-analyzer]', { timeout: 10000 })
  ok(true, '首页：手动切到频谱页')
  ok(await p.evaluate(() => localStorage.getItem('qs.tierForce')) === 'low', '首页：手动切换记住了')
  await p.locator('[data-home-switch] [data-tab="3d"]').click()
  await p.waitForSelector('[data-hero]', { timeout: 10000 })
  ok(true, '介绍页：能切回 3D')
  await setForce(null)
  const rc = await b.newContext({ viewport: { width: 1280, height: 720 }, locale: 'zh-CN', reducedMotion: 'reduce' })
  const rp = await rc.newPage()
  await rp.goto(`${BASE}/`)
  await rp.waitForSelector('.mb-map canvas', { timeout: 15000 }).catch(() => {})
  ok(await rp.locator('.mb-map canvas').count() === 1 && await rp.locator('[data-hero]').count() === 0, '首页：减少动态效果时直接方块图')
  await rc.close()
}

// ---------- 未登录和空状态：不留大黑块 ----------
await ctx.clearCookies()
await p.goto(`${BASE}/?page=risk`)
await p.waitForSelector('.out', { timeout: 8000 })
ok(/假设/.test(await p.locator('.out-r .cn-head').innerText()), '风险页未登录：压力测试写明是假设')
ok(await lit('.out-r canvas') > 500, '风险页未登录：示例图画出来了')
const before = await p.locator('.out-r .cn-head').innerText()
const sb = await p.locator('.out-r canvas').boundingBox()
await p.mouse.click(sb.x + sb.width * 0.3, sb.y + sb.height * 0.5)
await p.waitForTimeout(200)
ok(before !== await p.locator('.out-r .cn-head').innerText(), '风险页未登录：拖图能看到账户剩多少')
await fit('风险页未登录')
await p.goto(`${BASE}/?page=review`)
await p.waitForSelector('.rv-hint', { timeout: 8000 })
ok(/登录/.test(await p.locator('.rv-hint').innerText()), '复盘页未登录：进回放练习，提示登录')
await fit('复盘页未登录')
await p.goto(`${BASE}/?page=trade&i=BTC-PERP`)
await p.waitForSelector('.mk-chart canvas', { timeout: 20000 })
ok(await p.locator('.mk[data-empty]').count() === 1, '交易页没有持仓：底部收起来，图更大')
const cb = await p.locator('.mk-chart').boundingBox()
ok(cb.height > 730 * 0.55, `交易页没有持仓：图占一半以上高度（${Math.round(cb.height)}）`)

// ---------- 英文：没有漏翻的代号 ----------
const noCjk = (txt) => !/[\u3000-\u303f\u3400-\u9fff\uff00-\uffef]/.test(txt.replace(/中/g, ''))
await p.goto(`${BASE}/?page=market&tab=venues`)
await p.waitForSelector('.vn-tr')
await p.locator('.bar-lang button, .bar-lang [role=radio]').filter({ hasText: 'EN' }).first().click()
await p.waitForTimeout(800)
for (const tab of ['Venues', 'System']) {
  await p.locator('.lp-head [role=radio], .lp-head button').filter({ hasText: tab }).first().click()
  await p.waitForTimeout(1500)
  const txt = await p.locator('body').innerText()
  ok(!/\b(vn|sy|tr|ev|ov)\.[a-zA-Z]+/.test(txt), `英文 ${tab}：没有露出代号`)
  ok(!/[一-鿿]/.test(txt.replace(/中/g, '')), `英文 ${tab}：没有中文`)
}

await p.locator('.nav a').filter({ hasText: 'About' }).click()
await p.waitForSelector('[data-analyzer]')
await p.waitForTimeout(1500)
ok(noCjk(await p.locator('body').innerText()), '英文介绍页：没有中文和全角标点')
await p.goto(`${BASE}/?page=hero`)
await p.waitForSelector('.hero-bar.on', { timeout: 15000 }).catch(() => {})
await p.waitForTimeout(500)
ok(noCjk(await p.locator('body').innerText()), '英文首页：没有中文和全角标点')
await p.goto(`${BASE}/?page=about`)
await p.waitForSelector('[data-analyzer]')
await p.waitForTimeout(800)
ok(/Trade BTC/.test(await p.locator('.ab-ch').innerText()), '英文介绍页：能一键去交易')
ok(errs.length === 0, `没有页面错误${errs.length ? '：' + errs.slice(0, 3).join(' | ') : ''}`)
console.log(`\n结果：${good} 过，${bad} 失败`)
await b.close()
process.exit(bad ? 1 : 0)
