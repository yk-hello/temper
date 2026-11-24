// 高光的检查（D91）：真的下单、用价格注入做出赚钱的平仓，看够门槛的有没有记进生涯档案、卡片和视频对不对。
// 会注册账号、下单，只能对着模拟行情的测试环境跑（不要对着 8080）。
//   node tools/e2e-highlight.mjs <截图目录> <网址>
// 生成的视频存在截图目录的 hl-film.mp4，卡片存成 hl-card.png。
import { chromium } from 'playwright'
import { writeFileSync } from 'node:fs'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge', args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'] })
const newPage = async (opts = {}) => {
  const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN', timezoneId: 'Asia/Taipei', acceptDownloads: true, ...opts })
  await c.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1') })
  const p = await c.newPage()
  p.errs = []
  p.on('pageerror', (e) => p.errs.push(String(e)))
  p.on('console', (m) => { if (m.type() === 'error' && !/Failed to load resource/.test(m.text())) p.errs.push(m.text()) })
  return p
}
const call = (p, method, path, body) => p.evaluate(async ({ method, path, body }) => {
  const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
  let j = null
  try { j = await r.json() } catch { /* 不是 JSON */ }
  return { s: r.status, b: j }
}, { method, path, body })
const rand = () => Math.random().toString(36).slice(2)
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const until = async (fn, ms = 15000) => {
  const t0 = Date.now()
  while (Date.now() - t0 < ms) {
    const v = await fn()
    if (v) return v
    await sleep(250)
  }
  return null
}
const register = async (p, tag) => {
  const r = await call(p, 'POST', '/api/auth/register', { email: `hl${tag}${Date.now()}${rand().slice(0, 4)}@example.com`, password: `hl-${rand()}-pass` })
  if (r.s >= 300) throw new Error('注册失败 ' + JSON.stringify(r))
  const tp = 'tp-' + rand()
  await call(p, 'POST', '/api/me/trade-password', { new: tp })
  await call(p, 'POST', '/api/discipline/enabled', { on: false, trade_password: tp })
  const list = await call(p, 'GET', '/api/paper-accounts')
  return (list.b.accounts ?? list.b)[0].id
}
const INST = 'BTC-PERP'
const price = async (p) => Number((await call(p, 'GET', '/api/market/snapshot')).b.instruments.find((x) => x.instrument === INST).last)
const order = (p, acct, side, qty, extra = {}) => call(p, 'POST', `/api/trade/${acct}/orders`, { instrument: INST, side, type: 'market', qty: String(qty), ...extra })
const inject = (p, px) => call(p, 'POST', '/api/dev/sim-price', { instrument: INST, price: px })
const flat = (p, acct) => until(async () => { const v = await call(p, 'GET', `/api/trade/${acct}`).catch(() => null); return v?.s === 200 && !(v.b.positions ?? []).some((x) => x.instrument === INST) })
const round1 = (v) => Math.round(v * 10) / 10
const items = async (p) => (await call(p, 'GET', '/api/highlights')).b.items
// 做一笔：开多 0.1，价格按 path 一步步走（每步之间停一下，K 线和最深浮亏才有东西），最后平掉
const trade = async (p, acct, px0, path) => {
  await order(p, acct, 'buy', 0.1)
  for (const k of path) { await sleep(700); await inject(p, round1(px0 * k)) }
  await sleep(500)
  await order(p, acct, 'sell', 0.1, { reduce_only: true })
  await flat(p, acct)
}

const p = await newPage()
await p.goto(BASE + '/?page=login')
const A = await register(p, 'a')
await p.goto(BASE + '/?page=trade&i=BTC-PERP')
await p.waitForSelector('.mk-chart canvas')

// ---------- 门槛 ----------
const l0 = await call(p, 'GET', '/api/highlights')
ok(l0.s === 200 && l0.b.items.length === 0 && Math.abs(l0.b.min_roe - 0.2) < 1e-9, `默认门槛按保证金 20%（${l0.b?.min_roe}）`)
ok((await call(p, 'POST', '/api/highlights/prefs', { min_roe: 0 })).s === 400, '门槛不能是 0')
ok((await call(p, 'POST', '/api/highlights/prefs', { min_roe: 999 })).b.min_roe === 50, '门槛太大夹到 5000%')
ok((await call(p, 'POST', '/api/highlights/prefs', { min_roe: 0.1 })).b.min_roe === 0.1, '门槛改成 10%')
ok((await call(p, 'GET', '/api/highlights')).b.min_roe === 0.1, '门槛记住了')

// ---------- 去掉的接口 ----------
ok((await call(p, 'GET', '/api/v/abc')).s === 404, '验真接口没有了')
ok([404, 405].includes((await call(p, 'POST', '/api/invite/claim', { code: 'ABCDEFGH' })).s), '邀请接口没有了')

// ---------- 亏的、赚得不够的：不记 ----------
const px0 = await until(() => price(p))
await trade(p, A, px0, [0.99])
await inject(p, px0); await sleep(300)
await trade(p, A, px0, [1.004]) // 10 倍约 +4%，减掉手续费不到 10%
await inject(p, px0)
await sleep(4500)
ok((await items(p)).length === 0, '亏的一笔、赚得不够门槛的一笔都不记')

// ---------- 够门槛的：先跌一段再涨上去 ----------
await trade(p, A, px0, [0.997, 0.994, 0.998, 1.004, 1.009, 1.014])
const got = await until(async () => { const l = await items(p); return l.length ? l : null })
ok(!!got && got.length === 1, '够门槛的一笔记进了生涯档案')
const h = got?.[0]
const s = h?.snap ?? {}
const lev = s.leverage
ok(s.instrument === INST && s.side === 'long' && s.mode === 'sim' && s.origin === 'manual', '品种、方向、模拟盘、手动')
const roe = s.net / (s.entry * s.qty / lev)
ok(Math.abs(roe - s.roe) < 1e-4 && s.roe >= 0.1, `按保证金的回报 = 净盈亏 ÷ 初始保证金（${s.roe}）`)
ok(s.acct_ret > 0 && s.acct_ret < s.roe, `占账户比例（${s.acct_ret}）`)
ok(s.path_known ? s.mae < 0 && s.mfe >= s.roe * 0.8 : true, `最深浮亏、最高浮盈（${s.mae} / ${s.mfe}，${s.path_known ? '有价格路径' : '没有价格路径'}）`)
ok(!('token' in h) && !('show_amount' in h), '没有验真编号、没有公开金额的开关')
const nt = await until(async () => (await call(p, 'GET', '/api/notify')).b.items.find((x) => x.kind === 'highlight'))
ok(!!nt && nt.params.id === String(h.id) && Number(nt.params.roe) > 0, '站内通知发了')
const sc = await call(p, 'GET', `/api/highlights/${h.id}/scene`)
ok(sc.s === 200 && Array.isArray(sc.b.events), '持仓期间的事件')

// 不再有 24 小时冷却：再赚一笔也记
await inject(p, px0); await sleep(300)
await trade(p, A, px0, [1.006, 1.016])
ok(!!(await until(async () => (await items(p)).length === 2)), '又一笔够门槛的也记（没有冷却）')

// 别人看不到、删不了
const q = await newPage()
await q.goto(BASE + '/?page=login')
await register(q, 'b')
ok((await call(q, 'GET', '/api/highlights')).b.items.length === 0, '别人的档案是空的')
ok((await call(q, 'DELETE', `/api/highlights/${h.id}`)).s === 404, '别人删不了')
ok((await call(q, 'GET', `/api/highlights/${h.id}/scene`)).s === 404, '别人取不到')
const anon = await (await browser.newContext()).newPage()
await anon.goto(BASE + '/?page=login')
ok((await call(anon, 'GET', '/api/highlights')).s === 401 && (await call(anon, 'GET', '/api/highlights/demo')).s === 401, '没登录不能看')

// ---------- 顶部提示 ----------
await p.goto(BASE + '/?page=trade&i=BTC-PERP')
await inject(p, px0); await sleep(300)
await call(p, 'POST', '/api/highlights/prefs', { min_roe: 0.01 })
await trade(p, A, px0, [1.008])
const ping = await p.waitForSelector('[data-hl-ping]', { timeout: 25000 }).catch(() => null)
ok(!!ping && /高光/.test(await ping.innerText()), '顶部亮了一条提示')
if (ping) await p.screenshot({ path: `${OUT}/hl-ping.png` })
await call(p, 'POST', '/api/highlights/prefs', { min_roe: 0.1 })
await inject(p, px0)

// ---------- 生涯档案界面 ----------
await p.bringToFront() // 前面开过别的页面：这一页回到前台（用户生成视频时页面在前台）
await p.goto(BASE + `/?page=review&tab=hl&h=${h.id}`)
await p.waitForSelector('[data-hl-card]', { timeout: 15000 })
ok(await p.locator('[data-hl-card]').count() === 3, '档案里 3 张卡片')
ok(/10/.test(await p.inputValue('[data-hl-min]')), '门槛输入框是 10')
ok(/高光\s*3/.test(await p.locator('[data-hl-stats]').innerText()), '顶上写了高光次数')
await sleep(2500)
const pix = await p.evaluate((id) => {
  const c = document.querySelector(`[data-hl-card="${id}"] canvas`)
  const d = c.getContext('2d').getImageData(0, 0, c.width, c.height).data
  let gold = 0, lit = 0, red = 0, green = 0
  for (let i = 0; i < d.length; i += 4) {
    const [r, g, b] = [d[i], d[i + 1], d[i + 2]]
    if (r > 120 && g > 90 && b < 110 && r - b > 50) gold++
    if (r + g + b > 300) lit++
    if (r > 180 && g < 120 && b < 120) red++
    if (g > 150 && r < 100) green++
  }
  return { gold, lit, red, green }
}, h.id)
ok(pix.gold > 400 && pix.lit > 2000 && pix.green > 50, `卡片画出来了：金色区域、K 线、文字（金 ${pix.gold}、亮 ${pix.lit}、红 ${pix.red}、绿 ${pix.green}）`)
await p.screenshot({ path: `${OUT}/hl-album.png` })
const dl = p.waitForEvent('download', { timeout: 15000 }).catch(() => null)
await p.click(`[data-hl-card="${h.id}"] [data-hl-png]`)
const png = await dl
if (png) await png.saveAs(`${OUT}/hl-card.png`)
ok(!!png && /\.png$/.test(png.suggestedFilename()), `保存图片（${png?.suggestedFilename()}）`)

// ---------- 视频 ----------
await p.click(`[data-hl-card="${h.id}"] [data-hl-film]`)
await p.waitForSelector('[data-hl-preview]', { timeout: 15000 })
await sleep(3000)
const prevPix = await p.evaluate(() => {
  const c = document.querySelector('[data-hl-preview]')
  const d = c.getContext('2d').getImageData(0, 0, c.width, c.height).data
  let n = 0
  for (let i = 0; i < d.length; i += 16) if (d[i] + d[i + 1] + d[i + 2] > 120) n++
  return n
})
ok(prevPix > 300, `预览在播（亮的像素 ${prevPix}）`)
await p.screenshot({ path: `${OUT}/hl-film-dialog.png` })
await p.click('.hl-side [data-tab="1080p"]').catch(() => {}) // 无头浏览器没有显卡，4K 太慢；4K 在真显卡上另外看
await p.click('[data-hl-render]')
// 无头浏览器没有显卡，编码慢得不正常（实测 10 分钟 3%）；这里只查"开始生成、进度在走、能取消"。
// 完整生成在真显卡的浏览器上查：生成 4K、抽帧看画面（`07` 记了结果）。
let vid = await p.waitForSelector('[data-hl-video]', { timeout: 20000 }).catch(() => null)
if (vid) {
  const b64 = await p.evaluate(async () => {
    const v = document.querySelector('[data-hl-video]')
    const buf = new Uint8Array(await (await fetch(v.src)).arrayBuffer())
    let s = ''
    for (let i = 0; i < buf.length; i += 0x8000) s += String.fromCharCode(...buf.subarray(i, i + 0x8000))
    return btoa(s)
  })
  writeFileSync(`${OUT}/hl-film.mp4`, Buffer.from(b64, 'base64'))
  ok(true, '视频生成了')
} else {
  const pr = await p.locator('.hl-busy').innerText().catch(() => '')
  ok(/正在生成 \d+%/.test(pr), `开始生成，有进度（${pr}）`)
  await p.click('[data-hl-cancel]')
  const c0 = Date.now()
  const back = await p.waitForSelector('[data-hl-render]', { timeout: 15000 }).catch(() => null)
  ok(!!back && Date.now() - c0 < 8000, `能取消，而且马上生效（${((Date.now() - c0) / 1000).toFixed(1)} 秒；没显卡的电脑上一帧要编好几秒，不能等排着的帧编完）`)
}
ok(p.errs.length === 0, `档案没有报错 ${p.errs.slice(0, 2).join(' | ')}`)
await p.keyboard.press('Escape')
await sleep(400)

// ---------- 删除 ----------
await p.click(`[data-hl-card="${h.id}"] .rv-link`)
await p.click('[data-hl-del-ok]')
ok(!!(await until(async () => (await items(p)).length === 2)), '删掉一张')

// 手机宽度
await p.setViewportSize({ width: 390, height: 844 })
await p.goto(BASE + '/?page=review&tab=hl')
await p.waitForSelector('[data-hl-card]', { timeout: 15000 })
await sleep(1500)
ok(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), '手机宽度不横向溢出')
await p.screenshot({ path: `${OUT}/hl-album-phone.png` })

await browser.close()
console.log(`\n${pass} 过，${fail} 没过`)
process.exit(fail ? 1 : 0)
