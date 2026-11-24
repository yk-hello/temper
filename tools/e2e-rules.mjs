// 条件提醒（18 第 10.4 条）：账户页加一条"价格低于 X"的规则，用模拟价格让它从不满足变成满足，铃铛里出现一条、只出现一次；
// 不对的规则（价格填负数）说清楚；暂停、恢复、删除；英文；手机宽度不横向滚动。
// 会注册账号、改模拟价格：只能对着测试环境跑（/api/dev/sim-price 只在模拟行情下有）。
// 用法：node tools/e2e-rules.mjs <截图目录> [http://127.0.0.1:4173]
import { chromium } from 'playwright'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const rand = () => Math.random().toString(36).slice(2)
const until = async (f, ms = 30000) => { const t0 = Date.now(); while (Date.now() - t0 < ms) { try { const v = await f(); if (v) return v } catch { /* 再试 */ } await sleep(300) } return null }

const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN' })
await c.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1') })
const p = await c.newPage()
const errs = []
p.on('pageerror', (e) => errs.push(String(e)))
const call = (method, path, body) => p.evaluate(async ({ method, path, body }) => {
  const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
  return { s: r.status, b: await r.json().catch(() => null) }
}, { method, path, body })
await p.goto(BASE + '/?page=login')
await call('POST', '/api/auth/register', { email: `rl${Date.now()}${rand().slice(0, 4)}@example.com`, password: `rl-${rand()}-${rand()}` })
const lastPx = async () => Number((await call('GET', '/api/market/snapshot')).b.instruments.find((i) => i.instrument === 'ETH-PERP').last)

// 接口：不对的规则
let r = await call('POST', '/api/notify/rules', { instrument: 'ETH-PERP', conds: [{ field: 'px', op: '>', value: -5 }] })
ok(r.s === 422 && r.b.code === 'RULE_BAD_COND', '价格填负数被拒')
r = await call('POST', '/api/notify/rules', { instrument: 'ETH-PERP', conds: [{ field: 'eval(1)', op: '>', value: 1 }] })
ok(r.s === 422, '不认识的字段被拒（规则只是数据）')

// 界面：加一条"ETH 价格低于 现价 × 0.97"
await p.goto(BASE + '/?page=account')
const form = p.locator('[data-rule-form]')
await form.waitFor()
await form.scrollIntoViewIfNeeded()
ok(/只提醒，不下单/.test(await p.locator('.ac-sec', { has: form }).innerText()), '写明只提醒，不下单')
await form.locator('.ac-sel .sel-btn').click()
await p.locator('.sel-pop [role="option"]', { hasText: /^ETH$/ }).click()
await form.locator('[data-rule-name]').fill('ETH 回调')
await form.locator('.rl-field .sel-btn').first().click()
await p.locator('.sel-pop [role="option"]', { hasText: '价格' }).click()
await form.locator('[data-rule-cond="0"] .seg button', { hasText: '低于' }).click()
const px0 = await lastPx()
const line = Math.round(px0 * 0.97 * 100) / 100
await form.locator('[data-rule-value]').first().fill(String(line))
await form.locator('[data-rule-more]').click()
ok((await form.locator('[data-rule-cond]').count()) === 2, '可以再加一个条件')
await form.locator('[data-rule-cond="1"] button[aria-label="删掉这个条件"]').click()
ok((await form.locator('[data-rule-cond]').count()) === 1, '也可以删掉')
await form.screenshot({ path: `${OUT}/rl-01-form.png` })
await form.locator('[data-rule-add]').click()
const row = p.locator('[data-rules] tr').first()
ok(!!(await until(async () => /ETH 回调/.test(await row.innerText()))), '列表里有这条')
ok(/价格 < /.test(await row.innerText()) && /还没响过/.test(await row.innerText()), `写成一句话、还没响过（${(await row.innerText()).replace(/\s+/g, ' ')}）`)

// 让价格跌到线下面：从不满足变成满足，响一次
const bell = async () => (await call('GET', '/api/notify')).b.items.filter((n) => n.kind === 'rule_alert')
await call('POST', '/api/dev/sim-price', { instrument: 'ETH-PERP', price: Math.round(px0 * 0.95 * 100) / 100 })
const got = await until(async () => { const l = await bell(); return l.length ? l : null }, 40000)
ok(!!got && got.length === 1, `价格跌破线：铃铛里一条（${got?.length ?? 0}）`)
ok(/ETH 回调/.test(JSON.stringify(got?.[0]?.params ?? {})), '通知里带着规则名')
await sleep(12000)
ok((await bell()).length === 1, '一直在线下面：不再响')
await p.reload()
await p.locator('[data-rules]').scrollIntoViewIfNeeded()
ok(/上次/.test(await p.locator('[data-rules] tr').first().innerText()), '列表写着上次什么时候响的')
// 铃铛里的那句话
await p.locator('[data-bell]').click()
await p.locator('[data-bell-pop]').waitFor()
await sleep(400)
const bellText = await p.locator('[data-bell-pop]').innerText().catch(() => '')
ok(/ETH 回调 · ETH：价格 .*（低于 /.test(bellText), `铃铛里写清楚（${bellText.split('\n').find((l) => /ETH 回调/.test(l)) ?? ''}）`)
await p.screenshot({ path: `${OUT}/rl-02-bell.png` })
await p.keyboard.press('Escape')

// 暂停、恢复、删除
await p.locator('[data-rules] [data-rule-toggle]').first().click()
ok(!!(await until(async () => /已暂停/.test(await p.locator('[data-rules] tr').first().innerText()))), '暂停')
await p.locator('[data-rules] [data-rule-toggle]').first().click()
ok(!!(await until(async () => !/已暂停/.test(await p.locator('[data-rules] tr').first().innerText()))), '恢复')
await p.locator('.ac-sec', { has: p.locator('[data-rule-form]') }).screenshot({ path: `${OUT}/rl-03-section.png` })
await p.locator('[data-rules] [data-rule-del]').first().click()
ok(!!(await until(async () => (await p.locator('[data-rules]').count()) === 0)), '删除')
await call('POST', '/api/dev/sim-price', { instrument: 'ETH-PERP', price: px0 })

// 手机
const m = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true, locale: 'zh-CN', storageState: await c.storageState() })
const mp = await m.newPage()
mp.on('pageerror', (e) => errs.push(String(e)))
await mp.goto(BASE + '/?page=account')
await mp.locator('[data-rule-form]').scrollIntoViewIfNeeded()
await sleep(500)
const over = await mp.evaluate(() => document.documentElement.scrollWidth - innerWidth)
ok(over <= 1, `手机不横向滚动（${over}）`)
await mp.locator('[data-rule-form]').screenshot({ path: `${OUT}/rl-04-phone.png` })

ok(errs.length === 0, `页面没有报错（${errs.slice(0, 2).join(' | ')}）`)
console.log(`\n${pass} 通过，${fail} 失败`)
await browser.close()
process.exit(fail ? 1 : 0)
