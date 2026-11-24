// 回测（D95）的检查：写信号表 → 运行 → 汇总、权益曲线、交易、信号结果；坏的信号表说清楚是哪一行。
// 会下载币安的公开历史数据（不要密钥），会注册账号，只能对着测试环境跑（不要对着 8080）。
//   node tools/e2e-backtest.mjs <截图目录> <网址>
import { chromium } from 'playwright'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const newPage = async (opts = {}) => {
  const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN', timezoneId: 'Asia/Taipei', ...opts })
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
  try { j = await r.json() } catch { /* 空 */ }
  return { s: r.status, b: j }
}, { method, path, body })
const rand = () => Math.random().toString(36).slice(2)
const DAY = 86_400_000
const iso = (ms) => new Date(ms).toISOString().slice(0, 10)
const d0 = Math.floor(Date.now() / DAY) * DAY - 4 * DAY // 四天前（UTC 零点）
const at = (ms) => new Date(ms).toISOString().slice(0, 16).replace('T', ' ')

const p = await newPage()
await p.goto(BASE + '/?page=login')
await call(p, 'POST', '/api/auth/register', { email: `bt${Date.now()}${rand().slice(0, 4)}@example.com`, password: `bt-${rand()}-pass` })

// ---------- 接口：校验 ----------
ok((await call(p, 'POST', '/api/backtest', { from: d0, to: d0 + 200 * DAY, lines: `${at(d0)},做多 BTC 10%` })).b?.code === 'BT_BAD_RANGE', '超过 180 天被拒')
const badLine = await call(p, 'POST', '/api/backtest', { from: d0, to: d0 + DAY, lines: `${at(d0)},做多 BTC 10%\n昨天下午,平仓 BTC` })
ok(badLine.s === 400 && badLine.b.code === 'BT_LINE_TIME' && badLine.b.params.line === '2', '时间看不懂：说是第 2 行')
ok((await call(p, 'POST', '/api/backtest', { from: d0, to: d0 + DAY, lines: `${at(d0)},做多 NOSUCHCOIN 10%` })).b?.code === 'BT_BAD_INSTRUMENT', '不支持的币种被拒')

// ---------- 界面：跑一次 ----------
await p.goto(BASE + '/?page=review&tab=bt')
const form = p.locator('[data-bt="form"]')
await form.waitFor()
ok(await p.locator('.rv-tools').count() === 0, '回测标签不显示账户和来源选择')
await p.screenshot({ path: `${OUT}/bt-01-empty.png` })
await p.locator('[data-bt="from"]').fill(iso(d0))
await p.locator('[data-bt="to"]').fill(iso(d0 + 2 * DAY))
const lines = [
  `${at(d0 + 2 * 3600_000)},做多 BTC 50%`,
  `${at(d0 + 20 * 3600_000)},平仓 BTC`,
  `${at(d0 + 21 * 3600_000)},做空 ETH 30%`,
  `${at(d0 + 40 * 3600_000)},{"action":"close","symbol":"ETHUSDT"}`,
  `${at(d0 + 41 * 3600_000)},buy BTC 10%`, // 现货信号：合约回测不执行，要说明
].join('\n')
await p.locator('[data-bt="lines"]').fill(lines)
await p.locator('[data-bt="latency"]').fill('120')
const t0 = Date.now()
await p.locator('[data-bt="run"]').click()
const tiles = p.locator('[data-bt="tiles"]')
await tiles.waitFor({ timeout: 120000 }).catch(() => {})
ok(await tiles.count() === 1, `跑完了（${Math.round((Date.now() - t0) / 1000)} 秒）`)
const tileText = (await tiles.innerText().catch(() => '')).replace(/\s+/g, ' ')
ok(/收益/.test(tileText) && /最大回撤/.test(tileText) && /平仓笔数 2/.test(tileText), `汇总：${tileText.slice(0, 90)}`)
const rows = await p.locator('[data-bt="trades"] tbody tr').count()
ok(rows === 2, `两笔交易（${rows}）`)
const tr1 = (await p.locator('[data-bt="trades"] tbody tr').first().innerText()).replace(/\s+/g, ' ')
ok(/BTC/.test(tr1) && /做多|多/.test(tr1), `第一笔是 BTC 多单（${tr1.slice(0, 70)}）`)
ok(tr1.includes(at(d0 + 2 * 3600_000).slice(5)), '开仓时间和信号表一样按 UTC 写')
const canvas = await p.locator('.bt-chart canvas').count()
ok(canvas >= 1, '有权益曲线')
await p.mouse.move(0, 0)
await p.waitForTimeout(400)
await p.screenshot({ path: `${OUT}/bt-02-result.png` })
await p.locator('.bt-tabs button', { hasText: '信号' }).click()
const out = await p.locator('[data-bt="outcomes"] tbody tr').allInnerTexts()
ok(out.length === 5 && out.filter((x) => /已执行/.test(x)).length === 4 && /现货信号/.test(out[4]), `信号结果：4 条执行，现货那条说明原因（${out.length}）`)
ok(out.length > 0 && /02:00:00\.120/.test(out[0]), `到交易所的时刻按延迟算到毫秒（${(out[0] ?? '').replace(/\s+/g, ' ')}）`)
await p.screenshot({ path: `${OUT}/bt-03-signals.png` })
ok((await p.locator('.bt-notes li').count()) >= 3, '写明和真实交易不一样的地方')

// ---------- 坏信号表：界面说是哪一行 ----------
await p.locator('[data-bt="lines"]').fill(`${at(d0)},做多 BTC 10%\n这一行没有逗号`)
await p.locator('[data-bt="run"]').click()
const err = await p.locator('.form-err.on').innerText({ timeout: 10000 }).catch(() => '')
ok(/第 2 行/.test(err), `坏的信号表说是哪一行（${err}）`)

// ---------- 整页不滚动；窄屏 ----------
const scroll = await p.evaluate(() => document.scrollingElement.scrollHeight - innerHeight)
ok(scroll <= 1, `整页不滚动（多 ${scroll}px）`)
const m = await newPage({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true })
await m.context().addCookies(await p.context().cookies())
await m.goto(BASE + '/?page=review&tab=bt')
await m.locator('[data-bt="form"]').waitFor()
const over = await m.evaluate(() => document.documentElement.scrollWidth - innerWidth)
ok(over <= 1, `手机宽度不横向滚动（多 ${over}px）`)
const stack = await m.evaluate(() => { const f = document.querySelector('[data-bt="form"]').getBoundingClientRect(); const o = document.querySelector('[data-bt="out"]').getBoundingClientRect(); return o.top - f.bottom })
ok(stack >= -1, `手机宽度：结果在表单下面，不盖住表单（间隔 ${Math.round(stack)}px）`)
const btn = await m.locator('[data-bt="run"]').boundingBox()
const lns = await m.locator('[data-bt="lines"]').boundingBox()
ok(!!btn && !!lns && btn.y > lns.y + lns.height, '运行按钮在信号表下面')
await m.screenshot({ path: `${OUT}/bt-04-phone.png` })
await m.locator('[data-bt="run"]').scrollIntoViewIfNeeded()
await m.screenshot({ path: `${OUT}/bt-05-phone-run.png` })

ok(p.errs.length === 0 && m.errs.length === 0, `页面没有报错（${[...p.errs, ...m.errs].slice(0, 2).join(' | ')}）`)
console.log(`\n${pass} 通过，${fail} 失败`)
await browser.close()
process.exit(fail ? 1 : 0)
