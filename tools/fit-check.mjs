// 全尺寸适配检查（12 的 V.36，D85 第 17 条）：每个页面、登录前后、中英文、每种常见裁切。
// 脚本查：横向溢出、整页滚动、控件出屏或被裁、文字被切、当前页的导航看不见、主画面太小、英文里有中文。
// 每个尺寸都留截图，之后拼成缩略图用眼看。脚本只能发现"出界"，看不出"丑"。
// 用法：node fit-check.mjs <输出目录> <前端地址> [只跑的尺寸 id，逗号分隔]。会注册账号、下单，不要对着 8080 跑。
import { createRequire } from 'module'
import { randomBytes } from 'crypto'
import fs from 'fs'
const require = createRequire(import.meta.url)
const { chromium } = require('playwright-core')
const OUT = process.argv[2] ?? 'shots/fit'
const BASE = process.argv[3] ?? 'http://127.0.0.1:4173'
const ONLY = process.argv[4] ? process.argv[4].split(',') : null
if (/:8080/.test(BASE)) throw new Error('不要对着 8080 跑')
fs.mkdirSync(OUT, { recursive: true })
const rnd = () => randomBytes(6).toString('hex')

// id、名字、宽、高
const SIZES = [
  ['fold-in', '折叠屏内屏', 884, 1104], ['fold-in-l', '折叠屏内屏横放', 1104, 884], ['fold-out', '折叠屏外屏', 344, 882],
  ['flip', '翻盖长屏', 412, 1006], ['pad-s', '小平板竖', 768, 1024], ['pad-s-l', '小平板横', 1024, 768],
  ['pad-m', '中平板竖', 820, 1180], ['pad-m-l', '中平板横', 1180, 820], ['pad-l', '大平板竖', 1024, 1366], ['pad-l-l', '大平板横', 1366, 1024],
  ['sq-s', '小正方形', 600, 600], ['sq-m', '中正方形', 800, 800], ['sq-l', '大正方形', 1200, 1200],
  ['ph-l', '手机横屏', 844, 390], ['ph-l2', '大手机横屏', 915, 412], ['ph-s', '小手机', 360, 640], ['ph', '手机', 390, 844], ['ph-b', '大手机', 430, 932],
  ['narrow', '窄长窗口', 480, 1000], ['narrow-s', '更窄的窗口', 320, 900], ['short', '矮宽窗口', 1280, 400], ['short-w', '更宽的矮窗口', 1600, 500], ['short-s', '矮窄窗口', 1000, 360],
  ['ultra', '带鱼屏', 3440, 1440], ['ultra-s', '小带鱼屏', 2560, 1080],
  ['user', '用户的屏幕 1080p 125%', 1536, 730], ['lap', '1080p 150%', 1280, 590], ['old', '老笔记本', 1366, 625], ['desk', '1080p 台式', 1920, 950], ['pane', 'Claude 侧边窗', 618, 691],
]

const OUT_PAGES = [
  ['hero', '/?page=hero'], ['about', '/?page=about'], ['landing', '/?page=landing'], ['login', '/?page=login'],
  ['market', '/?page=market'], ['ai', '/?page=ai'], ['trade', '/?page=trade'], ['risk', '/?page=risk'], ['review', '/?page=review'],
]
const IN_PAGES = [
  ['market', '/?page=market&tab=map'], ['m-heat', '/?page=market&tab=heat'], ['m-flow', '/?page=market&tab=flow'], ['m-chain', '/?page=market&tab=chain'],
  ['m-venues', '/?page=market&tab=venues'], ['m-system', '/?page=market&tab=system'],
  ['ai', '/?page=ai'], ['trade', '/?page=trade'], ['risk', '/?page=risk'],
  ['r-log', '/?page=review&tab=log'], ['r-perf', '/?page=review&tab=perf'], ['r-mae', '/?page=review&tab=mae'], ['r-find', '/?page=review&tab=findings'], ['r-pb', '/?page=review&tab=playbooks'], ['r-prac', '/?page=review&tab=practice'],
  ['account', '/?page=account'], ['about', '/?page=about'],
]

// 在页面里跑的检查
const inspect = (lang) => {
  const vw = innerWidth, vh = innerHeight
  const out = []
  const de = document.documentElement
  if (de.scrollWidth > de.clientWidth + 1) out.push(`横向溢出 ${de.scrollWidth - de.clientWidth}px`)
  if (de.scrollHeight > de.clientHeight + 1 && document.body.scrollHeight > vh + 1) out.push(`整页滚动 ${de.scrollHeight - de.clientHeight}px`)
  const nm = (e) => {
    const t = (e.getAttribute('aria-label') || e.innerText || e.getAttribute('title') || e.getAttribute('placeholder') || e.tagName).trim().replace(/\s+/g, ' ')
    return t.slice(0, 22)
  }
  const shown = (e) => {
    const s = getComputedStyle(e)
    if (s.visibility === 'hidden' || s.display === 'none' || Number(s.opacity) === 0) return false
    const r = e.getBoundingClientRect()
    return r.width > 1 && r.height > 1
  }
  // 能横向滚动的容器里出界没关系（比如标签条）
  const scroller = (e) => {
    for (let p = e.parentElement; p; p = p.parentElement) {
      const s = getComputedStyle(p)
      if ((s.overflowX === 'auto' || s.overflowX === 'scroll') && p.scrollWidth > p.clientWidth + 1) return p
    }
    return null
  }
  const seen = new Set()
  const add = (s) => { if (!seen.has(s) && out.length < 40) { seen.add(s); out.push(s) } }
  for (const e of document.querySelectorAll('a, button, input, select, textarea, [role=button], [role=radio], [role=tab]')) {
    if (!shown(e) || e.closest('.sr-only, .toast')) continue
    const r = e.getBoundingClientRect()
    const sc = scroller(e)
    const vert = (() => { for (let p = e.parentElement; p; p = p.parentElement) { const s = getComputedStyle(p); if ((s.overflowY === 'auto' || s.overflowY === 'scroll') && p.scrollHeight > p.clientHeight + 1) return true } return false })()
    if (!sc && (r.left < -1 || r.right > vw + 1)) add(`控件出屏：${nm(e)}（${Math.round(r.left)}..${Math.round(r.right)} / ${vw}）`)
    if (!vert && (r.top < -1 || r.bottom > vh + 1)) add(`控件在屏幕外：${nm(e)}（${Math.round(r.top)}..${Math.round(r.bottom)} / ${vh}）`)
    // 被隐藏溢出的祖先切掉
    if (!sc) {
      for (let p = e.parentElement; p && p !== document.body; p = p.parentElement) {
        const s = getComputedStyle(p)
        if (s.overflowX === 'hidden' || s.overflowX === 'clip') {
          const q = p.getBoundingClientRect()
          if (r.right > q.right + 2 || r.left < q.left - 2) { add(`控件被切：${nm(e)}（容器 .${String(p.className).split(' ')[0]}）`); break }
        }
      }
    }
  }
  // 文字被切：隐藏溢出又没有省略号
  for (const e of document.querySelectorAll('button, a, span, b, em, td, th, h1, h2, h3, label, p, dd, dt')) {
    if (!shown(e) || !e.childNodes.length || e.children.length > 1) continue
    const s = getComputedStyle(e)
    if (s.overflowX !== 'hidden' && s.overflowX !== 'clip') continue
    if (s.textOverflow === 'ellipsis') continue
    if (e.scrollWidth > e.clientWidth + 2 && e.innerText?.trim()) add(`文字被切：${nm(e)}`)
  }
  // 当前页的导航要看得见
  const cur = document.querySelector('.nav [aria-current=page]')
  if (cur && shown(cur)) {
    const r = cur.getBoundingClientRect()
    const nav = cur.closest('.nav').getBoundingClientRect()
    if (r.left < Math.max(0, nav.left) - 1 || r.right > Math.min(vw, nav.right) + 1) add(`当前页的导航看不见：${nm(cur)}`)
  }
  // 交易页：品种条不能被压扁，K 线绘图区不能矮得看不清（只查出界发现不了这两个，是用眼看出来的）
  const strip = document.querySelector('.mk-strip')
  if (strip && shown(strip) && strip.scrollHeight > strip.clientHeight + 2) add(`品种条被压扁（内容 ${strip.scrollHeight}，只有 ${strip.clientHeight}）`)
  const plot = document.querySelector('.mk-chart .display-body')
  if (plot && shown(plot) && plot.getBoundingClientRect().height < 140) add(`K 线绘图区太矮（${Math.round(plot.getBoundingClientRect().height)}px）`)
  // 主画面：最大的画布不能小得看不清
  let big = 0
  for (const c of document.querySelectorAll('canvas')) {
    if (!shown(c)) continue
    const r = c.getBoundingClientRect()
    big = Math.max(big, Math.min(r.width, vw) * Math.min(r.height, vh))
  }
  // 英文里不能有中文
  if (lang === 'en') {
    const cjk = /[一-鿿＀-￯　-〿]/
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      const v = n.nodeValue.trim()
      if (!v || !cjk.test(v) || v === '中' || v === '中文') continue
      const el = n.parentElement
      if (!el || el.closest('script, style, textarea') || !shown(el)) continue
      add(`英文里有中文：${v.slice(0, 26)}`)
    }
    for (const e of document.querySelectorAll('[title], [aria-label], [placeholder]')) {
      for (const a of ['title', 'aria-label', 'placeholder']) {
        const v = e.getAttribute(a)
        if (v && cjk.test(v) && v !== '中' && v !== '中文') add(`英文属性里有中文：${v.slice(0, 26)}`)
      }
    }
  }
  return { issues: out, canvas: Math.round(big), hasText: document.body.innerText.trim().length }
}

const browser = await chromium.launch({ channel: process.env.PW_CHANNEL ?? 'msedge', headless: true, args: ['--enable-webgl', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'] })

// 准备有数据的账号：持仓、挂单、平掉的交易、通知、到价提醒。语言存在账户上，所以中英文各一个
async function seed(lang) {
const seedCtx = await browser.newContext({ viewport: { width: 1536, height: 730 } })
const sp = await seedCtx.newPage()
await sp.goto(`${BASE}/?page=login`)
const call = (m, path, body) => sp.evaluate(async ({ m, path, body }) => {
  const r = await fetch(path, { method: m, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
  let j = null
  try { j = await r.json() } catch { /* 没有内容 */ }
  return { s: r.status, b: j }
}, { m, path, body })
await call('POST', '/api/auth/register', { email: `fit${Date.now()}${rnd().slice(0, 4)}@example.com`, password: 'pw-' + rnd() + rnd() })
await call('POST', '/api/me/lang', { lang })
const tp = 'tp-' + rnd()
await call('POST', '/api/me/trade-password', { new: tp })
await call('POST', '/api/discipline/enabled', { on: false, trade_password: tp })
const A = (await call('GET', '/api/paper-accounts')).b.accounts[0].id
let px0 = 0
for (let i = 0; i < 40 && !px0; i++) { const s = (await call('GET', '/api/market/snapshot')).b.instruments.find((x) => x.instrument === 'BTC-PERP'); px0 = Number(s?.bid ?? 0); if (!px0) await sp.waitForTimeout(300) }
const order = (inst, side, qty, extra = {}) => call('POST', `/api/trade/${A}/orders`, { instrument: inst, side, type: 'market', qty: String(qty), ...extra })
await call('POST', `/api/notify/prefs`, { kind: 'fill', site: true, tg: false })
// 几笔平掉的交易，复盘页才有东西
for (let i = 0; i < 6; i++) {
  await order('BTC-PERP', i % 2 ? 'sell' : 'buy', 0.01)
  await sp.waitForTimeout(300)
  await call('POST', `/api/trade/${A}/close`, { instrument: 'BTC-PERP' })
  await sp.waitForTimeout(200)
}
// 留着的持仓和挂单
await order('BTC-PERP', 'buy', 0.02)
await order('ETH-PERP', 'sell', 0.2)
await call('POST', `/api/trade/${A}/tpsl`, { instrument: 'BTC-PERP', sl: String(Math.round(px0 * 0.95)), tp: String(Math.round(px0 * 1.05)) })
await call('POST', `/api/trade/${A}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'limit', tif: 'GTC', qty: '0.01', price: String(Math.round(px0 * 0.97)) })
await call('POST', '/api/notify/alerts', { instrument: 'BTC-PERP', px: Math.round(px0 * 1.2) })
const state = await seedCtx.storageState()
await seedCtx.close()
return state
}
const states = { en: await seed('en'), zh: await seed('zh') }

const all = []
const report = (r) => { all.push(r); if (r.issues.length) console.log(`✗ ${r.size} ${r.who} ${r.lang} ${r.page}: ${r.issues.slice(0, 4).join(' | ')}`) }

async function runSize([id, name, w, h]) {
  for (const who of ['out', 'in']) {
    for (const lang of ['en', 'zh']) {
      const ctx = await browser.newContext({ viewport: { width: w, height: h }, deviceScaleFactor: 1, locale: lang === 'en' ? 'en-US' : 'zh-CN', timezoneId: 'Asia/Shanghai', storageState: who === 'in' ? states[lang] : undefined })
      await ctx.addInitScript((l) => { try { localStorage.setItem('qs.lang', l); localStorage.setItem('qs.wizardSeen', '1'); sessionStorage.setItem('qs.heroSeen', '1') } catch { /* 忽略 */ } }, lang)
      const p = await ctx.newPage()
      const errs = []
      p.on('pageerror', (e) => errs.push(String(e).slice(0, 160)))
      const pages = who === 'in' ? IN_PAGES : OUT_PAGES
      for (const [pid, url] of pages) {
        try {
          await p.goto(BASE + url, { waitUntil: 'domcontentloaded', timeout: 20000 })
          await p.waitForTimeout(/hero|terrain/.test(pid) ? 2600 : pid === 'about' ? 1800 : 1300)
          const r = await p.evaluate(inspect, lang)
          if (errs.length) { r.issues.push(`页面报错：${errs.join(' | ')}`); errs.length = 0 }
          report({ size: id, name, w, h, who, lang, page: pid, ...r })
          if (lang === 'en') await p.screenshot({ path: `${OUT}/${id}__${who}__${pid}.png` })
        } catch (e) {
          report({ size: id, name, w, h, who, lang, page: pid, issues: [`打不开：${String(e).slice(0, 80)}`], canvas: 0 })
        }
      }
      // 弹层：铃铛
      if (who === 'in' && lang === 'en') {
        try {
          await p.goto(BASE + '/?page=trade', { waitUntil: 'domcontentloaded' })
          await p.waitForSelector('[data-bell]', { timeout: 8000 })
          await p.waitForTimeout(600)
          await p.locator('[data-bell]').click({ timeout: 3000 })
          await p.waitForSelector('[data-bell-pop]', { timeout: 3000 })
          await p.waitForTimeout(300)
          const box = await p.locator('[data-bell-pop]').boundingBox()
          const iss = []
          if (box && (box.x < -1 || box.x + box.width > w + 1)) iss.push(`通知弹层出屏（${Math.round(box.x)}..${Math.round(box.x + box.width)} / ${w}）`)
          if (box && box.y + box.height > h + 1) iss.push(`通知弹层超出屏幕底部（${Math.round(box.y + box.height)} / ${h}）`)
          report({ size: id, name, w, h, who, lang, page: 'bell', issues: iss, canvas: 0 })
          await p.screenshot({ path: `${OUT}/${id}__${who}__bell.png` })
        } catch (e) {
          report({ size: id, name, w, h, who, lang, page: 'bell', issues: [`铃铛点不到：${String(e).slice(0, 80)}`], canvas: 0 })
        }
      }
      await ctx.close()
    }
  }
  console.log(`· ${id} ${w}×${h} 做完`)
}

const todo = SIZES.filter((s) => !ONLY || ONLY.includes(s[0]))
const queue = [...todo]
await Promise.all(Array.from({ length: 3 }, async () => { while (queue.length) await runSize(queue.shift()) }))

// 缩放：1×、1.25×、1.5×、2×。布局用的是 CSS 像素不变，这里看细线和文字有没有糊，只拍几个代表页
for (const k of [1, 1.25, 1.5, 2]) {
  const w = Math.round(1920 / k), h = Math.round(950 / k)
  const ctx = await browser.newContext({ viewport: { width: w, height: h }, deviceScaleFactor: k, locale: 'en-US', timezoneId: 'Asia/Shanghai', storageState: states.en })
  const p = await ctx.newPage()
  for (const [pid, url] of [['trade', '/?page=trade'], ['risk', '/?page=risk'], ['account', '/?page=account']]) {
    await p.goto(BASE + url, { waitUntil: 'domcontentloaded' })
    await p.waitForTimeout(1500)
    const r = await p.evaluate(inspect, 'en')
    report({ size: `scale-${k}`, name: `缩放 ${k}×`, w, h, who: 'in', lang: 'en', page: pid, ...r })
    await p.screenshot({ path: `${OUT}/scale-${k}__in__${pid}.png` })
  }
  await ctx.close()
}

await browser.close()
fs.writeFileSync(`${OUT}/result.json`, JSON.stringify(all, null, 1))
const bad = all.filter((r) => r.issues.length)
console.log(`\n一共查了 ${all.length} 个画面，有问题的 ${bad.length} 个`)
