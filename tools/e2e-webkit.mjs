// iPhone、iPad 的 Safari 内核（WebKit）+ 触屏：所有 iPhone 机型按屏幕尺寸去重，竖屏、横屏各走一遍主要页面；
// 加上 iPhone Duo（苹果 2026 年的折叠屏）合上、打开、横竖四种状态，和用着用着打开 / 合上（2026-11-18，用户"iPhone 的折叠屏"）。
// iPhone Duo 的网页尺寸是按苹果公布的像素 ÷ 3 推算的（外屏 1398×2034、内屏 1878×2670），拿到真机以后核对一次 innerWidth / innerHeight。
// 查：横向溢出、页面报错、交易页下单按钮在屏幕里、输入框字号（iPhone 上小于 16px 的输入框一点进去整页会自动放大，下单区这类固定高度的布局会被撑乱）。
// 要先装 WebKit：npx playwright install webkit（约 60MB）。没装时跳过，不算失败。
// 用法：node e2e-webkit.mjs <截图目录> <前端地址>。会注册账号，不要对着 8080 跑。
import { createRequire } from 'module'
import { randomBytes } from 'crypto'
import fs from 'fs'
const require = createRequire(import.meta.url)
const { webkit, devices } = require('playwright')
const OUT = (process.argv[2] ?? 'shots') + '/webkit'
const BASE = process.argv[3] ?? 'http://127.0.0.1:4173'
if (/:8080/.test(BASE)) throw new Error('不要对着 8080 跑')
fs.mkdirSync(OUT, { recursive: true })
let bad = 0, good = 0
const ok = (c, s) => { if (c) { good++; console.log('✓', s) } else { bad++; console.log('✗', s) } }
const rnd = () => randomBytes(6).toString('hex')

let b
try { b = await webkit.launch() } catch (e) {
  console.log('✓ 跳过：这台电脑没装 WebKit（npx playwright install webkit）', String(e).split('\n')[0].slice(0, 100))
  console.log('\n通过 1，失败 0')
  process.exit(0)
}

// 所有 iPhone 按屏幕尺寸去重（竖屏），横屏用同一台的 landscape；再加 iPad mini
const seen = new Map()
for (const [name, d] of Object.entries(devices)) {
  if (!/^iPhone /.test(name) || /landscape/.test(name) || d.defaultBrowserType !== 'webkit') continue
  const k = `${d.viewport.width}x${d.viewport.height}`
  if (!seen.has(k)) seen.set(k, name)
}
const list = []
for (const name of seen.values()) list.push(name, `${name} landscape`)
list.push('iPad Mini', 'iPad Mini landscape')
// iPhone Duo：每种状态一个"开着 Safari 地址栏和工具栏"的高度（照 Playwright 给 iPhone 15 扣的：竖屏 193、横屏宽 118 高 50），一个全屏的（加到主屏幕、工具栏收起）
const base = devices['iPhone 15']
const duo = (label, w, h) => { devices[label] = { ...base, viewport: { width: w, height: h }, screen: { width: w, height: h } }; list.push(label) }
duo('iPhone Duo 合上竖着（Safari 栏）', 466, 485)
duo('iPhone Duo 合上竖着（全屏）', 466, 678)
duo('iPhone Duo 合上横着（Safari 栏）', 560, 416)
duo('iPhone Duo 合上横着（全屏）', 678, 466)
duo('iPhone Duo 打开竖着（Safari 栏）', 626, 697)
duo('iPhone Duo 打开竖着（全屏）', 626, 890)
duo('iPhone Duo 打开横着（Safari 栏）', 890, 566)
duo('iPhone Duo 打开横着（全屏）', 890, 626)
console.log(`机型（按尺寸去重）：${list.length} 种：${[...seen.entries()].map(([k, n]) => `${n} ${k}`).join('、')}`)

// 注册一个账号，cookie 给所有机型用
const c0 = await b.newContext({ ...devices['iPhone 15'], locale: 'zh-CN' })
const p0 = await c0.newPage()
await p0.goto(BASE + '/?page=login')
const call = (method, path, body) => p0.evaluate(async ({ method, path, body }) => {
  const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
  return { s: r.status, b: await r.json().catch(() => null) }
}, { method, path, body })
const reg = await call('POST', '/api/auth/register', { email: `wk${Date.now()}${rnd().slice(0, 4)}@example.com`, password: 'pw-' + rnd() + rnd() })
ok(reg.s === 200, `注册（${reg.s}）`)
await call('POST', '/api/me/prefs', { lang: 'zh' }).catch(() => {})
const state = await c0.storageState()
await c0.close()

const pages = [['首页', '/'], ['全市场', '/?page=market'], ['今日', '/?page=market&tab=today'], ['交易', '/?page=trade&i=BTC-PERP'], ['策略', '/?page=ai'], ['风险', '/?page=risk'], ['复盘', '/?page=review'], ['账户', '/?page=account']]
const small = new Set()
// iPhone Duo 先跑；代表性的尺寸（最小、最大、iPad、Duo）走全部 8 个页面，其余只走首页、交易、账户（46 种尺寸全走要超过 15 分钟）
list.sort((a, c) => Number(/Duo/.test(c)) - Number(/Duo/.test(a)))
const fullSet = (n) => /Duo|iPad|SE|Pro Max|^iPhone 6/.test(n)
for (const name of list) {
  const d = devices[name]
  if (!d) { ok(false, `没有这个机型：${name}`); continue }
  const ctx = await b.newContext({ ...d, locale: 'zh-CN', timezoneId: 'Asia/Shanghai', storageState: state })
  await ctx.addInitScript(() => { try { localStorage.setItem('qs.wizardSeen', '1'); sessionStorage.setItem('qs.heroSeen', '1') } catch {} })
  const p = await ctx.newPage()
  const errs = []
  p.on('pageerror', (e) => { if (!/access control checks/.test(String(e))) errs.push(String(e).slice(0, 160)) }) // 切页时 Safari 中断请求会报这句，停在页面上不会出
  const tag = `${name}（${d.viewport.width}×${d.viewport.height}）`
  const overs = []
  for (const [pn, q] of pages.filter(([pn]) => fullSet(name) || ['首页', '交易', '账户'].includes(pn))) {
    await p.goto(BASE + q)
    await p.waitForTimeout(pn === '首页' ? 3500 : 2500)
    const over = await p.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    if (over > 1) overs.push(`${pn} ${over}px`)
    // 输入框字号：看得见的、能打字的
    const fs16 = await p.evaluate(() => [...document.querySelectorAll('input:not([type=checkbox]):not([type=radio]):not([type=range]):not([type=hidden]), textarea, select')]
      .filter((el) => el.offsetParent && !el.disabled && !el.readOnly)
      .map((el) => ({ k: el.getAttribute('aria-label') || el.getAttribute('placeholder') || el.name || el.className.split(' ')[0], f: parseFloat(getComputedStyle(el).fontSize) }))
      .filter((x) => x.f < 16))
    if (d.isMobile) for (const x of fs16) small.add(`${pn}：${x.k}（${x.f}px）`)
    if (pn === '交易') {
      let go = await p.locator('.op-submit').first().boundingBox().catch(() => null)
      if (d.viewport.height > 480) {
        ok(!!go && go.y >= 0 && go.y + go.height <= d.viewport.height + 1, `${tag} 交易页：下单按钮不用滑就在屏幕里（${go ? Math.round(go.y) + '–' + Math.round(go.y + go.height) : '没找到'}）`)
      } else { // 很矮的横屏按设计 K 线优先，下单区往下滑（market.css 的 max-height: 480px）
        await p.evaluate(() => { const m = document.querySelector('.mk'); if (m) m.scrollTop = m.scrollHeight })
        await p.waitForTimeout(300)
        go = await p.locator('.op-submit').first().boundingBox().catch(() => null)
        ok(!!go && go.y >= 0 && go.y + go.height <= d.viewport.height + 1, `${tag} 交易页（很矮的横屏）：往下滑就够到下单按钮（${go ? Math.round(go.y) + '–' + Math.round(go.y + go.height) : '没找到'}）`)
      }
    }
    if (name === list[0] || /Pro Max landscape|iPad Mini landscape|Duo/.test(name) || /SE.*landscape/.test(name)) await p.screenshot({ path: `${OUT}/${name.replace(/[\s（）()]+/g, '_')}-${pn}.png` })
  }
  ok(overs.length === 0, `${tag} 没有横向溢出${overs.length ? '：' + overs.join('、') : ''}`)
  ok(errs.length === 0, `${tag} 页面没有报错${errs.length ? '：' + errs.slice(0, 2).join(' | ') : ''}`)
  await ctx.close()
}
// iPhone Duo 的开屏（没登录第一次打开的 3D 地形）：8 种状态都要铺满屏幕、看不到地形边界（V.37）。
// 开屏先实测帧时间，太慢换成介绍页；模拟器是软件绘图，展开的内屏画不动，所以强制按高档看排版（真机有 GPU）
for (const name of list.filter((n) => /Duo/.test(n))) {
  const d = devices[name]
  const ctx = await b.newContext({ ...d, locale: 'zh-CN' })
  await ctx.addInitScript(() => { try { localStorage.setItem('qs.tierForce', 'high') } catch {} })
  const p = await ctx.newPage()
  const errs = []
  p.on('pageerror', (e) => { if (!/access control checks/.test(String(e))) errs.push(String(e).slice(0, 160)) })
  await p.goto(BASE + '/')
  await p.waitForTimeout(6000)
  const r = await p.evaluate(() => { const c = document.querySelector('.hero-gl')?.getBoundingClientRect(); return { hero: !!document.querySelector('.hero'), cw: c ? Math.round(c.width) : 0, ch: c ? Math.round(c.height) : 0, over: document.documentElement.scrollWidth - document.documentElement.clientWidth } })
  ok(r.hero && r.cw === d.viewport.width && r.ch === d.viewport.height && r.over <= 1 && !errs.length, `${name}（${d.viewport.width}×${d.viewport.height}）开屏：地形铺满屏幕（${r.cw}×${r.ch}）${errs.length ? '，报错：' + errs[0] : ''}`)
  await p.screenshot({ path: `${OUT}/${name.replace(/[\s（）()]+/g, '_')}-开屏.png` })
  await ctx.close()
}

// iPhone 上点进字号小于 16px 的输入框，Safari 会自动放大整页：苹果设备加了 maximum-scale=1 就不会（main.tsx），否则字号都要 ≥ 16px
{
  const ctx = await b.newContext({ ...devices['iPhone 15'], storageState: state })
  const p = await ctx.newPage()
  await p.goto(BASE + '/?page=trade&i=BTC-PERP')
  await p.waitForTimeout(2000)
  const vp = await p.evaluate(() => document.querySelector('meta[name="viewport"]')?.getAttribute('content') ?? '')
  ok(/maximum-scale=1/.test(vp) || small.size === 0, `iPhone 上点输入框不会自动放大整页（视口：${vp}${small.size ? '；小于 16px 的输入框：' + [...small].slice(0, 6).join('；') : ''}）`)
  // 用着用着打开、合上：合上的外屏 → 打开横着 → 再合上，页面跟着重新排好
  await p.setViewportSize({ width: 466, height: 485 })
  await p.waitForTimeout(1200)
  const gap0 = await p.evaluate(() => { const cv = document.querySelector('.mk-chart .surface canvas.interactive'); const box = document.querySelector('.mk-chart .display-body, .mk-chart'); return cv && box ? Math.round(box.getBoundingClientRect().width - cv.getBoundingClientRect().width) : -1 }) // 右边价格刻度的宽度
  for (const [label, w, h] of [['打开横着', 890, 566], ['再合上', 466, 485], ['打开竖着', 626, 697]]) {
    await p.setViewportSize({ width: w, height: h })
    await p.waitForTimeout(1500)
    const r = await p.evaluate(() => {
      const cv = document.querySelector('.mk-chart .surface canvas.interactive')
      const box = document.querySelector('.mk-chart .display-body, .mk-chart')
      return { over: document.documentElement.scrollWidth - document.documentElement.clientWidth, cw: cv ? Math.round(cv.getBoundingClientRect().width) : -1, bw: box ? Math.round(box.getBoundingClientRect().width) : -1 }
    })
    const go = await p.locator('.op-submit').first().boundingBox().catch(() => null)
    ok(r.over <= 1 && r.cw > 0 && Math.abs(r.bw - r.cw - gap0) <= 4 && !!go && go.y + go.height <= h + 1, `iPhone Duo ${label}（${w}×${h}）：没有横向溢出、K 线跟着变宽（画布 ${r.cw}，外框 ${r.bw}，刻度 ${gap0}）、下单按钮在屏幕里（${go ? Math.round(go.y + go.height) : '无'}）`)
    await p.screenshot({ path: `${OUT}/Duo-切换-${label}.png` })
  }
  await ctx.close()
}
await b.close()
console.log(`\n通过 ${good}，失败 ${bad}`)
process.exit(bad ? 1 : 0)
