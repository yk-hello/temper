// 新闻标题（18 第 8.6 条）：在本机 2526 端口起假的 RSS、Atom 和 X 接口（tools/dev/restart.ps1 让测试后端去那里取），
// 让后端取一次，查接口和市场页"今日"里的新闻：只有标题、来源、时间、链接；坏链接不进页面；按币筛；手机宽度不横向滚动。
// 不注册账号、不下单，但要用测试环境的开发接口（/api/dev/news-poll 只在模拟行情下有）。
// 用法：node tools/e2e-news.mjs <截图目录> [http://127.0.0.1:4173]
import { chromium } from 'playwright'
import { createServer } from 'node:http'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const now = Date.now()
const rfc = (ms) => new Date(ms).toUTCString().replace('GMT', '+0000')
const iso = (ms) => new Date(ms).toISOString()

const rss = `<?xml version="1.0"?><rss version="2.0"><channel><title>Test Crypto Wire</title>
<item><title>Bitcoin ETF inflows hit a monthly high</title><link>https://news.example.com/btc-etf</link><pubDate>${rfc(now - 20 * 60_000)}</pubDate></item>
<item><title><![CDATA[Ethereum devs schedule <script>alert(1)</script> upgrade call]]></title><link>https://news.example.com/eth-call</link><pubDate>${rfc(now - 50 * 60_000)}</pubDate></item>
<item><title>Phishing link should not render</title><link>javascript:alert(1)</link><pubDate>${rfc(now - 60 * 60_000)}</pubDate></item>
<item><title>A very long headline about market structure, liquidity, funding rates, open interest and everything else that might matter to anyone reading it on a phone</title><link>https://news.example.com/long</link><pubDate>${rfc(now - 70 * 60_000)}</pubDate></item>
</channel></rss>`
const atom = `<?xml version="1.0" encoding="utf-8"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Test Exchange Notices</title>
<entry><title>Will list SOL margin pairs</title><link rel="alternate" href="https://notices.example.org/1"/><id>1</id><updated>${iso(now - 30 * 60_000)}</updated></entry></feed>`
let xAuth = ''
const srv = createServer((req, res) => {
  const u = new URL(req.url, 'http://x')
  if (u.pathname === '/rss.xml') { res.writeHead(200, { 'Content-Type': 'application/rss+xml' }); return res.end(rss) }
  if (u.pathname === '/atom.xml') { res.writeHead(200, { 'Content-Type': 'application/atom+xml' }); return res.end(atom) }
  if (u.pathname === '/v5/announcements/index') { // Bybit 公告：按分类
    res.writeHead(200, { 'Content-Type': 'application/json' })
    const typ = u.searchParams.get('type')
    const list = typ === 'new_crypto' ? [{ title: 'New listing: XYZUSDT Perpetual Contract', url: 'https://announcements.bybit.com/a/1', dateTimestamp: now - 3 * 3600_000, publishTime: now - 3 * 3600_000 }]
      : typ === 'delistings' ? [{ title: 'Delisting of ETHUSDT Perpetual Contract', url: 'https://announcements.bybit.com/a/2', dateTimestamp: now - 4 * 3600_000, publishTime: now - 4 * 3600_000 }] : []
    return res.end(JSON.stringify({ retCode: 0, result: { list } }))
  }
  if (u.pathname === '/api/v5/support/announcements') { // OKX 公告：活动推广要被筛掉
    res.writeHead(200, { 'Content-Type': 'application/json' })
    return res.end(JSON.stringify({ code: '0', data: [{ details: [
      { annType: 'announcements-new-listings', title: 'OKX will launch ABC/USDT for spot trading', url: 'https://www.okx.com/help/abc', pTime: String(now - 5 * 3600_000) },
      { annType: 'latest-events', title: 'Win 100,000 USDT in our trading campaign', url: 'https://www.okx.com/help/promo', pTime: String(now - 5 * 3600_000) },
    ] }] }))
  }
  if (u.pathname.startsWith('/2/')) {
    xAuth = req.headers.authorization || ''
    res.writeHead(200, { 'Content-Type': 'application/json' })
    if (u.pathname === '/2/users/by') return res.end(JSON.stringify({ data: [{ id: '77', username: 'exchange_notice' }] }))
    if (u.pathname === '/2/users/77/tweets') return res.end(JSON.stringify({ data: [{ id: '501', text: 'Scheduled maintenance for BTC withdrawals at 02:00 UTC', created_at: iso(now - 10 * 60_000) }] }))
  }
  res.writeHead(404); res.end()
})
await new Promise((r, j) => { srv.once('error', j); srv.listen(2526, '127.0.0.1', r) })

const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN' })
await c.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1') })
const p = await c.newPage()
const errs = []
p.on('pageerror', (e) => errs.push(String(e)))
await p.goto(BASE + '/?page=market&tab=today')
const call = (method, path) => p.evaluate(async ({ method, path }) => {
  const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web' } })
  return { s: r.status, b: await r.json().catch(() => null) }
}, { method, path })

const poll = await call('POST', '/api/dev/news-poll')
ok(poll.s === 204, `让后端取一次（${poll.s}）`)
const r = await call('GET', '/api/news?limit=50')
const items = r.b?.items ?? []
ok(r.s === 200 && items.length === 8, `接口：8 条（RSS 3、Atom 1、X 1、交易所公告 3：${items.length}）`)
ok(items.filter((i) => i.kind === 'notice').length === 3 && !items.some((i) => /campaign/i.test(i.title)), '交易所公告 3 条，活动推广不要')
ok(items[0]?.kind === 'x' && items[0]?.url === 'https://x.com/exchange_notice/status/501', 'X 那条最新，链接指向 x.com')
ok(xAuth === 'Bearer test-x-e2e', 'X 用部署的人自己的 token')
ok(!items.some((i) => /javascript/i.test(i.url) || /Phishing/.test(i.title)), '坏链接那条不收')
ok(items.every((i) => !/<|>/.test(i.title)), '标题去掉了标签')
ok(items.every((i) => Object.keys(i).every((k) => ['id', 'source', 'title', 'url', 'time', 'coins', 'kind'].includes(k))), '只有标题、来源、时间、链接（没有正文）')
ok(!JSON.stringify(r.b).includes('test-x-e2e'), '接口不带出 token')
ok(r.b?.feeds === 2 && r.b?.x === 1 && r.b?.notices === true, '来源数')
const btc = await call('GET', '/api/news?coin=BTC')
ok((btc.b?.items ?? []).length === 2, `按币：BTC 2 条（${(btc.b?.items ?? []).length}）`)

// ---------- 页面 ----------
await p.reload()
const card = p.locator('[data-td="news"]')
await card.waitFor()
const rows = card.locator('.td-newsl li')
let n = 0
for (let i = 0; i < 40; i++) { n = await card.locator('.td-newsl a').count(); if (n >= 8) break; await sleep(250) }
ok(n === 8, `今日里有 8 条（${n}）`)
const a0 = card.locator('.td-newsl a').first()
ok((await a0.getAttribute('target')) === '_blank' && /noopener/.test(await a0.getAttribute('rel') || ''), '新窗口打开，不带来源页')
ok(/Test Crypto Wire|X @exchange_notice/.test(await card.innerText()), '写了来源')
ok(!(await card.innerText()).includes('Phishing'), '页面上也没有坏链接那条')
const chips = card.locator('.td-news-f .seg button')
ok((await chips.count()) >= 3, `按币筛（${(await chips.allInnerTexts()).join(' ')}）`)
await chips.filter({ hasText: '交易所公告' }).click()
await sleep(200)
ok((await card.locator('.td-newsl a').count()) === 3 && (await card.locator('.td-news-tag').count()) === 3, '选"交易所公告"：3 条，都标了"公告"')
await card.screenshot({ path: `${OUT}/news-04-notices.png` })
await chips.filter({ hasText: 'ETH' }).click()
await sleep(200)
ok((await card.locator('.td-newsl a').count()) === 1, '选 ETH 只剩 1 条')
await chips.first().click()
await sleep(200)
const st = await card.locator('.td-newsl a').last().evaluate((a) => { const s = getComputedStyle(a); return `${s.whiteSpace} ${s.textOverflow} ${s.overflow}` })
ok(st === 'nowrap ellipsis hidden', `标题一行，放不下时省略、不撑开（${st}）`)
const cardW = await card.evaluate((el) => el.scrollWidth - el.clientWidth)
ok(cardW <= 1, `卡片没有被长标题撑宽（多 ${cardW}px）`)
await card.scrollIntoViewIfNeeded()
await card.screenshot({ path: `${OUT}/news-01-card.png` })
await p.screenshot({ path: `${OUT}/news-02-today.png`, fullPage: false })

// 英文
await p.evaluate(() => localStorage.setItem('qs.lang', 'en'))
await p.reload()
await card.waitFor()
ok(/News/.test(await card.locator('h3').innerText()), '英文标题')
await p.evaluate(() => localStorage.removeItem('qs.lang'))

// 手机
const m = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true, locale: 'zh-CN' })
await m.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1') })
const mp = await m.newPage()
mp.on('pageerror', (e) => errs.push(String(e)))
await mp.goto(BASE + '/?page=market&tab=today')
const mc = mp.locator('[data-td="news"]')
await mc.waitFor()
for (let i = 0; i < 40 && (await mc.locator('.td-newsl a').count()) < 8; i++) await sleep(250)
await mc.scrollIntoViewIfNeeded()
const mover = await mp.evaluate(() => document.documentElement.scrollWidth - innerWidth)
ok(mover <= 1, `手机宽度不横向滚动（多 ${mover}px）`)
const lines = await mc.locator('.td-newsl a').first().evaluate((a) => Math.round(a.getBoundingClientRect().height / parseFloat(getComputedStyle(a).lineHeight || '18')))
ok(lines >= 1 && lines <= 2, `手机上标题最多两行（${lines}）`)
await mc.screenshot({ path: `${OUT}/news-03-phone.png` })

ok(errs.length === 0, `页面没有报错（${errs.slice(0, 2).join(' | ')}）`)
console.log(`\n${pass} 通过，${fail} 失败`)
await browser.close()
srv.close()
process.exit(fail ? 1 : 0)
