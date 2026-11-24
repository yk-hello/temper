// AI 点评的真实链路：真的下单、平仓，在复盘页点按钮，真的调用模型。要花 token，不在回归里跑（回归里跑 e2e-aifake.mjs：同一套流程，模型换成本机的假 Claude）。
// 用法：node e2e-ai-real.mjs <截图目录> <前端地址>。会注册账号、下单，不要对着 8080 跑。
// 后端要配 AI_API_KEY、AI_BASE_URL、AI_MODEL，并且用模拟行情（要手动改价造止损）。
import { createRequire } from 'module'
import { randomBytes } from 'crypto'
import fs from 'fs'
const require = createRequire(import.meta.url)
const { chromium } = require('playwright-core')
const OUT = process.argv[2] ?? 'shots/ai-real'
const BASE = process.argv[3] ?? 'http://127.0.0.1:4173'
if (/:8080/.test(BASE)) throw new Error('不要对着 8080 跑')
fs.mkdirSync(OUT, { recursive: true })
let bad = 0, good = 0
const ok = (c, s) => { if (c) { good++; console.log('✓', s) } else { bad++; console.log('✗', s) } }
const rnd = () => randomBytes(6).toString('hex')
const noZh = (s) => !/[一-鿿＀-￯　-〿]/.test(s)

const b = await chromium.launch({ channel: process.env.PW_CHANNEL ?? 'msedge', headless: true })
const ctx = await b.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN', timezoneId: 'Asia/Shanghai' })
await ctx.addInitScript(() => { try { localStorage.setItem('qs.wizardSeen', '1'); sessionStorage.setItem('qs.heroSeen', '1') } catch {} })
const p = await ctx.newPage()
const errs = []
p.on('pageerror', (e) => errs.push(String(e).slice(0, 300)))
const call = (method, path, body) => p.evaluate(async ({ method, path, body }) => {
  const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
  let j = null
  try { j = await r.json() } catch { /* 没有内容 */ }
  return { s: r.status, b: j }
}, { method, path, body })
const until = async (fn, ms = 15000) => { const t0 = Date.now(); while (Date.now() - t0 < ms) { const v = await fn(); if (v) return v; await new Promise((r) => setTimeout(r, 250)) } return null }
const simPrice = (px, inst = 'BTC-PERP') => call('POST', '/api/dev/sim-price', { instrument: inst, price: Number(Number(px).toFixed(1)) })

await p.goto(`${BASE}/?page=login`)
const email = `real${Date.now()}${rnd().slice(0, 4)}@example.com`
await call('POST', '/api/auth/register', { email, password: 'pw-' + rnd() + rnd() })
const tp = 'tp-' + rnd()
await call('POST', '/api/me/trade-password', { new: tp })
await call('POST', '/api/discipline/enabled', { on: false, trade_password: tp })
const A = (await call('GET', '/api/paper-accounts')).b.accounts[0].id
// e2e-aifake.mjs 调进来时：在系统设置里把 AI 点评接到本机的假 Claude（新库里第一个账号就是管理的人）
if (process.env.QS_E2E_AI_FAKE) {
  // 11-23 起 AI 是厂商列表（D112）：加第一家就直接用它；思考程度设"低"，假 Claude 检查请求里真的带了
  const r = await call('POST', '/api/admin/ai', { name: '假 Claude', format: 'anthropic', base: process.env.QS_E2E_AI_FAKE, key: 'sk-ant-fake-0123456789', model: 'claude-opus-5-5', effort: 'low', trade_password: tp })
  ok(r.s === 200, `系统页加一家 AI 厂商，接到假 Claude（${r.s}）`)
  ok((await call('GET', '/api/admin/ai')).b?.active === r.b?.id, '第一家加进来就用它')
}
const px0 = Number((await until(async () => { const s = (await call('GET', '/api/market/snapshot')).b.instruments.find((x) => x.instrument === 'BTC-PERP'); return s?.bid ? s : null })).bid)
const market = (side, qty) => call('POST', `/api/trade/${A}/orders`, { instrument: 'BTC-PERP', side, type: 'market', qty: String(qty) })
const closedCount = async () => (await call('GET', `/api/journal/a/${A}/trades`)).b.trades.filter((x) => x.close_time).length

// 第一笔：没设止损，手动平，赚一点
ok((await market('buy', 0.05)).s === 200, '开多 0.05 BTC，不设止损')
await p.waitForTimeout(1500)
await simPrice(px0 * 1.006)
await p.waitForTimeout(1500)
ok((await call('POST', `/api/trade/${A}/close`, { instrument: 'BTC-PERP' })).s === 200, '手动平仓')
ok(!!(await until(async () => (await closedCount()) === 1)), '第一笔平掉了')

// 第二笔：设了止损，被打掉
await simPrice(px0)
await p.waitForTimeout(800)
ok((await market('buy', 0.05)).s === 200, '再开多')
ok((await call('POST', `/api/trade/${A}/tpsl`, { instrument: 'BTC-PERP', sl: String(Math.round(px0 * 0.992)) })).s === 200, '设止损，离开仓价 0.8%')
await p.waitForTimeout(1200)
await simPrice(px0 * 0.99)
ok(!!(await until(async () => (await closedCount()) === 2)), '第二笔止损出局')
await simPrice(px0)

// 给第二笔写一条笔记：不能发出去（发出去的内容由单元测试和 e2e-notify 检查，这里看回复里有没有带出来）
const trades = (await call('GET', `/api/journal/a/${A}/trades`)).b.trades.filter((x) => x.close_time)
const secret = 'SECRETNOTE' + rnd()
await call('PUT', `/api/journal/a/${A}/trades/${trades[0].id}/note`, { tags: [], note: secret, emotion: '', playbook: 0 })

const generate = async (label, shot) => {
  const t0 = Date.now()
  await p.locator('[data-ai-go]').click()
  const busy = await p.locator('[data-ai-go]').innerText().catch(() => '')
  await p.waitForSelector('[data-ai-text]', { timeout: 62000 }).catch(() => {})
  const secs = ((Date.now() - t0) / 1000).toFixed(1)
  const has = await p.locator('[data-ai-text]').count()
  if (!has) {
    const toast = await p.locator('.toast.show').innerText().catch(() => '')
    ok(false, `${label}：没出来（${secs} 秒，提示：${toast}）`)
    return ''
  }
  const text = await p.locator('[data-ai-text]').innerText()
  ok(true, `${label}：${secs} 秒，按钮显示“${busy}”`)
  console.log(text.split('\n').map((l) => '    ' + l).join('\n'))
  await p.locator('[data-ai]').scrollIntoViewIfNeeded()
  if (shot) await p.locator('.rv-detail').screenshot({ path: `${OUT}/${shot}.png` })
  return text
}

await p.goto(`${BASE}/?page=review`)
await p.waitForSelector('.rv-table', { timeout: 15000 })
const rows = p.locator('.rv-table tbody tr')
ok(await rows.count() === 2, '复盘页有 2 笔')

const zhLabels = /做对了/.test.bind(/做对了/)
for (let i = 0; i < 2; i++) {
  await rows.nth(i).click()
  await p.waitForSelector('[data-ai-go]', { timeout: 8000 })
  const txt = await generate(`中文第 ${i + 1} 笔`, `中文-${i + 1}`)
  ok(zhLabels(txt) && /先改这个/.test(txt) && /下次的规则/.test(txt), `中文第 ${i + 1} 笔：三个标题都在`)
  ok(!txt.includes('SECRETNOTE'), `中文第 ${i + 1} 笔：回复里没有笔记内容`)
  if (i === 0) {
    const left = await p.locator('[data-ai] p').innerText()
    ok(/不构成投资建议/.test(left), '写着不构成投资建议')
  }
}

// 刷新后直接读缓存，不再调用
await p.reload()
await p.waitForSelector('.rv-table')
await rows.nth(0).click()
const t0 = Date.now()
await p.waitForSelector('[data-ai-text]', { timeout: 8000 })
ok(Date.now() - t0 < 3000, `刷新后直接显示存好的点评（${Date.now() - t0} 毫秒）`)
ok(await p.locator('[data-ai-go]').count() === 0, '已生成的不再显示按钮')

// 英文
await p.locator('.bar-lang button, .bar-lang [role=radio]').filter({ hasText: 'EN' }).first().click()
await p.waitForSelector('[data-ai-go]', { timeout: 8000 })
const en = await generate('英文', '英文-1')
ok(/Did well/.test(en) && /Fix first/.test(en) && /Rule for next time/.test(en), '英文：三个标题都在')
ok(noZh(await p.locator('[data-ai]').innerText()), '英文：整块没有中文')

// 手机
await p.setViewportSize({ width: 390, height: 844 })
await p.waitForTimeout(500)
const over = await p.evaluate(() => {
  const box = document.querySelector('[data-ai]')?.getBoundingClientRect()
  return { docOver: document.documentElement.scrollWidth - document.documentElement.clientWidth, right: box?.right ?? 0, vw: innerWidth }
})
ok(over.right <= over.vw + 1, `手机上点评不超出屏幕（右边 ${Math.round(over.right)}，屏幕 ${over.vw}）`)
await p.locator('[data-ai]').scrollIntoViewIfNeeded()
await p.screenshot({ path: `${OUT}/手机-英文.png` })

ok(errs.length === 0, `页面没有报错 ${errs.join(' | ').slice(0, 300)}`)
console.log(`\n通过 ${good}，失败 ${bad}`)
await b.close()
process.exit(bad ? 1 : 0)
